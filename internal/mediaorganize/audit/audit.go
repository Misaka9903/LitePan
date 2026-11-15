// Package audit 检查一棵目录树里的媒体文件名携带的信息是否齐全。
//
// 只读文件名，不读文件头、不访问网盘：输入是一棵已经拉下来的树，
// 输出是「哪些剧、哪些季、缺了哪几项」的报告。
package audit

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"

	"litepan/internal/mediaorganize/rules"
)

// defaultExts 与设置项 mo_file_extensions 的默认值保持一致。
const defaultExts = "mkv;mp4;avi;ts;mov;wmv;iso;m2ts;rmvb;flv;m4v;webm"

// validKeys 允许出现在 Required 里的媒体标签键，与媒体信息顺序保持一致。
var validKeys = map[string]struct{}{
	"screen_size": {}, "frame_rate": {}, "video_codec": {},
	"audio_codec": {}, "audio_channels": {},
}

// Node 是同构目录树的一个节点。
type Node struct {
	ID       string
	Name     string
	IsDir    bool
	Size     int64
	Children []Node
}

// Options 判定参数。
type Options struct {
	// Required 必填标签键，来自「媒体信息顺序」，例如
	// []string{"screen_size","video_codec","audio_codec"}。
	Required []string
	// Exts 参与判定的扩展名，分号分隔；空串用默认值。
	Exts string
	// Unit 归集粒度："show" 以根目录下一层为一组（默认），"season" 以含文件的目录各成一组。
	Unit string
}

// Bucket 是同一目录、同一扩展名的一组同源文件，一季通常是一个桶。
type Bucket struct {
	// DirID 是桶所在目录的 file id，后续移动要用。
	DirID   string
	DirPath string
	Ext     string
	Sample  string
	Files   int
	Bytes   int64
	// Missing 该桶中每个文件都缺的标签键（保守判定：只要有一个文件带就不算缺）。
	Missing []string
	// Mixed 桶内文件签名不一致（例如混了 2160p 与 1080p）。
	Mixed bool
	// MixedExt 该目录里存在其它媒体扩展名（例如一季里混了 mkv 与 mp4）。
	MixedExt bool
}

// Group 是归集单位：一部剧（Unit=show）或一季目录（Unit=season）。
type Group struct {
	// ID 是归集单位的目录 file id（Unit=show 时为根目录下一层的目录）。
	ID      string
	Name    string
	Path    string
	Files   int
	Bytes   int64
	Missing []string
	Mixed   bool
	// MixedExt 组内任一季度目录存在扩展名不一致。
	MixedExt bool
	Buckets  []Bucket
}

// OK 表示该组信息齐全，不需要搬动。
func (g Group) OK() bool { return len(g.Missing) == 0 && !g.MixedExt }

// Report 是一次体检的结果。
type Report struct {
	Groups      []Group
	TotalGroups int
	BadGroups   int
	TotalFiles  int
	BadFiles    int
	BadBytes    int64
}

// Run 对整棵树做体检。root 必须是扫描的根目录。
func Run(root Node, opts Options) Report {
	required := normalizeRequired(opts.Required)
	exts := parseExts(opts.Exts)
	unit := strings.ToLower(strings.TrimSpace(opts.Unit))
	if unit != "season" {
		unit = "show"
	}

	type acc struct {
		group Group
	}
	groups := map[string]*Group{}
	order := make([]string, 0, 16)
	ids := map[string]string{}

	walk(root, "", func(path string, dir Node) {
		ids[path] = dir.ID
	}, func(path string, dir Node, files []Node) {
		buckets, mixedExt := judgeDir(path, dir.ID, files, required, exts)
		for _, b := range buckets {
			key, name, gpath := groupKey(unit, path, root.Name)
			g, ok := groups[key]
			if !ok {
				g = &Group{ID: ids[gpath], Name: name, Path: gpath}
				groups[key] = g
				order = append(order, key)
			}
			g.Buckets = append(g.Buckets, b)
			g.Files += b.Files
			g.Bytes += b.Bytes
			if b.Mixed {
				g.Mixed = true
			}
			if mixedExt {
				b.MixedExt = true
				g.MixedExt = true
			}
			g.Missing = mergeKeys(g.Missing, b.Missing)
		}
	})

	rep := Report{TotalGroups: len(order)}
	sort.Strings(order)
	csvRows := make([]Group, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Slice(g.Buckets, func(i, j int) bool { return g.Buckets[i].DirPath < g.Buckets[j].DirPath })
		rep.TotalGroups = len(order)
		rep.TotalFiles += g.Files
		if !g.OK() {
			rep.BadGroups++
			rep.BadFiles += g.Files
			rep.BadBytes += g.Bytes
		}
		csvRows = append(csvRows, *g)
	}
	rep.Groups = csvRows
	return rep
}

