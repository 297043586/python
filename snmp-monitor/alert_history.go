package main

// 告警历史(v13)。
//
// 以前告警只有"当前状态"(alerts.json): 恢复 24 小时后就被删掉; 谁在什么时候确认/关闭的、
// 故障持续了多久、同一个口一天抖了几次, 事后都查不到。现在每一次告警从出现到恢复记成一个
// "事件"(AlertIncident), 连同处理过程(确认 / 关闭 / 重新打开 / 级别变化 / 恢复)一起按月追加
// 写到 data/alert_history/YYYY-MM.jsonl, 默认保留 180 天(设置 → 告警 里可改)。
//
//   - 同一条告警(设备 + 类别 + 对象)恢复后再次出现, 算一个新事件; 事后能统计"抖了几次";
//   - 事件的开始时间沿用 alerts.json 里的首次出现时间, 升级 / 重启不会把进行中的告警拆成两段;
//   - 文件只追加不改写, 断电最多丢最后一轮; 启动时按行回放, 坏行跳过。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type AlertIncident struct {
	ID         string `json:"id"`
	AlertID    string `json:"alert_id"`
	DeviceID   string `json:"device_id"`
	DeviceIP   string `json:"device_ip"`
	DeviceName string `json:"device_name"`
	Level      string `json:"level"` // 期间出现过的最高级别
	Category   string `json:"category"`
	Subject    string `json:"subject,omitempty"`
	Text       string `json:"text"`          // 最近一次的告警内容
	Start      int64  `json:"start"`         // Unix 秒
	End        int64  `json:"end,omitempty"` // 恢复时间, 0 = 仍在告警
	LastSeen   int64  `json:"last_seen"`
	AckedBy    string `json:"acked_by,omitempty"`
	AckedAt    int64  `json:"acked_at,omitempty"`
	Closed     bool   `json:"closed,omitempty"`
	Note       string `json:"note,omitempty"` // 比如"设备已删除"
}

type AlertEvent struct {
	TS   int64  `json:"ts"`
	Type string `json:"type"` // open / level / ack / close / unack / reopen / recover / removed
	User string `json:"user,omitempty"`
	Text string `json:"text,omitempty"`
}

type alertHistLine struct {
	K   string         `json:"k"` // inc = 事件快照, ev = 处理过程
	Inc *AlertIncident `json:"inc,omitempty"`
	ID  string         `json:"id,omitempty"`
	Ev  *AlertEvent    `json:"ev,omitempty"`
}

type AlertHistory struct {
	mu        sync.RWMutex
	dir       string
	days      int
	maxRecs   int
	incidents map[string]*AlertIncident
	events    map[string][]AlertEvent
	open      map[string]string // 告警 ID -> 进行中的事件 ID
	pending   []alertHistLine
	lastPurge time.Time
	now       func() time.Time
}

const alertHistoryMaxIncidentsDefault = 300000

// SetMaxRecords 从系统设置改总条数硬上限, 立即生效(下次超限检查就用新值)。
func (h *AlertHistory) SetMaxRecords(n int) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if n <= 0 {
		n = alertHistoryMaxIncidentsDefault
	}
	h.maxRecs = n
	h.mu.Unlock()
}

func (h *AlertHistory) maxRecords() int {
	h.mu.RLock()
	n := h.maxRecs
	h.mu.RUnlock()
	if n <= 0 {
		return alertHistoryMaxIncidentsDefault
	}
	return n
}

func newAlertHistory(dataDir string, days int) *AlertHistory {
	if days <= 0 {
		days = 180
	}
	return &AlertHistory{
		dir: filepath.Join(dataDir, "alert_history"), days: days,
		incidents: map[string]*AlertIncident{}, events: map[string][]AlertEvent{}, open: map[string]string{},
		now: time.Now,
	}
}

// SetRetention 页面上改了保留天数: 立即生效(下次清理时按新值删)。
func (h *AlertHistory) SetRetention(days int) {
	if h == nil || days <= 0 {
		return
	}
	h.mu.Lock()
	h.days = days
	h.lastPurge = time.Time{}
	h.mu.Unlock()
}

