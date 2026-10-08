package main

// 采集: 光模块收发光功率(锐捷 / H3C / 华为私有 MIB)。

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// ----------------- 光功率解析 -----------------

const (
	optMinDbm = -45.0
	optMaxDbm = 12.0
)

// parseOpticalRaw 把原始整数换算成 dBm。无效返回 ok=false。
func parseOpticalRaw(raw int64, scale float64) (float64, bool) {
	// 厂商约定的"不支持/未插模块"哨兵值
	switch raw {
	case 0, -1, 65535, 2147483647, -2147483647, -2147483648, 4294967295:
		return 0, false
	}
	if raw > math.MaxInt32 && raw <= 4294967295 { // 被当成无符号读上来的负数
		raw -= 4294967296
	}
	if scale <= 0 {
		scale = 1
	}
	dbm := float64(raw) / scale
	// 只接受该来源文档化的量纲。这里不做"换个倍数试试"的猜测:
	// 猜中了看不出来, 猜错了会把一个超范围的异常值粉饰成正常读数。
	if dbm < optMinDbm || dbm > optMaxDbm {
		return 0, false
	}
	return roundDbm(dbm), true
}

func roundDbm(v float64) float64 {
	return math.Round(v*100) / 100
}

func formatDbm(v float64) string {
	return fmt.Sprintf("%.2f dBm", v)
}

// microWattToDbm 少数设备用 uW 上报
func microWattToDbm(uw float64) (float64, bool) {
	if uw <= 0 {
		return 0, false
	}
	d := 10 * math.Log10(uw/1000.0)
	if d < optMinDbm || d > optMaxDbm {
		return 0, false
	}
	return roundDbm(d), true
}

// ----------------- 光功率 -----------------

// collectOptical 采集收光(Rx)/发光(Tx)功率。
//
// 旧版的 bug: 只有当 "rx 和 tx 都一无所获" 时才会尝试下一个来源。
// 但实际观察到的情况是收光能取到、发光取不到 —— 某些型号的固件只支持
// 主 OID 里的 RX 列, TX 列要么返回哨兵值要么干脆不存在。旧逻辑一看 rx
// 有数据就判定"这个来源可用", 永远不会再去找 TX 的替代来源。
//
// 改法: rx 和 tx 分别独立地走完整条回退链, 每一步只去补那些"还没有值"
// 的端口, 不因为另一侧已经有数据就整体跳过。
func collectOptical(s *snmpSession, brand string, r *indexResolver) (map[string]float64, map[string]float64, string) {
	rx := make(map[string]float64)
	tx := make(map[string]float64)
	var rxSrc, txSrc []string
	vp := vendorProfile(brand)
	copperPorts := collectCopperOpticalInterfaces(s, r, brand, vp.OpticalCopper, vp.OpticalCopperValue, vp.OpticalCopperMAU)

	applyOID := func(dst map[string]float64, oid string, scale float64) int {
		n := 0
		_ = s.walk(oid, func(idx string, pdu gosnmp.SnmpPDU) {
			ifIdx := r.byTableIndex(idx)
			if ifIdx == "" {
				return
			}
			if copperPorts[ifIdx] {
				return // 电口模块没有光收发功率, 忽略设备返回的 -40 等哨兵值
			}
			if _, exists := dst[ifIdx]; exists {
				return
			}
			if v, ok := parseOpticalRaw(toInt64(pdu.Value), scale); ok {
				dst[ifIdx] = v
				n++
			}
		})
		return n
	}

	// 1) 厂商私有 MIB 主 OID
	for _, src := range vendorProfile(brand).Optical {
		if src.RX != "" && applyOID(rx, src.RX, src.Scale) > 0 {
			rxSrc = append(rxSrc, "厂商私有 MIB")
		}
		if src.TX != "" && applyOID(tx, src.TX, src.Scale) > 0 {
			txSrc = append(txSrc, "厂商私有 MIB")
		}
	}

	// 2) 锐捷老固件: 整数位/小数位/符号位 三列拼装 —— 只补缺的那一侧
	if brand == "ruijie" {
		if applySplit(s, r, rx, ruijieSplitRx, copperPorts) > 0 {
			rxSrc = append(rxSrc, "整数/小数位拼装")
		}
		if applySplit(s, r, tx, ruijieSplitTx, copperPorts) > 0 {
			txSrc = append(txSrc, "整数/小数位拼装")
		}
	}

	// 3) 跨厂商兜底: ENTITY-SENSOR-MIB 里 type=dBm(14) 的传感器,
	//    同样只用来补缺, 已经取到的值不会被覆盖。
	if n1, n2 := collectSensorOptical(s, r, rx, tx, copperPorts); n1 > 0 || n2 > 0 {
		if n1 > 0 {
			rxSrc = append(rxSrc, "ENTITY-SENSOR-MIB")
		}
		if n2 > 0 {
			txSrc = append(txSrc, "ENTITY-SENSOR-MIB")
		}
	}

	// 4) H3C 最后手段: 极老固件干脆不支持 CurTXPower(.9), 但几乎都支持
	//    MinTXPower(.11, 模块规格下限)。用它顶上去总比空着好, 但这不是
	//    实时值, 必须明确标注, 不能让人误以为是真实发光功率。
	if brand == "h3c" {
		if n := applyOID(tx, ".1.3.6.1.4.1.25506.2.70.1.1.1.11", 100); n > 0 {
			txSrc = append(txSrc, "模块规格下限(近似值,非实时)")
		}
	}

	// 5) 锐捷对没有 DDM 的模块(电口 SFP、DAC 等)在 RX 和 TX 上同时返回 -40 dBm。
	//    真实光口即使无光,发光侧也会有读数;两侧同为 -40 才视为"无 DDM"。
	//    这一步兜住收发器类型 MIB 未开放、无法按型号识别电口模块的固件。
	noDDM := 0
	if brand == "ruijie" {
		for ifIdx, rv := range rx {
			if tv, ok := tx[ifIdx]; ok && rv <= ruijieNoDDMDbm && tv <= ruijieNoDDMDbm {
				delete(rx, ifIdx)
				delete(tx, ifIdx)
				noDDM++
			}
		}
	}

	source := describeOpticalSource(rxSrc, txSrc)
	var details []string
	if len(copperPorts) > 0 {
		details = append(details, fmt.Sprintf("%d 个铜缆电口已识别并跳过光功率", len(copperPorts)))
	}
	if noDDM > 0 {
		details = append(details, fmt.Sprintf("%d 个端口 RX/TX 同为 -40 dBm,按无 DDM 模块忽略", noDDM))
	}
	if len(details) > 0 {
		if source == "未采集到 DDM 数据" {
			source = strings.Join(details, "；")
		} else {
			source += "；" + strings.Join(details, "；")
		}
	}
	return rx, tx, source
}

