package mediaprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"litepan/internal/playback"
)

const (
	// probeMaxRead 是单次探测允许从网盘读取的上限，不是每次都会读这么多：
	// ffmpeg 解析完容器头就退出，普通文件 1-3MB 即结束。
	// 上限只是安全闸——既防异常驱动整片拉流，也要够容纳「多字幕轨 + 内嵌字体」的头部
	// （实测某番 MKV 已用到 7.9MB）。大文件（几十 GB 的 remux）头部并不会等比变大，
	// 留 64MB 的余量足够，正常片源不会因此多读。
	probeMaxRead = 64 << 20
	// probeTimeout 单次探测超时；限速严重时宁可跳过也不要拖住整理任务。
	probeTimeout = 20 * time.Second
	// probeWindow 是 ffmpeg 的头部探测窗口，与 probeMaxRead 一起控制读取量。
	probeWindow = "5M"
	// tokenTTL 是临时取流入口的有效期。
	tokenTTL = 10 * time.Minute
	// outputLimit 限制保留的 ffmpeg stderr 字节数。
	outputLimit = 256 << 10

	// cacheTTL 是探测结果在进程内的有效期：片源可能被替换，过期后重新读一次文件头。
	cacheTTL = 30 * 24 * time.Hour
	// cacheMaxItems 限制缓存条数（单条约 200 字节），超出按最近一次探测时间淘汰。
	cacheMaxItems = 5000
)

// Options 是探测服务的依赖。
type Options struct {
	DataDir  string
	Playback *playback.Service
	Log      *slog.Logger
}

type sourceToken struct {
	accountID int64
	fileID    string
	expires   time.Time
	maxRead   int64
	read      int64
	// capped 表示读取曾撞到上限，ffmpeg 的输出可能因此残缺。
	capped bool
}

// cacheSchemaVersion 变更标签字段集合时递增，旧缓存自动失效。
const cacheSchemaVersion = 2

type cacheEntry struct {
	Version int               `json:"version,omitempty"`
	OK      bool              `json:"ok"`
	Tags    map[string]string `json:"tags,omitempty"`
	At      int64             `json:"at"`
	FileID  string            `json:"file_id,omitempty"`
	Size    int64             `json:"size,omitempty"`
}

// Service 读取网盘文件头补全媒体标签，并按桶缓存结果。
type Service struct {
	opts    Options
	log     *slog.Logger
	binary  string
	useJSON bool
	addr    string
	server  *http.Server
	sem     chan struct{}

	mu     sync.Mutex
	tokens map[string]*sourceToken
	// cache 只在进程内有效：重启后重新探测，天然不存在陈旧数据。
	cache map[string]cacheEntry
}

// New 创建探测服务。找不到 ffmpeg 时服务仍可用，只是每次探测都返回「不可用」。
func New(opts Options) (*Service, error) {
	if opts.Playback == nil {
		return nil, errors.New("媒体信息探测服务缺少播放层")
	}
	if strings.TrimSpace(opts.DataDir) == "" {
		return nil, errors.New("媒体信息探测服务缺少数据目录")
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		opts:   opts,
		log:    log,
		sem:    make(chan struct{}, 2),
		tokens: make(map[string]*sourceToken),
		cache:  make(map[string]cacheEntry),
	}
	if binary, err := findProbeBinary(opts.DataDir); err == nil {
		s.binary = binary
		s.useJSON = strings.Contains(filepath.Base(binary), "ffprobe")
		if s.useJSON {
			log.Info("媒体信息探测使用 ffprobe（JSON）", "path", binary)
		} else {
			log.Warn("媒体信息探测回退 ffmpeg 文本解析：未找到 ffprobe，动态范围/色深可能不准", "path", binary)
		}
	} else {
		log.Info("媒体信息探测不可用：未找到 ffprobe/ffmpeg", "error", err)
	}
	if err := s.startServer(); err != nil {
		return nil, err
	}
	return s, nil
}

