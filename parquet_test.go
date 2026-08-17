package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xitongsys/parquet-go/parquet"
	"github.com/xitongsys/parquet-go/writer"
)

// pqRow 是测试夹具行:文本/数字/可空/列表/二进制。
type pqRow struct {
	Name  string   `parquet:"name=name, type=BYTE_ARRAY, convertedtype=UTF8"`
	Age   int32    `parquet:"name=age, type=INT32"`
	Score float64  `parquet:"name=score, type=DOUBLE"`
	Ok    bool     `parquet:"name=ok, type=BOOLEAN"`
	Note  *string  `parquet:"name=note, type=BYTE_ARRAY, convertedtype=UTF8"`
	Tags  []string `parquet:"name=tags, type=MAP, convertedtype=LIST, valuetype=BYTE_ARRAY, valueconvertedtype=UTF8"`
	Bin   string   `parquet:"name=bin, type=BYTE_ARRAY"`
}

func strPtr(s string) *string { return &s }

func writeParquet(t *testing.T, path string, rows []pqRow) {
	t.Helper()
	fw, err := createParquetOS(path)
	if err != nil {
		t.Fatalf("create parquet %s: %v", path, err)
	}
	pw, err := writer.NewParquetWriter(fw, new(pqRow), 1)
	if err != nil {
		fw.Close()
		t.Fatalf("parquet writer: %v", err)
	}
	pw.CompressionType = parquet.CompressionCodec_UNCOMPRESSED
	for i, r := range rows {
		if err := pw.Write(r); err != nil {
			pw.WriteStop()
			fw.Close()
			t.Fatalf("write row %d: %v", i, err)
		}
	}
	if err := pw.WriteStop(); err != nil {
		fw.Close()
		t.Fatalf("write stop: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("close parquet: %v", err)
	}
}

func parquetFixture(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "data.parquet")
	writeParquet(t, p, []pqRow{
		{Name: "你好", Age: 20, Score: 3.5, Ok: true, Note: strPtr("a"), Tags: []string{"x", "y"}, Bin: string([]byte{0x00, 0xff, 0x10})},
		{Name: "world", Age: 21, Score: 2.0, Ok: false, Note: nil, Tags: []string{"z"}, Bin: "ok"},
		{Name: "x", Age: 22, Score: 1.5, Ok: true, Note: strPtr("b"), Tags: nil, Bin: "c"},
	})
	return "data.parquet"
}

type parquetMetaResp struct {
	Columns   []parquetCol `json:"columns"`
	Rows      int64        `json:"rows"`
	RowGroups int          `json:"rowGroups"`
}

func TestParquetSniff(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.parquet")
	writeParquet(t, p, []pqRow{{Name: "a", Age: 1}})
	if got := sniffMime(p); got != "application/vnd.apache.parquet" {
		t.Fatalf("sniffMime(parquet) = %q, want application/vnd.apache.parquet", got)
	}
	if !isParquetFile(p) {
		t.Fatal("isParquetFile = false")
	}
	txt := filepath.Join(dir, "t.txt")
	writeFile(t, txt, "hello")
	if got := sniffMime(txt); got == "application/vnd.apache.parquet" {
		t.Fatalf("sniffMime(txt) = parquet, want not")
	}
}

func TestParquetMeta(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	rr := doGet(t, h, "/api/parquet/meta?path=data.parquet")
	assertStatus(t, rr, http.StatusOK)
	var resp parquetMetaResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Rows != 3 {
		t.Fatalf("rows = %d, want 3", resp.Rows)
	}
	if resp.RowGroups < 1 {
		t.Fatalf("rowGroups = %d, want >=1", resp.RowGroups)
	}
	names := map[string]string{}
	for _, c := range resp.Columns {
		names[c.Name] = c.Type
	}
	if names["name"] != "UTF8" || names["age"] != "INT32" || names["score"] != "DOUBLE" {
		t.Fatalf("columns = %+v", resp.Columns)
	}
	if names["tags"] != "LIST" {
		t.Fatalf("tags type = %q, want LIST", names["tags"])
	}
	var note parquetCol
	for _, c := range resp.Columns {
		if c.Name == "note" {
			note = c
		}
	}
	if note.Repetition != "optional" {
		t.Fatalf("note repetition = %q, want optional", note.Repetition)
	}
}

