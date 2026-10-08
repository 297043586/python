package main

// 数据模型: 一台设备一轮巡检的结果(端口 / 聚合 / MAC / ARP / 告警 / 趋势)。

import (
	"sync"
)

// ----------------- 数据模型 -----------------

type HardwareStatus struct {
	FanStatus   string `json:"fan_status"`
	PowerStatus string `json:"power_status"`
	Temperature string `json:"temperature"`
}

type PortDetail struct {
	Index           string   `json:"index"`
	Name            string   `json:"name"`
	Alias           string   `json:"alias"`
	Status          string   `json:"status"` // UP / DOWN / ADMIN-DOWN
	SpeedMbps       int64    `json:"speed_mbps"`
	InSpeedMbps     float64  `json:"in_speed_mbps"`
	OutSpeedMbps    float64  `json:"out_speed_mbps"`
	InUsagePercent  float64  `json:"in_usage_percent"`
	OutUsagePercent float64  `json:"out_usage_percent"`
	DeltaErrors     int64    `json:"delta_errors"`
	TotalErrors     int64    `json:"total_errors"`
	OpticalRxPower  string   `json:"optical_rx_power"`
	OpticalTxPower  string   `json:"optical_tx_power"`
	OpticalRxDbm    *float64 `json:"optical_rx_dbm"`
	OpticalTxDbm    *float64 `json:"optical_tx_dbm"`
	LLDPRemoteDev   string   `json:"lldp_remote_dev"`
	LLDPRemotePort  string   `json:"lldp_remote_port"`
	// 对端端口的另一种写法(LLDP 的 PortId / PortDesc 里没被选作 lldp_remote_port 的那个),
	// 拓扑里匹配两端端口、合并同一条物理链路时用
	LLDPRemotePortAlt string `json:"lldp_remote_port_alt,omitempty"`
	NeighborSrc       string `json:"neighbor_src,omitempty"` // 邻居来源: 空 = LLDP, "CDP"
	// LLDPRemoteIP: 对端上报的管理 IP(lldpRemManAddrTable), 不代表这台邻居
	// 已经被监控——用来在页面上给"还没监控的邻居"提供一键添加。
	LLDPRemoteIP string `json:"lldp_remote_ip,omitempty"`
	// LLDPRemoteMAC: 对端机框 MAC; LLDPRemoteIPSrc 非空 = 对端没上报管理 IP, 是按机框 MAC 从全网 ARP 查出来的
	LLDPRemoteMAC   string `json:"lldp_remote_mac,omitempty"`
	LLDPRemoteIPSrc string `json:"lldp_remote_ip_src,omitempty"`
	MACCount        int    `json:"mac_count"`
	IsUplink        bool   `json:"is_uplink"`

	// 下面三项是"有就显示, 没有就不显示"的补充信息, 不强求每个端口都有:
	Layer  string `json:"layer,omitempty"`   // "L2" 或 "L3", 判断不出来就留空
	VlanID string `json:"vlan_id,omitempty"` // Layer=="L2" 时填充(PVID)
	IPAddr string `json:"ip_addr,omitempty"` // Layer=="L3" 时填充(该口配置的 IP)

	// VLAN 成员关系(见 portinfo.go): VlanMode=access/trunk/hybrid, VlanID 是
	// access 口的 VLAN 或 trunk 口的 native VLAN(没读到成员表时退回 PVID)。
	VlanMode     string `json:"vlan_mode,omitempty"`
	VlanTagged   string `json:"vlan_tagged,omitempty"`   // 打标签放行的 VLAN, 压缩成 "10-20,30"
	VlanUntagged string `json:"vlan_untagged,omitempty"` // 不打标签的 VLAN(hybrid 口会有多个)
	VlanSource   string `json:"vlan_source,omitempty"`

	// 双工不再依赖扩展信息开关; 取不到为空。DuplexSource 说明来自哪张表或是推断。
	Duplex       string `json:"duplex,omitempty"`
	DuplexSource string `json:"duplex_source,omitempty"`

	AggGroup string `json:"agg_group,omitempty"` // 所属聚合口显示名, 不是成员则为空

	// 扩展指标(CollectOptions.SkipExtended=false 时才有), nil 表示没采集。
	Ext *PortExt `json:"ext,omitempty"`
}

