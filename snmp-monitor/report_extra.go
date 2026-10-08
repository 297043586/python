package main

// 巡检报告增强(v14)。
//
// 以前的报告只有"这一刻"的状态: 评分、问题清单、几张表。做周报 / 月报时最常被问的几件事——
// 跟上次比变好还是变坏了、这段时间告警多不多、多久恢复、哪几台最不省心、端口还够不够用、
// 资产清单——都得自己去翻。这里在规则报告的基础上补充:
//
//   - 与上次报告对比: 评分变化, 新增 / 已解决的问题(每份报告存一份问题快照 Snapshot 用来比);
//   - 近 7 天告警统计: 次数、严重、平均恢复 / 响应时长、每天趋势、告警最多的设备、反复出现的告警;
//   - 二层健康: 🩺 二层体检里的严重 / 警告结论;
//   - 图表: 评分分布、CPU / 内存 TOP10、端口使用率;
//   - 端口容量(扩容预警)、运行时长(近期重启 / 长期未重启)、资产清单(型号 / 序列号 / 版本 / 位置 / 联系人)。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// reportSnapshot 是一份报告里"有哪些问题"的结构化快照, 下一份报告拿它来对比。
type reportSnapshot struct {
	Score  int               `json:"score"`
	Issues map[string]string `json:"issues"` // 稳定的问题键 -> 展示文字
}

