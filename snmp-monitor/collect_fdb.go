package main

// 采集: 二层转发表(MAC → 物理口, 含上联口判定和歧义裁决)和 ARP。

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// ----------------- 二层转发表 (MAC -> 物理口) -----------------

type fdbResult struct {
	macToIf      map[string]string
	macVlan      map[string]string
	macLocations map[string][]string // mac -> 该 mac 在原始表里出现过的所有 ifIndex(去重), 用于识别"上联/下联都学到"的重复
	fdbRows      []fdbRow            // VLAN+MAC 粒度的原始转发表行；不把跨 VLAN 的同 MAC 合并
	portMACs     map[string]int
	portVlan     map[string]string // ifIndex -> PVID, 精确 VLAN 缺失时的兜底猜测
	source       string
}

type fdbRow struct {
	MAC       string
	VlanID    string
	Locations []string
}

// collectFDB 同时支持 Q-BRIDGE 与传统 BRIDGE 两张表。
//
// 这是 H3C "具体接入端口" 错误的根因: Comware 把 MAC 表放在
// dot1qTpFdbTable(带 VLAN 索引)里, dot1dTpFdbTable 往往只返回默认 VLAN 或干脆为空。
// 旧版只读 dot1dTpFdbPort, 查不到就退回显示 VLAN 接口名, 看起来就像"端口错了"。
//
// 同一个 MAC 出现在多个端口是常见现象(尤其是 STP 收敛过程中、或者两张表
// 抓取时间点不完全一致), 这里不在采集阶段就武断地"谁先来算谁", 而是把
// 一个 mac 见过的所有端口都记下来(macLocations), 交给 inspectDevice 在
// 拿到 LLDP/端口角色信息之后统一裁决——这样能优先选出真正的接入口,
// 而不是随机选中上联口。
// resolveBridgePortIfIndex 把 dot1dBasePortNum(桥端口号)换算成 ifIndex。
//
// 之前的兜底规则是"只有 dot1dBasePortIfIndex 整张表一条都查不到时, 才把
// 桥端口号直接当 ifIndex 用", 隐含假设是"一台设备要么整张表都能查、要么
// 整张表都查不到"。实测锐捷部分型号是混合状态: 这张表本身能查到, 但只
// 覆盖一部分端口(比如只有以太网口, 没有聚合口/VLAN 口), 其余端口在
// dot1qPvid 等表里露出的桥端口号其实就是它们自己的 ifIndex——按旧规则
// 因为"表不是空的"就不会再兜底, 这部分端口的 VLAN 就丢了。
// 现在改成逐个端口判断: 换算不出来就检查这个号码本身是不是一个存在的
// ifIndex, 存在就采信, 不再要求整张表全空这个前提。
func resolveBridgePortIfIndex(bport string, bridgeToIf map[string]string, r *indexResolver) string {
	if ifIdx, ok := bridgeToIf[bport]; ok {
		return ifIdx
	}
	if r.ifExists[bport] {
		return bport
	}
	return ""
}

