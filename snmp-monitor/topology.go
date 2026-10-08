package main

// 网络拓扑: 设备节点 + LLDP/CDP 链路(两端配对去重、聚合合并) + 外部邻居; MAC 推断 / 手工链路见 topo_links.go。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ----------------- 拓扑 (基于 LLDP 邻居关系推导) -----------------

type topoDevice struct {
	IP             string        `json:"ip"`
	Name           string        `json:"name"`
	Brand          string        `json:"brand"`
	Alive          bool          `json:"alive"`
	CPUUsage       int           `json:"cpu_usage"`
	MemUsage       int           `json:"mem_usage"`
	OpenAlerts     int           `json:"open_alerts"`
	PortUsageRatio string        `json:"port_usage_ratio"`
	UpTimeStr      string        `json:"uptime_str"`
	X              float64       `json:"x,omitempty"` // 0-100, 有保存过位置才非零
	Y              float64       `json:"y,omitempty"`
	HasPos         bool          `json:"has_pos"`
	P3             *NodePosition `json:"p3,omitempty"`   // 3D 拓扑里手工拖过的位置
	Note           string        `json:"note,omitempty"` // 设备管理里填的备注
	Group          string        `json:"group,omitempty"`

	// 3D 拓扑按形态画设备: 几台堆叠 / 框式多高, 见 form.go
	Form  DeviceForm `json:"form"`
	Model string     `json:"model,omitempty"`
}

// topoLink 带上两侧端口的实时速率, 这样悬停链路的时候能看到带宽占用和
// 收发方向, 而不是只有一个端口名字的 tooltip。
//
// 两台设备之间如果是通过聚合口(Eth-Trunk/Port-Channel/AggregatePort)互联,
// LLDP 是逐个物理成员口独立上报邻居的(协议本身没有"聚合"的概念), 一个
// 8 口的聚合组会产生 8 条物理层面的邻居记录。两侧端口能定位到聚合组时
// 合并成一条逻辑链路: IsAgg=true, APort/BPort 是聚合口显示名,
// AMembers/BMembers 是参与合并的物理成员口(一一对应), 速率是全部成员求和。
type topoLink struct {
	A          string   `json:"a"` // 设备 IP
	APort      string   `json:"a_port"`
	ASpeedMbps int64    `json:"a_speed_mbps"`
	AInMbps    float64  `json:"a_in_mbps"`
	AOutMbps   float64  `json:"a_out_mbps"`
	B          string   `json:"b"` // 设备 IP
	BPort      string   `json:"b_port"`
	BSpeedMbps int64    `json:"b_speed_mbps"`
	BInMbps    float64  `json:"b_in_mbps"`
	BOutMbps   float64  `json:"b_out_mbps"`
	IsAgg      bool     `json:"is_agg"`              // true = APort/BPort 是聚合口, 下面两个 Members 有多条
	AMembers   []string `json:"a_members,omitempty"` // 物理成员口名, 与 BMembers 按下标一一对应
	BMembers   []string `json:"b_members,omitempty"`
	Source     string   `json:"source"`         // lldp / cdp / mac(按 MAC 表推断) / manual(手工)
	ID         string   `json:"id,omitempty"`   // 手工链路的 ID
	Note       string   `json:"note,omitempty"` // 手工链路备注 / 推断说明
	Key        string   `json:"key"`            // 稳定标识, 隐藏链路时用
	// OneSided=true: 只有一端上报了这条链路(另一端没开 LLDP / 这个口没发 LLDP), 对端端口是对方上报的原文
	OneSided bool `json:"one_sided,omitempty"`
	// DupOf 非空: 这条手工链路跟自动发现的某条链路是同一条(值为那条的 key), 前端不重复画,
	// 只在那条链路的信息里提示; 仍然放在返回里, 是为了编辑手工链路时能原样带回去。
	DupOf string `json:"dup_of,omitempty"`
}

type topoExternal struct {
	ID     string  `json:"id"`
	Key    string  `json:"key"` // 稳定 key(设备IP|邻居名), 拖动定位时用, 跨轮询不变
	Name   string  `json:"name"`
	Device string  `json:"device"` // 挂在哪台监控设备上
	Port   string  `json:"port"`
	IP     string  `json:"ip,omitempty"` // 对端上报的管理 IP(或按机框 MAC 从 ARP 查到的), 页面上可一键添加
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	HasPos bool    `json:"has_pos"`
}

