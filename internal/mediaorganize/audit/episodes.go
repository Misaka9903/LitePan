package audit

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"litepan/internal/mediaorganize/rules"
)

// 本文件只做「本地季集 vs TMDB 季集」的纯函数比对，不访问网络、不读文件头。
//
// 口径（与方案一致）：
//   - 特典（SP/OVA/特典/花絮…）与 Season 0 / S00 不参与集数判定；
//   - TMDB 侧只算已播出（air_date 非空且 <= today），未播的集不算缺；
//   - 在播季按已播集比对，剩余集只在 Aired/Total 里体现；
//   - 缺失比例过高或与 TMDB 完全不重叠时视为「编号差异」（番剧绝对集号、分季差异），
//     只提示不算缺。

// TMDBSeason 是 TMDB 单季的最小信息。
type TMDBSeason struct {
	Number       int
	AirDate      string
	EpisodeCount int
	// Episodes 为空表示没拉季详情，只能用 EpisodeCount 做数量核对。
	Episodes []TMDBEpisode
}

// TMDBEpisode 是 TMDB 单集的最小信息。
type TMDBEpisode struct {
	Number  int
	AirDate string
	Type    string
}

// SeasonDiff 是一季的比对结果。
type SeasonDiff struct {
	Season int `json:"season"`
	// Local 是本地解析出的集数，Aired 是 TMDB 已播出集数，Total 是 TMDB 该季总集数。
	Local int `json:"local"`
	Aired int `json:"aired"`
	Total int `json:"total"`
	// Missing 是已播出但本地没有的集号；Extra 是本地有而 TMDB 已播出列表里没有的集号。
	Missing []int `json:"missing"`
	Extra   []int `json:"extra"`
	// Airing 表示该季尚未播完。
	Airing bool `json:"airing"`
	// Unknown 表示本地集号与 TMDB 对不上（疑似绝对集号/分季差异），Missing 仅供参考。
	Unknown bool   `json:"unknown"`
	Note    string `json:"note"`
}

// AbsoluteDiff 是「季划分不一致」时的兜底结论：两边都换算成连续集号再比。
//
// 例：TMDB 把整部作品记成一季 85 集，本地按 S1/S2/S3 分开放，
// 逐季比对必然对不上；换算成绝对集号后能说清「缺第 67–85 集」。
type AbsoluteDiff struct {
	Local   int   `json:"local"`
	Aired   int   `json:"aired"`
	Missing []int `json:"missing"`
}

// Range 把缺失集号压成「67–85」这样的区间描述，供日志与前端显示。
func (a AbsoluteDiff) Range() string {
	return episodeRanges(a.Missing)
}

// ShowDiff 是一部剧的季集比对结果。
type ShowDiff struct {
	Seasons []SeasonDiff `json:"seasons"`
	// MissingSeasons 是 TMDB 已播出、本地整季没有的季号。
	MissingSeasons []int `json:"missing_seasons"`
	// ExtraSeasons 是本地有、TMDB 没有的季号（只作提示）。
	ExtraSeasons []int `json:"extra_seasons"`
	LocalSeasons []int `json:"local_seasons"`
	LatestSeason int   `json:"latest_season"`
	Unreliable   bool  `json:"unreliable"`
	// Absolute 只在季划分对不上时给出（Unreliable 为 true 时）。
	Absolute *AbsoluteDiff `json:"absolute,omitempty"`
}

// Bad 表示存在真正需要补的缺失（缺季或确有缺集）。
func (d ShowDiff) Bad() bool {
	if len(d.MissingSeasons) > 0 {
		return true
	}
	for _, s := range d.Seasons {
		if len(s.Missing) > 0 && !s.Unknown {
			return true
		}
	}
	return false
}

// EpisodeKey 是文件名里解析出来的一集。
type EpisodeKey struct {
	Season  int
	Episode int
}

