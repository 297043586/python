package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormPortKeyCrossVendor(t *testing.T) {
	// 同一个口在不同来源里的写法, 归一化后必须相等
	groups := [][]string{
		{"GigabitEthernet 0/1", "Gi0/1", "GigabitEthernet0/1", "GE0/1"},
		{"Ten-GigabitEthernet1/0/49", "XGE1/0/49", "TenGigabitEthernet1/0/49", "Te1/0/49"},
		{"GigabitEthernet0/0/1", "GE0/0/1", "Gi0/0/1"},
		{"FastEthernet0/5", "Fa0/5", "FE0/5"},
		{"Eth-Trunk10", "BAGG10", "Bridge-Aggregation10", "AggregatePort10"},
	}
	for _, g := range groups {
		want := normPortKey(g[0])
		for _, name := range g[1:] {
			if got := normPortKey(name); got != want {
				t.Errorf("normPortKey(%q)=%q, 期望与 %q 的 %q 一致", name, got, g[0], want)
			}
		}
	}
	// 不同速率族不能互相匹配
	if normPortKey("Gi0/1") == normPortKey("XGE0/1") {
		t.Error("千兆口和万兆口不应该归一化成同一个 key")
	}
}

func TestIndexResolver(t *testing.T) {
	ifs := map[string]*ifInfo{
		"1":  {Index: "1", Descr: "GigabitEthernet 0/1", Name: "Gi0/1"},
		"49": {Index: "49", Descr: "TenGigabitEthernet 0/49", Name: "Te0/49"},
	}
	r := newIndexResolver(ifs)

	if got := r.byName("Gi0/1"); got != "1" {
		t.Errorf("锐捷 LLDP 简称 Gi0/1 应解析到 ifIndex 1, 实际 %q", got)
	}
	if got := r.byName("GigabitEthernet0/1"); got != "1" {
		t.Errorf("全称应解析到 ifIndex 1, 实际 %q", got)
	}
	if got := r.byName("XGE0/49"); got != "49" {
		t.Errorf("万兆口简称应解析到 49, 实际 %q", got)
	}
	// 多级索引: 锐捷光模块通道表的 "49.1" 必须取第一段, 而不是最后一段
	if got := r.byTableIndex("49.1"); got != "49" {
		t.Errorf("byTableIndex(49.1) 应得到 49, 实际 %q", got)
	}
	if got := r.byTableIndex("1"); got != "1" {
		t.Errorf("byTableIndex(1) 应得到 1, 实际 %q", got)
	}
	if got := r.byTableIndex("999"); got != "" {
		t.Errorf("未知索引应返回空, 实际 %q", got)
	}
}

