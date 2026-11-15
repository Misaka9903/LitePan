package mediaorganize

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/mediaorganize/audit"
	"litepan/internal/settings"
)

const (
	auditReportTTL  = 24 * time.Hour
	auditReportMax  = 20
	auditDirMax     = 50000
	auditFileMax    = 400000
	auditDepthMax   = 24
	auditUnitShow   = "show"
	auditUnitSeason = "season"
)

// auditLister 是体检需要的文件列表能力，*file.Service 直接满足；抽成接口便于单测。
type auditLister interface {
	List(ctx context.Context, accountID int64, parentID string, forceRefresh bool) ([]domain.FileItem, error)
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
}

// AuditSummary 是体检的汇总数字。
type AuditSummary struct {
	TotalGroups  int   `json:"total_groups"`
	BadGroups    int   `json:"bad_groups"`
	TotalFiles   int   `json:"total_files"`
	BadFiles     int   `json:"bad_files"`
	BadBytes     int64 `json:"bad_bytes"`
	ScannedDirs  int   `json:"scanned_dirs"`
	UsedFullList bool  `json:"used_full_list"`
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

// AuditGroup 是一部剧（Unit=show）或一季（Unit=season）。
type AuditGroup struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Path    string        `json:"path"`
	OK      bool          `json:"ok"`
	Files   int           `json:"files"`
	Bytes   int64         `json:"bytes"`
	Missing []string      `json:"missing"`
	Mixed   bool          `json:"mixed"`
	Seasons []AuditSeason `json:"seasons"`
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

	rep, used, dirs, err := runAudit(ctx, s.files, req, required, unit)
	if err != nil {
		return nil, err
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
			UsedFullList: used,
		},
		raw: rep,
	}
	report.Request.Required = required
	report.Request.Unit = unit
	for _, g := range rep.Groups {
		report.Groups = append(report.Groups, toAuditGroup(g))
	}
	s.storeAudit(report)
	return report, nil
}

// runAudit 拉树并判定，供 RunAudit 与单测共用。
func runAudit(ctx context.Context, lister auditLister, req AuditRequest, required []string, unit string) (audit.Report, bool, int, error) {
	tree, used, dirs, err := buildAuditTree(ctx, lister, req.AccountID, req.RootID, req.RootName)
	if err != nil {
		return audit.Report{}, used, dirs, err
	}
	return audit.Run(tree, audit.Options{Required: required, Unit: unit, Exts: req.Exts}), used, dirs, nil
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
	out := AuditGroup{
		ID:      g.ID,
		Name:    g.Name,
		Path:    g.Path,
		OK:      g.OK(),
		Files:   g.Files,
		Bytes:   g.Bytes,
		Missing: g.Missing,
		Mixed:   g.Mixed,
	}
	for _, b := range g.Buckets {
		out.Seasons = append(out.Seasons, AuditSeason{
			DirID:   b.DirID,
			DirPath: b.DirPath,
			Ext:     b.Ext,
			Sample:  b.Sample,
			Files:   b.Files,
			Bytes:   b.Bytes,
			Missing: b.Missing,
			Mixed:   b.Mixed,
		})
	}
	return out
}

// buildAuditTree 拉取 rootID 下的完整目录树。
// 网盘支持全量列表时一次调用搞定，否则退回逐目录递归（有深度与数量上限）。
func buildAuditTree(ctx context.Context, lister auditLister, accountID int64, rootID, rootName string) (audit.Node, bool, int, error) {
	name := strings.TrimSpace(rootName)
	if name == "" {
		name = rootID
	}
	full, err := lister.SupportsFullList(ctx, accountID)
	if err == nil && full {
		entries, ferr := lister.ListAllFiles(ctx, accountID, rootID)
		if ferr == nil {
			node, dirs := treeFromFullList(entries, rootID, name)
			return node, true, dirs, nil
		}
	}
	node, dirs, rerr := treeFromList(ctx, lister, accountID, rootID, name)
	if rerr != nil {
		return audit.Node{}, false, 0, rerr
	}
	return node, false, dirs, nil
}

func treeFromFullList(entries []driver.FullListEntry, rootID, rootName string) (audit.Node, int) {
	byParent := map[string][]driver.FullListEntry{}
	dirIDs := map[string]struct{}{rootID: {}}
	for _, e := range entries {
		byParent[e.ParentID] = append(byParent[e.ParentID], e)
		dirIDs[e.ParentID] = struct{}{}
	}
	var walk func(id, name string, depth int) audit.Node
	walk = func(id, name string, depth int) audit.Node {
		n := audit.Node{ID: id, Name: name, IsDir: true}
		if depth > auditDepthMax {
			return n
		}
		for _, e := range byParent[id] {
			if _, isDir := dirIDs[e.FileID]; isDir {
				n.Children = append(n.Children, walk(e.FileID, e.Name, depth+1))
				continue
			}
			n.Children = append(n.Children, audit.Node{ID: e.FileID, Name: e.Name, Size: e.Size})
		}
		return n
	}
	root := walk(rootID, rootName, 0)
	return root, len(dirIDs)
}

func treeFromList(ctx context.Context, lister auditLister, accountID int64, rootID, rootName string) (audit.Node, int, error) {
	var (
		dirs  int
		files int
	)
	var walk func(id, name string, depth int) (audit.Node, error)
	walk = func(id, name string, depth int) (audit.Node, error) {
		n := audit.Node{ID: id, Name: name, IsDir: true}
		dirs++
		if depth > auditDepthMax || dirs > auditDirMax || files > auditFileMax {
			return n, nil
		}
		items, err := lister.List(ctx, accountID, id, false)
		if err != nil {
			return n, err
		}
		for _, it := range items {
			if it.IsDir {
				child, cerr := walk(it.ID, it.Name, depth+1)
				if cerr != nil {
					return n, cerr
				}
				n.Children = append(n.Children, child)
				continue
			}
			files++
			n.Children = append(n.Children, audit.Node{ID: it.ID, Name: it.Name, Size: it.Size})
		}
		return n, nil
	}
	root, err := walk(rootID, rootName, 0)
	if err != nil {
		return audit.Node{}, dirs, err
	}
	return root, dirs, nil
}

func newAuditID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "audit_" + strconv.FormatInt(time.Now().Unix(), 36) + "_" + hex.EncodeToString(b[:])
}
