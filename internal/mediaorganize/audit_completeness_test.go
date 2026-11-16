package mediaorganize

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"litepan/internal/mediaorganize/audit"
)

// stubAuditTMDB 是完整性比对用的假 TMDB：按 id 返回预设 JSON。
type stubAuditTMDB struct {
	tv          map[string]map[string]any
	movie       map[string]map[string]any
	seasons     map[string]map[int][]map[string]any
	collections map[string]map[string]any
	calls       int
}

func (s *stubAuditTMDB) toRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *stubAuditTMDB) Lookup(_ context.Context, id, mediaType string) (json.RawMessage, error) {
	s.calls++
	switch mediaType {
	case "tv":
		if v, ok := s.tv[id]; ok {
			return s.toRaw(v), nil
		}
	case "movie":
		if v, ok := s.movie[id]; ok {
			return s.toRaw(v), nil
		}
	}
	return nil, errors.New("tmdb: http status 404")
}

func (s *stubAuditTMDB) FetchTVSeason(_ context.Context, id string, season int) (json.RawMessage, error) {
	s.calls++
	eps, ok := s.seasons[id][season]
	if !ok {
		return nil, errors.New("tmdb: http status 404")
	}
	return s.toRaw(map[string]any{"episodes": eps}), nil
}

func (s *stubAuditTMDB) FetchCollection(_ context.Context, id string) (json.RawMessage, error) {
	s.calls++
	v, ok := s.collections[id]
	if !ok {
		return nil, errors.New("tmdb: http status 404")
	}
	return s.toRaw(v), nil
}

func episodeList(numbers ...int) []map[string]any {
	out := make([]map[string]any, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, map[string]any{"episode_number": n, "air_date": "2020-01-01", "episode_type": "standard"})
	}
	return out
}

func auditRunTree(t *testing.T, root audit.Node) audit.Report {
	t.Helper()
	return audit.Run(root, audit.Options{Required: []string{"screen_size"}, CollectEpisodes: true})
}

func TestApplyCompletenessFindsMissingEpisodes(t *testing.T) {
	rep := auditRunTree(t, dirNode("韩剧", "root",
		dirNode("缺集剧 (2020) {tmdb-100}", "g1",
			dirNode("Season 1", "s1",
				fileNode("缺集剧 (2020) S01E01 [1080p].mkv"),
				fileNode("缺集剧 (2020) S01E02 [1080p].mkv"),
				fileNode("缺集剧 (2020) S01E04 [1080p].mkv"),
			),
		),
	))

	client := &stubAuditTMDB{
		tv: map[string]map[string]any{
			"100": {"id": 100, "name": "缺集剧", "seasons": []any{
				map[string]any{"season_number": 1, "episode_count": 4, "air_date": "2020-01-01"},
			}},
		},
		seasons: map[string]map[int][]map[string]any{"100": {1: episodeList(1, 2, 3, 4)}},
	}
	svc := &Service{}
	if calls := svc.applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now()); calls == 0 {
		t.Fatal("应发起 TMDB 请求")
	}

	g := rep.Groups[0]
	if g.Match != "matched" || g.MediaType != "tv" || g.Completeness == nil {
		t.Fatalf("比对结论缺失: match=%q type=%q", g.Match, g.MediaType)
	}
	missing := g.Completeness.Seasons[0].Missing
	if len(missing) != 1 || missing[0] != 3 {
		t.Fatalf("缺失集 = %v, 期望 [3]", missing)
	}
	if !g.Completeness.Bad() {
		t.Fatal("缺集应判不合格")
	}
}

func TestApplyCompletenessFindsMissingSeason(t *testing.T) {
	rep := auditRunTree(t, dirNode("动漫", "root",
		dirNode("剧 (2018) {tmdb-200}", "g1",
			dirNode("S01", "s1",
				fileNode("剧(2018)-S01E1.mkv"),
			),
		),
	))
	client := &stubAuditTMDB{
		tv: map[string]map[string]any{
			"200": {"id": 200, "seasons": []any{
				map[string]any{"season_number": 1, "episode_count": 1, "air_date": "2018-10-02"},
				map[string]any{"season_number": 2, "episode_count": 24, "air_date": "2026-04-03"},
			}},
		},
		seasons: map[string]map[int][]map[string]any{"200": {1: episodeList(1)}},
	}
	svc := &Service{}
	svc.applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())

	g := rep.Groups[0]
	if g.Completeness == nil || len(g.Completeness.MissingSeasons) != 1 || g.Completeness.MissingSeasons[0] != 2 {
		t.Fatalf("缺失季结论不对: %+v", g.Completeness)
	}
}