func TestParseOpticalRaw(t *testing.T) {
	cases := []struct {
		raw   int64
		scale float64
		want  float64
		ok    bool
	}{
		{-523, 100, -5.23, true},    // H3C/华为/锐捷 0.01 dBm
		{-1, 100, 0, false},         // 华为 DOM 未开启
		{2147483647, 100, 0, false}, // H3C "不支持"
		{0, 100, 0, false},          // 未插模块
		{65535, 100, 0, false},
		{-4000, 100, -40, true},
		{-6000, 100, 0, false},         // -60 dBm 超范围
		{4294966773, 100, -5.23, true}, // 按无符号读回来的负数
	}
	for _, c := range cases {
		got, ok := parseOpticalRaw(c.raw, c.scale)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseOpticalRaw(%d)=%v,%v 期望 %v,%v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestCounterDelta(t *testing.T) {
	if got := counterDelta(1000, 400, false); got != 600 {
		t.Errorf("正常增量应为 600, 实际 %d", got)
	}
	// 32 位计数器回绕
	if got := counterDelta(100, 4294967200, true); got != 196 {
		t.Errorf("32 位回绕应为 196, 实际 %d", got)
	}
	// 32 位计数器被清零(而不是回绕): 不能刷出几十亿的假增量
	if got := counterDelta(5, 3000000000, false); got != 0 {
		t.Errorf("错包计数器清零应归 0, 实际 %d", got)
	}
	if got := plausibleRate(9800, 1000); got != 0 {
		t.Errorf("超出线速的速率应丢弃, 实际 %v", got)
	}
	if got := plausibleRate(420.5, 1000); got != 420.5 {
		t.Errorf("正常速率被误杀: %v", got)
	}
	// 设备重启导致 64 位计数器倒退 -> 不能产生天文数字
	if got := counterDelta(50, 999999999999, false); got != 0 {
		t.Errorf("64 位倒退应归 0, 实际 %d", got)
	}
}

func TestMacFromOIDDigits(t *testing.T) {
	if got := macFromOIDDigits([]string{"0", "26", "128", "1", "2", "255"}); got != "00:1A:80:01:02:FF" {
		t.Errorf("MAC 解析错误: %q", got)
	}
	if got := macFromOIDDigits([]string{"1", "2", "3"}); got != "" {
		t.Errorf("长度不足应返回空, 实际 %q", got)
	}
	if got := normalizeMACString("00e0-fc12-3456"); got != "00:E0:FC:12:34:56" {
		t.Errorf("H3C 风格 MAC 解析错误: %q", got)
	}
}

func TestStateKeyIsolatesInstances(t *testing.T) {
	a := DeviceTarget{IP: "10.0.0.1", Community: "ruijie"}
	b := DeviceTarget{IP: "10.0.0.1", Community: "h3c"}
	if a.stateKey() == b.stateKey() {
		t.Error("同一 IP 的不同实例必须有独立的历史基线")
	}
}

func TestDetectBrand(t *testing.T) {
	cases := map[string]string{
		".1.3.6.1.4.1.2011.2.239.45":    "huawei",
		".1.3.6.1.4.1.25506.11.1.212":   "h3c",
		".1.3.6.1.4.1.4881.1.1.10.1.55": "ruijie",
		".1.3.6.1.4.1.9.1.1":            "cisco",
		".1.3.6.1.4.1.3902.3.100":       "",
	}
	for oid, want := range cases {
		if got := detectBrand(oid, ""); got != want {
			t.Errorf("detectBrand(%s)=%q 期望 %q", oid, got, want)
		}
	}
	if got := detectBrand("", "Ruijie Full Ten-Gigabit Switch RGOS 11.4"); got != "ruijie" {
		t.Errorf("按 sysDescr 识别锐捷失败: %q", got)
	}
}

func TestShortSysNameKeepsDottedValues(t *testing.T) {
	if got := shortSysName("SW-CORE.corp.local"); got != "SW-CORE" {
		t.Errorf("域名后缀应被去掉: %q", got)
	}
	if got := shortSysName("10.1.1.254"); got != "10.1.1.254" {
		t.Errorf("IP 不应被截断: %q", got)
	}
	// 旧版的通用清洗会把这类文本截断成 "-5"
	if got := decodeText("-5.23 dBm"); got != "-5.23 dBm" {
		t.Errorf("普通文本被破坏: %q", got)
	}
}

func TestNaturalLess(t *testing.T) {
	if !naturalLess("Gi0/2", "Gi0/10") {
		t.Error("Gi0/2 应排在 Gi0/10 前面")
	}
	if naturalLess("Gi0/10", "Gi0/2") {
		t.Error("排序方向反了")
	}
}

func TestConfigDefaultsPreventFalseAlarms(t *testing.T) {
	var c Config
	c.Devices = []DeviceTarget{{IP: "1.1.1.1"}}
	c.applyDefaults()
	if c.Alerts.CPU.Threshold == 0 || c.Alerts.Mem.Threshold == 0 {
		t.Error("阈值为 0 会让每台设备都刷假告警")
	}
	if c.Devices[0].Community != "public" {
		t.Error("community 缺省值未生效")
	}
}

// TestMigrateLegacyConfigPreservesOldFlatFields 确认 v6 把告警阈值/被动采集
// 端口从 Config 顶层挪进 Alerts/Collectors 嵌套结构之后, 老的扁平字段名
// (cpu_threshold/syslog_port 等)升级时不会被静默丢弃——尤其是已经打开的
// syslog_port 这种"丢了也不报错, 用户很难第一时间发现"的字段。
func TestMigrateLegacyConfigPreservesOldFlatFields(t *testing.T) {
	raw := []byte(`{
		"cpu_threshold": 70,
		"mem_threshold": 75,
		"error_delta_threshold": 5,
		"optical_rx_warn_dbm": -20,
		"syslog_port": 514,
		"netflow_port": 2055,
		"sflow_port": 6343
	}`)
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	migrateLegacyConfig(raw, &c)
	c.applyDefaults()

	if c.Alerts.CPU.Threshold != 70 {
		t.Errorf("cpu_threshold 没迁移过来, got %v", c.Alerts.CPU.Threshold)
	}
	if c.Alerts.Mem.Threshold != 75 {
		t.Errorf("mem_threshold 没迁移过来, got %v", c.Alerts.Mem.Threshold)
	}
	if c.Alerts.PortError.Threshold != 5 {
		t.Errorf("error_delta_threshold 没迁移过来, got %v", c.Alerts.PortError.Threshold)
	}
	if c.Alerts.Optical.Threshold != -20 {
		t.Errorf("optical_rx_warn_dbm 没迁移过来, got %v", c.Alerts.Optical.Threshold)
	}
	if c.Collectors.SyslogPort != 514 {
		t.Errorf("syslog_port 没迁移过来, got %v (老配置升级后会被静默关掉 syslog 接收)", c.Collectors.SyslogPort)
	}
	if c.Collectors.NetflowPort != 2055 || c.Collectors.SflowPort != 6343 {
		t.Errorf("netflow_port/sflow_port 没迁移过来, got %v/%v", c.Collectors.NetflowPort, c.Collectors.SflowPort)
	}
}

// TestMigrateLegacyConfigDoesNotOverrideNewFormat 确认已经用新嵌套格式显式
// 配置过的字段, 不会被旧扁平字段(哪怕两者同时出现在同一份配置里)覆盖回去。
func TestMigrateLegacyConfigDoesNotOverrideNewFormat(t *testing.T) {
	raw := []byte(`{
		"cpu_threshold": 70,
		"syslog_port": 514,
		"alerts": {"cpu": {"threshold": 95}},
		"collectors": {"syslog_port": 1514}
	}`)
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	migrateLegacyConfig(raw, &c)

	if c.Alerts.CPU.Threshold != 95 {
		t.Errorf("新格式的 alerts.cpu.threshold 被旧字段覆盖了, got %v", c.Alerts.CPU.Threshold)
	}
	if c.Collectors.SyslogPort != 1514 {
		t.Errorf("新格式的 collectors.syslog_port 被旧字段覆盖了, got %v", c.Collectors.SyslogPort)
	}
}

// 渲染一次页面外壳, 确认模板可执行, 且前端引用的 JSON 字段确实存在
func TestDashboardTemplateAndTrendFieldNames(t *testing.T) {
	blob, err := json.Marshal([]TrendPoint{{TS: 1, Time: "10:00:00", CPU: 12, Mem: 40, MACCount: 8, InMbps: 3.2, OutMbps: 1.1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"cpu"`, `"mem"`, `"mac_count"`, `"in_mbps"`, `"out_mbps"`, `"ts"`} {
		if !bytes.Contains(blob, []byte(field)) {
			t.Errorf("趋势数据缺少前端需要的字段 %s: %s", field, blob)
		}
	}

	b, err := currentWebBundle()
	if err != nil {
		t.Fatalf("前端资源构建失败: %v", err)
	}
	html := string(b.page) + string(b.js.body)
	for _, need := range []string{
		"x.cpu", "x.mem", "x.mac_count", "cpuCanvas", "memCanvas", "macCanvas",
		"/api/data", "/api/device", "/api/topology", "/api/syslog",
		"chartMeta", "X-CSRF-Token", "openAuthGate", "authedApi",
	} {
		if !strings.Contains(html, need) {
			t.Errorf("页面缺少 %q", need)
		}
	}
	if strings.Contains(html, "d.CPU") {
		t.Error("仍残留旧版的大写字段引用 d.CPU")
	}
	// 页面外壳里不应该出现任何设备数据(改成按需拉取了)
	if strings.Contains(html, "var summaryData = [{") {
		t.Error("页面外壳不应内嵌设备数据")
	}
}

func TestPBKDF2AndPasswordStore(t *testing.T) {
	// 与 RFC 6070 同构的自检: 同输入同输出, 不同盐不同结果
	a := pbkdf2SHA256([]byte("password"), []byte("salt"), 1000, 32)
	b := pbkdf2SHA256([]byte("password"), []byte("salt"), 1000, 32)
	if !bytes.Equal(a, b) {
		t.Fatal("PBKDF2 结果不稳定")
	}
	c := pbkdf2SHA256([]byte("password"), []byte("salt2"), 1000, 32)
	if bytes.Equal(a, c) {
		t.Fatal("不同盐必须得到不同派生值")
	}

	dir := t.TempDir()
	st := newAuthStore(dir)
	if err := st.setPassword("admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if !st.verify("admin", "correct-horse") {
		t.Error("正确口令应通过")
	}
	if st.verify("admin", "wrong") {
		t.Error("错误口令不应通过")
	}
	if st.verify("root", "correct-horse") {
		t.Error("错误用户名不应通过")
	}
	if err := st.setPassword("admin", "short"); err == nil {
		t.Error("过短的口令应被拒绝")
	}
	// 磁盘上不能出现明文口令
	blob, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("correct-horse")) {
		t.Error("口令明文落盘了")
	}
}

func TestDeviceStoreNeverLeaksCommunity(t *testing.T) {
	dir := t.TempDir()
	box, err := loadSecretBox(dir)
	if err != nil {
		t.Fatal(err)
	}
	ds := newDeviceStore(dir, box)
	if _, err := ds.Add(DeviceTarget{IP: "10.1.1.1", Community: "s3cr3t-community", Brand: "ruijie"}); err != nil {
		t.Fatal(err)
	}

	// 1) 落盘的是密文
	blob, err := os.ReadFile(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("s3cr3t-community")) {
		t.Error("团体名明文落盘了")
	}

	// 2) 对外视图不含团体名
	pub, err := json.Marshal(ds.Public())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pub, []byte("s3cr3t-community")) {
		t.Error("接口响应里泄露了团体名")
	}
	if !bytes.Contains(pub, []byte(`"has_community":true`)) {
		t.Error("对外视图应标记团体名已配置")
	}

	// 3) 重新加载后仍能解密出原值
	ds2 := newDeviceStore(dir, box)
	if err := ds2.load(); err != nil {
		t.Fatal(err)
	}
	got := ds2.List()
	if len(got) != 1 || got[0].Community != "s3cr3t-community" {
		t.Fatalf("重新加载后团体名不正确: %+v", got)
	}

	// 4) 重复地址要被拒绝
	if _, err := ds.Add(DeviceTarget{IP: "10.1.1.1", Community: "x"}); err == nil {
		t.Error("重复地址应被拒绝")
	}
	// 5) 非法地址要被拒绝
	if _, err := ds.Add(DeviceTarget{IP: "10.1.1.1; rm -rf /", Community: "x"}); err == nil {
		t.Error("非法地址应被拒绝")
	}
	// 6) 更新时留空 community 表示不修改
	id := got[0].ID
	if err := ds2.Update(id, DeviceTarget{IP: "10.1.1.2", Brand: "h3c", Community: ""}); err != nil {
		t.Fatal(err)
	}
	after, _ := ds2.Get(id)
	if after.Community != "s3cr3t-community" || after.IP != "10.1.1.2" {
		t.Errorf("更新语义不正确: %+v", after)
	}
}

func TestAlertAckSurvivesNextRound(t *testing.T) {
	dir := t.TempDir()
	as := newAlertStore(dir)

	build := func() *DeviceReport {
		rep := &DeviceReport{IP: "10.0.0.1", stateKey: "dev1"}
		rep.alert("critical", "psu", "", "电源模块异常")
		rep.alert("warning", "port_err", "Gi0/1", "端口错包增加")
		return rep
	}

	r1 := build()
	as.Sync(r1)
	if r1.OpenAlerts != 2 {
		t.Fatalf("初始应有 2 条未处理, 实际 %d", r1.OpenAlerts)
	}
	first := r1.Criticals[0].FirstSeen

	// 确认其中一条
	if err := as.setAck(r1.Criticals[0].ID, true, false, "admin"); err != nil {
		t.Fatal(err)
	}

	// 下一轮同样的故障还在 -> 确认状态必须保留, 且 ID 不变
	r2 := build()
	as.Sync(r2)
	if r2.Criticals[0].ID != r1.Criticals[0].ID {
		t.Error("同一故障在不同轮次的 ID 必须稳定")
	}
	if !r2.Criticals[0].Acked || r2.Criticals[0].AckedBy != "admin" {
		t.Error("确认状态没有保留到下一轮")
	}
	if r2.Criticals[0].FirstSeen != first {
		t.Error("首次出现时间不应被刷新")
	}
	if r2.OpenAlerts != 1 {
		t.Errorf("确认后应剩 1 条未处理, 实际 %d", r2.OpenAlerts)
	}

	// 故障恢复后状态丢弃; 再次发生时应作为新告警重新提醒
	r3 := &DeviceReport{IP: "10.0.0.1", stateKey: "dev1"}
	as.Sync(r3)
	r4 := build()
	as.Sync(r4)
	if r4.Criticals[0].Acked {
		t.Error("恢复后再次发生的告警不应还是已确认状态")
	}

	// 落盘后能读回
	as.save()
	as2 := newAlertStore(dir)
	if err := as2.load(); err != nil {
		t.Fatal(err)
	}
	if len(as2.state) == 0 {
		t.Error("告警状态没有持久化")
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	stateLock.Lock()
	deviceTrendMap = map[string][]TrendPoint{"dev1": {{TS: 1, Time: "10:00:00", CPU: 30}}}
	portHistory = map[string]PortSnapshot{"dev1:1": {InOctets: 100, LastSeen: time.Now().Unix()}}
	stateLock.Unlock()

	saveHistory(dir, 100)

	stateLock.Lock()
	deviceTrendMap = map[string][]TrendPoint{}
	portHistory = map[string]PortSnapshot{}
	stateLock.Unlock()

	devs, points := loadHistory(dir)
	if devs != 1 || points != 1 {
		t.Fatalf("历史数据没有恢复: devs=%d points=%d", devs, points)
	}
	stateLock.RLock()
	defer stateLock.RUnlock()
	if deviceTrendMap["dev1"][0].CPU != 30 {
		t.Error("趋势点内容不正确")
	}
	if portHistory["dev1:1"].InOctets != 100 {
		t.Error("速率基线没有恢复, 重启后第一轮又要等热身")
	}
}

func TestPruneHistoryDropsDeletedDevices(t *testing.T) {
	stateLock.Lock()
	deviceTrendMap = map[string][]TrendPoint{"keep": {{CPU: 1}}, "gone": {{CPU: 2}}}
	portHistory = map[string]PortSnapshot{"keep:1": {}, "gone:1": {}}
	stateLock.Unlock()

	pruneHistory(map[string]bool{"keep": true})

	stateLock.RLock()
	defer stateLock.RUnlock()
	if _, ok := deviceTrendMap["gone"]; ok {
		t.Error("已删除设备的趋势数据应被清理")
	}
	if _, ok := portHistory["gone:1"]; ok {
		t.Error("已删除设备的计数器基线应被清理")
	}
	if _, ok := deviceTrendMap["keep"]; !ok {
		t.Error("在用设备的数据被误删")
	}
}

func TestSecretBoxTamperDetection(t *testing.T) {
	dir := t.TempDir()
	box, err := loadSecretBox(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.seal("public")
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "public" {
		t.Fatal("密文不应等于明文")
	}
	got, err := box.open(sealed)
	if err != nil || got != "public" {
		t.Fatalf("解密失败: %v %q", err, got)
	}
	// 换一把密钥就打不开了, 证明数据确实被绑定到 secret.key
	other, err := loadSecretBox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.open(sealed); err == nil {
		t.Error("用别的密钥不应能解密")
	}
}

func TestResolveFDBPrefersAccessOverUplink(t *testing.T) {
	// mac1 只在一个口学到 -> 直接采用
	// mac2 同时在"接入口 1"和"上联口 49"都学到过 -> 应该选接入口
	// mac3 只在两个上联口都学到过(冗余链路可见) -> 应该选流量更大的那条, 并标记为有歧义
	fdb := fdbResult{
		macLocations: map[string][]string{
			"AA:AA:AA:AA:AA:AA": {"1"},
			"BB:BB:BB:BB:BB:BB": {"1", "49"},
			"CC:CC:CC:CC:CC:CC": {"49", "50"},
		},
		portMACs: map[string]int{"1": 1, "49": 20, "50": 5},
		macVlan:  map[string]string{},
	}
	ifs := map[string]*ifInfo{
		"1":  {Index: "1", Descr: "Gi0/1"},
		"49": {Index: "49", Descr: "Te0/49"},
		"50": {Index: "50", Descr: "Te0/50"},
	}
	lldp := map[string]lldpNeighbor{
		"49": {Dev: "CORE", Port: "Gi0/1"},
		"50": {Dev: "CORE2", Port: "Gi0/2"},
	}
	macToIf, ambiguous := resolveFDB(fdb, uplinkPortSet(ifs, lldp, nil), 8)

	if macToIf["AA:AA:AA:AA:AA:AA"] != "1" {
		t.Errorf("单一位置应直接采用, 实际 %q", macToIf["AA:AA:AA:AA:AA:AA"])
	}
	if macToIf["BB:BB:BB:BB:BB:BB"] != "1" {
		t.Errorf("接入口+上联口都学到时应优先选接入口, 实际 %q", macToIf["BB:BB:BB:BB:BB:BB"])
	}
	if !ambiguous["BB:BB:BB:BB:BB:BB"] {
		t.Error("多个位置的 mac 应标记为有歧义")
	}
	if macToIf["CC:CC:CC:CC:CC:CC"] != "49" {
		t.Errorf("全是上联口时应选流量最大的那条, 实际 %q", macToIf["CC:CC:CC:CC:CC:CC"])
	}
	if ambiguous["AA:AA:AA:AA:AA:AA"] {
		t.Error("单一位置不应标记为有歧义")
	}
}

func TestCollectOptionsZeroValueMeansCollectEverything(t *testing.T) {
	// devices.json 从旧版本升级上来时没有 collect 字段, JSON 解出来是零值,
	// 零值必须等价于"什么都不跳过", 否则升级完所有可选数据都会突然消失。
	var c CollectOptions
	if c.SkipOptical || c.SkipLLDP || c.SkipRates || c.SkipHosts {
		t.Error("CollectOptions 零值应该表示全部采集, 不是全部跳过")
	}

	raw := `{"id":"x","ip":"10.1.1.1","community_enc":""}`
	var sd storedDevice
	if err := json.Unmarshal([]byte(raw), &sd); err != nil {
		t.Fatal(err)
	}
	if sd.Collect.SkipHosts {
		t.Error("旧格式 JSON(没有 collect 字段) 解出来不应该导致 MAC/ARP 被跳过")
	}
}

func TestSummaryPayloadOmitsFullDetail(t *testing.T) {
	hub := &dataHub{}
	hub.set([]DeviceReport{{
		IP: "10.1.1.1", SysName: "SW1", IsAlive: true,
		Ports:        []PortDetail{{Name: "Gi0/1"}},
		HostMappings: []HostMappingEntry{{IP: "10.1.1.5"}},
		MACTable:     []MACEntry{{MAC: "AA:BB:CC:DD:EE:FF"}},
		Logs:         []string{"巡检完成"},
	}}, "2026-01-01 00:00:00")

	sum := hub.summaryPayload()
	for _, leak := range []string{"Gi0/1", "10.1.1.5", "AA:BB:CC:DD:EE:FF", "巡检完成"} {
		if bytes.Contains(sum, []byte(leak)) {
			t.Errorf("摘要接口不应该包含明细数据 %q, 会让 /api/data 越用越大: %s", leak, sum)
		}
	}

	detail, ok := hub.devicePayload("10.1.1.1")
	if !ok {
		t.Fatal("按 IP 查询详情应该成功")
	}
	if !bytes.Contains(detail, []byte("Gi0/1")) {
		t.Error("详情接口应该包含完整端口信息")
	}

	if _, ok := hub.devicePayload("10.9.9.9"); ok {
		t.Error("查询不存在的设备应该返回未找到")
	}
}

func TestTopologyNameMatching(t *testing.T) {
	if normalizeDevName("SW-CORE.office.local") != "sw-core" {
		t.Errorf("拓扑设备名归一化错误: %q", normalizeDevName("SW-CORE.office.local"))
	}
	if normalizeDevName("sw-core") != normalizeDevName("SW-CORE") {
		t.Error("拓扑设备名匹配应该大小写不敏感")
	}
}

func TestEnrichCrossDeviceHosts(t *testing.T) {
	// 核心交换机(三层): 有 ARP, 但 MAC 表里这个 MAC 挂在上联口上
	// 接入交换机(二层): 完全没有 ARP, 但 MAC 表里这个 MAC 在真实接入口上
	core := DeviceReport{
		IP: "10.0.0.1", SysName: "Core",
		HostMappings: []HostMappingEntry{
			{IP: "10.0.0.50", MAC: "AA:AA:AA:AA:AA:AA", RealPort: "Gi0/1(上联口)", PortKind: "上联/级联口"},
		},
		MACTable: []MACEntry{
			{MAC: "AA:AA:AA:AA:AA:AA", Port: "Gi0/1(上联口)", PortKind: "上联/级联口", IsUplink: true},
		},
	}
	access := DeviceReport{
		IP: "10.0.0.2", SysName: "Access",
		HostMappings: []HostMappingEntry{}, // 纯二层, 没有 ARP
		MACTable: []MACEntry{
			{MAC: "AA:AA:AA:AA:AA:AA", VlanID: "10", Port: "Gi0/5", PortKind: "接入口", IsUplink: false},
		},
	}
	reports := []DeviceReport{core, access}
	enrichCrossDeviceHosts(reports)

	accessResult := reports[1]
	if len(accessResult.HostMappings) != 1 {
		t.Fatalf("接入交换机应该借到一条 host mapping, 实际 %d 条", len(accessResult.HostMappings))
	}
	h := accessResult.HostMappings[0]
	if h.IP != "10.0.0.50" {
		t.Errorf("跨设备关联的 IP 不对: %q", h.IP)
	}
	if h.IPSource != "Core" {
		t.Errorf("应该标注 IP 来源设备, 实际 %q", h.IPSource)
	}
	if h.RealPort != "Gi0/5" || h.PortKind != "接入口" {
		t.Errorf("端口应该用接入交换机自己的真实定位, 不是核心的上联口: port=%q kind=%q", h.RealPort, h.PortKind)
	}

	// 核心交换机自己已经有这条 ARP 记录, 不应该被重复添加或覆盖
	coreResult := reports[0]
	if len(coreResult.HostMappings) != 1 {
		t.Errorf("核心交换机自己已有的记录不应该被改动, 实际 %d 条", len(coreResult.HostMappings))
	}
	if coreResult.HostMappings[0].IPSource != "" {
		t.Error("本机自己查到的 ARP 不应该被标记为跨设备来源")
	}

	if len(accessResult.Diagnostics) == 0 {
		t.Error("应该有一条诊断信息说明借用了跨设备数据")
	}
}

func TestEnrichCrossDeviceHostsNoInfiniteChaining(t *testing.T) {
	// A 提供权威 ARP, B 从 A 借到, C 不应该能从 B 借到的(已经是借来的)再借一次
	// 保证信息只跨一跳, 不会以讹传讹越传越不可信
	a := DeviceReport{
		IP: "1.1.1.1", SysName: "A",
		HostMappings: []HostMappingEntry{{IP: "9.9.9.9", MAC: "BB:BB:BB:BB:BB:BB", RealPort: "p1"}},
		MACTable:     []MACEntry{{MAC: "BB:BB:BB:BB:BB:BB", Port: "p1"}},
	}
	b := DeviceReport{
		IP: "1.1.1.2", SysName: "B",
		HostMappings: []HostMappingEntry{},
		MACTable:     []MACEntry{{MAC: "BB:BB:BB:BB:BB:BB", Port: "p2"}},
	}
	reports := []DeviceReport{a, b}
	enrichCrossDeviceHosts(reports)
	if reports[1].HostMappings[0].IPSource != "A" {
		t.Errorf("B 应该直接从权威来源 A 借到, 实际来源 %q", reports[1].HostMappings[0].IPSource)
	}
}

func TestTopologyStorePositionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ts := newTopologyStore(dir)
	if err := ts.SetPosition("10.1.1.1", 42.5, 88.1); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetPosition("10.1.1.2", 150, -20); err != nil { // 越界应该被夹在 0-100
		t.Fatal(err)
	}

	ts2 := newTopologyStore(dir)
	if err := ts2.load(); err != nil {
		t.Fatal(err)
	}
	pos, _ := ts2.Snapshot()
	if pos["10.1.1.1"].X != 42.5 || pos["10.1.1.1"].Y != 88.1 {
		t.Errorf("位置没有正确落盘/读回: %+v", pos["10.1.1.1"])
	}
	if pos["10.1.1.2"].X != 100 || pos["10.1.1.2"].Y != 0 {
		t.Errorf("越界坐标应该被夹到 0-100 之间, 实际 %+v", pos["10.1.1.2"])
	}

	ts2.DropDevice("10.1.1.1")
	pos2, _ := ts2.Snapshot()
	if _, ok := pos2["10.1.1.1"]; ok {
		t.Error("删除设备后残留坐标应该被清理")
	}
}

func TestTopologyStoreImageValidation(t *testing.T) {
	dir := t.TempDir()
	ts := newTopologyStore(dir)

	// 一个最小的合法 PNG (1x1 像素) 的字节序列
	png1x1 := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
	if err := ts.SetImage(png1x1, "image/png"); err != nil {
		t.Fatalf("合法 PNG 应该被接受: %v", err)
	}
	_, hasBg := ts.Snapshot()
	if !hasBg {
		t.Error("上传后应该报告已有背景图")
	}
	path, ok := ts.imagePath()
	if !ok {
		t.Fatal("应该能拿到图片路径")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("图片文件应该落盘: %v", err)
	}

	// 不是图片的数据应该被拒绝, 不能让人拿这个接口当任意文件上传用
	if err := ts.SetImage([]byte("not an image, just text"), "image/png"); err == nil {
		t.Error("非图片数据应该被拒绝, 即使声称的 content-type 是图片")
	}

	// 超过大小上限应该被拒绝
	huge := make([]byte, 9*1024*1024)
	if err := ts.SetImage(huge, "image/png"); err == nil {
		t.Error("超过 8MB 的图片应该被拒绝")
	}

	if err := ts.ClearImage(); err != nil {
		t.Fatal(err)
	}
	if _, hasBg := ts.Snapshot(); hasBg {
		t.Error("清除后不应该再报告有背景图")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("清除背景图应该删掉磁盘上的文件")
	}
}

// TestHandleTopologyMergesAggregatedLinks 覆盖用户反馈的那个 bug:
// 两台设备之间如果是聚合链路(比如 8 个物理成员口), LLDP 会给每个成员口
// 各上报一条邻居记录, 以前按"设备对"去重只保留第一条, 拓扑图上看起来
// 就是"聚合链路却只有一根线、一个口, 而且带宽只反映 1/8"。现在两侧都
// 能定位到聚合组时应该合并成一条 IsAgg=true 的逻辑链路, 成员口和速率
// 都要能对上。
func TestHandleTopologyMergesAggregatedLinks(t *testing.T) {
	hub := &dataHub{}
	hub.set([]DeviceReport{
		{
			IP: "10.0.0.1", SysName: "Core", IsAlive: true,
			AggGroups: []AggGroupDetail{{Name: "Ag9", Members: []string{"Te1/8/9", "Te2/8/9"}}},
			Ports: []PortDetail{
				{Name: "Te1/8/9", SpeedMbps: 10000, InSpeedMbps: 100, OutSpeedMbps: 50, LLDPRemoteDev: "Access", LLDPRemotePort: "Te1/0/9"},
				{Name: "Te2/8/9", SpeedMbps: 10000, InSpeedMbps: 80, OutSpeedMbps: 40, LLDPRemoteDev: "Access", LLDPRemotePort: "Te2/0/9"},
			},
		},
		{
			IP: "10.0.0.2", SysName: "Access", IsAlive: true,
			AggGroups: []AggGroupDetail{{Name: "Ag1", Members: []string{"Te1/0/9", "Te2/0/9"}}},
			Ports: []PortDetail{
				{Name: "Te1/0/9", SpeedMbps: 10000},
				{Name: "Te2/0/9", SpeedMbps: 10000},
			},
		},
	}, "2026-01-01 00:00:00")

	s := &server{hub: hub, topology: newTopologyStore(t.TempDir())}
	req := httptest.NewRequest("GET", "/api/topology", nil)
	rec := httptest.NewRecorder()
	s.handleTopology(rec, req)

	var payload topoPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(payload.Links) != 1 {
		t.Fatalf("两个成员口应该合并成 1 条逻辑链路, 实际 %d 条: %+v", len(payload.Links), payload.Links)
	}
	link := payload.Links[0]
	if !link.IsAgg {
		t.Error("两侧都能定位到聚合组, 应该标记为聚合链路")
	}
	if len(link.AMembers) != 2 || len(link.BMembers) != 2 {
		t.Errorf("应该保留 2 个成员口, 实际 A=%v B=%v", link.AMembers, link.BMembers)
	}
	if link.ASpeedMbps != 20000 {
		t.Errorf("聚合链路带宽应该是两个成员口求和 20000, 实际 %d", link.ASpeedMbps)
	}
	if link.AInMbps != 180 || link.AOutMbps != 90 {
		t.Errorf("聚合链路收发速率应该是成员口求和, 实际 in=%v out=%v", link.AInMbps, link.AOutMbps)
	}
}

// TestHandleTopologyKeepsIndependentLinksSeparate 确认没有识别到聚合组的
// 情况下(比如两条独立物理链路, 或者聚合组信息缺失), 不会被臆造成一条
// 聚合链路, 而是继续按旧行为各画各的。
func TestHandleTopologyKeepsIndependentLinksSeparate(t *testing.T) {
	hub := &dataHub{}
	hub.set([]DeviceReport{
		{
			IP: "10.0.0.1", SysName: "SW1", IsAlive: true,
			Ports: []PortDetail{
				{Name: "Gi0/1", SpeedMbps: 1000, LLDPRemoteDev: "SW2", LLDPRemotePort: "Gi0/1"},
				{Name: "Gi0/2", SpeedMbps: 1000, LLDPRemoteDev: "SW2", LLDPRemotePort: "Gi0/2"},
			},
		},
		{
			IP: "10.0.0.2", SysName: "SW2", IsAlive: true,
			Ports: []PortDetail{
				{Name: "Gi0/1", SpeedMbps: 1000},
				{Name: "Gi0/2", SpeedMbps: 1000},
			},
		},
	}, "2026-01-01 00:00:00")

	s := &server{hub: hub, topology: newTopologyStore(t.TempDir())}
	req := httptest.NewRequest("GET", "/api/topology", nil)
	rec := httptest.NewRecorder()
	s.handleTopology(rec, req)

	var payload topoPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(payload.Links) != 2 {
		t.Fatalf("没有聚合组信息时两条物理链路应该各画各的, 实际 %d 条", len(payload.Links))
	}
	for _, l := range payload.Links {
		if l.IsAgg {
			t.Error("没有识别到聚合组不应该臆造出聚合链路")
		}
	}
}

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"10.1.2.3": true, "172.16.0.1": true, "172.31.255.255": true, "172.32.0.1": false,
		"192.168.1.1": true, "8.8.8.8": false, "1.1.1.1": false, "127.0.0.1": true, "169.254.1.1": true,
	}
	for ip, want := range cases {
		if got := isPrivateIP(ip); got != want {
			t.Errorf("isPrivateIP(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestClassifySuspicious(t *testing.T) {
	hit, reason := classifySuspicious(FlowRecord{DstPort: 445, Packets: 1})
	if !hit || reason == "" {
		t.Error("命中敏感端口且包数很少应该标为疑似扫描")
	}
	if hit, _ := classifySuspicious(FlowRecord{DstPort: 445, Packets: 50}); hit {
		t.Error("包数很多的正常 SMB 会话不应该被标为疑似扫描")
	}
	if hit, _ := classifySuspicious(FlowRecord{DstPort: 80, Packets: 1}); hit {
		t.Error("非敏感端口不应该被标为疑似扫描")
	}
}

func TestClassifyPortBucket(t *testing.T) {
	cases := []struct {
		proto string
		port  int
		want  string
	}{
		{"TCP", 443, "HTTPS"}, {"TCP", 22, "SSH"}, {"UDP", 53, "DNS"},
		{"TCP", 3389, "RDP"}, {"TCP", 51234, "高位端口"},
	}
	for _, c := range cases {
		if got := classifyPortBucket(c.proto, c.port); got != c.want {
			t.Errorf("classifyPortBucket(%s,%d) = %q, want %q", c.proto, c.port, got, c.want)
		}
	}
}

// ----------------- 第八批: 聚合口识别 / 拓扑聚合合并 / 速率时间窗 / sFlow 扩展 -----------------

// TestIsAggInterface: 锐捷部分型号的 AggregatePort ifType 报 6, 不能只靠
// ifType=161 判断; 同时聚合口不能被算成物理口。
func TestIsAggInterface(t *testing.T) {
	cases := []struct {
		e    ifInfo
		want bool
	}{
		{ifInfo{Descr: "AggregatePort 1", Type: 6}, true},
		{ifInfo{Name: "Ag3", Type: 53}, true},
		{ifInfo{Descr: "Eth-Trunk10", Type: 161}, true},
		{ifInfo{Descr: "Bridge-Aggregation2"}, true},
		{ifInfo{Descr: "weird-lag-name", Type: 161}, true},
		{ifInfo{Descr: "GigabitEthernet 0/1", Type: 6}, false},
		{ifInfo{Descr: "TenGigabitEthernet 1/0/49", Type: 6}, false},
	}
	for _, c := range cases {
		e := c.e
		if got := isAggInterface(&e); got != c.want {
			t.Errorf("isAggInterface(%q/%q type=%d) = %v, want %v", e.Descr, e.Name, e.Type, got, c.want)
		}
	}
	ap := ifInfo{Descr: "AggregatePort 1", Type: 6}
	if isPhysicalPort(&ap, true) {
		t.Error("ifType=6 的 AggregatePort 不应该被算成物理口")
	}
}

func TestMissingAggMembers(t *testing.T) {
	aggs := map[string]*ifInfo{"100": {Descr: "AggregatePort 1"}, "101": {Descr: "AggregatePort 2"}}
	if !missingAggMembers(aggs, map[string]string{"1": "AggregatePort 1"}) {
		t.Error("AggregatePort 2 一个成员都没有, 应该判定为还缺成员")
	}
	if missingAggMembers(aggs, map[string]string{"1": "AggregatePort 1", "2": "AggregatePort 2"}) {
		t.Error("两个聚合口都有成员了, 不应该再判定为缺")
	}
}

// TestHandleTopologyOneSidedAggWithShortPortNames 覆盖两个现实场景:
//  1. 对端 LLDP 上报的端口名是缩写(XGE1/0/1、Te1/1), 跟本端 ifDescr 全称对不上;
//  2. 只有一侧(锐捷核心)识别到了聚合组, 另一侧(接入)没识别到。
//
// 期望: 合并成 1 条聚合链路, 两个成员各算一次(不因为两端各报一次、名字写法不同而翻倍)。
func TestHandleTopologyOneSidedAggWithShortPortNames(t *testing.T) {
	hub := &dataHub{}
	hub.set([]DeviceReport{
		{
			IP: "10.0.0.1", SysName: "Core", IsAlive: true,
			AggGroups: []AggGroupDetail{{Name: "AggregatePort 1", Members: []string{"TenGigabitEthernet 1/1", "TenGigabitEthernet 1/2"}}},
			Ports: []PortDetail{
				{Name: "TenGigabitEthernet 1/1", SpeedMbps: 10000, InSpeedMbps: 100, LLDPRemoteDev: "Access", LLDPRemotePort: "XGE1/0/1"},
				{Name: "TenGigabitEthernet 1/2", SpeedMbps: 10000, InSpeedMbps: 50, LLDPRemoteDev: "Access", LLDPRemotePort: "XGE1/0/2"},
			},
		},
		{
			IP: "10.0.0.2", SysName: "Access", IsAlive: true,
			Ports: []PortDetail{
				{Name: "Ten-GigabitEthernet1/0/1", SpeedMbps: 10000, LLDPRemoteDev: "Core", LLDPRemotePort: "Te1/1"},
				{Name: "Ten-GigabitEthernet1/0/2", SpeedMbps: 10000, LLDPRemoteDev: "Core", LLDPRemotePort: "Te1/2"},
			},
		},
	}, "2026-01-01 00:00:00")

	s := &server{hub: hub, topology: newTopologyStore(t.TempDir())}
	rec := httptest.NewRecorder()
	s.handleTopology(rec, httptest.NewRequest("GET", "/api/topology", nil))
	var payload topoPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(payload.Links) != 1 {
		t.Fatalf("应该合并成 1 条聚合链路, 实际 %d 条: %+v", len(payload.Links), payload.Links)
	}
	l := payload.Links[0]
	if !l.IsAgg || l.APort != "AggregatePort 1" {
		t.Errorf("应该是聚合链路且 A 侧显示聚合口名, 实际 is_agg=%v a_port=%q", l.IsAgg, l.APort)
	}
	if len(l.AMembers) != 2 {
		t.Errorf("两个成员口各算一次, 实际 %v", l.AMembers)
	}
	if l.ASpeedMbps != 20000 || l.BSpeedMbps != 20000 {
		t.Errorf("两侧带宽都应该是 20000(对端速率靠缩写名匹配查到), 实际 A=%d B=%d", l.ASpeedMbps, l.BSpeedMbps)
	}
	if !strings.Contains(l.BPort, "Ten-GigabitEthernet1/0/1") {
		t.Errorf("没识别到聚合口的一侧应该列出成员口全称, 实际 %q", l.BPort)
	}
}

func TestSampleSecondsPrefersMillis(t *testing.T) {
	if got := sampleSeconds(61500, 1500, 999, 0); got != 60 {
		t.Errorf("有毫秒时刻时应该用毫秒差, 实际 %v", got)
	}
	if got := sampleSeconds(61500, 0, 100, 40); got != 60 {
		t.Errorf("旧快照没有毫秒时刻时应该退回整秒 LastSeen, 实际 %v", got)
	}
}

// ---- sFlow 报文构造工具 ----

func xdr32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.BigEndian, v) }
func xdr64(b *bytes.Buffer, v uint64) { _ = binary.Write(b, binary.BigEndian, v) }
func xdrOpaque(b *bytes.Buffer, data []byte) {
	b.Write(data)
	for i := 0; i < (4-len(data)%4)%4; i++ {
		b.WriteByte(0)
	}
}
func xdrRecord(b *bytes.Buffer, format uint32, body []byte) {
	xdr32(b, format)
	xdr32(b, uint32(len(body)))
	xdrOpaque(b, body)
}

func buildClientHello(sni string) []byte {
	var ext bytes.Buffer
	name := []byte(sni)
	_ = binary.Write(&ext, binary.BigEndian, uint16(0))           // server_name
	_ = binary.Write(&ext, binary.BigEndian, uint16(len(name)+5)) // ext len
	_ = binary.Write(&ext, binary.BigEndian, uint16(len(name)+3)) // list len
	ext.WriteByte(0)                                              // host_name
	_ = binary.Write(&ext, binary.BigEndian, uint16(len(name)))   //
	ext.Write(name)
	var hello bytes.Buffer
	hello.Write([]byte{0x03, 0x03})
	hello.Write(make([]byte, 32)) // random
	hello.WriteByte(0)            // session id
	hello.Write([]byte{0x00, 0x02, 0x13, 0x01})
	hello.Write([]byte{0x01, 0x00})
	_ = binary.Write(&hello, binary.BigEndian, uint16(ext.Len()))
	hello.Write(ext.Bytes())
	var out bytes.Buffer
	out.Write([]byte{0x16, 0x03, 0x01})
	_ = binary.Write(&out, binary.BigEndian, uint16(hello.Len()+4))
	out.WriteByte(0x01)
	n := hello.Len()
	out.Write([]byte{byte(n >> 16), byte(n >> 8), byte(n)})
	out.Write(hello.Bytes())
	return out.Bytes()
}

// buildTaggedTCPFrame: 以太网 + 802.1Q(VLAN) + IPv4 + TCP + payload
func buildTaggedTCPFrame(vlan uint16, src, dst [4]byte, sport, dport uint16, flags byte, payload []byte) []byte {
	var f bytes.Buffer
	f.Write([]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}) // dst mac
	f.Write([]byte{0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb}) // src mac
	_ = binary.Write(&f, binary.BigEndian, uint16(0x8100))
	_ = binary.Write(&f, binary.BigEndian, vlan)
	_ = binary.Write(&f, binary.BigEndian, uint16(0x0800))
	ip := make([]byte, 20)
	ip[0], ip[9] = 0x45, 6
	copy(ip[12:16], src[:])
	copy(ip[16:20], dst[:])
	f.Write(ip)
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], sport)
	binary.BigEndian.PutUint16(tcp[2:4], dport)
	tcp[12], tcp[13] = 5<<4, flags
	f.Write(tcp)
	f.Write(payload)
	return f.Bytes()
}

func buildSflowDatagram(frame []byte, frameLen, rate uint32, withCounters bool) []byte {
	var raw bytes.Buffer
	xdr32(&raw, 1) // ethernet
	xdr32(&raw, frameLen)
	xdr32(&raw, 4)
	xdr32(&raw, uint32(len(frame)))
	xdrOpaque(&raw, frame)

	var fs bytes.Buffer
	xdr32(&fs, 1)    // seq
	xdr32(&fs, 5)    // source id
	xdr32(&fs, rate) // sampling rate
	xdr32(&fs, 1000) // sample pool
	xdr32(&fs, 0)    // drops
	xdr32(&fs, 5)    // input ifIndex
	xdr32(&fs, 7)    // output ifIndex
	xdr32(&fs, 1)    // records
	xdrRecord(&fs, 1, raw.Bytes())

	var cs bytes.Buffer
	if withCounters {
		var gc bytes.Buffer
		xdr32(&gc, 5)            // ifIndex
		xdr32(&gc, 6)            // ifType
		xdr64(&gc, 10000000000)  // 10G
		xdr32(&gc, 1)            // direction
		xdr32(&gc, 3)            // admin+oper up
		xdr64(&gc, 123456789)    // in octets
		for i := 0; i < 6; i++ { // in ucast/mcast/bcast/discards/errors/unknown
			xdr32(&gc, uint32(i+1))
		}
		xdr64(&gc, 987654321) // out octets
		for i := 0; i < 6; i++ {
			xdr32(&gc, uint32(i+10))
		}
		xdr32(&cs, 1) // seq
		xdr32(&cs, 5) // source id
		xdr32(&cs, 1) // records
		xdrRecord(&cs, 1, gc.Bytes())
	}

	var d bytes.Buffer
	xdr32(&d, 5) // version
	xdr32(&d, 1) // ipv4 agent
	d.Write([]byte{10, 9, 9, 9})
	xdr32(&d, 0)   // sub agent
	xdr32(&d, 1)   // seq
	xdr32(&d, 100) // uptime
	if withCounters {
		xdr32(&d, 2)
	} else {
		xdr32(&d, 1)
	}
	xdrRecord(&d, 1, fs.Bytes())
	if withCounters {
		xdrRecord(&d, 2, cs.Bytes())
	}
	return d.Bytes()
}

func TestParseSflowRawHeaderWithVlanTLSAndCounters(t *testing.T) {
	frame := buildTaggedTCPFrame(100, [4]byte{10, 1, 1, 5}, [4]byte{1, 2, 3, 4}, 50000, 443, 0x18, buildClientHello("www.example.com"))
	pkt := buildSflowDatagram(frame, 1500, 1000, true)

	res := parseSflowV5(pkt, "10.9.9.9", true)
	if len(res.flows) != 1 {
		t.Fatalf("应该解出 1 条流记录, 实际 %d", len(res.flows))
	}
	r := res.flows[0]
	if r.VLAN != 100 || r.SrcIP != "10.1.1.5" || r.DstIP != "1.2.3.4" || r.DstPort != 443 || r.Protocol != "TCP" {
		t.Errorf("五元组/VLAN 解析不对: %+v", r)
	}
	if r.AppProto != "TLS" || r.AppHost != "www.example.com" {
		t.Errorf("应该从 ClientHello 里认出 SNI, 实际 proto=%q host=%q", r.AppProto, r.AppHost)
	}
	if r.TCPFlags != 0x18 || r.SrcMAC != "66:77:88:99:aa:bb" {
		t.Errorf("TCP 标志/MAC 不对: flags=%#x mac=%s", r.TCPFlags, r.SrcMAC)
	}
	if r.Bytes != 1500*1000 || r.Packets != 1000 || r.InIfIndex != "5" || r.Source != "sflow" || r.TS == 0 {
		t.Errorf("抽样折算/接口/来源字段不对: %+v", r)
	}
	if len(res.counters) != 1 || res.counters[0].ifIndex != "5" || res.counters[0].raw.inOct != 123456789 ||
		res.counters[0].raw.inDisc != 4 || res.counters[0].raw.outDisc != 13 || !res.counters[0].raw.operUp {
		t.Errorf("接口计数器解析不对: %+v", res.counters)
	}

	off := parseSflowV5(pkt, "10.9.9.9", false)
	if len(off.flows) != 1 || off.flows[0].AppHost != "" || off.flows[0].AppProto != "" {
		t.Errorf("L7 开关关闭时不应该解析域名, 实际 %+v", off.flows)
	}
}

func TestParseL7Helpers(t *testing.T) {
	dns := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0,
		3, 'w', 'w', 'w', 5, 'b', 'a', 'i', 'd', 'u', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	if name, ok := parseDNSQueryName(dns); !ok || name != "www.baidu.com" {
		t.Errorf("DNS 查询名解析不对: %q %v", name, ok)
	}
	if _, ok := parseDNSQueryName(dns[:18]); ok {
		t.Error("被截断的 DNS 报文不应该给出半截域名")
	}

	hello := buildClientHello("api.example.cn")
	if sni, isHello := parseTLSClientHello(hello); !isHello || sni != "api.example.cn" {
		t.Errorf("SNI 解析不对: %q %v", sni, isHello)
	}
	if sni, isHello := parseTLSClientHello(hello[:60]); !isHello || sni != "" {
		t.Errorf("截断的 ClientHello 应该识别为 TLS 但 SNI 为空, 实际 %q %v", sni, isHello)
	}

	req := []byte("GET /index.html?a=1 HTTP/1.1\r\nUser-Agent: x\r\nHost: Intranet.Example.com\r\n\r\n")
	host, path, ok := parseHTTPRequest(req)
	if !ok || host != "intranet.example.com" || path != "GET /index.html?a=1" {
		t.Errorf("HTTP 请求解析不对: host=%q path=%q ok=%v", host, path, ok)
	}
	if _, _, ok := parseHTTPRequest([]byte("HTTP/1.1 200 OK\r\n")); ok {
		t.Error("HTTP 响应不应该被当成请求")
	}
}

// TestNetflowV5AppliesSamplingInterval: 路由器开了抽样 NetFlow 时, 头部
// sampling_interval 低 14 位是 N, 包数/字节数要乘回 N。
func TestNetflowV5AppliesSamplingInterval(t *testing.T) {
	buf := make([]byte, 24+48)
	binary.BigEndian.PutUint16(buf[0:2], 5)
	binary.BigEndian.PutUint16(buf[2:4], 1)
	binary.BigEndian.PutUint16(buf[22:24], 0x4000|100) // 模式 01, 间隔 100
	rec := buf[24:]
	copy(rec[0:4], []byte{10, 0, 0, 1})
	copy(rec[4:8], []byte{10, 0, 0, 2})
	binary.BigEndian.PutUint32(rec[16:20], 2)
	binary.BigEndian.PutUint32(rec[20:24], 300)
	binary.BigEndian.PutUint16(rec[34:36], 22)
	rec[37], rec[38] = 0x02, 6

	out := parseNetflowV5(buf, "10.9.9.9")
	if len(out) != 1 {
		t.Fatalf("应该解出 1 条, 实际 %d", len(out))
	}
	if out[0].Packets != 200 || out[0].Bytes != 30000 || out[0].SamplingPct != 1 {
		t.Errorf("应该按 1/100 抽样折算, 实际 packets=%d bytes=%d pct=%d", out[0].Packets, out[0].Bytes, out[0].SamplingPct)
	}
	if out[0].TCPFlags != 0x02 || out[0].Source != "netflow" {
		t.Errorf("TCP 标志/来源不对: %+v", out[0])
	}
}

func TestFlowStoreRingAndCounters(t *testing.T) {
	f := newFlowStore("", true)
	for i := 0; i < flowRingSize+3; i++ {
		f.add(FlowRecord{DeviceIP: "d", TS: int64(i + 1)})
	}
	got := f.get("d")
	if len(got) != flowRingSize || got[0].TS != 4 || got[len(got)-1].TS != int64(flowRingSize+3) {
		t.Errorf("环形缓冲应该只保留最新的 %d 条且按时间顺序, 实际 len=%d first=%d last=%d",
			flowRingSize, len(got), got[0].TS, got[len(got)-1].TS)
	}

	base := ifCounterRaw{speed: 1e9, operUp: true, inOct: 0, outOct: 0, outDisc: 5, atMs: 1_000_000}
	f.addCounter("d", "5", base)
	if len(f.counterStats("d")) != 0 {
		t.Error("只有一次计数器样本时还算不出速率, 不应该返回")
	}
	next := base
	next.inOct = 12_500_000 // 10 秒 12.5MB = 10 Mbps
	next.outDisc = 25
	next.atMs = base.atMs + 10_000
	f.addCounter("d", "5", next)
	st, ok := f.counterStats("d")["d|5"]
	if !ok {
		t.Fatal("第二次样本之后应该有速率")
	}
	if st.InMbps != 10 || st.OutDiscards != 20 || st.InUtil != 1 {
		t.Errorf("速率/丢包增量/利用率不对: %+v", st)
	}
}

func TestFlowL7SettingPersists(t *testing.T) {
	dir := t.TempDir()
	f := newFlowStore(dir, true)
	if !f.l7Enabled() {
		t.Fatal("没有落盘设置时应该用默认值(开启)")
	}
	if err := f.setL7(false); err != nil {
		t.Fatal(err)
	}
	if newFlowStore(dir, true).l7Enabled() {
		t.Error("页面上关掉之后, 重启应该保持关闭, 不能被默认值覆盖")
	}
}

// TestHandleFlowsWindowAndSynScan: 时间窗口外的记录不参与统计; 同一个源
// 对大量不同目标发纯 SYN 会被列为疑似扫描源; 接口号会被换成端口名。
func TestHandleFlowsWindowAndSynScan(t *testing.T) {
	hub := &dataHub{}
	hub.set([]DeviceReport{{IP: "10.9.9.9", SysName: "SW", Ports: []PortDetail{{Index: "5", Name: "Gi0/5"}}}}, "t")
	store := newFlowStore("", true)
	now := time.Now().UnixMilli()
	store.add(FlowRecord{DeviceIP: "10.9.9.9", SrcIP: "10.0.0.1", DstIP: "8.8.8.8", Protocol: "UDP", DstPort: 53,
		Bytes: 999999, TS: now - 3600*1000, Source: "sflow"}) // 一小时前, 不在 5 分钟窗口里
	for i := 0; i < 12; i++ {
		store.add(FlowRecord{DeviceIP: "10.9.9.9", SrcIP: "10.0.0.66", DstIP: fmt.Sprintf("10.0.1.%d", i), DstPort: 445,
			Protocol: "TCP", TCPFlags: tcpSYN, Bytes: 64, Packets: 1, TS: now - int64(i)*1000, Source: "sflow", InIfIndex: "5"})
	}
	store.add(FlowRecord{DeviceIP: "10.9.9.9", SrcIP: "10.0.0.2", DstIP: "1.1.1.1", DstPort: 443, Protocol: "TCP",
		TCPFlags: 0x18, AppProto: "TLS", AppHost: "example.com", Bytes: 1000, TS: now, Source: "sflow"})

	s := &server{hub: hub, flows: store, cfg: &Config{Collectors: CollectorsConfig{SflowPort: 6343}}}
	rec := httptest.NewRecorder()
	s.handleFlows(rec, httptest.NewRequest("GET", "/api/flows?ip=10.9.9.9&window=300", nil))
	var resp struct {
		Summary     struct{ Samples int } `json:"summary"`
		SynSuspects []flowSynSuspect      `json:"syn_suspects"`
		TopDomains  []flowDomainStat      `json:"top_domains"`
		Records     []FlowRecord          `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Summary.Samples != 13 {
		t.Errorf("5 分钟窗口里应该只有 13 条, 实际 %d", resp.Summary.Samples)
	}
	if len(resp.SynSuspects) != 1 || resp.SynSuspects[0].SrcIP != "10.0.0.66" || resp.SynSuspects[0].Targets != 12 {
		t.Errorf("应该识别出 1 个疑似扫描源 10.0.0.66(12 个目标), 实际 %+v", resp.SynSuspects)
	}
	if len(resp.TopDomains) != 1 || resp.TopDomains[0].Host != "example.com" {
		t.Errorf("域名统计不对: %+v", resp.TopDomains)
	}
	named, marked := false, 0
	for _, r := range resp.Records {
		if r.InIfIndex == "5" && r.InIfName == "Gi0/5" {
			named = true
		}
		if r.Suspicious {
			marked++
		}
	}
	if !named {
		t.Error("记录里的 ifIndex 5 应该被换成端口名 Gi0/5")
	}
	if marked != 12 {
		t.Errorf("扫描源的 12 个纯 SYN 样本都应该被标红, 实际 %d", marked)
	}
}
