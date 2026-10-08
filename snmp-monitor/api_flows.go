package main

// NetFlow / sFlow 流量分析接口。

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// flowTalker 是按五元组(去掉端口方向噪音, 只留协议+目的端口)聚合出的
// 一条会话统计, 用于"最近流量里谁在跟谁通信、走了多少流量"这种排障视图。
type flowTalker struct {
	SrcIP    string `json:"src_ip"`
	DstIP    string `json:"dst_ip"`
	DstPort  int    `json:"dst_port"`
	Protocol string `json:"protocol"`
	Bytes    uint64 `json:"bytes"`
	Packets  uint64 `json:"packets"`
	Flows    int    `json:"flows"`
	App      string `json:"app,omitempty"`
}

// flowProtoStat: 协议分布(含 ARP/STP/LLDP 等二层协议)。
type flowProtoStat struct {
	Name  string `json:"name"`
	L2    bool   `json:"l2"`
	Bytes uint64 `json:"bytes"`
	Flows int    `json:"flows"`
}

// flowAppStat: 应用 Top(域名归属/负载特征/知名端口识别结果)。
type flowAppStat struct {
	App     string `json:"app"`
	Cat     string `json:"cat"`
	Bytes   uint64 `json:"bytes"`
	Flows   int    `json:"flows"`
	Clients int    `json:"clients"`
	clients map[string]bool
}

type flowAppCatStat struct {
	Cat   string `json:"cat"`
	Bytes uint64 `json:"bytes"`
	Flows int    `json:"flows"`
}

// flowL2Stats: 二层控制报文。ARP 估算速率高、某个 MAC 狂发 ARP 是 ARP 风暴/
// 扫描/中毒的信号; 接入口上收到 STP BPDU 说明下面私接了交换机(环路隐患)。
type flowL2Stats struct {
	Total      int            `json:"total"`
	Counts     map[string]int `json:"counts"`
	ArpPackets uint64         `json:"arp_packets"`
	ArpPps     float64        `json:"arp_pps"`
	ArpTop     []flowMacStat  `json:"arp_top"`
	BpduIfs    []flowBpduIf   `json:"bpdu_ifs"`
}

type flowMacStat struct {
	MAC     string `json:"mac"`
	Packets uint64 `json:"packets"`
	Samples int    `json:"samples"`
}

type flowBpduIf struct {
	DeviceIP   string `json:"device_ip"`
	DeviceName string `json:"device_name"`
	IfIndex    string `json:"if_index"`
	IfName     string `json:"if_name"`
	SrcMAC     string `json:"src_mac"`
	Samples    int    `json:"samples"`
	Suspicious bool   `json:"suspicious"`
}

// flowIPStat 是"Top 源 IP"/"Top 目的 IP"用的按单 IP 聚合统计——跟
// flowTalker(按完整五元组)不是一回事: 一个源 IP 可能同时打开几十条会话,
// 单条会话流量都不大, 但汇总起来它就是全网最能"占带宽"的那个, 只看
// 单条会话 Top 看不出这个。
type flowIPStat struct {
	IP        string `json:"ip"`
	Bytes     uint64 `json:"bytes"`
	Packets   uint64 `json:"packets"`
	Flows     int    `json:"flows"`
	IsPrivate bool   `json:"is_private"`
}

// flowPortBucket 是协议/端口分布饼图的一条数据(见 classifyPortBucket)。
type flowPortBucket struct {
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
	Flows int    `json:"flows"`
}

// flowDirStat 是"公网/私网"四象限的流量构成——不是为了监控本身需要,
// 是为了回答用户"这些流量是不是不全"的疑问: 大多数交换机的 NetFlow/sFlow
// 只在报文经过三层路由(SVI/网关)时才会被记账, 同一 VLAN 内两台私网
// 主机之间纯二层转发的流量天然不会出现在这里——这是协议本身的行为,
// 不是这边接收端漏采集了。把四象限的实际占比摆出来, 用户能自己判断
// "私网到私网确实是 0" 到底是符合预期还是该去查交换机配置。
type flowDirStat struct {
	Label string `json:"label"`
	Bytes uint64 `json:"bytes"`
	Flows int    `json:"flows"`
}

