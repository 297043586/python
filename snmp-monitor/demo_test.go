package main

// 带假数据的演示服务, 只用于在浏览器里验证页面(不参与 go test 的正常运行):
//
//   NETMON_DEMO=127.0.0.1:18080 NETMON_DEMO_SECS=600 go test -run TestDemoServer -v
//
// 页面会额外加载 /demo.js: 收集 JS 错误, 并按 ?demo=xxx 自动操作页面(打开设置 / 3D 机房 /
// 大屏...), 结果 POST 到 /demo/report 打印出来, 配合无头浏览器截图使用。

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func demoPorts(prefix string, from, to int, speed int64, peerEvery int) []PortDetail {
	var out []PortDetail
	for i := from; i <= to; i++ {
		p := PortDetail{Index: fmt.Sprintf("%s%d", prefix, i), Name: fmt.Sprintf("%s%d", prefix, i), SpeedMbps: speed, Status: "DOWN", LLDPRemoteDev: "-"}
		switch {
		case i%5 == 0:
			p.Status = "ADMIN-DOWN"
		case i%3 != 0:
			p.Status = "UP"
			p.InSpeedMbps = float64(i%7) * float64(speed) / 100
			p.OutSpeedMbps = float64(i%4) * float64(speed) / 120
			p.InUsagePercent = float64(i%7) * 1.0
			p.OutUsagePercent = float64(i % 90)
		}
		if i == from+1 {
			p.DeltaErrors = 12
		}
		if speed >= 10000 {
			rx, tx := -3.5-float64(i%20), -2.1
			p.OpticalRxDbm, p.OpticalTxDbm = &rx, &tx
		}
		out = append(out, p)
	}
	return out
}

