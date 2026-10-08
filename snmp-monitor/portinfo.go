package main

// 端口 VLAN 模式 / 允许 VLAN 列表 / 双工 的采集。
//
// 旧版端口表的 VLAN 只来自 dot1qPvid(端口默认 VLAN), 这张表只能回答
// "未打标签的帧进来算哪个 VLAN", 于是:
//   - access 口能显示对, 但 trunk 口只能看到 native VLAN(通常是 1);
//   - 前端再拿"是不是上联口"去猜 access/trunk, 猜错的情况很多;
//   - 聚合成员口在桥里不是独立的桥端口(桥端口是 AggregatePort 本身),
//     dot1qPvid 里根本没有它们, 成员口的 VLAN 整列空白;
//   - 部分设备 dot1qPvid 只给一部分端口。
//
// 现在改成读 Q-BRIDGE-MIB 的 VLAN 成员表(每个 VLAN 一个端口位图, 分"出口
// 成员"和"不打标签成员"), 反推出每个口:
//   - 只作为不打标签成员出现在 1 个 VLAN → access
//   - 在若干 VLAN 里是打标签成员 → trunk(不打标签的那个就是 native VLAN)
//   - 在多个 VLAN 里都不打标签 → hybrid
// 优先 dot1qVlanCurrentTable(运行态), 拿不到退回 dot1qVlanStaticTable(配置态);
// 标准表都拿不到时, Cisco 用 CISCO-VTP-MIB / CISCO-VLAN-MEMBERSHIP-MIB。
// 聚合成员口继承所属聚合口的 VLAN 信息。
//
// 双工: EtherLike-MIB 的 dot3StatsDuplexStatus 很多设备只对部分口填(或者
// DOWN 口一律 unknown), 旧版又只在"扩展信息"开关打开时才采, 所以页面上
// 显示不全。现在无论扩展开关与否都采, 并依次补充:
//   1. dot3StatsDuplexStatus
//   2. MAU-MIB ifMauType(值是一个 OID, 类型名里带 HD/FD)
//   3. LLDP-EXT-DOT3 lldpXdot3LocPortOperMauType(开了 LLDP 的设备基本都有)
//   4. H3C hh3cifEthernetDuplex
//   5. 端口 UP 且速率 ≥10G: 标准里 10G 及以上只有全双工, 直接判定(标"推断")

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

const (
	oidDot1qVlanCurrentEgress   = ".1.3.6.1.2.1.17.7.1.4.2.1.4" // 索引 timeMark.vlanId
	oidDot1qVlanCurrentUntagged = ".1.3.6.1.2.1.17.7.1.4.2.1.5"
	oidDot1qVlanStaticName      = ".1.3.6.1.2.1.17.7.1.4.3.1.1" // 索引 vlanId
	oidDot1qVlanStaticEgress    = ".1.3.6.1.2.1.17.7.1.4.3.1.2"
	oidDot1qVlanStaticUntagged  = ".1.3.6.1.2.1.17.7.1.4.3.1.4"

	// Cisco: 标准 Q-BRIDGE 基本不实现, 用私有表
	oidCiscoVlanTrunkPortNative = ".1.3.6.1.4.1.9.9.46.1.6.1.1.5"  // 索引 ifIndex
	oidCiscoVlanTrunkPortStatus = ".1.3.6.1.4.1.9.9.46.1.6.1.1.14" // 1=trunking 2=notTrunking
	oidCiscoVmVlan              = ".1.3.6.1.4.1.9.9.68.1.2.2.1.2"  // access 口 VLAN, 索引 ifIndex

	oidIfMauType                   = ".1.3.6.1.2.1.26.2.1.1.3"            // 索引 ifIndex.mauIndex, 值是 dot3MauType OID
	oidLldpXdot3LocPortOperMauType = ".1.0.8802.1.1.2.1.5.4623.1.2.1.1.4" // 索引 lldpLocPortNum, 值是 dot3MauType 编号
	oidH3cIfEthernetDuplex         = ".1.3.6.1.4.1.25506.8.35.1.1.1.3"    // 1=full 2=half 3=auto
)

// portVlanInfo 是一个口的 VLAN 成员关系。
type portVlanInfo struct {
	mode     string // access / trunk / hybrid, 判断不出来为空
	native   int    // 不打标签的 VLAN(access 口就是它所属的 VLAN)
	tagged   []int  // 打标签放行的 VLAN
	untagged []int
	source   string // 数据来源, 页面悬停提示用
}

