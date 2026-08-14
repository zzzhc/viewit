package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func newTestHandler(t *testing.T, dev bool) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	h, err := newHandler(root, dev)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	return h, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func doGet(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// doGetAE issues GET with an Accept-Encoding header.
func doGetAE(t *testing.T, h http.Handler, target, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("Accept-Encoding", acceptEncoding)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decodeList(t *testing.T, rr *httptest.ResponseRecorder) listResponse {
	t.Helper()
	var resp listResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v; body=%s", err, rr.Body.String())
	}
	return resp
}

func assertStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
	}
}

func TestListRoot(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "b.txt"), "bb")
	writeFile(t, filepath.Join(root, "a.txt"), "aa")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := doGet(t, h, "/api/list?path=")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if !resp.IsDir || resp.Path != "/" {
		t.Fatalf("want dir at /, got isDir=%v path=%q", resp.IsDir, resp.Path)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(resp.Entries), resp.Entries)
	}
	if resp.Entries[0].Name != "sub" || !resp.Entries[0].IsDir {
		t.Fatalf("entries[0] = %+v, want dir 'sub' first", resp.Entries[0])
	}
	if resp.Entries[1].Name != "a.txt" || resp.Entries[2].Name != "b.txt" {
		t.Fatalf("file order = %s, %s; want a.txt, b.txt", resp.Entries[1].Name, resp.Entries[2].Name)
	}
	if resp.Entries[1].Size != 2 {
		t.Fatalf("a.txt size = %d, want 2", resp.Entries[1].Size)
	}
}

func TestListDirsFirst(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	if err := os.Mkdir(filepath.Join(root, "zzz"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := doGet(t, h, "/api/list?path=")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if len(resp.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(resp.Entries))
	}
	if resp.Entries[0].Name != "zzz" || !resp.Entries[0].IsDir {
		t.Fatalf("entries[0] = %+v, want dir 'zzz' before files", resp.Entries[0])
	}
}

// TestListArchiveSize 覆盖 zip/tar 文件在目录列表中的契约:它们作为目录
// 排在前列(isDir=true,点击进入归档浏览),但仍是真实文件——isArchive=true
// 且 size 为磁盘上真实大小,前端据此显示大小而非 "-";真实目录无大小
// (isArchive 缺省 false),保持 "-"。
func TestListArchiveSize(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"x.txt": "x"})
	writeFile(t, filepath.Join(root, "b.tar"), strings.Repeat("x", 1024)) // 只需扩展名,内容无关
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "f.txt"), "f")

	rr := doGet(t, h, "/api/list?path=")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	byName := map[string]fileEntry{}
	for _, e := range resp.Entries {
		byName[e.Name] = e
	}
	z := byName["a.zip"]
	if !z.IsDir || !z.IsArchive || z.Size <= 0 {
		t.Fatalf("a.zip = %+v, want isDir+isArchive with real size", z)
	}
	tar := byName["b.tar"]
	if !tar.IsDir || !tar.IsArchive || tar.Size != 1024 {
		t.Fatalf("b.tar = %+v, want isDir+isArchive with size 1024", tar)
	}
	d := byName["dir"]
	if !d.IsDir || d.IsArchive {
		t.Fatalf("dir = %+v, want plain dir without isArchive", d)
	}
	f := byName["f.txt"]
	if f.IsDir || f.IsArchive {
		t.Fatalf("f.txt = %+v, want plain file", f)
	}
}

