package planner

import (
	"context"
	"path"
	"strings"

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
	tags, ok := p.prober.ProbeBucketed(ctx, mediaprobe.Request{
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
	})
	if !ok || len(tags) == 0 {
		return parsed
	}
	return rules.MergeProbedTags(parsed, tags, p.probeMode)
}
