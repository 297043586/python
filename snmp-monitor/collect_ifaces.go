package main

// 采集: 接口表(速率 / 利用率 / 错包)和 ifIndex 解析器(接口名 → ifIndex)。

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// ----------------- 接口采集 -----------------

type ifInfo struct {
	Index     string
	Descr     string
	Name      string
	Alias     string
	Type      int64
	Admin     int64
	Oper      int64
	SpeedMbps int64
	InOctets  uint64
	OutOctets uint64
	InErrors  uint64
	HC        bool
	Physical  bool
	InAtMs    int64 // 入/出计数器各自被读到的时刻, 见 PortSnapshot.InAtMs
	OutAtMs   int64
}

// timedWalk 执行一次 walk 并返回它的"代表时刻"(开始和结束的中点, Unix 毫秒)。
// 一张计数器表 walk 下来可能要好几秒, 取中点比取开始或结束都更接近表里
// 大多数行实际被读出的时刻。
func timedWalk(fn func()) int64 {
	t0 := time.Now().UnixMilli()
	fn()
	return (t0 + time.Now().UnixMilli()) / 2
}

func (i *ifInfo) display() string {
	if i.Descr != "" {
		return i.Descr
	}
	if i.Name != "" {
		return i.Name
	}
	return "ifIndex-" + i.Index
}

const (
	ifTypeEthernet = 6
	ifTypeLag      = 161 // ieee8023adLag
)

// isAggInterface 判断一个接口本身是不是聚合口(Eth-Trunk / Port-Channel /
// AggregatePort / Bridge-Aggregation ...): ifType=161, 或者名字归一化后
// 属于 "agg" 族。两个条件任一满足即可——锐捷部分型号的 AggregatePort
// ifType 报的是 6 或 53, 只认 ifType 会漏; 只认名字又会漏掉命名不规范的。
func isAggInterface(e *ifInfo) bool {
	if e == nil {
		return false
	}
	if e.Type == ifTypeLag {
		return true
	}
	for _, n := range []string{e.Descr, e.Name} {
		if n == "" {
			continue
		}
		if fam, _, _ := strings.Cut(normPortKey(n), "|"); fam == "agg" {
			return true
		}
	}
	return false
}

// cleanAlias 过滤掉厂商给端口自动生成的"假描述"。
// 典型例子是 H3C: 端口没手动配置描述时, ifAlias 会原样回填成
// "<接口名> Interface"(如 "GigabitEthernet1/2/0/1 Interface"), 看起来
// 像是有人填过描述, 其实什么信息都没有, 不该当成真实描述显示出来。
var aliasIsAutoSuffixRe = regexp.MustCompile(`(?i)^\s*interface\s*$`)

func cleanAlias(alias, name, descr string) string {
	a := strings.TrimSpace(alias)
	if a == "" {
		return ""
	}
	for _, ifName := range []string{name, descr} {
		ifName = strings.TrimSpace(ifName)
		if ifName == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(a), strings.ToLower(ifName)) {
			continue
		}
		rest := a[len(ifName):]
		if aliasIsAutoSuffixRe.MatchString(rest) {
			return ""
		}
	}
	return a
}