// TestListPagination 覆盖目录列表的 limit/offset 分页:排序全局一致(目录
// 优先 + 名称升序),每页只含切片内的条目,total 报告全量;不带参数时
// 保持全量旧行为。
func TestListPagination(t *testing.T) {
	h, root := newTestHandler(t, false)
	// 2 个目录 + 5 个文件,排序后:dir_a, dir_z, f1..f5
	for _, name := range []string{"dir_a", "dir_z"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"f3.txt", "f1.txt", "f5.txt", "f2.txt", "f4.txt"} {
		writeFile(t, filepath.Join(root, name), "x")
	}

	// 第 0 页:2 条(只含目录)
	rr := doGet(t, h, "/api/list?path=&limit=2")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if resp.Total != 7 || resp.Offset != 0 {
		t.Fatalf("total/offset = %d/%d, want 7/0", resp.Total, resp.Offset)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(resp.Entries))
	}
	if resp.Entries[0].Name != "dir_a" || resp.Entries[1].Name != "dir_z" {
		t.Fatalf("page0 = %q, %q; want dir_a, dir_z", resp.Entries[0].Name, resp.Entries[1].Name)
	}

	// 第 1 页:offset=2, 3 条文件
	rr = doGet(t, h, "/api/list?path=&offset=2&limit=3")
	resp = decodeList(t, rr)
	if resp.Offset != 2 {
		t.Fatalf("offset = %d, want 2", resp.Offset)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(resp.Entries))
	}
	if resp.Entries[0].Name != "f1.txt" || resp.Entries[2].Name != "f3.txt" {
		t.Fatalf("page1 = %s..%s; want f1..f3", resp.Entries[0].Name, resp.Entries[2].Name)
	}

	// 最后一页:offset 超过 total 时不越界
	rr = doGet(t, h, "/api/list?path=&offset=100&limit=10")
	resp = decodeList(t, rr)
	if resp.Offset != 7 || len(resp.Entries) != 0 {
		t.Fatalf("overflow page: offset=%d entries=%d, want 7/0", resp.Offset, len(resp.Entries))
	}

	// 不带分页参数:全量 + total
	rr = doGet(t, h, "/api/list?path=")
	resp = decodeList(t, rr)
	if resp.Total != 7 || len(resp.Entries) != 7 {
		t.Fatalf("full list: total=%d entries=%d, want 7/7", resp.Total, len(resp.Entries))
	}

	// 文件分支忽略分页参数
	rr = doGet(t, h, "/api/list?path=f1.txt&limit=0")
	assertStatus(t, rr, http.StatusOK)
	resp = decodeList(t, rr)
	if resp.IsDir || resp.File == nil {
		t.Fatalf("file branch: want file response, got %+v", resp)
	}
}

// TestListImages 覆盖 /api/list?images=1:只返回目录下图片文件名(目录排序
// 序,目录/归档视为目录排除,扩展名大小写不敏感),不做分页;归档内部路径
// 同样支持。该列表是图片查看器"上一张/下一张"切换的契约。
func TestListImages(t *testing.T) {
	h, root := newTestHandler(t, false)
	if err := os.Mkdir(filepath.Join(root, "pics"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"d.txt", "B.JPG", "a.png", "e.gif", "c.tif"} {
		writeFile(t, filepath.Join(root, name), "x")
	}

	rr := doGet(t, h, "/api/list?path=&images=1")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if !resp.IsDir || resp.Images == nil {
		t.Fatalf("want dir with images, got %+v", resp)
	}
	want := []string{"B.JPG", "a.png", "c.tif", "e.gif"}
	if len(resp.Images) != len(want) {
		t.Fatalf("images = %v, want %v", resp.Images, want)
	}
	for i, n := range want {
		if resp.Images[i] != n {
			t.Fatalf("images = %v, want %v", resp.Images, want)
		}
	}
	if resp.Total != 0 || resp.Offset != 0 || len(resp.Entries) != 0 {
		t.Fatalf("images=1 不应返回分页字段: %+v", resp)
	}

	// 归档内部:图片成员按同序返回,目录成员排除
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{
		"img/pic1.png":  "p1",
		"img/pic2.JPG":  "p2",
		"img/note.txt":  "n",
		"img/":          "",
		"docs/readme":   "r",
		"pic0.png":      "p0",
	})
	rr = doGet(t, h, "/api/list?path=a.zip&images=1")
	assertStatus(t, rr, http.StatusOK)
	resp = decodeList(t, rr)
	want = []string{"pic0.png"}
	if len(resp.Images) != len(want) || resp.Images[0] != want[0] {
		t.Fatalf("zip root images = %v, want %v", resp.Images, want)
	}
	rr = doGet(t, h, "/api/list?path=a.zip/img&images=1")
	assertStatus(t, rr, http.StatusOK)
	resp = decodeList(t, rr)
	want = []string{"pic1.png", "pic2.JPG"}
	if len(resp.Images) != len(want) {
		t.Fatalf("zip img images = %v, want %v", resp.Images, want)
	}
	for i, n := range want {
		if resp.Images[i] != n {
			t.Fatalf("zip img images = %v, want %v", resp.Images, want)
		}
	}

	// 单文件路径:images=1 不影响单文件分支(返回 file)
	rr = doGet(t, h, "/api/list?path=a.png&images=1")
	assertStatus(t, rr, http.StatusOK)
	resp = decodeList(t, rr)
	if resp.IsDir || resp.File == nil || resp.File.Name != "a.png" {
		t.Fatalf("file branch with images=1: want file a.png, got %+v", resp)
	}
}

