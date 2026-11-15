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
		p.log("[计划] 跳过整部剧：" + reason)
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
	type seasonMix struct {
		season string
		counts map[string]int
	}
	perShow := make(map[string][]seasonMix)

	for key, items := range groups {
		if key.mediaKind != "tv" {
			continue
		}
		counts := make(map[string]int, 2)
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
			counts[ext]++
		}
		if len(counts) < 2 {
			continue
		}
		perShow[key.title] = append(perShow[key.title], seasonMix{season: seasonLabelOf(key), counts: counts})
	}

	out := make(map[string]string, len(perShow))
	for title, seasons := range perShow {
		sort.Slice(seasons, func(i, j int) bool { return seasons[i].season < seasons[j].season })
		parts := make([]string, 0, len(seasons))
		for i, s := range seasons {
			if i >= 2 {
				parts = append(parts, fmt.Sprintf("等 %d 季存在多种扩展名", len(seasons)))
				break
			}
			parts = append(parts, fmt.Sprintf("%s存在多种扩展名（%s）", s.season, formatExtensionCounts(s.counts)))
		}
		out[title] = strings.Join(parts, "；")
	}
	return out
}

func seasonLabelOf(key groupKey) string {
	if key.hasSeason {
		return fmt.Sprintf("第 %d 季", key.season)
	}
	return "未标季"
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
