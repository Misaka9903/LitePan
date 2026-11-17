package mediaprobe

import "testing"

const jsonHDR10 = `{"streams":[
 {"codec_type":"video","codec_name":"hevc","profile":"Main 10","width":3840,"height":1600,"pix_fmt":"yuv420p10le","avg_frame_rate":"24000/1001","r_frame_rate":"24000/1001","color_transfer":"smpte2084","color_primaries":"bt2020","bits_per_raw_sample":"10"},
 {"codec_type":"audio","codec_name":"dts","profile":"DTS-HD MA","channels":8,"channel_layout":"7.1","disposition":{"default":1}},
 {"codec_type":"audio","codec_name":"ac3","channels":6,"channel_layout":"5.1(side)","disposition":{"default":0}}
]}`

const jsonDoVi = `{"streams":[
 {"codec_type":"video","codec_name":"hevc","width":3840,"height":2160,"pix_fmt":"yuv420p10le","avg_frame_rate":"24/1","color_transfer":"smpte2084",
  "side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":8},{"side_data_type":"Mastering display metadata"}]},
 {"codec_type":"audio","codec_name":"eac3","profile":"Dolby Digital Plus + Dolby Atmos","channels":6,"channel_layout":"5.1","disposition":{"default":1}}
]}`

const jsonHDR10Plus = `{"streams":[
 {"codec_type":"video","codec_name":"hevc","width":3840,"height":2160,"pix_fmt":"yuv420p10le","avg_frame_rate":"23.976","color_transfer":"smpte2084",
  "side_data_list":[{"side_data_type":"HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"}]}
]}`

const jsonSDR = `{"streams":[
 {"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","avg_frame_rate":"25/1","color_transfer":"bt709"},
 {"codec_type":"audio","codec_name":"aac","channels":2,"channel_layout":"stereo","disposition":{"default":1}}
]}`

func TestParseJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Info
	}{
		{
			name: "HDR10 10bit DTS-HD MA 默认轨",
			in:   jsonHDR10,
			want: Info{ScreenSize: "2160p", HDR: "HDR10", BitDepth: "10-bit", FrameRate: "23.976",
				VideoCodec: "H.265", AudioCodec: "DTS-HD MA", AudioChannels: "7.1"},
		},
		{
			name: "DoVi 优先于 HDR10",
			in:   jsonDoVi,
			want: Info{ScreenSize: "2160p", HDR: "DoVi P8", BitDepth: "10-bit", FrameRate: "24",
				VideoCodec: "H.265", AudioCodec: "DDP Atmos", AudioChannels: "5.1"},
		},
		{
			name: "HDR10+ side data",
			in:   jsonHDR10Plus,
			want: Info{ScreenSize: "2160p", HDR: "HDR10+", BitDepth: "10-bit", FrameRate: "23.976",
				VideoCodec: "H.265"},
		},
		{
			name: "SDR 8bit AAC",
			in:   jsonSDR,
			want: Info{ScreenSize: "1080p", HDR: "SDR", BitDepth: "8-bit", FrameRate: "25",
				VideoCodec: "H.264", AudioCodec: "AAC", AudioChannels: "2.0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJSON([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseJSON error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseJSON() = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseJSONInvalid(t *testing.T) {
	if _, err := ParseJSON([]byte("not json")); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}

func TestParseDynamicRangeDoViProfile(t *testing.T) {
	cases := map[string]string{
		"Stream #0:0: Video: hevc\n      DOVI configuration record: version: 1.0, profile: 5": "DoVi P5",
		"Stream #0:0: Video: hevc\n      DOVI configuration record: version: 1.0, profile: 7": "DoVi P7",
		"Stream #0:0: Video: hevc\n      DOVI configuration record: version: 1.0, profile: 8": "DoVi P8",
		"Stream #0:0: Video: hevc (dvhe.08.06)":                                               "DoVi",
	}
	for in, want := range cases {
		if got := parseDynamicRange(in); got != want {
			t.Fatalf("parseDynamicRange(%q) = %q, want %q", in, got, want)
		}
	}
}
