package main

import (
	"strings"
	"testing"
)

// 核心 AggregatePort 6 下面是 ZBC-L4(LLDP 只在成员口上报, 下面终端不多, 聚合口没被认成上联口),
// 终端真正接在 ZBC-L4 Gi2/0/18: 总表要算在 ZBC-L4, 核心那条算路过, 不能报 MAC 漂移。
func TestMACAttributionPrefersLowerLayer(t *testing.T) {
	core := DeviceReport{IP: "10.0.0.1", SysName: "DC-L4-S18000", IsAlive: true,
		Ports: []PortDetail{
			{Name: "TenGigabitEthernet 1/1/1", Status: "UP", AggGroup: "AggregatePort 6", LLDPRemoteDev: "ZBC-L4", LLDPRemotePort: "TenGigabitEthernet 1/0/49"},
			{Name: "AggregatePort 6", Status: "UP"},
		},
		AggGroups: []AggGroupDetail{{Name: "AggregatePort 6", Members: []string{"TenGigabitEthernet 1/1/1"}}},
		MACTable: []MACEntry{
			{MAC: "00:0E:A9:01:76:34", Port: "AggregatePort 6"},
			{MAC: "00:0E:A9:42:DF:81", Port: "AggregatePort 6"},
			{MAC: "00:0E:A9:99:99:99", Port: "AggregatePort 6", IsUplink: true}, // 只在上联口见过
		}}
	zbc := DeviceReport{IP: "10.0.0.2", SysName: "ZBC-L4", IsAlive: true,
		Ports: []PortDetail{{Name: "TenGigabitEthernet 1/0/49", Status: "UP", LLDPRemoteDev: "DC-L4-S18000"}},
		MACTable: []MACEntry{
			{MAC: "00:0E:A9:01:76:34", Port: "GigabitEthernet 2/0/18"},
			{MAC: "00:0E:A9:42:DF:81", Port: "GigabitEthernet 2/0/17"},
			{MAC: "00:0E:A9:99:99:99", Port: "TenGigabitEthernet 1/0/49", IsUplink: true},
		}}
	got := globalMACEntries([]DeviceReport{core, zbc})
	byMAC := map[string][]globalMACEntry{}
	for _, e := range got {
		byMAC[e.MAC] = append(byMAC[e.MAC], e)
	}
	for _, mac := range []string{"00:0E:A9:01:76:34", "00:0E:A9:42:DF:81"} {
		l := byMAC[mac]
		if len(l) != 1 || l[0].DeviceName != "ZBC-L4" || l[0].CrossDeviceFlap {
			t.Fatalf("%s 应该只算在 ZBC-L4 且不是漂移, 实际 %+v", mac, l)
		}
		if l[0].TransitCount != 2 || l[0].TransitVia[0].DeviceName != "DC-L4-S18000" {
			t.Errorf("%s 核心那条要记成路过: %+v", mac, l[0])
		}
	}
	// 两边都只在上联口见过: 挑层级更深的 ZBC-L4, 不挑 IP 小的核心
	if l := byMAC["00:0E:A9:99:99:99"]; len(l) != 1 || l[0].DeviceName != "ZBC-L4" {
		t.Errorf("全是上联口时要挑下层设备, 实际 %+v", l)
	}

	cfg := &Config{}
	cfg.applyDefaults()
	for _, f := range l2HealthFindings([]DeviceReport{core, zbc}, cfg) {
		if f.Kind == "loop" {
			t.Errorf("聚合口朝向下层交换机, 不该报 MAC 漂移: %+v", f)
		}
	}
}

