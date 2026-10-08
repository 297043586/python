package main

// 全网上下文: MAC 总表 / ARP 总表 / 二层体检共用的"这台设备是谁、这个口朝向谁、谁在下层"。
//
// 单台设备的 MAC 表只能用"MAC 数够不够多、有没有 LLDP 邻居"猜一个口是不是上联口, 常见的误判:
//   - 核心的聚合口(AggregatePort 6)下面是一台接入交换机, 但 LLDP 是在成员口上报的, 聚合口本身
//     查不到邻居; 下面那台交换机终端又不多, MAC 数没到阈值 —— 于是聚合口被当成"接入口",
//     终端看起来就像直接插在核心上, 跟接入交换机的真实接入口一起被报成"MAC 漂移";
//   - 所有设备都只在上联口见过某个 MAC 时, 以前按设备 IP 挑代表, 经常挑中核心。
//
// 这里用全网信息补上:
//   - 设备自己的 MAC(桥 MAC / 三层口 MAC / 全网 ARP 里设备管理 IP 对应的 MAC): 核心的某个口学到了
//     接入交换机自己的 MAC, 这个口就是朝向那台交换机的;
//   - LLDP 邻居(包括聚合成员口上报的, 归到聚合口上)、对端管理 IP;
//   - 设备层级: 以邻居最多的设备为根(核心)按 LLDP/MAC 推断的连接做 BFS, 越深越靠近终端。

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

type netCtx struct {
	reports []DeviceReport
	byIP    map[string]int                    // 设备管理 IP -> reports 下标
	devMAC  map[string]string                 // MAC(大写冒号) -> 设备管理 IP(被监控设备自己的 MAC)
	ownIP   map[string]string                 // IP -> 设备管理 IP(设备自己的接口地址, 含管理 IP)
	addr    map[string]map[string][]OwnIfAddr // IP -> 设备管理 IP -> 该设备所有配置此 IP 的接口
	// facing[设备IP][归一化端口] = 这个口朝向的被监控设备 IP 集合
	facing map[string]map[string]map[string]bool
	// extNbr[设备IP][归一化端口] = LLDP 看到但没被监控的邻居名
	extNbr map[string]map[string]string
	depth  map[string]int // 设备层级, 0 = 核心; 不在连通图里的没有
	root   string
	nets   []*net.IPNet // 已知网段, 掩码长的在前(subnetOf 懒加载)
}

func portNorm(p string) string {
	if k := normPortKey(p); k != "" {
		return k
	}
	return strings.ToLower(strings.TrimSpace(p))
}

