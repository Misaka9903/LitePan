//go:build !windows

// Package procattr 统一设置子进程的窗口行为。
package procattr

import "os/exec"

// Hide 在非 Windows 平台是空实现。
func Hide(cmd *exec.Cmd) *exec.Cmd { return cmd }
