package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ----------------- 流量分析 (NetFlow v5 / sFlow v5) -----------------
//
// 跟 syslog.go 是同一个思路: 交换机/路由器主动把数据"推"过来, 这里起一个
// UDP 监听, 按来源设备 IP 分类, 有界环形缓冲(不做长期落盘, 量级比 MAC 表、
// 日志大得多), 满足"最近几分钟发生过什么流量、谁在跟谁通信、访问了哪些
// 域名、哪个口在丢包是被谁打满的"这类排障需求。
//
//   - NetFlow: 只支持 v5(最常见、格式固定、不需要模板协商)。v9/IPFIX 用
//     模板描述字段, 需要先收模板包再解模板, 复杂度高一截, 这轮没做。
//   - sFlow v5, 只认标准结构(enterprise=0):
//     flow_sample: raw_packet_header(以太网/802.1Q/QinQ + IPv4/IPv6 +
//     TCP/UDP, 可选的 DNS/TLS SNI/HTTP Host 识别)、sampled_ipv4/ipv6、
//     extended_switch(VLAN);
//     counters_sample: generic_interface_counters(接口收发/丢包/错包)、
//     processor_information(CPU/内存)。
//
// 两个解析器都遵循同一个原则: 用协议里自带的长度字段来跳过看不懂/不关心
// 的部分, 而不是假设自己认识所有字段——这样对方发了些我们没实现的扩展
// 字段时, 只是那一条子记录没提取出五元组, 不会导致整包解析失败或崩溃。

// FlowRecord 是单条流量记录: NetFlow/sFlow 解析出来的五元组 + 流量计数。
type FlowRecord struct {
	Time        string `json:"time"`
	DeviceIP    string `json:"device_ip"` // 上报这条流的设备(交换机/路由器), 即 UDP 包的来源 IP
	SrcIP       string `json:"src_ip"`
	DstIP       string `json:"dst_ip"`
	SrcPort     int    `json:"src_port"`
	DstPort     int    `json:"dst_port"`
	Protocol    string `json:"protocol"` // TCP/UDP/ICMP/...
	Bytes       uint64 `json:"bytes"`
	Packets     uint64 `json:"packets"`
	InIfIndex   string `json:"in_ifindex,omitempty"`
	OutIfIndex  string `json:"out_ifindex,omitempty"`
	SamplingPct int    `json:"sampling_pct,omitempty"` // sFlow 抽样场景下的抽样率(1/N 转成百分比), NetFlow 通常是 100

	TS       int64  `json:"ts"`                  // 收到的时刻, Unix 毫秒; 时间窗口筛选和秒级趋势都靠它
	Source   string `json:"source"`              // "sflow" / "netflow"
	SampleN  uint32 `json:"sample_n,omitempty"`  // sFlow 抽样率 1/N 的 N
	SrcMAC   string `json:"src_mac,omitempty"`   // 只有 sFlow 原始报文头才有二层信息
	DstMAC   string `json:"dst_mac,omitempty"`   //
	VLAN     int    `json:"vlan,omitempty"`      // 802.1Q tag 或 sFlow extended_switch 里的 VLAN
	IPVer    int    `json:"ip_ver,omitempty"`    // 4 / 6
	TCPFlags uint8  `json:"tcp_flags,omitempty"` // TCP 标志位原始值, 前端/统计按位解读
	AppProto string `json:"app_proto,omitempty"` // L7 识别结果: DNS / TLS / HTTP
	AppHost  string `json:"app_host,omitempty"`  // DNS 查询域名 / TLS SNI / HTTP Host
	AppPath  string `json:"app_path,omitempty"`  // HTTP 请求方法 + URI 前缀
	App      string `json:"app,omitempty"`       // 应用名(域名归属 / 负载特征 / 知名端口, 见 flowapp.go)
	AppCat   string `json:"app_cat,omitempty"`   // 应用类别: Web / 视频 / 即时通讯 / 二层控制 ...

	// 下面几项是查询时(handleFlows)现算现填的展示层信息, 不在采集阶段
	// 写入、也不落盘: 谁在跟谁通信一眼就能看出来是不是像扫描, 不用用户
	// 自己盯着源端口/包数猜; 接口号换算成接口名也在查询时做。
	Suspicious       bool   `json:"suspicious,omitempty"`
	SuspiciousReason string `json:"suspicious_reason,omitempty"`
	InIfName         string `json:"in_if_name,omitempty"`
	OutIfName        string `json:"out_if_name,omitempty"`
}

// TCP 标志位
const (
	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpPSH = 0x08
	tcpACK = 0x10
)

// tcpFlagsText 把标志位转成 "SYN,ACK" 这种可读形式。
func tcpFlagsText(f uint8) string {
	if f == 0 {
		return ""
	}
	var parts []string
	for _, x := range []struct {
		bit  uint8
		name string
	}{{tcpSYN, "SYN"}, {tcpACK, "ACK"}, {tcpFIN, "FIN"}, {tcpRST, "RST"}, {tcpPSH, "PSH"}} {
		if f&x.bit != 0 {
			parts = append(parts, x.name)
		}
	}
	return strings.Join(parts, ",")
}

// isPrivateIP 判断是不是 RFC1918 私网地址(含链路本地/环回), 用来给流量
// 打"公网/私网"方向标签。判断不了(比如空字符串、IPv6, 这里的 NetFlow/sFlow
// 解析目前只支持 IPv4)时保守地当作"未知", 调用方按需处理。
func isPrivateIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
		return true
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	default:
		return false
	}
}

// sensitivePorts 是常见的高危管理/横向移动端口——SMB/RDP/SSH/RPC 这些一旦
// 被大范围试探(同一源对很多目的地址只打一两个包就换下一个), 十有八九是
// 扫描而不是正常业务。端口本身出现不代表有问题(正常运维也会连 22/3389),
// 所以只有"命中敏感端口 且 这条流的包数很少"才标"疑似扫描", 见 classifySuspicious。
var sensitivePorts = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 135: "RPC/DCOM", 139: "NetBIOS",
	445: "SMB", 1433: "MSSQL", 1521: "Oracle", 3306: "MySQL", 3389: "RDP",
	5432: "PostgreSQL", 5900: "VNC", 6379: "Redis", 9200: "Elasticsearch", 27017: "MongoDB",
}

// suspiciousPacketThreshold: 一条流的包数不超过这个值(默认按用户反馈的
// "≤2 包"), 命中敏感端口时才判定为疑似扫描——正常访问一个服务往往是
// 三次握手+多次数据交互, 包数会明显更多; 只打一两个包大概率是探测/扫描
// 打完就走, 不等回应。
const suspiciousPacketThreshold = 2

