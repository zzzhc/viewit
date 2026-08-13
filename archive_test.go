package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeZipFile(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTarFile(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	// 显式目录条目先写,确保目录排序/合成行为可预测
	dirs := map[string]bool{}
	for name := range entries {
		for {
			i := strings.LastIndexByte(name, '/')
			if i < 0 {
				break
			}
			name = name[:i]
			dirs[name] = true
		}
	}
	for d := range dirs {
		if err := tw.WriteHeader(&tar.Header{Name: d + "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func doGetRange(t *testing.T, h http.Handler, target, rng string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("Range", rng)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// 归档文件在父目录列表中被视为目录。
func TestListArchiveAsDir(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"x.txt": "x"})
	writeTarFile(t, filepath.Join(root, "b.tar"), map[string]string{"y.txt": "y"})

	rr := doGet(t, h, "/api/list?path=")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(resp.Entries))
	}
	for _, e := range resp.Entries {
		if !e.IsDir {
			t.Errorf("entry %q should be a directory (archive), got isDir=false", e.Name)
		}
	}
}

func TestListZipContents(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{
		"top.txt":   "top",
		"sub/":      "",
		"sub/b.txt": "b",
		"data.json": `{"k":1}`,
	})

	// 归档根:目录在前,文件按名排序
	rr := doGet(t, h, "/api/list?path=a.zip")
	assertStatus(t, rr, http.StatusOK)
	rootResp := decodeList(t, rr)
	if !rootResp.IsDir {
		t.Fatalf("archive root should be a dir")
	}
	if len(rootResp.Entries) != 3 {
		t.Fatalf("archive root entries = %d, want 3: %+v", len(rootResp.Entries), rootResp.Entries)
	}
	if rootResp.Entries[0].Name != "sub" || !rootResp.Entries[0].IsDir {
		t.Fatalf("entries[0] = %+v, want dir 'sub' first", rootResp.Entries[0])
	}

	// 子目录
	rr = doGet(t, h, "/api/list?path=a.zip/sub")
	assertStatus(t, rr, http.StatusOK)
	subResp := decodeList(t, rr)
	if len(subResp.Entries) != 1 || subResp.Entries[0].Name != "b.txt" || subResp.Entries[0].IsDir {
		t.Fatalf("sub entries = %+v, want file b.txt", subResp.Entries)
	}

	// 归档内单文件:返回 file 且按内容嗅探 MIME
	rr = doGet(t, h, "/api/list?path=a.zip/data.json")
	assertStatus(t, rr, http.StatusOK)
	fResp := decodeList(t, rr)
	if fResp.IsDir || fResp.File == nil || fResp.File.Name != "data.json" {
		t.Fatalf("want file data.json, got %+v", fResp)
	}
	if fResp.File.Mime != "application/json" {
		t.Fatalf("mime = %q, want application/json", fResp.File.Mime)
	}
}

func TestListTarContents(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeTarFile(t, filepath.Join(root, "a.tar"), map[string]string{
		"top.txt":        "top",
		"sub/b.txt":      "b",
		"sub/deep/c.txt": "c",
	})

	rr := doGet(t, h, "/api/list?path=a.tar")
	assertStatus(t, rr, http.StatusOK)
	rootResp := decodeList(t, rr)
	if len(rootResp.Entries) != 2 {
		t.Fatalf("tar root entries = %d, want 2: %+v", len(rootResp.Entries), rootResp.Entries)
	}
	if rootResp.Entries[0].Name != "sub" || !rootResp.Entries[0].IsDir {
		t.Fatalf("entries[0] = %+v, want dir 'sub'", rootResp.Entries[0])
	}

	rr = doGet(t, h, "/api/list?path=a.tar/sub")
	assertStatus(t, rr, http.StatusOK)
	subResp := decodeList(t, rr)
	if len(subResp.Entries) != 2 {
		t.Fatalf("tar sub entries = %d, want 2 (b.txt + deep): %+v", len(subResp.Entries), subResp.Entries)
	}
	if subResp.Entries[0].Name != "deep" || !subResp.Entries[0].IsDir {
		t.Fatalf("entries[0] = %+v, want dir 'deep'", subResp.Entries[0])
	}
	if subResp.Entries[1].Name != "b.txt" || subResp.Entries[1].Size != 1 {
		t.Fatalf("entries[1] = %+v, want file b.txt size 1", subResp.Entries[1])
	}
}