// PortExt 是端口表里可开关的扩展列: 丢包/出错包/广播组播反映拥塞和环路,
// 双工不匹配是"链路 UP 但就是慢"的经典原因, 状态持续时间用来发现抖动口。
type PortExt struct {
	Duplex         string  `json:"duplex,omitempty"`   // 全双工/半双工, 取不到则空
	LastChangeSec  int64   `json:"last_change_sec"`    // 距上次 UP/DOWN 变化多少秒, -1 = 取不到
	DeltaOutErrors int64   `json:"delta_out_errors"`   // 本轮出方向错包增量
	DeltaInDiscard int64   `json:"delta_in_discards"`  // 本轮入方向丢包增量(缓冲区溢出/ACL 丢弃)
	DeltaOutDiscrd int64   `json:"delta_out_discards"` // 本轮出方向丢包增量(拥塞丢包的直接证据)
	InBcastPps     float64 `json:"in_bcast_pps"`       // 入方向广播包速率, 突增是广播风暴/环路的典型信号
	InMcastPps     float64 `json:"in_mcast_pps"`       // 入方向组播包速率
}

// DeviceInventory 是硬件页的资产信息(扩展信息开关打开时才采)。
type DeviceInventory struct {
	Model    string `json:"model,omitempty"`
	Serial   string `json:"serial,omitempty"`
	SoftVer  string `json:"soft_ver,omitempty"`
	HardVer  string `json:"hard_ver,omitempty"`
	Location string `json:"location,omitempty"`
	Contact  string `json:"contact,omitempty"`

	// ENTITY-MIB 里的机框(class=3)列表: 堆叠(IRF/iStack/VSU)时每个成员一个,
	// 框式设备只有一个。EntModules/EntSlots 是板卡/槽位数量, 用来辅助区分框式和盒式。
	Chassis    []EntChassis `json:"chassis,omitempty"`
	EntModules int          `json:"ent_modules,omitempty"`
	EntSlots   int          `json:"ent_slots,omitempty"`
}

type EntChassis struct {
	Index  string `json:"index"`
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	Serial string `json:"serial,omitempty"`
}

