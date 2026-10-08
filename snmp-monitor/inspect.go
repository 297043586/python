package main

// 局域网交换机 SNMP 深度巡检 (锐捷 / 华三 / 华为)
//
// 相较于旧版本的主要修复:
//  1. 全部表项改为 BulkWalk 一次取回, 并按 "OID 前缀" 精确切出索引(旧版用 split(".") 取最后一段,
//     在多级索引的表上必然取错), 端口数据不再逐口 Get, 48 口设备的采集报文从 ~200 个降到 ~15 个。
//  2. LLDP: 旧版把 lldpLocPortIdSubtype(.3.7.1.2, 一个枚举整数)当成了端口名, 锐捷必然匹配失败。
//     现改为 lldpLocPortId(.3.7.1.3) + lldpLocPortDesc(.3.7.1.4) 建表, 再用"接口名归一化"
//     把 Gi0/1 / GigabitEthernet 0/1 / XGE1/0/49 等写法映射到 ifIndex。
//  3. 光功率: 锐捷换成 ruijieFiberMIB(4881.1.1.10.2.105), H3C 的 TX 由 .11(MinTXPower 规格值)
//     修正为 .9(CurTXPower 实时值), 华为保留 .8/.9 并兼容 entPhysicalIndex 索引。
//  4. IP-MAC-端口: 增加 dot1qTpFdbPort(Q-BRIDGE, H3C/华为 Comware/VRP 的实际落表位置),
//     dot1dTpFdbPort 仅作回退; 并识别"上联/级联口"避免把汇聚口当成终端接入口。
//  5. 趋势图: 后端趋势切片改为拷贝(旧版共享底层数组, 与 JSON 序列化竞争),
//     前端修正字段名 d.CPU -> d.cpu, 并修掉隐藏 tab 下 canvas 宽度为 0 的问题。
//
// 本文件: 巡检主流程(一台设备一轮: 分阶段采集 → 组装端口表 → 判定告警)。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ----------------- 巡检主流程 -----------------

func alertID(deviceID, category, subject string) string {
	sum := sha256.Sum256([]byte(deviceID + "|" + category + "|" + subject))
	return hex.EncodeToString(sum[:])[:16]
}

// alert 登记一条告警。同一设备 + 同一类别 + 同一对象 在多轮之间保持同一个 ID,
// 这样用户"确认"过之后, 下一轮不会又冒出来一条新的。
func (r *DeviceReport) alert(level, category, subject, text string) {
	a := Alert{
		ID: alertID(r.stateKey, category, subject), DeviceID: r.stateKey, DeviceIP: r.IP,
		Level: level, Category: category, Subject: subject, Text: text,
	}
	if level == "critical" {
		r.Criticals = append(r.Criticals, a)
	} else {
		r.Warnings = append(r.Warnings, a)
	}
}

