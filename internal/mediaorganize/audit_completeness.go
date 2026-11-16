package mediaorganize

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"litepan/internal/mediaorganize/audit"
	"litepan/internal/mediaorganize/rules"
)

// 本文件把「信息体检」的目录树结果接到 TMDB：
// 剧集按季集比对，电影按合集（系列）比对。只读，不动任何文件。

const (
	// tmdbCacheTTL 是进程内 TMDB 结果缓存的有效期；同一份库一天内重复体检几乎零请求。
	tmdbCacheTTL = 12 * time.Hour
	// franchiseEnrichMax 限制每个合集额外查详情的部数（补 runtime 用于判断重剪版）。
	// 实测《鬼灭之刃（系列）》有 13 部候选，8 部不够用。
	franchiseEnrichMax = 20
)

// auditTMDBClient 是完整性比对需要的 TMDB 能力，*tmdb.Client 直接满足；抽成接口便于单测。
type auditTMDBClient interface {
	Lookup(ctx context.Context, tmdbID string, mediaType string) (json.RawMessage, error)
	FetchTVSeason(ctx context.Context, tmdbID string, seasonNumber int) (json.RawMessage, error)
	FetchCollection(ctx context.Context, collectionID string) (json.RawMessage, error)
}

type tmdbCacheEntry struct {
	raw json.RawMessage
	at  time.Time
}

// tmdbFetcher 包一层缓存与限速，所有 TMDB 请求都从这里出去。
type tmdbFetcher struct {
	ctx      context.Context
	client   auditTMDBClient
	svc      *Service
	interval time.Duration
	calls    int
}

func (f *tmdbFetcher) get(key string, fn func() (json.RawMessage, error)) (json.RawMessage, error) {
	if raw, ok := f.svc.cachedTMDB(key); ok {
		return raw, nil
	}
	raw, err := fn()
	if err != nil {
		return nil, err
	}
	f.calls++
	f.svc.storeTMDB(key, raw)
	f.sleep()
	return raw, nil
}

func (f *tmdbFetcher) sleep() {
	if f.interval <= 0 {
		return
	}
	select {
	case <-f.ctx.Done():
	case <-time.After(f.interval):
	}
}

func (f *tmdbFetcher) lookup(id, mediaType string) (map[string]any, error) {
	raw, err := f.get(mediaType+":"+id, func() (json.RawMessage, error) {
		return f.client.Lookup(f.ctx, id, mediaType)
	})
	if err != nil {
		return nil, err
	}
	return rules.RawJSONToMap(raw), nil
}