// AggGroupDetail 是一个聚合口(Eth-Trunk/Port-Channel/AggregatePort)及其
// 成员物理口列表。之前聚合组归属是端口表里的一列, 挤占横向空间且容易被
// 忽略; 现在拆成独立的一张小表, 直接按"聚合口 -> 成员"的自然结构展示。
type AggGroupDetail struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`

	// 聚合口自身的状态和汇总指标(v9 新增, 只有成员口名单太简单):
	Index      string   `json:"index,omitempty"`
	Status     string   `json:"status,omitempty"`      // 聚合口 ifOperStatus
	Mode       string   `json:"mode,omitempty"`        // LACP / 静态
	SpeedMbps  int64    `json:"speed_mbps"`            // UP 成员速率之和
	UpMembers  int      `json:"up_members"`            //
	InMbps     float64  `json:"in_mbps"`               // 成员入向速率之和
	OutMbps    float64  `json:"out_mbps"`              //
	InUsage    float64  `json:"in_usage"`              // 相对 UP 成员总带宽
	OutUsage   float64  `json:"out_usage"`             //
	VlanText   string   `json:"vlan_text,omitempty"`   // "trunk · native 1 · 允许 10-20"
	IPAddr     string   `json:"ip_addr,omitempty"`     //
	Alias      string   `json:"alias,omitempty"`       //
	PeerDevice string   `json:"peer_device,omitempty"` // LLDP 看到的对端设备(多个用 "、" 连接)
	PeerIP     string   `json:"peer_ip,omitempty"`     // 对端也被监控时的 IP, 页面可跳转
	PeerAgg    string   `json:"peer_agg,omitempty"`    // 对端设备上对应的聚合口(跨设备关联)
	Issues     []string `json:"issues,omitempty"`      // 一致性检查发现的问题

	MemberInfo []AggMemberDetail `json:"member_info,omitempty"`
}

// AggMemberDetail 是聚合组里一个成员口的详细状态。
type AggMemberDetail struct {
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	SpeedMbps int64    `json:"speed_mbps"`
	Duplex    string   `json:"duplex,omitempty"`
	InMbps    float64  `json:"in_mbps"`
	OutMbps   float64  `json:"out_mbps"`
	InUsage   float64  `json:"in_usage"`
	OutUsage  float64  `json:"out_usage"`
	Errors    int64    `json:"delta_errors"`
	LacpState string   `json:"lacp_state,omitempty"` // 已同步·收发中 / 对端无 LACP 响应 ...
	LacpOK    bool     `json:"lacp_ok"`
	PeerDev   string   `json:"peer_dev,omitempty"`
	PeerPort  string   `json:"peer_port,omitempty"`
	PeerAgg   string   `json:"peer_agg,omitempty"` // 对端这个口所属的聚合口(跨设备关联)
	RxDbm     *float64 `json:"rx_dbm,omitempty"`
}

type HostMappingEntry struct {
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	VlanIf    string `json:"vlan_if"`
	VlanID    string `json:"vlan_id"`
	VlanExact bool   `json:"vlan_exact"` // false = 从端口 PVID 或接口名猜测, 非 FDB 精确值
	RealPort  string `json:"real_port"`
	PortKind  string `json:"port_kind"` // 接入口 / 上联级联口 / 未定位

	// IPSource 非空时, 说明这一行的 IP 不是本机 ARP 表查到的, 而是巡检
	// 完所有设备后, 跨设备关联出来的(某台设备的 ARP 知道这个 IP, 这台
	// 设备的 MAC 表知道它的真实接入口)。取值是提供 IP 的那台设备名字。
	//
	// 这解决的是一个很实际的现象: 接入交换机通常自己完全没有 ARP 表
	// (纯二层设备, MAC 表是被动从流量里学到的, 跟这台设备是否解析过
	// 对应 IP 无关), 而核心/三层设备虽然有 ARP, 但它看到的物理端口只是
	// "通往接入交换机的上联口", 不是终端真正接的那个口。两边各知道一半,
	// 单独看哪一台都不全。
	IPSource string `json:"ip_source,omitempty"`
}

// MACEntry 是"MAC 地址总表"里的一行: 汇总并去重后的最终定位结果,
// 不要求这个 MAC 一定有对应的 IP/ARP 记录(交换机上很多终端本身
// 就不会被这台设备学到 ARP, 但 MAC 表里一定有)。
type MACEntry struct {
	MAC       string   `json:"mac"`
	VlanID    string   `json:"vlan_id"`
	Port      string   `json:"port"`
	PortKind  string   `json:"port_kind"`
	IsUplink  bool     `json:"is_uplink"`
	Ambiguous bool     `json:"ambiguous"` // 原始表里在多个端口都学到过, 这是裁决后的结果
	Locations []string `json:"locations"` // Ambiguous=true 时, 全部出现过的端口(诊断用)
}

// RouteEntry 是设备通过标准 IP-FORWARD-MIB 暴露的一条活动路由。
type RouteEntry struct {
	Destination  string `json:"destination"`
	NextHop      string `json:"next_hop"`
	IfIndex      int64  `json:"if_index,omitempty"`
	Interface    string `json:"interface"`
	Type         string `json:"type"`
	Protocol     string `json:"protocol"`
	TypeCode     int64  `json:"type_code"`     // SNMP 原始枚举值
	ProtocolCode int64  `json:"protocol_code"` // SNMP 原始枚举值
	TypeEnum     string `json:"type_enum"`     // 解码 TypeCode 所用的表(各表枚举定义不同)
	ProtocolEnum string `json:"protocol_enum"` // 解码 ProtocolCode 所用的表
	Metric       int64  `json:"metric"`        // -1 = 设备未提供
	Age          int64  `json:"age"`           // 秒; -1 = 设备未提供
}

// Alert 是一条可确认/可关闭的告警。
// 同一个故障在多轮巡检里必须保持同一个 ID, 否则用户确认过的告警下一轮又会冒出来。
type Alert struct {
	ID        string `json:"id"`
	DeviceID  string `json:"device_id"`
	DeviceIP  string `json:"device_ip"`
	Level     string `json:"level"`    // critical / warning
	Category  string `json:"category"` // cpu / mem / temp / fan / psu / port_err / optical / bandwidth / reboot / unreachable
	Subject   string `json:"subject"`  // 端口名等具体对象, 没有则为空
	Text      string `json:"text"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Acked     bool   `json:"acked"`
	AckedBy   string `json:"acked_by"`
	AckedAt   string `json:"acked_at"`
	Closed    bool   `json:"closed"` // 关闭 = 隐藏, 直到该告警恢复后再次出现

	// Recovered=true 表示本轮已经不再触发这条告警(故障已恢复), 但还在
	// 24 小时保留期内, 继续显示给用户看一眼"曾经发生过什么", 而不是一
	// 恢复就悄无声息地消失。超过保留期、或者用户手动确认/关闭后才会被
	// 彻底清掉。
	Recovered   bool   `json:"recovered"`
	RecoveredAt string `json:"recovered_at,omitempty"`
}

