package planner

import (
	"strings"
	"testing"
)

// 同一季里「有的写了编码、有的没写」不算混画质（分辨率一致、编码只有一种）。
func TestMixedQualityIgnoresPartialCodec(t *testing.T) {
	p := &Planner{skipMixedQuality: true}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01 1080p.mkv", 1, "1080p", ""),
			qualityEntry("e2", "02 1080p H.265.mkv", 1, "1080p", "H.265"),
			qualityEntry("e3", "03 1080p H.265.mkv", 1, "1080p", "H.265"),
		},
	}
	if out := p.mixedQualityShows(groups); len(out) != 0 {
		t.Fatalf("部分文件没写编码不应判为混画质: %v", out)
	}
}

// 分辨率一致但编码有两种，仍应判为混画质。
func TestMixedQualityDetectsCodecMix(t *testing.T) {
	p := &Planner{skipMixedQuality: true}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01 1080p H.264.mkv", 1, "1080p", "H.264"),
			qualityEntry("e2", "02 1080p H.265.mkv", 1, "1080p", "H.265"),
		},
	}
	out := p.mixedQualityShows(groups)
	if len(out) != 1 || !strings.Contains(out["某剧"], "编码") {
		t.Fatalf("编码混用应被识别: %v", out)
	}
}

// 分辨率混用（编码一致或缺失）应被识别。
func TestMixedQualityDetectsResolutionMix(t *testing.T) {
	p := &Planner{skipMixedQuality: true}
	key := groupKey{mediaKind: "tv", title: "某剧"}
	groups := map[groupKey][]batchEntry{
		key: {
			qualityEntry("e1", "01 2160p.mkv", 1, "2160p", ""),
			qualityEntry("e2", "02 1080p.mkv", 1, "1080p", ""),
		},
	}
	out := p.mixedQualityShows(groups)
	if len(out) != 1 || !strings.Contains(out["某剧"], "分辨率") {
		t.Fatalf("分辨率混用应被识别: %v", out)
	}
}
