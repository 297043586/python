package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ----------------- 数据中枢 -----------------
//
// 关键改动: /api/data 不再把全量数据(每台设备的全部端口、MAC 表、ARP、
// 历史趋势、日志)一次性怼给浏览器。设备一多、端口一多, 这个 JSON blob
// 能到几 MB, 还是每隔几十秒就整份重发一次——浏览器内存涨得快, 手机端
// 尤其明显, 长时间开着页面容易卡死或被系统回收。
// 现在 /api/data 只回轻量的卡片摘要(DeviceSummary), 打开某台设备的详情
// 窗口时才单独拉一次该设备的全量数据(/api/device?ip=), 窗口开着的时候
// 才会继续为这一台设备刷新详情。服务端内存里仍然保留全量数据(数据量
// 跟网络规模成正比,这是正常的,不是"泄漏"), 省的是浏览器和网络的那一份。

type DeviceSummary struct {
	IP             string         `json:"ip"`
	Note           string         `json:"note"`
	SysName        string         `json:"sys_name"`
	Brand          string         `json:"brand"`
	IsAlive        bool           `json:"is_alive"`
	UpTimeStr      string         `json:"uptime_str"`
	CPUUsage       int            `json:"cpu_usage"`
	MemUsage       int            `json:"mem_usage"`
	Hardware       HardwareStatus `json:"hardware"`
	PortUsageRatio string         `json:"port_usage_ratio"`
	CriticalCount  int            `json:"critical_count"`
	WarningCount   int            `json:"warning_count"`
	OpenAlerts     int            `json:"open_alerts"`

	// 首页分组 / 筛选用(v13): 自定义分组、型号、位置(sysLocation)、端口总数 / UP 数
	Group    string `json:"group,omitempty"`
	Model    string `json:"model,omitempty"`
	Location string `json:"location,omitempty"`
	Ports    int    `json:"ports"`
	UpPorts  int    `json:"up_ports"`
}

func summarize(r DeviceReport) DeviceSummary {
	s := DeviceSummary{
		IP: r.IP, Note: r.Note, SysName: r.SysName, Brand: r.Brand, IsAlive: r.IsAlive,
		UpTimeStr: r.UpTimeStr, CPUUsage: r.CPUUsage, MemUsage: r.MemUsage,
		Hardware: r.Hardware, PortUsageRatio: r.PortUsageRatio,
		CriticalCount: len(r.Criticals), WarningCount: len(r.Warnings), OpenAlerts: r.OpenAlerts,
		Group: r.Group, Ports: r.TotalPorts, UpPorts: r.UpPorts,
	}
	if r.Inventory != nil {
		s.Model, s.Location = r.Inventory.Model, r.Inventory.Location
	}
	return s
}

type dataHub struct {
	mu          sync.RWMutex
	reports     []DeviceReport
	byIP        map[string]int // ip -> reports 下标, 详情按需查找不用整表扫描
	checkTime   string
	summaryJSON []byte
	version     uint64            // 每轮巡检 +1
	cache       map[string][]byte // 按轮缓存的大汇总(MAC 总表等), 见 dataHub.cached
}

func (h *dataHub) set(reports []DeviceReport, checkTime string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports = reports
	h.checkTime = checkTime
	h.version++
	h.cache = nil
	h.byIP = make(map[string]int, len(reports))
	for i, r := range reports {
		h.byIP[r.IP] = i
	}
	h.summaryJSON = nil
}

// applyAlert 就地更新告警状态, 这样用户点"确认"之后立刻生效,
// 不用等下一轮巡检(可能是 60 秒之后)。
func (h *dataHub) applyAlert(id string, acked, closed bool, user, at string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.reports {
		rep := &h.reports[i]
		open := 0
		for _, list := range [][]Alert{rep.Criticals, rep.Warnings} {
			for j := range list {
				if list[j].ID == id {
					list[j].Acked = acked
					list[j].Closed = closed
					list[j].AckedBy = user
					list[j].AckedAt = at
				}
			}
		}
		for _, list := range [][]Alert{rep.Criticals, rep.Warnings} {
			for _, al := range list {
				if !al.Acked && !al.Closed && !al.Recovered {
					open++
				}
			}
		}
		rep.OpenAlerts = open
	}
	h.summaryJSON = nil
}

