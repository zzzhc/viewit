package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sahilm/fuzzy"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	h, err := newHandler(root, false)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, root
}

func dialFinder(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func sendQuery(t *testing.T, conn *websocket.Conn, q string) {
	t.Helper()
	if err := conn.WriteJSON(struct {
		Q string `json:"q"`
	}{q}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readFind(t *testing.T, conn *websocket.Conn) findResponse {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp findResponse
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp
}

// waitFinderReady polls with empty queries until the index walk finishes.
func waitFinderReady(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sendQuery(t, conn, "")
		if !readFind(t, conn).Partial {
			return
		}
	}
	t.Fatal("index walk did not finish in time")
}

func findResultPaths(t *testing.T, conn *websocket.Conn, q string) (findResponse, []string) {
	t.Helper()
	return findResultPathsIn(t, conn, q, "")
}

// findResultPathsIn 发送带查找范围(scope,当前目录相对路径)的查询并读取响应。
func findResultPathsIn(t *testing.T, conn *websocket.Conn, q, scope string) (findResponse, []string) {
	t.Helper()
	if err := conn.WriteJSON(struct {
		Q    string `json:"q"`
		Path string `json:"path"`
	}{q, scope}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readFind(t, conn)
	paths := make([]string, 0, len(resp.Matches))
	for _, m := range resp.Matches {
		paths = append(paths, m.Path)
	}
	return resp, paths
}

func TestFinderSearchUnicodeAndDirs(t *testing.T) {
	srv, root := newTestServer(t)
	for _, f := range []string{
		"a.txt", "b.go", "docs/中文文档.txt", "docs/报告.pdf",
		"src/数据.json", "src/app.js", "sub/deep/x.txt",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "x")
	}
	conn := dialFinder(t, srv)
	waitFinderReady(t, conn)

	resp, paths := findResultPaths(t, conn, "中文")
	if len(paths) != 1 || paths[0] != "docs/中文文档.txt" {
		t.Fatalf("query 中文: got %v, want [docs/中文文档.txt]", paths)
	}
	// "docs/" is 5 runes, so the matched 中文 sits at rune offsets 5,6.
	if got := resp.Matches[0].Marks; len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("marks = %v, want [5 6]", got)
	}

	_, paths = findResultPaths(t, conn, "数据")
	if len(paths) != 1 || paths[0] != "src/数据.json" {
		t.Fatalf("query 数据: got %v, want [src/数据.json]", paths)
	}

	// directories are indexed too
	_, paths = findResultPaths(t, conn, "docs")
	if len(paths) != 3 { // docs dir + its two files
		t.Fatalf("query docs: got %v, want 3 matches", paths)
	}
	resp, paths = findResultPaths(t, conn, "sub")
	if len(resp.Matches) == 0 || !resp.Matches[0].IsDir {
		t.Fatalf("query sub: first match %+v, want a directory", resp.Matches[0])
	}

	// best match first: exact filename beats a deep path
	_, paths = findResultPaths(t, conn, "app")
	if len(paths) == 0 || paths[0] != "src/app.js" {
		t.Fatalf("query app: got %v, want src/app.js first", paths)
	}

	// empty query: progress only, no matches
	resp, _ = findResultPaths(t, conn, "")
	if resp.Partial || resp.Indexed < 7 || len(resp.Matches) != 0 {
		t.Fatalf("empty query: partial=%v indexed=%d matches=%d", resp.Partial, resp.Indexed, len(resp.Matches))
	}
}

func TestFinderScopedSearch(t *testing.T) {
	srv, root := newTestServer(t)
	for _, f := range []string{
		"a.txt", "docs/中文文档.txt", "docs/报告.pdf", "docs/sub/深.txt",
		"src/数据.json", "docs2/边界.txt",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "x")
	}
	conn := dialFinder(t, srv)
	waitFinderReady(t, conn)

	// 子目录范围内:只出该目录(含嵌套)的结果,scopeCount 为范围内条目数
	resp, paths := findResultPathsIn(t, conn, "文", "docs")
	if len(paths) != 1 || paths[0] != "docs/中文文档.txt" {
		t.Fatalf("scope=docs q=文: got %v, want [docs/中文文档.txt]", paths)
	}
	if resp.ScopeCount != 4 { // docs, docs/sub, docs/sub/深.txt, docs/中文文档.txt, docs/报告.pdf 中区间内 4 项
		t.Fatalf("scope=docs: scopeCount=%d, want 4", resp.ScopeCount)
	}
	// 空查询也返回范围规模
	resp, _ = findResultPathsIn(t, conn, "", "docs")
	if resp.ScopeCount != 4 {
		t.Fatalf("scope=docs empty query: scopeCount=%d, want 4", resp.ScopeCount)
	}
	resp, _ = findResultPathsIn(t, conn, "", "")
	if resp.ScopeCount != 10 { // 全库 10 项(6 文件 + 4 目录)
		t.Fatalf("scope=all empty query: scopeCount=%d, want 10", resp.ScopeCount)
	}
	// 范围外的文件不出现,前缀相近的兄弟目录也不误入
	_, paths = findResultPathsIn(t, conn, "数据", "docs")
	if len(paths) != 0 {
		t.Fatalf("scope=docs q=数据: got %v, want none", paths)
	}
	_, paths = findResultPathsIn(t, conn, "边界", "docs")
	if len(paths) != 0 {
		t.Fatalf("scope=docs q=边界: got %v, want none (docs2 excluded)", paths)
	}
	// 嵌套范围
	_, paths = findResultPathsIn(t, conn, "深", "docs/sub")
	if len(paths) != 1 || paths[0] != "docs/sub/深.txt" {
		t.Fatalf("scope=docs/sub q=深: got %v, want [docs/sub/深.txt]", paths)
	}
	// scope 为文件路径时取其父目录
	_, paths = findResultPathsIn(t, conn, "报告", "docs/报告.pdf")
	if len(paths) != 1 || paths[0] != "docs/报告.pdf" {
		t.Fatalf("scope=file q=报告: got %v, want [docs/报告.pdf]", paths)
	}
	// 空范围 = 全量
	_, paths = findResultPathsIn(t, conn, "数据", "")
	if len(paths) != 1 || paths[0] != "src/数据.json" {
		t.Fatalf("scope=all q=数据: got %v, want [src/数据.json]", paths)
	}
}

