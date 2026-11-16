package audit

import (
	"reflect"
	"testing"
	"time"
)

var testToday = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

func season(num, count int, dates ...string) TMDBSeason {
	s := TMDBSeason{Number: num, EpisodeCount: count, AirDate: "2020-01-01"}
	for i, d := range dates {
		s.Episodes = append(s.Episodes, TMDBEpisode{Number: i + 1, AirDate: d, Type: "standard"})
	}
	if len(dates) > 0 {
		s.EpisodeCount = len(dates)
	}
	return s
}

func dates(n int, day string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = day
	}
	return out
}

func TestCompareShowComplete(t *testing.T) {
	diff := CompareShow(
		map[int][]int{1: {1, 2, 3, 4, 5, 6}},
		[]TMDBSeason{season(1, 6, dates(6, "2021-08-27")...)},
		testToday,
	)
	if diff.Bad() || len(diff.MissingSeasons) != 0 {
		t.Fatalf("齐全的剧被判缺: %+v", diff)
	}
	if diff.Seasons[0].Local != 6 || diff.Seasons[0].Aired != 6 {
		t.Fatalf("计数不对: %+v", diff.Seasons[0])
	}
}

func TestCompareShowMissingMiddleEpisodes(t *testing.T) {
	diff := CompareShow(
		map[int][]int{1: {1, 2, 3, 4, 6, 8}},
		[]TMDBSeason{season(1, 8, dates(8, "2021-08-27")...)},
		testToday,
	)
	if !diff.Bad() {
		t.Fatal("缺集没被判为不合格")
	}
	if want := []int{5, 7}; !reflect.DeepEqual(diff.Seasons[0].Missing, want) {
		t.Fatalf("缺失集 = %v, 期望 %v", diff.Seasons[0].Missing, want)
	}
}

func TestCompareShowMissingWholeSeason(t *testing.T) {
	diff := CompareShow(
		map[int][]int{1: {1, 2, 3}},
		[]TMDBSeason{
			season(1, 3, dates(3, "2018-10-02")...),
			season(2, 24, dates(24, "2026-04-03")...),
		},
		testToday,
	)
	if want := []int{2}; !reflect.DeepEqual(diff.MissingSeasons, want) {
		t.Fatalf("缺失季 = %v, 期望 %v", diff.MissingSeasons, want)
	}
	if !diff.Bad() {
		t.Fatal("缺整季应判不合格")
	}
	if diff.LatestSeason != 2 {
		t.Fatalf("最新季 = %d, 期望 2", diff.LatestSeason)
	}
}

func TestCompareShowAiringSeasonOnlyCountsAired(t *testing.T) {
	// 16 集里只播了 6 集，本地有 6 集 → 不算缺；Airing 为 true
	s := TMDBSeason{Number: 2, EpisodeCount: 16, AirDate: "2026-10-01"}
	for i := 1; i <= 16; i++ {
		day := "2026-10-01"
		if i > 6 {
			day = "2027-01-01" // 未播出
		}
		s.Episodes = append(s.Episodes, TMDBEpisode{Number: i, AirDate: day})
	}
	diff := CompareShow(map[int][]int{2: {1, 2, 3, 4, 5, 6}}, []TMDBSeason{s}, testToday)
	if diff.Bad() {
		t.Fatalf("在播季未播集被当成缺失: %+v", diff.Seasons)
	}
	if !diff.Seasons[0].Airing || diff.Seasons[0].Aired != 6 || diff.Seasons[0].Total != 16 {
		t.Fatalf("在播季数字不对: %+v", diff.Seasons[0])
	}
}

func TestCompareShowIgnoresSpecialsAndUnairedSeasons(t *testing.T) {
	diff := CompareShow(
		map[int][]int{1: {1, 2}},
		[]TMDBSeason{
			{Number: 0, EpisodeCount: 1, AirDate: "2017-05-29", Episodes: []TMDBEpisode{{Number: 1, AirDate: "2017-05-29"}}},
			{Number: 3, EpisodeCount: 12, AirDate: "2027-04-01",
				Episodes: []TMDBEpisode{{Number: 1, AirDate: "2027-04-01", Type: "standard"}}}, // 未播出
		},
		testToday,
	)
	if len(diff.MissingSeasons) != 0 || diff.Bad() {
		t.Fatalf("特别篇/未播出季被算作缺失: %+v", diff)
	}
}

