package mediaprobe

import (
	"testing"
	"time"
)

func TestIdleForTracksLastRead(t *testing.T) {
	s := &Service{tokens: make(map[string]*sourceToken)}
	s.tokens["t"] = &sourceToken{maxRead: 1 << 30, lastRead: time.Now().Add(-30 * time.Second)}

	if d := s.idleFor("t"); d < 29*time.Second {
		t.Fatalf("应反映 30 秒未读，实际 %v", d)
	}
	if d := s.idleFor("missing"); d != 0 {
		t.Fatalf("未知令牌应返回 0，实际 %v", d)
	}

	// 收到数据后空闲计时清零。
	s.countRead("t", 1024)
	if d := s.idleFor("t"); d > time.Second {
		t.Fatalf("读到数据后应刷新时间戳，实际 %v", d)
	}
}

func TestIdleTimeoutConstants(t *testing.T) {
	// 空闲阈值必须明显小于总上限，否则总上限会先命中，空闲检测形同虚设。
	if probeIdleTimeout >= probeTotalTimeout {
		t.Fatalf("空闲阈值 %v 不应大于总上限 %v", probeIdleTimeout, probeTotalTimeout)
	}
	if probeIdleTimeout < 5*time.Second {
		t.Fatalf("空闲阈值过短，慢速网盘会被误判：%v", probeIdleTimeout)
	}
}