// ruijieNoDDMDbm 是锐捷对无 DDM 模块返回的占位读数。
const ruijieNoDDMDbm = -40.0

// collectCopperOpticalInterfaces 按品牌配置的介质类型 OID 和枚举值标记铜缆电口。
// H3C 默认表以 ifIndex 为索引,copper(4); Huawei hwEthernetPortMode 使用 copper(1)。
// Ruijie 先查标准 MAU-MIB ifMauType,再查 RuijieFiberMIB 的收发器类型/连接器文本；
// 前者返回 dot3MauType 对象标识符,如 1000BASE-T 的 ...26.4.1.1.29/.30,
// 后者可直接识别 1000BASE-GT-SFP / RJ45。
func collectCopperOpticalInterfaces(s *snmpSession, r *indexResolver, brand string, oids []string, copperValue int64, mauOIDs []string) map[string]bool {
	out := make(map[string]bool)
	for _, oid := range oids {
		if oid == "" {
			continue
		}
		for idx, media := range s.walkInts(oid) {
			if media != copperValue {
				continue
			}
			if ifIdx := r.byTableIndex(idx); ifIdx != "" {
				out[ifIdx] = true
			}
		}
	}
	for _, oid := range mauOIDs {
		if oid == "" {
			continue
		}
		// 同一 ifIndex 若有多条 MAU 记录,只有确认全部记录均为铜介质时才跳过光功率;
		// 设备同时报告铜/光类型时保持原读数,避免把真实光口误屏蔽。
		copperByIf, fiberByIf := map[string]bool{}, map[string]bool{}
		_ = s.walk(oid, func(idx string, pdu gosnmp.SnmpPDU) {
			if pdu.Type != gosnmp.ObjectIdentifier {
				return
			}
			media := mauTypeMedium(fmt.Sprint(pdu.Value))
			if media == 0 {
				return
			}
			ifIdx := r.byTableIndex(idx)
			if ifIdx == "" {
				return
			}
			if media == 1 {
				copperByIf[ifIdx] = true
			} else {
				fiberByIf[ifIdx] = true
			}
		})
		for ifIdx := range copperByIf {
			if !fiberByIf[ifIdx] {
				out[ifIdx] = true
			}
		}
	}
	if brand == "ruijie" {
		// RuijieFiberMIB 给出实际插入模块的类型与连接器文本,比 MAU 更直接:
		// 能识别 MAU 表未实现时的 1000BASE-GT-SFP / Mini-GBIC-GT,
		// 也能纠正 combo 口上 MAU 报铜介质、实际插着光模块的情况。
		types := s.walkStrings(oidRuijieFiberTransceiverTypeStr)
		connectors := s.walkStrings(oidRuijieFiberConnectorTypeStr)
		rows := make(map[string]bool, len(types)+len(connectors))
		for idx := range types {
			rows[idx] = true
		}
		for idx := range connectors {
			rows[idx] = true
		}
		for idx := range rows {
			ifIdx := r.byTableIndex(idx)
			if ifIdx == "" {
				continue
			}
			switch transceiverMedium(types[idx], connectors[idx]) {
			case 1:
				out[ifIdx] = true
			case 2:
				delete(out, ifIdx)
			}
		}
	}
	return out
}

