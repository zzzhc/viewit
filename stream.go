package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// streamDefaultChunk 是客户端未指定时每次 more 拉取的字节数;
// streamMaxChunk 封顶单次拉取,保证服务端瞬时内存有界。
const (
	streamDefaultChunk = 256 << 10
	streamMaxChunk     = 1 << 20
)

// streamMessage 是一次客户端请求:"open" 命名 root-relative 路径,
// "more" 拉取接下来 Bytes 字节。
type streamMessage struct {
	Type  string `json:"type"`
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// streamState 是一次 open 的流式读取状态:普通文件直接读原始字节,
// .gz 文件透明 gzip 解压。前端不区分两者。
type streamState struct {
	src   io.Reader
	close func()
	meta  map[string]any
}

// isGzPath 报告 p 是否为 .gz 文件(按扩展名,与内容无关)。
func isGzPath(p string) bool {
	return strings.ToLower(filepath.Ext(p)) == ".gz"
}

// gzReader 包装一个 .gz 文件为解压流,并把用于嗅探 MIME 的头部回填进流,
// 保证不丢字节。返回解压流与嗅探出的内层 MIME。调用方负责关闭 f。
func gzReader(f *os.File) (io.Reader, string, error) {
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", err
	}
	head := make([]byte, 512)
	n, rerr := io.ReadFull(gzr, head)
	if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
		return nil, "", rerr
	}
	head = head[:n]
	return io.MultiReader(bytes.NewReader(head), gzr), sniffMimeFrom(bytes.NewReader(head)), nil
}

// gzInfo 返回 .gz 文件解压后的内容 MIME 与解压后大小。
// size 取 gzip trailer 的 ISIZE(单流且 <4GB 时精确);读不到返回 -1(未知)。
func gzInfo(path string) (mime string, size int64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", -1, false
	}
	defer f.Close()
	_, mime, err = gzReader(f)
	if err != nil {
		return "", -1, false
	}
	if st, err := f.Stat(); err == nil && st.Size() >= 4 {
		tail := make([]byte, 4)
		if _, err := f.ReadAt(tail, st.Size()-4); err == nil {
			size = int64(uint32(tail[0]) | uint32(tail[1])<<8 | uint32(tail[2])<<16 | uint32(tail[3])<<24)
		}
	}
	return mime, size, true
}

// openStream 解析 root-relative 路径并返回流式读取状态:.gz 透明解压,
// 归档成员解压流式读取,普通文件直接流式读字节。meta 下发内层 name/mime
// 供前端分派与语言检测。
func (s *server) openStream(p string) (*streamState, error) {
	loc, err := s.resolveVirtual(p)
	if err != nil {
		return nil, err
	}
	if loc.archive {
		return s.openArchiveStream(loc)
	}
	f, err := os.Open(loc.hostPath)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(loc.hostPath)
	if isGzPath(loc.hostPath) {
		src, mime, err := gzReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("gzip: %w", err)
		}
		inner := strings.TrimSuffix(name, filepath.Ext(name))
		return &streamState{
			src:   src,
			close: func() { f.Close() },
			meta:  map[string]any{"type": "meta", "name": inner, "mime": mime},
		}, nil
	}
	// 普通文件:嗅探后回退,再流式读全量。
	mime := sniffMimeFrom(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &streamState{
		src:   f,
		close: func() { f.Close() },
		meta:  map[string]any{"type": "meta", "name": name, "mime": mime},
	}, nil
}

// openArchiveStream opens a file member inside a zip/tar archive for streaming.
// The MIME is sniffed from the head (never the whole member) and the member is
// decompressed on the fly, so a huge member streams its first chunk immediately
// instead of being extracted to the disk cache first.
func (s *server) openArchiveStream(loc location) (*streamState, error) {
	a, err := s.openArchive(loc.hostPath)
	if err != nil {
		return nil, err
	}
	if loc.inside == "" {
		a.close()
		return nil, errors.New("is a directory")
	}
	e, ok := a.stat(loc.inside)
	if !ok {
		a.close()
		return nil, os.ErrNotExist
	}
	if e.IsDir {
		a.close()
		return nil, errors.New("is a directory")
	}
	rc, err := a.stream(loc.inside)
	if err != nil {
		a.close()
		return nil, err
	}
	return &streamState{
		src: rc,
		close: func() {
			rc.Close()
			a.close()
		},
		meta: map[string]any{"type": "meta", "name": e.Name, "mime": a.sniff(loc.inside)},
	}, nil
}

