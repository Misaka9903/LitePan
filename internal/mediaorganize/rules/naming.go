package rules

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

func SanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "",
		"\\", "",
		":", "：",
		"*", "",
		"?", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "",
	)
	return strings.TrimSpace(replacer.Replace(name))
}

func IsSameGeneratedName(currentName, generatedName string) bool {
	return strings.TrimSpace(currentName) == strings.TrimSpace(generatedName)
}

func BuildFolderName(parsed ParsedMedia, tmdbID string) string {
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		return ""
	}
	tag := ""
	if tmdbID != "" {
		tag = fmt.Sprintf("{tmdb-%s}", tmdbID)
	}
	parts := []string{title}
	if parsed.Year != nil {
		parts = append(parts, fmt.Sprintf("(%d)", *parsed.Year))
	}
	if tag != "" {
		parts = append(parts, tag)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func BuildTargetFilename(parsed ParsedMedia, marker, tmdbID string) string {
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		return ""
	}
	parts := []string{title}
	if parsed.Year != nil {
		parts = append(parts, fmt.Sprintf("%d", *parsed.Year))
	}

	tag := ""
	if !IsMarkerOff(marker) {
		switch strings.TrimSpace(marker) {
		case "tmdb", "tmdbid":
			if tmdbID != "" {
				tag = fmt.Sprintf("{tmdb-%s}", tmdbID)
			}
		default:
			if m := strings.TrimSpace(marker); m != "" {
				tag = fmt.Sprintf("[%s]", m)
			}
		}
	}
	if tag != "" {
		parts = append(parts, tag)
	}

	season := asFirstInt(parsed.Season)
	episode := asFirstInt(parsed.Episode)
	if season != nil && episode != nil {
		parts = append(parts, fmt.Sprintf("S%02dE%02d", *season, *episode))
	}
	return strings.Join(parts, ".")
}

func BuildDisplayTitle(tmdbTitle, tmdbOriginal, fallbackTitle string) string {
	if tmdbTitle == "" {
		return fallbackTitle
	}
	if tmdbOriginal == "" || strings.EqualFold(tmdbOriginal, tmdbTitle) {
		return tmdbTitle
	}
	if isLatinScript(tmdbOriginal) {
		return tmdbTitle + " - " + tmdbOriginal
	}
	return tmdbTitle
}

func FitFilenameBytes(filename, tmdbLang string) string {
	if len([]byte(filename)) <= MaxFilenameBytes {
		return filename
	}
	preferZh := strings.HasPrefix(strings.ToLower(tmdbLang), "zh")
	// 双标题：中文名 - 原名.年份.其余（新格式）
	if m := dualTitleReV2.FindStringSubmatch(filename); m != nil {
		title1, title2, year, rest := m[1], m[2], m[3], m[4]
		short := title2 + "." + year + rest
		if preferZh {
			short = title1 + "." + year + rest
		}
		if len([]byte(short)) <= MaxFilenameBytes {
			return short
		}
		filename = short
	} else if m := dualTitleRe.FindStringSubmatch(filename); m != nil {
		// 旧格式：中文名 - 原名 (年份) 其余
		title1, title2, year, rest := m[1], m[2], m[3], m[4]
		short := title2 + " (" + year + ")" + rest
		if preferZh {
			short = title1 + " (" + year + ")" + rest
		}
		if len([]byte(short)) <= MaxFilenameBytes {
			return short
		}
		filename = short
	}
	// 方括号标签组逐项丢弃（启用 marker 时存在）
	if matches := bracketTagRe.FindAllStringSubmatchIndex(filename, -1); len(matches) > 0 {
		last := matches[len(matches)-1]
		tags := strings.Fields(filename[last[2]:last[3]])
		for i := len(tags); i > 0; i-- {
			newTag := "[" + strings.Join(tags[:i], " ") + "]"
			short := filename[:last[0]] + newTag + filename[last[1]:]
			if len([]byte(short)) <= MaxFilenameBytes {
				return short
			}
		}
		return filename[:last[0]] + filename[last[1]:]
	}
	// 新格式兜底：按点分段从尾部丢弃规格段，保留扩展名
	if dot := strings.LastIndex(filename, "."); dot > 0 {
		stem, ext := filename[:dot], filename[dot:]
		for {
			idx := strings.LastIndex(stem, ".")
			if idx <= 0 {
				return stem + ext
			}
			stem = stem[:idx]
			if len([]byte(stem+ext)) <= MaxFilenameBytes {
				return stem + ext
			}
		}
	}
	return filename
}

func IsAlreadyOrganized(filename, marker string) bool {
	if IsMarkerOff(marker) {
		return looksLikeOrganizedStructure(filename) || FindTMDBIDInName(filename) != ""
	}
	m := strings.TrimSpace(marker)
	switch m {
	case "tmdb", "tmdbid":
		return FindTMDBIDInName(filename) != ""
	default:
		return strings.Contains(filename, fmt.Sprintf("[%s]", m))
	}
}

func looksLikeOrganizedStructure(filename string) bool {
	name := strings.TrimSpace(filename)
	return organizedStructureRe.MatchString(name) || organizedStructureReV2.MatchString(name)
}

func IsMarkerOff(marker string) bool {
	switch strings.ToLower(strings.TrimSpace(marker)) {
	case "", "0", "off", "none", "no", "false":
		return true
	default:
		return false
	}
}

func isLatinScript(text string) bool {
	if text == "" {
		return false
	}
	hasLetter := false
	for _, ch := range text {
		if unicode.IsSpace(ch) || strings.ContainsRune(".,:;'\"-_!?()&+/", ch) {
			continue
		}
		if unicode.IsDigit(ch) {
			continue
		}
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= 0x00C0 && ch <= 0x024F) || (ch >= 0x1E00 && ch <= 0x1EFF) {
			hasLetter = true
			continue
		}
		return false
	}
	return hasLetter
}

var (
	dualTitleRe   = regexp.MustCompile(`^(.+?) - (.+?) \((\d{4})\)(.*)$`)
	dualTitleReV2 = regexp.MustCompile(`^(.+?) - (.+?)\.(\d{4})(.*)$`)
	bracketTagRe  = regexp.MustCompile(`\[([^\]]+)\]`)
)
