package main

// 文本 / 数值清洗(GBK 解码、MAC 格式化、计数器增量)和接口名归一化。

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// ----------------- 文本 / 数值 清洗 -----------------

// decodeText 把 SNMP 返回的字节串转成可读文本。
// 旧版本无条件按 GBK 解码, 华为/新版 Comware 返回 UTF-8 中文时会被解成乱码;
// 这里先判断是否已是合法 UTF-8, 只有不是才走 GBK/GB18030。
func decodeText(val interface{}) string {
	if val == nil {
		return ""
	}
	var raw []byte
	switch v := val.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	case nil:
		return ""
	default:
		raw = []byte(fmt.Sprintf("%v", v))
	}
	if len(raw) == 0 {
		return ""
	}

	text := string(raw)
	if !utf8.Valid(raw) {
		if d, err := io.ReadAll(transform.NewReader(bytes.NewReader(raw), simplifiedchinese.GBK.NewDecoder())); err == nil && len(d) > 0 {
			text = string(d)
		} else if d, err := io.ReadAll(transform.NewReader(bytes.NewReader(raw), simplifiedchinese.GB18030.NewDecoder())); err == nil && len(d) > 0 {
			text = string(d)
		}
	}

	var sb strings.Builder
	for _, r := range text {
		switch {
		case r == '\t' || r == ' ':
			sb.WriteRune(' ')
		case r < 32 || r == 127: // 丢掉控制字符
		case r == utf8.RuneError:
		default:
			sb.WriteRune(r)
		}
	}
	return strings.TrimSpace(sb.String())
}

// shortSysName 只用于设备名/LLDP 对端名: 去掉 DNS 后缀。
// 旧版把这个截断逻辑放在通用清洗函数里, 于是任何带点的字符串(子接口名、版本号、
// 甚至光功率数值)都会被拦腰截断。
func shortSysName(s string) string {
	if s == "" {
		return ""
	}
	if strings.Contains(s, " ") {
		return s
	}
	if i := strings.Index(s, "."); i > 0 {
		// 形如 10.1.1.1 的纯 IP 不截断
		if net.ParseIP(s) == nil {
			return s[:i]
		}
	}
	return s
}

func formatMACBytes(b []byte) string {
	if len(b) != 6 {
		return ""
	}
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", b[0], b[1], b[2], b[3], b[4], b[5])
}

func formatMACValue(val interface{}) string {
	switch v := val.(type) {
	case []byte:
		if len(v) == 6 {
			return formatMACBytes(v)
		}
		// 有的设备把 MAC 以 "aa:bb:cc:dd:ee:ff" 或 "aabb-ccdd-eeff" 的字符串返回
		return normalizeMACString(string(v))
	case string:
		if len(v) == 6 {
			return formatMACBytes([]byte(v))
		}
		return normalizeMACString(v)
	}
	return ""
}

var macCharRe = regexp.MustCompile(`[^0-9a-fA-F]`)

func normalizeMACString(s string) string {
	hex := macCharRe.ReplaceAllString(s, "")
	if len(hex) != 12 {
		return ""
	}
	hex = strings.ToUpper(hex)
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, hex[i:i+2])
	}
	return strings.Join(parts, ":")
}

// macFromOIDDigits 把 OID 里的 6 段十进制转成 MAC 文本。
func macFromOIDDigits(parts []string) string {
	if len(parts) != 6 {
		return ""
	}
	b := make([]byte, 6)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return ""
		}
		b[i] = byte(n)
	}
	return formatMACBytes(b)
}

func toInt64(val interface{}) int64 {
	if val == nil {
		return 0
	}
	switch v := val.(type) {
	case int:
		return int64(v)
	case int64:
		return int64(v)
	case uint:
		return int64(v)
	case uint64:
		if v > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(v)
	case uint32:
		return int64(v)
	}
	bi := gosnmp.ToBigInt(val)
	if bi == nil {
		return 0
	}
	return bi.Int64()
}

func toUint64(val interface{}) uint64 {
	if val == nil {
		return 0
	}
	bi := gosnmp.ToBigInt(val)
	if bi == nil {
		return 0
	}
	if bi.Sign() < 0 {
		return 0
	}
	return bi.Uint64()
}

