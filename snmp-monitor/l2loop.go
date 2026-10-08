package main

// 二层环路研判。
//
// 环路时单看哪一种现象都不可靠: 风暴可能是终端中毒, 生成树变化可能是终端开关机,
// 一两个 MAC 出现在两个口可能只是无线漫游或者换了个口。这里把能拿到的证据按可信度分层,
// 再用同一台设备上的其他现象互相印证:
//
//  1. 同一台设备、同一 VLAN 内, 被监控设备自己的 MAC(上游交换机 / 网关)从两个不同的
//     逻辑口学到。生成树正常时阻塞口不学 MAC, 上游设备只会从一个口进来 —— 最直接的环路证据。
//  2. 同一台设备、同一 VLAN 内, 一批 MAC 在两个逻辑口之间来回(一次 FDB walk 里同一条
//     VLAN+MAC 出现在多个口)。
//  3. LLDP 明确显示设备 X 接在 A 口, X 自己的 MAC 却从一个没有 LLDP 邻居的 B 口学到:
//     X 的流量绕了另一条路进来(环路, 或经过非网管设备的冗余路径正在转发)。
//  4. 跨设备: 同一个 MAC 同时出现在两台设备的接入口上(MAC 总表裁决后的 CrossDeviceFlap)。
//
// 佐证: 同一台设备本轮有广播风暴 / 生成树变化超过阈值 / 多个端口抖动。
// 有佐证时等级上调一档; 少量 MAC 漂移没有佐证时只报 info, 并说明漫游也会这样。
//
// 注意: 环路时上游设备的 MAC 会从接入口学到, netCtx 按 MAC 推断"这个口朝向谁"会把这个
// 接入口当成级联口。这里的判断只用 LLDP 和端口自身属性, 不依赖按 MAC 推断出的朝向;
// 认定的疑似环路口(suspect)也会让「没开 LLDP」一类结论跳过这些口, 避免把环路报成正常级联。

import (
	"fmt"
	"sort"
	"strings"
)

// loopSignal 是一台设备本轮可以佐证环路的现象。
type loopSignal struct {
	stormPorts []string // "Gi0/1(8000 pps)"
	stpDelta   int64
	flaps      int
}

type loopAnalysis struct {
	nc       *netCtx
	signals  map[string]*loopSignal     // 设备 IP -> 佐证
	suspect  map[string]map[string]bool // 设备 IP -> portNorm -> 疑似环路口
	stpTh    int64
	nameToIP map[string]string
}

func newLoopAnalysis(reports []DeviceReport, nc *netCtx, stormTh float64, stpTh int64) *loopAnalysis {
	la := &loopAnalysis{nc: nc, signals: map[string]*loopSignal{}, suspect: map[string]map[string]bool{},
		stpTh: stpTh, nameToIP: map[string]string{}}
	for _, r := range reports {
		if n := normalizeDevName(r.SysName); n != "" {
			la.nameToIP[n] = r.IP
		}
		if !r.IsAlive {
			continue
		}
		s := &loopSignal{}
		for _, p := range r.Ports {
			x := p.Ext
			if x == nil {
				continue
			}
			if p.Status == "UP" && x.InBcastPps >= stormTh {
				s.stormPorts = append(s.stormPorts, fmt.Sprintf("%s(%.0f pps)", p.Name, x.InBcastPps))
			}
			if x.LastChangeSec >= 0 && x.LastChangeSec < 600 {
				s.flaps++
			}
		}
		if r.STP != nil {
			s.stpDelta = r.STP.Delta
		}
		la.signals[r.IP] = s
	}
	return la
}

// corroboration 列出这些设备上能佐证环路的现象, 空表示没有佐证。
func (la *loopAnalysis) corroboration(devs ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range devs {
		if seen[d] {
			continue
		}
		seen[d] = true
		s := la.signals[d]
		if s == nil {
			continue
		}
		name := la.nc.name(d)
		if len(s.stormPorts) > 0 {
			out = append(out, name+" 广播风暴: "+strings.Join(firstN(s.stormPorts, 4), ", "))
		}
		if s.stpDelta >= la.stpTh {
			out = append(out, fmt.Sprintf("%s 生成树本轮变化 %d 次", name, s.stpDelta))
		}
		if s.flaps >= 3 {
			out = append(out, fmt.Sprintf("%s 有 %d 个口 10 分钟内 UP/DOWN 过", name, s.flaps))
		}
	}
	return out
}

