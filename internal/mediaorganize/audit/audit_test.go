package audit

import (
	"bytes"
	"strings"
	"testing"
)

func dir(name string, id string, kids ...Node) Node {
	return Node{ID: id, Name: name, IsDir: true, Children: kids}
}

func file(name string, size int64) Node {
	return Node{Name: name, Size: size}
}

func find(t *testing.T, rep Report, name string) Group {
	t.Helper()
	for _, g := range rep.Groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("报告里没有分组 %q", name)
	return Group{}
}

func TestRunMissingKeys(t *testing.T) {
	full := []string{"screen_size", "video_codec", "audio_codec"}
	root := dir("韩剧", "root",
		dir("剧齐全 (2024)", "a",
			dir("Season 01", "a1",
				file("剧齐全 (2024) S01E01 [2160p H.265 DDP 5.1].mkv", 100),
				file("剧齐全 (2024) S01E02 [2160p H.265 DDP 5.1].mkv", 100),
			),
		),
		dir("剧缺视频编码 (2019)", "b",
			dir("Season 01", "b1",
				file("剧缺视频编码 (2019) S01E01 [2160p AAC].mkv", 200),
			),
		),
		dir("剧缺音频编码 (2022)", "c",
			dir("Season 01", "c1",
				file("剧缺音频编码 (2022) S01E01 [2160p H.265].mkv", 300),
			),
		),
		dir("剧缺两项 (2026)", "d",
			dir("Season 01", "d1",
				file("剧缺两项 (2026) S01E01 [2160p].mp4", 400),
			),
		),
	)
	rep := Run(root, Options{Required: full})

	if rep.TotalGroups != 4 {
		t.Fatalf("分组数 = %d, 期望 4", rep.TotalGroups)
	}
	if rep.BadGroups != 3 {
		t.Fatalf("不合格分组 = %d, 期望 3", rep.BadGroups)
	}
	if rep.BadFiles != 3 {
		t.Fatalf("不合格文件数 = %d, 期望 3", rep.BadFiles)
	}
	if rep.BadBytes != 900 {
		t.Fatalf("不合格体积 = %d, 期望 900", rep.BadBytes)
	}

	if g := find(t, rep, "剧齐全 (2024)"); !g.OK() {
		t.Fatalf("齐全的剧被判定为缺 %v", g.Missing)
	}
	if g := find(t, rep, "剧缺视频编码 (2019)"); strings.Join(g.Missing, ",") != "video_codec" {
		t.Fatalf("缺项 = %v, 期望 video_codec", g.Missing)
	}
	if g := find(t, rep, "剧缺音频编码 (2022)"); strings.Join(g.Missing, ",") != "audio_codec" {
		t.Fatalf("缺项 = %v, 期望 audio_codec", g.Missing)
	}
	if g := find(t, rep, "剧缺两项 (2026)"); strings.Join(g.Missing, ",") != "audio_codec,video_codec" {
		t.Fatalf("缺项 = %v, 期望 audio_codec+video_codec", g.Missing)
	}
}