// portListBits 把 Q-BRIDGE 的 PortList 位图展开成桥端口号: 第一个字节的最高位是 1 号口。
func portListBits(v interface{}) []int {
	b, ok := v.([]byte)
	if !ok {
		return nil
	}
	var out []int
	for i, octet := range b {
		if octet == 0 {
			continue
		}
		for bit := 0; bit < 8; bit++ {
			if octet&(0x80>>uint(bit)) != 0 {
				out = append(out, i*8+bit+1)
			}
		}
	}
	return out
}

// collectPortVlans 读 VLAN 成员位图, 返回 ifIndex -> VLAN 信息, 以及 vlanId -> VLAN 名。
func collectPortVlans(s *snmpSession, bridgeToIf map[string]string, r *indexResolver) (map[string]*portVlanInfo, map[int]string) {
	egress := map[string]map[int]bool{}   // ifIndex -> vlan 集合
	untagged := map[string]map[int]bool{} // ifIndex -> 不打标签的 vlan 集合
	add := func(dst map[string]map[int]bool, vlan int, v interface{}) {
		for _, bp := range portListBits(v) {
			ifIdx := resolveBridgePortIfIndex(strconv.Itoa(bp), bridgeToIf, r)
			if ifIdx == "" {
				continue
			}
			if dst[ifIdx] == nil {
				dst[ifIdx] = map[int]bool{}
			}
			dst[ifIdx][vlan] = true
		}
	}
	// 索引最后一段是 vlanId(Current 表前面还有一段 timeMark)
	vlanOf := func(idx string) int {
		if i := strings.LastIndexByte(idx, '.'); i >= 0 {
			idx = idx[i+1:]
		}
		n, _ := strconv.Atoi(idx)
		return n
	}
	walkPair := func(eg, ut string) {
		_ = s.walk(eg, func(idx string, pdu gosnmp.SnmpPDU) {
			if v := vlanOf(idx); v > 0 && v < 4095 {
				add(egress, v, pdu.Value)
			}
		})
		_ = s.walk(ut, func(idx string, pdu gosnmp.SnmpPDU) {
			if v := vlanOf(idx); v > 0 && v < 4095 {
				add(untagged, v, pdu.Value)
			}
		})
	}
	source := "Q-BRIDGE 运行态"
	walkPair(oidDot1qVlanCurrentEgress, oidDot1qVlanCurrentUntagged)
	if len(egress) == 0 {
		source = "Q-BRIDGE 配置态"
		walkPair(oidDot1qVlanStaticEgress, oidDot1qVlanStaticUntagged)
	}

	names := map[int]string{}
	for idx, n := range s.walkStrings(oidDot1qVlanStaticName) {
		if v := vlanOf(idx); v > 0 && strings.TrimSpace(n) != "" {
			names[v] = strings.TrimSpace(n)
		}
	}

	out := map[string]*portVlanInfo{}
	for ifIdx, set := range egress {
		info := &portVlanInfo{source: source}
		for v := range set {
			if untagged[ifIdx][v] {
				info.untagged = append(info.untagged, v)
			} else {
				info.tagged = append(info.tagged, v)
			}
		}
		// 个别设备只把口放进 untagged 位图、不放进 egress 位图
		for v := range untagged[ifIdx] {
			if !set[v] {
				info.untagged = append(info.untagged, v)
			}
		}
		sort.Ints(info.tagged)
		sort.Ints(info.untagged)
		switch {
		case len(info.tagged) == 0 && len(info.untagged) == 1:
			info.mode, info.native = "access", info.untagged[0]
		case len(info.tagged) > 0 && len(info.untagged) <= 1:
			info.mode = "trunk"
			if len(info.untagged) == 1 {
				info.native = info.untagged[0]
			}
		case len(info.untagged) > 1:
			info.mode = "hybrid"
		}
		out[ifIdx] = info
	}
	if len(out) > 0 {
		return out, names
	}

	// Cisco 私有表
	status := s.walkInts(oidCiscoVlanTrunkPortStatus)
	if len(status) == 0 {
		for ifIdx, v := range s.walkInts(oidCiscoVmVlan) {
			if v > 0 && r.ifExists[ifIdx] {
				out[ifIdx] = &portVlanInfo{mode: "access", native: int(v), untagged: []int{int(v)}, source: "CISCO-VLAN-MEMBERSHIP"}
			}
		}
		return out, names
	}
	native := s.walkInts(oidCiscoVlanTrunkPortNative)
	access := s.walkInts(oidCiscoVmVlan)
	for ifIdx, st := range status {
		if !r.ifExists[ifIdx] {
			continue
		}
		if st == 1 {
			out[ifIdx] = &portVlanInfo{mode: "trunk", native: int(native[ifIdx]), source: "CISCO-VTP"}
		} else if v := access[ifIdx]; v > 0 {
			out[ifIdx] = &portVlanInfo{mode: "access", native: int(v), untagged: []int{int(v)}, source: "CISCO-VLAN-MEMBERSHIP"}
		}
	}
	return out, names
}

