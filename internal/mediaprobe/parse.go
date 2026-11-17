package mediaprobe

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Info 是一次探测得到的媒体信息，字段与「媒体信息标签排序」使用的标签一一对应。
type Info struct {
	ScreenSize    string
	HDR           string
	BitDepth      string
	FrameRate     string
	VideoCodec    string
	AudioCodec    string
	AudioChannels string
}

// Tags 转成标签表，键与 media_tag_order 一致；探测不到的字段不出现在表中。
func (i Info) Tags() map[string]string {
	out := make(map[string]string, 7)
	put := func(key, value string) {
		if v := strings.TrimSpace(value); v != "" {
			out[key] = v
		}
	}
	put("screen_size", i.ScreenSize)
	put("hdr", i.HDR)
	put("bit_depth", i.BitDepth)
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
	bestRest := ""
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
				bestRest = rest
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
	if bestRest != "" {
		out.BitDepth = parseBitDepth(bestRest)
		out.HDR = parseDynamicRange(stderr)
	}
	return out
}

var pixFmtRe = regexp.MustCompile(`\b(yuvj?\d{3}p\d{0,2}(?:le|be)?|yuva\d{3}p\d{0,2}(?:le|be)?|gbrp\d{0,2}(?:le|be)?|p0\d{2}[a-z]*|p2\d{2}[a-z]*|p4\d{2}[a-z]*|nv12|nv16|nv21)\b`)

// parseBitDepth 从 pix_fmt 推断色深。ffmpeg -i 不会单独给位深，只能看像素格式后缀。
func parseBitDepth(rest string) string {
	m := pixFmtRe.FindStringSubmatch(rest)
	if len(m) < 2 {
		return ""
	}
	pf := strings.ToLower(m[1])
	switch {
	case strings.Contains(pf, "12le"), strings.Contains(pf, "12be"),
		strings.Contains(pf, "p012"), strings.Contains(pf, "p212"), strings.Contains(pf, "p412"):
		return "12-bit"
	case strings.Contains(pf, "10le"), strings.Contains(pf, "10be"),
		strings.Contains(pf, "p010"), strings.Contains(pf, "p210"), strings.Contains(pf, "p410"):
		return "10-bit"
	default:
		return "8-bit"
	}
}

// parseDynamicRange 判定动态范围：DoVi > HDR10+ > HLG > HDR10 > SDR。
// 无法从像素格式确认 HDR 的文件按 SDR 处理。
func parseDynamicRange(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "dovi configuration record"),
		strings.Contains(lower, "dvhe"),
		strings.Contains(lower, "dvh1"),
		strings.Contains(lower, "dvav"):
		if prof := doviProfileFromText(stderr); prof > 0 {
			return doviLabel(prof)
		}
		return "DoVi"
	case strings.Contains(lower, "smpte2094-40"), strings.Contains(lower, "hdr10+"):
		return "HDR10+"
	case strings.Contains(lower, "arib-std-b67"):
		return "HLG"
	case strings.Contains(lower, "smpte2084"):
		return "HDR10"
	default:
		return "SDR"
	}
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
	label := baseAudioCodec(codec, rest)
	if label != "" && strings.Contains(strings.ToLower(rest), "atmos") && !strings.Contains(label, "Atmos") {
		label += " Atmos"
	}
	return label
}

func baseAudioCodec(codec, rest string) string {
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

// ffprobeStream 是 ffprobe -show_streams 里单个流的关键字段。
type ffprobeStream struct {
	CodecType        string `json:"codec_type"`
	CodecName        string `json:"codec_name"`
	CodecLongName    string `json:"codec_long_name"`
	Profile          string `json:"profile"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	PixFmt           string `json:"pix_fmt"`
	AvgFrameRate     string `json:"avg_frame_rate"`
	RFrameRate       string `json:"r_frame_rate"`
	Channels         int    `json:"channels"`
	ChannelLayout    string `json:"channel_layout"`
	ColorTransfer    string `json:"color_transfer"`
	ColorPrimaries   string `json:"color_primaries"`
	BitsPerRawSample string `json:"bits_per_raw_sample"`
	SideDataList     []struct {
		SideDataType string `json:"side_data_type"`
		DvProfile    int    `json:"dv_profile"`
	} `json:"side_data_list"`
	Disposition struct {
		Default int `json:"default"`
	} `json:"disposition"`
}

// ParseJSON 解析 ffprobe -print_format json 的输出。
func ParseJSON(data []byte) (Info, error) {
	var doc struct {
		Streams []ffprobeStream `json:"streams"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Info{}, err
	}
	var out Info
	bestArea := -1
	var best *ffprobeStream
	var audioDefault, audioFirst *ffprobeStream
	for i := range doc.Streams {
		st := &doc.Streams[i]
		switch st.CodecType {
		case "video":
			if isImageCodec(st.CodecName) || st.Width <= 0 || st.Height <= 0 {
				continue
			}
			if area := st.Width * st.Height; area > bestArea {
				bestArea = area
				best = st
			}
		case "audio":
			if audioFirst == nil {
				audioFirst = st
			}
			if audioDefault == nil && st.Disposition.Default == 1 {
				audioDefault = st
			}
		}
	}
	if best != nil {
		out.ScreenSize = ScreenSizeFromDimensions(best.Width, best.Height)
		out.VideoCodec = normalizeVideoCodec(best.CodecName)
		out.FrameRate = parseFrameRateJSON(best.AvgFrameRate, best.RFrameRate)
		out.BitDepth = bitDepthFromStream(best)
		out.HDR = hdrFromStream(best)
	}
	if a := pickAudioStream(audioDefault, audioFirst); a != nil {
		out.AudioCodec = normalizeAudioCodec(a.CodecName, a.Profile+" "+a.CodecLongName)
		out.AudioChannels = channelsFromJSON(a.Channels, a.ChannelLayout)
	}
	return out, nil
}

