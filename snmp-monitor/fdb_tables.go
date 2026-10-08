package main

// 二层转发表 / ARP 的纯数据处理: 不碰 SNMP, 只把采到的原始行整理成
// fdbResult, 再生成设备详情里的 MAC 表、MAC 转发表和 IP-MAC-端口(ARP)表。
// 采集(walk)在 collect_fdb.go, 这里的函数都可以直接单元测试。

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// fdbBuilder 收集原始转发表行, 最后用 finish() 落定成 fdbResult。
//
// 行的粒度是 (MAC, VLAN): 同一 VLAN 里一个 MAC 只会在一个口上, 不同 VLAN 里可以在不同口上
// (比如路由器 / 三层交换机的 MAC 在业务 VLAN 走接入口、在互联 VLAN 走上联口), 这是正常的,
// 不能先按 MAC 合并再去"裁决"。
type fdbBuilder struct {
	rows     map[fdbRowKey]map[string]bool // (mac, vlan) -> ifIndex 集合; vlan "" = 没有 VLAN 信息
	portVlan map[string]string             // ifIndex -> PVID
	nQ, nD   int                           // Q-BRIDGE / 传统 BRIDGE 表各采到的有效行
	nVlan    int                           // per-VLAN 团体名补采到的行
	vlanNote string                        // per-VLAN 团体名补采的说明(查了哪些 VLAN)
}

type fdbRowKey struct {
	mac  string
	vlan string
}

func newFDBBuilder() *fdbBuilder {
	return &fdbBuilder{rows: map[fdbRowKey]map[string]bool{}, portVlan: map[string]string{}}
}

func (b *fdbBuilder) add(mac, vlan, ifIdx string) bool {
	if mac == "" || ifIdx == "" {
		return false
	}
	k := fdbRowKey{mac: mac, vlan: vlan}
	if b.rows[k] == nil {
		b.rows[k] = map[string]bool{}
	}
	b.rows[k][ifIdx] = true
	return true
}

// fdbStatusUsable: dot1dTpFdbStatus / dot1qTpFdbStatus 里 invalid(2) 是已失效的行,
// self(4) 是设备自己的 MAC(落在 CPU 上, 不是哪个口学到的), 这两种不要;
// learned(3)、mgmt(5, 静态配置 / 黏滞 MAC)和 other(1) 都是真实的转发位置。
// 以前只收 learned(3), 静态绑定 / port-security 黏滞的终端在 MAC 表里整个消失。
func fdbStatusUsable(st int64, ok bool) bool {
	return !ok || (st != 2 && st != 4)
}

// fdbVlanMapper 把 dot1qTpFdbTable 索引里的 FdbId 换成 VLAN 号。
//
// 标准里 dot1qTpFdbTable 的第一段索引是 dot1qFdbId(过滤数据库号), 不是 VLAN 号。
// 华为 / H3C / 锐捷大多让两者相等, 所以以前直接当 VLAN 用也"能对上"; 但有的设备
// FdbId 是内部序号(Juniper、部分 HP / Dell / 白牌), VLAN 列就整列是错的, 按 VLAN
// 筛选、ARP 定位、同 VLAN 漂移判断跟着全错。这里用 dot1qVlanFdbId(vlan -> FdbId)反查:
//   - 一个 FdbId 只对应一个 VLAN: 换成那个 VLAN;
//   - 多个 VLAN 共用一个 FdbId(共享学习 SVL): 分不出是哪个 VLAN, 返回 "";
//   - 设备没提供映射表 / 映射里没有这个 FdbId: 按 FdbId == VLAN 处理(与旧行为一致)。
func fdbVlanMapper(vlanFdb map[string]int64) func(fdbID string) string {
	byFdb := map[string]map[string]bool{}
	for idx, fid := range vlanFdb {
		if fid <= 0 {
			continue
		}
		vlan := idx
		if i := strings.LastIndexByte(idx, '.'); i >= 0 { // 索引 = timeMark.vlanIndex
			vlan = idx[i+1:]
		}
		if !validVlanID(vlan) {
			continue
		}
		k := strconv.FormatInt(fid, 10)
		if byFdb[k] == nil {
			byFdb[k] = map[string]bool{}
		}
		byFdb[k][vlan] = true
	}
	return func(fdbID string) string {
		set, ok := byFdb[fdbID]
		if !ok {
			if validVlanID(fdbID) {
				return fdbID
			}
			return ""
		}
		if len(set) != 1 {
			return ""
		}
		for v := range set {
			return v
		}
		return ""
	}
}