func TestFileInZip(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := "0123456789abcdef"
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"data.txt": content})

	rr := doGet(t, h, "/api/file?path=a.zip/data.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != content {
		t.Fatalf("body = %q, want %q", rr.Body.String(), content)
	}

	// Range/206:zip 成员可 seek
	rr = doGetRange(t, h, "/api/file?path=a.zip/data.txt", "bytes=4-7")
	assertStatus(t, rr, http.StatusPartialContent)
	if rr.Body.String() != "4567" {
		t.Fatalf("range body = %q, want %q", rr.Body.String(), "4567")
	}
}

func TestFileInTar(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := "0123456789abcdef"
	writeTarFile(t, filepath.Join(root, "a.tar"), map[string]string{"data.txt": content})

	rr := doGet(t, h, "/api/file?path=a.tar/data.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != content {
		t.Fatalf("body = %q, want %q", rr.Body.String(), content)
	}

	// Range/206:tar 成员可 seek
	rr = doGetRange(t, h, "/api/file?path=a.tar/data.txt", "bytes=8-11")
	assertStatus(t, rr, http.StatusPartialContent)
	if rr.Body.String() != "89ab" {
		t.Fatalf("range body = %q, want %q", rr.Body.String(), "89ab")
	}
}

func TestDownloadZipMember(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"data.txt": "hello member"})

	rr := doGet(t, h, "/api/download?path=a.zip/data.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "hello member" {
		t.Fatalf("body = %q, want member bytes", rr.Body.String())
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "data.txt") {
		t.Fatalf("Content-Disposition = %q, want filename data.txt", cd)
	}
}

func TestDownloadArchiveFileItself(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"data.txt": "hello"})

	// 下载归档文件本身,应原样返回归档字节而非重新打包
	raw, err := os.ReadFile(filepath.Join(root, "a.zip"))
	if err != nil {
		t.Fatal(err)
	}
	rr := doGet(t, h, "/api/download?path=a.zip")
	assertStatus(t, rr, http.StatusOK)
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/zip") {
		t.Fatalf("Content-Type = %q, want zip", rr.Header().Get("Content-Type"))
	}
	if rr.Body.Len() != len(raw) {
		t.Fatalf("body len = %d, want %d (archive bytes verbatim)", rr.Body.Len(), len(raw))
	}
}

func TestRawZipMember(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"page.html": "<html>hi</html>"})

	rr := doGet(t, h, "/api/raw/a.zip/page.html")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "<html>hi</html>" {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestArchiveTraversalRejected(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"data.txt": "x"})
	writeFile(t, filepath.Join(root, "secret.txt"), "secret")

	for _, p := range []string{
		"a.zip/../secret.txt",
		"a.zip/data.txt/../../secret.txt",
	} {
		rr := doGet(t, h, "/api/file?path="+p)
		assertStatus(t, rr, http.StatusBadRequest)
	}
}

func TestCacheFileFor(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("no HOME")
	}
	// key 经 SHA-256 映射为固定长度文件名:嵌套归档的全路径(宿主路径 +
	// 成员路径)可能超过 255 字节单组件限制,不能直接用作文件名。
	got := cacheFileFor("/data/a.zip")
	if filepath.Dir(got) != filepath.Join(home, ".viewit") || filepath.Ext(got) != ".txt" {
		t.Fatalf("cacheFileFor = %q, want a hashed name under ~/.viewit", got)
	}
	if strings.Contains(got, "a.zip") {
		t.Fatalf("cacheFileFor must not embed the key path: %q", got)
	}
	if cacheFileFor("/data/a.zip") != got {
		t.Fatal("cacheFileFor must be deterministic for the same key")
	}
	if cacheFileFor("/data/b.zip") == got {
		t.Fatal("cacheFileFor must differ for different keys")
	}
}

