package main

// 采集: LLDP / CDP 邻居。

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// ----------------- LLDP -----------------

type lldpNeighbor struct {
	Dev    string
	Port   string
	Alt    string // 对端端口的另一种写法(PortId 和 PortDesc 里没被选作 Port 的那个), 拓扑匹配两端端口时用
	Src    string // 空 = LLDP, "CDP"
	MgmtIP string // 对端上报的管理 IP(不一定被监控), 用来"一键添加"这台邻居
	// Chassis: 对端机框 MAC(lldpRemChassisId, subtype=MAC 时)。对端没上报管理 IP 时,
	// 巡检完用全网 ARP 按这个 MAC 反查 IP(见 fillLLDPRemoteIPs)。
	Chassis string
}

// collectLLDPMgmtIP 解析 lldpRemManAddrTable。这张表比较特殊: 地址本身不是
// 某一列的值, 而是编码在 OID 索引里(…timeMark.localPortNum.remIndex.
// 地址类型.地址长度.地址字节…), 只看 IPv4(类型 1、长度 4)的, IPv6/其他
// 类型跳过。返回 key 是 "timeMark.localPortNum.remIndex", 跟 lldpRemSysName
// 等其他列用的 key 一致, 可以直接用同一个 remIdx 去查。
func collectLLDPMgmtIP(s *snmpSession) map[string]string {
	out := make(map[string]string)
	_ = s.walk(oidLldpRemManAddrIfSubtype, func(idx string, _ gosnmp.SnmpPDU) {
		if remIdx, ip, ok := parseLLDPMgmtAddrIdx(idx); ok {
			out[remIdx] = ip
		}
	})
	if len(out) == 0 {
		// 有的设备不填 IfSubtype 这一列(只填 IfId / OID 列): 走整张表, 索引前面多一段列号
		_ = s.walk(oidLldpRemManAddrEntry, func(idx string, _ gosnmp.SnmpPDU) {
			if i := strings.IndexByte(idx, '.'); i > 0 {
				if remIdx, ip, ok := parseLLDPMgmtAddrIdx(idx[i+1:]); ok {
					if _, exists := out[remIdx]; !exists {
						out[remIdx] = ip
					}
				}
			}
		})
	}
	return out
}

// collectLLDPChassis: lldpRemChassisId, subtype 4 = MAC(转成大写冒号格式), 5 = 网络地址(直接就是 IP)。
func collectLLDPChassis(s *snmpSession) (macs, ips map[string]string) {
	macs, ips = map[string]string{}, map[string]string{}
	sub := s.walkInts(oidLldpRemChassisIdSubtype)
	if len(sub) == 0 {
		return
	}
	_ = s.walk(oidLldpRemChassisId, func(idx string, pdu gosnmp.SnmpPDU) {
		switch sub[idx] {
		case 4:
			if m := formatMACValue(pdu.Value); m != "" {
				macs[idx] = m
			}
		case 5:
			// networkAddress: 第一个字节是地址族(1 = IPv4), 后面 4 字节
			if b, ok := pdu.Value.([]byte); ok && len(b) == 5 && b[0] == 1 {
				ips[idx] = strconv.Itoa(int(b[1])) + "." + strconv.Itoa(int(b[2])) + "." + strconv.Itoa(int(b[3])) + "." + strconv.Itoa(int(b[4]))
			}
		}
	})
	return
}

// parseLLDPMgmtAddrIdx 解析 lldpRemManAddrTable 一行的 OID 索引后缀:
// timeMark.localPortNum.remIndex.地址类型.地址长度.地址字节…
// 只认 IPv4(类型 1、长度 4), 其他(IPv6/MAC 等管理地址)返回 ok=false。
func parseLLDPMgmtAddrIdx(idx string) (remIdx, ip string, ok bool) {
	parts := strings.Split(idx, ".")
	if len(parts) < 9 {
		return "", "", false
	}
	subtype, addrLen := parts[3], parts[4]
	if subtype != "1" || addrLen != "4" { // 1 = IPv4
		return "", "", false
	}
	bs := make([]byte, 4)
	for i, o := range parts[5:9] {
		v, err := strconv.Atoi(o)
		if err != nil || v < 0 || v > 255 {
			return "", "", false
		}
		bs[i] = byte(v)
	}
	remIdx = strings.Join(parts[:3], ".")
	ip = strconv.Itoa(int(bs[0])) + "." + strconv.Itoa(int(bs[1])) + "." + strconv.Itoa(int(bs[2])) + "." + strconv.Itoa(int(bs[3]))
	return remIdx, ip, true
}

