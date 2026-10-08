package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGuestPolicy(t *testing.T) {
	auth := newAuthStore(t.TempDir())
	if err := auth.setPassword("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	s := &server{auth: auth, sessions: newSessionMgr(time.Hour)}
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	code := func(keys ...string) int {
		w := httptest.NewRecorder()
		s.view(ok, keys...)(w, httptest.NewRequest("GET", "/x", nil))
		return w.Code
	}
	// 没配置过: 跟升级前一样全部开放
	if c := code("mactable"); c != 200 {
		t.Fatalf("默认游客应该能看, got %d", c)
	}
	if err := auth.setGuestPolicy(guestPolicy{Enabled: true, Views: []string{"bigscreen"}}); err != nil {
		t.Fatal(err)
	}
	if c := code("mactable"); c != http.StatusForbidden {
		t.Errorf("没开放的页面应该 403, got %d", c)
	}
	if c := code("topology", "bigscreen"); c != 200 {
		t.Errorf("大屏开放时拓扑数据(大屏要用)应该能看, got %d", c)
	}
	if p := auth.guestPolicy(); len(p.Views) != 2 || p.Views[0] != "home" {
		t.Errorf("home 总是包含: %+v", p)
	}
	_ = auth.setGuestPolicy(guestPolicy{Enabled: false})
	if c := code("home"); c != http.StatusUnauthorized {
		t.Errorf("关闭游客访问后应该 401, got %d", c)
	}
	// 重新加载后策略还在
	auth2 := newAuthStore(auth.path[:len(auth.path)-len("auth.json")])
	if err := auth2.load("admin"); err != nil {
		t.Fatal(err)
	}
	if auth2.guestPolicy().Enabled {
		t.Error("游客策略要保存到 auth.json")
	}
}
