package main

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xitongsys/parquet-go/common"
	"github.com/xitongsys/parquet-go/parquet"
	"github.com/xitongsys/parquet-go/reader"
	"github.com/xitongsys/parquet-go/schema"
	"github.com/xitongsys/parquet-go/source"
)

// parquetIdleTTL 是句柄缓存中闲置 reader 的淘汰阈值,与 sqlite 同规则:
// 超过该时长没有请求,下次访问时关闭重开。
const parquetIdleTTL = 60 * time.Second

// maxParquetCellJSON 是嵌套/二进制单元格塞进 JSON 响应的上限;超过的
// 只给占位 {n, big},避免响应膨胀。
const maxParquetCellJSON = 64 * 1024

// maxParquetSafeInt 是 JS Number 能无损表示的最大整数(2^53-1)。
const maxParquetSafeInt = int64(9007199254740991)

// parquetReadNP 是 parquet-go 解组并行度;>0 才能避免内部 worker 死锁。
const parquetReadNP int64 = 4

// parquetMagic 是 Parquet 文件头/尾签名("PAR1")。
const parquetMagic = "PAR1"

// errNotParquetFile 是路径不是 parquet 文件时的哨兵错误。
var errNotParquetFile = errors.New("not a parquet file")

// pqEntry 是 parquet 只读 reader 缓存条目。reader 有顺序位置,同一文件
// 的请求串行化(mu);pos 是下一次 Read 的行号,与 offset 对齐时可免 Skip。
type pqEntry struct {
	mu   sync.Mutex
	pr   *reader.ParquetReader
	pf   source.ParquetFile
	pos  int64
	last time.Time
	path string
}

// parquetFile 实现 parquet-go 的 source.ParquetFile,本地只读(测试写夹具
// 也走同一类型)。Open("") 按 path 再开一个句柄,供列缓冲各自 Seek。
type parquetFile struct {
	path string
	f    *os.File
}

func openParquetOS(path string) (*parquetFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &parquetFile{path: path, f: f}, nil
}

func createParquetOS(path string) (*parquetFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &parquetFile{path: path, f: f}, nil
}

func (p *parquetFile) Seek(offset int64, whence int) (int64, error) {
	return p.f.Seek(offset, whence)
}

// Read 循环读满 buf(与 parquet-go-source/local 同语义),EOF 时返回已读字节。
func (p *parquetFile) Read(b []byte) (cnt int, err error) {
	var n int
	ln := len(b)
	for cnt < ln {
		n, err = p.f.Read(b[cnt:])
		cnt += n
		if err != nil {
			break
		}
	}
	return cnt, err
}

func (p *parquetFile) Write(b []byte) (int, error) {
	return p.f.Write(b)
}

func (p *parquetFile) Close() error {
	if p.f == nil {
		return nil
	}
	err := p.f.Close()
	p.f = nil
	return err
}

func (p *parquetFile) Open(name string) (source.ParquetFile, error) {
	if name == "" {
		name = p.path
	}
	return openParquetOS(name)
}

func (p *parquetFile) Create(name string) (source.ParquetFile, error) {
	if name == "" {
		name = p.path
	}
	return createParquetOS(name)
}

// isParquetFile 报告 path 是否是 parquet 文件(按文件头 magic 判断)。
func isParquetFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [4]byte
	n, err := io.ReadFull(f, head[:])
	return err == nil && n == 4 && string(head[:]) == parquetMagic
}

func openParquetReader(path string) (*reader.ParquetReader, source.ParquetFile, error) {
	pf, err := openParquetOS(path)
	if err != nil {
		return nil, nil, err
	}
	pr, err := reader.NewParquetReader(pf, nil, parquetReadNP)
	if err != nil {
		pf.Close()
		return nil, nil, err
	}
	return pr, pf, nil
}

func (e *pqEntry) closeLocked() {
	if e.pr != nil {
		e.pr.ReadStop()
		e.pr = nil
	}
	if e.pf != nil {
		e.pf.Close()
		e.pf = nil
	}
	e.pos = 0
}