// classifySuspicious 返回这条流是否疑似扫描, 以及给用户看的原因文案。
func classifySuspicious(rec FlowRecord) (bool, string) {
	name, sensitive := sensitivePorts[rec.DstPort]
	if !sensitive {
		return false, ""
	}
	// sFlow 的包数是按抽样率放大后的估算值(一个样本代表 N 个包), "包数≤2"
	// 这条规则对它永远不成立; 单个纯 SYN 样本也可能只是一次正常的 SSH 登录。
	// sFlow 的扫描判定改在 handleFlows 里按源 IP 聚合做(同一个源对很多
	// 不同目标发纯 SYN), 见 synScanSuspects。
	if rec.Source == "sflow" {
		return false, ""
	}
	if rec.Packets == 0 || rec.Packets > suspiciousPacketThreshold {
		return false, ""
	}
	return true, fmt.Sprintf("命中敏感端口 %d(%s), 仅 %d 个包, 疑似扫描/探测", rec.DstPort, name, rec.Packets)
}

// classifyPortBucket 把目的端口归到一个粗粒度的业务分类, 用于协议/端口
// 分布饼图——具体端口号太细, 用户真正关心的是"HTTP/HTTPS/DNS 正常占多少,
// Other/高位端口异常占多少"这种大盘子。
func classifyPortBucket(protocol string, port int) string {
	switch port {
	case 80, 8080, 8000, 8888:
		return "HTTP"
	case 443, 8443:
		return "HTTPS"
	case 53:
		return "DNS"
	case 22:
		return "SSH"
	case 3389:
		return "RDP"
	case 445, 139:
		return "SMB"
	case 21:
		return "FTP"
	case 23:
		return "Telnet"
	case 25, 110, 143, 465, 587, 993, 995:
		return "邮件"
	case 1433, 1521, 3306, 5432, 6379, 27017:
		return "数据库"
	case 123:
		return "NTP"
	case 161, 162:
		return "SNMP"
	}
	if protocol == "ICMP" || protocol == "ICMPv6" {
		return "ICMP"
	}
	if port > 0 && port < 1024 {
		return "其他知名端口"
	}
	if port >= 1024 {
		return "高位端口"
	}
	return "Other"
}

// flowRingSize: 每台设备最多保留这么多条最近的流记录, 超过滚掉最旧的。
// 之前是 500 条, 1/1000 抽样、几 Gbps 的上联口几秒钟就能刷满, 5/15 分钟
// 这种时间窗口根本看不到; 单条记录约 300 字节, 5000 条每台设备 1.5MB 左右。
// 可以在系统设置里改, 但因为已经在用的环形缓冲是定长数组, 只对重启之后
// 新建的缓冲生效(见 setFlowRingSize, main.go 在任何采集开始之前调用一次)。
const flowRingSize = 5000

var flowRingCap = flowRingSize

func setFlowRingSize(n int) {
	if n > 0 {
		flowRingCap = n
	}
}

// 秒级趋势: 每台设备按 10 秒一个桶累计估算字节/包数, 保留最近 30 分钟。
// 跟原始记录环是两套东西——记录环会被高流量设备很快刷掉, 趋势桶不会。
const (
	flowBucketSec = 10
	flowBuckets   = 180
)

type flowBucket struct {
	TS      int64 // 桶起始时刻, Unix 秒, 按 flowBucketSec 对齐
	Bytes   uint64
	Packets uint64
	Samples int
}

// flowRing 是定长环形缓冲。之前用 append + 截尾部切片实现, 每次超限都
// 保留底层大数组, 也谈不上"环形"; 这里改成真正的定长环。
type flowRing struct {
	buf  []FlowRecord
	next int
	full bool
}

func (r *flowRing) push(rec FlowRecord) {
	if r.buf == nil {
		r.buf = make([]FlowRecord, flowRingCap)
	}
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

func (r *flowRing) appendTo(out []FlowRecord) []FlowRecord {
	if r.full {
		out = append(out, r.buf[r.next:]...)
	}
	return append(out, r.buf[:r.next]...)
}

type flowStore struct {
	mu       sync.RWMutex
	records  map[string]*flowRing                  // 设备 IP -> 环形保留的流记录
	timeline map[string]*[flowBuckets]flowBucket   // 设备 IP -> 10 秒粒度的流量趋势
	counters map[string]map[string]*ifCounterState // 设备 IP -> ifIndex -> sFlow 接口计数器
	hosts    map[string]*SflowHostStat             // 设备 IP -> sFlow 上报的 CPU/内存

	// l7 决定是否从 sFlow 报文头里解析 DNS 域名 / TLS SNI / HTTP Host。
	// 这些是"谁访问了哪个网站"级别的信息, 有的单位对这类数据有合规要求,
	// 所以做成可以在页面上随时关掉的开关, 状态落盘到 data/flow_settings.json。
	l7           atomic.Bool
	settingsPath string
}

type flowSettings struct {
	L7Enabled bool `json:"l7_enabled"`
}

// newFlowStore: dataDir 为空时不落盘(测试用); l7Default 只在还没有落盘
// 过设置时生效, 之后以页面上的开关为准。
func newFlowStore(dataDir string, l7Default bool) *flowStore {
	f := &flowStore{
		records:  make(map[string]*flowRing),
		timeline: make(map[string]*[flowBuckets]flowBucket),
		counters: make(map[string]map[string]*ifCounterState),
		hosts:    make(map[string]*SflowHostStat),
	}
	f.l7.Store(l7Default)
	if dataDir != "" {
		f.settingsPath = filepath.Join(dataDir, "flow_settings.json")
		var st flowSettings
		if err := readJSON(f.settingsPath, &st); err == nil {
			f.l7.Store(st.L7Enabled)
		}
	}
	return f
}

func (f *flowStore) l7Enabled() bool { return f.l7.Load() }

func (f *flowStore) setL7(on bool) error {
	f.l7.Store(on)
	if f.settingsPath == "" {
		return nil
	}
	return atomicWriteJSON(f.settingsPath, flowSettings{L7Enabled: on}, 0o600)
}

func (f *flowStore) add(rec FlowRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ring, ok := f.records[rec.DeviceIP]
	if !ok {
		ring = &flowRing{}
		f.records[rec.DeviceIP] = ring
	}
	ring.push(rec)

	tl, ok := f.timeline[rec.DeviceIP]
	if !ok {
		tl = &[flowBuckets]flowBucket{}
		f.timeline[rec.DeviceIP] = tl
	}
	sec := rec.TS / 1000
	start := sec - sec%flowBucketSec
	b := &tl[(start/flowBucketSec)%flowBuckets]
	if b.TS != start {
		*b = flowBucket{TS: start}
	}
	b.Bytes += rec.Bytes
	b.Packets += rec.Packets
	b.Samples++
}

func (f *flowStore) get(ip string) []FlowRecord {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ring, ok := f.records[ip]
	if !ok {
		return []FlowRecord{}
	}
	return ring.appendTo(make([]FlowRecord, 0, flowRingCap))
}

// all 汇总所有设备最近的流记录, 供全网视图使用; 设备数和单设备环形都
// 有界, 这里的开销可控。
func (f *flowStore) all() []FlowRecord {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]FlowRecord, 0, len(f.records)*64)
	for _, ring := range f.records {
		out = ring.appendTo(out)
	}
	return out
}

