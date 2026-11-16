package audit

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// 本文件比对「电影合集（TMDB collection）里的各部」与本地已有作品，
// 用来报「有续集没收录」。同样是纯函数，不访问网络。
//
// 分两档，因为实测里合集常混入用户不想要的东西：
//   - 缺（Missing）：正片续集，计入不合格；
//   - 可选（Optional）：上映早于已收录全部作品的前传/序章，或上映更晚但明显是重剪/合集版；
//   - 未上映（Upcoming）：没上映或没定档，永远不算缺。

// FranchisePart 是合集里的一部作品（本地或 TMDB 一侧）。
type FranchisePart struct {
	ID          string
	Title       string
	ReleaseDate string
	Runtime     int
}

// CollectionDiff 是一个合集的比对结果。
type CollectionDiff struct {
	Name     string
	Parts    int
	Local    []FranchisePart
	Missing  []FranchisePart
	Optional []FranchisePart
	Upcoming []FranchisePart
	// Notes 记录 Optional 里每一部被判为可选的原因。
	Notes map[string]string
}

// Bad 表示合集里有该补的正片续集。
func (d CollectionDiff) Bad() bool { return len(d.Missing) > 0 }

// CompareCollection 比对合集与本地作品。
//
// local 是「tmdb id → 本地该部作品」，只包含本次扫描范围内的作品；
// 不在本次范围内的续集一律按缺处理，报告需注明「只按本次扫描范围判断」。
func CompareCollection(name string, parts []FranchisePart, local map[string]FranchisePart, today time.Time) CollectionDiff {
	todayStr := today.Format("2006-01-02")
	out := CollectionDiff{Name: name, Parts: len(parts), Notes: map[string]string{}}

	localParts := make([]FranchisePart, 0, len(local))
	for _, p := range parts {
		if lp, ok := local[p.ID]; ok {
			localParts = append(localParts, lp)
		}
	}

	for _, p := range parts {
		release := strings.TrimSpace(p.ReleaseDate)
		if release == "" || release > todayStr {
			out.Upcoming = append(out.Upcoming, p)
			continue
		}
		if _, ok := local[p.ID]; ok {
			out.Local = append(out.Local, p)
			continue
		}
		if reason := optionalReason(p, localParts, name); reason != "" {
			out.Optional = append(out.Optional, p)
			out.Notes[p.ID] = reason
			continue
		}
		out.Missing = append(out.Missing, p)
	}

	sortParts(out.Local)
	sortParts(out.Missing)
	sortParts(out.Optional)
	sortParts(out.Upcoming)
	return out
}

// optionalReason 判断一部未收录的作品是否属于「可选」档；返回空串表示按正片缺处理。
//
// 合集里常混着三类「不算缺」的东西：早于正片的前传/序章、同一剧情的重剪合集版、
// 以及番剧按篇章重剪的上映版（如《鬼灭之刃：游郭潜入篇》）。判据：
//   - 早于已收录全部作品，且标题与本地作品或合集名同系列 → 前传/序章；
//   - 晚于已收录全部作品，且标题与本地某部同系列、时长不超过其 1.1 倍 → 重剪/合集版；
//   - 晚于已收录全部作品，标题与合集同系列且本身是「…篇/编/总集/合集/BONUS」→ 篇章合集。
//
// 只用「合集名」而不看时长是危险的：像《三十极夜2》这种真续集，名同、时长也接近，
// 会被误降级，所以那条分支额外要求标题带合集性标记。
func optionalReason(part FranchisePart, localParts []FranchisePart, collectionName string) string {
	if len(localParts) == 0 || strings.TrimSpace(part.ReleaseDate) == "" {
		return ""
	}
	earliest, latest, maxRuntime := "", "", 0
	for _, lp := range localParts {
		rel := strings.TrimSpace(lp.ReleaseDate)
		if rel != "" {
			if earliest == "" || rel < earliest {
				earliest = rel
			}
			if latest == "" || rel > latest {
				latest = rel
			}
		}
		if lp.Runtime > maxRuntime {
			maxRuntime = lp.Runtime
		}
	}
	sharedLocal := false
	sharedLocalNonSequel := false
	for _, lp := range localParts {
		if !sharesSeriesName(part.Title, lp.Title) {
			continue
		}
		sharedLocal = true
		if !sequelOf(part.Title, lp.Title) {
			sharedLocalNonSequel = true
		}
	}
	sharedCollection := sharesSeriesName(part.Title, collectionName)
	if !sharedLocal && !sharedCollection {
		return ""
	}
	if earliest != "" && part.ReleaseDate < earliest {
		return "前传/序章（上映早于已收录的正片）"
	}
	if latest == "" || part.ReleaseDate <= latest {
		return ""
	}
	if sharedLocalNonSequel && runtimeComparable(part.Runtime, maxRuntime) {
		return "重剪/合集版（时长不超过已收录的最长版本）"
	}
	if sharedCollection && isCompilationTitle(part.Title) {
		return "篇章合集/总集篇（按剧集篇章重剪）"
	}
	return ""
}

// sequelOf 判断 partTitle 是否为 baseTitle 的「序号续集」：
// 标题在公共前缀之后紧跟数字（《三十极夜2》《教父2》《疾速追杀4》）。
// 这类条目即使名同、时长接近，也必须按正片缺报，不能降级成重剪版。
func sequelOf(partTitle, baseTitle string) bool {
	a := []rune(normalizeTitle(partTitle))
	b := []rune(normalizeTitle(baseTitle))
	if len(b) == 0 || len(a) <= len(b) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return unicode.IsDigit(a[len(b)])
}

// isCompilationTitle 判断标题是否带「篇章合集」性质的字样。
func isCompilationTitle(title string) bool {
	lower := strings.ToLower(title)
	for _, kw := range []string{"总集", "合集", "篇", "编", "bonus", "special", "recap"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func runtimeComparable(part, max int) bool {
	if part <= 0 || max <= 0 {
		return false
	}
	return float64(part) <= 1.1*float64(max)
}

// sharesSeriesName 判断两个标题是否同系列：去掉空白与标点后，
// 公共前缀占较短标题的比例 >= 0.6。
//
// 例：「小森林：四季」vs「小森林：夏秋篇」→ 公共前缀「小森林」= 3/5 = 0.6（同系列）；
// 「天使陷落」vs「伦敦陷落」→ 公共前缀为空（不同作品）。
func sharesSeriesName(a, b string) bool {
	x, y := normalizeTitle(a), normalizeTitle(b)
	if x == "" || y == "" {
		return false
	}
	xa, ya := []rune(x), []rune(y)
	n := len(xa)
	if len(ya) < n {
		n = len(ya)
	}
	common := 0
	for i := 0; i < n; i++ {
		if xa[i] != ya[i] {
			break
		}
		common++
	}
	shortest := len(xa)
	if len(ya) < shortest {
		shortest = len(ya)
	}
	if shortest == 0 {
		return false
	}
	return float64(common)/float64(shortest) >= 0.6
}

func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sortParts(list []FranchisePart) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ReleaseDate != list[j].ReleaseDate {
			return list[i].ReleaseDate < list[j].ReleaseDate
		}
		return list[i].ID < list[j].ID
	})
}