func TestListFilePathReturnsFile(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "x.go"), "package main\n")

	rr := doGet(t, h, "/api/list?path=x.go")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if resp.IsDir || resp.File == nil {
		t.Fatalf("want file response, got isDir=%v file=%v", resp.IsDir, resp.File)
	}
	if resp.File.Name != "x.go" || resp.File.Size != 13 {
		t.Fatalf("file = %+v, want x.go size 13", resp.File)
	}
	if resp.Path != "/x.go" {
		t.Fatalf("path = %q, want /x.go", resp.Path)
	}
}

func TestListMissing404(t *testing.T) {
	h, _ := newTestHandler(t, false)
	rr := doGet(t, h, "/api/list?path=missing")
	assertStatus(t, rr, http.StatusNotFound)
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("want JSON error, got body=%s", rr.Body.String())
	}
}

func TestFileServeContentType(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := "package main\n\nfunc main() {}\n"
	writeFile(t, filepath.Join(root, "hello.go"), content)

	rr := doGet(t, h, "/api/file?path=hello.go")
	assertStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/") {
		t.Fatalf("Content-Type = %q, want text/*", ct)
	}
	if rr.Body.String() != content {
		t.Fatalf("body = %q, want %q", rr.Body.String(), content)
	}
}

// TestAPIContentEncoding 覆盖 /api/list 与 /api/file 的 Accept-Encoding
// 协商:可压缩内容(JSON 列表、文本文件)按客户端接受度以 br/gzip 编码
// 传输,未声明时原样发送;Vary 头始终存在供缓存区分编码;Range 请求
// 必须原样字节,绝不压缩。
func TestAPIContentEncoding(t *testing.T) {
	h, root := newTestHandler(t, false)
	// 一批重复度高的文件,让列表 JSON 有明显压缩空间
	for i := range 50 {
		writeFile(t, filepath.Join(root, "log"+strconv.Itoa(i)+".txt"), strings.Repeat("line of log\n", 20))
	}

	// gzip
	rr := doGetAE(t, h, "/api/list?path=", "gzip")
	assertStatus(t, rr, http.StatusOK)
	if enc := rr.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("list Content-Encoding = %q, want gzip", enc)
	}
	if vary := rr.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	var resp listResponse
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("decompressed list: bad json: %v", err)
	}
	if len(resp.Entries) != 50 {
		t.Fatalf("entries = %d, want 50", len(resp.Entries))
	}

	// br 优先于 gzip
	rr = doGetAE(t, h, "/api/list?path=", "gzip, br")
	assertStatus(t, rr, http.StatusOK)
	if enc := rr.Header().Get("Content-Encoding"); enc != "br" {
		t.Fatalf("list Content-Encoding = %q, want br (better ratio wins)", enc)
	}
	if _, err := io.ReadAll(brotli.NewReader(rr.Body)); err != nil {
		t.Fatalf("brotli read: %v", err)
	}

	// 未声明 Accept-Encoding:原样发送,但仍带 Vary
	rr = doGet(t, h, "/api/list?path=")
	assertStatus(t, rr, http.StatusOK)
	if enc := rr.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("list Content-Encoding = %q, want none", enc)
	}
	if vary := rr.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding even without encoding", vary)
	}

	// 文本文件经 /api/file 压缩
	content := strings.Repeat("hello viewit\n", 100)
	writeFile(t, filepath.Join(root, "notes.txt"), content)
	rr = doGetAE(t, h, "/api/file?path=notes.txt", "gzip")
	assertStatus(t, rr, http.StatusOK)
	if enc := rr.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("file Content-Encoding = %q, want gzip", enc)
	}
	zr, err = gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err = io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if string(got) != content {
		t.Fatalf("decompressed file mismatch: got %d bytes, want %d", len(got), len(content))
	}

	// Range 请求不压缩(ServeContent 的 206 必须原样字节)
	req := httptest.NewRequest("GET", "/api/file?path=notes.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("Accept-Encoding", "gzip")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rr.Code)
	}
	if enc := rr.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Range response Content-Encoding = %q, want none", enc)
	}
}