// buckets 返回 sinceSec 之后的趋势桶(ip 为空 = 全部设备按时刻求和), 按时间升序。
func (f *flowStore) buckets(ip string, sinceSec int64) []flowBucket {
	f.mu.RLock()
	defer f.mu.RUnlock()
	sum := make(map[int64]*flowBucket)
	for dev, tl := range f.timeline {
		if ip != "" && dev != ip {
			continue
		}
		for _, b := range tl {
			if b.TS == 0 || b.TS < sinceSec {
				continue
			}
			s, ok := sum[b.TS]
			if !ok {
				s = &flowBucket{TS: b.TS}
				sum[b.TS] = s
			}
			s.Bytes += b.Bytes
			s.Packets += b.Packets
			s.Samples += b.Samples
		}
	}
	out := make([]flowBucket, 0, len(sum))
	for _, b := range sum {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

func (f *flowStore) drop(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, ip)
	delete(f.timeline, ip)
	delete(f.counters, ip)
	delete(f.hosts, ip)
}

// clearAll 系统设置「清理工具」用: 清空所有设备的流量记录/趋势/计数器基线。
func (f *flowStore) clearAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.records {
		if r.full {
			n += len(r.buf)
		} else {
			n += r.next
		}
	}
	f.records = map[string]*flowRing{}
	f.timeline = map[string]*[flowBuckets]flowBucket{}
	f.counters = map[string]map[string]*ifCounterState{}
	f.hosts = map[string]*SflowHostStat{}
	return n
}

// ----------------- sFlow counter sampling -----------------
//
// sFlow 除了抽样报文(flow_sample), 还会周期性(通常 20~30 秒)推送接口
// 计数器(counters_sample): 收发字节、单播/组播/广播包、丢包、错包, 部分
// 设备还带 CPU/内存。跟 SNMP 轮询比, 好处是推送频率高、不占 SNMP 带宽,
// 而且跟抽样报文出自同一个数据源——某个接口出现拥塞丢包时, 可以直接把
// 同一时间窗口里经过这个接口的抽样流量拉出来, 看是谁把它打满的。

// ifCounterRaw 对应 sFlow generic_interface_counters(format=1)。
type ifCounterRaw struct {
	speed                                      uint64
	operUp                                     bool
	inOct, outOct                              uint64
	inMc, inBc, inDisc, inErr, outDisc, outErr uint32
	inUc, outUc, outMc, outBc                  uint32
	atMs                                       int64
}

// IfCounterStat 是前端展示用的接口计数器视图: 两次推送之间的速率和增量。
type IfCounterStat struct {
	IfIndex     string  `json:"if_index"`
	IfName      string  `json:"if_name,omitempty"` // 查询时按设备端口表换算
	SpeedMbps   int64   `json:"speed_mbps"`
	Up          bool    `json:"up"`
	InMbps      float64 `json:"in_mbps"`
	OutMbps     float64 `json:"out_mbps"`
	InUtil      float64 `json:"in_util"`
	OutUtil     float64 `json:"out_util"`
	InDiscards  uint64  `json:"in_discards"`
	OutDiscards uint64  `json:"out_discards"`
	InErrors    uint64  `json:"in_errors"`
	OutErrors   uint64  `json:"out_errors"`
	InBcastPps  float64 `json:"in_bcast_pps"`
	IntervalSec float64 `json:"interval_sec"`
	UpdatedAt   string  `json:"updated_at"`
}

type ifCounterState struct {
	last ifCounterRaw
	stat IfCounterStat
	has  bool // stat 里是否已经有一次有效的差值
}

// SflowHostStat 对应 sFlow processor_information(counters format=1001)。
type SflowHostStat struct {
	CPU5s     float64 `json:"cpu_5s"` // 百分比, -1 = 设备没报
	CPU1m     float64 `json:"cpu_1m"`
	CPU5m     float64 `json:"cpu_5m"`
	MemTotal  uint64  `json:"mem_total"`
	MemFree   uint64  `json:"mem_free"`
	UpdatedAt string  `json:"updated_at"`
}

func (f *flowStore) addCounter(deviceIP, ifIndex string, raw ifCounterRaw) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.counters[deviceIP]
	if !ok {
		m = make(map[string]*ifCounterState)
		f.counters[deviceIP] = m
	}
	st, ok := m[ifIndex]
	if !ok {
		st = &ifCounterState{}
		m[ifIndex] = st
	}
	prev := st.last
	st.last = raw
	st.stat.IfIndex = ifIndex
	st.stat.SpeedMbps = int64(raw.speed / 1e6)
	st.stat.Up = raw.operUp
	st.stat.UpdatedAt = time.UnixMilli(raw.atMs).Format("2006-01-02 15:04:05")
	if prev.atMs == 0 {
		return
	}
	dt := float64(raw.atMs-prev.atMs) / 1000
	if dt < 1 || dt > 600 { // 太近(重复包)或太久(中间断过)的差值没有意义
		return
	}
	d32 := func(cur, old uint32) uint64 { return counterDelta(uint64(cur), uint64(old), true) }
	st.stat.InMbps = plausibleRate(float64(counterDelta(raw.inOct, prev.inOct, false))*8/dt/1e6, st.stat.SpeedMbps)
	st.stat.OutMbps = plausibleRate(float64(counterDelta(raw.outOct, prev.outOct, false))*8/dt/1e6, st.stat.SpeedMbps)
	st.stat.InUtil, st.stat.OutUtil = 0, 0
	if st.stat.SpeedMbps > 0 {
		st.stat.InUtil = math.Min(st.stat.InMbps/float64(st.stat.SpeedMbps)*100, 100)
		st.stat.OutUtil = math.Min(st.stat.OutMbps/float64(st.stat.SpeedMbps)*100, 100)
	}
	st.stat.InDiscards = d32(raw.inDisc, prev.inDisc)
	st.stat.OutDiscards = d32(raw.outDisc, prev.outDisc)
	st.stat.InErrors = d32(raw.inErr, prev.inErr)
	st.stat.OutErrors = d32(raw.outErr, prev.outErr)
	st.stat.InBcastPps = math.Round(float64(d32(raw.inBc, prev.inBc))/dt*10) / 10
	st.stat.IntervalSec = math.Round(dt*10) / 10
	st.has = true
}

