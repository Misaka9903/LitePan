package mediaprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"litepan/internal/playback"
)

const (
	// probeMaxRead 是单次探测读取上限的默认值，实际以设置 mo_probe_max_read_mb 为准。
	// 它不是「每次都会读这么多」：ffmpeg 解析完容器头就退出，普通文件 1-3MB 即结束。
	// 上限只是安全闸——既防异常驱动整片拉流，也要够容纳「多字幕轨 + 内嵌字体」的头部
	// （实测某番 MKV 已用到 7.9MB）。大文件（几十 GB 的 remux）头部并不会等比变大。
	// 但并非所有 mkv 都把 Tracks 放在开头：实测某 BDRip 需要读到 98MB 才拿到轨信息
	// （Cues 在文件尾部，SeekHead 又指不准），64MB 会在半路切断，所以默认给 192MB。
	probeMaxRead = 192 << 20
	// probeTotalTimeout 是单次探测的总时间上限，只做兜底。
	// 真正的判死准则是 probeIdleTimeout：网速慢但数据仍在流动时不打断，
	// 否则宽带给力与否会直接决定探测成功还是失败。
	probeTotalTimeout = 5 * time.Minute
	// probeIdleTimeout 连续这么久读不到新数据就判定卡死（断流 / 网盘限速到 0）。
	probeIdleTimeout = 15 * time.Second
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
	// MaxReadMB 返回单次探测允许读取的上限（MB）；为空或返回非正数时用 probeMaxRead。
	// 做成回调是为了让设置页改动立即生效，不用重启服务。
	MaxReadMB func() int64
}

type sourceToken struct {
	accountID int64
	fileID    string
	expires   time.Time
	maxRead   int64
	read      int64
	// lastRead 是最近一次收到数据的时刻，用于空闲检测。
	lastRead time.Time
	// capped 表示读取曾撞到上限，ffmpeg 的输出可能因此残缺。
	capped bool
	// serveErr 记录取流入口转发过程中的错误，用于区分「网盘侧取流失败」。
	serveErr string
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
	tags, _ := s.ProbeBucketedWithReason(ctx, req)
	return tags, len(tags) > 0
}

// ProbeBucketedWithReason 与 ProbeBucketed 相同，失败时额外给出分类原因，
// 便于在整理日志里区分「文件本身有问题」和「网盘取流失败」。
func (s *Service) ProbeBucketedWithReason(ctx context.Context, req Request) (map[string]string, *Failure) {
	if s == nil {
		return nil, &Failure{Reason: ReasonProbeUnavailable, Detail: "探测服务未启用"}
	}
	if s.binary == "" {
		return nil, &Failure{Reason: ReasonProbeUnavailable, Detail: "未找到 ffprobe 或 ffmpeg"}
	}
	if IsSampleName(req.FileName) {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := BucketKey(req)
	if tags, done := s.cached(key, req); done {
		return tags, nil
	}

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		if ctx.Err() == context.Canceled {
			return nil, &Failure{Reason: ReasonCanceled, Detail: "任务已停止"}
		}
		return nil, &Failure{Reason: ReasonTimeout, Detail: "等待探测槽位超时"}
	}
	defer func() { <-s.sem }()

	// 并发下同一桶可能排队两次，进入临界区后再查一次。
	if tags, done := s.cached(key, req); done {
		return tags, nil
	}

	info, failure := s.probe(ctx, req.AccountID, req.FileID, req.Size)
	if failure != nil {
		// 不写失败缓存：调用方可以换同季另一个文件重试一次。
		s.log.Debug("媒体信息探测失败，等待换文件重试", "file", req.FileName, "reason", failure.Reason)
		return nil, failure
	}
	tags := info.Tags()
	if len(tags) == 0 {
		s.store(key, req, nil)
		return nil, nil
	}
	s.store(key, req, tags)
	return tags, nil
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
	maxRead := s.readLimit(size)
	s.mu.Lock()
	s.tokens[token] = &sourceToken{accountID: accountID, fileID: fileID, expires: time.Now().Add(tokenTTL), maxRead: maxRead, lastRead: time.Now()}
	s.mu.Unlock()
	return token, "http://" + s.addr + "/source/" + token, nil
}

// readLimit 返回本次探测允许读取的字节上限：设置项优先，文件更小则按文件大小。
func (s *Service) readLimit(size int64) int64 {
	limit := int64(probeMaxRead)
	if s.opts.MaxReadMB != nil {
		if mb := s.opts.MaxReadMB(); mb > 0 {
			limit = mb << 20
		}
	}
	if size > 0 && size < limit {
		limit = size
	}
	return limit
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
	cur.lastRead = time.Now()
	if cur.read > cur.maxRead {
		cur.capped = true
		return false
	}
	return true
}

// idleFor 返回某个临时入口距离上次收到数据过了多久；没有令牌时返回 0。
func (s *Service) idleFor(token string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tokens[token]; t != nil && !t.lastRead.IsZero() {
		return time.Since(t.lastRead)
	}
	return 0
}