func TestApplyCompletenessFranchise(t *testing.T) {
	rep := auditRunTree(t, dirNode("电影", "root",
		dirNode("陷落 (2016) {tmdb-267860}", "m1",
			fileNode("陷落 (2016) [2160p].mkv"),
		),
	))
	client := &stubAuditTMDB{
		movie: map[string]map[string]any{
			"267860": {"id": 267860, "title": "伦敦陷落", "release_date": "2016-03-02", "runtime": 99,
				"belongs_to_collection": map[string]any{"id": 9, "name": "陷落（系列）"}},
			"117263": {"id": 117263, "title": "奥林匹斯的陷落", "release_date": "2013-03-20", "runtime": 120},
			"423204": {"id": 423204, "title": "天使陷落", "release_date": "2019-08-21", "runtime": 122},
		},
		collections: map[string]map[string]any{
			"9": {"id": 9, "name": "陷落（系列）", "parts": []any{
				map[string]any{"id": 117263, "title": "奥林匹斯的陷落", "release_date": "2013-03-20"},
				map[string]any{"id": 267860, "title": "伦敦陷落", "release_date": "2016-03-02"},
				map[string]any{"id": 423204, "title": "天使陷落", "release_date": "2019-08-21"},
			}},
		},
	}
	svc := &Service{}
	svc.applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())

	g := rep.Groups[0]
	if g.MediaType != "movie" || g.Franchise == nil {
		t.Fatalf("电影合集结论缺失: type=%q", g.MediaType)
	}
	if len(g.Franchise.Missing) != 2 || len(g.Franchise.Local) != 1 {
		t.Fatalf("合集结论不对: missing=%d local=%d", len(g.Franchise.Missing), len(g.Franchise.Local))
	}
}

func TestApplyCompletenessMarksUnmatched(t *testing.T) {
	rep := auditRunTree(t, dirNode("韩剧", "root",
		dirNode("没标签的剧 (2021)", "g1",
			dirNode("Season 1", "s1",
				fileNode("没标签的剧 (2021) S01E01 [1080p].mkv"),
			),
		),
	))
	svc := &Service{}
	svc.applyCompleteness(context.Background(), &rep, &stubAuditTMDB{}, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())
	if rep.Groups[0].Match != "unmatched" {
		t.Fatalf("未匹配标记 = %q", rep.Groups[0].Match)
	}
}

func TestApplyCompletenessCachesTMDB(t *testing.T) {
	rep := auditRunTree(t, dirNode("韩剧", "root",
		dirNode("剧 (2020) {tmdb-100}", "g1",
			dirNode("Season 1", "s1", fileNode("剧 (2020) S01E01 [1080p].mkv")),
		),
	))
	client := &stubAuditTMDB{
		tv:      map[string]map[string]any{"100": {"seasons": []any{map[string]any{"season_number": 1, "episode_count": 1, "air_date": "2020-01-01"}}}},
		seasons: map[string]map[int][]map[string]any{"100": {1: episodeList(1)}},
	}
	svc := &Service{}
	first := svc.applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())
	if first == 0 {
		t.Fatal("首次应发起请求")
	}
	before := client.calls
	second := svc.applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())
	if second != 0 || client.calls != before {
		t.Fatalf("第二次应命中缓存：calls=%d→%d, second=%d", before, client.calls, second)
	}
	if c, ok := svc.cachedTMDB("tv:100"); !ok || c == nil {
		t.Fatal("缓存里应有季信息")
	}
}

func TestApplyCompletenessSkipsWhenDisabled(t *testing.T) {
	rep := auditRunTree(t, dirNode("韩剧", "root",
		dirNode("剧 (2020) {tmdb-100}", "g1",
			dirNode("Season 1", "s1", fileNode("剧 (2020) S01E01 [1080p].mkv")),
		),
	))
	client := &stubAuditTMDB{}
	if calls := (&Service{}).applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{}, time.Now()); calls != 0 || client.calls != 0 {
		t.Fatalf("关闭时不该请求：calls=%d", client.calls)
	}
}

// ---- 测试用目录树构造 ----

func dirNode(name, id string, kids ...audit.Node) audit.Node {
	return audit.Node{ID: id, Name: name, IsDir: true, Children: kids}
}

func fileNode(name string) audit.Node {
	return audit.Node{Name: name, Size: 100}
}

