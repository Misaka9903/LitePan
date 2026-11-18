package planner

import (
	"context"
	"testing"

	"litepan/internal/mediaprobe"
)

// emptyProber 模拟「探测跑完但没拿到标签」的实现（真实服务会把这种空结果写进缓存）。
type emptyProber struct{}

func (emptyProber) ProbeBucketed(context.Context, mediaprobe.Request) (map[string]string, bool) {
	return nil, false
}

func (emptyProber) ProbeBucketedWithReason(context.Context, mediaprobe.Request) (map[string]string, *mediaprobe.Failure) {
	return nil, nil
}

func TestPrecheckProbeEmptyResultStillBlocks(t *testing.T) {
	p := &Planner{prober: emptyProber{}, probeMode: "prefer", mediaTagOrder: fullTagOrder()}
	if failure := p.precheckProbeGroup(groupKey{}, probeTestItems(3)); failure == nil {
		t.Fatal("拿不到标签时应整组跳过，不能静默回退文件名")
	}
}

// canceledProber 模拟任务被停止时的探测。
type canceledProber struct{}

func (canceledProber) ProbeBucketed(context.Context, mediaprobe.Request) (map[string]string, bool) {
	return nil, false
}

func (canceledProber) ProbeBucketedWithReason(context.Context, mediaprobe.Request) (map[string]string, *mediaprobe.Failure) {
	return nil, &mediaprobe.Failure{Reason: mediaprobe.ReasonCanceled, Detail: "任务已停止"}
}

func TestPrecheckProbeCanceledReportsCanceled(t *testing.T) {
	p := &Planner{prober: canceledProber{}, probeMode: "prefer", mediaTagOrder: fullTagOrder()}
	failure := p.precheckProbeGroup(groupKey{}, probeTestItems(3))
	if failure == nil || failure.Reason != mediaprobe.ReasonCanceled {
		t.Fatalf("停止任务应报 canceled，实际 %+v", failure)
	}
}
