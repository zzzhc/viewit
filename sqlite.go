package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite" // 注册 "sqlite" 驱动,纯 Go 实现(CGO_ENABLED=0 可用)
)

// sqliteIdleTTL 是句柄缓存中闲置连接的淘汰阈值,与 leveldb 缓存同规则:
// 超过该时长没有请求,下次访问时关闭重开(新快照语义,惰性淘汰)。
const sqliteIdleTTL = 60 * time.Second

// maxSQLiteBlobJSON 是 BLOB 单元格经 base64 塞进 JSON 响应的上限;超过的
// 只给占位 {n, big},前端提示用 SQL 或下载查看,避免响应膨胀。
const maxSQLiteBlobJSON = 64 * 1024

// maxSQLiteSafeInt 是 JS Number 能无损表示的最大整数(2^53-1);超过的
// INTEGER 单元格转字符串下发,前端展示/复制不丢精度。
const maxSQLiteSafeInt = int64(9007199254740991)

// maxSQLiteQueryRows 是单次 rows/query 响应最多返回的行数上限。
const maxSQLiteQueryRows = 2000

// defaultSQLiteRows 是 rows/query 的默认分页大小。
const defaultSQLiteRows = 500

// sqEntry 是 sqlite 只读句柄缓存条目。
type sqEntry struct {
	db   *sql.DB
	last time.Time // 最近一次 release 时间(活跃会话保活)
}

// errNotSQLiteFile 是路径不是 sqlite 文件时的哨兵错误。
var errNotSQLiteFile = errors.New("not a sqlite file")

// sqliteMagic 是 SQLite 数据库文件头签名("SQLite format 3\0",16 字节)。
// 与 sniffMimeFrom 中的识别一致;isSQLiteFile 单独 open 读头。
const sqliteMagic = "SQLite format 3"

// isSQLiteFile 报告 path 是否是 sqlite 数据库文件(按文件头 magic 判断)。
func isSQLiteFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [16]byte
	n, err := io.ReadFull(f, head[:])
	return err == nil && n == 16 && string(head[:15]) == sqliteMagic && head[15] == 0
}

// sqliteAcquire 返回 path 的只读连接与 release 函数。
// 缓存命中且活跃(<=sqliteIdleTTL)时直接复用;闲置超时则关闭重开
// (快照语义);未命中则打开后入缓存。单连接(SetMaxOpenConns(1)):
// 避免多连接下 WAL 库的锁竞争,行为可预期。
// DSN 用 _query_only(最后应用,连接严格只读,写语句一律拒绝)与
// _busy_timeout(3s,其他进程持锁时等待而非立即失败)。
// sql.Open 惰性建立连接,这里 Ping 一次验证可打开。
func (s *server) sqliteAcquire(path string) (*sql.DB, func(), error) {
	s.sqMu.Lock()
	defer s.sqMu.Unlock()
	if e, ok := s.sqCache[path]; ok {
		if time.Since(e.last) > sqliteIdleTTL {
			e.db.Close()
			delete(s.sqCache, path)
		} else {
			e.last = time.Now()
			return e.db, func() {}, nil
		}
	}
	dsn := "file:" + url.PathEscape(path) + "?_query_only=1&_busy_timeout=3000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	s.sqCache[path] = &sqEntry{db: db, last: time.Now()}
	return db, func() {
		s.sqMu.Lock()
		if e, ok := s.sqCache[path]; ok && e.db == db {
			e.last = time.Now()
		}
		s.sqMu.Unlock()
	}, nil
}

// mapSQLiteOpenErr 将打开/Ping 失败映射为响应;返回 true 表示已写响应。
func mapSQLiteOpenErr(w http.ResponseWriter, err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case 5: // SQLITE_BUSY:库正被其他进程写锁(或超时未等到共享锁)
			writeErr(w, http.StatusConflict, "数据库正被其他进程写入,暂时无法读取(请稍后重试或停止写入进程)")
			return true
		case 26: // SQLITE_NOTADB:文件不是有效数据库(头 magic 命中但内容损坏)
			writeErr(w, http.StatusBadRequest, "文件不是有效的 SQLite 数据库")
			return true
		}
	}
	writeErr(w, http.StatusInternalServerError, "打开 SQLite 失败: "+err.Error())
	return true
}

