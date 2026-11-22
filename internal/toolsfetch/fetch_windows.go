//go:build windows

// Package toolsfetch 在 Windows 首次启动时补齐 ffmpeg / ffprobe。
//
// 仓库不带这两个二进制（各 200MB+），而 Windows 版没有可用的自动下载路径，
// 新机器上媒体探测和封面提取会直接降级。这里在启动后台异步拉一次官方构建，
// 解压出两个 exe 放进 data/tools，后续启动检测到就不再下载。
package toolsfetch

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// gyan.dev 的 essentials 构建，稳定且含 ffmpeg + ffprobe。
	downloadURL = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
	toolDirName = "tools"
	wantTools   = "ffmpeg.exe,ffprobe.exe"
)

var once sync.Once

// Ensure 检查 data/tools 下是否已有 ffmpeg/ffprobe，缺了就在后台下载。
// 只能调用一次；失败只记日志，不影响应用启动。
func Ensure(dataDir string, log *slog.Logger) {
	if log == nil {
		return
	}
	once.Do(func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warn("媒体工具下载异常中断", "err", fmt.Sprint(r))
				}
			}()
			if err := ensure(dataDir, log); err != nil {
				log.Warn("自动获取 ffmpeg/ffprobe 失败，媒体信息探测将不可用", "err", err)
			}
		}()
	})
}

func ensure(dataDir string, log *slog.Logger) error {
	dir := filepath.Join(dataDir, toolDirName)
	missing := make([]string, 0, 2)
	for _, name := range strings.Split(wantTools, ",") {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.IsDir() {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	log.Info("首次启动：缺少媒体工具，开始下载", "url", downloadURL, "missing", missing)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	tmpFile, err := download(ctx, dataDir, log)
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	if err := extract(tmpFile, dir, missing); err != nil {
		return err
	}
	log.Info("媒体工具已就位", "dir", dir, "files", missing)
	return nil
}

// download 把 zip 落到数据目录下的临时文件，返回路径。
func download(ctx context.Context, dataDir string, log *slog.Logger) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(dataDir, "ffmpeg-*.zip")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	st, err := os.Stat(name)
	if err != nil {
		os.Remove(name)
		return "", fmt.Errorf("下载内容异常: %w", err)
	}
	if st.Size() < 1<<20 {
		os.Remove(name)
		return "", fmt.Errorf("下载内容异常（体积 %d 字节）", st.Size())
	}
	log.Info("媒体工具下载完成，开始解压", "bytes", mustSize(name))
	return name, nil
}

// extract 从 zip 里只取目标 exe，直接写到 tools 目录。
func extract(zipPath, dir string, want []string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开压缩包失败: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	found := 0
	for _, f := range zr.File {
		base := strings.ToLower(filepath.Base(f.Name))
		target := ""
		for _, w := range want {
			if strings.EqualFold(base, w) {
				target = filepath.Join(dir, w)
				break
			}
		}
		if target == "" {
			continue
		}
		if err := writeFileFromZip(f, target); err != nil {
			return err
		}
		found++
	}
	if found < len(want) {
		return fmt.Errorf("压缩包里只找到 %d/%d 个文件", found, len(want))
	}
	return nil
}

func writeFileFromZip(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := target + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}

func mustSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}
