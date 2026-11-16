package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"litepan/internal/mediaorganize/rules"
)

var mediaExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".ts": true, ".mov": true, ".wmv": true,
	".iso": true, ".m2ts": true, ".rmvb": true, ".flv": true, ".m4v": true, ".webm": true,
}

var required = []string{"screen_size", "video_codec", "audio_codec"}

type dirStat struct {
	path    string
	files   []string
	missing map[string]int
}

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var payload struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		panic(err)
	}
	var tree struct {
		TreeText string `json:"tree_text"`
	}
	if err := json.Unmarshal([]byte(payload.Result.Content[0].Text), &tree); err != nil {
		panic(err)
	}

	stack := []string{}
	dirs := map[string]*dirStat{}
	for _, line := range strings.Split(tree.TreeText, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		depth := strings.Count(line, "|")
		idx := strings.LastIndex(line, "|-")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[idx+2:])
		if name == "" {
			continue
		}
		if depth > len(stack) {
			stack = append(stack, name)
		} else {
			if depth-1 >= 0 && depth-1 < len(stack) {
				stack = append(stack[:depth-1], name)
			}
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !mediaExts[ext] {
			continue
		}
		dirPath := strings.Join(stack[:len(stack)-1], "/")
		d, ok := dirs[dirPath]
		if !ok {
			d = &dirStat{path: dirPath, missing: map[string]int{}}
			dirs[dirPath] = d
		}
		d.files = append(d.files, name)
	}

	partial, allMissing, clean := 0, 0, 0
	var examples []string
	for _, d := range dirs {
		if len(d.files) < 2 {
			continue
		}
		hasAll, hasPartial := 0, 0
		for _, k := range required {
			miss := 0
			for _, f := range d.files {
				tags := map[string]any{}
				rules.EnrichMediaTagsFromFilename(f, tags)
				v := strings.TrimSpace(fmt.Sprint(tags[k]))
				if v == "" || v == "<nil>" {
					miss++
				}
			}
			if miss == 0 {
				continue
			}
			if miss == len(d.files) {
				hasAll++
			} else {
				hasPartial++
				if len(examples) < 12 {
					examples = append(examples, fmt.Sprintf("%s | %s 缺 %d/%d 集", d.path, k, miss, len(d.files)))
				}
			}
		}
		switch {
		case hasAll > 0:
			allMissing++
		case hasPartial > 0:
			partial++
		default:
			clean++
		}
	}
	sort.Strings(examples)
	fmt.Printf("有 ≥2 个媒体文件的目录：%d\n", len(dirs))
	fmt.Printf("整组缺某项：%d\n部分文件缺某项：%d\n全齐：%d\n", allMissing, partial, clean)
	fmt.Println("部分缺的例子：")
	for _, e := range examples {
		fmt.Println("  ", e)
	}
}