type topoPayload struct {
	Devices       []topoDevice   `json:"devices"`
	Links         []topoLink     `json:"links"`
	Externals     []topoExternal `json:"externals"`
	HasBackground bool           `json:"has_background"`
	// 手工添加的节点(防火墙/路由器/运营商...), 链路里用 "m:<id>" 引用
	ManualNodes []topoManualNode `json:"manual_nodes"`
	HiddenLinks int              `json:"hidden_links"`
	Hidden      []string         `json:"hidden"` // 隐藏的链路 key(编辑时要原样带回去)
}

func normalizeDevName(s string) string {
	return strings.ToLower(strings.TrimSpace(shortSysName(s)))
}

// findPort 先按端口名精确匹配, 匹配不上再按归一化名(normPortKey)匹配——
// LLDP 对端上报的端口名常常是缩写("Gi0/1"、"Ag1"), 跟本设备 ifDescr 的
// 全称("GigabitEthernet 0/1")对不上, 只做精确匹配的话对端速率和聚合
// 归属都查不到, 拓扑上的聚合链路也就合并不起来。
func findPort(ports []PortDetail, name string) (PortDetail, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	key := normPortKey(name)
	if key == "" {
		return PortDetail{}, false
	}
	var hit PortDetail
	n := 0
	for _, p := range ports {
		if normPortKey(p.Name) == key {
			hit = p
			n++
		}
	}
	if n == 1 {
		return hit, true
	}
	return PortDetail{}, false
}

// findPeerPort 用对端上报的端口名的几种写法(LLDP PortId / PortDesc)去对端设备的端口表里找:
// 先按接口名(精确 / 归一化), 再按接口描述(ifAlias, 必须唯一), 最后按槽位号(必须唯一)。
func findPeerPort(ports []PortDetail, names ...string) (PortDetail, bool) {
	for _, n := range names {
		if n == "" || n == "-" {
			continue
		}
		if p, ok := findPort(ports, n); ok {
			return p, true
		}
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || n == "-" {
			continue
		}
		var hit PortDetail
		cnt := 0
		for _, p := range ports {
			if p.Alias != "" && strings.EqualFold(strings.TrimSpace(p.Alias), n) {
				hit = p
				cnt++
			}
		}
		if cnt == 1 {
			return hit, true
		}
	}
	for _, n := range names {
		sk := slotKey(n)
		if sk == "" || !strings.Contains(sk, "/") {
			continue
		}
		var hit PortDetail
		cnt := 0
		for _, p := range ports {
			if slotKey(p.Name) == sk && !logicalPortRe.MatchString(p.Name) {
				hit = p
				cnt++
			}
		}
		if cnt == 1 {
			return hit, true
		}
	}
	return PortDetail{}, false
}

// handleTopology 用每台设备自己上报的 LLDP 邻居信息拼出一张简单拓扑图:
// 邻居名能匹配上另一台被监控设备的 sysName, 就画成设备间连线; 匹配不上
// (对端没装 SNMP、或没被加进来监控)就画成一个外部叶子节点, 至少能看出
// "这个口接了个啥"。链路和设备节点都带上关键指标(带宽/方向/CPU/告警
// 数量), 前端悬停即可看到, 不用再跳去详情页找。
func (s *server) handleTopology(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, 200, s.topologyPayload())
}