func buildNetCtx(reports []DeviceReport) *netCtx {
	c := &netCtx{reports: reports, byIP: map[string]int{}, devMAC: map[string]string{}, ownIP: map[string]string{},
		addr: map[string]map[string][]OwnIfAddr{}, facing: map[string]map[string]map[string]bool{}, extNbr: map[string]map[string]string{}, depth: map[string]int{}}
	nameToIP := map[string]string{}
	for i, r := range reports {
		c.byIP[r.IP] = i
		c.ownIP[r.IP] = r.IP
		for _, ip := range r.OwnIPs {
			c.ownIP[ip] = r.IP
		}
		for _, a := range r.OwnAddrs {
			if c.addr[a.IP] == nil {
				c.addr[a.IP] = map[string][]OwnIfAddr{}
			}
			c.addr[a.IP][r.IP] = append(c.addr[a.IP][r.IP], a)
		}
		for _, m := range r.OwnMACs {
			c.devMAC[strings.ToUpper(m)] = r.IP
		}
		if n := normalizeDevName(r.SysName); n != "" {
			nameToIP[n] = r.IP
		}
	}
	// 老数据 / 没读到 ifPhysAddress 的设备: 用全网 ARP 里"设备管理 IP -> MAC"补
	for _, r := range reports {
		for _, h := range r.HostMappings {
			if h.IPSource != "" || h.MAC == "" || isVirtualGatewayMAC(h.MAC) {
				continue
			}
			if _, ok := c.byIP[h.IP]; !ok {
				continue
			}
			m := strings.ToUpper(h.MAC)
			if _, ok := c.devMAC[m]; !ok {
				c.devMAC[m] = h.IP
			}
		}
	}

	addFace := func(dev, port, peer string) {
		if port == "" || peer == "" || peer == dev {
			return
		}
		k := portNorm(port)
		if c.facing[dev] == nil {
			c.facing[dev] = map[string]map[string]bool{}
		}
		if c.facing[dev][k] == nil {
			c.facing[dev][k] = map[string]bool{}
		}
		c.facing[dev][k][peer] = true
	}
	adj := map[string]map[string]bool{}
	link := func(a, b string) {
		if a == b {
			return
		}
		if adj[a] == nil {
			adj[a] = map[string]bool{}
		}
		if adj[b] == nil {
			adj[b] = map[string]bool{}
		}
		adj[a][b], adj[b][a] = true, true
	}
	for _, r := range reports {
		for _, p := range r.Ports {
			if p.LLDPRemoteDev == "" || p.LLDPRemoteDev == "-" {
				continue
			}
			peer := nameToIP[normalizeDevName(p.LLDPRemoteDev)]
			if peer == "" && p.LLDPRemoteIP != "" {
				peer = c.ownIP[p.LLDPRemoteIP]
			}
			if peer == "" {
				if c.extNbr[r.IP] == nil {
					c.extNbr[r.IP] = map[string]string{}
				}
				c.extNbr[r.IP][portNorm(p.Name)] = p.LLDPRemoteDev
				if p.AggGroup != "" {
					c.extNbr[r.IP][portNorm(p.AggGroup)] = p.LLDPRemoteDev
				}
				continue
			}
			addFace(r.IP, p.Name, peer)
			addFace(r.IP, p.AggGroup, peer) // 成员口上报的邻居, 聚合口也朝向它
			link(r.IP, peer)
		}
	}
	// 核心的 X 口学到了设备 B 自己的 MAC: X 朝向 B(没开 LLDP 也能认出来)
	aggOf := func(r DeviceReport) map[string]string {
		m := map[string]string{}
		for _, g := range r.AggGroups {
			for _, mem := range g.Members {
				m[portNorm(mem)] = g.Name
			}
		}
		return m
	}
	macFace := map[string]map[string]bool{} // 只按 MAC 推断出的相邻关系, 用来算层级
	for _, r := range reports {
		agg := aggOf(r)
		for _, e := range r.MACTable {
			peer, ok := c.devMAC[strings.ToUpper(e.MAC)]
			if !ok || peer == r.IP {
				continue
			}
			addFace(r.IP, e.Port, peer)
			if g := agg[portNorm(e.Port)]; g != "" {
				addFace(r.IP, g, peer)
			}
			if macFace[r.IP] == nil {
				macFace[r.IP] = map[string]bool{}
			}
			macFace[r.IP][peer] = true
		}
	}
	// MAC 推断的相邻关系: A 学到 B 的 MAC 只说明 B 在 A 的某个口后面(中间可能隔着 C),
	// 只有 A、B 互相学到对方且都没有 LLDP 链路时才当成直连。
	for a, peers := range macFace {
		for b := range peers {
			if macFace[b][a] && len(adj[a]) == 0 && len(adj[b]) == 0 {
				link(a, b)
			}
		}
	}
	c.computeDepth(adj)
	return c
}

func isOwnManagementIP(nc *netCtx, dev, ip string) bool {
	if ip == dev {
		return true
	}
	for _, a := range nc.addr[ip][dev] {
		if isMgmtInterfaceName(a.If) {
			return true
		}
	}
	return false
}

