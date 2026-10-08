package main

import "testing"

// v13: 2D 拓扑"一条链路画成两根线"的几种成因, 每种都要只剩一条。

func topoFor(t *testing.T, reps []DeviceReport, setup func(ts *TopologyStore)) topoPayload {
	t.Helper()
	hub := &dataHub{}
	hub.set(reps, "2026-01-01 00:00:00")
	ts := newTopologyStore(t.TempDir())
	if setup != nil {
		setup(ts)
	}
	s := &server{hub: hub, topology: ts}
	return s.topologyPayload()
}

// 画出来的线(手工链路跟自动链路重复的不算)
func drawn(p topoPayload) []topoLink {
	var out []topoLink
	for _, l := range p.Links {
		if l.DupOf == "" {
			out = append(out, l)
		}
	}
	return out
}

func TestTopologyPortDescriptionDoesNotDuplicateLink(t *testing.T) {
	// CORE 看到 ACC 的端口报的是接口描述 "To-Core"(LLDP PortDesc), ACC 端口表里叫 GigabitEthernet0/0/48;
	// ACC 看到 CORE 的端口是 10GE1/0/1 —— 同一条物理链路两端各报一次, 旧版会画成两根线。
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "CORE", IsAlive: true, Ports: []PortDetail{
			{Name: "10GE1/0/1", SpeedMbps: 10000, InSpeedMbps: 5, OutSpeedMbps: 9, LLDPRemoteDev: "ACC", LLDPRemotePort: "To-Core"},
		}},
		{IP: "10.0.0.2", SysName: "ACC", IsAlive: true, Ports: []PortDetail{
			{Name: "GigabitEthernet0/0/48", Alias: "To-Core", SpeedMbps: 1000, LLDPRemoteDev: "CORE", LLDPRemotePort: "10GE1/0/1"},
		}},
	}, nil)
	links := drawn(p)
	if len(links) != 1 {
		t.Fatalf("应该只有 1 条链路, 实际 %d: %+v", len(links), links)
	}
	l := links[0]
	if l.APort != "10GE1/0/1" || l.BPort != "GigabitEthernet0/0/48" || l.OneSided {
		t.Errorf("两端端口应该都是端口表里的全称: %+v", l)
	}
}

func TestTopologyBothSidesUnresolvedPairUp(t *testing.T) {
	// 两边报的对端端口都是描述, 谁也找不到谁 —— 两台设备之间只有一条链路, 按顺序配成一对
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "A", IsAlive: true, Ports: []PortDetail{
			{Name: "Gi0/1", SpeedMbps: 1000, LLDPRemoteDev: "B", LLDPRemotePort: "uplink to A"},
		}},
		{IP: "10.0.0.2", SysName: "B", IsAlive: true, Ports: []PortDetail{
			{Name: "Gi0/24", SpeedMbps: 1000, LLDPRemoteDev: "A", LLDPRemotePort: "downlink to B"},
		}},
	}, nil)
	if links := drawn(p); len(links) != 1 || links[0].APort != "Gi0/1" || links[0].BPort != "Gi0/24" {
		t.Fatalf("应该配成 1 条 Gi0/1 ↔ Gi0/24: %+v", links)
	}
}

func TestTopologyAltPortNameResolves(t *testing.T) {
	// 对端端口主名是描述, 备用名(PortId)才是接口名
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "A", IsAlive: true, Ports: []PortDetail{
			{Name: "Gi0/1", LLDPRemoteDev: "B", LLDPRemotePort: "core-link", LLDPRemotePortAlt: "XGE1/0/49"},
			{Name: "Gi0/2", LLDPRemoteDev: "B", LLDPRemotePort: "core-link-2", LLDPRemotePortAlt: "XGE1/0/50"},
		}},
		{IP: "10.0.0.2", SysName: "B", IsAlive: true, Ports: []PortDetail{
			{Name: "Ten-GigabitEthernet1/0/49", LLDPRemoteDev: "A", LLDPRemotePort: "GigabitEthernet0/1"},
			{Name: "Ten-GigabitEthernet1/0/50", LLDPRemoteDev: "A", LLDPRemotePort: "GigabitEthernet0/2"},
		}},
	}, nil)
	links := drawn(p)
	if len(links) != 2 {
		t.Fatalf("两条独立物理链路, 应该是 2 条, 实际 %d: %+v", len(links), links)
	}
	if links[0].BPort != "Ten-GigabitEthernet1/0/49" || links[1].BPort != "Ten-GigabitEthernet1/0/50" {
		t.Errorf("端口配对错了: %+v", links)
	}
}