func (f *flowStore) setHost(deviceIP string, h SflowHostStat) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts[deviceIP] = &h
}

// counterStats 返回某台设备(ip 为空 = 全部)已经算出过差值的接口计数器。
// 返回的 map 以 "设备IP|ifIndex" 为键, 便于调用方跟流记录的接口号关联。
func (f *flowStore) counterStats(ip string) map[string]IfCounterStat {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]IfCounterStat)
	for dev, m := range f.counters {
		if ip != "" && dev != ip {
			continue
		}
		for idx, st := range m {
			if st.has {
				out[dev+"|"+idx] = st.stat
			}
		}
	}
	return out
}

// lastCounterSampleAt 返回每台设备最后一次 sFlow counter/processor 上报时间，
// 包括尚未积累出速率差值的首个计数器样本，供平台覆盖率使用。
func (f *flowStore) lastCounterSampleAt() map[string]int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]int64, len(f.counters)+len(f.hosts))
	for ip, counters := range f.counters {
		for _, st := range counters {
			if st.last.atMs > out[ip] {
				out[ip] = st.last.atMs
			}
		}
	}
	for ip, host := range f.hosts {
		if host == nil || host.UpdatedAt == "" {
			continue
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", host.UpdatedAt, time.Local); err == nil && t.UnixMilli() > out[ip] {
			out[ip] = t.UnixMilli()
		}
	}
	return out
}

func (f *flowStore) host(ip string) *SflowHostStat {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if h, ok := f.hosts[ip]; ok {
		cp := *h
		return &cp
	}
	return nil
}

func ipProtoName(p uint8) string {
	switch p {
	case 1:
		return "ICMP"
	case 2:
		return "IGMP"
	case 4:
		return "IPIP"
	case 6:
		return "TCP"
	case 17:
		return "UDP"
	case 41:
		return "IPv6-in-IPv4"
	case 47:
		return "GRE"
	case 50:
		return "ESP"
	case 51:
		return "AH"
	case 58:
		return "ICMPv6"
	case 88:
		return "EIGRP"
	case 89:
		return "OSPF"
	case 103:
		return "PIM"
	case 112:
		return "VRRP"
	case 124:
		return "IS-IS"
	case 132:
		return "SCTP"
	default:
		return fmt.Sprintf("proto-%d", p)
	}
}

// startFlowCollectors 按配置启动 NetFlow v5 / sFlow v5 的 UDP 监听。
// allow 为空表示接受任何来源, 非空时只接受列表内 IP 的数据包(复用跟
// syslog 一样的"当前设备库里的 IP"白名单, 避免网络里随便什么设备都能
// 把流量记录灌进来占内存)。
func startFlowCollectors(cfg *Config, store *flowStore, allow func(ip string) bool) {
	if cfg.Collectors.NetflowPort > 0 {
		startNetflowServer(cfg.Collectors.NetflowPort, store, allow)
	}
	if cfg.Collectors.SflowPort > 0 {
		startSflowServer(cfg.Collectors.SflowPort, store, allow)
	}
}

// ----------------- NetFlow v5 -----------------
//
// 报文格式固定(RFC 未正式发布但事实标准广泛遵循): 24 字节头 + N*48 字节
// 定长流记录, 不需要模板协商, 是三种协议里实现起来最简单的一种。

func startNetflowServer(port int, store *flowStore, allow func(ip string) bool) {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Printf("⚠️  NetFlow 接收未启动 (监听 UDP %d 失败): %v\n", port, err)
		return
	}
	fmt.Printf("📊 NetFlow v5 接收已开启: UDP %d (交换机/路由器上配置导出目标指向本机即可)\n", port)

	buf := make([]byte, 65535)
	go func() {
		defer conn.Close()
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			ip := src.IP.String()
			if allow != nil && !allow(ip) {
				continue
			}
			for _, rec := range parseNetflowV5(buf[:n], ip) {
				store.add(rec)
			}
		}
	}()
}

// parseNetflowV5 解析失败(包太短、版本不对)时静默返回空, 不影响接收循环——
// 畸形/非 NetFlow 流量不该打断服务。
func parseNetflowV5(buf []byte, deviceIP string) []FlowRecord {
	if len(buf) < 24 {
		return nil
	}
	version := binary.BigEndian.Uint16(buf[0:2])
	if version != 5 {
		return nil // v9/IPFIX 用模板协商, 这里没实现, 直接跳过而不是猜
	}
	count := int(binary.BigEndian.Uint16(buf[2:4]))
	nowT := time.Now()
	now := nowT.Format("2006-01-02 15:04:05")

	// 头部最后 2 字节是 sampling_interval: 高 2 位是抽样模式, 低 14 位是
	// 抽样间隔 N(1/N)。路由器上开了抽样 NetFlow(ip flow-sampling / sampler)
	// 时, 导出的 dPkts/dOctets 只是被抽中的那部分, 跟 sFlow 一样要乘回 N;
	// 没开抽样时这个字段是 0。之前把 NetFlow 当成"一定是全量"处理, 没看这个字段。
	scale := uint64(1)
	if n := uint64(binary.BigEndian.Uint16(buf[22:24]) & 0x3fff); n > 1 {
		scale = n
	}
	samplingPct := 100
	if scale > 1 {
		samplingPct = int(100 / scale)
		if samplingPct <= 0 {
			samplingPct = 1
		}
	}

	const hdrLen, recLen = 24, 48
	need := hdrLen + count*recLen
	if count <= 0 || count > 30 || len(buf) < need {
		return nil
	}

	out := make([]FlowRecord, 0, count)
	for i := 0; i < count; i++ {
		o := hdrLen + i*recLen
		rec := buf[o : o+recLen]
		out = append(out, FlowRecord{
			Time:        now,
			DeviceIP:    deviceIP,
			SrcIP:       net.IP(rec[0:4]).String(),
			DstIP:       net.IP(rec[4:8]).String(),
			InIfIndex:   fmt.Sprintf("%d", binary.BigEndian.Uint16(rec[12:14])),
			OutIfIndex:  fmt.Sprintf("%d", binary.BigEndian.Uint16(rec[14:16])),
			Packets:     uint64(binary.BigEndian.Uint32(rec[16:20])) * scale,
			Bytes:       uint64(binary.BigEndian.Uint32(rec[20:24])) * scale,
			SrcPort:     int(binary.BigEndian.Uint16(rec[32:34])),
			DstPort:     int(binary.BigEndian.Uint16(rec[34:36])),
			Protocol:    ipProtoName(rec[38]),
			SamplingPct: samplingPct,
			TS:          nowT.UnixMilli(),
			Source:      "netflow",
			IPVer:       4,
			TCPFlags:    tcpFlagsIfTCP(rec[38], rec[37]), // 整条流所有包标志位的按位或
		})
		classifyApp(&out[len(out)-1], nil)
	}
	return out
}

