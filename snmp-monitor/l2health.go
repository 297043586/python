package main

// 二层体检(v14)。
//
// 广播风暴 / 环路 / ARP 攻击这类问题, 单看某一台设备很难下结论: 风暴时每台交换机的上联口
// 广播都高, 环路时同一个 MAC 在两台交换机上"都像是接入终端", ARP 欺骗时是某台接入交换机下
// 的一个终端在冒充网关。这里把每轮巡检已经采到的数据按全网汇总, 挑出这些信号:
//
//   storm       端口入方向广播包/秒超阈值; 多台设备同时出现时单独给一条"大概率是环路"
//   loop        同一个 MAC 同时出现在两台不同设备的接入口上(MAC 漂移)
//   stp         生成树拓扑在两轮巡检之间发生了变化(BRIDGE-MIB dot1dStpTopChanges)
//   rogue       接入口下挂了很多 MAC, 没有 LLDP 邻居, 也没学到任何被监控设备自己的 MAC: 私接设备
//   no_lldp     同上但学到了某台被监控设备的 MAC: 是已纳管设备, 只是这条链路没开 LLDP
//   unmanaged   有 LLDP 邻居但没加入监控(可以一键添加)
//   arp_spoof   设备自己的地址(网关 / 互联 / 管理, 分开报)对应的 MAC 不是设备自己的接口 MAC,
//               或者出现在接入口上(终端冒充网关)
//   arp_multi   同一网段里一个 MAC 对应多个 IP(代理 ARP / 欺骗 / 扫描); 三层设备自己的多个接口、
//               路由器跨网段的多个接口不算
//
// 每条结论带 Steps: 针对这台设备这个口、按厂商给出的排查命令, 不是通用说明。
//   ip_conflict 同一个 IP 被多个 MAC 占用
//   flap        10 分钟内 UP/DOWN 过的端口(一台设备上多个口一起抖更可疑)
//   duplex      半双工
//   congestion  出方向丢包(拥塞)
//
// 都是启发式判断, 每条都带证据(哪台设备、哪个口、哪些 MAC), 页面上再给排查思路和命令。

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type L2Finding struct {
	Level    string   `json:"level"` // critical / warning / info
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	DeviceIP string   `json:"device_ip,omitempty"`
	Device   string   `json:"device,omitempty"`
	rank     float64  // 同类里排序用(广播包/秒、MAC 数), 不输出
	Port     string   `json:"port,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	// Steps: 针对这一条的排查步骤(具体到哪台设备、哪个口、按设备厂商给出命令), 不是通用说明
	Steps []string `json:"steps,omitempty"`
	// AddIP: 未纳管的 LLDP 邻居上报的管理 IP, 页面上给"添加设备"按钮
	AddIP   string `json:"add_ip,omitempty"`
	AddName string `json:"add_name,omitempty"`
}

var l2LevelOrder = map[string]int{"critical": 0, "warning": 1, "info": 2}

// VRRP / HSRP 虚拟 MAC: 网关 IP 对应它跟对应接口真实 MAC 都是正常的, 不算"一个网关 IP 多个 MAC"
func isVirtualGatewayMAC(mac string) bool {
	m := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(mac, "-", ":"), ".", ""))
	return strings.HasPrefix(m, "00:00:5e:00:01") || strings.HasPrefix(m, "00:00:5e:00:02") || strings.HasPrefix(m, "00:00:0c:07:ac")
}

// ---------- 按厂商给命令 ----------

// cliMAC 把 MAC 写成设备命令行认的格式: 华为/H3C xxxx-xxxx-xxxx, 锐捷/Cisco xxxx.xxxx.xxxx
func cliMAC(brand, mac string) string {
	h := strings.ToLower(macCharRe.ReplaceAllString(mac, ""))
	if len(h) != 12 {
		return mac
	}
	sep := "."
	if brand == "huawei" || brand == "h3c" {
		sep = "-"
	}
	return h[0:4] + sep + h[4:8] + sep + h[8:12]
}

// l2Cmd 返回 "设备名(厂商): 命令" 形式的一行; 不认识的厂商给华为/H3C 和锐捷/Cisco 两种写法。
func l2Cmd(nc *netCtx, dev, what, arg string) string {
	b := nc.brand(dev)
	pick := func(hw, h3c, rj, cisco string) string {
		switch b {
		case "huawei":
			return hw
		case "h3c":
			return h3c
		case "ruijie":
			return rj
		case "cisco":
			return cisco
		}
		if hw == h3c && rj == cisco {
			return hw + "   (锐捷/Cisco: " + rj + ")"
		}
		return h3c + "   (华为: " + hw + "; 锐捷: " + rj + ")"
	}
	var c string
	switch what {
	case "mac_port":
		c = pick("display mac-address interface "+arg, "display mac-address interface "+arg, "show mac-address-table interface "+arg, "show mac address-table interface "+arg)
	case "mac_find":
		c = pick("display mac-address "+cliMAC("huawei", arg), "display mac-address "+cliMAC("h3c", arg), "show mac-address-table address "+cliMAC("ruijie", arg), "show mac address-table address "+cliMAC("cisco", arg))
	case "lldp":
		c = pick("display lldp neighbor interface "+arg, "display lldp neighbor-information interface "+arg+" verbose", "show lldp neighbors interface "+arg+" detail", "show lldp neighbors "+arg+" detail")
	case "lldp_on":
		c = pick("lldp enable   (系统视图)", "lldp global enable   (系统视图)", "lldp enable   (全局配置模式)", "lldp run   (全局配置模式)")
	case "shut":
		c = pick("interface "+arg+" → shutdown", "interface "+arg+" → shutdown", "interface "+arg+" → shutdown", "interface "+arg+" → shutdown")
	case "bcast":
		c = pick("display interface "+arg+"   (看 Broadcast 计数和 Input peak rate)", "display interface "+arg+"   (看 Broadcast)", "show interfaces "+arg+" counters rate", "show interfaces "+arg+" counters")
	case "stp":
		c = pick("display stp tc-bpdu statistics   (看哪个口收到的 TC 最多)", "display stp tc   (看哪个口收到的 TC 最多)", "show spanning-tree summary + show spanning-tree interface "+firstNonEmpty(arg, "<口>"), "show spanning-tree detail | include ieee|occurr|from|exec")
	case "arp":
		c = pick("display arp | include "+arg, "display arp | include "+arg, "show arp | include "+arg, "show ip arp "+arg)
	case "log":
		c = pick("display logbuffer | include "+arg, "display logbuffer | include "+arg, "show logging | include "+arg, "show logging | include "+arg)
	case "portsec":
		c = pick("interface "+arg+" → port-security enable + port-security max-mac-num 1", "interface "+arg+" → port-security max-mac-count 1", "interface "+arg+" → switchport port-security + switchport port-security maximum 1", "interface "+arg+" → switchport port-security maximum 1")
	case "edge":
		c = pick("interface "+arg+" → stp edged-port enable; 全局 stp bpdu-protection", "interface "+arg+" → stp edged-port; 全局 stp bpdu-protection", "interface "+arg+" → spanning-tree portfast + spanning-tree bpduguard enable", "interface "+arg+" → spanning-tree portfast + spanning-tree bpduguard enable")
	case "arp_guard":
		c = pick("arp anti-attack gateway-duplicate enable; VLAN 下 dhcp snooping enable + arp anti-attack check user-bind enable", "VLAN 下 arp detection enable + dhcp snooping enable", "ip dhcp snooping + 接入口 arp-check / ip arp inspection vlan "+firstNonEmpty(arg, "X"), "ip dhcp snooping vlan "+firstNonEmpty(arg, "X")+" + ip arp inspection vlan "+firstNonEmpty(arg, "X"))
	case "duplex":
		c = pick("interface "+arg+" → undo duplex + undo speed (两端都自协商)", "interface "+arg+" → duplex auto + speed auto", "interface "+arg+" → duplex auto + speed auto", "interface "+arg+" → duplex auto + speed auto")
	case "mac_flap":
		c = pick("display mac-address flapping record", "display mac-address mac-move", "show logging | include MAC", "show logging | include MACFLAP")
	case "loop_detect":
		c = pick("interface "+arg+" → loopback-detect enable + loopback-detect action shutdown; 并开 stp edged-port + 全局 stp bpdu-protection",
			"全局 loopback-detection global enable vlan all + interface "+arg+" → loopback-detection action shutdown",
			"全局 rldp enable + interface "+arg+" → rldp port loop-detect shutdown-port",
			"interface "+arg+" → spanning-tree portfast + spanning-tree bpduguard enable; 全局 errdisable recovery cause bpduguard")
	case "drops":
		c = pick("display qos queue statistics interface "+arg, "display qos queue-statistics interface "+arg+" outbound", "show interfaces "+arg+" counters", "show interfaces "+arg+" | include drops")
	default:
		c = arg
	}
	return nc.name(dev) + ": " + c
}

// macsOnPort 列出某台设备某个口下面学到的 MAC(带 IP), 私接判断时当证据。
func macsOnPort(r DeviceReport, port string, ipOf map[string]string, n int) []string {
	key := portNorm(port)
	var out []string
	total := 0
	for _, m := range r.MACTable {
		if portNorm(m.Port) != key {
			continue
		}
		total++
		if len(out) < n {
			s := m.MAC
			if ip := ipOf[m.MAC]; ip != "" {
				s += "  " + ip
			}
			if macIsLocal(m.MAC) {
				s += "  (随机 MAC)"
			}
			out = append(out, s)
		}
	}
	if total > n {
		out = append(out, fmt.Sprintf("… 共 %d 个", total))
	}
	return out
}

func l2HealthFindings(reports []DeviceReport, cfg *Config) []L2Finding {
	nc := buildNetCtx(reports)
	var out []L2Finding
	add := func(f L2Finding) { out = append(out, f) }
	stormTh := cfg.Alerts.Broadcast.Threshold
	if stormTh <= 0 {
		stormTh = 1000
	}
	stpTh := int64(cfg.Alerts.STP.Threshold)
	if stpTh <= 0 {
		stpTh = 3
	}
	uplinkTh := cfg.UplinkMACThreshold
	if uplinkTh < 3 {
		uplinkTh = 3
	}
	ipOf := map[string]string{}
	for _, r := range reports {
		for _, h := range r.HostMappings {
			if h.MAC != "" && h.IP != "" && ipOf[h.MAC] == "" {
				ipOf[h.MAC] = h.IP
			}
		}
	}

	// ---------- 二层环路(先做: 认定的疑似环路口不再被当成级联口 / 私接口报) ----------
	macs := globalMACEntriesCtx(reports, nc)
	loops := newLoopAnalysis(reports, nc, stormTh, stpTh)
	loopFindings := loops.findings(reports, macs)
	sort.SliceStable(loopFindings, func(i, j int) bool {
		if l2LevelOrder[loopFindings[i].Level] != l2LevelOrder[loopFindings[j].Level] {
			return l2LevelOrder[loopFindings[i].Level] < l2LevelOrder[loopFindings[j].Level]
		}
		return loopFindings[i].rank > loopFindings[j].rank
	})
	out = append(out, loopFindings...)

	// ---------- 端口级: 风暴 / 抖动 / 半双工 / 拥塞 / 私接 ----------
	stormDevs := map[string][]string{}
	var stormTop []L2Finding
	var mildStorm []L2Finding
	var rogue, noLLDP, unmanaged []L2Finding
	seenUnmanaged := map[string]bool{}
	for _, r := range reports {
		if !r.IsAlive {
			continue
		}
		var flaps []string
		for _, p := range r.Ports {
			x := p.Ext
			if x != nil && p.Status == "UP" {
				switch {
				case x.InBcastPps >= stormTh:
					lvl := "warning"
					if x.InBcastPps >= stormTh*5 {
						lvl = "critical"
					}
					peer := "下面有 " + strconv.Itoa(p.MACCount) + " 个 MAC"
					if p.LLDPRemoteDev != "" && p.LLDPRemoteDev != "-" {
						peer = "对端是 " + p.LLDPRemoteDev
					}
					f := L2Finding{Level: lvl, Kind: "storm", DeviceIP: r.IP, Device: r.SysName, Port: p.Name, rank: x.InBcastPps,
						Title:  fmt.Sprintf("广播风暴: %s %s 入方向广播 %.0f 包/秒", r.SysName, p.Name, x.InBcastPps),
						Detail: fmt.Sprintf("阈值 %.0f 包/秒; 正常办公网一个口通常只有几十包/秒", stormTh),
						Steps: []string{
							"确认实时广播量(看两次, 间隔几秒): " + l2Cmd(nc, r.IP, "bcast", p.Name),
							fmt.Sprintf("这个口%s。广播是从这个口\"进来\"的, 源头在它下面", peer),
						}}
					if p.IsUplink {
						f.Steps = append(f.Steps, "这是上联/级联口, 不要直接 shutdown: 到对端设备上找广播最高的非上联口, 一级一级往下追到接入口")
					} else {
						f.Steps = append(f.Steps, "这是接入口: 先 shutdown 观察 1~2 分钟广播是否回落 —— "+l2Cmd(nc, r.IP, "shut", p.Name),
							"回落了就去现场看这个口接了什么(常见: 一根网线两头插在同一台傻瓜交换机上、无线网桥、终端中毒); 处理完开边缘口保护: "+l2Cmd(nc, r.IP, "edge", p.Name))
					}
					add(f)
					stormDevs[r.SysName] = append(stormDevs[r.SysName], fmt.Sprintf("%s(%.0f pps)", p.Name, x.InBcastPps))
					if !p.IsUplink {
						stormTop = append(stormTop, f)
					}
				case x.InBcastPps >= stormTh/4:
					mildStorm = append(mildStorm, L2Finding{Level: "info", Kind: "storm", DeviceIP: r.IP, Device: r.SysName, Port: p.Name, rank: x.InBcastPps,
						Title: fmt.Sprintf("广播偏高: %s %s %.0f 包/秒", r.SysName, p.Name, x.InBcastPps),
						Steps: []string{"持续观察, 连续几轮都偏高再查: " + l2Cmd(nc, r.IP, "bcast", p.Name)}})
				}
				if x.DeltaOutDiscrd >= 1000 {
					add(L2Finding{Level: "info", Kind: "congestion", DeviceIP: r.IP, Device: r.SysName, Port: p.Name,
						Title:  fmt.Sprintf("出方向丢包: %s %s 本轮丢了 %d 个包", r.SysName, p.Name, x.DeltaOutDiscrd),
						Detail: "出方向丢包是拥塞(队列满)的直接证据; 风暴期间上联口也常见",
						Steps: []string{
							fmt.Sprintf("看这个口的利用率(当前 入 %.0f%% / 出 %.0f%%, 速率 %dM): 设备详情 → 端口", p.InUsagePercent, p.OutUsagePercent, p.SpeedMbps),
							"看哪个队列在丢: " + l2Cmd(nc, r.IP, "drops", p.Name),
							"利用率不高却丢包 = 突发流量(微突发), 考虑扩容或调整 QoS 队列; 同时有广播风暴先处理风暴",
						}})
				}
			}
			if x != nil && x.LastChangeSec >= 0 && x.LastChangeSec < 600 {
				flaps = append(flaps, fmt.Sprintf("%s(%s %s前)", p.Name, p.Status, fmtDuration(x.LastChangeSec)))
			}
			if p.Status == "UP" && p.Duplex == "半双工" && p.SpeedMbps >= 100 {
				add(L2Finding{Level: "warning", Kind: "duplex", DeviceIP: r.IP, Device: r.SysName, Port: p.Name,
					Title:  fmt.Sprintf("半双工: %s %s (%dM)", r.SysName, p.Name, p.SpeedMbps),
					Detail: "百兆以上半双工基本都是两端一边强制一边自协商, 会有大量冲突和 CRC 错包",
					Steps: []string{
						"两端改成一致(推荐都自协商): " + l2Cmd(nc, r.IP, "duplex", p.Name),
						"对端是终端就在网卡属性里把速率双工改回\"自动协商\"; 改完看错包是否还在涨",
					}})
			}
			hasNbr := p.LLDPRemoteDev != "" && p.LLDPRemoteDev != "-"
			if p.Status != "UP" {
				continue
			}
			// 有 LLDP 邻居但没加入监控: 单独一类, 可以一键添加
			if hasNbr {
				if ext := nc.extNbr[r.IP][portNorm(p.Name)]; ext != "" {
					k := normalizeDevName(ext)
					if !seenUnmanaged[k] {
						seenUnmanaged[k] = true
						f := L2Finding{Level: "info", Kind: "unmanaged", DeviceIP: r.IP, Device: r.SysName, Port: p.Name, rank: float64(p.MACCount),
							Title:   fmt.Sprintf("未纳管设备: %s %s 下面是 %s", r.SysName, p.Name, ext),
							Detail:  "LLDP 能看到这台设备, 但它没有加入监控; 它下面的终端只能看到\"路过\", 定位不到具体接入口",
							AddName: ext, AddIP: p.LLDPRemoteIP}
						if p.LLDPRemoteIP != "" {
							f.Steps = []string{"对端上报的管理 IP 是 " + p.LLDPRemoteIP + ", 点右下角「添加到监控」", "对端要先开 SNMP 并放行本机 IP"}
						} else {
							f.Steps = []string{"对端没有上报管理 IP: " + l2Cmd(nc, r.IP, "lldp", p.Name) + " 看 Management address, 或者到对端设备上查", "在「设备管理」里手工添加"}
						}
						unmanaged = append(unmanaged, f)
					}
				}
				continue
			}
			if !p.IsUplink || p.AggGroup != "" || p.MACCount < uplinkTh || loops.isSuspect(r.IP, p.Name) {
				continue
			}
			// 没有 LLDP, 但这个口学到了某台被监控设备自己的 MAC: 是已纳管设备, 只是链路没开 LLDP
			if peers := nc.faces(r.IP, p.Name); len(peers) > 0 {
				var names []string
				var steps []string
				steps = append(steps, l2Cmd(nc, r.IP, "lldp_on", ""))
				for ip := range peers {
					names = append(names, nc.name(ip))
					steps = append(steps, l2Cmd(nc, ip, "lldp_on", ""))
				}
				sort.Strings(names)
				noLLDP = append(noLLDP, L2Finding{Level: "info", Kind: "no_lldp", DeviceIP: r.IP, Device: r.SysName, Port: p.Name, rank: float64(p.MACCount),
					Title:  fmt.Sprintf("没开 LLDP: %s %s 下面是已纳管的 %s", r.SysName, p.Name, strings.Join(names, "、")),
					Detail: "这个口学到了对方设备自己的 MAC, 不是私接; 两端开了 LLDP 后拓扑和终端定位会更准",
					Steps:  append([]string{"两端都开 LLDP(开了之后下一轮巡检就能看到邻居):"}, steps...)})
				continue
			}
			rogue = append(rogue, L2Finding{Level: "info", Kind: "rogue", DeviceIP: r.IP, Device: r.SysName, Port: p.Name, rank: float64(p.MACCount),
				Title:    fmt.Sprintf("疑似私接交换机: %s %s 下面有 %d 个 MAC, 没有 LLDP 邻居, 也不是已纳管设备", r.SysName, p.Name, p.MACCount),
				Detail:   "可能是傻瓜交换机 / 无线路由器 / HUB, 也可能是没开 LLDP 的正规设备、带很多虚拟机的服务器",
				Evidence: macsOnPort(r, p.Name, ipOf, 10),
				Steps: []string{
					"看这个口下面都是谁(证据里是前 10 个): " + l2Cmd(nc, r.IP, "mac_port", p.Name),
					"IP 都在同一网段、MAC 厂商杂 → 下面是傻瓜交换机 / 无线 AP; 只有一个 IP 但很多 MAC → 服务器上的虚拟机",
					"是正规交换机: 开 LLDP / 加入监控; 不该有: 限制 MAC 数 —— " + l2Cmd(nc, r.IP, "portsec", p.Name),
				}})
		}
		if len(flaps) >= 3 {
			add(L2Finding{Level: "warning", Kind: "flap", DeviceIP: r.IP, Device: r.SysName,
				Title:    fmt.Sprintf("端口抖动: %s 有 %d 个口 10 分钟内 UP/DOWN 过", r.SysName, len(flaps)),
				Detail:   "多个口同时抖动常见于环路引发的风暴、上游断电 / 重启, 或者 PoE 供电不足",
				Evidence: firstN(flaps, 20),
				Steps: []string{
					"看日志里是不是同一时刻一起 DOWN 的: " + l2Cmd(nc, r.IP, "log", "DOWN"),
					"同一时刻一起抖 → 查供电 / 上游; 有广播风暴 → 先按风暴那条处理",
					"PoE 口一起抖 → 看 PoE 总功率是不是超了",
				}})
		} else {
			for _, f := range flaps {
				port := strings.SplitN(f, "(", 2)[0]
				add(L2Finding{Level: "info", Kind: "flap", DeviceIP: r.IP, Device: r.SysName, Port: port, Title: "端口最近 UP/DOWN 过: " + r.SysName + " " + f,
					Steps: []string{l2Cmd(nc, r.IP, "log", port), "反复抖就查网线 / 水晶头 / 光模块, 看错包和收光"}})
			}
		}
		if s := r.STP; s != nil && s.Delta > 0 {
			lvl := "info"
			if s.Delta >= stpTh*5 {
				lvl = "critical"
			} else if s.Delta >= stpTh {
				lvl = "warning"
			}
			add(L2Finding{Level: lvl, Kind: "stp", DeviceIP: r.IP, Device: r.SysName,
				Title:  fmt.Sprintf("生成树变化: %s 上一轮之后拓扑变化了 %d 次(最近一次 %s前)", r.SysName, s.Delta, fmtDuration(s.SinceChangeSec)),
				Detail: fmt.Sprintf("设备启动以来累计 %d 次", s.TopChanges),
				Steps: []string{
					"找出是哪个口收到的拓扑变化: " + l2Cmd(nc, r.IP, "stp", ""),
					"收到 TC 最多的口如果是上联口, 到对端设备上重复这一步, 一直追到源头",
					"源头是接入口(终端开关机 / 网线松动): 开边缘口, 终端上下线就不会再触发 TC —— " + l2Cmd(nc, r.IP, "edge", "<源头口>"),
				}})
		}
	}
	if len(stormDevs) >= 2 {
		var ev []string
		for d, ports := range stormDevs {
			ev = append(ev, d+": "+strings.Join(firstN(ports, 6), ", "))
		}
		sort.Strings(ev)
		sort.Slice(stormTop, func(i, j int) bool { return stormTop[i].rank > stormTop[j].rank })
		steps := []string{}
		// MAC 漂移已经定位到的环路口最直接, 排在风暴最高的接入口前面
		seenLoop := map[string]bool{}
		for _, f := range loopFindings {
			if f.Port == "" || f.Level == "info" || seenLoop[f.DeviceIP+" "+f.Port] || len(seenLoop) >= 3 {
				continue
			}
			seenLoop[f.DeviceIP+" "+f.Port] = true
			steps = append(steps, fmt.Sprintf("第 %d 个查 %s %s(MAC 漂移定位到的疑似环路口): %s", len(steps)+1, f.Device, f.Port, l2Cmd(nc, f.DeviceIP, "shut", f.Port)))
		}
		for _, f := range stormTop {
			if len(steps) >= 4 {
				break
			}
			if seenLoop[f.DeviceIP+" "+f.Port] {
				continue
			}
			steps = append(steps, fmt.Sprintf("第 %d 个查 %s %s(%.0f 包/秒, 接入口): %s", len(steps)+1, f.Device, f.Port, f.rank, l2Cmd(nc, f.DeviceIP, "shut", f.Port)))
		}
		if len(steps) == 0 {
			steps = append(steps, "风暴全在上联口上: 从层级最深的那台设备开始, 找它广播最高的非上联口")
		}
		steps = append(steps, "每关一个口等 1~2 分钟看其他设备的广播是否一起回落; 回落了源头就在这个口下面")
		add(L2Finding{Level: "critical", Kind: "storm", Title: fmt.Sprintf("%d 台设备同时出现广播风暴, 大概率是二层环路", len(stormDevs)),
			Detail: "环路时广播在环里无限循环, 沿途所有交换机的相关端口都会一起飙高", Evidence: ev, Steps: steps})
	}
	sort.Slice(mildStorm, func(i, j int) bool { return mildStorm[i].rank > mildStorm[j].rank })
	out = append(out, firstNF(mildStorm, 10)...)
	for _, list := range [][]L2Finding{rogue, noLLDP, unmanaged} {
		sort.Slice(list, func(i, j int) bool { return list[i].rank > list[j].rank })
		out = append(out, firstNF(list, 30)...)
	}

	// ---------- 接入口上的 MAC(ARP 判断用) ----------
	type loc struct{ dev, name, port string }
	accessMAC := map[string]loc{} // mac -> 真实接入口
	for _, e := range macs {
		if !e.IsUplink {
			if _, ok := accessMAC[e.MAC]; !ok {
				accessMAC[e.MAC] = loc{e.DeviceIP, e.DeviceName, e.Port}
			}
		}
	}

	// ---------- ARP: 冒充网关 / 互联地址异常 / 一 MAC 多 IP / IP 冲突 ----------
	ownerMACs := map[string]map[string]bool{} // 设备 IP -> 自己的 MAC
	for mac, dev := range nc.devMAC {
		if ownerMACs[dev] == nil {
			ownerMACs[dev] = map[string]bool{}
		}
		ownerMACs[dev][mac] = true
	}
	gwMACs := map[string]map[string][]string{} // 设备自己的 IP -> MAC -> 哪些设备的 ARP 表里看到
	for _, r := range reports {
		for _, h := range r.HostMappings {
			if _, ok := nc.ownIP[h.IP]; !ok || h.MAC == "" || h.IPSource != "" {
				continue
			}
			if gwMACs[h.IP] == nil {
				gwMACs[h.IP] = map[string][]string{}
			}
			gwMACs[h.IP][h.MAC] = append(gwMACs[h.IP][h.MAC], r.SysName)
		}
	}
	for ip, m := range gwMACs {
		owner := nc.ownIP[ip]
		own := ownerMACs[owner]
		what := describeOwnIP(nc, owner, ip)
		inter := nc.isInterconnect(owner, ip)
		management := isOwnManagementIP(nc, owner, ip)
		adminDown, operDown := ownInterfaceDownStatus(nc, owner, ip)
		kindName := "网关"
		if adminDown {
			kindName = "ADMIN-DOWN 接口"
		} else if management && operDown {
			kindName = "DOWN 管理地址"
		} else if management {
			kindName = "管理地址"
		} else if inter && operDown {
			kindName = "DOWN 互联地址"
		} else if inter {
			kindName = "互联地址"
		} else if operDown {
			kindName = "DOWN 接口"
		}
		var foreign []string // 不是设备自己、也不是 VRRP 虚 MAC 的
		var ev []string
		for mac, seen := range m {
			line := mac + " (在 " + strings.Join(seen, "、") + " 的 ARP 表里)"
			switch {
			case isVirtualGatewayMAC(mac):
				line += " ← VRRP/HSRP 虚 MAC, 正常"
			case own[strings.ToUpper(mac)]:
				line += " ← " + nc.name(owner) + " 自己的 MAC, 正常"
			default:
				if o := nc.ownerOfMAC(mac); o != "" {
					line += " ← 是另一台被监控设备 " + nc.name(o) + " 的 MAC"
				}
				foreign = append(foreign, mac)
			}
			ev = append(ev, line)
		}
		sort.Strings(ev)
		for _, mac := range foreign {
			l, onAccess := accessMAC[mac]
			if !onAccess || nc.ownerOfMAC(mac) != "" {
				continue
			}
			lvl := "critical"
			title := fmt.Sprintf("终端冒充网关: %s(%s)对应的 MAC %s 接在接入口 %s %s", ip, what, mac, l.name, l.port)
			detail := "网关地址被一个接入终端应答了 —— ARP 欺骗 / 中毒的典型特征, 或者有人把终端 IP 配成了网关地址"
			if adminDown {
				lvl = "warning"
				title = fmt.Sprintf("停用接口地址出现在接入口 ARP: %s(%s)对应的 MAC %s 接在 %s %s", ip, what, mac, l.name, l.port)
				detail = "地址所在接口当前是 ADMIN-DOWN, 设备不会从这个接口正常应答 ARP。接入口看到的 MAC 可能是缓存残留、地址复用或 IP 冲突; 先确认地址和接口状态, 不能仅凭此认定为 ARP 欺骗"
			} else if operDown {
				lvl = "info"
				title = fmt.Sprintf("DOWN 接口地址仍出现在接入口 ARP: %s(%s)对应的 MAC %s 接在 %s %s", ip, what, mac, l.name, l.port)
				detail = "所有配置这个地址的本机接口当前都处于 DOWN, 接入口看到的 MAC 可能是旧 ARP 缓存、地址复用或 IP 冲突; 不能仅凭此认定为 ARP 欺骗"
			} else if management {
				lvl = "warning"
				title = fmt.Sprintf("管理地址冲突: %s(%s)对应的 MAC %s 接在接入口 %s %s", ip, what, mac, l.name, l.port)
				detail = "管理接口地址被另一台设备使用, 可能是交换机默认 / 备用地址冲突、静态地址重复或 ARP 欺骗; 先确认这个管理地址是否仍在使用"
			} else if inter {
				lvl = "warning"
				title = fmt.Sprintf("互联地址被接入口终端应答: %s(%s)对应的 MAC %s 在 %s %s", ip, what, mac, l.name, l.port)
				detail = "点到点互联地址一般不会出现在接入口; 常见于对端换了设备 / 防火墙双机切换后 MAC 变了, 或者有终端配了这个地址"
			}
			steps := []string{
				"确认这个 MAC 的位置: " + l2Cmd(nc, l.dev, "mac_find", mac),
			}
			if adminDown || operDown || management {
				steps = append(steps, "先确认 "+what+" 是否仍计划使用、是否为备用 / 默认管理地址; 查清地址归属后再决定是否隔离接入口")
			} else {
				steps = append(steps, "先隔离: "+l2Cmd(nc, l.dev, "shut", l.port)+"; 再去现场找这台终端(查 IP 配置 / 查毒)")
			}
			steps = append(steps, "在 "+nc.name(owner)+" 上看 "+ip+" 现在对应谁: "+l2Cmd(nc, owner, "arp", ip))
			if !adminDown && !operDown && !management {
				steps = append(steps, "根治: "+l2Cmd(nc, l.dev, "arp_guard", ""))
			}
			add(L2Finding{Level: lvl, Kind: "arp_spoof", DeviceIP: l.dev, Device: l.name, Port: l.port, Title: title, Detail: detail, Evidence: ev, Steps: steps})
		}
		if len(foreign) == 0 {
			continue
		}
		ownSeen := 0
		for mac := range m {
			if own[strings.ToUpper(mac)] {
				ownSeen = 1
			}
		}
		distinct := len(foreign) + ownSeen
		var lvl, detail string
		switch {
		case adminDown:
			lvl, detail = "info", "这个地址所在接口当前是 ADMIN-DOWN, 设备不会从该接口正常应答 ARP。其他 MAC 可能来自 ARP 缓存残留、地址复用或 IP 冲突; 请先确认接口和地址是否仍在使用, 不能仅凭此判断为 ARP 欺骗"
		case operDown:
			lvl, detail = "info", "所有配置这个地址的本机接口当前都处于 DOWN。其他设备 ARP 表里的 MAC 可能来自旧缓存、地址复用或 IP 冲突; 请先确认接口和地址是否仍在使用, 不能仅凭此判断为 ARP 欺骗"
		case len(own) == 0:
			// 老数据没有设备自己的接口 MAC: 只有出现两个以上物理 MAC 才提一句, 分不清谁是真的
			if distinct < 2 {
				continue
			}
			lvl, detail = "info", "没采到 "+nc.name(owner)+" 自己的接口 MAC(升级后下一轮巡检会采), 暂时分不清哪个是设备自己的; 两个都是物理 MAC 时也可能是堆叠 / 双机 / 换过板卡"
		case inter:
			lvl, detail = "warning", "互联地址在对端 ARP 里对应的 MAC 不是 "+nc.name(owner)+" 的接口 MAC: 对端 ARP 没老化(刚换过设备 / 板卡), 或者这个互联地址在别处也配了"
		case management:
			lvl, detail = "warning", "管理地址对应的 MAC 不是设备自己的: 有别的设备配了同一个管理 IP, 或者 ARP 欺骗"
		default:
			lvl, detail = "critical", "网关地址对应的 MAC 不是 "+nc.name(owner)+" 自己的接口 MAC: 有人在发伪造的 ARP 应答(ARP 欺骗 / 中毒), 受害终端会把流量发给攻击者"
		}
		steps := []string{"在 " + nc.name(owner) + " 上看这个地址的 ARP / 接口 MAC: " + l2Cmd(nc, owner, "arp", ip)}
		if adminDown || operDown {
			state := "DOWN"
			if adminDown {
				state = "ADMIN-DOWN"
			}
			steps = append(steps, "接口处于 "+state+": 确认是否是已停用 / 备用接口, 再检查其他设备上的 ARP 是否只是缓存残留")
		}
		for _, mac := range foreign {
			steps = append(steps, "找 "+mac+" 在哪: 到「🗂️ MAC 总表」搜这个 MAC, 或 "+l2Cmd(nc, nc.root, "mac_find", mac))
		}
		if adminDown || operDown {
			steps = append(steps, "若接口确实已停用且没有业务复用该地址, 清除观察设备上的旧 ARP 后再看下一轮是否仍出现")
		} else if inter {
			steps = append(steps, "对端刚换过设备: 在对端清一下这条 ARP 即可; 否则查是谁配了这个互联地址")
		} else if !management {
			steps = append(steps, "根治: "+l2Cmd(nc, owner, "arp_guard", ""))
		}
		add(L2Finding{Level: lvl, Kind: "arp_spoof", DeviceIP: owner, Device: nc.name(owner),
			Title:  fmt.Sprintf("%s %s(%s)对应了 %d 个不同的 MAC", kindName, ip, what, distinct),
			Detail: detail, Evidence: ev, Steps: steps})
	}

	arps := globalARPEntriesCtx(reports, nc)
	multi := map[string][]string{}
	conflicts := map[string][]string{}
	routers := map[string]int{}
	devMACs := map[string]int{}
	for _, e := range arps {
		switch {
		case e.MACMultiIP:
			multi[e.MAC] = append(multi[e.MAC], e.IP)
		case e.RouterMAC:
			routers[e.MAC]++
		case e.DeviceMAC && e.OtherIPs > 0:
			devMACs[e.MAC]++
		}
		if e.IPConflict {
			where := e.DeviceName + " " + e.RealPort
			if e.LocDeviceIP != "" {
				where = e.LocDeviceName + " " + e.LocPort
			}
			conflicts[e.IP] = append(conflicts[e.IP], e.MAC+" @ "+where)
		}
	}
	for mac, ips := range multi {
		l, onAccess := accessMAC[mac]
		if len(ips) < 2 || isVirtualGatewayMAC(mac) {
			continue
		}
		sort.Slice(ips, func(i, j int) bool { return ipLess(ips[i], ips[j]) })
		lvl, where := "info", "位置未定位到接入口"
		if onAccess {
			where = "接在 " + l.name + " " + l.port
			if len(ips) >= 5 {
				lvl = "warning"
			}
		}
		f := L2Finding{Level: lvl, Kind: "arp_multi", Title: fmt.Sprintf("同网段一个 MAC 占了 %d 个 IP: %s(%s)", len(ips), mac, where),
			Detail:   "已经排除了被监控设备自己的接口和跨网段的路由器接口。同一网段里一个 MAC 对应多个 IP: 私接路由器开了代理 ARP、终端配了多个 IP, 或者 ARP 欺骗 / 扫描",
			Evidence: firstN(ips, 20)}
		if onAccess {
			f.DeviceIP, f.Device, f.Port = l.dev, l.name, l.port
			f.Steps = []string{
				"确认位置: " + l2Cmd(nc, l.dev, "mac_find", mac),
				"看这个口下面是不是还有别的 MAC(是的话下面是路由器 / 交换机): " + l2Cmd(nc, l.dev, "mac_port", l.port),
				"2~3 个 IP 且固定不变: 大多是终端配了多个地址或虚拟机桥接, 确认后可忽略; IP 很多且在增加: 先隔离 —— " + l2Cmd(nc, l.dev, "shut", l.port),
			}
		} else {
			f.Steps = []string{"到「🗂️ MAC 总表」搜 " + mac + " 看它在哪台设备哪个口", "查到接入口后看这个口下面是不是私接了路由器"}
		}
		add(f)
	}
	if len(routers)+len(devMACs) > 0 {
		add(L2Finding{Level: "info", Kind: "arp_multi", Title: fmt.Sprintf("已排除: %d 个 MAC 是被监控设备自己的多个接口, %d 个 MAC 是路由器 / 三层设备跨网段的多个接口", len(devMACs), len(routers)),
			Detail: "三层交换机每个 VLANIF / 互联口都是同一个 MAC; 路由器 / 防火墙在每个网段各有一个 IP —— 这些不算异常, 到「🌐 ARP 总表」按类型筛选可以看到明细"})
	}
	var cips []string
	for ip := range conflicts {
		cips = append(cips, ip)
	}
	sort.Slice(cips, func(i, j int) bool { return ipLess(cips[i], cips[j]) })
	for i, ip := range cips {
		if i >= 30 {
			add(L2Finding{Level: "warning", Kind: "ip_conflict", Title: fmt.Sprintf("还有 %d 个 IP 冲突没有列出来, 到「🌐 ARP 总表」里按「IP 冲突」筛选查看全部", len(cips)-30)})
			break
		}
		f := L2Finding{Level: "warning", Kind: "ip_conflict", Title: fmt.Sprintf("IP 冲突: %s 被 %d 个 MAC 占用", ip, len(conflicts[ip])),
			Evidence: firstN(conflicts[ip], 8)}
		if dev, ok := nc.ownIP[ip]; ok {
			f.Title += " (这是 " + describeOwnIP(nc, dev, ip) + ")"
		}
		for _, c := range conflicts[ip] {
			mac := strings.SplitN(c, " @ ", 2)[0]
			if l, ok := accessMAC[mac]; ok {
				f.Steps = append(f.Steps, mac+" 接在 "+l.name+" "+l.port+": "+l2Cmd(nc, l.dev, "mac_find", mac))
			} else {
				f.Steps = append(f.Steps, mac+" 没定位到接入口, 到「🗂️ MAC 总表」搜它")
			}
		}
		f.Steps = append(f.Steps, "两台里通常有一台是手工配的静态 IP 跟 DHCP 地址池重叠: 改掉静态那台, 或在 DHCP 里把这个地址排除 / 做保留")
		add(f)
	}

	sort.SliceStable(out, func(i, j int) bool { return l2LevelOrder[out[i].Level] < l2LevelOrder[out[j].Level] })
	return out
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string(nil), s[:n]...), fmt.Sprintf("… 共 %d 条", len(s)))
	}
	return s
}

func firstNF(s []L2Finding, n int) []L2Finding {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *server) handleL2Health(w http.ResponseWriter, r *http.Request) {
	cfg := s.conf()
	blob := s.hub.cached("l2health", func(reports []DeviceReport) interface{} {
		f := l2HealthFindings(reports, cfg)
		counts := map[string]int{}
		levels := map[string]int{}
		for _, x := range f {
			counts[x.Kind]++
			levels[x.Level]++
		}
		stp := 0
		for _, rep := range reports {
			if rep.STP != nil {
				stp++
			}
		}
		return map[string]interface{}{"findings": f, "counts": counts, "levels": levels, "devices": len(reports),
			"stp_devices": stp, "storm_threshold": cfg.Alerts.Broadcast.Threshold, "check_time": s.hub.checkTimeLocked()}
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}
