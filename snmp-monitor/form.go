package main

// 设备形态识别: 盒式单机 / 堆叠(几台) / 框式(多高), 3D 拓扑、3D 机房、机柜视图共用。
//
// 以前机柜视图只看端口名: 路径第一段不同(1/0/x、2/0/x)就算不同的堆叠成员, 每个
// 成员画 1U。这有三个问题:
//   - 华为 CE / H3C 新型号的端口名是 "10GE1/0/1"、"25GE2/0/3" 这种数字开头的前缀,
//     前端正则解析不了, 两台堆叠的所有端口被塞进同一行 → 堆叠被识别成一台;
//   - 框式交换机一块板卡一个槽位号, 也被当成"很多台 1U 堆叠";
//   - 2U 的盒式交换机被画成 1U。
//
// 现在按下面的优先级判断(每一步都记下依据, 页面上悬停可以看到):
//  1. 手工指定(3D 机房 / 机柜视图里"设备形态") —— 自动识别不对时的兜底;
//  2. 型号: 框式型号表(S77xx/S127xx/CE128xx/S75xxE/S105xx/N18000/Catalyst 9400...),
//     型号末尾的槽位数决定高度; 少数 2U 盒式型号单独列出;
//  3. ENTITY-MIB: 机框(class=3)有几个 = 堆叠几台(IRF/iStack/VSU 都是每个成员一个机框);
//     只有一个机框但端口分布在多个槽位 = 框式;
//  4. 没有 ENTITY-MIB(没开扩展信息采集)时只能看端口名: 多个成员号 = 堆叠。

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type DeviceForm struct {
	Kind    string   `json:"kind"`    // box / stack / chassis
	Members int      `json:"members"` // 堆叠成员数(box=1, chassis=1)
	UEach   int      `json:"u_each"`  // 每个成员占几 U(框式 = 整机高度)
	TotalU  int      `json:"total_u"`
	Slots   int      `json:"slots,omitempty"`  // 框式: 业务板槽位数(估计)
	Models  []string `json:"models,omitempty"` // 每个成员的型号
	Keys    []string `json:"keys,omitempty"`   // 每个成员对应的端口名成员号(1/0/x 里的 1)
	Source  string   `json:"source"`           // 判断依据
	Manual  bool     `json:"manual,omitempty"` // 手工指定
	// 框式: 板卡方向 v = 竖插(板卡左右并排, 端口从上往下) / h = 横插(板卡上下叠放)
	Orient       string `json:"orient,omitempty"`
	OrientManual bool   `json:"orient_manual,omitempty"`
}

// FormOverride 是手工指定的形态, 存在 data/datacenter.json 里。
type FormOverride struct {
	Kind    string `json:"kind"`              // box / stack / chassis, 空 = 自动
	Members int    `json:"members,omitempty"` // 堆叠成员数
	UEach   int    `json:"u_each,omitempty"`  // 每台高度(框式 = 整机高度)
	Orient  string `json:"orient,omitempty"`  // 框式板卡方向 v / h, 空 = 按型号
}

// 板卡竖插的框式系列(大多是 8 槽以上的数据中心 / 园区核心框)。不在表里的按横插画,
// 判断错了在形态设置里改「板卡方向」。
var verticalChassisRe = regexp.MustCompile(`(?i)(\bCE(128|168)\d\d|\bS127\d\d|\bS125\d\d|\bS105\d\d|\bN180\d\d|\bS(97)(08|12)|Nexus\s*9[0-9]{3}|\bN9K-C95\d\d|\bN7K-C77\d\d)`)

func chassisOrient(model string) string {
	if verticalChassisRe.MatchString(model) {
		return "v"
	}
	return "h"
}

// portNameRe: 可选前缀(允许数字开头, 如 10GE / 25GE / 100GE) + 路径 + 端口号。
// 跟前端 pnlSplit 保持一致。
var portNameRe = regexp.MustCompile(`^(\d*[A-Za-z][A-Za-z_\-]*|)((?:\d+[/:.])*)(\d+)$`)