// 回归：电影库里的 id 常常也能撞上一个无关的剧（TMDB 剧/电影是两套 ID 空间）。
// 目录里只有单个正片文件时必须按电影处理，不能拿剧的季集去比。
func TestApplyCompletenessMovieWithCollidingTVID(t *testing.T) {
	rep := auditRunTree(t, dirNode("电影", "root",
		dirNode("三十极夜 (2007) {tmdb-4513}", "m1",
			fileNode("三十极夜 (2007) [2160p].mkv"),
		),
	))
	client := &stubAuditTMDB{
		// id 4513 同时存在于剧与电影（真实情况），剧那边是一堆无关的集号
		tv: map[string]map[string]any{"4513": {"id": 4513, "seasons": []any{
			map[string]any{"season_number": 1, "episode_count": 22, "air_date": "2010-01-01"},
		}}},
		seasons: map[string]map[int][]map[string]any{"4513": {1: episodeList(1, 2, 3, 4, 5)}},
		movie: map[string]map[string]any{"4513": {"id": 4513, "title": "三十极夜",
			"release_date": "2007-10-17", "runtime": 113}},
	}
	(&Service{}).applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())

	g := rep.Groups[0]
	if g.MediaType != "movie" {
		t.Fatalf("电影被当成剧: type=%q", g.MediaType)
	}
	if g.Completeness != nil {
		t.Fatalf("电影不该有季集结论: %+v", g.Completeness)
	}
}

// 回归：《杀死比尔2》这类片名带序号的电影，不能被解析出的「集号」判成剧。
func TestApplyCompletenessMovieWithNumberInName(t *testing.T) {
	rep := auditRunTree(t, dirNode("电影", "root",
		dirNode("杀死比尔2 (2004) {tmdb-393}", "m1",
			fileNode("杀死比尔2 (2004) [2160p].mkv"),
		),
	))
	client := &stubAuditTMDB{
		tv: map[string]map[string]any{"393": {"seasons": []any{
			map[string]any{"season_number": 1, "episode_count": 14, "air_date": "2010-01-01"},
		}}},
		seasons: map[string]map[int][]map[string]any{"393": {1: episodeList(1, 2, 3)}},
		movie:   map[string]map[string]any{"393": {"id": 393, "title": "杀死比尔2", "release_date": "2004-04-16", "runtime": 137}},
	}
	(&Service{}).applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())
	if g := rep.Groups[0]; g.MediaType != "movie" || g.Completeness != nil {
		t.Fatalf("应按电影处理: type=%q completeness=%+v", g.MediaType, g.Completeness)
	}
}

// 只查缺季：不拉季详情，也能报「第三季整季没收录」。
func TestApplyCompletenessSeasonsOnly(t *testing.T) {
	rep := auditRunTree(t, dirNode("动漫", "root",
		dirNode("只有两季的剧 (2021) {tmdb-300}", "g1",
			dirNode("Season 1", "s1", fileNode("剧 (2021) S01E01 [1080p].mkv")),
			dirNode("Season 2", "s2", fileNode("剧 (2021) S02E01 [1080p].mkv")),
		),
	))
	client := &stubAuditTMDB{
		tv: map[string]map[string]any{
			"300": {"id": 300, "seasons": []any{
				map[string]any{"season_number": 1, "episode_count": 12, "air_date": "2021-01-01"},
				map[string]any{"season_number": 2, "episode_count": 12, "air_date": "2023-01-01"},
				map[string]any{"season_number": 3, "episode_count": 12, "air_date": "2026-09-01"},
				map[string]any{"season_number": 4, "episode_count": 12, "air_date": "2028-01-01"}, // 未播出
			}},
		},
		seasons: map[string]map[int][]map[string]any{"300": {1: episodeList(1), 2: episodeList(1)}},
	}
	svc := &Service{}
	svc.applyCompleteness(context.Background(), &rep, client, 0, completenessOptions{Seasons: true}, time.Now())

	g := rep.Groups[0]
	if g.Completeness == nil {
		t.Fatal("缺季结论缺失")
	}
	if want := []int{3}; len(g.Completeness.MissingSeasons) != 1 || g.Completeness.MissingSeasons[0] != 3 {
		t.Fatalf("缺失季 = %v, 期望 %v", g.Completeness.MissingSeasons, want)
	}
	if len(g.Completeness.Seasons) != 0 {
		t.Fatalf("只查缺季时不该有逐季集号结论: %+v", g.Completeness.Seasons)
	}
	// 3 次调用：1 次剧详情 + 0 次季详情（stub 里 seasons 命中也不该被拉）
	if client.calls != 1 {
		t.Fatalf("TMDB 调用 = %d, 期望 1（只查剧详情）", client.calls)
	}
}