func TestRunShowRollupAnySeasonBad(t *testing.T) {
	root := dir("韩剧", "root",
		dir("两季剧 (2017)", "s",
			dir("Season 01", "s1",
				file("两季剧 (2017) S01E01 [1080p H.264 AAC].mkv", 10),
			),
			dir("Season 02", "s2",
				file("两季剧 (2017) S02E01 [1080p AAC].mkv", 20),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})
	if rep.BadGroups != 1 {
		t.Fatalf("不合格分组 = %d, 期望 1（只要一季不合格整部剧不合格）", rep.BadGroups)
	}
	g := find(t, rep, "两季剧 (2017)")
	if strings.Join(g.Missing, ",") != "video_codec" {
		t.Fatalf("缺项 = %v, 期望 video_codec", g.Missing)
	}
	if g.Files != 2 {
		t.Fatalf("整部剧文件数 = %d, 期望 2（整剧移动时按整组计）", g.Files)
	}
	if len(g.Buckets) != 2 {
		t.Fatalf("桶数 = %d, 期望 2", len(g.Buckets))
	}
}

func TestRunMixedDoesNotCountAsMissing(t *testing.T) {
	root := dir("韩剧", "root",
		dir("混扩展名 (2021)", "m",
			dir("Season 03", "m1",
				file("混扩展名 (2021) S03E01 [1080p H.264 AAC].mkv", 10),
				file("混扩展名 (2021) S03E02 [1080p H.264 AAC].mkv", 10),
				file("混扩展名 (2021) S03E16 [1080p H.264 AAC].mp4", 10),
			),
		),
		dir("签名不一 (2020)", "n",
			dir("Season 01", "n1",
				file("签名不一 (2020) S01E01 [2160p H.265 AAC].mkv", 10),
				file("签名不一 (2020) S01E02 [1080p H.265 AAC].mkv", 10),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})

	g := find(t, rep, "混扩展名 (2021)")
	if !g.OK() {
		t.Fatalf("同标签混扩展名不该判缺，实际缺 %v", g.Missing)
	}
	if g.Mixed {
		t.Fatalf("同标签混扩展名不该标混合")
	}

	g = find(t, rep, "签名不一 (2020)")
	if !g.OK() {
		t.Fatalf("标签都有值只是取值不同，不该判缺，实际缺 %v", g.Missing)
	}
	if !g.Mixed {
		t.Fatalf("分辨率不一致应标混合")
	}
}

func TestRunRootLevelFiles(t *testing.T) {
	root := dir("电影", "root",
		file("某电影 (2020) [2160p H.265 AAC].mkv", 10),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})
	if rep.TotalGroups != 1 {
		t.Fatalf("分组数 = %d, 期望 1", rep.TotalGroups)
	}
	g := rep.Groups[0]
	if g.Name != "电影" || g.Path != "" {
		t.Fatalf("根目录文件的归集名 = %q/%q, 期望 电影/空", g.Name, g.Path)
	}
	if !g.OK() {
		t.Fatalf("根目录文件不该判缺，实际缺 %v", g.Missing)
	}
}

func TestRunSeasonUnit(t *testing.T) {
	root := dir("韩剧", "root",
		dir("两季剧 (2017)", "s",
			dir("Season 01", "s1", file("两季剧 (2017) S01E01 [1080p H.264 AAC].mkv", 10)),
			dir("Season 02", "s2", file("两季剧 (2017) S02E01 [1080p AAC].mkv", 20)),
		),
	)
	rep := Run(root, Options{Required: []string{"video_codec"}, Unit: "season"})
	if rep.TotalGroups != 2 {
		t.Fatalf("按季归集分组数 = %d, 期望 2", rep.TotalGroups)
	}
	if rep.BadGroups != 1 {
		t.Fatalf("不合格季数 = %d, 期望 1", rep.BadGroups)
	}
}

func TestRunIgnoresNonMediaAndEmptyRequired(t *testing.T) {
	root := dir("韩剧", "root",
		dir("剧 (2020)", "a",
			dir("Season 01", "a1",
				file("剧 (2020) S01E01 [2160p H.265 AAC].mkv", 10),
				file("剧 (2020) S01E01.nfo", 1),
				file("poster.jpg", 1),
			),
		),
	)
	rep := Run(root, Options{})
	if rep.TotalGroups != 1 || rep.BadGroups != 0 {
		t.Fatalf("未指定必填项时应全部合格，实际 groups=%d bad=%d", rep.TotalGroups, rep.BadGroups)
	}
	if rep.TotalFiles != 1 {
		t.Fatalf("非媒体文件不该计入，实际 %d", rep.TotalFiles)
	}
}

func TestWriteCSV(t *testing.T) {
	root := dir("韩剧", "root",
		dir("剧 (2020)", "a",
			dir("Season 01", "a1", file("剧 (2020) S01E01 [2160p].mkv", 10)),
		),
	)
	rep := Run(root, Options{Required: []string{"video_codec"}})
	var buf bytes.Buffer
	if err := rep.WriteCSV(&buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"分组", "video_codec", "S01E01"} {
		if !strings.Contains(out, want) {
			t.Fatalf("CSV 缺少 %q:\n%s", want, out)
		}
	}
}
