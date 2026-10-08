package main

// 页面和 JSON 接口的 gzip 压缩。
//
// 主页面(HTML + 全部内联 JS/CSS)约 500KB, /api/datacenter、/api/panels、/api/device
// 带着每台交换机的全部端口, 设备一多就是几 MB, 而且每 15~30 秒刷新一次。以前全部明文发送,
// 远程/无线/VPN 打开时很明显地卡。文本 JSON 压缩比通常在 8~15 倍。
//
// 只压缩 text/html、application/json、text/plain 且 >= 1KB 的响应; 已经带 Content-Encoding
// 的(static.go 里预压缩的 three.js)原样透传。

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

const gzipMinSize = 1024

var gzipPool = sync.Pool{New: func() interface{} {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return zw
}}

type gzipWriter struct {
	http.ResponseWriter
	code    int
	buf     []byte // 还没决定要不要压缩时先攒着
	zw      *gzip.Writer
	decided bool
	plain   bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.code == 0 {
		g.code = code
	}
}

func (g *gzipWriter) compressible() bool {
	h := g.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Disposition") != "" {
		return false
	}
	if g.code != 0 && g.code != http.StatusOK {
		return false
	}
	ct := h.Get("Content-Type")
	return strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain")
}

func (g *gzipWriter) decide(force bool) {
	if g.decided {
		return
	}
	if !g.compressible() {
		g.decided, g.plain = true, true
	} else if len(g.buf) >= gzipMinSize {
		g.decided = true
		h := g.Header()
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Del("Content-Length")
		g.zw = gzipPool.Get().(*gzip.Writer)
		g.zw.Reset(g.ResponseWriter)
	} else if force {
		g.decided, g.plain = true, true
	} else {
		return
	}
	if g.code != 0 {
		g.ResponseWriter.WriteHeader(g.code)
	}
	buf := g.buf
	g.buf = nil
	if len(buf) > 0 {
		if g.zw != nil {
			_, _ = g.zw.Write(buf)
		} else {
			_, _ = g.ResponseWriter.Write(buf)
		}
	}
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.decided {
		if !g.compressible() {
			g.decide(true)
		} else {
			g.buf = append(g.buf, p...)
			g.decide(false)
			return len(p), nil
		}
	}
	if g.zw != nil {
		return g.zw.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipWriter) finish() {
	g.decide(true)
	if g.zw != nil {
		_ = g.zw.Close()
		g.zw.Reset(io.Discard)
		gzipPool.Put(g.zw)
		g.zw = nil
	}
}

func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w}
		defer g.finish()
		next.ServeHTTP(g, r)
	})
}
