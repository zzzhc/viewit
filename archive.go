package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// archiveThreshold is the size above which a scanned archive index or an
// extracted member is cached to disk under ~/.viewit instead of being held in
// memory or rescanned on every request.
const archiveThreshold = 16 << 20 // 16 MiB

// scanBufSize buffers the sequential tar scan so archive/tar's 512-byte block
// reads become bulk reads. Without it, a multi-GB tar issues millions of tiny
// read syscalls, which is orders of magnitude slower on any filesystem.
const scanBufSize = 4 << 20 // 4 MiB

// archive is a uniform read-only view over a zip or tar archive, exposing it
// as a virtual directory tree.
type archive interface {
	// list returns the immediate children of dir ("" = archive root), sorted
	// directories-first then by name.
	list(dir string) ([]fileEntry, error)
	// stat reports the entry exactly at name ("" = archive root). ok is false
	// when no such entry exists.
	stat(name string) (fileEntry, bool)
	// open returns a seekable reader over the file member at name.
	open(name string) (io.ReadSeekCloser, error)
	// sniff reads only the head of the file member at name to type it, so a
	// multi-GB member is never extracted just to sniff its MIME.
	sniff(name string) string
	// stream returns a sequential reader over the file member at name that
	// decompresses on the fly. Unlike open, it never materializes the whole
	// member, so the text stream viewer sees the first chunk immediately.
	stream(name string) (io.ReadCloser, error)
	close() error
}

// archiveSource 描述一个归档的字节来源:宿主文件,或上层归档里的一个
// 成员(嵌套)。key 是唯一的来源标识——宿主绝对路径,或 "父key/成员路径"
// ——兼作缓存与日志键;ra 提供对 size 字节的随机访问;closer 负责关闭
// 底层(文件句柄/成员流),可为 nil。
type archiveSource struct {
	key     string
	size    int64
	modTime time.Time
	ra      io.ReaderAt
	closer  io.Closer
}

// openArchive opens a zip or tar by extension. Tar indexes are served from a
// shared, process-wide store so repeated browsing never rescans the archive.
func (s *server) openArchive(src *archiveSource) (archive, error) {
	switch strings.ToLower(filepath.Ext(src.key)) {
	case ".zip":
		return openZip(src)
	case ".tar":
		return s.openTar(src)
	}
	return nil, fmt.Errorf("not an archive: %s", src.key)
}

// cacheDir returns ~/.viewit (falling back to the temp dir when the home
// directory is unavailable).
func cacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return os.TempDir()
	}
	return filepath.Join(home, ".viewit")
}

// cacheFileFor maps a source key to its on-disk cache path:
// ~/.viewit/<sha256(key)>.txt. The key can grow long with nested archives
// (host path + member paths), which would overflow a 255-byte filename
// component, so the digest replaces it entirely.
func cacheFileFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(cacheDir(), hex.EncodeToString(sum[:])+".txt")
}

// writeCacheFile atomically writes data to cachePath (temp file + rename), so
// a partially written cache entry is never mistaken for a complete one.
func writeCacheFile(cachePath string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), cachePath)
}

// sortEntries orders entries directories-first, then byte-wise by name — the
// same ordering the filesystem listing uses.
func sortEntries(entries []fileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
}

// memReadSeekCloser adapts a bytes.Reader into io.ReadSeekCloser.
type memReadSeekCloser struct {
	*bytes.Reader
}

func (m *memReadSeekCloser) Close() error { return nil }

// fileReadSeekCloser exposes an *os.File as io.ReadSeekCloser.
type fileReadSeekCloser struct {
	*os.File
}

// sectionReadSeekCloser exposes an io.SectionReader as io.ReadSeekCloser.
type sectionReadSeekCloser struct {
	*io.SectionReader
}

func (s *sectionReadSeekCloser) Close() error { return nil }

// ---------------------------------------------------------------------------
// zip

type zipArchive struct {
	key    string
	zr     *zip.Reader
	closer io.Closer // 底层来源(宿主文件句柄或上层成员流),可 nil
}

