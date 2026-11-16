package mediaorganize

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/mediaorganize/audit"
	"litepan/internal/settings"
)

const (
	auditReportTTL = 24 * time.Hour
	auditReportMax = 20
	auditDirMax    = 50000
	auditFileMax   = 400000
	auditDepthMax  = 24
	// auditListParallel 是取树时每层并发列目录的上限：网盘有风控，别开太大。
	auditListParallel = 6
	auditUnitShow     = "show"
	auditUnitSeason   = "season"
)

// auditLister 是体检需要的文件列表能力，*file.Service 直接满足；抽成接口便于单测。
//
// 不用网盘的「一次拉全量文件」接口：115 的全量列表只返回文件不返回目录，
// 没有目录名就无法还原分组路径，所以统一走逐目录遍历。
type auditLister interface {
	List(ctx context.Context, accountID int64, parentID string, forceRefresh bool) ([]domain.FileItem, error)
}

// auditFullLister 是可选能力：一次拿到子树里的全部文件（115 的 cur=0 模式，只回文件不回目录）。
// 有它就能把「逐目录列名字」的请求从「目录数」压到「中间层目录数」。
type auditFullLister interface {
	SupportsFullList(ctx context.Context, accountID int64) (bool, error)
	ListAllFiles(ctx context.Context, accountID int64, parentID string) ([]driver.FullListEntry, error)
}

// AuditRequest 是一次体检的入参。
type AuditRequest struct {
	AccountID int64    `json:"account_id"`
	RootID    string   `json:"root_id"`
	RootName  string   `json:"root_name"`
	Required  []string `json:"required"`
	Unit      string   `json:"unit"`
	Exts      string   `json:"exts"`
	// CheckSeasons / CheckEpisodes / CheckFranchise 为 nil 时按开启处理（前端不传＝默认检查）。
	CheckSeasons   *bool `json:"check_seasons,omitempty"`
	CheckEpisodes  *bool `json:"check_episodes,omitempty"`
	CheckFranchise *bool `json:"check_franchise,omitempty"`
}

// AuditSummary 是体检的汇总数字。
type AuditSummary struct {
	TotalGroups int   `json:"total_groups"`
	BadGroups   int   `json:"bad_groups"`
	TotalFiles  int   `json:"total_files"`
	BadFiles    int   `json:"bad_files"`
	BadBytes    int64 `json:"bad_bytes"`
	ScannedDirs int   `json:"scanned_dirs"`
	// 以下为 TMDB 完整性比对结果（未开启时全为 0）。
	IncompleteGroups  int `json:"incomplete_groups"`
	MissingSeasons    int `json:"missing_seasons"`
	MissingEpisodes   int `json:"missing_episodes"`
	MissingFranchises int `json:"missing_franchises"`
	MissingMovies     int `json:"missing_movies"`
	UnmatchedGroups   int `json:"unmatched_groups"`
	TMDBRequests      int `json:"tmdb_requests"`
}