// ParseEpisode 从文件名解析季集号；seasonFallback 为空文件名的季目录号（可为 0）。
// 识别为特典、Season 0 或解析不出集号时返回 ok=false，reason 说明原因。
func ParseEpisode(name string, seasonFallback int) (EpisodeKey, bool) {
	if rules.ExtractSpecialLabel(name) != "" {
		return EpisodeKey{}, false
	}
	parsed := rules.ParseFilenameStrict(name)
	// 文件名里显式写了季号（S02/第二季/Season 2）才以文件名为准；
	// 否则用所在季目录的季号——解析器在只有 E03 时会把季号默认成 1。
	season := 0
	switch {
	case parsed.Season != nil && rules.HasExplicitSeasonToken(name):
		season = *parsed.Season
	case seasonFallback > 0:
		season = seasonFallback
	case parsed.Season != nil:
		season = *parsed.Season
	}
	if parsed.Episode == nil || *parsed.Episode <= 0 || season <= 0 {
		return EpisodeKey{}, false
	}
	return EpisodeKey{Season: season, Episode: *parsed.Episode}, true
}

// CompareShow 比对本地季集与 TMDB 季集。
//
// local 是「季号 → 本地集号列表」，today 用于判断已播出。
func CompareShow(local map[int][]int, tmdb []TMDBSeason, today time.Time) ShowDiff {
	todayStr := today.Format("2006-01-02")
	aired := airedSeasons(tmdb, todayStr)

	localNums := make([]int, 0, len(local))
	for n := range local {
		if n > 0 {
			localNums = append(localNums, n)
		}
	}
	sort.Ints(localNums)

	out := ShowDiff{LocalSeasons: localNums}
	for _, n := range localNums {
		if n > out.LatestSeason {
			out.LatestSeason = n
		}
	}

	for _, n := range sortedSeasonNumbers(aired) {
		if _, ok := local[n]; ok {
			continue
		}
		out.MissingSeasons = append(out.MissingSeasons, n)
		if n > out.LatestSeason {
			out.LatestSeason = n
		}
	}

	for _, n := range localNums {
		s, ok := aired[n]
		if !ok {
			out.ExtraSeasons = append(out.ExtraSeasons, n)
			out.Seasons = append(out.Seasons, SeasonDiff{
				Season: n,
				Local:  len(local[n]),
				Note:   "TMDB 没有该季（已播出），只作提示",
			})
			continue
		}
		diff := compareSeason(n, local[n], s, todayStr)
		out.Seasons = append(out.Seasons, diff)
		if diff.Unknown {
			out.Unreliable = true
		}
	}
	// 只要逐季结论里出现「缺季/缺集/编号对不上」，就用绝对集号再核一遍：
	// 两侧季划分不同是常见的（TMDB 按播出季、本地按合集切），绝对集号能给出更实的结果。
	if out.Unreliable || len(out.MissingSeasons) > 0 || anyMissingEpisodes(out.Seasons) {
		if abs := compareAbsolute(local, aired, todayStr); abs != nil {
			if len(abs.Missing) == 0 {
				// 绝对集号齐全：季划分不同而已，撤掉逐季的误报。
				out.MissingSeasons = nil
				for i := range out.Seasons {
					out.Seasons[i].Missing = nil
					out.Seasons[i].Unknown = true
					out.Seasons[i].Note = "季划分与 TMDB 不同，按绝对集号核对是齐的"
				}
				out.Unreliable = true
				out.Absolute = abs
			} else if out.Unreliable {
				// 逐季结论不可靠时，绝对集号才是正式结论
				out.Absolute = abs
			}
			// 逐季可靠且绝对比对同样有缺：保留逐季结果，不设置 Absolute
		}
	}
	return out
}

// anyMissingEpisodes 判断逐季结论里是否有「确凿的缺集」（排除编号差异）。
func anyMissingEpisodes(seasons []SeasonDiff) bool {
	for _, s := range seasons {
		if len(s.Missing) > 0 && !s.Unknown {
			return true
		}
	}
	return false
}