func demoReports() []DeviceReport {
	now := time.Now().Unix() / 60 * 60
	trend := func(k float64) []TrendPoint {
		var t []TrendPoint
		for i := 0; i < 60; i++ {
			t = append(t, TrendPoint{TS: now - int64(60-i)*300, InMbps: k * (100 + float64(i%13)*20), OutMbps: k * (60 + float64(i%7)*15)})
		}
		return t
	}
	link := func(ps []PortDetail, name, peer, peerPort string, in, out float64) {
		for i := range ps {
			if ps[i].Name == name {
				ps[i].Status, ps[i].LLDPRemoteDev, ps[i].LLDPRemotePort, ps[i].InSpeedMbps, ps[i].OutSpeedMbps, ps[i].IsUplink = "UP", peer, peerPort, in, out, true
				ps[i].InUsagePercent, ps[i].OutUsagePercent = in/float64(ps[i].SpeedMbps)*100, out/float64(ps[i].SpeedMbps)*100
			}
		}
	}
	core := append(append(append(demoPorts("10GE1/0/", 1, 24, 10000, 0), demoPorts("10GE2/0/", 1, 24, 10000, 0)...), demoPorts("40GE1/0/", 1, 2, 40000, 0)...),
		PortDetail{Index: "m", Name: "MEth0/0/0", Status: "UP", SpeedMbps: 1000, LLDPRemoteDev: "-"})
	link(core, "10GE1/0/1", "AGG-H3C-S7506E", "XGE4/0/1", 2300, 4100)
	link(core, "10GE2/0/1", "AGG-H3C-S7506E", "XGE4/0/2", 1800, 3700)
	link(core, "40GE1/0/1", "SPINE-CE8850", "100GE1/0/1", 9000, 7000)
	agg := append(append(demoPorts("GigabitEthernet2/0/", 1, 24, 1000, 0), demoPorts("GigabitEthernet3/0/", 1, 24, 1000, 0)...), demoPorts("Ten-GigabitEthernet4/0/", 1, 8, 10000, 0)...)
	link(agg, "Ten-GigabitEthernet4/0/1", "CORE-HW-CE6881", "10GE1/0/1", 4100, 2300)
	link(agg, "GigabitEthernet2/0/1", "ACC-RJ-01", "TenGigabitEthernet 0/49", 300, 800)
	link(agg, "GigabitEthernet3/0/1", "ACC-HW-02", "GigabitEthernet0/0/48", 650, 120)
	rj := append(demoPorts("GigabitEthernet 0/", 1, 48, 1000, 0), demoPorts("TenGigabitEthernet 0/", 49, 52, 10000, 0)...)
	link(rj, "TenGigabitEthernet 0/49", "AGG-H3C-S7506E", "GigabitEthernet2/0/1", 800, 300)
	rj[4].LLDPRemoteDev, rj[4].LLDPRemotePort, rj[4].Status = "AP-3F-01", "eth0", "UP"
	// 一堆未监控的 LLDP 邻居(AP / 话机 / 服务器), 看拓扑上的折叠效果
	for i := 6; i < 40; i++ {
		kind := []string{"AP-3F-%02d", "IPPHONE-%02d", "SRV-%02d"}[i%3]
		rj[i].LLDPRemoteDev, rj[i].LLDPRemotePort, rj[i].Status = fmt.Sprintf(kind, i), "eth0", "UP"
	}
	hw2 := append(demoPorts("GigabitEthernet0/0/", 1, 48, 1000, 0), demoPorts("GigabitEthernet1/0/", 1, 48, 1000, 0)...)
	link(hw2, "GigabitEthernet0/0/48", "AGG-H3C-S7506E", "GigabitEthernet3/0/1", 120, 650)
	for i := 10; i < 22; i++ {
		hw2[i].LLDPRemoteDev, hw2[i].LLDPRemotePort, hw2[i].Status = fmt.Sprintf("AP-2F-%02d", i), "GE0/0/0", "UP"
	}
	// CDP 发现的邻居
	hw2[30].LLDPRemoteDev, hw2[30].LLDPRemotePort, hw2[30].Status, hw2[30].NeighborSrc = "CISCO-2960", "Gi0/1", "UP", "CDP"
	spine := demoPorts("100GE1/0/", 1, 32, 100000, 0)
	link(spine, "100GE1/0/1", "CORE-HW-CE6881", "40GE1/0/1", 7000, 9000)

	hosts := []HostMappingEntry{}
	macs := []MACEntry{}
	for i := 1; i <= 40; i++ {
		port := fmt.Sprintf("GigabitEthernet 0/%d", i%12+1)
		kind := "接入口"
		if i%9 == 0 {
			kind = "上联/级联口"
		}
		if i%13 == 0 {
			kind = "未定位"
		}
		vlan := strconv.Itoa(10 + i%3*10)
		mac := fmt.Sprintf("00:1a:2b:3c:%02x:%02x", i/256, i%256)
		hosts = append(hosts, HostMappingEntry{IP: fmt.Sprintf("192.168.%s.%d", vlan, i), MAC: mac, VlanID: vlan, VlanExact: i%4 != 0, RealPort: port, PortKind: kind, VlanIf: "Vlanif" + vlan})
		macs = append(macs, MACEntry{MAC: mac, VlanID: vlan, Port: port, PortKind: kind, IsUplink: kind == "上联/级联口", Ambiguous: i%11 == 0})
	}
	al := func(level, cat, text string) Alert {
		return Alert{ID: cat + text, Level: level, Category: cat, Text: text, FirstSeen: "2026-09-24 08:00:00", LastSeen: "2026-09-24 09:" + fmt.Sprintf("%02d", len(text)%60) + ":00"}
	}
	var warnList []Alert
	for i := 0; i < 12; i++ {
		warnList = append(warnList, al("warning", "port_err", fmt.Sprintf("GigabitEthernet0/0/%d 本轮错包 +%d", i+1, 10+i)))
	}
	hw := HardwareStatus{PowerStatus: "正常 (2/2)", FanStatus: "正常 (4/4)", Temperature: "41℃"}
	return []DeviceReport{
		{IP: "10.0.0.1", SysName: "CORE-HW-CE6881", Brand: "huawei", IsAlive: true, CPUUsage: 23, MemUsage: 41, Hardware: HardwareStatus{PowerStatus: "异常 (1/2)", FanStatus: "正常", Temperature: "45℃"},
			Ports: core, TrendData: trend(3), UpTimeStr: "120 天", PortUsageRatio: "30/51",
			Inventory: &DeviceInventory{Model: "CE6881-48S6CQ", Location: "数据中心A", Chassis: []EntChassis{{Index: "1", Model: "CE6881-48S6CQ"}, {Index: "2", Model: "CE6881-48S6CQ"}}},
			Criticals: []Alert{al("critical", "psu", "电源模块 2 故障")}, OpenAlerts: 1},
		{IP: "10.0.0.2", SysName: "AGG-H3C-S7506E", Brand: "h3c", IsAlive: true, CPUUsage: 35, MemUsage: 52, Hardware: hw, Ports: agg, TrendData: trend(2), UpTimeStr: "88 天",
			Inventory: &DeviceInventory{Model: "S7506E-X", Location: "数据中心A", Chassis: []EntChassis{{Index: "1", Model: "S7506E-X"}}, EntModules: 9}},
		{IP: "10.0.0.3", SysName: "SPINE-CE8850", Brand: "huawei", IsAlive: true, CPUUsage: 12, MemUsage: 30, Hardware: hw, Ports: spine, TrendData: trend(4),
			Inventory: &DeviceInventory{Model: "CE8850-32CQ-EI", Location: "数据中心A", Chassis: []EntChassis{{Index: "1", Model: "CE8850-32CQ-EI"}}}},
		{IP: "10.0.1.1", SysName: "ACC-RJ-01", Brand: "ruijie", IsAlive: true, CPUUsage: 8, MemUsage: 33, Hardware: hw, Ports: rj, TrendData: trend(1), HostMappings: hosts, MACTable: macs,
			Inventory: &DeviceInventory{Model: "S5750-48GT4XS-H", Location: "数据中心A"}},
		{IP: "10.0.1.2", SysName: "ACC-HW-02", Brand: "huawei", IsAlive: true, CPUUsage: 92, MemUsage: 71, Hardware: hw, Ports: hw2, TrendData: trend(1),
			Warnings: append([]Alert{al("warning", "cpu", "CPU 92%")}, warnList...), OpenAlerts: 13, MACTable: macs[:10]},
		{IP: "10.0.1.3", SysName: "ACC-OFFLINE", Brand: "h3c", IsAlive: false, Criticals: []Alert{al("critical", "unreachable", "设备不可达")}, OpenAlerts: 1,
			Inventory: &DeviceInventory{Location: "1号楼弱电间"}},
	}
}