// summaryPayload 是 /api/data 的响应体: 只够画卡片网格的那点信息。
func (h *dataHub) summaryPayload() []byte {
	h.mu.RLock()
	if h.summaryJSON != nil {
		out := h.summaryJSON
		h.mu.RUnlock()
		return out
	}
	summaries := make([]DeviceSummary, len(h.reports))
	for i, r := range h.reports {
		summaries[i] = summarize(r)
	}
	body := struct {
		CheckTime string          `json:"check_time"`
		Devices   []DeviceSummary `json:"devices"`
	}{h.checkTime, summaries}
	blob, err := json.Marshal(body)
	h.mu.RUnlock()
	if err != nil {
		return []byte(`{"check_time":"","devices":[]}`)
	}
	h.mu.Lock()
	h.summaryJSON = blob
	h.mu.Unlock()
	return blob
}

// devicePayload 是打开某台设备详情窗口时按需拉取的全量数据。
func (h *dataHub) devicePayload(ip string) ([]byte, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	idx, ok := h.byIP[ip]
	if !ok {
		return nil, false
	}
	blob, err := json.Marshal(h.reports[idx])
	if err != nil {
		return nil, false
	}
	return blob, true
}

// ----------------- 服务器 -----------------

type server struct {
	cfg        *Config
	devices    *DeviceStore
	alerts     *AlertStore
	auth       *authStore
	sessions   *sessionMgr
	hub        *dataHub
	syslog     *syslogRing
	flows      *flowStore
	topology   *TopologyStore
	reports    *ReportStore
	backups    *BackupStore
	racks      *RackStore
	dc         *DatacenterStore
	vendors    *VendorStore
	history    *AlertHistory
	routeCache *RouteStore
	kick       chan struct{}

	// pageFilter 只给演示服务用(往页面里注入测试脚本), 平时为 nil
	pageFilter func([]byte) []byte

	// 页面上保存设置后: cfgLive = 已生效的配置(需要重启的字段保持启动时的值),
	// cfgSaved = 写进 config.json 的配置。都为空时用启动时的 cfg。
	cfgLive  atomic.Pointer[Config]
	cfgSaved atomic.Pointer[Config]
	cfgFile  string

	refreshMu   sync.Mutex
	lastRefresh time.Time

	deviceRefreshMu   sync.Mutex
	lastDeviceRefresh map[string]time.Time
}

func (s *server) writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) fail(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

// secureHeaders 统一加上防缓存/防嵌套/防引用泄露的响应头。
// 页面上有全网 IP 和拓扑, 不该被中间缓存或第三方页面读到。
func secureHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	h.Set("Pragma", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
}

// requireAuth 包装需要登录的处理器(设备增删改、告警操作、改密码);
// POST 还要过 CSRF 校验。查看类接口(仪表盘页面、/api/data、/api/device、
// /api/topology)不走这层, 任何人打开链接都能看, 只有会改数据的操作才
// 需要先登录——这是本轮按需求调整的权限模型。
func (s *server) requireAuth(fn func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureHeaders(w)
		sess, ok := currentSession(s.sessions, r)
		if !ok {
			s.fail(w, http.StatusUnauthorized, "此操作需要登录")
			return
		}
		if _, _, live := s.auth.access(sess.user); !live {
			s.sessions.destroyUser(sess.user)
			s.fail(w, http.StatusUnauthorized, "账号已被停用或删除, 请重新登录")
			return
		}
		if !checkCSRF(sess, r) {
			s.fail(w, http.StatusForbidden, "CSRF 校验失败, 请刷新页面重试")
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			s.fail(w, http.StatusForbidden, "请求来源不合法")
			return
		}
		fn(w, r, sess)
	}
}

