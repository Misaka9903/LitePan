package mediaprobe

import "testing"

func TestCountReadMarksCapped(t *testing.T) {
	s := newCacheTestService(t)
	s.tokens["tok"] = &sourceToken{maxRead: 10}

	if !s.countRead("tok", 8) {
		t.Fatal("未越过上限时应允许继续读取")
	}
	read, capped := s.sourceStats("tok")
	if read != 8 || capped {
		t.Fatalf("期望 read=8 capped=false，得到 read=%d capped=%v", read, capped)
	}

	if s.countRead("tok", 8) {
		t.Fatal("越过上限时应返回 false 以中断读取")
	}
	read, capped = s.sourceStats("tok")
	if read != 16 || !capped {
		t.Fatalf("越界后应打标，得到 read=%d capped=%v", read, capped)
	}
}

func TestCountReadUnknownToken(t *testing.T) {
	s := newCacheTestService(t)
	if s.countRead("missing", 1) {
		t.Fatal("未知 token 应返回 false")
	}
	read, capped := s.sourceStats("missing")
	if read != 0 || capped {
		t.Fatalf("未知 token 应返回零值，得到 %d %v", read, capped)
	}
}

func TestCountReadExactLimitAllowed(t *testing.T) {
	s := newCacheTestService(t)
	s.tokens["tok"] = &sourceToken{maxRead: 10}
	if !s.countRead("tok", 10) {
		t.Fatal("正好等于上限时应允许（不是越界）")
	}
	if _, capped := s.sourceStats("tok"); capped {
		t.Fatal("正好等于上限不应打标")
	}
}
