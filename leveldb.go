package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/syndtr/goleveldb/leveldb"
	lerrors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// ldbIdleTTL 是句柄缓存中闲置 DB 的淘汰阈值:超过该时长没有请求,
// 下次访问时关闭并重开(新快照语义,惰性淘汰,无后台协程)。
const ldbIdleTTL = 60 * time.Second

// maxLDBValue 是 get 单值上限,与代码/XML 查看器 5MB 惯例一致;
// 超过的值以 tooBig 标记,提示用 dump 导出。
const maxLDBValue = 5 * 1024 * 1024

// ldbEntry 是 leveldb 句柄缓存条目。
type ldbEntry struct {
	db   *leveldb.DB
	last time.Time // 最近一次 release 时间(活跃会话保活)
}

// errNotLevelDBDir 是路径不是 leveldb 数据目录时的哨兵错误。
var errNotLevelDBDir = errors.New("not a leveldb data directory")

// isLevelDBEntries 仅按条目名字判断目录是否为 leveldb 数据目录:
// 存在名为 CURRENT 的条目且存在以 MANIFEST- 开头的条目。不 stat。
func isLevelDBEntries(de []os.DirEntry) bool {
	hasCurrent, hasManifest := false, false
	for _, e := range de {
		name := e.Name()
		switch {
		case name == "CURRENT":
			hasCurrent = true
		case len(name) > len("MANIFEST-") && name[:len("MANIFEST-")] == "MANIFEST-":
			hasManifest = true
		}
		if hasCurrent && hasManifest {
			return true
		}
	}
	return false
}

// isLevelDBDir 报告 dir 是否为 leveldb 数据目录;ReadDir 出错返回 false。
func isLevelDBDir(dir string) bool {
	de, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return isLevelDBEntries(de)
}

// levelDBAcquire 返回 dir 的只读 DB 句柄与 release 函数。
// 缓存命中且活跃(<=ldbIdleTTL)时直接复用;闲置超时则关闭重开
// (快照语义);未命中则 OpenFile(ReadOnly) 后入缓存。
// release 保活:加锁后若缓存仍指向该 db,更新 last。
func (s *server) levelDBAcquire(dir string) (*leveldb.DB, func(), error) {
	s.ldbMu.Lock()
	defer s.ldbMu.Unlock()
	if e, ok := s.ldbCache[dir]; ok {
		if time.Since(e.last) > ldbIdleTTL {
			e.db.Close()
			delete(s.ldbCache, dir)
		} else {
			e.last = time.Now()
			return e.db, func() {}, nil
		}
	}
	db, err := leveldb.OpenFile(dir, &opt.Options{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	s.ldbCache[dir] = &ldbEntry{db: db, last: time.Now()}
	return db, func() {
		s.ldbMu.Lock()
		if e, ok := s.ldbCache[dir]; ok && e.db == db {
			e.last = time.Now()
		}
		s.ldbMu.Unlock()
	}, nil
}

// mapLevelDBOpenErr 将只读打开错误映射为响应;返回 true 表示已写响应。
func mapLevelDBOpenErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, syscall.EWOULDBLOCK):
		writeErr(w, http.StatusConflict, "数据库正被其他进程写入,无法只读打开(请先停止写入进程)")
	case lerrors.IsCorrupted(err):
		writeErr(w, http.StatusInternalServerError, "数据库损坏: "+err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "打开 leveldb 失败: "+err.Error())
	}
	return true
}

// resolveLevelDBDir 解析 leveldb 接口的 path 参数:真实目录、root 内、
// 且被识别为 leveldb 数据目录,返回规范路径(作缓存 key)。
// 非目录/归档内/非 leveldb 目录 → errNotLevelDBDir。
func (s *server) resolveLevelDBDir(r *http.Request) (string, error) {
	loc, err := s.resolveVirtual(r.URL.Query().Get("path"))
	if err != nil {
		return "", err
	}
	defer loc.close()
	if len(loc.chain) > 0 {
		return "", errNotLevelDBDir
	}
	st, err := os.Stat(loc.hostPath)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errNotLevelDBDir
	}
	if !isLevelDBDir(loc.hostPath) {
		return "", errNotLevelDBDir
	}
	return loc.hostPath, nil
}

// ldbValue 是 get/dump 共用的值响应形状:key 始终为 JSON 字符串,
// text 仅 UTF-8 合法时给出,否则给 base64;get 超过 maxLDBValue 给 tooBig。
type ldbValue struct {
	Key    string  `json:"key"`
	Size   int64   `json:"size"`
	Text   *string `json:"text,omitempty"` // 仅 UTF-8 合法时
	Base64 string  `json:"base64,omitempty"`
	TooBig bool    `json:"tooBig,omitempty"`
}