func validVlanID(v string) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n >= 1 && n <= 4094
}

// finish 把原始行落定成 fdbResult。
//
//   - 同一个 MAC 在 Q-BRIDGE(带 VLAN)表里有行时, 传统 BRIDGE 表(不带 VLAN)里它的行全部丢弃。
//     两张表是同一份转发表的两种视图, dot1dTpFdb 往往只反映默认 VLAN 或是抓取时刻不同的旧值,
//     以前把两边的口并到一起, 凭空造出一批"多个口都学到"的歧义 MAC。
//   - portMACs 是每个口学到的不同 MAC 数(一个 MAC 在多个口出现就每个口各算一次), 跟交换机
//     上按端口查 MAC 的数量一致, 用来判断上联口。以前只把 MAC 记到 ifIndex 按字符串排序后的
//     第一个口上("100" 排在 "12" 前面), 上联口的计数会偏小或偏大。
func (b *fdbBuilder) finish() fdbResult {
	res := fdbResult{
		macToIf:      map[string]string{},
		macVlan:      map[string]string{},
		macLocations: map[string][]string{},
		portMACs:     map[string]int{},
		portVlan:     b.portVlan,
	}
	if res.portVlan == nil {
		res.portVlan = map[string]string{}
	}
	hasVlan := map[string]bool{}
	for k := range b.rows {
		if k.vlan != "" {
			hasVlan[k.mac] = true
		}
	}
	locSet := map[string]map[string]bool{}
	vlans := map[string]map[string]bool{}
	for k, set := range b.rows {
		if k.vlan == "" && hasVlan[k.mac] {
			continue
		}
		if locSet[k.mac] == nil {
			locSet[k.mac] = map[string]bool{}
		}
		for ifIdx := range set {
			locSet[k.mac][ifIdx] = true
		}
		if k.vlan != "" {
			if vlans[k.mac] == nil {
				vlans[k.mac] = map[string]bool{}
			}
			vlans[k.mac][k.vlan] = true
		}
		vlan := k.vlan
		if vlan == "" {
			vlan = "-"
		}
		res.fdbRows = append(res.fdbRows, fdbRow{MAC: k.mac, VlanID: vlan, Locations: sortedIfIndexes(set)})
	}
	for mac, set := range locSet {
		ports := sortedIfIndexes(set)
		res.macLocations[mac] = ports
		res.macToIf[mac] = ports[0] // 朴素默认值; inspectDevice 里会用 resolveFDB 重新裁决
		for _, p := range ports {
			res.portMACs[p]++
		}
	}
	for mac, set := range vlans {
		if len(set) == 1 {
			for v := range set {
				res.macVlan[mac] = v
			}
		}
	}
	sort.Slice(res.fdbRows, func(i, j int) bool {
		if res.fdbRows[i].VlanID != res.fdbRows[j].VlanID {
			return naturalLess(res.fdbRows[i].VlanID, res.fdbRows[j].VlanID)
		}
		return res.fdbRows[i].MAC < res.fdbRows[j].MAC
	})

	var parts []string
	if b.nQ > 0 {
		parts = append(parts, fmt.Sprintf("dot1qTpFdb(%d 条)", b.nQ))
	}
	if b.nD > 0 {
		parts = append(parts, fmt.Sprintf("dot1dTpFdb(%d 条)", b.nD))
	}
	if b.nVlan > 0 {
		parts = append(parts, fmt.Sprintf("按 VLAN 团体名补采(%d 条)", b.nVlan))
	}
	if len(parts) == 0 {
		res.source = "MAC 地址表为空 (SNMP 视图可能未放通 bridge MIB)"
	} else {
		res.source = strings.Join(parts, " + ")
	}
	if b.vlanNote != "" {
		res.source += "; " + b.vlanNote
	}
	return res
}

