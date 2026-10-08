package main

// 游客(未登录)可见范围。
//
// 以前所有查看类页面对任何打开链接的人都开放。现在管理员可以在「👥 账号权限 → 游客访问」里:
//   - 关掉游客访问: 不登录什么都看不到, 打开页面直接要求登录;
//   - 或者只放开一部分页面(比如只给看仪表盘和大屏, 不给看 MAC / ARP 总表这类带终端信息的)。
// 登录后的账号(管理员 / 子账号)查看不受限制, 写操作仍按原来的权限走。
// 没配置过时默认"游客可以看全部", 跟升级前行为一致。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// viewDefs 是可以单独对游客开放的查看类页面。home 是基础(首页设备列表 / 设备详情),
// 游客访问打开时总是包含它。
var viewDefs = []permDef{
	{"home", "仪表盘 / 设备详情", "首页设备列表、设备详情里的端口 / 告警 / 趋势(打开游客访问时必选)"},
	{"hosts", "终端与网络表", "设备详情里的 ARP、MAC、VLANIF 和路由表(能看到 IP / MAC / 网络结构)"},
	{"topology", "拓扑", "2D / 3D 拓扑"},
	{"racks", "机柜 / 机房", "机柜面板、3D 机房"},
	{"bigscreen", "大屏", "📺 大屏(会用到拓扑和机房数据)"},
	{"alerts", "告警中心", "当前告警和告警历史"},
	{"mactable", "MAC 总表", "全网 MAC 定位"},
	{"arptable", "ARP 总表", "全网 IP-MAC"},
	{"l2health", "二层体检", "风暴 / 环路 / ARP 欺骗汇总"},
	{"reports", "巡检报告", "查看已生成的报告"},
	{"flows", "流量 / 日志", "设备详情里的流量分析和 syslog"},
}

type guestPolicy struct {
	Enabled bool     `json:"enabled"`
	Views   []string `json:"views"`
}

func defaultGuestPolicy() guestPolicy {
	p := guestPolicy{Enabled: true}
	for _, d := range viewDefs {
		p.Views = append(p.Views, d.Key)
	}
	return p
}

func (p guestPolicy) allows(keys ...string) bool {
	if !p.Enabled {
		return false
	}
	for _, k := range keys {
		for _, v := range p.Views {
			if v == k {
				return true
			}
		}
	}
	return false
}

func (p guestPolicy) normalized() guestPolicy {
	out := guestPolicy{Enabled: p.Enabled}
	seen := map[string]bool{}
	for _, v := range append([]string{"home"}, p.Views...) {
		for _, d := range viewDefs {
			if d.Key == v && !seen[v] {
				seen[v] = true
				out.Views = append(out.Views, v)
			}
		}
	}
	return out
}

func viewName(k string) string {
	for _, d := range viewDefs {
		if d.Key == k {
			return d.Name
		}
	}
	return k
}

func (a *authStore) guestPolicy() guestPolicy {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.guest == nil {
		return defaultGuestPolicy()
	}
	return *a.guest
}

func (a *authStore) setGuestPolicy(p guestPolicy) error {
	p = p.normalized()
	a.mu.Lock()
	a.guest = &p
	a.mu.Unlock()
	return a.save()
}

// loggedIn: 带着有效会话(账号还在、没停用)。
func (s *server) loggedIn(r *http.Request) bool {
	sess, ok := currentSession(s.sessions, r)
	if !ok {
		return false
	}
	_, _, live := s.auth.access(sess.user)
	return live
}

// view 包装查看类接口: 登录了直接放行; 没登录按游客策略, keys 里任意一个开放就行
// (比如大屏要用拓扑数据: /api/topology 对 topology 或 bigscreen 开放都能访问)。
func (s *server) view(fn http.HandlerFunc, keys ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureHeaders(w)
		if s.loggedIn(r) {
			fn(w, r)
			return
		}
		p := s.auth.guestPolicy()
		if !p.Enabled {
			s.fail(w, http.StatusUnauthorized, "需要登录才能查看")
			return
		}
		if !p.allows(keys...) {
			s.fail(w, http.StatusForbidden, "游客不能查看「"+viewName(keys[0])+"」, 请登录")
			return
		}
		fn(w, r)
	}
}

// stripHostsForGuest: 游客没有"终端信息"权限时, 设备详情里去掉 IP-MAC-端口 和 MAC 表。
func (s *server) stripHostsForGuest(r *http.Request, blob []byte) []byte {
	if s.loggedIn(r) || s.auth.guestPolicy().allows("hosts") {
		return blob
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		return blob
	}
	for _, k := range []string{"host_mappings", "mac_table", "fdb_table", "own_ips", "own_addrs", "own_macs"} {
		if _, ok := m[k]; ok {
			m[k] = json.RawMessage("[]")
		}
	}
	m["hosts_hidden"] = json.RawMessage("true")
	out, err := json.Marshal(m)
	if err != nil {
		return blob
	}
	return out
}

func (s *server) handleGuestPolicy(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodPost {
		var p guestPolicy
		if !s.decode(w, r, &p) {
			return
		}
		for _, v := range p.Views {
			if viewName(v) == v {
				s.fail(w, http.StatusBadRequest, "未知的页面: "+v)
				return
			}
		}
		if err := s.auth.setGuestPolicy(p); err != nil {
			s.fail(w, http.StatusInternalServerError, "保存失败: "+err.Error())
			return
		}
		state := "关闭"
		if p.Enabled {
			state = "开放: " + strings.Join(s.auth.guestPolicy().Views, ",")
		}
		fmt.Printf("👥 [%s] %s 修改了游客访问 → %s\n", time.Now().Format("15:04:05"), sess.user, state)
	}
	s.writeJSON(w, 200, map[string]interface{}{"guest": s.auth.guestPolicy(), "views": viewDefs})
}
