package mediaorganize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"litepan/internal/domain"
)

// fakeMover 用一张「父目录 → 子项」的状态表模拟网盘，移动会真的改动状态，
// 这样才能验证「以结果为准」的核对逻辑。
type fakeMover struct {
	state      map[string][]domain.FileItem
	moveErr    error
	applyOnErr bool // 返回错误但实际执行了移动（115 异步队列的典型表现）
	createErr  error
	listErr    error
	moved      [][]string
	created    []string
	listCalls  int
}

func newFakeMover() *fakeMover {
	return &fakeMover{state: map[string][]domain.FileItem{}}
}

func (f *fakeMover) addDir(parentID, id, name string) {
	f.state[parentID] = append(f.state[parentID], domain.FileItem{ID: id, Name: name, IsDir: true})
}

func (f *fakeMover) MoveFiles(_ context.Context, _ int64, ids []string, target, source string) error {
	f.moved = append(f.moved, append([]string{"->" + target}, ids...))
	if f.moveErr != nil && !f.applyOnErr {
		return f.moveErr
	}
	for _, id := range ids {
		rest := make([]domain.FileItem, 0, len(f.state[source]))
		var moved *domain.FileItem
		for i := range f.state[source] {
			if f.state[source][i].ID == id {
				item := f.state[source][i]
				moved = &item
				continue
			}
			rest = append(rest, f.state[source][i])
		}
		f.state[source] = rest
		if moved != nil {
			src := *moved
			src.IsDir = true
			f.state[target] = append(f.state[target], src)
		}
	}
	return f.moveErr
}

func (f *fakeMover) CreateFolder(_ context.Context, _ int64, parentID, name string) (*domain.FileItem, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, parentID+"/"+name)
	return &domain.FileItem{ID: "new-" + name, Name: name, IsDir: true}, nil
}