func TestRawFileServesAtMirroredPath(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := "<html><body>hi</body></html>"
	if err := os.MkdirAll(filepath.Join(root, "site", "pages"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "site", "pages", "index.html"), content)

	rr := doGet(t, h, "/api/raw/site/pages/index.html")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != content {
		t.Fatalf("body = %q, want %q", rr.Body.String(), content)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}

	// module scripts refuse to run without a JavaScript MIME type
	writeFile(t, filepath.Join(root, "site", "pages", "app.js"), "export const x = 1\n")
	rr = doGet(t, h, "/api/raw/site/pages/app.js")
	assertStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("js Content-Type = %q, want text/javascript", ct)
	}
}

func TestRawFileRefusals(t *testing.T) {
	h, root := newTestHandler(t, false)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := doGet(t, h, "/api/raw/")
	assertStatus(t, rr, http.StatusBadRequest)
	rr = doGet(t, h, "/api/raw/dir")
	assertStatus(t, rr, http.StatusBadRequest)
	rr = doGet(t, h, "/api/raw/missing.txt")
	assertStatus(t, rr, http.StatusNotFound)
}

func TestNullOriginCORS(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "page.html"), "<html></html>")

	// sandboxed preview iframe sends Origin: null; its module scripts and
	// fetches need the ACAO header to be readable
	req := httptest.NewRequest("GET", "/api/raw/page.html", nil)
	req.Header.Set("Origin", "null")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("ACAO = %q, want * for Origin: null", rr.Header().Get("Access-Control-Allow-Origin"))
	}

	// same-origin requests must not carry the header
	rr = doGet(t, h, "/api/raw/page.html")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("ACAO = %q, want unset for same-origin request", rr.Header().Get("Access-Control-Allow-Origin"))
	}

	// preflight for a null-origin POST is answered without reaching the handler
	pre := httptest.NewRequest("OPTIONS", "/api/list", nil)
	pre.Header.Set("Origin", "null")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	preRr := httptest.NewRecorder()
	h.ServeHTTP(preRr, pre)
	assertStatus(t, preRr, http.StatusNoContent)
	if preRr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight ACAO = %q, want *", preRr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestFileRange(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := "0123456789"
	writeFile(t, filepath.Join(root, "hello.txt"), content)

	req := httptest.NewRequest("GET", "/api/file?path=hello.txt", nil)
	req.Header.Set("Range", "bytes=0-3")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assertStatus(t, rr, http.StatusPartialContent)
	if cr := rr.Header().Get("Content-Range"); cr != "bytes 0-3/10" {
		t.Fatalf("Content-Range = %q, want bytes 0-3/10", cr)
	}
	if rr.Body.String() != "0123" {
		t.Fatalf("body = %q, want first 4 bytes", rr.Body.String())
	}
	if rr.Header().Get("Content-Length") != "4" {
		t.Fatalf("Content-Length = %q, want 4", rr.Header().Get("Content-Length"))
	}
}

func TestTraversalRejected(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "ok.txt"), "ok")
	paths := []string{
		"..",
		"../..",
		"..%2f..%2fetc%2fpasswd",
		"%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/etc/passwd",
		"sub/../../etc/passwd",
	}
	for _, p := range paths {
		rr := doGet(t, h, "/api/list?path="+p)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400; body=%s", p, rr.Code, rr.Body.String())
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Errorf("path=%q: want JSON error, got body=%s", p, rr.Body.String())
		}
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	h, root := newTestHandler(t, false)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rr := doGet(t, h, "/api/file?path=evil")
	assertStatus(t, rr, http.StatusBadRequest)
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("want JSON error, got body=%s", rr.Body.String())
	}
}

func TestSymlinkInsideRootFollowed(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "real.txt"), "hello")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rr := doGet(t, h, "/api/file?path=link.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "hello" {
		t.Fatalf("body = %q, want hello (symlink followed)", rr.Body.String())
	}
}

func TestDevModeServesSPA(t *testing.T) {
	h, _ := newTestHandler(t, true)
	for _, p := range []string{"/", "/some/spa/route"} {
		rr := doGet(t, h, p)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (dev mode serves the embedded frontend)", p, rr.Code)
			continue
		}
		if rr.Body.Len() == 0 {
			t.Errorf("%s: empty body", p)
		}
	}
}