// CDP(Cisco Discovery Protocol)邻居表, 索引 = ifIndex.deviceIndex。
// 思科设备、以及一部分开了 CDP 兼容的华为/H3C/锐捷会填这张表; 对端没开 LLDP
// 但开了 CDP 时也能画出链路。只用来补 LLDP 没发现邻居的端口。
const (
	oidCdpCacheDeviceId   = ".1.3.6.1.4.1.9.9.23.1.2.1.1.6"
	oidCdpCacheDevicePort = ".1.3.6.1.4.1.9.9.23.1.2.1.1.7"
)

func collectCDP(s *snmpSession, r *indexResolver, into map[string]lldpNeighbor) int {
	ids := s.walkStrings(oidCdpCacheDeviceId)
	if len(ids) == 0 {
		return 0
	}
	ports := s.walkStrings(oidCdpCacheDevicePort)
	n := 0
	for idx, dev := range ids {
		ifIdx := strings.SplitN(idx, ".", 2)[0]
		if !r.ifExists[ifIdx] {
			continue
		}
		if cur, ok := into[ifIdx]; ok && cur.Dev != "" {
			continue // LLDP 已经有了
		}
		// Cisco 的 DeviceId 常带域名或序列号括号: SW1.example.com(FOC123) -> SW1
		dev = shortSysName(strings.TrimSpace(dev))
		if i := strings.IndexByte(dev, '('); i > 0 {
			dev = dev[:i]
		}
		if dev == "" {
			continue
		}
		into[ifIdx] = lldpNeighbor{Dev: dev, Port: strings.TrimSpace(ports[idx]), Src: "CDP"}
		n++
	}
	return n
}

// collectLLDP 返回 ifIndex -> 邻居。
//
// 旧版的两个致命问题:
//  1. 用 lldpLocPortIdSubtype(.3.7.1.2) 当本地端口名 —— 那一列是枚举整数(5=interfaceName),
//     所以锐捷侧 extractSlotKey("5") 永远是空, LLDP 全表落空。
//  2. 对 H3C 直接把 lldpRemLocalPortNum 当 ifIndex —— 只有部分机型成立。
//
// 现在统一走 lldpLocPortTable 建立 localPortNum -> 接口名 -> ifIndex 的映射,
// 映射不到时才退回"localPortNum 本身就是 ifIndex"的假设。
func collectLLDP(s *snmpSession, r *indexResolver) (map[string]lldpNeighbor, int, int) {
	locToIf := lldpLocalPortMap(s, r)

	remSubtype := s.walkInts(oidLldpRemPortIdSubtype)
	remSysName := s.walkStrings(oidLldpRemSysName)
	remPortDesc := s.walkStrings(oidLldpRemPortDesc)
	remPortIdRaw := make(map[string]string)
	_ = s.walk(oidLldpRemPortId, func(idx string, pdu gosnmp.SnmpPDU) {
		if remSubtype[idx] == 3 { // macAddress
			if m := formatMACValue(pdu.Value); m != "" {
				remPortIdRaw[idx] = m
				return
			}
		}
		remPortIdRaw[idx] = decodeText(pdu.Value)
	})
	mgmtIP := collectLLDPMgmtIP(s)
	chassisMAC, chassisIP := collectLLDPChassis(s)
	for k, ip := range chassisIP {
		if _, ok := mgmtIP[k]; !ok {
			mgmtIP[k] = ip
		}
	}
	out, total, unres := buildLLDPNeighbors(r, locToIf, remSysName, remPortDesc, remPortIdRaw, mgmtIP)
	if len(chassisMAC) > 0 {
		// buildLLDPNeighbors 按 ifIndex 出结果, 这里按同样的 localPortNum -> ifIndex 规则把机框 MAC 补上
		for remIdx, mac := range chassisMAC {
			parts := strings.Split(remIdx, ".")
			if len(parts) < 2 {
				continue
			}
			ifIdx := locToIf[parts[1]]
			if ifIdx == "" && r.ifExists[parts[1]] {
				ifIdx = parts[1]
			}
			if n, ok := out[ifIdx]; ok && n.Chassis == "" {
				n.Chassis = mac
				out[ifIdx] = n
			}
		}
	}
	return out, total, unres
}