func collectInterfaces(p *sessionPool, includeRates bool) map[string]*ifInfo {
	// 11 张表并行取回, 再统一装配。includeRates=false 时跳过三张计数器表
	// (通常是端口数最多、响应最重的几张), 该设备只看 UP/DOWN 和容量,
	// 不计算利用率 —— 对低端/老旧设备是最立竿见影的减负项。
	var (
		descr, name, alias           map[string]string
		ityp, admin, oper, hi, lo    map[string]int64
		hcIn, hcOut, in32, out32, er map[string]uint64
		inAt, outAt                  int64
	)
	walks := []func(*snmpSession){}
	if includeRates {
		// 入/出两张计数器表排在最前面: p.run 按下标错峰领取会话, 放在最后
		// 的话它们要排在 8 张描述类表后面、且各自拿到会话的时刻不一样。
		// 排在前两位能让入/出几乎同时开始读, 再配合各自记录的读取时刻,
		// 入/出速率的时间窗口才对得齐。
		walks = append(walks,
			func(s *snmpSession) { inAt = timedWalk(func() { hcIn = s.walkUints(oidIfHCInOctets) }) },
			func(s *snmpSession) { outAt = timedWalk(func() { hcOut = s.walkUints(oidIfHCOutOcts) }) },
			func(s *snmpSession) { er = s.walkUints(oidIfInErrors) },
		)
	}
	walks = append(walks,
		func(s *snmpSession) { descr = s.walkStrings(oidIfDescr) },
		func(s *snmpSession) { name = s.walkStrings(oidIfName) },
		func(s *snmpSession) { alias = s.walkStrings(oidIfAlias) },
		func(s *snmpSession) { ityp = s.walkInts(oidIfType) },
		func(s *snmpSession) { admin = s.walkInts(oidIfAdminStatus) },
		func(s *snmpSession) { oper = s.walkInts(oidIfOperStatus) },
		func(s *snmpSession) { hi = s.walkInts(oidIfHighSpeed) },
		func(s *snmpSession) { lo = s.walkInts(oidIfSpeed) },
	)
	p.run(walks...)

	ifs := make(map[string]*ifInfo)
	touch := func(idx string) *ifInfo {
		if v, ok := ifs[idx]; ok {
			return v
		}
		v := &ifInfo{Index: idx}
		ifs[idx] = v
		return v
	}
	for idx, v := range descr {
		touch(idx).Descr = v
	}
	for idx, v := range name {
		touch(idx).Name = v
	}
	for idx, v := range alias {
		touch(idx).Alias = v
	}
	for idx, v := range ityp {
		touch(idx).Type = v
	}
	for idx, v := range admin {
		touch(idx).Admin = v
	}
	for idx, v := range oper {
		touch(idx).Oper = v
	}
	for idx, v := range hi {
		touch(idx).SpeedMbps = v
	}
	for idx, v := range lo {
		e := touch(idx)
		if e.SpeedMbps == 0 && v > 0 {
			e.SpeedMbps = v / 1000000
		}
	}
	if !includeRates {
		hasType := false
		for _, e := range ifs {
			if e.Type != 0 {
				hasType = true
				break
			}
		}
		for _, e := range ifs {
			e.Physical = isPhysicalPort(e, hasType)
		}
		return ifs
	}
	for idx, v := range hcIn {
		e := touch(idx)
		e.InOctets = v
		e.HC = true
	}
	for idx, v := range hcOut {
		touch(idx).OutOctets = v
	}
	if len(hcIn) == 0 { // 老设备只有 32 位计数器
		p.run(
			func(s *snmpSession) { inAt = timedWalk(func() { in32 = s.walkUints(oidIfInOctets32) }) },
			func(s *snmpSession) { outAt = timedWalk(func() { out32 = s.walkUints(oidIfOutOctets32) }) },
		)
		for idx, v := range in32 {
			e := touch(idx)
			e.InOctets = v
			e.HC = false
		}
		for idx, v := range out32 {
			touch(idx).OutOctets = v
		}
	}
	for idx, v := range er {
		touch(idx).InErrors = v
	}
	for _, e := range ifs {
		e.InAtMs, e.OutAtMs = inAt, outAt
	}

	hasType := false
	for _, e := range ifs {
		if e.Type != 0 {
			hasType = true
			break
		}
	}
	for _, e := range ifs {
		e.Physical = isPhysicalPort(e, hasType)
	}
	return ifs
}

// warmUpCounters 只取三张计数器表, 用来建立速率基线。
// 旧版启动时跑了两轮"完整巡检"才拿到速率, 等于把最慢的一步做了两遍。
func warmUpCounters(target DeviceTarget, cfg *Config) {
	p, err := newSessionPool(target, cfg, 2)
	if err != nil {
		return
	}
	defer p.close()
	var hcIn, hcOut, er map[string]uint64
	var inAt, outAt int64
	p.run(
		func(s *snmpSession) { inAt = timedWalk(func() { hcIn = s.walkUints(oidIfHCInOctets) }) },
		func(s *snmpSession) { outAt = timedWalk(func() { hcOut = s.walkUints(oidIfHCOutOcts) }) },
		func(s *snmpSession) { er = s.walkUints(oidIfInErrors) },
	)
	hc := true
	if len(hcIn) == 0 {
		hc = false // 之前这里退回 32 位计数器后仍标 HC=true, 32 位回绕就不会被正确处理
		p.run(
			func(s *snmpSession) { inAt = timedWalk(func() { hcIn = s.walkUints(oidIfInOctets32) }) },
			func(s *snmpSession) { outAt = timedWalk(func() { hcOut = s.walkUints(oidIfOutOctets32) }) },
		)
	}
	now := time.Now().Unix()
	stateLock.Lock()
	for idx, v := range hcIn {
		portHistory[target.stateKey()+":"+idx] = PortSnapshot{
			InOctets: v, OutOctets: hcOut[idx], InErrors: er[idx], HC: hc, LastSeen: now,
			InAtMs: inAt, OutAtMs: outAt,
		}
	}
	stateLock.Unlock()
}

// isPhysicalPort: 优先用 ifType==ethernetCsmacd(6) 判断。
// 旧版只按名字前缀过滤, Tunnel / Eth-Trunk / NULL0 / InLoopBack 等虚接口
// 都会被算进"物理口总数", 接入率因此失真。
func isPhysicalPort(e *ifInfo, hasType bool) bool {
	name := strings.ToUpper(e.display())
	for _, bad := range []string{"NULL", "LOOP", "INLOOP", "VLAN", "TUNNEL", "REGISTER", "VIRTUAL-", "NVE", "M-E"} {
		if strings.HasPrefix(name, bad) {
			return false
		}
	}
	// 聚合口是逻辑口。有的固件(实测部分锐捷)给 AggregatePort 报的 ifType
	// 是 6(ethernetCsmacd)而不是 161, 光看 ifType 会把它算成物理口, 在端口
	// 表里多出一行、接入率也跟着失真。
	if isAggInterface(e) {
		return false
	}
	if hasType {
		return isEthernetType(e.Type)
	}
	return true
}

