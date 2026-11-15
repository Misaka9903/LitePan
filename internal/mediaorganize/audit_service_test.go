package mediaorganize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"litepan/internal/domain"
)

// fakeAuditLister 用 parentID→子项 的映射模拟网盘目录。
type fakeAuditLister struct {
	lists   map[string][]domain.FileItem
	listErr error
	calls   int
}

func (f *fakeAuditLister) List(_ context.Context, _ int64, parentID string, _ bool) ([]domain.FileItem, error) {
	f.calls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.lists[parentID], nil
}

func TestRunAuditShowUnit(t *testing.T) {
	lister := &fakeAuditLister{lists: map[string][]domain.FileItem{
		"root": {
			{ID: "show1", Name: "剧缺编码 (2019)", IsDir: true},
			{ID: "show2", Name: "剧齐全 (2024)", IsDir: true},
		},
		"show1": {{ID: "s1", Name: "Season 01", IsDir: true}},
		"s1":    {{ID: "f1", Name: "剧缺编码 (2019) S01E01 [2160p AAC].mkv", Size: 100}},
		"show2": {{ID: "s2", Name: "Season 01", IsDir: true}},
		"s2":    {{ID: "f2", Name: "剧齐全 (2024) S01E01 [2160p H.265 DDP 5.1].mkv", Size: 200}},
	}}
	rep, dirs, err := runAudit(context.Background(), lister, AuditRequest{
		RootID: "root", RootName: "韩剧",
	}, []string{"screen_size", "video_codec", "audio_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	if dirs != 5 {
		t.Fatalf("目录数 = %d, 期望 5", dirs)
	}
	if rep.TotalGroups != 2 || rep.BadGroups != 1 {
		t.Fatalf("分组 %d/不合格 %d, 期望 2/1", rep.TotalGroups, rep.BadGroups)
	}
	for i := range rep.Groups {
		g := rep.Groups[i]
		if g.OK() {
			continue
		}
		if g.ID != "show1" {
			t.Fatalf("不合格分组 ID = %q, 期望 show1", g.ID)
		}
		if len(g.Buckets) != 1 || g.Buckets[0].DirID != "s1" {
			t.Fatalf("桶的目录 ID 不对: %+v", g.Buckets)
		}
		if strings.Join(g.Missing, ",") != "video_codec" {
			t.Fatalf("缺项 = %v, 期望 video_codec", g.Missing)
		}
	}
}

func TestRunAuditAnySeasonBadRollsUp(t *testing.T) {
	lister := &fakeAuditLister{lists: map[string][]domain.FileItem{
		"root": {{ID: "show", Name: "两季剧 (2017)", IsDir: true}},
		"show": {{ID: "s1", Name: "Season 01", IsDir: true}, {ID: "s2", Name: "Season 02", IsDir: true}},
		"s1":   {{ID: "f1", Name: "两季剧 (2017) S01E01 [1080p H.264 AAC].mkv"}},
		"s2":   {{ID: "f2", Name: "两季剧 (2017) S02E01 [1080p AAC].mkv"}},
	}}
	rep, _, err := runAudit(context.Background(), lister, AuditRequest{RootID: "root", RootName: "韩剧"},
		[]string{"video_codec", "audio_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	if rep.BadGroups != 1 {
		t.Fatalf("只要一季不合格整部剧就该不合格, bad=%d", rep.BadGroups)
	}
	if rep.Groups[0].Files != 2 {
		t.Fatalf("整剧文件数 = %d, 期望 2", rep.Groups[0].Files)
	}
}

func TestRunAuditListError(t *testing.T) {
	lister := &fakeAuditLister{listErr: errors.New("网盘超时")}
	if _, _, err := runAudit(context.Background(), lister, AuditRequest{RootID: "root", RootName: "x"},
		[]string{"video_codec"}, auditUnitShow); err == nil {
		t.Fatalf("列表失败应整次报错")
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
