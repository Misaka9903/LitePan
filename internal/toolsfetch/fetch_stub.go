//go:build !windows

// Package toolsfetch 在非 Windows 平台不做任何事：
// Docker / 裸机部署由镜像或系统包提供 ffmpeg。
package toolsfetch

import "log/slog"

// Ensure 在非 Windows 上是空实现。
func Ensure(dataDir string, log *slog.Logger) {}