// flowDomainStat 是 L7 域名统计(DNS 查询名 / TLS SNI / HTTP Host)。
type flowDomainStat struct {
	Host    string `json:"host"`
	Proto   string `json:"proto"`
	Bytes   uint64 `json:"bytes"`
	Flows   int    `json:"flows"`
	Clients int    `json:"clients"` // 有多少个不同的源 IP 访问过
	clients map[string]bool
}

type flowVlanStat struct {
	VLAN  int    `json:"vlan"`
	Bytes uint64 `json:"bytes"`
	Flows int    `json:"flows"`
}

// flowMatrixCell 是网段间流量矩阵的一格: 源网段 -> 目的网段(IPv4 按 /24,
// IPv6 按 /64 归并)。比单 IP 的 Top-N 更能看出"哪两个业务区之间在大量互访"。
type flowMatrixCell struct {
	SrcNet string `json:"src_net"`
	DstNet string `json:"dst_net"`
	Bytes  uint64 `json:"bytes"`
	Flows  int    `json:"flows"`
	Kind   string `json:"kind"` // 东西向 / 南北向
}

// flowSynSuspect: 同一个源对很多不同目标发纯 SYN(只有 SYN 没有 ACK),
// 是端口扫描/SYN Flood 的典型特征。只统计 TCP。
type flowSynSuspect struct {
	SrcIP   string   `json:"src_ip"`
	SynOnly int      `json:"syn_only"` // 纯 SYN 样本数
	Targets int      `json:"targets"`  // 不同 目的IP:端口 的个数
	Sample  []string `json:"sample"`   // 举几个目标
	targets map[string]bool
}

type flowTCPStats struct {
	SynOnly int `json:"syn_only"`
	SynAck  int `json:"syn_ack"`
	Rst     int `json:"rst"`
	Fin     int `json:"fin"`
	Total   int `json:"total"`
}

type flowL7Stats struct {
	DNS      int `json:"dns"`
	TLS      int `json:"tls"`
	TLSNoSNI int `json:"tls_no_sni"` // 认出是 ClientHello 但 SNI 被截断
	HTTP     int `json:"http"`
}

type flowTimelinePoint struct {
	TS   int64   `json:"ts"` // Unix 秒
	Mbps float64 `json:"mbps"`
	Pps  float64 `json:"pps"`
}

type flowIfCounter struct {
	IfCounterStat
	DeviceIP   string `json:"device_ip"`
	DeviceName string `json:"device_name"`
}

// flowCongestion 把"接口计数器显示在丢包/错包/接近跑满"跟"同一时间窗口里
// 经过这个接口的抽样流量"关联起来——回答的是"这个口为什么丢包、是谁打满的"。
type flowCongestion struct {
	DeviceIP   string       `json:"device_ip"`
	DeviceName string       `json:"device_name"`
	IfIndex    string       `json:"if_index"`
	IfName     string       `json:"if_name"`
	Reason     string       `json:"reason"`
	Talkers    []flowTalker `json:"talkers"`
}

type flowDeviceCoverage struct {
	DeviceIP          string `json:"device_ip"`
	DeviceName        string `json:"device_name"`
	FlowSamples       int    `json:"flow_samples"`
	FlowBytes         uint64 `json:"flow_bytes"`
	SFlowSamples      int    `json:"sflow_samples"`
	NetFlowSamples    int    `json:"netflow_samples"`
	LastFlowAt        string `json:"last_flow_at,omitempty"`
	CounterInterfaces int    `json:"counter_interfaces"`
	LastCounterAt     string `json:"last_counter_at,omitempty"`
	FlowRecent        bool   `json:"flow_recent"`
	CounterRecent     bool   `json:"counter_recent"`
	HasRecentData     bool   `json:"has_recent_data"`
}

var flowWindows = map[int64]bool{60: true, 300: true, 900: true, 1800: true, 0: true}

