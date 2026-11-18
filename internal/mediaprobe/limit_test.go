package mediaprobe

import "testing"

func TestReadLimitUsesSetting(t *testing.T) {
	s := &Service{opts: Options{MaxReadMB: func() int64 { return 32 }}}
	if got := s.readLimit(0); got != 32<<20 {
		t.Fatalf("应按设置取 32MB，实际 %d", got)
	}
	// 文件比上限小时按文件大小读，避免多读不存在的字节。
	if got := s.readLimit(1 << 20); got != 1<<20 {
		t.Fatalf("小文件应按文件大小，实际 %d", got)
	}

	// 没接设置时回落到默认值。
	bare := &Service{}
	if got := bare.readLimit(0); got != int64(probeMaxRead) {
		t.Fatalf("默认应为 probeMaxRead，实际 %d", got)
	}
}
