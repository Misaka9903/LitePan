package mediaprobe

import "testing"

func tvRequest(file string) Request {
	return Request{
		AccountID: 7,
		FileID:    "f-" + file,
		FileName:  file,
		MediaKind: "tv",
		DirID:     "dir-s01",
		Title:     "Some Show",
		Season:    1,
		Ext:       ".mkv",
	}
}

func TestBucketKeyGroupsSameSeasonExt(t *testing.T) {
	first := tvRequest("Some.Show.S01E01.1080p.mkv")
	second := tvRequest("Some.Show.S01E02.1080p.mkv")
	first.HintScreen, first.HintVideo = "1080p", "h.264"
	second.HintScreen, second.HintVideo = "1080p", "h.264"
	if BucketKey(first) != BucketKey(second) {
		t.Fatalf("同季同扩展名应命中同一个桶: %q vs %q", BucketKey(first), BucketKey(second))
	}

	other := tvRequest("Some.Show.S02E01.1080p.mkv")
	other.Season = 2
	other.HintScreen, other.HintVideo = "1080p", "h.264"
	if BucketKey(first) == BucketKey(other) {
		t.Fatal("不同季不能共用一个桶：跨季画质经常不同")
	}

	mp4 := tvRequest("Some.Show.S01E03.1080p.mp4")
	mp4.Ext = ".mp4"
	mp4.HintScreen, mp4.HintVideo = "1080p", "h.264"
	if BucketKey(first) == BucketKey(mp4) {
		t.Fatal("不同扩展名不能共用一个桶")
	}
}

func TestBucketKeySplitsMixedQuality(t *testing.T) {
	hd := tvRequest("Some.Show.S01E01.1080p.mkv")
	hd.HintScreen = "1080p"
	uhd := tvRequest("Some.Show.S01E01.2160p.mkv")
	uhd.HintScreen = "2160p"
	if BucketKey(hd) == BucketKey(uhd) {
		t.Fatal("文件名已声明不同分辨率时必须拆桶，否则混装目录会被统一成一种画质")
	}
}

func TestBucketKeyMovieIgnoresDirectory(t *testing.T) {
	a := Request{AccountID: 7, FileName: "Movie.A.2019.1080p.mkv", MediaKind: "movie", DirID: "collection", Title: "Movie A", Year: 2019, Ext: ".mkv", HintScreen: "1080p"}
	b := Request{AccountID: 7, FileName: "Movie.B.2020.1080p.mkv", MediaKind: "movie", DirID: "collection", Title: "Movie B", Year: 2020, Ext: ".mkv"}
	if BucketKey(a) == BucketKey(b) {
		t.Fatal("合集目录里是不同影片，必须按片名分桶")
	}

	c := Request{AccountID: 7, FileName: "Movie.A.2019.mkv", MediaKind: "movie", DirID: "other", Title: "Movie A", Year: 2019, Ext: ".mkv"}
	if BucketKey(a) == BucketKey(c) {
		t.Fatal("已声明画质的文件应进独立桶")
	}
}

func TestBucketKeySpecialIsolated(t *testing.T) {
	main := tvRequest("Some.Show.S01E01.mkv")
	extra := tvRequest("Some.Show.S01E01.mkv")
	extra.Special = "特典"
	if BucketKey(main) == BucketKey(extra) {
		t.Fatal("特典应单独成桶：花絮常是低分辨率")
	}
}

func TestIsSampleName(t *testing.T) {
	skip := []string{"Movie.2020.sample.mkv", "Show.S01E01.trailer.mkv", "剧集-预告.mp4", "花絮01.mkv"}
	for _, name := range skip {
		if !IsSampleName(name) {
			t.Fatalf("%q 应被识别为样片/花絮", name)
		}
	}
	keep := []string{"Movie.2020.1080p.mkv", "Show.S01E01.mkv", "Some.Show.S01E01.SP.mkv"}
	for _, name := range keep {
		if IsSampleName(name) {
			t.Fatalf("%q 不应被跳过", name)
		}
	}
}

func TestBucketKeyMoviePerFile(t *testing.T) {
	a := Request{MediaKind: "movie", FileID: "f1", Title: "阿凡达", Year: 2009, Ext: ".mkv"}
	b := Request{MediaKind: "movie", FileID: "f2", Title: "阿凡达", Year: 2009, Ext: ".mkv"}
	if BucketKey(a) == BucketKey(b) {
		t.Fatal("同一部电影的不同文件不应共用探测结果，否则一个失败换另一个会覆盖规格")
	}
	if BucketKey(a) != BucketKey(Request{MediaKind: "movie", FileID: "f1", Title: "阿凡达", Year: 2009, Ext: ".mkv"}) {
		t.Fatal("同一文件重复请求应命中同一桶")
	}
}
