package mediaorganize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/driver"
)

// fakeAuditLister 用 flat 条目模拟网盘：ParentID 关系即目录结构。
type fakeAuditLister struct {
	full     bool
	entries  []driver.FullListEntry
	lists    map[string][]domain.FileItem
	listErr  error
	fullErr  error
	supports error
	calls    int
}

func (f *fakeAuditLister) List(_ context.Context, _ int64, parentID string, _ bool) ([]domain.FileItem, error) {
	f.calls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.lists[parentID], nil
}

func (f *fakeAuditLister) SupportsFullList(context.Context, int64) (bool, error) {
	return f.full, f.supports
}

func (f *fakeAuditLister) ListAllFiles(context.Context, int64, string) ([]driver.FullListEntry, error) {
	f.calls++
	if f.fullErr != nil {
		return nil, f.fullErr
	}
	return f.entries, nil
}

func TestRunAuditFullList(t *testing.T) {
	lister := &fakeAuditLister{
		full: true,
		entries: []driver.FullListEntry{
			{FileID: "show1", ParentID: "root", Name: "剧缺编码 (2019)"},
			{FileID: "s1", ParentID: "show1", Name: "Season 01"},
			{FileID: "f1", ParentID: "s1", Name: "剧缺编码 (2019) S01E01 [2160p AAC].mkv", Size: 100},
			{FileID: "show2", ParentID: "root", Name: "剧齐全 (2024)"},
			{FileID: "s2", ParentID: "show2", Name: "Season 01"},
			{FileID: "f2", ParentID: "s2", Name: "剧齐全 (2024) S01E01 [2160p H.265 DDP 5.1].mkv", Size: 200},
		},
	}
	rep, used, dirs, err := runAudit(context.Background(), lister, AuditRequest{
		RootID: "root", RootName: "韩剧",
	}, []string{"screen_size", "video_codec", "audio_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	if !used {
		t.Fatalf("应走全量列表")
	}
	if dirs != 5 { // root + 2 show + 2 season
		t.Fatalf("目录数 = %d, 期望 5", dirs)
	}
	if rep.TotalGroups != 2 || rep.BadGroups != 1 {
		t.Fatalf("分组 %d/不合格 %d, 期望 2/1", rep.TotalGroups, rep.BadGroups)
	}
	var found bool
	for i := range rep.Groups {
		if rep.Groups[i].OK() {
			continue
		}
		found = true
		g := rep.Groups[i]
		if g.ID != "show1" {
			t.Fatalf("不合格分组的 ID = %q, 期望 show1", g.ID)
		}
		if len(g.Buckets) != 1 || g.Buckets[0].DirID != "s1" {
			t.Fatalf("桶的目录 ID 不对: %+v", g.Buckets)
		}
		if strings.Join(g.Missing, ",") != "video_codec" {
			t.Fatalf("缺项 = %v, 期望 video_codec", g.Missing)
		}
	}
	if !found {
		t.Fatalf("没有找到不合格分组")
	}
}

func TestRunAuditFallbackList(t *testing.T) {
	lister := &fakeAuditLister{
		full: false,
		lists: map[string][]domain.FileItem{
			"root":  {{ID: "show1", Name: "剧 (2020)", IsDir: true}},
			"show1": {{ID: "s1", Name: "Season 01", IsDir: true}},
			"s1":    {{ID: "f1", Name: "剧 (2020) S01E01 [1080p H.264].mkv", Size: 10}},
		},
	}
	rep, used, _, err := runAudit(context.Background(), lister, AuditRequest{
		RootID: "root", RootName: "韩剧",
	}, []string{"video_codec", "audio_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	if used {
		t.Fatalf("不支持全量列表时应走递归")
	}
	if rep.BadGroups != 1 {
		t.Fatalf("不合格分组 = %d, 期望 1", rep.BadGroups)
	}
	if strings.Join(rep.Groups[0].Missing, ",") != "audio_codec" {
		t.Fatalf("缺项 = %v, 期望 audio_codec", rep.Groups[0].Missing)
	}
}

func TestRunAuditFullListFallsBackOnError(t *testing.T) {
	lister := &fakeAuditLister{
		full:    true,
		fullErr: errors.New("driver 不支持"),
		lists: map[string][]domain.FileItem{
			"root": {{ID: "f1", Name: "某电影 (2020) [2160p H.265 AAC].mkv"}},
		},
	}
	rep, used, _, err := runAudit(context.Background(), lister, AuditRequest{
		RootID: "root", RootName: "电影",
	}, []string{"video_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("全量列表失败应退回递归，实际报错: %v", err)
	}
	if used {
		t.Fatalf("退回了递归，used_full_list 应为 false")
	}
	if rep.TotalGroups != 1 || rep.BadGroups != 0 {
		t.Fatalf("分组 %d/不合格 %d, 期望 1/0", rep.TotalGroups, rep.BadGroups)
	}
}

func TestNormalizeAuditRequired(t *testing.T) {
	got := normalizeAuditRequired([]string{"video_codec", " video_codec ", "", "audio_codec"})
	if strings.Join(got, ",") != "video_codec,audio_codec" {
		t.Fatalf("归一化结果 = %v", got)
	}
}

func TestNewAuditIDUnique(t *testing.T) {
	a, b := newAuditID(), newAuditID()
	if a == b || !strings.HasPrefix(a, "audit_") {
		t.Fatalf("ID 生成异常: %q / %q", a, b)
	}
}