// counterDelta 计算两次采样之间的增量。
//
// wrap32 只对 32 位 octet 计数器开启: 千兆口跑满时 32 位字节计数器 34 秒就会绕一圈,
// 所以那里的"倒退"确实多半是回绕。而错包计数器现实中不会绕, 倒退一定是设备重启或
// 手工 clear counters —— 旧版对所有计数器一律按回绕处理, 于是清零后会刷出
// "错包增加 +4294967279" 这种告警。
func counterDelta(cur, prev uint64, wrap32 bool) uint64 {
	if cur >= prev {
		return cur - prev
	}
	if !wrap32 {
		return 0
	}
	const max32 = uint64(1) << 32
	if prev < max32 && cur < max32 {
		return max32 - prev + cur
	}
	return 0
}

// sampleSeconds 返回两次计数器读取之间隔了多少秒: 两次都有毫秒级读取时刻
// 就用它, 否则退回整秒的 LastSeen(旧版本落盘的快照没有毫秒时刻)。
func sampleSeconds(curMs, prevMs, curSec, prevSec int64) float64 {
	if curMs > 0 && prevMs > 0 {
		return float64(curMs-prevMs) / 1000
	}
	return float64(curSec - prevSec)
}

// plausibleRate 过滤掉超出端口物理线速的速率值。
// 计数器清零、采样间隔漂移都会算出离谱的瞬时速率, 直接显示会误导判断。
func plausibleRate(mbps float64, speedMbps int64) float64 {
	if mbps < 0 || math.IsNaN(mbps) || math.IsInf(mbps, 0) {
		return 0
	}
	if speedMbps > 0 && mbps > float64(speedMbps)*1.2 {
		return 0
	}
	return mbps
}

// ----------------- 接口名归一化 -----------------
//
// 同一个物理口在 ifDescr / ifName / lldpLocPortId / lldpRemPortDesc 里的写法差别很大:
//   锐捷  ifDescr="GigabitEthernet 0/1"   lldpLocPortId="Gi0/1"
//   H3C   ifDescr="Ten-GigabitEthernet1/0/49"  lldpRemPortId="XGE1/0/49"
//   华为  ifDescr="GigabitEthernet0/0/1"  lldpRemPortDesc="GigabitEthernet0/0/1"
// 归一化后统一成 "速率族|槽位号", 才能把 LLDP 邻居挂到正确的 ifIndex 上。

var portFamilyAlias = map[string]string{
	"hundredgige": "100g", "hundredgigabitethernet": "100g", "100ge": "100g", "cge": "100g", "hgei": "100g",
	"fortygige": "40g", "fortygigabitethernet": "40g", "xlge": "40g", "40ge": "40g", "fge": "40g",
	"twentyfivegige": "25g", "twentyfivegigabitethernet": "25g", "25ge": "25g", "xxge": "25g",
	"tengigabitethernet": "10g", "tengige": "10g", "tengigabitether": "10g", "xgigabitethernet": "10g",
	"xge": "10g", "te": "10g", "tge": "10g", "10ge": "10g", "tenge": "10g", "xg": "10g",
	"gigabitethernet": "1g", "gigaethernet": "1g", "gigabitether": "1g", "ge": "1g", "gi": "1g", "gei": "1g",
	"fastethernet": "100m", "fastether": "100m", "fa": "100m", "fe": "100m", "fei": "100m",
	"ethernet": "eth", "eth": "eth",
	"aggregateport": "agg", "ag": "agg", "bridgeaggregation": "agg", "bagg": "agg",
	"routeaggregation": "agg", "ragg": "agg", "ethtrunk": "agg", "portchannel": "agg", "po": "agg",
	"mgmtethernet": "mgmt", "meth": "mgmt", "mgmt": "mgmt", "mgigabitethernet": "mgmt", "management": "mgmt",
	"vlan": "vlan", "vlanif": "vlan", "vlaninterface": "vlan",
}

var keepRe = regexp.MustCompile(`[^a-z0-9/:.]`)

// normPortKey 返回 "族|编号", 例如 "1g|0/1"。无法识别时返回小写原串。
func normPortKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return ""
	}
	s = keepRe.ReplaceAllString(s, "")
	digit := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			digit = i
			break
		}
	}
	if digit <= 0 {
		return s
	}
	prefix := s[:digit]
	num := s[digit:]
	if fam, ok := portFamilyAlias[prefix]; ok {
		return fam + "|" + num
	}
	return prefix + "|" + num
}

// slotKey 只取槽位号部分, 作为最后的兜底匹配(必须唯一才使用)。
var slotRe = regexp.MustCompile(`\d+(/\d+)+`)

func slotKey(name string) string {
	return slotRe.FindString(name)
}
