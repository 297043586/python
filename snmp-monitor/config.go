package main

// 配置模型: config.json 的结构、缺省值、旧版字段迁移。

import (
	"encoding/json"
)

// ----------------- 配置模型 -----------------

type Config struct {
	WebhookURL           string `json:"webhook_url"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	PortSpeedThresholdMB int    `json:"port_speed_threshold_mb"`
	WebPort              int    `json:"web_port"`
	SNMPPort             int    `json:"snmp_port"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
	Retries              int    `json:"retries"`
	MaxConcurrency       int    `json:"max_concurrency"`
	TrendPoints          int    `json:"trend_points"`
	UplinkMACThreshold   int    `json:"uplink_mac_threshold"`
	MaxRepetitions       int    `json:"max_repetitions"` // 单次 GETBULK 批大小, 老旧/低性能设备调小防止超时

	// Alerts: 每一类告警独立结构化, 而不是散在顶层的裸阈值数字——想临时
	// 关掉某一类告警(比如某个环境光衰误报特别多、暂时不想被打扰)之前只能
	// 把阈值调成不可能触发的极端值, 很别扭。现在每类都带一个 Disabled 开关。
	// 跟下面 CollectOptions 的 Skip* 是同一个惯例: 零值(Disabled=false)
	// 等价于"开启", 老配置没写这段时行为完全不变。
	Alerts AlertsConfig `json:"alerts"`

	// Collectors: 三个"被动接收"型采集器(交换机主动把数据推过来, 不是本机
	// 轮询), 统一收进一个对象里——之前是摊在 Config 顶层的三个裸端口字段,
	// 每次要加白名单/缓冲区大小之类的新选项都得继续摊平, 现在先把结构定下来。
	// 语义不变: port<=0 表示不启用。
	Collectors CollectorsConfig `json:"collectors"`

	BindAddress         string `json:"bind_address"`          // 监听地址, 默认 0.0.0.0
	DataDir             string `json:"data_dir"`              // 持久化目录, 默认 ./data
	WalkParallel        int    `json:"walk_parallel"`         // 单台设备内部并行的 SNMP 会话数
	HeavyEveryNRounds   int    `json:"heavy_every_n_rounds"`  // MAC/ARP 大表每 N 轮采一次
	TrendRetentionHours int    `json:"trend_retention_hours"` // 趋势数据保留时长
	AlertHistoryDays    int    `json:"alert_history_days"`    // 告警历史保留天数(v13), 见 alert_history.go
	AdminUser           string `json:"admin_user"`            // 管理员用户名
	SessionHours        int    `json:"session_hours"`         // 登录会话有效期

	// 数据保留/大小上限(v14): 以前这几个是写死在代码里的常量, 设备多、
	// 日志/流量大的场合经常想调大或调小, 现在挪到系统设置里可以直接改。
	SyslogMaxLines         int `json:"syslog_max_lines"`          // 每台设备最多保留多少条 syslog, 立即生效
	FlowRingSize           int `json:"flow_ring_size"`            // 每台设备流量记录环形缓冲大小, 重启生效
	AlertHistoryMaxRecords int `json:"alert_history_max_records"` // 告警历史总条数硬上限(超过按时间顺序丢最老的), 立即生效

	// VlanCommunityFormat: 形如 "%s@%s" (原团体名, VLAN 号)。部分设备只有
	// 切到对应 VLAN 的团体名才能看全 MAC 表, 配了这项才会在还有"未定位"
	// 记录时做补充查询; 语法因厂商而异, 默认关闭。可被设备自己的同名字段覆盖。
	VlanCommunityFormat string `json:"vlan_community_format"`

	// UI: 页面显示参数(大屏样式/刷新间隔、3D 默认开关等), 见 settings.go。
	UI UISettings `json:"ui"`

	// BackupCommands: 配置备份的各厂商默认命令(huawei/h3c/ruijie/cisco/generic),
	// 每行一条, 最后一条的输出作为配置。不写用内置默认值。
	BackupCommands map[string]string `json:"backup_commands"`

	// devices 只在首次启动时导入设备库, 之后以 data/devices.json 为准,
	// 这样在线增删的节点不会被配置文件覆盖回去。
	Devices []DeviceTarget `json:"devices"`
}

