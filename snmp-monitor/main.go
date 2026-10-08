package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sort"
	"sync"
	"syscall"
	"time"
)

func loadConfig(path string) (*Config, error) {
	cfg := &Config{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("ℹ️  没有找到 %s, 使用默认配置启动 (设备可以登录后在页面上添加)\n", path)
			cfg.applyDefaults()
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("%s 格式有误: %w", path, err)
	}
	migrateLegacyConfig(raw, cfg)
	cfg.applyDefaults()
	return cfg, nil
}

// ----------------- 调度 -----------------

// crashLog 把意料之外的 panic 连堆栈一起记到磁盘。
// 单台设备的采集异常早就用 per-goroutine 的 recover 兜住了(runRound 里那层);
// 这里防的是"计划外的那一种" —— 持久化、模板渲染、告警合并这些跑在主
// goroutine 上的代码要是 panic, 没有任何东西能接住, 整个进程直接退出。
// 以前版本连着跑几天自动停掉, 找不到原因, 大概率就是这类没被记录下来的
// panic —— 加上这层之后至少能在 data/crash.log 里看到是哪里炸的, 而且
// 调度循环会继续跑, 不会整个程序陪葬。
func crashLog(dataDir string, where string, r interface{}) {
	msg := fmt.Sprintf("[%s] panic in %s: %v\n%s\n\n",
		time.Now().Format("2006-01-02 15:04:05"), where, r, debug.Stack())
	fmt.Printf("💥 %s\n", msg)
	f, err := os.OpenFile(dataDir+"/crash.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(msg)
}

func (s *server) runRound(round int64) {
	devices := s.devices.Active()
	started := time.Now()
	fmt.Printf("\n[%s] >>> 第 %d 轮巡检 (目标 %d 台) <<<\n",
		started.Format("2006-01-02 15:04:05"), round+1, len(devices))

	if len(devices) == 0 {
		s.hub.set([]DeviceReport{}, started.Format("2006-01-02 15:04:05"))
		fmt.Println("  (设备列表为空, 请登录页面后在「设备管理」里添加节点)")
		return
	}

	// 本轮统一用同一份配置快照; 页面上改了设置, 下一轮开始生效
	cfg := s.conf()
	var wg sync.WaitGroup
	results := make([]DeviceReport, len(devices))
	sem := make(chan struct{}, cfg.MaxConcurrency)

	for i, dev := range devices {
		wg.Add(1)
		go func(slot int, d DeviceTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					rep := DeviceReport{
						IP: d.IP, Brand: d.Brand, SysName: d.IP, stateKey: d.stateKey(),
						Timestamp: time.Now().Format("2006-01-02 15:04:05"),
						Criticals: []Alert{}, Warnings: []Alert{},
						Diagnostics: []string{}, Logs: []string{},
						Ports: []PortDetail{}, AggGroups: []AggGroupDetail{}, HostMappings: []HostMappingEntry{}, MACTable: []MACEntry{},
						TrendData: []TrendPoint{}, Phases: []PhaseCost{},
					}
					rep.alert("critical", "internal", "", fmt.Sprintf("采集线程异常: %v", r))
					results[slot] = rep
				}
			}()
			results[slot] = inspectDevice(d, cfg, round)
		}(i, dev)
	}
	wg.Wait()

	reports := make([]DeviceReport, 0, len(results))
	alive := make(map[string]bool, len(results))
	for _, r := range results {
		if r.IP == "" {
			continue
		}
		s.alerts.Sync(&r)
		alive[r.stateKey] = true
		reports = append(reports, r)
	}
	sort.Slice(reports, func(i, j int) bool { return ipLess(reports[i].IP, reports[j].IP) })
	enrichCrossDeviceHosts(reports)
	enrichAggPeers(reports)
	fillLLDPRemoteIPs(reports)
	detectMACFlapping(reports)

	for _, r := range reports {
		state := "在线"
		if !r.IsAlive {
			state = "离线"
		}
		// 每台设备各自的最慢阶段, 不能沿用上一台的值
		slowest := "-"
		if len(r.Phases) > 0 {
			slowest = fmt.Sprintf("%s %dms", r.Phases[0].Name, r.Phases[0].Ms)
		}
		fmt.Printf("  %-15s %-22s %s | 物理口 %-14s | 终端定位 %3d 条 | 未处理告警 %d | %dms (最慢: %s)\n",
			r.IP, truncate(r.SysName, 22), state, r.PortUsageRatio,
			len(r.HostMappings), r.OpenAlerts, r.CostMs, slowest)
	}

	s.hub.set(reports, time.Now().Format("2006-01-02 15:04:05"))
	pushWebhook(cfg, reports)

	// 持久化: 告警确认状态 + 趋势 + 速率基线
	s.alerts.save()
	pruneHistory(alive)
	pruneLive()
	saveHistory(cfg.DataDir, cfg.trendCap())

	fmt.Printf("✅ 本轮耗时 %.1fs\n", time.Since(started).Seconds())
}