// resolveSQLiteFile 解析 sqlite 接口的 path 参数:真实文件、root 内、
// 且被识别为 sqlite 数据库文件,返回规范路径(作缓存 key)。
// 归档内成员/目录/非 sqlite 文件 → errNotSQLiteFile。
func (s *server) resolveSQLiteFile(r *http.Request) (string, error) {
	loc, err := s.resolveVirtual(r.URL.Query().Get("path"))
	if err != nil {
		return "", err
	}
	defer loc.close()
	if len(loc.chain) > 0 {
		return "", errNotSQLiteFile // 归档内成员无文件路径,sqlite 驱动无法打开
	}
	st, err := os.Stat(loc.hostPath)
	if err != nil {
		return "", err
	}
	if st.IsDir() || !isSQLiteFile(loc.hostPath) {
		return "", errNotSQLiteFile
	}
	return loc.hostPath, nil
}

// sqliteResolveErr 把 resolveSQLiteFile 的错误映射为响应。
func sqliteResolveErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotSQLiteFile) {
		writeErr(w, http.StatusBadRequest, "不是 SQLite 数据库文件")
		return
	}
	mapResolveErr(w, err)
}

// sqliteTable 是 tables 响应中的单张表/视图条目。
type sqliteTable struct {
	Name string `json:"name"`
	Type string `json:"type"` // table | view
	SQL  string `json:"sql"`  // CREATE 语句(来自 sqlite_master.sql)
	Rows int64  `json:"rows"` // 行数(COUNT,大表可能较慢)
}

// escapeSQLiteIdent 用 SQLite 双引号规则转义标识符(表名/列名),防止
// 元数据中的引号破坏拼接的 SQL。表名来自 sqlite_master,可信但不可假设。
func escapeSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// handleSQLiteTables 返回库中的表与视图列表(sqlite_% 内部表、FTS 影子
// 表不展示),每张附行数(COUNT)与 CREATE 语句。
// 参数:path。行数统计与深分页一样可能扫全表,超阈值记 [slow] sqlite-count。
func (s *server) handleSQLiteTables(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveSQLiteFile(r)
	if err != nil {
		sqliteResolveErr(w, err)
		return
	}
	db, release, err := s.sqliteAcquire(path)
	if err != nil {
		mapSQLiteOpenErr(w, err)
		return
	}
	defer release()

	t0 := time.Now()
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE type IN ('table','view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
		ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取 schema 失败: "+err.Error())
		return
	}
	var tables []sqliteTable
	for rows.Next() {
		var t sqliteTable
		var sqlText sql.NullString
		if err := rows.Scan(&t.Type, &t.Name, &sqlText); err != nil {
			rows.Close()
			writeErr(w, http.StatusInternalServerError, "读取 schema 失败: "+err.Error())
			return
		}
		t.SQL = sqlText.String
		tables = append(tables, t)
	}
	if err := rows.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, "读取 schema 失败: "+err.Error())
		return
	}
	// 每张表 COUNT 一次(视图也会执行其 SELECT);大表慢,记 [slow]。
	for i := range tables {
		var n int64
		if err := db.QueryRow("SELECT COUNT(*) FROM " + escapeSQLiteIdent(tables[i].Name)).Scan(&n); err != nil {
			log.Printf("[slow] sqlite-count path=%s table=%s error=%v", path, tables[i].Name, err)
			continue // 视图/表出错(如坏视图):行数留 0,不阻断整个列表
		}
		tables[i].Rows = n
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] sqlite-tables path=%s tables=%d took=%s", path, len(tables), d.Round(time.Millisecond))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

// sqliteRowRange 解析 rows 接口的 offset/limit 参数(offset>=0,limit 夹在
// 1..maxSQLiteQueryRows,默认 defaultSQLiteRows)。
func sqliteRowRange(r *http.Request) (offset, limit int) {
	offset = 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	limit = defaultSQLiteRows
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxSQLiteQueryRows {
		limit = maxSQLiteQueryRows
	}
	return offset, limit
}

