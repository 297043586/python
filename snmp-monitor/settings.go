package main

// 系统设置(顶栏「🛠️ 设置」)。
//
// config.json 里的每一项都能在页面上改, 另外加了几组原来写死在前端的显示参数
// (UISettings) 和配置备份的各厂商默认命令 (BackupCommands)。保存流程:
//
//  1. 在当前生效的配置上叠加页面提交的值, 逐项校验, 不合法直接报错(不会像
//     启动时那样悄悄改成默认值, 免得用户以为改成功了);
//  2. 写回 config.json: 按固定顺序输出, devices 段和不认识的字段原样保留,
//     v6 之前的旧字段名(cpu_threshold 等)删掉, 旧文件先备份成 config.json.bak;
//  3. 能热生效的立即生效(巡检间隔、SNMP 参数、告警阈值、Webhook、显示参数、
//     备份命令、会话时长...), 监听地址/端口、数据目录、管理员用户名、被动
//     采集器端口这类需要重启的只写文件, 页面上标出"重启后生效"。
//
// 写死、不放进设置的(改了反而容易出问题, 或者没人需要调):
//   - 端口抖动判定窗口 10 分钟、趋势图采样点上限 20000、SNMP OID 列表、
//     厂商私有 MIB 的识别规则;
//   - 设备清单本身(在「设备管理」里维护, 存 data/devices.json);
//   - 巡检报告/配置备份的定时任务、AI 接口(各自窗口里已有设置页, 这里只放入口)。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// UISettings 是页面显示相关的参数, 所有浏览器共用(值班室电视打开大屏不用再单独设置)。
// 个人在页面上临时改的(首页刷新间隔、主题、3D 开关)仍记在各自浏览器里, 优先级更高。
type UISettings struct {
	SiteTitle          string `json:"site_title"`           // 首页左上角标题
	DefaultTheme       string `json:"default_theme"`        // 浏览器还没选过主题时用哪个
	PageRefreshSeconds int    `json:"page_refresh_seconds"` // 首页卡片默认刷新间隔

	BigscreenTitle           string `json:"bigscreen_title"`
	BigscreenStyle           string `json:"bigscreen_style"`            // tech / cyber / aurora / gold / mono / redalert
	BigscreenCenter          string `json:"bigscreen_center"`           // map / topo3d / dc3d / carousel
	BigscreenRefreshSeconds  int    `json:"bigscreen_refresh_seconds"`  // 数据刷新间隔
	BigscreenCarouselSeconds int    `json:"bigscreen_carousel_seconds"` // center=carousel 时每个画面停留多久
	BigscreenScrollSpeed     *int   `json:"bigscreen_scroll_speed"`     // 列表自动滚动速度 像素/秒, 0 = 不自动滚(不写 = 30)
	BigscreenRiskCount       int    `json:"bigscreen_risk_count"`       // 风险设备排行显示几台
	BigscreenHotCount        int    `json:"bigscreen_hot_count"`        // 最忙端口显示几条
	BigscreenAnimations      *bool  `json:"bigscreen_animations"`       // 飞线/粒子/扫描等动画

	TopoRefreshSeconds int     `json:"topo_refresh_seconds"` // 拓扑(2D/3D)自动刷新
	Topo3DParticles    *bool   `json:"topo3d_particles"`     // 3D 拓扑流量粒子默认开关
	Topo3DAutoRotate   *bool   `json:"topo3d_auto_rotate"`   // 3D 拓扑自动旋转默认开关
	RackRefreshSeconds int     `json:"rack_refresh_seconds"` // 机柜/机房视图自动刷新
	WeakOpticalDbm     float64 `json:"weak_optical_dbm"`     // 面板上"收光偏弱"的标记线(告警阈值是 alerts.optical)
	BusyUtilPercent    float64 `json:"busy_util_percent"`    // "高负载"标记线
}

var validBigscreenStyles = []string{"tech", "cyber", "aurora", "gold", "mono", "redalert"}
var validBigscreenCenters = []string{"map", "topo3d", "dc3d", "carousel"}

func boolPtr(v bool) *bool { return &v }