// compareAbsolute 把两侧都换算成「从第 1 集起的连续集号」再比，用于季划分不一致的作品。
func compareAbsolute(local map[int][]int, aired map[int]TMDBSeason, todayStr string) *AbsoluteDiff {
	// 任何一季没有集列表时都不换算：只有 episode_count 的话无法判断哪些集已播，
	// 换算出来会把在播季的未播集也算成缺，比逐季结论更差。
	for _, s := range aired {
		if len(s.Episodes) == 0 {
			return nil
		}
	}
	localAbs, localTotal := absoluteNumbers(local, nil, todayStr)
	airedAbs, airedTotal := absoluteNumbers(nil, aired, todayStr)
	if localTotal == 0 || airedTotal == 0 {
		return nil
	}
	missing := make([]int, 0, 8)
	for abs := 1; abs <= airedTotal; abs++ {
		if !airedAbs[abs] {
			continue // 未播出/无该集
		}
		if !localAbs[abs] {
			missing = append(missing, abs)
		}
	}
	return &AbsoluteDiff{Local: localTotal, Aired: airedTotal, Missing: missing}
}

// absoluteNumbers 把「季号→集号」换算成连续集号：
// 按季号升序累加，第 n 季的起始集号 = 前面各季的集数之和 + 1。
func absoluteNumbers(local map[int][]int, aired map[int]TMDBSeason, todayStr string) (map[int]bool, int) {
	out := map[int]bool{}
	base := 0
	if local != nil {
		nums := make([]int, 0, len(local))
		for n := range local {
			if n > 0 {
				nums = append(nums, n)
			}
		}
		sort.Ints(nums)
		for _, n := range nums {
			eps := append([]int(nil), local[n]...)
			sort.Ints(eps)
			offset := 0
			for i, e := range eps {
				if i == 0 {
					offset = e - 1
				}
				out[base+e-offset] = true
			}
			if len(eps) > 0 {
				base += eps[len(eps)-1] - offset
			}
		}
		return out, base
	}
	for _, n := range sortedSeasonNumbers(aired) {
		s := aired[n]
		eps := make([]int, 0, len(s.Episodes))
		for _, e := range s.Episodes {
			if e.Number <= 0 || strings.EqualFold(e.Type, "special") {
				continue
			}
			d := strings.TrimSpace(e.AirDate)
			if d == "" || d > todayStr {
				continue
			}
			eps = append(eps, e.Number)
		}
		sort.Ints(eps)
		offset := 0
		for i, e := range eps {
			if i == 0 {
				offset = e - 1
			}
			out[base+e-offset] = true
		}
		if len(eps) > 0 {
			base += eps[len(eps)-1] - offset
		}
	}
	return out, base
}

// episodeRanges 把集号列表压成「5–7、9」这样的区间串。
func episodeRanges(eps []int) string {
	if len(eps) == 0 {
		return ""
	}
	sorted := append([]int(nil), eps...)
	sort.Ints(sorted)
	parts := make([]string, 0, 4)
	start, prev := sorted[0], sorted[0]
	flush := func() {
		if start == prev {
			parts = append(parts, fmt.Sprintf("%d", start))
			return
		}
		parts = append(parts, fmt.Sprintf("%d–%d", start, prev))
	}
	for _, e := range sorted[1:] {
		if e == prev+1 {
			prev = e
			continue
		}
		flush()
		start, prev = e, e
	}
	flush()
	return strings.Join(parts, "、")
}

// CompareSeasons 只比对「哪一季整季没收录」，不看集号。
//
// 用于「只查缺季」模式：只需要剧的季列表，不需要逐季拉集详情，请求量小。
func CompareSeasons(local map[int][]int, tmdb []TMDBSeason, today time.Time) ShowDiff {
	aired := airedSeasons(tmdb, today.Format("2006-01-02"))
	out := ShowDiff{}
	for n := range local {
		if n > 0 {
			out.LocalSeasons = append(out.LocalSeasons, n)
			if n > out.LatestSeason {
				out.LatestSeason = n
			}
		}
	}
	sort.Ints(out.LocalSeasons)
	for _, n := range sortedSeasonNumbers(aired) {
		if _, ok := local[n]; ok {
			continue
		}
		out.MissingSeasons = append(out.MissingSeasons, n)
		if n > out.LatestSeason {
			out.LatestSeason = n
		}
	}
	return out
}