// subnetOf 把 IP 归到 /24(IPv4) 或 /64(IPv6)。
func subnetOf(s string) string {
	ip := net.ParseIP(s)
	if ip == nil {
		return s
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}

// handleFlows 提供 NetFlow/sFlow 的查看接口。ip 为空表示汇总全部设备;
// window 是时间窗口(秒, 0 = 缓冲区里的全部), 默认 5 分钟。返回:
//   - 概览 + 10 秒粒度的流量趋势
//   - Top 会话 / Top 源 IP / Top 目的 IP / 协议端口分布 / 东西向-南北向构成
//   - L7 域名 Top(DNS/TLS SNI/HTTP Host)、VLAN 分布、网段流量矩阵
//   - TCP 标志位统计和疑似 SYN 扫描源
//   - sFlow 接口计数器(速率/丢包/错包/广播), 以及"有丢包/错包/接近跑满的
//     接口 + 同一窗口里经过它的 Top 会话"的拥塞关联
//   - 最近原始记录(最多 500 条)
func (s *server) handleFlows(w http.ResponseWriter, r *http.Request) {
	enabled := s.conf().Collectors.NetflowPort > 0 || s.conf().Collectors.SflowPort > 0
	if s.flows == nil || !enabled {
		s.writeJSON(w, 200, map[string]interface{}{"enabled": false, "records": []FlowRecord{}, "top_talkers": []flowTalker{}, "coverage": map[string]interface{}{"total_devices": 0, "devices": []flowDeviceCoverage{}}})
		return
	}
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	window := int64(300)
	if v, err := strconv.ParseInt(r.URL.Query().Get("window"), 10, 64); err == nil && flowWindows[v] {
		window = v
	}
	nowMs := time.Now().UnixMilli()
	sinceMs := int64(0)
	if window > 0 {
		sinceMs = nowMs - window*1000
	}

	var all []FlowRecord
	if ip == "" {
		all = s.flows.all()
	} else {
		all = s.flows.get(ip)
	}
	records := all[:0]
	for _, rec := range all {
		if rec.TS >= sinceMs {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].TS > records[j].TS })

	// 设备 IP -> (ifIndex -> 端口名 / 设备名), 用来把流记录和接口计数器
	// 里的 ifIndex 换成人能看懂的端口名。
	s.hub.mu.RLock()
	reports := s.hub.reports
	s.hub.mu.RUnlock()
	ifNames := make(map[string]map[string]string, len(reports))
	devNames := make(map[string]string, len(reports))
	for _, rep := range reports {
		m := make(map[string]string, len(rep.Ports))
		for _, p := range rep.Ports {
			m[p.Index] = p.Name
		}
		ifNames[rep.IP] = m
		devNames[rep.IP] = rep.SysName
	}

	// 平台级覆盖率: 统计启用监控的设备中最近 5 分钟收到流记录、最近 10 分钟
	// 收到 sFlow counter/processor 的设备。沉默只表示未回传，不直接判为故障。
	var coverage map[string]interface{}
	if ip == "" {
		coverageByIP := make(map[string]*flowDeviceCoverage)
		if s.devices != nil {
			for _, dev := range s.devices.Active() {
				name := devNames[dev.IP]
				if name == "" {
					name = dev.Note
				}
				if name == "" {
					name = dev.IP
				}
				coverageByIP[dev.IP] = &flowDeviceCoverage{DeviceIP: dev.IP, DeviceName: name}
			}
		}
		lastFlowAt := make(map[string]int64)
		for _, rec := range all {
			if rec.TS > lastFlowAt[rec.DeviceIP] {
				lastFlowAt[rec.DeviceIP] = rec.TS
			}
		}
		for _, rec := range records {
			st := coverageByIP[rec.DeviceIP]
			if st == nil {
				continue
			}
			st.FlowSamples++
			st.FlowBytes += rec.Bytes
			switch rec.Source {
			case "sflow":
				st.SFlowSamples++
			case "netflow":
				st.NetFlowSamples++
			}
		}
		now := time.Now()
		flowFreshMs := now.UnixMilli() - int64(5*time.Minute/time.Millisecond)
		counterFreshMs := now.UnixMilli() - int64(10*time.Minute/time.Millisecond)
		for ip, ts := range lastFlowAt {
			if st := coverageByIP[ip]; st != nil {
				st.LastFlowAt = time.UnixMilli(ts).Local().Format("2006-01-02 15:04:05")
				st.FlowRecent = ts >= flowFreshMs
			}
		}
		for key, stat := range s.flows.counterStats("") {
			dev, _, ok := strings.Cut(key, "|")
			if !ok {
				continue
			}
			if st := coverageByIP[dev]; st != nil {
				st.CounterInterfaces++
				if stat.UpdatedAt > st.LastCounterAt {
					st.LastCounterAt = stat.UpdatedAt
				}
			}
		}
		for ip, ts := range s.flows.lastCounterSampleAt() {
			if st := coverageByIP[ip]; st != nil {
				if st.LastCounterAt == "" || ts >= counterFreshMs {
					st.LastCounterAt = time.UnixMilli(ts).Local().Format("2006-01-02 15:04:05")
				}
				st.CounterRecent = ts >= counterFreshMs
			}
		}
		coverageDevices := make([]flowDeviceCoverage, 0, len(coverageByIP))
		flowDevices, counterDevices, dataDevices := 0, 0, 0
		for _, st := range coverageByIP {
			st.HasRecentData = st.FlowRecent || st.CounterRecent
			if st.FlowRecent {
				flowDevices++
			}
			if st.CounterRecent {
				counterDevices++
			}
			if st.HasRecentData {
				dataDevices++
			}
			coverageDevices = append(coverageDevices, *st)
		}
		sort.Slice(coverageDevices, func(i, j int) bool {
			if coverageDevices[i].HasRecentData != coverageDevices[j].HasRecentData {
				return coverageDevices[i].HasRecentData
			}
			if coverageDevices[i].FlowBytes != coverageDevices[j].FlowBytes {
				return coverageDevices[i].FlowBytes > coverageDevices[j].FlowBytes
			}
			return ipLess(coverageDevices[i].DeviceIP, coverageDevices[j].DeviceIP)
		})
		coveragePct := 0.0
		if len(coverageDevices) > 0 {
			coveragePct = math.Round(float64(dataDevices)/float64(len(coverageDevices))*1000) / 10
		}
		coverage = map[string]interface{}{
			"total_devices": len(coverageDevices), "flow_devices": flowDevices,
			"counter_devices": counterDevices, "data_devices": dataDevices,
			"coverage_percent": coveragePct, "flow_freshness_sec": 300,
			"counter_freshness_sec": 600, "devices": coverageDevices,
		}
	}

	agg := make(map[string]*flowTalker, 64)
	bySrc := make(map[string]*flowIPStat, 64)
	byDst := make(map[string]*flowIPStat, 64)
	byBucket := make(map[string]*flowPortBucket, 16)
	byDomain := make(map[string]*flowDomainStat, 64)
	byVlan := make(map[int]*flowVlanStat, 16)
	byNet := make(map[string]*flowMatrixCell, 64)
	synSrc := make(map[string]*flowSynSuspect, 16)
	byIf := make(map[string]map[string]*flowTalker) // "设备|ifIndex" -> 经过这个接口的会话
	var tcp flowTCPStats
	var l7 flowL7Stats
	var totalBytes, totalPkts uint64
	srcSet, dstSet := make(map[string]bool), make(map[string]bool)
	dirStats := map[string]*flowDirStat{
		"priv-priv": {Label: "私网 → 私网 (东西向)"},
		"priv-pub":  {Label: "私网 → 公网 (南北向·出)"},
		"pub-priv":  {Label: "公网 → 私网 (南北向·入)"},
		"pub-pub":   {Label: "公网 → 公网 (穿透)"},
	}
	addTalker := func(m map[string]*flowTalker, rec FlowRecord) {
		key := rec.SrcIP + "|" + rec.DstIP + "|" + rec.Protocol + "|" + strconv.Itoa(rec.DstPort)
		t, ok := m[key]
		if !ok {
			t = &flowTalker{SrcIP: rec.SrcIP, DstIP: rec.DstIP, DstPort: rec.DstPort, Protocol: rec.Protocol, App: rec.App}
			m[key] = t
		}
		t.Bytes += rec.Bytes
		t.Packets += rec.Packets
		t.Flows++
	}
	byProto := make(map[string]*flowProtoStat, 16)
	byApp := make(map[string]*flowAppStat, 64)
	byCat := make(map[string]*flowAppCatStat, 16)
	l2 := flowL2Stats{Counts: map[string]int{}}
	arpBySrc := make(map[string]*flowMacStat)
	bpduByIf := make(map[string]*flowBpduIf)
	portInfo := make(map[string]PortDetail) // "设备|ifIndex" -> 端口
	for _, rep := range reports {
		for _, p := range rep.Ports {
			portInfo[rep.IP+"|"+p.Index] = p
		}
	}
	for _, rec := range records {
		totalBytes += rec.Bytes
		totalPkts += rec.Packets

		ps, ok := byProto[rec.Protocol]
		if !ok {
			ps = &flowProtoStat{Name: rec.Protocol, L2: isL2Record(rec)}
			byProto[rec.Protocol] = ps
		}
		ps.Bytes += rec.Bytes
		ps.Flows++
		appName := rec.App
		if appName == "" {
			appName = rec.Protocol
		}
		as, ok := byApp[appName]
		if !ok {
			as = &flowAppStat{App: appName, Cat: rec.AppCat, clients: map[string]bool{}}
			byApp[appName] = as
		}
		as.Bytes += rec.Bytes
		as.Flows++
		if rec.SrcIP != "" {
			as.clients[rec.SrcIP] = true
		}
		cat := rec.AppCat
		if cat == "" {
			cat = "未识别"
		}
		cs, ok := byCat[cat]
		if !ok {
			cs = &flowAppCatStat{Cat: cat}
			byCat[cat] = cs
		}
		cs.Bytes += rec.Bytes
		cs.Flows++

		if isL2Record(rec) {
			l2.Counts[rec.Protocol]++
			l2.Total++
			switch rec.Protocol {
			case "ARP":
				l2.ArpPackets += rec.Packets
				m, ok := arpBySrc[rec.SrcMAC]
				if !ok {
					m = &flowMacStat{MAC: rec.SrcMAC}
					arpBySrc[rec.SrcMAC] = m
				}
				m.Packets += rec.Packets
				m.Samples++
			case "STP", "PVST+":
				k := rec.DeviceIP + "|" + rec.InIfIndex
				b, ok := bpduByIf[k]
				if !ok {
					p := portInfo[k]
					b = &flowBpduIf{DeviceIP: rec.DeviceIP, DeviceName: devNames[rec.DeviceIP], IfIndex: rec.InIfIndex, IfName: p.Name, SrcMAC: rec.SrcMAC}
					// 接入口(没有 LLDP 邻居、MAC 数也不多)收到 BPDU, 说明下面挂了交换机
					if p.Name != "" && !p.IsUplink {
						b.Suspicious = true
					}
					bpduByIf[k] = b
				}
				b.Samples++
			}
			continue // 二层帧没有 IP, 不参与下面按 IP/端口的统计
		}
		srcSet[rec.SrcIP], dstSet[rec.DstIP] = true, true
		addTalker(agg, rec)
		for _, idx := range []string{rec.InIfIndex, rec.OutIfIndex} {
			if idx == "" || idx == "0" {
				continue
			}
			k := rec.DeviceIP + "|" + idx
			if byIf[k] == nil {
				byIf[k] = make(map[string]*flowTalker)
			}
			addTalker(byIf[k], rec)
		}

		srcPriv, dstPriv := isPrivateIP(rec.SrcIP), isPrivateIP(rec.DstIP)

		s1, ok := bySrc[rec.SrcIP]
		if !ok {
			s1 = &flowIPStat{IP: rec.SrcIP, IsPrivate: srcPriv}
			bySrc[rec.SrcIP] = s1
		}
		s1.Bytes += rec.Bytes
		s1.Packets += rec.Packets
		s1.Flows++

		d1, ok := byDst[rec.DstIP]
		if !ok {
			d1 = &flowIPStat{IP: rec.DstIP, IsPrivate: dstPriv}
			byDst[rec.DstIP] = d1
		}
		d1.Bytes += rec.Bytes
		d1.Packets += rec.Packets
		d1.Flows++

		bucketName := classifyPortBucket(rec.Protocol, rec.DstPort)
		b, ok := byBucket[bucketName]
		if !ok {
			b = &flowPortBucket{Name: bucketName}
			byBucket[bucketName] = b
		}
		b.Bytes += rec.Bytes
		b.Flows++

		var dirKey string
		switch {
		case srcPriv && dstPriv:
			dirKey = "priv-priv"
		case srcPriv && !dstPriv:
			dirKey = "priv-pub"
		case !srcPriv && dstPriv:
			dirKey = "pub-priv"
		default:
			dirKey = "pub-pub"
		}
		dirStats[dirKey].Bytes += rec.Bytes
		dirStats[dirKey].Flows++

		if rec.AppProto != "" {
			switch rec.AppProto {
			case "DNS":
				l7.DNS++
			case "TLS":
				l7.TLS++
				if rec.AppHost == "" {
					l7.TLSNoSNI++
				}
			case "HTTP":
				l7.HTTP++
			}
			if rec.AppHost != "" {
				k := rec.AppProto + "|" + rec.AppHost
				d, ok := byDomain[k]
				if !ok {
					d = &flowDomainStat{Host: rec.AppHost, Proto: rec.AppProto, clients: map[string]bool{}}
					byDomain[k] = d
				}
				d.Bytes += rec.Bytes
				d.Flows++
				d.clients[rec.SrcIP] = true
			}
		}

		if rec.VLAN > 0 {
			v, ok := byVlan[rec.VLAN]
			if !ok {
				v = &flowVlanStat{VLAN: rec.VLAN}
				byVlan[rec.VLAN] = v
			}
			v.Bytes += rec.Bytes
			v.Flows++
		}

		sn, dn := subnetOf(rec.SrcIP), subnetOf(rec.DstIP)
		nk := sn + "|" + dn
		cell, ok := byNet[nk]
		if !ok {
			kind := "南北向"
			if srcPriv && dstPriv {
				kind = "东西向"
			}
			cell = &flowMatrixCell{SrcNet: sn, DstNet: dn, Kind: kind}
			byNet[nk] = cell
		}
		cell.Bytes += rec.Bytes
		cell.Flows++

		if rec.Protocol == "TCP" && rec.TCPFlags != 0 {
			tcp.Total++
			f := rec.TCPFlags
			switch {
			case f&tcpSYN != 0 && f&tcpACK == 0:
				tcp.SynOnly++
				sy, ok := synSrc[rec.SrcIP]
				if !ok {
					sy = &flowSynSuspect{SrcIP: rec.SrcIP, targets: map[string]bool{}}
					synSrc[rec.SrcIP] = sy
				}
				sy.SynOnly++
				t := rec.DstIP + ":" + strconv.Itoa(rec.DstPort)
				if !sy.targets[t] {
					sy.targets[t] = true
					if len(sy.Sample) < 5 {
						sy.Sample = append(sy.Sample, t)
					}
				}
			case f&tcpSYN != 0 && f&tcpACK != 0:
				tcp.SynAck++
			}
			if f&tcpRST != 0 {
				tcp.Rst++
			}
			if f&tcpFIN != 0 {
				tcp.Fin++
			}
		}
	}

	topTalkers := func(m map[string]*flowTalker, n int) []flowTalker {
		out := make([]flowTalker, 0, len(m))
		for _, t := range m {
			out = append(out, *t)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
		if len(out) > n {
			out = out[:n]
		}
		return out
	}
	topIPs := func(m map[string]*flowIPStat) []flowIPStat {
		out := make([]flowIPStat, 0, len(m))
		for _, v := range m {
			out = append(out, *v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
		if len(out) > 20 {
			out = out[:20]
		}
		return out
	}

	buckets := make([]flowPortBucket, 0, len(byBucket))
	for _, b := range byBucket {
		buckets = append(buckets, *b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Bytes > buckets[j].Bytes })

	dirs := make([]flowDirStat, 0, 4)
	for _, k := range []string{"priv-priv", "priv-pub", "pub-priv", "pub-pub"} {
		dirs = append(dirs, *dirStats[k])
	}

	domains := make([]flowDomainStat, 0, len(byDomain))
	for _, d := range byDomain {
		d.Clients = len(d.clients)
		domains = append(domains, *d)
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i].Flows != domains[j].Flows {
			return domains[i].Flows > domains[j].Flows
		}
		return domains[i].Bytes > domains[j].Bytes
	})
	if len(domains) > 50 {
		domains = domains[:50]
	}

	vlans := make([]flowVlanStat, 0, len(byVlan))
	for _, v := range byVlan {
		vlans = append(vlans, *v)
	}
	sort.Slice(vlans, func(i, j int) bool { return vlans[i].Bytes > vlans[j].Bytes })

	matrix := make([]flowMatrixCell, 0, len(byNet))
	for _, c := range byNet {
		matrix = append(matrix, *c)
	}
	sort.Slice(matrix, func(i, j int) bool { return matrix[i].Bytes > matrix[j].Bytes })
	if len(matrix) > 30 {
		matrix = matrix[:30]
	}

	// 一个源对 >= 10 个不同 目的IP:端口 发纯 SYN 才算疑似扫描: 抽样之下
	// 正常客户端短时间内很难被抽中这么多次不同目标的建连包。
	const synScanTargets = 10
	synSuspects := make([]flowSynSuspect, 0)
	scanners := make(map[string]bool)
	for _, sy := range synSrc {
		sy.Targets = len(sy.targets)
		if sy.Targets >= synScanTargets {
			synSuspects = append(synSuspects, *sy)
			scanners[sy.SrcIP] = true
		}
	}
	sort.Slice(synSuspects, func(i, j int) bool { return synSuspects[i].Targets > synSuspects[j].Targets })

	protos := make([]flowProtoStat, 0, len(byProto))
	for _, p := range byProto {
		protos = append(protos, *p)
	}
	sort.Slice(protos, func(i, j int) bool { return protos[i].Bytes > protos[j].Bytes })
	apps := make([]flowAppStat, 0, len(byApp))
	for _, a := range byApp {
		a.Clients = len(a.clients)
		apps = append(apps, *a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Bytes > apps[j].Bytes })
	if len(apps) > 60 {
		apps = apps[:60]
	}
	cats := make([]flowAppCatStat, 0, len(byCat))
	for _, c := range byCat {
		cats = append(cats, *c)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Bytes > cats[j].Bytes })
	for _, m := range arpBySrc {
		l2.ArpTop = append(l2.ArpTop, *m)
	}
	sort.Slice(l2.ArpTop, func(i, j int) bool { return l2.ArpTop[i].Packets > l2.ArpTop[j].Packets })
	if len(l2.ArpTop) > 15 {
		l2.ArpTop = l2.ArpTop[:15]
	}
	for _, b := range bpduByIf {
		l2.BpduIfs = append(l2.BpduIfs, *b)
	}
	sort.Slice(l2.BpduIfs, func(i, j int) bool {
		if l2.BpduIfs[i].Suspicious != l2.BpduIfs[j].Suspicious {
			return l2.BpduIfs[i].Suspicious
		}
		return l2.BpduIfs[i].Samples > l2.BpduIfs[j].Samples
	})

	// sFlow 接口计数器 + 拥塞关联
	ifCounters := make([]flowIfCounter, 0)
	congestion := make([]flowCongestion, 0)
	for k, st := range s.flows.counterStats(ip) {
		dev, idx, _ := strings.Cut(k, "|")
		st.IfName = ifNames[dev][idx]
		ifCounters = append(ifCounters, flowIfCounter{IfCounterStat: st, DeviceIP: dev, DeviceName: devNames[dev]})

		var reasons []string
		if st.OutDiscards > 0 {
			reasons = append(reasons, fmt.Sprintf("出方向丢包 %d", st.OutDiscards))
		}
		if st.InDiscards > 0 {
			reasons = append(reasons, fmt.Sprintf("入方向丢包 %d", st.InDiscards))
		}
		if st.InErrors+st.OutErrors > 0 {
			reasons = append(reasons, fmt.Sprintf("错包 %d", st.InErrors+st.OutErrors))
		}
		if st.InUtil >= 80 || st.OutUtil >= 80 {
			reasons = append(reasons, fmt.Sprintf("利用率 入%.0f%%/出%.0f%%", st.InUtil, st.OutUtil))
		}
		if len(reasons) == 0 {
			continue
		}
		congestion = append(congestion, flowCongestion{
			DeviceIP: dev, DeviceName: devNames[dev], IfIndex: idx, IfName: st.IfName,
			Reason: strings.Join(reasons, ", "), Talkers: topTalkers(byIf[k], 5),
		})
	}
	sort.Slice(ifCounters, func(i, j int) bool {
		a, b := ifCounters[i], ifCounters[j]
		return a.InMbps+a.OutMbps > b.InMbps+b.OutMbps
	})
	if len(ifCounters) > 100 {
		ifCounters = ifCounters[:100]
	}
	sort.Slice(congestion, func(i, j int) bool { return congestion[i].Reason > congestion[j].Reason })

	// 趋势: 默认窗口至少画 5 分钟, 全部 = 最多 30 分钟(趋势桶的保留上限)
	tlWindow := window
	if tlWindow == 0 || tlWindow > flowBucketSec*flowBuckets {
		tlWindow = flowBucketSec * flowBuckets
	}
	if tlWindow < 300 {
		tlWindow = 300
	}
	timeline := make([]flowTimelinePoint, 0, tlWindow/flowBucketSec)
	for _, b := range s.flows.buckets(ip, nowMs/1000-tlWindow) {
		timeline = append(timeline, flowTimelinePoint{
			TS:   b.TS,
			Mbps: math.Round(float64(b.Bytes)*8/flowBucketSec/1e6*100) / 100,
			Pps:  math.Round(float64(b.Packets) / flowBucketSec),
		})
	}

	suspiciousCount := 0
	for i := range records {
		hit, reason := classifySuspicious(records[i])
		if !hit && scanners[records[i].SrcIP] && records[i].TCPFlags&tcpSYN != 0 && records[i].TCPFlags&tcpACK == 0 {
			hit, reason = true, "该源在时间窗口内对大量不同目标发纯 SYN, 疑似扫描"
		}
		records[i].Suspicious, records[i].SuspiciousReason = hit, reason
		if hit {
			suspiciousCount++
		}
	}
	sampled := records
	if len(sampled) > 500 {
		sampled = sampled[:500] // 原始记录只回最近 500 条, 排障看个大概, 统计是完整窗口的聚合结果
	}
	out := make([]FlowRecord, len(sampled))
	copy(out, sampled)
	for i := range out {
		out[i].InIfName = ifNames[out[i].DeviceIP][out[i].InIfIndex]
		out[i].OutIfName = ifNames[out[i].DeviceIP][out[i].OutIfIndex]
	}

	// 估算平均速率: 窗口 = 实际覆盖的时间段(缓冲区可能还没攒满窗口)
	var avgMbps float64
	if len(records) > 0 {
		span := float64(nowMs-records[len(records)-1].TS) / 1000
		if window > 0 && span > float64(window) {
			span = float64(window)
		}
		if span < 1 {
			span = 1
		}
		avgMbps = math.Round(float64(totalBytes)*8/span/1e6*100) / 100
		l2.ArpPps = math.Round(float64(l2.ArpPackets)/span*10) / 10
	}

	resp := map[string]interface{}{
		"enabled": true, "window": window, "l7_enabled": s.flows.l7Enabled(),
		"netflow_port": s.conf().Collectors.NetflowPort, "sflow_port": s.conf().Collectors.SflowPort,
		"summary": map[string]interface{}{
			"samples": len(records), "bytes": totalBytes, "packets": totalPkts, "avg_mbps": avgMbps,
			"src_ips": len(srcSet), "dst_ips": len(dstSet), "buffered": len(all),
		},
		"timeline": timeline, "records": out, "top_talkers": topTalkers(agg, 50),
		"top_src_ips": topIPs(bySrc), "top_dst_ips": topIPs(byDst), "port_buckets": buckets, "direction_stats": dirs,
		"top_domains": domains, "l7_stats": l7, "top_vlans": vlans, "subnet_matrix": matrix,
		"tcp_stats": tcp, "syn_suspects": synSuspects,
		"if_counters": ifCounters, "congestion": congestion,
		"suspicious_count": suspiciousCount,
		"protocols":        protos, "top_apps": apps, "app_cats": cats, "l2_stats": l2,
	}
	if ip != "" {
		if h := s.flows.host(ip); h != nil {
			resp["sflow_host"] = h
		}
	} else {
		resp["coverage"] = coverage
	}
	s.writeJSON(w, 200, resp)
}

// handleFlowSettings 切换 L7 域名解析开关(需要登录)。只影响之后收到的
// 数据, 已经在缓冲区里的记录不会回头删掉域名字段。
func (s *server) handleFlowSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		L7Enabled bool `json:"l7_enabled"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if s.flows == nil {
		s.fail(w, 400, "流量采集未开启")
		return
	}
	if err := s.flows.setL7(req.L7Enabled); err != nil {
		s.fail(w, 500, "保存失败: "+err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "l7_enabled": req.L7Enabled})
}
