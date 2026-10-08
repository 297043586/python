package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWebBundleComplete(t *testing.T) {
	b, err := buildWebBundle(webSource())
	if err != nil {
		t.Fatal(err)
	}
	page := string(b.page)
	for _, bad := range []string{"@include", "{{APP_CSS}}", "{{APP_JS}}"} {
		if strings.Contains(page, bad) {
			t.Errorf("页面里还残留 %q", bad)
		}
	}
	// 每个弹窗 / 大屏的结构都要展开进页面
	for _, id := range []string{`id="devModal"`, `id="topoModal"`, `id="macModal"`, `id="alertModal"`, `id="settingsModal"`,
		`id="rackModal"`, `id="bsRoot"`, `id="usersModal"`, `id="reportModal"`, `id="backupModal"`} {
		if !strings.Contains(page, id) {
			t.Errorf("页面缺少 %s", id)
		}
	}
	if !strings.Contains(page, "/assets/app.js?v=") || !strings.Contains(page, "/assets/app.css?v=") {
		t.Error("页面没有引用带指纹的 app.js / app.css")
	}
	if len(b.js.body) < 100000 || len(b.css.body) < 20000 || b.js.gz == nil {
		t.Errorf("前端资源大小不对: js=%d css=%d", len(b.js.body), len(b.css.body))
	}
	if !strings.Contains(string(b.login), "/api/login") {
		t.Error("登录页不对")
	}
}

func TestWebManifestMustListEveryFile(t *testing.T) {
	fsys := fstest.MapFS{
		"manifest.json":      {Data: []byte(`{"css":["css/a.css"],"js":["js/a.js"]}`)},
		"index.html":         {Data: []byte(`<!-- @include partials/x.html -->{{APP_CSS}}{{APP_JS}}`)},
		"login.html":         {Data: []byte(`login`)},
		"partials/x.html":    {Data: []byte(`<div id="x"></div>`)},
		"css/a.css":          {Data: []byte(`a{}`)},
		"js/a.js":            {Data: []byte(`var a = 1`)},
		"js/forgotten.js":    {Data: []byte(`var b = 2`)},
		"js/sub/notlisted.x": {Data: []byte(`ignored`)},
	}
	if _, err := buildWebBundle(fsys); err == nil || !strings.Contains(err.Error(), "js/forgotten.js") {
		t.Fatalf("漏写进清单的文件应该报错, got %v", err)
	}
	delete(fsys, "js/forgotten.js")
	b, err := buildWebBundle(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b.page), `<div id="x"></div>`) {
		t.Errorf("@include 没展开: %s", b.page)
	}
	// 文件之间用分号隔开, 防止两个文件首尾粘成一条语句
	if !strings.Contains(string(b.js.body), ";/* ===== js/a.js ===== */") {
		t.Errorf("js 拼接格式不对: %s", b.js.body)
	}
	fsys["manifest.json"] = &fstest.MapFile{Data: []byte(`{"css":["css/a.css"],"js":["js/a.js","js/missing.js"]}`)}
	if _, err := buildWebBundle(fsys); err == nil || !strings.Contains(err.Error(), "missing.js") {
		t.Fatalf("清单里不存在的文件应该报错, got %v", err)
	}
}

func TestAssetsHandlerCaching(t *testing.T) {
	b, err := currentWebBundle()
	if err != nil {
		t.Fatal(err)
	}
	h := assetsHandler()
	ver := strings.Trim(b.js.etag, `"`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/app.js?v="+ver, nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("带正确指纹的 app.js 应长期缓存: %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/app.js?v=stale", nil))
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("指纹对不上时不能长期缓存: %v", w.Header())
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/assets/app.css", nil)
	r.Header.Set("If-None-Match", b.css.etag)
	h.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Errorf("ETag 相同应该回 304, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/other.js", nil))
	if w.Code != 404 {
		t.Errorf("未知资源应该 404, got %d", w.Code)
	}
}
