package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func gzipString(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func dialStream(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/stream"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

type streamResp struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Offset int64  `json:"offset"`
	B64    string `json:"b64"`
	Size   int64  `json:"size"`
	Error  string `json:"error"`
}

func sendStream(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readStream(t *testing.T, conn *websocket.Conn) streamResp {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp streamResp
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp
}

// streamAll open 后拉到 "end",返回 meta 与重构出的完整文本。
func streamAll(t *testing.T, conn *websocket.Conn, path string) (streamResp, string) {
	t.Helper()
	sendStream(t, conn, map[string]any{"type": "open", "path": path})
	meta := readStream(t, conn)
	if meta.Type != "meta" {
		t.Fatalf("expected meta after open, got %+v", meta)
	}
	var buf []byte
	for {
		sendStream(t, conn, map[string]any{"type": "more", "bytes": 4096})
		resp := readStream(t, conn)
		switch resp.Type {
		case "data":
			b, err := base64.StdEncoding.DecodeString(resp.B64)
			if err != nil {
				t.Fatalf("b64 decode: %v", err)
			}
			buf = append(buf, b...)
		case "end":
			return meta, string(buf)
		default:
			t.Fatalf("unexpected message %+v", resp)
		}
	}
}

func TestStreamPlainRoundTrip(t *testing.T) {
	srv, root := newTestServer(t)
	content := strings.Repeat("plain line 中文内容\n", 5000)
	writeFile(t, filepath.Join(root, "big.txt"), content)

	conn := dialStream(t, srv)
	meta, got := streamAll(t, conn, "big.txt")
	if meta.Name != "big.txt" {
		t.Errorf("name = %q, want big.txt", meta.Name)
	}
	if !strings.HasPrefix(meta.Mime, "text/") {
		t.Errorf("mime = %q, want text/*", meta.Mime)
	}
	if got != content {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestStreamGzRoundTrip(t *testing.T) {
	srv, root := newTestServer(t)
	content := "[" + strings.Repeat(`{"a":1,"b":"中文"},`, 300) + "]"
	writeFile(t, filepath.Join(root, "data.json.gz"), string(gzipString(t, content)))

	conn := dialStream(t, srv)
	meta, got := streamAll(t, conn, "data.json.gz")
	if meta.Name != "data.json" {
		t.Errorf("name = %q, want data.json", meta.Name)
	}
	if meta.Mime != "application/json" {
		t.Errorf("mime = %q, want application/json", meta.Mime)
	}
	if got != content {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestStreamGzShort(t *testing.T) {
	srv, root := newTestServer(t)
	content := "hello\nworld\n"
	writeFile(t, filepath.Join(root, "note.txt.gz"), string(gzipString(t, content)))

	conn := dialStream(t, srv)
	meta, got := streamAll(t, conn, "note.txt.gz")
	if meta.Name != "note.txt" {
		t.Errorf("name = %q, want note.txt", meta.Name)
	}
	if got != content {
		t.Fatalf("content mismatch: %q != %q", got, content)
	}
}

func TestStreamZipMember(t *testing.T) {
	srv, root := newTestServer(t)
	content := "[" + strings.Repeat(`{"id":1,"title":"中文标题"},`, 200) + "]"
	writeZipFile(t, filepath.Join(root, "arch.zip"), map[string]string{
		"sub/data.json": content,
	})

	conn := dialStream(t, srv)
	meta, got := streamAll(t, conn, "arch.zip/sub/data.json")
	if meta.Name != "data.json" {
		t.Errorf("name = %q, want data.json", meta.Name)
	}
	if meta.Mime != "application/json" {
		t.Errorf("mime = %q, want application/json", meta.Mime)
	}
	if got != content {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestStreamTarMember(t *testing.T) {
	srv, root := newTestServer(t)
	content := strings.Repeat("tar 文本行 中文内容\n", 500)
	writeTarFile(t, filepath.Join(root, "arch.tar"), map[string]string{
		"docs/notes.txt": content,
	})

	conn := dialStream(t, srv)
	meta, got := streamAll(t, conn, "arch.tar/docs/notes.txt")
	if meta.Name != "notes.txt" {
		t.Errorf("name = %q, want notes.txt", meta.Name)
	}
	if !strings.HasPrefix(meta.Mime, "text/") {
		t.Errorf("mime = %q, want text/*", meta.Mime)
	}
	if got != content {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestListGzTransparent(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := `{"a":1,"b":"中文"}`
	writeFile(t, filepath.Join(root, "obj.json.gz"), string(gzipString(t, content)))

	rr := doGet(t, h, "/api/list?path=obj.json.gz")
	assertStatus(t, rr, 200)
	resp := decodeList(t, rr)
	if resp.File == nil {
		t.Fatal("expected file entry")
	}
	if resp.File.Mime != "application/json" {
		t.Errorf("mime = %q, want application/json", resp.File.Mime)
	}
	if resp.File.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d (decompressed ISIZE)", resp.File.Size, len(content))
	}
}

func TestFileGzTransparent(t *testing.T) {
	h, root := newTestHandler(t, false)
	content := "# 标题\n\n正文内容\n"
	writeFile(t, filepath.Join(root, "doc.md.gz"), string(gzipString(t, content)))

	rr := doGet(t, h, "/api/file?path=doc.md.gz")
	assertStatus(t, rr, 200)
	if got := rr.Body.String(); got != content {
		t.Fatalf("decompressed body = %q, want %q", got, content)
	}
}

func TestStreamErrors(t *testing.T) {
	srv, root := newTestServer(t)
	writeFile(t, filepath.Join(root, "bad.gz"), "not really gzip")

	conn := dialStream(t, srv)

	// 损坏 gz → error。
	sendStream(t, conn, map[string]any{"type": "open", "path": "bad.gz"})
	if resp := readStream(t, conn); resp.Type != "error" {
		t.Errorf("bad.gz: expected error, got %+v", resp)
	}

	// 未 open 先 more → error。
	conn2 := dialStream(t, srv)
	sendStream(t, conn2, map[string]any{"type": "more", "bytes": 1024})
	if resp := readStream(t, conn2); resp.Type != "error" {
		t.Errorf("more before open: expected error, got %+v", resp)
	}

	// 越界路径 → error。
	sendStream(t, conn, map[string]any{"type": "open", "path": "../etc/passwd"})
	if resp := readStream(t, conn); resp.Type != "error" {
		t.Errorf("outside root: expected error, got %+v", resp)
	}
}