type TrendPoint struct {
	TS       int64   `json:"ts"`
	Time     string  `json:"time"`
	CPU      int     `json:"cpu"`
	Mem      int     `json:"mem"`
	MACCount int     `json:"mac_count"` // 本轮学到的 MAC 地址数, 用作在网终端数的代理指标
	InMbps   float64 `json:"in_mbps"`
	OutMbps  float64 `json:"out_mbps"`
}

type DeviceReport struct {
	IP             string             `json:"ip"`
	Note           string             `json:"note"`
	Group          string             `json:"group,omitempty"`
	SysName        string             `json:"sys_name"`
	SysDescr       string             `json:"sys_descr"`
	Brand          string             `json:"brand"`
	BrandDetected  string             `json:"brand_detected"`
	IsAlive        bool               `json:"is_alive"`
	UpTimeStr      string             `json:"uptime_str"`
	UpTimeSec      int64              `json:"uptime_sec,omitempty"`
	CPUUsage       int                `json:"cpu_usage"`
	MemUsage       int                `json:"mem_usage"`
	Hardware       HardwareStatus     `json:"hardware"`
	TotalPorts     int                `json:"total_ports"`
	UpPorts        int                `json:"up_ports"`
	DownPorts      int                `json:"down_ports"`
	PortUsageRatio string             `json:"port_usage_ratio"`
	Criticals      []Alert            `json:"criticals"`
	Warnings       []Alert            `json:"warnings"`
	OpenAlerts     int                `json:"open_alerts"` // 未确认的告警数
	Collect        CollectOptions     `json:"collect"`
	Diagnostics    []string           `json:"diagnostics"`
	Logs           []string           `json:"logs"`
	Ports          []PortDetail       `json:"ports"`
	AggGroups      []AggGroupDetail   `json:"agg_groups"`
	HostMappings   []HostMappingEntry `json:"host_mappings"`
	MACTable       []MACEntry         `json:"mac_table"`
	FDBTable       []MACEntry         `json:"fdb_table"` // VLAN+MAC 粒度的原始二层转发表
	FDBSource      string             `json:"fdb_source,omitempty"`
	TrendData      []TrendPoint       `json:"trend_data"`
	Timestamp      string             `json:"timestamp"`
	CostMs         int64              `json:"cost_ms"`
	Phases         []PhaseCost        `json:"phases"`
	Inventory      *DeviceInventory   `json:"inventory,omitempty"`
	STP            *STPInfo           `json:"stp,omitempty"` // 生成树拓扑变化, 见 collect_stp.go
	// OwnIPs: 设备自己所有接口(含 VLANIF/SVI)上配的 IP —— 也就是各网段的网关地址。
	// 端口表只列物理口, 看不到 VLANIF 上的 IP; 二层体检靠它识别"有终端冒充网关"。
	OwnIPs []string `json:"own_ips,omitempty"`
	// OwnAddrs: 同上但带掩码和接口名(/30、/31 是互联地址, 其余是网关地址); 老版本采集的数据没有。
	OwnAddrs []OwnIfAddr `json:"own_addrs,omitempty"`
	// OwnMACs: 设备自己的 MAC(桥 MAC + 三层口 MAC)。ARP 里这些 MAC 对应一堆 IP 是正常的(三层交换机的各个 VLANIF)。
	OwnMACs []string `json:"own_macs,omitempty"`

	stateKey string // 内部用, 不外发
}