// topologyPayload 组装拓扑数据, 拓扑页和大屏总览共用。
func (s *server) topologyPayload() topoPayload {
	s.hub.mu.RLock()
	reports := s.hub.reports
	s.hub.mu.RUnlock()

	positions, hasBg := s.topology.Snapshot()
	pos3d := s.topology.Snapshot3D()
	groups := s.devices.groupsByIP()

	out := topoPayload{Devices: []topoDevice{}, Links: []topoLink{}, Externals: []topoExternal{}, HasBackground: hasBg}
	for _, rep := range reports {
		td := topoDevice{
			IP: rep.IP, Name: rep.SysName, Brand: rep.Brand, Alive: rep.IsAlive,
			CPUUsage: rep.CPUUsage, MemUsage: rep.MemUsage, OpenAlerts: rep.OpenAlerts,
			PortUsageRatio: rep.PortUsageRatio, UpTimeStr: rep.UpTimeStr, Note: rep.Note, Group: groups[rep.IP],
			Form: detectDeviceForm(rep, s.formOverride(rep.IP)),
		}
		if rep.Inventory != nil {
			td.Model = rep.Inventory.Model
		}
		if pos, ok := positions[rep.IP]; ok {
			td.X, td.Y, td.HasPos = pos.X, pos.Y, true
		}
		if p, ok := pos3d[rep.IP]; ok {
			p := p
			td.P3 = &p
		}
		out.Devices = append(out.Devices, td)
	}

	links, externals := buildNeighborLinks(reports)
	out.Links = links
	for _, ext := range externals {
		if pos, ok := positions[ext.Key]; ok {
			ext.X, ext.Y, ext.HasPos = pos.X, pos.Y, true
		}
		out.Externals = append(out.Externals, ext)
	}
	s.addExtraLinks(&out, reports)
	return out
}

// lldpObs 是一台设备在一个口上看到的一个被监控的邻居(LLDP/CDP 一条记录)。
type lldpObs struct {
	local  *DeviceReport
	port   PortDetail // 本端端口
	peer   *DeviceReport
	remote []string // 对端上报的端口名(几种写法)
	cdp    bool
	resolv string // 在对端端口表里找到的端口全称, 找不到为空
}

// physLink 是一条物理链路(两端配好对之后)。
type physLink struct {
	a, b         *DeviceReport
	aPort, bPort string      // 端口名; 找不到对端端口时是对方上报的原文
	aDet, bDet   *PortDetail // 端口详情, 找不到为 nil
	cdp          bool
	oneSided     bool
}

// buildNeighborLinks 把各台设备各自上报的 LLDP/CDP 邻居配成对, 得到设备间链路和未监控的外部邻居。
//
// 同一条物理链路 A、B 两端通常各报一次。旧版按"两端端口名拼成的 key"去重, 但 A 报的
// 对端端口名(PortDesc 可能是接口描述、PortId 可能是缩写)经常跟 B 端口表里的名字对不上,
// key 不一样就没去掉, 于是一条线画成两条。现在按设备对分组, 两边的记录逐条配对:
//  1. A 在 pA 口看到 B, 且(A 报的对端口能在 B 上找到 = pB, 或者 B 报的对端口能在 A 上找到 = pA)→ 同一条;
//  2. 剩下两边都"找不到对方端口"的记录, 按端口名顺序一一配对(两台设备之间 1 条链路时必然配上);
//  3. 还剩下的就是只有一端报了的链路(对端没开 LLDP), 单独成一条。
//
// 配好对的物理链路再按聚合组合并: 同一对设备之间, 任一侧落在同一个聚合组里的成员口并成一条逻辑链路。
func buildNeighborLinks(reports []DeviceReport) ([]topoLink, []topoExternal) {
	nameToIdx := make(map[string]int, len(reports))
	for i := range reports {
		if n := normalizeDevName(reports[i].SysName); n != "" {
			nameToIdx[n] = i
		}
	}
	var externals []topoExternal
	seenExternal := map[string]bool{}
	byPair := map[string][]*lldpObs{}
	var pairOrder []string
	for i := range reports {
		rep := &reports[i]
		for _, p := range rep.Ports {
			if p.LLDPRemoteDev == "" || p.LLDPRemoteDev == "-" {
				continue
			}
			j, ok := nameToIdx[normalizeDevName(p.LLDPRemoteDev)]
			if !ok || j == i {
				// 对端不是任何一台被监控的设备: 画成外部叶子节点(同一台设备上同名邻居只画一个)
				extKey := rep.IP + "|" + normalizeDevName(p.LLDPRemoteDev)
				if seenExternal[extKey] {
					continue
				}
				seenExternal[extKey] = true
				externals = append(externals, topoExternal{
					ID: fmt.Sprintf("ext_%d", len(externals)+1), Key: extKey, Name: p.LLDPRemoteDev, Device: rep.IP, Port: p.Name, IP: p.LLDPRemoteIP,
				})
				continue
			}
			peer := &reports[j]
			o := &lldpObs{local: rep, port: p, peer: peer, remote: []string{p.LLDPRemotePort, p.LLDPRemotePortAlt}, cdp: p.NeighborSrc == "CDP"}
			if bp, ok := findPeerPort(peer.Ports, o.remote...); ok {
				o.resolv = bp.Name
			}
			key := pairKey(rep.IP, peer.IP)
			if _, ok := byPair[key]; !ok {
				pairOrder = append(pairOrder, key)
			}
			byPair[key] = append(byPair[key], o)
		}
	}

	portToAgg := make(map[string]map[string]string, len(reports))
	for _, rep := range reports {
		m := make(map[string]string, 8)
		for _, g := range rep.AggGroups {
			for _, member := range g.Members {
				m[member] = g.Name
			}
		}
		portToAgg[rep.IP] = m
	}

	links := make([]topoLink, 0, len(pairOrder))
	for _, key := range pairOrder {
		phys := pairObservations(byPair[key])
		links = append(links, mergeAggLinks(phys, portToAgg)...)
	}
	return links, externals
}

