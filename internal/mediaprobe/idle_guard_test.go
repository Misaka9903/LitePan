package mediaprobe

import (
	"testing"
	"time"
)

func TestShouldIdleOut(t *testing.T) {
	cases := []struct {
		name   string
		idle   time.Duration
		capped bool
		want   bool
	}{
		{"刚读到数据", time.Second, false, false},
		{"空闲超过阈值", probeIdleTimeout + time.Second, false, true},
		{"撞上限后不再读", probeIdleTimeout + time.Minute, true, false},
		{"撞上限但未超阈值", time.Second, true, false},
	}
	for _, c := range cases {
		if got := shouldIdleOut(c.idle, c.capped); got != c.want {
			t.Fatalf("%s: shouldIdleOut(%v, %v) = %v，期望 %v", c.name, c.idle, c.capped, got, c.want)
		}
	}
}