func inspectDevice(target DeviceTarget, cfg *Config, round int64) DeviceReport {
	start := time.Now()
	nowStr := start.Format("2006-01-02 15:04:05")
	rep := DeviceReport{
		IP:           target.IP,
		Note:         target.Note,
		Group:        target.Group,
		stateKey:     target.stateKey(),
		SysName:      target.IP,
		Brand:        strings.ToLower(strings.TrimSpace(target.Brand)),
		Collect:      target.Collect,
		Timestamp:    nowStr,
		Criticals:    []Alert{},
		Warnings:     []Alert{},
		Diagnostics:  []string{},
		Logs:         []string{},
		Ports:        []PortDetail{},
		AggGroups:    []AggGroupDetail{},
		HostMappings: []HostMappingEntry{},
		MACTable:     []MACEntry{},
		FDBTable:     []MACEntry{},
		TrendData:    []TrendPoint{},
		Phases:       []PhaseCost{},
		Hardware:     HardwareStatus{FanStatus: "未采集", PowerStatus: "未采集", Temperature: "未采集"},
	}

	// 设备管理里手工填的位置/联系人优先于 SNMP 自己上报的 sysLocation/
	// sysContact —— 很多设备压根没配过这两个标量(要在设备上敲一条
	// snmp-agent 命令), 手工填之后机柜/机房的自动归类(按位置分组)才有得靠。
	// 放在最前面、任何early return之前, 这样设备离线时机柜视图也不会跟着丢位置。
	if target.Location != "" || target.Contact != "" {
		rep.Inventory = &DeviceInventory{}
		if target.Location != "" {
			rep.Inventory.Location = target.Location
		}
		if target.Contact != "" {
			rep.Inventory.Contact = target.Contact
		}
	}

	var phaseLock sync.Mutex
	phase := func(name string, fn func()) {
		t0 := time.Now()
		fn()
		phaseLock.Lock()
		rep.Phases = append(rep.Phases, PhaseCost{Name: name, Ms: time.Since(t0).Milliseconds()})
		phaseLock.Unlock()
	}

	pool, err := newSessionPool(target, cfg, cfg.WalkParallel)
	if err != nil {
		rep.alert("critical", "unreachable", "", "无法建立 SNMP 会话 (UDP "+strconv.Itoa(cfg.SNMPPort)+" 不可达)")
		rep.Logs = append(rep.Logs, fmt.Sprintf("[%s] 异常: %v", nowStr, err))
		rep.CostMs = time.Since(start).Milliseconds()
		return rep
	}
	defer pool.close()
	s := pool.primary()

	// 1. 系统信息
	base, err := s.c.Get([]string{oidSysName, oidSysUpTime, oidSysDescr, oidSysObjectID})
	if err != nil {
		// 设备本身可达, 只是第一个包响应慢(CPU 忙/丢包)时, 直接判"离线"
		// 太武断。用双倍超时的独立连接单独再试一次, 拿一次机会区分
		// "确实不可达" 和 "只是慢"。
		slowCfg := *cfg
		slowCfg.TimeoutSeconds *= 2
		if s2, dialErr := dialSNMP(target, &slowCfg); dialErr == nil {
			base, err = s2.c.Get([]string{oidSysName, oidSysUpTime, oidSysDescr, oidSysObjectID})
			if s2.c.Conn != nil {
				_ = s2.c.Conn.Close()
			}
		}
	}
	if err != nil || base == nil || len(base.Variables) == 0 {
		rep.alert("critical", "unreachable", "",
			"SNMP 无响应 (请检查团体名/ACL 是否放通; 若确认设备可达但经常超时, "+
				"可在 config.json 里调大 timeout_seconds、调小 max_repetitions 或调小 walk_parallel)")
		rep.Logs = append(rep.Logs, fmt.Sprintf("[%s] 异常: %v", nowStr, err))
		rep.CostMs = time.Since(start).Milliseconds()
		return rep
	}
	rep.IsAlive = true

	var sysObjectID string
	var upTicks int64 // sysUpTime(1/100 秒), 用来把 ifLastChange 换算成"多久之前"
	for _, v := range base.Variables {
		val := strings.TrimPrefix(v.Name, ".")
		switch {
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.5"):
			if n := shortSysName(decodeText(v.Value)); n != "" {
				rep.SysName = n
			}
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.1"):
			rep.SysDescr = decodeText(v.Value)
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.2"):
			sysObjectID = fmt.Sprintf("%v", v.Value)
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.3"):
			upTicks = toInt64(v.Value)
			sec := upTicks / 100
			rep.UpTimeStr = fmt.Sprintf("%d天 %d小时 %d分", sec/86400, (sec%86400)/3600, (sec%3600)/60)
			rep.UpTimeSec = sec
			if sec > 0 && sec < 600 {
				rep.alert("critical", "reboot", "", fmt.Sprintf("设备疑似刚重启, 运行时长仅 %d 秒", sec))
			}
		}
	}

	// 2. 厂商识别
	detected := detectBrand(sysObjectID, rep.SysDescr)
	rep.BrandDetected = detected
	if rep.Brand == "" || rep.Brand == "auto" {
		rep.Brand = detected
	}
	if rep.Brand == "" {
		rep.Brand = "generic"
		rep.Diagnostics = append(rep.Diagnostics, "未能识别厂商, 仅采集标准 MIB (CPU/内存/光功率可能为空)")
	} else if detected != "" && detected != rep.Brand {
		rep.Diagnostics = append(rep.Diagnostics,
			fmt.Sprintf("配置写的是 %s, 但 sysObjectID 显示这是 %s 设备, 已按配置采集", rep.Brand, detected))
	}
	brand := rep.Brand

	// 3. 接口表 (内部已并行)
	var ifs map[string]*ifInfo
	phase("接口表", func() { ifs = collectInterfaces(pool, !target.Collect.SkipRates) })
	if len(ifs) == 0 {
		rep.alert("critical", "unreachable", "", "读不到接口表 ifTable, 请检查 SNMP view 是否放通")
		rep.CostMs = time.Since(start).Milliseconds()
		return rep
	}
	resolver := newIndexResolver(ifs)
	phase("实体表", func() { resolver.loadEntity(pool.primary()) })

	// 4. 其余各表互不依赖, 全部并行 —— 但 LLDP/光衰/MAC-ARP 这三张开销较大
	// 的表按设备的 Collect 设置决定采不采, 低端设备可以关掉不需要的部分。
	var (
		cpuVal, memVal       int
		cpuOK, memOK         bool
		lldp                 map[string]lldpNeighbor
		lldpTotal, lldpUnres int
		rxPower, txPower     map[string]float64
		optSource            string
		fdb                  fdbResult
		arp                  []arpEntry
		portLayer            portLayerInfo
		portExt              portExtRaw
		stp                  *STPInfo
		previousHeavy        heavyCache
		hasPreviousHeavy     bool
		heavy                bool
	)
	if !target.Collect.SkipHosts {
		stateLock.RLock()
		previousHeavy, hasPreviousHeavy = lastHeavy[target.stateKey()]
		stateLock.RUnlock()
	}
	// 首次采集没有可复用的大表缓存, 立即读一次; 后续按上次大表采集轮次计算间隔。
	heavy = !target.Collect.SkipHosts && (cfg.HeavyEveryNRounds <= 1 || !hasPreviousHeavy ||
		round-previousHeavy.round >= int64(cfg.HeavyEveryNRounds))
	hwRep := DeviceReport{IP: rep.IP, stateKey: rep.stateKey}
	rxPower, txPower = map[string]float64{}, map[string]float64{}
	fdb = fdbResult{macToIf: map[string]string{}, macVlan: map[string]string{}, macLocations: map[string][]string{},
		portMACs: map[string]int{}, portVlan: map[string]string{}, source: "本轮跳过(按 heavy_every_n_rounds 抽样)"}

	tasks := []func(*snmpSession){
		func(s *snmpSession) {
			phase("CPU/内存", func() {
				vp := vendorProfile(brand)
				cpuVal, cpuOK = s.probeMetric(vp.CPU, 0, 100)
				memVal, memOK = s.probeMetric(vp.Mem, 0, 100)
				if !memOK && len(vp.MemPool) > 0 {
					memVal, memOK = s.probeMemPool(vp.MemPool)
				}
			})
		},
		func(s *snmpSession) {
			// 硬件采集会产生告警, 写到临时对象里, 等并行阶段结束再合并,
			// 免得多个 goroutine 同时往同一个切片上 append
			phase("硬件健康", func() { collectHardware(s, brand, resolver, &hwRep) })
		},
		func(s *snmpSession) {
			phase("端口层级/聚合", func() { portLayer = collectPortLayerInfo(s, ifs, resolver) })
			stp = collectSTP(s)
		},
	}
	if !target.Collect.SkipExtended {
		tasks = append(tasks, func(s *snmpSession) {
			phase("扩展信息", func() {
				portExt = collectPortExt(s)
				rep.Inventory = cachedInventory(rep.stateKey, s)
			})
		})
	}
	if !target.Collect.SkipLLDP {
		tasks = append(tasks, func(s *snmpSession) {
			phase("LLDP", func() {
				lldp, lldpTotal, lldpUnres = collectLLDP(s, resolver)
				lldpTotal += collectCDP(s, resolver, lldp)
			})
		})
	}
	if !target.Collect.SkipOptical {
		tasks = append(tasks, func(s *snmpSession) {
			phase("光功率", func() { rxPower, txPower, optSource = collectOptical(s, brand, resolver) })
		})
	}
	if heavy {
		tasks = append(tasks,
			func(s *snmpSession) { phase("MAC表", func() { fdb = collectFDB(s, resolver) }) },
			func(s *snmpSession) { phase("ARP表", func() { arp = collectARP(s) }) },
		)
	} else if !target.Collect.SkipHosts && hasPreviousHeavy {
		// 跳过大表时沿用上一轮结果, 页面不至于突然空掉
		fdb, arp = previousHeavy.fdb, previousHeavy.arp
	}
	pool.run(tasks...)

	seenOwn := map[string]bool{}
	for _, a := range portLayer.addrs {
		if !seenOwn[a.IP] {
			seenOwn[a.IP] = true
			rep.OwnIPs = append(rep.OwnIPs, a.IP)
		}
	}
	for _, ip := range portLayer.ip { // 聚合成员口继承的是同一个 IP, 去重
		if !seenOwn[ip] {
			seenOwn[ip] = true
			rep.OwnIPs = append(rep.OwnIPs, ip)
		}
	}
	rep.OwnAddrs, rep.OwnMACs = portLayer.addrs, portLayer.ownMACs
	sort.Slice(rep.OwnIPs, func(i, j int) bool { return ipLess(rep.OwnIPs[i], rep.OwnIPs[j]) })

	if stp != nil {
		applySTPDelta(target.stateKey(), stp)
		rep.STP = stp
		if !cfg.Alerts.STP.Disabled && stp.Delta >= int64(cfg.Alerts.STP.Threshold) {
			lvl := "warning"
			if float64(stp.Delta) >= cfg.Alerts.STP.Threshold*5 {
				lvl = "critical"
			}
			rep.alert(lvl, "stp", "",
				fmt.Sprintf("生成树拓扑在上一轮巡检之后变化了 %d 次(最近一次 %s 前); 频繁重算通常是有口在反复 UP/DOWN、私接交换机形成环路或边缘口没开 edged-port/portfast",
					stp.Delta, fmtDuration(stp.SinceChangeSec)))
		}
	}

	// 设备管理里手工填的位置/联系人优先于 SNMP 自己上报的 sysLocation/
	// sysContact —— 很多设备压根没配过这两个标量(要在设备上敲一条
	// snmp-agent 命令), 手工填之后机柜/机房的自动归类(按位置分组)才有得靠。
	if target.Location != "" || target.Contact != "" {
		if rep.Inventory == nil {
			rep.Inventory = &DeviceInventory{}
		}
		if target.Location != "" {
			rep.Inventory.Location = target.Location
		}
		if target.Contact != "" {
			rep.Inventory.Contact = target.Contact
		}
	}

	if heavy {
		stateLock.Lock()
		lastHeavy[target.stateKey()] = heavyCache{fdb: fdb, arp: arp, round: round}
		stateLock.Unlock()
	}
	rep.FDBSource = fdb.source

	// 用 LLDP + 端口角色信息对"一个 MAC 出现在多个端口"的情况做最终裁决,
	// 把 fdb.macToIf / fdb.portMACs 换成裁决后的结果, 后面所有引用这两个
	// 字段的地方(端口 MAC 计数、上联口判定、IP-MAC 定位)都自动生效。
	var macAmbiguous map[string]bool
	if !target.Collect.SkipHosts {
		fdb.macToIf, fdb.portMACs, macAmbiguous = resolveFDB(fdb, ifs, lldp, cfg.UplinkMACThreshold)
	}

	rep.Hardware = hwRep.Hardware
	rep.Criticals = append(rep.Criticals, hwRep.Criticals...)
	rep.Warnings = append(rep.Warnings, hwRep.Warnings...)

	if cpuOK {
		rep.CPUUsage = cpuVal
		if !cfg.Alerts.CPU.Disabled {
			if cpuVal >= 90 {
				rep.alert("critical", "cpu", "", fmt.Sprintf("CPU 严重过载: %d%%", cpuVal))
			} else if cpuVal >= int(cfg.Alerts.CPU.Threshold) {
				rep.alert("warning", "cpu", "", fmt.Sprintf("CPU 负荷偏高: %d%%", cpuVal))
			}
		}
	} else {
		rep.Diagnostics = append(rep.Diagnostics, "CPU 利用率未采集到 (该型号私有 OID 不同或未开放)")
	}
	if memOK {
		rep.MemUsage = memVal
		if !cfg.Alerts.Mem.Disabled && memVal >= int(cfg.Alerts.Mem.Threshold) {
			rep.alert("warning", "mem", "", fmt.Sprintf("内存使用率偏高: %d%%", memVal))
		}
	}
	if target.Collect.SkipLLDP {
		rep.Diagnostics = append(rep.Diagnostics, "LLDP 采集已按该设备的配置关闭")
	} else if lldpTotal == 0 {
		rep.Diagnostics = append(rep.Diagnostics, "LLDP 邻居表为空 (设备未开启 lldp enable, 或 SNMP view 未放通 1.0.8802)")
	} else if lldpUnres > 0 {
		rep.Diagnostics = append(rep.Diagnostics,
			fmt.Sprintf("LLDP 共 %d 条邻居, 其中 %d 条无法对应到本地接口", lldpTotal, lldpUnres))
	}
	switch {
	case target.Collect.SkipOptical:
		rep.Diagnostics = append(rep.Diagnostics, "光功率采集已按该设备的配置关闭")
	case len(rxPower) == 0 && len(txPower) == 0:
		if strings.Contains(optSource, "铜缆电口模块已识别并跳过光功率") {
			rep.Diagnostics = append(rep.Diagnostics, "光功率: "+optSource)
		} else {
			rep.Diagnostics = append(rep.Diagnostics, "光功率: "+optSource+" (光口需支持 DDM, 电口无此数据)")
		}
	case len(txPower) < len(rxPower):
		rep.Diagnostics = append(rep.Diagnostics,
			fmt.Sprintf("光功率: 收光(Rx)采到 %d 个光口, 发光(Tx)只采到 %d 个 (来源: %s), 该型号可能不支持读取实时发光功率",
				len(rxPower), len(txPower), optSource))
	case len(rxPower) < len(txPower):
		rep.Diagnostics = append(rep.Diagnostics,
			fmt.Sprintf("光功率: 发光(Tx)采到 %d 个光口, 收光(Rx)只采到 %d 个 (来源: %s)",
				len(txPower), len(rxPower), optSource))
	}
	if target.Collect.SkipHosts {
		rep.Diagnostics = append(rep.Diagnostics, "MAC 表/ARP/IP-MAC 定位已按该设备的配置关闭")
	}
	if portLayer.aggDiag != "" {
		rep.Diagnostics = append(rep.Diagnostics, "聚合口: "+portLayer.aggDiag)
	}
	if target.Collect.SkipRates {
		rep.Diagnostics = append(rep.Diagnostics, "端口速率/利用率/错包统计已按该设备的配置关闭 (仍会显示 UP/DOWN)")
	}

	// 老设备兼容性提示: 以前这类设备"SNMP 通了但什么都没有", 页面上看不出原因
	for _, ps := range pool.all {
		if ps.noBulk.Load() {
			rep.Diagnostics = append(rep.Diagnostics, "这台设备对 GETBULK 响应不正常(老设备常见), 已自动改用逐条 GETNEXT 读取, 采集会慢一些")
			break
		}
	}
	if phys := countPhysical(ifs); phys == 0 {
		rep.Diagnostics = append(rep.Diagnostics, "接口表里有 "+strconv.Itoa(len(ifs))+" 个接口, 但没有一个被认成物理口 (ifType: "+ifTypeSummary(ifs)+"), 如果这是个交换机, 请把这条信息反馈给开发")
	}
	if !memOK && cpuOK {
		rep.Diagnostics = append(rep.Diagnostics, "内存使用率未采集到 (该型号私有 OID 不同或未开放, 可在 设置 → 厂商/OID 里补充)")
	}

	// 5. 组装端口明细
	now := time.Now()
	idxList := make([]string, 0, len(ifs))
	for idx, e := range ifs {
		if e.Physical {
			idxList = append(idxList, idx)
		}
	}
	sort.Slice(idxList, func(i, j int) bool {
		return naturalLess(ifs[idxList[i]].display(), ifs[idxList[j]].display())
	})

	var totalIn, totalOut float64
	aggMembers := make(map[string][]string) // 聚合口显示名 -> 成员物理口显示名, 组装独立的聚合口表用
	maxSampleAge := int64(cfg.CheckIntervalSeconds)*10 + 600
	for _, idx := range idxList {
		e := ifs[idx]
		name := e.display()

		status := "DOWN"
		switch {
		case e.Oper == 1:
			status = "UP"
			rep.UpPorts++
		case e.Admin == 2:
			status = "ADMIN-DOWN"
			rep.DownPorts++
		default:
			rep.DownPorts++
		}
		rep.TotalPorts++

		var inSpeed, outSpeed, inUsage, outUsage float64
		var deltaErr int64
		var ext *PortExt

		if !target.Collect.SkipRates || !target.Collect.SkipExtended {
			key := target.stateKey() + ":" + idx
			snap := PortSnapshot{LastSeen: now.Unix()}
			if !target.Collect.SkipRates {
				snap.InOctets, snap.OutOctets, snap.InErrors, snap.HC = e.InOctets, e.OutOctets, e.InErrors, e.HC
				snap.InAtMs, snap.OutAtMs = e.InAtMs, e.OutAtMs
			}
			if !target.Collect.SkipExtended {
				snap.OutErrors, snap.InDiscards, snap.OutDiscards = portExt.outErr[idx], portExt.inDisc[idx], portExt.outDisc[idx]
				snap.InBcast, snap.InMcast, snap.ExtAtMs = portExt.bcast[idx], portExt.mc[idx], portExt.atMs
			}
			stateLock.Lock()
			prev, exists := portHistory[key]
			portHistory[key] = snap
			stateLock.Unlock()

			if exists && !target.Collect.SkipRates {
				// 入/出各用各自被读到的时刻算时间窗口(见 PortSnapshot.InAtMs)。
				// 旧快照没有毫秒时刻时退回整秒 LastSeen。
				inDur := sampleSeconds(e.InAtMs, prev.InAtMs, now.Unix(), prev.LastSeen)
				outDur := sampleSeconds(e.OutAtMs, prev.OutAtMs, now.Unix(), prev.LastSeen)
				// 重启后恢复的快照如果太旧就不用, 否则会算出一个被长间隔稀释的假平均值
				if inDur >= 2 && outDur >= 2 && inDur <= float64(maxSampleAge) && outDur <= float64(maxSampleAge) {
					inSpeed = plausibleRate(float64(counterDelta(e.InOctets, prev.InOctets, !e.HC))*8/inDur/1e6, e.SpeedMbps)
					outSpeed = plausibleRate(float64(counterDelta(e.OutOctets, prev.OutOctets, !e.HC))*8/outDur/1e6, e.SpeedMbps)
					deltaErr = int64(counterDelta(e.InErrors, prev.InErrors, false))

					if e.SpeedMbps > 0 {
						inUsage = math.Min(inSpeed/float64(e.SpeedMbps)*100, 100)
						outUsage = math.Min(outSpeed/float64(e.SpeedMbps)*100, 100)
					}
					if status == "UP" {
						totalIn += inSpeed
						totalOut += outSpeed
					}
				}
			}

			if !target.Collect.SkipExtended {
				ext = &PortExt{Duplex: duplexText(portExt.duplex[idx]), LastChangeSec: -1}
				if lc, ok := portExt.lastChange[idx]; ok && upTicks > 0 && lc <= upTicks {
					ext.LastChangeSec = (upTicks - lc) / 100
				}
				if exists && prev.ExtAtMs > 0 {
					dur := float64(snap.ExtAtMs-prev.ExtAtMs) / 1000
					if dur >= 2 && dur <= float64(maxSampleAge) {
						ext.DeltaOutErrors = int64(counterDelta(snap.OutErrors, prev.OutErrors, false))
						ext.DeltaInDiscard = int64(counterDelta(snap.InDiscards, prev.InDiscards, false))
						ext.DeltaOutDiscrd = int64(counterDelta(snap.OutDiscards, prev.OutDiscards, false))
						ext.InBcastPps = math.Round(float64(counterDelta(snap.InBcast, prev.InBcast, false))/dur*10) / 10
						ext.InMcastPps = math.Round(float64(counterDelta(snap.InMcast, prev.InMcast, false))/dur*10) / 10
					}
				}
			}
		}

		duplex, dupSrc := portLayer.duplex[idx], portLayer.dupSrc[idx]
		if ext != nil && ext.Duplex != "" && duplex == "" {
			duplex, dupSrc = ext.Duplex, "EtherLike-MIB"
		}
		if duplex == "" && status == "UP" && e.SpeedMbps >= 10000 {
			duplex, dupSrc = "全双工", "推断: 10G 及以上只有全双工"
		}
		if ext != nil {
			ext.Duplex = duplex
		}
		if status == "UP" && duplex == "半双工" && e.SpeedMbps >= 100 {
			rep.alert("warning", "duplex", name,
				fmt.Sprintf("端口 [%s] 工作在半双工 (%dM), 百兆以上链路半双工基本都是两端协商不一致, 会导致大量冲突和丢包", name, e.SpeedMbps))
		}
		if ext != nil && status == "UP" && !cfg.Alerts.Broadcast.Disabled && ext.InBcastPps >= cfg.Alerts.Broadcast.Threshold {
			lvl := "warning"
			if ext.InBcastPps >= cfg.Alerts.Broadcast.Threshold*5 {
				lvl = "critical"
			}
			rep.alert(lvl, "broadcast", name,
				fmt.Sprintf("端口 [%s] 入方向广播 %.0f 包/秒 (阈值 %.0f), 是广播风暴 / 二层环路的典型信号; 到「🩺 二层体检」里看是不是多台设备同时出现", name, ext.InBcastPps, cfg.Alerts.Broadcast.Threshold))
		}

		rxStr, txStr := "-", "-"
		var rxPtr, txPtr *float64
		if v, ok := rxPower[idx]; ok {
			vv := v
			rxPtr = &vv
			rxStr = formatDbm(v)
		}
		if v, ok := txPower[idx]; ok {
			vv := v
			txPtr = &vv
			txStr = formatDbm(v)
		}

		neighbor := lldp[idx]
		lldpDev, lldpPort := "-", "-"
		if neighbor.Dev != "" {
			lldpDev, lldpPort = neighbor.Dev, neighbor.Port
		}
		nbrSrc := neighbor.Src

		macCount := fdb.portMACs[idx]
		isUplink := neighbor.Dev != "" || macCount >= cfg.UplinkMACThreshold

		if !cfg.Alerts.PortError.Disabled && status == "UP" && deltaErr >= int64(cfg.Alerts.PortError.Threshold) {
			rep.alert("warning", "port_err", name,
				fmt.Sprintf("端口 [%s] 收包错误持续增加 +%d (收光 %s)", name, deltaErr, rxStr))
		}
		if !cfg.Alerts.Optical.Disabled && rxPtr != nil && *rxPtr <= cfg.Alerts.Optical.Threshold && status == "UP" {
			rep.alert("warning", "optical", name,
				fmt.Sprintf("端口 [%s] 收光功率过低: %s, 建议检查光纤/法兰盘", name, rxStr))
		}
		if !cfg.Alerts.Bandwidth.Disabled && (inUsage >= cfg.Alerts.Bandwidth.Threshold || outUsage >= cfg.Alerts.Bandwidth.Threshold) {
			rep.alert("warning", "bandwidth", name,
				fmt.Sprintf("端口 [%s] 带宽接近跑满 (入 %.1f%% / 出 %.1f%%)", name, inUsage, outUsage))
		}

		layer, vlanID, ipAddr := "", "", ""
		var vlanMode, vlanTagged, vlanUntagged, vlanSrc string
		if v, ok := portLayer.ip[idx]; ok {
			layer, ipAddr = "L3", v
		} else {
			if v, ok := portLayer.vlan[idx]; ok {
				layer, vlanID, vlanSrc = "L2", v, "PVID"
			}
			if vi := portLayer.vlans[idx]; vi != nil {
				layer, vlanMode, vlanSrc = "L2", vi.mode, vi.source
				vlanTagged, vlanUntagged = compactVlanList(vi.tagged), compactVlanList(vi.untagged)
				if vi.native > 0 {
					vlanID = strconv.Itoa(vi.native)
				}
			}
		}

		if g := portLayer.aggGroup[idx]; g != "" {
			aggMembers[g] = append(aggMembers[g], name)
		}

		rep.Ports = append(rep.Ports, PortDetail{
			Index: idx, Name: name, Alias: cleanAlias(e.Alias, e.Name, e.Descr), Status: status,
			SpeedMbps: e.SpeedMbps, InSpeedMbps: inSpeed, OutSpeedMbps: outSpeed,
			InUsagePercent: inUsage, OutUsagePercent: outUsage,
			DeltaErrors: deltaErr, TotalErrors: int64(e.InErrors),
			OpticalRxPower: rxStr, OpticalTxPower: txStr,
			OpticalRxDbm: rxPtr, OpticalTxDbm: txPtr,
			LLDPRemoteDev: lldpDev, LLDPRemotePort: lldpPort, LLDPRemotePortAlt: neighbor.Alt, NeighborSrc: nbrSrc, LLDPRemoteIP: neighbor.MgmtIP, LLDPRemoteMAC: neighbor.Chassis,
			MACCount: macCount, IsUplink: isUplink,
			Layer: layer, VlanID: vlanID, IPAddr: ipAddr,
			VlanMode: vlanMode, VlanTagged: vlanTagged, VlanUntagged: vlanUntagged, VlanSource: vlanSrc,
			Duplex: duplex, DuplexSource: dupSrc,
			AggGroup: portLayer.aggGroup[idx], Ext: ext,
		})
	}

	rep.AggGroups = buildAggGroups(aggMembers, rep.Ports, ifs, portLayer)

	// 6. MAC 地址总表: 汇总 + 去重后的全量视图, 不要求有 ARP/IP。
	if !target.Collect.SkipHosts {
		rep.MACTable = make([]MACEntry, 0, len(fdb.macLocations))
		for mac, ifIdx := range fdb.macToIf {
			e, ok := ifs[ifIdx]
			if !ok {
				continue
			}
			amb := macAmbiguous[mac]
			kind := "接入口"
			isUp := fdb.portMACs[ifIdx] >= cfg.UplinkMACThreshold || lldp[ifIdx].Dev != ""
			if isUp {
				kind = "上联/级联口"
			}
			vlan := fdb.macVlan[mac]
			if vlan == "" {
				if v, ok := fdb.portVlan[ifIdx]; ok {
					vlan = v
				} else {
					vlan = "-"
				}
			}
			entry := MACEntry{MAC: mac, VlanID: vlan, Port: e.display(), PortKind: kind, IsUplink: isUp, Ambiguous: amb}
			if amb {
				for _, l := range fdb.macLocations[mac] {
					if le, ok := ifs[l]; ok {
						entry.Locations = append(entry.Locations, le.display())
					}
				}
			}
			rep.MACTable = append(rep.MACTable, entry)
		}
		sort.Slice(rep.MACTable, func(i, j int) bool { return rep.MACTable[i].MAC < rep.MACTable[j].MAC })
	}
	for _, row := range fdb.fdbRows {
		if len(row.Locations) == 0 {
			continue
		}
		ifIdx := row.Locations[0]
		for _, candidate := range row.Locations {
			if candidate == fdb.macToIf[row.MAC] {
				ifIdx = candidate
				break
			}
		}
		e, ok := ifs[ifIdx]
		if !ok {
			continue
		}
		isUplink := fdb.portMACs[ifIdx] >= cfg.UplinkMACThreshold || lldp[ifIdx].Dev != ""
		kind := "接入口"
		if isUplink {
			kind = "上联/级联口"
		}
		entry := MACEntry{
			MAC: row.MAC, VlanID: row.VlanID, Port: e.display(),
			PortKind: kind, IsUplink: isUplink, Ambiguous: len(row.Locations) > 1,
		}
		if entry.Ambiguous {
			for _, location := range row.Locations {
				if le, ok := ifs[location]; ok {
					entry.Locations = append(entry.Locations, le.display())
				}
			}
		}
		rep.FDBTable = append(rep.FDBTable, entry)
	}

	// 7. IP - MAC - 物理端口
	//
	// VLAN 号的可信度分三档, 依次尝试:
	//   1. fdb.macVlan  —— 来自 dot1qTpFdbTable, 精确值
	//   2. fdb.portVlan —— 该口的 PVID, 对 access 口准确, 对 trunk 口只是主 VLAN 猜测
	//   3. 从三层 SVI 接口名(如 "Vlan-interface10")里抠数字 —— 猜测, 且只对该条 ARP 本身适用
	// 只有第 1 档才标记 VlanExact=true。
	buildEntry := func(a arpEntry) HostMappingEntry {
		vlanName := "-"
		if e, ok := ifs[a.IfIndex]; ok {
			vlanName = e.display()
		} else if a.IfIndex != "" {
			vlanName = "ifIndex-" + a.IfIndex
		}
		entry := HostMappingEntry{IP: a.IP, MAC: a.MAC, VlanIf: vlanName, RealPort: "未定位", PortKind: "未定位"}

		if v, ok := fdb.macVlan[a.MAC]; ok && v != "" {
			entry.VlanID, entry.VlanExact = v, true
		}
		if ifIdx, ok := fdb.macToIf[a.MAC]; ok {
			if e, ok2 := ifs[ifIdx]; ok2 {
				entry.RealPort = e.display()
				if fdb.portMACs[ifIdx] >= cfg.UplinkMACThreshold || lldp[ifIdx].Dev != "" {
					entry.PortKind = "上联/级联口"
				} else {
					entry.PortKind = "接入口"
				}
				if entry.VlanID == "" {
					if v, ok := fdb.portVlan[ifIdx]; ok {
						entry.VlanID = v // 档 2: PVID 猜测
					}
				}
			}
		}
		if entry.VlanID == "" {
			if v := vlanFromIfaceName(vlanName); v != "" {
				entry.VlanID = v // 档 3: 接口名猜测
			}
		}
		if entry.VlanID == "" {
			entry.VlanID = "-"
		}
		return entry
	}

	for _, a := range arp {
		rep.HostMappings = append(rep.HostMappings, buildEntry(a))
	}

	// 补充查询: 如果配置了 per-VLAN 团体名, 就针对"还没定位到端口, 但已经
	// 猜到/确定 VLAN 号"的记录, 按 VLAN 去重后各查一次, 把新解出的
	// MAC->端口 合并回 fdb 再重新生成一遍 HostMappings。
	vlanFormat := target.VlanCommunityFormat
	if vlanFormat == "" {
		vlanFormat = cfg.VlanCommunityFormat
	}
	if vlanFormat != "" {
		need := map[string]bool{}
		for _, h := range rep.HostMappings {
			if h.PortKind == "未定位" && h.VlanID != "-" {
				need[h.VlanID] = true
			}
		}
		gained := 0
		for vlanID := range need {
			extra := resolveVlanCommunity(target, cfg, vlanFormat, vlanID, resolver)
			for mac, ifIdx := range extra {
				if _, dup := fdb.macToIf[mac]; dup {
					continue
				}
				fdb.macToIf[mac] = ifIdx
				fdb.portMACs[ifIdx]++
				gained++
			}
		}
		if gained > 0 {
			rep.HostMappings = rep.HostMappings[:0]
			for _, a := range arp {
				rep.HostMappings = append(rep.HostMappings, buildEntry(a))
			}
			rep.Diagnostics = append(rep.Diagnostics,
				fmt.Sprintf("通过 per-VLAN 团体名补充定位到 %d 条 MAC 记录", gained))
		}
	}

	if rep.TotalPorts > 0 {
		ratio := float64(rep.UpPorts) / float64(rep.TotalPorts) * 100
		rep.PortUsageRatio = fmt.Sprintf("%.1f%% (%d/%d口)", ratio, rep.UpPorts, rep.TotalPorts)
	} else {
		rep.PortUsageRatio = "-"
	}

	// 7. 趋势点
	stateLock.Lock()
	list := append(deviceTrendMap[target.stateKey()], TrendPoint{
		TS:       now.Unix(),
		Time:     now.Format("15:04:05"),
		CPU:      rep.CPUUsage,
		Mem:      rep.MemUsage,
		MACCount: len(fdb.macToIf),
		InMbps:   math.Round(totalIn*100) / 100,
		OutMbps:  math.Round(totalOut*100) / 100,
	})
	if len(list) > cfg.trendCap() {
		list = list[len(list)-cfg.trendCap():]
	}
	deviceTrendMap[target.stateKey()] = list
	rep.TrendData = append([]TrendPoint(nil), list...)
	stateLock.Unlock()

	lldpCount := 0
	for _, pd := range rep.Ports {
		if pd.LLDPRemoteDev != "-" {
			lldpCount++
		}
	}
	located := 0
	for _, h := range rep.HostMappings {
		if h.PortKind == "接入口" {
			located++
		}
	}
	rep.CostMs = time.Since(start).Milliseconds()
	sort.Slice(rep.Phases, func(i, j int) bool { return rep.Phases[i].Ms > rep.Phases[j].Ms })
	rep.Logs = append(rep.Logs, fmt.Sprintf(
		"[%s] 巡检完成: 物理口 %d/%d UP, LLDP 拓扑 %d 条, MAC 表来源 %s, ARP %d 条(精确定位到接入口 %d 条), 光功率来源 %s, 耗时 %dms",
		nowStr, rep.UpPorts, rep.TotalPorts, lldpCount, fdb.source, len(rep.HostMappings), located, optSource, rep.CostMs))
	return rep
}