// 三层交换机一堆 VLANIF 用同一个 MAC: 不算"一 MAC 多 IP"; 路由器跨网段多个接口也不算; 同网段多个才算。
func TestARPMultiIPClassification(t *testing.T) {
	l3 := DeviceReport{IP: "172.31.200.10", SysName: "LZC-L4", IsAlive: true,
		OwnIPs: []string{"172.16.136.1", "172.16.192.1", "172.31.200.10", "172.31.255.38"},
		OwnAddrs: []OwnIfAddr{{IP: "172.16.136.1", Prefix: 23, If: "VLAN 136"}, {IP: "172.16.192.1", Prefix: 23, If: "VLAN 192"},
			{IP: "172.31.200.10", Prefix: 32, If: "Loopback 200"}, {IP: "172.31.255.38", Prefix: 30, If: "VLAN 3010"}},
		OwnMACs: []string{"58:69:6C:00:00:01"},
		// 锐捷的 ARP 表里带着自己接口的条目
		HostMappings: []HostMappingEntry{
			{IP: "172.16.136.1", MAC: "58:69:6C:00:00:01"}, {IP: "172.16.192.1", MAC: "58:69:6C:00:00:01"},
			{IP: "172.31.255.38", MAC: "58:69:6C:00:00:01"},
			{IP: "172.31.255.37", MAC: "AA:00:00:00:00:01"}, {IP: "172.16.137.254", MAC: "AA:00:00:00:00:01"}, // 防火墙两个网段
			{IP: "172.16.192.50", MAC: "BB:00:00:00:00:01"}, {IP: "172.16.192.51", MAC: "BB:00:00:00:00:01"}, // 同网段两个
		}}
	var dev, router, multi int
	for _, e := range globalARPEntries([]DeviceReport{l3}) {
		switch {
		case e.MACMultiIP:
			multi++
			if !strings.HasPrefix(e.MAC, "BB") {
				t.Errorf("只有同网段的才算可疑: %+v", e)
			}
		case e.RouterMAC:
			router++
		case e.DeviceMAC:
			dev++
		}
		if e.IP == "172.31.255.38" && !strings.Contains(e.Note, "互联地址 /30") {
			t.Errorf("互联地址要标出来: %q", e.Note)
		}
		if e.IP == "172.16.136.1" && !strings.Contains(e.Note, "网关 /23") {
			t.Errorf("网关地址要标出来: %q", e.Note)
		}
	}
	if dev != 3 || router != 2 || multi != 2 {
		t.Errorf("分类不对: 设备自身 %d(应 3) 路由器 %d(应 2) 可疑 %d(应 2)", dev, router, multi)
	}
}

// 互联地址对端 ARP 里是个陌生 MAC: 报"互联地址", 级别低于网关被冒充。
func TestARPSpoofDistinguishesInterconnect(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	a := DeviceReport{IP: "10.0.0.1", SysName: "A", IsAlive: true, OwnIPs: []string{"10.11.127.38", "172.16.192.1"},
		OwnAddrs: []OwnIfAddr{{IP: "10.11.127.38", Prefix: 30}, {IP: "172.16.192.1", Prefix: 23}}, OwnMACs: []string{"11:11:11:11:11:11"}}
	b := DeviceReport{IP: "10.0.0.2", SysName: "B", IsAlive: true,
		HostMappings: []HostMappingEntry{{IP: "10.11.127.38", MAC: "22:22:22:22:22:22"}, {IP: "172.16.192.1", MAC: "33:33:33:33:33:33"}, {IP: "172.16.192.1", MAC: "11:11:11:11:11:11"}}}
	var inter, gw *L2Finding
	for _, f := range l2HealthFindings([]DeviceReport{a, b}, cfg) {
		f := f
		if f.Kind != "arp_spoof" {
			continue
		}
		if strings.HasPrefix(f.Title, "互联地址") {
			inter = &f
		}
		if strings.HasPrefix(f.Title, "网关") {
			gw = &f
		}
	}
	if inter == nil || inter.Level != "warning" {
		t.Errorf("互联地址要单独报成 warning: %+v", inter)
	}
	if gw == nil || gw.Level != "critical" || len(gw.Steps) == 0 {
		t.Errorf("网关地址多 MAC 要报 critical 且带排查步骤: %+v", gw)
	}
}
