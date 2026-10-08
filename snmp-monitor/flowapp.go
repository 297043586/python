package main

// 流量的协议 / 应用识别。
//
// 之前页面上"只能看到 TCP/UDP"有两个原因:
//  1. 解析 sFlow 原始报文头时, 非 IP 帧(ARP/STP/LLDP/LACP/802.1X...)直接丢弃,
//     这些二层控制报文恰恰是排查环路、私接交换机、ARP 风暴最有用的;
//  2. "应用"只认 DNS/TLS SNI/HTTP 三种带域名的, 其余一律没有应用名。
//
// 现在分三层识别, 从准到粗:
//   - 负载特征: SSH 版本串、SMB、RDP、QUIC、STUN、RTSP、SIP、BitTorrent、DHCP ...
//     (只看抽样报文头里已有的前几十字节, 不做 DPI 重组)
//   - 域名归属: TLS SNI / HTTP Host / DNS 查询域名 → 微信/抖音/B站/Windows 更新 ...
//   - 知名端口: 取两端里"像服务端"的那个端口(知名端口优先, 否则取较小的)
// 每条记录得到 App(应用名)和 AppCat(类别), 页面按应用/类别做 Top 和占比。

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// l2ProtoName 给非 IP 的以太网帧起名字。p 是以太类型之后的负载。
func l2ProtoName(ethType uint16, p []byte) string {
	if ethType < 0x0600 { // 802.3 长度字段, 后面是 LLC
		if len(p) >= 3 && p[0] == 0x42 && p[1] == 0x42 {
			return "STP"
		}
		if len(p) >= 8 && p[0] == 0xaa && p[1] == 0xaa && bytes.Equal(p[3:6], []byte{0x00, 0x00, 0x0c}) {
			switch binary.BigEndian.Uint16(p[6:8]) {
			case 0x2000:
				return "CDP"
			case 0x010b:
				return "PVST+"
			case 0x2004:
				return "DTP"
			}
		}
		return "LLC"
	}
	switch ethType {
	case 0x0806:
		return "ARP"
	case 0x8035:
		return "RARP"
	case 0x88cc:
		return "LLDP"
	case 0x8809:
		if len(p) > 0 && p[0] == 0x01 {
			return "LACP"
		}
		return "Slow-Protocol"
	case 0x888e:
		return "802.1X"
	case 0x8863, 0x8864:
		return "PPPoE"
	case 0x8847, 0x8848:
		return "MPLS"
	case 0x88f7:
		return "PTP"
	case 0x88e5:
		return "MACsec"
	case 0x8902:
		return "CFM"
	case 0x88a7:
		return "堆叠/私有"
	case 0x9000:
		return "Loopback"
	}
	return "L2-0x" + strings.ToUpper(strings.TrimLeft(hex4(ethType), "0"))
}

func hex4(v uint16) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>12&0xf], digits[v>>8&0xf], digits[v>>4&0xf], digits[v&0xf]})
}

// isL2Proto: 这条记录是非 IP 的二层帧。
func isL2Record(rec FlowRecord) bool { return rec.IPVer == 0 && rec.SrcIP == "" }

type appDef struct{ name, cat string }

