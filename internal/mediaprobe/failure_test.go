package mediaprobe

import (
	"strings"
	"testing"
)

func TestClassifyFailurePrefersContainerEvidence(t *testing.T) {
	// 数据完整（没读满）时，结构错误才指向文件本身。
	stderr := "[matroska,webm] Duplicate element\n[matroska,webm] Element at 0x35 ending at 0x34df5 exceeds containing master element ending at 0x13e1"
	got := classifyFailure(summarizeStderr(stderr), 1, false, false, "", 5<<20, probeMaxRead)
	if got != ReasonContainerInvalid {
		t.Fatalf("应判为文件结构异常，实际 %s", got)
	}
}

func TestClassifyFailureCappedIgnoresBrokenHints(t *testing.T) {
	// 读取被上限切断时，解析器同样会吐出 Duplicate element 之类的措辞，
	// 那是截断的副产品，不能据此把文件判成结构损坏。
	stderr := "[matroska,webm] Duplicate element\n[matroska,webm] Element at 0x35 ending at 0x34df5 exceeds containing master element ending at 0x13e1"
	got := classifyFailure(summarizeStderr(stderr), 1, false, true, "", probeMaxRead+1024, probeMaxRead)
	if got != ReasonReadCapped {
		t.Fatalf("截断时应判 read_capped，实际 %s", got)
	}
}

func TestClassifyFailureTimeout(t *testing.T) {
	if got := classifyFailure("", -1, true, false, "", 0, probeMaxRead); got != ReasonTimeout {
		t.Fatalf("超时应判 timeout，实际 %s", got)
	}
}

func TestClassifyFailureSourceError(t *testing.T) {
	// 只读了 300KB 就报取流错误，说明网盘侧没给数据。
	got := classifyFailure("Server returned 403 Forbidden", 1, false, false, "上行失败", 300<<10, probeMaxRead)
	if got != ReasonSourceError {
		t.Fatalf("应判为网盘取流失败，实际 %s", got)
	}
}

func TestClassifyFailureReadCapped(t *testing.T) {
	got := classifyFailure("", 1, false, true, "", probeMaxRead+1024, probeMaxRead)
	if got != ReasonReadCapped {
		t.Fatalf("读满上限且无结构证据应判 read_capped，实际 %s", got)
	}
}

func TestClassifyFailureNoStreams(t *testing.T) {
	got := classifyFailure("", 0, false, false, "", 1024, probeMaxRead)
	if got != ReasonNoStreams {
		t.Fatalf("正常退出但无流应判 no_streams，实际 %s", got)
	}
}

func TestSummarizeStderrTrimsNoise(t *testing.T) {
	raw := "[matroska,webm @ 0x3e819800] Duplicate element\n[matroska,webm @ 0x3e819800] open https://example.invalid/x failed"
	got := summarizeStderr(raw)
	if strings.Contains(got, "0x3e819800") {
		t.Fatalf("内存地址应被去掉: %s", got)
	}
	if strings.Contains(got, "example.invalid") {
		t.Fatalf("链接应被替换: %s", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("应压成一行: %s", got)
	}
}

func TestFailureLabels(t *testing.T) {
	cases := map[string]string{
		ReasonContainerInvalid: "文件结构异常",
		ReasonSourceError:      "网盘取流失败",
		ReasonReadCapped:       "读取超限未解析",
		ReasonProbeUnavailable: "本机缺少 ffprobe",
		"whatever":             "原因未知",
	}
	for reason, want := range cases {
		if got := ReasonLabel(reason); got != want {
			t.Fatalf("%s 标签应为 %s，实际 %s", reason, want, got)
		}
	}
}