// ----------------- sFlow v5 -----------------
//
// XDR 编码, 4 字节对齐, TLV 风格(每个 sample/flow_record 前面都带一个
// "接下来有多少字节"的长度字段)——这是能只实现一部分结构、其余安全跳过
// 的关键。这里只提取 flow_sample/expanded_flow_sample 里 raw_packet_header
// (以太网 + IPv4 + TCP/UDP)得到的五元组; counters_sample 整段跳过。

type xdrReader struct {
	buf []byte
	pos int
}

func (r *xdrReader) remaining() int { return len(r.buf) - r.pos }

func (r *xdrReader) u32() (uint32, bool) {
	if r.remaining() < 4 {
		return 0, false
	}
	v := binary.BigEndian.Uint32(r.buf[r.pos : r.pos+4])
	r.pos += 4
	return v, true
}

func (r *xdrReader) skip(n int) bool {
	if n < 0 || r.remaining() < n {
		return false
	}
	r.pos += n
	return true
}

// bytes 读取 n 个字节, 并跳过 XDR 要求的 4 字节对齐 padding。
func (r *xdrReader) bytes(n int) ([]byte, bool) {
	if n < 0 || r.remaining() < n {
		return nil, false
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	if pad := (4 - n%4) % 4; pad > 0 {
		if !r.skip(pad) {
			return nil, false
		}
	}
	return b, true
}

func (r *xdrReader) u64() (uint64, bool) {
	if r.remaining() < 8 {
		return 0, false
	}
	v := binary.BigEndian.Uint64(r.buf[r.pos : r.pos+8])
	r.pos += 8
	return v, true
}

func startSflowServer(port int, store *flowStore, allow func(ip string) bool) {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Printf("⚠️  sFlow 接收未启动 (监听 UDP %d 失败): %v\n", port, err)
		return
	}
	fmt.Printf("📊 sFlow v5 接收已开启: UDP %d (交换机上配置 sFlow agent 指向本机即可)\n", port)

	buf := make([]byte, 65535)
	go func() {
		defer conn.Close()
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			ip := src.IP.String()
			if allow != nil && !allow(ip) {
				continue
			}
			res := parseSflowV5(buf[:n], ip, store.l7Enabled())
			for _, rec := range res.flows {
				store.add(rec)
			}
			for _, c := range res.counters {
				store.addCounter(ip, c.ifIndex, c.raw)
			}
			if res.host != nil {
				store.setHost(ip, *res.host)
			}
		}
	}()
}

type sflowCounterSample struct {
	ifIndex string
	raw     ifCounterRaw
}

// sflowResult 是一个 sFlow 数据报解出来的全部内容: 抽样报文(流记录)、
// 接口计数器、设备 CPU/内存, 三者可能同时出现在同一个数据报里。
type sflowResult struct {
	flows    []FlowRecord
	counters []sflowCounterSample
	host     *SflowHostStat
}

// parseSflowV5 同样遵循"看不懂就跳过, 不让一条畸形/不认识的子结构拖垮
// 整包解析"的原则; 任何一步长度对不上就直接放弃这个数据包剩余部分,
// 已经解出来的记录仍然返回。l7=false 时不解析应用层域名(见 flowStore.l7)。
func parseSflowV5(buf []byte, deviceIP string, l7 bool) (res sflowResult) {
	defer func() { _ = recover() }() // XDR 里全是手动切片偏移, 防御性兜底不让畸形包搞崩采集协程

	r := &xdrReader{buf: buf}
	version, ok := r.u32()
	if !ok || version != 5 {
		return res
	}
	addrType, ok := r.u32()
	if !ok {
		return res
	}
	agentLen := 4
	if addrType == 2 { // IPv6
		agentLen = 16
	}
	if _, ok := r.bytes(agentLen); !ok {
		return res
	}
	if _, ok := r.u32(); !ok { // sub_agent_id
		return res
	}
	if _, ok := r.u32(); !ok { // sequence_number
		return res
	}
	if _, ok := r.u32(); !ok { // uptime
		return res
	}
	numSamples, ok := r.u32()
	if !ok {
		return res
	}
	nowT := time.Now()

	for i := uint32(0); i < numSamples && r.remaining() >= 8; i++ {
		sampleType, _ := r.u32()
		sampleLen, ok := r.u32()
		if !ok || int(sampleLen) > r.remaining() {
			return res // 长度对不上, 后面数据不可信, 就此打住
		}
		sampleBuf := r.buf[r.pos : r.pos+int(sampleLen)]
		r.pos += int(sampleLen)
		if pad := (4 - int(sampleLen)%4) % 4; pad > 0 {
			r.skip(pad)
		}
		if sampleType>>12 != 0 {
			continue // 厂商私有 enterprise 的 sample, 不认识
		}
		switch sampleType & 0xfff {
		case 1, 3: // flow_sample / expanded_flow_sample
			res.flows = append(res.flows, parseSflowFlowSample(sampleBuf, sampleType&0xfff == 3, deviceIP, nowT, l7)...)
		case 2, 4: // counters_sample / expanded_counters_sample
			cs, host := parseSflowCounterSample(sampleBuf, sampleType&0xfff == 4, nowT)
			res.counters = append(res.counters, cs...)
			if host != nil {
				res.host = host
			}
		}
	}
	return res
}

