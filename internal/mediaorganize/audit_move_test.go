package mediaorganize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"litepan/internal/domain"
)

// fakeMover 记录移动调用，可注入 create/list 行为。
type fakeMover struct {
	folders   map[string][]domain.FileItem
	moved     [][]string
	moveErr   error
	created   []string
	createErr error
	listErr   error
}

func (f *fakeMover) MoveFiles(_ context.Context, _ int64, ids []string, target, _ string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moved = append(f.moved, append([]string{"->" + target}, ids...))
	return nil
}

func (f *fakeMover) CreateFolder(_ context.Context, _ int64, parentID, name string) (*domain.FileItem, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, parentID+"/"+name)
	return &domain.FileItem{ID: "new-" + name, Name: name, IsDir: true}, nil
}

func (f *fakeMover) List(_ context.Context, _ int64, parentID string, _ bool) ([]domain.FileItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.folders[parentID], nil
}

func badReport() *AuditReport {
	return &AuditReport{
		ID: "audit_x",
		Request: AuditRequest{
			AccountID: 1,
			RootID:    "root",
			Unit:      auditUnitShow,
		},
		Groups: []AuditGroup{
			{ID: "show_a", Name: "缺编码剧", Files: 16, Bytes: 100, Missing: []string{"video_codec"}},
			{ID: "show_b", Name: "齐全剧", OK: true, Files: 12, Bytes: 50},
		},
	}
}

func TestMoveAuditGroupsDryRun(t *testing.T) {
	mover := &fakeMover{}
	rep := badReport()
	out, err := moveAuditGroups(context.Background(), rep, mover, AuditMoveRequest{
		TargetID: "dest", DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !out.DryRun || out.Total != 1 {
		t.Fatalf("dry run 结果不对: %+v", out)
	}
	if out.Files != 16 || out.Bytes != 100 {
		t.Fatalf("应只统计不合格的剧, 得到 files=%d bytes=%d", out.Files, out.Bytes)
	}
	if len(mover.moved) != 0 || len(mover.created) != 0 {
		t.Fatalf("dry run 不应有任何写操作")
	}
}

func TestMoveAuditGroupsApply(t *testing.T) {
	mover := &fakeMover{}
	out, err := moveAuditGroups(context.Background(), badReport(), mover, AuditMoveRequest{
		TargetID: "dest", DryRun: false,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Moved != 1 || out.Failed != 0 {
		t.Fatalf("移动结果 = %+v", out)
	}
	if len(mover.moved) != 1 || mover.moved[0][0] != "->dest" || mover.moved[0][1] != "show_a" {
		t.Fatalf("移动调用不对: %v", mover.moved)
	}
}

func TestMoveAuditGroupsCreatesTarget(t *testing.T) {
	mover := &fakeMover{}
	out, err := moveAuditGroups(context.Background(), badReport(), mover, AuditMoveRequest{
		TargetParentID: "recv",
		TargetName:     "待整理",
		DryRun:         false,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(mover.created) != 1 || mover.created[0] != "recv/待整理" {
		t.Fatalf("应创建目标目录，实际 %v", mover.created)
	}
	if out.TargetID != "new-待整理" || out.Moved != 1 {
		t.Fatalf("结果 = %+v", out)
	}
}

func TestMoveAuditGroupsReusesExistingTarget(t *testing.T) {
	mover := &fakeMover{folders: map[string][]domain.FileItem{
		"recv": {{ID: "exist", Name: "待整理", IsDir: true}},
	}}
	out, err := moveAuditGroups(context.Background(), badReport(), mover, AuditMoveRequest{
		TargetParentID: "recv", TargetName: "待整理", DryRun: false,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(mover.created) != 0 || out.TargetID != "exist" {
		t.Fatalf("不应重建目录，实际 created=%v target=%s", mover.created, out.TargetID)
	}
}

func TestMoveAuditGroupsSkipsTargetItself(t *testing.T) {
	rep := badReport()
	rep.Groups[0].ID = "dest"
	mover := &fakeMover{}
	out, err := moveAuditGroups(context.Background(), rep, mover, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Skipped != 1 || len(mover.moved) != 0 {
		t.Fatalf("目标目录本身应跳过, 结果 %+v", out)
	}
}

func TestMoveAuditGroupsSkipsRootGroup(t *testing.T) {
	rep := badReport()
	rep.Groups = []AuditGroup{{ID: "root", Name: "电影", Files: 3, Missing: []string{"audio_codec"}}}
	mover := &fakeMover{}
	out, err := moveAuditGroups(context.Background(), rep, mover, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Skipped != 1 || len(mover.moved) != 0 {
		t.Fatalf("分组就是根目录时应跳过, 结果 %+v", out)
	}
}

func TestMoveAuditGroupsRejectsSeasonUnit(t *testing.T) {
	rep := badReport()
	rep.Request.Unit = auditUnitSeason
	if _, err := moveAuditGroups(context.Background(), rep, &fakeMover{}, AuditMoveRequest{TargetID: "dest"}); err == nil {
		t.Fatalf("按季的报告应拒绝整剧移动")
	}
}

func TestMoveAuditGroupsRejectsRootAsTarget(t *testing.T) {
	if _, err := moveAuditGroups(context.Background(), badReport(), &fakeMover{}, AuditMoveRequest{TargetID: "root"}); err == nil {
		t.Fatalf("目标不能是体检根目录")
	}
}

func TestMoveAuditGroupsRecordsFailure(t *testing.T) {
	mover := &fakeMover{moveErr: errors.New("网盘拒绝")}
	out, err := moveAuditGroups(context.Background(), badReport(), mover, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("单部失败不应返回整体错误: %v", err)
	}
	if out.Failed != 1 || !strings.Contains(out.Items[0].Error, "网盘拒绝") {
		t.Fatalf("失败应记录在条目里: %+v", out)
	}
}

type pendingMover struct {
	fakeMover
	fails int
	err   error
	calls int
}

func (p *pendingMover) MoveFiles(ctx context.Context, accountID int64, ids []string, target, source string) error {
	p.calls++
	if p.calls <= p.fails {
		return p.err
	}
	return p.fakeMover.MoveFiles(ctx, accountID, ids, target, source)
}

func TestMoveRetriesWhenNetworkStillProcessing(t *testing.T) {
	m := &pendingMover{fails: 1, err: errors.New("DRIVER_ERROR: 115 API 错误(990019)：移动[x]操作尚未执行完成，请稍后再试！")}
	out, err := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if out.Moved != 1 || out.Failed != 0 {
		t.Fatalf("结果 = %+v", out)
	}
	if m.calls != 2 {
		t.Fatalf("应重试一次, 实际调用 %d 次", m.calls)
	}
}

func TestMoveDoesNotRetryOtherErrors(t *testing.T) {
	m := &pendingMover{fails: 99, err: errors.New("DRIVER_ERROR: 115 API 错误(990001)：目录不存在")}
	out, _ := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{TargetID: "dest"})
	if out.Failed != 1 {
		t.Fatalf("非「处理中」错误应直接失败: %+v", out)
	}
	if m.calls != 1 {
		t.Fatalf("不该重试, 实际 %d 次", m.calls)
	}
}