// pairObservations 把同一对设备之间两个方向的观测记录配成物理链路, 规则见 buildNeighborLinks。
func pairObservations(obs []*lldpObs) []physLink {
	if len(obs) == 0 {
		return nil
	}
	// 统一方向: a = IP 小的那台
	a, b := obs[0].local, obs[0].peer
	if a.IP > b.IP {
		a, b = b, a
	}
	var fromA, fromB []*lldpObs
	for _, o := range obs {
		if o.local == a {
			fromA = append(fromA, o)
		} else {
			fromB = append(fromB, o)
		}
	}
	byName := func(list []*lldpObs) {
		sort.SliceStable(list, func(i, j int) bool { return naturalLess(list[i].port.Name, list[j].port.Name) })
	}
	byName(fromA)
	byName(fromB)
	// 同一个口重复上报(LLDP + CDP 都有)只留一条
	dedupe := func(list []*lldpObs) []*lldpObs {
		seen := map[string]bool{}
		out := list[:0]
		for _, o := range list {
			if !seen[o.port.Name] {
				seen[o.port.Name] = true
				out = append(out, o)
			}
		}
		return out
	}
	fromA, fromB = dedupe(fromA), dedupe(fromB)

	det := func(rep *DeviceReport, name string) *PortDetail {
		if p, ok := findPort(rep.Ports, name); ok {
			return &p
		}
		return nil
	}
	both := func(oa, ob *lldpObs) physLink {
		pa, pb := oa.port, ob.port
		return physLink{a: a, b: b, aPort: pa.Name, bPort: pb.Name, aDet: &pa, bDet: &pb, cdp: oa.cdp || ob.cdp}
	}
	var out []physLink
	usedB := make([]bool, len(fromB))
	var leftA []*lldpObs
	for _, oa := range fromA {
		hit := -1
		for j, ob := range fromB {
			if usedB[j] {
				continue
			}
			if (oa.resolv != "" && oa.resolv == ob.port.Name) || (ob.resolv != "" && ob.resolv == oa.port.Name) {
				hit = j
				break
			}
		}
		if hit >= 0 {
			usedB[hit] = true
			out = append(out, both(oa, fromB[hit]))
			continue
		}
		leftA = append(leftA, oa)
	}
	var leftB []*lldpObs
	for j, ob := range fromB {
		if !usedB[j] {
			leftB = append(leftB, ob)
		}
	}
	// 两边都没能定位到对方端口的记录: 按顺序一一配对
	var restA, restB []*lldpObs
	ib := 0
	unresolvedB := func() []*lldpObs {
		var l []*lldpObs
		for _, ob := range leftB {
			if ob.resolv == "" {
				l = append(l, ob)
			} else {
				restB = append(restB, ob)
			}
		}
		return l
	}()
	for _, oa := range leftA {
		if oa.resolv == "" && ib < len(unresolvedB) {
			out = append(out, both(oa, unresolvedB[ib]))
			ib++
			continue
		}
		restA = append(restA, oa)
	}
	restB = append(restB, unresolvedB[ib:]...)
	// 只有一端报了的链路
	for _, oa := range restA {
		pa := oa.port
		l := physLink{a: a, b: b, aPort: pa.Name, aDet: &pa, cdp: oa.cdp, oneSided: true}
		if oa.resolv != "" {
			l.bPort, l.bDet = oa.resolv, det(b, oa.resolv)
		} else {
			l.bPort = firstNonEmpty(oa.remote...)
		}
		out = append(out, l)
	}
	for _, ob := range restB {
		pb := ob.port
		l := physLink{a: a, b: b, bPort: pb.Name, bDet: &pb, cdp: ob.cdp, oneSided: true}
		if ob.resolv != "" {
			l.aPort, l.aDet = ob.resolv, det(a, ob.resolv)
		} else {
			l.aPort = firstNonEmpty(ob.remote...)
		}
		out = append(out, l)
	}
	return out
}