func (u *UISettings) applyDefaults() {
	if u.SiteTitle == "" {
		u.SiteTitle = "局域网智能监控大屏"
	}
	if u.PageRefreshSeconds < 5 || u.PageRefreshSeconds > 600 {
		u.PageRefreshSeconds = 30
	}
	if u.BigscreenTitle == "" {
		u.BigscreenTitle = "网络运行态势总览"
	}
	if !inList(u.BigscreenStyle, validBigscreenStyles) {
		u.BigscreenStyle = "tech"
	}
	if !inList(u.BigscreenCenter, validBigscreenCenters) {
		u.BigscreenCenter = "map"
	}
	if u.BigscreenRefreshSeconds < 5 || u.BigscreenRefreshSeconds > 3600 {
		u.BigscreenRefreshSeconds = 15
	}
	if u.BigscreenCarouselSeconds < 10 || u.BigscreenCarouselSeconds > 3600 {
		u.BigscreenCarouselSeconds = 30
	}
	if u.BigscreenScrollSpeed == nil || *u.BigscreenScrollSpeed < 0 || *u.BigscreenScrollSpeed > 300 {
		v := 30
		u.BigscreenScrollSpeed = &v
	}
	if u.BigscreenRiskCount <= 0 || u.BigscreenRiskCount > 30 {
		u.BigscreenRiskCount = 8
	}
	if u.BigscreenHotCount <= 0 || u.BigscreenHotCount > 30 {
		u.BigscreenHotCount = 6
	}
	if u.BigscreenAnimations == nil {
		u.BigscreenAnimations = boolPtr(true)
	}
	if u.TopoRefreshSeconds < 10 || u.TopoRefreshSeconds > 3600 {
		u.TopoRefreshSeconds = 30
	}
	if u.Topo3DParticles == nil {
		u.Topo3DParticles = boolPtr(true)
	}
	if u.Topo3DAutoRotate == nil {
		u.Topo3DAutoRotate = boolPtr(true)
	}
	if u.RackRefreshSeconds < 10 || u.RackRefreshSeconds > 3600 {
		u.RackRefreshSeconds = 30
	}
	if u.WeakOpticalDbm >= 0 || u.WeakOpticalDbm < -40 {
		u.WeakOpticalDbm = -20
	}
	if u.BusyUtilPercent <= 0 || u.BusyUtilPercent > 100 {
		u.BusyUtilPercent = 70
	}
}

func inList(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// 配置备份的默认命令(每行一条, 最后一条的输出作为配置)。华为/H3C 用
// display current-configuration(可简写 dis cu), 锐捷/Cisco 用 show running-config
// (可简写 show run); 前面的命令关掉分页, 免得输出停在 ---- More ----。
var builtinBackupCommands = map[string]string{
	"huawei":  "screen-length 0 temporary\ndisplay current-configuration",
	"h3c":     "screen-length disable\ndisplay current-configuration",
	"ruijie":  "terminal length 0\nshow running-config",
	"cisco":   "terminal length 0\nshow running-config",
	"generic": "terminal length 0\nshow running-config",
}

var backupVendors = []string{"huawei", "h3c", "ruijie", "cisco", "generic"}

func applyBackupCommandDefaults(m map[string]string) map[string]string {
	out := make(map[string]string, len(builtinBackupCommands))
	for k, v := range m {
		k = strings.ToLower(strings.TrimSpace(k))
		if !inList(k, backupVendors) {
			continue
		}
		if v = strings.TrimSpace(strings.ReplaceAll(v, "\r", "")); v != "" {
			out[k] = v
		}
	}
	for k, v := range builtinBackupCommands {
		if out[k] == "" {
			out[k] = v
		}
	}
	return out
}

// liveBackupCommands 是当前生效的各厂商备份命令, 备份模块直接读(不依赖 server)。
var liveBackupCommands atomic.Pointer[map[string]string]

func setLiveBackupCommands(m map[string]string) {
	cp := applyBackupCommandDefaults(m)
	liveBackupCommands.Store(&cp)
}

// backupCommandsFor 返回某厂商的默认备份命令列表。
func backupCommandsFor(vendor string) []string {
	vendor = strings.ToLower(vendor)
	if !inList(vendor, backupVendors) {
		// 页面上新增的品牌可以在「厂商 / OID」里自带备份命令
		if p, ok := vendors().byID[vendor]; ok && p.BackupCmd != "" {
			return splitCmdLines(p.BackupCmd)
		}
		vendor = "generic"
	}
	text := builtinBackupCommands[vendor]
	if p := liveBackupCommands.Load(); p != nil && (*p)[vendor] != "" {
		text = (*p)[vendor]
	}
	return splitCmdLines(text)
}

func splitCmdLines(text string) []string {
	var cmds []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			cmds = append(cmds, l)
		}
	}
	return cmds
}

