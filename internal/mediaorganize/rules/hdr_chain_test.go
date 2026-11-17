package rules

import "testing"

func TestEnrichFilenameHDRAndBitDepth(t *testing.T) {
	cases := []struct {
		name    string
		wantHDR string
		wantBit string
	}{
		{"Movie.2024.2160p.DoVi.10bit.H.265.DDP5.1.mkv", "DoVi", "10-bit"},
		{"Movie.2024.2160p.HDR10.H.265.mkv", "HDR10", ""},
		{"Movie.2024.1080p.BluRay.x264.8bit.mkv", "", "8-bit"},
		{"Movie.2024.2160p.SDR.H.264.mkv", "SDR", ""},
	}
	for _, tc := range cases {
		m := map[string]any{}
		EnrichMediaTagsFromFilename(tc.name, m)
		if got := m["hdr"]; got != tc.wantHDR && !(got == nil && tc.wantHDR == "") {
			t.Fatalf("%s: hdr = %v, want %q", tc.name, got, tc.wantHDR)
		}
		got, _ := m["bit_depth"].(string)
		if got != tc.wantBit {
			t.Fatalf("%s: bit_depth = %v, want %q", tc.name, m["bit_depth"], tc.wantBit)
		}
	}
}

func TestMergeProbedTagsKeepsHDRAndBitDepth(t *testing.T) {
	probed := map[string]string{
		"screen_size": "2160p",
		"hdr":         "DoVi",
		"bit_depth":   "10-bit",
		"video_codec": "H.265",
	}
	got := MergeProbedTags(ParsedMedia{Title: "钢铁森林"}, probed)
	if got.HDR != "DoVi" || got.BitDepth != "10-bit" {
		t.Fatalf("探测值未写入结构体: HDR=%q BitDepth=%q", got.HDR, got.BitDepth)
	}
	if tag := BuildMediaInfoTags(got, DefaultMediaTagOrder); tag != "2160p.DoVi.H.265.10-bit" {
		t.Fatalf("标签串 = %q", tag)
	}
}