// load 回放保留期内的月文件。
func (h *AlertHistory) load() error {
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(h.dir, "*.jsonl"))
	sort.Strings(files)
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := h.cutoffMonth()
	for _, f := range files {
		if strings.TrimSuffix(filepath.Base(f), ".jsonl") < cut {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var ln alertHistLine
			if json.Unmarshal(sc.Bytes(), &ln) != nil {
				continue // 断电写了半行: 跳过
			}
			switch {
			case ln.K == "inc" && ln.Inc != nil && ln.Inc.ID != "":
				inc := *ln.Inc
				h.incidents[inc.ID] = &inc
			case ln.K == "ev" && ln.Ev != nil && ln.ID != "":
				h.events[ln.ID] = append(h.events[ln.ID], *ln.Ev)
			}
		}
		fh.Close()
	}
	for id := range h.events {
		if h.incidents[id] == nil {
			delete(h.events, id)
		}
	}
	for _, inc := range h.incidents {
		if inc.End == 0 {
			if cur, ok := h.open[inc.AlertID]; !ok || h.incidents[cur].Start < inc.Start {
				h.open[inc.AlertID] = inc.ID
			}
		}
	}
	h.purgeLocked(true)
	return nil
}

func (h *AlertHistory) cutoffMonth() string {
	return h.now().AddDate(0, 0, -h.days).Format("2006-01")
}