// portApps: 知名端口 → 应用。协议前缀 "t"/"u" 区分 TCP/UDP, 不区分的直接写端口。
var portApps = map[string]appDef{
	"t20": {"FTP", "文件传输"}, "t21": {"FTP", "文件传输"}, "t22": {"SSH", "远程管理"}, "t23": {"Telnet", "远程管理"},
	"t25": {"SMTP", "邮件"}, "t465": {"SMTP", "邮件"}, "t587": {"SMTP", "邮件"}, "t110": {"POP3", "邮件"}, "t995": {"POP3", "邮件"},
	"t143": {"IMAP", "邮件"}, "t993": {"IMAP", "邮件"},
	"53": {"DNS", "基础服务"}, "u67": {"DHCP", "基础服务"}, "u68": {"DHCP", "基础服务"}, "u69": {"TFTP", "文件传输"},
	"t80": {"HTTP", "Web"}, "t8080": {"HTTP", "Web"}, "t8000": {"HTTP/海康 SDK", "视频监控"}, "t8888": {"HTTP", "Web"},
	"t443": {"HTTPS", "Web"}, "t8443": {"HTTPS", "Web"}, "u443": {"QUIC", "Web"},
	"88": {"Kerberos", "认证"}, "u123": {"NTP", "基础服务"}, "t135": {"RPC", "Windows 服务"},
	"137": {"NetBIOS", "Windows 服务"}, "138": {"NetBIOS", "Windows 服务"}, "t139": {"NetBIOS", "Windows 服务"},
	"u161": {"SNMP", "网管"}, "u162": {"SNMP Trap", "网管"}, "t179": {"BGP", "路由协议"}, "389": {"LDAP", "认证"}, "t636": {"LDAPS", "认证"},
	"t445": {"SMB 文件共享", "文件传输"}, "u500": {"IPsec IKE", "VPN"}, "u4500": {"IPsec NAT-T", "VPN"}, "u514": {"Syslog", "网管"},
	"t515": {"LPD 打印", "打印"}, "u520": {"RIP", "路由协议"}, "t554": {"RTSP", "视频监控"}, "u554": {"RTSP", "视频监控"},
	"t631": {"IPP 打印", "打印"}, "t873": {"rsync", "文件传输"}, "u1194": {"OpenVPN", "VPN"}, "t1194": {"OpenVPN", "VPN"},
	"t1433": {"SQL Server", "数据库"}, "t1521": {"Oracle", "数据库"}, "u1701": {"L2TP", "VPN"}, "t1723": {"PPTP", "VPN"},
	"u1812": {"RADIUS", "认证"}, "u1813": {"RADIUS", "认证"}, "t1883": {"MQTT", "物联网"}, "u1900": {"SSDP", "发现协议"},
	"2049": {"NFS", "文件传输"}, "t3260": {"iSCSI", "存储"}, "t3306": {"MySQL", "数据库"}, "t3389": {"RDP 远程桌面", "远程管理"},
	"u3389": {"RDP 远程桌面", "远程管理"}, "3478": {"STUN/TURN", "音视频通话"}, "t5060": {"SIP", "音视频通话"}, "u5060": {"SIP", "音视频通话"},
	"t5061": {"SIP-TLS", "音视频通话"}, "u5353": {"mDNS", "发现协议"}, "u5355": {"LLMNR", "发现协议"}, "t5432": {"PostgreSQL", "数据库"},
	"t5900": {"VNC", "远程管理"}, "t5985": {"WinRM", "远程管理"}, "t5986": {"WinRM", "远程管理"}, "t6379": {"Redis", "数据库"},
	"t9100": {"RAW 打印", "打印"}, "t9200": {"Elasticsearch", "数据库"}, "t10050": {"Zabbix", "网管"}, "t10051": {"Zabbix", "网管"},
	"t27017": {"MongoDB", "数据库"}, "t37777": {"大华 SDK", "视频监控"}, "u37778": {"大华 SDK", "视频监控"}, "u4789": {"VXLAN", "隧道"},
	"u51820": {"WireGuard", "VPN"}, "t502": {"Modbus", "工控"}, "t102": {"S7comm", "工控"}, "u47808": {"BACnet", "工控"},
	"t8291": {"Winbox", "远程管理"}, "t2375": {"Docker API", "运维"}, "t6443": {"Kubernetes API", "运维"}, "t9092": {"Kafka", "中间件"},
	"t5672": {"AMQP", "中间件"}, "t11211": {"Memcached", "数据库"}, "t8081": {"HTTP", "Web"}, "t1080": {"SOCKS 代理", "代理"},
	"t3128": {"HTTP 代理", "代理"}, "t6881": {"BitTorrent", "P2P 下载"}, "u6881": {"BitTorrent", "P2P 下载"}, "t8086": {"InfluxDB", "数据库"},
}