// AuditSeason 是报告里的一季（一个桶）。
type AuditSeason struct {
	DirID   string   `json:"dir_id"`
	DirPath string   `json:"dir_path"`
	Ext     string   `json:"ext"`
	Sample  string   `json:"sample"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Missing []string `json:"missing"`
	Mixed   bool     `json:"mixed"`
}

// AuditSeasonStatus 是一季的季集比对结果。
type AuditSeasonStatus struct {
	Season  int    `json:"season"`
	Local   int    `json:"local"`
	Aired   int    `json:"aired"`
	Total   int    `json:"total"`
	Missing []int  `json:"missing"`
	Extra   []int  `json:"extra"`
	Airing  bool   `json:"airing"`
	Unknown bool   `json:"unknown"`
	Note    string `json:"note,omitempty"`
}

// AuditCompleteness 是一部剧的季集完整性。
type AuditCompleteness struct {
	Seasons        []AuditSeasonStatus `json:"seasons"`
	MissingSeasons []int               `json:"missing_seasons"`
	ExtraSeasons   []int               `json:"extra_seasons"`
	LatestSeason   int                 `json:"latest_season"`
	Unreliable     bool                `json:"unreliable"`
	Unparsed       int                 `json:"unparsed"`
}

// AuditFranchisePart 是合集里的一部作品。
type AuditFranchisePart struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date,omitempty"`
	Runtime     int    `json:"runtime,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// AuditFranchise 是一个电影合集的完整性。
type AuditFranchise struct {
	Name     string               `json:"name"`
	Parts    int                  `json:"parts"`
	Local    []AuditFranchisePart `json:"local"`
	Missing  []AuditFranchisePart `json:"missing"`
	Optional []AuditFranchisePart `json:"optional"`
	Upcoming []AuditFranchisePart `json:"upcoming"`
}

// AuditGroup 是一部剧（Unit=show）或一季（Unit=season）。
type AuditGroup struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	OK       bool          `json:"ok"`
	Files    int           `json:"files"`
	Bytes    int64         `json:"bytes"`
	Missing  []string      `json:"missing"`
	Mixed    bool          `json:"mixed"`
	MixedExt bool          `json:"mixed_ext"`
	Seasons  []AuditSeason `json:"seasons"`

	// 以下由 TMDB 完整性比对填充。
	TMDBID       string             `json:"tmdb_id,omitempty"`
	MediaType    string             `json:"media_type,omitempty"`
	Match        string             `json:"match,omitempty"`
	Completeness *AuditCompleteness `json:"completeness,omitempty"`
	Franchise    *AuditFranchise    `json:"franchise,omitempty"`
}

// AuditReport 是一次体检的结果，进程内保存，供前端取用与导出 CSV。
type AuditReport struct {
	ID        string       `json:"id"`
	CreatedAt int64        `json:"created_at"`
	Request   AuditRequest `json:"request"`
	Summary   AuditSummary `json:"summary"`
	Groups    []AuditGroup `json:"groups"`

	raw audit.Report
}

// RunAudit 拉取目录树并做一次体检。
func (s *Service) RunAudit(ctx context.Context, req AuditRequest) (*AuditReport, error) {
	if s == nil || s.files == nil {
		return nil, domain.Errorf(domain.CodeInternal, "文件服务不可用")
	}
	if strings.TrimSpace(req.RootID) == "" {
		return nil, domain.Errorf(domain.CodeValidation, "root_id 不能为空")
	}
	unit := strings.ToLower(strings.TrimSpace(req.Unit))
	if unit != auditUnitSeason {
		unit = auditUnitShow
	}
	required := normalizeAuditRequired(req.Required)
	if len(required) == 0 {
		required = s.mediaTagOrderFromSettings()
	}
	if len(required) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "没有可判定的媒体信息项，请先在设置里配置媒体信息顺序")
	}

	checkSeasons := boolOrDefault(req.CheckSeasons, true)
	checkEpisodes := boolOrDefault(req.CheckEpisodes, true)
	checkFranchise := boolOrDefault(req.CheckFranchise, true)

	// 取树会打出上百个请求，走和整理任务同一套「API 额外补偿间隔」，
	// 避免体检绕过限速设置把网盘打出风控。
	scanCtx := s.withAPIDelay(ctx)
	// 缺季和缺集都要用本地季号，所以任一开启就收集（纯内存，不额外请求）。
	rep, dirs, err := runAudit(scanCtx, s.files, req, required, unit, checkSeasons || checkEpisodes)
	if err != nil {
		return nil, err
	}

	// TMDB 完整性比对：只打 TMDB，不增加网盘请求；没配 key 时整段跳过。
	tmdbCalls := 0
	opts := completenessOptions{Seasons: checkSeasons, Episodes: checkEpisodes, Franchise: checkFranchise}
	if opts.any() {
		if client, cerr := s.newTMDBClient(""); cerr == nil {
			tmdbCalls = s.applyCompleteness(ctx, &rep, client, s.tmdbInterval(), opts, time.Now())
		} else {
			checkSeasons, checkEpisodes, checkFranchise = false, false, false
			req.CheckSeasons, req.CheckEpisodes, req.CheckFranchise = &checkSeasons, &checkEpisodes, &checkFranchise
		}
	}
	report := &AuditReport{
		ID:        newAuditID(),
		CreatedAt: time.Now().Unix(),
		Request:   req,
		Summary: AuditSummary{
			TotalGroups:  rep.TotalGroups,
			BadGroups:    rep.BadGroups,
			TotalFiles:   rep.TotalFiles,
			BadFiles:     rep.BadFiles,
			BadBytes:     rep.BadBytes,
			ScannedDirs:  dirs,
			TMDBRequests: tmdbCalls,
		},
		raw: rep,
	}
	report.Request.Required = required
	report.Request.Unit = unit
	franchises := map[string]struct{}{}
	for _, g := range rep.Groups {
		ag := toAuditGroup(g)
		report.Groups = append(report.Groups, ag)
		if ag.Match == "unmatched" || ag.Match == "failed" {
			report.Summary.UnmatchedGroups++
		}
		if ag.Completeness != nil {
			if len(ag.Completeness.MissingSeasons) > 0 {
				report.Summary.MissingSeasons += len(ag.Completeness.MissingSeasons)
			}
			for _, sd := range ag.Completeness.Seasons {
				if !sd.Unknown {
					report.Summary.MissingEpisodes += len(sd.Missing)
				}
			}
		}
		if ag.Franchise != nil && len(ag.Franchise.Missing) > 0 {
			// 同一合集可能落在多个作品目录里，按合集名去重，避免重复计数。
			franchises[ag.Franchise.Name] = struct{}{}
			report.Summary.MissingMovies += len(ag.Franchise.Missing)
		}
		if auditGroupIncomplete(ag) {
			report.Summary.IncompleteGroups++
		}
	}
	report.Summary.MissingFranchises = len(franchises)
	s.storeAudit(report)
	return report, nil
}

// runAudit 拉树并判定，供 RunAudit 与单测共用。
func runAudit(ctx context.Context, lister auditLister, req AuditRequest, required []string, unit string,
	collectEpisodes bool) (audit.Report, int, error) {
	tree, dirs, err := buildAuditTree(ctx, lister, req.AccountID, req.RootID, req.RootName)
	if err != nil {
		return audit.Report{}, dirs, err
	}
	return audit.Run(tree, audit.Options{
		Required:        required,
		Unit:            unit,
		Exts:            req.Exts,
		CollectEpisodes: collectEpisodes,
	}), dirs, nil
}

// GetAudit 取一份缓存中的报告。
func (s *Service) GetAudit(id string) (*AuditReport, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rep, ok := s.auditReports[id]
	if !ok {
		return nil, false
	}
	if time.Since(time.Unix(rep.CreatedAt, 0)) > auditReportTTL {
		delete(s.auditReports, id)
		return nil, false
	}
	return rep, true
}

// AuditCSV 导出报告的 CSV（一行一个桶）。
func (s *Service) AuditCSV(id string) ([]byte, bool) {
	rep, ok := s.GetAudit(id)
	if !ok {
		return nil, false
	}
	var buf strings.Builder
	if err := rep.raw.WriteCSV(&buf); err != nil {
		return nil, false
	}
	return []byte(buf.String()), true
}

// ListAudits 返回最近的报告摘要，供前端列表用。
func (s *Service) ListAudits() []map[string]any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.auditOrder))
	for i := len(s.auditOrder) - 1; i >= 0; i-- {
		rep, ok := s.auditReports[s.auditOrder[i]]
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"id":         rep.ID,
			"created_at": rep.CreatedAt,
			"root_id":    rep.Request.RootID,
			"root_name":  rep.Request.RootName,
			"summary":    rep.Summary,
		})
	}
	return out
}

func (s *Service) storeAudit(rep *AuditReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditReports == nil {
		s.auditReports = map[string]*AuditReport{}
	}
	s.auditReports[rep.ID] = rep
	s.auditOrder = append(s.auditOrder, rep.ID)
	for len(s.auditOrder) > auditReportMax {
		old := s.auditOrder[0]
		s.auditOrder = s.auditOrder[1:]
		delete(s.auditReports, old)
	}
}

// mediaTagOrderFromSettings 从设置里读「媒体信息顺序」，解析失败返回 nil。
func (s *Service) mediaTagOrderFromSettings() []string {
	if s.settings == nil {
		return nil
	}
	parsed, err := parseMediaTagOrder(s.settings.String(settings.KeyMOMediaTagOrder))
	if err != nil {
		return nil
	}
	var items []string
	if err := json.Unmarshal(parsed, &items); err != nil {
		return nil
	}
	return normalizeAuditRequired(items)
}

func normalizeAuditRequired(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" {
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

func toAuditGroup(g audit.Group) AuditGroup {
	// 空切片统一给 []，避免前端拿到 null 后 .length 直接抛错。
	missing := g.Missing
	if missing == nil {
		missing = []string{}
	}
	out := AuditGroup{
		ID:       g.ID,
		Name:     g.Name,
		Path:     g.Path,
		OK:       g.OK(),
		Files:    g.Files,
		Bytes:    g.Bytes,
		Missing:  missing,
		Mixed:    g.Mixed,
		MixedExt: g.MixedExt,
	}
	for _, b := range g.Buckets {
		seasonMissing := b.Missing
		if seasonMissing == nil {
			seasonMissing = []string{}
		}
		out.Seasons = append(out.Seasons, AuditSeason{
			DirID:   b.DirID,
			DirPath: b.DirPath,
			Ext:     b.Ext,
			Sample:  b.Sample,
			Files:   b.Files,
			Bytes:   b.Bytes,
			Missing: seasonMissing,
			Mixed:   b.Mixed,
		})
	}
	if out.Seasons == nil {
		out.Seasons = []AuditSeason{}
	}
	out.TMDBID = g.TMDBID
	out.MediaType = g.MediaType
	out.Match = g.Match
	if g.Completeness != nil {
		out.Completeness = toAuditCompleteness(*g.Completeness, g.UnparsedEpisodes)
	}
	if g.Franchise != nil {
		out.Franchise = toAuditFranchise(*g.Franchise)
	}
	return out
}

func toAuditCompleteness(d audit.ShowDiff, unparsed int) *AuditCompleteness {
	out := &AuditCompleteness{
		MissingSeasons: nonNilInts(d.MissingSeasons),
		ExtraSeasons:   nonNilInts(d.ExtraSeasons),
		LatestSeason:   d.LatestSeason,
		Unreliable:     d.Unreliable,
		Unparsed:       unparsed,
	}
	for _, sd := range d.Seasons {
		out.Seasons = append(out.Seasons, AuditSeasonStatus{
			Season:  sd.Season,
			Local:   sd.Local,
			Aired:   sd.Aired,
			Total:   sd.Total,
			Missing: nonNilInts(sd.Missing),
			Extra:   nonNilInts(sd.Extra),
			Airing:  sd.Airing,
			Unknown: sd.Unknown,
			Note:    sd.Note,
		})
	}
	if out.Seasons == nil {
		out.Seasons = []AuditSeasonStatus{}
	}
	return out
}

func toAuditFranchise(d audit.CollectionDiff) *AuditFranchise {
	out := &AuditFranchise{
		Name:     d.Name,
		Parts:    d.Parts,
		Local:    toFranchiseParts(d.Local, nil),
		Missing:  toFranchiseParts(d.Missing, nil),
		Optional: toFranchiseParts(d.Optional, d.Notes),
		Upcoming: toFranchiseParts(d.Upcoming, nil),
	}
	return out
}

func toFranchiseParts(list []audit.FranchisePart, notes map[string]string) []AuditFranchisePart {
	out := make([]AuditFranchisePart, 0, len(list))
	for _, p := range list {
		out = append(out, AuditFranchisePart{
			ID:          p.ID,
			Title:       p.Title,
			ReleaseDate: p.ReleaseDate,
			Runtime:     p.Runtime,
			Reason:      notes[p.ID],
		})
	}
	return out
}

func nonNilInts(in []int) []int {
	if in == nil {
		return []int{}
	}
	return in
}

// auditGroupIncomplete 判断分组是否存在需要处理的问题（信息缺失/混用/缺季缺集/缺续集）。
func auditGroupIncomplete(g AuditGroup) bool {
	if !g.OK {
		return true
	}
	if g.Completeness != nil && len(g.Completeness.MissingSeasons) > 0 {
		return true
	}
	if g.Completeness != nil {
		for _, sd := range g.Completeness.Seasons {
			if len(sd.Missing) > 0 && !sd.Unknown {
				return true
			}
		}
	}
	return g.Franchise != nil && len(g.Franchise.Missing) > 0
}

func boolOrDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// buildAuditTree 拉取 rootID 下的完整目录树（逐目录并行遍历）。
func buildAuditTree(ctx context.Context, lister auditLister, accountID int64, rootID, rootName string) (audit.Node, int, error) {
	name := strings.TrimSpace(rootName)
	if name == "" {
		name = rootID
	}
	if fl, ok := lister.(auditFullLister); ok {
		if node, dirs, err := treeFromFullFiles(ctx, lister, fl, accountID, rootID, name); err == nil {
			return node, dirs, nil
		}
	}
	return treeFromList(ctx, lister, accountID, rootID, name)
}

// treeFromFullFiles 用「一次拉全量文件」+「按需列目录名」重建树。
//
// 思路：全量列表只给文件与它的 pid，不给目录名；于是从根开始按层列目录，
// 一旦某个子目录出现在「有文件的 pid 集合」里，就把它的文件挂上并停止下钻。
// 结束前核对挂上的文件数是否等于全量文件数，不相等说明结构比假设更深，
// 直接返回错误让上层退回逐目录遍历，宁可慢也不能出现残缺的树。
func treeFromFullFiles(ctx context.Context, lister auditLister, fl auditFullLister, accountID int64, rootID, rootName string) (audit.Node, int, error) {
	supported, err := fl.SupportsFullList(ctx, accountID)
	if err != nil || !supported {
		return audit.Node{}, 0, domain.Errorf(domain.CodeNotImplement, "网盘不支持全量文件列表")
	}
	entries, err := fl.ListAllFiles(ctx, accountID, rootID)
	if err != nil {
		return audit.Node{}, 0, err
	}

	byParent := make(map[string][]audit.Node, len(entries))
	for _, e := range entries {
		byParent[e.ParentID] = append(byParent[e.ParentID], audit.Node{ID: e.FileID, Name: e.Name, Size: e.Size})
	}

	type dirNode struct {
		id    string
		name  string
		dirs  []*dirNode
		files []audit.Node
	}

	root := &dirNode{id: rootID, name: rootName}
	level := []*dirNode{root}
	dirs, attached := 0, 0

	for depth := 0; len(level) > 0; depth++ {
		if depth > auditDepthMax || dirs > auditDirMax {
			break
		}
		listed := make([][]domain.FileItem, len(level))
		errs := make([]error, len(level))
		sem := make(chan struct{}, auditListParallel)
		var wg sync.WaitGroup
		for i, n := range level {
			wg.Add(1)
			go func(i int, n *dirNode) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				items, lerr := lister.List(ctx, accountID, n.id, false)
				if lerr != nil {
					errs[i] = lerr
					return
				}
				listed[i] = items
			}(i, n)
		}
		wg.Wait()
		for _, lerr := range errs {
			if lerr != nil {
				return audit.Node{}, dirs, lerr
			}
		}

		next := make([]*dirNode, 0, len(level))
		for i, n := range level {
			dirs++
			for _, child := range listed[i] {
				if !child.IsDir {
					n.files = append(n.files, audit.Node{ID: child.ID, Name: child.Name, Size: child.Size})
					attached++
					continue
				}
				cn := &dirNode{id: child.ID, name: child.Name}
				if files, ok := byParent[child.ID]; ok {
					// 这个目录本身就有文件：挂上即可，不再下钻（结构比这更深时由结尾的
					// 文件数校验发现，退回逐目录遍历）。
					cn.files = append(cn.files, files...)
					attached += len(files)
				} else if depth+1 <= auditDepthMax {
					next = append(next, cn)
				}
				n.dirs = append(n.dirs, cn)
			}
		}
		level = next
	}

	if attached != len(entries) {
		return audit.Node{}, dirs, domain.Errorf(domain.CodeInternal,
			"全量文件数与目录结构不匹配（%d/%d）", attached, len(entries))
	}

	var build func(n *dirNode) audit.Node
	build = func(n *dirNode) audit.Node {
		out := audit.Node{ID: n.id, Name: n.name, IsDir: true, Children: make([]audit.Node, 0, len(n.files)+len(n.dirs))}
		out.Children = append(out.Children, n.files...)
		for _, c := range n.dirs {
			out.Children = append(out.Children, build(c))
		}
		return out
	}
	return build(root), dirs, nil
}

func treeFromList(ctx context.Context, lister auditLister, accountID int64, rootID, rootName string) (audit.Node, int, error) {
	type dirNode struct {
		id    string
		name  string
		dirs  []*dirNode
		files []audit.Node
	}

	root := &dirNode{id: rootID, name: rootName}
	level := []*dirNode{root}
	dirs, files := 0, 0

	for depth := 0; len(level) > 0; depth++ {
		if depth > auditDepthMax || dirs > auditDirMax || files > auditFileMax {
			break
		}
		results := make([][]domain.FileItem, len(level))
		errs := make([]error, len(level))
		sem := make(chan struct{}, auditListParallel)
		var wg sync.WaitGroup
		for i, n := range level {
			wg.Add(1)
			go func(i int, n *dirNode) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				items, err := lister.List(ctx, accountID, n.id, false)
				if err != nil {
					errs[i] = err
					return
				}
				results[i] = items
			}(i, n)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return audit.Node{}, dirs, err
			}
		}
		next := make([]*dirNode, 0, len(level))
		for i, n := range level {
			dirs++
			for _, item := range results[i] {
				if item.IsDir {
					child := &dirNode{id: item.ID, name: item.Name}
					n.dirs = append(n.dirs, child)
					next = append(next, child)
					continue
				}
				files++
				n.files = append(n.files, audit.Node{ID: item.ID, Name: item.Name, Size: item.Size})
			}
		}
		level = next
	}

	var build func(n *dirNode) audit.Node
	build = func(n *dirNode) audit.Node {
		out := audit.Node{ID: n.id, Name: n.name, IsDir: true, Children: make([]audit.Node, 0, len(n.files)+len(n.dirs))}
		out.Children = append(out.Children, n.files...)
		for _, c := range n.dirs {
			out.Children = append(out.Children, build(c))
		}
		return out
	}
	return build(root), dirs, nil
}

func newAuditID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "audit_" + strconv.FormatInt(time.Now().Unix(), 36) + "_" + hex.EncodeToString(b[:])
}
