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
	"screen_size": {}, "hdr": {}, "bit_depth": {}, "frame_rate": {},
	"video_codec": {}, "audio_codec": {}, "audio_channels": {},
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
	// CollectEpisodes 为 true 时顺带收集每季的集号（供剧集完整性比对用），不产生额外请求。
	CollectEpisodes bool
	// SkipTags 为 true 时不做媒体信息（分辨率/编码）判定。
	SkipTags bool
	// SkipMixedExt 为 true 时不做扩展名一致性判定。
	SkipMixedExt bool
}

// Bucket 是同一目录、同一扩展名的一组同源文件，一季通常是一个桶。
type Bucket struct {
	// DirID 是桶所在目录的 file id，后续移动要用。
	DirID   string `json:"dir_id"`
	DirPath string `json:"dir_path"`
	Ext     string `json:"ext"`
	Sample  string `json:"sample"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	// Missing 该桶里有文件缺的标签键（缺一集也算，特典不参与）。
	Missing []string `json:"missing"`
	// Total 参与标签判定的文件数（已排除特典）。
	Total int `json:"total"`
	// MissingFiles 每个缺失项缺了多少个文件，键与 Missing 一致。
	MissingFiles map[string]int `json:"missing_files"`
	// Mixed 桶内文件签名不一致（例如混了 2160p 与 1080p）。
	Mixed bool `json:"mixed"`
	// MixedExt 该目录里存在其它媒体扩展名（例如一季里混了 mkv 与 mp4）。
	MixedExt bool `json:"mixed_ext"`
}

// Group 是归集单位：一部剧（Unit=show）或一季目录（Unit=season）。
type Group struct {
	// ID 是归集单位的目录 file id（Unit=show 时为根目录下一层的目录）。
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Missing []string `json:"missing"`
	// MissingTotal 参与标签判定的总集数；MissingFiles 汇总各季每个缺失项缺的集数。
	MissingTotal int            `json:"missing_total"`
	MissingFiles map[string]int `json:"missing_files"`
	Mixed        bool           `json:"mixed"`
	// MixedExt 组内任一季度目录存在扩展名不一致。
	MixedExt bool     `json:"mixed_ext"`
	Buckets  []Bucket `json:"seasons"`
	// Episodes 是「季号 → 集号」，仅在 Options.CollectEpisodes 时填充。
	Episodes map[int][]int `json:"episodes"`
	// UnparsedEpisodes 是媒体文件里解析不出集号的个数。
	UnparsedEpisodes int `json:"unparsed_episodes"`

	// 以下字段由服务层在拿到 TMDB 数据后填充（audit 包只承载结果，不发请求）。
	TMDBID    string `json:"tmdb_id"`
	MediaType string `json:"media_type"`
	// Match 为 matched / unmatched / failed / skipped。
	Match        string          `json:"match"`
	Completeness *ShowDiff       `json:"completeness"`
	Franchise    *CollectionDiff `json:"franchise"`
}

// Complete 报告该组是否包含 TMDB 比对结论。
func (g Group) Complete() bool { return g.Completeness != nil || g.Franchise != nil }

// OK 表示该组信息齐全，不需要搬动。
func (g Group) OK() bool { return len(g.Missing) == 0 && !g.MixedExt }

// Report 是一次体检的结果。
type Report struct {
	Groups      []Group `json:"groups"`
	TotalGroups int     `json:"total_groups"`
	BadGroups   int     `json:"bad_groups"`
	TotalFiles  int     `json:"total_files"`
	BadFiles    int     `json:"bad_files"`
	BadBytes    int64   `json:"bad_bytes"`
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
	// 扫描根自己带着 {tmdb-xxx} 时（用户直接选了某一部作品），整棵树都属于这一部作品。
	rootTagged := rules.FindTMDBIDInName(root.Name) != ""

	walk(root, "", func(path string, dir Node) {
		ids[path] = dir.ID
	}, func(path string, dir Node, files []Node) {
		buckets, mixedExt := judgeDir(path, dir.ID, files, required, exts, opts.SkipTags, opts.SkipMixedExt)
		seasonFallback := 0
		if n := rules.ParseSeasonDirNumber(dir.Name); n != nil {
			seasonFallback = *n
		}
		for _, b := range buckets {
			key, name, gpath := groupKey(unit, path, root.Name, rootTagged)
			g, ok := groups[key]
			if !ok {
				g = &Group{ID: ids[gpath], Name: name, Path: gpath}
				if opts.CollectEpisodes {
					g.Episodes = map[int][]int{}
				}
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
			g.MissingTotal += b.Total
			if len(b.MissingFiles) > 0 {
				if g.MissingFiles == nil {
					g.MissingFiles = map[string]int{}
				}
				for k, n := range b.MissingFiles {
					g.MissingFiles[k] += n
				}
			}
		}
		if opts.CollectEpisodes && len(buckets) > 0 {
			key, _, _ := groupKey(unit, path, root.Name, rootTagged)
			if g, ok := groups[key]; ok {
				collectEpisodes(g, files, exts, seasonFallback)
			}
		}
	})

	rep := Report{TotalGroups: len(order)}
	sort.Strings(order)
	csvRows := make([]Group, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Slice(g.Buckets, func(i, j int) bool { return g.Buckets[i].DirPath < g.Buckets[j].DirPath })
		for season, eps := range g.Episodes {
			g.Episodes[season] = uniqueInts(eps)
		}
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

// collectEpisodes 把目录里的媒体文件解析成「季号 → 集号」，特典与 Season 0 不计。
func collectEpisodes(g *Group, files []Node, exts map[string]struct{}, seasonFallback int) {
	if g.Episodes == nil {
		g.Episodes = map[int][]int{}
	}
	for _, f := range files {
		ext := strings.ToLower(extOf(f.Name))
		if _, ok := exts[ext]; !ok {
			continue
		}
		ep, ok := ParseEpisode(f.Name, seasonFallback)
		if !ok {
			g.UnparsedEpisodes++
			continue
		}
		g.Episodes[ep.Season] = append(g.Episodes[ep.Season], ep.Episode)
	}
}

// uniqueInts 排序并去重。
func uniqueInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	sort.Ints(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
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
func judgeDir(path, dirID string, files []Node, required []string, exts map[string]struct{},
	skipTags, skipMixedExt bool) ([]Bucket, bool) {
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
	mixedExt := len(byExt) > 1 && !skipMixedExt
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

		// 特典（SP/OVA/特典/花絮/NCOP…）不参与标签判定：
		// 它们本来就不带分辨率/编码，参与进来只会制造噪声。
		scored := make([]Node, 0, len(group))
		for _, f := range group {
			if rules.ExtractSpecialLabel(f.Name) != "" {
				continue
			}
			scored = append(scored, f)
		}
		if skipTags {
			scored = nil
		}
		b.Total = len(scored)

		tagMaps := make([]map[string]any, len(scored))
		for i, f := range scored {
			tagMaps[i] = parseTags(f.Name)
		}
		sigs := map[string]struct{}{}
		for _, tags := range tagMaps {
			sigs[signature(tags, required)] = struct{}{}
		}
		for _, k := range required {
			miss := 0
			for _, tags := range tagMaps {
				if !hasTag(tags, k) {
					miss++
				}
			}
			if miss == 0 {
				continue
			}
			// 缺一集就记：整理是逐文件拼标签的，个别集缺会直接体现成同一季命名不一致。
			b.Missing = append(b.Missing, k)
			if b.MissingFiles == nil {
				b.MissingFiles = map[string]int{}
			}
			b.MissingFiles[k] = miss
		}
		b.Mixed = len(sigs) > 1
		if len(b.Missing) > 0 {
			sort.Strings(b.Missing)
		}
		out = append(out, b)
	}
	return out, mixedExt
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
func groupKey(unit, path, rootName string, rootTagged bool) (key, name, gpath string) {
	path = strings.Trim(path, "/")
	if unit == "season" {
		if path == "" {
			return "\x00root", shortName(rootName), ""
		}
		return "d:" + path, path, path
	}
	if path == "" || rootTagged {
		// 根目录自己就是一部作品（用户直接选了某部剧/电影）时，整棵树归它。
		return "\x00root", shortName(rootName), ""
	}
	gp := showGroupPath(path)
	segs := strings.Split(gp, "/")
	return "s:" + gp, segs[len(segs)-1], gp
}

// showGroupPath 找出文件所属「作品目录」：
// 从最深层往上找第一个带 {tmdb-xxx} / {tmdbid-xxx} 标签的祖先目录；
// 都没有（未整理过的剧）就退回根目录下一层。
//
// 这样三种目录形态都能正确归组：
//   - 韩剧/剧名 {tmdb-1}/Season 01        → 剧名目录
//   - 动漫/鬼灭之刃（系列）/鬼灭之刃 {tmdbid-2}/Season 1 → 容器里的作品目录
//   - 动漫/进击的巨人 {tmdb-3}/最终季 完结篇 {tmdbid-4}/ → 剧场版目录（不并进剧集）
//
// shortName 取路径最后一段，用于扫描根自身的显示名。
func shortName(name string) string {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func showGroupPath(path string) string {
	segs := strings.Split(path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if rules.FindTMDBIDInName(segs[i]) != "" {
			return strings.Join(segs[:i+1], "/")
		}
	}
	return segs[0]
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
	header := []string{"分组", "分组路径", "所在目录", "扩展名", "样本文件", "文件数", "体积", "缺失", "缺失集数", "混合", "扩展名不一致",
		"TMDB", "匹配", "缺失季", "缺失集", "多余集", "缺续集"}
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
				missingDetailText(b),
				boolText(b.Mixed),
				boolText(b.MixedExt),
				tmdbText(g),
				matchText(g),
				missingSeasonsText(g),
				missingEpisodesText(g, b.DirPath),
				extraEpisodesText(g, b.DirPath),
				missingFranchiseText(g),
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

// seasonFromDirPath 从目录路径的最后一段取季号（"Season 02"/"S03"/"第二季"）。
func seasonFromDirPath(dirPath string) (int, bool) {
	segs := strings.Split(strings.Trim(dirPath, "/"), "/")
	if len(segs) == 0 {
		return 0, false
	}
	n := rules.ParseSeasonDirNumber(segs[len(segs)-1])
	if n == nil || *n <= 0 {
		return 0, false
	}
	return *n, true
}

// missingDetailText 输出「分辨率 8/24」这类明细，整数不算缺集数时只写项名。
func missingDetailText(b Bucket) string {
	if len(b.Missing) == 0 {
		return ""
	}
	parts := make([]string, 0, len(b.Missing))
	for _, k := range b.Missing {
		if n := b.MissingFiles[k]; n > 0 && b.Total > 0 {
			parts = append(parts, fmt.Sprintf("%s %d/%d", k, n, b.Total))
			continue
		}
		parts = append(parts, k)
	}
	return strings.Join(parts, "+")
}

func tmdbText(g Group) string {
	if g.TMDBID == "" {
		return ""
	}
	return "tmdb-" + g.TMDBID
}

func matchText(g Group) string {
	switch g.Match {
	case "matched":
		return "已匹配"
	case "unmatched":
		return "未匹配"
	case "failed":
		return "查询失败"
	}
	return ""
}

func missingSeasonsText(g Group) string {
	if g.Completeness == nil || len(g.Completeness.MissingSeasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(g.Completeness.MissingSeasons))
	for _, n := range g.Completeness.MissingSeasons {
		parts = append(parts, fmt.Sprintf("S%02d", n))
	}
	return strings.Join(parts, "+")
}

// missingEpisodesText 按桶所在目录挑出该季的缺集，方便一行一个桶地看。
func missingEpisodesText(g Group, dirPath string) string {
	if g.Completeness == nil || len(g.Completeness.Seasons) == 0 {
		return ""
	}
	season, ok := seasonFromDirPath(dirPath)
	if !ok {
		return ""
	}
	for _, s := range g.Completeness.Seasons {
		if s.Season != season {
			continue
		}
		if len(s.Missing) == 0 {
			return ""
		}
		return joinEpisodes(s.Missing, s.Unknown)
	}
	return ""
}

func extraEpisodesText(g Group, dirPath string) string {
	if g.Completeness == nil || len(g.Completeness.Seasons) == 0 {
		return ""
	}
	season, ok := seasonFromDirPath(dirPath)
	if !ok {
		return ""
	}
	for _, s := range g.Completeness.Seasons {
		if s.Season == season && len(s.Extra) > 0 {
			return joinEpisodes(s.Extra, false)
		}
	}
	return ""
}

func joinEpisodes(eps []int, unknown bool) string {
	parts := make([]string, 0, len(eps))
	for _, e := range eps {
		parts = append(parts, fmt.Sprintf("E%02d", e))
	}
	out := strings.Join(parts, "+")
	if unknown {
		out += "（疑似编号差异）"
	}
	return out
}

func missingFranchiseText(g Group) string {
	if g.Franchise == nil || len(g.Franchise.Missing) == 0 {
		return ""
	}
	parts := make([]string, 0, len(g.Franchise.Missing))
	for _, p := range g.Franchise.Missing {
		parts = append(parts, p.Title)
	}
	return strings.Join(parts, "、")
}
