package main

// 巡检报告模块。
//
//   - 机器报告: 直接从最近一轮采集结果(DeviceReport)按规则算出来——健康评分、
//     硬件/资源/端口/光模块/聚合/告警各章节、问题清单和处置建议。不依赖外网,
//     结果可复现。分"单台设备报告"和"全网报告"两种。
//   - AI 报告: 把机器报告的结构化结果交给大模型, 让它写"结论 + 风险排序 +
//     原因分析 + 处置建议"。支持 Anthropic Claude API 和 OpenAI 兼容接口
//     (DeepSeek / 通义千问 / 本地 Ollama / vLLM 等), 在页面「报告设置」里配置。
//   - 权限: 生成/删除/设置只有管理员(登录用户)能做; 游客能不能看由管理员决定——
//     总开关「允许游客查看巡检报告」+ 每份报告单独的「公开」开关, 两个都开游客才看得到。
//   - 定时: 可以设成每天固定时刻自动生成全网报告(可选每台设备各一份、可选附带 AI 分析)。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ReportItem struct {
	Level string `json:"level"` // ok / info / warn / bad
	Text  string `json:"text"`
}

type ReportTable struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

type ReportSection struct {
	Title string       `json:"title"`
	Note  string       `json:"note,omitempty"` // 章节说明(小字)
	Chart *ReportChart `json:"chart,omitempty"`
	Items []ReportItem `json:"items,omitempty"`
	Table *ReportTable `json:"table,omitempty"`
}

// ReportChart 是报告里的简单图表, 页面上画成 SVG(打印 / 导出 PDF 也清楚)。
type ReportChart struct {
	Type   string    `json:"type"` // bar(竖柱) / hbar(横条) / stack(每天严重+警告堆叠)
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"`
	Extra  []float64 `json:"extra,omitempty"` // stack 的第二组(警告)
	Unit   string    `json:"unit,omitempty"`
	Max    float64   `json:"max,omitempty"` // 固定满刻度(比如百分比 100), 0 = 按数据自动
	Colors []string  `json:"colors,omitempty"`
}

// InspReport 是一份巡检报告。
type InspReport struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // device / network
	DeviceIP   string `json:"device_ip,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	Title      string `json:"title"`
	CreatedAt  string `json:"created_at"`
	CreatedBy  string `json:"created_by"`
	Public     bool   `json:"public"`
	Auto       bool   `json:"auto"`

	Score    int             `json:"score"`
	Grade    string          `json:"grade"`
	Summary  []string        `json:"summary"`
	Sections []ReportSection `json:"sections"`
	Advice   []string        `json:"advice"`
	DataTime string          `json:"data_time"` // 使用的是哪一轮巡检的数据

	// Snapshot: 这份报告里有哪些问题(稳定的键), 下一份报告用来算"新增 / 已解决", 见 report_extra.go
	Snapshot *reportSnapshot `json:"snapshot,omitempty"`

	AIStatus string `json:"ai_status,omitempty"` // running / done / error
	AIText   string `json:"ai_text,omitempty"`   // Markdown
	AIModel  string `json:"ai_model,omitempty"`
	AIError  string `json:"ai_error,omitempty"`
	AIAt     string `json:"ai_at,omitempty"`
}

// reportMeta 是列表页用的轻量信息。
type reportMeta struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	DeviceIP   string `json:"device_ip,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	Title      string `json:"title"`
	CreatedAt  string `json:"created_at"`
	CreatedBy  string `json:"created_by"`
	Public     bool   `json:"public"`
	Auto       bool   `json:"auto"`
	Score      int    `json:"score"`
	Grade      string `json:"grade"`
	AIStatus   string `json:"ai_status,omitempty"`
}

func (r *InspReport) meta() reportMeta {
	return reportMeta{ID: r.ID, Kind: r.Kind, DeviceIP: r.DeviceIP, DeviceName: r.DeviceName, Title: r.Title,
		CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, Public: r.Public, Auto: r.Auto, Score: r.Score, Grade: r.Grade, AIStatus: r.AIStatus}
}

// AISettings 是大模型接口配置。APIKey 落盘时加密。
type AISettings struct {
	Provider  string `json:"provider"` // anthropic / openai
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeyEnc string `json:"api_key_enc,omitempty"`
	MaxTokens int    `json:"max_tokens"`
	Extra     string `json:"extra_prompt"` // 管理员追加的要求, 比如"按我们单位的格式写"
}

type ReportSettings struct {
	GuestVisible  bool       `json:"guest_visible"`       // 总开关: 允许游客查看已公开的报告
	AutoEnabled   bool       `json:"auto_enabled"`        // 每天定时生成全网报告
	AutoHour      int        `json:"auto_hour"`           // 几点(0-23)
	AutoPerDevice bool       `json:"auto_per_device"`     // 同时给每台设备各生成一份
	AutoAI        bool       `json:"auto_ai"`             // 自动报告也调用 AI
	AutoPublic    bool       `json:"auto_public"`         // 自动报告生成后直接公开
	Keep          int        `json:"keep"`                // 最多保留多少份
	KeepDays      int        `json:"keep_days,omitempty"` // 保留天数, 0 = 不按天清, 见 storage.go
	AI            AISettings `json:"ai"`
	LastAutoDay   string     `json:"last_auto_day,omitempty"`
}

type ReportStore struct {
	mu       sync.RWMutex
	dir      string
	box      *secretBox
	settings ReportSettings
	index    []reportMeta // 新的在前
}