// compactVlanList 把 [1 2 3 5 10 11] 压缩成 "1-3,5,10-11"。
func compactVlanList(vs []int) string {
	if len(vs) == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(vs); {
		j := i
		for j+1 < len(vs) && vs[j+1] == vs[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(vs[i]))
		if j > i {
			b.WriteByte('-')
			b.WriteString(strconv.Itoa(vs[j]))
		}
		i = j + 1
	}
	return b.String()
}

// mauDuplex 把 dot3MauType 编号(MAU-MIB 里 dot3MauType 下的最后一段)换成双工。
// 编号按 RFC 4836: 奇偶交替出现 HD/FD 变体; 10G 及以上(≥31 且不是 1000BaseT HD)全是全双工。
func mauDuplex(n int) string {
	switch n {
	case 10, 15, 17, 21, 23, 25, 27, 29, 31:
		return "半双工"
	case 11, 16, 18, 22, 24, 26, 28, 30, 32:
		return "全双工"
	}
	if n >= 33 && n <= 200 {
		return "全双工"
	}
	return ""
}

// collectDuplex 采集各口双工(不依赖扩展信息开关), 返回 ifIndex -> (双工, 来源)。
func collectDuplex(s *snmpSession, r *indexResolver, locToIf map[string]string) (map[string]string, map[string]string) {
	dup, src := map[string]string{}, map[string]string{}
	set := func(ifIdx, d, from string) {
		if d == "" || !r.ifExists[ifIdx] || dup[ifIdx] != "" {
			return
		}
		dup[ifIdx], src[ifIdx] = d, from
	}
	for ifIdx, v := range s.walkInts(oidDot3StatsDuplexStat) {
		set(ifIdx, duplexText(v), "EtherLike-MIB")
	}
	_ = s.walk(oidIfMauType, func(idx string, pdu gosnmp.SnmpPDU) {
		ifIdx, _, _ := strings.Cut(idx, ".")
		oid, _ := pdu.Value.(string)
		if i := strings.LastIndexByte(oid, '.'); i >= 0 {
			n, _ := strconv.Atoi(oid[i+1:])
			set(ifIdx, mauDuplex(n), "MAU-MIB")
		}
	})
	if locToIf != nil {
		for num, v := range s.walkInts(oidLldpXdot3LocPortOperMauType) {
			ifIdx := locToIf[num]
			if ifIdx == "" {
				ifIdx = num
			}
			set(ifIdx, mauDuplex(int(v)), "LLDP MAU")
		}
	}
	for ifIdx, v := range s.walkInts(oidH3cIfEthernetDuplex) {
		switch v {
		case 1:
			set(ifIdx, "全双工", "H3C 私有 MIB")
		case 2:
			set(ifIdx, "半双工", "H3C 私有 MIB")
		}
	}
	return dup, src
}

// portVlanText 把端口的 VLAN 信息写成一行给人看的文字。
func portVlanText(p PortDetail) string {
	if p.IPAddr != "" {
		return "L3 " + p.IPAddr
	}
	switch p.VlanMode {
	case "access":
		return "access VLAN " + p.VlanID
	case "trunk":
		t := "trunk"
		if p.VlanID != "" {
			t += " · native " + p.VlanID
		}
		if p.VlanTagged != "" {
			t += " · 允许 " + p.VlanTagged
		}
		return t
	case "hybrid":
		return "hybrid · untagged " + p.VlanUntagged + " · tagged " + p.VlanTagged
	}
	if p.VlanID != "" {
		return "PVID " + p.VlanID
	}
	return ""
}