func TestDemoServer(t *testing.T) {
	addr := os.Getenv("NETMON_DEMO")
	if addr == "" {
		t.Skip("设置 NETMON_DEMO=127.0.0.1:18080 才会启动演示服务")
	}
	secs, _ := strconv.Atoi(os.Getenv("NETMON_DEMO_SECS"))
	if secs <= 0 {
		secs = 300
	}
	dir := t.TempDir()
	if d := os.Getenv("NETMON_DEMO_DIR"); d != "" {
		dir = d
		_ = os.MkdirAll(dir, 0o700)
	}
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.DataDir = dir
	cfg.Collectors.SflowPort = 6343
	cfgPath := filepath.Join(dir, "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		_ = os.WriteFile(cfgPath, []byte(`{"web_port": 18080, "devices": [{"ip": "10.9.9.9", "community": "public"}], "my_note": "手写的字段要保留"}`), 0o600)
	}
	box, _ := loadSecretBox(dir)
	vendorStore := newVendorStore(dir)
	_ = vendorStore.load()
	defer liveVendors.Store(newVendorSet(mergeVendors(nil)))
	devices := newDeviceStore(dir, box)
	_ = devices.load()
	reps := demoReports()
	for i := range reps {
		for j := range reps[i].Criticals {
			reps[i].Criticals[j].DeviceIP = reps[i].IP
		}
		for j := range reps[i].Warnings {
			reps[i].Warnings[j].DeviceIP = reps[i].IP
		}
	}
	// 演示备注: 面板上的标签贴、拓扑、大屏都会用到
	notes := map[string]string{"10.0.0.1": "核心 · 勿随意重启", "10.0.1.2": "2楼东侧 · 财务部", "10.0.0.2": "汇聚 · 办公区"}
	for i := range reps {
		reps[i].Note = notes[reps[i].IP]
		reps[i].Group = demoGroups[reps[i].IP]
		// 端口计数跟真实巡检(inspect.go)一样从端口表数出来, 大屏 / 首页的「物理口使用」才不是 0/0
		if reps[i].TotalPorts == 0 {
			for _, p := range reps[i].Ports {
				reps[i].TotalPorts++
				if p.Status == "UP" {
					reps[i].UpPorts++
				} else {
					reps[i].DownPorts++
				}
			}
			if reps[i].TotalPorts > 0 {
				reps[i].PortUsageRatio = fmt.Sprintf("%.1f%% (%d/%d口)", float64(reps[i].UpPorts)/float64(reps[i].TotalPorts)*100, reps[i].UpPorts, reps[i].TotalPorts)
			}
		}
	}
	for _, r := range reps {
		_, _ = devices.Add(DeviceTarget{IP: r.IP, Community: "public", Brand: r.Brand, Note: r.Note, Group: r.Group})
	}
	auth := newAuthStore(dir)
	_ = auth.load("admin")
	_ = auth.setPassword("admin", "demo-pass-123")
	demoBigMACTable(reps)
	hub := &dataHub{}
	hub.set(reps, time.Now().Format("2006-01-02 15:04:05"))
	racks := newRackStore(dir)
	_ = racks.load()
	_ = racks.Set("10.0.1.2", RackSlot{Rack: "A02", U: 30})
	dc := newDatacenterStore(dir)
	_ = dc.load()
	// NETMON_DEMO_EMPTY_DC=1: 不预置机柜, 模拟新装时的空机房
	if len(dc.Snapshot().Racks) == 0 && os.Getenv("NETMON_DEMO_EMPTY_DC") == "" {
		lay := dc.Snapshot()
		lay.Racks = []DCRack{
			{ID: "r1", Name: "A01", Room: "room1", X: 3, Z: 3, Height: 42},
			{ID: "r2", Name: "A02", Room: "room1", X: 4, Z: 3, Height: 42},
			{ID: "r3", Name: "A03", Room: "room1", X: 5, Z: 3, Height: 42, Color: "#1e3a5f"},
			{ID: "ac1", Name: "空调-1", Room: "room1", X: 8, Z: 3, Type: "ac"},
			{ID: "u1", Name: "UPS-1", Room: "room1", X: 10, Z: 8, Type: "ups"},
			{ID: "u2", Name: "配电柜-1", Room: "room1", X: 11, Z: 8, Type: "pdc"},
			{ID: "u3", Name: "电池柜-1", Room: "room1", X: 12, Z: 8, Type: "battery"},
			// 对面一排, 正面朝北, 跟 A 排之间是冷通道
			{ID: "r4", Name: "B01", Room: "room1", X: 3, Z: 7, Rot: 180, Height: 42},
			{ID: "r5", Name: "B02", Room: "room1", X: 4, Z: 7, Rot: 180, Height: 42},
			{ID: "r6", Name: "B03", Room: "room1", X: 5, Z: 7, Rot: 180, Height: 47},
			{ID: "r7", Name: "DR-01", Room: "room2", X: 2, Z: 2, Height: 42},
			{ID: "r8", Name: "DR-02", Room: "room2", X: 3, Z: 2, Height: 42},
		}
		// 灾备机房: 东北角是一段弧形墙, 东南角斜切(一面斜墙)
		lay.Rooms = append(lay.Rooms, DCRoom{ID: "room2", Name: "灾备机房", W: 10, D: 8, Cuts: []DCRect{{X: 6, Z: 0, W: 4, D: 3, Type: "arc", Corner: "ne"}, {X: 7, Z: 5, W: 3, D: 3, Type: "tri", Corner: "se"}}})
		lay.Aisles = []DCAisle{{ID: "a1", Name: "冷通道 A", Room: "room1", X: 3, Z: 5, W: 3, D: 2}}
		lay.Rooms[0].H = 320
		// 场景物件: 门、墙挂大屏、摄像头、柱子、操作台、灭火器箱、气体灭火钢瓶
		lay.Objects = []DCObject{
			{ID: "o1", Kind: "door", Room: "room1", X: 9, Z: 13, Rot: 180, H: 250},
			{ID: "o2", Kind: "screen", Room: "room1", X: 8, Z: 0, Rot: 0},
			{ID: "o3", Kind: "camera", Room: "room1", X: 0, Z: 1, Rot: 90},
			{ID: "o4", Kind: "camera", Room: "room1", X: 19, Z: 12, Rot: 270},
			{ID: "o5", Kind: "pillar", Room: "room1", X: 14, Z: 6, Rot: 0},
			{ID: "o6", Kind: "desk", Name: "值班台", Room: "room1", X: 15, Z: 2, Rot: 0},
			{ID: "o7", Kind: "extinguisher", Room: "room1", X: 1, Z: 12, Rot: 0},
			{ID: "o8", Kind: "gas", Room: "room1", X: 17, Z: 11, Rot: 0},
			{ID: "o9", Kind: "door", Room: "room2", X: 0, Z: 5, Rot: 90},
			{ID: "o10", Kind: "screen", Room: "room2", X: 3, Z: 7, Rot: 180},
			// 弧墙上一扇门, 斜墙上一块大屏
			{ID: "o11", Kind: "door", Room: "room2", Wall: "ne", T: 0.5},
			{ID: "o12", Kind: "screen", Room: "room2", Wall: "se", T: 0.5},
		}
		lay.Items = []DCItem{
			{ID: "i1", Rack: "r1", U: 2, H: 2, Kind: "server", Name: "Web-01", Model: "R740", Status: "ok"},
			{ID: "i2", Rack: "r1", U: 5, H: 4, Kind: "storage", Name: "NAS-01", Status: "warn"},
			{ID: "i3", Rack: "r2", U: 40, H: 1, Kind: "patch", Name: "配线架-1"},
			{ID: "i4", Rack: "r3", U: 20, H: 1, Kind: "firewall", Name: "FW-01", Status: "bad"},
			{ID: "i5", Rack: "r3", U: 1, H: 3, Kind: "ups", Name: "机架 UPS"},
			{ID: "i6", Rack: "r4", U: 30, H: 2, Kind: "server", Name: "DB-01", Model: "R750", Status: "ok", Note: "主库"},
			{ID: "i7", Rack: "r4", U: 26, H: 2, Kind: "server", Name: "DB-02", Model: "R750", Status: "ok"},
			{ID: "i8", Rack: "r5", U: 38, H: 1, Kind: "patch", Name: "配线架-2"},
			{ID: "i9", Rack: "r5", U: 20, H: 1, Kind: "pdu", Name: "PDU-B02"},
			{ID: "i10", Rack: "r7", U: 20, H: 2, Kind: "server", Name: "备份服务器", Status: "ok"},
			{ID: "i11", Rack: "r8", U: 10, H: 4, Kind: "storage", Name: "备份存储", Status: "ok"},
		}
		if err := dc.Save(lay, "demo"); err != nil {
			t.Fatal(err)
		}
	}
	topology := newTopologyStore(dir)
	_ = topology.load()
	if n, _, _ := topology.Manual(); len(n) == 0 && os.Getenv("NETMON_DEMO_NO_MANUAL") == "" {
		_ = topology.SetManual([]ManualNode{{ID: "fw", Name: "出口防火墙", Kind: "firewall", IP: "10.0.0.254"}, {ID: "isp", Name: "电信专线 500M", Kind: "isp"}},
			[]ManualLink{{ID: "l1", A: "10.0.0.1", APort: "10GE1/0/24", B: "m:fw", BPort: "ge0/0"}, {ID: "l2", A: "m:fw", B: "m:isp", Note: "电信 500M"}}, nil,
			func(string) bool { return true })
	}
	alerts := newAlertStore(dir)
	history := newAlertHistory(dir, 180)
	_ = history.load()
	if len(history.incidents) == 0 {
		demoSeedHistory(history, reps)
	}
	alerts.hist = history
	reports := newReportStore(dir, box)
	_ = reports.load()
	backups := newBackupStore(dir, box)
	_ = backups.load()
	flows := newFlowStore(dir, true)
	if _, err := buildDashboardPage(); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: cfg, devices: devices, alerts: alerts, auth: auth, sessions: newSessionMgr(12 * time.Hour), hub: hub,
		syslog: newSyslogRing(), flows: flows, topology: topology, reports: reports, backups: backups, racks: racks, dc: dc, vendors: vendorStore, history: history,
		kick: make(chan struct{}, 1), cfgFile: cfgPath,
		pageFilter: func(page []byte) []byte {
			return bytes.Replace(page, []byte("</body>"), []byte(`<script src="/demo.js"></script></body>`), 1)
		}}
	setLiveBackupCommands(cfg.BackupCommands)
	mux := s.routes()
	demoJS, _ := os.ReadFile("demo_page_test.js")
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/demo.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			js, _ := os.ReadFile("demo_page_test.js")
			if len(js) == 0 {
				js = demoJS
			}
			_, _ = w.Write(js)
			return
		case "/demo/report":
			b, _ := io.ReadAll(r.Body)
			fmt.Printf("DEMO-REPORT %s\n", b)
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: addr, Handler: h}
	go func() { time.Sleep(time.Duration(secs) * time.Second); _ = srv.Close() }()
	fmt.Printf("演示服务: http://%s/ (%d 秒后退出), 数据目录 %s\n", addr, secs, dir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
