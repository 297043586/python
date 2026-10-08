package main

// 拓扑链路的其他来源(LLDP 之外):
//
//   - CDP: 采集时和 LLDP 一起取(engine.go collectCDP), 端口上的 NeighborSrc = "CDP";
//   - MAC 推断: 两台被监控设备互相在对方的 MAC 表里学到了对方的管理 MAC, 且中间没有
//     第三台被监控设备同时出现在这两个口上 —— 就认为它们直连(可能隔着傻瓜交换机)。
//     适合没开 LLDP 的老设备, 页面上画成虚线、标「推断」, 可以一键转成手工链路或隐藏;
//   - 手工: 页面上自己连线, 还可以添加不支持 SNMP 的节点(防火墙、路由器、运营商、
//     服务器、云...)。存在 data/topology.json。
//
// 另外可以隐藏某条链路(推断错了/不想看), 同样存在 topology.json。

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type ManualNode struct {
	ID   string `json:"id"` // m1 / m2 ...; 链路里用 "m:<id>" 引用
	Name string `json:"name"`
	Kind string `json:"kind"` // firewall / router / server / cloud / isp / ap / wireless / camera / other
	IP   string `json:"ip,omitempty"`
	Note string `json:"note,omitempty"`
}

type ManualLink struct {
	ID    string `json:"id"`
	A     string `json:"a"` // 设备 IP 或 "m:<手工节点 ID>"
	APort string `json:"a_port,omitempty"`
	B     string `json:"b"`
	BPort string `json:"b_port,omitempty"`
	Speed int64  `json:"speed_mbps,omitempty"`
	Note  string `json:"note,omitempty"`
}

var manualNodeKinds = []string{"firewall", "router", "server", "cloud", "isp", "ap", "wireless", "camera", "switch", "other"}

type topoManualNode struct {
	ManualNode
	X      float64       `json:"x,omitempty"`
	Y      float64       `json:"y,omitempty"`
	HasPos bool          `json:"has_pos"`
	P3     *NodePosition `json:"p3,omitempty"`
}

// ----------------- store -----------------

func (t *TopologyStore) Manual() ([]ManualNode, []ManualLink, []string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]ManualNode{}, t.data.Nodes...), append([]ManualLink{}, t.data.Links...), append([]string{}, t.data.Hidden...)
}

var manualIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,24}$`)

func (t *TopologyStore) SetManual(nodes []ManualNode, links []ManualLink, hidden []string, known func(string) bool) error {
	if len(nodes) > 500 || len(links) > 2000 || len(hidden) > 2000 {
		return errors.New("手工节点/链路数量超过上限")
	}
	ids := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if !manualIDRe.MatchString(n.ID) || ids[n.ID] {
			return errors.New("节点 ID 为空或重复")
		}
		ids[n.ID] = true
		var err error
		if n.Name, err = dcText(n.Name, 40, "节点名"); err != nil {
			return err
		}
		if n.Name == "" {
			return errors.New("节点名不能为空")
		}
		if n.Note, err = dcText(n.Note, 200, "备注"); err != nil {
			return err
		}
		if n.IP, err = dcText(n.IP, 60, "地址"); err != nil {
			return err
		}
		if !inList(n.Kind, manualNodeKinds) {
			n.Kind = "other"
		}
	}
	endpoint := func(s string) bool {
		if strings.HasPrefix(s, "m:") {
			return ids[strings.TrimPrefix(s, "m:")]
		}
		return known(s)
	}
	lids := map[string]bool{}
	for i := range links {
		l := &links[i]
		if !manualIDRe.MatchString(l.ID) || lids[l.ID] {
			return errors.New("链路 ID 为空或重复")
		}
		lids[l.ID] = true
		if l.A == l.B {
			return errors.New("链路两端不能是同一个节点")
		}
		if !endpoint(l.A) || !endpoint(l.B) {
			return fmt.Errorf("链路 %s 的端点不存在(设备被删了?)", l.ID)
		}
		var err error
		if l.APort, err = dcText(l.APort, 60, "端口名"); err != nil {
			return err
		}
		if l.BPort, err = dcText(l.BPort, 60, "端口名"); err != nil {
			return err
		}
		if l.Note, err = dcText(l.Note, 200, "备注"); err != nil {
			return err
		}
		if l.Speed < 0 || l.Speed > 1000000 {
			l.Speed = 0
		}
	}
	hs := []string{}
	for _, h := range hidden {
		if h = strings.TrimSpace(h); h != "" && len(h) < 300 {
			hs = append(hs, h)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Nodes, t.data.Links, t.data.Hidden = nodes, links, hs
	// 删掉的手工节点顺便清掉坐标
	for k := range t.data.Positions {
		if strings.HasPrefix(k, "m:") && !ids[strings.TrimPrefix(k, "m:")] {
			delete(t.data.Positions, k)
		}
	}
	return t.saveLocked()
}

// SetPositions 批量保存坐标(自动编排 / 多选拖动后一次提交); clear 里的 key 删掉坐标(回到自动位置)。
func (t *TopologyStore) SetPositions(pos map[string]NodePosition, clear []string) error {
	if len(pos) > 5000 || len(clear) > 20000 {
		return errors.New("一次提交的坐标太多")
	}
	// v12: 2D 画布可以往外扩(节点多了挤不下时往四周摆), 不再限死在 0-100
	clamp := func(v float64) float64 {
		if v < -400 || v != v {
			return -400
		}
		if v > 500 {
			return 500
		}
		return v
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range clear {
		delete(t.data.Positions, k)
	}
	for k, p := range pos {
		if k == "" || len(k) > 300 {
			continue
		}
		t.data.Positions[k] = NodePosition{X: clamp(p.X), Y: clamp(p.Y)}
	}
	return t.saveLocked()
}

// SetPositions3D 保存 3D 拓扑里拖动后的位置; reset = 全部回到自动布局。
func (t *TopologyStore) SetPositions3D(pos map[string]NodePosition, clear []string, reset bool) error {
	if len(pos) > 5000 || len(clear) > 20000 {
		return errors.New("一次提交的坐标太多")
	}
	lim := func(v float64) float64 { return math.Max(-50000, math.Min(50000, v)) }
	t.mu.Lock()
	defer t.mu.Unlock()
	if reset || t.data.Pos3D == nil {
		t.data.Pos3D = map[string]NodePosition{}
	}
	for _, k := range clear {
		delete(t.data.Pos3D, k)
	}
	for k, p := range pos {
		if k == "" || len(k) > 300 || math.IsNaN(p.X) || math.IsNaN(p.Y) {
			continue
		}
		t.data.Pos3D[k] = NodePosition{X: lim(p.X), Y: lim(p.Y)}
	}
	return t.saveLocked()
}

// ResetPositions 清掉全部坐标(回到自动布局)。
func (t *TopologyStore) ResetPositions() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Positions = map[string]NodePosition{}
	return t.saveLocked()
}

// ----------------- payload -----------------

// linkKey 是一条链路的稳定标识(隐藏链路时用): 来源|两端排序后|两端口。
func linkKey(l topoLink) string {
	a, ap, b, bp := l.A, l.APort, l.B, l.BPort
	if a > b {
		a, ap, b, bp = b, bp, a, ap
	}
	return l.Source + "|" + a + "|" + ap + "|" + b + "|" + bp
}

// hiddenMatcher 判断一条链路是不是被隐藏了。除了 key 完全一样, 还要容忍端口名写法变了
// (比如升级后 LLDP 对端端口从接口描述换成了接口名): 同一来源类别、同一对设备, 只要有一端
// 端口对得上就算; MAC 推断链路每对设备最多一条, 只看设备对。
type hiddenMatcher struct {
	exact map[string]bool
	parts [][5]string
}

func newHiddenMatcher(hidden []string) hiddenMatcher {
	m := hiddenMatcher{exact: map[string]bool{}}
	for _, h := range hidden {
		m.exact[h] = true
		if p := strings.SplitN(h, "|", 5); len(p) == 5 {
			m.parts = append(m.parts, [5]string{p[0], p[1], p[2], p[3], p[4]})
		}
	}
	return m
}

func (m hiddenMatcher) match(l topoLink) bool {
	if m.exact[l.Key] {
		return true
	}
	kind := func(s string) string {
		if s == "lldp" || s == "cdp" {
			return "nbr"
		}
		return s
	}
	a, ap, b, bp := l.A, l.APort, l.B, l.BPort
	if a > b {
		a, ap, b, bp = b, bp, a, ap
	}
	for _, p := range m.parts {
		if kind(p[0]) != kind(l.Source) || p[1] != a || p[3] != b || l.Source == "manual" {
			continue
		}
		if l.Source == "mac" || (ap != "" && p[2] == ap) || (bp != "" && p[4] == bp) {
			return true
		}
	}
	return false
}

// samePort: 手工链路填的端口名跟自动发现的端口名是不是同一个口(写法可能不同)。
func samePort(manual string, auto ...string) bool {
	if manual == "" {
		return true
	}
	mk := normPortKey(manual)
	for _, a := range auto {
		for _, x := range strings.Split(a, ", ") {
			if x == manual || (mk != "" && normPortKey(x) == mk) {
				return true
			}
		}
	}
	return false
}

// addExtraLinks 在 LLDP/CDP 链路之外补上 MAC 推断链路和手工链路, 并去掉隐藏的链路。
//
// 两种会让同一条链路画成两根线的情况在这里处理掉:
//   - 手工链路跟自动发现的链路是同一条(先手工连了线, 后来开了 LLDP; 或者把推断链路"转手工"
//     后推断结果的端口名变了): 手工链路标记 DupOf, 前端不重复画;
//   - 手工节点就是某台设备的 LLDP 邻居(名字一样, 比如手工加的出口防火墙): 不再额外画一个
//     "未监控邻居"小圆点, 直接把这个邻居接到手工节点上。
func (s *server) addExtraLinks(out *topoPayload, reports []DeviceReport) {
	nodes, manual, hidden := s.topology.Manual()
	positions, _ := s.topology.Snapshot()
	pos3d := s.topology.Snapshot3D()
	manualByName := map[string]string{}
	for _, n := range nodes {
		mn := topoManualNode{ManualNode: n}
		if p, ok := positions["m:"+n.ID]; ok {
			mn.X, mn.Y, mn.HasPos = p.X, p.Y, true
		}
		if p, ok := pos3d["m:"+n.ID]; ok {
			p := p
			mn.P3 = &p
		}
		out.ManualNodes = append(out.ManualNodes, mn)
		if k := normalizeDevName(n.Name); k != "" {
			manualByName[k] = "m:" + n.ID
		}
	}
	repByIP := map[string]*DeviceReport{}
	for i := range reports {
		repByIP[reports[i].IP] = &reports[i]
	}

	// 未监控的 LLDP 邻居正好是某个手工节点 → 变成设备到手工节点的链路
	keptExt := out.Externals[:0]
	for _, e := range out.Externals {
		mid, ok := manualByName[normalizeDevName(e.Name)]
		if !ok {
			keptExt = append(keptExt, e)
			continue
		}
		l := topoLink{A: e.Device, APort: e.Port, B: mid, Source: "lldp"}
		if rep := repByIP[e.Device]; rep != nil {
			if p, ok := findPort(rep.Ports, e.Port); ok {
				l.BPort = p.LLDPRemotePort
				l.ASpeedMbps, l.AInMbps, l.AOutMbps = p.SpeedMbps, p.InSpeedMbps, p.OutSpeedMbps
				l.BSpeedMbps, l.BInMbps, l.BOutMbps = p.SpeedMbps, p.OutSpeedMbps, p.InSpeedMbps
				if p.NeighborSrc == "CDP" {
					l.Source = "cdp"
				}
			}
		}
		out.Links = append(out.Links, l)
	}
	out.Externals = keptExt

	autoByPair := map[string][]int{} // 设备对 -> out.Links 下标(自动发现的)
	for i, l := range out.Links {
		k := pairKey(l.A, l.B)
		autoByPair[k] = append(autoByPair[k], i)
	}
	linked := map[string]bool{}
	for k := range autoByPair {
		linked[k] = true
	}
	// 手工连过线的设备对不再做 MAC 推断, 免得"转手工"之后推断链路又冒出来一条
	for _, m := range manual {
		linked[pairKey(m.A, m.B)] = true
	}
	for _, l := range inferMACLinks(reports, linked) {
		out.Links = append(out.Links, l)
		linked[pairKey(l.A, l.B)] = true
	}
	for i := range out.Links {
		out.Links[i].Key = linkKey(out.Links[i])
	}

	for _, m := range manual {
		l := topoLink{A: m.A, APort: m.APort, B: m.B, BPort: m.BPort, Source: "manual", ID: m.ID, Note: m.Note, ASpeedMbps: m.Speed, BSpeedMbps: m.Speed}
		// 手工链路两端是被监控设备的端口时, 带上实时流量
		fill := func(ip, port string, speed *int64, in, outM *float64) {
			if rep := repByIP[ip]; rep != nil && port != "" {
				if p, ok := findPort(rep.Ports, port); ok {
					if p.SpeedMbps > 0 {
						*speed = p.SpeedMbps
					}
					*in, *outM = p.InSpeedMbps, p.OutSpeedMbps
				}
			}
		}
		fill(l.A, l.APort, &l.ASpeedMbps, &l.AInMbps, &l.AOutMbps)
		fill(l.B, l.BPort, &l.BSpeedMbps, &l.BInMbps, &l.BOutMbps)
		// 跟自动发现的某条链路是同一条: 端口没填, 或者两端端口(任一方向)对得上
		for _, i := range autoByPair[pairKey(l.A, l.B)] {
			al := out.Links[i]
			ap, bp := al.APort, al.BPort
			am, bm := strings.Join(al.AMembers, ", "), strings.Join(al.BMembers, ", ")
			if al.A != l.A {
				ap, bp, am, bm = bp, ap, bm, am
			}
			if samePort(l.APort, ap, am) && samePort(l.BPort, bp, bm) {
				l.DupOf = al.Key
				break
			}
		}
		l.Key = linkKey(l)
		out.Links = append(out.Links, l)
	}

	hm := newHiddenMatcher(hidden)
	gone := map[string]bool{}
	kept := out.Links[:0]
	for _, l := range out.Links {
		if hm.match(l) {
			out.HiddenLinks++
			gone[l.Key] = true
			continue
		}
		kept = append(kept, l)
	}
	// 自动链路被隐藏了, 跟它重复的手工链路就要自己画出来
	for i := range kept {
		if kept[i].DupOf != "" && gone[kept[i].DupOf] {
			kept[i].DupOf = ""
		}
	}
	out.Links = kept
	out.Hidden = append([]string{}, hidden...)
	if out.ManualNodes == nil {
		out.ManualNodes = []topoManualNode{}
	}
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// inferMACLinks: A 在 pA 口学到 B 的管理 MAC、B 在 pB 口学到 A 的管理 MAC, 两个口都没有
// LLDP/CDP 邻居, 并且没有第三台被监控设备 C 同时出现在 A.pA 和 B.pB 上(C 夹在中间的情况)
// —— 认为 A.pA 和 B.pB 直连。管理 MAC 从全网 ARP(HostMappings)里按设备 IP 找。
func inferMACLinks(reports []DeviceReport, linked map[string]bool) []topoLink {
	ipMAC := map[string]string{}
	for _, rep := range reports {
		for _, h := range rep.HostMappings {
			if h.IP != "" && h.MAC != "" {
				if _, ok := ipMAC[h.IP]; !ok {
					ipMAC[h.IP] = strings.ToLower(h.MAC)
				}
			}
		}
	}
	type devInfo struct {
		rep     *DeviceReport
		mac     string
		macPort map[string]string // mac -> 端口
		hasNbr  map[string]bool   // 端口有 LLDP/CDP 邻居
	}
	var devs []*devInfo
	for i := range reports {
		r := &reports[i]
		mac := ipMAC[r.IP]
		if mac == "" || !r.IsAlive {
			continue
		}
		d := &devInfo{rep: r, mac: mac, macPort: map[string]string{}, hasNbr: map[string]bool{}}
		for _, e := range r.MACTable {
			if e.MAC != "" && e.Port != "" {
				d.macPort[strings.ToLower(e.MAC)] = e.Port
			}
		}
		for _, p := range r.Ports {
			if p.LLDPRemoteDev != "" && p.LLDPRemoteDev != "-" {
				d.hasNbr[p.Name] = true
			}
		}
		devs = append(devs, d)
	}
	var out []topoLink
	for i := 0; i < len(devs); i++ {
		for j := i + 1; j < len(devs); j++ {
			a, b := devs[i], devs[j]
			if linked[pairKey(a.rep.IP, b.rep.IP)] {
				continue
			}
			pa, pb := a.macPort[b.mac], b.macPort[a.mac]
			if pa == "" || pb == "" || a.hasNbr[pa] || b.hasNbr[pb] {
				continue
			}
			between := false
			for _, c := range devs {
				if c == a || c == b {
					continue
				}
				if a.macPort[c.mac] == pa && b.macPort[c.mac] == pb {
					between = true
					break
				}
			}
			if between {
				continue
			}
			l := topoLink{A: a.rep.IP, APort: pa, B: b.rep.IP, BPort: pb, Source: "mac", Note: "按 MAC 表推断(两端都没有 LLDP/CDP 邻居), 中间可能还有不可网管的交换机"}
			if p, ok := findPort(a.rep.Ports, pa); ok {
				l.APort, l.ASpeedMbps, l.AInMbps, l.AOutMbps = p.Name, p.SpeedMbps, p.InSpeedMbps, p.OutSpeedMbps
			}
			if p, ok := findPort(b.rep.Ports, pb); ok {
				l.BPort, l.BSpeedMbps, l.BInMbps, l.BOutMbps = p.Name, p.SpeedMbps, p.InSpeedMbps, p.OutSpeedMbps
			}
			if l.A > l.B {
				l.A, l.B = l.B, l.A
				l.APort, l.BPort = l.BPort, l.APort
				l.ASpeedMbps, l.BSpeedMbps = l.BSpeedMbps, l.ASpeedMbps
				l.AInMbps, l.BInMbps = l.BInMbps, l.AInMbps
				l.AOutMbps, l.BOutMbps = l.BOutMbps, l.AOutMbps
			}
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return pairKey(out[i].A, out[i].B) < pairKey(out[j].A, out[j].B) })
	return out
}

// ----------------- handlers -----------------

func (s *server) handleTopoManual(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Nodes  []ManualNode `json:"nodes"`
		Links  []ManualLink `json:"links"`
		Hidden []string     `json:"hidden"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	known := map[string]bool{}
	for _, d := range s.devices.List() {
		known[d.IP] = true
	}
	if req.Nodes == nil {
		req.Nodes = []ManualNode{}
	}
	if req.Links == nil {
		req.Links = []ManualLink{}
	}
	if err := s.topology.SetManual(req.Nodes, req.Links, req.Hidden, func(ip string) bool { return known[ip] }); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) handleTopoPositions(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Positions map[string]NodePosition `json:"positions"`
		Clear     []string                `json:"clear"`
		Reset     bool                    `json:"reset"`
		Mode      string                  `json:"mode"` // "3d" = 3D 拓扑里的位置
	}
	if !s.decode(w, r, &req) {
		return
	}
	var err error
	if req.Mode == "3d" {
		err = s.topology.SetPositions3D(req.Positions, req.Clear, req.Reset)
	} else if req.Reset {
		err = s.topology.ResetPositions()
	} else {
		err = s.topology.SetPositions(req.Positions, req.Clear)
	}
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}