// portLayerInfo 是"端口表"里三项按需显示的补充信息。
// collectAggMembership 查出"物理成员口 -> 所属聚合口", 依次尝试四个来源,
// 前面的来源给出结果的成员口, 后面的来源不再覆盖:
//
//  1. dot3adAggPortAttachedAggID(.13) —— LACP 协商后的实际归属
//  2. dot3adAggPortSelectedAggID(.12) —— 部分固件只填这一列
//  3. ifStackTable —— 上层=聚合口、下层=成员口的堆叠关系
//  4. lldpXdot3LocLinkAggPortId —— 开了 LLDP 的设备会通告"本口属于哪个聚合",
//     静态聚合(锐捷 aggregatePort 手工模式)也会填
//
// 之前只查 1(而且 OID 实际写成了 .12)和 3, 并且对 dot3ad 返回值不做任何
// 校验, 只要是个存在的 ifIndex 就当成聚合口名。问题在于 802.3ad 里每个物理
// 口都有一个自己的 aggregator, 没有加入聚合的口报的往往就是"它自己的
// ifIndex"——于是每个口都被记成"属于以自己命名的聚合组", 而且因为已经有
// 值了, 后面 ifStack 的兜底完全不会生效, 真正的 AggregatePort 成员反而
// 查不出来。现在返回值必须指向一个真正的聚合口(ifType=161 或名字属于
// 聚合族)才采信; 另外有的固件在这里填的是聚合组号(AggregatePort 3 就填 3)
// 而不是 ifIndex, 也按组号兜一次。
//
// 设备上有聚合口、但四个来源都没查到任何成员时, 返回一段诊断文字, 写明
// 每个来源各返回了多少行, 用户拿去对照 snmpwalk 结果就能看出是哪一步断了。
func collectAggMembership(s *snmpSession, ifs map[string]*ifInfo, r *indexResolver) (map[string]string, string) {
	out := map[string]string{}

	aggIfs := map[string]*ifInfo{}
	aggByNum := map[string]string{} // 聚合组号 -> ifIndex, 例如 "3" -> AggregatePort 3 的 ifIndex
	numDup := map[string]bool{}
	for idx, e := range ifs {
		if !isAggInterface(e) {
			continue
		}
		aggIfs[idx] = e
		_, num, _ := strings.Cut(normPortKey(e.display()), "|")
		if num == "" {
			continue
		}
		if prev, ok := aggByNum[num]; ok && prev != idx {
			numDup[num] = true
		}
		aggByNum[num] = idx
	}
	for num := range numDup {
		delete(aggByNum, num)
	}
	if len(aggIfs) == 0 {
		return out, "" // 设备上根本没有聚合口, 几张表都不用查
	}

	resolve := func(member string, v int64) string {
		if v <= 0 || !r.ifExists[member] || aggIfs[member] != nil {
			return ""
		}
		id := strconv.FormatInt(v, 10)
		if id == member {
			return "" // 未聚合的口指向自己的 aggregator, 不是成员关系
		}
		if e, ok := aggIfs[id]; ok {
			return e.display()
		}
		if idx, ok := aggByNum[id]; ok {
			return aggIfs[idx].display()
		}
		return ""
	}

	var nAttached, nSelected, nStack, nStackAgg, nLLDP int
	for _, src := range []struct {
		oid string
		n   *int
	}{{oidDot3adAggPortAttachedAggID, &nAttached}, {oidDot3adAggPortSelectedAggID, &nSelected}} {
		_ = s.walk(src.oid, func(idx string, pdu gosnmp.SnmpPDU) {
			*src.n++
			if _, done := out[idx]; done {
				return
			}
			if g := resolve(idx, toInt64(pdu.Value)); g != "" {
				out[idx] = g
			}
		})
	}

	// ifStackTable: 只排除明确表示"已删除"的 destroy(6)。RowStatus 严格区分
	// active/notInService 是给 SET 用的, 只读遍历时设备肯把这一行返回出来
	// 就说明堆叠关系存在(锐捷 S18000 这张表填的就不是 active(1))。
	_ = s.walk(oidIfStackStatus, func(idx string, pdu gosnmp.SnmpPDU) {
		nStack++
		if toInt64(pdu.Value) == 6 {
			return
		}
		higher, lower, ok := strings.Cut(idx, ".")
		if !ok || higher == "0" || lower == "0" || higher == lower {
			return
		}
		e, isAgg := aggIfs[higher]
		if !isAgg {
			return
		}
		nStackAgg++
		if _, done := out[lower]; done || !r.ifExists[lower] || aggIfs[lower] != nil {
			return
		}
		out[lower] = e.display()
	})

	if missingAggMembers(aggIfs, out) {
		locToIf := lldpLocalPortMap(s, r)
		_ = s.walk(oidLldpXdot3LocLinkAggPortId, func(idx string, pdu gosnmp.SnmpPDU) {
			nLLDP++
			ifIdx := locToIf[idx]
			if ifIdx == "" && r.ifExists[idx] {
				ifIdx = idx // localPortNum 即 ifIndex 的设备
			}
			if ifIdx == "" {
				return
			}
			if _, done := out[ifIdx]; done {
				return
			}
			if g := resolve(ifIdx, toInt64(pdu.Value)); g != "" {
				out[ifIdx] = g
			}
		})
	}

	if len(out) > 0 {
		return out, ""
	}
	names := make([]string, 0, len(aggIfs))
	for _, e := range aggIfs {
		names = append(names, e.display())
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	if len(names) > 6 {
		names = append(names[:6], "...")
	}
	return out, fmt.Sprintf("发现 %d 个聚合口(%s), 但没查到成员口: "+
		"dot3adAggPortAttachedAggID 返回 %d 行, dot3adAggPortSelectedAggID %d 行, "+
		"ifStackTable %d 行(其中上层是聚合口的 %d 行), lldpXdot3LocLinkAggPortId %d 行。"+
		"如果全是 0, 多半是 SNMP view 没放通 1.2.840.10006 / 1.3.6.1.2.1.31.1.2 / 1.0.8802; "+
		"不是 0 但仍识别不到, 请把这几个 OID 的 snmpwalk 结果发过来",
		len(aggIfs), strings.Join(names, "、"), nAttached, nSelected, nStack, nStackAgg, nLLDP)
}

// missingAggMembers 判断是否还有聚合口一个成员都没查到——只有这时才值得
// 再多 walk 几张 LLDP 表兜底。
func missingAggMembers(aggIfs map[string]*ifInfo, found map[string]string) bool {
	has := make(map[string]bool, len(aggIfs))
	for _, g := range found {
		has[g] = true
	}
	for _, e := range aggIfs {
		if !has[e.display()] {
			return true
		}
	}
	return false
}

// portExtRaw 是扩展端口指标的原始计数, 在 inspectDevice 组装端口表时跟上一轮
// 快照相减得到增量/速率。
type portExtRaw struct {
	lastChange                         map[string]int64 // ifLastChange, TimeTicks(1/100 秒)
	duplex                             map[string]int64
	inDisc, outDisc, outErr, bcast, mc map[string]uint64
	atMs                               int64
}

func collectPortExt(s *snmpSession) portExtRaw {
	var x portExtRaw
	x.atMs = timedWalk(func() {
		x.inDisc = s.walkUints(oidIfInDiscards)
		x.outDisc = s.walkUints(oidIfOutDiscards)
		x.outErr = s.walkUints(oidIfOutErrors)
		x.bcast = s.walkUints(oidIfHCInBcastPkts)
		x.mc = s.walkUints(oidIfHCInMcastPkts)
	})
	x.lastChange = s.walkInts(oidIfLastChange)
	// 双工改在 collectDuplex 里采(不依赖扩展开关, 且有多个兜底来源)
	return x
}

// collectInventory 取资产信息: sysLocation/sysContact 两个标量, 加上
// ENTITY-MIB 里第一个机框(entPhysicalClass=3)的型号/序列号/软硬件版本。
// 盒式交换机只有一个机框; 框式设备取到的是整机机框, 板卡序列号不在这里展示。
//
// 资产信息几乎不变, 但以前每轮都整套重新采: 堆叠/框式设备每个机框 3~4 个单独的 GET,
// 16 个机框就是 60 多次串行往返, 是 v10 之后巡检变慢的原因之一。现在:
//   - 同一台设备 invCacheTTL 内直接用缓存(sysLocation 改了最多晚半小时显示);
//   - 所有标量合并成一次多 OID 的 GET(getStrings)。
const invCacheTTL = 30 * time.Minute

type invCacheEntry struct {
	inv *DeviceInventory
	at  time.Time
}

var (
	invCacheLock sync.Mutex
	invCache     = map[string]invCacheEntry{}
)

func cachedInventory(key string, s *snmpSession) *DeviceInventory {
	invCacheLock.Lock()
	e, ok := invCache[key]
	invCacheLock.Unlock()
	if ok && time.Since(e.at) < invCacheTTL {
		return e.inv
	}
	inv := collectInventory(s)
	invCacheLock.Lock()
	invCache[key] = invCacheEntry{inv: inv, at: time.Now()}
	invCacheLock.Unlock()
	return inv
}

// getStrings 把多个标量 OID 合并成少数几次 GET(每包 getBatch 个)。
// 某一包整体失败(SNMPv1 遇到不存在的 OID 会整包报 noSuchName、或包太大)时退回逐个 GET。
const getBatch = 12

func (s *snmpSession) getStrings(oids []string) map[string]string {
	out := make(map[string]string, len(oids))
	for i := 0; i < len(oids); i += getBatch {
		part := oids[i:min(i+getBatch, len(oids))]
		res, err := s.c.Get(part)
		if err != nil || res == nil || res.Error != gosnmp.NoError || len(res.Variables) != len(part) {
			for _, o := range part {
				if v := s.getString(o); v != "" {
					out[o] = v
				}
			}
			continue
		}
		for j, v := range res.Variables {
			switch v.Type {
			case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
				continue
			}
			if v.Value != nil {
				out[part[j]] = decodeText(v.Value)
			}
		}
	}
	return out
}

func collectInventory(s *snmpSession) *DeviceInventory {
	sys := s.getStrings([]string{oidSysLocation, oidSysContact})
	inv := &DeviceInventory{
		Location: strings.TrimSpace(sys[oidSysLocation]),
		Contact:  strings.TrimSpace(sys[oidSysContact]),
	}
	chassis := ""
	var frames []string
	for idx, cls := range s.walkInts(oidEntPhysicalClass) {
		switch cls {
		case 3: // chassis: 堆叠时每个成员一个
			frames = append(frames, idx)
			if chassis == "" || naturalLess(idx, chassis) {
				chassis = idx
			}
		case 9: // module: 框式设备的板卡(盒式设备的电源/风扇一般也报成 module, 只作参考)
			inv.EntModules++
		case 5: // container: 槽位
			inv.EntSlots++
		}
	}
	// 每个机框的型号/名称, 用来识别堆叠(IRF/iStack/VSU)成员数和成员型号
	sort.Slice(frames, func(i, j int) bool { return naturalLess(frames[i], frames[j]) })
	if len(frames) > 16 {
		frames = frames[:16]
	}
	var oids []string
	if chassis != "" {
		oids = append(oids, oidEntPhysicalModel+"."+chassis, oidEntPhysicalSerial+"."+chassis,
			oidEntPhysicalSoftRev+"."+chassis, oidEntPhysicalHardRev+"."+chassis)
	}
	for _, idx := range frames {
		oids = append(oids, oidEntPhysicalName+"."+idx, oidEntPhysicalModel+"."+idx,
			oidEntPhysicalSerial+"."+idx, oidEntPhysicalDescr+"."+idx)
	}
	val := s.getStrings(oids)
	v := func(base, idx string) string { return strings.TrimSpace(val[base+"."+idx]) }
	if chassis != "" {
		inv.Model = v(oidEntPhysicalModel, chassis)
		inv.Serial = v(oidEntPhysicalSerial, chassis)
		inv.SoftVer = v(oidEntPhysicalSoftRev, chassis)
		inv.HardVer = v(oidEntPhysicalHardRev, chassis)
	}
	for _, idx := range frames {
		f := EntChassis{Index: idx, Name: v(oidEntPhysicalName, idx), Model: v(oidEntPhysicalModel, idx), Serial: v(oidEntPhysicalSerial, idx)}
		if f.Model == "" {
			f.Model = v(oidEntPhysicalDescr, idx)
		}
		inv.Chassis = append(inv.Chassis, f)
	}
	if inv.Model == "" && len(inv.Chassis) > 0 {
		inv.Model = inv.Chassis[0].Model
	}
	if inv.Model == "" && inv.Serial == "" && inv.SoftVer == "" && inv.HardVer == "" && inv.Location == "" && inv.Contact == "" && len(inv.Chassis) == 0 {
		return nil
	}
	return inv
}

func duplexText(v int64) string {
	switch v {
	case 2:
		return "半双工"
	case 3:
		return "全双工"
	}
	return ""
}

type portLayerInfo struct {
	aggGroup map[string]string // ifIndex -> 所属聚合组显示名(不是成员则不在此 map 里)
	aggDiag  string            // 有聚合口但成员关系没查全时的诊断说明, 空 = 正常
	vlan     map[string]string // ifIndex -> 二层 PVID(是三层口则不在此 map 里)
	ip       map[string]string // ifIndex -> 三层配置的 IP(是纯二层口则不在此 map 里)

	vlans     map[string]*portVlanInfo // ifIndex -> VLAN 成员关系(Q-BRIDGE / Cisco 私有表)
	vlanNames map[int]string
	duplex    map[string]string // ifIndex -> 全双工/半双工
	dupSrc    map[string]string

	lacp map[string]lacpState // 聚合成员口 ifIndex -> LACP 协商状态(静态聚合没有)

	// 设备自己的全部接口地址(一个口多个 IP 也都在)和自己的 MAC(桥 MAC + 三层口 MAC):
	// ARP 总表 / 二层体检靠它们区分"三层交换机自己的多个接口"跟"一个终端占了很多 IP"。
	addrs   []OwnIfAddr
	ownMACs []string
}

// oidDot3adAggPortActorOperState: 本端 LACP 状态字节(activity/timeout/aggregation/
// synchronization/collecting/distributing/defaulted/expired)。
const oidDot3adAggPortActorOperState = ".1.2.840.10006.300.43.1.2.1.1.21"

type lacpState struct {
	text string
	ok   bool
}

// decodeLacpState 解读 LACP 状态字节。MIB 里定义成 BITS(bit0=activity 在最高位),
// 但不少设备直接把协议里的原始字节(activity 在最低位)塞进来; aggregation 位
// 在所有可聚合口上都是 1, 用它判断是哪种位序。
func decodeLacpState(v interface{}) (lacpState, bool) {
	b, ok := v.([]byte)
	if !ok || len(b) == 0 {
		return lacpState{}, false
	}
	raw := b[0]
	var rev uint8
	for i := 0; i < 8; i++ {
		if raw&(1<<uint(i)) != 0 {
			rev |= 0x80 >> uint(i)
		}
	}
	// 两种位序各解读一遍, 取"更像真实状态"的那个: 可聚合位应为 1、正在收发时
	// 不应同时处于 expired、distributing 意味着 collecting 和 synchronization。
	plausible := func(s uint8) int {
		n := 0
		if s&0x04 != 0 {
			n += 2
		}
		if s&0x80 == 0 {
			n += 2
		}
		if s&0x20 == 0 || (s&0x10 != 0 && s&0x08 != 0) {
			n++
		}
		return n
	}
	st := rev // 标准 BITS 位序换成协议位序
	if plausible(raw) > plausible(rev) {
		st = raw
	}
	if st == 0 {
		return lacpState{}, false
	}
	const (
		sync, collecting, distributing, defaulted, expired = 0x08, 0x10, 0x20, 0x40, 0x80
	)
	switch {
	case st&expired != 0:
		return lacpState{text: "LACP 超时(对端报文中断)"}, true
	case st&defaulted != 0:
		return lacpState{text: "对端无 LACP 响应(对端可能是静态聚合或没配聚合)"}, true
	case st&sync != 0 && st&collecting != 0 && st&distributing != 0:
		return lacpState{text: "已同步 · 正常收发", ok: true}, true
	case st&sync == 0:
		return lacpState{text: "未同步(协商中或被对端拒绝)"}, true
	}
	return lacpState{text: "已同步 · 未进入收发状态"}, true
}

// collectPortLayerInfo 判断每个口是二层还是三层, 二层的给 VLAN, 三层的给 IP,
// 再顺带给出聚合组归属。三张表都很小(全设备一次性一张表, 不是按端口挨个查),
// 开销可以忽略不计, 不需要跟着 skip_rates/skip_hosts 这些开关走。
//
//   - 聚合组: IEEE8023-LAG-MIB dot3adAggPortAttachedAggID, 成员口 -> 聚合口 ifIndex,
//     反查聚合口自己的显示名。
//   - 三层 IP: IP-MIB ipAddrTable 反查, 哪个 ifIndex 配了 IP 地址——配了就是三层口
//     (路由口或 SVI), 直接把 IP 显示出来。
//   - 二层 VLAN: 没查到 IP 的口, 用 dot1qPvid(所在桥端口的默认 VLAN)展示, 这里复用
//     跟 collectFDB 里一样的桥端口号->ifIndex 换算。
func collectPortLayerInfo(s *snmpSession, ifs map[string]*ifInfo, r *indexResolver) portLayerInfo {
	out := portLayerInfo{vlan: map[string]string{}, ip: map[string]string{}}
	out.aggGroup, out.aggDiag = collectAggMembership(s, ifs, r)

	addrIf := map[string]string{}
	_ = s.walk(oidIpAdEntIfIndex, func(idx string, pdu gosnmp.SnmpPDU) {
		ifIdx := strconv.FormatInt(toInt64(pdu.Value), 10)
		if !r.ifExists[ifIdx] {
			return
		}
		addrIf[idx] = ifIdx
		if _, exists := out.ip[ifIdx]; !exists { // 一个口可能配多个 IP(主/从), 端口表只展示第一个
			out.ip[ifIdx] = idx
		}
	})
	if len(addrIf) > 0 {
		masks := map[string]int{}
		_ = s.walk(oidIpAdEntNetMask, func(idx string, pdu gosnmp.SnmpPDU) {
			if b, ok := pdu.Value.(string); ok {
				if ip := net.ParseIP(b).To4(); ip != nil {
					ones, _ := net.IPMask(ip).Size()
					masks[idx] = ones
				}
			}
		})
		l3 := map[string]bool{}
		for ip, ifIdx := range addrIf {
			if strings.HasPrefix(ip, "127.") {
				continue
			}
			name := ""
			adminDown := false
			operDown := false
			if e, ok := ifs[ifIdx]; ok {
				name = e.display()
				adminDown = e.Admin == 2 // ifAdminStatus=down; don't treat its old ARP entries as active gateway spoofing
				operDown = e.Oper == 2   // ifOperStatus=down; the interface is not currently forwarding
			}
			var adminStatus, operStatus int64
			if e, ok := ifs[ifIdx]; ok {
				adminStatus, operStatus = e.Admin, e.Oper
			}
			out.addrs = append(out.addrs, OwnIfAddr{
				IP: ip, Prefix: masks[ip], If: name,
				AdminStatus: adminStatus, OperStatus: operStatus,
				AdminDown: adminDown, OperDown: operDown,
			})
			l3[ifIdx] = true
		}
		sort.Slice(out.addrs, func(i, j int) bool { return ipLess(out.addrs[i].IP, out.addrs[j].IP) })
		seenMAC := map[string]bool{}
		addMAC := func(v interface{}) {
			if m := formatMACValue(v); m != "" && m != "00:00:00:00:00:00" && !seenMAC[m] {
				seenMAC[m] = true
				out.ownMACs = append(out.ownMACs, m)
			}
		}
		if pdu, ok := s.get(oidDot1dBaseBridgeAddress); ok {
			addMAC(pdu.Value)
		}
		_ = s.walk(oidIfPhysAddress, func(idx string, pdu gosnmp.SnmpPDU) {
			if l3[idx] {
				addMAC(pdu.Value)
			}
		})
	}

	bridgeToIf := make(map[string]string)
	for bport, v := range s.walkInts(oidDot1dBasePortIfIndex) {
		ifIdx := strconv.FormatInt(v, 10)
		if r.ifExists[ifIdx] {
			bridgeToIf[bport] = ifIdx
		}
	}
	for bport, pvid := range s.walkInts(oidDot1qPvid) {
		if pvid <= 0 {
			continue
		}
		ifIdx := resolveBridgePortIfIndex(bport, bridgeToIf, r)
		if ifIdx == "" {
			continue
		}
		if _, hasIP := out.ip[ifIdx]; hasIP {
			continue // 三层口不再重复标 VLAN, 层级只显示一种
		}
		if _, exists := out.vlan[ifIdx]; !exists {
			out.vlan[ifIdx] = strconv.FormatInt(pvid, 10)
		}
	}

	if len(out.aggGroup) > 0 {
		out.lacp = map[string]lacpState{}
		_ = s.walk(oidDot3adAggPortActorOperState, func(idx string, pdu gosnmp.SnmpPDU) {
			if _, member := out.aggGroup[idx]; !member {
				return
			}
			if st, ok := decodeLacpState(pdu.Value); ok {
				out.lacp[idx] = st
			}
		})
	}

	out.vlans, out.vlanNames = collectPortVlans(s, bridgeToIf, r)
	out.duplex, out.dupSrc = collectDuplex(s, r, lldpLocalPortMap(s, r))

	// 聚合成员口在桥里不是独立桥端口, PVID/VLAN 位图里只有聚合口本身, 成员口继承聚合口的
	aggIdx := map[string]string{}
	for idx, e := range ifs {
		if isAggInterface(e) {
			aggIdx[e.display()] = idx
		}
	}
	for member, g := range out.aggGroup {
		a := aggIdx[g]
		if a == "" {
			continue
		}
		if _, ok := out.vlans[member]; !ok && out.vlans[a] != nil {
			cp := *out.vlans[a]
			cp.source += " (继承聚合口)"
			out.vlans[member] = &cp
		}
		if _, ok := out.vlan[member]; !ok {
			if v, ok := out.vlan[a]; ok {
				out.vlan[member] = v
			}
		}
		if _, ok := out.ip[member]; !ok {
			if v, ok := out.ip[a]; ok {
				out.ip[member] = v
			}
		}
	}
	return out
}

func collectFDB(s *snmpSession, r *indexResolver) fdbResult {
	res := fdbResult{
		macToIf:      make(map[string]string),
		macVlan:      make(map[string]string),
		macLocations: make(map[string][]string),
		portMACs:     make(map[string]int),
		portVlan:     make(map[string]string),
	}

	bridgeToIf := make(map[string]string)
	for bport, v := range s.walkInts(oidDot1dBasePortIfIndex) {
		ifIdx := strconv.FormatInt(v, 10)
		if r.ifExists[ifIdx] {
			bridgeToIf[bport] = ifIdx
		}
	}
	// PVID 兜底: 只在 dot1qTpFdb 没给出精确 VLAN 时才会用到, 见 inspectDevice。
	for bport, pvid := range s.walkInts(oidDot1qPvid) {
		if pvid <= 0 {
			continue
		}
		ifIdx := resolveBridgePortIfIndex(bport, bridgeToIf, r)
		if ifIdx != "" {
			res.portVlan[ifIdx] = strconv.FormatInt(pvid, 10)
		}
	}
	resolveBridge := func(bport string) string {
		return resolveBridgePortIfIndex(bport, bridgeToIf, r)
	}

	locSet := make(map[string]map[string]bool) // mac -> 见过的 ifIndex 集合
	rowLocSet := make(map[fdbRowKey]map[string]bool)
	legacyLocSet := make(map[string]map[string]bool)
	qMACs := make(map[string]bool)
	macVlanSet := make(map[string]map[string]bool)
	add := func(mac, vlan, ifIdx string) {
		if mac == "" || ifIdx == "" {
			return
		}
		if locSet[mac] == nil {
			locSet[mac] = make(map[string]bool)
		}
		locSet[mac][ifIdx] = true
		if vlan != "" {
			if macVlanSet[mac] == nil {
				macVlanSet[mac] = make(map[string]bool)
			}
			macVlanSet[mac][vlan] = true
			key := fdbRowKey{mac: mac, vlan: vlan}
			if rowLocSet[key] == nil {
				rowLocSet[key] = make(map[string]bool)
			}
			rowLocSet[key][ifIdx] = true
		} else {
			if legacyLocSet[mac] == nil {
				legacyLocSet[mac] = make(map[string]bool)
			}
			legacyLocSet[mac][ifIdx] = true
		}
	}

	// 1) Q-BRIDGE: 索引 = vlanId . 6 段 MAC
	q := 0
	qStatus := s.walkInts(oidDot1qTpFdbStatus)
	_ = s.walk(oidDot1qTpFdbPort, func(idx string, pdu gosnmp.SnmpPDU) {
		parts := strings.Split(idx, ".")
		if len(parts) != 7 {
			return
		}
		if st, ok := qStatus[idx]; ok && st != 3 { // 只要 learned(3)
			return
		}
		bport := strconv.FormatInt(toInt64(pdu.Value), 10)
		if bport == "0" {
			return
		}
		mac := macFromOIDDigits(parts[1:])
		if ifIdx := resolveBridge(bport); ifIdx != "" {
			add(mac, parts[0], ifIdx)
			qMACs[mac] = true
			q++
		}
	})

	// 2) 传统 BRIDGE: 索引 = 6 段 MAC
	d := 0
	dStatus := s.walkInts(oidDot1dTpFdbStatus)
	_ = s.walk(oidDot1dTpFdbPort, func(idx string, pdu gosnmp.SnmpPDU) {
		parts := strings.Split(idx, ".")
		if len(parts) != 6 {
			return
		}
		if st, ok := dStatus[idx]; ok && st != 3 {
			return
		}
		bport := strconv.FormatInt(toInt64(pdu.Value), 10)
		if bport == "0" {
			return
		}
		mac := macFromOIDDigits(parts)
		if ifIdx := resolveBridge(bport); ifIdx != "" {
			add(mac, "", ifIdx)
			d++
		}
	})

	switch {
	case q > 0 && d > 0:
		res.source = fmt.Sprintf("dot1qTpFdb(%d) + dot1dTpFdb(%d)", q, d)
	case q > 0:
		res.source = fmt.Sprintf("dot1qTpFdb(%d 条)", q)
	case d > 0:
		res.source = fmt.Sprintf("dot1dTpFdb(%d 条)", d)
	default:
		res.source = "MAC 地址表为空 (SNMP 视图可能未放通 bridge MIB)"
	}

	// 把 (mac -> 端口集合) 落定成有序切片, 并给出一个朴素的默认解
	// (按端口名排序取第一个), 好让没走 resolveFDB() 的老代码路径/测试
	// 仍然有个可用的 macToIf。inspectDevice 里拿到 LLDP 之后会调用
	// resolveFDB() 覆盖成更准确的选择(优先选接入口而不是上联口)。
	for mac, set := range locSet {
		ports := make([]string, 0, len(set))
		for p := range set {
			ports = append(ports, p)
		}
		sort.Strings(ports)
		res.macLocations[mac] = ports
		res.macToIf[mac] = ports[0]
		res.portMACs[ports[0]]++
	}
	for mac, vlans := range macVlanSet {
		if len(vlans) == 1 {
			for vlan := range vlans {
				res.macVlan[mac] = vlan
			}
		}
	}
	for mac, locations := range legacyLocSet {
		if qMACs[mac] {
			continue // Q-BRIDGE 已有精确 VLAN 行，不再用无 VLAN 的 BRIDGE 行重复计数
		}
		rowLocSet[fdbRowKey{mac: mac, vlan: "-"}] = locations
	}
	for key, set := range rowLocSet {
		locations := make([]string, 0, len(set))
		for ifIdx := range set {
			locations = append(locations, ifIdx)
		}
		sort.Strings(locations)
		res.fdbRows = append(res.fdbRows, fdbRow{MAC: key.mac, VlanID: key.vlan, Locations: locations})
	}
	sort.Slice(res.fdbRows, func(i, j int) bool {
		if res.fdbRows[i].VlanID != res.fdbRows[j].VlanID {
			return naturalLess(res.fdbRows[i].VlanID, res.fdbRows[j].VlanID)
		}
		return res.fdbRows[i].MAC < res.fdbRows[j].MAC
	})
	return res
}

type fdbRowKey struct {
	mac  string
	vlan string
}

// resolveFDB 在 LLDP 和端口角色都已知的情况下, 对每个"在多个端口都学到过"
// 的 MAC 做最终裁决: 优先选非上联口(更可能是真实接入点), 多个候选都不是
// 上联口时选学到 MAC 数最少的那个(更"专一", 更像是专属接入口); 全部候选
// 都是上联口时说明这台终端是通过冗余链路间接可见, 保留 MAC 数最多的上联口
// (最可能是当前的主用链路), 并标记为有歧义, 在 MAC 总表里如实呈现。
func resolveFDB(fdb fdbResult, ifs map[string]*ifInfo, lldp map[string]lldpNeighbor, uplinkThreshold int) (macToIf map[string]string, portMACs map[string]int, ambiguous map[string]bool) {
	macToIf = make(map[string]string, len(fdb.macLocations))
	portMACs = make(map[string]int)
	ambiguous = make(map[string]bool)

	isUplink := func(ifIdx string) bool {
		return fdb.portMACs[ifIdx] >= uplinkThreshold || lldp[ifIdx].Dev != ""
	}

	for mac, locs := range fdb.macLocations {
		if len(locs) == 1 {
			macToIf[mac] = locs[0]
			portMACs[locs[0]]++
			continue
		}
		ambiguous[mac] = true

		var access, uplink []string
		for _, l := range locs {
			if isUplink(l) {
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
		var chosen string
		if len(access) > 0 {
			chosen = pick(access, true) // 多个接入候选: 选最"专一"的
		} else {
			chosen = pick(uplink, false) // 全是上联: 选当前承载流量最大的那条
		}
		macToIf[mac] = chosen
		portMACs[chosen]++
	}
	// portMACs 需要覆盖所有在 ifs 里出现过、哪怕这一轮没有歧义 mac 落在
	// 上面的端口(0 也是有效信息), 但 map 默认零值已经等价于 0, 不用补。
	_ = ifs
	return
}

// vlanFromIfaceName 从三层 SVI 接口名里抠出 VLAN 号, 覆盖各厂商的常见写法:
// "Vlan10" / "Vlan-interface10" (H3C) / "Vlanif10" (华为) / "VLAN 10" (锐捷)。
var vlanNameRe = regexp.MustCompile(`(?i)^vlan[-_ ]?(?:interface|if)?[-_ ]?0*(\d{1,4})$`)

func vlanFromIfaceName(name string) string {
	m := vlanNameRe.FindStringSubmatch(strings.TrimSpace(name))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// resolveVlanCommunity 用 "community@vlanID" 风格的团体名(常见于要求
// per-VLAN 视图才能看到完整 MAC 表的设备)单独查一次 dot1dTpFdbTable,
// 把结果合并进主 FDB。只在配置了 vlan_community_format 且确实还有
// "未定位"记录时才会用到, 按 VLAN 去重, 不会放大成每个终端一次查询。
func resolveVlanCommunity(target DeviceTarget, cfg *Config, format, vlanID string, r *indexResolver) map[string]string {
	out := map[string]string{}
	if format == "" || vlanID == "" || !strings.Contains(format, "%s") {
		return out
	}
	vt := target
	vt.Community = fmt.Sprintf(format, target.Community, vlanID)

	s, err := dialSNMP(vt, cfg)
	if err != nil {
		return out
	}
	defer func() {
		if s.c.Conn != nil {
			_ = s.c.Conn.Close()
		}
	}()

	bridgeToIf := make(map[string]string)
	for bport, v := range s.walkInts(oidDot1dBasePortIfIndex) {
		ifIdx := strconv.FormatInt(v, 10)
		if r.ifExists[ifIdx] {
			bridgeToIf[bport] = ifIdx
		}
	}
	status := s.walkInts(oidDot1dTpFdbStatus)
	_ = s.walk(oidDot1dTpFdbPort, func(idx string, pdu gosnmp.SnmpPDU) {
		parts := strings.Split(idx, ".")
		if len(parts) != 6 {
			return
		}
		if st, ok := status[idx]; ok && st != 3 {
			return
		}
		bport := strconv.FormatInt(toInt64(pdu.Value), 10)
		ifIdx := resolveBridgePortIfIndex(bport, bridgeToIf, r)
		if ifIdx == "" {
			return
		}
		if mac := macFromOIDDigits(parts); mac != "" {
			out[mac] = ifIdx
		}
	})
	return out
}

// ----------------- ARP -----------------

type arpEntry struct {
	IP      string
	MAC     string
	IfIndex string
}

func collectARP(s *snmpSession) []arpEntry {
	seen := make(map[string]bool)
	var out []arpEntry

	push := func(ifIdx, ip, mac string) {
		if ip == "" || mac == "" || mac == "00:00:00:00:00:00" {
			return
		}
		if strings.HasPrefix(ip, "127.") || strings.HasPrefix(ip, "0.") || strings.HasSuffix(ip, ".255") {
			return
		}
		key := ip + "/" + mac
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, arpEntry{IP: ip, MAC: mac, IfIndex: ifIdx})
	}

	// ipNetToMediaPhysAddress: 索引 = ifIndex . a.b.c.d
	_ = s.walk(oidIpNetToMediaPhysAddr, func(idx string, pdu gosnmp.SnmpPDU) {
		parts := strings.Split(idx, ".")
		if len(parts) != 5 {
			return
		}
		push(parts[0], strings.Join(parts[1:], "."), formatMACValue(pdu.Value))
	})

	// ipNetToPhysicalPhysAddress: 索引 = ifIndex . addrType . len . a.b.c.d
	_ = s.walk(oidIpNetToPhysicalPhysAd, func(idx string, pdu gosnmp.SnmpPDU) {
		parts := strings.Split(idx, ".")
		if len(parts) != 7 || parts[1] != "1" || parts[2] != "4" {
			return // 只取 IPv4
		}
		push(parts[0], strings.Join(parts[3:], "."), formatMACValue(pdu.Value))
	})

	sort.Slice(out, func(i, j int) bool { return ipLess(out[i].IP, out[j].IP) })
	return out
}

func ipLess(a, b string) bool {
	ia, ib := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if ia == nil || ib == nil {
		return a < b
	}
	return bytes.Compare(ia, ib) < 0
}