func newReportStore(dataDir string, box *secretBox) *ReportStore {
	return &ReportStore{dir: filepath.Join(dataDir, "reports"), box: box}
}

func (s *ReportStore) settingsPath() string { return filepath.Join(s.dir, "settings.json") }

func (s *ReportStore) load() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	s.settings = ReportSettings{AutoHour: 8, Keep: 200, AI: AISettings{Provider: "anthropic", Model: "claude-opus-5", MaxTokens: 8000}}
	_ = readJSON(s.settingsPath(), &s.settings)
	if s.settings.Keep <= 0 {
		s.settings.Keep = 200
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "r_") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r InspReport
		if err := readJSON(filepath.Join(s.dir, e.Name()), &r); err != nil || r.ID == "" {
			continue
		}
		if r.AIStatus == "running" { // 上次没跑完程序就退出了
			r.AIStatus, r.AIError = "error", "生成过程中程序重启, 请重新生成"
			_ = s.saveReport(&r)
		}
		s.index = append(s.index, r.meta())
	}
	sort.Slice(s.index, func(i, j int) bool { return s.index[i].CreatedAt > s.index[j].CreatedAt })
	return nil
}

func (s *ReportStore) reportPath(id string) string {
	return filepath.Join(s.dir, "r_"+id+".json")
}

func (s *ReportStore) saveReport(r *InspReport) error {
	return atomicWriteJSON(s.reportPath(r.ID), r, 0o600)
}

func (s *ReportStore) Settings() ReportSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *ReportStore) UpdateSettings(fn func(*ReportSettings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.settings)
	return atomicWriteJSON(s.settingsPath(), s.settings, 0o600)
}

func (s *ReportStore) apiKey() string {
	enc := s.Settings().AI.APIKeyEnc
	if enc == "" {
		return ""
	}
	k, err := s.box.open(enc)
	if err != nil {
		return ""
	}
	return k
}

func (s *ReportStore) Add(r *InspReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveReport(r); err != nil {
		return err
	}
	s.index = append([]reportMeta{r.meta()}, s.index...)
	s.trimKeepLocked()
	return nil
}

// trimKeep: 超出保留数量删最旧的
func (s *ReportStore) trimKeep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trimKeepLocked()
}

func (s *ReportStore) trimKeepLocked() {
	for len(s.index) > s.settings.Keep {
		old := s.index[len(s.index)-1]
		s.index = s.index[:len(s.index)-1]
		_ = os.Remove(s.reportPath(old.ID))
	}
}

