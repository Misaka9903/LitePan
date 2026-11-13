package mediaprobe

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Info 是一次探测得到的媒体信息，字段与「媒体信息标签排序」使用的标签一一对应。
type Info struct {
	ScreenSize    string
	FrameRate     string
	VideoCodec    string
	AudioCodec    string
	AudioChannels string
}

// Tags 转成标签表，键与 media_tag_order 一致；探测不到的字段不出现在表中。
func (i Info) Tags() map[string]string {
	out := make(map[string]string, 5)
	put := func(key, value string) {
		if v := strings.TrimSpace(value); v != "" {
			out[key] = v
		}
	}
	put("screen_size", i.ScreenSize)
	put("frame_rate", i.FrameRate)
	put("video_codec", i.VideoCodec)
	put("audio_codec", i.AudioCodec)
	put("audio_channels", i.AudioChannels)
	return out
}

// Empty 表示没有任何可用字段。
func (i Info) Empty() bool { return len(i.Tags()) == 0 }

var (
	// ffmpeg 的流信息行：Stream #0:0: Video: hevc (Main 10), ...
	// 带语言标记时是 Stream #0:1(eng): Audio: eac3, ...
	streamLineRe   = regexp.MustCompile(`(?m)^\s*Stream #\d+:\d+(?:\([^)]*\))?: (Video|Audio): ([A-Za-z0-9_]+)(.*)$`)
	dimensionsRe   = regexp.MustCompile(`(\d{2,5})x(\d{2,5})`)
	fpsRe          = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*fps`)
	channelFieldRe = regexp.MustCompile(`^(\d\.\d)(?:\((?:side|back|front|wide)\))?$`)
	channelsNumRe  = regexp.MustCompile(`^(\d+) channels$`)
)

type audioCandidate struct {
	codec     string
	rest      string
	isDefault bool
}

// ParseOutput 解析 ffmpeg -i 的 stderr，取出分辨率、编码、声道与帧率。
// 解析失败时返回零值字段，调用方按「探测不到」处理。
func ParseOutput(stderr string) Info {
	var out Info
	bestArea := -1
	audios := make([]audioCandidate, 0, 2)

	for _, m := range streamLineRe.FindAllStringSubmatch(stderr, -1) {
		codec, rest := m[2], m[3]
		switch m[1] {
		case "Video":
			// MKV 常把封面当视频流（mjpeg/png），按面积取最大的一路，避免封面盖掉正片。
			if isImageCodec(codec) {
				continue
			}
			width, height, ok := parseDimensions(rest)
			if !ok {
				continue
			}
			if area := width * height; area > bestArea {
				bestArea = area
				out.ScreenSize = ScreenSizeFromDimensions(width, height)
				out.VideoCodec = normalizeVideoCodec(codec)
				out.FrameRate = parseFrameRate(rest)
			}
		case "Audio":
			audios = append(audios, audioCandidate{
				codec:     codec,
				rest:      rest,
				isDefault: strings.Contains(rest, "(default)"),
			})
		}
	}

	if picked := pickAudio(audios); picked != nil {
		out.AudioCodec = normalizeAudioCodec(picked.codec, picked.rest)
		out.AudioChannels = parseChannels(picked.rest)
	}
	return out
}

// pickAudio 优先取 ffmpeg 标记为默认的音轨，否则取第一条。
func pickAudio(candidates []audioCandidate) *audioCandidate {
	for i := range candidates {
		if candidates[i].isDefault {
			return &candidates[i]
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return &candidates[0]
}

func isImageCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "mjpeg", "png", "bmp", "gif", "webp", "tiff":
		return true
	default:
		return false
	}
}

func parseDimensions(rest string) (int, int, bool) {
	m := dimensionsRe.FindStringSubmatch(rest)
	if len(m) < 3 {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(m[1])
	height, errH := strconv.Atoi(m[2])
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// ScreenSizeFromDimensions 按宽高推导分辨率标签。
// 电影多在容器里是 3840x1600 这类宽银幕比例，所以宽高各算一档后取更高的一档，
// 不能只看高度，否则 2.39:1 的 4K 会被判成 1080p。
func ScreenSizeFromDimensions(width, height int) string {
	byWidth := screenSizeByWidth(width)
	byHeight := screenSizeByHeight(height)
	if screenSizeRank(byWidth) >= screenSizeRank(byHeight) {
		return byWidth
	}
	return byHeight
}

func screenSizeByWidth(width int) string {
	switch {
	case width >= 3800:
		return "2160p"
	case width >= 1900:
		return "1080p"
	case width >= 1200:
		return "720p"
	case width >= 700:
		return "480p"
	default:
		return ""
	}
}

func screenSizeByHeight(height int) string {
	switch {
	case height >= 2100:
		return "2160p"
	case height >= 1000:
		return "1080p"
	case height >= 680:
		return "720p"
	case height >= 400:
		return "480p"
	default:
		return ""
	}
}

func screenSizeRank(size string) int {
	switch size {
	case "2160p":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	default:
		return 0
	}
}

func normalizeVideoCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "h264", "avc":
		return "H.264"
	case "hevc", "h265":
		return "H.265"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	default:
		return ""
	}
}

func normalizeAudioCodec(codec, rest string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "aac":
		return "AAC"
	case "ac3":
		return "DD"
	case "eac3":
		return "DDP"
	case "truehd":
		return "TrueHD"
	case "dts":
		// ffmpeg 把 DTS 的具体档次写在括号里：dts (DTS-HD MA)
		switch {
		case strings.Contains(rest, "DTS-HD MA"):
			return "DTS-HD MA"
		case strings.Contains(rest, "DTS-HD HRA"):
			return "DTS-HD HRA"
		case strings.Contains(rest, "DTS:X"):
			return "DTS:X"
		default:
			return "DTS"
		}
	case "flac":
		return "FLAC"
	case "opus":
		return "Opus"
	case "mp3":
		return "MP3"
	case "alac":
		return "ALAC"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(codec)), "pcm_") {
			return "PCM"
		}
		return ""
	}
}

// parseChannels 取声道布局。ffmpeg 输出形如 "48000 Hz, 5.1(side), fltp, 768 kb/s"，
// 现代版本常带 (side)/(back) 后缀，必须按字段归一。
func parseChannels(rest string) string {
	channelCount := ""
	for _, field := range strings.Split(rest, ",") {
		trimmed := strings.TrimSpace(field)
		switch trimmed {
		case "mono":
			return "1.0"
		case "stereo":
			return "2.0"
		}
		if m := channelFieldRe.FindStringSubmatch(trimmed); len(m) >= 2 {
			return m[1]
		}
		if m := channelsNumRe.FindStringSubmatch(trimmed); len(m) >= 2 && channelCount == "" {
			channelCount = m[1]
		}
	}
	switch channelCount {
	case "1":
		return "1.0"
	case "2":
		return "2.0"
	case "6":
		return "5.1"
	case "8":
		return "7.1"
	default:
		return ""
	}
}

func parseFrameRate(rest string) string {
	m := fpsRe.FindStringSubmatch(rest)
	if len(m) < 2 {
		return ""
	}
	return normalizeFrameRate(m[1])
}

// normalizeFrameRate 统一帧率写法：23.98 → 23.976，29.97 → 29.97，25 → 25。
func normalizeFrameRate(raw string) string {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return ""
	}
	switch {
	case math.Abs(value-23.976) < 0.02:
		return "23.976"
	case math.Abs(value-29.97) < 0.02:
		return "29.97"
	case math.Abs(value-59.94) < 0.02:
		return "59.94"
	}
	text := strconv.FormatFloat(value, 'f', 3, 64)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	return text
}