func (e *pqEntry) reopenLocked() error {
	e.closeLocked()
	pr, pf, err := openParquetReader(e.path)
	if err != nil {
		return err
	}
	e.pr, e.pf, e.pos = pr, pf, 0
	return nil
}

// seekLocked 把 reader 移到 offset:已过目标则重开,未到则 SkipRows。
func (e *pqEntry) seekLocked(offset int64) error {
	if offset < 0 {
		offset = 0
	}
	if offset == e.pos {
		return nil
	}
	if offset < e.pos {
		if err := e.reopenLocked(); err != nil {
			return err
		}
	}
	if offset > e.pos {
		if err := e.pr.SkipRows(offset - e.pos); err != nil {
			_ = e.reopenLocked()
			return err
		}
		e.pos = offset
	}
	return nil
}

// parquetAcquire 返回 path 的缓存 reader(已持有 e.mu)与 release。
// 命中且活跃时复用;闲置超时则关闭重开;未命中则打开后入缓存。
func (s *server) parquetAcquire(path string) (*pqEntry, func(), error) {
	s.pqMu.Lock()
	if e, ok := s.pqCache[path]; ok {
		if time.Since(e.last) > parquetIdleTTL {
			e.mu.Lock()
			e.closeLocked()
			e.mu.Unlock()
			delete(s.pqCache, path)
		} else {
			e.last = time.Now()
			e.mu.Lock()
			s.pqMu.Unlock()
			return e, func() {
				e.last = time.Now()
				e.mu.Unlock()
			}, nil
		}
	}
	t0 := time.Now()
	pr, pf, err := openParquetReader(path)
	if err != nil {
		s.pqMu.Unlock()
		return nil, nil, err
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] parquet-open path=%s took=%s", path, d.Round(time.Millisecond))
	}
	e := &pqEntry{pr: pr, pf: pf, last: time.Now(), path: path}
	s.pqCache[path] = e
	e.mu.Lock()
	s.pqMu.Unlock()
	return e, func() {
		e.last = time.Now()
		e.mu.Unlock()
	}, nil
}

// resolveParquetFile 解析 parquet 接口的 path 参数:真实文件、root 内、
// 且被识别为 parquet 文件,返回规范路径(作缓存 key)。
// 归档内成员/目录/非 parquet 文件 → errNotParquetFile。
func (s *server) resolveParquetFile(r *http.Request) (string, error) {
	loc, err := s.resolveVirtual(r.URL.Query().Get("path"))
	if err != nil {
		return "", err
	}
	defer loc.close()
	if len(loc.chain) > 0 {
		return "", errNotParquetFile // 归档内成员无独立文件路径
	}
	st, err := os.Stat(loc.hostPath)
	if err != nil {
		return "", err
	}
	if st.IsDir() || !isParquetFile(loc.hostPath) {
		return "", errNotParquetFile
	}
	return loc.hostPath, nil
}

func parquetResolveErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotParquetFile) {
		writeErr(w, http.StatusBadRequest, "不是 Parquet 文件")
		return
	}
	mapResolveErr(w, err)
}

func mapParquetOpenErr(w http.ResponseWriter, err error) bool {
	writeErr(w, http.StatusBadRequest, "打开 Parquet 失败: "+err.Error())
	return true
}

// parquetCol 是 meta 响应中的单列:Name 为文件内原名(ExName)。
type parquetCol struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Repetition string `json:"repetition"` // required | optional | repeated
	inName     string
	path       string // parquet-go InName 路径,列扫描用
	leaf       bool
}

func parquetRepetition(rt parquet.FieldRepetitionType) string {
	switch rt {
	case parquet.FieldRepetitionType_OPTIONAL:
		return "optional"
	case parquet.FieldRepetitionType_REPEATED:
		return "repeated"
	default:
		return "required"
	}
}

