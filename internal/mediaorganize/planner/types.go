package planner

import (
	"context"

	"litepan/internal/domain"
	"litepan/internal/mediaprobe"
)

// MediaProber 用真实文件头补全媒体标签，实现见 internal/mediaprobe。
type MediaProber interface {
	ProbeBucketed(ctx context.Context, req mediaprobe.Request) (map[string]string, bool)
}

// DetailedProber 是可选的扩展接口：失败时能给出分类原因，供日志与汇总区分责任方。
// 实现方（internal/mediaprobe.Service）同时实现两个接口，测试替身只需实现 MediaProber。
type DetailedProber interface {
	ProbeBucketedWithReason(ctx context.Context, req mediaprobe.Request) (map[string]string, *mediaprobe.Failure)
}

type FileService interface {
	List(ctx context.Context, accountID int64, parentID string, forceRefresh bool) ([]domain.FileItem, error)
}

type LogFunc func(string)

type StopFunc func() error

type ProgressFunc func(Progress)

type Progress struct {
	Stage        string `json:"stage"`
	ScannedDirs  int    `json:"scanned_dirs"`
	ScannedFiles int    `json:"scanned_files"`
	Groups       int    `json:"groups"`
	Actions      int    `json:"actions"`
	Skipped      int    `json:"skipped"`
	CurrentDir   string `json:"current_dir"`
	PlannedWorks int    `json:"planned_works"`
	MaxWorks     int    `json:"max_works"`
	QuotaReached bool   `json:"quota_reached"`
	AITotal      int    `json:"ai_total"`
	AICompleted  int    `json:"ai_completed"`
	AICached     int    `json:"ai_cached"`
	AIFailed     int    `json:"ai_failed"`
	AIChunk      int    `json:"ai_chunk"`
	AIChunks     int    `json:"ai_chunks"`
	AIBatchSize  int    `json:"ai_batch_size"`
	AISplitDepth int    `json:"ai_split_depth"`
	AIStartedAt  int64  `json:"ai_attempt_started_at"`
	AITimeout    int    `json:"ai_attempt_timeout_seconds"`
	AIRetrying   bool   `json:"ai_retrying"`
}

type TaskConfig struct {
	TargetDirectoryID    string
	TargetRootID         string
	ActionType           string
	MediaType            string
	RenameMarker         string
	UseTMDB              bool
	OverwriteExisting    bool
	Recursive            bool
	SeasonFolderTemplate string
	FileExtensions       string
	MetadataExtensions   string
}

type Settings map[string]any

var ErrStopped = stopError{}

type stopError struct{}

func (stopError) Error() string { return "planner stopped" }