func countPhysical(ifs map[string]*ifInfo) int {
	n := 0
	for _, e := range ifs {
		if e.Physical {
			n++
		}
	}
	return n
}

// ifTypeSummary: "6×24, 53×3" 这种各类型的个数, 排查老设备端口认不出来时用
func ifTypeSummary(ifs map[string]*ifInfo) string {
	cnt := map[int64]int{}
	for _, e := range ifs {
		cnt[e.Type]++
	}
	keys := make([]int64, 0, len(cnt))
	for k := range cnt {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d×%d", k, cnt[k]))
	}
	return strings.Join(parts, ", ")
}

// trendCap 按保留时长和巡检间隔算出要留多少个采样点
func (c *Config) trendCap() int {
	n := c.TrendRetentionHours * 3600 / c.CheckIntervalSeconds
	if n < 30 {
		n = 30
	}
	if n > 20000 {
		n = 20000
	}
	if c.TrendPoints > n {
		return c.TrendPoints
	}
	return n
}

type heavyCache struct {
	fdb   fdbResult
	arp   []arpEntry
	round int64
}

var lastHeavy = make(map[string]heavyCache)

// naturalLess 让 Gi0/2 排在 Gi0/10 前面
func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ac, bc := a[ai], b[bi]
		if isDigit(ac) && isDigit(bc) {
			as, bs := ai, bi
			for ai < len(a) && isDigit(a[ai]) {
				ai++
			}
			for bi < len(b) && isDigit(b[bi]) {
				bi++
			}
			an, _ := strconv.Atoi(a[as:ai])
			bn, _ := strconv.Atoi(b[bs:bi])
			if an != bn {
				return an < bn
			}
			continue
		}
		if ac != bc {
			return ac < bc
		}
		ai++
		bi++
	}
	return len(a)-ai < len(b)-bi
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