func TestIndexServed(t *testing.T) {
	h, _ := newTestHandler(t, false)
	rr := doGet(t, h, "/")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.Len() == 0 {
		t.Fatal("index body is empty")
	}
}

func TestListMimeFromContent(t *testing.T) {
	h, root := newTestHandler(t, false)
	files := map[string]struct {
		content []byte
		want    string
	}{
		"real.png":  {[]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRjunk"), "image/png"},
		"fake.png":  {[]byte("this is text pretending to be a png\n"), "text/plain"},
		"logo.svg":  {[]byte("<?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"><path/></svg>"), "image/svg+xml"},
		"clip.webm": {[]byte{0x1a, 0x45, 0xdf, 0xa3, 0x01, 0x00, 0x00, 0x00}, "video/webm"},
		"img.avif":  {[]byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f'}, "image/avif"},
		"icon.ico":  {[]byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, "image/x-icon"},
		// TIFF 签名不在 DetectContentType 表里,由自定义签名兜底(小端/大端)
		"le.tif":    {[]byte{0x49, 0x49, 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00}, "image/tiff"},
		"be.tiff":   {[]byte{0x4d, 0x4d, 0x00, 0x2a, 0x00, 0x00, 0x00, 0x08}, "image/tiff"},
		"notes.map": {[]byte("{\"version\":3,\"sources\":[]}\n"), "application/json"},
		"arr.json":  {[]byte("[\n  1,\n  2\n]\n"), "application/json"},
		"doc.go":    {[]byte("package main\n"), "text/plain"},
		// '['/'{' 开头的非 JSON 文本不得误判为 application/json(严格结构校验)
		"codegraph.log": {[]byte("[CodeGraph] v1.5.0 is available. Update with `codegraph upgrade`.\n"), "text/plain"},
		"ts.log":        {[]byte("[2026-08-14 16:25:03] INFO starting worker\n"), "text/plain"},
		"brace.txt":     {[]byte("{ echo hello; }\n"), "text/plain"},
		// 真 JSON 数组/对象仍要识别,包括 512B 截断的情况
		"json.log":  {[]byte("[{\"a\":1},{\"b\":2}]\n"), "application/json"},
		"big.json":  {[]byte("{\"data\": [" + strings.Repeat("1,", 300) + "1]}\n"), "application/json"},
		"open.json": {[]byte("[" + strings.Repeat("1,", 300)), "application/json"},
		// 截断且括号未闭合的超长日志行:数字后跟 '-' 不是合法 JSON 数字
		"long.log": {[]byte("[2026-08-14 16:25:03] " + strings.Repeat("x", 600) + "\n"), "text/plain"},
	}
	for name, tc := range files {
		if err := os.WriteFile(filepath.Join(root, name), tc.content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for name, tc := range files {
		rr := doGet(t, h, "/api/list?path="+name)
		assertStatus(t, rr, http.StatusOK)
		resp := decodeList(t, rr)
		if resp.File == nil {
			t.Fatalf("%s: want file response, got %+v", name, resp)
		}
		if resp.File.Mime != tc.want {
			t.Errorf("%s: mime = %q, want %q", name, resp.File.Mime, tc.want)
		}
	}
}

func TestDownloadFileGzip(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := strings.Repeat("hello viewit\n", 2000) // ~28KB, compressible
	writeFile(t, filepath.Join(root, "log.txt"), content)

	rr := doGetAE(t, h, "/api/download?path=log.txt", "gzip")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rr.Header().Get("Content-Encoding"))
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "log.txt") {
		t.Fatalf("Content-Disposition = %q, want attachment with log.txt", cd)
	}
	if rr.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length = %q, want absent (chunked gzip stream)", rr.Header().Get("Content-Length"))
	}
	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(got) != content {
		t.Fatalf("decompressed body mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestDownloadFileCompressedRatio(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := strings.Repeat("aaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", 10000) // highly repetitive
	writeFile(t, filepath.Join(root, "rep.txt"), content)

	rr := doGetAE(t, h, "/api/download?path=rep.txt", "gzip")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.Len() >= len(content) {
		t.Fatalf("gzip body = %d bytes, want < %d (BestCompression should shrink repetitive text)", rr.Body.Len(), len(content))
	}
}

func TestDownloadFileBinaryIdentity(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRjunkbinarydata\x00\x01\x02")
	if err := os.WriteFile(filepath.Join(root, "img.png"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	rr := doGet(t, h, "/api/download?path=img.png")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want none for already-compressed content", rr.Header().Get("Content-Encoding"))
	}
	if rr.Header().Get("Content-Length") != strconv.Itoa(len(content)) {
		t.Fatalf("Content-Length = %q, want %d", rr.Header().Get("Content-Length"), len(content))
	}
	if !bytes.Equal(rr.Body.Bytes(), content) {
		t.Fatalf("binary body mismatch: got %d bytes, want %d", rr.Body.Len(), len(content))
	}
}

func TestDownloadDirZip(t *testing.T) {
	h, root := newTestHandler(t, false)
	for _, d := range []string{"sub", "sub/nested"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "sub", "a.txt"), strings.Repeat("text data\n", 100))
	writeFile(t, filepath.Join(root, "sub", "b.bin"), "\x00\x01\x02\x03")
	writeFile(t, filepath.Join(root, "sub", "nested", "c.log"), "nested content\n")
	writeFile(t, filepath.Join(root, "sub", "图片 文件.txt"), "中文文件名\n")

	rr := doGet(t, h, "/api/download?path=sub")
	assertStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "sub.zip") {
		t.Fatalf("Content-Disposition = %q, want attachment with sub.zip", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	wantNames := []string{"sub/", "sub/a.txt", "sub/b.bin", "sub/nested/", "sub/nested/c.log", "sub/图片 文件.txt"}
	if len(zr.File) != len(wantNames) {
		t.Fatalf("zip entries = %d, want %d: %v", len(zr.File), len(wantNames), zr.File)
	}
	for _, name := range wantNames {
		if _, ok := byName[name]; !ok {
			t.Errorf("zip missing entry %q; have %v", name, byName)
		}
	}
	// text entry: deflated; binary entry: stored verbatim
	if byName["sub/a.txt"].Method != zip.Deflate {
		t.Errorf("a.txt method = %v, want Deflate", byName["sub/a.txt"].Method)
	}
	if byName["sub/b.bin"].Method != zip.Store {
		t.Errorf("b.bin method = %v, want Store", byName["sub/b.bin"].Method)
	}
	for name, want := range map[string]string{
		"sub/a.txt":        strings.Repeat("text data\n", 100),
		"sub/b.bin":        "\x00\x01\x02\x03",
		"sub/nested/c.log": "nested content\n",
		"sub/图片 文件.txt":    "中文文件名\n",
	} {
		rc, err := byName[name].Open()
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: read: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s: content = %q, want %q", name, got, want)
		}
	}
}

func TestDownloadEmptyDirZip(t *testing.T) {
	h, root := newTestHandler(t, false)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := doGet(t, h, "/api/download?path=empty")
	assertStatus(t, rr, http.StatusOK)
	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "empty/" {
		t.Fatalf("entries = %v, want just the empty/ folder entry", zr.File)
	}
}

func TestDownloadZipSymlinks(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "real.txt"), "hello")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "dir", "inside.txt"), "inside")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "ln.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "evil.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "dirlink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rr := doGet(t, h, "/api/download?path=")
	assertStatus(t, rr, http.StatusOK)
	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	top := filepath.Base(root) + "/"
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	// safe file symlink is followed into the archive...
	if !names[top+"ln.txt"] {
		t.Errorf("zip missing followed symlink ln.txt: %v", names)
	}
	// ...but escaping and directory symlinks are dropped
	if names[top+"evil.txt"] {
		t.Errorf("zip contains escaping symlink evil.txt")
	}
	if names[top+"dirlink/"] || names[top+"dirlink/inside.txt"] {
		t.Errorf("zip contains directory symlink dirlink")
	}
}

func TestDownloadRootUsesRootDirName(t *testing.T) {
	root := t.TempDir()
	h, err := newHandler(root, false)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	writeFile(t, filepath.Join(root, "x.txt"), "x")

	rr := doGet(t, h, "/api/download?path=")
	assertStatus(t, rr, http.StatusOK)
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, ".zip") {
		t.Fatalf("Content-Disposition = %q, want zip attachment", cd)
	}
}