func openZip(src *archiveSource) (*zipArchive, error) {
	t0 := time.Now()
	zr, err := zip.NewReader(src.ra, src.size)
	d := time.Since(t0)
	if err != nil {
		// 打开失败本身就要留痕:用户点开一个坏 zip 时,这是唯一的线索。
		log.Printf("[slow] open-zip path=%s error=%v took=%s", src.key, err, d.Round(time.Millisecond))
		return nil, err
	}
	// 成功但慢:大 zip 读中央目录可能数百 ms,每次浏览都重新打开,重复
	// 的 slow 行能暴露"每次列表都慢"。
	if d >= slowThreshold {
		log.Printf("[slow] open-zip path=%s size=%d took=%s", src.key, src.size, d.Round(time.Millisecond))
	}
	return &zipArchive{key: src.key, zr: zr, closer: src.closer}, nil
}

func (z *zipArchive) close() error {
	if z.closer != nil {
		return z.closer.Close()
	}
	return nil
}

// zipDirs returns every directory path in the archive (explicit entries and
// parents implied by file entries), mapped to their explicit entry when one
// exists (for its mod time), each without a trailing slash.
func zipDirs(files []*zip.File) map[string]*zip.File {
	dirs := make(map[string]*zip.File)
	imply := func(name string) {
		for {
			i := strings.LastIndexByte(name, '/')
			if i < 0 {
				return
			}
			name = name[:i]
			if _, ok := dirs[name]; !ok {
				dirs[name] = nil
			}
		}
	}
	for _, f := range files {
		if name := strings.TrimSuffix(f.Name, "/"); name != "" {
			imply(name)
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name, "/") {
			if name := strings.TrimSuffix(f.Name, "/"); name != "" {
				dirs[name] = f
			}
		}
	}
	return dirs
}

func (z *zipArchive) list(dir string) ([]fileEntry, error) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	dirs := zipDirs(z.zr.File)
	children := make(map[string]fileEntry)
	for name, f := range dirs {
		if dir != "" && (name == dir || !strings.HasPrefix(name, prefix)) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || strings.ContainsRune(rel, '/') {
			continue
		}
		e := fileEntry{Name: rel, IsDir: true}
		if f != nil {
			e.ModTime = f.Modified
		}
		children[rel] = e
	}
	for _, f := range z.zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" {
			continue
		}
		if dir != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || strings.ContainsRune(rel, '/') {
			continue
		}
		e := fileEntry{
			Name:    rel,
			Size:    int64(f.UncompressedSize64),
			ModTime: f.Modified,
			IsDir:   false,
		}
		// zip/tar 成员与宿主上的归档文件同等对待:是虚拟目录,点击进入
		// 归档浏览(嵌套)。
		if isArchivePath(rel) {
			e.IsDir = true
			e.IsArchive = true
		}
		children[rel] = e
	}
	entries := make([]fileEntry, 0, len(children))
	for _, e := range children {
		entries = append(entries, e)
	}
	sortEntries(entries)
	return entries, nil
}

func (z *zipArchive) stat(name string) (fileEntry, bool) {
	if name == "" {
		return fileEntry{IsDir: true}, true
	}
	for _, f := range z.zr.File {
		if strings.TrimSuffix(f.Name, "/") == name {
			e := fileEntry{
				Name:    path.Base(name),
				Size:    int64(f.UncompressedSize64),
				ModTime: f.Modified,
				IsDir:   strings.HasSuffix(f.Name, "/"),
			}
			// zip/tar 成员是虚拟目录(与列表一致):进入即嵌套浏览。
			if !e.IsDir && isArchivePath(name) {
				e.IsDir = true
				e.IsArchive = true
			}
			return e, true
		}
	}
	if _, ok := zipDirs(z.zr.File)[name]; ok {
		return fileEntry{Name: path.Base(name), IsDir: true}, true
	}
	return fileEntry{}, false
}