// public 包装只读接口: 加安全头, 不要求登录。
func (s *server) public(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureHeaders(w)
		fn(w, r)
	}
}

// sameOrigin 校验写操作的 Origin/Referer, 与 CSRF 令牌形成双保险
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true // 非浏览器客户端(如 curl 带令牌调用)不做限制
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

func (s *server) decode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	if err := dec.Decode(v); err != nil {
		s.fail(w, http.StatusBadRequest, "请求格式有误")
		return false
	}
	return true
}

// ----------------- 路由 -----------------

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/login", s.handleLoginPage)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// 存活探针不需要登录, 但也绝不吐露设备信息
		secureHeaders(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "ok")
	})

	// ---- 查看类: 登录后都能看; 没登录按「游客访问」策略(见 guest.go) ----
	mux.HandleFunc("/", s.public(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, err := currentWebBundle()
		if err != nil {
			http.Error(w, "前端资源有误: "+err.Error(), http.StatusInternalServerError)
			return
		}
		page := b.page
		if s.pageFilter != nil {
			page = s.pageFilter(page)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	}))

	mux.HandleFunc("/api/session", s.public(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := currentSession(s.sessions, r)
		if !ok {
			s.writeJSON(w, 200, map[string]interface{}{"authenticated": false, "guest": s.auth.guestPolicy(), "views": viewDefs})
			return
		}
		role, perms, live := s.auth.access(sess.user)
		if !live { // 账号被删除/停用了
			s.sessions.destroyUser(sess.user)
			clearSessionCookie(w)
			s.writeJSON(w, 200, map[string]interface{}{"authenticated": false, "guest": s.auth.guestPolicy(), "views": viewDefs})
			return
		}
		s.writeJSON(w, 200, map[string]interface{}{
			"authenticated": true, "user": sess.user, "csrf": sess.csrf,
			"interval": s.conf().CheckIntervalSeconds, "role": role, "perms": perms,
		})
	}))

	mux.HandleFunc("/api/logout", s.public(func(w http.ResponseWriter, r *http.Request) {
		// 没登录时调用也直接 200, 前端不用先判断状态再决定要不要调
		if c, err := r.Cookie(sessionCookie); err == nil {
			s.sessions.destroy(c.Value)
		}
		clearSessionCookie(w)
		s.writeJSON(w, 200, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/data", s.view(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(s.hub.summaryPayload())
	}, "home", "bigscreen"))

	mux.HandleFunc("/api/device", s.view(func(w http.ResponseWriter, r *http.Request) {
		ip := strings.TrimSpace(r.URL.Query().Get("ip"))
		if ip == "" {
			s.fail(w, http.StatusBadRequest, "缺少 ip 参数")
			return
		}
		blob, ok := s.hub.devicePayload(ip)
		if !ok {
			s.fail(w, http.StatusNotFound, "没有这台设备的数据(可能还没跑完第一轮, 或已被删除)")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(s.stripHostsForGuest(r, blob))
	}, "home", "bigscreen", "topology", "racks"))
	mux.HandleFunc("/api/device/routes", s.view(s.handleDeviceRoutes, "hosts"))

	mux.HandleFunc("/api/topology", s.view(s.handleTopology, "topology", "bigscreen"))
	mux.HandleFunc("/api/topology/background", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.requirePerm("topology", s.handleTopoBackgroundSet)(w, r)
			return
		}
		s.view(s.handleTopoBackgroundGet, "topology", "bigscreen")(w, r)
	})
	mux.HandleFunc("/api/topology/background/clear", s.requirePerm("topology", s.handleTopoBackgroundClear))
	mux.HandleFunc("/api/topology/position", s.requirePerm("topology", s.handleTopoPosition))
	mux.HandleFunc("/api/topology/positions", s.requirePerm("topology", s.handleTopoPositions))
	mux.HandleFunc("/api/topology/manual", s.requirePerm("topology", s.handleTopoManual))
	mux.HandleFunc("/api/mactable", s.view(s.handleGlobalMACTable, "mactable"))
	mux.HandleFunc("/api/arptable", s.view(s.handleGlobalARPTable, "arptable"))
	mux.HandleFunc("/api/l2health", s.view(s.handleL2Health, "l2health"))
	mux.HandleFunc("/api/alerts", s.view(s.handleGlobalAlerts, "alerts", "bigscreen"))
	mux.HandleFunc("/api/alerts/history", s.view(s.handleAlertHistory, "alerts"))
	mux.HandleFunc("/api/alerts/history/detail", s.view(s.handleAlertHistoryDetail, "alerts"))
	mux.HandleFunc("/api/alerts/history/export", s.view(s.handleAlertHistoryExport, "alerts"))

	mux.HandleFunc("/api/syslog", s.view(s.handleSyslog, "flows"))

	mux.HandleFunc("/api/flows", s.view(s.handleFlows, "flows"))

	// ---- 需要登录: 会改变数据的操作 ----
	mux.HandleFunc("/api/refresh", s.requireAuth(s.handleManualRefresh))
	mux.HandleFunc("/api/device/refresh", s.requireAuth(s.handleDeviceRefresh))
	mux.HandleFunc("/api/device/live", s.requireAuth(s.handleDeviceLive))
	mux.HandleFunc("/api/flows/settings", s.requirePerm("settings", s.handleFlowSettings))
	mux.HandleFunc("/api/devices", s.requirePerm("devices", func(w http.ResponseWriter, r *http.Request, sess *session) {
		s.writeJSON(w, 200, map[string]interface{}{"devices": s.devices.Public()})
	}))

	mux.HandleFunc("/api/devices/add", s.requirePerm("devices", s.handleDeviceAdd))
	mux.HandleFunc("/api/devices/update", s.requirePerm("devices", s.handleDeviceUpdate))
	mux.HandleFunc("/api/devices/delete", s.requirePerm("devices", s.handleDeviceDelete))
	mux.HandleFunc("/api/devices/test", s.requirePerm("devices", s.handleDeviceTest))
	mux.HandleFunc("/api/devices/lldp", s.requirePerm("devices", s.handleLLDPDiscovered))
	mux.HandleFunc("/api/alerts/action", s.requirePerm("alerts", s.handleAlertAction))
	mux.HandleFunc("/api/password", s.requireAuth(s.handlePassword))

	// 巡检报告: 列表/查看对游客按"总开关 + 单份公开"过滤, 其余需要登录
	mux.HandleFunc("/api/reports", s.view(s.handleReportList, "reports"))
	mux.HandleFunc("/api/reports/get", s.view(s.handleReportGet, "reports"))
	mux.HandleFunc("/api/reports/generate", s.requirePerm("reports", s.handleReportGenerate))
	mux.HandleFunc("/api/reports/action", s.requirePerm("reports", s.handleReportAction))
	mux.HandleFunc("/api/reports/settings", s.requirePerm("reports", s.handleReportSettings))

	// 机柜 / 面板视图: 查看公开, 调整机柜位置需要登录
	mux.HandleFunc("/api/panels", s.view(s.handlePanels, "racks", "topology", "bigscreen"))
	mux.HandleFunc("/api/overview", s.view(s.handleOverview, "bigscreen", "home"))
	mux.HandleFunc("/api/racks/assign", s.requirePerm("datacenter", s.handleRackAssign))

	// 配置备份: 全部需要登录(配置里有密码哈希、团体名等敏感信息)
	mux.HandleFunc("/api/backups", s.requirePerm("backup_view", s.handleBackupList))
	mux.HandleFunc("/api/backups/get", s.requirePerm("backup_view", s.handleBackupGet))
	mux.HandleFunc("/api/backups/run", s.requirePerm("backup", s.handleBackupRun))
	mux.HandleFunc("/api/backups/upload", s.requirePerm("backup", s.handleBackupUpload))
	mux.HandleFunc("/api/backups/delete", s.requirePerm("backup", s.handleBackupDelete))
	mux.HandleFunc("/api/backups/cred", s.requirePerm("backup", s.handleBackupCred))
	mux.HandleFunc("/api/backups/settings", s.requirePerm("backup", s.handleBackupSettings))

	// 配置下发: 权限按类型在 handler 里判断(只读的 show 只要求 backup_view)
	mux.HandleFunc("/api/push/preview", s.requireAuth(s.handlePushPreview))
	mux.HandleFunc("/api/push/run", s.requireAuth(s.handlePushRun))
	mux.HandleFunc("/api/push/job", s.requireAuth(s.handlePushJob))
	mux.HandleFunc("/api/push/meta", s.requireAuth(s.handlePushMeta))

	// 系统设置: 读写 config.json 需要登录; 显示参数公开(大屏/游客也要用)
	mux.HandleFunc("/api/settings", s.requirePerm("settings", s.handleSettings))
	mux.HandleFunc("/api/settings/storage", s.requirePerm("settings", s.handleStorage))
	mux.HandleFunc("/api/settings/cleanup", s.requirePerm("settings", s.handleDataCleanup))
	mux.HandleFunc("/api/ui-settings", s.public(s.handleUISettings))

	// 3D 机房: 查看公开, 编辑需要登录
	mux.HandleFunc("/api/datacenter", s.view(s.handleDatacenterGet, "racks", "bigscreen", "home"))
	mux.HandleFunc("/api/datacenter/save", s.requirePerm("datacenter", s.handleDatacenterSave))

	// 厂商识别规则 / 私有 OID(vendors.go)
	mux.HandleFunc("/api/vendors/list", s.public(s.handleVendorsPublic))
	mux.HandleFunc("/api/vendors", s.requirePerm("settings", s.handleVendors))
	mux.HandleFunc("/api/vendors/test", s.requirePerm("settings", s.handleVendorTest))

	// 内置的前端库(three.js 等), 内网离线也能用
	// 账号权限(只有管理员)
	mux.HandleFunc("/api/users", s.requireAdmin(s.handleUsers))
	mux.HandleFunc("/api/users/save", s.requireAdmin(s.handleUserSave))
	mux.HandleFunc("/api/users/delete", s.requireAdmin(s.handleUserDelete))
	mux.HandleFunc("/api/users/guest", s.requireAdmin(s.handleGuestPolicy))

	mux.Handle("/static/", staticHandler())
	mux.Handle("/assets/", assetsHandler())

	return gzipHandler(mux)
}

