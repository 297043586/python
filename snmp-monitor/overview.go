package main

// 大屏总览的数据接口 /api/overview: 把大屏需要的东西一次性在后端汇总好,
// 页面每 15 秒拉一次, 不用前端再去逐台请求设备详情。
//
//   - 站点: 跟机柜视图同一套分组(手工指定的机柜名 > sysLocation > 未分配位置);
//     拓扑页上传过园区底图并摆过设备位置时, 站点坐标取成员设备位置的平均值。
//   - 站点间链路: 用拓扑的设备间链路按站点两两汇总, 两个方向的流量分开算。
//   - 全网趋势: 各设备的吞吐趋势按巡检轮次对齐后求和(最近 24 小时)。
//   - 另外还有: 健康评分(与巡检报告同一套规则)、风险设备排行、未处理告警流、
//     告警分类、最忙端口、以及开了 sFlow/NetFlow 时最近 5 分钟的应用 Top。

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

type ovSite struct {
	Name    string   `json:"name"`
	Devices int      `json:"devices"`
	Online  int      `json:"online"`
	Alerts  int      `json:"alerts"`
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	HasPos  bool     `json:"has_pos"`
	Names   []string `json:"names"`
}

type ovLink struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	ABMbps float64 `json:"ab_mbps"` // A -> B 方向
	BAMbps float64 `json:"ba_mbps"`
	Speed  int64   `json:"speed"`
	Count  int     `json:"count"` // 汇总了几条物理/逻辑链路
}