func (z *zipArchive) open(name string) (io.ReadSeekCloser, error) {
	var f *zip.File
	for _, e := range z.zr.File {
		if strings.TrimSuffix(e.Name, "/") == name {
			f = e
			break
		}
	}
	if f == nil {
		return nil, os.ErrNotExist
	}
	size := int64(f.UncompressedSize64)
	if size <= archiveThreshold {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		buf, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		return &memReadSeekCloser{Reader: bytes.NewReader(buf)}, nil
	}
	// Large member: cache the first extraction to disk so Range/seek reads do
	// not re-decompress from the start every time. The cache is keyed by the
	// member's full source key and validated by uncompressed size.
	cache := cacheFileFor(z.key + "/" + name)
	if st, err := os.Stat(cache); err == nil && st.Size() == size {
		if fh, err := os.Open(cache); err == nil {
			return &fileReadSeekCloser{File: fh}, nil
		}
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".tmp-*")
	if err != nil {
		return nil, err
	}
	// 首次解压大成员(>16MiB)到磁盘缓存:GB 级成员要数秒,是无条件记的
	// 重操作;视频等大文件的第一次 Range 请求会走到这里。
	t0 := time.Now()
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := os.Rename(tmp.Name(), cache); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	d := time.Since(t0)
	log.Printf("[slow] extract-zip path=%s member=%s size=%d took=%s",
		z.key, name, size, d.Round(time.Millisecond))
	fh, err := os.Open(cache)
	if err != nil {
		return nil, err
	}
	return &fileReadSeekCloser{File: fh}, nil
}

func (z *zipArchive) sniff(name string) string {
	for _, e := range z.zr.File {
		if strings.TrimSuffix(e.Name, "/") != name {
			continue
		}
		rc, err := e.Open()
		if err != nil {
			return ""
		}
		defer rc.Close()
		return sniffMimeFrom(rc)
	}
	return ""
}

func (z *zipArchive) stream(name string) (io.ReadCloser, error) {
	for _, e := range z.zr.File {
		if strings.TrimSuffix(e.Name, "/") == name {
			return e.Open()
		}
	}
	return nil, os.ErrNotExist
}

// ---------------------------------------------------------------------------
// tar