// ThresholdAlert 是一类告警的开关+阈值。Disabled=false(零值)是"开启",
// 跟 CollectOptions 的 Skip* 同一个惯例, 见 Config.Alerts 上的注释。
type ThresholdAlert struct {
	Disabled  bool    `json:"disabled"`
	Threshold float64 `json:"threshold"`
}

type AlertsConfig struct {
	CPU       ThresholdAlert `json:"cpu"`        // Threshold: CPU 百分比, >= 触发 warning(>=90 恒定触发 critical, 除非本类被 Disabled)
	Mem       ThresholdAlert `json:"mem"`        // Threshold: 内存百分比
	Optical   ThresholdAlert `json:"optical"`    // Threshold: 收光 dBm, 是负数, <= 触发
	PortError ThresholdAlert `json:"port_error"` // Threshold: 单轮错包增量, >= 触发
	Bandwidth ThresholdAlert `json:"bandwidth"`  // Threshold: 端口带宽利用率百分比, >= 触发
	Broadcast ThresholdAlert `json:"broadcast"`  // Threshold: 端口入方向广播包/秒, >= 触发 warning, 5 倍以上 critical
	STP       ThresholdAlert `json:"stp"`        // Threshold: 两轮巡检之间生成树拓扑变化次数, >= 触发(5 倍以上 critical)
}

// CollectorsConfig: 三个被动接收型采集器统一用 port 表示监听端口, <=0 = 不启用。
type CollectorsConfig struct {
	SyslogPort  int `json:"syslog_port"`  // 标准 514 需要 root/CAP_NET_BIND_SERVICE, 也可用 1514 之类的高位端口再在交换机侧配置对应端口
	NetflowPort int `json:"netflow_port"` // NetFlow v5 收集端口, 常见 2055/9995
	SflowPort   int `json:"sflow_port"`   // sFlow v5 收集端口, 常见 6343

	// FlowL7: 是否从 sFlow 报文头里解析 DNS 域名/TLS SNI/HTTP Host。
	// 只决定首次启动时的默认值(不写 = 开启), 之后以页面上的开关为准
	// (状态存在 data/flow_settings.json)。
	FlowL7 *bool `json:"flow_l7,omitempty"`
}

type DeviceTarget struct {
	ID                  string `json:"id"`
	IP                  string `json:"ip"`
	Community           string `json:"community"`
	Note                string `json:"note"`
	Group               string `json:"group"`              // 分组(比如 楼栋 / 区域 / 业务), 首页按它分组显示
	Location            string `json:"location,omitempty"` // 手工填的机房/机柜位置, 优先于 SNMP 上报的 sysLocation
	Contact             string `json:"contact,omitempty"`  // 手工填的负责人/联系方式, 优先于 SNMP 上报的 sysContact
	Disabled            bool   `json:"disabled"`
	VlanCommunityFormat string `json:"vlan_community_format"` // 覆盖全局设置, 留空则用全局值
	// ruijie / h3c / huawei; 留空或写 auto 时按 sysObjectID 自动识别
	Brand string `json:"brand"`
	// "2c"(默认) 或 "1"
	Version string `json:"version"`

	// Collect 决定这台设备要采集哪些可选数据。CPU/内存/电源/风扇/端口
	// UP-DOWN 状态是固定项, 任何配置都会采; 光衰、LLDP、端口速率/错包、
	// MAC-ARP 定位这些开销更大的表则按需勾选 —— 对低端/老旧设备尤其重要,
	// 少轮询几张大表能显著降低它的 CPU 占用和超时概率。
	Collect CollectOptions `json:"collect"`
}