func sortedIfIndexes(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i], out[j]) })
	return out
}

// uplinkPortSet: 有 LLDP/CDP 邻居的口, 加上成员口有邻居的聚合口。
// 转发表是在聚合口(AggregatePort / Eth-Trunk / Bridge-Aggregation)上学 MAC 的, 但 LLDP
// 是在成员口上报的; 以前只看聚合口本身有没有邻居, 下联接入交换机的聚合口常被当成"接入口"。
func uplinkPortSet(ifs map[string]*ifInfo, lldp map[string]lldpNeighbor, aggGroup map[string]string) map[string]bool {
	out := map[string]bool{}
	aggIdx := map[string]string{}
	for idx, e := range ifs {
		if isAggInterface(e) {
			aggIdx[e.display()] = idx
		}
	}
	for idx, n := range lldp {
		if n.Dev == "" {
			continue
		}
		out[idx] = true
		if a := aggIdx[aggGroup[idx]]; a != "" {
			out[a] = true
		}
	}
	return out
}

func isUplinkPort(ifIdx string, portMACs map[string]int, nbrPorts map[string]bool, threshold int) bool {
	return nbrPorts[ifIdx] || (threshold > 0 && portMACs[ifIdx] >= threshold)
}

// resolveFDB 在 LLDP 和端口角色都已知的情况下, 对每个"在多个端口都学到过"
// 的 MAC 做最终裁决: 优先选非上联口(更可能是真实接入点), 多个候选都不是
// 上联口时选学到 MAC 数最少的那个(更"专一", 更像是专属接入口); 全部候选
// 都是上联口时说明这台终端是通过冗余链路间接可见, 保留 MAC 数最多的上联口
// (最可能是当前的主用链路), 并标记为有歧义, 在 MAC 总表里如实呈现。
// 端口 MAC 计数(fdb.portMACs)是原始值, 不随裁决变化。
func resolveFDB(fdb fdbResult, nbrPorts map[string]bool, uplinkThreshold int) (macToIf map[string]string, ambiguous map[string]bool) {
	macToIf = make(map[string]string, len(fdb.macLocations))
	ambiguous = make(map[string]bool)

	for mac, locs := range fdb.macLocations {
		if len(locs) == 1 {
			macToIf[mac] = locs[0]
			continue
		}
		ambiguous[mac] = true

		var access, uplink []string
		for _, l := range locs {
			if isUplinkPort(l, fdb.portMACs, nbrPorts, uplinkThreshold) {
				uplink = append(uplink, l)
			} else {
				access = append(access, l)
			}
		}
		pick := func(cands []string, minimize bool) string {
			best := cands[0]
			for _, c := range cands[1:] {
				if minimize && fdb.portMACs[c] < fdb.portMACs[best] {
					best = c
				} else if !minimize && fdb.portMACs[c] > fdb.portMACs[best] {
					best = c
				}
			}
			return best
		}
		if len(access) > 0 {
			macToIf[mac] = pick(access, true) // 多个接入候选: 选最"专一"的
		} else {
			macToIf[mac] = pick(uplink, false) // 全是上联: 选当前承载 MAC 最多的那条
		}
	}
	return
}

