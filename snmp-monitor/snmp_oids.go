package main

// 采集用到的标准 MIB OID 和厂商私有 OID。

import ()

// ----------------- 标准 OID -----------------

const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = ".1.3.6.1.2.1.1.3.0"
	oidSysName     = ".1.3.6.1.2.1.1.5.0"

	oidIfDescr       = ".1.3.6.1.2.1.2.2.1.2"
	oidIfType        = ".1.3.6.1.2.1.2.2.1.3"
	oidIfSpeed       = ".1.3.6.1.2.1.2.2.1.5"
	oidIfAdminStatus = ".1.3.6.1.2.1.2.2.1.7"
	oidIfOperStatus  = ".1.3.6.1.2.1.2.2.1.8"
	oidIfInErrors    = ".1.3.6.1.2.1.2.2.1.14"
	oidIfOutErrors   = ".1.3.6.1.2.1.2.2.1.20"
	oidIfInOctets32  = ".1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets32 = ".1.3.6.1.2.1.2.2.1.16"

	oidIfName       = ".1.3.6.1.2.1.31.1.1.1.1"
	oidIfHCInOctets = ".1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOcts  = ".1.3.6.1.2.1.31.1.1.1.10"
	oidIfHighSpeed  = ".1.3.6.1.2.1.31.1.1.1.15"
	oidIfAlias      = ".1.3.6.1.2.1.31.1.1.1.18"

	// LLDP (IEEE 802.1AB)
	oidLldpLocPortIdSubtype = ".1.0.8802.1.1.2.1.3.7.1.2"
	oidLldpLocPortId        = ".1.0.8802.1.1.2.1.3.7.1.3"
	oidLldpLocPortDesc      = ".1.0.8802.1.1.2.1.3.7.1.4"
	oidLldpRemPortIdSubtype = ".1.0.8802.1.1.2.1.4.1.1.6"
	oidLldpRemPortId        = ".1.0.8802.1.1.2.1.4.1.1.7"
	oidLldpRemPortDesc      = ".1.0.8802.1.1.2.1.4.1.1.8"
	oidLldpRemSysName       = ".1.0.8802.1.1.2.1.4.1.1.9"
	// lldpRemManAddrTable: 对端上报的管理 IP, 不在值里, 编码在 OID 索引的最后几段
	// (…timeMark.localPortNum.remIndex.地址类型.地址长度.地址字节…), 见 collectLLDPMgmtIP。
	oidLldpRemManAddrIfSubtype = ".1.0.8802.1.1.2.1.4.2.1.2"
	oidLldpRemManAddrEntry     = ".1.0.8802.1.1.2.1.4.2.1" // 整张表: 有的设备只填 .3/.4/.5 列, 不填 .2
	oidLldpRemChassisIdSubtype = ".1.0.8802.1.1.2.1.4.1.1.4"
	oidLldpRemChassisId        = ".1.0.8802.1.1.2.1.4.1.1.5" // subtype 4 = MAC, 5 = 网络地址

	// 二层转发表
	oidDot1dBasePortIfIndex = ".1.3.6.1.2.1.17.1.4.1.2"
	oidDot1dTpFdbPort       = ".1.3.6.1.2.1.17.4.3.1.2"
	oidDot1dTpFdbStatus     = ".1.3.6.1.2.1.17.4.3.1.3"
	oidDot1qTpFdbPort       = ".1.3.6.1.2.1.17.7.1.2.2.1.2"
	oidDot1qTpFdbStatus     = ".1.3.6.1.2.1.17.7.1.2.2.1.3"

	// 三层 ARP
	oidIpNetToMediaPhysAddr  = ".1.3.6.1.2.1.4.22.1.2"
	oidIpNetToPhysicalPhysAd = ".1.3.6.1.2.1.4.35.1.4"

	// 标准 IP-FORWARD-MIB 路由表。优先读支持 IPv4/IPv6 的 inetCidrRouteTable，
	// 再回退到 IPv4 ipCidrRouteTable 和旧版 ipRouteTable。
	// RFC 4292 inetCidrRouteEntry: 7=IfIndex 8=Type 9=Proto 10=Age 12=Metric1 17=Status。
	// 注意 .16 是 Metric5(未使用时为 -1),不是 Status。
	oidInetCidrRouteIfIndex = ".1.3.6.1.2.1.4.24.7.1.7"
	oidInetCidrRouteType    = ".1.3.6.1.2.1.4.24.7.1.8"
	oidInetCidrRouteProto   = ".1.3.6.1.2.1.4.24.7.1.9"
	oidInetCidrRouteAge     = ".1.3.6.1.2.1.4.24.7.1.10"
	oidInetCidrRouteMetric1 = ".1.3.6.1.2.1.4.24.7.1.12"
	oidInetCidrRouteStatus  = ".1.3.6.1.2.1.4.24.7.1.17"

	// RFC 2096 ipCidrRouteEntry: 5=IfIndex 6=Type 7=Proto 8=Age 11=Metric1 16=Status。
	// 12..15 是 Metric2..5,未使用时为 -1;列号错位会把 -1 当成协议/年龄。
	oidIpCidrRouteIfIndex = ".1.3.6.1.2.1.4.24.4.1.5"
	oidIpCidrRouteType    = ".1.3.6.1.2.1.4.24.4.1.6"
	oidIpCidrRouteProto   = ".1.3.6.1.2.1.4.24.4.1.7"
	oidIpCidrRouteAge     = ".1.3.6.1.2.1.4.24.4.1.8"
	oidIpCidrRouteMetric1 = ".1.3.6.1.2.1.4.24.4.1.11"
	oidIpCidrRouteStatus  = ".1.3.6.1.2.1.4.24.4.1.16"

	// RFC 1213 ipRouteEntry: 2=IfIndex 3=Metric1 7=NextHop 8=Type 9=Proto 10=Age 11=Mask。
	oidIpRouteIfIndex = ".1.3.6.1.2.1.4.21.1.2"
	oidIpRouteMetric1 = ".1.3.6.1.2.1.4.21.1.3"
	oidIpRouteNextHop = ".1.3.6.1.2.1.4.21.1.7"
	oidIpRouteType    = ".1.3.6.1.2.1.4.21.1.8"
	oidIpRouteProto   = ".1.3.6.1.2.1.4.21.1.9"
	oidIpRouteAge     = ".1.3.6.1.2.1.4.21.1.10"
	oidIpRouteMask    = ".1.3.6.1.2.1.4.21.1.11"

	// RFC 2465 IPv6-MIB legacy route table. Some Huawei VRP versions expose
	// IPv6 routes here but do not implement inetCidrRouteTable.
	oidIPv6RouteIfIndex = ".1.3.6.1.2.1.55.1.11.1.4"
	oidIPv6RouteNextHop = ".1.3.6.1.2.1.55.1.11.1.5"
	oidIPv6RouteType    = ".1.3.6.1.2.1.55.1.11.1.6"
	oidIPv6RouteProto   = ".1.3.6.1.2.1.55.1.11.1.7"
	oidIPv6RouteAge     = ".1.3.6.1.2.1.55.1.11.1.9"
	oidIPv6RouteMetric  = ".1.3.6.1.2.1.55.1.11.1.11"
	oidIPv6RouteValid   = ".1.3.6.1.2.1.55.1.11.1.14"

	// dot1qPvid: 每个桥端口的默认(未打标签)VLAN, 索引是 dot1dBasePort (不是 ifIndex)。
	// FDB 只能通过 dot1qTpFdbTable 拿到 VLAN 时才是精确值; 拿不到时, 用端口的 PVID
	// 做一个"多半正确"的兜底 —— 对 access 口完全准确, 对 trunk 口只是主 VLAN 猜测。
	oidDot1qPvid = ".1.3.6.1.2.1.17.7.1.4.5.1.1"

	// 端口聚合与三层信息, 用来在端口表里补充"聚合组 / 二层 VLAN / 三层 IP"
	// 这几项"有就显示没有就不显示"的辅助信息。
	//
	// IEEE8023-LAG-MIB dot3adAggPortTable 的列号: .12 是 SelectedAggID,
	// .13 才是 AttachedAggID。之前代码把 .12 当成 AttachedAggID 在用——
	// 两者在 LACP 协商成功时通常相同, 但静态聚合/协商中间态下 Selected 可能
	// 是 0 或指向端口自己的 aggregator, 两列都读、谁有效用谁。
	oidDot3adAggPortSelectedAggID = ".1.2.840.10006.300.43.1.2.1.1.12"
	oidDot3adAggPortAttachedAggID = ".1.2.840.10006.300.43.1.2.1.1.13" // 成员口 -> 所属聚合口 ifIndex, 0=不是成员
	oidIpAdEntIfIndex             = ".1.3.6.1.2.1.4.20.1.2"            // IP-MIB ipAddrTable: OID 后缀是点分十进制 IP, 值是 ifIndex
	oidIpAdEntNetMask             = ".1.3.6.1.2.1.4.20.1.3"            // 同上, 值是掩码(区分 /30 互联地址和网关地址)
	oidIfPhysAddress              = ".1.3.6.1.2.1.2.2.1.6"             // 接口 MAC: 三层口的 MAC 就是 ARP 里"网关/设备自己"的 MAC
	oidDot1dBaseBridgeAddress     = ".1.3.6.1.2.1.17.1.1.0"            // 桥 MAC(设备自身 MAC)

	// LLDP-EXT-DOT3-MIB lldpXdot3LocLinkAggPortId: 索引是 lldpLocPortNum,
	// 值是该口所属聚合口的 ifIndex(0 = 不在聚合里)。LLDP 要往外通告"我这个口
	// 属于哪个聚合", 所以只要开了 LLDP, 静态聚合也会填——是 dot3ad/ifStack
	// 都没数据时的第三条路。
	oidLldpXdot3LocLinkAggPortId = ".1.0.8802.1.1.2.1.5.4623.1.2.3.1.2"

	// ifStackTable: 部分锐捷设备的静态(非 LACP 协商)聚合口不填
	// dot3adAggPortAttachedAggID, 但几乎所有厂商都支持这张标准 IF-MIB 表——
	// 物理口作为 ifStackLowerLayer 挂在 Eth-Trunk/AggregatePort(ifStackHigherLayer)
	// 下面, 拿来当聚合组归属的兜底判定。索引 = higherIfIndex.lowerIfIndex。
	oidIfStackStatus = ".1.3.6.1.2.1.31.1.2.1.3"

	// ENTITY-MIB / ENTITY-SENSOR-MIB (跨厂商回退)
	oidEntPhysicalDescr    = ".1.3.6.1.2.1.47.1.1.1.1.2"
	oidEntPhysicalClass    = ".1.3.6.1.2.1.47.1.1.1.1.5"
	oidEntPhysicalName     = ".1.3.6.1.2.1.47.1.1.1.1.7"
	oidEntPhysicalHardRev  = ".1.3.6.1.2.1.47.1.1.1.1.8"
	oidEntPhysicalSoftRev  = ".1.3.6.1.2.1.47.1.1.1.1.10"
	oidEntPhysicalSerial   = ".1.3.6.1.2.1.47.1.1.1.1.11"
	oidEntPhysicalModel    = ".1.3.6.1.2.1.47.1.1.1.1.13"
	oidEntAliasMappingIden = ".1.3.6.1.2.1.47.1.3.2.1.2"

	// 扩展信息(CollectOptions.SkipExtended)
	oidSysContact          = ".1.3.6.1.2.1.1.4.0"
	oidSysLocation         = ".1.3.6.1.2.1.1.6.0"
	oidIfLastChange        = ".1.3.6.1.2.1.2.2.1.9"
	oidIfInDiscards        = ".1.3.6.1.2.1.2.2.1.13"
	oidIfOutDiscards       = ".1.3.6.1.2.1.2.2.1.19"
	oidIfHCInMcastPkts     = ".1.3.6.1.2.1.31.1.1.1.8"
	oidIfHCInBcastPkts     = ".1.3.6.1.2.1.31.1.1.1.9"
	oidDot3StatsDuplexStat = ".1.3.6.1.2.1.10.7.2.1.19" // EtherLike-MIB, 索引即 ifIndex: 1=unknown 2=half 3=full
	oidEntPhySensorType    = ".1.3.6.1.2.1.99.1.1.1.1"
	oidEntPhySensorScale   = ".1.3.6.1.2.1.99.1.1.1.2"
	oidEntPhySensorPrec    = ".1.3.6.1.2.1.99.1.1.1.3"
	oidEntPhySensorValue   = ".1.3.6.1.2.1.99.1.1.1.4"
	oidEntPhySensorOper    = ".1.3.6.1.2.1.99.1.1.1.5"
)