// splitPortName 把 "GigabitEthernet 1/0/12" 拆成 ("GigabitEthernet", ["1","0"], 12)。
func splitPortName(name string) (prefix string, path []string, num int, ok bool) {
	n := strings.Join(strings.Fields(name), "")
	m := portNameRe.FindStringSubmatch(n)
	if m == nil {
		return n, nil, 0, false
	}
	for _, p := range strings.FieldsFunc(m[2], func(r rune) bool { return r == '/' || r == ':' || r == '.' }) {
		path = append(path, p)
	}
	num, _ = strconv.Atoi(m[3])
	return m[1], path, num, true
}

var mgmtPortRe = regexp.MustCompile(`(?i)^(meth|mgmt|management|me|mng|m-gigabitethernet|m-ge|eth-?0)`)
var logicalPortRe = regexp.MustCompile(`(?i)^(vlan|vlanif|loopback|null|eth-trunk|port-channel|po|aggregateport|ag|bridge-aggregation|route-aggregation|tunnel|nve|vbdif|inloopback|register|sip|cpu|stack-port)`)

func isMgmtInterfaceName(name string) bool {
	return mgmtPortRe.MatchString(strings.Join(strings.Fields(name), ""))
}

// portMemberKeys 返回业务口路径第一段(三段式 1/0/x 才算)的去重列表。
func portMemberKeys(ports []PortDetail) []string {
	seen := map[string]bool{}
	var keys []string
	for _, p := range ports {
		prefix, path, _, ok := splitPortName(p.Name)
		if !ok || len(path) < 2 || mgmtPortRe.MatchString(prefix) || logicalPortRe.MatchString(prefix) {
			continue
		}
		if !seen[path[0]] {
			seen[path[0]] = true
			keys = append(keys, path[0])
		}
	}
	sort.Slice(keys, func(i, j int) bool { return naturalLess(keys[i], keys[j]) })
	return keys
}

// portSlotKeys: 四段式端口名(机框/槽位/子卡/端口, 比如 H3C 框式 IRF 的 Ten-GigabitEthernet1/3/0/1、
// 华为集群的 10GE1/2/0/1)按"机框/槽位"去重。以前只看第一段(机框号), 所有板卡的口都挤在
// 同一个"成员"上, 画出来一个 U 里几百个口。fourSeg/total 是四段式业务口占多少。
func portSlotKeys(ports []PortDetail) (slots []string, frames int, fourSeg, total int) {
	seen, fr := map[string]bool{}, map[string]bool{}
	for _, p := range ports {
		prefix, path, _, ok := splitPortName(p.Name)
		if !ok || len(path) < 2 || mgmtPortRe.MatchString(prefix) || logicalPortRe.MatchString(prefix) {
			continue
		}
		total++
		if len(path) < 3 {
			continue
		}
		fourSeg++
		fr[path[0]] = true
		k := path[0] + "/" + path[1]
		if !seen[k] {
			seen[k] = true
			slots = append(slots, k)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return naturalLess(slots[i], slots[j]) })
	return slots, len(fr), fourSeg, total
}

// portThreeSegmentSlotLayout identifies "frame/slot/port" names among three-part
// interfaces. A simple unique-value comparison fails when each frame has the same
// number of cards (for example 1/3/x, 1/8/x, 2/3/x, 2/8/x). Repeated slot numbers
// across frames plus multiple slots in a frame are stronger evidence. A zero in
// the second component usually means slot/subcard/port (for example 3/0/x), where
// the first component is the slot.
func portThreeSegmentSlotLayout(ports []PortDetail) (slots, frames int, secondIsSlot bool) {
	byFrame := map[string]map[string]bool{}
	allSlots := map[string]bool{}
	hasZeroSubslot := false
	for _, p := range ports {
		prefix, path, _, ok := splitPortName(p.Name)
		if !ok || len(path) != 2 || mgmtPortRe.MatchString(prefix) || logicalPortRe.MatchString(prefix) {
			continue
		}
		if byFrame[path[0]] == nil {
			byFrame[path[0]] = map[string]bool{}
		}
		byFrame[path[0]][path[1]] = true
		allSlots[path[1]] = true
		if path[1] == "0" {
			hasZeroSubslot = true
		}
	}
	frames = len(byFrame)
	if frames == 0 {
		return 0, 0, false
	}
	repeatedSlots := map[string]int{}
	for _, frameSlots := range byFrame {
		if len(frameSlots) > slots {
			slots = len(frameSlots)
		}
		for slot := range frameSlots {
			repeatedSlots[slot]++
		}
	}
	repeated := false
	for _, count := range repeatedSlots {
		if count > 1 {
			repeated = true
			break
		}
	}
	secondIsSlot = !hasZeroSubslot && (len(allSlots) > frames || (frames > 1 && slots > 1 && repeated))
	return slots, frames, secondIsSlot
}

