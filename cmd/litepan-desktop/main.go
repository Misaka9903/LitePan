//go:build windows

// Command litepan-desktop 是 LitePan 的 Windows 桌面版入口。
//
// 与 cmd/litepan 的区别：没有控制台窗口，常驻系统托盘，
// 启动完成后自动用默认浏览器打开面板。后端逻辑完全复用 internal/app。
package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	_ "modernc.org/sqlite"

	"litepan/internal/app"
	"litepan/internal/config"
	"litepan/internal/logx"

	_ "litepan/drivers"
)

//go:embed icon.ico
var iconData []byte

const (
	appName        = "LitePan"
	defaultPort    = 5211
	portScanLimit  = 20
	readyTimeout   = 40 * time.Second
	shutdownBudget = 25 * time.Second
	urlFileName    = "desktop.url"
)

type options struct {
	port       int
	dataDir    string
	lan        bool
	noBrowse   bool
	restarting bool
	portSet    bool
	lanSet     bool
}

var restartRequested bool

func main() {
	// 必须在任何 HTTP 请求之前：Go 只认 HTTP_PROXY 环境变量，不读系统代理注册表。
	proxy := applySystemProxy()

	opts := parseFlags()

	// 单实例：第二个实例只负责把已经跑起来的面板打开，然后退出。
	// --restarting 是自重启拉起的新进程，它要等旧进程把手里的锁放掉。
	ok := acquireInstance(opts.dataDir)
	if !ok && opts.restarting {
		for i := 0; i < 60 && !ok; i++ {
			time.Sleep(500 * time.Millisecond)
			ok = acquireInstance(opts.dataDir)
		}
	}
	if !ok {
		openURL(waitForURL(opts.dataDir))
		return
	}
	defer releaseInstance()

	ensureAutoStartSilent()

	// 端口和局域网访问可以在设置里改，落库后下次启动生效。
	fillFromSettings(&opts)

	code := run(opts, proxy)
	if restartRequested {
		spawnRestart()
	}
	os.Exit(code)
}

// applySystemProxy 把 Windows 系统代理翻译成进程级环境变量。
// Go 的 http.ProxyFromEnvironment 只读 HTTP_PROXY/HTTPS_PROXY，完全不看
// WinINET 的注册表设置，所以 Clash 这类"系统代理"默认对 LitePan 无效。
// 返回实际生效的代理地址（空表示直连），仅用于写日志。
func applySystemProxy() string {
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		return "" // 用户显式配了环境变量，尊重他的选择
	}
	httpProxy, httpsProxy, ok := systemProxyFromRegistry()
	if !ok {
		return ""
	}
	_ = os.Setenv("HTTP_PROXY", httpProxy)
	_ = os.Setenv("HTTPS_PROXY", httpsProxy)
	if os.Getenv("NO_PROXY") == "" {
		// 内网与回环直连；Go 本身也会跳过 localhost/loopback，这里只是补齐内网段。
		_ = os.Setenv("NO_PROXY", "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16")
	}
	return httpProxy
}

// fillFromSettings 用数据库里的桌面版设置补齐没有被命令行覆盖的项。
func fillFromSettings(o *options) {
	if o.portSet && o.lanSet {
		return
	}
	dbPath := filepath.Join(o.dataDir, "litepan.db")
	if _, err := os.Stat(dbPath); err != nil {
		return // 首次运行，库还没建，用默认值
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	if !o.portSet {
		var v string
		if err := db.QueryRow(`select value from configs where key = 'desktop_listen_port'`).Scan(&v); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p >= 1024 && p <= 65535 {
				o.port = p
			}
		}
	}
	if !o.lanSet {
		var v string
		if err := db.QueryRow(`select value from configs where key = 'desktop_lan_access'`).Scan(&v); err == nil {
			o.lan = strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
}

const (
	autoRunKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	autoRunName = "LitePan"
)

// isAutoStartEnabled 判断当前 exe 是否已在 HKCU 的 Run 键里。
func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autoRunName)
	if err != nil {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// setAutoStart 写入或移除开机自启项。只动当前用户，不需要管理员权限。
func setAutoStart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(autoRunName); err != nil {
			return nil // 本来就不存在
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 开机启动时静默驻留托盘，不主动拉起浏览器。
	return k.SetStringValue(autoRunName, `"`+exe+`" --no-browser`)
}

// ensureAutoStartSilent 把旧版本写入的自启项补上 --no-browser，
// 避免升级后开机仍然抢前台。
func ensureAutoStartSilent() {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autoRunName)
	if err != nil || strings.Contains(v, "--no-browser") {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = k.SetStringValue(autoRunName, `"`+exe+`" --no-browser`)
}

// spawnRestart 拉起一个新实例。此时旧进程已经完成 shutdown，端口已释放，
// 新进程只需短暂等待单实例锁随旧进程退出而释放。
func spawnRestart() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		if a == "--restarting" {
			continue // 多次重启不累积该参数
		}
		args = append(args, a)
	}
	args = append(args, "--restarting")
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	_ = cmd.Start()
}