// ----------------- 厂商私有 OID -----------------
//
// 每一项都是"候选列表", 按顺序尝试, 取第一个能拿到合理值的。
// 不同型号/固件对同一指标的实现位置差别很大, 硬编码单个 OID 是旧版失败的主因之一。
// CPU / 内存 / 温度 / 光功率 / 风扇电源 的 OID 和品牌识别规则在 vendors.go, 页面上可编辑。

// 锐捷老版本 RGOS 没有 .76/.81, 只有"整数位 + 小数位 + 符号位"三列
type ruijieSplitOptical struct {
	intOID  string
	decOID  string
	signOID string
}

var ruijieSplitRx = ruijieSplitOptical{
	intOID:  ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.31",
	decOID:  ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.32",
	signOID: ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.66",
}

var ruijieSplitTx = ruijieSplitOptical{
	intOID:  ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.51",
	decOID:  ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.52",
	signOID: ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.71",
}

// 风扇/电源。锐捷: myFanStateTable/myPowerStateTable(4881.1.1.10.2.21.1.6/.5)
// 旧版用的 4881.1.1.10.2.35.* 是内存池 MIB, 跟风扇电源毫无关系。
const (
	oidRuijieFanState   = ".1.3.6.1.4.1.4881.1.1.10.2.21.1.6.1.3" // 1=work 2=stop
	oidRuijiePowerState = ".1.3.6.1.4.1.4881.1.1.10.2.21.1.5.1.3" // 4=linkAndPower 5=abnormal
	oidRuijieTempState  = ".1.3.6.1.4.1.4881.1.1.10.2.21.1.4.1.3" // 1=normal 2=warning

	// RuijieFiberMIB: the row index is ruijieFiberPortIndex (IF-MIB ifIndex).
	oidRuijieFiberTransceiverTypeStr = ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.165"
	oidRuijieFiberConnectorTypeStr   = ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.166"

	oidH3cEntityErrStatus = ".1.3.6.1.4.1.25506.2.6.1.1.1.1.19" // hh3cEntityExtErrorStatus
	// hh3cTransceiverFiberDiameterType: 索引是 ifIndex, copper(4) 表示电口模块。
	oidH3cTransceiverFiberDiameterType       = ".1.3.6.1.4.1.25506.2.70.1.1.1.6"
	oidH3cLegacyTransceiverFiberDiameterType = ".1.3.6.1.4.1.2011.10.2.70.1.1.1.6"
	// hwEthernetPortMode: copper(1), fiber(2), other(3); 只读当前接口介质模式。
	oidHwEthernetPortMode = ".1.3.6.1.4.1.2011.5.25.157.1.1.1.1.39"
	oidHwEntityOperStatus = ".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.2"
)