func TestScanTarOffsets(t *testing.T) {
	var buf strings.Builder
	tw := tar.NewWriter(&buf)
	for name, content := range map[string]string{"a.txt": "aaaa", "sub/b.txt": "bbbbbb"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	idx, err := scanTar(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 {
		t.Fatalf("scanTar entries = %d, want 2", len(idx))
	}
	// 用记录的 offset+size 直接随机读取,验证 seek 正确性
	byName := map[string]tarEntry{}
	for _, e := range idx {
		byName[e.Name] = e
	}
	data := buf.String()
	for name, want := range map[string]string{"a.txt": "aaaa", "sub/b.txt": "bbbbbb"} {
		e := byName[name]
		if int64(len(data)) < e.Offset+e.Size {
			t.Fatalf("entry %q offset/size out of range: %+v", name, e)
		}
		if got := data[e.Offset : e.Offset+e.Size]; got != want {
			t.Fatalf("entry %q content = %q, want %q", name, got, want)
		}
	}
}

func TestTarIndexCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "idx.txt")
	modTime := time.Now().Truncate(time.Second)
	ix := newTarIndex([]tarEntry{
		{Name: "sub/b.txt", Offset: 512, Size: 4, ModTime: modTime, IsDir: false},
		{Name: "a.txt", Offset: 0, Size: 3, ModTime: modTime, IsDir: false},
	})
	if err := writeTarIndexCache(cache, 1024, modTime, ix); err != nil {
		t.Fatal(err)
	}
	got, ok := loadTarIndexCache(cache, 1024, modTime)
	if !ok {
		t.Fatal("loadTarIndexCache rejected a valid cache")
	}
	// 排序应保持(name 升序)
	if len(got.entries) != 2 || got.entries[0].Name != "a.txt" || got.entries[1].Name != "sub/b.txt" {
		t.Fatalf("round-trip mismatch: %+v", got.entries)
	}
	// size 不符 → 失效
	if _, ok := loadTarIndexCache(cache, 2048, modTime); ok {
		t.Fatal("stale cache (wrong size) should be rejected")
	}
	// modTime 不符 → 失效
	if _, ok := loadTarIndexCache(cache, 1024, modTime.Add(time.Second)); ok {
		t.Fatal("stale cache (wrong modTime) should be rejected")
	}
}

// 二分 list 对深层目录只触及子树条目,且隐式目录/直接子项判断正确。
func TestTarIndexListBinarySearch(t *testing.T) {
	ix := newTarIndex([]tarEntry{
		{Name: "url/BR/img/50000050150/a.jpg", Offset: 0, Size: 1, IsDir: false},
		{Name: "url/BR/img/50000050150/b.jpg", Offset: 1, Size: 1, IsDir: false},
		{Name: "url/BR/img/other/c.jpg", Offset: 2, Size: 1, IsDir: false},
		{Name: "top.txt", Offset: 3, Size: 1, IsDir: false},
	})

	root := ix.list("")
	if len(root) != 2 {
		t.Fatalf("root entries = %d, want 2 (url dir + top.txt): %+v", len(root), root)
	}
	if root[0].Name != "url" || !root[0].IsDir {
		t.Fatalf("root[0] = %+v, want dir 'url'", root[0])
	}

	sub := ix.list("url/BR/img/50000050150")
	if len(sub) != 2 || sub[0].Name != "a.jpg" || sub[1].Name != "b.jpg" {
		t.Fatalf("deep dir entries = %+v, want a.jpg + b.jpg", sub)
	}
	if e, ok := ix.stat("url/BR/img/50000050150"); !ok || !e.IsDir {
		t.Fatalf("stat implied dir = %+v ok=%v, want dir", e, ok)
	}
	if _, ok := ix.stat("url/BR/img/50000050150/a.jpg"); !ok {
		t.Fatal("stat file a.jpg should be found")
	}
}

// ---------------------------------------------------------------------------
// 嵌套归档:zip/tar 内的 zip/tar 成员可继续递归浏览(任意深度)

// zipBytes 把条目打包成内存中的 zip 字节(测试辅助)。
func zipBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tarBytes 把条目打包成内存中的 tar 字节(测试辅助)。
func tarBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 外层 zip 列表:zip/tar 成员是虚拟目录(isDir+isArchive,目录序在前)。
func TestNestedZipListMarksArchiveMember(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "outer.zip"), map[string]string{
		"inner.zip": string(zipBytes(t, map[string]string{"n.txt": "n"})),
		"a.tar":     string(tarBytes(t, map[string]string{"t.txt": "t"})),
		"top.txt":   "top",
	})

	rr := doGet(t, h, "/api/list?path=outer.zip")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if !resp.IsDir {
		t.Fatalf("outer.zip should be a dir")
	}
	byName := map[string]fileEntry{}
	for _, e := range resp.Entries {
		byName[e.Name] = e
	}
	for _, name := range []string{"inner.zip", "a.tar"} {
		e := byName[name]
		if !e.IsDir || !e.IsArchive || e.Size <= 0 {
			t.Fatalf("%s = %+v, want virtual dir with isArchive and real size", name, e)
		}
	}
	if e := byName["top.txt"]; e.IsDir || e.IsArchive {
		t.Fatalf("top.txt = %+v, want plain file", e)
	}
}

