package main

import (
	"strings"
	"testing"
)

func TestL2HealthFindings(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	core := DeviceReport{IP: "10.0.0.1", SysName: "CORE", IsAlive: true, OwnIPs: []string{"192.168.10.1"},
		// 核心 ARP 表里, 网关自己的 IP 被一个接入终端的 MAC 应答了
		HostMappings: []HostMappingEntry{
			{IP: "192.168.10.1", MAC: "aa:aa:aa:00:00:01"},
			{IP: "192.168.10.50", MAC: "bb:bb:bb:00:00:01"},
			{IP: "192.168.10.51", MAC: "bb:bb:bb:00:00:02"},
		},
		STP: &STPInfo{TopChanges: 40, Delta: 20, SinceChangeSec: 12}}
	sw1 := DeviceReport{IP: "10.0.1.1", SysName: "SW1", IsAlive: true,
		Ports: []PortDetail{
			{Name: "Gi0/1", Status: "UP", Ext: &PortExt{InBcastPps: 8000, LastChangeSec: -1}},
			{Name: "Gi0/2", Status: "UP", IsUplink: true, LLDPRemoteDev: "-", MACCount: 15, Ext: &PortExt{LastChangeSec: -1}},
			{Name: "Gi0/3", Status: "UP", SpeedMbps: 100, Duplex: "半双工", Ext: &PortExt{LastChangeSec: -1}},
		},
		MACTable: []MACEntry{
			{MAC: "aa:aa:aa:00:00:01", Port: "Gi0/5"}, // 冒充网关的终端
			{MAC: "cc:cc:cc:00:00:01", Port: "Gi0/7"}, // 同时在 SW2 接入口出现 → 漂移/环路
			{MAC: "dd:dd:dd:00:00:01", Port: "Gi0/8"}, // 一个 MAC 多个 IP
			{MAC: "ee:ee:ee:00:00:01", Port: "Te0/49", IsUplink: true},
		},
		HostMappings: []HostMappingEntry{
			{IP: "192.168.10.60", MAC: "dd:dd:dd:00:00:01"}, {IP: "192.168.10.61", MAC: "dd:dd:dd:00:00:01"},
			{IP: "192.168.10.62", MAC: "dd:dd:dd:00:00:01"}, {IP: "192.168.10.63", MAC: "dd:dd:dd:00:00:01"},
			{IP: "192.168.10.64", MAC: "dd:dd:dd:00:00:01"},
			{IP: "192.168.10.50", MAC: "bb:bb:bb:00:00:09"}, // 跟核心看到的 MAC 不同 → IP 冲突
		}}
	sw2 := DeviceReport{IP: "10.0.1.2", SysName: "SW2", IsAlive: true,
		Ports:    []PortDetail{{Name: "Gi0/9", Status: "UP", Ext: &PortExt{InBcastPps: 1500, LastChangeSec: -1}}},
		MACTable: []MACEntry{{MAC: "cc:cc:cc:00:00:01", Port: "Gi0/20"}}}

	got := l2HealthFindings([]DeviceReport{core, sw1, sw2}, cfg)
	kinds := map[string][]L2Finding{}
	for _, f := range got {
		kinds[f.Kind] = append(kinds[f.Kind], f)
	}
	has := func(kind, level, sub string) {
		t.Helper()
		for _, f := range kinds[kind] {
			if f.Level == level && strings.Contains(f.Title, sub) {
				return
			}
		}
		t.Errorf("缺少 %s/%s 包含「%s」的结论; 实际: %+v", kind, level, sub, kinds[kind])
	}
	has("storm", "critical", "SW1 Gi0/1")     // 8000 ≥ 5×1000
	has("storm", "warning", "SW2 Gi0/9")      // 1500
	has("storm", "critical", "2 台设备同时出现广播风暴") // 多台同时风暴
	has("loop", "warning", "SW1 ↔ SW2")
	has("stp", "critical", "变化了 20 次") // 20 ≥ 5×3
	has("rogue", "info", "SW1 Gi0/2")
	has("duplex", "warning", "SW1 Gi0/3")
	has("arp_spoof", "critical", "接入口 SW1 Gi0/5")
	has("arp_multi", "warning", "5 个 IP")
	has("ip_conflict", "warning", "192.168.10.50")
	if got[0].Level != "critical" || got[len(got)-1].Level == "critical" {
		t.Error("结论要按严重程度排序, 严重的在前")
	}
}

func TestL2HealthIgnoresVRRPGatewayMAC(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	r := DeviceReport{IP: "10.0.0.1", SysName: "CORE", IsAlive: true, OwnIPs: []string{"192.168.10.1"},
		HostMappings: []HostMappingEntry{{IP: "192.168.10.1", MAC: "00:00:5e:00:01:0a"}, {IP: "192.168.10.1", MAC: "70:7b:e8:00:00:01"}}}
	for _, f := range l2HealthFindings([]DeviceReport{r}, cfg) {
		if f.Kind == "arp_spoof" {
			t.Fatalf("VRRP 虚拟 MAC + 真实接口 MAC 是正常的, 不该报 ARP 欺骗: %+v", f)
		}
	}
}
