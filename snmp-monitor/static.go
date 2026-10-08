package main

// 内置静态文件: three.js (r160, MIT 协议, 见 static/three.LICENSE.txt) 和它的
// OrbitControls。用 go:embed 编进程序里, 内网离线环境也能用 3D 拓扑/3D 机房/大屏,
// 不依赖任何外网 CDN。
//
// 选 r160 是因为它是最后一个还支持 WebGL1 的版本, 老电脑/虚拟机的集显也能跑。
// OrbitControls.js 的 import 改成了相对路径 './three.module.min.js', 页面不需要
// importmap, 直接 import('/static/OrbitControls.js') 即可。
//
// 启动时预先 gzip 一份(670KB → 约 170KB), 浏览器支持就发压缩版; 文件名不带版本号,
// 所以用 ETag + 一天缓存, 升级程序后浏览器最多一天内会重新拉取。

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

//go:embed static
var staticFS embed.FS

type staticFile struct {
	body, gz []byte
	ctype    string
	etag     string
}

// newStaticFile 算好 ETag, 大于 1KB 的顺便预压缩一份 gzip。
func newStaticFile(body []byte, ctype string) *staticFile {
	f := &staticFile{body: body, ctype: ctype}
	sum := sha256.Sum256(body)
	f.etag = `"` + hex.EncodeToString(sum[:8]) + `"`
	if len(body) > 1024 {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(body)
		_ = zw.Close()
		if buf.Len() < len(body) {
			f.gz = buf.Bytes()
		}
	}
	return f
}

// serve 按 ETag / Accept-Encoding 输出文件。
func (f *staticFile) serve(w http.ResponseWriter, r *http.Request, cacheControl string) {
	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("Cache-Control", cacheControl)
	h.Set("ETag", f.etag)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := f.body
	if f.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = f.gz
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

var (
	staticOnce  sync.Once
	staticFiles map[string]*staticFile
)

func loadStaticFiles() {
	staticFiles = map[string]*staticFile{}
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		body, err := staticFS.ReadFile(p)
		if err != nil {
			return nil
		}
		ctype := "text/plain; charset=utf-8"
		switch strings.ToLower(path.Ext(p)) {
		case ".js", ".mjs":
			// 不能交给 mime.TypeByExtension: Windows 注册表里 .js 常被改成 text/plain,
			// 模块脚本的 MIME 不对浏览器会直接拒绝执行
			ctype = "text/javascript; charset=utf-8"
		case ".css":
			ctype = "text/css; charset=utf-8"
		case ".json":
			ctype = "application/json"
		}
		staticFiles["/"+p] = newStaticFile(body, ctype)
		return nil
	})
}

func staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		staticOnce.Do(loadStaticFiles)
		f, ok := staticFiles[r.URL.Path]
		if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		f.serve(w, r, "public, max-age=86400")
	})
}