func TestCompareShowAbsoluteNumberingIsUnreliable(t *testing.T) {
	// 本地 S2 用绝对集号 14–26，TMDB S2 是 1–13：完全对不上 → 标不可靠，不算缺
	local := map[int][]int{}
	eps := make([]int, 0, 13)
	for i := 14; i <= 26; i++ {
		eps = append(eps, i)
	}
	local[2] = eps
	diff := CompareShow(local, []TMDBSeason{season(2, 13, dates(13, "2021-01-01")...)}, testToday)
	if diff.Bad() {
		t.Fatalf("绝对集号被当成缺集: %+v", diff.Seasons)
	}
	if !diff.Seasons[0].Unknown || !diff.Unreliable {
		t.Fatalf("应标记为编号差异: %+v", diff.Seasons[0])
	}
}

func TestCompareShowExtraAndUnknownSeasons(t *testing.T) {
	diff := CompareShow(
		map[int][]int{1: {1, 2, 3, 99}, 7: {1}},
		[]TMDBSeason{season(1, 3, dates(3, "2020-01-01")...)},
		testToday,
	)
	if len(diff.Seasons[0].Missing) != 0 {
		t.Fatalf("多余集不该算缺失: %+v", diff.Seasons[0])
	}
	if want := []int{99}; !reflect.DeepEqual(diff.Seasons[0].Extra, want) {
		t.Fatalf("多余集 = %v, 期望 %v", diff.Seasons[0].Extra, want)
	}
	if want := []int{7}; !reflect.DeepEqual(diff.ExtraSeasons, want) {
		t.Fatalf("多出的季 = %v, 期望 %v", diff.ExtraSeasons, want)
	}
}

func TestCompareShowWithoutEpisodeListIsUnknown(t *testing.T) {
	diff := CompareShow(map[int][]int{1: {1, 2}},
		[]TMDBSeason{{Number: 1, EpisodeCount: 16, AirDate: "2020-01-01"}}, testToday)
	if diff.Bad() {
		t.Fatalf("没有集列表时不应判缺: %+v", diff.Seasons)
	}
	if !diff.Seasons[0].Unknown || diff.Seasons[0].Note == "" {
		t.Fatalf("应标注需人工确认: %+v", diff.Seasons[0])
	}
}

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		name     string
		fallback int
		wantOK   bool
		want     EpisodeKey
	}{
		{"剧名 (2024) S02E05 [1080p].mkv", 0, true, EpisodeKey{2, 5}},
		{"关于我转生变成史莱姆这档事(2018)-S01E1.mkv", 0, true, EpisodeKey{1, 1}},
		{"Attack on Titan.2013.S04E28.2160p.mkv", 0, true, EpisodeKey{4, 28}},
		{"剧名 E03.mkv", 3, true, EpisodeKey{3, 3}}, // 季号来自季目录
		{"剧名 NCOP01.mkv", 1, false, EpisodeKey{}}, // 特典
		{"剧名 S00E01.mkv", 0, false, EpisodeKey{}}, // Season 0 不参与
		{"剧名 花絮.mkv", 1, false, EpisodeKey{}},     // 解析不出集号
	}
	for _, c := range cases {
		got, ok := ParseEpisode(c.name, c.fallback)
		if ok != c.wantOK || got != c.want {
			t.Fatalf("ParseEpisode(%q, %d) = %+v/%v, 期望 %+v/%v", c.name, c.fallback, got, ok, c.want, c.wantOK)
		}
	}
}