// CollectOptions 的零值(全 false)必须等价于"全部采集", 这样从旧版本
// devices.json(没有 collect 字段)升级上来的设备不会突然被关掉所有数据。
// 因此字段语义是反的 —— Skip* = true 才是"不采集"; DeviceStore.load()
// 负责把这份对内使用的字段渲染成前端友好的正向勾选框。
type CollectOptions struct {
	SkipOptical bool `json:"skip_optical"` // 跳过光衰 (Rx/Tx dBm)
	SkipLLDP    bool `json:"skip_lldp"`    // 跳过 LLDP 邻居
	SkipRates   bool `json:"skip_rates"`   // 跳过端口速率/利用率/错包计数(仍会采 UP/DOWN)
	SkipHosts   bool `json:"skip_hosts"`   // 跳过 MAC 表/ARP/IP-MAC 定位

	// SkipExtended 跳过设备详情里的扩展信息: 端口丢包/出错包/广播组播
	// 速率/双工/状态持续时间, 以及机框型号/序列号/软件版本/位置/联系人。
	// 多出来 7~8 张小表, 低端设备可以关掉。
	SkipExtended bool `json:"skip_extended"`
}

// stateKey 是设备在内存状态表里的唯一标识。
// 只用 IP 会让"同一 IP 的多个采集实例"(例如同一台设备配了不同 community,
// 或做端口映射的多套环境)共用一份速率/错包基线, 相减后得到天文数字的假增量。
func (d DeviceTarget) stateKey() string {
	if d.ID != "" {
		return d.ID
	}
	return d.IP + "|" + d.Community
}

// applyDefaults 填补缺省值。旧版本没有做这件事, 配置里少写 cpu_threshold 时
// 阈值为 0, 于是 "CPUUsage >= 0" 恒成立, 每台设备每轮都会刷一条假告警。
func (c *Config) applyDefaults() {
	if c.Alerts.CPU.Threshold <= 0 || c.Alerts.CPU.Threshold > 100 {
		c.Alerts.CPU.Threshold = 80
	}
	if c.Alerts.Mem.Threshold <= 0 || c.Alerts.Mem.Threshold > 100 {
		c.Alerts.Mem.Threshold = 85
	}
	if c.Alerts.PortError.Threshold <= 0 {
		c.Alerts.PortError.Threshold = 10
	}
	if c.Alerts.Optical.Threshold == 0 {
		c.Alerts.Optical.Threshold = -25
	}
	if c.Alerts.Bandwidth.Threshold <= 0 || c.Alerts.Bandwidth.Threshold > 100 {
		c.Alerts.Bandwidth.Threshold = 90
	}
	if c.Alerts.Broadcast.Threshold <= 0 {
		// 正常办公网一个接入口的广播(ARP/DHCP/NetBIOS)通常几十包/秒;
		// 上千包/秒持续出现基本就是环路或有终端在疯狂发广播。
		c.Alerts.Broadcast.Threshold = 1000
	}
	if c.Alerts.STP.Threshold <= 0 {
		c.Alerts.STP.Threshold = 3
	}
	if c.CheckIntervalSeconds < 30 {
		c.CheckIntervalSeconds = 60
	}
	if c.WebPort <= 0 || c.WebPort > 65535 {
		c.WebPort = 8080
	}
	if c.SNMPPort <= 0 || c.SNMPPort > 65535 {
		c.SNMPPort = 161
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 5
	}
	if c.Retries < 0 || c.Retries > 5 {
		c.Retries = 2
	}
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 30
	}
	if c.TrendPoints <= 1 {
		c.TrendPoints = 60
	}
	if c.UplinkMACThreshold <= 0 {
		c.UplinkMACThreshold = 8
	}
	if c.BindAddress == "" {
		c.BindAddress = "0.0.0.0"
	}
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.WalkParallel <= 0 || c.WalkParallel > 16 {
		// 4 条并行会话对多数设备是安全的, 但部分低端/老旧型号的管理 CPU
		// 本来就弱, 4 路 BulkWalk 同时砸过去会导致它开始丢包超时。
		// 3 是观察下来兼顾速度和稳妥的折中值, 扛不住的设备可以在
		// config.json 里针对性调到 1~2。
		c.WalkParallel = 3
	}
	if c.HeavyEveryNRounds <= 0 {
		c.HeavyEveryNRounds = 1
	}
	if c.TrendRetentionHours <= 0 {
		c.TrendRetentionHours = 24
	}
	if c.AlertHistoryDays <= 0 {
		c.AlertHistoryDays = 180
	}
	if c.AdminUser == "" {
		c.AdminUser = "admin"
	}
	if c.SessionHours <= 0 {
		c.SessionHours = 12
	}
	if c.SyslogMaxLines <= 0 {
		c.SyslogMaxLines = 300
	}
	if c.FlowRingSize <= 0 {
		c.FlowRingSize = 5000
	}
	if c.AlertHistoryMaxRecords <= 0 {
		c.AlertHistoryMaxRecords = 300000
	}
	// SyslogPort 默认关闭(0), 不强行占用 514 —— 多数环境下这个端口需要 root。
	// 想用的话在 config.json 里显式写 "syslog_port": 514 或选一个不需要特权的端口。
	if c.MaxRepetitions <= 0 || c.MaxRepetitions > 200 {
		// 50 在弱 CPU 的老交换机上是常见的超时诱因; GETBULK 一次问 50 个
		// OID, 对方 CPU 来不及在超时窗口内拼完响应包就直接判超时。
		// 20 更保守, 性能富余的设备可在配置里调大。
		c.MaxRepetitions = 20
	}
	c.UI.applyDefaults()
	c.BackupCommands = applyBackupCommandDefaults(c.BackupCommands)
	for i := range c.Devices {
		if c.Devices[i].Community == "" {
			c.Devices[i].Community = "public"
		}
	}
}

