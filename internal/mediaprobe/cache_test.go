package mediaprobe

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

func newCacheTestService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		opts:   Options{DataDir: t.TempDir()},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokens: map[string]*sourceToken{},
		cache:  map[string]cacheEntry{},
	}
}

func testRequest(fileID string, size int64) Request {
	return Request{
		AccountID: 7, FileID: fileID, FileName: "Some.Show.S01E01.mkv", Size: size,
		MediaKind: "tv", DirID: "d1", Season: 1, Ext: ".mkv",
	}
}

func sampleTags() map[string]string {
	return map[string]string{"screen_size": "1080p", "video_codec": "H.264"}
}

func TestCacheExpiresAfterTTL(t *testing.T) {
	s := newCacheTestService(t)
	req := testRequest("f1", 1024)
	key := BucketKey(req)
	s.cache[key] = cacheEntry{OK: true, Tags: sampleTags(), At: time.Now().Add(-cacheTTL - time.Hour).Unix(), FileID: "f1", Size: 1024}

	if _, done := s.cached(key, req); done {
		t.Fatal("超过 TTL 的条目不应命中")
	}
	if _, exists := s.cache[key]; exists {
		t.Fatal("过期条目应被删除")
	}
}

func TestCacheKeepsFreshEntry(t *testing.T) {
	s := newCacheTestService(t)
	req := testRequest("f1", 1024)
	key := BucketKey(req)
	s.cache[key] = cacheEntry{OK: true, Tags: sampleTags(), At: time.Now().Add(-time.Hour).Unix(), FileID: "f1", Size: 1024}

	tags, done := s.cached(key, req)
	if !done || tags["screen_size"] != "1080p" {
		t.Fatalf("有效期内的条目应命中，得到 %v done=%v", tags, done)
	}
}

func TestCacheInvalidatesWhenSameFileResized(t *testing.T) {
	s := newCacheTestService(t)
	key := BucketKey(testRequest("f1", 1024))
	s.cache[key] = cacheEntry{OK: true, Tags: sampleTags(), At: time.Now().Unix(), FileID: "f1", Size: 1024}

	replaced := testRequest("f1", 4096) // 同一集换了更高码率的源
	if _, done := s.cached(key, replaced); done {
		t.Fatal("抽样文件 size 变化后应重新探测")
	}
	if _, exists := s.cache[key]; exists {
		t.Fatal("失效条目应被删除")
	}
}

func TestCacheKeepsOtherFilesInBucket(t *testing.T) {
	s := newCacheTestService(t)
	key := BucketKey(testRequest("f1", 1024))
	s.cache[key] = cacheEntry{OK: true, Tags: sampleTags(), At: time.Now().Unix(), FileID: "f1", Size: 1024}

	// 同季另一集，体积本来就和抽样那一集不同，不能因此判为失效。
	other := testRequest("f2", 999999)
	if _, done := s.cached(key, other); !done {
		t.Fatal("桶内其它文件应继续命中缓存")
	}
}

func TestCacheEvictsOldestBeyondLimit(t *testing.T) {
	s := newCacheTestService(t)
	base := time.Now().Add(-time.Duration(cacheMaxItems) * time.Minute).Unix()
	for i := 0; i < cacheMaxItems+5; i++ {
		s.cache[fmt.Sprintf("k%05d", i)] = cacheEntry{OK: true, Tags: sampleTags(), At: base + int64(i), FileID: "f"}
	}
	s.evictLocked()

	if len(s.cache) != cacheMaxItems {
		t.Fatalf("应压到 %d 条，实际 %d", cacheMaxItems, len(s.cache))
	}
	if _, exists := s.cache["k00000"]; exists {
		t.Fatal("最旧的条目应被淘汰")
	}
	if _, exists := s.cache[fmt.Sprintf("k%05d", cacheMaxItems+4)]; !exists {
		t.Fatal("最新的条目不应被淘汰")
	}
}