// domainApps: 域名后缀 → 应用。按后缀匹配, 越具体的写在前面也没关系(取最长匹配)。
var domainApps = map[string]appDef{
	"weixin.qq.com": {"微信", "即时通讯"}, "wechat.com": {"微信", "即时通讯"}, "weixinbridge.com": {"微信", "即时通讯"}, "wx.qq.com": {"微信", "即时通讯"},
	"qq.com": {"腾讯/QQ", "即时通讯"}, "qpic.cn": {"腾讯/QQ", "即时通讯"}, "gtimg.cn": {"腾讯/QQ", "即时通讯"}, "tencent.com": {"腾讯", "即时通讯"},
	"meeting.tencent.com": {"腾讯会议", "视频会议"}, "work.weixin.qq.com": {"企业微信", "办公协同"},
	"dingtalk.com": {"钉钉", "办公协同"}, "feishu.cn": {"飞书", "办公协同"}, "larksuite.com": {"飞书", "办公协同"}, "zoom.us": {"Zoom", "视频会议"},
	"douyin.com": {"抖音", "短视频"}, "douyinvod.com": {"抖音", "短视频"}, "douyinpic.com": {"抖音", "短视频"}, "amemv.com": {"抖音", "短视频"},
	"snssdk.com": {"字节系(抖音/头条)", "短视频"}, "bytedance.com": {"字节跳动", "短视频"}, "pstatp.com": {"字节系(抖音/头条)", "短视频"},
	"kuaishou.com": {"快手", "短视频"}, "gifshow.com": {"快手", "短视频"}, "bilibili.com": {"哔哩哔哩", "视频"}, "bilivideo.com": {"哔哩哔哩", "视频"},
	"hdslb.com": {"哔哩哔哩", "视频"}, "iqiyi.com": {"爱奇艺", "视频"}, "youku.com": {"优酷", "视频"}, "mgtv.com": {"芒果TV", "视频"},
	"v.qq.com": {"腾讯视频", "视频"}, "youtube.com": {"YouTube", "视频"}, "googlevideo.com": {"YouTube", "视频"}, "netflix.com": {"Netflix", "视频"},
	"huya.com": {"虎牙直播", "视频"}, "douyu.com": {"斗鱼直播", "视频"},
	"music.163.com": {"网易云音乐", "音乐"}, "kugou.com": {"酷狗音乐", "音乐"}, "y.qq.com": {"QQ 音乐", "音乐"},
	"taobao.com": {"淘宝", "购物"}, "tmall.com": {"天猫", "购物"}, "alicdn.com": {"阿里系", "购物"}, "jd.com": {"京东", "购物"},
	"pinduoduo.com": {"拼多多", "购物"}, "yangkeduo.com": {"拼多多", "购物"}, "meituan.com": {"美团", "购物"},
	"alipay.com": {"支付宝", "支付"}, "tenpay.com": {"微信支付", "支付"},
	"baidu.com": {"百度", "搜索"}, "bdstatic.com": {"百度", "搜索"}, "bing.com": {"Bing", "搜索"}, "google.com": {"Google", "搜索"},
	"weibo.com": {"微博", "社交"}, "weibo.cn": {"微博", "社交"}, "sinaimg.cn": {"微博", "社交"}, "zhihu.com": {"知乎", "社交"},
	"xiaohongshu.com": {"小红书", "社交"}, "xhscdn.com": {"小红书", "社交"},
	"windowsupdate.com": {"Windows 更新", "系统更新"}, "update.microsoft.com": {"Windows 更新", "系统更新"}, "delivery.mp.microsoft.com": {"Windows 更新", "系统更新"},
	"office.com": {"Microsoft 365", "办公协同"}, "office365.com": {"Microsoft 365", "办公协同"}, "outlook.com": {"Outlook", "邮件"},
	"microsoft.com": {"微软", "系统更新"}, "msftconnecttest.com": {"Windows 联网检测", "系统更新"}, "live.com": {"微软", "办公协同"},
	"apple.com": {"Apple", "系统更新"}, "icloud.com": {"iCloud", "网盘"}, "mzstatic.com": {"App Store", "系统更新"},
	"pan.baidu.com": {"百度网盘", "网盘"}, "baidupcs.com": {"百度网盘", "网盘"}, "aliyundrive.com": {"阿里云盘", "网盘"}, "alipan.com": {"阿里云盘", "网盘"},
	"quark.cn": {"夸克", "网盘"}, "123pan.com": {"123 云盘", "网盘"},
	"github.com": {"GitHub", "开发"}, "githubusercontent.com": {"GitHub", "开发"}, "gitee.com": {"Gitee", "开发"},
	"aliyuncs.com": {"阿里云", "云服务"}, "aliyun.com": {"阿里云", "云服务"}, "myqcloud.com": {"腾讯云", "云服务"}, "amazonaws.com": {"AWS", "云服务"},
	"huaweicloud.com": {"华为云", "云服务"}, "volces.com": {"火山引擎", "云服务"},
	"360.cn": {"360", "安全软件"}, "360safe.com": {"360", "安全软件"}, "qhimg.com": {"360", "安全软件"},
	"steampowered.com": {"Steam", "游戏"}, "steamcontent.com": {"Steam", "游戏"}, "wegame.com": {"WeGame", "游戏"}, "mihoyo.com": {"米哈游", "游戏"},
	"hoyoverse.com": {"米哈游", "游戏"}, "163.com": {"网易", "门户"}, "126.net": {"网易", "门户"}, "sohu.com": {"搜狐", "门户"}, "sina.com.cn": {"新浪", "门户"},
	"openai.com": {"ChatGPT", "AI"}, "chatgpt.com": {"ChatGPT", "AI"}, "anthropic.com": {"Claude", "AI"}, "claude.ai": {"Claude", "AI"},
	"deepseek.com": {"DeepSeek", "AI"}, "doubao.com": {"豆包", "AI"}, "kimi.moonshot.cn": {"Kimi", "AI"}, "moonshot.cn": {"Kimi", "AI"},
	"tongyi.aliyun.com": {"通义千问", "AI"}, "yiyan.baidu.com": {"文心一言", "AI"},
	"hikvision.com": {"海康威视", "视频监控"}, "ys7.com": {"萤石云", "视频监控"}, "dahuatech.com": {"大华", "视频监控"}, "imoulife.com": {"乐橙", "视频监控"},
}