func (la *loopAnalysis) markSuspect(dev, port string) {
	if la.suspect[dev] == nil {
		la.suspect[dev] = map[string]bool{}
	}
	la.suspect[dev][portNorm(port)] = true
}

func (la *loopAnalysis) isSuspect(dev, port string) bool {
	return la.suspect[dev][portNorm(port)]
}

func raiseLevel(l string) string {
	if l == "info" {
		return "warning"
	}
	return "critical"
}

func levelByCount(n, warnAt, critAt int) string {
	switch {
	case n >= critAt:
		return "critical"
	case n >= warnAt:
		return "warning"
	default:
		return "info"
	}
}

// ---------- 端口角色(只用 LLDP / 聚合 / 端口属性, 不用按 MAC 推断的朝向) ----------

type loopPorts struct {
	agg   map[string]string     // portNorm(成员口) -> 聚合口名
	aggs  map[string]bool       // portNorm(聚合口)
	port  map[string]PortDetail // portNorm -> 端口
	lldp  map[string]bool       // portNorm(逻辑口) -> 有任何 LLDP 邻居(含未纳管)
	toDev map[string][]string   // 被监控设备 IP -> LLDP 朝向它的逻辑口
}

func (la *loopAnalysis) portsOf(r DeviceReport) loopPorts {
	lp := loopPorts{agg: map[string]string{}, aggs: map[string]bool{}, port: map[string]PortDetail{},
		lldp: map[string]bool{}, toDev: map[string][]string{}}
	for _, g := range r.AggGroups {
		lp.aggs[portNorm(g.Name)] = true
		for _, m := range g.Members {
			lp.agg[portNorm(m)] = g.Name
		}
	}
	for _, p := range r.Ports {
		lp.port[portNorm(p.Name)] = p
		if p.AggGroup != "" {
			lp.agg[portNorm(p.Name)] = p.AggGroup
			lp.aggs[portNorm(p.AggGroup)] = true
		}
	}
	peerSeen := map[string]bool{}
	for _, p := range r.Ports {
		if p.LLDPRemoteDev == "" || p.LLDPRemoteDev == "-" {
			continue
		}
		logical := lp.logical(p.Name)
		k := portNorm(logical)
		lp.lldp[k] = true
		peer := la.nameToIP[normalizeDevName(p.LLDPRemoteDev)]
		if peer == "" && p.LLDPRemoteIP != "" {
			peer = la.nc.ownIP[p.LLDPRemoteIP]
		}
		if peer != "" && peer != r.IP && !peerSeen[peer+"|"+k] {
			peerSeen[peer+"|"+k] = true
			lp.toDev[peer] = append(lp.toDev[peer], logical)
		}
	}
	return lp
}

// logical 把聚合成员口归到聚合口: 同一聚合组的成员口之间 MAC 来回是正常的负载分担。
func (lp loopPorts) logical(port string) string {
	if g := lp.agg[portNorm(port)]; g != "" {
		return g
	}
	return port
}

// isUplink: 有 LLDP 邻居、是聚合口, 或者本机按 MAC 数判成上联的口。
func (lp loopPorts) isUplink(port string) bool {
	k := portNorm(port)
	if lp.lldp[k] || lp.aggs[k] {
		return true
	}
	p, ok := lp.port[k]
	return ok && p.IsUplink
}

// ---------- 研判 ----------

type loopGroup struct {
	ports []string        // 逻辑口, 已排序
	macs  []string        // "MAC (VLAN x)"
	infra map[string]bool // 从这组口都学到的被监控设备, "设备名 (VLAN x)"
}

func (la *loopAnalysis) findings(reports []DeviceReport, macs []globalMACEntry) []L2Finding {
	var out []L2Finding
	covered := map[string]bool{} // 已经有 MAC 证据的设备, 不再单独报"风暴 + 生成树"
	for _, r := range reports {
		if !r.IsAlive {
			continue
		}
		fs := la.intraDevice(r)
		if len(fs) > 0 {
			covered[r.IP] = true
		}
		out = append(out, fs...)
	}
	cross, crossDevs := la.crossDevice(macs)
	for d := range crossDevs {
		covered[d] = true
	}
	out = append(out, cross...)

	// 没有 MAC 证据, 但同一台设备风暴和生成树变化同时出现
	for _, r := range reports {
		s := la.signals[r.IP]
		if covered[r.IP] || s == nil || len(s.stormPorts) == 0 || s.stpDelta < la.stpTh {
			continue
		}
		out = append(out, L2Finding{Level: "warning", Kind: "loop", DeviceIP: r.IP, Device: r.SysName,
			Title:    fmt.Sprintf("疑似环路: %s 广播风暴的同时生成树本轮变化 %d 次", r.SysName, s.stpDelta),
			Detail:   "风暴和拓扑变化同时出现是环路的典型表现; 本轮 MAC 表里没抓到漂移(可能 MAC 表没采到, 或者环路已被生成树切断后又恢复), 需要在设备上确认",
			Evidence: la.corroboration(r.IP),
			Steps: []string{
				"看 MAC 漂移记录, 找来回漂移的两个口: " + l2Cmd(la.nc, r.IP, "mac_flap", ""),
				"找收到 TC 最多的口: " + l2Cmd(la.nc, r.IP, "stp", ""),
				"风暴最高的非上联口先 shutdown 观察 1~2 分钟; 回落了源头就在这个口下面",
				"确认后开环路检测: " + l2Cmd(la.nc, r.IP, "loop_detect", "<接入口>"),
			}})
	}
	return out
}