// refreshSingleDevice 立即对一台设备重新采集, 不用等下一轮全网巡检——
// 现场排查的时候(比如刚在设备上改完配置), 等全网轮询周期(通常几分钟)
// 才能看到最新数据体验很差。跑完之后原地替换 hub 里这一条, 重新过一遍
// 跨设备关联(否则这台设备的 IP-MAC-端口 / 环路提示会短暂跟别的设备脱节),
// 但不重新采集其他设备, 也不影响下一次全网巡检的调度节奏。
func (s *server) refreshSingleDevice(ip string) (DeviceReport, error) {
	var target DeviceTarget
	found := false
	for _, d := range s.devices.Active() {
		if d.IP == ip {
			target, found = d, true
			break
		}
	}
	if !found {
		return DeviceReport{}, fmt.Errorf("设备不存在或已被禁用")
	}

	cfg := s.conf()
	var rep DeviceReport
	func() {
		defer func() {
			if r := recover(); r != nil {
				rep = DeviceReport{IP: target.IP, SysName: target.IP, stateKey: target.stateKey(), Timestamp: time.Now().Format("2006-01-02 15:04:05")}
				rep.alert("critical", "internal", "", fmt.Sprintf("采集线程异常: %v", r))
			}
		}()
		rep = inspectDevice(target, cfg, 0)
	}()
	s.alerts.Sync(&rep)

	s.hub.mu.RLock()
	reports := append([]DeviceReport(nil), s.hub.reports...)
	s.hub.mu.RUnlock()
	replaced := false
	for i := range reports {
		if reports[i].IP == ip {
			reports[i] = rep
			replaced = true
			break
		}
	}
	if !replaced {
		reports = append(reports, rep)
		sort.Slice(reports, func(i, j int) bool { return ipLess(reports[i].IP, reports[j].IP) })
	}
	enrichCrossDeviceHosts(reports)
	enrichAggPeers(reports)
	fillLLDPRemoteIPs(reports)
	detectMACFlapping(reports)
	s.hub.set(reports, time.Now().Format("2006-01-02 15:04:05"))

	s.alerts.save()
	saveHistory(cfg.DataDir, cfg.trendCap())
	return rep, nil
}

// hasBaseline 判断内存里是否已有速率基线(来自落盘数据)
func hasBaseline(devices []DeviceTarget) bool {
	stateLock.RLock()
	defer stateLock.RUnlock()
	if len(portHistory) == 0 {
		return false
	}
	fresh := time.Now().Unix() - 3600
	for _, snap := range portHistory {
		if snap.LastSeen > fresh {
			return true
		}
	}
	return false
}