func TestTopologyAggKnownOnOneSideMerges(t *testing.T) {
	// 聚合组只在 A 侧识别到, B 侧 MIB 没覆盖(静态聚合); B 报的 A 端口是缩写
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "A", IsAlive: true,
			AggGroups: []AggGroupDetail{{Name: "Eth-Trunk1", Members: []string{"GigabitEthernet0/0/1", "GigabitEthernet0/0/2"}}},
			Ports: []PortDetail{
				{Name: "GigabitEthernet0/0/1", SpeedMbps: 1000, LLDPRemoteDev: "B", LLDPRemotePort: "Gi1/0/1"},
				{Name: "GigabitEthernet0/0/2", SpeedMbps: 1000, LLDPRemoteDev: "B", LLDPRemotePort: "Gi1/0/2"},
			}},
		{IP: "10.0.0.2", SysName: "B", IsAlive: true, Ports: []PortDetail{
			{Name: "GigabitEthernet1/0/1", SpeedMbps: 1000, LLDPRemoteDev: "A", LLDPRemotePort: "GE0/0/1"},
			{Name: "GigabitEthernet1/0/2", SpeedMbps: 1000, LLDPRemoteDev: "A", LLDPRemotePort: "GE0/0/2"},
		}},
	}, nil)
	links := drawn(p)
	if len(links) != 1 || !links[0].IsAgg || len(links[0].AMembers) != 2 || links[0].ASpeedMbps != 2000 {
		t.Fatalf("应该合并成 1 条聚合链路(2 成员, 2000M): %+v", links)
	}
}

func TestTopologyOneSidedLink(t *testing.T) {
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "A", IsAlive: true, Ports: []PortDetail{
			{Name: "Gi0/1", SpeedMbps: 1000, InSpeedMbps: 10, OutSpeedMbps: 20, LLDPRemoteDev: "B", LLDPRemotePort: "Gi0/9"},
		}},
		{IP: "10.0.0.2", SysName: "B", IsAlive: true, Ports: []PortDetail{{Name: "GigabitEthernet0/9", SpeedMbps: 1000}}},
	}, nil)
	links := drawn(p)
	if len(links) != 1 || !links[0].OneSided || links[0].BPort != "GigabitEthernet0/9" {
		t.Fatalf("只有一端上报的链路也要画, 对端端口用 B 的全称: %+v", links)
	}
	// B 那头没有速率时用 A 这头的(方向相反)
	if links[0].BSpeedMbps != 1000 || links[0].BInMbps != 20 || links[0].BOutMbps != 10 {
		t.Errorf("对端速率没补齐: %+v", links[0])
	}
}

func TestTopologyManualDuplicateNotDrawnTwice(t *testing.T) {
	reps := []DeviceReport{
		{IP: "10.0.0.1", SysName: "A", IsAlive: true, Ports: []PortDetail{{Name: "Gi0/1", LLDPRemoteDev: "B", LLDPRemotePort: "Gi0/2"}}},
		{IP: "10.0.0.2", SysName: "B", IsAlive: true, Ports: []PortDetail{{Name: "Gi0/2", LLDPRemoteDev: "A", LLDPRemotePort: "Gi0/1"}}},
	}
	p := topoFor(t, reps, func(ts *TopologyStore) {
		// 先手工连过线(端口写法跟 LLDP 不一样), 后来 LLDP 也发现了
		if err := ts.SetManual(nil, []ManualLink{{ID: "l1", A: "10.0.0.2", APort: "GigabitEthernet0/2", B: "10.0.0.1"}}, nil, func(string) bool { return true }); err != nil {
			t.Fatal(err)
		}
	})
	if len(p.Links) != 2 {
		t.Fatalf("手工链路要留在返回里(编辑时要带回去), 实际 %d 条", len(p.Links))
	}
	if links := drawn(p); len(links) != 1 || links[0].Source != "lldp" {
		t.Fatalf("重复的手工链路不应该再画一条: %+v", p.Links)
	}
	// 端口明确不同的手工链路是另一条线
	p = topoFor(t, reps, func(ts *TopologyStore) {
		_ = ts.SetManual(nil, []ManualLink{{ID: "l1", A: "10.0.0.1", APort: "Gi0/5", B: "10.0.0.2", BPort: "Gi0/6"}}, nil, func(string) bool { return true })
	})
	if len(drawn(p)) != 2 {
		t.Fatalf("端口不同的手工链路是另一条物理链路: %+v", p.Links)
	}
}