func TestParquetRowsPaging(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	base := "/api/parquet/rows?path=data.parquet"

	rr := doGet(t, h, base+"&offset=0&limit=2")
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 2 || !rp.HasMore {
		t.Fatalf("page1 rows=%d hasMore=%v, want 2/true", len(rp.Rows), rp.HasMore)
	}
	if rp.Total == nil || *rp.Total != 3 {
		t.Fatalf("page1 total = %v, want 3", rp.Total)
	}
	if rp.Rows[0][0] != "你好" {
		t.Fatalf("row0.name = %v, want 你好", rp.Rows[0][0])
	}
	blob, ok := rp.Rows[0][6].(map[string]any)
	if !ok || blob["b"] == "" || blob["n"] != float64(3) {
		t.Fatalf("row0.bin = %#v, want {b,n} blob", rp.Rows[0][6])
	}
	if rp.Rows[1][4] != nil {
		t.Fatalf("row1.note = %#v, want null", rp.Rows[1][4])
	}
	tags, ok := rp.Rows[0][5].([]any)
	if !ok || len(tags) != 2 || tags[0] != "x" {
		t.Fatalf("row0.tags = %#v, want [x y]", rp.Rows[0][5])
	}

	rr = doGet(t, h, base+"&offset=2&limit=2")
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.HasMore {
		t.Fatalf("page2 rows=%d hasMore=%v, want 1/false", len(rp.Rows), rp.HasMore)
	}
	if rp.Total != nil {
		t.Fatalf("page2 total = %v, want nil", rp.Total)
	}
}

func TestParquetBigInt(t *testing.T) {
	h, root := newTestHandler(t, false)
	type bigRow struct {
		V int64 `parquet:"name=v, type=INT64"`
	}
	p := filepath.Join(root, "big.parquet")
	fw, err := createParquetOS(p)
	if err != nil {
		t.Fatal(err)
	}
	pw, err := writer.NewParquetWriter(fw, new(bigRow), 1)
	if err != nil {
		t.Fatal(err)
	}
	pw.CompressionType = parquet.CompressionCodec_UNCOMPRESSED
	if err := pw.Write(bigRow{V: 9007199254740992}); err != nil {
		t.Fatal(err)
	}
	if err := pw.WriteStop(); err != nil {
		t.Fatal(err)
	}
	fw.Close()

	rr := doGet(t, h, "/api/parquet/rows?path=big.parquet")
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if v, ok := rp.Rows[0][0].(string); !ok || v != "9007199254740992" {
		t.Fatalf("huge int = %#v, want string 9007199254740992", rp.Rows[0][0])
	}
}

func TestParquetNotFile(t *testing.T) {
	h, root := newTestHandler(t, false)
	writeFile(t, filepath.Join(root, "x.txt"), "not parquet")
	rr := doGet(t, h, "/api/parquet/meta?path=x.txt")
	assertStatus(t, rr, http.StatusBadRequest)
	os.Mkdir(filepath.Join(root, "dir"), 0o755)
	rr = doGet(t, h, "/api/parquet/meta?path=dir")
	assertStatus(t, rr, http.StatusBadRequest)
	rr = doGet(t, h, "/api/parquet/meta?path=missing.parquet")
	assertStatus(t, rr, http.StatusNotFound)
}