// intraDevice 找同一台设备内部的环路证据(上面的 1/2/3 类)。
func (la *loopAnalysis) intraDevice(r DeviceReport) []L2Finding {
	lp := la.portsOf(r)
	groups := map[string]*loopGroup{}
	group := func(ports []string) *loopGroup {
		sort.Slice(ports, func(i, j int) bool { return naturalLess(ports[i], ports[j]) })
		k := strings.Join(ports, "\x00")
		g := groups[k]
		if g == nil {
			g = &loopGroup{ports: ports, infra: map[string]bool{}}
			groups[k] = g
		}
		return g
	}
	logicalSet := func(locs []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, l := range locs {
			p := lp.logical(l)
			if k := portNorm(p); !seen[k] {
				seen[k] = true
				out = append(out, p)
			}
		}
		return out
	}

	// 1 / 2: VLAN+MAC 粒度的转发表。没有确切 VLAN 的行(dot1d / 按 VLAN 团体名合并的)
	// 同一个 MAC 出现在不同口可能只是不同 VLAN, 不用来判断。
	peerPorts := map[string]map[string]string{} // 设备IP|VLAN -> portNorm -> 口名
	for _, e := range r.FDBTable {
		if e.VlanID == "" || e.VlanID == "-" {
			continue
		}
		locs := e.Locations
		if len(locs) == 0 {
			locs = []string{e.Port}
		}
		ports := logicalSet(locs)
		if peer := la.nc.devMAC[strings.ToUpper(e.MAC)]; peer != "" && peer != r.IP {
			k := peer + "|" + e.VlanID
			if peerPorts[k] == nil {
				peerPorts[k] = map[string]string{}
			}
			for _, p := range ports {
				peerPorts[k][portNorm(p)] = p
			}
		}
		if len(ports) >= 2 {
			g := group(ports)
			g.macs = append(g.macs, e.MAC+" (VLAN "+e.VlanID+")")
		}
	}
	for k, set := range peerPorts {
		if len(set) < 2 {
			continue
		}
		peer, vlan, _ := strings.Cut(k, "|")
		ports := make([]string, 0, len(set))
		for _, p := range set {
			ports = append(ports, p)
		}
		group(ports).infra[la.nc.name(peer)+" (VLAN "+vlan+")"] = true
	}

	var out []L2Finding
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, la.intraFinding(r, lp, groups[k]))
	}

	// 3: LLDP 说 X 在 A 口, X 的 MAC 却从没有 LLDP 邻居的 B 口进来
	odd := map[string]map[string]bool{} // 口名 -> "设备名 (LLDP 在 A)"
	oddMAC := map[string][]string{}
	for _, e := range r.MACTable {
		peer := la.nc.devMAC[strings.ToUpper(e.MAC)]
		if peer == "" || peer == r.IP || len(lp.toDev[peer]) == 0 {
			continue
		}
		port := lp.logical(e.Port)
		if lp.lldp[portNorm(port)] || la.isSuspect(r.IP, port) {
			continue
		}
		if odd[port] == nil {
			odd[port] = map[string]bool{}
		}
		odd[port][la.nc.name(peer)+" (LLDP 在 "+strings.Join(lp.toDev[peer], "、")+")"] = true
		oddMAC[port] = append(oddMAC[port], e.MAC)
	}
	oddPorts := make([]string, 0, len(odd))
	for p := range odd {
		oddPorts = append(oddPorts, p)
	}
	sort.Slice(oddPorts, func(i, j int) bool { return naturalLess(oddPorts[i], oddPorts[j]) })
	for _, port := range oddPorts {
		peers := make([]string, 0, len(odd[port]))
		for p := range odd[port] {
			peers = append(peers, p)
		}
		sort.Strings(peers)
		la.markSuspect(r.IP, port)
		lvl := "warning"
		corr := la.corroboration(r.IP)
		if len(corr) > 0 {
			lvl = "critical"
		}
		sort.Strings(oddMAC[port])
		ev := append([]string{"从 " + port + " 学到的设备 MAC: " + strings.Join(firstN(oddMAC[port], 6), ", ")}, corr...)
		out = append(out, L2Finding{Level: lvl, Kind: "loop", DeviceIP: r.IP, Device: r.SysName, Port: port,
			Title:    fmt.Sprintf("疑似环路: %s 的 %s 没有 LLDP 邻居, 却学到了 %s 自己的 MAC", r.SysName, port, strings.Join(peers, "、")),
			Detail:   "LLDP 显示那台设备直接接在另一个口上, 它的流量却从这个口进来: 这个口下面有一条通往网络其他部分的旁路(私接的线 / 傻瓜交换机 / 无线网桥)。如果是有意设计的冗余链路, 说明生成树当前选了这条路、主链路被阻塞",
			Evidence: ev,
			Steps: []string{
				"确认这个口下面学到了哪些 MAC: " + l2Cmd(la.nc, r.IP, "mac_port", port),
				"看 MAC 漂移记录: " + l2Cmd(la.nc, r.IP, "mac_flap", ""),
				"不是设计好的冗余链路: shutdown 这个口, 看风暴 / 漂移是否停止 —— " + l2Cmd(la.nc, r.IP, "shut", port),
				"去现场查这个口接了什么; 恢复前开环路检测和边缘口保护: " + l2Cmd(la.nc, r.IP, "loop_detect", port),
			}})
	}
	return out
}