// 问题文字里会变的数字(错包 +12、收光 -21.3 dBm、利用率 85%)去掉, 端口名 / IP 保留,
// 这样同一个问题两次巡检数值不同也能认成同一个。
var issueNumRe = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?\s*(?:%|dBm|Mbps|Gbps|pps|M\b|G\b|次|包/秒|包|条|个|项|秒|分钟|分|小时|天|℃|°C)|\+\d+`)

func issueKey(parts ...string) string {
	for i := range parts {
		parts[i] = strings.TrimSpace(issueNumRe.ReplaceAllString(parts[i], "#"))
	}
	return strings.Join(parts, "|")
}

func portOnly(s string) string {
	if i := strings.IndexByte(s, '('); i > 0 {
		return s[:i]
	}
	return s
}

// ----------------- 全网报告 -----------------

func networkSnapshot(rep *InspReport, reports []DeviceReport, l2 []L2Finding) *reportSnapshot {
	snap := &reportSnapshot{Score: rep.Score, Issues: map[string]string{}}
	add := func(k, text string) { snap.Issues[k] = text }
	for _, r := range reports {
		name := r.SysName
		if name == "" {
			name = r.IP
		}
		if !r.IsAlive {
			add("离线|"+r.IP, "设备离线: "+name+"("+r.IP+")")
			continue
		}
		f := scoreDevice(r)
		if f.score < 75 {
			add("低分|"+r.IP, fmt.Sprintf("评分偏低: %s(%s)", name, r.IP))
		}
		for _, p := range f.halfPorts {
			add("半双工|"+r.IP+"|"+portOnly(p), "半双工: "+name+" "+p)
		}
		for _, p := range f.lowOptPorts {
			add("弱光|"+r.IP+"|"+portOnly(p), "弱光: "+name+" "+p)
		}
		for _, p := range f.hotPorts {
			add("高负载|"+r.IP+"|"+portOnly(p), "高负载: "+name+" "+p)
		}
		for _, a := range append(append([]Alert{}, r.Criticals...), r.Warnings...) {
			if !a.Recovered && !a.Closed {
				add(issueKey("告警", r.IP, a.Category, a.Subject), "告警: "+name+" "+a.Text)
			}
		}
	}
	for _, f := range l2 {
		if f.Level == "info" {
			continue
		}
		if f.Device != "" {
			add(issueKey("二层", f.Kind, f.DeviceIP, f.Port), "二层: "+f.Title)
		} else {
			add(issueKey("二层", f.Kind, f.Title), "二层: "+f.Title)
		}
	}
	return snap
}

// compareSection 跟上一份同类报告比, 生成"与上次报告对比"章节。
func compareSection(prev *InspReport, cur *reportSnapshot) *ReportSection {
	if prev == nil {
		return nil
	}
	sec := &ReportSection{Title: "与上次报告对比", Note: "上次报告: " + prev.CreatedAt + "(数据 " + prev.DataTime + ")"}
	d := cur.Score - prev.Score
	lvl, word := "info", "持平"
	switch {
	case d > 0:
		lvl, word = "ok", fmt.Sprintf("上升 %d 分", d)
	case d <= -5:
		lvl, word = "warn", fmt.Sprintf("下降 %d 分", -d)
	case d < 0:
		word = fmt.Sprintf("下降 %d 分", -d)
	}
	sec.Items = append(sec.Items, ReportItem{lvl, fmt.Sprintf("评分 %d → %d, %s", prev.Score, cur.Score, word)})
	if prev.Snapshot == nil {
		sec.Items = append(sec.Items, ReportItem{"info", "上一份报告是旧版本生成的, 没有问题快照, 只能对比评分"})
		return sec
	}
	var added, solved []string
	for k, t := range cur.Issues {
		if _, ok := prev.Snapshot.Issues[k]; !ok {
			added = append(added, t)
		}
	}
	for k, t := range prev.Snapshot.Issues {
		if _, ok := cur.Issues[k]; !ok {
			solved = append(solved, t)
		}
	}
	sort.Strings(added)
	sort.Strings(solved)
	sec.Items = append(sec.Items, ReportItem{"info", fmt.Sprintf("新增问题 %d 项, 已解决 %d 项, 仍然存在 %d 项", len(added), len(solved), len(cur.Issues)-len(added))})
	for i, t := range added {
		if i >= 20 {
			sec.Items = append(sec.Items, ReportItem{"info", fmt.Sprintf("…… 另有 %d 项新增", len(added)-20)})
			break
		}
		sec.Items = append(sec.Items, ReportItem{"warn", "🆕 " + t})
	}
	for i, t := range solved {
		if i >= 20 {
			sec.Items = append(sec.Items, ReportItem{"info", fmt.Sprintf("…… 另有 %d 项已解决", len(solved)-20)})
			break
		}
		sec.Items = append(sec.Items, ReportItem{"ok", "✅ " + t})
	}
	return sec
}

// alertWeekSection: 近 7 天告警统计; ip 非空时只统计这台设备。
func alertWeekSection(hist *AlertHistory, now time.Time, ip string) []ReportSection {
	if hist == nil {
		return nil
	}
	q := alertHistQuery{From: now.AddDate(0, 0, -7).Unix(), To: now.Unix(), Limit: 1}
	if ip != "" {
		q.Devices = map[string]bool{ip: true}
	}
	_, _, st := hist.query(q)
	sec := ReportSection{Title: "近 7 天告警统计"}
	if st.Total == 0 {
		sec.Items = []ReportItem{{"ok", "近 7 天没有发生告警"}}
		return []ReportSection{sec}
	}
	lvl := "info"
	if st.Critical > 0 {
		lvl = "warn"
	}
	sec.Items = append(sec.Items, ReportItem{lvl, fmt.Sprintf("共 %d 次告警(严重 %d / 警告 %d), 目前仍未恢复 %d 次", st.Total, st.Critical, st.Warning, st.Active)})
	if st.Recovered > 0 {
		sec.Items = append(sec.Items, ReportItem{"info", fmt.Sprintf("已恢复 %d 次, 平均恢复用时 %s(中位数 %s)", st.Recovered, fmtDuration(st.MTTR), fmtDuration(st.MTTRMed))})
	}
	if st.MTTA > 0 {
		sec.Items = append(sec.Items, ReportItem{"info", "平均响应(确认)用时 " + fmtDuration(st.MTTA)})
	}
	if len(st.Days) > 0 {
		ch := &ReportChart{Type: "stack", Unit: "次", Colors: []string{"#ef4444", "#f59e0b"}}
		for _, d := range st.Days {
			lab := d.Day
			if len(lab) >= 10 {
				lab = strings.TrimPrefix(lab[5:], "0")
			}
			ch.Labels = append(ch.Labels, lab)
			ch.Values = append(ch.Values, float64(d.Critical))
			ch.Extra = append(ch.Extra, float64(d.Warning))
		}
		sec.Chart = ch
		sec.Note = "柱子: 每天新增告警, 红 = 严重, 橙 = 警告"
	}
	out := []ReportSection{sec}
	if ip == "" && len(st.Devices) > 0 {
		t := &ReportTable{Headers: []string{"设备", "告警次数", "其中严重", "累计持续"}}
		for i, c := range st.Devices {
			if i >= 10 {
				break
			}
			name := c.Name
			if name == "" {
				name = c.Key
			}
			t.Rows = append(t.Rows, []string{name, fmt.Sprint(c.Count), fmt.Sprint(c.Crit), fmtDuration(c.Dur)})
		}
		out = append(out, ReportSection{Title: "告警最多的设备(近 7 天)", Table: t})
	}
	if len(st.Flapping) > 0 {
		var items []ReportItem
		for i, c := range st.Flapping {
			if i >= 10 {
				break
			}
			name := c.Name
			if name == "" {
				name = c.Key
			}
			items = append(items, ReportItem{"warn", fmt.Sprintf("%s: 7 天内反复出现 %d 次", name, c.Count)})
		}
		out = append(out, ReportSection{Title: "反复出现的告警", Note: "同一条告警恢复了又出现, 说明根因没解决(接触不良的线缆、临界的阈值、频繁抖动的链路)", Items: items})
	}
	return out
}

func l2Section(l2 []L2Finding, ip string) *ReportSection {
	sec := &ReportSection{Title: "二层健康(环路 / 风暴 / ARP)", Note: "来自「🩺 二层体检」, 只列严重和警告"}
	n := 0
	for _, f := range l2 {
		if f.Level == "info" || (ip != "" && f.DeviceIP != ip) {
			continue
		}
		if n >= 25 {
			sec.Items = append(sec.Items, ReportItem{"info", "…… 更多结论见「🩺 二层体检」"})
			break
		}
		lvl := "warn"
		if f.Level == "critical" {
			lvl = "bad"
		}
		sec.Items = append(sec.Items, ReportItem{lvl, f.Title})
		n++
	}
	if n == 0 {
		sec.Items = []ReportItem{{"ok", "未发现广播风暴、环路、ARP 欺骗等二层异常"}}
	}
	return sec
}

func topHBar(labels []string, vals []float64, n int, unit string, max float64) *ReportChart {
	idx := make([]int, len(vals))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vals[idx[a]] > vals[idx[b]] })
	ch := &ReportChart{Type: "hbar", Unit: unit, Max: max}
	for i, k := range idx {
		if i >= n {
			break
		}
		ch.Labels = append(ch.Labels, labels[k])
		ch.Values = append(ch.Values, vals[k])
	}
	return ch
}

// enrichNetworkReport 在 buildNetworkReport 的结果上补充 v14 的章节。
func enrichNetworkReport(rep *InspReport, reports []DeviceReport, hist *AlertHistory, prev *InspReport, cfg *Config, now time.Time) {
	l2 := l2HealthFindings(reports, cfg)
	rep.Snapshot = networkSnapshot(rep, reports, l2)

	// 评分分布
	dist := []float64{0, 0, 0, 0, 0}
	var names []string
	var cpu, mem, portUse []float64
	var portNames []string
	for _, r := range reports {
		name := r.SysName
		if name == "" {
			name = r.IP
		}
		if !r.IsAlive {
			dist[4]++
			continue
		}
		s := scoreDevice(r).score
		switch {
		case s >= 90:
			dist[0]++
		case s >= 75:
			dist[1]++
		case s >= 60:
			dist[2]++
		default:
			dist[3]++
		}
		names = append(names, name)
		cpu = append(cpu, float64(r.CPUUsage))
		mem = append(mem, float64(r.MemUsage))
		total, upN := r.TotalPorts, r.UpPorts
		if total == 0 {
			for _, p := range r.Ports {
				total++
				if p.Status == "UP" {
					upN++
				}
			}
		}
		if total > 0 {
			portNames = append(portNames, name)
			portUse = append(portUse, float64(upN)*100/float64(total))
		}
	}
	head := []ReportSection{}
	if c := compareSection(prev, rep.Snapshot); c != nil {
		head = append(head, *c)
	}
	head = append(head, ReportSection{Title: "设备评分分布", Chart: &ReportChart{Type: "bar", Unit: "台",
		Labels: []string{"优秀 ≥90", "良好 75-89", "一般 60-74", "较差 <60", "离线"}, Values: dist,
		Colors: []string{"#10b981", "#3b82f6", "#f59e0b", "#ef4444", "#94a3b8"}}})

	// 近 7 天告警 + 二层健康插在"重点风险设备"后面
	var mid []ReportSection
	mid = append(mid, alertWeekSection(hist, now, "")...)
	mid = append(mid, *l2Section(l2, ""))
	var secs []ReportSection
	secs = append(secs, head...)
	inserted := false
	for _, s := range rep.Sections {
		secs = append(secs, s)
		if s.Title == "重点风险设备" {
			secs = append(secs, mid...)
			inserted = true
		}
	}
	if !inserted {
		secs = append(secs, mid...)
	}

	if len(names) > 0 {
		secs = append(secs, ReportSection{Title: "CPU 使用率 TOP 10", Chart: topHBar(names, cpu, 10, "%", 100)},
			ReportSection{Title: "内存使用率 TOP 10", Chart: topHBar(names, mem, 10, "%", 100)})
	}
	if len(portNames) > 0 {
		sec := ReportSection{Title: "端口容量", Note: "物理口已使用(UP)的比例; ≥ 85% 建议规划扩容", Chart: topHBar(portNames, portUse, 15, "%", 100)}
		for i, n := range portNames {
			if portUse[i] >= 85 {
				sec.Items = append(sec.Items, ReportItem{"warn", fmt.Sprintf("扩容预警: %s 端口使用率 %.0f%%", n, portUse[i])})
			}
		}
		if len(sec.Items) == 0 {
			sec.Items = []ReportItem{{"ok", "所有设备端口使用率都在 85% 以下"}}
		}
		secs = append(secs, sec)
	}

	// 运行时长: 近期重启 / 超过两年没重启
	var up []ReportItem
	for _, r := range reports {
		if !r.IsAlive || r.UpTimeSec <= 0 {
			continue
		}
		name := r.SysName
		if name == "" {
			name = r.IP
		}
		if r.UpTimeSec < 86400 {
			up = append(up, ReportItem{"warn", fmt.Sprintf("%s(%s) 最近 24 小时内重启过(已运行 %s), 确认是否为计划内操作", name, r.IP, fmtDuration(r.UpTimeSec))})
		} else if r.UpTimeSec > 730*86400 {
			up = append(up, ReportItem{"info", fmt.Sprintf("%s(%s) 已连续运行 %d 天, 长期未重启通常也意味着长期未升级补丁", name, r.IP, r.UpTimeSec/86400)})
		}
	}
	if len(up) == 0 {
		up = []ReportItem{{"ok", "没有近期异常重启的设备"}}
	}
	secs = append(secs, ReportSection{Title: "运行时长", Items: up})

	// 资产清单
	at := &ReportTable{Headers: []string{"设备", "IP", "厂商", "型号", "序列号", "软件版本", "位置", "联系人", "运行时长"}}
	for _, r := range reports {
		inv := r.Inventory
		if inv == nil {
			inv = &DeviceInventory{}
		}
		name := r.SysName
		if name == "" {
			name = r.IP
		}
		at.Rows = append(at.Rows, []string{name, r.IP, dash(r.Brand), dash(inv.Model), dash(inv.Serial), dash(inv.SoftVer), dash(inv.Location), dash(inv.Contact), dash(r.UpTimeStr)})
	}
	sort.Slice(at.Rows, func(i, j int) bool { return ipLess(at.Rows[i][1], at.Rows[j][1]) })
	secs = append(secs, ReportSection{Title: "资产清单", Note: "型号 / 序列号 / 版本需要在「设备管理」里开启扩展信息采集; 位置 / 联系人可以在设备管理里手工填写", Table: at})

	rep.Sections = secs
	var lv = map[string]int{}
	for _, f := range l2 {
		lv[f.Level]++
	}
	if lv["critical"]+lv["warning"] > 0 {
		rep.Summary = append(rep.Summary, fmt.Sprintf("二层体检: 严重 %d 项, 警告 %d 项", lv["critical"], lv["warning"]))
		rep.Advice = append([]string{"先处理「二层健康」里的严重项(环路 / 风暴 / ARP 欺骗会影响整个广播域), 详细证据和排查命令见「🩺 二层体检」"}, rep.Advice...)
	}
}

// ----------------- 单台设备报告 -----------------

func deviceSnapshot(rep *InspReport) *reportSnapshot {
	snap := &reportSnapshot{Score: rep.Score, Issues: map[string]string{}}
	for _, s := range rep.Sections {
		for _, it := range s.Items {
			if it.Level == "bad" || it.Level == "warn" {
				snap.Issues[issueKey(s.Title, it.Text)] = s.Title + ": " + it.Text
			}
		}
	}
	return snap
}

func enrichDeviceReport(rep *InspReport, r DeviceReport, all []DeviceReport, hist *AlertHistory, prev *InspReport, cfg *Config, now time.Time) {
	rep.Snapshot = deviceSnapshot(rep)
	var secs []ReportSection
	if c := compareSection(prev, rep.Snapshot); c != nil {
		secs = append(secs, *c)
	}
	secs = append(secs, rep.Sections...)
	if r.IsAlive {
		var names []string
		var use []float64
		for _, p := range r.Ports {
			if p.Status != "UP" {
				continue
			}
			names = append(names, p.Name)
			use = append(use, maxF(p.InUsagePercent, p.OutUsagePercent))
		}
		if len(names) > 0 {
			secs = append(secs, ReportSection{Title: "端口利用率 TOP 10", Note: "入 / 出方向取较大的一个", Chart: topHBar(names, use, 10, "%", 100)})
		}
		if r.STP != nil {
			lvl := "ok"
			txt := fmt.Sprintf("生成树启动以来累计拓扑变化 %d 次, 最近一次在 %s前", r.STP.TopChanges, fmtDuration(r.STP.SinceChangeSec))
			if r.STP.Delta > 0 {
				lvl = "warn"
				txt += fmt.Sprintf("; 上一轮巡检之后又变化了 %d 次", r.STP.Delta)
			}
			secs = append(secs, ReportSection{Title: "生成树", Items: []ReportItem{{lvl, txt}}})
		}
		secs = append(secs, *l2Section(l2HealthFindings(all, cfg), r.IP))
	}
	secs = append(secs, alertWeekSection(hist, now, r.IP)...)
	rep.Sections = secs
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Latest 返回最新的一份同类报告(kind + 设备 IP), 用来做对比; except 是正在生成的这一份。
func (s *ReportStore) Latest(kind, ip string) *InspReport {
	for _, m := range s.List(false) {
		if m.Kind == kind && (kind != "device" || m.DeviceIP == ip) {
			if r, ok := s.Get(m.ID); ok {
				return r
			}
		}
	}
	return nil
}