// zip 内嵌 zip:逐层进入,内层文件可预览(含 Range)。
func TestNestedZipRecursion(t *testing.T) {
	h, root := newTestHandler(t, false)
	inner := zipBytes(t, map[string]string{"nested.txt": "nested content", "sub/deep.txt": "deep"})
	writeZipFile(t, filepath.Join(root, "outer.zip"), map[string]string{
		"inner.zip": string(inner),
		"top.txt":   "top",
	})

	// 进入内层 zip 根
	rr := doGet(t, h, "/api/list?path=outer.zip/inner.zip")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if !resp.IsDir {
		t.Fatalf("inner.zip should be a dir")
	}
	if len(resp.Entries) != 2 || resp.Entries[0].Name != "sub" || !resp.Entries[0].IsDir {
		t.Fatalf("inner entries = %+v, want dir 'sub' first", resp.Entries)
	}
	if resp.Entries[1].Name != "nested.txt" || resp.Entries[1].IsDir {
		t.Fatalf("inner entries[1] = %+v, want file nested.txt", resp.Entries[1])
	}

	// 内层子目录
	rr = doGet(t, h, "/api/list?path=outer.zip/inner.zip/sub")
	assertStatus(t, rr, http.StatusOK)
	resp = decodeList(t, rr)
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "deep.txt" {
		t.Fatalf("inner sub entries = %+v, want deep.txt", resp.Entries)
	}

	// 内层文件预览
	rr = doGet(t, h, "/api/file?path=outer.zip/inner.zip/nested.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "nested content" {
		t.Fatalf("body = %q, want nested content", rr.Body.String())
	}

	// Range/206:嵌套成员可 seek
	rr = doGetRange(t, h, "/api/file?path=outer.zip/inner.zip/nested.txt", "bytes=0-5")
	assertStatus(t, rr, http.StatusPartialContent)
	if rr.Body.String() != "nested" {
		t.Fatalf("range body = %q, want %q", rr.Body.String(), "nested")
	}
}

// tar 内嵌 tar;zip 内嵌 tar;tar 内嵌 zip:交叉嵌套都递归可进。
func TestCrossNestedArchives(t *testing.T) {
	h, root := newTestHandler(t, false)
	innerTar := tarBytes(t, map[string]string{"n.txt": "n", "dir/p.txt": "p"})
	writeTarFile(t, filepath.Join(root, "outer.tar"), map[string]string{
		"inner.tar": string(innerTar),
		"t.txt":     "t",
	})
	writeZipFile(t, filepath.Join(root, "zipintar.zip"), map[string]string{
		"inner.tar": string(innerTar),
	})
	writeTarFile(t, filepath.Join(root, "tarinzip.tar"), map[string]string{
		"inner.zip": string(zipBytes(t, map[string]string{"z.txt": "z"})),
	})

	// tar 内嵌 tar
	rr := doGet(t, h, "/api/list?path=outer.tar")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if len(resp.Entries) != 2 || resp.Entries[0].Name != "inner.tar" || !resp.Entries[0].IsDir || !resp.Entries[0].IsArchive {
		t.Fatalf("outer.tar entries = %+v, want virtual dir inner.tar", resp.Entries)
	}
	rr = doGet(t, h, "/api/list?path=outer.tar/inner.tar")
	assertStatus(t, rr, http.StatusOK)
	resp = decodeList(t, rr)
	if len(resp.Entries) != 2 || resp.Entries[0].Name != "dir" || resp.Entries[1].Name != "n.txt" {
		t.Fatalf("inner.tar entries = %+v, want dir + n.txt", resp.Entries)
	}
	rr = doGet(t, h, "/api/file?path=outer.tar/inner.tar/dir/p.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "p" {
		t.Fatalf("nested tar member body = %q, want p", rr.Body.String())
	}

	// zip 内嵌 tar
	rr = doGet(t, h, "/api/file?path=zipintar.zip/inner.tar/dir/p.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "p" {
		t.Fatalf("zip->tar member body = %q, want p", rr.Body.String())
	}

	// tar 内嵌 zip
	rr = doGet(t, h, "/api/file?path=tarinzip.tar/inner.zip/z.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "z" {
		t.Fatalf("tar->zip member body = %q, want z", rr.Body.String())
	}
}