func TestDownloadMissing404(t *testing.T) {
	h, _ := newTestHandler(t, false)
	rr := doGet(t, h, "/api/download?path=missing")
	assertStatus(t, rr, http.StatusNotFound)
}

func TestDownloadTraversalRejected(t *testing.T) {
	h, _ := newTestHandler(t, false)
	for _, p := range []string{"..", "../..", "/etc/passwd", "sub/../../etc/passwd"} {
		rr := doGet(t, h, "/api/download?path="+p)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", p, rr.Code)
		}
	}
}

func TestPreferredEncoding(t *testing.T) {
	cases := []struct {
		ae   string
		want string
	}{
		{"", ""},
		{"gzip", "gzip"},
		{"br", "br"},
		{"gzip, br", "br"}, // better ratio wins
		{"gzip;q=1.0, br;q=0.5", "gzip"},
		{"br;q=0, gzip;q=1.0", "gzip"},
		{"gzip;q=0", ""},
		{"*", "br"},
		{"*;q=0.5", "br"},
		{"*;q=0", ""},
		{"gzip;q=0.8, *;q=0", "gzip"}, // explicit entry beats the wildcard
		{"identity", ""},
		{"gzip;q=0, br;q=0, *;q=1", ""},           // explicit refusals beat the wildcard
		{"deflate", ""},                           // unsupported coding: identity
		{"gzip, br;q=0.5, deflate;q=0.1", "gzip"}, // client prefers gzip explicitly
		{"gzip;q=0.5, br;q=0.5", "br"},            // tie: better ratio wins
	}
	for _, tc := range cases {
		if got := preferredEncoding(tc.ae); got != tc.want {
			t.Errorf("preferredEncoding(%q) = %q, want %q", tc.ae, got, tc.want)
		}
	}
}