// walk 深度优先遍历。
// onDir 对每个目录都会调用（用来建立 path→id 映射），
// fn 只对有文件的目录调用，给出该目录相对路径与文件节点。
func walk(n Node, prefix string, onDir func(path string, dir Node), fn func(path string, dir Node, files []Node)) {
	if n.IsDir && onDir != nil {
		onDir(prefix, n)
	}
	var subdirs []Node
	var files []Node
	for _, c := range n.Children {
		if c.IsDir {
			subdirs = append(subdirs, c)
			continue
		}
		files = append(files, c)
	}
	if n.IsDir && files != nil {
		fn(prefix, n, files)
	}
	for _, d := range subdirs {
		p := d.Name
		if prefix != "" {
			p = prefix + "/" + d.Name
		}
		walk(d, p, onDir, fn)
	}
}

// judgeDir 把一个目录里的文件按扩展名分桶并判定。
func judgeDir(path, dirID string, files []Node, required []string, exts map[string]struct{}) ([]Bucket, bool) {
	byExt := map[string][]Node{}
	for _, f := range files {
		ext := strings.ToLower(extOf(f.Name))
		if _, ok := exts[ext]; !ok {
			continue
		}
		byExt[ext] = append(byExt[ext], f)
	}
	if len(byExt) == 0 {
		return nil, false
	}
	mixedExt := len(byExt) > 1
	extNames := make([]string, 0, len(byExt))
	for e := range byExt {
		extNames = append(extNames, e)
	}
	sort.Strings(extNames)

	out := make([]Bucket, 0, len(extNames))
	for _, ext := range extNames {
		group := byExt[ext]
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		b := Bucket{DirID: dirID, DirPath: path, Ext: ext, Sample: group[0].Name, Files: len(group)}
		for _, f := range group {
			b.Bytes += f.Size
		}
		sigs := map[string]struct{}{}
		for _, f := range group {
			tags := parseTags(f.Name)
			sigs[signature(tags, required)] = struct{}{}
			for _, k := range required {
				if !hasTag(tags, k) {
					// 该文件缺这一项；只有全桶都缺才记入 Missing
					if !containsKey(b.Missing, k) && allMissing(group, k) {
						b.Missing = append(b.Missing, k)
					}
				}
			}
		}
		b.Mixed = len(sigs) > 1
		if len(b.Missing) > 0 {
			sort.Strings(b.Missing)
		}
		out = append(out, b)
	}
	return out, mixedExt
}

// allMissing 判断整个桶是否都缺某个标签。
func allMissing(files []Node, key string) bool {
	for _, f := range files {
		if hasTag(parseTags(f.Name), key) {
			return false
		}
	}
	return true
}

func parseTags(name string) map[string]any {
	m := map[string]any{}
	rules.EnrichMediaTagsFromFilename(name, m)
	return m
}

func hasTag(tags map[string]any, key string) bool {
	v, ok := tags[key]
	if !ok {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(v)) != ""
}

func signature(tags map[string]any, required []string) string {
	parts := make([]string, 0, len(required))
	for _, k := range required {
		if hasTag(tags, k) {
			parts = append(parts, fmt.Sprint(tags[k]))
			continue
		}
		parts = append(parts, "")
	}
	return strings.Join(parts, "|")
}

// groupKey 返回归集键、显示名和路径。
func groupKey(unit, path, rootName string) (key, name, gpath string) {
	path = strings.Trim(path, "/")
	if unit == "season" || path == "" {
		if path == "" {
			return "\x00root", rootName, ""
		}
		return "d:" + path, path, path
	}
	head := path
	if i := strings.Index(path, "/"); i >= 0 {
		head = path[:i]
	}
	return "s:" + head, head, head
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

func parseExts(raw string) map[string]struct{} {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = defaultExts
	}
	out := map[string]struct{}{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
		p := strings.ToLower(strings.TrimSpace(part))
		p = strings.TrimPrefix(p, ".")
		if p == "" {
			continue
		}
		out["."+p] = struct{}{}
	}
	return out
}

func normalizeRequired(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, k := range in {
		k = strings.TrimSpace(k)
		if _, ok := validKeys[k]; !ok {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func mergeKeys(dst, add []string) []string {
	for _, k := range add {
		if !containsKey(dst, k) {
			dst = append(dst, k)
		}
	}
	return dst
}

func containsKey(list []string, k string) bool {
	for _, v := range list {
		if v == k {
			return true
		}
	}
	return false
}

// WriteCSV 把报告写成 CSV，一行一个桶。
func (r Report) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	header := []string{"分组", "分组路径", "所在目录", "扩展名", "样本文件", "文件数", "体积", "缺失", "混合", "扩展名不一致"}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, g := range r.Groups {
		for _, b := range g.Buckets {
			row := []string{
				g.Name,
				g.Path,
				b.DirPath,
				b.Ext,
				b.Sample,
				fmt.Sprint(b.Files),
				fmt.Sprint(b.Bytes),
				strings.Join(b.Missing, "+"),
				boolText(b.Mixed),
				boolText(b.MixedExt),
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

func boolText(b bool) string {
	if b {
		return "是"
	}
	return ""
}