// findProbeBinary 优先找 ffprobe（结构化 JSON），找不到再退回 ffmpeg（文本解析）。
func findProbeBinary(dataDir string) (string, error) {
	for _, name := range []string{"ffprobe", "ffmpeg"} {
		bin := name
		if runtime.GOOS == "windows" {
			bin = name + ".exe"
		}
		local := filepath.Join(dataDir, "tools", bin)
		if st, err := os.Stat(local); err == nil && !st.IsDir() {
			return local, nil
		}
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("未找到 ffprobe 或 ffmpeg")
}

// startServer 起一个只监听本机的取流入口。
// 复用 playback.ServeHTTP 而不是把网易盘直链交给 ffmpeg：
// 鉴权头不出本机，读取量也能被精确截断。
func (s *Service) startServer() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/source/", s.serveSource)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.server = server
	go func() { _ = server.Serve(ln) }()
	return nil
}

// Close 停止取流入口。探测缓存只在进程内，无需落盘或清理。
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// ProbeBucketed 返回一个桶的探测标签；已探测过的桶直接命中缓存。
// ok=false 表示本次无法提供探测结果，调用方应回退文件名标签。
func (s *Service) ProbeBucketed(ctx context.Context, req Request) (map[string]string, bool) {
	if s == nil || s.binary == "" {
		return nil, false
	}
	if IsSampleName(req.FileName) {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := BucketKey(req)
	if tags, done := s.cached(key, req); done {
		return tags, len(tags) > 0
	}

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, false
	}
	defer func() { <-s.sem }()

	// 并发下同一桶可能排队两次，进入临界区后再查一次。
	if tags, done := s.cached(key, req); done {
		return tags, len(tags) > 0
	}

	info, err := s.probe(ctx, req.AccountID, req.FileID, req.Size)
	if err != nil {
		s.log.Debug("媒体信息探测失败，回退文件名标签", "file", req.FileName, "error", err)
		s.store(key, req, nil)
		return nil, false
	}
	tags := info.Tags()
	if len(tags) == 0 {
		s.store(key, req, nil)
		return nil, false
	}
	s.store(key, req, tags)
	return tags, true
}

// cached 返回缓存结果。done=true 表示该桶已探测过（成功或失败），不必再试。
func (s *Service) cached(key string, req Request) (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	if entry.Version != cacheSchemaVersion {
		delete(s.cache, key)
		return nil, false
	}
	if entry.At > 0 && time.Since(time.Unix(entry.At, 0)) > cacheTTL {
		delete(s.cache, key)
		return nil, false
	}
	// 抽样过的那个文件被换源/重下：size 变了，旧标签不再可信。
	// 桶内其它文件 size 本来就不同，只在同一个 fileID 上比较。
	if entry.FileID != "" && entry.FileID == req.FileID && entry.Size > 0 && req.Size > 0 && entry.Size != req.Size {
		delete(s.cache, key)
		return nil, false
	}
	if !entry.OK || len(entry.Tags) == 0 {
		return nil, true
	}
	return entry.Tags, true
}

func (s *Service) store(key string, req Request, tags map[string]string) {
	s.mu.Lock()
	s.cache[key] = cacheEntry{
		Version: cacheSchemaVersion,
		OK:      tags != nil,
		Tags:    tags,
		At:      time.Now().Unix(),
		FileID:  req.FileID,
		Size:    req.Size,
	}
	s.evictLocked()
	s.mu.Unlock()
}

// evictLocked 超出上限时按探测时间从旧到新淘汰。
func (s *Service) evictLocked() {
	excess := len(s.cache) - cacheMaxItems
	if excess <= 0 {
		return
	}
	type cacheItem struct {
		key string
		at  int64
	}
	items := make([]cacheItem, 0, len(s.cache))
	for key, entry := range s.cache {
		items = append(items, cacheItem{key: key, at: entry.At})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at < items[j].at })
	for i := 0; i < excess && i < len(items); i++ {
		delete(s.cache, items[i].key)
	}
}

func (s *Service) newSource(accountID int64, fileID string, size int64) (string, string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(buf)
	maxRead := int64(probeMaxRead)
	if size > 0 && size < maxRead {
		maxRead = size
	}
	s.mu.Lock()
	s.tokens[token] = &sourceToken{accountID: accountID, fileID: fileID, expires: time.Now().Add(tokenTTL), maxRead: maxRead}
	s.mu.Unlock()
	return token, "http://" + s.addr + "/source/" + token, nil
}

