package mediaprobe

import (
	"fmt"
	"path"
	"strings"
)

// Request 描述一次按桶探测的请求。
// 同一个桶内的文件共用一次探测结果，避免整季/整片逐文件读头部。
type Request struct {
	AccountID int64
	FileID    string
	FileName  string
	Size      int64
	// MediaKind 为 "movie" 或 "tv"；空值按 tv 处理（季 + 扩展名分桶）。
	MediaKind string
	// DirID 仅剧集使用：同一季通常在同一目录，天然隔离不同画质。
	DirID string
	// Title/Year 仅电影使用：合集目录里是不同影片，不能按目录分桶。
	Title string
	Year  int
	// Season 仅剧集使用。
	Season int
	Ext    string
	// Special 特典/SP/花絮标记，单独成桶，避免低分辨率花絮污染正片标签。
	Special string
	// HintScreen/HintVideo 是文件名里已声明的画质，用于把混装目录拆成多个桶。
	HintScreen string
	HintVideo  string
}

var sampleNameTokens = []string{"sample", "trailer", "预告", "花絮", "彩蛋", "特典"}

// IsSampleName 判断是否是样片/预告/花絮类文件，这类文件画质不代表正片，跳过探测。
func IsSampleName(name string) bool {
	lower := strings.ToLower(name)
	for _, token := range sampleNameTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// BucketKey 描述"哪些文件可以共用一次探测结果"。
// 文件名里已声明画质的文件会进入独立的桶，因此同一目录混装 1080p/2160p 时
// 两组各自探测一次，而不是整目录只探一个。
func BucketKey(r Request) string {
	ext := strings.ToLower(strings.TrimSpace(r.Ext))
	if ext == "" {
		ext = strings.ToLower(path.Ext(r.FileName))
	}

	var base string
	if strings.EqualFold(strings.TrimSpace(r.MediaKind), "movie") {
		// 电影没有「同季同规格」的前提：同一部作品可能存在 4K/1080p 等多个版本，
		// 文件名未声明画质时它们会落进同一个桶并互相覆盖结果，因此按文件独立分桶。
		base = fmt.Sprintf("movie|%d|%s|%d|%s|%s", r.AccountID, strings.TrimSpace(r.Title), r.Year, ext, strings.TrimSpace(r.FileID))
	} else {
		base = fmt.Sprintf("tv|%d|%s|%d|%s", r.AccountID, r.DirID, r.Season, ext)
	}

	if special := strings.TrimSpace(r.Special); special != "" {
		base += "|" + special
	}
	if hint := hintKey(r); hint != "" {
		base += "|" + hint
	}
	return base
}

func hintKey(r Request) string {
	screen := strings.ToLower(strings.TrimSpace(r.HintScreen))
	video := strings.ToLower(strings.TrimSpace(r.HintVideo))
	if screen == "" && video == "" {
		return ""
	}
	return screen + "/" + video
}