// warmUp 只取三张计数器表建立基线, 远比跑一轮完整巡检快。
func (s *server) warmUp() {
	devices := s.devices.Active()
	if len(devices) == 0 || hasBaseline(devices) {
		return
	}
	fmt.Println("⚡ 正在建立速率基线 (只取计数器表, 通常几秒)...")
	t0 := time.Now()
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.cfg.MaxConcurrency)
	for _, dev := range devices {
		wg.Add(1)
		go func(d DeviceTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() { _ = recover() }()
			warmUpCounters(d, s.cfg)
		}(dev)
	}
	wg.Wait()
	fmt.Printf("   基线完成, 耗时 %.1fs\n", time.Since(t0).Seconds())
	time.Sleep(3 * time.Second)
}

func (s *server) scheduler() {
	s.warmUp()
	curInterval := s.conf().CheckIntervalSeconds
	ticker := time.NewTicker(time.Duration(curInterval) * time.Second)
	defer ticker.Stop()

	round := int64(0)
	for {
		// 页面上改了巡检间隔: 从这一轮开始按新间隔计时
		if iv := s.conf().CheckIntervalSeconds; iv != curInterval && iv > 0 {
			curInterval = iv
			ticker.Reset(time.Duration(iv) * time.Second)
		}
		func() {
			// 顶层安全网: 调度循环本身绝不能因为某一轮里的意外 panic 而退出。
			defer func() {
				if r := recover(); r != nil {
					crashLog(s.cfg.DataDir, "runRound", r)
				}
			}()
			s.runRound(round)
		}()
		round++
		select {
		case <-ticker.C:
		case <-s.kick:
			// 在线增删设备后立即跑一轮, 不用等下一个周期
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func main() {
	// 配置备份走系统 ssh 时, ssh 会把本程序当作 SSH_ASKPASS 拉起来要密码
	runAskpassIfRequested()

	cfg, err := loadConfig("config.json")
	if err != nil {
		fmt.Printf("❌ 读取配置失败: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fmt.Printf("❌ 无法创建数据目录 %s: %v\n", cfg.DataDir, err)
		os.Exit(1)
	}

	box, err := loadSecretBox(cfg.DataDir)
	if err != nil {
		fmt.Printf("❌ 初始化凭据加密失败: %v\n", err)
		os.Exit(1)
	}

	// 厂商规则要先于设备库载入: 设备的品牌可能是页面上新增的品牌
	vendorStore := newVendorStore(cfg.DataDir)
	if err := vendorStore.load(); err != nil {
		fmt.Printf("⚠️  读取厂商规则 vendors.json 失败, 先用内置规则: %v\n", err)
	}

	devices := newDeviceStore(cfg.DataDir, box)
	if err := devices.load(); err != nil {
		fmt.Printf("❌ 读取设备库失败: %v\n", err)
		os.Exit(1)
	}
	if n, err := devices.importFromConfig(cfg.Devices); err != nil {
		fmt.Printf("⚠️  导入配置里的设备失败: %v\n", err)
	} else if n > 0 {
		fmt.Printf("📥 已把 config.json 里的 %d 台设备导入设备库 (今后以设备库为准, 可在页面增删)\n", n)
	}

	alerts := newAlertStore(cfg.DataDir)
	if err := alerts.load(); err != nil {
		fmt.Printf("⚠️  读取告警状态失败: %v\n", err)
	}
	history := newAlertHistory(cfg.DataDir, cfg.AlertHistoryDays)
	history.SetMaxRecords(cfg.AlertHistoryMaxRecords)
	if err := history.load(); err != nil {
		fmt.Printf("⚠️  读取告警历史失败: %v\n", err)
	}
	alerts.hist = history

	auth := newAuthStore(cfg.DataDir)
	if err := auth.load(cfg.AdminUser); err != nil {
		fmt.Printf("❌ 初始化管理员账号失败: %v\n", err)
		os.Exit(1)
	}

	topology := newTopologyStore(cfg.DataDir)
	if err := topology.load(); err != nil {
		fmt.Printf("⚠️  读取拓扑布局失败: %v\n", err)
	}

	if devCount, points := loadHistory(cfg.DataDir); points > 0 {
		fmt.Printf("💾 已恢复 %d 台设备的 %d 个历史采样点\n", devCount, points)
	}

	// 前端资源(web/)启动时构建一次: 清单跟文件对不上直接报错退出, 不带着缺模块的页面跑起来
	if _, err := buildDashboardPage(); err != nil {
		fmt.Printf("❌ 前端资源有误: %v\n", err)
		os.Exit(1)
	}

	// 白名单按"当前设备库里的 IP"实时判定, 增删设备后立刻生效, 不用重启
	// 这几个 UDP 监听; syslog/NetFlow/sFlow 三个被动接收器共用同一份判定。
	allowFromDeviceList := func(ip string) bool {
		for _, d := range devices.List() {
			if d.IP == ip {
				return true
			}
		}
		return false
	}

	syslogRing := newSyslogRing()
	syslogRing.setCap(cfg.SyslogMaxLines)
	if cfg.Collectors.SyslogPort > 0 {
		startSyslogServer(cfg.Collectors.SyslogPort, syslogRing, allowFromDeviceList)
	}

	setFlowRingSize(cfg.FlowRingSize)
	l7Default := cfg.Collectors.FlowL7 == nil || *cfg.Collectors.FlowL7
	flows := newFlowStore(cfg.DataDir, l7Default)
	startFlowCollectors(cfg, flows, allowFromDeviceList)

	reports := newReportStore(cfg.DataDir, box)
	if err := reports.load(); err != nil {
		fmt.Printf("⚠️  读取巡检报告失败: %v\n", err)
	}
	backups := newBackupStore(cfg.DataDir, box)
	if err := backups.load(); err != nil {
		fmt.Printf("⚠️  读取配置备份索引失败: %v\n", err)
	}
	racks := newRackStore(cfg.DataDir)
	if err := racks.load(); err != nil {
		fmt.Printf("⚠️  读取机柜布局失败: %v\n", err)
	}
	dc := newDatacenterStore(cfg.DataDir)
	if err := dc.load(); err != nil {
		fmt.Printf("⚠️  读取 3D 机房布局失败: %v\n", err)
	}
	routeStore := newRouteStore(cfg.DataDir)
	if err := routeStore.load(); err != nil {
		fmt.Printf("⚠️  读取已保存路由表失败: %v\n", err)
	}
	setLiveBackupCommands(cfg.BackupCommands)

	srv := &server{
		cfg: cfg, devices: devices, alerts: alerts, auth: auth,
		sessions: newSessionMgr(time.Duration(cfg.SessionHours) * time.Hour),
		hub:      &dataHub{}, syslog: syslogRing, flows: flows, topology: topology,
		reports: reports, backups: backups, racks: racks, dc: dc, vendors: vendorStore, history: history, routeCache: routeStore,
		kick: make(chan struct{}, 1), cfgFile: "config.json",
	}
	go srv.reportScheduler()
	go srv.backupScheduler()
	go srv.storageJanitor()
	if err := srv.start(); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	// 退出前保存一次, 避免最后一轮的数据丢失。
	// SIGHUP 单独处理: 很多人是 `./netmon &` 直接起在 SSH 会话里, 没用
	// nohup/setsid, 一断连 shell 就把 SIGHUP 转发给所有子进程——这是长
	// 期后台运行的程序"莫名其妙跑几天就没了"最常见的原因之一, 而且不会
	// 在任何日志里留下痕迹, 因为进程是被系统杀掉而不是自己退出的。
	// 这里显式忽略 SIGHUP, 该谁负责的退出只认 Ctrl-C 或 kill/systemctl。
	signal.Ignore(syscall.SIGHUP)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		fmt.Println("\n🛑 收到退出信号, 正在保存数据...")
		alerts.save()
		saveHistory(cfg.DataDir, cfg.trendCap())
		fmt.Println("   已保存, 再见。")
		os.Exit(0)
	}()

	srv.scheduler()
}