func (f *fakeMover) List(_ context.Context, _ int64, parentID string, _ bool) ([]domain.FileItem, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.state[parentID], nil
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

// moverWithRoot 造一个「root 下挂着 show_a」的 mover。
func moverWithRoot() *fakeMover {
	m := newFakeMover()
	m.addDir("root", "show_a", "缺编码剧")
	return m
}

func TestMoveAuditGroupsDryRun(t *testing.T) {
	m := moverWithRoot()
	out, err := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{TargetID: "dest", DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !out.DryRun || out.Total != 1 || out.Files != 16 || out.Bytes != 100 {
		t.Fatalf("dry run 结果不对: %+v", out)
	}
	if len(m.moved) != 0 || len(m.created) != 0 {
		t.Fatalf("dry run 不应有写操作")
	}
}

func TestMoveAuditGroupsApply(t *testing.T) {
	m := moverWithRoot()
	m.state["dest"] = nil
	out, err := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Moved != 1 || out.Failed != 0 {
		t.Fatalf("结果 = %+v", out)
	}
	if len(m.state["root"]) != 0 {
		t.Fatalf("源目录应已清空")
	}
	if len(m.state["dest"]) != 1 || m.state["dest"][0].ID != "show_a" {
		t.Fatalf("目标目录内容不对: %+v", m.state["dest"])
	}
}

func TestMoveAuditGroupsCreatesTarget(t *testing.T) {
	m := moverWithRoot()
	out, err := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{
		TargetParentID: "recv", TargetName: "待整理",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(m.created) != 1 || m.created[0] != "recv/待整理" {
		t.Fatalf("应创建目标目录, 实际 %v", m.created)
	}
	if out.TargetID != "new-待整理" || out.Moved != 1 {
		t.Fatalf("结果 = %+v", out)
	}
}

func TestMoveAuditGroupsReusesExistingTarget(t *testing.T) {
	m := moverWithRoot()
	m.addDir("recv", "exist", "待整理")
	out, err := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{
		TargetParentID: "recv", TargetName: "待整理",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(m.created) != 0 || out.TargetID != "exist" {
		t.Fatalf("不该重建目录, created=%v target=%s", m.created, out.TargetID)
	}
}

func TestMoveAuditGroupsSkipsTargetItself(t *testing.T) {
	rep := badReport()
	rep.Groups[0].ID = "dest"
	m := newFakeMover()
	out, err := moveAuditGroups(context.Background(), rep, m, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Skipped != 1 || len(m.moved) != 0 {
		t.Fatalf("目标目录本身应跳过, 结果 %+v", out)
	}
}

func TestMoveAuditGroupsRejectsSeasonUnit(t *testing.T) {
	rep := badReport()
	rep.Request.Unit = auditUnitSeason
	if _, err := moveAuditGroups(context.Background(), rep, newFakeMover(), AuditMoveRequest{TargetID: "dest"}); err == nil {
		t.Fatalf("按季的报告应拒绝整剧移动")
	}
}

func TestMoveAuditGroupsRejectsRootAsTarget(t *testing.T) {
	if _, err := moveAuditGroups(context.Background(), badReport(), newFakeMover(), AuditMoveRequest{TargetID: "root"}); err == nil {
		t.Fatalf("目标不能是体检根目录")
	}
}

func TestMoveAuditGroupsRecordsRealFailure(t *testing.T) {
	// 报错且确实没搬走 → 失败
	m := moverWithRoot()
	m.moveErr = errors.New("网盘拒绝")
	out, err := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("单部失败不应返回整体错误: %v", err)
	}
	if out.Failed != 1 || out.Moved != 0 {
		t.Fatalf("应记 1 部失败, 结果 %+v", out)
	}
	if !strings.Contains(out.Items[0].Error, "仍留在原目录") && !strings.Contains(out.Items[0].Error, "网盘拒绝") {
		t.Fatalf("失败原因缺失: %+v", out.Items[0])
	}
}

func TestMoveAuditGroupsReconcilesErrorButMoved(t *testing.T) {
	// 报错但其实搬走了（115 异步队列的常态）→ 应算成功
	m := moverWithRoot()
	m.moveErr = errors.New("115 API 错误(990019)：操作尚未执行完成")
	m.applyOnErr = true
	out, _ := moveAuditGroups(context.Background(), badReport(), m, AuditMoveRequest{TargetID: "dest"})
	if out.Moved != 1 || out.Failed != 0 {
		t.Fatalf("实际搬走了应算成功, 结果 %+v", out)
	}
	if out.Items[0].Error != "" {
		t.Fatalf("成功后不该留错误信息: %+v", out.Items[0])
	}
}

func TestMoveAuditGroupsReconcilesSuccessButNotMoved(t *testing.T) {
	// 接口说成功但目录还在原处 → 必须记失败，这是真机上「秘密森林」那种情况
	m := moverWithRoot()
	m.applyOnErr = false
	// 用一个「声称成功但不做任何事」的 mover
	noop := &noopMover{fakeMover: m, state: m.state}
	out, _ := moveAuditGroups(context.Background(), badReport(), noop, AuditMoveRequest{TargetID: "dest"})
	if out.Failed != 1 || out.Moved != 0 {
		t.Fatalf("没搬走必须记失败, 结果 %+v", out)
	}
	if !strings.Contains(out.Items[0].Error, "仍留在原目录") {
		t.Fatalf("应说明仍留在原目录: %+v", out.Items[0])
	}
}

type noopMover struct {
	*fakeMover
	state map[string][]domain.FileItem
}

func (n *noopMover) MoveFiles(context.Context, int64, []string, string, string) error { return nil }

type pendingMover struct {
	*fakeMover
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
	m := moverWithRoot()
	p := &pendingMover{fakeMover: m, fails: 1, err: errors.New("DRIVER_ERROR: 115 API 错误(990019)：操作尚未执行完成，请稍后再试！")}
	out, err := moveAuditGroups(context.Background(), badReport(), p, AuditMoveRequest{TargetID: "dest"})
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if out.Moved != 1 || out.Failed != 0 {
		t.Fatalf("结果 = %+v", out)
	}
	if p.calls != 2 {
		t.Fatalf("应重试一次, 实际调用 %d 次", p.calls)
	}
}

func TestMoveDoesNotRetryOtherErrors(t *testing.T) {
	m := moverWithRoot()
	p := &pendingMover{fakeMover: m, fails: 99, err: errors.New("DRIVER_ERROR: 115 API 错误(990001)：目录不存在")}
	out, _ := moveAuditGroups(context.Background(), badReport(), p, AuditMoveRequest{TargetID: "dest"})
	if out.Failed != 1 {
		t.Fatalf("非「处理中」错误应直接失败: %+v", out)
	}
	if p.calls != 1 {
		t.Fatalf("不该重试, 实际 %d 次", p.calls)
	}
}
