package planner

import (
	"strings"
	"testing"

	"litepan/internal/domain"
)

func mixedTestPlanner() *Planner {
	return &Planner{
		mediaExts: map[string]struct{}{"mkv": {}, "mp4": {}},
		log:       func(string) {},
	}
}

func mixedEntry(id, name string) batchEntry {
	return batchEntry{item: domain.FileItem{ID: id, Name: name}}
}

// withSeason 给条目补上解析出的季号（剧集的 groupKey 不含季，季来自每个文件的解析结果）。
func withSeason(e batchEntry, season int) batchEntry {
	e.fileParsed.Season = &season
	return e
}

func showKey() groupKey {
	return groupKey{mediaKind: "tv", title: "Show"}
}

func TestMixedExtensionDetectsMixInSeason(t *testing.T) {
	p := mixedTestPlanner()
	groups := map[groupKey][]batchEntry{
		showKey(): {
			withSeason(mixedEntry("1", "Show.S01E01.mkv"), 1),
			withSeason(mixedEntry("2", "Show.S01E02.mkv"), 1),
			withSeason(mixedEntry("3", "Show.S01E03.mp4"), 1),
		},
	}
	got := p.mixedExtensionShows(groups)
	detail, ok := got["Show"]
	if !ok {
		t.Fatal("混装季应被识别")
	}
	if !strings.Contains(detail, "第 1 季存在多种扩展名") || !strings.Contains(detail, "mkv×2") || !strings.Contains(detail, "mp4×1") {
		t.Fatalf("说明文字不符合预期: %q", detail)
	}
}

func TestMixedExtensionIgnoresUniformSeason(t *testing.T) {
	p := mixedTestPlanner()
	groups := map[groupKey][]batchEntry{
		showKey(): {
			withSeason(mixedEntry("1", "a.mkv"), 1),
			withSeason(mixedEntry("2", "b.MKV"), 1),
		},
	}
	if got := p.mixedExtensionShows(groups); len(got) != 0 {
		t.Fatalf("大小写不同不应算混装，得到 %v", got)
	}
}

func TestMixedExtensionIgnoresSpecials(t *testing.T) {
	p := mixedTestPlanner()
	sp := withSeason(mixedEntry("3", "Show.SP01.mp4"), 1)
	sp.specialLabel = "SP01"
	groups := map[groupKey][]batchEntry{
		showKey(): {
			withSeason(mixedEntry("1", "Show.S01E01.mkv"), 1),
			sp,
		},
	}
	if got := p.mixedExtensionShows(groups); len(got) != 0 {
		t.Fatalf("特典不参与判定，得到 %v", got)
	}
}

func TestMixedExtensionTreatsSeasonZeroAsNormal(t *testing.T) {
	p := mixedTestPlanner()
	groups := map[groupKey][]batchEntry{
		showKey(): {
			withSeason(mixedEntry("1", "Show.S00E01.mkv"), 0),
			withSeason(mixedEntry("2", "Show.S00E02.mp4"), 0),
		},
	}
	got := p.mixedExtensionShows(groups)
	if _, ok := got["Show"]; !ok {
		t.Fatal("Season 0 算正片，应参与判定")
	}
}

func TestMixedExtensionIgnoresMovies(t *testing.T) {
	p := mixedTestPlanner()
	groups := map[groupKey][]batchEntry{
		{mediaKind: "movie", title: "Movie"}: {
			mixedEntry("1", "Movie.2020.mkv"),
			mixedEntry("2", "Movie.2020.mp4"),
		},
	}
	if got := p.mixedExtensionShows(groups); len(got) != 0 {
		t.Fatalf("电影不参与判定，得到 %v", got)
	}
}

func TestApplyMixedExtensionGuardSkipsWholeShow(t *testing.T) {
	var logs []string
	p := &Planner{
		mediaExts:    map[string]struct{}{"mkv": {}, "mp4": {}},
		skipMixedExt: true,
		log:          func(msg string) { logs = append(logs, msg) },
	}
	// 剧集的 groupKey 不含季号：整部剧就是一个组
	groups := map[groupKey][]batchEntry{
		showKey(): {
			withSeason(mixedEntry("1", "Show.S01E01.mkv"), 1),
			withSeason(mixedEntry("2", "Show.S01E02.mp4"), 1),
			withSeason(mixedEntry("3", "Show.S02E01.mkv"), 2),
		},
	}
	p.applyMixedExtensionGuard(groups)

	if len(groups) != 0 {
		t.Fatalf("整部剧的组都应被移除，剩余 %d 组", len(groups))
	}
	if len(p.skippedItems) != 3 {
		t.Fatalf("三个文件都应留下跳过记录，实际 %d 条", len(p.skippedItems))
	}
	reason, _ := p.skippedItems[0]["reason"].(string)
	if !strings.Contains(reason, "已跳过整部剧") {
		t.Fatalf("跳过原因不符合预期: %q", reason)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "跳过整部剧") {
		t.Fatalf("应记录一条跳过日志，实际 %v", logs)
	}
}

func TestApplyMixedExtensionGuardDisabled(t *testing.T) {
	p := &Planner{
		mediaExts:    map[string]struct{}{"mkv": {}, "mp4": {}},
		skipMixedExt: false,
	}
	groups := map[groupKey][]batchEntry{
		showKey(): {
			withSeason(mixedEntry("1", "a.mkv"), 1),
			withSeason(mixedEntry("2", "b.mp4"), 1),
		},
	}
	p.applyMixedExtensionGuard(groups)
	if len(groups) != 1 {
		t.Fatal("关闭开关时不应改动分组")
	}
}