// parseSflowFlowSample 解析 flow_sample(format=1) / expanded_flow_sample(format=3)。
// 一个 sample 里可能同时带几种 flow_record: raw_packet_header(原始报文头,
// 信息最全)、sampled_ipv4/ipv6(设备已经替你解好的五元组, 有的设备只发这个)、
// extended_switch(进出 VLAN)。这里合并成一条 FlowRecord。
func parseSflowFlowSample(buf []byte, expanded bool, deviceIP string, nowT time.Time, l7 bool) []FlowRecord {
	r := &xdrReader{buf: buf}
	if _, ok := r.u32(); !ok { // sequence_number
		return nil
	}
	if expanded {
		if _, ok := r.u32(); !ok { // source_id_type
			return nil
		}
		if _, ok := r.u32(); !ok { // source_id_index
			return nil
		}
	} else {
		if _, ok := r.u32(); !ok { // source_id (type+index 打包在一个字段里)
			return nil
		}
	}
	samplingRate, ok := r.u32()
	if !ok {
		return nil
	}
	if _, ok := r.u32(); !ok { // sample_pool
		return nil
	}
	if _, ok := r.u32(); !ok { // drops
		return nil
	}
	var inIdx, outIdx uint32
	if expanded {
		r.u32() // input type
		inIdx, _ = r.u32()
		r.u32() // output type
		outIdx, ok = r.u32()
	} else {
		inIdx, _ = r.u32()
		outIdx, ok = r.u32()
	}
	if !ok {
		return nil
	}
	// 非 expanded 格式里接口字段高 2 位是格式标记(0=ifIndex, 1=丢弃,
	// 2=多个接口), 只取低 30 位才是 ifIndex。
	if !expanded {
		inIdx &= 0x3fffffff
		outIdx &= 0x3fffffff
	}
	numRecords, ok := r.u32()
	if !ok {
		return nil
	}

	samplingPct := 100
	if samplingRate > 1 {
		samplingPct = int(100 / samplingRate)
		if samplingPct <= 0 {
			samplingPct = 1
		}
	}
	// sFlow 是统计抽样(比如 1/1000 采一个包), 上报的每条 flow_record 只是
	// "刚好抽中的那一个包"。sFlow 协议设计的本意就是"用抽样结果乘回抽样率
	// 来估算真实流量", 统计口径才跟真实流量对得上; SamplingPct 仍然保留
	// 在记录里, 前端可以提示"这是估算值"。
	scale := uint64(samplingRate)
	if scale < 1 {
		scale = 1
	}

	var rec FlowRecord
	have := false
	var srcVlan, dstVlan uint32
	for i := uint32(0); i < numRecords && r.remaining() >= 8; i++ {
		dataFormat, _ := r.u32()
		dataLen, ok := r.u32()
		if !ok || int(dataLen) > r.remaining() {
			break
		}
		data := r.buf[r.pos : r.pos+int(dataLen)]
		r.pos += int(dataLen)
		if pad := (4 - int(dataLen)%4) % 4; pad > 0 {
			r.skip(pad)
		}
		if dataFormat>>12 != 0 {
			continue
		}
		switch dataFormat & 0xfff {
		case 1: // raw_packet_header: 优先级最高, 覆盖 sampled_ipv4/6 的结果
			if hr, ok := parseSflowRawPacketHeader(data, l7); ok {
				if have && rec.VLAN != 0 && hr.VLAN == 0 {
					hr.VLAN = rec.VLAN
				}
				rec, have = hr, true
			}
		case 3, 4: // sampled_ipv4 / sampled_ipv6
			if !have {
				if sr, ok := parseSflowSampledIP(data, dataFormat&0xfff == 4); ok {
					rec, have = sr, true
				}
			}
		case 1001: // extended_switch
			er := &xdrReader{buf: data}
			srcVlan, _ = er.u32()
			er.u32() // src_priority
			dstVlan, _ = er.u32()
		}
	}
	if !have {
		return nil
	}
	if rec.VLAN == 0 {
		if srcVlan > 0 && srcVlan < 4096 {
			rec.VLAN = int(srcVlan)
		} else if dstVlan > 0 && dstVlan < 4096 {
			rec.VLAN = int(dstVlan)
		}
	}
	rec.Time, rec.TS, rec.DeviceIP, rec.Source = nowT.Format("2006-01-02 15:04:05"), nowT.UnixMilli(), deviceIP, "sflow"
	rec.InIfIndex = fmt.Sprintf("%d", inIdx)
	rec.OutIfIndex = fmt.Sprintf("%d", outIdx)
	rec.SamplingPct = samplingPct
	rec.SampleN = uint32(scale)
	rec.Bytes *= scale  // 按抽样率折算回估算的真实字节数
	rec.Packets = scale // 这一个抽样包代表大约 scale 个真实包
	return []FlowRecord{rec}
}

// parseSflowSampledIP 解析 sampled_ipv4(format=3) / sampled_ipv6(format=4):
// 设备已经把五元组解好了, 没有二层信息也没有报文内容(做不了 L7 识别)。
func parseSflowSampledIP(buf []byte, v6 bool) (FlowRecord, bool) {
	r := &xdrReader{buf: buf}
	length, ok := r.u32()
	if !ok {
		return FlowRecord{}, false
	}
	proto, _ := r.u32()
	alen := 4
	if v6 {
		alen = 16
	}
	src, ok1 := r.bytes(alen)
	dst, ok2 := r.bytes(alen)
	if !ok1 || !ok2 {
		return FlowRecord{}, false
	}
	sp, _ := r.u32()
	dp, _ := r.u32()
	flags, _ := r.u32()
	rec := FlowRecord{
		SrcIP: net.IP(src).String(), DstIP: net.IP(dst).String(),
		Protocol: ipProtoName(uint8(proto)), Bytes: uint64(length),
		SrcPort: int(sp), DstPort: int(dp), TCPFlags: tcpFlagsIfTCP(uint8(proto), uint8(flags)),
		IPVer: 4,
	}
	if v6 {
		rec.IPVer = 6
	}
	classifyApp(&rec, nil)
	return rec, true
}

func tcpFlagsIfTCP(proto, flags uint8) uint8 {
	if proto != 6 {
		return 0
	}
	return flags
}

