package mediaorganize

import (
	"context"
	"strings"
	"time"

	"litepan/internal/domain"
)

// AuditMoveItem 是一部剧的移动结果。
type AuditMoveItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// AuditMoveResult 是一次归集移动的结果（dry_run 时只给数量）。
type AuditMoveResult struct {
	DryRun      bool            `json:"dry_run"`
	TargetID    string          `json:"target_id"`
	TargetName  string          `json:"target_name"`
	WouldCreate bool            `json:"would_create"`
	Total       int             `json:"total"`
	Moved       int             `json:"moved"`
	Failed      int             `json:"failed"`
	Skipped     int             `json:"skipped"`
	Files       int             `json:"files"`
	Bytes       int64           `json:"bytes"`
	Items       []AuditMoveItem `json:"items,omitempty"`
}

// AuditMoveRequest 是归集移动的入参。
type AuditMoveRequest struct {
	// TargetID 直接用已存在的目标目录；为空时用 TargetParentID + TargetName 查找，找不到就建。
	TargetID       string `json:"target_id"`
	TargetParentID string `json:"target_parent_id"`
	TargetName     string `json:"target_name"`
	DryRun         bool   `json:"dry_run"`
}

// auditMover 是归集移动需要的文件操作能力，*file.Service 直接满足；抽成接口便于单测。
type auditMover interface {
	MoveFiles(ctx context.Context, accountID int64, fileIDs []string, targetParentID, sourceParentID string) error
	CreateFolder(ctx context.Context, accountID int64, parentID, name string) (*domain.FileItem, error)
	List(ctx context.Context, accountID int64, parentID string, forceRefresh bool) ([]domain.FileItem, error)
}

// MoveAuditGroups 把体检报告里不合格的剧整部移动到目标目录。
// 只处理 Unit=show 的报告：报告里的 Group.ID 是根目录下一层，其父目录即体检根目录。
func (s *Service) MoveAuditGroups(ctx context.Context, id string, req AuditMoveRequest) (*AuditMoveResult, error) {
	rep, ok := s.GetAudit(id)
	if !ok {
		return nil, domain.Errorf(domain.CodeNotFound, "体检报告不存在或已过期")
	}
	return s.moveAuditGroups(ctx, rep, req)
}

// moveAuditGroups 是 MoveAuditGroups 的可测实现。
func (s *Service) moveAuditGroups(ctx context.Context, rep *AuditReport, req AuditMoveRequest) (*AuditMoveResult, error) {
	if s == nil || s.files == nil {
		return nil, domain.Errorf(domain.CodeInternal, "文件服务不可用")
	}
	return moveAuditGroups(ctx, rep, s.files, req)
}

// moveAuditGroups 把报告里不合格的剧整体移动到目标目录；mover 抽象出来便于单测。
func moveAuditGroups(ctx context.Context, rep *AuditReport, mover auditMover, req AuditMoveRequest) (*AuditMoveResult, error) {
	if rep.Request.Unit != auditUnitShow {
		return nil, domain.Errorf(domain.CodeValidation, "只有「整部剧」粒度的体检报告支持归集移动")
	}
	rootID := rep.Request.RootID
	if strings.TrimSpace(rootID) == "" {
		return nil, domain.Errorf(domain.CodeValidation, "报告缺少根目录 ID")
	}

	bad := make([]AuditGroup, 0, len(rep.Groups))
	for _, g := range rep.Groups {
		if !g.OK {
			bad = append(bad, g)
		}
	}

	out := &AuditMoveResult{DryRun: req.DryRun, Total: len(bad)}
	targetID, targetName, wouldCreate, err := resolveAuditTarget(ctx, mover, rep.Request.AccountID, rootID, req)
	if err != nil {
		return nil, err
	}
	out.TargetID = targetID
	out.TargetName = targetName
	out.WouldCreate = wouldCreate

	if len(bad) == 0 {
		return out, nil
	}

	if req.DryRun {
		for _, g := range bad {
			out.Files += g.Files
			out.Bytes += g.Bytes
		}
		return out, nil
	}
	if targetID == "" {
		// 真移动必须先有目标目录
		created, cerr := mover.CreateFolder(ctx, rep.Request.AccountID, req.TargetParentID, req.TargetName)
		if cerr != nil {
			return nil, cerr
		}
		targetID = created.ID
		out.TargetID = targetID
	}

	for _, g := range bad {
		item := AuditMoveItem{ID: g.ID, Name: g.Name, Files: g.Files, Bytes: g.Bytes}
		if g.ID == "" {
			item.Error = "缺少目录 ID"
			out.Failed++
			out.Items = append(out.Items, item)
			continue
		}
		if g.ID == rootID {
			item.Skipped = true
			item.OK = true
			item.Error = "分组就是体检根目录本身，跳过"
			out.Skipped++
			out.Items = append(out.Items, item)
		} else if g.ID == targetID {
			item.Skipped = true
			item.OK = true
			out.Skipped++
			out.Items = append(out.Items, item)
		} else if err := moveWithRetry(ctx, mover, rep.Request.AccountID, g.ID, targetID, rootID); err != nil {
			item.Error = err.Error()
			out.Failed++
			out.Items = append(out.Items, item)
		} else {
			item.OK = true
			out.Moved++
			out.Files += g.Files
			out.Bytes += g.Bytes
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}

// movePendingHints 是 115 的「上一条操作还没跑完」类错误。这类失败往往稍后自己就成功了，
// 所以先等一会儿重试，重试完仍失败才记成失败。
var movePendingHints = []string{"990019", "尚未执行完成", "操作尚未完成"}

func isMovePending(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, hint := range movePendingHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// moveWithRetry 对「网盘还在处理上一条操作」做有限重试（3 次，间隔 2s/4s）。
func moveWithRetry(ctx context.Context, mover auditMover, accountID int64, fileID, targetID, sourceID string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt) * 2 * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		err = mover.MoveFiles(ctx, accountID, []string{fileID}, targetID, sourceID)
		if err == nil || !isMovePending(err) {
			return err
		}
	}
	return err
}

// resolveAuditTarget 确定目标目录：直传 ID 优先；否则在 TargetParentID 下按名字找，找不到标记为需创建。
func resolveAuditTarget(ctx context.Context, mover auditMover, accountID int64, rootID string, req AuditMoveRequest) (string, string, bool, error) {
	if id := strings.TrimSpace(req.TargetID); id != "" {
		if id == rootID {
			return "", "", false, domain.Errorf(domain.CodeValidation, "目标目录不能是体检根目录本身")
		}
		return id, "", false, nil
	}
	parent := strings.TrimSpace(req.TargetParentID)
	name := strings.TrimSpace(req.TargetName)
	if parent == "" || name == "" {
		return "", "", false, domain.Errorf(domain.CodeValidation, "需要 target_id，或 target_parent_id + target_name")
	}
	if parent == rootID {
		return "", "", false, domain.Errorf(domain.CodeValidation, "目标父目录不能是体检根目录本身")
	}
	items, err := mover.List(ctx, accountID, parent, false)
	if err != nil {
		return "", "", false, err
	}
	for _, it := range items {
		if it.IsDir && it.Name == name {
			return it.ID, name, false, nil
		}
	}
	return "", name, true, nil
}
