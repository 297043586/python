package main

import (
	"testing"
)

func testIfs() map[string]*ifInfo {
	return map[string]*ifInfo{
		"1":   {Index: "1", Descr: "GigabitEthernet 0/1", Physical: true},
		"2":   {Index: "2", Descr: "GigabitEthernet 0/2", Physical: true},
		"12":  {Index: "12", Descr: "GigabitEthernet 0/12", Physical: true},
		"49":  {Index: "49", Descr: "TenGigabitEthernet 0/49", Physical: true},
		"50":  {Index: "50", Descr: "TenGigabitEthernet 0/50", Physical: true},
		"100": {Index: "100", Descr: "GigabitEthernet 0/100", Physical: true},
		"200": {Index: "200", Descr: "AggregatePort 1", Type: ifTypeLag},
		"300": {Index: "300", Descr: "Vlan10"},
		"301": {Index: "301", Descr: "Vlan20"},
	}
}

// Q-BRIDGE 已有带 VLAN 的行时, 传统 BRIDGE 表里同一个 MAC 的行(往往只反映默认 VLAN)不能并进来造歧义;
// 端口 MAC 计数是每个口学到的不同 MAC 数, 不能只记在 ifIndex 字符串排序的第一个口上。
func TestFDBBuilderFinish(t *testing.T) {
	b := newFDBBuilder()
	b.add("AA:AA:AA:AA:AA:01", "10", "12")
	b.add("AA:AA:AA:AA:AA:01", "", "49") // dot1d 旧值, 应丢弃
	b.add("AA:AA:AA:AA:AA:02", "10", "100")
	b.add("AA:AA:AA:AA:AA:02", "20", "12") // 不同 VLAN 不同口: 两个口各算一次
	b.add("AA:AA:AA:AA:AA:03", "", "2")    // 只有 dot1d 的 MAC 保留, VLAN 未知
	b.nQ, b.nD = 3, 2
	f := b.finish()

	if got := f.macLocations["AA:AA:AA:AA:AA:01"]; len(got) != 1 || got[0] != "12" {
		t.Errorf("dot1d 行不应并进 Q-BRIDGE 已有的 MAC, 实际 %v", got)
	}
	if got := f.macLocations["AA:AA:AA:AA:AA:02"]; len(got) != 2 || got[0] != "12" || got[1] != "100" {
		t.Errorf("位置要按 ifIndex 自然序, 实际 %v", got)
	}
	if f.portMACs["12"] != 2 || f.portMACs["100"] != 1 || f.portMACs["49"] != 0 {
		t.Errorf("端口 MAC 计数不对: %v", f.portMACs)
	}
	if f.macVlan["AA:AA:AA:AA:AA:01"] != "10" || f.macVlan["AA:AA:AA:AA:AA:02"] != "" {
		t.Errorf("macVlan 只在 MAC 只有一个 VLAN 时给出: %v", f.macVlan)
	}
	if len(f.fdbRows) != 4 {
		t.Fatalf("转发表行应为 4 条((VLAN,MAC) 粒度), 实际 %d: %+v", len(f.fdbRows), f.fdbRows)
	}
	if f.fdbRows[0].VlanID != "-" || f.fdbRows[0].MAC != "AA:AA:AA:AA:AA:03" {
		t.Errorf("没有 VLAN 的行记成 \"-\": %+v", f.fdbRows[0])
	}
	if f.source != "dot1qTpFdb(3 条) + dot1dTpFdb(2 条)" {
		t.Errorf("来源说明: %q", f.source)
	}
}

func TestFdbVlanMapper(t *testing.T) {
	m := fdbVlanMapper(map[string]int64{
		"0.10": 5, // VLAN 10 用 FdbId 5
		"0.20": 6,
		"0.30": 7, "0.31": 7, // 共享学习: 分不出 VLAN
	})
	cases := map[string]string{"5": "10", "6": "20", "7": "", "99": "99", "5000": ""}
	for in, want := range cases {
		if got := m(in); got != want {
			t.Errorf("FdbId %s -> %q, 期望 %q", in, got, want)
		}
	}
	// 设备没有映射表: FdbId 就是 VLAN
	if got := fdbVlanMapper(nil)("10"); got != "10" {
		t.Errorf("无映射表时应按 FdbId=VLAN, 实际 %q", got)
	}
}