func TestScopeRange(t *testing.T) {
	// 索引路径按 WalkDir 字典序排列,目录条目连续;中文等 UTF-8 字节序
	// 大于 ASCII,测试数据必须来自真实 walk 才能保证顺序
	root := t.TempDir()
	for _, f := range []string{
		"a.txt", "b.go", "docs/报告.pdf", "docs/sub/深.txt",
		"docs/中文文档.txt", "docs2/x.txt", "src/数据.json",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "x")
	}
	ix := &findIndex{}
	ix.walk(root)
	wantOrder := []string{
		"a.txt", "b.go", "docs", "docs/sub", "docs/sub/深.txt",
		"docs/中文文档.txt", "docs/报告.pdf", "docs2", "docs2/x.txt",
		"src", "src/数据.json",
	}
	if len(ix.paths) != len(wantOrder) {
		t.Fatalf("indexed %d entries, want %d: %v", len(ix.paths), len(wantOrder), ix.paths)
	}
	for i := range wantOrder {
		if ix.paths[i] != wantOrder[i] {
			t.Fatalf("paths[%d] = %q, want %q (full: %v)", i, ix.paths[i], wantOrder[i], ix.paths)
		}
	}

	cases := []struct {
		scope  string
		wantLo int
		wantHi int
		desc   string
	}{
		{"", 0, 11, "空=全量"},
		{"/", 0, 11, "根=全量"},
		{"docs", 3, 7, "目录区间(不含目录自身)"},
		{"docs/sub", 4, 5, "嵌套目录"},
		{"docs/报告.pdf", 3, 7, "文件取父目录"},
		{"a.txt", 0, 11, "根下文件=全量"},
		{"docs2", 8, 9, "前缀相近兄弟目录"},
		{"missing", 9, 9, "不存在目录=空区间"},
	}
	for _, c := range cases {
		lo, hi := ix.scopeRange(c.scope)
		if lo != c.wantLo || hi != c.wantHi {
			t.Errorf("scopeRange(%q) = [%d,%d), want [%d,%d) (%s)",
				c.scope, lo, hi, c.wantLo, c.wantHi, c.desc)
		}
	}
	// 区间内全部以 scope(文件取父目录)为前缀
	for _, c := range cases {
		lo, hi := ix.scopeRange(c.scope)
		prefix := ix.scopePrefix(c.scope)
		if prefix == "" {
			continue // 全量区间
		}
		for _, p := range ix.paths[lo:hi] {
			if !strings.HasPrefix(p, prefix) {
				t.Errorf("scope %q: %q in interval but not under prefix %q", c.scope, p, prefix)
			}
		}
	}
}