// handleSQLiteRows 分页返回某张表/视图的数据(SELECT * 顺序)。
// 参数:path、table、offset(默认 0)、limit(默认 500,1..2000)。
// 多取一行判断 hasMore;offset=0 时顺带 COUNT 返回 total(供 UI 显示
// 总行数),深分页时不再重复统计。单元格编码见 encodeSQLiteValue。
func (s *server) handleSQLiteRows(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveSQLiteFile(r)
	if err != nil {
		sqliteResolveErr(w, err)
		return
	}
	db, release, err := s.sqliteAcquire(path)
	if err != nil {
		mapSQLiteOpenErr(w, err)
		return
	}
	defer release()

	table := r.URL.Query().Get("table")
	if table == "" {
		writeErr(w, http.StatusBadRequest, "缺少 table 参数")
		return
	}
	// 表必须存在于 sqlite_master,防任意表名拼接;同时返回其类型用于错误文案。
	var typ string
	err = db.QueryRow(`SELECT type FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, table).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "表不存在: "+table)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取 schema 失败: "+err.Error())
		return
	}

	offset, limit := sqliteRowRange(r)
	t0 := time.Now()
	rows, err := db.Query("SELECT * FROM "+escapeSQLiteIdent(table)+" LIMIT ? OFFSET ?", limit+1, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	columns, out, hasMore, qerr := scanSQLiteRows(rows, limit)
	if qerr != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+qerr.Error())
		return
	}
	resp := map[string]any{"columns": columns, "rows": out, "hasMore": hasMore}
	if offset == 0 {
		var total int64
		if err := db.QueryRow("SELECT COUNT(*) FROM " + escapeSQLiteIdent(table)).Scan(&total); err != nil {
			log.Printf("[slow] sqlite-count path=%s table=%s error=%v", path, table, err)
		} else {
			resp["total"] = total
		}
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] sqlite-rows path=%s table=%s offset=%d took=%s", path, table, offset, d.Round(time.Millisecond))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSQLiteQuery 执行任意只读 SQL(连接受 _query_only 保护,写语句会被
// SQLite 拒绝)并返回结果集;最多返回 limit 行,超出标记 truncated。
// 参数:path、sql、limit(默认 500,1..2000)。多语句只执行第一条。
func (s *server) handleSQLiteQuery(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveSQLiteFile(r)
	if err != nil {
		sqliteResolveErr(w, err)
		return
	}
	db, release, err := s.sqliteAcquire(path)
	if err != nil {
		mapSQLiteOpenErr(w, err)
		return
	}
	defer release()

	sqlText := strings.TrimSpace(r.URL.Query().Get("sql"))
	if sqlText == "" {
		writeErr(w, http.StatusBadRequest, "缺少 sql 参数")
		return
	}
	_, limit := sqliteRowRange(r)
	t0 := time.Now()
	rows, err := db.Query(sqlText)
	if err != nil {
		// 语法错误与写语句(SQLITE_READONLY)都在这:4xx,带 SQLite 原文
		writeErr(w, http.StatusBadRequest, "查询失败: "+err.Error())
		return
	}
	columns, out, hasMore, qerr := scanSQLiteRows(rows, limit)
	if qerr != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+qerr.Error())
		return
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] sqlite-query path=%s rows=%d took=%s", path, len(out), d.Round(time.Millisecond))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"columns": columns, "rows": out, "truncated": hasMore,
	})
}

// scanSQLiteRows 从 rows 读取最多 limit+1 行(多的一行用于判 hasMore),
// 返回列名、编码后的行数据与是否还有更多。调用方负责处理返回值后
// 写响应;rows 由本函数 Close。
func scanSQLiteRows(rows *sql.Rows, limit int) (columns []string, out [][]any, hasMore bool, err error) {
	defer rows.Close()
	columns, err = rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	ptrs := make([]any, len(columns))
	vals := make([]any, len(columns))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		row := make([]any, len(columns))
		for i, v := range vals {
			row[i] = encodeSQLiteValue(v)
		}
		out = append(out, row)
		if len(out) > limit {
			break // 已超限:多读了一行,hasMore=true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	if len(out) > limit {
		out = out[:limit]
		return columns, out, true, nil
	}
	return columns, out, false, nil
}

// handleSQLiteExport 流式导出表数据或查询结果为 CSV / JSONL 附件。
// 参数:path;table 或 sql 二选一(表浏览导出整表,查询导出完整结果,不受
// query 接口的行数截断限制);format=csv|jsonl(默认 csv)。
// 裸注册(无 withFrontendEncoding):流式压缩会破坏逐行 flush,与
// leveldb dump 同规则。值无上限,内存 O(1)。
func (s *server) handleSQLiteExport(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveSQLiteFile(r)
	if err != nil {
		sqliteResolveErr(w, err)
		return
	}
	db, release, err := s.sqliteAcquire(path)
	if err != nil {
		mapSQLiteOpenErr(w, err)
		return
	}
	defer release()

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format != "csv" && format != "jsonl" {
		format = "csv"
	}
	table := r.URL.Query().Get("table")
	sqlText := strings.TrimSpace(r.URL.Query().Get("sql"))
	var query, name string
	switch {
	case sqlText != "":
		query = sqlText
		name = "query"
	case table != "":
		var typ string
		err = db.QueryRow(`SELECT type FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, table).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "表不存在: "+table)
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取 schema 失败: "+err.Error())
			return
		}
		query = "SELECT * FROM " + escapeSQLiteIdent(table)
		name = table
	default:
		writeErr(w, http.StatusBadRequest, "缺少 table 或 sql 参数")
		return
	}

	ext, ct := "csv", "text/csv; charset=utf-8"
	if format == "jsonl" {
		ext, ct = "jsonl", "application/x-ndjson; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.%s"`, sanitizeName(name), ext))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	rows, err := db.Query(query)
	if err != nil {
		// 状态已提交,只能记日志(写语句/语法错误:sql 模式才会到这里)。
		log.Printf("[slow] sqlite-export path=%s error=%v", path, err)
		return
	}
	t0 := time.Now()
	cols, err := rows.Columns()
	if err != nil {
		rows.Close()
		return
	}
	ptrs := make([]any, len(cols))
	vals := make([]any, len(cols))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}

	if format == "csv" {
		cw := csv.NewWriter(w)
		if err := cw.Write(cols); err != nil {
			rows.Close()
			return
		}
		written := 0
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				break
			}
			rec := make([]string, len(cols))
			for i, v := range vals {
				rec[i] = sqliteCSVCell(v)
			}
			if err := cw.Write(rec); err != nil {
				rows.Close()
				return // 客户端断开:直接结束
			}
			if written++; written%500 == 0 {
				cw.Flush() // 定期落盘,客户端能看到进度
			}
		}
		cw.Flush()
		rows.Close()
	} else {
		enc := json.NewEncoder(w)
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				break
			}
			rec := make(map[string]any, len(cols))
			for i, v := range vals {
				rec[cols[i]] = sqliteJSONLCell(v)
			}
			if err := enc.Encode(rec); err != nil {
				rows.Close()
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		rows.Close()
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] sqlite-export path=%s table=%s format=%s took=%s", path, name, format, d.Round(time.Millisecond))
	}
}

// sqliteCSVCell 把单元格值编码为 CSV 字符串:NULL→空串,BLOB→base64,
// 大整数保持原样(CSV 无 JS 精度问题)。
func sqliteCSVCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return x
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// sqliteJSONLCell 把单元格值编码为 JSONL 值:BLOB→{"$blob": base64},
// |int64| 超 2^53 转字符串(与查看器同规则),NaN/Inf 转字符串。
func sqliteJSONLCell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		if x > maxSQLiteSafeInt || x < -maxSQLiteSafeInt {
			return strconv.FormatInt(x, 10)
		}
		return x
	case float64:
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if s == "NaN" || s == "+Inf" || s == "-Inf" {
			return s
		}
		return x
	case string:
		return x
	case []byte:
		return map[string]any{"$blob": base64.StdEncoding.EncodeToString(x)}
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		if s, ok := x.(fmt.Stringer); ok {
			return s.String()
		}
		return fmt.Sprintf("%v", x)
	}
}

// encodeSQLiteValue 把 database/sql 扫描出的单元格值编码为 JSON 安全形状:
// nil→null;|int64| 超过 2^53-1 转字符串(JS Number 精度);float64 的
// NaN/Inf(JSON 无法表示)转字符串;[]byte 按 maxSQLiteBlobJSON 阈值给
// base64 或占位 {n, big};其余类型防御性转字符串。
func encodeSQLiteValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		if x > maxSQLiteSafeInt || x < -maxSQLiteSafeInt {
			return strconv.FormatInt(x, 10)
		}
		return x
	case float64:
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if s == "NaN" || s == "+Inf" || s == "-Inf" {
			return s
		}
		return x
	case string:
		return x
	case []byte:
		if len(x) <= maxSQLiteBlobJSON {
			return map[string]any{"b": base64.StdEncoding.EncodeToString(x), "n": len(x)}
		}
		return map[string]any{"b": "", "n": len(x), "big": true}
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		if s, ok := x.(fmt.Stringer); ok {
			return s.String()
		}
		return fmt.Sprintf("%v", x)
	}
}