func TestFdbStatusUsable(t *testing.T) {
	for st, want := range map[int64]bool{1: true, 2: false, 3: true, 4: false, 5: true} {
		if got := fdbStatusUsable(st, true); got != want {
			t.Errorf("status %d -> %v", st, got)
		}
	}
	if !fdbStatusUsable(0, false) {
		t.Error("没读到状态列的行应保留")
	}
}

func TestARPAccept(t *testing.T) {
	cases := []struct {
		ip, mac string
		want    bool
	}{
		{"10.0.1.255", "00:11:22:33:44:55", true}, // /23 里的正常主机
		{"10.0.1.5", "FF:FF:FF:FF:FF:FF", false},
		{"10.0.1.5", "00:00:00:00:00:00", false},
		{"224.0.0.5", "01:00:5E:00:00:05", false},
		{"127.0.0.1", "00:11:22:33:44:55", false},
		{"0.0.0.0", "00:11:22:33:44:55", false},
		{"bad", "00:11:22:33:44:55", false},
	}
	for _, c := range cases {
		if got := arpAccept(c.ip, c.mac); got != c.want {
			t.Errorf("arpAccept(%s, %s) = %v", c.ip, c.mac, got)
		}
	}
	for idx, want := range map[string]string{"12.1.4.10.0.0.1": "10.0.0.1", "12.1.10.0.0.2": "10.0.0.2", "12.2.16.1.2.3.4": ""} {
		_, ip, _ := arpIndexIPv4(idx)
		if ip != want {
			t.Errorf("arpIndexIPv4(%s) = %q, 期望 %q", idx, ip, want)
		}
	}
}

// 聚合口在桥里学 MAC, LLDP 在成员口上报: 聚合口也要算上联口。
func TestUplinkPortSetIncludesAggregate(t *testing.T) {
	set := uplinkPortSet(testIfs(), map[string]lldpNeighbor{"49": {Dev: "CORE"}}, map[string]string{"49": "AggregatePort 1"})
	if !set["49"] || !set["200"] || set["50"] {
		t.Errorf("上联口集合不对: %v", set)
	}
}

func TestBuildHostTables(t *testing.T) {
	ifs := testIfs()
	b := newFDBBuilder()
	// 路由器 MAC: VLAN 10 走接入口 1, VLAN 20 走上联口 49
	b.add("00:00:00:00:00:0A", "10", "1")
	b.add("00:00:00:00:00:0A", "20", "49")
	// 只在 VLAN 30 学到、ARP 在 VLAN 20: 本 VLAN 已老化, 不能借 VLAN 30 的口
	b.add("00:00:00:00:00:0B", "30", "2")
	b.portVlan["2"] = "30"
	f := b.finish()
	nbr := map[string]bool{"49": true}
	f.macToIf, _ = resolveFDB(f, nbr, 8)
	arp := []arpEntry{
		{IP: "192.168.10.1", MAC: "00:00:00:00:00:0A", IfIndex: "300"},
		{IP: "192.168.20.1", MAC: "00:00:00:00:00:0A", IfIndex: "301"},
		{IP: "192.168.20.9", MAC: "00:00:00:00:00:0B", IfIndex: "301"},
		{IP: "10.255.0.2", MAC: "00:00:00:00:00:0C", IfIndex: "50"}, // 路由口
	}
	ht := buildHostTables(f, nil, arp, ifs, nbr, 8)

	byIP := map[string]HostMappingEntry{}
	for _, h := range ht.hosts {
		byIP[h.IP] = h
	}
	if h := byIP["192.168.10.1"]; h.RealPort != "GigabitEthernet 0/1" || h.VlanID != "10" || !h.VlanExact || h.PortKind != "接入口" {
		t.Errorf("VLAN 10 的 ARP 应定位到口 1: %+v", h)
	}
	if h := byIP["192.168.20.1"]; h.RealPort != "TenGigabitEthernet 0/49" || h.VlanID != "20" || h.PortKind != "上联/级联口" {
		t.Errorf("VLAN 20 的 ARP 应按 VLAN 20 的转发表行定位到上联口 49: %+v", h)
	}
	if h := byIP["192.168.20.9"]; h.PortKind != "未定位" || h.VlanID != "20" {
		t.Errorf("本 VLAN 没有转发表行时不应借别的 VLAN 的口: %+v", h)
	}
	if h := byIP["10.255.0.2"]; h.RealPort != "TenGigabitEthernet 0/50" || h.PortKind != "接入口" {
		t.Errorf("路由口上的 ARP 应定位到该口: %+v", h)
	}

	for _, m := range ht.macTable {
		if m.MAC == "00:00:00:00:00:0A" && (m.Port != "GigabitEthernet 0/1" || m.VlanID != "10") {
			t.Errorf("MAC 表应选接入口, VLAN 取该口上的行(不是 PVID): %+v", m)
		}
	}
	if len(ht.fdbTable) != 3 {
		t.Errorf("转发表应有 3 行, 实际 %d", len(ht.fdbTable))
	}
}