// 三层嵌套:zip -> zip -> zip。
func TestTripleNestedZip(t *testing.T) {
	h, root := newTestHandler(t, false)
	innermost := zipBytes(t, map[string]string{"leaf.txt": "leaf"})
	middle := zipBytes(t, map[string]string{"deep.zip": string(innermost)})
	writeZipFile(t, filepath.Join(root, "top.zip"), map[string]string{"mid.zip": string(middle)})

	rr := doGet(t, h, "/api/list?path=top.zip/mid.zip/deep.zip")
	assertStatus(t, rr, http.StatusOK)
	resp := decodeList(t, rr)
	if !resp.IsDir || len(resp.Entries) != 1 || resp.Entries[0].Name != "leaf.txt" {
		t.Fatalf("triple nested entries = %+v, want leaf.txt", resp.Entries)
	}
	rr = doGet(t, h, "/api/file?path=top.zip/mid.zip/deep.zip/leaf.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "leaf" {
		t.Fatalf("triple nested body = %q, want leaf", rr.Body.String())
	}
}

// 嵌套 zip 下载:内层 zip 成员解压后字节 = 完整 zip 文件;最外层仍是原文件。
func TestNestedZipDownload(t *testing.T) {
	h, root := newTestHandler(t, false)
	inner := zipBytes(t, map[string]string{"n.txt": "n"})
	writeZipFile(t, filepath.Join(root, "outer.zip"), map[string]string{
		"inner.zip": string(inner),
		"top.txt":   "top",
	})

	// 下载嵌套 zip 成员:字节必须等于内层 zip 的完整字节(可独立解压)
	rr := doGet(t, h, "/api/download?path=outer.zip/inner.zip")
	assertStatus(t, rr, http.StatusOK)
	if !bytes.Equal(rr.Body.Bytes(), inner) {
		t.Fatalf("nested zip download body != inner zip bytes (len %d vs %d)", rr.Body.Len(), len(inner))
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "inner.zip") {
		t.Fatalf("Content-Disposition = %q, want inner.zip", cd)
	}

	// 下载最外层归档:仍是原文件字节(链长 1 分支)
	raw, err := os.ReadFile(filepath.Join(root, "outer.zip"))
	if err != nil {
		t.Fatal(err)
	}
	rr = doGet(t, h, "/api/download?path=outer.zip")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.Len() != len(raw) {
		t.Fatalf("outer download len = %d, want %d", rr.Body.Len(), len(raw))
	}

	// 下载内层普通文件:与单层行为一致
	rr = doGet(t, h, "/api/download?path=outer.zip/inner.zip/n.txt")
	assertStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "n" {
		t.Fatalf("nested member download body = %q, want n", rr.Body.String())
	}
}

// 嵌套段不是有效归档:404 "not a readable archive",且不泄漏为文件预览。
func TestNestedBadArchive(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeZipFile(t, filepath.Join(root, "outer.zip"), map[string]string{
		"bad.zip": "this is not a zip",
	})

	rr := doGet(t, h, "/api/list?path=outer.zip/bad.zip")
	assertStatus(t, rr, http.StatusNotFound)
	if !strings.Contains(rr.Body.String(), "not a readable archive") {
		t.Fatalf("body = %s, want 'not a readable archive'", rr.Body.String())
	}
	// 下载同样 404(该位置无法作为归档浏览)
	rr = doGet(t, h, "/api/download?path=outer.zip/bad.zip")
	assertStatus(t, rr, http.StatusNotFound)
}

// 嵌套路径的 ".." 在字符串层被拒绝,与单层一致。
func TestNestedTraversalRejected(t *testing.T) {
	h, root := newTestHandler(t, false)
	inner := zipBytes(t, map[string]string{"secret.txt": "s"})
	writeZipFile(t, filepath.Join(root, "outer.zip"), map[string]string{"inner.zip": string(inner)})

	for _, p := range []string{
		"outer.zip/inner.zip/../secret.txt",
		"outer.zip/inner.zip/../../etc/passwd",
		"outer.zip/../outer.zip",
	} {
		rr := doGet(t, h, "/api/file?path="+p)
		assertStatus(t, rr, http.StatusBadRequest)
	}
}
