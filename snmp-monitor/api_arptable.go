package main

// 全网 ARP 总表。
//
// 单台设备的 host_mappings 已经是"IP-MAC-端口"了, 但只是这台设备(通常是
// 网关/核心)自己解析到的那一份。汇总所有设备的 host_mappings 到一张表里,
// 一是不用再去 MAC 总表反查 IP(MAC 总表本来就已经带 IP 了, 这里反过来
// 按 IP 为主索引), 二是能在汇总时发现两类问题:
//   - 同一个 IP 被不同的 MAC 占用(IP 冲突, 或者刚刚 DHCP 换过 MAC 没过期);
//   - 同一个 MAC 名下挂了好几个 IP(有的正常——多网卡/NAT 网关, 但也是
//     ARP 攻击/中毒常见的表现: 攻击主机会用同一个 MAC 冒充很多 IP)。
import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type globalARPEntry struct {
	IP         string `json:"ip"`
	MAC        string `json:"mac"`
	VlanID     string `json:"vlan_id"`
	DeviceIP   string `json:"device_ip"` // 这条 ARP 的来源设备(网关/核心, 不一定是 IPSource 关联来源)
	DeviceName string `json:"device_name"`
	RealPort   string `json:"real_port"`
	PortKind   string `json:"port_kind"`
	IPSource   string `json:"ip_source,omitempty"` // 非空=跨设备关联来的 IP, 见 HostMappingEntry.IPSource

	IPConflict bool `json:"ip_conflict,omitempty"`  // 同一个 IP 当前被多个不同 MAC 占用
	MACMultiIP bool `json:"mac_multi_ip,omitempty"` // 同一个 MAC 在同一网段里挂了多个 IP(可疑)
	OtherMACs  int  `json:"other_macs,omitempty"`   // IPConflict 时, 除自己外还有几个 MAC
	OtherIPs   int  `json:"other_ips,omitempty"`    // 一 MAC 多 IP 时, 除自己外还挂了几个 IP

	// 一个 MAC 挂多个 IP 的三种情况, 只有第三种可疑:
	//   DeviceMAC  这是被监控设备自己的 MAC(三层交换机每个 VLANIF / 互联口一个 IP, 全是同一个 MAC)
	//   RouterMAC  没被监控, 但这些 IP 分属不同网段 —— 路由器 / 防火墙 / 三层交换机的多个接口
	//   MACMultiIP 同一网段里一个 MAC 占了多个 IP —— 代理 ARP / ARP 欺骗 / 扫描, 或者配了从地址
	DeviceMAC bool `json:"device_mac,omitempty"`
	RouterMAC bool `json:"router_mac,omitempty"`
	// OwnIP: 这个 IP 是被监控设备自己接口上的地址(网关 / 互联地址 / 管理地址)
	OwnIP bool   `json:"own_ip,omitempty"`
	Note  string `json:"note,omitempty"` // 给人看的说明: "LZC-L4 · VLAN 100 · 网关" / "互联地址 /30"
}

// globalARPEntries 汇总全网 host_mappings, 按 (IP, MAC) 去重(同一台设备
// 和跨设备关联可能重复报告同一条绑定), 再标出冲突。
func globalARPEntries(reports []DeviceReport) []globalARPEntry {
	return globalARPEntriesCtx(reports, buildNetCtx(reports))
}

func globalARPEntriesCtx(reports []DeviceReport, nc *netCtx) []globalARPEntry {
	type key struct{ ip, mac string }
	seen := make(map[key]bool, 1024)
	out := make([]globalARPEntry, 0, 1024)
	for _, rep := range reports {
		for _, h := range rep.HostMappings {
			if h.IP == "" || h.MAC == "" {
				continue
			}
			k := key{h.IP, h.MAC}
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, globalARPEntry{
				IP: h.IP, MAC: h.MAC, VlanID: h.VlanID, DeviceIP: rep.IP, DeviceName: rep.SysName,
				RealPort: h.RealPort, PortKind: h.PortKind, IPSource: h.IPSource,
			})
		}
	}

	for i := range out {
		e := &out[i]
		if owner := nc.ownerOfMAC(e.MAC); owner != "" {
			e.DeviceMAC = true
			e.Note = nc.name(owner) + " 自身"
		}
		if dev, ok := nc.ownIP[e.IP]; ok {
			e.OwnIP = true
			e.Note = describeOwnIP(nc, dev, e.IP)
		}
	}

	byIP := make(map[string][]int, len(out))
	byMAC := make(map[string][]int, len(out))
	for i, e := range out {
		byIP[e.IP] = append(byIP[e.IP], i)
		byMAC[e.MAC] = append(byMAC[e.MAC], i)
	}
	for _, idxs := range byIP {
		if len(idxs) < 2 {
			continue
		}
		macs := map[string]bool{}
		for _, i := range idxs {
			if !isVirtualGatewayMAC(out[i].MAC) { // VRRP/HSRP 虚 MAC 跟真实接口 MAC 共用网关 IP 是正常的
				macs[out[i].MAC] = true
			}
		}
		if len(macs) < 2 {
			continue // 同一个 IP 的多条只是来源设备不同, MAC 其实一样, 不算冲突
		}
		for _, i := range idxs {
			out[i].IPConflict = true
			out[i].OtherMACs = len(macs) - 1
		}
	}
	for mac, idxs := range byMAC {
		if len(idxs) < 2 || isVirtualGatewayMAC(mac) {
			continue
		}
		ips := map[string]bool{}
		subnets := map[string]int{}
		allOwn := true
		for _, i := range idxs {
			if !ips[out[i].IP] {
				ips[out[i].IP] = true
				subnets[nc.subnetOf(out[i].IP)]++
			}
			if !out[i].OwnIP {
				allOwn = false
			}
		}
		if len(ips) < 2 {
			continue
		}
		sameSubnet := false
		for _, n := range subnets {
			if n >= 2 {
				sameSubnet = true
			}
		}
		devMAC := out[idxs[0]].DeviceMAC || allOwn
		for _, i := range idxs {
			out[i].OtherIPs = len(ips) - 1
			switch {
			case devMAC:
				out[i].DeviceMAC = true
				if out[i].Note == "" {
					out[i].Note = "被监控设备的接口"
				}
			case !sameSubnet:
				out[i].RouterMAC = true
				if out[i].Note == "" {
					out[i].Note = fmt.Sprintf("同一 MAC 在 %d 个网段各有一个 IP: 路由器 / 防火墙 / 三层设备的多个接口", len(subnets))
				}
			default:
				out[i].MACMultiIP = true
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].IP != out[j].IP {
			return ipLess(out[i].IP, out[j].IP)
		}
		return out[i].MAC < out[j].MAC
	})
	return out
}