func TestFinderSkipsGit(t *testing.T) {
	srv, root := newTestServer(t)
	writeFile(t, filepath.Join(root, "ok.txt"), "x")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".git", "config"), "x")
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".git", "objects", "pack", "x.pack"), "x")

	conn := dialFinder(t, srv)
	waitFinderReady(t, conn)

	_, paths := findResultPaths(t, conn, "pack")
	if len(paths) != 0 {
		t.Fatalf("query pack: got %v, want none (.git skipped)", paths)
	}
	_, paths = findResultPaths(t, conn, "ok")
	if len(paths) != 1 || paths[0] != "ok.txt" {
		t.Fatalf("query ok: got %v, want [ok.txt]", paths)
	}
}

func TestFinderTruncated(t *testing.T) {
	srv, root := newTestServer(t)
	for i := 0; i < 250; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f_%03d.txt", i)), "x")
	}
	conn := dialFinder(t, srv)
	waitFinderReady(t, conn)

	resp, paths := findResultPaths(t, conn, "f_")
	if len(paths) != findMaxResults || !resp.Truncated || resp.Matched != 250 {
		t.Fatalf("got %d matches, truncated=%v matched=%d; want %d matches, truncated, matched=250",
			len(paths), resp.Truncated, resp.Matched, findMaxResults)
	}
}

func TestBestMatchesMatchesFind(t *testing.T) {
	// bestMatches must produce the same top-N as fuzzy.Find, in the same order.
	paths := make([]string, 0, 4000)
	for i := 0; i < 4000; i++ {
		switch i % 4 {
		case 0:
			paths = append(paths, fmt.Sprintf("src/pkg%03d/module%d/main.go", i%50, i))
		case 1:
			paths = append(paths, fmt.Sprintf("文档/项目%03d/说明%d.txt", i%50, i))
		case 2:
			paths = append(paths, fmt.Sprintf("assets/img%02d/shot%d.png", i%30, i))
		default:
			paths = append(paths, fmt.Sprintf("cmd/tool%d/util%d.go", i%40, i))
		}
	}
	for _, q := range []string{"main", "项目", "img", "util", "xyz不存在"} {
		want := fuzzy.Find(q, paths)
		if len(want) > findMaxResults {
			want = want[:findMaxResults]
		}
		got, matched := bestMatches(q, paths)
		if matched != len(fuzzy.Find(q, paths)) {
			t.Fatalf("q=%q: matched=%d, want %d", q, matched, len(fuzzy.Find(q, paths)))
		}
		if len(got) != len(want) {
			t.Fatalf("q=%q: got %d matches, want %d", q, len(got), len(want))
		}
		for i := range want {
			if got[i].Str != want[i].Str || got[i].Index != want[i].Index || got[i].Score != want[i].Score {
				t.Fatalf("q=%q: rank %d = {%q idx=%d score=%d}, want {%q idx=%d score=%d}",
					q, i, got[i].Str, got[i].Index, got[i].Score,
					want[i].Str, want[i].Index, want[i].Score)
			}
		}
	}
}

func TestRuneOffsets(t *testing.T) {
	s := "a中文b"
	if got := runeOffsets(s, []int{1, 4}); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("runeOffsets = %v, want [1 2]", got)
	}
	if got := runeOffsets(s, nil); got != nil {
		t.Fatalf("runeOffsets(nil) = %v, want nil", got)
	}
}

func TestFinderNoIndexBuildUntilConnect(t *testing.T) {
	// The walk is lazy: no connection, no walk.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "lazy.txt"), "x")
	h, err := newHandler(root, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitFinderReady(t, conn)
	sendQuery(t, conn, "")
	resp := readFind(t, conn)
	if resp.Indexed != 1 || resp.Partial {
		t.Fatalf("after connect: indexed=%d partial=%v, want 1/false", resp.Indexed, resp.Partial)
	}
}