func macString(b []byte) string {
	if len(b) < 6 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

// parseSflowRawPacketHeader 从 sFlow 截取的原始报文头(通常 128 字节, 可以在
// 交换机上调到 256 或更大)里解出: 二层 MAC/VLAN(含 QinQ)、IPv4/IPv6 五元组、
// TCP 标志位, l7=true 时再尝试从负载里认出 DNS 查询域名 / TLS SNI / HTTP Host。
func parseSflowRawPacketHeader(buf []byte, l7 bool) (FlowRecord, bool) {
	r := &xdrReader{buf: buf}
	headerProtocol, ok := r.u32()
	if !ok || headerProtocol != 1 { // 1 = ethernet
		return FlowRecord{}, false
	}
	frameLength, ok := r.u32()
	if !ok {
		return FlowRecord{}, false
	}
	if _, ok := r.u32(); !ok { // stripped
		return FlowRecord{}, false
	}
	headerLength, ok := r.u32()
	if !ok {
		return FlowRecord{}, false
	}
	hdr, ok := r.bytes(int(headerLength))
	if !ok || len(hdr) < 14 {
		return FlowRecord{}, false
	}

	rec := FlowRecord{Bytes: uint64(frameLength), DstMAC: macString(hdr[0:6]), SrcMAC: macString(hdr[6:12])}
	off := 12
	ethType := binary.BigEndian.Uint16(hdr[off : off+2])
	off += 2
	for ethType == 0x8100 || ethType == 0x88a8 { // 802.1Q / QinQ, 取最外层 VLAN
		if len(hdr) < off+4 {
			return FlowRecord{}, false
		}
		if rec.VLAN == 0 {
			rec.VLAN = int(binary.BigEndian.Uint16(hdr[off:off+2]) & 0x0fff)
		}
		ethType = binary.BigEndian.Uint16(hdr[off+2 : off+4])
		off += 4
	}

	var proto uint8
	var l4 []byte
	ip := hdr[off:]
	switch ethType {
	case 0x0800:
		if len(ip) < 20 {
			return FlowRecord{}, false
		}
		ihl := int(ip[0]&0x0f) * 4
		if ihl < 20 || len(ip) < ihl {
			return FlowRecord{}, false
		}
		proto = ip[9]
		rec.SrcIP, rec.DstIP, rec.IPVer = net.IP(ip[12:16]).String(), net.IP(ip[16:20]).String(), 4
		if binary.BigEndian.Uint16(ip[6:8])&0x1fff == 0 { // 非首片的分片没有 L4 头
			l4 = ip[ihl:]
		}
	case 0x86dd:
		if len(ip) < 40 {
			return FlowRecord{}, false
		}
		proto = ip[6]
		rec.SrcIP, rec.DstIP, rec.IPVer = net.IP(ip[8:24]).String(), net.IP(ip[24:40]).String(), 6
		l4 = ip[40:]
		// 跳过常见的扩展头: 逐跳(0)/路由(43)/目的选项(60)/分片(44)
		for i := 0; i < 4 && l4 != nil; i++ {
			switch proto {
			case 0, 43, 60:
				if len(l4) < 8 {
					l4 = nil
					break
				}
				n := (int(l4[1]) + 1) * 8
				proto = l4[0]
				if len(l4) < n {
					l4 = nil
				} else {
					l4 = l4[n:]
				}
			case 44:
				if len(l4) < 8 || binary.BigEndian.Uint16(l4[2:4])&0xfff8 != 0 {
					proto = l4[0]
					l4 = nil
					break
				}
				proto = l4[0]
				l4 = l4[8:]
			}
		}
	default:
		// 非 IP 的二层帧(ARP/STP/LLDP/LACP/802.1X ...)以前直接丢弃; 现在保留下来,
		// 用于发现 ARP 风暴、接入口收到 BPDU(私接交换机)等二层问题。
		rec.Protocol = l2ProtoName(ethType, hdr[off:])
		rec.AppProto = ""
		classifyApp(&rec, nil)
		return rec, true
	}
	rec.Protocol = ipProtoName(proto)
	if ethType == 0x86dd && proto == 58 {
		rec.Protocol = "ICMPv6"
	}

	var payload []byte
	switch proto {
	case 6:
		if len(l4) >= 4 {
			rec.SrcPort = int(binary.BigEndian.Uint16(l4[0:2]))
			rec.DstPort = int(binary.BigEndian.Uint16(l4[2:4]))
		}
		if len(l4) >= 14 {
			rec.TCPFlags = l4[13]
			if doff := int(l4[12]>>4) * 4; doff >= 20 && len(l4) > doff {
				payload = l4[doff:]
			}
		}
	case 17:
		if len(l4) >= 4 {
			rec.SrcPort = int(binary.BigEndian.Uint16(l4[0:2]))
			rec.DstPort = int(binary.BigEndian.Uint16(l4[2:4]))
		}
		if len(l4) > 8 {
			payload = l4[8:]
		}
	}
	if l7 && len(payload) > 0 {
		detectL7(&rec, payload)
	}
	classifyApp(&rec, payload)
	return rec, true
}

// ----------------- 轻量级 L7 识别 -----------------
//
// sFlow 只截取报文前 128~256 字节, 但这已经足够覆盖三种最有价值的应用层
// 信息, 不需要旁路部署 DPI:
//   - DNS 查询域名: 查询包很短, 基本都能完整截到
//   - TLS ClientHello 里的 SNI: HTTPS 访问的域名(明文); ClientHello 前面有
//     随机数/会话 ID/密码套件列表, SNI 往往在负载 100~250 字节处, 交换机上
//     报文头截取长度只有 128 字节时大多截不到——这种情况标成"TLS(SNI 被截断)",
//     页面上提示去调大截取长度
//   - HTTP 请求行和 Host 头: 明文 HTTP 的访问地址
// 只认这三种, 认不出来就什么都不填, 不影响五元组统计。

func detectL7(rec *FlowRecord, payload []byte) {
	switch rec.Protocol {
	case "UDP":
		if rec.DstPort == 53 || rec.SrcPort == 53 || rec.DstPort == 5353 {
			if name, ok := parseDNSQueryName(payload); ok {
				rec.AppProto, rec.AppHost = "DNS", name
			}
		}
	case "TCP":
		if sni, isHello := parseTLSClientHello(payload); isHello {
			rec.AppProto, rec.AppHost = "TLS", sni
			return
		}
		if host, path, ok := parseHTTPRequest(payload); ok {
			rec.AppProto, rec.AppHost, rec.AppPath = "HTTP", host, path
		}
	}
}

// cleanHostname 只接受看起来像域名的字符串, 防止把二进制垃圾当域名展示。
func cleanHostname(b []byte) (string, bool) {
	if len(b) == 0 || len(b) > 253 {
		return "", false
	}
	for _, c := range b {
		ok := c == '.' || c == '-' || c == '_' || c == ':' ||
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return "", false
		}
	}
	return strings.ToLower(strings.TrimSuffix(string(b), ".")), true
}

// parseDNSQueryName 取 DNS 报文 question 段的第一个查询名(请求和应答都带)。
func parseDNSQueryName(p []byte) (string, bool) {
	if len(p) < 13 || binary.BigEndian.Uint16(p[4:6]) == 0 { // QDCOUNT
		return "", false
	}
	pos := 12
	var labels []string
	total := 0
	for {
		if pos >= len(p) {
			return "", false
		}
		l := int(p[pos])
		if l == 0 {
			break
		}
		if l&0xc0 != 0 { // question 段里出现压缩指针不正常, 放弃
			return "", false
		}
		pos++
		if pos+l > len(p) {
			return "", false
		}
		label, ok := cleanHostname(p[pos : pos+l])
		if !ok || strings.Contains(label, ".") {
			return "", false
		}
		labels = append(labels, label)
		total += l + 1
		if total > 253 {
			return "", false
		}
		pos += l
	}
	if len(labels) == 0 {
		return "", false
	}
	return strings.Join(labels, "."), true
}