func TestParquetExportCSV(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	rr := doGet(t, h, "/api/parquet/export?path=data.parquet&format=csv")
	assertStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content-type = %s, want text/csv", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "data.csv") {
		t.Fatalf("content-disposition = %s, want data.csv", cd)
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 4 { // 表头 + 3 行
		t.Fatalf("csv lines = %d, want 4:\n%s", len(lines), rr.Body.String())
	}
	if !strings.HasPrefix(lines[0], "name,age,score,ok,note,tags,bin") {
		t.Fatalf("csv header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "你好") {
		t.Fatalf("csv row1 missing 你好: %q", lines[1])
	}
	if !strings.Contains(lines[1], ",a,") {
		t.Fatalf("csv row1 note should be a, not JSON-quoted: %q", lines[1])
	}
}

func TestParquetExportJSONL(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	rr := doGet(t, h, "/api/parquet/export?path=data.parquet&format=jsonl")
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
	if rec["name"] != "你好" {
		t.Fatalf("jsonl name = %#v", rec["name"])
	}
	blob, ok := rec["bin"].(map[string]any)
	if !ok || blob["$blob"] == "" {
		t.Fatalf("jsonl blob = %#v, want {$blob: base64}", rec["bin"])
	}
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["note"] != nil {
		t.Fatalf("jsonl NULL note = %#v, want nil", rec["note"])
	}
}

func TestParquetListMime(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	rr := doGet(t, h, "/api/list?path=data.parquet")
	assertStatus(t, rr, http.StatusOK)
	lr := decodeList(t, rr)
	if lr.File == nil || lr.File.Mime != "application/vnd.apache.parquet" {
		t.Fatalf("list file = %+v, want parquet mime", lr.File)
	}
}

func TestParquetFilter(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	base := "/api/parquet/rows?path=data.parquet"

	// 等于
	rr := doGet(t, h, base+"&f="+url.QueryEscape("=:name=你好"))
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.Rows[0][0] != "你好" {
		t.Fatalf("eq name = %#v, want [[你好 …]]", rp.Rows)
	}
	if rp.Total == nil || *rp.Total != 1 {
		t.Fatalf("eq total = %v, want 1", rp.Total)
	}

	// 包含(全列):tags 含 x、name 为 x
	rr = doGet(t, h, base+"&f="+url.QueryEscape("~:=x"))
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 2 {
		t.Fatalf("contains x rows = %d, want 2", len(rp.Rows))
	}

	// 数值比较
	rr = doGet(t, h, base+"&f="+url.QueryEscape(">=:age=21"))
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 2 {
		t.Fatalf("age>=21 rows = %d, want 2", len(rp.Rows))
	}

	// 为空
	rr = doGet(t, h, base+"&f="+url.QueryEscape("null:note="))
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.Rows[0][0] != "world" {
		t.Fatalf("note null = %#v, want world", rp.Rows)
	}

	// AND
	rr = doGet(t, h, base+"&f="+url.QueryEscape("~:name=w")+"&f="+url.QueryEscape(">=:age=21"))
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.Rows[0][0] != "world" {
		t.Fatalf("AND filter = %#v, want world", rp.Rows)
	}

	// 布尔
	rr = doGet(t, h, base+"&f="+url.QueryEscape("=:ok=true"))
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 2 {
		t.Fatalf("ok=true rows = %d, want 2", len(rp.Rows))
	}

	// 未知列 / 非法 op
	rr = doGet(t, h, base+"&f="+url.QueryEscape("=:nope=x"))
	assertStatus(t, rr, http.StatusBadRequest)
	rr = doGet(t, h, base+"&f="+url.QueryEscape("??:name=x"))
	assertStatus(t, rr, http.StatusBadRequest)
}

func TestParquetFilterPaging(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	base := "/api/parquet/rows?path=data.parquet&f=" + url.QueryEscape(">=:age=20")

	rr := doGet(t, h, base+"&offset=0&limit=1")
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 1 || !rp.HasMore {
		t.Fatalf("page1 rows=%d hasMore=%v, want 1/true", len(rp.Rows), rp.HasMore)
	}
	if rp.Total == nil || *rp.Total != 3 {
		t.Fatalf("page1 total = %v, want 3", rp.Total)
	}
	if rp.Rows[0][0] != "你好" {
		t.Fatalf("page1 name = %v, want 你好", rp.Rows[0][0])
	}

	rr = doGet(t, h, base+"&offset=1&limit=1")
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 1 || !rp.HasMore || rp.Rows[0][0] != "world" {
		t.Fatalf("page2 = %#v hasMore=%v", rp.Rows, rp.HasMore)
	}

	rr = doGet(t, h, base+"&offset=2&limit=1")
	assertStatus(t, rr, http.StatusOK)
	rp = decodeRows(t, rr)
	if len(rp.Rows) != 1 || rp.HasMore || rp.Rows[0][0] != "x" {
		t.Fatalf("page3 = %#v hasMore=%v", rp.Rows, rp.HasMore)
	}
}

func TestParquetExportFilter(t *testing.T) {
	h, root := newTestHandler(t, false)
	parquetFixture(t, root)
	rr := doGet(t, h, "/api/parquet/export?path=data.parquet&format=csv&f="+url.QueryEscape("=:name=你好"))
	assertStatus(t, rr, http.StatusOK)
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 2 { // 表头 + 1 行
		t.Fatalf("filtered csv lines = %d, want 2:\n%s", len(lines), rr.Body.String())
	}
	if !strings.Contains(lines[1], "你好") {
		t.Fatalf("filtered csv row = %q", lines[1])
	}
}

func TestParquetCellMatch(t *testing.T) {
	if !parquetCellMatch("Hello", pqOpContains, "ell") {
		t.Fatal("contains should be case-insensitive")
	}
	if !parquetCellMatch(int32(20), pqOpEq, "20") || !parquetCellMatch(int32(20), pqOpGte, "20") {
		t.Fatal("int32 eq/gte 20")
	}
	if parquetCellMatch(nil, pqOpEq, "x") || !parquetCellMatch(nil, pqOpNull, "") {
		t.Fatal("nil match")
	}
	if !parquetCellMatch(true, pqOpEq, "true") || parquetCellMatch(false, pqOpEq, "true") {
		t.Fatal("bool match")
	}
}

func TestParquetValuesMatch(t *testing.T) {
	f := parquetFilter{Op: pqOpEq, Val: "吴万秀"}
	if !parquetValuesMatch([]any{"吴万秀"}, []int32{1}, 1, f) {
		t.Fatal("present string should match")
	}
	if parquetValuesMatch([]any{nil}, []int32{0}, 1, f) {
		t.Fatal("null should not eq")
	}
	if !parquetValuesMatch([]any{nil}, []int32{0}, 1, parquetFilter{Op: pqOpNull}) {
		t.Fatal("null op")
	}
	if !parquetValuesMatch([]any{"x", "y"}, []int32{1, 1}, 1, parquetFilter{Op: pqOpContains, Val: "x"}) {
		t.Fatal("list contains")
	}
	if parquetValuesMatch([]any{"x", "y"}, []int32{1, 1}, 1, parquetFilter{Op: pqOpNe, Val: "x"}) {
		t.Fatal("list != x should fail if any element is x")
	}
}

func TestParquetFilterOpenAlexSpeed(t *testing.T) {
	root := "/data/workspace/openalex"
	rel := "data/parquet/authors/updated_date=2016-06-24/part_0000.parquet"
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Skip(err)
	}
	h, err := newHandler(root, false)
	if err != nil {
		t.Fatal(err)
	}
	q := "/api/parquet/rows?path=" + url.QueryEscape(rel) +
		"&offset=0&limit=500&f=" + url.QueryEscape("=:display_name=吴万秀")
	t0 := time.Now()
	rr := doGet(t, h, q)
	d := time.Since(t0)
	assertStatus(t, rr, http.StatusOK)
	rp := decodeRows(t, rr)
	if len(rp.Rows) != 1 {
		t.Fatalf("rows = %d, want 1; body=%s", len(rp.Rows), rr.Body.String())
	}
	if rp.Total == nil || *rp.Total != 1 {
		t.Fatalf("total = %v, want 1", rp.Total)
	}
	if d > 2*time.Second {
		t.Fatalf("filter took %s, want <2s", d.Round(time.Millisecond))
	}
}
