package main

// 跨设备关联: 用全网 ARP / MAC 补全终端 IP、关联聚合口对端。

import (
	"fmt"
	"sort"
	"strings"
)

// ----------------- 跨设备 ARP/MAC 关联 -----------------
//
// 每台设备独立巡检时只能看到自己的那一份数据, 而"这个 MAC 有没有 IP"和
// "这个 MAC 真实接在哪个口"经常分别只有网络里的另外两台不同设备知道:
//
//   - 接入交换机(纯二层): MAC 表很全(被动从转发流量学到), 但通常压根
//     没有 ARP 表——它不是三层网关, 不会主动解析终端的 IP。
//   - 核心/三层网关设备: 有 ARP 表(它是终端所在 VLAN 的网关, 会解析),
//     但 MAC 表里对应这个 MAC 的端口只是"通往接入交换机的上联口",
//     不是终端真正插的那个口。
//
// 单独看任何一台设备的" IP-MAC-端口"都只有一半信息。enrichCrossDeviceHosts
// 在一轮巡检里所有设备都跑完之后, 把全网的 ARP 知识和全网的 MAC 定位
// 知识撮合到一起, 给每台设备的 mac_table 里那些"有 MAC 但本机 ARP 查
// 不到 IP"的条目, 尝试从别的设备的 ARP 结果里把 IP 借过来用, 并标注
// 清楚这个 IP 是从哪台设备关联来的, 不会假装是本机查到的。
func enrichCrossDeviceHosts(reports []DeviceReport) {
	// The reports passed here may already have been enriched (for example,
	// refreshSingleDevice starts from the current hub snapshot). Remove the old
	// derived rows and summary first so repeated enrichment rebuilds the same
	// result instead of retaining stale counts or duplicate diagnostics.
	for i := range reports {
		hosts := make([]HostMappingEntry, 0, len(reports[i].HostMappings))
		for _, h := range reports[i].HostMappings {
			if h.IPSource == "" {
				hosts = append(hosts, h)
			}
		}
		reports[i].HostMappings = hosts

		diagnostics := make([]string, 0, len(reports[i].Diagnostics))
		for _, d := range reports[i].Diagnostics {
			if strings.HasPrefix(d, "IP-MAC-端口: 另外 ") && strings.Contains(d, "条是跨设备关联出来的") {
				continue
			}
			diagnostics = append(diagnostics, d)
		}
		reports[i].Diagnostics = diagnostics
	}

	type arpSource struct {
		ip, deviceName string
	}
	globalARP := make(map[string]arpSource, 256) // mac -> 全网第一个查到的 (ip, 来源设备)
	for _, rep := range reports {
		for _, h := range rep.HostMappings {
			if h.MAC == "" || h.IP == "" || h.IPSource != "" {
				continue // 已经是关联来的就不再当"权威来源"用, 避免链式以讹传讹
			}
			if _, ok := globalARP[h.MAC]; !ok {
				globalARP[h.MAC] = arpSource{ip: h.IP, deviceName: rep.SysName}
			}
		}
	}
	if len(globalARP) == 0 {
		return
	}

	for i := range reports {
		rep := &reports[i]
		haveMAC := make(map[string]bool, len(rep.HostMappings))
		for _, h := range rep.HostMappings {
			haveMAC[h.MAC] = true
		}
		added := 0
		for _, m := range rep.MACTable {
			if haveMAC[m.MAC] {
				continue
			}
			src, ok := globalARP[m.MAC]
			if !ok || src.deviceName == rep.SysName {
				continue
			}
			vlan := m.VlanID
			if vlan == "" {
				vlan = "-"
			}
			rep.HostMappings = append(rep.HostMappings, HostMappingEntry{
				IP: src.ip, MAC: m.MAC, VlanIf: "-", VlanID: vlan, VlanExact: false,
				RealPort: m.Port, PortKind: m.PortKind, IPSource: src.deviceName,
			})
			haveMAC[m.MAC] = true
			added++
		}
		if added > 0 {
			sort.Slice(rep.HostMappings, func(a, b int) bool { return ipLess(rep.HostMappings[a].IP, rep.HostMappings[b].IP) })
			rep.Diagnostics = append(rep.Diagnostics,
				fmt.Sprintf("IP-MAC-端口: 另外 %d 条是跨设备关联出来的(本机 ARP 没有, 从其他设备的 ARP 表借用 IP), 已在列表里标出来源", added))
		}
	}
}

// detectMACFlapping: 同一个 MAC 如果同时被两台不同的设备都当成"自己接入口
// 上的终端"(不是上联/级联口, 是真正的接入口), 正常网络里不该发生——一个
// 终端同一时刻只会插在一个口上。这几乎总是二层环路(生成树没收敛/私接了
// 网线搭了个环)或者终端在两个口之间快速漂移, 是最直接的环路排查信号,
// 所以在两台设备各自的诊断里都提一句, 而不是只能去全网 MAC 总表里翻。
//
// 判定跟 MAC 总表共用 globalMACEntries: 朝向另一台被监控设备的口(聚合口、LLDP 没开但学到了
// 对方设备 MAC 的口)不算接入口, 上下层设备同时学到时算在下层, 不会再把"核心 AP6 + 接入交换机
// 的接入口"报成漂移。
func detectMACFlapping(reports []DeviceReport) {
	idx := make(map[string]int, len(reports))
	for i, r := range reports {
		idx[r.IP] = i
	}
	byMAC := map[string][]globalMACEntry{}
	for _, e := range globalMACEntries(reports) {
		if e.CrossDeviceFlap {
			byMAC[e.MAC] = append(byMAC[e.MAC], e)
		}
	}
	for mac, locs := range byMAC {
		devs := map[string]bool{}
		names := make([]string, 0, len(locs))
		for _, l := range locs {
			devs[l.DeviceIP] = true
			names = append(names, l.DeviceName+"("+l.Port+")")
		}
		sort.Strings(names)
		msg := fmt.Sprintf("MAC %s 同时被 %d 台设备的接入口学到: %s, 疑似二层环路或终端漂移, 建议查这几个口的生成树状态", mac, len(devs), strings.Join(names, ", "))
		for ip := range devs {
			i, ok := idx[ip]
			if !ok {
				continue
			}
			rep := &reports[i]
			dup := false
			for _, d := range rep.Diagnostics {
				if d == msg {
					dup = true
					break
				}
			}
			if !dup {
				rep.Diagnostics = append(rep.Diagnostics, msg)
			}
		}
	}
}

// fillLLDPRemoteIPs: 对端没在 LLDP 里上报管理 IP 时, 用它的机框 MAC 去全网 ARP 里找 IP,
// 找到了页面上就能"一键添加"这台邻居。对端已经被监控(按名字能对上)的不用找。
func fillLLDPRemoteIPs(reports []DeviceReport) {
	macIP := map[string]string{}
	for _, r := range reports {
		for _, h := range r.HostMappings {
			if h.IPSource == "" && h.MAC != "" && h.IP != "" {
				m := strings.ToUpper(h.MAC)
				if _, ok := macIP[m]; !ok {
					macIP[m] = h.IP
				}
			}
		}
	}
	if len(macIP) == 0 {
		return
	}
	for i := range reports {
		for j := range reports[i].Ports {
			p := &reports[i].Ports[j]
			if p.LLDPRemoteIP != "" || p.LLDPRemoteMAC == "" {
				continue
			}
			if ip, ok := macIP[strings.ToUpper(p.LLDPRemoteMAC)]; ok {
				p.LLDPRemoteIP, p.LLDPRemoteIPSrc = ip, "arp"
			}
		}
	}
}