// airedSeasons 过滤出「已经播出」的季：有集列表时以集列表为准，
// 否则看季的 air_date。未播出/未定档的季不参与缺失判定。
func airedSeasons(tmdb []TMDBSeason, todayStr string) map[int]TMDBSeason {
	aired := make(map[int]TMDBSeason, len(tmdb))
	for _, s := range tmdb {
		if s.Number <= 0 {
			continue
		}
		if s.EpisodeCount <= 0 && len(s.Episodes) == 0 {
			continue
		}
		if len(s.Episodes) > 0 {
			if !hasAiredEpisode(s.Episodes, todayStr) {
				continue
			}
		} else if strings.TrimSpace(s.AirDate) == "" || s.AirDate > todayStr {
			continue
		}
		aired[s.Number] = s
	}
	return aired
}

func compareSeason(number int, localEps []int, s TMDBSeason, todayStr string) SeasonDiff {
	diff := SeasonDiff{Season: number, Local: len(localEps), Total: s.EpisodeCount}

	airedEps := make([]int, 0, len(s.Episodes))
	total := s.EpisodeCount
	if len(s.Episodes) > 0 {
		if total <= 0 {
			total = len(s.Episodes)
		}
		for _, e := range s.Episodes {
			if e.Number <= 0 || strings.TrimSpace(e.AirDate) == "" || e.AirDate > todayStr {
				continue
			}
			if strings.EqualFold(e.Type, "special") {
				continue
			}
			airedEps = append(airedEps, e.Number)
		}
		sort.Ints(airedEps)
	} else if total > 0 {
		// 没拉季详情：只能按集数核对，集号未知。
		diff.Aired = total
		diff.Unknown = true
		diff.Note = "未取到 TMDB 集列表，只按集数核对"
		if diff.Local < total {
			diff.Note += "（本地更少，需人工确认）"
		}
		return diff
	} else {
		diff.Unknown = true
		diff.Note = "TMDB 该季没有可用集信息"
		return diff
	}

	diff.Aired = len(airedEps)
	diff.Total = total
	diff.Airing = total > 0 && len(airedEps) < total

	localSet := make(map[int]struct{}, len(localEps))
	for _, e := range localEps {
		localSet[e] = struct{}{}
	}
	airedSet := make(map[int]struct{}, len(airedEps))
	for _, e := range airedEps {
		airedSet[e] = struct{}{}
		if _, ok := localSet[e]; !ok {
			diff.Missing = append(diff.Missing, e)
		}
	}
	for _, e := range localEps {
		if _, ok := airedSet[e]; !ok {
			diff.Extra = append(diff.Extra, e)
		}
	}

	// 编号差异兜底：缺一大半或完全对不上，多半是绝对集号/分季差异，不当缺集报。
	if len(diff.Missing) > 0 && len(airedEps) > 0 {
		ratio := float64(len(diff.Missing)) / float64(len(airedEps))
		overlap := 0
		for _, e := range localEps {
			if _, ok := airedSet[e]; ok {
				overlap++
			}
		}
		if ratio > 0.5 || overlap == 0 {
			diff.Unknown = true
			diff.Note = "本地集号与 TMDB 对不上，疑似绝对集号或分季差异，需人工确认"
		}
	}
	return diff
}

// hasAiredEpisode 判断该季是否至少有一集已播出。
func hasAiredEpisode(eps []TMDBEpisode, todayStr string) bool {
	for _, e := range eps {
		if e.Number <= 0 || strings.EqualFold(e.Type, "special") {
			continue
		}
		d := strings.TrimSpace(e.AirDate)
		if d != "" && d <= todayStr {
			return true
		}
	}
	return false
}

func sortedSeasonNumbers(m map[int]TMDBSeason) []int {
	out := make([]int, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
