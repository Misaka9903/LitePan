package mediaprobe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarizeStderrKeepsValidUTF8(t *testing.T) {
	// 中文按字符截断；按字节截会把最后一个字劈成两半，日志里出现乱码。
	raw := strings.Repeat("结构异常", 120)
	got := summarizeStderr(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("截断后应是合法 UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 201 { // 200 字 + 省略号
		t.Fatalf("应截到 200 字，实际 %d", n)
	}
}

func TestSummarizeStderrShortTextUnchanged(t *testing.T) {
	got := summarizeStderr("EBML header parsing failed")
	if got != "EBML header parsing failed" {
		t.Fatalf("短文本应原样保留，实际 %q", got)
	}
}