// transceiverMedium 按收发器类型和连接器文本判断介质: 1=铜缆, 2=光纤, 0=无法判断。
// 两者矛盾(例如类型写 BASE-T、连接器写 LC)时返回 0,不据此屏蔽光功率。
//
// 铜缆: 1000BASE-T / 10GBASE-T / 100BASE-TX、锐捷型号中的 GT(Mini-GBIC-GT、
// 1000BASE-GT-SFP)、RJ45、COPPER、DAC/CU 直连铜缆。
// 光纤: LC/SC/FC/ST/MPO/MT-RJ 等光连接器,SX/LX/SR/LR/ER/ZR/BIDI、SM/MM 等光模块型号。
func transceiverMedium(moduleType, connector string) int {
	typeText := strings.ToUpper(strings.TrimSpace(moduleType))
	connText := strings.ToUpper(strings.TrimSpace(connector))
	compact := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(typeText + "|" + connText)
	tokens := strings.FieldsFunc(typeText+" "+connText, func(c rune) bool {
		return !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
	})
	copper := strings.Contains(compact, "RJ45") || strings.Contains(compact, "COPPER") ||
		strings.Contains(typeText, "BASE-T") // 含 BASE-TX
	fiber := strings.Contains(compact, "MTRJ")
	for _, t := range tokens {
		switch {
		case t == "T" || t == "GT" || t == "DAC" || t == "TWINAX":
			copper = true
		case len(t) > 2 && strings.HasPrefix(t, "CU") && t[2] >= '0' && t[2] <= '9': // XG-SFP-CU1M
			copper = true
		case t == "CU":
			copper = true
		case t == "LC" || t == "SC" || t == "FC" || t == "MPO" || t == "MTP" || t == "MU" || t == "FIBER" || t == "FIBRE":
			fiber = true
		case t == "BIDI" || t == "CWDM" || t == "DWDM" || t == "LRM":
			fiber = true
		case opticalReachToken(t):
			fiber = true
		}
	}
	switch {
	case copper && !fiber:
		return 1
	case fiber && !copper:
		return 2
	default:
		return 0
	}
}

// opticalReachToken 识别 SX、LX10、LR4、ZX80、SM1310、MM850 一类光模块型号片段。
func opticalReachToken(t string) bool {
	for _, p := range []string{"SX", "LX", "LH", "ZX", "EX", "SR", "LR", "ER", "ZR", "SM", "MM"} {
		if !strings.HasPrefix(t, p) {
			continue
		}
		rest := t[len(p):]
		if rest == "" {
			return true
		}
		if _, err := strconv.Atoi(rest); err == nil {
			return true
		}
	}
	return false
}