// systemProxyFromRegistry 读取当前用户的 WinINET 代理设置。
func systemProxyFromRegistry() (httpProxy, httpsProxy string, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer k.Close()

	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return "", "", false
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return "", "", false
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return "", "", false
	}

	// ProxyServer 可能是 "host:port"，也可能是 "http=host:port;https=host:port"。
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		switch {
		case strings.HasPrefix(lower, "http="):
			httpProxy = strings.TrimSpace(part[5:])
		case strings.HasPrefix(lower, "https="):
			httpsProxy = strings.TrimSpace(part[6:])
		case strings.HasPrefix(lower, "socks="):
			// Go 的 ProxyFromEnvironment 不支持 socks，跳过。
		default:
			if httpProxy == "" && httpsProxy == "" {
				httpProxy, httpsProxy = part, part
			}
		}
	}
	if httpProxy == "" && httpsProxy == "" {
		return "", "", false
	}
	if httpProxy == "" {
		httpProxy = httpsProxy
	}
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	return withProxyScheme(httpProxy), withProxyScheme(httpsProxy), true
}

func withProxyScheme(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

func parseFlags() options {
	port := flag.Int("port", defaultPort, "HTTP 监听端口（被占用时自动向后顺延）")
	dataDir := flag.String("data-dir", "", "数据目录（默认 exe 同级的 data 目录；目录不可写时用 %LOCALAPPDATA%\\LitePan）")
	lan := flag.Bool("lan", false, "监听所有网卡，允许局域网内其他设备访问")
	noBrowse := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	restarting := flag.Bool("restarting", false, "内部使用：等待旧实例释放单实例锁")
	flag.Parse()

	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	dir := strings.TrimSpace(*dataDir)
	if dir == "" {
		dir = defaultDataDir()
	}
	return options{
		port:       *port,
		dataDir:    filepath.Clean(dir),
		lan:        *lan,
		noBrowse:   *noBrowse,
		restarting: *restarting,
		portSet:    set["port"],
		lanSet:     set["lan"],
	}
}

// defaultDataDir 默认走便携模式：数据放在 exe 同级的 data 目录，
// 整个文件夹拷走即可迁移。只有 exe 所在目录不可写（Program Files、
// 只读介质）时才退回 %LOCALAPPDATA%。
func defaultDataDir() string {
	if v := strings.TrimSpace(os.Getenv("LITEPAN_DATA_DIR")); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		if dirWritable(base) {
			return filepath.Join(base, "data")
		}
	}
	if v := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); v != "" {
		return filepath.Join(v, appName)
	}
	return "./data"
}

// dirWritable 用创建临时文件的方式判断目录是否可写。
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func run(o options, proxy string) int {
	cfg := config.Load()
	cfg.DataDir = o.dataDir
	cfg.DBPath = filepath.Join(o.dataDir, "litepan.db")
	if strings.TrimSpace(os.Getenv("LITEPAN_STRM_DIR")) == "" {
		cfg.StrmDir = config.StrmDirForData(o.dataDir)
	}

	host := "127.0.0.1"
	if o.lan {
		host = "0.0.0.0"
	}
	port := pickPort(o.port, host)
	cfg.ListenAddr = fmt.Sprintf("%s:%d", host, port)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)

	logs, err := logx.New(logx.Options{
		Dir:   filepath.Join(cfg.DataDir, "log"),
		Level: cfg.LogLevel,
	})
	if err != nil {
		messageBox("无法创建日志目录：\n" + err.Error())
		return 1
	}
	if proxy != "" {
		logs.Root().Info("使用 Windows 系统代理", "proxy", proxy)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := app.New(ctx, app.Options{Config: cfg, Logs: logs})
	if err != nil {
		logs.Root().Error("启动失败", "err", err)
		messageBox("启动失败：\n" + err.Error())
		return 1
	}

	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	writeURLFile(cfg.DataDir, url)

	go func() {
		if !waitReady(url, readyTimeout) {
			logs.Root().Warn("等待服务就绪超时，浏览器可能打不开面板", "url", url)
			return
		}
		if !o.noBrowse {
			openURL(url)
		}
	}()

	systray.Run(func() { onReady(o, url, logs) }, func() {})

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, app.ErrRestartRequested) {
			logs.Root().Error("运行出错", "err", err)
		}
	case <-time.After(10 * time.Second):
	}

	shCtx, shCancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer shCancel()
	if err := a.Shutdown(shCtx); err != nil {
		logs.Root().Error("关闭出错", "err", err)
		return 1
	}
	return 0
}