func appByDomain(host string) (appDef, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	best, bestLen := appDef{}, 0
	for suf, d := range domainApps {
		if (host == suf || strings.HasSuffix(host, "."+suf)) && len(suf) > bestLen {
			best, bestLen = d, len(suf)
		}
	}
	return best, bestLen > 0
}

func appByPort(proto string, port int) (appDef, bool) {
	if port <= 0 {
		return appDef{}, false
	}
	p := itoa(port)
	pre := ""
	switch proto {
	case "TCP":
		pre = "t"
	case "UDP":
		pre = "u"
	default:
		return appDef{}, false
	}
	if d, ok := portApps[pre+p]; ok {
		return d, true
	}
	d, ok := portApps[p]
	return d, ok
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// appByPayload 按负载开头的特征识别应用。只认特征很明确、误判概率很低的几种。
func appByPayload(proto string, sport, dport int, p []byte) (appDef, bool) {
	if len(p) < 4 {
		return appDef{}, false
	}
	switch proto {
	case "TCP":
		switch {
		case bytes.HasPrefix(p, []byte("SSH-")):
			return appDef{"SSH", "远程管理"}, true
		case len(p) >= 20 && p[0] == 0x13 && bytes.HasPrefix(p[1:], []byte("BitTorrent protocol")):
			return appDef{"BitTorrent", "P2P 下载"}, true
		case len(p) >= 8 && p[0] == 0x00 && (bytes.HasPrefix(p[4:], []byte{0xff, 'S', 'M', 'B'}) || bytes.HasPrefix(p[4:], []byte{0xfe, 'S', 'M', 'B'})):
			return appDef{"SMB 文件共享", "文件传输"}, true
		case p[0] == 0x03 && p[1] == 0x00 && (dport == 3389 || sport == 3389):
			return appDef{"RDP 远程桌面", "远程管理"}, true
		case bytes.HasPrefix(p, []byte("RTSP/1.0")) || bytes.Contains(p[:min(len(p), 16)], []byte(" rtsp://")):
			return appDef{"RTSP", "视频监控"}, true
		case bytes.HasPrefix(p, []byte("SIP/2.0")) || bytes.HasPrefix(p, []byte("INVITE sip:")) || bytes.HasPrefix(p, []byte("REGISTER sip:")):
			return appDef{"SIP", "音视频通话"}, true
		case bytes.HasPrefix(p, []byte("HTTP/1.")):
			return appDef{"HTTP", "Web"}, true
		}
	case "UDP":
		switch {
		// QUIC 长包头: 首字节最高两位 11, 后面 4 字节版本号(v1=1, v2=0x6b3343cf, 草案 0xff0000xx)
		case (dport == 443 || sport == 443) && p[0]&0xc0 == 0xc0 && len(p) >= 5:
			return appDef{"QUIC (HTTP/3)", "Web"}, true
		case len(p) >= 20 && binary.BigEndian.Uint32(p[4:8]) == 0x2112a442:
			return appDef{"STUN/TURN (音视频/P2P)", "音视频通话"}, true
		case len(p) >= 240 && binary.BigEndian.Uint32(p[236:240]) == 0x63825363:
			return appDef{"DHCP", "基础服务"}, true
		case bytes.HasPrefix(p, []byte("SIP/2.0")) || bytes.HasPrefix(p, []byte("INVITE sip:")) || bytes.HasPrefix(p, []byte("REGISTER sip:")):
			return appDef{"SIP", "音视频通话"}, true
		case bytes.HasPrefix(p, []byte("d1:ad2:id20:")) || bytes.HasPrefix(p, []byte("d1:rd2:id20:")):
			return appDef{"BitTorrent DHT", "P2P 下载"}, true
		// RTP: 版本 2, 负载类型常见范围, 端口是偶数高位端口
		case p[0]&0xc0 == 0x80 && (p[1]&0x7f <= 34 || p[1]&0x7f >= 96) && sport >= 1024 && dport >= 1024 && sport%2 == 0 && dport%2 == 0 && len(p) >= 12:
			return appDef{"RTP 媒体流", "音视频通话"}, true
		}
	}
	return appDef{}, false
}

// classifyApp 给记录打应用标签。payload 可以为空(NetFlow / sampled_ipv4 没有报文内容)。
func classifyApp(rec *FlowRecord, payload []byte) {
	if rec.App != "" {
		return
	}
	if isL2Record(*rec) {
		rec.App, rec.AppCat = rec.Protocol, "二层控制"
		return
	}
	switch rec.Protocol {
	case "ICMP", "ICMPv6":
		rec.App, rec.AppCat = rec.Protocol, "网络诊断"
		return
	case "OSPF", "EIGRP", "PIM", "VRRP", "IGMP", "IS-IS":
		rec.App, rec.AppCat = rec.Protocol, "路由/组播协议"
		return
	case "GRE", "ESP", "AH", "IPIP", "IPv6-in-IPv4":
		rec.App, rec.AppCat = rec.Protocol, "隧道/VPN"
		return
	}
	if rec.AppHost != "" {
		if d, ok := appByDomain(rec.AppHost); ok {
			rec.App, rec.AppCat = d.name, d.cat
			return
		}
	}
	if d, ok := appByPayload(rec.Protocol, rec.SrcPort, rec.DstPort, payload); ok {
		rec.App, rec.AppCat = d.name, d.cat
		return
	}
	// 知名端口: 先看目的端口(请求方向), 再看源端口(应答方向)
	if d, ok := appByPort(rec.Protocol, rec.DstPort); ok {
		rec.App, rec.AppCat = d.name, d.cat
		return
	}
	if d, ok := appByPort(rec.Protocol, rec.SrcPort); ok {
		rec.App, rec.AppCat = d.name, d.cat
		return
	}
	if rec.Protocol == "TCP" || rec.Protocol == "UDP" {
		if rec.SrcPort >= 49152 && rec.DstPort >= 49152 {
			rec.App, rec.AppCat = rec.Protocol+" 高位端口互访", "未识别(疑似 P2P)"
		} else {
			rec.App, rec.AppCat = rec.Protocol+" 未识别", "未识别"
		}
		return
	}
	rec.App, rec.AppCat = rec.Protocol, "其他"
}
