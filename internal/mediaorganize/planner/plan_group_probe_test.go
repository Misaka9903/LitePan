package planner

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/rules"
	"litepan/internal/mediaprobe"
)

type countingProber struct {
	calls int
	tags  map[string]string
}

func (c *countingProber) ProbeBucketed(_ context.Context, _ mediaprobe.Request) (map[string]string, bool) {
	c.calls++
	if c.tags == nil {
		return nil, false
	}
	return c.tags, true
}

func fullTagOrder() []string {
	return []string{"screen_size", "video_codec", "audio_codec", "audio_channels"}
}

func completeParsed() rules.ParsedMedia {
	return rules.ParsedMedia{
		Title:         "Some Show",
		ScreenSize:    "1080p",
		VideoCodec:    "H.264",
		AudioCodec:    "AAC",
		AudioChannels: "2.0",
	}
}

func TestMediaTagsComplete(t *testing.T) {
	p := &Planner{mediaTagOrder: fullTagOrder()}
	if !p.mediaTagsComplete(completeParsed()) {
		t.Fatal("四个标签齐全时应判定为完整")
	}

	partial := completeParsed()
	partial.AudioChannels = ""
	if p.mediaTagsComplete(partial) {
		t.Fatal("缺声道数时不应判定为完整")
	}

	// 用户把帧率从顺序里删掉，就不要求帧率。
	p = &Planner{mediaTagOrder: append(fullTagOrder(), "frame_rate")}
	if p.mediaTagsComplete(completeParsed()) {
		t.Fatal("要求帧率但文件名没有时应判定为不完整")
	}

	p = &Planner{mediaTagOrder: fullTagOrder()}
	if p.mediaTagsComplete(rules.ParsedMedia{Title: "only title"}) {
		t.Fatal("没有任何标签时不应判定为完整")
	}

	// 没有配置标签顺序就等于没有要求，不应把文件判成「不完整」而反复重排。
	empty := &Planner{}
	if !empty.mediaTagsComplete(completeParsed()) {
		t.Fatal("未配置标签顺序时应视为完整")
	}
}

func probeEntry() batchEntry {
	return batchEntry{item: domain.FileItem{ID: "f1", Name: "Some.Show.S01E01.mkv", Size: 1024}}
}

func TestApplyMediaProbeProbesWhenFillAndIncomplete(t *testing.T) {
	prober := &countingProber{tags: map[string]string{"screen_size": "2160p"}}
	p := &Planner{prober: prober, probeMode: "fill", mediaTagOrder: fullTagOrder()}

	incomplete := completeParsed()
	incomplete.ScreenSize = ""

	got := p.applyMediaProbe(incomplete, groupKey{mediaKind: "tv"}, probeEntry(), nil)
	if prober.calls != 1 {
		t.Fatalf("缺标签时应探测一次，实际调用 %d 次", prober.calls)
	}
	if got.ScreenSize != "2160p" {
		t.Fatalf("探测值应补上空缺，得到 %q", got.ScreenSize)
	}
}

func TestApplyMediaProbeAlwaysProbesInPrefer(t *testing.T) {
	prober := &countingProber{tags: map[string]string{"screen_size": "2160p"}}
	p := &Planner{prober: prober, probeMode: "prefer", mediaTagOrder: fullTagOrder()}

	got := p.applyMediaProbe(completeParsed(), groupKey{mediaKind: "tv"}, probeEntry(), nil)
	if prober.calls != 1 {
		t.Fatalf("prefer 模式应探测以纠正文件名，实际调用 %d 次", prober.calls)
	}
	if got.ScreenSize != "2160p" {
		t.Fatalf("prefer 模式应以探测值为准，得到 %q", got.ScreenSize)
	}
}

func TestApplyMediaProbeDisabled(t *testing.T) {
	prober := &countingProber{tags: map[string]string{"screen_size": "2160p"}}
	p := &Planner{prober: prober, probeMode: "off", mediaTagOrder: fullTagOrder()}
	if got := p.applyMediaProbe(rules.ParsedMedia{ScreenSize: "1080p"}, groupKey{}, probeEntry(), nil); got.ScreenSize != "1080p" {
		t.Fatalf("关闭时不应改动标签，得到 %q", got.ScreenSize)
	}
	if prober.calls != 0 {
		t.Fatalf("关闭时不应探测，实际调用 %d 次", prober.calls)
	}
}

func TestJoinProbedTagsFollowsOrder(t *testing.T) {
	tags := map[string]string{
		"screen_size": "1080p", "video_codec": "H.264",
		"audio_codec": "FLAC", "audio_channels": "2.0",
	}
	order := []string{"audio_codec", "screen_size", "video_codec", "audio_channels"}
	if got := joinProbedTags(tags, order); got != "FLAC 1080p H.264 2.0" {
		t.Fatalf("应按配置顺序拼接，得到 %q", got)
	}
	if got := joinProbedTags(map[string]string{}, order); got != "(空)" {
		t.Fatalf("无标签时应返回占位，得到 %q", got)
	}
	// 顺序里没有的标签不出现
	tags["frame_rate"] = "23.976"
	if got := joinProbedTags(tags, order); strings.Contains(got, "23.976") {
		t.Fatalf("未配置的标签不应出现，得到 %q", got)
	}
}

func TestProbeLogOnlyOncePerBucket(t *testing.T) {
	var lines []string
	prober := &countingProber{tags: map[string]string{"screen_size": "2160p"}}
	p := &Planner{
		prober: prober, probeMode: "fill", mediaTagOrder: fullTagOrder(),
		log: func(msg string) { lines = append(lines, msg) },
	}
	incomplete := completeParsed()
	incomplete.ScreenSize = ""

	p.applyMediaProbe(incomplete, groupKey{mediaKind: "tv"}, probeEntry(), nil)
	p.applyMediaProbe(incomplete, groupKey{mediaKind: "tv"}, probeEntry(), nil)

	// 缓存命中由 mediaprobe.Service 负责，这里的假探测器不自带缓存；
	// 本用例验证的是「同一个桶只记一条日志」。
	if len(lines) != 1 {
		t.Fatalf("同桶只应记一条日志，实际 %d 条: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "2160p") || !strings.Contains(lines[0], "S01E01") {
		t.Fatalf("日志应包含探测结果与文件名，得到 %q", lines[0])
	}
}
