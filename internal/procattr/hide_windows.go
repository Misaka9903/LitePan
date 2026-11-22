//go:build windows

// Package procattr 统一设置子进程的窗口行为。
package procattr

import (
	"os/exec"
	"syscall"
)

// Hide 让子进程不创建控制台窗口。
// 桌面版本身是 -H windowsgui，但 ffmpeg/ffprobe 这类控制台程序
// 被拉起时 Windows 默认会给它们开一个黑框。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