// purgeLocked 清掉保留期以外的事件和月文件(一天最多做一次, 进行中的事件不清)。
func (h *AlertHistory) purgeLocked(force bool) {
	now := h.now()
	if !force && now.Sub(h.lastPurge) < 24*time.Hour {
		return
	}
	h.lastPurge = now
	cut := now.AddDate(0, 0, -h.days).Unix()
	for id, inc := range h.incidents {
		if inc.End != 0 && inc.End < cut {
			delete(h.incidents, id)
			delete(h.events, id)
		}
	}
	maxRecs := h.maxRecs
	if maxRecs <= 0 {
		maxRecs = alertHistoryMaxIncidentsDefault
	}
	if len(h.incidents) > maxRecs {
		list := make([]*AlertIncident, 0, len(h.incidents))
		for _, inc := range h.incidents {
			if inc.End != 0 {
				list = append(list, inc)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Start < list[j].Start })
		for _, inc := range list[:minInt(len(list), len(h.incidents)-maxRecs)] {
			delete(h.incidents, inc.ID)
			delete(h.events, inc.ID)
		}
	}
	cutMonth := h.cutoffMonth()
	files, _ := filepath.Glob(filepath.Join(h.dir, "*.jsonl"))
	for _, f := range files {
		if strings.TrimSuffix(filepath.Base(f), ".jsonl") < cutMonth {
			_ = os.Remove(f)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func levelRank(l string) int {
	if l == "critical" {
		return 2
	}
	return 1
}

func parseAlertTime(s string, def int64) int64 {
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t.Unix()
	}
	return def
}

func (h *AlertHistory) snapLocked(inc *AlertIncident) {
	c := *inc
	h.pending = append(h.pending, alertHistLine{K: "inc", Inc: &c})
}

func (h *AlertHistory) eventLocked(id string, ev AlertEvent) {
	h.events[id] = append(h.events[id], ev)
	h.pending = append(h.pending, alertHistLine{K: "ev", ID: id, Ev: &ev})
}

// observe: 本轮巡检仍在触发的告警。新告警开一个事件, 进行中的更新最后出现时间 / 内容 / 级别。
// 只有状态变化才写盘, 每轮都在的告警不会每轮写一行。
func (h *AlertHistory) observe(al Alert, deviceName string) {
	if h == nil {
		return
	}
	now := h.now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	if id, ok := h.open[al.ID]; ok {
		if inc := h.incidents[id]; inc != nil && inc.End == 0 {
			changed := false
			if levelRank(al.Level) > levelRank(inc.Level) {
				inc.Level = al.Level
				h.eventLocked(id, AlertEvent{TS: now, Type: "level", Text: "升级为严重: " + al.Text})
				changed = true
			}
			// 内容每轮都可能变(CPU 92% → 95%), 只改内存, 下次状态变化时随快照一起落盘
			inc.Text, inc.DeviceName = al.Text, deviceName
			inc.LastSeen = now
			if changed {
				h.snapLocked(inc)
			}
			return
		}
	}
	start := parseAlertTime(al.FirstSeen, now)
	if start > now {
		start = now
	}
	inc := &AlertIncident{
		ID: al.ID + "-" + strconv.FormatInt(start, 36), AlertID: al.ID, DeviceID: al.DeviceID, DeviceIP: al.DeviceIP, DeviceName: deviceName,
		Level: al.Level, Category: al.Category, Subject: al.Subject, Text: al.Text, Start: start, LastSeen: now,
	}
	if old := h.incidents[inc.ID]; old != nil {
		// 同一秒重复打开(比如升级后第一轮): 沿用
		old.End, old.LastSeen = 0, now
		h.open[al.ID] = old.ID
		h.snapLocked(old)
		return
	}
	h.incidents[inc.ID] = inc
	h.open[al.ID] = inc.ID
	h.snapLocked(inc)
	h.eventLocked(inc.ID, AlertEvent{TS: start, Type: "open", Text: al.Text})
}

// settleDevice: 一台设备巡检完, 它名下进行中但本轮没再出现的事件记为恢复。
func (h *AlertHistory) settleDevice(deviceID string, active map[string]bool) {
	if h == nil {
		return
	}
	now := h.now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	for aid, id := range h.open {
		inc := h.incidents[id]
		if inc == nil {
			delete(h.open, aid)
			continue
		}
		if inc.DeviceID != deviceID || active[aid] {
			continue
		}
		inc.End = now
		delete(h.open, aid)
		h.snapLocked(inc)
		h.eventLocked(id, AlertEvent{TS: now, Type: "recover", Text: "故障恢复, 持续 " + fmtDuration(now-inc.Start)})
	}
	h.purgeLocked(false)
}

// action 记录页面上的确认 / 关闭 / 重新打开。告警已恢复(还在 24 小时保留期里)时记到最近那次事件上。
func (h *AlertHistory) action(alertID, action, user string) {
	if h == nil {
		return
	}
	now := h.now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	inc := h.incidents[h.open[alertID]]
	if inc == nil {
		for _, x := range h.incidents {
			if x.AlertID == alertID && (inc == nil || x.Start > inc.Start) {
				inc = x
			}
		}
	}
	if inc == nil {
		return
	}
	switch action {
	case "ack":
		inc.AckedBy, inc.AckedAt, inc.Closed = user, now, false
	case "close":
		inc.AckedBy, inc.AckedAt, inc.Closed = user, now, true
	default:
		inc.AckedBy, inc.AckedAt, inc.Closed = "", 0, false
	}
	h.snapLocked(inc)
	h.eventLocked(inc.ID, AlertEvent{TS: now, Type: action, User: user})
}

// removed: 设备被删除, 它名下进行中的事件就此结束。
func (h *AlertHistory) removed(deviceID, by string) {
	if h == nil {
		return
	}
	now := h.now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	for aid, id := range h.open {
		inc := h.incidents[id]
		if inc == nil || inc.DeviceID != deviceID {
			continue
		}
		inc.End, inc.Note = now, "设备已从监控中删除"
		delete(h.open, aid)
		h.snapLocked(inc)
		h.eventLocked(id, AlertEvent{TS: now, Type: "removed", User: by, Text: "设备已从监控中删除"})
	}
}

// flush 把这段时间积攒的变化追加写到当月文件。
func (h *AlertHistory) flush() {
	if h == nil {
		return
	}
	h.mu.Lock()
	lines := h.pending
	h.pending = nil
	month := h.now().Format("2006-01")
	h.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	var sb strings.Builder
	for _, ln := range lines {
		b, err := json.Marshal(ln)
		if err != nil {
			continue
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.MkdirAll(h.dir, 0o700); err == nil {
		var f *os.File
		if f, err = os.OpenFile(filepath.Join(h.dir, month+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, err = f.WriteString(sb.String())
			f.Close()
		}
		if err == nil {
			return
		}
		fmt.Printf("⚠️  告警历史落盘失败: %v\n", err)
	}
}

// clearAll 系统设置「清理工具」用: 清空所有告警历史(内存 + data/alert_history/ 下的月文件)。
// 进行中(还没恢复)的告警状态在 alerts.json 里, 不受影响。
func (h *AlertHistory) clearAll() (int, error) {
	h.mu.Lock()
	n := len(h.incidents)
	h.incidents = map[string]*AlertIncident{}
	h.events = map[string][]AlertEvent{}
	h.open = map[string]string{}
	h.pending = nil
	dir := h.dir
	h.mu.Unlock()

	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return n, err
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return n, err
		}
	}
	return n, nil
}

func fmtDuration(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	switch {
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	case sec < 3600:
		return fmt.Sprintf("%d 分钟", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%d 小时 %d 分", sec/3600, sec%3600/60)
	}
	return fmt.Sprintf("%d 天 %d 小时", sec/86400, sec%86400/3600)
}

// ----------------- 查询 -----------------

type alertHistQuery struct {
	From, To        int64
	Devices, Levels map[string]bool
	Cats, Status    map[string]bool
	Q               string
	Sort            string // start / -start / duration / -duration / device / level
	Offset, Limit   int
}

type histItem struct {
	AlertIncident
	Duration int64 `json:"duration"` // 秒; 进行中的算到现在
	Flaps    int   `json:"flaps"`    // 查询范围内同一条告警出现了几次
}

type histCount struct {
	Key   string `json:"key"`
	Name  string `json:"name,omitempty"`
	Count int    `json:"count"`
	Crit  int    `json:"critical"`
	Dur   int64  `json:"duration,omitempty"` // 累计持续秒数
}

type histStats struct {
	Total     int         `json:"total"`
	Critical  int         `json:"critical"`
	Warning   int         `json:"warning"`
	Active    int         `json:"active"`
	Recovered int         `json:"recovered"`
	Handled   int         `json:"handled"`
	MTTR      int64       `json:"mttr"`       // 平均恢复时长(秒), 只算已恢复的
	MTTRMed   int64       `json:"mttr_p50"`   // 恢复时长中位数
	MTTA      int64       `json:"mtta"`       // 平均响应(确认)时长
	Days      []histDay   `json:"days"`       // 每天新增
	Hours     [24]int     `json:"hours"`      // 按小时(0-23)分布
	Cats      []histCount `json:"categories"` // 按类别
	Devices   []histCount `json:"devices"`    // 告警最多的设备
	Flapping  []histCount `json:"flapping"`   // 反复出现(≥3 次)的告警
}

type histDay struct {
	Day      string `json:"day"`
	Critical int    `json:"critical"`
	Warning  int    `json:"warning"`
}

func (q alertHistQuery) statusOK(inc *AlertIncident) bool {
	if len(q.Status) == 0 {
		return true
	}
	// 同一维度里多选是"或", 两个维度之间是"且"
	state := q.Status["active"] || q.Status["recovered"]
	if state && !((q.Status["active"] && inc.End == 0) || (q.Status["recovered"] && inc.End != 0)) {
		return false
	}
	handle := q.Status["handled"] || q.Status["unhandled"]
	if handle && !((q.Status["handled"] && inc.AckedBy != "") || (q.Status["unhandled"] && inc.AckedBy == "")) {
		return false
	}
	return true
}

func (h *AlertHistory) query(q alertHistQuery) ([]histItem, int, histStats) {
	now := h.now().Unix()
	kw := strings.ToLower(strings.TrimSpace(q.Q))
	h.mu.RLock()
	var hits []histItem
	for _, inc := range h.incidents {
		end := inc.End
		if end == 0 {
			end = now
		}
		if (q.To > 0 && inc.Start > q.To) || (q.From > 0 && end < q.From) {
			continue
		}
		if (len(q.Devices) > 0 && !q.Devices[inc.DeviceIP]) || (len(q.Levels) > 0 && !q.Levels[inc.Level]) ||
			(len(q.Cats) > 0 && !q.Cats[inc.Category]) || !q.statusOK(inc) {
			continue
		}
		if kw != "" && !strings.Contains(strings.ToLower(inc.DeviceName+" "+inc.DeviceIP+" "+inc.Subject+" "+inc.Text+" "+inc.AckedBy+" "+inc.Note), kw) {
			continue
		}
		hits = append(hits, histItem{AlertIncident: *inc, Duration: end - inc.Start})
	}
	h.mu.RUnlock()

	flaps := map[string]int{}
	for _, it := range hits {
		flaps[it.AlertID]++
	}
	for i := range hits {
		hits[i].Flaps = flaps[hits[i].AlertID]
	}
	sortHistItems(hits, q.Sort)
	st := histStatsOf(hits, q, now)
	total := len(hits)
	if q.Offset > total {
		q.Offset = total
	}
	end := total
	if q.Limit > 0 && q.Offset+q.Limit < total {
		end = q.Offset + q.Limit
	}
	return hits[q.Offset:end], total, st
}

func sortHistItems(list []histItem, by string) {
	desc := strings.HasPrefix(by, "-")
	key := strings.TrimPrefix(by, "-")
	less := func(a, b *histItem) bool {
		switch key {
		case "duration":
			if a.Duration != b.Duration {
				return a.Duration < b.Duration
			}
		case "device":
			if a.DeviceName != b.DeviceName {
				return a.DeviceName < b.DeviceName
			}
		case "level":
			if a.Level != b.Level {
				return levelRank(a.Level) < levelRank(b.Level)
			}
		case "flaps":
			if a.Flaps != b.Flaps {
				return a.Flaps < b.Flaps
			}
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.ID < b.ID
	}
	if key == "" {
		desc = true // 默认最近的在前
	}
	sort.Slice(list, func(i, j int) bool {
		if desc {
			return less(&list[j], &list[i])
		}
		return less(&list[i], &list[j])
	})
}

func histStatsOf(list []histItem, q alertHistQuery, now int64) histStats {
	st := histStats{Total: len(list)}
	var recov []int64
	var ackSum, ackN int64
	cats, devs, flap := map[string]*histCount{}, map[string]*histCount{}, map[string]*histCount{}
	days := map[string]*histDay{}
	for _, it := range list {
		crit := it.Level == "critical"
		if crit {
			st.Critical++
		} else {
			st.Warning++
		}
		if it.End == 0 {
			st.Active++
		} else {
			st.Recovered++
			recov = append(recov, it.Duration)
		}
		if it.AckedBy != "" {
			st.Handled++
			if it.AckedAt >= it.Start {
				ackSum += it.AckedAt - it.Start
				ackN++
			}
		}
		t := time.Unix(it.Start, 0)
		st.Hours[t.Hour()]++
		dk := t.Format("2006-01-02")
		d := days[dk]
		if d == nil {
			d = &histDay{Day: dk}
			days[dk] = d
		}
		if crit {
			d.Critical++
		} else {
			d.Warning++
		}
		add := func(m map[string]*histCount, k, name string) {
			c := m[k]
			if c == nil {
				c = &histCount{Key: k, Name: name}
				m[k] = c
			}
			c.Count++
			c.Dur += it.Duration
			if crit {
				c.Crit++
			}
		}
		add(cats, it.Category, "")
		add(devs, it.DeviceIP, it.DeviceName)
		add(flap, it.AlertID, it.DeviceName+" · "+firstNonEmpty(it.Subject, it.Category))
	}
	if len(recov) > 0 {
		var sum int64
		for _, d := range recov {
			sum += d
		}
		st.MTTR = sum / int64(len(recov))
		sort.Slice(recov, func(i, j int) bool { return recov[i] < recov[j] })
		st.MTTRMed = recov[len(recov)/2]
	}
	if ackN > 0 {
		st.MTTA = ackSum / ackN
	}
	// 每天一格(没告警的天也补 0, 前端画柱状图不用自己补); 范围太大时只取有数据的天
	from, to := q.From, q.To
	if to <= 0 {
		to = now
	}
	if from > 0 && to-from <= 400*86400 {
		for t := time.Unix(from, 0); !t.After(time.Unix(to, 0)); t = t.AddDate(0, 0, 1) {
			dk := t.Format("2006-01-02")
			if days[dk] == nil {
				days[dk] = &histDay{Day: dk}
			}
		}
	}
	for _, d := range days {
		st.Days = append(st.Days, *d)
	}
	sort.Slice(st.Days, func(i, j int) bool { return st.Days[i].Day < st.Days[j].Day })
	top := func(m map[string]*histCount, n, min int) []histCount {
		out := []histCount{}
		for _, c := range m {
			if c.Count >= min {
				out = append(out, *c)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Key < out[j].Key
		})
		if len(out) > n {
			out = out[:n]
		}
		return out
	}
	st.Cats = top(cats, 20, 1)
	st.Devices = top(devs, 10, 1)
	st.Flapping = top(flap, 10, 3)
	return st
}

// detail 返回一个事件的完整处理过程, 以及同一条告警最近的几次发生。
func (h *AlertHistory) detail(id string) (AlertIncident, []AlertEvent, []AlertIncident, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	inc := h.incidents[id]
	if inc == nil {
		return AlertIncident{}, nil, nil, false
	}
	evs := append([]AlertEvent(nil), h.events[id]...)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })
	var sib []AlertIncident
	for _, x := range h.incidents {
		if x.AlertID == inc.AlertID {
			sib = append(sib, *x)
		}
	}
	sort.Slice(sib, func(i, j int) bool { return sib[i].Start > sib[j].Start })
	if len(sib) > 30 {
		sib = sib[:30]
	}
	return *inc, evs, sib, true
}

// countSince 大屏 / 首页用: 某个时间点之后新出现的事件数。
func (h *AlertHistory) countSince(since int64) (total, crit int) {
	if h == nil {
		return 0, 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, inc := range h.incidents {
		if inc.Start >= since {
			total++
			if inc.Level == "critical" {
				crit++
			}
		}
	}
	return total, crit
}
