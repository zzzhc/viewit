package main

import (
	"container/heap"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sahilm/fuzzy"
)

const (
	// findChunkSize bounds per-query transient memory: candidates are matched
	// chunk-wise, only the best findMaxResults survive.
	findChunkSize = 16384
	// findMaxResults caps the payload sent to the client.
	findMaxResults = 200

	pongWait   = 60 * time.Second
	pingPeriod = 30 * time.Second
	writeWait  = 10 * time.Second
)

// findMatch is one fuzzy-find result.
type findMatch struct {
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Marks []int  `json:"marks,omitempty"` // rune offsets of matched chars, for highlighting
}

type findResponse struct {
	Type       string      `json:"type"`
	Partial    bool        `json:"partial"`    // index walk still in progress
	Indexed    int         `json:"indexed"`    // paths indexed so far (whole tree)
	ScopeCount int         `json:"scopeCount"` // entries under the search scope
	Matched    int         `json:"matched,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
	Matches    []findMatch `json:"matches,omitempty"`
}

// findIndex is the lazily-built, append-only file index for fuzzy search.
// paths[i]/dirs[i] hold the i-th entry (slash-separated path relative to
// root, in WalkDir order).
type findIndex struct {
	mu    sync.RWMutex
	paths []string
	dirs  []bool
	done  bool
	once  sync.Once
}

// start launches the background walk once; searches see partial results
// (findResponse.Partial) while it runs.
func (ix *findIndex) start(root string) {
	ix.once.Do(func() { go ix.walk(root) })
}

// walk indexes every entry under root. .git directories are skipped: version
// control internals bulk up the index and are never useful search targets.
// Batches are appended under lock so searches stay consistent.
func (ix *findIndex) walk(root string) {
	// 索引构建是搜索"为什么慢"的根源:大目录树全量遍历可达数秒到数十秒,
	// 期间查询都是部分结果。整个生命周期只走一次,无条件记。
	t0 := time.Now()
	var total int
	const batch = 512
	var paths []string
	var dirs []bool
	flush := func() {
		if len(paths) == 0 {
			return
		}
		total += len(paths)
		ix.mu.Lock()
		ix.paths = append(ix.paths, paths...)
		ix.dirs = append(ix.dirs, dirs...)
		ix.mu.Unlock()
		paths, dirs = paths[:0], dirs[:0]
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it rather than abort the walk
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		paths = append(paths, filepath.ToSlash(rel))
		dirs = append(dirs, d.IsDir())
		if len(paths) >= batch {
			flush()
		}
		return nil
	})
	flush()
	ix.mu.Lock()
	ix.done = true
	ix.mu.Unlock()
	log.Printf("[slow] find-walk root=%s entries=%d took=%s", root, total, time.Since(t0).Round(time.Millisecond))
}

// search returns the best matches for q against the current index, limited
// to entries under scope (a slash-separated path relative to root, "/" or ""
// for the whole tree). An empty query returns no matches but still reports
// indexing progress.
func (ix *findIndex) search(q, scope string) findResponse {
	// 锁内只做快照(paths/dirs 引用与索引区间),匹配放到锁外:一次模糊
	// 匹配可耗时数百 ms,若持读锁会阻塞 walk 的批量追加,拖慢索引构建。
	// 切片引用共享底层数组,walk 只做尾部追加、不改动已有元素,快照安全。
	ix.mu.RLock()
	paths := ix.paths
	dirs := ix.dirs
	done := ix.done
	lo, hi := ix.scopeRange(scope)
	ix.mu.RUnlock()
	resp := findResponse{Type: "results", Partial: !done, Indexed: len(paths)}
	resp.ScopeCount = hi - lo
	if q == "" {
		return resp
	}
	matches, matched := bestMatches(q, paths[lo:hi])
	for i := range matches {
		matches[i].Index += lo
	}
	resp.Matched = matched
	resp.Truncated = matched > findMaxResults
	resp.Matches = make([]findMatch, 0, len(matches))
	for _, m := range matches {
		resp.Matches = append(resp.Matches, findMatch{
			Path:  m.Str,
			IsDir: dirs[m.Index],
			Marks: runeOffsets(m.Str, m.MatchedIndexes),
		})
	}
	return resp
}

// scopePrefix returns the directory prefix for scope ("" means the whole
// tree). A file entry is reduced to its parent directory.
func (ix *findIndex) scopePrefix(scope string) string {
	scope = strings.Trim(scope, "/")
	if scope == "" {
		return ""
	}
	if i := sort.SearchStrings(ix.paths, scope); i < len(ix.paths) && ix.paths[i] == scope && !ix.dirs[i] {
		// scope names a file: search under its parent directory
		if j := strings.LastIndexByte(scope, '/'); j >= 0 {
			scope = scope[:j]
		} else {
			return "" // file at root: whole tree
		}
	}
	return scope + "/"
}

// scopeRange returns the index interval [lo, hi) of entries under scope.
// Index paths are in WalkDir (lexical) order, so a directory's entries are
// contiguous: the interval is found with two binary searches.
func (ix *findIndex) scopeRange(scope string) (int, int) {
	prefix := ix.scopePrefix(scope)
	if prefix == "" {
		return 0, len(ix.paths)
	}
	lo := sort.Search(len(ix.paths), func(i int) bool { return ix.paths[i] >= prefix })
	hi := sort.Search(len(ix.paths), func(i int) bool {
		return ix.paths[i] >= prefix && !strings.HasPrefix(ix.paths[i], prefix)
	})
	return lo, hi
}

// matchHeap keeps the best findMaxResults matches seen so far: a min-heap on
// score, ties broken by keeping the lower index, matching the ordering of
// fuzzy.Find (stable sort by score desc, original order preserved).
type matchHeap struct {
	max int
	ms  []fuzzy.Match
}

func (h matchHeap) Len() int      { return len(h.ms) }
func (h matchHeap) Swap(i, j int) { h.ms[i], h.ms[j] = h.ms[j], h.ms[i] }
func (h matchHeap) Less(i, j int) bool {
	if h.ms[i].Score == h.ms[j].Score {
		return h.ms[i].Index > h.ms[j].Index
	}
	return h.ms[i].Score < h.ms[j].Score
}
func (h *matchHeap) Push(x any) { h.ms = append(h.ms, x.(fuzzy.Match)) }
func (h *matchHeap) Pop() any {
	m := h.ms[len(h.ms)-1]
	h.ms = h.ms[:len(h.ms)-1]
	return m
}

// bestMatches scans paths in chunks so transient memory stays bounded no
// matter how large the index grows; chunks are matched in parallel (the
// library keeps no shared state) and each chunk is reduced to its own top-N
// before merging. Results are best-first, identically ordered to
// fuzzy.Find(q, paths)[:findMaxResults].
func bestMatches(q string, paths []string) ([]fuzzy.Match, int) {
	if len(paths) <= findChunkSize {
		return bestInChunk(q, paths, 0)
	}
	numChunks := (len(paths) + findChunkSize - 1) / findChunkSize
	chunkResults := make([][]fuzzy.Match, numChunks)
	chunkMatched := make([]int, numChunks)
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for c := 0; c < numChunks; c++ {
		start := c * findChunkSize
		end := min(start+findChunkSize, len(paths))
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			chunkResults[c], chunkMatched[c] = bestInChunk(q, paths[start:end], start)
		}()
	}
	wg.Wait()
	total := 0
	for _, n := range chunkMatched {
		total += n
	}
	return mergeChunks(chunkResults), total
}

// bestInChunk runs the fuzzy match over one chunk and reduces it to the
// best findMaxResults matches, with Index made global via base.
func bestInChunk(q string, paths []string, base int) ([]fuzzy.Match, int) {
	ms := fuzzy.FindNoSort(q, paths)
	for i := range ms {
		ms[i].Index += base
	}
	h := &matchHeap{max: findMaxResults}
	for i := range ms {
		heap.Push(h, ms[i])
		if h.Len() > findMaxResults {
			heap.Pop(h)
		}
	}
	out := make([]fuzzy.Match, 0, h.Len())
	for h.Len() > 0 {
		out = append(out, heap.Pop(h).(fuzzy.Match)) // worst first
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, len(ms)
}

// mergeChunks merges already-reduced chunk results into the global top-N.
func mergeChunks(chunks [][]fuzzy.Match) []fuzzy.Match {
	h := &matchHeap{max: findMaxResults}
	for _, chunk := range chunks {
		for i := range chunk {
			heap.Push(h, chunk[i])
			if h.Len() > findMaxResults {
				heap.Pop(h)
			}
		}
	}
	out := make([]fuzzy.Match, 0, h.Len())
	for h.Len() > 0 {
		out = append(out, heap.Pop(h).(fuzzy.Match))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// runeOffsets converts matched byte offsets (as produced by fuzzy.Match) to
// rune offsets, which map 1:1 onto JavaScript code points (Array.from) used
// for highlighting in the frontend.
func runeOffsets(s string, byteOffsets []int) []int {
	if len(byteOffsets) == 0 {
		return nil
	}
	out := make([]int, 0, len(byteOffsets))
	ri, n := 0, 0
	for bi := range s { // ranging a string yields one iteration per rune
		if n < len(byteOffsets) && byteOffsets[n] == bi {
			out = append(out, ri)
			n++
		}
		ri++
	}
	return out
}

var wsUpgrader = websocket.Upgrader{
	// In dev mode the frontend reaches us through the Vite proxy on :5173, a
	// different origin; this is a local tool, so any origin is accepted.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleWS serves the fuzzy-find protocol: the client sends {"q": "...",
// "path": current-dir} (path limits the search to that subtree) and receives
// {"type":"results", ...} responses. The index walk starts on the first
// connection; responses carry Partial=true until it finishes.
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	s.idx.start(s.root)

	conn.SetReadLimit(4 << 10)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ticker.C:
				// WriteControl is safe concurrently with the write below.
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
					return
				}
			}
		}
	}()

	for {
		var msg struct {
			Q    string `json:"q"`
			Path string `json:"path"` // search scope: current directory relative to root
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return // read error, bad JSON, or deadline: drop the client
		}
		// 每条查询记一行 [ws] 日志:q/scope 定位到是哪次查询、在哪个目录,
		// 耗时与命中数判断是匹配慢还是结果少。查询高频,再按阈值补一条
		// [slow] 行,让慢查询有独立 grep 面。
		t0 := time.Now()
		resp := s.idx.search(msg.Q, msg.Path)
		d := time.Since(t0)
		log.Printf("[ws] find q=%q scope=%q scopeCount=%d matched=%d took=%s",
			truncateForLog(msg.Q, 100), msg.Path, resp.ScopeCount, resp.Matched, d.Round(time.Microsecond))
		if d >= slowThreshold {
			log.Printf("[slow] find-query q=%q scope=%q indexed=%d scopeCount=%d matched=%d took=%s",
				truncateForLog(msg.Q, 100), msg.Path, resp.Indexed, resp.ScopeCount, resp.Matched, d.Round(time.Millisecond))
		}
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}
}