// vlanFromIfaceName 从三层 SVI 接口名里抠出 VLAN 号, 覆盖各厂商的常见写法:
// "Vlan10" / "Vlan-interface10" (H3C) / "Vlanif10" (华为) / "VLAN 10" (锐捷)。
func vlanFromIfaceName(name string) string {
	m := vlanNameRe.FindStringSubmatch(strings.TrimSpace(name))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// arpAccept: ARP 行是否可用。以前按 "IP 以 .255 结尾" 丢弃广播地址, /23、/22 网段里
// x.x.x.255 是正常主机地址, 那批终端在 ARP 表里整个看不到; 广播 / 组播地址真正的特征
// 是 MAC 为全 F 或 IP 落在组播 / 受限广播段。
func arpAccept(ip, mac string) bool {
	if mac == "" || mac == "00:00:00:00:00:00" || mac == "FF:FF:FF:FF:FF:FF" {
		return false
	}
	p := net.ParseIP(ip).To4()
	if p == nil || p[0] == 0 || p[0] == 127 || p[0] >= 224 {
		return false
	}
	return true
}

// arpIndexIPv4: ipNetToPhysicalTable 的索引是 ifIndex.addrType.len.a.b.c.d;
// 个别设备省略长度字节(ifIndex.1.a.b.c.d), 两种都认, 只取 IPv4。
func arpIndexIPv4(idx string) (ifIdx, ip string, ok bool) {
	parts := strings.Split(idx, ".")
	switch {
	case len(parts) == 7 && parts[1] == "1" && parts[2] == "4":
		return parts[0], strings.Join(parts[3:], "."), true
	case len(parts) == 6 && parts[1] == "1":
		return parts[0], strings.Join(parts[2:], "."), true
	}
	return "", "", false
}

type hostTables struct {
	macTable []MACEntry
	fdbTable []MACEntry
	hosts    []HostMappingEntry
}

// buildHostTables 生成设备详情里的三张表。不修改 fdb(它可能是上一轮的缓存)。
//
//   - MAC 表: 一个 MAC 一行, 端口是裁决后的位置; VLAN 取这个 MAC 在该口上的转发表行
//     (以前 MAC 跨多个 VLAN 时直接退回端口 PVID, trunk 口上就成了 native VLAN)。
//   - MAC 转发表: 一个 (VLAN, MAC) 一行, 跟交换机 CLI 的条目一一对应。
//   - ARP 表: VLAN 优先取 ARP 所在的 VLANIF(这条 ARP 就是在这个 VLAN 里解析的, 是确切值),
//     端口优先取同一 VLAN 的转发表行; 以前不论 VLAN 一律用按 MAC 合并后的位置, 一个 MAC
//     在两个 VLAN 走不同口时会给出另一个 VLAN 的口。ARP 学在路由口 / 三层聚合口上的,
//     转发表里没有它, 以前一律"未定位", 现在直接用 ARP 所在的口。
func buildHostTables(fdb fdbResult, macAmbiguous map[string]bool, arp []arpEntry, ifs map[string]*ifInfo,
	nbrPorts map[string]bool, threshold int) hostTables {
	var out hostTables
	isUp := func(ifIdx string) bool { return isUplinkPort(ifIdx, fdb.portMACs, nbrPorts, threshold) }
	kindOf := func(ifIdx string) string {
		if isUp(ifIdx) {
			return "上联/级联口"
		}
		return "接入口"
	}

	rowLoc := make(map[fdbRowKey][]string, len(fdb.fdbRows))
	macPortVlans := map[string]map[string][]string{} // mac -> ifIndex -> 在这个口上的 VLAN
	for _, row := range fdb.fdbRows {
		rowLoc[fdbRowKey{mac: row.MAC, vlan: row.VlanID}] = row.Locations
		if row.VlanID == "-" {
			continue
		}
		if macPortVlans[row.MAC] == nil {
			macPortVlans[row.MAC] = map[string][]string{}
		}
		for _, l := range row.Locations {
			macPortVlans[row.MAC][l] = append(macPortVlans[row.MAC][l], row.VlanID)
		}
	}

	out.macTable = make([]MACEntry, 0, len(fdb.macToIf))
	for mac, ifIdx := range fdb.macToIf {
		e, ok := ifs[ifIdx]
		if !ok {
			continue
		}
		vlan := fdb.macVlan[mac]
		if vlan == "" {
			if vs := macPortVlans[mac][ifIdx]; len(vs) > 0 {
				vs = append([]string(nil), vs...)
				sort.Slice(vs, func(i, j int) bool { return naturalLess(vs[i], vs[j]) })
				vlan = strings.Join(vs, ",")
			} else if v, ok := fdb.portVlan[ifIdx]; ok {
				vlan = v
			} else {
				vlan = "-"
			}
		}
		up := isUp(ifIdx)
		entry := MACEntry{MAC: mac, VlanID: vlan, Port: e.display(), PortKind: kindOf(ifIdx), IsUplink: up, Ambiguous: macAmbiguous[mac]}
		if entry.Ambiguous {
			for _, l := range fdb.macLocations[mac] {
				if le, ok := ifs[l]; ok {
					entry.Locations = append(entry.Locations, le.display())
				}
			}
		}
		out.macTable = append(out.macTable, entry)
	}
	sort.Slice(out.macTable, func(i, j int) bool { return out.macTable[i].MAC < out.macTable[j].MAC })

	for _, row := range fdb.fdbRows {
		if len(row.Locations) == 0 {
			continue
		}
		ifIdx := row.Locations[0]
		for _, candidate := range row.Locations {
			if candidate == fdb.macToIf[row.MAC] {
				ifIdx = candidate
				break
			}
		}
		e, ok := ifs[ifIdx]
		if !ok {
			continue
		}
		entry := MACEntry{
			MAC: row.MAC, VlanID: row.VlanID, Port: e.display(),
			PortKind: kindOf(ifIdx), IsUplink: isUp(ifIdx), Ambiguous: len(row.Locations) > 1,
		}
		if entry.Ambiguous {
			for _, location := range row.Locations {
				if le, ok := ifs[location]; ok {
					entry.Locations = append(entry.Locations, le.display())
				}
			}
		}
		out.fdbTable = append(out.fdbTable, entry)
	}

	out.hosts = make([]HostMappingEntry, 0, len(arp))
	for _, a := range arp {
		vlanName := "-"
		arpIf := ifs[a.IfIndex]
		if arpIf != nil {
			vlanName = arpIf.display()
		} else if a.IfIndex != "" {
			vlanName = "ifIndex-" + a.IfIndex
		}
		entry := HostMappingEntry{IP: a.IP, MAC: a.MAC, VlanIf: vlanName, RealPort: "未定位", PortKind: "未定位"}

		// VLAN: VLANIF 名 > 转发表里该 MAC 唯一的 VLAN > 端口 PVID(猜测)
		svi := ""
		if arpIf != nil {
			svi = vlanFromIfaceName(vlanName)
		}
		if svi != "" {
			entry.VlanID, entry.VlanExact = svi, true
		} else if v := fdb.macVlan[a.MAC]; v != "" {
			entry.VlanID, entry.VlanExact = v, true
		}

		port := ""
		if entry.VlanExact {
			if locs := rowLoc[fdbRowKey{mac: a.MAC, vlan: entry.VlanID}]; len(locs) > 0 {
				port = locs[0]
				for _, l := range locs {
					if l == fdb.macToIf[a.MAC] {
						port = l
					}
				}
			}
		}
		if port == "" && (!entry.VlanExact || len(macPortVlans[a.MAC]) == 0) {
			// 只有转发表里这个 MAC 没有任何带 VLAN 的行时才退回按 MAC 合并的位置;
			// 它在别的 VLAN 里有行、在本 VLAN 没有(已老化)时, 那个口不一定是这个 IP 的口。
			port = fdb.macToIf[a.MAC]
		}
		if port == "" && svi == "" && arpIf != nil && (arpIf.Physical || isAggInterface(arpIf)) {
			port = a.IfIndex // 路由口 / 三层聚合口: ARP 学在哪个口, 终端就在哪个口后面
		}
		if e, ok := ifs[port]; ok {
			entry.RealPort, entry.PortKind = e.display(), kindOf(port)
			if entry.VlanID == "" {
				entry.VlanID = fdb.portVlan[port]
			}
		}
		if entry.VlanID == "" {
			entry.VlanID = "-"
		}
		out.hosts = append(out.hosts, entry)
	}
	return out
}