func (la *loopAnalysis) intraFinding(r DeviceReport, lp loopPorts, g *loopGroup) L2Finding {
	n := len(g.macs)
	var access, uplink []string
	for _, p := range g.ports {
		if lp.isUplink(p) {
			uplink = append(uplink, p)
		} else {
			access = append(access, p)
		}
	}
	infra := make([]string, 0, len(g.infra))
	for d := range g.infra {
		infra = append(infra, d)
	}
	sort.Strings(infra)
	corr := la.corroboration(r.IP)

	lvl := levelByCount(n, 3, 10)
	if len(infra) > 0 {
		lvl = "critical"
	} else if len(corr) > 0 {
		lvl = raiseLevel(lvl)
	}
	portList := strings.Join(g.ports, "、")
	title := fmt.Sprintf("MAC 漂移: %s 的 %s 之间有 %d 个 MAC 来回", r.SysName, portList, n)
	if len(infra) > 0 {
		title = fmt.Sprintf("二层环路: %s 的 %s 同时学到了 %s 的 MAC", r.SysName, portList, strings.Join(infra, "、"))
	}
	var detail string
	switch {
	case len(uplink) == 0:
		detail = "都是接入口: 这几个口下面在现场被连通了(网线两头插回同一台交换机 / 两个口接到同一台傻瓜交换机 / 无线网桥)"
	case len(access) == 0:
		detail = "都是上联/级联口: 同一个 VLAN 里多条链路同时在转发 —— 生成树没生效、两端聚合配置不一致, 或者某条链路被当成了普通口"
	default:
		detail = "上联口和接入口之间来回: 上游的流量从接入口 " + strings.Join(access, "、") + " 绕回来了, 这个接入口下面连到了网络的其他部分"
	}
	if len(infra) == 0 && n < 3 && len(corr) == 0 {
		detail += "。只有少量 MAC 且没有风暴 / 生成树变化佐证, 终端换口、无线漫游也会这样, 连续几轮都出现再处理"
	}

	target := access
	if len(target) == 0 {
		target = g.ports
	}
	for _, p := range target {
		la.markSuspect(r.IP, p)
	}

	var ev []string
	if len(infra) > 0 {
		ev = append(ev, "从多个口学到的被监控设备: "+strings.Join(infra, "、"))
	}
	if n > 0 {
		sort.Strings(g.macs)
		ev = append(ev, fmt.Sprintf("同一 VLAN 内在这几个口之间出现的 MAC(%d 个): %s", n, strings.Join(firstN(g.macs, 8), ", ")))
	}
	ev = append(ev, corr...)

	steps := []string{"看 MAC 漂移记录, 确认是哪两个口在来回: " + l2Cmd(la.nc, r.IP, "mac_flap", "")}
	if len(access) == 0 {
		steps = append(steps, "检查这几条上联/级联链路的生成树状态, 正常应该只有一条在转发: "+l2Cmd(la.nc, r.IP, "stp", ""),
			"两端是聚合就核对两边的聚合配置(成员口、LACP 模式)是否一致")
	} else {
		steps = append(steps,
			"断开接入口验证(漂移 / 风暴停止即确认): "+l2Cmd(la.nc, r.IP, "shut", target[0]),
			"去现场查 "+strings.Join(target, "、")+" 接了什么; 恢复前开环路检测和边缘口保护: "+l2Cmd(la.nc, r.IP, "loop_detect", target[0]))
	}
	return L2Finding{Level: lvl, Kind: "loop", DeviceIP: r.IP, Device: r.SysName, Port: target[0],
		Title: title, Detail: detail, Evidence: ev, Steps: steps, rank: float64(n + 100*len(infra))}
}