// ----------------- 生效中的配置 -----------------

// conf 返回当前生效的配置。页面保存设置后会整体换成一份新的 Config(旧的
// 不再修改), 所以拿到指针以后在本轮里随便读, 不需要加锁。
func (s *server) conf() *Config {
	if c := s.cfgLive.Load(); c != nil {
		return c
	}
	return s.cfg
}

func (s *server) configPath() string {
	if s.cfgFile != "" {
		return s.cfgFile
	}
	return "config.json"
}

// cloneConfig 深拷贝(通过 JSON), 保证页面上改的是副本。
func cloneConfig(c *Config) *Config {
	blob, _ := json.Marshal(c)
	out := &Config{}
	_ = json.Unmarshal(blob, out)
	return out
}

// restartOnlyFields 是改了以后需要重启程序才生效的字段(按 JSON 路径)。
var restartOnlyFields = []string{"bind_address", "web_port", "data_dir", "admin_user",
	"collectors.syslog_port", "collectors.netflow_port", "collectors.sflow_port"}

func restartFieldsChanged(running, saved *Config) []string {
	var out []string
	add := func(name string, changed bool) {
		if changed {
			out = append(out, name)
		}
	}
	add("bind_address", running.BindAddress != saved.BindAddress)
	add("web_port", running.WebPort != saved.WebPort)
	add("data_dir", running.DataDir != saved.DataDir)
	add("admin_user", running.AdminUser != saved.AdminUser)
	add("collectors.syslog_port", running.Collectors.SyslogPort != saved.Collectors.SyslogPort)
	add("collectors.netflow_port", running.Collectors.NetflowPort != saved.Collectors.NetflowPort)
	add("collectors.sflow_port", running.Collectors.SflowPort != saved.Collectors.SflowPort)
	return out
}

