package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openSQLite 用写模式打开/创建库(测试夹具专用,生产端只读)。
func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open sqlite %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// writeSQLite 在 path 创建 sqlite 库并执行 schema 与多组 insert。
func writeSQLite(t *testing.T, path string, schema string, rows ...[]any) {
	t.Helper()
	db := openSQLite(t, path)
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("exec schema %s: %v", path, err)
	}
	for i, r := range rows {
		q := "INSERT INTO t VALUES (" + strings.Repeat("?,", len(r)-1) + "?)"
		if _, err := db.Exec(q, r...); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
}

// sqliteFixture 建 root/data.db,含 t 表(文本/数字/BLOB)与隐藏表
// (sqlite_ 前缀,应被 tables 接口过滤)。
func sqliteFixture(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "data.db")
	// AUTOINCREMENT 让 SQLite 自建 sqlite_sequence 内部表,验证 tables 接口过滤。
	writeSQLite(t, p,
		"CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, score REAL, data BLOB)",
		[]any{1, "你好", 3.5, []byte{0x00, 0xff, 0x10}},
		[]any{2, "world", 2.0, nil},
		[]any{3, "x", 1.5, []byte("text blob")},
	)
	db := openSQLite(t, p)
	if _, err := db.Exec("CREATE VIEW v1 AS SELECT id, name FROM t"); err != nil {
		t.Fatalf("create view: %v", err)
	}
	return "data.db"
}

type tablesResp struct {
	Tables []sqliteTable `json:"tables"`
}

func decodeTables(t *testing.T, rr *httptest.ResponseRecorder) tablesResp {
	t.Helper()
	var resp tablesResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v; body=%s", err, rr.Body.String())
	}
	return resp
}

type rowsResp struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	HasMore bool     `json:"hasMore"`
	Total   *int64   `json:"total"`
}

func decodeRows(t *testing.T, rr *httptest.ResponseRecorder) rowsResp {
	t.Helper()
	var resp rowsResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v; body=%s", err, rr.Body.String())
	}
	return resp
}

func TestSQLiteSniff(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.db")
	writeSQLite(t, p, "CREATE TABLE t (v INTEGER)")
	if got := sniffMime(p); got != "application/vnd.sqlite3" {
		t.Fatalf("sniffMime(sqlite) = %q, want application/vnd.sqlite3", got)
	}
	txt := filepath.Join(dir, "t.txt")
	writeFile(t, txt, "hello")
	if got := sniffMime(txt); got == "application/vnd.sqlite3" {
		t.Fatalf("sniffMime(txt) = sqlite, want not")
	}
}

func TestSQLiteTables(t *testing.T) {
	h, root := newTestHandler(t, false)
	name := sqliteFixture(t, root)
	rr := doGet(t, h, "/api/sqlite/tables?path="+name)
	assertStatus(t, rr, http.StatusOK)
	tr := decodeTables(t, rr)
	if len(tr.Tables) != 2 { // t + v1;sqlite_hidden 被过滤
		t.Fatalf("tables = %d, want 2 (got %v)", len(tr.Tables), tr.Tables)
	}
	var tbl, view sqliteTable
	for _, tb := range tr.Tables {
		switch tb.Name {
		case "t":
			tbl = tb
		case "v1":
			view = tb
		}
	}
	if tbl.Type != "table" || tbl.Rows != 3 {
		t.Fatalf("t = %+v, want type=table rows=3", tbl)
	}
	if !strings.Contains(tbl.SQL, "CREATE TABLE t") {
		t.Fatalf("t.sql missing CREATE TABLE: %q", tbl.SQL)
	}
	if view.Type != "view" || view.Rows != 3 { // 视图 COUNT 跟随基表
		t.Fatalf("v1 = %+v, want type=view rows=3", view)
	}
}