func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if _, ok := currentSession(s.sessions, r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	b, err := currentWebBundle()
	if err != nil {
		http.Error(w, "前端资源有误: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.login)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if r.Method != http.MethodPost {
		s.fail(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	if !sameOrigin(r) {
		s.fail(w, http.StatusForbidden, "请求来源不合法")
		return
	}
	ip := clientIP(r)
	if locked, left := s.sessions.throttled(ip); locked {
		s.fail(w, http.StatusTooManyRequests,
			fmt.Sprintf("失败次数过多, 请 %d 秒后再试", int(left.Seconds())+1))
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	canon, ok := s.auth.check(strings.TrimSpace(body.User), body.Password)
	if !ok {
		s.sessions.noteFail(ip)
		// 不区分"用户名不存在"和"口令错误", 避免帮攻击者做用户名枚举
		s.fail(w, http.StatusUnauthorized, "用户名或口令错误")
		return
	}
	s.sessions.noteSuccess(ip)
	s.auth.touchLogin(canon)
	token, csrf := s.sessions.create(canon)
	setSessionCookie(w, r, token, s.sessions.getTTL())
	s.writeJSON(w, 200, map[string]string{"status": "ok", "csrf": csrf})
}

func (s *server) handlePassword(w http.ResponseWriter, r *http.Request, sess *session) {
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if !s.auth.verify(sess.user, body.Old) {
		s.fail(w, http.StatusUnauthorized, "原口令不正确")
		return
	}
	if err := s.auth.setPassword(sess.user, body.New); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

// triggerRound 请求调度器立刻跑一轮(合并重复请求)
func (s *server) triggerRound() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// refreshCooldown 是手动"立即刷新"两次触发之间的最短间隔。
// 这个接口是公开的(不需要登录), 不加节流的话任何打开页面的人都能
// 反复点它把轮询频率拉到远超 check_interval_seconds 配置, 平白增加
// 对被巡检交换机的压力。5 秒对人手点击来说完全够用, 对脚本滥用足够挡。
const refreshCooldown = 5 * time.Second

// handleManualRefresh 是"立即刷新"按钮真正要调用的接口: 之前那个按钮
// 名不副实, 只是重新拉一遍已经缓存好的数据, 上一轮巡检没结束的话点了
// 也没反应, 看起来像坏了。这里才是真正把 SNMP 巡检提前触发一次。
// handleManualRefresh 触发一次真正的 SNMP 巡检, 只对登录用户开放——
// 未登录的访客点"立即刷新"只是重新拉一遍已经算好的数据(见前端 forceRefresh),
// 不会去戳一遍全网设备; 这样公开可看的仪表盘不会被人反复刷从而把巡检
// 频率拉到远超 check_interval_seconds 配置。
func (s *server) handleManualRefresh(w http.ResponseWriter, r *http.Request, sess *session) {
	s.refreshMu.Lock()
	elapsed := time.Since(s.lastRefresh)
	if elapsed < refreshCooldown {
		wait := refreshCooldown - elapsed
		s.refreshMu.Unlock()
		s.writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"status": "cooldown", "wait_ms": wait.Milliseconds(),
		})
		return
	}
	s.lastRefresh = time.Now()
	s.refreshMu.Unlock()
	s.triggerRound()
	s.writeJSON(w, 200, map[string]interface{}{"status": "ok"})
}

// handleDeviceRefresh: 设备详情页的"⚡ 立即巡检"——只重新采集这一台,
// 不用等下一轮全网巡检。单独一个每设备冷却时间, 跟全局的"立即巡检"
// (handleManualRefresh)冷却互不影响。
func (s *server) handleDeviceRefresh(w http.ResponseWriter, r *http.Request, sess *session) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		s.fail(w, http.StatusBadRequest, "缺少 ip 参数")
		return
	}
	s.deviceRefreshMu.Lock()
	if s.lastDeviceRefresh == nil {
		s.lastDeviceRefresh = map[string]time.Time{}
	}
	elapsed := time.Since(s.lastDeviceRefresh[ip])
	if elapsed < refreshCooldown {
		wait := refreshCooldown - elapsed
		s.deviceRefreshMu.Unlock()
		s.writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"status": "cooldown", "wait_ms": wait.Milliseconds(),
		})
		return
	}
	s.lastDeviceRefresh[ip] = time.Now()
	s.deviceRefreshMu.Unlock()

	if _, err := s.refreshSingleDevice(ip); err != nil {
		s.fail(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"status": "ok"})
}

func (s *server) start() error {
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.conf().BindAddress, s.conf().WebPort),
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	go func() {
		fmt.Printf("🌐 Web 服务已开启: http://%s:%d (需登录)\n", s.conf().BindAddress, s.conf().WebPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("❌ Web 服务启动失败: %v\n", err)
		}
	}()
	return nil
}
