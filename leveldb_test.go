package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
)

// writeLevelDB 在 dir 创建 leveldb 库并写入 kvs,然后关闭。
func writeLevelDB(t *testing.T, dir string, kvs [][2]string) {
	t.Helper()
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatalf("open leveldb %s: %v", dir, err)
	}
	for _, kv := range kvs {
		if err := db.Put([]byte(kv[0]), []byte(kv[1]), nil); err != nil {
			db.Close()
			t.Fatalf("put %q: %v", kv[0], err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close leveldb %s: %v", dir, err)
	}
}

// ldbFixture 建 root/db 库,写 a1,a2,b1,b2。
func ldbFixture(t *testing.T, root string) {
	t.Helper()
	writeLevelDB(t, filepath.Join(root, "db"), [][2]string{
		{"a1", "va1"}, {"a2", "va2"}, {"b1", "vb1"}, {"b2", "vb2"},
	})
}

type keysResp struct {
	Keys    []string `json:"keys"`
	HasMore bool     `json:"hasMore"`
}

func decodeKeys(t *testing.T, rr *httptest.ResponseRecorder) keysResp {
	t.Helper()
	var resp keysResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v; body=%s", err, rr.Body.String())
	}
	return resp
}

func TestLevelDBDetect(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeLevelDB(t, filepath.Join(root, "db"), [][2]string{{"k", "v"}})
	if err := os.Mkdir(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "plain", "x.txt"), "x")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := doGet(t, h, "/api/list?path=db")
	assertStatus(t, rr, http.StatusOK)
	if !decodeList(t, rr).LevelDB {
		t.Fatalf("db: leveldb = false, want true")
	}
	rr = doGet(t, h, "/api/list?path=plain")
	assertStatus(t, rr, http.StatusOK)
	if decodeList(t, rr).LevelDB {
		t.Fatalf("plain: leveldb = true, want false")
	}
	rr = doGet(t, h, "/api/list?path=empty")
	assertStatus(t, rr, http.StatusOK)
	if decodeList(t, rr).LevelDB {
		t.Fatalf("empty: leveldb = true, want false")
	}
}

func TestLevelDBKeysPaging(t *testing.T) {
	h, root := newTestHandler(t, false)
	ldbFixture(t, root)
	base := "/api/leveldb/keys?path=db"

	cases := []struct {
		name    string
		q       string
		want    []string
		hasMore bool
	}{
		{"prefix-a", "&prefix=a", []string{"a1", "a2"}, false},
		{"prefix-b-after-limit", "&prefix=b&after=b1&limit=1", []string{"b2"}, false},
		{"all-limit-1", "&limit=1", []string{"a1"}, true},
		{"after-a2", "&after=a2&limit=10", []string{"b1", "b2"}, false},
		{"no-match", "&prefix=x", []string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doGet(t, h, base+c.q)
			assertStatus(t, rr, http.StatusOK)
			got := decodeKeys(t, rr)
			if len(got.Keys) != len(c.want) {
				t.Fatalf("keys = %v, want %v", got.Keys, c.want)
			}
			for i := range got.Keys {
				if got.Keys[i] != c.want[i] {
					t.Fatalf("keys = %v, want %v", got.Keys, c.want)
				}
			}
			if got.HasMore != c.hasMore {
				t.Fatalf("hasMore = %v, want %v", got.HasMore, c.hasMore)
			}
		})
	}
}