func onReady(o options, url string, logs *logx.Manager) {
	systray.SetIcon(iconData)
	systray.SetTooltip("LitePan · " + url)
	logs.Root().Info("托盘已就绪", "url", url, "data_dir", o.dataDir)

	mOpen := systray.AddMenuItem("打开面板", url)
	mData := systray.AddMenuItem("打开数据目录", o.dataDir)
	mLog := systray.AddMenuItem("打开日志", filepath.Join(o.dataDir, "log"))
	mRestart := systray.AddMenuItem("重启", "重启后新的端口/局域网设置生效")
	mAutoStart := systray.AddMenuItemCheckbox("开机自启", "登录 Windows 后自动启动 LitePan", isAutoStartEnabled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "停止 LitePan 并关闭服务")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openURL(url)
			case <-mData.ClickedCh:
				openPath(o.dataDir)
			case <-mLog.ClickedCh:
				openPath(filepath.Join(o.dataDir, "log"))
			case <-mAutoStart.ClickedCh:
				if err := setAutoStart(!isAutoStartEnabled()); err == nil {
					if isAutoStartEnabled() {
						mAutoStart.Check()
					} else {
						mAutoStart.Uncheck()
					}
				}
			case <-mRestart.ClickedCh:
				restartRequested = true
				systray.Quit()
				return
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// pickPort 从 preferred 开始找第一个能监听的端口。
func pickPort(preferred int, host string) int {
	for p := preferred; p < preferred+portScanLimit; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return preferred
}

// waitReady 轮询面板首页，直到返回非 5xx 或超时。直连，不走系统代理。
func waitReady(url string, timeout time.Duration) bool {
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   3 * time.Second,
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return true
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// waitForURL 在第二个实例里读取第一个实例写下的面板地址。
func waitForURL(dataDir string) string {
	path := filepath.Join(dataDir, urlFileName)
	// 第一个实例可能还在初始化，给它 10 秒把地址写出来。
	for i := 0; i < 20; i++ {
		if b, err := os.ReadFile(path); err == nil {
			if u := strings.TrimSpace(string(b)); u != "" {
				return u
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ""
}

func writeURLFile(dataDir, url string) {
	path := filepath.Join(dataDir, urlFileName)
	_ = os.WriteFile(path, []byte(url), 0o644)
}

func openURL(url string) {
	if url == "" {
		return
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func openPath(path string) {
	_ = exec.Command("explorer", path).Start()
}

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
	lockFile        *os.File
)

// messageBox 在没有控制台的情况下把致命错误显示出来。
func messageBox(text string) {
	title, _ := windows.UTF16PtrFromString(appName)
	body, _ := windows.UTF16PtrFromString(text)
	const mbIconError = 0x00000010
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(body)),
		uintptr(unsafe.Pointer(title)),
		mbIconError)
}

// acquireInstance 用独占文件锁做单实例判断。
// 不用 CreateMutex + GetLastError：Go 的 goroutine 会在 OS 线程间迁移，
// 两次系统调用之间 last error 并不可靠。
// 文件锁在进程退出（包括崩溃）时由系统自动释放，不会留下僵尸锁。
func acquireInstance(dataDir string) bool {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return true // 目录都建不了，交给后续流程报错
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "desktop.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{},
	)
	if err != nil {
		_ = f.Close()
		return false
	}
	lockFile = f
	return true
}

func releaseInstance() {
	if lockFile == nil {
		return
	}
	_ = windows.UnlockFileEx(windows.Handle(lockFile.Fd()), 0, 1, 0, &windows.Overlapped{})
	_ = lockFile.Close()
	lockFile = nil
}