// isEthernetType: 以太网口的 ifType。新设备基本都报 6(ethernetCsmacd), 但老设备
// (老 Cisco / D-Link / 华为-3Com / 部分老锐捷)会报已经废弃的细分类型:
// 62 fastEther、69 fastEtherFX、117 gigabitEthernet、7 iso88023Csmacd、26 ethernet3Mbit。
// 以前只认 6, 这类设备 SNMP 通、系统信息也有, 但端口表一个口都没有。
func isEthernetType(t int64) bool {
	switch t {
	case ifTypeEthernet, 7, 26, 62, 69, 117:
		return true
	}
	return false
}

// ----------------- ifIndex 解析器 -----------------

type indexResolver struct {
	ifExists map[string]bool
	byNorm   map[string]string
	bySlot   map[string]string
	byRaw    map[string]string
	entToIf  map[string]string
	entName  map[string]string
}

func newIndexResolver(ifs map[string]*ifInfo) *indexResolver {
	r := &indexResolver{
		ifExists: make(map[string]bool),
		byNorm:   make(map[string]string),
		bySlot:   make(map[string]string),
		byRaw:    make(map[string]string),
		entToIf:  make(map[string]string),
		entName:  make(map[string]string),
	}
	normDup := make(map[string]bool)
	slotDup := make(map[string]bool)
	for idx, e := range ifs {
		r.ifExists[idx] = true
		for _, n := range []string{e.Descr, e.Name} {
			if n == "" {
				continue
			}
			raw := strings.ToLower(strings.ReplaceAll(n, " ", ""))
			if prev, ok := r.byRaw[raw]; !ok || prev == idx {
				r.byRaw[raw] = idx
			}
			k := normPortKey(n)
			if k == "" {
				continue
			}
			if prev, ok := r.byNorm[k]; ok && prev != idx {
				normDup[k] = true
			} else {
				r.byNorm[k] = idx
			}
			if sk := slotKey(n); sk != "" {
				if prev, ok := r.bySlot[sk]; ok && prev != idx {
					slotDup[sk] = true
				} else {
					r.bySlot[sk] = idx
				}
			}
		}
	}
	for k := range normDup {
		delete(r.byNorm, k)
	}
	for k := range slotDup {
		delete(r.bySlot, k)
	}
	return r
}

// loadEntity 载入 ENTITY-MIB, 用于把 entPhysicalIndex 索引的私有表挂回 ifIndex。
func (r *indexResolver) loadEntity(s *snmpSession) {
	r.entName = s.walkStrings(oidEntPhysicalName)
	// entAliasMappingIdentifier 的索引是 entPhysicalIndex.logicalIndex,
	// 值是形如 .1.3.6.1.2.1.2.2.1.1.49 的 OID, 末段即 ifIndex。
	_ = s.walk(oidEntAliasMappingIden, func(idx string, pdu gosnmp.SnmpPDU) {
		entIdx := strings.SplitN(idx, ".", 2)[0]
		val := strings.Trim(fmt.Sprintf("%v", pdu.Value), ". ")
		parts := strings.Split(val, ".")
		if len(parts) == 0 {
			return
		}
		ifIdx := parts[len(parts)-1]
		if r.ifExists[ifIdx] {
			r.entToIf[entIdx] = ifIdx
		}
	})
}

// byName 按接口名找 ifIndex: 原文 -> 归一化 -> 槽位号
func (r *indexResolver) byName(name string) string {
	if name == "" {
		return ""
	}
	raw := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))
	if idx, ok := r.byRaw[raw]; ok {
		return idx
	}
	if idx, ok := r.byNorm[normPortKey(name)]; ok {
		return idx
	}
	if sk := slotKey(name); sk != "" {
		if idx, ok := r.bySlot[sk]; ok {
			return idx
		}
	}
	return ""
}

// byTableIndex 把私有表的索引后缀解析成 ifIndex。
// 依次尝试: 整段就是 ifIndex -> 第一段是 ifIndex -> entPhysicalIndex 映射 -> 实体名匹配。
func (r *indexResolver) byTableIndex(idx string) string {
	if idx == "" {
		return ""
	}
	if r.ifExists[idx] {
		return idx
	}
	first := strings.SplitN(idx, ".", 2)[0]
	if r.ifExists[first] {
		return first
	}
	if v, ok := r.entToIf[idx]; ok {
		return v
	}
	if v, ok := r.entToIf[first]; ok {
		return v
	}
	if n, ok := r.entName[idx]; ok {
		if v := r.byName(n); v != "" {
			return v
		}
	}
	if n, ok := r.entName[first]; ok {
		if v := r.byName(n); v != "" {
			return v
		}
	}
	return ""
}
