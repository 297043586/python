package main

// 前端资源(web/ 目录, go:embed 编进程序, 内网离线可用)。
//
//   web/index.html        页面外壳; <!-- @include partials/xxx.html --> 在构建时展开
//   web/partials/*.html   各个弹窗 / 大屏的结构
//   web/css/*.css         样式, 拼成 /assets/app.css
//   web/js/**/*.js        脚本, 拼成 /assets/app.js(普通脚本, 共用一个全局作用域)
//   web/manifest.json     CSS / JS 的拼接顺序
//
// 以前整个前端是十几个 Go 反引号字符串在 main.go 里拼起来的: 没有语法高亮、不能用反引号、
// 改一个字要重新编译, 页面 HTML 也不能被浏览器缓存(每次打开都重新下 1MB)。现在:
//   - 页面外壳很小, app.css / app.js 带内容指纹(?v=), 浏览器缓存一年, 升级后指纹变了自动重新拉;
//   - 启动时预压缩 gzip;
//   - 开发时设置环境变量 NETMON_WEB_DIR=web, 每次打开首页都从磁盘重新读, 改前端刷新即可。
//
// manifest.json 里漏写 / 多写文件都会在启动(和单元测试)时直接报错, 不会悄悄少加载一个模块。

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed web
var webEmbed embed.FS

type webBundle struct {
	page  []byte
	login []byte
	css   *staticFile
	js    *staticFile
	files int
}

type webManifest struct {
	CSS []string `json:"css"`
	JS  []string `json:"js"`
}

var (
	webMu  sync.Mutex
	webCur *webBundle
)

// webDevDir 非空时前端每次都从磁盘读(开发用)。
func webDevDir() string { return strings.TrimSpace(os.Getenv("NETMON_WEB_DIR")) }

func webSource() fs.FS {
	if dir := webDevDir(); dir != "" {
		return os.DirFS(dir)
	}
	sub, err := fs.Sub(webEmbed, "web")
	if err != nil {
		panic(err)
	}
	return sub
}

// currentWebBundle 返回当前的前端资源; 开发模式下每次都重新构建。
func currentWebBundle() (*webBundle, error) {
	webMu.Lock()
	defer webMu.Unlock()
	if webCur != nil && webDevDir() == "" {
		return webCur, nil
	}
	b, err := buildWebBundle(webSource())
	if err != nil {
		return nil, err
	}
	webCur = b
	return b, nil
}

// buildDashboardPage 返回首页 HTML(启动时调用一次, 顺便校验前端资源完整)。
func buildDashboardPage() ([]byte, error) {
	b, err := currentWebBundle()
	if err != nil {
		return nil, err
	}
	return b.page, nil
}

var includeRe = regexp.MustCompile(`<!--\s*@include\s+([\w./-]+)\s*-->`)

func buildWebBundle(src fs.FS) (*webBundle, error) {
	raw, err := fs.ReadFile(src, "manifest.json")
	if err != nil {
		return nil, fmt.Errorf("读取 web/manifest.json 失败: %w", err)
	}
	var man webManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return nil, fmt.Errorf("web/manifest.json 格式有误: %w", err)
	}
	if err := checkManifest(src, "css", ".css", man.CSS); err != nil {
		return nil, err
	}
	if err := checkManifest(src, "js", ".js", man.JS); err != nil {
		return nil, err
	}
	concat := func(list []string, js bool) ([]byte, error) {
		var buf bytes.Buffer
		for _, p := range list {
			body, err := fs.ReadFile(src, p)
			if err != nil {
				return nil, fmt.Errorf("读取 web/%s 失败: %w", p, err)
			}
			if js {
				// 分号: 防止上一个文件结尾没有分号时跟下一个文件的开头粘成一条语句
				fmt.Fprintf(&buf, "\n;/* ===== %s ===== */\n", p)
			} else {
				fmt.Fprintf(&buf, "\n/* ===== %s ===== */\n", p)
			}
			buf.Write(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
		}
		return buf.Bytes(), nil
	}
	css, err := concat(man.CSS, false)
	if err != nil {
		return nil, err
	}
	js, err := concat(man.JS, true)
	if err != nil {
		return nil, err
	}
	b := &webBundle{
		css:   newStaticFile(css, "text/css; charset=utf-8"),
		js:    newStaticFile(js, "text/javascript; charset=utf-8"),
		files: len(man.CSS) + len(man.JS),
	}
	index, err := fs.ReadFile(src, "index.html")
	if err != nil {
		return nil, fmt.Errorf("读取 web/index.html 失败: %w", err)
	}
	page, err := expandIncludes(src, index, 0)
	if err != nil {
		return nil, err
	}
	ver := func(f *staticFile) string { return strings.Trim(f.etag, `"`) }
	page = bytes.Replace(page, []byte("{{APP_CSS}}"), []byte("/assets/app.css?v="+ver(b.css)), 1)
	page = bytes.Replace(page, []byte("{{APP_JS}}"), []byte("/assets/app.js?v="+ver(b.js)), 1)
	b.page = page
	if b.login, err = fs.ReadFile(src, "login.html"); err != nil {
		return nil, fmt.Errorf("读取 web/login.html 失败: %w", err)
	}
	return b, nil
}

func expandIncludes(src fs.FS, html []byte, depth int) ([]byte, error) {
	if depth > 4 {
		return nil, fmt.Errorf("web/ 里的 @include 嵌套太深(循环引用?)")
	}
	var firstErr error
	out := includeRe.ReplaceAllFunc(html, func(m []byte) []byte {
		name := string(includeRe.FindSubmatch(m)[1])
		part, err := fs.ReadFile(src, path.Clean(name))
		if err == nil {
			part, err = expandIncludes(src, part, depth+1)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("展开 @include %s 失败: %w", name, err)
			}
			return nil
		}
		return part
	})
	return out, firstErr
}

// checkManifest: 清单里的文件都得存在、不能重复; 目录里的文件也都得写进清单。
func checkManifest(src fs.FS, dir, ext string, list []string) error {
	listed := map[string]bool{}
	for _, p := range list {
		if listed[p] {
			return fmt.Errorf("web/manifest.json 里 %s 写了两次", p)
		}
		listed[p] = true
		if _, err := fs.Stat(src, p); err != nil {
			return fmt.Errorf("web/manifest.json 列了 %s, 但文件不存在", p)
		}
	}
	var missing []string
	_ = fs.WalkDir(src, dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ext) && !listed[p] {
			missing = append(missing, p)
		}
		return nil
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("这些前端文件没有写进 web/manifest.json, 不会被加载: %s", strings.Join(missing, ", "))
	}
	return nil
}

// assetsHandler 提供 /assets/app.css 和 /assets/app.js。
// 带 ?v=<指纹> 且指纹对得上时长期缓存; 其余情况每次校验(ETag)。
func assetsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := currentWebBundle()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var f *staticFile
		switch r.URL.Path {
		case "/assets/app.css":
			f = b.css
		case "/assets/app.js":
			f = b.js
		}
		if f == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		cache := "no-cache"
		if v := r.URL.Query().Get("v"); v != "" && `"`+v+`"` == f.etag {
			cache = "public, max-age=31536000, immutable"
		}
		f.serve(w, r, cache)
	})
}