// 容器目录（「某某（系列）」）里的作品目录各自成组；作品目录里内嵌的剧场版也独立分成一组。
func TestRunGroupsByTMDBTaggedDir(t *testing.T) {
	root := dir("动漫", "root",
		dir("鬼灭之刃（系列）", "c1",
			dir("鬼灭之刃 (2019) {tmdbid-85937}", "c1a",
				dir("Season 1", "c1a1",
					file("鬼灭之刃 (2019) S01E01 [1080p].mkv", 100),
					file("鬼灭之刃 (2019) S01E02 [1080p].mkv", 100),
				),
			),
			dir("鬼灭之刃：无限列车篇 (2020) {tmdbid-635302}", "c1b",
				file("鬼灭之刃：无限列车篇 (2020) [1080p].mkv", 300),
			),
		),
		dir("进击的巨人 (2013) {tmdb-1429}", "c2",
			dir("Season 4", "c2a",
				file("Attack on Titan.2013.S04E01.2160p.mkv", 200),
			),
			dir("进击的巨人 完结篇（前篇） (2023) {tmdbid-313024}", "c2b",
				file("进击的巨人 完结篇（前篇） (2023).mkv", 400),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size"}, CollectEpisodes: true})

	if rep.TotalGroups != 4 {
		t.Fatalf("分组数 = %d, 期望 4（容器里的两部作品 + 剧 + 剧场版）", rep.TotalGroups)
	}
	tv := find(t, rep, "鬼灭之刃 (2019) {tmdbid-85937}")
	if tv.Path != "鬼灭之刃（系列）/鬼灭之刃 (2019) {tmdbid-85937}" {
		t.Fatalf("分组路径 = %q", tv.Path)
	}
	if len(tv.Episodes[1]) != 2 {
		t.Fatalf("集号收集不对: %+v", tv.Episodes)
	}
	atk := find(t, rep, "进击的巨人 (2013) {tmdb-1429}")
	if len(atk.Episodes[4]) != 1 {
		t.Fatalf("进击的巨人集号不对: %+v", atk.Episodes)
	}
}

// 直接选中某一部作品当扫描根时，整棵树都属于它，不该拆成一堆「未匹配」的分组。
func TestRunSingleShowRootWithTag(t *testing.T) {
	root := dir("进击的巨人 (2013) {tmdb-1429}", "root",
		dir("Season 1", "s1", file("Attack on Titan.2013.S01E01.2160p.mkv", 100)),
		dir("Season 2", "s2", file("Attack on Titan.2013.S02E01.2160p.mkv", 100)),
		dir("进击的巨人 完结篇（前篇） (2023) {tmdbid-313024}", "m1",
			file("进击的巨人 完结篇（前篇） (2023).mkv", 200)),
	)
	rep := Run(root, Options{Required: []string{"screen_size"}, CollectEpisodes: true})
	if rep.TotalGroups != 1 {
		t.Fatalf("分组数 = %d, 期望 1", rep.TotalGroups)
	}
	g := rep.Groups[0]
	if g.Name != "进击的巨人 (2013) {tmdb-1429}" || len(g.Episodes) != 2 {
		t.Fatalf("单部作品分组不对: name=%q episodes=%+v", g.Name, g.Episodes)
	}
}

// TMDB 把整部作品记成一季 85 集、本地按 S1/S2/S3 分开时，
// 逐季对不上，改用绝对集号应报出「缺第 67–85 集」。
func TestCompareShowAbsoluteFallback(t *testing.T) {
	eps := make([]TMDBEpisode, 0, 85)
	for i := 1; i <= 85; i++ {
		eps = append(eps, TMDBEpisode{Number: i, AirDate: "2020-01-01", Type: "standard"})
	}
	local := map[int][]int{}
	for s := 1; s <= 3; s++ {
		n := 25
		if s == 3 {
			n = 16
		}
		list := make([]int, 0, n)
		for i := 1; i <= n; i++ {
			list = append(list, i)
		}
		local[s] = list
	}
	diff := CompareShow(local, []TMDBSeason{{Number: 1, EpisodeCount: 85, AirDate: "2016-04-03", Episodes: eps}}, testToday)

	if diff.Absolute == nil {
		t.Fatal("应给出绝对集号结论")
	}
	if diff.Absolute.Local != 66 || diff.Absolute.Aired != 85 {
		t.Fatalf("绝对集号统计 = %d/%d, 期望 66/85", diff.Absolute.Local, diff.Absolute.Aired)
	}
	if got := diff.Absolute.Range(); got != "67–85" {
		t.Fatalf("缺失区间 = %q, 期望 67–85", got)
	}
	if diff.Bad() {
		t.Fatal("编号差异不应算不合格")
	}
}

// 季划分不同但总集数齐全（TMDB 两季 12+12，本地并成一季 24）→ 不报缺季。
func TestCompareShowDifferentSeasonSplitButComplete(t *testing.T) {
	s1 := season(1, 12, dates(12, "2020-01-01")...)
	s2 := season(2, 12, dates(12, "2020-06-01")...)
	list := make([]int, 0, 24)
	for i := 1; i <= 24; i++ {
		list = append(list, i)
	}
	diff := CompareShow(map[int][]int{1: list}, []TMDBSeason{s1, s2}, testToday)

	if diff.Bad() {
		t.Fatalf("绝对集号齐全时不该报缺季/缺集: missingSeasons=%v seasons=%+v", diff.MissingSeasons, diff.Seasons)
	}
	if diff.Absolute == nil || len(diff.Absolute.Missing) != 0 {
		t.Fatalf("应有绝对集号结论且无缺失: %+v", diff.Absolute)
	}
}