// sourceDetail 返回某个临时入口的读取量、上限、是否撞过上限，以及取流侧的错误。
func (s *Service) sourceDetail(token string) (read, limit int64, capped bool, serveErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tokens[token]; t != nil {
		return t.read, t.maxRead, t.capped, t.serveErr
	}
	return 0, 0, false, ""
}

// sourceStats 返回某个临时入口已读取的字节数以及是否撞过上限。
func (s *Service) sourceStats(token string) (int64, bool) {
	read, _, capped, _ := s.sourceDetail(token)
	return read, capped
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
		// 失败原因要跟着令牌回传给 probe，否则只能看到「未取到信息」。
		s.mu.Lock()
		if cur := s.tokens[token]; cur != nil {
			cur.serveErr = err.Error()
		}
		s.mu.Unlock()
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

func (s *Service) probe(ctx context.Context, accountID int64, fileID string, size int64) (Info, *Failure) {
	if s.binary == "" {
		return Info{}, &Failure{Reason: ReasonProbeUnavailable, Detail: "未找到 ffprobe 或 ffmpeg"}
	}
	token, url, err := s.newSource(accountID, fileID, size)
	if err != nil {
		return Info{}, &Failure{Reason: ReasonSourceError, Detail: "取流入口创建失败: " + err.Error()}
	}
	defer s.dropToken(token)
	started := time.Now()

	runCtx, cancel := context.WithTimeout(ctx, probeTotalTimeout)
	defer cancel()

	// 空闲监控：连续 probeIdleTimeout 收不到数据才判定卡死，网速慢但仍在流动时不打断。
	var idleHit atomic.Bool
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if s.idleFor(token) >= probeIdleTimeout {
					idleHit.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	// 只读文件头：不指定输出，解析完流信息即退出，不会转码也不会下载整片。
	// stderr 必须留着（-v error）：区分「文件结构损坏」和「网盘取流失败」全靠它，
	// 用 -v quiet 会让分类器永远只剩「读满上限」一条证据。
	var stdout, stderr cappedBuffer
	var runErr error
	if s.useJSON {
		cmd := exec.CommandContext(runCtx, s.binary,
			"-v", "error", "-print_format", "json", "-show_streams", "-show_format",
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

	// 取消之前取一遍运行状态与读取统计，失败时一并写进日志。
	ctxErr := runCtx.Err()
	idleTimeout := idleHit.Load()
	idleSpan := s.idleFor(token)
	cancel()
	<-watchDone

	read, limit, capped, serveErr := s.sourceDetail(token)
	rc := exitCode(runErr)
	detail := summarizeStderr(stderr.String())
	if detail == "" && serveErr != "" {
		// ffprobe 自己没说话时，把取流侧的错误当证据（例如 "Stream ends prematurely"）。
		detail = summarizeStderr(serveErr)
	}
	if idleTimeout {
		head := fmt.Sprintf("%.0f 秒没有新数据（已读 %.1f MB）", idleSpan.Seconds(), float64(read)/(1<<20))
		if detail == "" {
			detail = head
		} else {
			detail = head + "；" + detail
		}
	}

	totalTimeout := ctxErr == context.DeadlineExceeded
	userCanceled := ctxErr == context.Canceled && !idleTimeout

	fail := func() (Info, *Failure) {
		reason := ""
		switch {
		case userCanceled:
			// 用户停止任务时 ffprobe 被杀，不能记成「文件有问题」。
			reason = ReasonCanceled
		case idleTimeout:
			// 数据不流动是网盘侧问题，不能归到文件结构。
			reason = ReasonTimeout
		default:
			reason = classifyFailure(detail, rc, totalTimeout, capped, serveErr, read, limit)
		}
		f := &Failure{Reason: reason, Detail: detail, Read: read, Limit: limit, Capped: capped, RC: rc}
		s.log.Info("媒体信息探测失败",
			"file_id", fileID,
			"reason", f.Reason,
			"read_bytes", f.Read,
			"limit_bytes", f.Limit,
			"capped", f.Capped,
			"rc", f.RC,
			"elapsed", time.Since(started).String(),
			"stderr", f.Detail,
		)
		return Info{}, f
	}

	var info Info
	if s.useJSON {
		parsed, err := ParseJSON(stdout.Bytes())
		if err != nil {
			return fail()
		}
		info = parsed
	} else {
		info = ParseOutput(stderr.String())
	}
	if info.Empty() {
		return fail()
	}
	s.log.Info("媒体信息探测完成", "file_id", fileID, "read_bytes", read, "elapsed", time.Since(started).String())
	if capped {
		// 撞上限说明 ffmpeg 是被硬切断的，流信息可能只解析到一半。
		s.log.Warn("探测读取达到上限被中断，结果可能不完整",
			"file_id", fileID, "read_bytes", read, "limit_bytes", limit)
	}
	return info, nil
}

// 探测失败的原因分类：让日志能区分「文件本身有问题」和「网盘侧取流失败」。
const (
	ReasonProbeUnavailable = "probe_unavailable"
	ReasonSourceError      = "source_error"
	ReasonTimeout          = "timeout"
	ReasonContainerInvalid = "container_invalid"
	ReasonReadCapped       = "read_capped"
	ReasonNoStreams        = "no_streams"
	ReasonCrashed          = "crashed"
	// ReasonCanceled 表示任务被用户停止或上层取消，不是文件/网盘的问题。
	ReasonCanceled = "canceled"
	// ReasonProbeFailed 用于探测器只报告「失败」、不给分类的旧实现。
	ReasonProbeFailed = "probe_failed"
)

// Failure 描述一次探测失败，并带上判断归属所需的证据。
type Failure struct {
	Reason string
	Detail string
	Read   int64
	Limit  int64
	Capped bool
	RC     int
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	return "媒体信息探测失败(" + f.Reason + ")"
}

// ReasonLabel 返回原因码对应的中文说明，供整理汇总直接使用。
func ReasonLabel(reason string) string {
	return (&Failure{Reason: reason}).Label()
}

// Label 返回给人看的原因说明，用于整理的跳过原因与汇总文案。
func (f *Failure) Label() string {
	if f == nil {
		return ""
	}
	switch f.Reason {
	case ReasonContainerInvalid:
		return "文件结构异常"
	case ReasonSourceError:
		return "网盘取流失败"
	case ReasonTimeout:
		return "探测超时"
	case ReasonReadCapped:
		return "读取超限未解析"
	case ReasonNoStreams:
		return "未解析到媒体流"
	case ReasonCrashed:
		return "探测器异常退出"
	case ReasonProbeUnavailable:
		return "本机缺少 ffprobe"
	case ReasonCanceled:
		return "任务已停止"
	case ReasonProbeFailed:
		return "原因未知"
	default:
		return "原因未知"
	}
}

var (
	// containerBrokenHints 是 ffprobe 判定容器结构损坏时的措辞。
	// 只有数据完整（未撞读取上限）时命中才算指向文件本身：截断的数据流同样会报这些错。
	containerBrokenHints = []string{
		"duplicate element",
		"exceeds containing master element",
		"invalid data found when processing input",
		"moov atom not found",
		"malformed",
		"invalid nal",
		"could not find codec parameters",
	}
	// sourceFailedHints 是取流半路失败时的措辞，仅在读取量未撞上限时才成立。
	sourceFailedHints = []string{
		"server returned",
		"connection reset",
		"connection refused",
		"connection timed out",
		"input/output error",
		"end of file",
		"file ended prematurely",
		"403",
		"404",
		"502",
		"503",
	}
	memAddrRe = regexp.MustCompile(`\s*@\s*0x[0-9a-fA-F]+`)
	urlRe     = regexp.MustCompile(`https?://\S+`)
)

// classifyFailure 按「直接证据优先」的顺序给失败定性：
// 结构错误优先于读满上限，读满上限优先于「没解析到流」，取流错误只在没读满时成立。
// classifyFailure 按「直接证据优先」的顺序给失败定性。
// 关键约束：容器结构错误的措辞只有在数据完整（没撞读取上限）时才可采信——
// 读取被上限切断时，ffprobe 也会报 "Duplicate element"/"element exceeds"，
// 那是截断产生的副产品，据此判定文件损坏会重演误判。
func classifyFailure(detail string, rc int, timedOut, capped bool, serveErr string, read, limit int64) string {
	lower := strings.ToLower(detail)
	notCapped := limit <= 0 || read < limit
	if notCapped && containsAnyHint(lower, containerBrokenHints) {
		return ReasonContainerInvalid
	}
	if timedOut {
		return ReasonTimeout
	}
	if rc < 0 {
		return ReasonCrashed
	}
	if serveErr != "" && notCapped {
		return ReasonSourceError
	}
	if notCapped && containsAnyHint(lower, sourceFailedHints) {
		return ReasonSourceError
	}
	if capped {
		return ReasonReadCapped
	}
	if strings.TrimSpace(serveErr) != "" {
		return ReasonSourceError
	}
	return ReasonNoStreams
}

func containsAnyHint(text string, hints []string) bool {
	for _, h := range hints {
		if strings.Contains(text, h) {
			return true
		}
	}
	return false
}

// summarizeStderr 把 ffprobe 的 stderr 压成一行短摘要：去掉内存地址与链接，截断后写日志。
func summarizeStderr(text string) string {
	s := memAddrRe.ReplaceAllString(text, "")
	s = urlRe.ReplaceAllString(s, "<url>")
	s = strings.Join(strings.Fields(s), " ")
	// 按字符截断：字节截断会把中文/emoji 切成半个，日志里出现乱码。
	const maxDetail = 200
	if utf8.RuneCountInString(s) > maxDetail {
		runes := []rune(s)
		s = string(runes[:maxDetail]) + "…"
	}
	return s
}

// exitCode 把 ffprobe 的退出状态压成整数：正常退出为 0，被信号终止为 -1。
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
		return -1
	}
	return -1
}