// mergeAggLinks: 同一对设备之间, 任一侧落在同一个聚合组里的物理链路合并成一条逻辑链路(并查集)。
func mergeAggLinks(phys []physLink, portToAgg map[string]map[string]string) []topoLink {
	if len(phys) == 0 {
		return nil
	}
	parent := make([]int, len(phys))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	aAgg := make([]string, len(phys))
	bAgg := make([]string, len(phys))
	firstA, firstB := map[string]int{}, map[string]int{}
	for i, p := range phys {
		aAgg[i], bAgg[i] = portToAgg[p.a.IP][p.aPort], portToAgg[p.b.IP][p.bPort]
		if g := aAgg[i]; g != "" {
			if j, ok := firstA[g]; ok {
				parent[find(i)] = find(j)
			} else {
				firstA[g] = i
			}
		}
		if g := bAgg[i]; g != "" {
			if j, ok := firstB[g]; ok {
				parent[find(i)] = find(j)
			} else {
				firstB[g] = i
			}
		}
	}
	groups := map[int][]int{}
	var order []int
	for i := range phys {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], i)
	}
	out := make([]topoLink, 0, len(order))
	for _, r := range order {
		idx := groups[r]
		first := phys[idx[0]]
		link := topoLink{A: first.a.IP, B: first.b.IP, Source: "lldp"}
		ga, gb := "", ""
		for _, i := range idx {
			if ga == "" {
				ga = aAgg[i]
			}
			if gb == "" {
				gb = bAgg[i]
			}
		}
		link.IsAgg = ga != "" || gb != ""
		oneSided := true
		for _, i := range idx {
			p := phys[i]
			if p.cdp {
				link.Source = "cdp"
			}
			if !p.oneSided {
				oneSided = false
			}
			if p.aDet != nil {
				link.ASpeedMbps += p.aDet.SpeedMbps
				link.AInMbps += p.aDet.InSpeedMbps
				link.AOutMbps += p.aDet.OutSpeedMbps
			}
			if p.bDet != nil {
				link.BSpeedMbps += p.bDet.SpeedMbps
				link.BInMbps += p.bDet.InSpeedMbps
				link.BOutMbps += p.bDet.OutSpeedMbps
			}
			if link.IsAgg {
				link.AMembers = append(link.AMembers, p.aPort)
				link.BMembers = append(link.BMembers, p.bPort)
			}
		}
		link.OneSided = oneSided
		if link.IsAgg {
			link.APort, link.BPort = ga, gb
			// 没识别到聚合口的一侧, 用成员口列表代替聚合口名
			if link.APort == "" {
				link.APort = strings.Join(link.AMembers, ", ")
			}
			if link.BPort == "" {
				link.BPort = strings.Join(link.BMembers, ", ")
			}
		} else {
			link.APort, link.BPort = first.aPort, first.bPort
		}
		// 只有一端有数据时, 两端互相补齐(同一条线两头速率一样, 收发方向相反);
		// 对端关了速率采集 / 端口没找到时, 流量也用这一端的
		if link.ASpeedMbps == 0 {
			link.ASpeedMbps = link.BSpeedMbps
		}
		if link.BSpeedMbps == 0 {
			link.BSpeedMbps = link.ASpeedMbps
		}
		if link.AInMbps == 0 && link.AOutMbps == 0 {
			link.AInMbps, link.AOutMbps = link.BOutMbps, link.BInMbps
		} else if link.BInMbps == 0 && link.BOutMbps == 0 {
			link.BInMbps, link.BOutMbps = link.AOutMbps, link.AInMbps
		}
		out = append(out, link)
	}
	return out
}