// serveGz 透明解压一个 .gz 文件作为全文响应(小文件的 /api/file 全文 fetch)。
// gzip 是顺序流,不支持 Range;Content-Length 未知 → chunked。
func serveGz(w http.ResponseWriter, r *http.Request, f *os.File, st fs.FileInfo) {
	src, mime, err := gzReader(f)
	if err != nil {
		// 损坏的 gz:回退为原始字节下载,而不是报错。
		f.Seek(0, io.SeekStart)
		http.ServeContent(w, r, st.Name(), st.ModTime(), f)
		return
	}
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	io.Copy(w, src)
}

// handleStream 流式发送一个文本文件的(解压后)内容:客户端 open 后按需
// more 拉取,两次拉取之间保持 reader 打开,内存恒定为一块,与文件大小无关。
func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadLimit(4 << 10)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
					return
				}
			}
		}
	}()

	var (
		stream *streamState
		total  int64
		eof    bool
		path   string // 当前 open 的 root-relative 路径,用于日志
		openAt time.Time
	)
	closeStream := func() {
		if stream != nil {
			stream.close()
			stream = nil
		}
		total = 0
		eof = false
		path = ""
		openAt = time.Time{}
	}
	defer closeStream()

	send := func(v any) bool { return conn.WriteJSON(v) == nil }

	for {
		var msg streamMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return // read error, bad JSON, or deadline: drop the client
		}
		switch msg.Type {
		case "open":
			closeStream()
			t0 := time.Now()
			st, err := s.openStream(msg.Path)
			d := time.Since(t0)
			if err != nil {
				// 打不开的路径(含损坏的 .gz)必须留痕,这是唯一的线索。
				log.Printf("[ws] stream-open path=%q error=%v took=%s", msg.Path, err, d.Round(time.Microsecond))
				send(map[string]any{"type": "error", "error": err.Error()})
				continue
			}
			stream = st
			path = msg.Path
			openAt = t0
			log.Printf("[ws] stream-open path=%q name=%v mime=%v took=%s",
				msg.Path, st.meta["name"], st.meta["mime"], d.Round(time.Microsecond))
			if !send(st.meta) {
				return
			}
		case "more":
			if stream == nil {
				send(map[string]any{"type": "error", "error": "open a file first"})
				continue
			}
			if eof {
				send(map[string]any{"type": "end", "size": total})
				continue
			}
			n := msg.Bytes
			if n <= 0 {
				n = streamDefaultChunk
			} else if n > streamMaxChunk {
				n = streamMaxChunk
			}
			buf := make([]byte, 0, n)
			tmp := make([]byte, 32<<10)
			for len(buf) < n {
				m, rerr := stream.src.Read(tmp)
				buf = append(buf, tmp[:m]...)
				if rerr != nil {
					if rerr == io.EOF {
						eof = true
					} else {
						log.Printf("[ws] stream-error path=%q error=%v", path, rerr)
						send(map[string]any{"type": "error", "error": rerr.Error()})
						return
					}
					break
				}
			}
			if len(buf) > 0 {
				total += int64(len(buf))
				if !send(map[string]any{
					"type":   "data",
					"offset": total - int64(len(buf)),
					"b64":    base64.StdEncoding.EncodeToString(buf),
				}) {
					return
				}
			}
			if eof {
				// 首次读到结尾才记:后续重复 more 走开头 eof 分支,不重复打日志。
				log.Printf("[ws] stream-end path=%q size=%d took=%s", path, total, time.Since(openAt).Round(time.Microsecond))
				send(map[string]any{"type": "end", "size": total})
			}
		}
	}
}