func parquetTypeLabel(se *parquet.SchemaElement) string {
	if se.ConvertedType != nil {
		switch *se.ConvertedType {
		case parquet.ConvertedType_DECIMAL:
			return fmt.Sprintf("DECIMAL(%d,%d)", se.GetPrecision(), se.GetScale())
		case parquet.ConvertedType_LIST:
			return "LIST"
		case parquet.ConvertedType_MAP, parquet.ConvertedType_MAP_KEY_VALUE:
			return "MAP"
		default:
			return se.ConvertedType.String()
		}
	}
	if se.Type != nil {
		if *se.Type == parquet.Type_FIXED_LEN_BYTE_ARRAY && se.TypeLength != nil {
			return fmt.Sprintf("FIXED_LEN_BYTE_ARRAY(%d)", se.GetTypeLength())
		}
		return se.Type.String()
	}
	if se.GetNumChildren() > 0 {
		return "STRUCT"
	}
	return "UNKNOWN"
}

func skipParquetSubtree(schemas []*parquet.SchemaElement, i int) int {
	n := int(schemas[i].GetNumChildren())
	i++
	for j := 0; j < n; j++ {
		i = skipParquetSubtree(schemas, i)
	}
	return i
}

func parquetColumns(sh *schema.SchemaHandler) []parquetCol {
	schemas := sh.SchemaElements
	if len(schemas) == 0 {
		return nil
	}
	n := int(schemas[0].GetNumChildren())
	cols := make([]parquetCol, 0, n)
	i := 1
	for c := 0; c < n && i < len(schemas); c++ {
		se := schemas[i]
		cols = append(cols, parquetCol{
			Name:       sh.Infos[i].ExName,
			inName:     sh.Infos[i].InName,
			Type:       parquetTypeLabel(se),
			Repetition: parquetRepetition(se.GetRepetitionType()),
			path:       sh.IndexMap[int32(i)],
			leaf:       se.GetNumChildren() == 0,
		})
		i = skipParquetSubtree(schemas, i)
	}
	return cols
}

func parquetColNames(cols []parquetCol) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// parquetFilter 是一条字段过滤:Col 为空表示任意列;多条之间 AND。
// 查询参数 f=<op>:<col>=<value>,例如 f=~:name=你好、f=>=:age=20、f=~:=foo(全列包含)。
type parquetFilter struct {
	Col string
	Op  string
	Val string
}

const (
	pqOpEq       = "="
	pqOpNe       = "!="
	pqOpContains = "~"
	pqOpGt       = ">"
	pqOpGte      = ">="
	pqOpLt       = "<"
	pqOpLte      = "<="
	pqOpNull     = "null"
	pqOpNNull    = "nnull"
)

func validParquetOp(op string) bool {
	switch op {
	case pqOpEq, pqOpNe, pqOpContains, pqOpGt, pqOpGte, pqOpLt, pqOpLte, pqOpNull, pqOpNNull:
		return true
	}
	return false
}

// parseParquetFilters 解析重复的 f 参数。col 必须是已知列名或空(全列);
// 非法 op / 未知列返回 error,由调用方写成 400。
func parseParquetFilters(r *http.Request, cols []parquetCol) ([]parquetFilter, error) {
	raw := r.URL.Query()["f"]
	if len(raw) == 0 {
		return nil, nil
	}
	known := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		known[c.Name] = struct{}{}
	}
	out := make([]parquetFilter, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		op, rest, ok := strings.Cut(s, ":")
		if !ok || !validParquetOp(op) {
			return nil, fmt.Errorf("无效过滤: %s", s)
		}
		col, val, ok := strings.Cut(rest, "=")
		if !ok {
			return nil, fmt.Errorf("无效过滤: %s", s)
		}
		if col != "" {
			if _, exists := known[col]; !exists {
				return nil, fmt.Errorf("未知列: %s", col)
			}
		}
		out = append(out, parquetFilter{Col: col, Op: op, Val: val})
	}
	return out, nil
}

// parquetFilterChunk 是列扫描每次读取的行数。按列解码,块可以比整行反序列化大很多。
const parquetFilterChunk int64 = 65536

// parquetCoalesceGap 是按行号取数时允许顺带读掉的空洞。SkipRows(1) 在宽 schema
// 上仍会打开所有列的首页,比多读几行整行更贵。
const parquetCoalesceGap int64 = 32