// ownInterfaceDownStatus 只有该 IP 在设备上的所有接口都明确处于 down 时才返回 down。
// 只要有一个接口仍 active 或状态未知, 该地址就继续按正常冲突逻辑检查。
func ownInterfaceDownStatus(nc *netCtx, dev, ip string) (adminDown, operDown bool) {
	addrs := nc.addr[ip][dev]
	if len(addrs) == 0 {
		return false, false
	}
	allAdminDown, allDown := true, true
	for _, a := range addrs {
		if !a.AdminDown {
			allAdminDown = false
		}
		if !a.AdminDown && !a.OperDown {
			allDown = false
		}
	}
	if allAdminDown {
		return true, false
	}
	if allDown {
		return false, true
	}
	return false, false
}

// computeDepth 以连接最多的设备为根做 BFS(多个连通块各自取根)。
func (c *netCtx) computeDepth(adj map[string]map[string]bool) {
	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	// 连接多的优先当根, 一样多时 MAC 表大的(核心学到全网 MAC)优先
	macs := func(ip string) int {
		if i, ok := c.byIP[ip]; ok {
			return len(c.reports[i].MACTable)
		}
		return 0
	}
	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if len(adj[a]) != len(adj[b]) {
			return len(adj[a]) > len(adj[b])
		}
		if macs(a) != macs(b) {
			return macs(a) > macs(b)
		}
		return a < b
	})
	for _, root := range nodes {
		if _, done := c.depth[root]; done {
			continue
		}
		if c.root == "" {
			c.root = root
		}
		c.depth[root] = 0
		q := []string{root}
		for len(q) > 0 {
			n := q[0]
			q = q[1:]
			for m := range adj[n] {
				if _, ok := c.depth[m]; !ok {
					c.depth[m] = c.depth[n] + 1
					q = append(q, m)
				}
			}
		}
	}
}

// faces 返回 dev 的 port 朝向的被监控设备(没有返回 nil)。
func (c *netCtx) faces(dev, port string) map[string]bool {
	return c.facing[dev][portNorm(port)]
}

func (c *netCtx) name(ip string) string {
	if i, ok := c.byIP[ip]; ok && c.reports[i].SysName != "" {
		return c.reports[i].SysName
	}
	return ip
}

func (c *netCtx) brand(ip string) string {
	if i, ok := c.byIP[ip]; ok {
		return c.reports[i].Brand
	}
	return ""
}

func (c *netCtx) depthOf(ip string) (int, bool) {
	d, ok := c.depth[ip]
	return d, ok
}

// ownerOfMAC: 这个 MAC 是哪台被监控设备自己的(没有返回 "")。
func (c *netCtx) ownerOfMAC(mac string) string {
	return c.devMAC[strings.ToUpper(mac)]
}

// isInterconnect: 设备自己的 /30、/31 地址(点到点互联), 没读到掩码时按 false。
func (c *netCtx) isInterconnect(dev, ip string) bool {
	for _, a := range c.addr[ip][dev] {
		if a.Prefix >= 30 {
			return true
		}
	}
	return false
}

// subnetOf 返回 IP 所在的已知网段(设备接口地址 + 掩码, 最长匹配), 找不到就按 /24 近似。
func (c *netCtx) subnetOf(ip string) string {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return ip
	}
	if c.nets == nil {
		c.nets = []*net.IPNet{}
		for _, byDev := range c.addr {
			for _, addrs := range byDev {
				for _, a := range addrs {
					if a.Prefix > 0 && a.Prefix < 32 {
						if _, n, err := net.ParseCIDR(a.IP + "/" + strconv.Itoa(a.Prefix)); err == nil {
							c.nets = append(c.nets, n)
						}
					}
				}
			}
		}
		sort.Slice(c.nets, func(i, j int) bool {
			a, _ := c.nets[i].Mask.Size()
			b, _ := c.nets[j].Mask.Size()
			return a > b
		})
	}
	for _, n := range c.nets {
		if n.Contains(p) {
			return n.String()
		}
	}
	return p.Mask(net.CIDRMask(24, 32)).String() + "/24"
}