// validateConfig 校验页面提交的值。启动时的 applyDefaults 会把离谱的值悄悄
// 改回默认, 页面保存不能这样(用户会以为自己改成功了), 这里直接报错。
func validateConfig(c *Config) error {
	var errs []string
	bad := func(cond bool, msg string, args ...interface{}) {
		if cond {
			errs = append(errs, fmt.Sprintf(msg, args...))
		}
	}
	portOK := func(p int) bool { return p >= 1 && p <= 65535 }
	bad(c.BindAddress != "" && net.ParseIP(c.BindAddress) == nil, "监听地址 %q 不是合法 IP(全部网卡用 0.0.0.0)", c.BindAddress)
	bad(!portOK(c.WebPort), "Web 端口需要在 1~65535")
	bad(strings.TrimSpace(c.DataDir) == "", "数据目录不能为空")
	bad(strings.TrimSpace(c.AdminUser) == "", "管理员用户名不能为空")
	bad(c.SessionHours < 1 || c.SessionHours > 24*30, "登录有效期需要在 1~720 小时")
	bad(!portOK(c.SNMPPort), "SNMP 端口需要在 1~65535")
	bad(c.CheckIntervalSeconds < 30 || c.CheckIntervalSeconds > 86400, "巡检间隔需要在 30~86400 秒")
	bad(c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60, "SNMP 超时需要在 1~60 秒")
	bad(c.Retries < 0 || c.Retries > 5, "重试次数需要在 0~5")
	bad(c.MaxConcurrency < 1 || c.MaxConcurrency > 500, "并发设备数需要在 1~500")
	bad(c.WalkParallel < 1 || c.WalkParallel > 16, "单台设备并行会话数需要在 1~16")
	bad(c.MaxRepetitions < 1 || c.MaxRepetitions > 200, "GETBULK 批大小需要在 1~200")
	bad(c.HeavyEveryNRounds < 1 || c.HeavyEveryNRounds > 100, "MAC/ARP 大表采集间隔需要在 1~100 轮")
	bad(c.UplinkMACThreshold < 1 || c.UplinkMACThreshold > 10000, "上联口判定 MAC 数需要在 1~10000")
	bad(c.TrendRetentionHours < 1 || c.TrendRetentionHours > 24*90, "趋势保留时长需要在 1~2160 小时")
	bad(c.AlertHistoryDays < 7 || c.AlertHistoryDays > 3650, "告警历史保留天数需要在 7~3650")
	for _, p := range []struct {
		n string
		v int
	}{{"syslog", c.Collectors.SyslogPort}, {"NetFlow", c.Collectors.NetflowPort}, {"sFlow", c.Collectors.SflowPort}} {
		bad(p.v < 0 || p.v > 65535, "%s 端口需要在 0~65535(0 = 不启用)", p.n)
	}
	cp := c.Collectors
	bad(cp.SyslogPort > 0 && (cp.SyslogPort == cp.NetflowPort || cp.SyslogPort == cp.SflowPort), "syslog 端口跟流量采集端口冲突")
	bad(cp.NetflowPort > 0 && cp.NetflowPort == cp.SflowPort, "NetFlow 和 sFlow 不能用同一个端口")
	a := c.Alerts
	bad(a.CPU.Threshold <= 0 || a.CPU.Threshold > 100, "CPU 告警阈值需要在 1~100%%")
	bad(a.Mem.Threshold <= 0 || a.Mem.Threshold > 100, "内存告警阈值需要在 1~100%%")
	bad(a.Bandwidth.Threshold <= 0 || a.Bandwidth.Threshold > 100, "带宽告警阈值需要在 1~100%%")
	bad(a.PortError.Threshold <= 0, "错包告警阈值需要大于 0")
	bad(a.Optical.Threshold >= 0 || a.Optical.Threshold < -50, "收光告警阈值是负数 dBm, 比如 -25")
	if c.VlanCommunityFormat != "" {
		bad(strings.Count(c.VlanCommunityFormat, "%s") != 2, "按 VLAN 团体名格式需要正好两个 %%s(原团体名, VLAN 号), 比如 %%s@%%s")
	}
	if c.WebhookURL != "" {
		u, err := url.Parse(c.WebhookURL)
		bad(err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "", "Webhook 地址需要是 http:// 或 https:// 开头的完整地址")
	}
	u := c.UI
	bad(u.PageRefreshSeconds < 5 || u.PageRefreshSeconds > 600, "首页刷新间隔需要在 5~600 秒")
	bad(u.BigscreenRefreshSeconds < 5 || u.BigscreenRefreshSeconds > 3600, "大屏刷新间隔需要在 5~3600 秒")
	bad(u.BigscreenCarouselSeconds < 10 || u.BigscreenCarouselSeconds > 3600, "大屏轮播间隔需要在 10~3600 秒")
	bad(u.BigscreenScrollSpeed == nil || *u.BigscreenScrollSpeed < 0 || *u.BigscreenScrollSpeed > 300, "列表滚动速度需要在 0~300")
	bad(u.BigscreenRiskCount < 1 || u.BigscreenRiskCount > 30, "风险设备排行条数需要在 1~30")
	bad(u.BigscreenHotCount < 1 || u.BigscreenHotCount > 30, "最忙端口条数需要在 1~30")
	bad(!inList(u.BigscreenStyle, validBigscreenStyles), "未知的大屏样式 %q", u.BigscreenStyle)
	bad(!inList(u.BigscreenCenter, validBigscreenCenters), "未知的大屏中间画面 %q", u.BigscreenCenter)
	bad(u.TopoRefreshSeconds < 10 || u.TopoRefreshSeconds > 3600, "拓扑刷新间隔需要在 10~3600 秒")
	bad(u.RackRefreshSeconds < 10 || u.RackRefreshSeconds > 3600, "机柜刷新间隔需要在 10~3600 秒")
	bad(u.WeakOpticalDbm >= 0 || u.WeakOpticalDbm < -40, "收光偏弱标记线是负数 dBm, 比如 -20")
	bad(u.BusyUtilPercent <= 0 || u.BusyUtilPercent > 100, "高负载标记线需要在 1~100%%")
	bad(len([]rune(u.SiteTitle)) > 40 || len([]rune(u.BigscreenTitle)) > 40, "标题最多 40 个字")
	for k, v := range c.BackupCommands {
		bad(!inList(k, backupVendors), "未知的厂商 %q", k)
		bad(len(v) > 2000, "%s 的备份命令太长", k)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// 写回 config.json 时的字段顺序(跟 config.example.json 一致, 方便手工对照)。
var configKeyOrder = []string{
	"bind_address", "web_port", "data_dir", "admin_user", "session_hours",
	"snmp_port", "check_interval_seconds", "timeout_seconds", "retries", "max_concurrency",
	"walk_parallel", "max_repetitions", "heavy_every_n_rounds",
	"collectors", "alerts",
	"uplink_mac_threshold", "trend_retention_hours", "alert_history_days", "trend_points", "vlan_community_format", "webhook_url",
	"ui", "backup_commands",
}

var legacyConfigKeys = map[string]bool{}

func init() {
	t := reflect.TypeOf(legacyConfigFields{})
	for i := 0; i < t.NumField(); i++ {
		legacyConfigKeys[strings.Split(t.Field(i).Tag.Get("json"), ",")[0]] = true
	}
}

// renderConfigFile 把新配置按固定顺序排好, 并保留旧文件里的 devices 段和不认识的字段。
func renderConfigFile(c *Config, old []byte) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	cc := *c
	cc.Devices = nil
	blob, err := json.Marshal(cc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(blob, &fields); err != nil {
		return nil, err
	}
	delete(fields, "devices")
	extra := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &extra); err != nil {
			return nil, fmt.Errorf("现有 config.json 不是合法 JSON, 为避免覆盖掉手写内容, 请先修好或删掉它: %v", err)
		}
	}
	for k := range fields {
		delete(extra, k)
	}
	for k := range legacyConfigKeys {
		delete(extra, k)
	}
	devices, hasDevices := extra["devices"]
	delete(extra, "devices")

	var keys []string
	seen := map[string]bool{}
	for _, k := range configKeyOrder {
		if _, ok := fields[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range fields {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	var extraKeys []string
	for k := range extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)

	var buf bytes.Buffer
	buf.WriteString("{\n")
	first := true
	write := func(k string, v json.RawMessage) {
		if !first {
			buf.WriteString(",\n")
		}
		first = false
		var ind bytes.Buffer
		if err := json.Indent(&ind, v, "  ", "  "); err != nil {
			ind.Reset()
			ind.Write(v)
		}
		kb, _ := json.Marshal(k)
		buf.WriteString("  ")
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(ind.Bytes())
	}
	for _, k := range keys {
		write(k, fields[k])
	}
	for _, k := range extraKeys {
		write(k, extra[k])
	}
	if hasDevices {
		write("devices", devices)
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}

// applyConfig 把新配置换上去: 需要重启的字段沿用正在运行的值。
func (s *server) applyConfig(saved *Config) []string {
	running := s.conf()
	live := cloneConfig(saved)
	pending := restartFieldsChanged(running, saved)
	live.BindAddress, live.WebPort, live.DataDir, live.AdminUser = running.BindAddress, running.WebPort, running.DataDir, running.AdminUser
	live.Collectors.SyslogPort, live.Collectors.NetflowPort, live.Collectors.SflowPort =
		running.Collectors.SyslogPort, running.Collectors.NetflowPort, running.Collectors.SflowPort
	live.Devices = running.Devices
	s.cfgLive.Store(live)
	setLiveBackupCommands(live.BackupCommands)
	s.history.SetRetention(live.AlertHistoryDays)
	s.history.SetMaxRecords(live.AlertHistoryMaxRecords)
	if s.syslog != nil {
		s.syslog.setCap(live.SyslogMaxLines)
	}
	if s.sessions != nil {
		s.sessions.setTTL(time.Duration(live.SessionHours) * time.Hour)
	}
	// 巡检间隔变了就马上排下一轮, 新间隔从这一轮开始算
	if running.CheckIntervalSeconds != live.CheckIntervalSeconds && s.kick != nil {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
	return pending
}

// ----------------- 接口 -----------------

func settingsView(c *Config) map[string]interface{} {
	blob, _ := json.Marshal(c)
	m := map[string]interface{}{}
	_ = json.Unmarshal(blob, &m)
	delete(m, "devices")
	delete(m, "port_speed_threshold_mb") // 早就没有地方用了
	return m
}

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodGet {
		def := &Config{}
		def.applyDefaults()
		l7 := false
		if s.flows != nil {
			l7 = s.flows.l7Enabled()
		}
		abs, _ := filepath.Abs(s.configPath())
		pending := restartFieldsChanged(s.conf(), s.savedConfigOrRunning())
		s.writeJSON(w, 200, map[string]interface{}{
			"config": settingsView(s.savedConfigOrRunning()), "defaults": settingsView(def),
			"restart_fields": restartOnlyFields, "restart_pending": pending,
			"file": abs, "l7_enabled": l7, "flow_available": s.flows != nil,
		})
		return
	}
	var req struct {
		Config    json.RawMessage `json:"config"`
		L7Enabled *bool           `json:"l7_enabled"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	next := cloneConfig(s.savedConfigOrRunning())
	next.UI.BigscreenAnimations, next.UI.Topo3DParticles, next.UI.Topo3DAutoRotate, next.UI.BigscreenScrollSpeed = nil, nil, nil, nil
	next.BackupCommands = nil
	if len(req.Config) > 0 {
		if err := json.Unmarshal(req.Config, next); err != nil {
			s.fail(w, 400, "设置格式有误: "+err.Error())
			return
		}
	}
	next.UI.applyDefaults()
	next.BackupCommands = applyBackupCommandDefaults(next.BackupCommands)
	if err := validateConfig(next); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	next.Devices = nil

	path := s.configPath()
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		s.fail(w, 500, "读取 config.json 失败: "+err.Error())
		return
	}
	out, err := renderConfigFile(next, old)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	if len(old) > 0 {
		_ = os.WriteFile(path+".bak", old, 0o600)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		s.fail(w, 500, "写入 config.json 失败: "+err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		s.fail(w, 500, "写入 config.json 失败: "+err.Error())
		return
	}
	s.cfgSaved.Store(cloneConfig(next))
	pending := s.applyConfig(next)
	if req.L7Enabled != nil && s.flows != nil {
		if err := s.flows.setL7(*req.L7Enabled); err != nil {
			s.fail(w, 500, "L7 开关保存失败: "+err.Error())
			return
		}
	}
	fmt.Printf("🛠️  %s 修改了系统设置 (已写入 %s)\n", sess.user, path)
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "restart_pending": pending})
}

// handleDataCleanup: 系统设置「清理工具」——手动清空某一类数据, 不用等
// 保留期自然过期, 也不用直接去删 data/ 目录下的文件(那样容易删错、
// 而且程序还在跑的时候内存里的缓存不会跟着变, 页面上看起来像没删掉)。
func (s *server) handleDataCleanup(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Target string `json:"target"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	var msg string
	switch req.Target {
	case "syslog":
		if s.syslog == nil {
			s.fail(w, 400, "syslog 接收未启用")
			return
		}
		n := s.syslog.clearAll()
		msg = fmt.Sprintf("已清空 syslog 缓存, 共 %d 条", n)
	case "flows":
		if s.flows == nil {
			s.fail(w, 400, "流量采集未启用")
			return
		}
		n := s.flows.clearAll()
		msg = fmt.Sprintf("已清空流量记录/趋势/计数器基线, 共 %d 条记录", n)
	case "trend":
		n := clearAllTrend()
		saveHistory(s.conf().DataDir, s.conf().trendCap())
		msg = fmt.Sprintf("已清空性能趋势数据, 共 %d 个采样点", n)
	case "alert_history":
		n, err := s.history.clearAll()
		if err != nil {
			s.fail(w, 500, "清空告警历史失败: "+err.Error())
			return
		}
		msg = fmt.Sprintf("已清空告警历史, 共 %d 条事件(进行中的告警不受影响)", n)
	default:
		s.fail(w, 400, "未知的清理目标: "+req.Target)
		return
	}
	fmt.Printf("🧹 %s 执行了数据清理: %s (%s)\n", sess.user, req.Target, msg)
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "message": msg})
}

// savedConfigOrRunning: 页面上显示的是"文件里保存的值"(可能包含还没重启生效的端口)。
func (s *server) savedConfigOrRunning() *Config {
	if c := s.cfgSaved.Load(); c != nil {
		return c
	}
	return s.conf()
}

// handleUISettings 是公开接口: 大屏/游客也要拿到显示参数。
func (s *server) handleUISettings(w http.ResponseWriter, r *http.Request) {
	c := s.conf()
	s.writeJSON(w, 200, map[string]interface{}{"ui": c.UI, "check_interval": c.CheckIntervalSeconds})
}