func TestDownloadFileBrotli(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := strings.Repeat("hello viewit\n", 2000)
	writeFile(t, filepath.Join(root, "log.txt"), content)

	rr := doGetAE(t, h, "/api/download?path=log.txt", "br")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("Content-Encoding = %q, want br", rr.Header().Get("Content-Encoding"))
	}
	if vary := rr.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	got, err := io.ReadAll(brotli.NewReader(rr.Body))
	if err != nil {
		t.Fatalf("brotli decompress: %v", err)
	}
	if string(got) != content {
		t.Fatalf("decompressed body mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestDownloadFileBrotliPreferredOverGzip(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "log.txt"), strings.Repeat("hello viewit\n", 2000))

	// 客户端同时接受两种编码时,选择压缩比更好的 br
	rr := doGetAE(t, h, "/api/download?path=log.txt", "gzip, br")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("Content-Encoding = %q, want br (better ratio wins)", rr.Header().Get("Content-Encoding"))
	}
}

func TestDownloadFileIdentityWhenNotAccepted(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := strings.Repeat("hello viewit\n", 2000)
	writeFile(t, filepath.Join(root, "log.txt"), content)

	for _, ae := range []string{"identity", "gzip;q=0, br;q=0"} {
		rr := doGetAE(t, h, "/api/download?path=log.txt", ae)
		assertStatus(t, rr, http.StatusOK)
		if rr.Header().Get("Content-Encoding") != "" {
			t.Fatalf("Accept-Encoding %q: Content-Encoding = %q, want none", ae, rr.Header().Get("Content-Encoding"))
		}
		if rr.Header().Get("Content-Length") != strconv.Itoa(len(content)) {
			t.Fatalf("Accept-Encoding %q: Content-Length = %q, want %d", ae, rr.Header().Get("Content-Length"), len(content))
		}
		if rr.Body.String() != content {
			t.Fatalf("Accept-Encoding %q: body mismatch", ae)
		}
	}
}

func TestDownloadFileVaryOnIdentity(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "log.txt"), strings.Repeat("hello viewit\n", 100))

	// 未声明 Accept-Encoding 时原样发送,但仍需 Vary 供缓存区分编码
	rr := doGet(t, h, "/api/download?path=log.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want none without Accept-Encoding", rr.Header().Get("Content-Encoding"))
	}
	if vary := rr.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
}

func gunzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		ae   string
		want bool
	}{
		{"", false},
		{"identity", false},
		{"gzip", true},
		{"gzip;q=0", false},
		{"br", false},
		{"br, gzip", true},
		{"*", true},
		{"br, *;q=0", false},   // 未提 gzip,通配拒绝 → 不接受
		{"gzip;q=0, *", false}, // 显式 q=0 拒绝优先于通配
		{"gzip;q=0.5", true},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.ae); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.ae, got, tc.want)
		}
	}
}

func TestIndexCacheControl(t *testing.T) {
	h, _ := newTestHandler(t, false)
	gz, err := fs.ReadFile(embedFS, "frontend/dist.gz/index.html.gz")
	if err != nil {
		t.Fatalf("read embedded gzipped index: %v", err)
	}
	want := gunzipBytes(t, gz)

	// 接受 gzip:原样下发预压缩字节
	rr := doGetAE(t, h, "/", "gzip")
	assertStatus(t, rr, http.StatusOK)
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rr.Header().Get("Content-Encoding"))
	}
	if rr.Header().Get("Content-Length") != strconv.Itoa(len(gz)) {
		t.Fatalf("Content-Length = %q, want %d (pre-gzipped)", rr.Header().Get("Content-Length"), len(gz))
	}
	if !bytes.Equal(rr.Body.Bytes(), gz) {
		t.Fatalf("index body = %d bytes, want %d pre-gzipped bytes", rr.Body.Len(), len(gz))
	}

	// 未声明 Accept-Encoding:解压后以 identity 下发原文(禁 gzip 的客户端可用)
	rr = doGet(t, h, "/")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want none (identity)", rr.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(rr.Body.Bytes(), want) {
		t.Fatalf("identity index body mismatch: got %d bytes, want %d", rr.Body.Len(), len(want))
	}

	// 显式拒绝 gzip:同样解压后下发原文
	rr = doGetAE(t, h, "/", "gzip;q=0")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want none (gzip refused)", rr.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(rr.Body.Bytes(), want) {
		t.Fatalf("refused-gzip index body mismatch: got %d bytes, want %d", rr.Body.Len(), len(want))
	}
}

func TestAssetCacheControl(t *testing.T) {
	h, _ := newTestHandler(t, false)
	assets, err := fs.Glob(embedFS, "frontend/dist.gz/assets/*.js.gz")
	if err != nil || len(assets) == 0 {
		t.Fatalf("no embedded gzipped js assets: %v", err)
	}
	asset := strings.TrimSuffix(strings.TrimPrefix(assets[0], "frontend/dist.gz/"), ".gz")
	gz, err := fs.ReadFile(embedFS, assets[0])
	if err != nil {
		t.Fatalf("read %s: %v", assets[0], err)
	}
	want := gunzipBytes(t, gz)

	// 接受 gzip:哈希资源不可变缓存,预 gzip 字节原样下发
	rr := doGetAE(t, h, "/"+asset, "gzip")
	assertStatus(t, rr, http.StatusOK)
	if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable", cc)
	}
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rr.Header().Get("Content-Encoding"))
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want a javascript MIME", ct)
	}
	if rr.Header().Get("Content-Length") != strconv.Itoa(len(gz)) {
		t.Fatalf("Content-Length = %q, want %d", rr.Header().Get("Content-Length"), len(gz))
	}
	if !bytes.Equal(rr.Body.Bytes(), gz) {
		t.Fatalf("asset body = %d bytes, want %d pre-gzipped bytes", rr.Body.Len(), len(gz))
	}

	// 未声明 Accept-Encoding:解压后以 identity 下发原文
	rr = doGet(t, h, "/"+asset)
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want none (identity)", rr.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(rr.Body.Bytes(), want) {
		t.Fatalf("identity asset body mismatch: got %d bytes, want %d", rr.Body.Len(), len(want))
	}

	// 客户端只接受 br:中间件把解压后的原文再压成 br
	rr = doGetAE(t, h, "/"+asset, "br")
	assertStatus(t, rr, http.StatusOK)
	if rr.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("Content-Encoding = %q, want br", rr.Header().Get("Content-Encoding"))
	}
	if vary := rr.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	got, err := io.ReadAll(brotli.NewReader(rr.Body))
	if err != nil {
		t.Fatalf("brotli decompress: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("brotli asset mismatch: got %d bytes, want %d", len(got), len(want))
	}
}