// countRead 累加读取字节并判断是否越过上限；越界时打标，供调用方判断结果是否可信。
func (s *Service) countRead(token string, n int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.tokens[token]
	if cur == nil {
		return false
	}
	cur.read += n
	if cur.read > cur.maxRead {
		cur.capped = true
		return false
	}
	return true
}

// sourceStats 返回某个临时入口已读取的字节数以及是否撞过上限。
func (s *Service) sourceStats(token string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tokens[token]; t != nil {
		return t.read, t.capped
	}
	return 0, false
}

func (s *Service) dropToken(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

func (s *Service) serveSource(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/source/")
	s.mu.Lock()
	current := s.tokens[token]
	s.mu.Unlock()
	if current == nil || time.Now().After(current.expires) {
		http.Error(w, "expired", http.StatusGone)
		return
	}
	cw := &cappedWriter{ResponseWriter: w, add: func(n int64) bool {
		return s.countRead(token, n)
	}}
	err := s.opts.Playback.ServeHTTP(cw, r, playback.Request{AccountID: current.accountID, FileID: current.fileID}, playback.Intent{
		FileName:       "probe",
		OriginalFile:   true,
		ForceProxy:     true,
		SkipRangeLimit: true,
	})
	if err != nil {
		s.log.Debug("媒体信息探测取流失败", "error", err)
	}
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// cappedWriter 在超过读取上限时中断响应，防止异常驱动整片下载。
type cappedWriter struct {
	http.ResponseWriter
	add func(int64) bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if !w.add(int64(len(p))) {
		return 0, errors.New("媒体信息探测读取量超过上限")
	}
	return w.ResponseWriter.Write(p)
}

// cappedBuffer 只保留 ffmpeg 输出的前若干字节，避免异常输出撑爆内存。
type cappedBuffer struct {
	data []byte
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(b.data) < outputLimit {
		room := outputLimit - len(b.data)
		if len(p) > room {
			b.data = append(b.data, p[:room]...)
		} else {
			b.data = append(b.data, p...)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.data) }

func (b *cappedBuffer) Bytes() []byte { return b.data }

func (s *Service) probe(ctx context.Context, accountID int64, fileID string, size int64) (Info, error) {
	if s.binary == "" {
		return Info{}, errors.New("未找到 ffmpeg")
	}
	token, url, err := s.newSource(accountID, fileID, size)
	if err != nil {
		return Info{}, err
	}
	defer s.dropToken(token)
	started := time.Now()

	runCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// 只读文件头：不指定输出，解析完流信息即退出，不会转码也不会下载整片。
	var stdout, stderr cappedBuffer
	var runErr error
	if s.useJSON {
		cmd := exec.CommandContext(runCtx, s.binary,
			"-v", "quiet", "-print_format", "json", "-show_streams", "-show_format",
			"-probesize", probeWindow, "-analyzeduration", "5000000",
			url,
		)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr = cmd.Run()
	} else {
		cmd := exec.CommandContext(runCtx, s.binary,
			"-hide_banner", "-nostdin", "-loglevel", "info",
			"-probesize", probeWindow, "-analyzeduration", probeWindow,
			"-i", url,
		)
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr
		runErr = cmd.Run()
	}

	read, capped := s.sourceStats(token)
	var info Info
	if s.useJSON {
		parsed, err := ParseJSON(stdout.Bytes())
		if err != nil {
			s.log.Info("ffprobe 输出解析失败", "file_id", fileID, "error", err.Error(), "run_error", errString(runErr))
			return Info{}, errors.New("ffprobe 输出无法解析")
		}
		info = parsed
	} else {
		info = ParseOutput(stderr.String())
	}
	if info.Empty() {
		s.log.Info("媒体信息探测未取到信息", "file_id", fileID, "read_bytes", read, "elapsed", time.Since(started).String())
		return Info{}, errors.New("ffprobe/ffmpeg 未返回可用媒体信息")
	}
	s.log.Info("媒体信息探测完成", "file_id", fileID, "read_bytes", read, "elapsed", time.Since(started).String())
	if capped {
		// 撞上限说明 ffmpeg 是被硬切断的，流信息可能只解析到一半。
		s.log.Warn("探测读取达到上限被中断，结果可能不完整",
			"file_id", fileID, "read_bytes", read, "limit_bytes", int64(probeMaxRead))
	}
	return info, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
