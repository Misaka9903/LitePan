package rules

import (
	"strings"
	"testing"
)

func ip(v int) *int { return &v }

func TestBuildTargetFilenameDotSeparated(t *testing.T) {
	tv := ParsedMedia{Title: "德鲁纳酒店", Year: ip(2019), Season: ip(1), Episode: ip(16)}
	if got, want := BuildTargetFilename(tv, "off", "90447"), "德鲁纳酒店.2019.S01E16"; got != want {
		t.Fatalf("剧集命名 = %q, want %q", got, want)
	}

	movie := ParsedMedia{Title: "疯狂动物城", Year: ip(2016)}
	if got, want := BuildTargetFilename(movie, "off", ""), "疯狂动物城.2016"; got != want {
		t.Fatalf("电影命名 = %q, want %q", got, want)
	}

	withMarker := ParsedMedia{Title: "德鲁纳酒店", Year: ip(2019), Season: ip(1), Episode: ip(16)}
	if got, want := BuildTargetFilename(withMarker, "tmdb", "90447"), "德鲁纳酒店.2019.{tmdb-90447}.S01E16"; got != want {
		t.Fatalf("带 marker 命名 = %q, want %q", got, want)
	}
}

func TestBuildMediaInfoTagsDotJoined(t *testing.T) {
	parsed := ParsedMedia{
		ScreenSize:    "2160p",
		FrameRate:     "24fps",
		VideoCodec:    "H.265",
		AudioCodec:    "AAC",
		AudioChannels: "2.0",
	}
	got := BuildMediaInfoTags(parsed, []string{"screen_size", "frame_rate", "video_codec", "audio_codec", "audio_channels"})
	if want := "2160p.24fps.H.265.AAC 2.0"; got != want {
		t.Fatalf("标签串 = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "[]") {
		t.Fatalf("标签串不应再带方括号: %q", got)
	}
}

func TestLooksLikeOrganizedOnlyAcceptsDotLayout(t *testing.T) {
	cases := []string{
		"德鲁纳酒店.2019.S01E01.2160p.H.265.AAC 2.0.mkv",
		"疯狂动物城.2016.mkv",
	}
	for _, c := range cases {
		if !looksLikeOrganizedStructure(c) {
			t.Fatalf("应认定为已整理: %q", c)
		}
	}
	legacy := []string{
		"德鲁纳酒店 (2019) S01E01 [2160p H.265 AAC 2.0].mkv",
		"德鲁纳酒店 S01E01 无年份.mkv",
	}
	for _, c := range legacy {
		if looksLikeOrganizedStructure(c) {
			t.Fatalf("旧格式不应再算已整理: %q", c)
		}
	}
}

func TestFitFilenameBytesTrimsDotSegments(t *testing.T) {
	long := "德鲁纳酒店." + strings.Repeat("很长的标题段", 12) + ".2019.S01E01.2160p.H.265.AAC 2.0.mkv"
	got := FitFilenameBytes(long, "zh")
	if len([]byte(got)) > MaxFilenameBytes {
		t.Fatalf("截断后仍超长: %d", len([]byte(got)))
	}
	if !strings.HasSuffix(got, ".mkv") {
		t.Fatalf("截断后丢失扩展名: %q", got)
	}
}