func TestTopologyExternalMatchingManualNodeBecomesLink(t *testing.T) {
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "CORE", IsAlive: true, Ports: []PortDetail{
			{Name: "Gi0/24", SpeedMbps: 1000, LLDPRemoteDev: "FW-Edge.example.com", LLDPRemotePort: "port1"},
			{Name: "Gi0/23", LLDPRemoteDev: "AP-01", LLDPRemotePort: "eth0"},
		}},
	}, func(ts *TopologyStore) {
		_ = ts.SetManual([]ManualNode{{ID: "fw", Name: "fw-edge", Kind: "firewall"}}, nil, nil, func(string) bool { return true })
	})
	if len(p.Externals) != 1 || p.Externals[0].Name != "AP-01" {
		t.Errorf("跟手工节点同名的 LLDP 邻居不应再画成外部小圆点: %+v", p.Externals)
	}
	links := drawn(p)
	if len(links) != 1 || links[0].B != "m:fw" || links[0].APort != "Gi0/24" || links[0].BPort != "port1" {
		t.Fatalf("应该自动接到手工节点上: %+v", links)
	}
}

func TestTopologyHiddenLegacyKeyStillHides(t *testing.T) {
	// 升级前隐藏的 key 里对端端口是接口描述, 升级后端口名变成了全称, 仍然要隐藏
	p := topoFor(t, []DeviceReport{
		{IP: "10.0.0.1", SysName: "A", IsAlive: true, Ports: []PortDetail{{Name: "Gi0/1", LLDPRemoteDev: "B", LLDPRemotePort: "to-A"}}},
		{IP: "10.0.0.2", SysName: "B", IsAlive: true, Ports: []PortDetail{{Name: "Gi0/9", Alias: "to-A", LLDPRemoteDev: "A", LLDPRemotePort: "Gi0/1"}}},
	}, func(ts *TopologyStore) {
		_ = ts.SetManual(nil, nil, []string{"lldp|10.0.0.1|Gi0/1|10.0.0.2|to-A"}, func(string) bool { return true })
	})
	if len(p.Links) != 0 || p.HiddenLinks != 1 {
		t.Fatalf("旧 key 应该仍然能隐藏这条链路: %+v", p.Links)
	}
}

func TestPickLLDPPort(t *testing.T) {
	cases := []struct{ id, desc, port, alt string }{
		{"GigabitEthernet0/0/1", "To-Core-1", "GigabitEthernet0/0/1", "To-Core-1"}, // 描述不能盖过接口名
		{"Gi0/1", "GigabitEthernet 0/1", "Gi0/1", "GigabitEthernet 0/1"},           //
		{"10GE1/0/1", "", "10GE1/0/1", ""},                                         // 华为 CE 的 10GE 前缀以数字开头
		{"AA:BB:CC:DD:EE:FF", "eth0", "eth0", "AA:BB:CC:DD:EE:FF"},                 // PortId 是 MAC
		{"5", "GigabitEthernet1/0/5 Interface", "GigabitEthernet1/0/5", "5"},       // 本地编号 + H3C 默认描述
		{"XGE1/0/49", "Ten-GigabitEthernet1/0/49 Interface", "XGE1/0/49", "Ten-GigabitEthernet1/0/49"},
		{"", "uplink", "uplink", ""},
	}
	for _, c := range cases {
		port, alt := pickLLDPPort(c.id, c.desc)
		if port != c.port || alt != c.alt {
			t.Errorf("pickLLDPPort(%q, %q) = (%q, %q), 期望 (%q, %q)", c.id, c.desc, port, alt, c.port, c.alt)
		}
	}
}