func pickAudioStream(preferred, fallback *ffprobeStream) *ffprobeStream {
	if preferred != nil {
		return preferred
	}
	return fallback
}

// parseFrameRateJSON 把 "24000/1001" 这类分数帧率转成标签。
func parseFrameRateJSON(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || v == "0/0" {
			continue
		}
		if i := strings.Index(v, "/"); i > 0 {
			num, errN := strconv.ParseFloat(v[:i], 64)
			den, errD := strconv.ParseFloat(v[i+1:], 64)
			if errN == nil && errD == nil && den > 0 {
				if s := normalizeFrameRate(fmt.Sprintf("%.3f", num/den)); s != "" {
					return s
				}
			}
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			if s := normalizeFrameRate(fmt.Sprintf("%.3f", f)); s != "" {
				return s
			}
		}
	}
	return ""
}

// bitDepthFromStream 优先用 bits_per_raw_sample，缺失时按 pix_fmt 推断。
func bitDepthFromStream(st *ffprobeStream) string {
	if n, err := strconv.Atoi(strings.TrimSpace(st.BitsPerRawSample)); err == nil {
		switch n {
		case 8, 10, 12, 16:
			return fmt.Sprintf("%d-bit", n)
		}
	}
	return parseBitDepth(st.PixFmt)
}

// hdrFromStream 判定动态范围：DoVi > HDR10+ > HLG > HDR10 > SDR。
func hdrFromStream(st *ffprobeStream) string {
	for _, sd := range st.SideDataList {
		t := strings.ToLower(sd.SideDataType)
		switch {
		case strings.Contains(t, "dovi"), strings.Contains(t, "dolby vision"):
			if sd.DvProfile > 0 {
				return doviLabel(sd.DvProfile)
			}
			if prof := doviProfileFromText(sd.SideDataType); prof > 0 {
				return doviLabel(prof)
			}
			return "DoVi"
		case strings.Contains(t, "hdr10+"), strings.Contains(t, "smpte2094-40"):
			return "HDR10+"
		}
	}
	switch strings.ToLower(strings.TrimSpace(st.ColorTransfer)) {
	case "smpte2084", "smpte-st-2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	}
	return "SDR"
}

// channelsFromJSON 把声道数与布局转成 5.1 / 7.1 这类标签。
func channelsFromJSON(n int, layout string) string {
	switch n {
	case 1:
		return "1.0"
	case 2:
		return "2.0"
	case 3:
		return "2.1"
	case 4:
		return "4.0"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	l := strings.ToLower(layout)
	switch {
	case strings.Contains(l, "7.1"):
		return "7.1"
	case strings.Contains(l, "5.1"):
		return "5.1"
	case strings.Contains(l, "quad"):
		return "4.0"
	case strings.Contains(l, "stereo"):
		return "2.0"
	case strings.Contains(l, "mono"):
		return "1.0"
	}
	return ""
}

var doviProfileRe = regexp.MustCompile(`(?i)(?:profile|dv_profile)[:\s=]*(\d)`)

// doviProfileFromText 从 ffmpeg/ffprobe 的 DoVi 描述里取 profile 号。
func doviProfileFromText(text string) int {
	if m := doviProfileRe.FindStringSubmatch(text); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

// doviLabel 输出与 TgtoDrive 一致的写法：DoVi P5 / P7 / P8。
func doviLabel(profile int) string {
	if profile <= 0 {
		return "DoVi"
	}
	return fmt.Sprintf("DoVi P%d", profile)
}
