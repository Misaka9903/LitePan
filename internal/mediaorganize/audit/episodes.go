package audit

import (
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
	Season int
	// Local 是本地解析出的集数，Aired 是 TMDB 已播出集数，Total 是 TMDB 该季总集数。
	Local int
	Aired int
	Total int
	// Missing 是已播出但本地没有的集号；Extra 是本地有而 TMDB 已播出列表里没有的集号。
	Missing []int
	Extra   []int
	// Airing 表示该季尚未播完。
	Airing bool
	// Unknown 表示本地集号与 TMDB 对不上（疑似绝对集号/分季差异），Missing 仅供参考。
	Unknown bool
	Note    string
}

// ShowDiff 是一部剧的季集比对结果。
type ShowDiff struct {
	Seasons []SeasonDiff
	// MissingSeasons 是 TMDB 已播出、本地整季没有的季号。
	MissingSeasons []int
	// ExtraSeasons 是本地有、TMDB 没有的季号（只作提示）。
	ExtraSeasons []int
	LocalSeasons []int
	LatestSeason int
	Unreliable   bool
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
	aired := make(map[int]TMDBSeason, len(tmdb))
	for _, s := range tmdb {
		if s.Number <= 0 {
			continue
		}
		if s.EpisodeCount <= 0 && len(s.Episodes) == 0 {
			continue
		}
		if len(s.Episodes) > 0 {
			// 以集列表为准：整季一集都没播（新番刚公布）就不算已播出季。
			if !hasAiredEpisode(s.Episodes, todayStr) {
				continue
			}
		} else if strings.TrimSpace(s.AirDate) == "" || s.AirDate > todayStr {
			continue // 未播出/未定档的季
		}
		aired[s.Number] = s
	}

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
	return out
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
