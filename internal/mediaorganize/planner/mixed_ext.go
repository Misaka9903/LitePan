package planner

import (
	"fmt"
	"sort"
	"strings"

	"litepan/internal/mediaorganize/rules"
)

// applyMixedExtensionGuard 在规划前做一次扩展名一致性检查：
// 同一部剧的某一季里如果混用了多种媒体扩展名（如 mkv + mp4），整部剧都不做任何操作，
// 只留下跳过记录（预览页「已跳过」里按原因聚合，汇总里计入「需关注」）。
//
// 放在这里而不是扫描阶段的原因：
//   - batch 里已经带着这部剧的全部文件，判定只是纯内存遍历，不额外扫网盘；
//   - 在 TMDB 匹配之前，可以省掉整部剧的 TMDB 请求。
//
// 特典按文件名识别（entry.specialLabel），不参与判定；S0 / Season 0 仍算正片。
// 注意：剧集的 groupKey 不含季号（季是逐文件算的），所以这里必须按 entry.fileParsed.Season 分季统计。
func (p *Planner) applyMixedExtensionGuard(groups map[groupKey][]batchEntry) {
	if !p.skipMixedExt || len(groups) == 0 {
		return
	}
	for title, detail := range p.mixedExtensionShows(groups) {
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

// mixedExtensionShows 返回「有混装季的剧」→ 说明文字（不含剧名）。
func (p *Planner) mixedExtensionShows(groups map[groupKey][]batchEntry) map[string]string {
	// title -> season(-1 表示未标季) -> 扩展名 -> 计数
	perShow := make(map[string]map[int]map[string]int)

	for key, items := range groups {
		if key.mediaKind != "tv" {
			continue
		}
		for _, entry := range items {
			// 特典（SP/OVA/特典/花絮…）不参与判定：它们常与正片来自不同压制组。
			if entry.specialLabel != "" {
				continue
			}
			ext := rules.FileExtension(entry.item.Name)
			if ext == "" {
				continue
			}
			if _, ok := p.mediaExts[ext]; !ok {
				continue
			}
			season := -1
			if entry.fileParsed.Season != nil {
				season = *entry.fileParsed.Season
			}
			if perShow[key.title] == nil {
				perShow[key.title] = make(map[int]map[string]int)
			}
			if perShow[key.title][season] == nil {
				perShow[key.title][season] = make(map[string]int, 2)
			}
			perShow[key.title][season][ext]++
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
				parts = append(parts, fmt.Sprintf("等 %d 季存在多种扩展名", len(bad)))
				break
			}
			parts = append(parts, fmt.Sprintf("%s存在多种扩展名（%s）", seasonLabel(season), formatExtensionCounts(seasons[season])))
		}
		out[title] = strings.Join(parts, "；")
	}
	return out
}

func seasonLabel(season int) string {
	if season < 0 {
		return "未标季"
	}
	return fmt.Sprintf("第 %d 季", season)
}

// formatExtensionCounts 输出形如 "mkv×11 / mp4×1"，按数量降序、同数量按扩展名升序。
func formatExtensionCounts(counts map[string]int) string {
	type item struct {
		ext string
		n   int
	}
	list := make([]item, 0, len(counts))
	for ext, n := range counts {
		list = append(list, item{ext: ext, n: n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].ext < list[j].ext
	})
	parts := make([]string, 0, len(list))
	for _, it := range list {
		parts = append(parts, fmt.Sprintf("%s×%d", it.ext, it.n))
	}
	return strings.Join(parts, " / ")
}