// buildAggGroups 组装聚合口表: 聚合口本身的状态/模式/VLAN、成员汇总带宽和
// 负载、每个成员的 LACP 状态和 LLDP 对端, 以及一致性检查。对端聚合口
// (PeerAgg)要等所有设备采完才知道, 在 enrichAggPeers 里跨设备补。
func buildAggGroups(aggMembers map[string][]string, ports []PortDetail, ifs map[string]*ifInfo, layer portLayerInfo) []AggGroupDetail {
	byName := make(map[string]PortDetail, len(ports))
	for _, p := range ports {
		byName[p.Name] = p
	}
	aggIf := map[string]*ifInfo{}
	for _, e := range ifs {
		if isAggInterface(e) {
			aggIf[e.display()] = e
		}
	}

	out := make([]AggGroupDetail, 0, len(aggMembers))
	for name, members := range aggMembers {
		sort.Slice(members, func(i, j int) bool { return naturalLess(members[i], members[j]) })
		g := AggGroupDetail{Name: name, Members: members, Mode: "静态"}
		if e := aggIf[name]; e != nil {
			g.Index = e.Index
			g.Alias = cleanAlias(e.Alias, e.Name, e.Descr)
			switch {
			case e.Oper == 1:
				g.Status = "UP"
			case e.Admin == 2:
				g.Status = "ADMIN-DOWN"
			default:
				g.Status = "DOWN"
			}
			if ip, ok := layer.ip[e.Index]; ok {
				g.IPAddr = ip
			}
		}

		speeds := map[int64]int{}
		peers := map[string]bool{}
		var peerOrder, vlanList []string
		vlanSeen := map[string]bool{}
		var maxLoad, minLoad float64 = -1, -1
		for _, m := range members {
			p := byName[m]
			md := AggMemberDetail{
				Name: m, Status: p.Status, SpeedMbps: p.SpeedMbps, Duplex: p.Duplex,
				InMbps: p.InSpeedMbps, OutMbps: p.OutSpeedMbps, InUsage: p.InUsagePercent, OutUsage: p.OutUsagePercent,
				Errors: p.DeltaErrors, RxDbm: p.OpticalRxDbm,
			}
			if st, ok := layer.lacp[p.Index]; ok {
				md.LacpState, md.LacpOK = st.text, st.ok
				g.Mode = "LACP"
			}
			if p.LLDPRemoteDev != "" && p.LLDPRemoteDev != "-" {
				md.PeerDev, md.PeerPort = p.LLDPRemoteDev, p.LLDPRemotePort
				if !peers[p.LLDPRemoteDev] {
					peers[p.LLDPRemoteDev] = true
					peerOrder = append(peerOrder, p.LLDPRemoteDev)
				}
			}
			if p.Status == "UP" {
				g.UpMembers++
				g.SpeedMbps += p.SpeedMbps
				g.InMbps += p.InSpeedMbps
				g.OutMbps += p.OutSpeedMbps
				speeds[p.SpeedMbps]++
				load := p.InSpeedMbps + p.OutSpeedMbps
				if maxLoad < 0 || load > maxLoad {
					maxLoad = load
				}
				if minLoad < 0 || load < minLoad {
					minLoad = load
				}
			}
			if t := portVlanText(p); t != "" && !vlanSeen[t] {
				vlanSeen[t] = true
				vlanList = append(vlanList, t)
			}
			g.MemberInfo = append(g.MemberInfo, md)
		}
		g.PeerDevice = strings.Join(peerOrder, "、")
		if g.SpeedMbps > 0 {
			g.InUsage = math.Min(g.InMbps/float64(g.SpeedMbps)*100, 100)
			g.OutUsage = math.Min(g.OutMbps/float64(g.SpeedMbps)*100, 100)
		}
		if g.Status == "" {
			g.Status = "DOWN"
			if g.UpMembers > 0 {
				g.Status = "UP"
			}
		}
		if len(vlanList) > 0 {
			g.VlanText = vlanList[0]
		}

		// 一致性检查: 都是实际排障里最常见的聚合问题
		if down := len(members) - g.UpMembers; down > 0 && g.UpMembers > 0 {
			g.Issues = append(g.Issues, fmt.Sprintf("%d 个成员口 DOWN, 聚合带宽降级为 %s", down, fmtMbpsText(g.SpeedMbps)))
		}
		if g.UpMembers == 0 && len(members) > 0 {
			g.Issues = append(g.Issues, "全部成员口 DOWN, 聚合链路中断")
		}
		if len(speeds) > 1 {
			g.Issues = append(g.Issues, "成员口速率不一致, 低速口会被踢出或拖累负载分担")
		}
		if len(vlanList) > 1 {
			g.Issues = append(g.Issues, "成员口 VLAN 配置不一致: "+strings.Join(vlanList, " / "))
		}
		if len(peerOrder) > 1 {
			g.Issues = append(g.Issues, "成员口连到了不同的对端设备("+g.PeerDevice+"), 除非对端是堆叠/M-LAG, 否则是接线或配置错误")
		}
		for _, md := range g.MemberInfo {
			if md.LacpState != "" && !md.LacpOK && md.Status == "UP" {
				g.Issues = append(g.Issues, "成员 "+md.Name+" LACP 异常: "+md.LacpState)
			}
			if md.Duplex == "半双工" && md.Status == "UP" {
				g.Issues = append(g.Issues, "成员 "+md.Name+" 半双工")
			}
		}
		// 负载分担: 流量足够大时, 最忙和最闲成员差 5 倍以上说明哈希算法不合适
		if g.UpMembers >= 2 && maxLoad >= 50 && maxLoad > (minLoad+1)*5 {
			g.Issues = append(g.Issues, fmt.Sprintf("负载分担不均: 最忙成员 %.0f Mbps, 最闲 %.0f Mbps, 可检查负载分担算法(建议按源目 IP+端口哈希)", maxLoad, minLoad))
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
	return out
}

func fmtMbpsText(v int64) string {
	if v >= 1000 && v%1000 == 0 {
		return strconv.FormatInt(v/1000, 10) + "G"
	}
	if v >= 1000 {
		return strconv.FormatFloat(float64(v)/1000, 'f', 1, 64) + "G"
	}
	return strconv.FormatInt(v, 10) + "M"
}

// enrichAggPeers 在所有设备采完后跨设备补对端聚合口: 本端成员口的 LLDP 对端
// 如果也被监控, 查它那个口属于哪个聚合组。一端聚合一端没聚合是常见的
// 配置错误(静态聚合下会形成环路或单通)。
func enrichAggPeers(reports []DeviceReport) {
	type devInfo struct {
		ip    string
		ports []PortDetail
	}
	byName := map[string]devInfo{}
	for _, r := range reports {
		if n := normalizeDevName(r.SysName); n != "" {
			byName[n] = devInfo{ip: r.IP, ports: r.Ports}
		}
	}
	for ri := range reports {
		for gi := range reports[ri].AggGroups {
			g := &reports[ri].AggGroups[gi]
			peerAggs := map[string]bool{}
			unAgg := 0
			for mi := range g.MemberInfo {
				md := &g.MemberInfo[mi]
				if md.PeerDev == "" {
					continue
				}
				d, ok := byName[normalizeDevName(md.PeerDev)]
				if !ok {
					continue
				}
				g.PeerIP = d.ip
				p, ok := findPort(d.ports, md.PeerPort)
				if !ok {
					continue
				}
				if p.AggGroup != "" {
					md.PeerAgg = p.AggGroup
					peerAggs[p.AggGroup] = true
				} else {
					unAgg++
				}
			}
			names := make([]string, 0, len(peerAggs))
			for n := range peerAggs {
				names = append(names, n)
			}
			sort.Strings(names)
			g.PeerAgg = strings.Join(names, "、")
			switch {
			case unAgg > 0 && len(peerAggs) > 0:
				g.Issues = append(g.Issues, fmt.Sprintf("对端有 %d 个口没加入聚合, 两端配置不对称", unAgg))
			case unAgg > 0 && g.Mode != "LACP":
				g.Issues = append(g.Issues, "对端这些口没有识别到聚合组(对端未配聚合, 或对端 MIB 未覆盖); 静态聚合两端不对称会形成环路")
			}
			if len(peerAggs) > 1 {
				g.Issues = append(g.Issues, "成员口在对端分属不同的聚合组("+g.PeerAgg+")")
			}
		}
	}
}
