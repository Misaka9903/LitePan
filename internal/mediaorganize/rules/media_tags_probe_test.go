package rules

import "testing"

func TestMergeProbedTagsPrefer(t *testing.T) {
	base := ParsedMedia{Title: "Movie", ScreenSize: "2160p", VideoCodec: "H.264", Source: "BluRay"}
	got := MergeProbedTags(base, map[string]string{
		"screen_size": "1080p",
		"video_codec": "H.265",
	})

	if got.ScreenSize != "1080p" || got.VideoCodec != "H.265" {
		t.Fatalf("prefer 模式应以探测值为准，得到 %q / %q", got.ScreenSize, got.VideoCodec)
	}
	// 来源类信息容器里没有，必须保留文件名解析结果。
	if got.Source != "BluRay" {
		t.Fatalf("探测无法提供的字段应保留原值，得到 %q", got.Source)
	}
}

func TestMergeProbedTagsIgnoresUnknownAndEmpty(t *testing.T) {
	base := ParsedMedia{Title: "Movie", ScreenSize: "1080p"}
	got := MergeProbedTags(base, map[string]string{"title": "Other", "screen_size": "  "})
	if got.Title != "Movie" || got.ScreenSize != "1080p" {
		t.Fatalf("未知键与空值都不应生效，得到 %+v", got)
	}
}