// levelDBResolveErr 把 resolveLevelDBDir 的错误映射为响应。
func levelDBResolveErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotLevelDBDir) {
		writeErr(w, http.StatusBadRequest, "不是 leveldb 数据目录")
		return
	}
	mapResolveErr(w, err)
}

// handleLevelDBKeys 返回前缀过滤的 key 列表(排他游标分页,免读 value)。
// 参数:path、prefix(默认 "")、after(默认 "",排他游标)、limit(默认 500,1..2000)。
func (s *server) handleLevelDBKeys(w http.ResponseWriter, r *http.Request) {
	dir, err := s.resolveLevelDBDir(r)
	if err != nil {
		levelDBResolveErr(w, err)
		return
	}
	db, release, err := s.levelDBAcquire(dir)
	if err != nil {
		mapLevelDBOpenErr(w, err)
		return
	}
	defer release()

	prefix := r.URL.Query().Get("prefix")
	after := r.URL.Query().Get("after")
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 2000 {
		limit = 2000
	}

	it := db.NewIterator(nil, nil)
	defer it.Release()
	if after != "" {
		it.Seek([]byte(after))
		if it.Valid() {
			it.Next() // 排他游标:跳过等于 after 的 key
		}
	} else {
		it.Seek([]byte(prefix))
	}
	keys := make([]string, 0, limit)
	for it.Valid() && bytes.HasPrefix(it.Key(), []byte(prefix)) && len(keys) < limit {
		keys = append(keys, string(it.Key())) // 拷贝:Next() 后 Key() 失效
		it.Next()
	}
	hasMore := it.Valid() && bytes.HasPrefix(it.Key(), []byte(prefix))
	if err := it.Error(); err != nil {
		writeErr(w, http.StatusInternalServerError, "迭代失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "hasMore": hasMore})
}

// handleLevelDBGet 返回单个 key 的值。
// 参数:path、key。
func (s *server) handleLevelDBGet(w http.ResponseWriter, r *http.Request) {
	dir, err := s.resolveLevelDBDir(r)
	if err != nil {
		levelDBResolveErr(w, err)
		return
	}
	db, release, err := s.levelDBAcquire(dir)
	if err != nil {
		mapLevelDBOpenErr(w, err)
		return
	}
	defer release()

	key := r.URL.Query().Get("key")
	v, err := db.Get([]byte(key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取失败: "+err.Error())
		return
	}
	resp := ldbValue{Key: key, Size: int64(len(v))}
	switch {
	case len(v) > maxLDBValue:
		resp.TooBig = true
	case utf8.Valid(v):
		text := string(v) // 拷贝:Get 返回的切片归调用方所有,但保持一致习惯
		resp.Text = &text
	default:
		resp.Base64 = base64.StdEncoding.EncodeToString(v)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLevelDBDump 流式导出前缀下的全部 key/value 为 NDJSON 附件。
// 参数:path、prefix(默认 "",= 全部)。裸注册(无 withFrontendEncoding):
// 流式压缩会破坏逐行 flush。值无上限。
func (s *server) handleLevelDBDump(w http.ResponseWriter, r *http.Request) {
	dir, err := s.resolveLevelDBDir(r)
	if err != nil {
		levelDBResolveErr(w, err)
		return
	}
	db, release, err := s.levelDBAcquire(dir)
	if err != nil {
		mapLevelDBOpenErr(w, err)
		return
	}
	defer release()

	prefix := r.URL.Query().Get("prefix")
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="dump-%s.jsonl"`, sanitizeName(prefix)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	it := db.NewIterator(nil, nil)
	defer it.Release()
	it.Seek([]byte(prefix))
	enc := json.NewEncoder(w)
	for it.Valid() && bytes.HasPrefix(it.Key(), []byte(prefix)) {
		k := string(it.Key()) // 拷贝:Next() 后 Key() 失效
		v := it.Value()
		val := make([]byte, len(v))
		copy(val, v) // 拷贝:Next() 后 Value() 失效
		rec := ldbValue{Key: k, Size: int64(len(val))}
		if utf8.Valid(val) {
			text := string(val)
			rec.Text = &text
		} else {
			rec.Base64 = base64.StdEncoding.EncodeToString(val)
		}
		if err := enc.Encode(rec); err != nil {
			return // 客户端断开等,流式语义:直接结束
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		it.Next()
	}
	if err := it.Error(); err != nil {
		// 状态已提交,只能记日志;客户端收到截断的 NDJSON。
		log.Printf("leveldb dump 迭代中断(%s): %v", dir, err)
	}
}

// sanitizeName 把任意字符串净化为文件名安全形式(非法字符 → _);
// 结果为空 → "all"。与前端 ldbName 同规则。
func sanitizeName(s string) string {
	s = nonNameChars.ReplaceAllString(s, "_")
	if s == "" {
		return "all"
	}
	return s
}

var nonNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
