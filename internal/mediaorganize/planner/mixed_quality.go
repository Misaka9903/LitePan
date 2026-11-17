package planner

import (
	"fmt"
	"sort"
	"strings"
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
// 分辨率与编码分别判定：任一项出现两种及以上取值才算混装。
// 只统计文件名里写了的项；某项整季都没写，视为无法判断，不拦截。
func (p *Planner) mixedQualityShows(groups map[groupKey][]batchEntry) map[string]string {
	type seasonQuality struct {
		screens map[string]int
		codecs  map[string]int
	}
	perShow := map[string]map[int]*seasonQuality{}

	for key, items := range groups {
		if key.mediaKind != "tv" {
			continue
		}
		for _, entry := range items {
			if entry.specialLabel != "" {
				continue
			}
			screen := strings.ToLower(strings.TrimSpace(entry.fileParsed.ScreenSize))
			codec := strings.ToLower(strings.TrimSpace(entry.fileParsed.VideoCodec))
			if screen == "" && codec == "" {
				continue
			}
			season := -1
			if entry.fileParsed.Season != nil {
				season = *entry.fileParsed.Season
			}
			if perShow[key.title] == nil {
				perShow[key.title] = map[int]*seasonQuality{}
			}
			q := perShow[key.title][season]
			if q == nil {
				q = &seasonQuality{screens: map[string]int{}, codecs: map[string]int{}}
				perShow[key.title][season] = q
			}
			if screen != "" {
				q.screens[screen]++
			}
			if codec != "" {
				q.codecs[codec]++
			}
		}
	}

	out := make(map[string]string, len(perShow))
	for title, seasons := range perShow {
		bad := make([]int, 0, len(seasons))
		for season, q := range seasons {
			if len(q.screens) >= 2 || len(q.codecs) >= 2 {
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
			q := seasons[season]
			detail := make([]string, 0, 2)
			if len(q.screens) >= 2 {
				detail = append(detail, "分辨率 "+formatQualityCounts(q.screens))
			}
			if len(q.codecs) >= 2 {
				detail = append(detail, "编码 "+formatQualityCounts(q.codecs))
			}
			parts = append(parts, fmt.Sprintf("%s存在多种画质（%s）", seasonLabel(season), strings.Join(detail, "；")))
		}
		out[title] = strings.Join(parts, "；")
	}
	return out
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