func (s *ReportStore) Get(id string) (*InspReport, bool) {
	if !validID(id) {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var r InspReport
	if err := readJSON(s.reportPath(id), &r); err != nil || r.ID == "" {
		return nil, false
	}
	return &r, true
}

// Update 读出-修改-写回一份报告, 并同步列表里的元信息。
func (s *ReportStore) Update(id string, fn func(*InspReport)) error {
	if !validID(id) {
		return errors.New("报告不存在")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r InspReport
	if err := readJSON(s.reportPath(id), &r); err != nil || r.ID == "" {
		return errors.New("报告不存在")
	}
	fn(&r)
	if err := s.saveReport(&r); err != nil {
		return err
	}
	for i := range s.index {
		if s.index[i].ID == id {
			s.index[i] = r.meta()
		}
	}
	return nil
}

func (s *ReportStore) Delete(id string) error {
	if !validID(id) {
		return errors.New("报告不存在")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.index {
		if s.index[i].ID == id {
			s.index = append(s.index[:i], s.index[i+1:]...)
			return os.Remove(s.reportPath(id))
		}
	}
	return errors.New("报告不存在")
}

func (s *ReportStore) List(onlyPublic bool) []reportMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]reportMeta, 0, len(s.index))
	for _, m := range s.index {
		if onlyPublic && !m.Public {
			continue
		}
		out = append(out, m)
	}
	return out
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// ----------------- 机器报告: 规则评分 -----------------

func gradeOf(score int, alive bool) string {
	switch {
	case !alive:
		return "离线"
	case score >= 90:
		return "优秀"
	case score >= 75:
		return "良好"
	case score >= 60:
		return "需关注"
	}
	return "高风险"
}

func hwBad(s string) bool { return strings.HasPrefix(s, "异常") }

type deviceFindings struct {
	score                                                                       int
	items                                                                       map[string][]ReportItem // 章节 -> 条目
	advice                                                                      []string
	errPorts, halfPorts, lowOptPorts, hotPorts, flapPorts, idlePorts, discPorts []string
}

// scoreDevice 按规则给一台设备打分并整理问题清单, 设备报告和全网报告共用。
func scoreDevice(r DeviceReport) deviceFindings {
	f := deviceFindings{score: 100, items: map[string][]ReportItem{}}
	add := func(sec, level, text string) {
		f.items[sec] = append(f.items[sec], ReportItem{Level: level, Text: text})
	}
	penalty := func(n, each, cap int) {
		p := n * each
		if p > cap {
			p = cap
		}
		f.score -= p
	}
	if !r.IsAlive {
		f.score = 0
		add("运行状态", "bad", "设备 SNMP 不可达(离线、网络中断或团体名/ACL 变化)")
		f.advice = append(f.advice, "优先排查 "+r.IP+" 的连通性: ping、SNMP 团体名、ACL、设备是否重启或掉电")
		return f
	}
	add("运行状态", "ok", "SNMP 在线, 已连续运行 "+r.UpTimeStr)

	// 资源
	switch {
	case r.CPUUsage >= 90:
		f.score -= 20
		add("资源", "bad", fmt.Sprintf("CPU 利用率 %d%%, 严重偏高", r.CPUUsage))
		f.advice = append(f.advice, "CPU 持续过高: 排查广播风暴/环路、ARP 攻击、大量上送 CPU 的报文(如 TTL=1、ICMP 重定向), 以及 SNMP 轮询是否过于频繁")
	case r.CPUUsage >= 80:
		f.score -= 10
		add("资源", "warn", fmt.Sprintf("CPU 利用率 %d%%, 偏高", r.CPUUsage))
	case r.CPUUsage >= 0:
		add("资源", "ok", fmt.Sprintf("CPU 利用率 %d%%", r.CPUUsage))
	}
	switch {
	case r.MemUsage >= 90:
		f.score -= 15
		add("资源", "bad", fmt.Sprintf("内存利用率 %d%%, 严重偏高", r.MemUsage))
		f.advice = append(f.advice, "内存过高: 检查 MAC/ARP/路由表规模是否接近规格上限, 是否存在内存泄漏(对比版本已知问题), 必要时安排升级或重启窗口")
	case r.MemUsage >= 85:
		f.score -= 8
		add("资源", "warn", fmt.Sprintf("内存利用率 %d%%, 偏高", r.MemUsage))
	case r.MemUsage >= 0:
		add("资源", "ok", fmt.Sprintf("内存利用率 %d%%", r.MemUsage))
	}
	if n := len(r.TrendData); n > 1 {
		var maxCPU, maxMem int
		var sumCPU, maxIn, maxOut float64
		for _, t := range r.TrendData {
			if t.CPU > maxCPU {
				maxCPU = t.CPU
			}
			if t.Mem > maxMem {
				maxMem = t.Mem
			}
			sumCPU += float64(t.CPU)
			maxIn = math.Max(maxIn, t.InMbps)
			maxOut = math.Max(maxOut, t.OutMbps)
		}
		add("资源", "info", fmt.Sprintf("统计周期内(%s 起, %d 个采样点): CPU 平均 %.0f%% / 峰值 %d%%, 内存峰值 %d%%, 整机吞吐峰值 入 %.1f / 出 %.1f Mbps",
			r.TrendData[0].Time, n, sumCPU/float64(n), maxCPU, maxMem, maxIn, maxOut))
	}

	// 硬件
	for _, x := range []struct{ name, val string }{{"电源", r.Hardware.PowerStatus}, {"风扇", r.Hardware.FanStatus}, {"温度", r.Hardware.Temperature}} {
		switch {
		case hwBad(x.val):
			f.score -= 15
			add("硬件", "bad", x.name+": "+x.val)
			f.advice = append(f.advice, x.name+"模块异常: 现场检查并准备备件, 电源异常先确认两路供电是否都在")
		case strings.HasPrefix(x.val, "未采集"):
			add("硬件", "info", x.name+": "+x.val)
		default:
			add("硬件", "ok", x.name+": "+x.val)
		}
	}

	// 端口
	var physUp int
	for _, p := range r.Ports {
		x := p.Ext
		if p.Status == "UP" {
			physUp++
		}
		if p.DeltaErrors > 0 || (x != nil && x.DeltaOutErrors > 0) {
			f.errPorts = append(f.errPorts, p.Name)
		}
		if x != nil && (x.DeltaInDiscard > 0 || x.DeltaOutDiscrd > 0) {
			f.discPorts = append(f.discPorts, p.Name)
		}
		if p.Status == "UP" && p.Duplex == "半双工" {
			f.halfPorts = append(f.halfPorts, p.Name)
		}
		if p.OpticalRxDbm != nil && *p.OpticalRxDbm <= -20 && p.Status == "UP" {
			f.lowOptPorts = append(f.lowOptPorts, fmt.Sprintf("%s(%.2f dBm)", p.Name, *p.OpticalRxDbm))
		}
		if p.InUsagePercent >= 70 || p.OutUsagePercent >= 70 {
			f.hotPorts = append(f.hotPorts, fmt.Sprintf("%s(入 %.0f%%/出 %.0f%%)", p.Name, p.InUsagePercent, p.OutUsagePercent))
		}
		if x != nil && x.LastChangeSec >= 0 && x.LastChangeSec < 600 {
			f.flapPorts = append(f.flapPorts, p.Name)
		}
		if x != nil && p.Status != "UP" && x.LastChangeSec >= 30*86400 {
			f.idlePorts = append(f.idlePorts, p.Name)
		}
	}
	add("端口", "info", fmt.Sprintf("物理口 %d 个, UP %d 个, 接入率 %s", r.TotalPorts, r.UpPorts, r.PortUsageRatio))
	listText := func(l []string) string {
		if len(l) > 12 {
			return strings.Join(l[:12], "、") + fmt.Sprintf(" 等 %d 个", len(l))
		}
		return strings.Join(l, "、")
	}
	if n := len(f.errPorts); n > 0 {
		penalty(n, 2, 10)
		add("端口", "warn", "本轮有错包增长的端口: "+listText(f.errPorts))
		f.advice = append(f.advice, "错包端口: 电口检查网线/水晶头/对端网卡, 光口检查光模块和尾纤清洁度, 持续增长则更换")
	}
	if n := len(f.discPorts); n > 0 {
		penalty(n, 2, 10)
		add("端口", "warn", "本轮有丢包的端口(出方向丢包多为拥塞): "+listText(f.discPorts))
		f.advice = append(f.advice, "丢包端口: 结合利用率判断是否拥塞, 拥塞口考虑扩容/聚合/调整 QoS 队列缓存")
	}
	if n := len(f.halfPorts); n > 0 {
		penalty(n, 5, 15)
		add("端口", "bad", "半双工运行的端口: "+listText(f.halfPorts))
		f.advice = append(f.advice, "半双工端口: 两端统一改为自协商或统一强制全双工, 一端强制一端自协商是最常见原因")
	}
	if n := len(f.lowOptPorts); n > 0 {
		penalty(n, 3, 12)
		add("端口", "warn", "收光偏低(≤ -20 dBm)的光口: "+listText(f.lowOptPorts))
		f.advice = append(f.advice, "收光偏低: 清洁光纤端面、检查熔接点和法兰, 对比对端发光; 距离长的链路确认模块型号是否匹配")
	}
	if n := len(f.hotPorts); n > 0 {
		penalty(n, 3, 15)
		add("端口", "warn", "利用率 ≥70% 的端口: "+listText(f.hotPorts))
		f.advice = append(f.advice, "高利用率端口: 用流量分析确认主要流量来源, 上联口考虑扩容或增加聚合成员")
	}
	if n := len(f.flapPorts); n > 0 {
		penalty(n, 2, 8)
		add("端口", "warn", "10 分钟内状态变化过的端口(可能抖动): "+listText(f.flapPorts))
	}
	if n := len(f.idlePorts); n > 0 {
		add("端口", "info", fmt.Sprintf("闲置超过 30 天的 DOWN 口 %d 个, 可作为扩容或回收依据", n))
	}
	if len(f.errPorts)+len(f.discPorts)+len(f.halfPorts)+len(f.lowOptPorts)+len(f.hotPorts)+len(f.flapPorts) == 0 {
		add("端口", "ok", "未发现错包、丢包、半双工、弱光、高负载或抖动端口")
	}

	// 聚合
	for _, g := range r.AggGroups {
		if len(g.Issues) == 0 {
			add("链路聚合", "ok", fmt.Sprintf("%s: %s, %d/%d 成员 UP, 带宽 %s", g.Name, g.Mode, g.UpMembers, len(g.Members), fmtMbpsText(g.SpeedMbps)))
			continue
		}
		penalty(len(g.Issues), 3, 12)
		for _, is := range g.Issues {
			add("链路聚合", "warn", g.Name+": "+is)
		}
	}

	// 告警
	var crit, warn int
	for _, a := range r.Criticals {
		if !a.Recovered && !a.Closed {
			crit++
		}
	}
	for _, a := range r.Warnings {
		if !a.Recovered && !a.Closed {
			warn++
		}
	}
	penalty(crit, 10, 30)
	penalty(warn, 2, 15)
	if crit+warn == 0 {
		add("告警", "ok", "当前没有未恢复的告警")
	} else {
		lvl := "warn"
		if crit > 0 {
			lvl = "bad"
		}
		add("告警", lvl, fmt.Sprintf("未恢复告警: 严重 %d 条, 警告 %d 条", crit, warn))
		for _, a := range append(append([]Alert{}, r.Criticals...), r.Warnings...) {
			if a.Recovered || a.Closed {
				continue
			}
			l := "warn"
			if a.Level == "critical" {
				l = "bad"
			}
			add("告警", l, "["+alertCategoryCN(a.Category)+"] "+a.Text+" (首次 "+a.FirstSeen+")")
		}
	}
	if r.Inventory != nil && r.Inventory.SoftVer != "" {
		add("运行状态", "info", "软件版本: "+r.Inventory.SoftVer)
	}
	if f.score < 0 {
		f.score = 0
	}
	return f
}

var alertCategoryNames = map[string]string{
	"cpu": "CPU", "mem": "内存", "temp": "温度", "fan": "风扇", "psu": "电源", "port_err": "端口错包",
	"optical": "光功率", "bandwidth": "带宽跑满", "reboot": "设备重启", "unreachable": "设备离线", "duplex": "双工不匹配", "internal": "采集异常",
	"broadcast": "广播风暴", "stp": "生成树变化",
}

func alertCategoryCN(c string) string {
	if n, ok := alertCategoryNames[c]; ok {
		return n
	}
	return c
}

var sectionOrder = []string{"运行状态", "资源", "硬件", "端口", "链路聚合", "告警"}

func buildDeviceReport(r DeviceReport, dataTime string) *InspReport {
	f := scoreDevice(r)
	name := r.SysName
	if name == "" {
		name = r.IP
	}
	rep := &InspReport{
		Kind: "device", DeviceIP: r.IP, DeviceName: name, Title: "设备巡检报告 · " + name + " (" + r.IP + ")",
		Score: f.score, Grade: gradeOf(f.score, r.IsAlive), DataTime: dataTime, Advice: dedupe(f.advice),
	}
	rep.Summary = append(rep.Summary, fmt.Sprintf("健康评分 %d 分(%s)", f.score, rep.Grade))
	if r.IsAlive {
		rep.Summary = append(rep.Summary, fmt.Sprintf("厂商 %s, CPU %d%%, 内存 %d%%, 物理口 %d/%d UP", r.Brand, r.CPUUsage, r.MemUsage, r.UpPorts, r.TotalPorts))
	}
	var bad, warn int
	for _, items := range f.items {
		for _, it := range items {
			switch it.Level {
			case "bad":
				bad++
			case "warn":
				warn++
			}
		}
	}
	rep.Summary = append(rep.Summary, fmt.Sprintf("发现严重问题 %d 项, 需关注 %d 项", bad, warn))
	for _, sec := range sectionOrder {
		if items := f.items[sec]; len(items) > 0 {
			rep.Sections = append(rep.Sections, ReportSection{Title: sec, Items: items})
		}
	}
	if !r.IsAlive {
		return rep // 离线设备的端口/聚合数据是旧的, 不再列明细
	}
	if len(r.AggGroups) > 0 {
		t := &ReportTable{Headers: []string{"聚合口", "模式", "成员 UP", "带宽", "入/出 Mbps", "对端设备", "对端聚合口"}}
		for _, g := range r.AggGroups {
			t.Rows = append(t.Rows, []string{g.Name, g.Mode, fmt.Sprintf("%d/%d", g.UpMembers, len(g.Members)), fmtMbpsText(g.SpeedMbps),
				fmt.Sprintf("%.1f / %.1f", g.InMbps, g.OutMbps), dash(g.PeerDevice), dash(g.PeerAgg)})
		}
		rep.Sections = append(rep.Sections, ReportSection{Title: "聚合组明细", Table: t})
	}
	// 上联口明细: 容量规划最常看的
	t := &ReportTable{Headers: []string{"端口", "速率", "对端", "入/出利用率", "收光"}}
	for _, p := range r.Ports {
		if !p.IsUplink || p.Status != "UP" {
			continue
		}
		rx := "-"
		if p.OpticalRxDbm != nil {
			rx = fmt.Sprintf("%.2f dBm", *p.OpticalRxDbm)
		}
		t.Rows = append(t.Rows, []string{p.Name, fmtMbpsText(p.SpeedMbps), dash(strings.Trim(p.LLDPRemoteDev+" "+p.LLDPRemotePort, "- ")),
			fmt.Sprintf("%.1f%% / %.1f%%", p.InUsagePercent, p.OutUsagePercent), rx})
	}
	if len(t.Rows) > 0 {
		rep.Sections = append(rep.Sections, ReportSection{Title: "上联/级联口", Table: t})
	}
	if len(rep.Advice) == 0 && r.IsAlive {
		rep.Advice = []string{"本次巡检未发现需要处置的问题, 保持现有巡检频率即可"}
	}
	return rep
}

func buildNetworkReport(reports []DeviceReport, dataTime string) *InspReport {
	rep := &InspReport{Kind: "network", Title: "全网巡检报告", DataTime: dataTime}
	type scored struct {
		r DeviceReport
		f deviceFindings
	}
	var list []scored
	var online, sum int
	var totalPorts, upPorts int
	alertCat := map[string]int{}
	var advice []string
	var aggIssues, halfAll, lowOptAll, hotAll []string
	for _, r := range reports {
		f := scoreDevice(r)
		list = append(list, scored{r, f})
		if r.IsAlive {
			online++
			sum += f.score
		}
		totalPorts += r.TotalPorts
		upPorts += r.UpPorts
		for _, a := range append(append([]Alert{}, r.Criticals...), r.Warnings...) {
			if !a.Recovered && !a.Closed {
				alertCat[alertCategoryCN(a.Category)]++
			}
		}
		name := r.SysName
		if name == "" {
			name = r.IP
		}
		for _, g := range r.AggGroups {
			for _, is := range g.Issues {
				aggIssues = append(aggIssues, name+" "+g.Name+": "+is)
			}
		}
		for _, p := range f.halfPorts {
			halfAll = append(halfAll, name+" "+p)
		}
		for _, p := range f.lowOptPorts {
			lowOptAll = append(lowOptAll, name+" "+p)
		}
		for _, p := range f.hotPorts {
			hotAll = append(hotAll, name+" "+p)
		}
		advice = append(advice, f.advice...)
	}
	n := len(reports)
	offline := n - online
	score := 0
	if online > 0 {
		score = sum / online
	}
	if n > 0 {
		score -= offline * 100 / n / 2 // 离线比例折半计入
	}
	if score < 0 {
		score = 0
	}
	rep.Score, rep.Grade = score, gradeOf(score, online > 0 || n == 0)
	rep.Summary = append(rep.Summary,
		fmt.Sprintf("全网健康评分 %d 分(%s)", score, rep.Grade),
		fmt.Sprintf("监控设备 %d 台, 在线 %d 台, 离线 %d 台", n, online, offline))
	if totalPorts > 0 {
		rep.Summary = append(rep.Summary, fmt.Sprintf("物理口 %d 个, 已使用 %d 个(%.1f%%)", totalPorts, upPorts, float64(upPorts)*100/float64(totalPorts)))
	}

	sort.Slice(list, func(i, j int) bool { return list[i].f.score < list[j].f.score })
	t := &ReportTable{Headers: []string{"设备", "IP", "状态", "评分", "CPU", "内存", "端口 UP/总数", "未恢复告警"}}
	for _, x := range list {
		st := "在线"
		if !x.r.IsAlive {
			st = "离线"
		}
		name := x.r.SysName
		if name == "" {
			name = x.r.IP
		}
		open := 0
		for _, a := range append(append([]Alert{}, x.r.Criticals...), x.r.Warnings...) {
			if !a.Recovered && !a.Closed {
				open++
			}
		}
		t.Rows = append(t.Rows, []string{name, x.r.IP, st, fmt.Sprintf("%d (%s)", x.f.score, gradeOf(x.f.score, x.r.IsAlive)),
			fmt.Sprintf("%d%%", x.r.CPUUsage), fmt.Sprintf("%d%%", x.r.MemUsage), fmt.Sprintf("%d/%d", x.r.UpPorts, x.r.TotalPorts), fmt.Sprint(open)})
	}
	rep.Sections = append(rep.Sections, ReportSection{Title: "设备健康排名(由低到高)", Table: t})

	var risk []ReportItem
	for _, x := range list {
		if !x.r.IsAlive {
			risk = append(risk, ReportItem{"bad", "离线: " + x.r.IP + " " + x.r.SysName})
		}
	}
	for _, x := range list {
		if x.r.IsAlive && x.f.score < 75 {
			risk = append(risk, ReportItem{"warn", fmt.Sprintf("%s(%s) 评分 %d, 需要重点关注", x.r.SysName, x.r.IP, x.f.score)})
		}
	}
	if len(risk) == 0 {
		risk = append(risk, ReportItem{"ok", "所有设备在线, 且评分均在 75 分以上"})
	}
	rep.Sections = append(rep.Sections, ReportSection{Title: "重点风险设备", Items: risk})

	if len(alertCat) > 0 {
		keys := make([]string, 0, len(alertCat))
		for k := range alertCat {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return alertCat[keys[i]] > alertCat[keys[j]] })
		var items []ReportItem
		for _, k := range keys {
			items = append(items, ReportItem{"warn", fmt.Sprintf("%s: %d 条", k, alertCat[k])})
		}
		rep.Sections = append(rep.Sections, ReportSection{Title: "未恢复告警分类", Items: items})
	}
	addList := func(title, level string, l []string, okText string) {
		if len(l) == 0 {
			rep.Sections = append(rep.Sections, ReportSection{Title: title, Items: []ReportItem{{"ok", okText}}})
			return
		}
		var items []ReportItem
		for i, s := range l {
			if i >= 30 {
				items = append(items, ReportItem{"info", fmt.Sprintf("…… 另有 %d 项", len(l)-30)})
				break
			}
			items = append(items, ReportItem{level, s})
		}
		rep.Sections = append(rep.Sections, ReportSection{Title: title, Items: items})
	}
	addList("链路聚合一致性", "warn", aggIssues, "未发现聚合配置问题")
	addList("半双工端口", "bad", halfAll, "未发现半双工端口")
	addList("弱光光口(≤ -20 dBm)", "warn", lowOptAll, "未发现弱光光口")
	addList("高负载端口(≥70%)", "warn", hotAll, "未发现高负载端口")

	// 软件版本分布: 同型号跑多个版本是常见的运维隐患
	ver := map[string][]string{}
	for _, r := range reports {
		if r.Inventory != nil && r.Inventory.SoftVer != "" {
			k := strings.TrimSpace(r.Inventory.Model + " " + r.Inventory.SoftVer)
			ver[k] = append(ver[k], r.IP)
		}
	}
	if len(ver) > 0 {
		vt := &ReportTable{Headers: []string{"型号 / 软件版本", "设备数", "设备"}}
		for k, ips := range ver {
			vt.Rows = append(vt.Rows, []string{k, fmt.Sprint(len(ips)), strings.Join(ips, " ")})
		}
		sort.Slice(vt.Rows, func(i, j int) bool { return vt.Rows[i][0] < vt.Rows[j][0] })
		rep.Sections = append(rep.Sections, ReportSection{Title: "型号与软件版本分布", Table: vt})
	}

	rep.Advice = dedupe(advice)
	if len(rep.Advice) == 0 {
		rep.Advice = []string{"本次巡检未发现需要处置的问题, 保持现有巡检频率即可"}
	}
	return rep
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range l {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ----------------- AI 报告 -----------------

const aiSystemPrompt = `你是一名资深的企业网络运维专家, 负责根据交换机巡检数据撰写巡检报告。
要求:
1. 只依据提供的巡检数据下结论, 不要编造数据里没有的设备、端口或数值; 数据不足时明确说"数据不足以判断"。
2. 使用简体中文, 输出 Markdown。结构: "## 总体结论"(3~5 句) / "## 主要风险(按优先级)"(编号列表, 每条写现象、影响、依据) / "## 原因分析" / "## 处置建议"(区分立即处理、计划处理) / "## 后续优化建议"。
3. 面向运维负责人, 语言简洁专业, 能给出具体的排查命令思路时可以写出(注明厂商差异)。`

func reportPromptPayload(r *InspReport) string {
	b, _ := json.MarshalIndent(struct {
		Title    string          `json:"标题"`
		Kind     string          `json:"类型"`
		DataTime string          `json:"数据时间"`
		Score    int             `json:"规则评分"`
		Grade    string          `json:"评级"`
		Summary  []string        `json:"摘要"`
		Sections []ReportSection `json:"章节"`
		Advice   []string        `json:"规则建议"`
	}{r.Title, r.Kind, r.DataTime, r.Score, r.Grade, r.Summary, r.Sections, r.Advice}, "", " ")
	return string(b)
}

var aiHTTPClient = &http.Client{Timeout: 6 * time.Minute}

// callAI 调用大模型, 返回 Markdown 文本。
func callAI(ctx context.Context, cfg AISettings, apiKey, system, user string) (string, error) {
	if apiKey == "" && cfg.Provider != "openai" {
		return "", errors.New("还没有配置 API Key(在「报告设置」里填写)")
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8000
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	switch cfg.Provider {
	case "openai":
		if base == "" {
			return "", errors.New("OpenAI 兼容接口需要填写 Base URL, 例如 https://api.deepseek.com/v1")
		}
		body, _ := json.Marshal(map[string]interface{}{
			"model": cfg.Model, "max_tokens": maxTokens,
			"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		})
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		headers := map[string]string{}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
		if err := postJSON(ctx, base+"/chat/completions", headers, body, &out); err != nil {
			return "", err
		}
		if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
			return "", errors.New("模型返回为空")
		}
		return out.Choices[0].Message.Content, nil
	default: // anthropic
		if base == "" {
			base = "https://api.anthropic.com"
		}
		model := cfg.Model
		if model == "" {
			model = "claude-opus-5"
		}
		body, _ := json.Marshal(map[string]interface{}{
			"model": model, "max_tokens": maxTokens, "system": system,
			"messages": []map[string]string{{"role": "user", "content": user}},
		})
		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		}
		headers := map[string]string{"x-api-key": apiKey, "anthropic-version": "2023-06-01"}
		if err := postJSON(ctx, base+"/v1/messages", headers, body, &out); err != nil {
			return "", err
		}
		if out.StopReason == "refusal" {
			return "", errors.New("模型拒绝了这次请求(stop_reason=refusal)")
		}
		var sb strings.Builder
		for _, c := range out.Content {
			if c.Type == "text" {
				sb.WriteString(c.Text)
			}
		}
		if strings.TrimSpace(sb.String()) == "" {
			return "", errors.New("模型返回为空")
		}
		text := sb.String()
		if out.StopReason == "max_tokens" {
			text += "\n\n> ⚠️ 输出达到 max_tokens 上限被截断, 可以在报告设置里调大。"
		}
		return text, nil
	}
}

func postJSON(ctx context.Context, url string, headers map[string]string, body []byte, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败(检查网络/代理/Base URL): %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		msg := string(raw)
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return fmt.Errorf("接口返回 HTTP %d: %s", resp.StatusCode, msg)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析返回失败: %v", err)
	}
	return nil
}

// runAI 在后台给一份报告生成 AI 分析。
func (s *server) runAI(id string) {
	st := s.reports.Settings()
	_ = s.reports.Update(id, func(r *InspReport) { r.AIStatus, r.AIError = "running", "" })
	r, ok := s.reports.Get(id)
	if !ok {
		return
	}
	user := "以下是本次巡检的结构化数据(JSON), 请据此撰写 AI 分析报告:\n\n" + reportPromptPayload(r)
	if x := strings.TrimSpace(st.AI.Extra); x != "" {
		user += "\n\n补充要求: " + x
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	text, err := callAI(ctx, st.AI, s.reports.apiKey(), aiSystemPrompt, user)
	_ = s.reports.Update(id, func(r *InspReport) {
		r.AIAt = time.Now().Format("2006-01-02 15:04:05")
		r.AIModel = st.AI.Provider + " / " + st.AI.Model
		if err != nil {
			r.AIStatus, r.AIError = "error", err.Error()
			return
		}
		r.AIStatus, r.AIText, r.AIError = "done", text, ""
	})
}

// generateReports 生成报告。kind=network 生成全网报告; kind=device 且 ip 为空时给每台设备各生成一份。
func (s *server) generateReports(kind, ip, user string, withAI, public, auto bool) ([]string, error) {
	s.hub.mu.RLock()
	reports := append([]DeviceReport(nil), s.hub.reports...)
	dataTime := s.hub.checkTime
	s.hub.mu.RUnlock()
	if len(reports) == 0 {
		return nil, errors.New("还没有巡检数据, 请等第一轮巡检完成")
	}
	var built []*InspReport
	switch kind {
	case "network":
		nr := buildNetworkReport(reports, dataTime)
		enrichNetworkReport(nr, reports, s.history, s.reports.Latest("network", ""), s.conf(), time.Now())
		built = append(built, nr)
	case "device":
		for _, r := range reports {
			if ip == "" || r.IP == ip {
				dr := buildDeviceReport(r, dataTime)
				enrichDeviceReport(dr, r, reports, s.history, s.reports.Latest("device", r.IP), s.conf(), time.Now())
				built = append(built, dr)
			}
		}
		if len(built) == 0 {
			return nil, errors.New("找不到这台设备的巡检数据")
		}
	default:
		return nil, errors.New("未知的报告类型")
	}
	now := time.Now()
	var ids []string
	for _, r := range built {
		r.ID = randomID()
		r.CreatedAt = now.Format("2006-01-02 15:04:05")
		r.CreatedBy, r.Public, r.Auto = user, public, auto
		if withAI {
			r.AIStatus = "running"
		}
		if err := s.reports.Add(r); err != nil {
			return ids, err
		}
		ids = append(ids, r.ID)
		if withAI {
			go s.runAI(r.ID)
		}
	}
	return ids, nil
}

// reportScheduler 每分钟检查一次是否到了定时生成的时刻。
func (s *server) reportScheduler() {
	for {
		time.Sleep(time.Minute)
		st := s.reports.Settings()
		now := time.Now()
		day := now.Format("2006-01-02")
		if !st.AutoEnabled || now.Hour() != st.AutoHour || st.LastAutoDay == day {
			continue
		}
		_ = s.reports.UpdateSettings(func(x *ReportSettings) { x.LastAutoDay = day })
		if _, err := s.generateReports("network", "", "定时任务", st.AutoAI, st.AutoPublic, true); err != nil {
			fmt.Printf("⚠️  定时巡检报告生成失败: %v\n", err)
			continue
		}
		if st.AutoPerDevice {
			_, _ = s.generateReports("device", "", "定时任务", st.AutoAI, st.AutoPublic, true)
		}
		fmt.Printf("📑 已生成定时巡检报告 (%s)\n", day)
	}
}

// ----------------- HTTP -----------------

// isAdmin: 能看全部报告(含未公开的) = 登录了且有「巡检报告」权限
func (s *server) isAdmin(r *http.Request) bool {
	sess, ok := currentSession(s.sessions, r)
	return ok && s.auth.can(sess.user, "reports")
}

func (s *server) handleReportList(w http.ResponseWriter, r *http.Request) {
	admin := s.isAdmin(r)
	st := s.reports.Settings()
	if !admin && !st.GuestVisible {
		s.writeJSON(w, 200, map[string]interface{}{"admin": false, "guest_visible": false, "reports": []reportMeta{}})
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"admin": admin, "guest_visible": st.GuestVisible, "reports": s.reports.List(!admin)})
}

func (s *server) handleReportGet(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.reports.Get(r.URL.Query().Get("id"))
	if !ok {
		s.fail(w, 404, "报告不存在")
		return
	}
	if !s.isAdmin(r) && !(rep.Public && s.reports.Settings().GuestVisible) {
		s.fail(w, 404, "报告不存在或未公开")
		return
	}
	s.writeJSON(w, 200, rep)
}

func (s *server) handleReportGenerate(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Kind   string `json:"kind"`
		IP     string `json:"ip"`
		AI     bool   `json:"ai"`
		Public bool   `json:"public"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	ids, err := s.generateReports(req.Kind, req.IP, sess.user, req.AI, req.Public, false)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "ids": ids})
}

func (s *server) handleReportAction(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"` // publish / unpublish / delete / ai
	}
	if !s.decode(w, r, &req) {
		return
	}
	var err error
	switch req.Action {
	case "publish", "unpublish":
		err = s.reports.Update(req.ID, func(x *InspReport) { x.Public = req.Action == "publish" })
	case "delete":
		err = s.reports.Delete(req.ID)
	case "ai":
		if _, ok := s.reports.Get(req.ID); !ok {
			err = errors.New("报告不存在")
		} else {
			_ = s.reports.Update(req.ID, func(x *InspReport) { x.AIStatus = "running" })
			go s.runAI(req.ID)
		}
	default:
		err = errors.New("未知操作")
	}
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// reportSettingsView 是返回给页面的设置, 不带密钥本身。
type reportSettingsView struct {
	ReportSettings
	HasKey bool `json:"has_key"`
}

func (s *server) handleReportSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodGet {
		st := s.reports.Settings()
		hasKey := st.AI.APIKeyEnc != ""
		st.AI.APIKeyEnc = ""
		s.writeJSON(w, 200, reportSettingsView{st, hasKey})
		return
	}
	var req struct {
		ReportSettings
		APIKey   string `json:"api_key"`   // 非空才更新
		ClearKey bool   `json:"clear_key"` //
		Test     bool   `json:"test"`      // 只测试连通性, 不保存
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.AutoHour < 0 || req.AutoHour > 23 {
		s.fail(w, 400, "定时时刻需要在 0~23 之间")
		return
	}
	if p := req.AI.Provider; p != "anthropic" && p != "openai" {
		s.fail(w, 400, "未知的接口类型")
		return
	}
	if req.Test {
		key := req.APIKey
		if key == "" {
			key = s.reports.apiKey()
		}
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
		defer cancel()
		ai := req.AI
		ai.MaxTokens = 200
		text, err := callAI(ctx, ai, key, "你是连通性测试助手。", "请只回复: 连接正常")
		if err != nil {
			s.fail(w, 400, err.Error())
			return
		}
		s.writeJSON(w, 200, map[string]interface{}{"ok": true, "reply": truncate(text, 200)})
		return
	}
	var encKey string
	if req.APIKey != "" {
		k, err := s.reports.box.seal(req.APIKey)
		if err != nil {
			s.fail(w, 500, "加密失败")
			return
		}
		encKey = k
	}
	err := s.reports.UpdateSettings(func(x *ReportSettings) {
		old := x.AI.APIKeyEnc
		lastDay, keepDays := x.LastAutoDay, x.KeepDays
		*x = req.ReportSettings
		x.LastAutoDay, x.KeepDays = lastDay, keepDays
		x.AI.APIKeyEnc = old
		if encKey != "" {
			x.AI.APIKeyEnc = encKey
		}
		if req.ClearKey {
			x.AI.APIKeyEnc = ""
		}
		if x.Keep <= 0 {
			x.Keep = 200
		}
	})
	if err != nil {
		s.fail(w, 500, "保存失败: "+err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true})
}