// ARP 总表的接入位置要用全网 MAC 归属(接入交换机的接入口), 不是核心上通往接入交换机的下联口;
// 同一 (IP, MAC) 的跨设备关联行要让位给本机 ARP 解析到的那条。
func TestGlobalARPLocation(t *testing.T) {
	core := DeviceReport{IP: "10.0.0.1", SysName: "CORE", IsAlive: true,
		Ports: []PortDetail{{Name: "TenGigabitEthernet 1/1", LLDPRemoteDev: "ACC-1"}},
		MACTable: []MACEntry{
			{MAC: "00:11:22:33:44:55", Port: "TenGigabitEthernet 1/1", PortKind: "上联/级联口", IsUplink: true},
		},
		HostMappings: []HostMappingEntry{
			{IP: "192.168.1.20", MAC: "00:11:22:33:44:55", VlanID: "10", VlanExact: true, RealPort: "TenGigabitEthernet 1/1", PortKind: "上联/级联口"},
		}}
	acc := DeviceReport{IP: "10.0.0.2", SysName: "ACC-1", IsAlive: true,
		Ports: []PortDetail{{Name: "TenGigabitEthernet 0/49", LLDPRemoteDev: "CORE"}},
		MACTable: []MACEntry{
			{MAC: "00:11:22:33:44:55", Port: "GigabitEthernet 0/5", PortKind: "接入口"},
		}}
	reports := []DeviceReport{acc, core}
	enrichCrossDeviceHosts(reports)
	if len(reports[0].HostMappings) != 1 || reports[0].HostMappings[0].IPSource != "CORE" {
		t.Fatalf("接入交换机应借到一条关联记录: %+v", reports[0].HostMappings)
	}

	got := globalARPEntries(reports)
	if len(got) != 1 {
		t.Fatalf("同一 (IP, MAC) 只留一条, 实际 %d: %+v", len(got), got)
	}
	e := got[0]
	if e.IPSource != "" || e.DeviceName != "CORE" {
		t.Errorf("应保留核心本机解析的那条, 实际 %+v", e)
	}
	if e.LocDeviceName != "ACC-1" || e.LocPort != "GigabitEthernet 0/5" || e.LocUplink {
		t.Errorf("接入位置应是 ACC-1 Gi0/5, 实际 %+v", e)
	}
	c := compactARPTable(got)
	row := c["rows"].([][]interface{})[0]
	devs := c["devices"].([][2]string)
	if li := row[8].(int); li < 0 || devs[li][1] != "ACC-1" || row[9] != "GigabitEthernet 0/5" {
		t.Errorf("紧凑格式的接入位置列不对: %v / %v", row, devs)
	}
}

func TestMACIPIndexDeterministic(t *testing.T) {
	a := DeviceReport{IP: "1", HostMappings: []HostMappingEntry{
		{IP: "10.0.0.9", MAC: "00:00:00:00:00:01"},
		{IP: "10.0.0.1", MAC: "00:00:00:00:00:02", IPSource: "X"},
	}}
	b := DeviceReport{IP: "2", HostMappings: []HostMappingEntry{
		{IP: "10.0.0.3", MAC: "00:00:00:00:00:01"},
		{IP: "10.0.0.7", MAC: "00:00:00:00:00:02"},
	}}
	for _, order := range [][]DeviceReport{{a, b}, {b, a}} {
		m := macIPIndex(order)
		if m["00:00:00:00:00:01"] != "10.0.0.3" || m["00:00:00:00:00:02"] != "10.0.0.7" {
			t.Errorf("MAC->IP 应与顺序无关且优先本机解析的: %v", m)
		}
	}
}
