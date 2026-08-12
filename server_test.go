package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
		"notes.map": {[]byte("{\"version\":3,\"sources\":[]}\n"), "application/json"},
		"arr.json":  {[]byte("[\n  1,\n  2\n]\n"), "application/json"},
		"doc.go":    {[]byte("package main\n"), "text/plain"},
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

	rr := doGet(t, h, "/api/download?path=log.txt")
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

	rr := doGet(t, h, "/api/download?path=rep.txt")
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
