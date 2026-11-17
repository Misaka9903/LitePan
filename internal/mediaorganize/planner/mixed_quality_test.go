package planner

import (
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/rules"
)

func qualityEntry(id, name string, season int, screen, codec string) batchEntry {
	s := season
	return batchEntry{
		item: domain.FileItem{ID: id, Name: name},
		fileParsed: rules.ParsedMedia{
			Season:     &s,
			ScreenSize: screen,
			VideoCodec: codec,
		},
	}
}

func TestMixedQualityShowsDetectsMixedSeason(t *testing.T) {
	p := &Planner{skipMixedQuality: true}
	key := groupKey{mediaKind: "tv", title: "完美世界"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01 4K.mp4", 1, "2160p", ""),
			qualityEntry("e2", "02 1080p.mp4", 1, "1080p", ""),
			qualityEntry("e3", "03 4K.mp4", 1, "2160p", ""),
		},
	}
	out := p.mixedQualityShows(groups)
	if len(out) != 1 {
		t.Fatalf("应识别出一部混画质的剧，实际 %v", out)
	}
	if !strings.Contains(out["完美世界"], "多种画质") {
		t.Fatalf("说明文字缺少关键字: %q", out["完美世界"])
	}
}

func TestMixedQualityShowsIgnoresUnlabeledSeason(t *testing.T) {
	p := &Planner{skipMixedQuality: true}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01.mp4", 1, "", ""),
			qualityEntry("e2", "02.mp4", 1, "", ""),
		},
	}
	if out := p.mixedQualityShows(groups); len(out) != 0 {
		t.Fatalf("整季都没写画质时不应拦截: %v", out)
	}
}

func TestMixedQualityGuardSkipsWholeShow(t *testing.T) {
	p := &Planner{skipMixedQuality: true, skippedItems: make([]map[string]any, 0)}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01 2160p.mkv", 1, "2160p", ""),
			qualityEntry("e2", "02 1080p.mkv", 1, "1080p", ""),
		},
	}
	p.applyMixedQualityGuard(groups)
	if len(groups) != 0 {
		t.Fatal("混画质的剧应从分组里移除")
	}
	if len(p.skippedItems) != 2 {
		t.Fatalf("两个文件都应记录跳过，实际 %d", len(p.skippedItems))
	}
	if reason, _ := p.skippedItems[0]["reason"].(string); !strings.Contains(reason, "已跳过整部剧") {
		t.Fatalf("跳过原因不对: %q", reason)
	}
}

func TestMixedQualityGuardDisabled(t *testing.T) {
	p := &Planner{skipMixedQuality: false}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01 2160p.mkv", 1, "2160p", ""),
			qualityEntry("e2", "02 1080p.mkv", 1, "1080p", ""),
		},
	}
	p.applyMixedQualityGuard(groups)
	if len(groups) != 1 {
		t.Fatal("关闭开关时不应拦截")
	}
}