// parseTLSClientHello: isHello 表示负载确实是 TLS ClientHello(不管 SNI
// 有没有被截断); sni 为空说明被截断或客户端没带 SNI。
func parseTLSClientHello(p []byte) (sni string, isHello bool) {
	// record: type(1)=22 handshake, version(2)=0x03xx, length(2)
	// handshake: type(1)=1 ClientHello, length(3)
	if len(p) < 6 || p[0] != 0x16 || p[1] != 0x03 || p[5] != 0x01 {
		return "", false
	}
	pos := 5 + 4 + 2 + 32 // record 头 + handshake 头 + client_version + random
	if pos >= len(p) {
		return "", true
	}
	pos += 1 + int(p[pos]) // session_id
	if pos+2 > len(p) {
		return "", true
	}
	pos += 2 + int(binary.BigEndian.Uint16(p[pos:pos+2])) // cipher_suites
	if pos >= len(p) {
		return "", true
	}
	pos += 1 + int(p[pos]) // compression_methods
	if pos+2 > len(p) {
		return "", true
	}
	end := pos + 2 + int(binary.BigEndian.Uint16(p[pos:pos+2]))
	pos += 2
	for pos+4 <= len(p) && pos+4 <= end {
		extType := binary.BigEndian.Uint16(p[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(p[pos+2 : pos+4]))
		pos += 4
		if extType != 0 { // 0 = server_name
			pos += extLen
			continue
		}
		// server_name_list: list_len(2) name_type(1)=0 name_len(2) name
		if pos+5 > len(p) || p[pos+2] != 0 {
			return "", true
		}
		nameLen := int(binary.BigEndian.Uint16(p[pos+3 : pos+5]))
		if pos+5+nameLen > len(p) {
			return "", true
		}
		if host, ok := cleanHostname(p[pos+5 : pos+5+nameLen]); ok {
			return host, true
		}
		return "", true
	}
	return "", true
}

var httpMethods = []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT "}

// parseHTTPRequest 认明文 HTTP 请求: 返回 Host 头(可能被截断而为空)和
// "方法 URI前缀"。
func parseHTTPRequest(p []byte) (host, path string, ok bool) {
	method := ""
	for _, m := range httpMethods {
		if len(p) >= len(m) && string(p[:len(m)]) == m {
			method = m
			break
		}
	}
	if method == "" {
		return "", "", false
	}
	line := p
	if i := indexCRLF(p); i >= 0 {
		line = p[:i]
	}
	uri := string(line[len(method):])
	if i := strings.Index(uri, " HTTP/"); i >= 0 {
		uri = uri[:i]
	}
	if len(uri) > 80 {
		uri = uri[:80] + "…"
	}
	path = strings.TrimSpace(method) + " " + strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return -1
		}
		return r
	}, uri)

	lower := strings.ToLower(string(p))
	if i := strings.Index(lower, "\r\nhost:"); i >= 0 {
		v := p[i+len("\r\nhost:"):]
		if j := indexCRLF(v); j >= 0 {
			v = v[:j]
		} else {
			v = nil // 行没结束, 说明 Host 被截断了
		}
		if h, ok := cleanHostname([]byte(strings.TrimSpace(string(v)))); ok {
			host = h
		}
	}
	return host, path, true
}

func indexCRLF(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

// parseSflowCounterSample 解析 counters_sample(format=2) / expanded(format=4),
// 只取 generic_interface_counters(1) 和 processor_information(1001) 两种记录。
func parseSflowCounterSample(buf []byte, expanded bool, nowT time.Time) ([]sflowCounterSample, *SflowHostStat) {
	r := &xdrReader{buf: buf}
	if _, ok := r.u32(); !ok { // sequence_number
		return nil, nil
	}
	if expanded {
		r.u32() // source_id_type
		r.u32() // source_id_index
	} else {
		r.u32() // source_id
	}
	n, ok := r.u32()
	if !ok {
		return nil, nil
	}
	var out []sflowCounterSample
	var host *SflowHostStat
	for i := uint32(0); i < n && r.remaining() >= 8; i++ {
		format, _ := r.u32()
		l, ok := r.u32()
		if !ok || int(l) > r.remaining() {
			break
		}
		data := r.buf[r.pos : r.pos+int(l)]
		r.pos += int(l)
		if pad := (4 - int(l)%4) % 4; pad > 0 {
			r.skip(pad)
		}
		if format>>12 != 0 {
			continue
		}
		switch format & 0xfff {
		case 1:
			if c, ok := parseGenericIfCounters(data, nowT); ok {
				out = append(out, c)
			}
		case 1001:
			hr := &xdrReader{buf: data}
			c5s, _ := hr.u32()
			c1m, _ := hr.u32()
			c5m, _ := hr.u32()
			total, _ := hr.u64()
			free, ok := hr.u64()
			if !ok {
				continue
			}
			pct := func(v uint32) float64 {
				if int32(v) < 0 {
					return -1
				}
				return float64(int32(v)) / 100 // sFlow percentage: 百分之一个百分点为单位
			}
			host = &SflowHostStat{
				CPU5s: pct(c5s), CPU1m: pct(c1m), CPU5m: pct(c5m),
				MemTotal: total, MemFree: free, UpdatedAt: nowT.Format("2006-01-02 15:04:05"),
			}
		}
	}
	return out, host
}

func parseGenericIfCounters(data []byte, nowT time.Time) (sflowCounterSample, bool) {
	r := &xdrReader{buf: data}
	ifIndex, _ := r.u32()
	r.u32() // ifType
	speed, _ := r.u64()
	r.u32() // ifDirection
	status, _ := r.u32()
	var c ifCounterRaw
	c.speed = speed
	c.operUp = status&0x2 != 0
	c.inOct, _ = r.u64()
	c.inUc, _ = r.u32()
	c.inMc, _ = r.u32()
	c.inBc, _ = r.u32()
	c.inDisc, _ = r.u32()
	c.inErr, _ = r.u32()
	r.u32() // ifInUnknownProtos
	c.outOct, _ = r.u64()
	c.outUc, _ = r.u32()
	c.outMc, _ = r.u32()
	c.outBc, _ = r.u32()
	c.outDisc, _ = r.u32()
	var ok bool
	c.outErr, ok = r.u32()
	if !ok || ifIndex == 0 {
		return sflowCounterSample{}, false
	}
	c.atMs = nowT.UnixMilli()
	return sflowCounterSample{ifIndex: fmt.Sprintf("%d", ifIndex), raw: c}, true
}