// crossDevice 处理跨设备的接入口漂移, 按"涉及的口"分组(而不只是设备对), 便于直接定位到口。
// 返回结论和涉及的设备。
func (la *loopAnalysis) crossDevice(macs []globalMACEntry) ([]L2Finding, map[string]bool) {
	type place struct{ dev, name, port string }
	byMAC := map[string][]place{}
	for _, e := range macs {
		if e.CrossDeviceFlap {
			byMAC[e.MAC] = append(byMAC[e.MAC], place{e.DeviceIP, e.DeviceName, e.Port})
		}
	}
	type group struct {
		places []place
		macs   []string
	}
	groups := map[string]*group{}
	for mac, ps := range byMAC {
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].name != ps[j].name {
				return ps[i].name < ps[j].name
			}
			return naturalLess(ps[i].port, ps[j].port)
		})
		keys := make([]string, 0, len(ps))
		for _, p := range ps {
			keys = append(keys, p.dev+" "+p.port)
		}
		k := strings.Join(keys, "\x00")
		g := groups[k]
		if g == nil {
			g = &group{places: ps}
			groups[k] = g
		}
		g.macs = append(g.macs, mac)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []L2Finding
	involved := map[string]bool{}
	for _, k := range keys {
		g := groups[k]
		sort.Strings(g.macs)
		var devs, names, where []string
		for _, p := range g.places {
			where = append(where, p.name+" "+p.port)
			if !contains(devs, p.dev) {
				devs = append(devs, p.dev)
				names = append(names, p.name)
			}
			involved[p.dev] = true
		}
		n := len(g.macs)
		corr := la.corroboration(devs...)
		lvl := levelByCount(n, 5, 20)
		if len(corr) > 0 {
			lvl = raiseLevel(lvl)
		}
		detail := "一个终端同一时刻只会插在一个口上。已排除朝向下游交换机的级联口 / 聚合口, 剩下的是几台设备的接入口同时学到: 这几个口下面在现场是连通的(私拉的线 / 同一台傻瓜交换机 / 无线网桥)"
		if n < 5 && len(corr) == 0 {
			detail += "。MAC 不多且没有风暴 / 生成树变化佐证: 终端换口、无线终端在不同交换机下的 AP 之间漫游(旧表项还没老化)也会这样, 连续几轮都出现再处理"
		}
		steps := make([]string, 0, len(g.places)+2)
		for _, p := range g.places {
			la.markSuspect(p.dev, p.port)
			steps = append(steps, "确认 "+g.macs[0]+" 现在学在哪: "+l2Cmd(la.nc, p.dev, "mac_find", g.macs[0]))
		}
		steps = append(steps,
			"断开其中一个口验证(漂移停止即确认): 先断下面 MAC 少的那个口",
			"确认后在这些接入口开环路检测和边缘口保护: "+l2Cmd(la.nc, g.places[0].dev, "loop_detect", g.places[0].port))
		ev := append([]string{"涉及的口: " + strings.Join(where, " / "),
			fmt.Sprintf("同时出现的 MAC(%d 个): %s", n, strings.Join(firstN(g.macs, 12), ", "))}, corr...)
		out = append(out, L2Finding{Level: lvl, Kind: "loop", DeviceIP: g.places[0].dev, Device: g.places[0].name, Port: g.places[0].port,
			Title:  fmt.Sprintf("MAC 漂移: %s 的接入口同时学到 %d 个相同 MAC", strings.Join(names, " ↔ "), n),
			Detail: detail, Evidence: ev, Steps: steps, rank: float64(n)})
	}
	return out, involved
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
