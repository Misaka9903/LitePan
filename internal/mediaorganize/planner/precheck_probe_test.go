package planner

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/rules"
	"litepan/internal/mediaprobe"
)

type flakyProber struct {
	calls int
	fails int
	tags  map[string]string
}

func (f *flakyProber) ProbeBucketed(_ context.Context, _ mediaprobe.Request) (map[string]string, bool) {
	f.calls++
	if f.calls <= f.fails {
		return nil, false
	}
	return f.tags, true
}

func probeTestItems(n int) []batchEntry {
	season := 1
	items := make([]batchEntry, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, batchEntry{
			item:       domain.FileItem{ID: "ep" + string(rune('0'+i)), Name: "Show.S01E0" + string(rune('0'+i)) + ".mkv"},
			fileParsed: rules.ParsedMedia{Season: &season, Episode: &i},
		})
	}
	return items
}

func TestPrecheckProbeRetriesOnceThenSkips(t *testing.T) {
	prober := &flakyProber{fails: 99, tags: map[string]string{"screen_size": "1080p"}}
	p := &Planner{prober: prober, probeMode: "prefer", mediaTagOrder: fullTagOrder()}

	if p.precheckProbeGroup(groupKey{}, probeTestItems(5)) == nil {
		t.Fatal("两次都失败时应返回失败原因（整部剧跳过）")
	}
	if prober.calls != probeRetryLimit {
		t.Fatalf("应只尝试 %d 次，实际 %d", probeRetryLimit, prober.calls)
	}
}

func TestPrecheckProbeSucceedsOnSecondFile(t *testing.T) {
	prober := &flakyProber{fails: 1, tags: map[string]string{"screen_size": "1080p"}}
	p := &Planner{prober: prober, probeMode: "prefer", mediaTagOrder: fullTagOrder()}

	if p.precheckProbeGroup(groupKey{}, probeTestItems(5)) != nil {
		t.Fatal("换文件重试成功后应返回 nil")
	}
	if prober.calls != 2 {
		t.Fatalf("首次失败 + 重试一次 = 2 次，实际 %d", prober.calls)
	}
}

func TestPrecheckProbeDisabledWhenProbeOff(t *testing.T) {
	prober := &flakyProber{fails: 99}
	p := &Planner{prober: prober, probeMode: "off", mediaTagOrder: fullTagOrder()}

	if p.precheckProbeGroup(groupKey{}, probeTestItems(3)) != nil {
		t.Fatal("探测关闭时不应拦截")
	}
	if prober.calls != 0 {
		t.Fatalf("探测关闭时不应调用探测，实际 %d", prober.calls)
	}
}

func TestPrecheckProbeMovieNoCrossFileRetry(t *testing.T) {
	prober := &flakyProber{fails: 1, tags: map[string]string{"screen_size": "2160p"}}
	p := &Planner{prober: prober, probeMode: "prefer", mediaTagOrder: fullTagOrder()}

	// 电影：两个版本各自成桶，第一个失败即整部不动，不会拿第二个的结果去命名第一个。
	if p.precheckProbeGroup(groupKey{mediaKind: "movie"}, probeTestItems(2)) == nil {
		t.Fatal("电影任一文件探测失败应整部跳过")
	}
	if prober.calls != 1 {
		t.Fatalf("电影不应跨文件重试，实际调用 %d 次", prober.calls)
	}
}

// detailedFlakyProber 模拟带失败分类的真实探测服务。
type detailedFlakyProber struct {
	flakyProber
	reason string
}

func (d *detailedFlakyProber) ProbeBucketedWithReason(ctx context.Context, req mediaprobe.Request) (map[string]string, *mediaprobe.Failure) {
	tags, ok := d.flakyProber.ProbeBucketed(ctx, req)
	if ok && len(tags) > 0 {
		return tags, nil
	}
	return nil, &mediaprobe.Failure{Reason: d.reason, Detail: "test detail"}
}

func TestPrecheckProbeReportsFailureReason(t *testing.T) {
	prober := &detailedFlakyProber{flakyProber: flakyProber{fails: 99}, reason: mediaprobe.ReasonContainerInvalid}
	p := &Planner{prober: prober, probeMode: "prefer", mediaTagOrder: fullTagOrder()}

	failure := p.precheckProbeGroup(groupKey{}, probeTestItems(3))
	if failure == nil {
		t.Fatal("两次都失败时应返回失败原因")
	}
	if failure.Reason != mediaprobe.ReasonContainerInvalid {
		t.Fatalf("原因码应透传，实际 %s", failure.Reason)
	}
	if failure.Label() != "文件结构异常" {
		t.Fatalf("中文标签不符，实际 %s", failure.Label())
	}
}

func TestPrecheckProbeUnavailableDoesNotBlock(t *testing.T) {
	prober := &detailedFlakyProber{flakyProber: flakyProber{fails: 99}, reason: mediaprobe.ReasonProbeUnavailable}
	p := &Planner{prober: prober, probeMode: "prefer", mediaTagOrder: fullTagOrder()}

	if p.precheckProbeGroup(groupKey{}, probeTestItems(3)) != nil {
		t.Fatal("本机缺少 ffprobe 时不应把整部剧判成失败")
	}
}
