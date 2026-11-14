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

	// fill 模式下文件名已写全标签就没什么可补的，直接省掉这次文件头读取。
	// prefer/force 的目的就是拿真实值纠正文件名，不能跳过。
	if p.probeMode == "fill" && p.mediaTagsComplete(parsed) {
		if p.firstProbeLog(bucket) {
			p.probeLog("[探测] 跳过 —— 文件名已含全部标签: %s", entry.item.Name)
		}
		return parsed
	}

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
	return rules.MergeProbedTags(parsed, tags, p.probeMode)
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
		return false
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