// 框式型号: 型号里的槽位数 -> 高度(U)。不在表里的框式按槽位数估算。
var chassisModelRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bS(77|97|127)(0[2-9]|1[0-8])(\b|[-A-Z])`),                     // 华为 S7703/S7706/S7712, S9706, S12704/08/12
	regexp.MustCompile(`(?i)\bCE(128|168)(0[2-9]|1[0-8])(\b|[-A-Z])`),                      // 华为 CE12804/08/12/16, CE16804
	regexp.MustCompile(`(?i)\bS(75|105|125)(0[2-9]|1[0-8])(\b|[-A-Z])`),                    // H3C S7503E/S7506E/S7510E, S10508, S12508
	regexp.MustCompile(`(?i)\bN(180)(0[2-9]|1[0-8])(\b|[-A-Z])`),                           // 锐捷 N18007/N18010/N18018
	regexp.MustCompile(`(?i)\bS(86|78)(0[2-9]|1[0-8])E?(\b|[-A-Z])`),                       // 锐捷 S8605E/S8607E/S8610E, S7805/S7808
	regexp.MustCompile(`(?i)(?:\bC|WS-C|Catalyst\s*)(94|45|65)(0[2-9]|1[0-8])(\b|[-A-Z])`), // Cisco Catalyst 9404/9407/9410, 4506/4510, 6506/6509
}

// 2U 的盒式型号(不全, 识别不到可以手工指定)
var twoUBoxRe = regexp.MustCompile(`(?i)(CE88\d\d|CE8861|CE8868|-4C\b|-2C\b|S6850-2C|S9850-4C|S6800-4C|S12500X?-AF|\bRG-S6510-4C|\bS6520X?-\d+C?-SI-2U)`)

// chassisSlotsFromModel 从框式型号里取槽位数(S7706 -> 6, S12708 -> 8, CE12816 -> 16)。
func chassisSlotsFromModel(model string) (int, bool) {
	for _, re := range chassisModelRes {
		m := re.FindStringSubmatch(model)
		if m == nil {
			continue
		}
		// S9706 是 97 系列 6 槽; S12708 是 127 系列 8 槽
		n, err := strconv.Atoi(m[2])
		if err != nil || n <= 0 {
			continue
		}
		return n, true
	}
	return 0, false
}

// chassisHeightU 按槽位数估整机高度(各厂商差别不大, 仅用于画图)。
func chassisHeightU(slots int) int {
	switch {
	case slots <= 3:
		return 5
	case slots <= 4:
		return 7
	case slots <= 6:
		return 10
	case slots <= 8:
		return 14
	case slots <= 10:
		return 16
	case slots <= 12:
		return 20
	default:
		return 25
	}
}

func boxHeightU(model string) int {
	if twoUBoxRe.MatchString(model) {
		return 2
	}
	return 1
}

// detectDeviceForm 按上面的优先级判断设备形态。ov 为空表示没有手工指定。
func detectDeviceForm(rep DeviceReport, ov *FormOverride) DeviceForm {
	model := ""
	var chassis []EntChassis
	if rep.Inventory != nil {
		model = rep.Inventory.Model
		chassis = rep.Inventory.Chassis
	}
	keys := portMemberKeys(rep.Ports)
	f := DeviceForm{Kind: "box", Members: 1, UEach: boxHeightU(model), Keys: keys}
	for _, c := range chassis {
		f.Models = append(f.Models, c.Model)
	}

	slots, isChassisModel := chassisSlotsFromModel(model)
	slotKeys, frames, fourSeg, bizPorts := portSlotKeys(rep.Ports)
	perFrame := len(slotKeys)
	if frames > 1 {
		perFrame = (len(slotKeys) + frames - 1) / frames
	}
	tripleSlots, tripleFrames, tripleSecondSlot := portThreeSegmentSlotLayout(rep.Ports)
	switch {
	case isChassisModel:
		f.Kind, f.Slots, f.UEach = "chassis", slots, chassisHeightU(slots)
		f.Source = "型号 " + model + " 是框式设备(" + strconv.Itoa(slots) + " 槽)"
	case fourSeg*2 > bizPorts && len(slotKeys) >= 2:
		// 四段式端口名: 第二段才是槽位号
		f.Kind, f.Slots, f.UEach = "chassis", perFrame, chassisHeightU(perFrame+2)
		f.Source = "端口名是四段式(机框/槽位/子卡/端口), 分布在 " + strconv.Itoa(len(slotKeys)) + " 块板卡上, 按框式处理"
		if frames > 1 {
			f.Source += "(" + strconv.Itoa(frames) + " 台框式组成 IRF / 集群)"
		}
	case tripleSecondSlot:
		// 三段式 frame/slot/port: 按每台机框识别第二段槽位, 不让 1/3/x 与 1/8/x
		// 因为机框数和槽位数刚好相等而被合并。
		f.Kind, f.Slots, f.UEach = "chassis", tripleSlots, chassisHeightU(tripleSlots+2)
		f.Source = "三段式端口名按机框/槽位/端口识别, 每框有 " + strconv.Itoa(tripleSlots) + " 个槽位"
		if tripleFrames > 1 {
			f.Source += "(" + strconv.Itoa(tripleFrames) + " 个机框 / 成员)"
		}
	case len(chassis) >= 2:
		f.Kind, f.Members = "stack", len(chassis)
		f.Source = "ENTITY-MIB 有 " + strconv.Itoa(len(chassis)) + " 个机框(堆叠成员)"
		// 堆叠成员可能是不同型号, 取最高的那个
		for _, c := range chassis {
			if u := boxHeightU(c.Model); u > f.UEach {
				f.UEach = u
			}
		}
	case len(chassis) == 1 && len(keys) >= 3:
		// 只有一个机框, 端口却分布在 3 个以上的槽位号上: 框式
		f.Kind, f.Slots = "chassis", len(keys)
		f.UEach = chassisHeightU(len(keys) + 2) // 再加两块主控板
		f.Source = "ENTITY-MIB 只有 1 个机框, 端口分布在 " + strconv.Itoa(len(keys)) + " 个槽位, 按框式处理"
	case len(keys) >= 2:
		f.Kind, f.Members = "stack", len(keys)
		if len(chassis) == 1 {
			f.Source = "端口名有 " + strconv.Itoa(len(keys)) + " 个成员号(" + strings.Join(keys, "/") + "), ENTITY-MIB 只报了 1 个机框, 按堆叠处理"
		} else {
			f.Source = "端口名有 " + strconv.Itoa(len(keys)) + " 个成员号(" + strings.Join(keys, "/") + "), 按堆叠处理(打开扩展信息采集后可以用 ENTITY-MIB 确认)"
		}
	default:
		f.Source = "单台盒式设备"
		if model != "" {
			f.Source += "(" + model + ")"
		}
	}
	if f.Kind == "chassis" {
		f.Members = 1
	}

	if ov != nil && ov.Kind != "" {
		f.Manual = true
		f.Kind = ov.Kind
		switch ov.Kind {
		case "box":
			f.Members = 1
		case "stack":
			f.Members = ov.Members
			if f.Members < 2 {
				f.Members = 2
			}
		case "chassis":
			f.Members = 1
		}
		if ov.UEach > 0 {
			f.UEach = ov.UEach
		} else if ov.Kind == "chassis" && f.UEach < 5 {
			f.UEach = 10
		} else if ov.Kind != "chassis" && f.UEach > 4 {
			f.UEach = 1
		}
		f.Source = "手工指定"
	}
	if f.Members < 1 {
		f.Members = 1
	}
	if f.UEach < 1 {
		f.UEach = 1
	}
	if f.Kind == "chassis" {
		f.Orient = chassisOrient(model)
		if ov != nil && (ov.Orient == "v" || ov.Orient == "h") {
			f.Orient, f.OrientManual = ov.Orient, true
		}
	}
	f.TotalU = f.Members * f.UEach
	return f
}
