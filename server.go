package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"

	"errors"
	"fmt"
	"github.com/andybalholm/brotli"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// errOutsideRoot is the sentinel for any resolved path that escapes root.
var errOutsideRoot = errors.New("path outside root")

type server struct {
	root     string         // canonicalized, symlink-free root directory
	dist     fs.FS          // embedded frontend/dist.gz (pre-gzipped assets)
	index    []byte         // gzipped index.html, served for index/SPA routes
	idx      *findIndex     // fuzzy-find index, built lazily on first WS connection
	tarStore *tarIndexStore // resident tar indexes, keyed by host path

	ldbMu    sync.Mutex               // guards ldbCache
	ldbCache map[string]*ldbEntry     // leveldb 只读句柄缓存,按规范路径

	sqMu    sync.Mutex           // guards sqCache
	sqCache map[string]*sqEntry  // sqlite 只读句柄缓存,按规范路径
}

type fileEntry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"modTime"`
	IsDir     bool      `json:"isDir"`
	IsArchive bool      `json:"isArchive,omitempty"` // zip/tar 文件:真实文件,点击进入归档浏览
	Mime      string    `json:"mime,omitempty"`      // content-sniffed, type/subtype only
}

type listResponse struct {
	Path    string      `json:"path"`
	IsDir   bool        `json:"isDir"`
	Total   int         `json:"total,omitempty"`  // 目录模式:排序后总条目数(分页时前端用它撑虚拟滚动高度)
	Offset  int         `json:"offset,omitempty"` // 目录模式:本页在总条目中的起始下标
	Entries []fileEntry `json:"entries,omitempty"`
	File    *fileEntry  `json:"file,omitempty"`
	Images  []string    `json:"images,omitempty"` // images=1 时:目录下图片文件名(目录排序序),供图片查看器上一张/下一张切换
	LevelDB bool        `json:"leveldb,omitempty"` // 真实目录模式:当前目录被识别为 leveldb 数据目录
}

// imageExts 是图片查看器"上一张/下一张"切换的扩展名集合,必须与
// frontend/src/viewers.js 的 IMAGE+TIFF 保持一致(两端同步修改),
// 否则切换会漏掉或误收文件。
var imageExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "jfif": true, "gif": true,
	"webp": true, "svg": true, "bmp": true, "avif": true, "apng": true,
	"ico": true, "tif": true, "tiff": true,
}

// isImageName 按扩展名(不区分大小写)判定文件名是否为图片。
func isImageName(name string) bool {
	i := strings.LastIndexByte(name, '.')
	return i >= 0 && imageExts[strings.ToLower(name[i+1:])]
}

//	newHandler canonicalizes root (Abs + EvalSymlinks) and wires the routes:
//
//	GET /api/list, GET /api/file, GET /api/raw/{path...}, GET /api/download,
//	GET /api/ws (fuzzy find), GET /api/stream (on-demand streaming, .gz transparent)
//	GET /{$} -> index, GET / -> SPA fallback
//
// The embedded frontend is served in dev mode too, so the server is fully
// usable standalone; Vite on :5173 remains the HMR alternative.
func newHandler(root string, dev bool) (http.Handler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("root %q: %w", root, err)
	}
	s := &server{root: resolved, idx: &findIndex{}, tarStore: newTarIndexStore(), ldbCache: map[string]*ldbEntry{}, sqCache: map[string]*sqEntry{}}

	dist, err := fs.Sub(embedFS, "frontend/dist.gz")
	if err != nil {
		return nil, err
	}
	s.dist = dist
	index, err := fs.ReadFile(dist, "index.html.gz")
	if err != nil {
		return nil, err
	}
	s.index = index

	mux := http.NewServeMux()
	// 列表/文件内容按 Accept-Encoding 协商压缩(list 的大 JSON、file/raw
	// 的文本内容都受益);download 自带编码协商;ws 是 WebSocket 升级,
	// 压缩由 WS 协议层负责,不能经 HTTP 编码中间件(它需要 Hijacker)。
	mux.Handle("GET /api/list", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleList))))
	mux.Handle("GET /api/file", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleFile))))
	mux.Handle("GET /api/raw/{path...}", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleRaw))))
	mux.Handle("GET /api/download", allowNullOrigin(http.HandlerFunc(s.handleDownload)))
	mux.Handle("GET /api/ws", allowNullOrigin(http.HandlerFunc(s.handleWS)))
	mux.Handle("GET /api/stream", allowNullOrigin(http.HandlerFunc(s.handleStream)))
	// leveldb 控制台:keys/get 是 JSON,走编码协商;dump 是流式 NDJSON
	// 附件(逐行 flush),与 /api/download 一样裸注册,不包 withFrontendEncoding。
	mux.Handle("GET /api/leveldb/keys", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleLevelDBKeys))))
	mux.Handle("GET /api/leveldb/get", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleLevelDBGet))))
	mux.Handle("GET /api/leveldb/dump", allowNullOrigin(http.HandlerFunc(s.handleLevelDBDump)))
	// sqlite 查看器:tables/rows/query 都是 JSON,走编码协商。
	mux.Handle("GET /api/sqlite/tables", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleSQLiteTables))))
	mux.Handle("GET /api/sqlite/rows", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleSQLiteRows))))
	mux.Handle("GET /api/sqlite/query", allowNullOrigin(withFrontendEncoding(http.HandlerFunc(s.handleSQLiteQuery))))
	// export 是流式 CSV/JSONL 附件(逐行 flush),与 leveldb dump 一样裸注册。
	mux.Handle("GET /api/sqlite/export", allowNullOrigin(http.HandlerFunc(s.handleSQLiteExport)))
	// preflight for null-origin CORS requests: the GET routes above do not
	// match OPTIONS, so answer them here without reaching a handler
	mux.HandleFunc("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "null" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.Handle("GET /{$}", withFrontendEncoding(http.HandlerFunc(s.handleIndex)))
	mux.Handle("GET /", withFrontendEncoding(http.HandlerFunc(s.handleSPA)))
	// 最外层包访问日志:任何路由(含 404/405)都记一行,耗时覆盖到
	// handler 完全返回为止(WebSocket 即整个会话)。
	return accessLog(mux), nil
}

// allowNullOrigin adds permissive CORS headers only when the request's
// Origin is the literal "null" — the signature of a sandboxed iframe or a
// file:// page. The HTML preview runs in an opaque-origin sandboxed iframe,
// where module scripts and fetches are always CORS-mode and would otherwise
// be blocked. Same-origin requests (no Origin header or a real origin) pass
// through untouched, so the app's own browsing stays exactly as before.
func allowNullOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "null" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// contains reports whether p is root itself or lives under it.
func (s *server) contains(p string) bool {
	return p == s.root || strings.HasPrefix(p, s.root+string(filepath.Separator))
}

// location is the result of resolveVirtual: either a real filesystem path
// (chain empty) or an already-opened chain of zip/tar archives with the
// member path inside the deepest one (inside, "" = that archive's root).
// keys[i] is chain[i]'s source identifier — the host absolute path for the
// outer archive, or "parentKey/memberPath" for nested ones (cache/log key);
// members[i] is the member path used to open chain[i] inside chain[i-1]
// (empty for the outer archive). The caller must close() the location.
type location struct {
	hostPath string
	keys     []string
	members  []string
	chain    []archive
	inside   string
}

// inner 返回最深一层归档。
func (l *location) inner() archive { return l.chain[len(l.chain)-1] }

// close 逆序关闭归档链(内层先关:内层 zip 的底层可能是外层成员流)。
func (l *location) close() {
	for i := len(l.chain) - 1; i >= 0; i-- {
		l.chain[i].close()
	}
	l.chain, l.keys, l.members = nil, nil, nil
}

// isArchivePath reports whether p names a zip or tar file by extension. The
// extension is the user's intent signal: a file named *.zip/*.tar is meant to
// be browsed as a directory, even if its content is malformed (which then
// surfaces as a readable-archive error rather than a silent download).
func isArchivePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".zip", ".tar":
		return true
	}
	return false
}

// errNotReadableArchive 是打开 zip/tar 失败(坏归档,或嵌套段不是有效
// 归档)时的哨兵错误,映射为 404 "not a readable archive"。
var errNotReadableArchive = errors.New("not a readable archive")

// resolveVirtual maps a root-relative path ("" or "/" meaning root) to a
// location. Real path segments are resolved with the same containment rules
// as before; the first segment that names a zip/tar file becomes the outer
// archive boundary and the remaining segments address members inside it —
// recursively, so a zip/tar member naming a zip/tar file opens as the next
// layer (arbitrary nesting depth).
//
// Defense in depth (per component):
//  1. String level: ".." elements and absolute paths (other than "/") are
//     refused outright — they are never legitimate root-relative paths.
//  2. EvalSymlinks follows symlinks, so each component's final target is
//     checked.
//  3. The resolved target must equal root or live under root+"/".
//  4. Inside an archive there is no filesystem access at all: member paths
//     are matched by name against the archive's own entries only.
func (s *server) resolveVirtual(p string) (loc location, err error) {
	// 出错时释放已打开的归档链。
	defer func() {
		if err != nil {
			loc.close()
		}
	}()

	if p == "" {
		p = "/"
	}
	if p != "/" {
		if strings.HasPrefix(p, "/") {
			return location{}, errOutsideRoot
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return location{}, errOutsideRoot
			}
		}
	}
	clean := path.Clean("/" + p)
	segs := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	host := s.root
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		candidate := filepath.Join(host, filepath.FromSlash(seg))
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return location{}, err
		}
		if !s.contains(resolved) {
			return location{}, errOutsideRoot
		}
		st, err := os.Stat(resolved)
		if err != nil {
			return location{}, err
		}
		if st.IsDir() {
			host = resolved
			continue
		}
		if isArchivePath(resolved) {
			f, err := os.Open(resolved)
			if err != nil {
				return location{}, err
			}
			a, err := s.openArchive(&archiveSource{
				key:     resolved,
				size:    st.Size(),
				modTime: st.ModTime(),
				ra:      f,
				closer:  f,
			})
			if err != nil {
				f.Close()
				return location{}, errNotReadableArchive
			}
			loc.hostPath = resolved
			loc.keys = []string{resolved}
			loc.chain = []archive{a}
			if err := s.resolveArchivePath(&loc, segs[i+1:]); err != nil {
				return location{}, err
			}
			return loc, nil
		}
		if i+1 < len(segs) {
			// a non-archive file cannot be a directory in the path
			return location{}, errOutsideRoot
		}
		return location{hostPath: resolved}, nil
	}
	return location{hostPath: host}, nil
}

// resolveArchivePath 在已打开的归档链上消费剩余路径段:某段命中 zip/tar
// 成员就把该成员打开为下一层归档(支持任意深度嵌套),其余段留作最终
// inside。成员按扩展名判定,与宿主文件的意图信号一致;成员名只与归档
// 自身条目匹配,不做任何文件系统访问。
func (s *server) resolveArchivePath(loc *location, segs []string) error {
	inside := ""
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		next := seg
		if inside != "" {
			next = inside + "/" + seg
		}
		cur := loc.chain[len(loc.chain)-1]
		if isArchivePath(seg) {
			if e, ok := cur.stat(next); ok {
				rc, err := cur.open(next)
				if err != nil {
					return err
				}
				// 所有成员读取器(内存 bytes.Reader、磁盘缓存文件、tar
				// 的 SectionReader)都实现 ReaderAt;断言保证类型安全。
				rar, ok := rc.(io.ReaderAt)
				if !ok {
					rc.Close()
					return fmt.Errorf("member not random-accessible: %s", next)
				}
				key := loc.keys[len(loc.keys)-1] + "/" + next
				a, err := s.openArchive(&archiveSource{
					key:     key,
					size:    e.Size,
					modTime: e.ModTime,
					ra:      rar,
					closer:  rc,
				})
				if err != nil {
					rc.Close()
					return errNotReadableArchive
				}
				loc.chain = append(loc.chain, a)
				loc.keys = append(loc.keys, key)
				loc.members = append(loc.members, next)
				inside = "" // 进入新一层,后续段在新层内解析
				continue
			}
		}
		inside = next
	}
	loc.inside = inside
	return nil
}

// cleanURLPath returns the URL-style path (leading "/") for the response JSON.
func cleanURLPath(r *http.Request) string {
	p := r.URL.Query().Get("path")
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	loc, err := s.resolveVirtual(r.URL.Query().Get("path"))
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	defer loc.close()
	cp := cleanURLPath(r)

	if len(loc.chain) == 0 {
		st, err := os.Stat(loc.hostPath)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !st.IsDir() {
			name := st.Name()
			size := st.Size()
			mime := sniffMime(loc.hostPath)
			// .gz 对前端透明:内层 MIME + 解压后大小,前端按内层类型分派。
			if isGzPath(loc.hostPath) {
				if gm, gs, ok := gzInfo(loc.hostPath); ok {
					mime, size = gm, gs
				}
			}
			writeJSON(w, http.StatusOK, listResponse{
				Path:  cp,
				IsDir: false,
				File:  &fileEntry{Name: name, Size: size, ModTime: st.ModTime(), IsDir: false, Mime: mime},
			})
			return
		}
		dirEntries, err := os.ReadDir(loc.hostPath)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		// 目录列表不嗅探文件 MIME:每个文件一次 open+read 在 HDD 大目录下是
		// 灾难(上万次磁盘寻道)。列表只需要 size/modTime/isDir;MIME 在点开
		// 文件预览时由上面的单文件分支内容嗅探,仍以内容为准。
		//
		// 分页(limit/offset):排序只读 DirEntry(d_type 免 stat),stat 只对
		// 页内条目做。大目录下首页从"stat 全部条目"降到"stat 一页",HDD
		// 场景这是数量级差异;前端虚拟滚动按需拉页。
		//
		// zip/tar 文件视为目录:先为每个条目一次性判定 isDir(含归档),再
		// 排序,避免在排序比较器里反复做路径拼接。
		type view struct {
			de        os.DirEntry
			isDir     bool
			isArchive bool
		}
		views := make([]view, len(dirEntries))
		for i, de := range dirEntries {
			isDir := de.IsDir()
			isArchive := false
			if !isDir {
				isArchive = isArchivePath(filepath.Join(loc.hostPath, de.Name()))
				isDir = isArchive
			}
			views[i] = view{de: de, isDir: isDir, isArchive: isArchive}
		}
		sort.Slice(views, func(i, j int) bool {
			if views[i].isDir != views[j].isDir {
				return views[i].isDir // directories (and archives) first
			}
			return views[i].de.Name() < views[j].de.Name() // byte-wise ascending
		})
		// images=1:只返回图片文件名列表(排序序),不做分页、不 stat——
		// 切换上一张/下一张只需要名字,避免大目录下为整目录条目取 stat。
		if r.URL.Query().Get("images") == "1" {
			imgs := make([]string, 0, len(views)/4)
			for _, v := range views {
				if !v.isDir && isImageName(v.de.Name()) {
					imgs = append(imgs, v.de.Name())
				}
			}
			writeJSON(w, http.StatusOK, listResponse{Path: cp, IsDir: true, Images: imgs})
			return
		}
		total := len(views)
		offset, limit := listRange(r)
		if offset > total {
			offset = total
		}
		end := total
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}
		entries := make([]fileEntry, 0, end-offset)
		for _, v := range views[offset:end] {
			info, err := v.de.Info()
			if err != nil {
				continue // unreadable entry: skip rather than fail the listing
			}
			entries = append(entries, fileEntry{
				Name:      v.de.Name(),
				Size:      info.Size(),
				ModTime:   info.ModTime(),
				IsDir:     v.isDir,
				IsArchive: v.isArchive,
			})
		}
		writeJSON(w, http.StatusOK, listResponse{
			Path: cp, IsDir: true, Total: total, Offset: offset, Entries: entries,
			LevelDB: isLevelDBEntries(dirEntries),
		})
		return
	}

	// 归档内部路径:chain 最内层是所在归档,inside 是其中的成员路径
	// (可能为空 = 该归档根)。链已在 resolveVirtual 打开。
	a := loc.inner()
	if e, ok := a.stat(loc.inside); ok && !e.IsDir {
		// 归档内单文件:嗅探成员内容定 MIME(与普通单文件分支一致)。
		// 只读头部,绝不为了 MIME 解压整个成员(GB 级成员会解压到磁盘缓存)。
		e.Mime = a.sniff(loc.inside)
		writeJSON(w, http.StatusOK, listResponse{Path: cp, IsDir: false, File: &e})
		return
	}

	entries, err := a.list(loc.inside)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if r.URL.Query().Get("images") == "1" {
		imgs := make([]string, 0, len(entries)/4)
		for _, e := range entries {
			if !e.IsDir && isImageName(e.Name) {
				imgs = append(imgs, e.Name)
			}
		}
		writeJSON(w, http.StatusOK, listResponse{Path: cp, IsDir: true, Images: imgs})
		return
	}
	total := len(entries)
	offset, limit := listRange(r)
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	writeJSON(w, http.StatusOK, listResponse{
		Path: cp, IsDir: true, Total: total, Offset: offset, Entries: entries[offset:end],
	})
}

// listRange parses the optional offset/limit query parameters for directory
// listing pagination. limit<=0 means "no limit" (full listing), preserving
// the pre-pagination behavior for callers that do not opt in.
func listRange(r *http.Request) (offset, limit int) {
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	return offset, limit
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	loc, err := s.resolveVirtual(r.URL.Query().Get("path"))
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	defer loc.close()
	if len(loc.chain) > 0 {
		s.serveMember(w, r, loc, false)
		return
	}
	f, err := os.Open(loc.hostPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if st.IsDir() {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	// .gz 文件透明解压后返回(小文件全文 fetch);普通文件走 ServeContent。
	if isGzPath(loc.hostPath) {
		serveGz(w, r, f, st)
		return
	}
	// ServeContent handles Content-Type, Range/206 (video seeking) and
	// If-Modified-Since.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// handleRaw streams a file at its URL-mirrored path, so a document's
// relative resources resolve against its own directory:
// /api/raw/dir/page.html renders ./img.png as /api/raw/dir/img.png.
// The path is rooted at the served root with the same containment rules as
// /api/file; directories are refused.
func (s *server) handleRaw(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	loc, err := s.resolveVirtual(strings.TrimPrefix(r.URL.Path, "/api/raw/"))
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	defer loc.close()
	if len(loc.chain) > 0 {
		s.serveMember(w, r, loc, true)
		return
	}
	f, err := os.Open(loc.hostPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if st.IsDir() {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	// The extension pins the MIME type when known: content sniffing cannot
	// tell a .js from a .txt, and module scripts refuse to run unless the
	// response is a JavaScript MIME type. Unknown extensions fall through
	// to ServeContent's own sniffing.
	if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(st.Name()))); mt != "" {
		w.Header().Set("Content-Type", mt)
	}
	// ServeContent handles Content-Type (when unset), Range/206 (video
	// seeking) and If-Modified-Since.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// handleDownload streams a download: a directory becomes a zip archive
// written entry-by-entry (the client receives data before packing finishes),
// a single file is streamed with the best content-coding the client accepts
// (brotli, then gzip) when its content is compressible, verbatim otherwise.
func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	loc, err := s.resolveVirtual(r.URL.Query().Get("path"))
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	defer loc.close()
	if len(loc.chain) > 0 {
		s.downloadMember(w, r, loc)
		return
	}
	st, err := os.Stat(loc.hostPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if st.IsDir() {
		s.streamZip(w, loc.hostPath, st.Name())
		return
	}
	s.streamFile(w, r, loc.hostPath, st)
}

// serveMember streams one file member of a zip/tar archive through
// ServeContent, so Range/206 (video seeking) works exactly as for real files.
// pinExt mirrors handleRaw's behavior of pinning the MIME type by extension
// (content sniffing cannot tell .js from .txt, and module scripts need a
// JavaScript MIME type to run).
func (s *server) serveMember(w http.ResponseWriter, r *http.Request, loc location, pinExt bool) {
	a := loc.inner()
	if loc.inside == "" {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	e, ok := a.stat(loc.inside)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if e.IsDir {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	rc, err := a.open(loc.inside)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer rc.Close()
	if pinExt {
		if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(e.Name))); mt != "" {
			w.Header().Set("Content-Type", mt)
		}
	}
	http.ServeContent(w, r, e.Name, e.ModTime, rc)
}

// downloadMember streams a download for an archive path: the archive file
// itself goes out verbatim (its own bytes, not a re-zip) for the outer
// archive, a nested archive member goes out as its decompressed bytes (which
// form a complete archive file), and a regular member is sent as an
// attachment.
func (s *server) downloadMember(w http.ResponseWriter, r *http.Request, loc location) {
	if loc.inside != "" {
		a := loc.inner()
		e, ok := a.stat(loc.inside)
		if !ok {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if e.IsDir {
			writeErr(w, http.StatusBadRequest, "is a directory")
			return
		}
		rc, err := a.open(loc.inside)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.Name}))
		w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
		io.Copy(w, rc)
		return
	}
	// inside 为空:下载"当前位置代表的那个归档文件"。
	if len(loc.chain) == 1 {
		// 最外层归档文件本身:原样下发。
		st, err := os.Stat(loc.hostPath)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		s.streamFile(w, r, loc.hostPath, st)
		return
	}
	// 嵌套归档:下载父层中的该归档成员(解压后字节即完整归档文件)。
	parent := loc.chain[len(loc.chain)-2]
	member := loc.members[len(loc.members)-1]
	e, ok := parent.stat(member)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	rc, err := parent.open(member)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(member)}))
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	io.Copy(w, rc)
}

// streamFile sends one file as an attachment. Compressible content goes out
// with the content-coding negotiated from Accept-Encoding — brotli when the
// client accepts it (best compression ratio), gzip otherwise — and no
// Content-Length (chunked); already-compressed payloads are copied verbatim
// with a known size. Vary: Accept-Encoding is set on every compressible
// response so caches key on the negotiated coding.
func (s *server) streamFile(w http.ResponseWriter, r *http.Request, resolved string, st fs.FileInfo) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": st.Name()}))
	f, err := os.Open(resolved)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	if !compressible(sniffMimeFrom(f)) {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		io.Copy(w, f)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// flusher is the common surface of gzip.Writer and brotli.Writer.
	type flusher interface {
		io.WriteCloser
		Flush() error
	}
	w.Header().Set("Vary", "Accept-Encoding")
	var codec flusher
	switch enc := preferredEncoding(r.Header.Get("Accept-Encoding")); enc {
	case "br":
		w.Header().Set("Content-Encoding", "br")
		codec = brotli.NewWriterLevel(w, brotli.BestCompression)
	case "gzip":
		w.Header().Set("Content-Encoding", "gzip")
		zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		codec = zw
	default: // identity: the client did not ask for a coding, send verbatim
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		io.Copy(w, f)
		return
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			codec.Write(buf[:n])
			if rerr == nil { // keep the download progressive on large files
				codec.Flush()
				rc.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	codec.Close()
	rc.Flush()
}

// streamZip archives a directory into a zip written straight to the response.
// Entries are emitted as they are walked and flushed per entry, so the client
// starts receiving bytes immediately instead of waiting for the archive to be
// fully built. Compressible content is deflated, already-compressed payloads
// stored verbatim. Symlinks are followed only when they resolve to a regular
// file inside root; directory symlinks are skipped (cycle risk) and symlinks
// escaping root are dropped. Failures mid-stream abort the response (the
// status is already committed, so the client just sees a truncated archive).
func (s *server) streamZip(w http.ResponseWriter, resolved, name string) {
	// 目录打包下载:大目录遍历 + 逐文件压缩,服务端开销与目录规模成正比;
	// 超阈值时记下打包规模,与 access log 里的响应耗时互相印证。
	t0 := time.Now()
	var zipped int64
	defer func() {
		if d := time.Since(t0); d >= slowThreshold {
			log.Printf("[slow] zip-download dir=%s entries=%d took=%s", resolved, zipped, d.Round(time.Millisecond))
		}
	}()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))

	bufw := bufio.NewWriterSize(w, 64*1024)
	zw := zip.NewWriter(bufw)
	rc := http.NewResponseController(w)
	flush := func() {
		bufw.Flush()
		rc.Flush()
	}
	// a top-level folder named after the downloaded directory keeps the
	// archive self-contained (extracting yields one folder, also for the
	// empty-dir case)
	if _, err := zw.Create(name + "/"); err != nil {
		return
	}
	flush()
	abort := func(err error) {
		zw.Close()
		bufw.Flush()
		rc.Flush()
	}
	err := filepath.WalkDir(resolved, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // unreadable entry: skip rather than abort the archive
		}
		rel, err := filepath.Rel(resolved, p)
		if err != nil || rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		zipped++
		zpath := name + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			_, err := zw.Create(zpath + "/")
			if err == nil {
				flush()
			}
			return err
		}
		target := p
		if info.Mode()&fs.ModeSymlink != 0 {
			rp, err := filepath.EvalSymlinks(p)
			if err != nil || !s.contains(rp) {
				return nil // broken or escaping symlink: drop it
			}
			ti, err := os.Stat(rp)
			if err != nil || ti.IsDir() {
				return nil // directory symlink: skip (cycle risk)
			}
			target, info = rp, ti
		}
		f, err := os.Open(target)
		if err != nil {
			return nil
		}
		method := zip.Deflate
		if !compressible(sniffMimeFrom(f)) {
			method = zip.Store
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil
		}
		hdr := &zip.FileHeader{Name: zpath, Method: method, Modified: info.ModTime()}
		hdr.SetMode(info.Mode())
		hd, err := zw.CreateHeader(hdr)
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(hd, f)
		f.Close()
		if err != nil {
			return err
		}
		flush()
		return nil
	})
	if err != nil {
		abort(err)
		return
	}
	zw.Close()
	bufw.Flush()
	rc.Flush()
}

// compressible reports whether content of mime type mt gains from lossless
// compression (text, markup, structured data). Already-compressed payloads
// (images, video, archives) are sent and stored as-is. Parameters (e.g.
// "application/json; charset=utf-8") are stripped before the match.
func compressible(mt string) bool {
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch mt {
	case "application/json", "application/xml", "application/javascript",
		"application/x-javascript", "application/wasm", "image/svg+xml":
		return true
	}
	return false
}

// parseAcceptEncoding 解析 Accept-Encoding,返回 br/gzip/"*" 三者的 q 值
// (-1 = 未提及),供 preferredEncoding 与 acceptsGzip 共用。
func parseAcceptEncoding(ae string) (br, gz, star float64) {
	br, gz, star = -1.0, -1.0, -1.0
	for _, part := range strings.Split(ae, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, q := part, 1.0
		if i := strings.IndexByte(part, ';'); i >= 0 {
			name = strings.TrimSpace(part[:i])
			if qv, ok := parseQ(strings.TrimSpace(part[i+1:])); ok {
				q = qv
			}
		}
		switch name {
		case "br":
			br = q
		case "gzip":
			gz = q
		case "*":
			star = q
		}
	}
	return
}

// preferredEncoding returns the best content-coding the client accepts per
// Accept-Encoding (RFC 9110 §12.5.3): the acceptable coding with the highest
// q-value wins; ties go to brotli, which yields the better compression ratio
// than gzip; "" means identity. An explicit q=0 refusal beats a "*"
// wildcard for that coding.
func preferredEncoding(ae string) string {
	br, gz, star := parseAcceptEncoding(ae)
	// acceptable: explicitly listed with q>0, or covered by "*" with q>0
	acceptable := func(q, star float64) bool {
		if q >= 0 {
			return q > 0
		}
		return star > 0
	}
	// effective q-value: the explicit entry, else the wildcard's
	effQ := func(q, star float64) float64 {
		if q >= 0 {
			return q
		}
		return star
	}
	brOK, gzOK := acceptable(br, star), acceptable(gz, star)
	if !brOK && !gzOK {
		return "" // identity; there is no better coding the client takes
	}
	brQ, gzQ := effQ(br, star), effQ(gz, star)
	if gzQ > brQ {
		return "gzip" // the client prefers gzip explicitly
	}
	return "br"
}

// acceptsGzip 报告客户端 Accept-Encoding 是否接受 gzip(显式列出 q>0,或由
// "*" 通配覆盖;显式 q=0 拒绝优先)。预 gzip 资源据此决定直出压缩字节还是
// 解压后以 identity 发送。
func acceptsGzip(ae string) bool {
	_, gz, star := parseAcceptEncoding(ae)
	if gz >= 0 {
		return gz > 0
	}
	return star > 0
}

// parseQ parses a "q=0.8" quality value (default 1 when absent).
func parseQ(s string) (float64, bool) {
	const prefix = "q="
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	q, err := strconv.ParseFloat(strings.TrimSpace(s[len(prefix):]), 64)
	if err != nil || q < 0 || q > 1 {
		return 0, false
	}
	return q, true
}

// withFrontendEncoding negotiates Content-Encoding for the frontend routes:
// compressible responses are served brotli-encoded when the client accepts
// it (best ratio), gzip otherwise; the coding is decided at WriteHeader time
// from the handler's Content-Type, so errors and non-compressible payloads
// pass through untouched. Range requests are never compressed (a compressed
// body cannot be ranged). Vary: Accept-Encoding is added to every
// compressible response, encoded or not, so caches key on the coding.
func withFrontendEncoding(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		ew := &encodingWriter{ResponseWriter: w, ae: r.Header.Get("Accept-Encoding")}
		next.ServeHTTP(ew, r)
		ew.Close()
	})
}

// encodingWriter compresses a response body at WriteHeader time when the
// handler produced a 200 with compressible Content-Type and the client
// accepts a coding; otherwise it is a transparent passthrough.
type encodingWriter struct {
	http.ResponseWriter
	ae    string // client's Accept-Encoding
	codec interface {
		io.WriteCloser
		Flush() error
	}
	started bool
}

func (e *encodingWriter) WriteHeader(code int) {
	if e.started {
		return
	}
	e.started = true
	h := e.ResponseWriter.Header()
	if compressible(h.Get("Content-Type")) {
		h.Add("Vary", "Accept-Encoding")
	}
	if code != http.StatusOK || h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		e.ResponseWriter.WriteHeader(code)
		return
	}
	// br 用默认级别而非 BestCompression:大 JSON 列表(数 MB)在最高级别
	// 下压缩要数秒 CPU,默认级别(6)压缩比接近而速度与 gzip 相当。
	switch enc := preferredEncoding(e.ae); enc {
	case "br":
		h.Set("Content-Encoding", "br")
		e.codec = brotli.NewWriterLevel(e.ResponseWriter, brotli.DefaultCompression)
	case "gzip":
		h.Set("Content-Encoding", "gzip")
		e.codec, _ = gzip.NewWriterLevel(e.ResponseWriter, gzip.BestCompression)
	default:
		e.ResponseWriter.WriteHeader(code)
		return
	}
	h.Del("Content-Length") // encoded size is unknown until the stream ends
	e.ResponseWriter.WriteHeader(code)
}

func (e *encodingWriter) Write(p []byte) (int, error) {
	if !e.started {
		e.WriteHeader(http.StatusOK)
	}
	if e.codec == nil {
		return e.ResponseWriter.Write(p)
	}
	return e.codec.Write(p)
}

func (e *encodingWriter) Flush() {
	if e.codec != nil {
		e.codec.Flush()
	}
	if f, ok := e.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close finishes the codec stream; the handler's body must already be
// complete (the middleware calls it after next.ServeHTTP returns).
func (e *encodingWriter) Close() {
	if e.codec != nil {
		e.codec.Close()
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// index.html 引用内容哈希资源,必须每次回源校验,缓存 no-cache
	w.Header().Set("Cache-Control", "no-cache")
	s.serveIndexBody(w, r)
}

func (s *server) handleSPA(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if _, err := fs.Stat(s.dist, name); err == nil {
		// 非可压缩资源(字体/图片)原样直出,Vite 内容哈希名可长期缓存
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.FileServerFS(s.dist).ServeHTTP(w, r)
		return
	}
	if f, err := s.dist.Open(name + ".gz"); err == nil {
		// 预 gzip 资源(JS/CSS/HTML 等):按客户端接受度直出或解压
		if st, serr := f.Stat(); serr == nil {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			servePreGz(w, r, f, int(st.Size()), assetContentType(name))
			f.Close()
			return
		}
		f.Close()
	}
	// SPA 回退:任意路径都返回 index.html
	w.Header().Set("Cache-Control", "no-cache")
	s.serveIndexBody(w, r)
}

// serveIndexBody 下发预 gzip 的 index.html(按客户端接受度直出或解压)。
func (s *server) serveIndexBody(w http.ResponseWriter, r *http.Request) {
	servePreGz(w, r, bytes.NewReader(s.index), len(s.index), "text/html; charset=utf-8")
}

// servePreGz 下发一份预 gzip 内容(gz 为压缩字节流,gzLen 为其长度,
// contentType 为内层 MIME)。客户端接受 gzip 时原样直出压缩字节
// (Content-Encoding: gzip,零运行时压缩);否则解压后以 identity 发送——此时
// withFrontendEncoding 中间件会按 Accept-Encoding 决定是否再压缩成 br。
// 两类响应均经中间件补 Vary。
func servePreGz(w http.ResponseWriter, r *http.Request, gz io.Reader, gzLen int, contentType string) {
	w.Header().Set("Content-Type", contentType)
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(gzLen))
		w.WriteHeader(http.StatusOK)
		io.Copy(w, gz)
		return
	}
	zr, err := gzip.NewReader(gz)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer zr.Close()
	w.WriteHeader(http.StatusOK)
	io.Copy(w, zr)
}

// assetContentType 返回预 gzip 资源的内层 MIME,取原始文件名扩展名(而非
// .gz 后缀),与 http.FileServerFS 对同名原始文件的判定一致。
func assetContentType(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// sniffMime reads the head of the file and returns a content-based MIME type
// (type/subtype only, no parameters). Content is the source of truth: a file
// named .png holding text comes back as text/plain. Custom signatures fill
// the gaps in http.DetectContentType's table (svg, webm/mkv, avif, ico, tiff).
func sniffMime(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return sniffMimeFrom(f)
}

// sniffMimeFrom sniffs the head of r (caller keeps ownership). The reader is
// left positioned after the sniffed head, so a seekable source can rewind
// with Seek(0, io.SeekStart) before streaming the full content.
func sniffMimeFrom(r io.Reader) string {
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return ""
	}
	head = head[:n]
	if len(head) == 0 {
		return "text/plain"
	}
	switch {
	case isSVG(head):
		return "image/svg+xml"
	case looksLikeJSON(head):
		return "application/json"
	case len(head) >= 4 && head[0] == 0x1a && head[1] == 0x45 && head[2] == 0xdf && head[3] == 0xa3:
		return "video/webm" // EBML: webm/mkv container
	case len(head) >= 16 && string(head[:15]) == "SQLite format 3" && head[15] == 0:
		return "application/vnd.sqlite3" // sqlite 数据库文件
	case len(head) >= 12 && string(head[4:8]) == "ftyp" && string(head[8:12]) == "avif":
		return "image/avif"
	case len(head) >= 4 && head[0] == 0 && head[1] == 0 && head[2] == 1 && head[3] == 0:
		return "image/x-icon"
		// TIFF 签名("II*\0" 小端 / "MM\0*" 大端)不在 DetectContentType 的表里
	case len(head) >= 4 &&
		((head[0] == 0x49 && head[1] == 0x49 && head[2] == 0x2a && head[3] == 0x00) ||
			(head[0] == 0x4d && head[1] == 0x4d && head[2] == 0x00 && head[3] == 0x2a)):
		return "image/tiff"
	}
	mt := http.DetectContentType(head)
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	return strings.TrimSpace(mt)
}

// looksLikeJSON reports whether the head starts with a JSON value. A loose
// check on the first non-space byte is enough: valid JSON documents begin
// with '{' or '[', and a truncated 512-byte prefix cannot be validated.
func looksLikeJSON(head []byte) bool {
	s := strings.TrimSpace(string(head))
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

// isSVG reports whether the head looks like an SVG document (the svg root
// element appears near the start, possibly after an xml declaration).
func isSVG(head []byte) bool {
	s := strings.TrimPrefix(string(head), "\ufeff")
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<svg") {
		return true
	}
	return strings.HasPrefix(s, "<?xml") && strings.Contains(s, "<svg")
}

func mapResolveErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutsideRoot):
		writeErr(w, http.StatusBadRequest, "path outside root")
	case errors.Is(err, errNotReadableArchive):
		writeErr(w, http.StatusNotFound, "not a readable archive")
	case os.IsNotExist(err):
		writeErr(w, http.StatusNotFound, "not found")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
