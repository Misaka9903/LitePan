package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func NormalizeMediaTagValue(key string, value any) any {
	if value == nil || value == "" {
		return nil
	}
	switch key {
	case "frame_rate":
		return normalizeFrameRate(value)
	case "video_codec":
		return normalizeVideoCodec(fmt.Sprint(value))
	case "audio_codec":
		switch v := value.(type) {
		case []string:
			if len(v) > 0 {
				return normalizeAudioCodec(v[0])
			}
		case []any:
			if len(v) > 0 {
				return normalizeAudioCodec(fmt.Sprint(v[0]))
			}
		}
		return normalizeAudioCodec(fmt.Sprint(value))
	case "audio_channels":
		return fmt.Sprint(value)
	case "hdr":
		return normalizeDynamicRange(fmt.Sprint(value))
	case "bit_depth":
		return normalizeBitDepth(fmt.Sprint(value))
	default:
		return fmt.Sprint(value)
	}
}

func normalizeFrameRate(value any) string {
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(raw), "fps") {
		return raw
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	rounded := int(f + 0.5)
	if absFloat(f-float64(rounded)) < 0.05 {
		return fmt.Sprintf("%dfps", rounded)
	}
	return fmt.Sprintf("%.2ffps", f)
}

func normalizeVideoCodec(v string) string {
	if mapped := classifyVideoCodecToken(v); mapped != "" {
		return mapped
	}
	if v != "" && isAlphaToken(v) {
		return strings.ToUpper(v)
	}
	return strings.TrimSpace(v)
}

// trailingVersionRe 匹配音频编码尾部的版本号（如 Atmos 7.1）。
var trailingVersionRe = regexp.MustCompile(`(?i)\d+\.\d+$`)

func normalizeAudioCodec(v string) string {
	raw := strings.TrimSpace(v)
	hasAtmos := strings.HasSuffix(strings.ToLower(raw), " atmos")
	if hasAtmos {
		raw = strings.TrimSpace(raw[:len(raw)-len(" atmos")])
	}
	v = trailingVersionRe.ReplaceAllString(raw, "")
	v = strings.ToLower(strings.TrimSpace(v))
	mapping := map[string]string{
		"dolby digital plus":     "DDP",
		"dolby digital":          "DD",
		"dolby truehd":           "TrueHD",
		"dts-hd master audio":    "DTS-HD MA",
		"dts-hd high resolution": "DTS-HD HRA",
		"dts-hd ma":              "DTS-HD MA",
		"dts-hd hra":             "DTS-HD HRA",
		"dts-hd":                 "DTS-HD",
		"dts-x":                  "DTS:X",
		"dts:x":                  "DTS:X",
		"dts":                    "DTS",
		"aac":                    "AAC",
		"mp3":                    "MP3",
		"flac":                   "FLAC",
		"pcm":                    "PCM",
		"opus":                   "Opus",
		"vorbis":                 "Vorbis",
		"wma":                    "WMA",
		"eac3":                   "DDP",
		"ac3":                    "DD",
		"truehd":                 "TrueHD",
		"ddp":                    "DDP",
		"dd+":                    "DDP",
		"dd":                     "DD",
		"dolby atmos":            "",
		"atmos":                  "",
	}
	out := ""
	if mapped, ok := mapping[v]; ok {
		out = mapped
	} else if v != "" {
		out = strings.ToUpper(v)
	}
	if out == "" {
		return ""
	}
	if hasAtmos {
		out += " Atmos"
	}
	return out
}

func isAlphaToken(v string) bool {
	for _, r := range v {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return v != ""
}

func absFloat(n float64) float64 {
	if n < 0 {
		return -n
	}
	return n
}

func MergeAlignedMediaTags(parsed ParsedMedia, defaults map[string]any) ParsedMedia {
	m := parsed.ToMap()
	for key, value := range defaults {
		found := false
		for _, field := range MediaTagFields {
			if field == key {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		current := NormalizeMediaTagValue(key, m[key])
		if current != nil && current != "" {
			continue
		}
		if value != nil && value != "" {
			m[key] = value
		}
	}
	return parsedFromMap(m)
}

func BuildMediaInfoTags(parsed ParsedMedia, tagOrder []string) string {
	if len(tagOrder) == 0 {
		return ""
	}
	m := parsed.ToMap()
	parts := make([]string, 0, len(tagOrder))
	for _, key := range tagOrder {
		v := NormalizeMediaTagValue(key, m[key])
		if v == nil || v == "" {
			continue
		}
		s := fmt.Sprint(v)
		if s == "" {
			continue
		}
		// 声道并入前一段音轨编码，写成 "AAC 2.0"；前面没有编码时独立成段。
		if key == "audio_channels" && len(parts) > 0 {
			// Atmos 放在声道之后：DDP 5.1 Atmos，与 TgtoDrive 的写法一致。
			last := parts[len(parts)-1]
			if strings.HasSuffix(last, " Atmos") {
				parts[len(parts)-1] = strings.TrimSuffix(last, " Atmos") + " " + s + " Atmos"
			} else {
				parts[len(parts)-1] = last + " " + s
			}
			continue
		}
		if !containsStr(parts, s) {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ".")
}

func containsStr(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func normalizeDynamicRange(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "dovi", "dolby vision", "dv":
		return "DoVi"
	case "hdr10+", "hdr10plus":
		return "HDR10+"
	case "hdr10", "hdr":
		return "HDR10"
	case "hlg":
		return "HLG"
	case "sdr":
		return "SDR"
	default:
		return strings.TrimSpace(v)
	}
}

func normalizeBitDepth(v string) string {
	raw := strings.ToLower(strings.TrimSpace(v))
	if raw == "" {
		return ""
	}
	for _, n := range []string{"8", "10", "12", "16"} {
		if strings.HasPrefix(raw, n) {
			return n + "-bit"
		}
	}
	return raw
}