func TestSQLiteRowsPaging(t *testing.T) {
	h, root := newTestHandler(t, false)
	sqliteFixture(t, root)
	base := "/api/sqlite/rows?path=data.db&table=t"

	// 首屏:limit=2 → 2 行 + hasMore;total 有值
	rr := doGet(t, h, base+"&offset=0&limit=2")
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 2 || !rp.HasMore {
		t.Fatalf("page1 rows=%d hasMore=%v, want 2/true", len(rp.Rows), rp.HasMore)
	}
	if rp.Total == nil || *rp.Total != 3 {
		t.Fatalf("page1 total = %v, want 3", rp.Total)
	}
	if len(rp.Columns) != 4 {
		t.Fatalf("columns = %v, want 4", rp.Columns)
	}
	// BLOB 单元格:小 BLOB 带 base64,无 big
	blob, ok := rp.Rows[0][3].(map[string]any)
	if !ok || blob["b"] == "" || blob["n"] != float64(3) {
		t.Fatalf("row0.data = %#v, want {b,n} blob", rp.Rows[0][3])
	}
	if rp.Rows[0][1] != "你好" {
		t.Fatalf("row0.name = %v, want 你好", rp.Rows[0][1])
	}

	// 第二页:offset=2 → 剩 1 行,hasMore=false;total 不再下发(深分页免 COUNT)
	rr = doGet(t, h, base+"&offset=2&limit=2")
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.HasMore {
		t.Fatalf("page2 rows=%d hasMore=%v, want 1/false", len(rp.Rows), rp.HasMore)
	}
	if rp.Total != nil {
		t.Fatalf("page2 total = %v, want nil", rp.Total)
	}
	if _, ok := rp.Rows[0][3].(map[string]any); !ok {
		t.Fatalf("row2.data = %#v, want blob {b,n}", rp.Rows[0][3])
	}
}

func TestSQLiteBigBlobAndInt64(t *testing.T) {
	h, root := newTestHandler(t, false)
	p := filepath.Join(root, "big.db")
	db := openSQLite(t, p)
	if _, err := db.Exec("CREATE TABLE t (v INTEGER, b BLOB)"); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", maxSQLiteBlobJSON+1)
	huge := int64(9007199254740992) // 2^53,超过 JS 安全整数
	if _, err := db.Exec("INSERT INTO t VALUES (?, ?)", huge, []byte(big)); err != nil {
		t.Fatal(err)
	}
	rr := doGet(t, h, "/api/sqlite/rows?path=big.db&table=t")
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	// 大整数转字符串,不丢精度
	if v, ok := rp.Rows[0][0].(string); !ok || v != "9007199254740992" {
		t.Fatalf("huge int = %#v, want string 9007199254740992", rp.Rows[0][0])
	}
	// 大 BLOB 只给占位
	blob, ok := rp.Rows[0][1].(map[string]any)
	if !ok || blob["big"] != true || blob["b"] != "" {
		t.Fatalf("big blob = %#v, want {b:\"\",big:true}", rp.Rows[0][1])
	}
}

