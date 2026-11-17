package planner

import (
	"fmt"
	"sort"
	"strings"

	"litepan/internal/mediaorganize/rules"
)

// applyMixedQualityGuard 规划前检查：同一部剧某一季里文件名声明的画质不一致时，
// 整部剧不做任何操作（与「扩展名混用」同等处理）。
func (p *Planner) applyMixedQualityGuard(groups map[groupKey][]batchEntry) {
	if !p.skipMixedQuality || len(groups) == 0 {
		return
	}
	for title, detail := range p.mixedQualityShows(groups) {
		victims := make([]groupKey, 0, 2)
		for key := range groups {
			if key.mediaKind == "tv" && key.title == title {
				victims = append(victims, key)
			}
		}
		if len(victims) == 0 {
			continue
		}
		label := strings.TrimSpace(title)
		if label == "" {
			label = "未识别标题"
		}
		reason := fmt.Sprintf("%s：%s，已跳过整部剧", label, detail)
		p.logLine("[计划] 跳过整部剧：" + reason)
		for _, key := range victims {
			for _, entry := range groups[key] {
				p.skip(entry.item, reason)
			}
			delete(groups, key)
		}
	}
}

// mixedQualityShows 返回「某季画质声明不一致」的剧 → 说明文字（不含剧名）。
// 只统计文件名里写了画质的文件；整季都没写画质的，视为无法判断，不拦截。
func (p *Planner) mixedQualityShows(groups map[groupKey][]batchEntry) map[string]string {
	perShow := map[string]map[int]map[string]int{}

	for key, items := range groups {
		if key.mediaKind != "tv" {
			continue
		}
		for _, entry := range items {
			if entry.specialLabel != "" {
				continue
			}
			sig := qualitySignature(entry.fileParsed)
			if sig == "" {
				continue
			}
			season := -1
			if entry.fileParsed.Season != nil {
				season = *entry.fileParsed.Season
			}
			if perShow[key.title] == nil {
				perShow[key.title] = map[int]map[string]int{}
			}
			if perShow[key.title][season] == nil {
				perShow[key.title][season] = map[string]int{}
			}
			perShow[key.title][season][sig]++
		}
	}

	out := make(map[string]string, len(perShow))
	for title, seasons := range perShow {
		bad := make([]int, 0, len(seasons))
		for season, counts := range seasons {
			if len(counts) >= 2 {
				bad = append(bad, season)
			}
		}
		if len(bad) == 0 {
			continue
		}
		sort.Ints(bad)
		parts := make([]string, 0, len(bad))
		for i, season := range bad {
			if i >= 2 {
				parts = append(parts, fmt.Sprintf("等 %d 季存在多种画质", len(bad)))
				break
			}
			parts = append(parts, fmt.Sprintf("%s存在多种画质（%s）", seasonLabel(season), formatQualityCounts(seasons[season])))
		}
		out[title] = strings.Join(parts, "；")
	}
	return out
}

// qualitySignature 取文件名里声明的画质标识（分辨率 + 编码），与探测分桶的 hint 口径一致。
func qualitySignature(parsed rules.ParsedMedia) string {
	parts := make([]string, 0, 2)
	if s := strings.TrimSpace(parsed.ScreenSize); s != "" {
		parts = append(parts, strings.ToLower(s))
	}
	if v := strings.TrimSpace(parsed.VideoCodec); v != "" {
		parts = append(parts, strings.ToLower(v))
	}
	return strings.Join(parts, "/")
}

// formatQualityCounts 输出形如 "2160p/h.265×11 / 1080p/h.264×1"，按数量降序、同数量按画质升序。
func formatQualityCounts(counts map[string]int) string {
	type item struct {
		sig   string
		count int
	}
	items := make([]item, 0, len(counts))
	for sig, n := range counts {
		items = append(items, item{sig: sig, count: n})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].sig < items[j].sig
	})
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s×%d", it.sig, it.count))
	}
	return strings.Join(parts, " / ")
}
