package mediaprobe

import "testing"

// 真实 ffmpeg 输出片段，取自 `ffmpeg -i` 的 stderr。
const (
	out4KDTSHD = `Input #0, matroska,webm, from 'http://127.0.0.1:41234/source/abc':
  Metadata:
    encoder         : libebml v1.4.4 + libmatroska v1.7.1
  Duration: 01:58:24.32, start: 0.000000, bitrate: 24571 kb/s
  Stream #0:0(eng): Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/smpte2084), 3840x1600 [SAR 1:1 DAR 12:5], 23.98 fps, 23.98 tbr, 1k tbn
  Stream #0:1(eng): Audio: dts (DTS-HD MA), 48000 Hz, 7.1, fltp, 2048 kb/s (default)
  Stream #0:2(chi): Audio: ac3, 48000 Hz, 5.1(side), fltp, 448 kb/s
  Stream #0:3(eng): Subtitle: subrip
`

	outCoverAAC = `Input #0, matroska,webm, from 'http://127.0.0.1:41234/source/def':
  Duration: 00:42:11.04, start: 0.000000, bitrate: 3102 kb/s
  Stream #0:0: Video: mjpeg (Baseline), yuvj420p(pc, bt470bg/unknown/unknown), 600x900 [SAR 1:1 DAR 2:3], 90k tbr, 90k tbn (attached pic)
  Stream #0:1: Video: h264 (High), yuv420p(tv, bt709), 1920x1080 [SAR 1:1 DAR 16:9], 25 fps, 25 tbr, 1k tbn
  Stream #0:2: Audio: aac (LC), 48000 Hz, stereo, fltp, 192 kb/s (default)
`

	outDefaultTrack = `  Stream #0:1: Audio: ac3, 48000 Hz, 5.1(side), fltp, 448 kb/s
  Stream #0:2(eng): Audio: eac3, 48000 Hz, 5.1(side), fltp, 768 kb/s (default)
`

	outSilent = `  Stream #0:0: Video: av1 (Main), yuv420p(tv, bt709), 3840x2160, 59.94 fps, 59.94 tbr
`

	outSD = `  Stream #0:0: Video: mpeg2video (Main), yuv420p, 720x480 [SAR 32:27 DAR 16:9], 29.97 fps, 29.97 tbr
  Stream #0:1: Audio: mp3, 48000 Hz, stereo, fltp, 192 kb/s
`
)

func TestParseOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Info
	}{
		{
			name: "4K HEVC + DTS-HD MA 音轨优先取默认",
			in:   out4KDTSHD,
			want: Info{ScreenSize: "2160p", HDR: "HDR10", BitDepth: "10-bit", FrameRate: "23.976", VideoCodec: "H.265", AudioCodec: "DTS-HD MA", AudioChannels: "7.1"},
		},
		{
			name: "封面流不能盖掉正片",
			in:   outCoverAAC,
			want: Info{ScreenSize: "1080p", HDR: "SDR", BitDepth: "8-bit", FrameRate: "25", VideoCodec: "H.264", AudioCodec: "AAC", AudioChannels: "2.0"},
		},
		{
			name: "默认音轨优先于首条音轨",
			in:   outDefaultTrack,
			want: Info{AudioCodec: "DDP", AudioChannels: "5.1"},
		},
		{
			name: "无音轨时音频标签留空",
			in:   outSilent,
			want: Info{ScreenSize: "2160p", HDR: "SDR", BitDepth: "8-bit", FrameRate: "59.94", VideoCodec: "AV1"},
		},
		{
			name: "DVD 分辨率与不认识的编码",
			in:   outSD,
			want: Info{ScreenSize: "480p", HDR: "SDR", BitDepth: "8-bit", FrameRate: "29.97", AudioCodec: "MP3", AudioChannels: "2.0"},
		},
		{
			name: "无流信息时为空",
			in:   `http://127.0.0.1/source/x: Invalid data found when processing input`,
			want: Info{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOutput(tc.in)
			if got != tc.want {
				t.Fatalf("ParseOutput() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseOutputEmpty(t *testing.T) {
	if !ParseOutput("").Empty() {
		t.Fatal("空输入应当被判定为无可用信息")
	}
	if got := ParseOutput(out4KDTSHD).Tags(); len(got) != 7 {
		t.Fatalf("Tags() 应返回 7 个字段，实际 %d: %v", len(got), got)
	}
}

func TestScreenSizeFromDimensions(t *testing.T) {
	cases := []struct {
		width, height int
		want          string
	}{
		{3840, 2160, "2160p"},
		{3840, 1600, "2160p"}, // 2.39:1 宽银幕，按高度会误判成 1080p
		{4096, 2160, "2160p"},
		{1920, 1080, "1080p"},
		{1920, 800, "1080p"},
		{1440, 1080, "1080p"}, // 4:3 的 1080p，宽度不足 1920
		{1280, 720, "720p"},
		{720, 480, "480p"},
		{720, 576, "480p"},
		{320, 240, ""},
	}
	for _, tc := range cases {
		if got := ScreenSizeFromDimensions(tc.width, tc.height); got != tc.want {
			t.Fatalf("%dx%d = %q, want %q", tc.width, tc.height, got, tc.want)
		}
	}
}

func TestNormalizeFrameRate(t *testing.T) {
	cases := map[string]string{
		"23.98":  "23.976",
		"23.976": "23.976",
		"24":     "24",
		"25":     "25",
		"29.97":  "29.97",
		"30":     "30",
		"50":     "50",
		"59.94":  "59.94",
		"60":     "60",
		"":       "",
		"abc":    "",
	}
	for in, want := range cases {
		if got := normalizeFrameRate(in); got != want {
			t.Fatalf("normalizeFrameRate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseChannels(t *testing.T) {
	cases := map[string]string{
		", 48000 Hz, 5.1(side), fltp, 768 kb/s": "5.1",
		", 48000 Hz, 7.1, fltp, 2048 kb/s":      "7.1",
		", 48000 Hz, stereo, fltp, 192 kb/s":    "2.0",
		", 48000 Hz, mono, fltp, 96 kb/s":       "1.0",
		", 48000 Hz, 2 channels, fltp":          "2.0",
		", 48000 Hz, 8 channels, fltp":          "7.1",
		", 48000 Hz, fltp, 96 kb/s":             "",
	}
	for in, want := range cases {
		if got := parseChannels(in); got != want {
			t.Fatalf("parseChannels(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDynamicRangeAndBitDepth(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantHDR  string
		wantBits string
	}{
		{
			name:     "HDR10 10bit",
			in:       "  Stream #0:0: Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/smpte2084), 3840x2160, 23.98 fps",
			wantHDR:  "HDR10",
			wantBits: "10-bit",
		},
		{
			name:     "HLG",
			in:       "  Stream #0:0: Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/arib-std-b67), 3840x2160, 25 fps",
			wantHDR:  "HLG",
			wantBits: "10-bit",
		},
		{
			name:     "HDR10+ side data",
			in:       "  Stream #0:0: Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/smpte2084), 3840x2160, 23.98 fps\n    Side data:\n      HDR Dynamic Metadata SMPTE2094-40",
			wantHDR:  "HDR10+",
			wantBits: "10-bit",
		},
		{
			name:     "DoVi P8",
			in:       "  Stream #0:0: Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/smpte2084), 3840x2160, 23.98 fps\n    Side data:\n      DOVI configuration record: version: 1.0, profile: 8",
			wantHDR:  "DoVi P8",
			wantBits: "10-bit",
		},
		{
			name:     "SDR 8bit",
			in:       "  Stream #0:0: Video: h264 (High), yuv420p(tv, bt709), 1920x1080, 25 fps",
			wantHDR:  "SDR",
			wantBits: "8-bit",
		},
		{
			name:     "12bit",
			in:       "  Stream #0:0: Video: hevc (Main 12), yuv420p12le(tv, bt2020nc/bt2020/smpte2084), 3840x2160, 23.98 fps",
			wantHDR:  "HDR10",
			wantBits: "12-bit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOutput(tc.in)
			if got.HDR != tc.wantHDR {
				t.Fatalf("HDR = %q, want %q", got.HDR, tc.wantHDR)
			}
			if got.BitDepth != tc.wantBits {
				t.Fatalf("BitDepth = %q, want %q", got.BitDepth, tc.wantBits)
			}
		})
	}
}