func (f *tmdbFetcher) season(id string, number int) (*audit.TMDBSeason, error) {
	key := fmt.Sprintf("season:%s:%d", id, number)
	raw, err := f.get(key, func() (json.RawMessage, error) {
		return f.client.FetchTVSeason(f.ctx, id, number)
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Episodes []struct {
			EpisodeNumber int    `json:"episode_number"`
			AirDate       string `json:"air_date"`
			EpisodeType   string `json:"episode_type"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := &audit.TMDBSeason{Number: number, Episodes: make([]audit.TMDBEpisode, 0, len(payload.Episodes))}
	for _, e := range payload.Episodes {
		out.Episodes = append(out.Episodes, audit.TMDBEpisode{
			Number:  e.EpisodeNumber,
			AirDate: strings.TrimSpace(e.AirDate),
			Type:    e.EpisodeType,
		})
	}
	return out, nil
}

func (f *tmdbFetcher) collection(id string) (name string, parts []audit.FranchisePart, err error) {
	raw, err := f.get("collection:"+id, func() (json.RawMessage, error) {
		return f.client.FetchCollection(f.ctx, id)
	})
	if err != nil {
		return "", nil, err
	}
	var payload struct {
		Name  string `json:"name"`
		Parts []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", nil, err
	}
	for _, p := range payload.Parts {
		if p.ID <= 0 {
			continue
		}
		parts = append(parts, audit.FranchisePart{
			ID:          fmt.Sprint(p.ID),
			Title:       p.Title,
			ReleaseDate: strings.TrimSpace(p.ReleaseDate),
		})
	}
	return payload.Name, parts, nil
}

// movieInfo 是电影 detail 里完整性比对用到的字段。
type movieInfo struct {
	Title         string
	ReleaseDate   string
	Runtime       int
	CollectionID  string
	CollectionNm  string
	OriginalTitle string
}

func parseMovieInfo(m map[string]any) movieInfo {
	out := movieInfo{
		Title:         jsonString(m["title"]),
		ReleaseDate:   jsonString(m["release_date"]),
		OriginalTitle: jsonString(m["original_title"]),
	}
	if n := rules.AsFirstInt(m["runtime"]); n != nil {
		out.Runtime = *n
	}
	if col, ok := m["belongs_to_collection"].(map[string]any); ok {
		if id := rules.AsFirstInt(col["id"]); id != nil && *id > 0 {
			out.CollectionID = fmt.Sprint(*id)
			out.CollectionNm = strings.TrimSpace(fmt.Sprint(col["name"]))
		}
	}
	return out
}

// applyCompleteness 给报告里的每个分组补上 TMDB 比对结论。
//
// 只处理带 {tmdb-xxx} 标签的作品目录（用户库里都是整理过的，标签齐全）；
// 未匹配/查询失败只做标记，不影响其它分组。
func (s *Service) applyCompleteness(ctx context.Context, rep *audit.Report, client auditTMDBClient, interval time.Duration,
	checkEpisodes, checkFranchise bool, today time.Time) int {
	if rep == nil || client == nil || (!checkEpisodes && !checkFranchise) {
		return 0
	}
	f := &tmdbFetcher{ctx: ctx, client: client, svc: s, interval: interval}

	byCollection := map[string][]int{}
	localMovies := map[string]audit.FranchisePart{}
	movieGroups := 0

	for i := range rep.Groups {
		g := &rep.Groups[i]
		g.TMDBID = tmdbIDOfGroup(*g)
		if g.TMDBID == "" {
			if looksLikeTVGroup(*g) {
				g.Match = "unmatched"
			}
			continue
		}
		// 先按目录内容判断是剧还是电影，再去对应的 TMDB 接口。
		// 不能「先查 tv、成功就算剧」：TMDB 的剧和电影是两套独立 ID 空间，
		// 电影库里的 id 往往也能撞上一个同名无关的剧，那样会把电影当成一季剧去比集号。
		tvFirst := looksLikeTVGroup(*g)
		if tvFirst {
			if tv, err := f.lookup(g.TMDBID, "tv"); err == nil && tv != nil {
				g.MediaType = "tv"
				g.Match = "matched"
				if checkEpisodes {
					s.fillShowDiff(f, g, tv, today)
				}
				continue
			}
		}
		mv, err := f.lookup(g.TMDBID, "movie")
		if (err != nil || mv == nil) && !tvFirst {
			// 目录看着不像剧，但电影也查不到：再给 tv 一次机会（无季目录的番剧/剧集）。
			if tv, terr := f.lookup(g.TMDBID, "tv"); terr == nil && tv != nil {
				g.MediaType = "tv"
				g.Match = "matched"
				if checkEpisodes {
					s.fillShowDiff(f, g, tv, today)
				}
				continue
			}
		}
		if err != nil || mv == nil {
			g.Match = "failed"
			continue
		}
		info := parseMovieInfo(mv)
		g.MediaType = "movie"
		g.Match = "matched"
		localMovies[g.TMDBID] = audit.FranchisePart{
			ID:          g.TMDBID,
			Title:       info.Title,
			ReleaseDate: info.ReleaseDate,
			Runtime:     info.Runtime,
		}
		movieGroups++
		if info.CollectionID != "" {
			byCollection[info.CollectionID] = append(byCollection[info.CollectionID], i)
		}
	}

	if checkFranchise && movieGroups > 0 && len(byCollection) > 0 {
		s.fillFranchises(f, rep, byCollection, localMovies, today)
	}
	return f.calls
}

// fillShowDiff 拉取该剧各本地季的集列表并比对。
func (s *Service) fillShowDiff(f *tmdbFetcher, g *audit.Group, detail map[string]any, today time.Time) {
	seasons := parseTMDBSeasons(detail)
	if len(seasons) == 0 || len(g.Episodes) == 0 {
		return
	}
	byNumber := make(map[int]*audit.TMDBSeason, len(seasons))
	for i := range seasons {
		byNumber[seasons[i].Number] = &seasons[i]
	}
	// 只拉本地已有季的集列表：缺的整季不需要集号。
	localNums := make([]int, 0, len(g.Episodes))
	for n := range g.Episodes {
		if n > 0 {
			localNums = append(localNums, n)
		}
	}
	sort.Ints(localNums)
	for _, n := range localNums {
		base, ok := byNumber[n]
		if !ok {
			continue
		}
		detailSeason, err := f.season(g.TMDBID, n)
		if err != nil || detailSeason == nil {
			continue // 该季没取到集列表时，CompareShow 会按集数核对并标「需人工确认」
		}
		detailSeason.EpisodeCount = base.EpisodeCount
		detailSeason.AirDate = base.AirDate
		*base = *detailSeason
	}
	diff := audit.CompareShow(g.Episodes, seasons, today)
	g.Completeness = &diff
}

func parseTMDBSeasons(detail map[string]any) []audit.TMDBSeason {
	raw, ok := detail["seasons"].([]any)
	if !ok {
		return nil
	}
	out := make([]audit.TMDBSeason, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		num := rules.AsFirstInt(m["season_number"])
		count := rules.AsFirstInt(m["episode_count"])
		if num == nil || *num <= 0 {
			continue
		}
		s := audit.TMDBSeason{Number: *num, AirDate: jsonString(m["air_date"])}
		if count != nil {
			s.EpisodeCount = *count
		}
		out = append(out, s)
	}
	return out
}

// fillFranchises 按合集归并后比对，同一合集只查一次。
func (s *Service) fillFranchises(f *tmdbFetcher, rep *audit.Report, byCollection map[string][]int,
	localMovies map[string]audit.FranchisePart, today time.Time) {
	for collectionID, indexes := range byCollection {
		name, parts, err := f.collection(collectionID)
		if err != nil || len(parts) == 0 {
			continue
		}
		if name == "" {
			name = "合集 " + collectionID
		}
		enrichMissingParts(f, parts, localMovies, today)
		diff := audit.CompareCollection(name, parts, localMovies, today)
		for _, idx := range indexes {
			d := diff
			rep.Groups[idx].Franchise = &d
		}
	}
}

// enrichMissingParts 给「已上映但本地没有」的 part 补 runtime，
// 用于区分正片续集和重剪/合集版（合集接口本身不带 runtime）。
func enrichMissingParts(f *tmdbFetcher, parts []audit.FranchisePart, local map[string]audit.FranchisePart, today time.Time) {
	todayStr := today.Format("2006-01-02")
	enriched := 0
	for i := range parts {
		if enriched >= franchiseEnrichMax {
			return
		}
		p := &parts[i]
		if _, ok := local[p.ID]; ok {
			continue
		}
		if p.ReleaseDate == "" || p.ReleaseDate > todayStr {
			continue
		}
		mv, err := f.lookup(p.ID, "movie")
		if err != nil || mv == nil {
			continue
		}
		info := parseMovieInfo(mv)
		if info.Runtime > 0 {
			p.Runtime = info.Runtime
		}
		if info.Title != "" {
			p.Title = info.Title
		}
		enriched++
	}
}

var tvLikeNameRe = regexp.MustCompile(`(?i)s\d{1,2}\s*e\d{1,4}`)

// looksLikeTVGroup 判断未匹配的分组看起来是不是剧集（避免把无名电影目录报成未匹配）。
func looksLikeTVGroup(g audit.Group) bool {
	if len(g.Episodes) > 0 {
		return true
	}
	for _, b := range g.Buckets {
		if tvLikeNameRe.MatchString(b.Sample) {
			return true
		}
		if rules.ParseSeasonDirNumber(lastPathSegment(b.DirPath)) != nil {
			return true
		}
	}
	return false
}

func lastPathSegment(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	return segs[len(segs)-1]
}

// tmdbIDOfGroup 取分组对应的 TMDB ID：目录名标签优先，其次路径最后一段。
func tmdbIDOfGroup(g audit.Group) string {
	if id := rules.FindTMDBIDInName(g.Name); id != "" {
		return id
	}
	return rules.FindTMDBIDInName(lastPathSegment(g.Path))
}

// ---- 进程内 TMDB 缓存 ----

func (s *Service) cachedTMDB(key string) (json.RawMessage, bool) {
	s.tmdbMu.Lock()
	defer s.tmdbMu.Unlock()
	if s.tmdbCache == nil {
		return nil, false
	}
	entry, ok := s.tmdbCache[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.at) > tmdbCacheTTL {
		delete(s.tmdbCache, key)
		return nil, false
	}
	return entry.raw, true
}

func (s *Service) storeTMDB(key string, raw json.RawMessage) {
	s.tmdbMu.Lock()
	defer s.tmdbMu.Unlock()
	if s.tmdbCache == nil {
		s.tmdbCache = map[string]tmdbCacheEntry{}
	}
	s.tmdbCache[key] = tmdbCacheEntry{raw: raw, at: time.Now()}
}

// jsonString 取 JSON 里的字符串字段，缺失或类型不对返回空串。
func jsonString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// tmdbInterval 返回 TMDB 请求间隔（设置项 mo_tmdb_request_interval_ms，默认 250ms）。
func (s *Service) tmdbInterval() time.Duration {
	dict := SettingsDict(s.settings)
	ms := intFromAny(dict["tmdb_request_interval_ms"], 250)
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