// describeOwnIP: "LZC-L4 · VLAN 100 · 网关 /24" / "LZC-L4 · VLAN 3010 · 互联地址 /30" / "LZC-L4 · 管理地址"
func describeOwnIP(nc *netCtx, dev, ip string) string {
	parts := []string{nc.name(dev)}
	addrs := nc.addr[ip][dev]
	var a OwnIfAddr
	ok := len(addrs) > 0
	if ok {
		a = addrs[0]
		var ifs []string
		seen := map[string]bool{}
		for _, candidate := range addrs {
			if candidate.If != "" && !seen[candidate.If] {
				seen[candidate.If] = true
				ifs = append(ifs, candidate.If)
			}
			if isMgmtInterfaceName(candidate.If) && !isMgmtInterfaceName(a.If) {
				a = candidate
			} else if a.Prefix == 0 && candidate.Prefix > 0 {
				a = candidate
			}
		}
		sort.Strings(ifs)
		if len(ifs) > 0 {
			parts = append(parts, strings.Join(ifs, " / "))
		}
	}
	switch {
	case isOwnManagementIP(nc, dev, ip):
		if ok && a.Prefix > 0 {
			parts = append(parts, fmt.Sprintf("管理地址 /%d", a.Prefix))
		} else {
			parts = append(parts, "管理地址")
		}
	case ok && a.Prefix == 32:
		parts = append(parts, "Loopback /32")
	case ok && a.Prefix >= 30:
		parts = append(parts, fmt.Sprintf("互联地址 /%d", a.Prefix))
	case ok && a.Prefix > 0:
		parts = append(parts, fmt.Sprintf("网关 /%d", a.Prefix))
	default:
		parts = append(parts, "接口地址")
	}
	adminDown, operDown := ownInterfaceDownStatus(nc, dev, ip)
	if adminDown {
		parts = append(parts, "接口 ADMIN-DOWN")
	} else if operDown {
		parts = append(parts, "接口 DOWN")
	}
	return strings.Join(parts, " · ")
}

// ARP 总表紧凑格式 flags 位
const (
	arpFlagIPConflict = 1
	arpFlagMultiIP    = 2
	arpFlagRelayed    = 4  // ip_source 非空: 跨设备关联出来的, 不是这台设备自己解析的
	arpFlagDeviceMAC  = 8  // 被监控设备自己的 MAC(三层交换机多个接口)
	arpFlagRouterMAC  = 16 // 多个 IP 分属不同网段: 路由器 / 防火墙的多个接口
	arpFlagOwnIP      = 32 // 被监控设备自己的接口 IP(网关 / 互联 / 管理)
)

// compactARPTable 列式输出: devices 只出现一次, 每行是
// [ip, mac, 设备下标, vlan, 端口, flags, other_count, 说明]。
func compactARPTable(list []globalARPEntry) map[string]interface{} {
	devIdx := map[string]int{}
	devices := [][2]string{}
	rows := make([][]interface{}, 0, len(list))
	for _, e := range list {
		di, ok := devIdx[e.DeviceIP]
		if !ok {
			di = len(devices)
			devIdx[e.DeviceIP] = di
			devices = append(devices, [2]string{e.DeviceIP, e.DeviceName})
		}
		flags := 0
		other := 0
		if e.IPConflict {
			flags |= arpFlagIPConflict
			other = e.OtherMACs
		}
		if e.MACMultiIP {
			flags |= arpFlagMultiIP
		}
		if e.DeviceMAC {
			flags |= arpFlagDeviceMAC
		}
		if e.RouterMAC {
			flags |= arpFlagRouterMAC
		}
		if e.OwnIP {
			flags |= arpFlagOwnIP
		}
		if e.OtherIPs > other {
			other = e.OtherIPs
		}
		if e.IPSource != "" {
			flags |= arpFlagRelayed
		}
		rows = append(rows, []interface{}{e.IP, e.MAC, di, e.VlanID, e.RealPort, flags, other, e.Note})
	}
	return map[string]interface{}{"fmt": "compact", "devices": devices, "rows": rows, "count": len(rows)}
}

func (s *server) handleGlobalARPTable(w http.ResponseWriter, r *http.Request) {
	compact := r.URL.Query().Get("fmt") == "compact"
	blob := s.hub.cached("arp:"+map[bool]string{true: "c", false: "f"}[compact], func(reports []DeviceReport) interface{} {
		list := globalARPEntries(reports)
		if compact {
			out := compactARPTable(list)
			out["check_time"] = s.hub.checkTimeLocked()
			return out
		}
		return map[string]interface{}{"entries": list, "count": len(list)}
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}
