package rules

import "testing"

// TestEnrichAudioCodecVariants 覆盖 DTS 系列与常见音频编码的解析。
// DTS-HD / DTS-HD MA / DTS:X 依赖连字符区分，归一化时不能把 "-" 换成空格。
func TestEnrichAudioCodecVariants(t *testing.T) {
	cases := []struct {
		name  string
		want  string
		chans string
	}{
		{"剧 (2016) S01E01 [1080p H.264 DTS-HD 2.0].mkv", "DTS-HD", "2.0"},
		{"剧 (2016) S01E01 [1080p H.264 DTS-HD MA 5.1].mkv", "DTS-HD MA", "5.1"},
		{"剧 (2016) S01E01 [1080p H.264 DTS-HD HRA 5.1].mkv", "DTS-HD HRA", "5.1"},
		{"剧 (2016) S01E01 [1080p H.264 DTS:X 7.1].mkv", "DTS:X", "7.1"},
		{"剧 (2016) S01E01 [1080p H.264 DTS 5.1].mkv", "DTS", "5.1"},
		{"剧 (2016) S01E01 [2160p H.265 DDP 5.1].mkv", "DDP", "5.1"},
		{"剧 (2016) S01E01 [1080p H.264 AAC 2.0].mkv", "AAC", "2.0"},
		{"剧 (2016) S01E01 [1080p H.264 TrueHD 7.1].mkv", "TrueHD", "7.1"},
		{"剧 (2016) S01E01 [1080p H.264 FLAC 2.0].mkv", "FLAC", "2.0"},
	}
	for _, c := range cases {
		m := map[string]any{}
		EnrichMediaTagsFromFilename(c.name, m)
		if got := m["audio_codec"]; got != c.want {
			t.Errorf("%s: audio_codec = %v, 期望 %s", c.name, got, c.want)
		}
		if got := m["audio_channels"]; got != c.chans {
			t.Errorf("%s: audio_channels = %v, 期望 %s", c.name, got, c.chans)
		}
	}
}
