package mediaorganize

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/mediaorganize/audit"
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

// fakeFullLister 同时支持全量文件列表，用来验证「全量文件 + 按需目录名」的取树路。
type fakeFullLister struct {
	fakeAuditLister
	full      bool
	entries   []driver.FullListEntry
	fullErr   error
	fullCalls int
}

func (f *fakeFullLister) SupportsFullList(context.Context, int64) (bool, error) { return f.full, nil }

func (f *fakeFullLister) ListAllFiles(_ context.Context, _ int64, _ string) ([]driver.FullListEntry, error) {
	f.fullCalls++
	return f.entries, f.fullErr
}

func TestTreeFromFullFilesShowStructure(t *testing.T) {
	// 韩剧结构：root/剧名/Season/文件 —— 只需要列 root 和每个剧名，共 3 次 List
	f := &fakeFullLister{
		full: true,
		fakeAuditLister: fakeAuditLister{lists: map[string][]domain.FileItem{
			"root": {
				{ID: "show1", Name: "剧A (2020)", IsDir: true},
				{ID: "show2", Name: "剧B (2021)", IsDir: true},
			},
			"show1": {{ID: "s1", Name: "Season 01", IsDir: true}},
			"show2": {{ID: "s2", Name: "Season 01", IsDir: true}},
		}},
		entries: []driver.FullListEntry{
			{FileID: "f1", ParentID: "s1", Name: "剧A (2020) S01E01 [2160p H.265 AAC].mkv", Size: 1},
			{FileID: "f2", ParentID: "s1", Name: "剧A (2020) S01E02 [2160p H.265 AAC].mkv", Size: 1},
			{FileID: "f3", ParentID: "s2", Name: "剧B (2021) S01E01 [1080p H.264].mkv", Size: 1},
		},
	}
	rep, dirs, err := runAudit(context.Background(), f, AuditRequest{RootID: "root", RootName: "韩剧"},
		[]string{"video_codec", "audio_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	if dirs != 3 {
		t.Fatalf("列目录次数 = %d, 期望 3（root + 两个剧名）", dirs)
	}
	if rep.TotalGroups != 2 || rep.BadGroups != 1 {
		t.Fatalf("分组 %d/不合格 %d, 期望 2/1", rep.TotalGroups, rep.BadGroups)
	}
}

func TestTreeFromFullFilesMovieStructure(t *testing.T) {
	// 电影结构：root/片名/文件 —— 只列 root 一次
	f := &fakeFullLister{
		full: true,
		fakeAuditLister: fakeAuditLister{lists: map[string][]domain.FileItem{
			"root": {
				{ID: "m1", Name: "七宗罪 (1995)", IsDir: true},
				{ID: "m2", Name: "1917 (2019)", IsDir: true},
			},
		}},
		entries: []driver.FullListEntry{
			{FileID: "f1", ParentID: "m1", Name: "七宗罪 (1995) [2160p H.265 DDP 5.1].mkv"},
			{FileID: "f2", ParentID: "m2", Name: "1917 (2019) [2160p].mkv"},
		},
	}
	rep, dirs, err := runAudit(context.Background(), f, AuditRequest{RootID: "root", RootName: "电影"},
		[]string{"screen_size", "video_codec", "audio_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	if dirs != 1 {
		t.Fatalf("列目录次数 = %d, 期望 1（只列 root）", dirs)
	}
	if rep.BadGroups != 1 {
		t.Fatalf("不合格 = %d, 期望 1（1917 缺编码）", rep.BadGroups)
	}
}

func TestTreeFromFullFilesFallsBackWhenIncomplete(t *testing.T) {
	// 全量列表声明 2 个文件，但按剪枝规则只能挂上 1 个 → 必须退回逐目录遍历
	f := &fakeFullLister{
		full: true,
		fakeAuditLister: fakeAuditLister{lists: map[string][]domain.FileItem{
			"root": {{ID: "show", Name: "剧 (2020)", IsDir: true}},
			"show": {{ID: "s1", Name: "Season 01", IsDir: true}, {ID: "s2", Name: "Season 02", IsDir: true}},
			"s1":   {{ID: "f1", Name: "剧 (2020) S01E01 [1080p H.264 AAC].mkv"}},
			"s2":   {{ID: "f2", Name: "剧 (2020) S02E01 [1080p H.264 AAC].mkv"}},
		}},
		// 故意把两个文件都算在 s1 名下，剪枝会跳过 s2
		entries: []driver.FullListEntry{
			{FileID: "f1", ParentID: "s1", Name: "剧 (2020) S01E01 [1080p H.264 AAC].mkv"},
			{FileID: "f9", ParentID: "s1", Name: "剧 (2020) S01E09 [1080p H.264 AAC].mkv"},
		},
	}
	// s2 的 f2 只有靠逐目录遍历才看得到
	if _, _, err := treeFromFullFiles(context.Background(), f, f, 0, "root", "韩剧"); err == nil {
		t.Fatalf("文件数不匹配时应返回错误以便退回")
	}
	rep, _, err := runAudit(context.Background(), f, AuditRequest{RootID: "root", RootName: "韩剧"},
		[]string{"video_codec"}, auditUnitShow)
	if err != nil {
		t.Fatalf("退回后仍应成功: %v", err)
	}
	if rep.TotalFiles != 2 {
		t.Fatalf("退回逐目录后应看到 2 个文件, 实际 %d", rep.TotalFiles)
	}
}

func TestToAuditGroupSerializesEmptySlices(t *testing.T) {
	// 前端模板里有 group.missing.length，null 会直接抛错，所以必须是 []。
	out, err := json.Marshal(toAuditGroup(audit.Group{ID: "s1", Name: "齐全剧"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"missing":[]`) {
		t.Fatalf("missing 应为空数组, 实际 %s", got)
	}
	if !strings.Contains(got, `"seasons":[]`) {
		t.Fatalf("seasons 应为空数组, 实际 %s", got)
	}
}