// legacyConfigFields 是 v6 结构化之前 Config 顶层的裸字段名, 只用来在
// migrateLegacyConfig 里做一次性迁移, 不再是 Config 本身的一部分。
type legacyConfigFields struct {
	CPUThreshold        int     `json:"cpu_threshold"`
	MemThreshold        int     `json:"mem_threshold"`
	ErrorDeltaThreshold int64   `json:"error_delta_threshold"`
	OpticalRxWarnDbm    float64 `json:"optical_rx_warn_dbm"`
	SyslogPort          int     `json:"syslog_port"`
	NetflowPort         int     `json:"netflow_port"`
	SflowPort           int     `json:"sflow_port"`
}

// migrateLegacyConfig 把 v6 之前摊在顶层的裸字段(cpu_threshold/syslog_port
// 等)搬进新的 Alerts/Collectors 结构里。
//
// 背景: v6 把这些字段从 Config 顶层挪进了嵌套结构, 老的 config.json 里这些
// 裸字段会被 json.Unmarshal 直接忽略(未知字段不报错), 如果不做迁移, 一台
// 已经配了 "syslog_port": 514 的设备升级后会在没有任何报错的情况下被静默
// 关掉 syslog 接收, 用户很难第一时间发现。只在新字段还是零值(=没有用新格式
// 显式配置过)时才用旧值填充, 避免覆盖用户已经改成新格式的配置。
func migrateLegacyConfig(raw []byte, c *Config) {
	var legacy legacyConfigFields
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return // 旧字段解不出来就算了, 不影响新格式字段已经生效的解析结果
	}
	if c.Alerts.CPU.Threshold == 0 && legacy.CPUThreshold != 0 {
		c.Alerts.CPU.Threshold = float64(legacy.CPUThreshold)
	}
	if c.Alerts.Mem.Threshold == 0 && legacy.MemThreshold != 0 {
		c.Alerts.Mem.Threshold = float64(legacy.MemThreshold)
	}
	if c.Alerts.PortError.Threshold == 0 && legacy.ErrorDeltaThreshold != 0 {
		c.Alerts.PortError.Threshold = float64(legacy.ErrorDeltaThreshold)
	}
	if c.Alerts.Optical.Threshold == 0 && legacy.OpticalRxWarnDbm != 0 {
		c.Alerts.Optical.Threshold = legacy.OpticalRxWarnDbm
	}
	if c.Collectors.SyslogPort == 0 && legacy.SyslogPort != 0 {
		c.Collectors.SyslogPort = legacy.SyslogPort
	}
	if c.Collectors.NetflowPort == 0 && legacy.NetflowPort != 0 {
		c.Collectors.NetflowPort = legacy.NetflowPort
	}
	if c.Collectors.SflowPort == 0 && legacy.SflowPort != 0 {
		c.Collectors.SflowPort = legacy.SflowPort
	}
}
