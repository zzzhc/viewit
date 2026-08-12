package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errOutsideRoot is the sentinel for any resolved path that escapes root.
var errOutsideRoot = errors.New("path outside root")

type server struct {
	root  string // canonicalized, symlink-free root directory
	dist  fs.FS  // embedded frontend/dist
	index []byte
	idx   *findIndex // fuzzy-find index, built lazily on first WS connection
}

type fileEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	IsDir   bool      `json:"isDir"`
	Mime    string    `json:"mime,omitempty"` // content-sniffed, type/subtype only
}

type listResponse struct {
	Path    string      `json:"path"`
	IsDir   bool        `json:"isDir"`
	Entries []fileEntry `json:"entries,omitempty"`
	File    *fileEntry  `json:"file,omitempty"`
}

//	newHandler canonicalizes root (Abs + EvalSymlinks) and wires the routes:
//
//	GET /api/list, GET /api/file, GET /api/download, GET /api/ws (fuzzy find)
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
	s := &server{root: resolved, idx: &findIndex{}}

	dist, err := fs.Sub(embedFS, "frontend/dist")
	if err != nil {
		return nil, err
	}
	s.dist = dist
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err
	}
	s.index = index

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/list", s.handleList)
	mux.HandleFunc("GET /api/file", s.handleFile)
	mux.HandleFunc("GET /api/download", s.handleDownload)
	mux.HandleFunc("GET /api/ws", s.handleWS)
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /", s.handleSPA)
	return mux, nil
}

// contains reports whether p is root itself or lives under it.
func (s *server) contains(p string) bool {
	return p == s.root || strings.HasPrefix(p, s.root+string(filepath.Separator))
}

// resolve maps the ?path= query parameter to an absolute path inside root.
//
// Defense in depth:
//  1. String level: ".." elements and absolute paths (other than "/") are
//     refused outright — they are never legitimate root-relative paths.
//  2. EvalSymlinks follows symlinks, so the final target is checked.
//  3. The resolved target must equal root or live under root+"/".
func (s *server) resolve(r *http.Request) (string, error) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	if p != "/" {
		if strings.HasPrefix(p, "/") {
			return "", errOutsideRoot
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return "", errOutsideRoot
			}
		}
	}
	clean := path.Clean("/" + p)
	host := filepath.Join(s.root, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(host)
	if err != nil {
		return "", err
	}
	if !s.contains(resolved) {
		return "", errOutsideRoot
	}
	return resolved, nil
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
	resolved, err := s.resolve(r)
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	st, err := os.Stat(resolved)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	cp := cleanURLPath(r)
	if !st.IsDir() {
		writeJSON(w, http.StatusOK, listResponse{
			Path:  cp,
			IsDir: false,
			File: &fileEntry{
				Name: st.Name(), Size: st.Size(), ModTime: st.ModTime(), IsDir: false,
				Mime: sniffMime(resolved),
			},
		})
		return
	}
	dirEntries, err := os.ReadDir(resolved)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	entries := make([]fileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			continue // unreadable entry: skip rather than fail the listing
		}
		fe := fileEntry{
			Name:    de.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		}
		if !info.IsDir() {
			fe.Mime = sniffMime(filepath.Join(resolved, de.Name()))
		}
		entries = append(entries, fe)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // directories first
		}
		return entries[i].Name < entries[j].Name // byte-wise ascending
	})
	writeJSON(w, http.StatusOK, listResponse{Path: cp, IsDir: true, Entries: entries})
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	resolved, err := s.resolve(r)
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	f, err := os.Open(resolved)
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
	// ServeContent handles Content-Type, Range/206 (video seeking) and
	// If-Modified-Since.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// handleDownload streams a download: a directory becomes a zip archive
// written entry-by-entry (the client receives data before packing finishes),
// a single file is streamed with gzip at the highest compression level when
// its content is compressible, verbatim otherwise.
func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	resolved, err := s.resolve(r)
	if err != nil {
		mapResolveErr(w, err)
		return
	}
	st, err := os.Stat(resolved)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if st.IsDir() {
		s.streamZip(w, resolved, st.Name())
		return
	}
	s.streamFile(w, resolved, st)
}

// streamFile sends one file as an attachment. Compressible content goes out
// gzip-encoded with gzip.BestCompression and no Content-Length (chunked);
// already-compressed payloads are copied verbatim with a known size.
func (s *server) streamFile(w http.ResponseWriter, resolved string, st fs.FileInfo) {
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
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Vary", "Accept-Encoding")
	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			zw.Write(buf[:n])
			if rerr == nil { // keep the download progressive on large files
				zw.Flush()
				rc.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	zw.Close()
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
// (images, video, archives) are sent and stored as-is.
func compressible(mt string) bool {
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

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(s.index))
}

func (s *server) handleSPA(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if _, err := fs.Stat(s.dist, name); err == nil {
		http.FileServerFS(s.dist).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(s.index)
}

// sniffMime reads the head of the file and returns a content-based MIME type
// (type/subtype only, no parameters). Content is the source of truth: a file
// named .png holding text comes back as text/plain. Custom signatures fill
// the gaps in http.DetectContentType's table (svg, webm/mkv, avif, ico).
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
	case len(head) >= 12 && string(head[4:8]) == "ftyp" && string(head[8:12]) == "avif":
		return "image/avif"
	case len(head) >= 4 && head[0] == 0 && head[1] == 0 && head[2] == 1 && head[3] == 0:
		return "image/x-icon"
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
