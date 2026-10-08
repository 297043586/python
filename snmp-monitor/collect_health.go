package main

// 采集: 硬件健康(电源 / 风扇 / 温度 / CPU / 内存)。

import (
	"fmt"
	"math"
	"strings"
)

// ----------------- 硬件健康 -----------------

// collectHardware 只在真正读到了可识别的状态时才下结论。
// 旧版把默认值写成"正常 (冗余供电)", 即便一个 OID 都没读到也显示正常;
// 同时又用 "val != 1 && val != 2 -> 告警" 的粗暴规则, 把 H3C 的
// entityAbsent(空槽位) 误判成"电源掉电"的致命告警。
func collectHardware(s *snmpSession, brand string, r *indexResolver, rep *DeviceReport) {
	hw := HardwareStatus{FanStatus: "未采集", PowerStatus: "未采集", Temperature: "未采集"}

	entClass := s.walkInts(oidEntPhysicalClass)
	fanIdx := map[string]bool{}
	psuIdx := map[string]bool{}
	for idx, c := range entClass {
		switch c {
		case 6:
			psuIdx[idx] = true
		case 7:
			fanIdx[idx] = true
		}
	}

	type tally struct{ ok, bad, absent, unknown int }
	var fan, psu tally
	var badDetail []string

	classify := func(t *tally, label, idx string, state int64, normal, absent []int64) {
		in := func(set []int64) bool {
			for _, v := range set {
				if v == state {
					return true
				}
			}
			return false
		}
		switch {
		case in(normal):
			t.ok++
		case in(absent):
			t.absent++
		case state <= 0:
			t.unknown++
		default:
			t.bad++
			name := r.entName[idx]
			if name == "" {
				name = idx
			}
			badDetail = append(badDetail, fmt.Sprintf("%s[%s] 状态码 %d", label, name, state))
		}
	}

	// 状态表和"哪些值算正常/不在位"来自厂商规则(vendors.go, 页面可编辑)
	for _, rule := range vendorProfile(brand).Status {
		st := s.walkInts(rule.OID)
		switch rule.Kind {
		case "fan":
			for idx, v := range st {
				classify(&fan, "风扇", idx, v, rule.OK, rule.Absent)
			}
		case "power":
			for idx, v := range st {
				classify(&psu, "电源", idx, v, rule.OK, rule.Absent)
			}
		case "entity":
			for idx := range fanIdx {
				if v, ok := st[idx]; ok {
					classify(&fan, "风扇", idx, v, rule.OK, rule.Absent)
				}
			}
			for idx := range psuIdx {
				if v, ok := st[idx]; ok {
					classify(&psu, "电源", idx, v, rule.OK, rule.Absent)
				}
			}
		}
	}

	describe := func(t tally, kind string) string {
		total := t.ok + t.bad + t.absent + t.unknown
		if total == 0 {
			return "未采集 (该型号未提供对应 OID)"
		}
		if t.bad > 0 {
			return fmt.Sprintf("异常 (%d/%d 个%s故障)", t.bad, total, kind)
		}
		if t.ok > 0 {
			return fmt.Sprintf("正常 (%d 个%s在位运行)", t.ok, kind)
		}
		return fmt.Sprintf("未知 (%d 个%s无有效状态)", total, kind)
	}
	hw.FanStatus = describe(fan, "风扇")
	hw.PowerStatus = describe(psu, "电源")

	if fan.bad > 0 {
		rep.alert("critical", "fan", "", "机箱风扇模块异常: "+strings.Join(filterPrefix(badDetail, "风扇"), "; "))
	}
	if psu.bad > 0 {
		rep.alert("critical", "psu", "", "电源模块异常(请排查一路市电是否断电): "+strings.Join(filterPrefix(badDetail, "电源"), "; "))
	}

	// 温度: 先私有 OID, 再 ENTITY-SENSOR 摄氏度传感器
	temp, ok := s.probeMetric(vendorProfile(brand).Temp, 1, 120)
	if !ok {
		temp, ok = sensorTemperature(s)
	}
	if ok {
		hw.Temperature = fmt.Sprintf("%d ℃", temp)
		profile := vendorProfile(brand)
		warningC, criticalC := profile.TempWarningC, profile.TempCriticalC
		if warningC < 1 || warningC >= 120 {
			warningC = 60
		}
		if criticalC <= warningC || criticalC > 120 {
			criticalC = 70
			if criticalC <= warningC {
				criticalC = warningC + 1
			}
		}
		if temp >= criticalC {
			rep.alert("critical", "temp", "", fmt.Sprintf("板卡温度过高: %d ℃", temp))
		} else if temp >= warningC {
			rep.alert("warning", "temp", "", fmt.Sprintf("板卡温度偏高: %d ℃", temp))
		}
	}
	rep.Hardware = hw
}

func filterPrefix(list []string, prefix string) []string {
	var out []string
	for _, v := range list {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}

func sensorTemperature(s *snmpSession) (int, bool) {
	types := s.walkInts(oidEntPhySensorType)
	if len(types) == 0 {
		return 0, false
	}
	vals := s.walkInts(oidEntPhySensorValue)
	prec := s.walkInts(oidEntPhySensorPrec)
	best := int64(-1)
	for idx, t := range types {
		if t != 8 { // celsius
			continue
		}
		raw, ok := vals[idx]
		if !ok {
			continue
		}
		div := math.Pow(10, float64(prec[idx]))
		if div <= 0 {
			div = 1
		}
		v := int64(math.Round(float64(raw) / div))
		if v > 0 && v < 120 && v > best {
			best = v
		}
	}
	if best < 0 {
		return 0, false
	}
	return int(best), true
}

// 厂商识别 detectBrand 见 vendors.go(规则可在页面上编辑)。