// OwnIfAddr 是设备自己某个接口上配的一个 IP。
type OwnIfAddr struct {
	IP          string `json:"ip"`
	Prefix      int    `json:"prefix,omitempty"` // 掩码位数, 0 = 没读到
	If          string `json:"if,omitempty"`
	AdminStatus int64  `json:"admin_status,omitempty"`
	OperStatus  int64  `json:"oper_status,omitempty"`
	AdminDown   bool   `json:"admin_down,omitempty"` // 接口管理状态为 down; 该地址的 ARP 记录可能是缓存残留
	OperDown    bool   `json:"oper_down,omitempty"`  // 接口运行状态为 down; 该地址当前不会从此接口正常转发
}

// PhaseCost 记录每个采集阶段的耗时, 用于定位"第一次采集特别慢"到底慢在哪。
type PhaseCost struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

type PortSnapshot struct {
	InOctets  uint64
	OutOctets uint64
	InErrors  uint64
	HC        bool  // 是否用的是 64 位计数器
	LastSeen  int64 // Unix 秒, 便于落盘

	// InAtMs/OutAtMs 是入/出计数器各自真正被读到的时刻(Unix 毫秒)。之前
	// 速率分母用的是"整台设备采完、开始组装端口表"的时刻, 这个时刻排在
	// LLDP/MAC 表/ARP 这些慢阶段之后, 每轮耗时不一样(heavy_every_n_rounds
	// 抽样时差距尤其大), 算出来的速率跟着忽高忽低; 入/出两张表又是两个
	// 并行会话分别 walk 的, 大设备上读到的时刻能差出好几秒, 入/出利用率
	// 看起来就"不同步"。旧快照没有这两个字段(0)时退回用 LastSeen。
	InAtMs  int64 `json:",omitempty"`
	OutAtMs int64 `json:",omitempty"`

	// 扩展端口指标(见 CollectOptions.SkipExtended)的上一轮计数, 用来算增量/速率。
	OutErrors   uint64 `json:",omitempty"`
	InDiscards  uint64 `json:",omitempty"`
	OutDiscards uint64 `json:",omitempty"`
	InBcast     uint64 `json:",omitempty"`
	InMcast     uint64 `json:",omitempty"`
	ExtAtMs     int64  `json:",omitempty"`
}

var (
	stateLock      sync.RWMutex
	portHistory    = make(map[string]PortSnapshot)
	deviceTrendMap = make(map[string][]TrendPoint)
	lastCriticals  = make(map[string]map[string]bool)

	renderLock  sync.RWMutex
	latestHTML  []byte
	latestJSON  []byte
	lastRunTime string
)
