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
	if len(g.Missing) != 0 {
		t.Fatalf("标签齐全时不该记缺失，实际缺 %v", g.Missing)
	}
	if !g.MixedExt {
		t.Fatalf("同季混扩展名必须标 MixedExt（整理会被 skip_mixed_ext 跳过）")
	}
	if g.Mixed {
		t.Fatalf("标签一致，不该标签名混合")
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

func TestRunFlagsMixedExtension(t *testing.T) {
	root := dir("韩剧", "root",
		dir("混扩展名 (2021)", "m",
			dir("Season 03", "m1",
				file("混扩展名 (2021) S03E01 [1080p H.264 AAC].mkv", 10),
				file("混扩展名 (2021) S03E16 [1080p H.264 AAC].mp4", 10),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})
	if rep.BadGroups != 1 {
		t.Fatalf("同季混扩展名应判不合格, bad=%d", rep.BadGroups)
	}
	g := rep.Groups[0]
	if !g.MixedExt {
		t.Fatalf("MixedExt 应为 true")
	}
	if len(g.Missing) != 0 {
		t.Fatalf("标签是齐的, 不该记缺失: %v", g.Missing)
	}
	if g.OK() {
		t.Fatalf("混扩展名的组不该 OK")
	}
}

func TestRunUniformExtensionNotFlagged(t *testing.T) {
	root := dir("韩剧", "root",
		dir("单扩展名 (2021)", "m",
			dir("Season 01", "m1",
				file("单扩展名 (2021) S01E01 [1080p H.264 AAC].mkv", 10),
				file("单扩展名 (2021) S01E02 [1080p H.264 AAC].mkv", 10),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"video_codec"}})
	if rep.BadGroups != 0 || rep.Groups[0].MixedExt {
		t.Fatalf("单一扩展名不该被判混用")
	}
}

// 缺一集就记：12 集里 1 集没有分辨率，也要报出来（整理是逐文件拼标签的）。
func TestRunPartialMissingCounted(t *testing.T) {
	root := dir("韩剧", "root",
		dir("部分缺 (2024) {tmdb-1}", "p",
			dir("Season 01", "p1",
				file("剧 (2024) S01E01 [2160p H.265 AAC].mkv", 10),
				file("剧 (2024) S01E02 [2160p H.265 AAC].mkv", 10),
				file("剧 (2024) S01E03 [H.265 AAC].mkv", 10),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})
	g := find(t, rep, "部分缺 (2024) {tmdb-1}")
	if strings.Join(g.Missing, ",") != "screen_size" {
		t.Fatalf("缺项 = %v, 期望 screen_size", g.Missing)
	}
	if g.MissingFiles["screen_size"] != 1 || g.MissingTotal != 3 {
		t.Fatalf("缺失集数 = %v / 总 %d, 期望 1 / 3", g.MissingFiles, g.MissingTotal)
	}
	if rep.BadGroups != 1 {
		t.Fatalf("不合格分组 = %d, 期望 1", rep.BadGroups)
	}
}

// 特典（NCOP/SP/OVA）不带分辨率编码，不参与标签判定。
func TestRunSpecialsExcludedFromTags(t *testing.T) {
	root := dir("动漫", "root",
		dir("番剧 (2020) {tmdb-2}", "a",
			dir("Season 01", "a1",
				file("番剧 (2020) S01E01 [1080p H.264 AAC].mkv", 10),
				file("番剧 (2020) S01E02 [1080p H.264 AAC].mkv", 10),
				file("番剧 (2020) NCOP01.mkv", 10),
				file("番剧 (2020) SP01 特典.mkv", 10),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})
	g := find(t, rep, "番剧 (2020) {tmdb-2}")
	if len(g.Missing) != 0 {
		t.Fatalf("特典不该让整季被判缺: %v", g.Missing)
	}
	if g.MissingTotal != 2 {
		t.Fatalf("参与判定的集数 = %d, 期望 2（特典已排除）", g.MissingTotal)
	}
	if rep.BadGroups != 0 {
		t.Fatalf("不合格分组 = %d, 期望 0", rep.BadGroups)
	}
}

// 整季都缺时集数 = 总集数，前端据此显示「整季缺」而不是「x/y」。
func TestRunAllMissingCountsAsTotal(t *testing.T) {
	root := dir("韩剧", "root",
		dir("整季缺 (2023) {tmdb-3}", "m",
			dir("Season 01", "m1",
				file("剧 (2023) S01E01 [H.265].mkv", 10),
				file("剧 (2023) S01E02 [H.265].mkv", 10),
			),
		),
	)
	rep := Run(root, Options{Required: []string{"screen_size", "video_codec", "audio_codec"}})
	g := find(t, rep, "整季缺 (2023) {tmdb-3}")
	if g.MissingFiles["screen_size"] != 2 || g.MissingTotal != 2 {
		t.Fatalf("整季缺集数 = %d / 总 %d, 期望 2 / 2", g.MissingFiles["screen_size"], g.MissingTotal)
	}
}

// 关掉「媒体信息」时，标签缺失与扩展名混用都不再判定。
func TestRunSkipTagsAndMixedExt(t *testing.T) {
	root := dir("韩剧", "root",
		dir("没标签的剧 (2020) {tmdb-9}", "s",
			dir("Season 01", "s1",
				file("剧 (2020) S01E01.mkv", 10),
				file("剧 (2020) S01E02.mp4", 10),
			),
		),
	)
	on := Run(root, Options{Required: []string{"screen_size", "video_codec"}})
	if on.BadGroups != 1 {
		t.Fatalf("开启时应判不合格，实际 %d", on.BadGroups)
	}
	off := Run(root, Options{Required: []string{"screen_size", "video_codec"}, SkipTags: true, SkipMixedExt: true})
	if off.BadGroups != 0 {
		t.Fatalf("全部跳过时不该判不合格: %+v", off.Groups)
	}
	if len(off.Groups[0].Missing) != 0 || off.Groups[0].MixedExt {
		t.Fatalf("跳过标记没生效: missing=%v mixedExt=%v", off.Groups[0].Missing, off.Groups[0].MixedExt)
	}
}