// tarEntry is one indexed tar member. Offset is the byte offset of its data
// within the archive; Size is the uncompressed data length. Entries are kept
// sorted by Name for binary-search lookup.
type tarEntry struct {
	Name    string
	Offset  int64
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// tarIndex is an immutable, name-sorted list of tar entries. It is shared
// across requests for the same archive and queried by binary search, so
// listing a deep subdirectory touches only that subtree's entries rather than
// scanning the whole index.
type tarIndex struct {
	entries []tarEntry // sorted by Name ascending
}

func newTarIndex(entries []tarEntry) *tarIndex {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return &tarIndex{entries: entries}
}

// find returns the index of the exact entry name, if present.
func (ix *tarIndex) find(name string) (int, bool) {
	i := sort.Search(len(ix.entries), func(i int) bool { return ix.entries[i].Name >= name })
	if i < len(ix.entries) && ix.entries[i].Name == name {
		return i, true
	}
	return 0, false
}

func (ix *tarIndex) stat(name string) (fileEntry, bool) {
	if name == "" {
		return fileEntry{IsDir: true}, true
	}
	if i, ok := ix.find(name); ok {
		e := ix.entries[i]
		fe := fileEntry{Name: path.Base(name), Size: e.Size, ModTime: e.ModTime, IsDir: e.IsDir}
		// zip/tar 成员是虚拟目录(与列表一致):进入即嵌套浏览。
		if !e.IsDir && isArchivePath(name) {
			fe.IsDir = true
			fe.IsArchive = true
		}
		return fe, true
	}
	// An implied directory has no explicit entry but names a prefix of some
	// deeper entry.
	prefix := name + "/"
	i := sort.Search(len(ix.entries), func(i int) bool { return ix.entries[i].Name >= prefix })
	if i < len(ix.entries) && strings.HasPrefix(ix.entries[i].Name, prefix) {
		return fileEntry{Name: path.Base(name), IsDir: true}, true
	}
	return fileEntry{}, false
}

func (ix *tarIndex) list(dir string) []fileEntry {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	n := len(ix.entries)
	lo := sort.Search(n, func(i int) bool { return ix.entries[i].Name >= prefix })
	hi := lo + sort.Search(n-lo, func(i int) bool {
		return !strings.HasPrefix(ix.entries[lo+i].Name, prefix)
	})
	children := make(map[string]fileEntry)
	for i := lo; i < hi; i++ {
		e := ix.entries[i]
		rel := e.Name[len(prefix):]
		if rel == "" {
			continue
		}
		base := rel
		if j := strings.IndexByte(rel, '/'); j >= 0 {
			base = rel[:j]
		}
		if _, ok := children[base]; ok {
			continue
		}
		if strings.ContainsRune(rel, '/') {
			children[base] = fileEntry{Name: base, IsDir: true} // implied dir
		} else {
			e := fileEntry{Name: base, Size: e.Size, ModTime: e.ModTime, IsDir: e.IsDir}
			// zip/tar 成员是虚拟目录(与 stat 一致):进入即嵌套浏览。
			if !e.IsDir && isArchivePath(base) {
				e.IsDir = true
				e.IsArchive = true
			}
			children[base] = e
		}
	}
	entries := make([]fileEntry, 0, len(children))
	for _, e := range children {
		entries = append(entries, e)
	}
	sortEntries(entries)
	return entries
}

// tarArchive is a per-request handle: the shared index plus an independently
// opened source (host file or an upper-level archive member), used only via
// ReadAt, so it is safe alongside other requests' handles.
type tarArchive struct {
	ra     io.ReaderAt
	closer io.Closer // 底层来源,可 nil
	ix     *tarIndex
}

func (ta *tarArchive) close() error {
	if ta.closer != nil {
		return ta.closer.Close()
	}
	return nil
}
func (ta *tarArchive) list(dir string) ([]fileEntry, error) {
	return ta.ix.list(dir), nil
}
func (ta *tarArchive) stat(name string) (fileEntry, bool) { return ta.ix.stat(name) }
func (ta *tarArchive) open(name string) (io.ReadSeekCloser, error) {
	i, ok := ta.ix.find(name)
	if !ok || ta.ix.entries[i].IsDir {
		return nil, os.ErrNotExist
	}
	e := ta.ix.entries[i]
	return &sectionReadSeekCloser{SectionReader: io.NewSectionReader(ta.ra, e.Offset, e.Size)}, nil
}

func (ta *tarArchive) sniff(name string) string {
	i, ok := ta.ix.find(name)
	if !ok || ta.ix.entries[i].IsDir {
		return ""
	}
	e := ta.ix.entries[i]
	head := make([]byte, 512)
	if e.Size < int64(len(head)) {
		head = head[:int(e.Size)]
	}
	if _, err := ta.ra.ReadAt(head, e.Offset); err != nil && err != io.EOF {
		return ""
	}
	return sniffMimeFrom(bytes.NewReader(head))
}

func (ta *tarArchive) stream(name string) (io.ReadCloser, error) {
	return ta.open(name)
}

// tarIndexStore keeps scanned tar indexes resident in memory across requests,
// bounded by total entry count, so browsing the same large archive never
// re-reads or re-parses its on-disk index.
type tarIndexStore struct {
	mu    sync.Mutex
	cache map[string]*cachedTarIndex
	count int
}

type cachedTarIndex struct {
	size    int64
	modTime time.Time
	index   *tarIndex
}

// tarIndexStoreMaxEntries bounds resident memory (~1 GiB worst case).
const tarIndexStoreMaxEntries = 10_000_000

func newTarIndexStore() *tarIndexStore {
	return &tarIndexStore{cache: map[string]*cachedTarIndex{}}
}

func (st *tarIndexStore) get(key string, size int64, modTime time.Time) *tarIndex {
	st.mu.Lock()
	defer st.mu.Unlock()
	if c, ok := st.cache[key]; ok && c.size == size && c.modTime.Equal(modTime) {
		return c.index
	}
	return nil
}

func (st *tarIndexStore) put(key string, size int64, modTime time.Time, ix *tarIndex) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.count+len(ix.entries) > tarIndexStoreMaxEntries {
		st.cache = map[string]*cachedTarIndex{}
		st.count = 0
	}
	st.cache[key] = &cachedTarIndex{size: size, modTime: modTime, index: ix}
	st.count += len(ix.entries)
}