func TestLevelDBGet(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeLevelDB(t, filepath.Join(root, "db"), [][2]string{
		{"text", "hello 世界"},
		{"bin", "\xff\x00\x01"},
		{"big", strings.Repeat("x", 6*1024*1024)},
	})

	// 文本值:text 正确、无 base64。
	rr := doGet(t, h, "/api/leveldb/get?path=db&key=text")
	assertStatus(t, rr, http.StatusOK)
	var v ldbValue
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if v.Key != "text" || v.Text == nil || *v.Text != "hello 世界" || v.Base64 != "" {
		t.Fatalf("text value = %+v, want text=hello 世界", v)
	}

	// 二进制值:base64 正确、text 缺省。
	rr = doGet(t, h, "/api/leveldb/get?path=db&key=bin")
	assertStatus(t, rr, http.StatusOK)
	v = ldbValue{}
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if v.Base64 != "/wAB" || v.Text != nil || v.Size != 3 {
		t.Fatalf("bin value = %+v, want base64=/wAB size=3", v)
	}

	// 缺失 key → 404。
	rr = doGet(t, h, "/api/leveldb/get?path=db&key=nope")
	assertStatus(t, rr, http.StatusNotFound)

	// 6MB 值 → tooBig,无 text/base64。
	rr = doGet(t, h, "/api/leveldb/get?path=db&key=big")
	assertStatus(t, rr, http.StatusOK)
	v = ldbValue{}
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if !v.TooBig || v.Text != nil || v.Base64 != "" || v.Size != 6*1024*1024 {
		t.Fatalf("big value = %+v, want tooBig size=6MB", v)
	}
}

func TestLevelDBDump(t *testing.T) {
	h, root := newTestHandler(t, false)
	ldbFixture(t, root)

	rr := doGet(t, h, "/api/leveldb/dump?path=db&prefix=b")
	assertStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); ct != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="dump-b.jsonl"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	lines := bytes.Split(bytes.TrimSpace(rr.Body.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %s", len(lines), rr.Body.String())
	}
	for i, want := range []struct {
		key  string
		text string
		size int
	}{{"b1", "vb1", 3}, {"b2", "vb2", 3}} {
		var v ldbValue
		if err := json.Unmarshal(lines[i], &v); err != nil {
			t.Fatalf("line %d bad json: %v", i, err)
		}
		if v.Key != want.key || v.Text == nil || *v.Text != want.text || v.Size != int64(want.size) {
			t.Fatalf("line %d = %+v, want key=%s text=%s size=%d", i, v, want.key, want.text, want.size)
		}
	}
}

func TestLevelDBRejects(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeLevelDB(t, filepath.Join(root, "db"), [][2]string{{"k", "v"}})
	if err := os.Mkdir(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "plain", "x.txt"), "x")
	writeZipFile(t, filepath.Join(root, "a.zip"), map[string]string{"x.txt": "x"})

	// 普通目录 → 400
	rr := doGet(t, h, "/api/leveldb/keys?path=plain")
	assertStatus(t, rr, http.StatusBadRequest)
	// 路径穿越 → 非 200
	rr = doGet(t, h, "/api/leveldb/keys?path=../../etc")
	if rr.Code == http.StatusOK {
		t.Fatalf("traversal status = %d, want non-200", rr.Code)
	}
	// 归档内部 → 400(非 leveldb 目录)
	rr = doGet(t, h, "/api/leveldb/keys?path=a.zip")
	assertStatus(t, rr, http.StatusBadRequest)
	// 不存在的目录 → 404
	rr = doGet(t, h, "/api/leveldb/keys?path=nope")
	assertStatus(t, rr, http.StatusNotFound)
}

func TestLevelDBLocked(t *testing.T) {
	h, root := newTestHandler(t, false)
	dir := filepath.Join(root, "db")
	writeLevelDB(t, dir, [][2]string{{"k", "v"}})

	// 独占打开(持写锁)不关闭 → 只读打开应失败并映射 409。
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatalf("exclusive open: %v", err)
	}
	defer db.Close()

	rr := doGet(t, h, "/api/leveldb/keys?path=db")
	assertStatus(t, rr, http.StatusConflict)
	if !strings.Contains(rr.Body.String(), "无法只读打开") {
		t.Fatalf("body = %s, want 无法只读打开 message", rr.Body.String())
	}

	// 释放后正常。
	db.Close()
	rr = doGet(t, h, "/api/leveldb/keys?path=db")
	assertStatus(t, rr, http.StatusOK)
	got := decodeKeys(t, rr)
	if len(got.Keys) != 1 || got.Keys[0] != "k" {
		t.Fatalf("keys = %v, want [k]", got.Keys)
	}
}