func parquetFilterLeafPaths(cols []parquetCol, f parquetFilter, sh *schema.SchemaHandler) []string {
	if f.Col == "" {
		out := make([]string, len(sh.ValueColumns))
		copy(out, sh.ValueColumns)
		return out
	}
	var col parquetCol
	found := false
	for _, c := range cols {
		if c.Name == f.Col {
			col = c
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if col.leaf {
		return []string{col.path}
	}
	prefix := col.path + common.PAR_GO_PATH_DELIMITER
	var out []string
	for _, p := range sh.ValueColumns {
		if p == col.path || strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

func parquetValuesMatch(vals []any, dls []int32, maxDL int32, f parquetFilter) bool {
	present := make([]any, 0, 1)
	for i, v := range vals {
		if i < len(dls) && dls[i] < maxDL {
			continue
		}
		if v == nil {
			continue
		}
		present = append(present, v)
	}
	switch f.Op {
	case pqOpNull:
		return len(present) == 0
	case pqOpNNull:
		return len(present) > 0
	}
	if len(present) == 0 {
		return false
	}
	if f.Op == pqOpNe {
		for _, v := range present {
			if parquetCellMatch(v, pqOpEq, f.Val) {
				return false
			}
		}
		return true
	}
	for _, v := range present {
		if parquetCellMatch(v, f.Op, f.Val) {
			return true
		}
	}
	return false
}

func parquetResetColumn(cr *reader.ParquetReader, path string) {
	if cb, ok := cr.ColumnBuffers[path]; ok && cb != nil {
		cb.PFile.Close()
		delete(cr.ColumnBuffers, path)
	}
}

func parquetScanLeaf(cr *reader.ParquetReader, path string, f parquetFilter, hit []bool) error {
	maxDL, err := cr.SchemaHandler.MaxDefinitionLevel(common.StrToPath(path))
	if err != nil {
		maxDL = 0
	}
	total := int64(len(hit))
	var row int64
	for row < total {
		n := parquetFilterChunk
		if total-row < n {
			n = total - row
		}
		vals, rls, dls, err := cr.ReadColumnByPath(path, n)
		if err != nil {
			return err
		}
		if len(vals) == 0 {
			break
		}
		start := 0
		for i := 1; i <= len(vals); i++ {
			if i < len(vals) && (i >= len(rls) || rls[i] != 0) {
				continue
			}
			if row >= total {
				break
			}
			end := i
			if parquetValuesMatch(vals[start:end], dls[start:end], maxDL, f) {
				hit[row] = true
			}
			row++
			start = i
		}
	}
	return nil
}

// parquetMatchIndices 按列扫描过滤条件,返回命中行号(升序)。多条 f 为 AND;
// 同一条件扫该列下全部叶子(OR)。不反序列化嵌套行。
func parquetMatchIndices(path string, cols []parquetCol, filters []parquetFilter) ([]int64, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	pf, err := openParquetOS(path)
	if err != nil {
		return nil, err
	}
	cr, err := reader.NewParquetColumnReader(pf, parquetReadNP)
	if err != nil {
		pf.Close()
		return nil, err
	}
	defer func() {
		cr.ReadStop()
		pf.Close()
	}()

	total := cr.GetNumRows()
	if total <= 0 {
		return nil, nil
	}
	acc := make([]bool, total)
	for i := range acc {
		acc[i] = true
	}
	anyHit := true
	for _, f := range filters {
		leaves := parquetFilterLeafPaths(cols, f, cr.SchemaHandler)
		if len(leaves) == 0 {
			return nil, nil
		}
		hit := make([]bool, total)
		for _, lp := range leaves {
			if lp == "" {
				continue
			}
			parquetResetColumn(cr, lp)
			if err := parquetScanLeaf(cr, lp, f, hit); err != nil {
				return nil, err
			}
		}
		anyHit = false
		for i := range acc {
			acc[i] = acc[i] && hit[i]
			if acc[i] {
				anyHit = true
			}
		}
		if !anyHit {
			return nil, nil
		}
	}
	out := make([]int64, 0)
	for i, ok := range acc {
		if ok {
			out = append(out, int64(i))
		}
	}
	return out, nil
}

func parquetRowMatch(raw []any, cols []parquetCol, filters []parquetFilter) bool {
	for _, f := range filters {
		if f.Col == "" {
			ok := false
			for _, cell := range raw {
				if parquetCellMatch(cell, f.Op, f.Val) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
			continue
		}
		idx := -1
		for i, c := range cols {
			if c.Name == f.Col {
				idx = i
				break
			}
		}
		if idx < 0 || !parquetCellMatch(raw[idx], f.Op, f.Val) {
			return false
		}
	}
	return true
}

func parquetCellMatch(v any, op, val string) bool {
	switch op {
	case pqOpNull:
		return v == nil
	case pqOpNNull:
		return v != nil
	}
	if v == nil {
		return false
	}
	switch op {
	case pqOpContains:
		return strings.Contains(strings.ToLower(parquetCellText(v)), strings.ToLower(val))
	case pqOpEq:
		return parquetCellEq(v, val)
	case pqOpNe:
		return !parquetCellEq(v, val)
	case pqOpGt, pqOpGte, pqOpLt, pqOpLte:
		return parquetCellCmp(v, val, op)
	}
	return false
}

func parquetCellEq(v any, val string) bool {
	if n, ok := parquetAsFloat(v); ok {
		if vn, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
			return n == vn
		}
	}
	if b, ok := v.(bool); ok {
		if vb, ok := parseParquetBool(val); ok {
			return b == vb
		}
	}
	return parquetCellText(v) == val
}

func parquetCellCmp(v any, val, op string) bool {
	n, ok := parquetAsFloat(v)
	vn, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
	if ok && err == nil {
		switch op {
		case pqOpGt:
			return n > vn
		case pqOpGte:
			return n >= vn
		case pqOpLt:
			return n < vn
		case pqOpLte:
			return n <= vn
		}
	}
	s := parquetCellText(v)
	switch op {
	case pqOpGt:
		return s > val
	case pqOpGte:
		return s >= val
	case pqOpLt:
		return s < val
	case pqOpLte:
		return s <= val
	}
	return false
}

func parquetAsFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return float64(rv.Int()), true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return float64(rv.Uint()), true
		case reflect.Float32, reflect.Float64:
			return rv.Float(), true
		}
		return 0, false
	}
}

func parseParquetBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

func parquetCellText(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []byte:
		if utf8.Valid(x) {
			return string(x)
		}
		return ""
	default:
		if n, ok := parquetAsFloat(v); ok {
			if n == float64(int64(n)) {
				return strconv.FormatInt(int64(n), 10)
			}
			return strconv.FormatFloat(n, 'g', -1, 64)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func encodeParquetRow(raw []any) []any {
	out := make([]any, len(raw))
	for i, v := range raw {
		out[i] = encodeParquetValue(v)
	}
	return out
}

// handleParquetMeta 返回 schema(顶层列名/类型/重复性)与行数。
// 参数:path。打开大文件/宽表可能较慢,超阈值记 [slow] parquet-open。
func (s *server) handleParquetMeta(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveParquetFile(r)
	if err != nil {
		parquetResolveErr(w, err)
		return
	}
	e, release, err := s.parquetAcquire(path)
	if err != nil {
		mapParquetOpenErr(w, err)
		return
	}
	defer release()

	cols := parquetColumns(e.pr.SchemaHandler)
	writeJSON(w, http.StatusOK, map[string]any{
		"columns":   cols,
		"rows":      e.pr.GetNumRows(),
		"createdBy": e.pr.Footer.GetCreatedBy(),
		"rowGroups": len(e.pr.Footer.GetRowGroups()),
	})
}

// handleParquetRows 分页返回行数据。参数:path、offset(默认 0)、
// limit(默认 500,1..2000)、f(可重复,字段过滤,见 parseParquetFilters)。
// offset=0 时带 total:无过滤为文件行数,有过滤为列扫描得到的匹配数。
func (s *server) handleParquetRows(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveParquetFile(r)
	if err != nil {
		parquetResolveErr(w, err)
		return
	}
	e, release, err := s.parquetAcquire(path)
	if err != nil {
		mapParquetOpenErr(w, err)
		return
	}
	defer release()

	offset, limit := sqliteRowRange(r)
	cols := parquetColumns(e.pr.SchemaHandler)
	filters, err := parseParquetFilters(r, cols)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t0 := time.Now()
	out, hasMore, matchTotal, err := e.readRows(int64(offset), limit, cols, filters)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取失败: "+err.Error())
		return
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] parquet-rows path=%s offset=%d filters=%d took=%s", path, offset, len(filters), d.Round(time.Millisecond))
	}
	resp := map[string]any{"columns": parquetColNames(cols), "rows": out, "hasMore": hasMore}
	if offset == 0 {
		if len(filters) == 0 {
			resp["total"] = e.pr.GetNumRows()
		} else if matchTotal != nil {
			resp["total"] = *matchTotal
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (e *pqEntry) readRows(offset int64, limit int, cols []parquetCol, filters []parquetFilter) ([][]any, bool, *int64, error) {
	if len(filters) > 0 {
		return e.readFiltered(offset, limit, cols, filters)
	}
	total := e.pr.GetNumRows()
	if offset > total {
		offset = total
	}
	if err := e.seekLocked(offset); err != nil {
		return nil, false, nil, err
	}
	remain := total - e.pos
	if remain <= 0 {
		n := total
		return [][]any{}, false, &n, nil
	}
	want := limit
	if int64(want) > remain {
		want = int(remain)
	}
	objs, err := e.pr.ReadByNumber(want)
	if err != nil {
		_ = e.reopenLocked()
		return nil, false, nil, err
	}
	e.pos += int64(len(objs))
	out := make([][]any, len(objs))
	for i, obj := range objs {
		out[i] = parquetRowCells(obj, cols, encodeParquetValue)
	}
	return out, e.pos < total, &total, nil
}

// readFiltered 先按列扫描得到命中行号,再只反序列化当前页的行。
// 列扫描失败时回退到整行扫描(readFilteredScan)。
func (e *pqEntry) readFiltered(offset int64, limit int, cols []parquetCol, filters []parquetFilter) ([][]any, bool, *int64, error) {
	t0 := time.Now()
	idxs, err := parquetMatchIndices(e.path, cols, filters)
	if err != nil {
		log.Printf("[slow] parquet-filter-fallback path=%s err=%v", e.path, err)
		return e.readFilteredScan(offset, limit, cols, filters)
	}
	scanDur := time.Since(t0)
	n := int64(len(idxs))
	if offset > n {
		offset = n
	}
	end := offset + int64(limit)
	if end > n {
		end = n
	}
	page := idxs[offset:end]
	t1 := time.Now()
	out, err := e.readIndexRows(page, cols)
	if err != nil {
		return nil, false, nil, err
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] parquet-filter path=%s matches=%d scan=%s fetch=%s took=%s",
			e.path, n, scanDur.Round(time.Millisecond), time.Since(t1).Round(time.Millisecond), d.Round(time.Millisecond))
	}
	return out, end < n, &n, nil
}

func (e *pqEntry) readIndexRows(idxs []int64, cols []parquetCol) ([][]any, error) {
	out := make([][]any, 0, len(idxs))
	err := e.visitIndexRows(idxs, cols, func(raw []any) error {
		out = append(out, encodeParquetRow(raw))
		return nil
	})
	return out, err
}

// visitIndexRows 按升序行号读取整行,空洞 <= parquetCoalesceGap 时顺带读掉。
func (e *pqEntry) visitIndexRows(idxs []int64, cols []parquetCol, fn func([]any) error) error {
	want := 0
	for want < len(idxs) {
		last := idxs[want]
		j := want
		for j+1 < len(idxs) && idxs[j+1]-last <= parquetCoalesceGap {
			j++
			last = idxs[j]
		}
		readFrom := idxs[want]
		if readFrom > e.pos && readFrom-e.pos <= parquetCoalesceGap {
			readFrom = e.pos
		}
		if err := e.seekLocked(readFrom); err != nil {
			return err
		}
		n := int(last - e.pos + 1)
		if n <= 0 {
			want = j + 1
			continue
		}
		objs, err := e.pr.ReadByNumber(n)
		if err != nil {
			_ = e.reopenLocked()
			return err
		}
		base := e.pos
		e.pos += int64(len(objs))
		wi := want
		for k, obj := range objs {
			row := base + int64(k)
			if wi < len(idxs) && row == idxs[wi] {
				if err := fn(parquetRowCells(obj, cols, identityParquetValue)); err != nil {
					return err
				}
				wi++
			}
		}
		want = wi
		if len(objs) < n {
			break
		}
	}
	return nil
}

// readFilteredScan 整行反序列化扫描,仅作列扫描失败时的回退。
func (e *pqEntry) readFilteredScan(offset int64, limit int, cols []parquetCol, filters []parquetFilter) ([][]any, bool, *int64, error) {
	if err := e.reopenLocked(); err != nil {
		return nil, false, nil, err
	}
	total := e.pr.GetNumRows()
	const chunk = 256
	skipped := 0
	need := limit + 1
	var out [][]any
	for e.pos < total && len(out) < need {
		n := chunk
		if remain := total - e.pos; int64(n) > remain {
			n = int(remain)
		}
		objs, err := e.pr.ReadByNumber(n)
		if err != nil {
			_ = e.reopenLocked()
			return nil, false, nil, err
		}
		if len(objs) == 0 {
			break
		}
		e.pos += int64(len(objs))
		for _, obj := range objs {
			raw := parquetRowCells(obj, cols, identityParquetValue)
			if !parquetRowMatch(raw, cols, filters) {
				continue
			}
			if skipped < int(offset) {
				skipped++
				continue
			}
			out = append(out, encodeParquetRow(raw))
			if len(out) >= need {
				break
			}
		}
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	if e.pos >= total && !hasMore {
		n := int64(skipped + len(out))
		return out, false, &n, nil
	}
	return out, hasMore, nil, nil
}

// handleParquetExport 流式导出全部行为 CSV / JSONL 附件。
// 参数:path、format=csv|jsonl(默认 csv)。独立打开 reader,不占用缓存位置。
func (s *server) handleParquetExport(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveParquetFile(r)
	if err != nil {
		parquetResolveErr(w, err)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format != "csv" && format != "jsonl" {
		format = "csv"
	}

	t0 := time.Now()
	pr, pf, err := openParquetReader(path)
	if err != nil {
		mapParquetOpenErr(w, err)
		return
	}
	defer func() {
		pr.ReadStop()
		pf.Close()
	}()

	cols := parquetColumns(pr.SchemaHandler)
	filters, err := parseParquetFilters(r, cols)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	names := parquetColNames(cols)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	ext, ct := "csv", "text/csv; charset=utf-8"
	if format == "jsonl" {
		ext, ct = "jsonl", "application/x-ndjson; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.%s"`, sanitizeName(base), ext))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if format == "csv" {
		cw := csv.NewWriter(w)
		if err := cw.Write(names); err != nil {
			return
		}
		written := 0
		if err := parquetEachExportRow(pr, pf, path, cols, filters, func(cells []any) error {
			rec := make([]string, len(cols))
			for i, v := range cells {
				rec[i] = parquetCSVCell(v)
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
			if written++; written%500 == 0 {
				cw.Flush()
			}
			return nil
		}); err != nil {
			return
		}
		cw.Flush()
	} else {
		enc := json.NewEncoder(w)
		if err := parquetEachExportRow(pr, pf, path, cols, filters, func(cells []any) error {
			rec := make(map[string]any, len(cols))
			for i, v := range cells {
				rec[names[i]] = parquetJSONLCell(v)
			}
			if err := enc.Encode(rec); err != nil {
				return err
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return nil
		}); err != nil {
			return
		}
	}
	if d := time.Since(t0); d >= slowThreshold {
		log.Printf("[slow] parquet-export path=%s format=%s took=%s", path, format, d.Round(time.Millisecond))
	}
}

func parquetEachExportRow(pr *reader.ParquetReader, pf source.ParquetFile, path string, cols []parquetCol, filters []parquetFilter, fn func([]any) error) error {
	if len(filters) > 0 {
		idxs, err := parquetMatchIndices(path, cols, filters)
		if err == nil {
			tmp := &pqEntry{pr: pr, pf: pf, path: path}
			return tmp.visitIndexRows(idxs, cols, fn)
		}
	}
	total := pr.GetNumRows()
	const chunk = 500
	var read int64
	for read < total {
		n := chunk
		if remain := total - read; int64(n) > remain {
			n = int(remain)
		}
		objs, err := pr.ReadByNumber(n)
		if err != nil || len(objs) == 0 {
			break
		}
		read += int64(len(objs))
		for _, obj := range objs {
			cells := parquetRowCells(obj, cols, identityParquetValue)
			if len(filters) > 0 && !parquetRowMatch(cells, cols, filters) {
				continue
			}
			if err := fn(cells); err != nil {
				return err
			}
		}
	}
	return nil
}

func identityParquetValue(v any) any { return v }

func parquetRowCells(row any, cols []parquetCol, encode func(any) any) []any {
	out := make([]any, len(cols))
	if row == nil {
		return out
	}
	rv := reflect.ValueOf(row)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return out
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return out
	}
	for i, c := range cols {
		f := rv.FieldByName(c.inName)
		if !f.IsValid() {
			continue
		}
		for f.Kind() == reflect.Ptr || f.Kind() == reflect.Interface {
			if f.IsNil() {
				f = reflect.Value{}
				break
			}
			f = f.Elem()
		}
		if !f.IsValid() {
			continue
		}
		if (f.Kind() == reflect.Map || f.Kind() == reflect.Slice) && f.IsNil() {
			continue
		}
		out[i] = encode(f.Interface())
	}
	return out
}

func encodeParquetBlob(b []byte) any {
	if len(b) <= maxParquetCellJSON {
		return map[string]any{"b": base64.StdEncoding.EncodeToString(b), "n": len(b)}
	}
	return map[string]any{"b": "", "n": len(b), "big": true}
}

func encodeParquetValue(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return encodeParquetValue(rv.Elem().Interface())
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		x := rv.Int()
		if x > maxParquetSafeInt || x < -maxParquetSafeInt {
			return strconv.FormatInt(x, 10)
		}
		return x
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		x := rv.Uint()
		if x > uint64(maxParquetSafeInt) {
			return strconv.FormatUint(x, 10)
		}
		return x
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if s == "NaN" || s == "+Inf" || s == "-Inf" {
			return s
		}
		return f
	case reflect.String:
		s := rv.String()
		if !utf8.ValidString(s) {
			return encodeParquetBlob([]byte(s))
		}
		return s
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return encodeParquetBlob(rv.Bytes())
		}
		return encodeParquetJSON(v)
	case reflect.Map, reflect.Struct:
		return encodeParquetJSON(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func encodeParquetJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(b) > maxParquetCellJSON {
		return map[string]any{"n": len(b), "big": true}
	}
	return json.RawMessage(b)
}

func parquetCSVCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		if !utf8.ValidString(x) {
			return base64.StdEncoding.EncodeToString([]byte(x))
		}
		return x
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	}
}

func parquetJSONLCell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		if x > maxParquetSafeInt || x < -maxParquetSafeInt {
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
		if !utf8.ValidString(x) {
			return map[string]any{"$blob": base64.StdEncoding.EncodeToString([]byte(x))}
		}
		return x
	case []byte:
		return map[string]any{"$blob": base64.StdEncoding.EncodeToString(x)}
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Ptr, reflect.Interface:
			if rv.IsNil() {
				return nil
			}
			return parquetJSONLCell(rv.Elem().Interface())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
			return parquetJSONLCell(rv.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			u := rv.Uint()
			if u > uint64(maxParquetSafeInt) {
				return strconv.FormatUint(u, 10)
			}
			return u
		case reflect.Float32:
			return parquetJSONLCell(rv.Float())
		case reflect.Bool:
			return rv.Bool()
		case reflect.Slice, reflect.Map, reflect.Struct:
			return v
		default:
			return fmt.Sprintf("%v", v)
		}
	}
}