// 本地文件名既没有 SxxExx、也没有季目录时，不能把 TMDB 的每一季都报成缺失。
func TestApplyCompletenessUnparsedLocalSeasons(t *testing.T) {
	rep := auditRunTree(t, dirNode("韩剧", "root",
		dirNode("无季号剧 (2021) {tmdb-500}", "g1",
			fileNode("无季号剧 (2021) 上.mkv"),
			fileNode("无季号剧 (2021) 下.mkv"),
		),
	))
	client := &stubAuditTMDB{
		tv: map[string]map[string]any{"500": {"seasons": []any{
			map[string]any{"season_number": 1, "episode_count": 12, "air_date": "2021-01-01"},
			map[string]any{"season_number": 2, "episode_count": 12, "air_date": "2023-01-01"},
		}}},
	}
	(&Service{}).applyCompleteness(context.Background(), &rep, client, 0,
		completenessOptions{Seasons: true, Episodes: true}, time.Now())

	g := rep.Groups[0]
	if g.Match != "unparsed" {
		t.Fatalf("应标记为未识别季集，实际 match=%q", g.Match)
	}
	if g.Completeness != nil {
		t.Fatalf("不该给出缺季结论: %+v", g.Completeness.MissingSeasons)
	}
}

// 只给「晚于本地全部作品」的 part 补详情：更早的（前传判定）与夹在中间的都不必查。
func TestEnrichMissingPartsOnlyForLaterParts(t *testing.T) {
	rep := auditRunTree(t, dirNode("电影", "root",
		dirNode("完结篇 (2019) {tmdb-300}", "m1", fileNode("完结篇 (2019) [2160p].mkv")),
	))
	client := &stubAuditTMDB{
		movie: map[string]map[string]any{
			"300": {"id": 300, "title": "完结篇", "release_date": "2019-01-01", "runtime": 120,
				"belongs_to_collection": map[string]any{"id": 77, "name": "某系列"}},
		},
		collections: map[string]map[string]any{
			"77": {"id": 77, "name": "某系列", "parts": []any{
				map[string]any{"id": 100, "title": "第一部", "release_date": "2013-01-01"},
				map[string]any{"id": 200, "title": "第二部", "release_date": "2016-01-01"},
				map[string]any{"id": 300, "title": "完结篇", "release_date": "2019-01-01"},
			}},
		},
	}
	(&Service{}).applyCompleteness(context.Background(), &rep, client, 0,
		completenessOptions{Seasons: true, Episodes: true, Franchise: true}, time.Now())

	// 1 次电影详情 + 1 次合集；两部更早的 part 不再逐个查详情
	if client.calls != 2 {
		t.Fatalf("TMDB 调用 = %d, 期望 2（不额外 enrich 更早的续集）", client.calls)
	}
}

// 回归：整库里有更新的电影时，不能让老合集里的重剪版被跳过详情查询（会误判成缺）。
func TestEnrichMissingPartsUsesCollectionLocalDates(t *testing.T) {
	rep := auditRunTree(t, dirNode("电影", "root",
		dirNode("冬春篇 (2015) {tmdb-336026}", "m1", fileNode("冬春篇 (2015) [2160p].mkv")),
		dirNode("夏秋篇 (2014) {tmdb-294682}", "m3", fileNode("夏秋篇 (2014) [2160p].mkv")),
		dirNode("新片 (2026) {tmdb-900}", "m2", fileNode("新片 (2026) [2160p].mkv")),
	))
	client := &stubAuditTMDB{
		movie: map[string]map[string]any{
			"336026": {"id": 336026, "title": "小森林：冬春篇", "release_date": "2015-01-28", "runtime": 120,
				"belongs_to_collection": map[string]any{"id": 66, "name": "小森林（系列）"}},
			"294682": {"id": 294682, "title": "小森林：夏秋篇", "release_date": "2014-08-30", "runtime": 111},
			"890406": {"id": 890406, "title": "小森林：四季", "release_date": "2018-03-22", "runtime": 120},
			"900":    {"id": 900, "title": "新片", "release_date": "2026-05-01", "runtime": 100},
		},
		collections: map[string]map[string]any{
			"66": {"id": 66, "name": "小森林（系列）", "parts": []any{
				map[string]any{"id": 294682, "title": "小森林：夏秋篇", "release_date": "2014-08-30"},
				map[string]any{"id": 336026, "title": "小森林：冬春篇", "release_date": "2015-01-28"},
				map[string]any{"id": 890406, "title": "小森林：四季", "release_date": "2018-03-22"},
			}},
		},
	}
	(&Service{}).applyCompleteness(context.Background(), &rep, client, 0,
		completenessOptions{Franchise: true}, time.Now())

	for _, g := range rep.Groups {
		if g.Franchise == nil {
			continue
		}
		if len(g.Franchise.Missing) != 0 || len(g.Franchise.Optional) != 1 {
			t.Fatalf("重剪版应判可选: missing=%v optional=%v", g.Franchise.Missing, g.Franchise.Optional)
		}
	}
}
