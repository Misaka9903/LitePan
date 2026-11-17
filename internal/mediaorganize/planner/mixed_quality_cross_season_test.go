package planner

import "testing"

// 跨季画质不同（S01 全 1080p、S02 全 4K）属于正常情况，不应拦截。
func TestMixedQualityAllowsCrossSeasonDifference(t *testing.T) {
	p := &Planner{skipMixedQuality: true}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "S01E01 1080p.mkv", 1, "1080p", "h.264"),
			qualityEntry("e2", "S01E02 1080p.mkv", 1, "1080p", "h.264"),
			qualityEntry("e3", "S02E01 2160p.mkv", 2, "2160p", "h.265"),
			qualityEntry("e4", "S02E02 2160p.mkv", 2, "2160p", "h.265"),
		},
	}
	if out := p.mixedQualityShows(groups); len(out) != 0 {
		t.Fatalf("跨季画质不同不应判定为混画质: %v", out)
	}
}