func firstNonEmpty(list ...string) string {
	for _, s := range list {
		if s != "" && s != "-" {
			return s
		}
	}
	return ""
}

// ----------------- 拓扑背景图 + 节点位置 -----------------

func (s *server) handleTopoBackgroundGet(w http.ResponseWriter, r *http.Request) {
	path, ok := s.topology.imagePath()
	if !ok {
		s.fail(w, http.StatusNotFound, "还没有上传背景图")
		return
	}
	secureHeaders(w)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, path)
}

func (s *server) handleTopoBackgroundSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var body struct {
		Data string `json:"data"` // data:image/png;base64,xxxx
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 11*1024*1024)) // base64 比原始数据大约膨胀 1.37 倍, 留点余量
	if err := dec.Decode(&body); err != nil {
		s.fail(w, http.StatusBadRequest, "请求格式有误")
		return
	}
	payload := body.Data
	declaredType := ""
	if i := strings.Index(payload, ","); i > 0 && strings.HasPrefix(payload, "data:") {
		declaredType = payload[5:i]
		payload = payload[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "图片数据解码失败")
		return
	}
	if err := s.topology.SetImage(raw, declaredType); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	fmt.Printf("🖼️  [%s] 拓扑背景图已更新 (由 %s 操作)\n", time.Now().Format("15:04:05"), sess.user)
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) handleTopoBackgroundClear(w http.ResponseWriter, r *http.Request, sess *session) {
	if err := s.topology.ClearImage(); err != nil {
		s.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) handleTopoPosition(w http.ResponseWriter, r *http.Request, sess *session) {
	var body struct {
		IP string  `json:"ip"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.IP) == "" {
		s.fail(w, http.StatusBadRequest, "缺少 ip")
		return
	}
	if err := s.topology.SetPosition(body.IP, body.X, body.Y); err != nil {
		s.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

// lldpDiscovered 汇总"LLDP 看得到、但还没加入监控"的邻居, 设备管理里一键添加用。
type lldpDiscovered struct {
	Name  string   `json:"name"`
	IP    string   `json:"ip,omitempty"`
	IPSrc string   `json:"ip_src,omitempty"` // "arp" = 对端没上报管理 IP, 按机框 MAC 从 ARP 查到的
	MAC   string   `json:"mac,omitempty"`
	Seen  []string `json:"seen"` // "设备名 端口"
}

func lldpDiscoveredList(reports []DeviceReport) []lldpDiscovered {
	known := map[string]bool{}
	for _, r := range reports {
		known[r.IP] = true
		for _, ip := range r.OwnIPs {
			known[ip] = true
		}
		if n := normalizeDevName(r.SysName); n != "" {
			known["name:"+n] = true
		}
	}
	byKey := map[string]*lldpDiscovered{}
	var order []string
	for _, r := range reports {
		for _, p := range r.Ports {
			if p.LLDPRemoteDev == "" || p.LLDPRemoteDev == "-" || known["name:"+normalizeDevName(p.LLDPRemoteDev)] || (p.LLDPRemoteIP != "" && known[p.LLDPRemoteIP]) {
				continue
			}
			k := normalizeDevName(p.LLDPRemoteDev)
			d := byKey[k]
			if d == nil {
				d = &lldpDiscovered{Name: p.LLDPRemoteDev}
				byKey[k] = d
				order = append(order, k)
			}
			if d.IP == "" && p.LLDPRemoteIP != "" {
				d.IP, d.IPSrc = p.LLDPRemoteIP, p.LLDPRemoteIPSrc
			}
			if d.MAC == "" {
				d.MAC = p.LLDPRemoteMAC
			}
			d.Seen = append(d.Seen, firstNonEmpty(r.SysName, r.IP)+" "+p.Name)
		}
	}
	out := make([]lldpDiscovered, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].IP == "") != (out[j].IP == "") {
			return out[i].IP != ""
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *server) handleLLDPDiscovered(w http.ResponseWriter, r *http.Request, sess *session) {
	s.hub.mu.RLock()
	list := lldpDiscoveredList(s.hub.reports)
	s.hub.mu.RUnlock()
	s.writeJSON(w, 200, map[string]interface{}{"neighbors": list})
}
