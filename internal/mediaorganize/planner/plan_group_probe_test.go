package planner

import (
	"context"
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

	empty := &Planner{}
	if empty.mediaTagsComplete(completeParsed()) {
		t.Fatal("标签顺序为空时不应判定为完整")
	}
}

func probeEntry() batchEntry {
	return batchEntry{item: domain.FileItem{ID: "f1", Name: "Some.Show.S01E01.mkv", Size: 1024}}
}

func TestApplyMediaProbeSkipsWhenFillAndComplete(t *testing.T) {
	prober := &countingProber{tags: map[string]string{"screen_size": "2160p"}}
	p := &Planner{prober: prober, probeMode: "fill", mediaTagOrder: fullTagOrder()}

	got := p.applyMediaProbe(completeParsed(), groupKey{mediaKind: "tv"}, probeEntry(), nil)
	if prober.calls != 0 {
		t.Fatalf("文件名已写全标签时不应探测，实际调用 %d 次", prober.calls)
	}
	if got.ScreenSize != "1080p" {
		t.Fatalf("跳过后应保持文件名结果，得到 %q", got.ScreenSize)
	}
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
