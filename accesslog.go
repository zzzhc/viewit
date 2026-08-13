package main

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

// slowThreshold 是 [slow] 耗时操作日志的门槛:低于它的操作不记,保持日志
// 聚焦在真正值得排查的路径上。main 用 -slow flag 覆盖;测试可临时调小以
// 触发记录。
var slowThreshold = time.Second

// accessLog 为每个 HTTP 请求输出一行访问日志:
//
//	[access] <remote> <method> <uri> <status> <bytes> <duration>
//
// 覆盖全部路由(含 SPA 静态资源与 404/405)。WebSocket 路由(/api/ws、
// /api/stream)的 duration 是整个会话时长:handler 在连接关闭后才返回,
// 前端挂着不关的连接会在日志里暴露为长耗时;升级后字节不再经过响应
// writer,bytes 记为 0。
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("[access] %s %s %s %d %d %s",
			r.RemoteAddr, r.Method, r.URL.RequestURI(),
			status, sw.bytes, time.Since(start).Round(time.Microsecond))
	})
}

// statusWriter 捕获响应状态码与写入字节数,并透传 Flush/Hijack/Unwrap:
// WebSocket 升级需要 Hijacker,流式响应需要 Flusher,http.ResponseController
// 通过 Unwrap 找到内层 writer。三个能力缺一不可,否则 WS/流式路由会坏。
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying response writer does not support hijacking")
	}
	return hj.Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// truncateForLog 把 s 截断到 max 个 rune 再加省略号,防止超长查询刷爆日志行。
func truncateForLog(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