type ovDevice struct {
	IP     string  `json:"ip"`
	Name   string  `json:"name"`
	Site   string  `json:"site"`
	Alive  bool    `json:"alive"`
	Alerts int     `json:"alerts"`
	CPU    int     `json:"cpu"`
	Mem    int     `json:"mem"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	HasPos bool    `json:"has_pos"`
	Note   string  `json:"note,omitempty"` // 设备管理里的备注(v12: 大屏悬停/风险/告警里显示)
}

type ovAlert struct {
	Device   string `json:"device"`
	IP       string `json:"ip"`
	Level    string `json:"level"`
	Category string `json:"category"`
	Text     string `json:"text"`
	Time     string `json:"time"`
	Note     string `json:"note,omitempty"`
}

type ovRisk struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	Score int    `json:"score"`
	Grade string `json:"grade"`
	Issue string `json:"issue"`
	Note  string `json:"note,omitempty"`
}

type ovPort struct {
	Device string  `json:"device"`
	IP     string  `json:"ip"`
	Port   string  `json:"port"`
	InU    float64 `json:"in_u"`
	OutU   float64 `json:"out_u"`
	Mbps   float64 `json:"mbps"`
	Speed  int64   `json:"speed"`
	Peer   string  `json:"peer,omitempty"`

	peerPort string // 只用来给同一条链路的两端去重

}

type ovTrend struct {
	TS  int64   `json:"ts"`
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

type ovCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ovApp struct {
	App   string `json:"app"`
	Cat   string `json:"cat"`
	Bytes uint64 `json:"bytes"`
}

// siteOf 跟 handlePanels 的机柜分组保持一致。
func siteOf(rep DeviceReport, slots map[string]RackSlot) string {
	if sl, ok := slots[rep.IP]; ok && sl.Rack != "" {
		return sl.Rack
	}
	if rep.Inventory != nil && strings.TrimSpace(rep.Inventory.Location) != "" {
		return strings.TrimSpace(rep.Inventory.Location)
	}
	return "未分配位置"
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	s.hub.mu.RLock()
	reports := s.hub.reports
	checkTime := s.hub.checkTime
	s.hub.mu.RUnlock()

	var slots map[string]RackSlot
	if s.racks != nil {
		slots = s.racks.Snapshot()
	}
	topo := s.topologyPayload()
	pos := map[string]topoDevice{}
	for _, d := range topo.Devices {
		pos[d.IP] = d
	}

	// ---- 设备 / 站点 ----
	siteMap := map[string]*ovSite{}
	var siteOrder []string
	devSite := map[string]string{}
	devices := make([]ovDevice, 0, len(reports))
	online, totalPorts, upPorts := 0, 0, 0
	var throughputIn, throughputOut float64
	for _, rep := range reports {
		name := rep.SysName
		if name == "" {
			name = rep.IP
		}
		site := siteOf(rep, slots)
		devSite[rep.IP] = site
		sm := siteMap[site]
		if sm == nil {
			sm = &ovSite{Name: site}
			siteMap[site] = sm
			siteOrder = append(siteOrder, site)
		}
		sm.Devices++
		if rep.Note != "" {
			sm.Names = append(sm.Names, name+" · "+rep.Note)
		} else {
			sm.Names = append(sm.Names, name)
		}
		sm.Alerts += rep.OpenAlerts
		d := ovDevice{IP: rep.IP, Name: name, Site: site, Alive: rep.IsAlive, Alerts: rep.OpenAlerts, CPU: rep.CPUUsage, Mem: rep.MemUsage, Note: rep.Note}
		if p, ok := pos[rep.IP]; ok && p.HasPos {
			d.X, d.Y, d.HasPos = p.X, p.Y, true
		}
		devices = append(devices, d)
		if rep.IsAlive {
			online++
			sm.Online++
			if n := len(rep.TrendData); n > 0 {
				throughputIn += rep.TrendData[n-1].InMbps
				throughputOut += rep.TrendData[n-1].OutMbps
			}
		}
		totalPorts += rep.TotalPorts
		upPorts += rep.UpPorts
	}
	// 站点坐标: 成员设备里有摆过位置的取平均
	for _, name := range siteOrder {
		sm := siteMap[name]
		var sx, sy float64
		n := 0
		for _, d := range devices {
			if d.Site == name && d.HasPos {
				sx += d.X
				sy += d.Y
				n++
			}
		}
		if n > 0 {
			sm.X, sm.Y, sm.HasPos = sx/float64(n), sy/float64(n), true
		}
		sort.Slice(sm.Names, func(i, j int) bool { return naturalLess(sm.Names[i], sm.Names[j]) })
	}
	sites := make([]ovSite, 0, len(siteOrder))
	sort.Slice(siteOrder, func(i, j int) bool { return naturalLess(siteOrder[i], siteOrder[j]) })
	for _, n := range siteOrder {
		sites = append(sites, *siteMap[n])
	}

	// ---- 链路: 设备级 + 站点级汇总 ----
	devLinks := make([]ovLink, 0, len(topo.Links))
	siteLinks := map[string]*ovLink{}
	for _, l := range topo.Links {
		if _, ok := devSite[l.A]; !ok {
			continue // 手工节点("m:xx")不参与站点地图
		}
		if _, ok := devSite[l.B]; !ok {
			continue
		}
		speed := l.ASpeedMbps
		if l.BSpeedMbps > speed {
			speed = l.BSpeedMbps
		}
		devLinks = append(devLinks, ovLink{A: l.A, B: l.B, ABMbps: l.AOutMbps, BAMbps: l.BOutMbps, Speed: speed, Count: 1})
		sa, sb := devSite[l.A], devSite[l.B]
		if sa == "" || sb == "" || sa == sb {
			continue
		}
		ab, ba := l.AOutMbps, l.BOutMbps
		if sa > sb { // 统一方向, 同一对站点只算一条
			sa, sb, ab, ba = sb, sa, ba, ab
		}
		k := sa + "\x00" + sb
		sl := siteLinks[k]
		if sl == nil {
			sl = &ovLink{A: sa, B: sb}
			siteLinks[k] = sl
		}
		sl.ABMbps += ab
		sl.BAMbps += ba
		sl.Speed += speed
		sl.Count++
	}
	sLinks := make([]ovLink, 0, len(siteLinks))
	for _, l := range siteLinks {
		sLinks = append(sLinks, *l)
	}
	sort.Slice(sLinks, func(i, j int) bool { return sLinks[i].ABMbps+sLinks[i].BAMbps > sLinks[j].ABMbps+sLinks[j].BAMbps })

	// ---- 告警 ----
	var alerts []ovAlert
	catCount := map[string]int{}
	crit, warn := 0, 0
	for _, rep := range reports {
		name := rep.SysName
		if name == "" {
			name = rep.IP
		}
		for _, a := range append(append([]Alert{}, rep.Criticals...), rep.Warnings...) {
			if a.Acked || a.Closed || a.Recovered {
				continue
			}
			if a.Level == "critical" {
				crit++
			} else {
				warn++
			}
			cn := alertCategoryCN(a.Category)
			catCount[cn]++
			alerts = append(alerts, ovAlert{Device: name, IP: rep.IP, Level: a.Level, Category: cn, Text: a.Text, Time: a.LastSeen, Note: rep.Note})
		}
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Level != alerts[j].Level {
			return alerts[i].Level == "critical"
		}
		return alerts[i].Time > alerts[j].Time
	})
	if len(alerts) > 100 {
		alerts = alerts[:100]
	}
	cats := make([]ovCount, 0, len(catCount))
	for k, v := range catCount {
		cats = append(cats, ovCount{k, v})
	}
	sort.Slice(cats, func(i, j int) bool {
		return cats[i].Count > cats[j].Count || (cats[i].Count == cats[j].Count && cats[i].Name < cats[j].Name)
	})

	// ---- 风险排行 / 健康分 ----
	risks := make([]ovRisk, 0, len(reports))
	for _, rep := range reports {
		f := scoreDevice(rep)
		issue := ""
		for _, sec := range sectionOrder {
			for _, it := range f.items[sec] {
				if issue == "" && (it.Level == "bad" || it.Level == "warn") {
					issue = it.Text
				}
			}
		}
		name := rep.SysName
		if name == "" {
			name = rep.IP
		}
		risks = append(risks, ovRisk{Name: name, IP: rep.IP, Score: f.score, Grade: gradeOf(f.score, rep.IsAlive), Issue: issue, Note: rep.Note})
	}
	sort.Slice(risks, func(i, j int) bool {
		return risks[i].Score < risks[j].Score || (risks[i].Score == risks[j].Score && naturalLess(risks[i].Name, risks[j].Name))
	})
	// 大屏上显示几条在「设置 → 大屏」里调(最多 30), 这里多给一些
	if len(risks) > 30 {
		risks = risks[:30]
	}
	health := 0
	if len(reports) > 0 {
		health = buildNetworkReport(reports, checkTime).Score
	}

	// ---- 最忙端口 ----
	var hot []ovPort
	for _, rep := range reports {
		if !rep.IsAlive {
			continue
		}
		name := rep.SysName
		if name == "" {
			name = rep.IP
		}
		for _, p := range rep.Ports {
			if p.Status != "UP" || (p.InUsagePercent < 1 && p.OutUsagePercent < 1) {
				continue
			}
			op := ovPort{Device: name, IP: rep.IP, Port: p.Name, InU: p.InUsagePercent, OutU: p.OutUsagePercent, Mbps: p.InSpeedMbps + p.OutSpeedMbps, Speed: p.SpeedMbps}
			if p.LLDPRemoteDev != "" && p.LLDPRemoteDev != "-" {
				op.Peer, op.peerPort = p.LLDPRemoteDev, p.LLDPRemotePort
			}
			hot = append(hot, op)
		}
	}
	// 同一条链路两端的口利用率一样, 只留一端, 免得每条忙链路都占两行
	linkKey := func(p ovPort) string {
		if p.Peer == "" {
			return ""
		}
		a := normalizeDevName(p.Device) + "|" + normPortKey(p.Port)
		b := normalizeDevName(p.Peer) + "|" + normPortKey(p.peerPort)
		if a > b {
			a, b = b, a
		}
		return a + "~" + b
	}
	seenLink := map[string]bool{}
	dedup := hot[:0]
	for _, p := range hot {
		if k := linkKey(p); k != "" {
			if seenLink[k] {
				continue
			}
			seenLink[k] = true
		}
		dedup = append(dedup, p)
	}
	hot = dedup
	sort.Slice(hot, func(i, j int) bool {
		mi, mj := hot[i].InU, hot[j].InU
		if hot[i].OutU > mi {
			mi = hot[i].OutU
		}
		if hot[j].OutU > mj {
			mj = hot[j].OutU
		}
		return mi > mj
	})
	if len(hot) > 30 {
		hot = hot[:30]
	}

	// ---- 全网吞吐趋势: 按巡检间隔分桶求和 ----
	bucket := int64(60)
	if s.conf() != nil && int64(s.conf().CheckIntervalSeconds) > bucket {
		bucket = int64(s.conf().CheckIntervalSeconds)
	}
	since := time.Now().Unix() - 24*3600
	sum := map[int64]*ovTrend{}
	for _, rep := range reports {
		for _, t := range rep.TrendData {
			if t.TS < since {
				continue
			}
			k := t.TS / bucket * bucket
			x := sum[k]
			if x == nil {
				x = &ovTrend{TS: k}
				sum[k] = x
			}
			x.In += t.InMbps
			x.Out += t.OutMbps
		}
	}
	trend := make([]ovTrend, 0, len(sum))
	for _, v := range sum {
		trend = append(trend, *v)
	}
	sort.Slice(trend, func(i, j int) bool { return trend[i].TS < trend[j].TS })
	if n := len(trend); n > 288 { // 大屏上画不下那么多点, 等间隔抽样
		step := float64(n) / 288
		out := make([]ovTrend, 0, 288)
		for i := 0; i < 288; i++ {
			out = append(out, trend[int(float64(i)*step)])
		}
		trend = out
	}

	// ---- 应用 Top(最近 5 分钟) ----
	var apps []ovApp
	flowOn := s.flows != nil && s.conf() != nil && (s.conf().Collectors.NetflowPort > 0 || s.conf().Collectors.SflowPort > 0)
	if flowOn {
		cut := time.Now().UnixMilli() - 5*60*1000
		agg := map[string]*ovApp{}
		for _, rec := range s.flows.all() {
			if rec.TS < cut || rec.App == "" {
				continue
			}
			a := agg[rec.App]
			if a == nil {
				a = &ovApp{App: rec.App, Cat: rec.AppCat}
				agg[rec.App] = a
			}
			a.Bytes += rec.Bytes
		}
		for _, a := range agg {
			apps = append(apps, *a)
		}
		sort.Slice(apps, func(i, j int) bool { return apps[i].Bytes > apps[j].Bytes })
		if len(apps) > 8 {
			apps = apps[:8]
		}
	}

	brands := map[string]int{}
	for _, rep := range reports {
		b := rep.Brand
		if b == "" {
			b = "未知"
		}
		brands[b]++
	}
	brandList := make([]ovCount, 0, len(brands))
	for k, v := range brands {
		brandList = append(brandList, ovCount{k, v})
	}
	sort.Slice(brandList, func(i, j int) bool { return brandList[i].Count > brandList[j].Count })

	s.writeJSON(w, 200, map[string]interface{}{
		"check_time": checkTime, "health": health,
		"devices_total": len(reports), "devices_online": online,
		"ports_total": totalPorts, "ports_up": upPorts,
		"throughput_in": throughputIn, "throughput_out": throughputOut,
		"alerts_critical": crit, "alerts_warning": warn,
		"sites": sites, "site_links": sLinks, "devices": devices, "dev_links": devLinks,
		"has_background": topo.HasBackground,
		"alerts":         alerts, "alert_cats": cats, "risks": risks, "hot_ports": hot,
		"trend": trend, "apps": apps, "flow_enabled": flowOn, "brands": brandList,
	})
}