// mauTypeMedium 从 IANA dot3MauType OID 的末段分类介质。
// 返回 1=铜缆, 2=光纤, 0=未知/不适合据此屏蔽光功率。
func mauTypeMedium(value string) int {
	value = strings.TrimPrefix(strings.TrimSpace(value), ".")
	const root = "1.3.6.1.2.1.26.4.1.1."
	if !strings.HasPrefix(value, root) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(value, root))
	if err != nil {
		return 0
	}
	switch n {
	case 2, 4, 5, 10, 11, 14, 15, 16, 19, 20, 27, 28, 29, 30, 41, 42, 43:
		return 1 // coax / twisted pair / 1000BASE-CX,T / 10GBASE-CX4 等铜介质
	case 6, 8, 12, 13, 17, 18, 23, 24, 25, 26, 32, 34, 35, 36, 38, 39, 40, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53:
		return 2 // 明确为光纤的 MAU 类型; 通用/未知类型不做推断
	default:
		return 0
	}
}

func describeOpticalSource(rxSrc, txSrc []string) string {
	uniq := func(list []string) string {
		if len(list) == 0 {
			return "未采集到"
		}
		seen := map[string]bool{}
		var out []string
		for _, s := range list {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return strings.Join(out, "+")
	}
	rxD, txD := uniq(rxSrc), uniq(txSrc)
	if rxD == "未采集到" && txD == "未采集到" {
		return "未采集到 DDM 数据"
	}
	if rxD == txD {
		return rxD
	}
	return fmt.Sprintf("Rx: %s / Tx: %s", rxD, txD)
}

func applySplit(s *snmpSession, r *indexResolver, dst map[string]float64, sp ruijieSplitOptical, copperPorts map[string]bool) int {
	ints := s.walkInts(sp.intOID)
	decs := s.walkInts(sp.decOID)
	signs := s.walkInts(sp.signOID)
	n := 0
	for idx, iv := range ints {
		ifIdx := r.byTableIndex(idx)
		if ifIdx == "" || copperPorts[ifIdx] { // 电口模块的 -40 等占位值同样要跳过
			continue
		}
		if iv == 2147483647 {
			continue
		}
		dv := decs[idx]
		if dv < 0 {
			dv = -dv
		}
		if dv > 99 {
			dv = dv % 100
		}
		val := math.Abs(float64(iv)) + float64(dv)/100.0
		sign := signs[idx]
		if sign < 0 || iv < 0 {
			val = -val
		}
		if val == 0 || val < optMinDbm || val > optMaxDbm {
			continue
		}
		if _, exists := dst[ifIdx]; !exists {
			dst[ifIdx] = roundDbm(val)
			n++
		}
	}
	return n
}

func collectSensorOptical(s *snmpSession, r *indexResolver, rx, tx map[string]float64, copperPorts map[string]bool) (int, int) {
	types := s.walkInts(oidEntPhySensorType)
	if len(types) == 0 {
		return 0, 0
	}
	prec := s.walkInts(oidEntPhySensorPrec)
	vals := s.walkInts(oidEntPhySensorValue)
	oper := s.walkInts(oidEntPhySensorOper)
	names := r.entName
	if len(names) == 0 {
		names = s.walkStrings(oidEntPhysicalName)
	}
	descr := s.walkStrings(oidEntPhysicalDescr)

	n1, n2 := 0, 0
	for idx, t := range types {
		if t != 14 { // dBm
			continue
		}
		if st, ok := oper[idx]; ok && st != 1 { // 1 = ok
			continue
		}
		raw, ok := vals[idx]
		if !ok {
			continue
		}
		scale := math.Pow(10, float64(prec[idx]))
		if scale <= 0 {
			scale = 1
		}
		v, ok := parseOpticalRaw(raw, scale)
		if !ok {
			continue
		}
		label := strings.ToLower(names[idx] + " " + descr[idx])
		ifIdx := r.byTableIndex(idx)
		if ifIdx == "" {
			continue
		}
		if copperPorts[ifIdx] {
			continue
		}
		switch {
		case strings.Contains(label, "rx") || strings.Contains(label, "receive") || strings.Contains(label, "接收"):
			if _, e := rx[ifIdx]; !e {
				rx[ifIdx] = v
				n1++
			}
		case strings.Contains(label, "tx") || strings.Contains(label, "transmit") || strings.Contains(label, "发送"):
			if _, e := tx[ifIdx]; !e {
				tx[ifIdx] = v
				n2++
			}
		}
	}
	return n1, n2
}
