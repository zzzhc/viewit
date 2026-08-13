package main

import (
	"archive/zip"
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// captureLog 把包级 logger 输出重定向到内存 buffer,测试结束恢复。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// TestAccessLog 验证每个请求记一行 [access] 日志:方法、URI、状态码、真实
// 响应字节数与耗时;字节数统计的是写入响应体的字节(含压缩后)。
func TestAccessLog(t *testing.T) {
	h, _ := newTestHandler(t, false)
	buf := captureLog(t)

	rr := doGet(t, h, "/api/list?path=/")
	assertStatus(t, rr, http.StatusOK)

	re := regexp.MustCompile(`\[access\] \S+ GET /api/list\?path=/ 200 (\d+) \S+`)
	m := re.FindStringSubmatch(buf.String())
	if m == nil {
		t.Fatalf("access log line missing or malformed: %q", buf.String())
	}
	if n, err := strconv.Atoi(m[1]); err != nil || n == 0 {
		t.Fatalf("bytes field = %q, want > 0", m[1])
	}
}

// TestAccessLogErrorStatus 验证错误状态码也记日志(定位 404/400 的请求)。
func TestAccessLogErrorStatus(t *testing.T) {
	h, _ := newTestHandler(t, false)
	buf := captureLog(t)

	rr := doGet(t, h, "/api/file?path=missing")
	assertStatus(t, rr, http.StatusNotFound)

	if !strings.Contains(buf.String(), "GET /api/file?path=missing 404 ") {
		t.Fatalf("access log missing 404 entry: %q", buf.String())
	}
}

// openSourceForTest opens a host file as an archiveSource for tests.
func openSourceForTest(t *testing.T, path string) *archiveSource {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	return &archiveSource{key: path, size: st.Size(), modTime: st.ModTime(), ra: f, closer: f}
}

// TestSlowScanTarLog 验证 tar 无缓存全量扫描记 [slow] scan-tar(含条目数)。
// 扫描是浏览 tar 最重的操作,日志必须能对比两次扫描的耗时。
func TestSlowScanTarLog(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "a.tar")
	writeTarFile(t, tarPath, map[string]string{"x.txt": "x"})
	buf := captureLog(t)

	s := &server{tarStore: newTarIndexStore()}
	ta, err := s.openTar(openSourceForTest(t, tarPath))
	if err != nil {
		t.Fatal(err)
	}
	defer ta.close()

	line := buf.String()
	if !strings.Contains(line, "[slow] scan-tar") || !strings.Contains(line, "entries=1") {
		t.Fatalf("missing scan-tar log: %q", line)
	}
}

// TestSlowOpenZipLog 验证超过阈值的 zip 打开记 [slow] open-zip。
func TestSlowOpenZipLog(t *testing.T) {
	old := slowThreshold
	slowThreshold = time.Nanosecond
	t.Cleanup(func() { slowThreshold = old })

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "a.zip")
	writeZipFile(t, zipPath, map[string]string{"x.txt": "x"})
	buf := captureLog(t)

	za, err := openZip(openSourceForTest(t, zipPath))
	if err != nil {
		t.Fatal(err)
	}
	defer za.close()

	if !strings.Contains(buf.String(), "[slow] open-zip") {
		t.Fatalf("missing open-zip log: %q", buf.String())
	}
}

// TestSlowExtractZipLog 验证大成员(>16MiB)首次解压到磁盘缓存记
// [slow] extract-zip;缓存目录用临时 HOME 隔离,避免污染用户 ~/.viewit。
func TestSlowExtractZipLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "big.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// Store 模式:16MiB+1 的成员不经压缩直接写入,测试开销最小。
	hw, err := zw.CreateHeader(&zip.FileHeader{Name: "big.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, archiveThreshold+1)
	if _, err := hw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	za, err := openZip(openSourceForTest(t, zipPath))
	if err != nil {
		t.Fatal(err)
	}
	defer za.close()

	buf := captureLog(t)
	rc, err := za.open("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, rc)
	rc.Close()

	if !strings.Contains(buf.String(), "[slow] extract-zip") {
		t.Fatalf("missing extract-zip log: %q", buf.String())
	}
}

// TestSlowFindWalkLog 验证索引构建完成记 [slow] find-walk。
func TestSlowFindWalkLog(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	buf := captureLog(t)

	ix := &findIndex{}
	ix.walk(root)

	if !strings.Contains(buf.String(), "[slow] find-walk") {
		t.Fatalf("missing find-walk log: %q", buf.String())
	}
}

// TestWSFindLog 验证每条 WS 查询记 [ws] find 日志(含 q/scope/命中数/耗时),
// 超阈值时再补一条 [slow] find-query 行。
func TestWSFindLog(t *testing.T) {
	old := slowThreshold
	slowThreshold = time.Nanosecond
	t.Cleanup(func() { slowThreshold = old })

	srv, root := newTestServer(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	buf := captureLog(t)

	conn := dialFinder(t, srv)
	waitFinderReady(t, conn)
	findResultPaths(t, conn, "a")

	out := buf.String()
	if !strings.Contains(out, "[ws] find q=\"a\"") {
		t.Fatalf("missing [ws] find log: %q", out)
	}
	if !strings.Contains(out, "[slow] find-query") {
		t.Fatalf("missing [slow] find-query log: %q", out)
	}
}

// TestWSStreamLog 验证 stream 的 open 与 end 都记 [ws] 日志:open 含路径与
// 内容类型,end 含累计字节数与总耗时。
func TestWSStreamLog(t *testing.T) {
	srv, root := newTestServer(t)
	writeFile(t, filepath.Join(root, "a.txt"), "hello stream")
	buf := captureLog(t)

	conn := dialStream(t, srv)
	streamAll(t, conn, "a.txt")

	out := buf.String()
	if !strings.Contains(out, `[ws] stream-open path="a.txt"`) {
		t.Fatalf("missing stream-open log: %q", out)
	}
	if !strings.Contains(out, "[ws] stream-end") || !strings.Contains(out, "size=12") {
		t.Fatalf("missing stream-end log: %q", out)
	}
}

// TestSlowZipDownloadLog 验证目录打包下载超阈值记 [slow] zip-download。
func TestSlowZipDownloadLog(t *testing.T) {
	old := slowThreshold
	slowThreshold = time.Nanosecond
	t.Cleanup(func() { slowThreshold = old })

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "hello")
	buf := captureLog(t)

	s := &server{root: root}
	rec := httptest.NewRecorder()
	s.streamZip(rec, root, "d")

	if !strings.Contains(buf.String(), "[slow] zip-download") {
		t.Fatalf("missing zip-download log: %q", buf.String())
	}
}