func TestSQLiteQuery(t *testing.T) {
	h, root := newTestHandler(t, false)
	sqliteFixture(t, root)
	base := "/api/sqlite/query?path=data.db"

	// 普通 SELECT
	rr := doGet(t, h, base+"&sql="+url.QueryEscape("SELECT name FROM t WHERE score > 2 ORDER BY name"))
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.Rows[0][0] != "你好" {
		t.Fatalf("query rows = %#v, want [[你好]]", rp.Rows)
	}
	if rp.Columns[0] != "name" {
		t.Fatalf("columns = %v, want [name]", rp.Columns)
	}

	// 截断:limit=1 且结果 3 行 → truncated=true,rows=1
	rr = doGet(t, h, base+"&sql="+url.QueryEscape("SELECT id FROM t")+"&limit=1")
	assertStatus(t, rr, http.StatusOK)
	var qr struct {
		Rows      [][]any `json:"rows"`
		Truncated bool    `json:"truncated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &qr); err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || !qr.Truncated {
		t.Fatalf("truncated query = %d rows, truncated=%v, want 1/true", len(qr.Rows), qr.Truncated)
	}

	// 写语句被 query_only 拒绝 → 400
	rr = doGet(t, h, base+"&sql="+url.QueryEscape("INSERT INTO t VALUES (9, 'x', 0, NULL)"))
	assertStatus(t, rr, http.StatusBadRequest)
	if !strings.Contains(rr.Body.String(), "readonly") {
		t.Fatalf("write query error = %s, want readonly hint", rr.Body.String())
	}

	// 语法错误 → 400
	rr = doGet(t, h, base+"&sql="+url.QueryEscape("SELEC bogus"))
	assertStatus(t, rr, http.StatusBadRequest)
}

func TestSQLiteNotFile(t *testing.T) {
	h, root := newTestHandler(t, false)
	// 普通文本文件
	writeFile(t, filepath.Join(root, "x.txt"), "not a db")
	rr := doGet(t, h, "/api/sqlite/tables?path=x.txt")
	assertStatus(t, rr, http.StatusBadRequest)
	// 目录
	os.Mkdir(filepath.Join(root, "dir"), 0o755)
	rr = doGet(t, h, "/api/sqlite/tables?path=dir")
	assertStatus(t, rr, http.StatusBadRequest)
	// 不存在的表
	sqliteFixture(t, root)
	rr = doGet(t, h, "/api/sqlite/rows?path=data.db&table=nope")
	assertStatus(t, rr, http.StatusNotFound)
}

func TestSQLiteExportCSV(t *testing.T) {
	h, root := newTestHandler(t, false)
	sqliteFixture(t, root)
	rr := doGet(t, h, "/api/sqlite/export?path=data.db&table=t&format=csv")
	assertStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content-type = %s, want text/csv", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "t.csv") {
		t.Fatalf("content-disposition = %s, want t.csv", cd)
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 4 { // 表头 + 3 行
		t.Fatalf("csv lines = %d, want 4:\n%s", len(lines), rr.Body.String())
	}
	if !strings.HasPrefix(lines[0], "id,name,score,data") {
		t.Fatalf("csv header = %q", lines[0])
	}
	// 第 1 行:BLOB 单元格应 base64;NULL 应空串
	if !strings.Contains(lines[1], "AP8Q") {
		t.Fatalf("csv row1 blob not base64: %q", lines[1])
	}
	nullRow := strings.Split(lines[2], ",") // 第 2 条数据:data=NULL
	if nullRow[3] != "" {
		t.Fatalf("csv NULL cell = %q, want empty", nullRow[3])
	}
	// 视图同样可导出
	rr = doGet(t, h, "/api/sqlite/export?path=data.db&table=v1&format=csv")
	assertStatus(t, rr, http.StatusOK)
	if !strings.HasPrefix(rr.Body.String(), "id,name") {
		t.Fatalf("view csv header = %q", rr.Body.String())
	}
}

func TestSQLiteExportJSONL(t *testing.T) {
	h, root := newTestHandler(t, false)
	sqliteFixture(t, root)
	rr := doGet(t, h, "/api/sqlite/export?path=data.db&table=t&format=jsonl")
	assertStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Header().Get("Content-Type"), "ndjson") {
		t.Fatalf("content-type = %s, want ndjson", rr.Header().Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl lines = %d, want 3:\n%s", len(lines), rr.Body.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("bad jsonl: %v", err)
	}
	blob, ok := rec["data"].(map[string]any)
	if !ok || blob["$blob"] == "" {
		t.Fatalf("jsonl blob = %#v, want {$blob: base64}", rec["data"])
	}
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["data"] != nil {
		t.Fatalf("jsonl NULL = %#v, want nil", rec["data"])
	}
}

func TestSQLiteExportQuery(t *testing.T) {
	h, root := newTestHandler(t, false)
	sqliteFixture(t, root)
	// sql 模式:导出完整查询结果(不受 query 接口截断限制)
	rr := doGet(t, h, "/api/sqlite/export?path=data.db&format=csv&sql="+url.QueryEscape("SELECT name FROM t ORDER BY id"))
	assertStatus(t, rr, http.StatusOK)
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 4 || lines[0] != "name" {
		t.Fatalf("query csv = %q", rr.Body.String())
	}
	// 缺参数 → 400
	rr = doGet(t, h, "/api/sqlite/export?path=data.db&format=csv")
	assertStatus(t, rr, http.StatusBadRequest)
	// 写语句 → 500(状态已提交后记日志;先断言响应已提交为 200)
	rr = doGet(t, h, "/api/sqlite/export?path=data.db&format=csv&sql="+url.QueryEscape("DELETE FROM t"))
	if rr.Code < 200 || rr.Code > 299 {
		t.Fatalf("write export status = %d, want committed 2xx", rr.Code)
	}
}

// TestSQLiteWAL 验证 WAL 模式库未 checkpoint 的数据也能只读看到
// (codegraph.db 即 WAL 模式,查看器必须读到 -wal 中的最新数据)。
func TestSQLiteWAL(t *testing.T) {
	h, root := newTestHandler(t, false)
	p := filepath.Join(root, "wal.db")
	db := openSQLite(t, p)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t VALUES ('uncheckpointed')"); err != nil {
		t.Fatal(err)
	}
	// 连接保持打开:数据仍在 -wal,未 checkpoint 回主库
	rr := doGet(t, h, "/api/sqlite/rows?path=wal.db&table=t")
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.Rows[0][0] != "uncheckpointed" {
		t.Fatalf("WAL rows = %#v, want [[uncheckpointed]]", rp.Rows)
	}
	rr = doGet(t, h, "/api/sqlite/tables?path=wal.db")
	assertStatus(t, rr, http.StatusOK)
	if tr := decodeTables(t, rr); len(tr.Tables) != 1 || tr.Tables[0].Rows != 1 {
		t.Fatalf("WAL tables = %+v, want t rows=1", tr.Tables)
	}
}