// lldpLocalPortMap 把 lldpLocPortNum 换算成 ifIndex(按本地口的 PortId/PortDesc
// 名字匹配)。换算不出来的不放进结果; 调用方可以再按"localPortNum 就是
// ifIndex"(H3C/华为常见)兜底。
func lldpLocalPortMap(s *snmpSession, r *indexResolver) map[string]string {
	locName := make(map[string]string) // localPortNum -> 接口名
	subtype := s.walkInts(oidLldpLocPortIdSubtype)
	for num, v := range s.walkStrings(oidLldpLocPortId) {
		// subtype 3 = macAddress, 这种情况下 PortId 不是接口名
		if subtype[num] == 3 {
			continue
		}
		if v != "" {
			locName[num] = v
		}
	}
	for num, v := range s.walkStrings(oidLldpLocPortDesc) {
		if v == "" {
			continue
		}
		if cur, ok := locName[num]; !ok || r.byName(cur) == "" {
			locName[num] = v
		}
	}

	locToIf := make(map[string]string)
	for num, name := range locName {
		if idx := r.byName(name); idx != "" {
			locToIf[num] = idx
		}
	}
	return locToIf
}

func buildLLDPNeighbors(r *indexResolver, locToIf, remSysName, remPortDesc, remPortIdRaw, mgmtIP map[string]string) (map[string]lldpNeighbor, int, int) {
	// 把所有出现过的远端索引汇总(某些设备只填 PortId 不填 SysName)
	seen := make(map[string]bool)
	for k := range remSysName {
		seen[k] = true
	}
	for k := range remPortDesc {
		seen[k] = true
	}
	for k := range remPortIdRaw {
		seen[k] = true
	}

	out := make(map[string]lldpNeighbor)
	total, unresolved := 0, 0
	for remIdx := range seen {
		parts := strings.Split(remIdx, ".")
		if len(parts) < 2 {
			continue
		}
		locNum := parts[1] // timeMark . lldpRemLocalPortNum . lldpRemIndex
		dev := shortSysName(remSysName[remIdx])
		port, alt := pickLLDPPort(remPortIdRaw[remIdx], remPortDesc[remIdx])
		if dev == "" && port == "" {
			continue
		}
		total++

		ifIdx := locToIf[locNum]
		if ifIdx == "" && r.ifExists[locNum] {
			ifIdx = locNum // H3C / 华为 常见: localPortNum 即 ifIndex
		}
		if ifIdx == "" {
			unresolved++
			continue
		}
		if dev == "" {
			dev = "(未上报设备名)"
		}
		if port == "" {
			port = "-"
		}
		out[ifIdx] = lldpNeighbor{Dev: dev, Port: port, Alt: alt, MgmtIP: mgmtIP[remIdx]}
	}
	return out, total, unresolved
}

// lldpPortNameRe: 长得像接口名(字母前缀 + 槽位号), 比如 GigabitEthernet 0/1、XGE1/0/49、10GE1/0/1、Eth-Trunk10、eth0。
var lldpPortNameRe = regexp.MustCompile(`(?i)^\d{0,3}[a-z][a-z\-_ ]*\d+(\s*[/:.]\s*\d+)*$`)

// lldpDescSuffixRe: H3C 等默认的端口描述是 "GigabitEthernet1/0/1 Interface", 去掉后缀才是接口名。
var lldpDescSuffixRe = regexp.MustCompile(`(?i)\s+interface$`)

// pickLLDPPort 从对端上报的 PortId / PortDesc 里挑一个当"对端端口", 另一个留作备用(alt)。
//
// 旧版总是优先用 PortDesc。但 PortDesc 是对端接口的"描述": 配了 description 的口(很常见,
// 比如 "To-Core-1")上报的就是这段描述, 根本不是接口名。于是 A 看 B 的端口是 "To-Core-1",
// B 自己的端口表里叫 "GigabitEthernet0/0/1", 拓扑按端口名去重时对不上, 同一条物理链路
// 两端各报一次就画成了两根线。现在 PortId 像接口名时优先用 PortId, 两个都留着给拓扑匹配用。
func pickLLDPPort(id, desc string) (port, alt string) {
	id, desc = strings.TrimSpace(id), strings.TrimSpace(desc)
	if desc != "" && looksLikeGarbage(desc) {
		desc = ""
	}
	if id != "" && looksLikeGarbage(id) {
		id = ""
	}
	if d := lldpDescSuffixRe.ReplaceAllString(desc, ""); d != desc && lldpPortNameRe.MatchString(d) {
		desc = d
	}
	idIsName := id != "" && normalizeMACString(id) == "" && lldpPortNameRe.MatchString(id)
	switch {
	case idIsName:
		port, alt = id, desc
	case desc != "":
		port, alt = desc, id
	default:
		port = id
	}
	if strings.EqualFold(alt, port) {
		alt = ""
	}
	return port, alt
}

func looksLikeGarbage(s string) bool {
	if s == "" {
		return true
	}
	printable := 0
	for _, r := range s {
		if r >= 32 {
			printable++
		}
	}
	return printable*2 < len([]rune(s))
}