// openTar opens a tar for one request. The entry index comes from the
// resident store when available, then from the on-disk cache, then from a
// full scan; the source (host file or upper-level member) is passed in.
func (s *server) openTar(src *archiveSource) (*tarArchive, error) {
	if ix := s.tarStore.get(src.key, src.size, src.modTime); ix != nil {
		return &tarArchive{ra: src.ra, closer: src.closer, ix: ix}, nil
	}

	cache := cacheFileFor(src.key)
	var ix *tarIndex
	if src.size > archiveThreshold {
		t0 := time.Now()
		ix, _ = loadTarIndexCache(cache, src.size, src.modTime)
		// 磁盘索引解码是大档案(16MiB+)特有的开销,重复的 slow 行能暴露
		// 缓存未命中导致的反复重扫。
		if d := time.Since(t0); d >= slowThreshold {
			log.Printf("[slow] tar-index-load path=%s size=%d took=%s", src.key, src.size, d.Round(time.Millisecond))
		}
	}
	if ix == nil {
		// 全量扫描是整个 tar 浏览最重的操作(多 GB 档案可达数十秒),无论
		// 成败都留痕:失败行说明档案损坏,成功行用于对比两次扫描的耗时。
		t0 := time.Now()
		entries, err := scanTar(bufio.NewReaderSize(io.NewSectionReader(src.ra, 0, src.size), scanBufSize))
		d := time.Since(t0)
		if err != nil {
			log.Printf("[slow] scan-tar path=%s size=%d error=%v took=%s", src.key, src.size, err, d.Round(time.Millisecond))
			return nil, err
		}
		ix = newTarIndex(entries)
		log.Printf("[slow] scan-tar path=%s size=%d entries=%d took=%s", src.key, src.size, len(entries), d.Round(time.Millisecond))
		if src.size > archiveThreshold {
			_ = writeTarIndexCache(cache, src.size, src.modTime, ix) // best effort
		}
	}

	s.tarStore.put(src.key, src.size, src.modTime, ix)
	return &tarArchive{ra: src.ra, closer: src.closer, ix: ix}, nil
}

// offsetReader tracks the absolute byte offset consumed by the tar.Reader so
// each entry's data start can be recorded for random access.
type offsetReader struct {
	r   io.Reader
	off int64
}

func (o *offsetReader) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	o.off += int64(n)
	return n, err
}

// scanTar walks the whole archive once, recording each entry's data offset.
// archive/tar reads exactly one 512-byte block at a time with no read-ahead,
// so the offset after Next() returns is the entry's data start.
func scanTar(r io.Reader) ([]tarEntry, error) {
	or := &offsetReader{r: r}
	tr := tar.NewReader(or)
	var idx []tarEntry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if name == "" || name == "." {
			continue
		}
		e := tarEntry{
			Name:    name,
			Size:    hdr.Size,
			ModTime: hdr.ModTime,
			IsDir:   hdr.Typeflag == tar.TypeDir,
		}
		if !e.IsDir {
			e.Offset = or.off // data starts here; Next() skips it on the next call
		}
		idx = append(idx, e)
	}
	return idx, nil
}

// tarIndexCache is the on-disk gob encoding of a tar index, keyed by the
// archive's size + mod time so a changed archive invalidates the cache.
type tarIndexCache struct {
	Size    int64
	ModTime time.Time
	Entries []tarEntry
}

func loadTarIndexCache(cachePath string, size int64, modTime time.Time) (*tarIndex, bool) {
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	dec := gob.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	var c tarIndexCache
	if err := dec.Decode(&c); err != nil {
		return nil, false
	}
	if c.Size != size || !c.ModTime.Equal(modTime) {
		return nil, false
	}
	return &tarIndex{entries: c.Entries}, true
}

func writeTarIndexCache(cachePath string, size int64, modTime time.Time, ix *tarIndex) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	bw := bufio.NewWriterSize(tmp, 1<<20)
	if err := gob.NewEncoder(bw).Encode(tarIndexCache{Size: size, ModTime: modTime, Entries: ix.entries}); err != nil {
		tmp.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), cachePath)
}
