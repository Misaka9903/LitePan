package audit

import (
	"reflect"
	"testing"
)

func part(id, title, release string, runtime int) FranchisePart {
	return FranchisePart{ID: id, Title: title, ReleaseDate: release, Runtime: runtime}
}

func titles(list []FranchisePart) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Title)
	}
	return out
}

// 实测：本地只有《伦敦陷落》，合集里另两部正片（含一部更早的前作）应算缺。
func TestCompareCollectionMissingSequels(t *testing.T) {
	parts := []FranchisePart{
		part("117263", "奥林匹斯的陷落", "2013-03-20", 120),
		part("267860", "伦敦陷落", "2016-03-02", 99),
		part("423204", "天使陷落", "2019-08-21", 122),
		part("762409", "Night Has Fallen", "", 0),
	}
	local := map[string]FranchisePart{"267860": parts[1]}
	diff := CompareCollection("陷落（系列）", parts, local, testToday)

	if !diff.Bad() {
		t.Fatal("缺续集没被判为不合格")
	}
	if want := []string{"奥林匹斯的陷落", "天使陷落"}; !reflect.DeepEqual(titles(diff.Missing), want) {
		t.Fatalf("缺失 = %v, 期望 %v", titles(diff.Missing), want)
	}
	if want := []string{"Night Has Fallen"}; !reflect.DeepEqual(titles(diff.Upcoming), want) {
		t.Fatalf("未上映 = %v, 期望 %v", titles(diff.Upcoming), want)
	}
}

// 实测：本地《小森林 夏秋/冬春》，另有 2018 年重剪合集版 → 可选，不算缺。
func TestCompareCollectionRemakeIsOptional(t *testing.T) {
	parts := []FranchisePart{
		part("294682", "小森林：夏秋篇", "2014-08-30", 111),
		part("336026", "小森林：冬春篇", "2015-01-28", 120),
		part("890406", "小森林：四季", "2018-03-22", 120),
	}
	local := map[string]FranchisePart{"294682": parts[0], "336026": parts[1]}
	diff := CompareCollection("小森林（系列）", parts, local, testToday)

	if diff.Bad() {
		t.Fatalf("重剪版被当成缺续集: %v", titles(diff.Missing))
	}
	if want := []string{"小森林：四季"}; !reflect.DeepEqual(titles(diff.Optional), want) {
		t.Fatalf("可选 = %v, 期望 %v", titles(diff.Optional), want)
	}
	if diff.Notes["890406"] == "" {
		t.Fatal("可选条目应带原因")
	}
}

// 实测：本地《恶之教典》，另有早播的电视前传 → 可选。
func TestCompareCollectionPrequelIsOptional(t *testing.T) {
	parts := []FranchisePart{
		part("239458", "恶之教典序章", "2012-10-15", 109),
		part("120852", "恶之教典", "2012-11-10", 129),
	}
	local := map[string]FranchisePart{"120852": parts[1]}
	diff := CompareCollection("恶之教典（系列）", parts, local, testToday)

	if diff.Bad() || len(diff.Optional) != 1 {
		t.Fatalf("前传应判可选: missing=%v optional=%v", titles(diff.Missing), titles(diff.Optional))
	}
}

func TestCompareCollectionComplete(t *testing.T) {
	parts := []FranchisePart{
		part("1", "电锯惊魂", "2004-10-01", 103),
		part("2", "电锯惊魂2", "2005-10-28", 93),
		part("3", "电锯惊魂11", "", 0),
	}
	local := map[string]FranchisePart{"1": parts[0], "2": parts[1]}
	diff := CompareCollection("电锯惊魂系列", parts, local, testToday)

	if diff.Bad() {
		t.Fatalf("齐全的合集被判缺: %v", titles(diff.Missing))
	}
	if len(diff.Local) != 2 || len(diff.Upcoming) != 1 {
		t.Fatalf("统计不对: local=%v upcoming=%v", titles(diff.Local), titles(diff.Upcoming))
	}
	if diff.Parts != 3 {
		t.Fatalf("合集总数 = %d, 期望 3", diff.Parts)
	}
}

func TestCompareCollectionNothingCollected(t *testing.T) {
	parts := []FranchisePart{part("1", "教父", "1972-03-14", 175), part("2", "教父2", "1974-12-20", 202)}
	diff := CompareCollection("教父（系列）", parts, map[string]FranchisePart{}, testToday)

	if len(diff.Missing) != 2 {
		t.Fatalf("整系列未收应全算缺: %v", titles(diff.Missing))
	}
}

func TestSharesSeriesName(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"小森林：四季", "小森林：夏秋篇", true},
		{"恶之教典序章", "恶之教典", true},
		{"天使陷落", "伦敦陷落", false},
		{"奥林匹斯的陷落", "伦敦陷落", false},
		{"", "教父", false},
	}
	for _, c := range cases {
		if got := sharesSeriesName(c.a, c.b); got != c.want {
			t.Fatalf("sharesSeriesName(%q, %q) = %v, 期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// 实测：鬼灭之刃（系列）13 部里大量是按剧集篇章重剪的上映版，
// 早于已收录作品或被标为「…篇」的应降为可选，真续集（三十极夜2）不能被降级。
func TestCompareCollectionRecapFilmsAreOptional(t *testing.T) {
	local2024 := part("1216221", "鬼灭之刃：绊之奇迹，迈向柱训练", "2024-02-02", 105)
	parts := []FranchisePart{
		local2024,
		part("1", "鬼灭之刃：兄妹之绊", "2019-03-29", 113),
		part("2", "鬼灭之刃：那田蜘蛛山篇", "2021-11-17", 138),
		part("3", "鬼灭之刃 游郭潜入篇", "2025-05-23", 122),
		part("4", "鬼灭之刃 刀匠村 敌袭篇", "2025-06-06", 0),
	}
	local := map[string]FranchisePart{"1216221": local2024}
	diff := CompareCollection("鬼灭之刃（系列）", parts, local, testToday)

	if diff.Bad() {
		t.Fatalf("篇章重剪版被当成缺续集: %v", titles(diff.Missing))
	}
	if len(diff.Optional) != 4 {
		t.Fatalf("可选数 = %d, 期望 4 (%v)", len(diff.Optional), titles(diff.Optional))
	}
}

// 回归：同合集名、时长更短的真续集不能被降级为「重剪版」。
func TestCompareCollectionSequelWithSameNameStaysMissing(t *testing.T) {
	first := part("4513", "三十极夜", "2007-10-17", 113)
	parts := []FranchisePart{first, part("42941", "三十极夜2：黑暗的日子", "2010-10-04", 92)}
	local := map[string]FranchisePart{"4513": first}
	diff := CompareCollection("三十极夜（系列）", parts, local, testToday)

	if len(diff.Missing) != 1 || diff.Missing[0].ID != "42941" {
		t.Fatalf("真续集应算缺: missing=%v optional=%v", titles(diff.Missing), titles(diff.Optional))
	}
}
