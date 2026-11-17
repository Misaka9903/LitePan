package planner

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"litepan/internal/mediaorganize/rules"
	"litepan/internal/mediaprobe"
)

// applyMediaProbe 用真实文件头的探测结果补全媒体标签。
// 同一个桶（同季同扩展名、同片同扩展名）只探测一次，其余文件复用结果；
// 文件名已声明的画质会参与分桶，因此混装目录不会被统一成一种画质。
func (p *Planner) applyMediaProbe(parsed rules.ParsedMedia, key groupKey, entry batchEntry, season *int) rules.ParsedMedia {
	if p.prober == nil || p.probeMode == "off" || entry.item.IsDir {
		return parsed
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	seasonNo := 0
	if season != nil {
		seasonNo = *season
	}
	req := mediaprobe.Request{
		AccountID:  p.accountID,
		FileID:     entry.item.ID,
		FileName:   entry.item.Name,
		Size:       entry.item.Size,
		MediaKind:  key.mediaKind,
		DirID:      entry.sourceDirID,
		Title:      key.title,
		Year:       key.year,
		Season:     seasonNo,
		Ext:        strings.ToLower(path.Ext(entry.item.Name)),
		Special:    entry.specialLabel,
		HintScreen: parsed.ScreenSize,
		HintVideo:  parsed.VideoCodec,
	}
	bucket := mediaprobe.BucketKey(req)

	started := time.Now()
	tags, ok := p.prober.ProbeBucketed(ctx, req)
	elapsed := time.Since(started)

	if !ok || len(tags) == 0 {
		// 失败要留痕：fill 模式会静默回退文件名，用户否则完全无感。
		if p.firstProbeLog(bucket) {
			p.probeLog("[探测] 未取到媒体信息，回退文件名: %s", entry.item.Name)
		}
		return parsed
	}
	if p.firstProbeLog(bucket) {
		p.probeLog("[探测] %s → %s（%.1fs）", entry.item.Name, joinProbedTags(tags, p.mediaTagOrder), elapsed.Seconds())
	}
	return rules.MergeProbedTags(parsed, tags)
}

// firstProbeLog 让同一个桶在一次计划里只记一条日志，避免整季刷屏。
func (p *Planner) firstProbeLog(bucket string) bool {
	if p.probeSeen == nil {
		p.probeSeen = make(map[string]struct{})
	}
	if _, ok := p.probeSeen[bucket]; ok {
		return false
	}
	p.probeSeen[bucket] = struct{}{}
	return true
}

func (p *Planner) probeLog(format string, args ...any) {
	if p.log == nil {
		return
	}
	p.log(fmt.Sprintf(format, args...))
}

// joinProbedTags 按用户配置的标签顺序拼出可读串，顺序与最终文件名一致。
func joinProbedTags(tags map[string]string, order []string) string {
	parts := make([]string, 0, len(order))
	for _, key := range order {
		if value := strings.TrimSpace(tags[key]); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return "(空)"
	}
	return strings.Join(parts, " ")
}

// mediaTagsComplete 判断文件名解析结果是否已覆盖 media_tag_order 里的全部标签。
// 用户把某个标签从顺序里删掉，就等于不要求它，这里也随之放宽。
func (p *Planner) mediaTagsComplete(parsed rules.ParsedMedia) bool {
	if len(p.mediaTagOrder) == 0 {
		// 没有配置标签顺序就等于没有要求，不应因此把所有文件都判成「不完整」。
		return true
	}
	tags := parsed.ToMap()
	for _, key := range p.mediaTagOrder {
		value, ok := tags[key]
		if !ok || value == nil {
			return false
		}
		if strings.TrimSpace(fmt.Sprint(value)) == "" {
			return false
		}
	}
	return true
}

// probeRetryLimit 每个桶最多尝试的文件数：首次 + 换一个文件重试一次。
const probeRetryLimit = 2

// precheckProbeGroup 在规划前按「季 + 扩展名」各探一次，失败时换同季另一个文件重试一次。
// 任一桶两次都失败则返回 false，调用方应整组跳过，不做任何重命名或移动。
func (p *Planner) precheckProbeGroup(key groupKey, items []batchEntry) bool {
	if p.prober == nil || p.probeMode == "off" {
		return true
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	type bucket struct {
		season int
		ext    string
		fileID string
	}
	order := make([]bucket, 0, 2)
	byBucket := make(map[bucket][]batchEntry, 2)
	for _, entry := range items {
		if entry.item.IsDir || mediaprobe.IsSampleName(entry.item.Name) {
			continue
		}
		bk := bucket{ext: strings.ToLower(path.Ext(entry.item.Name))}
		if entry.fileParsed.Season != nil {
			bk.season = *entry.fileParsed.Season
		}
		// 电影没有「同季同规格」的前提：每个文件各探一次，失败即整部不动，
		// 不做跨文件重试（多版本之间规格本来就不同）。
		if strings.EqualFold(strings.TrimSpace(key.mediaKind), "movie") {
			bk.fileID = entry.item.ID
		}
		if _, ok := byBucket[bk]; !ok {
			order = append(order, bk)
		}
		byBucket[bk] = append(byBucket[bk], entry)
	}

	for _, bk := range order {
		candidates := byBucket[bk]
		ok := false
		for i, cand := range candidates {
			if i >= probeRetryLimit {
				break
			}
			req := mediaprobe.Request{
				AccountID:  p.accountID,
				FileID:     cand.item.ID,
				FileName:   cand.item.Name,
				Size:       cand.item.Size,
				MediaKind:  key.mediaKind,
				DirID:      cand.sourceDirID,
				Title:      key.title,
				Year:       key.year,
				Season:     bk.season,
				Ext:        bk.ext,
				Special:    cand.specialLabel,
				HintScreen: cand.fileParsed.ScreenSize,
				HintVideo:  cand.fileParsed.VideoCodec,
			}
			tags, good := p.prober.ProbeBucketed(ctx, req)
			if good && len(tags) > 0 {
				ok = true
				break
			}
			p.probeLog("[探测] 第 %d 次失败，换文件重试: %s", i+1, cand.item.Name)
		}
		if !ok {
			p.probeLog("[探测] 第 %d 季两次探测均失败，整部剧保持原样: %s", bk.season, key.title)
			return false
		}
	}
	return true
}
