package main

import (
	"strings"
	"testing"
)

func loopTestCfg() *Config {
	cfg := &Config{}
	cfg.applyDefaults()
	return cfg
}

func loopFindingsOf(reports []DeviceReport) []L2Finding {
	var out []L2Finding
	for _, f := range l2HealthFindings(reports, loopTestCfg()) {
		if f.Kind == "loop" {
			out = append(out, f)
		}
	}
	return out
}

func idlePort(name string) PortDetail {
	return PortDetail{Name: name, Status: "UP", Ext: &PortExt{LastChangeSec: -1}}
}

// 核心 + 接入交换机 SW1, SW1 Gi0/48 通过 LLDP 上联核心。
func loopTopology() (DeviceReport, DeviceReport) {
	core := DeviceReport{IP: "10.0.0.1", SysName: "CORE", IsAlive: true, OwnMACs: []string{"70:00:00:00:00:01"},
		Ports: []PortDetail{idlePort("Te0/1")}}
	up := idlePort("Gi0/48")
	up.LLDPRemoteDev, up.IsUplink = "CORE", true
	sw1 := DeviceReport{IP: "10.0.1.1", SysName: "SW1", IsAlive: true, OwnMACs: []string{"70:00:00:00:01:01"},
		Ports: []PortDetail{up, idlePort("Gi0/5"), idlePort("Gi0/7")}}
	return core, sw1
}

func TestLoopSameDeviceAccessPorts(t *testing.T) {
	core, sw1 := loopTopology()
	for i := 0; i < 12; i++ {
		mac := "cc:cc:cc:00:00:" + string(rune('a'+i/10)) + string(rune('0'+i%10))
		sw1.FDBTable = append(sw1.FDBTable, MACEntry{MAC: mac, VlanID: "10", Port: "Gi0/5", Locations: []string{"Gi0/5", "Gi0/7"}})
	}
	got := loopFindingsOf([]DeviceReport{core, sw1})
	if len(got) != 1 || got[0].Level != "critical" || !strings.Contains(got[0].Title, "Gi0/5、Gi0/7 之间有 12 个 MAC") {
		t.Fatalf("同一台设备两个接入口之间大量 MAC 漂移应报 critical: %+v", got)
	}
	if !strings.Contains(got[0].Detail, "都是接入口") {
		t.Errorf("应说明是接入口之间被连通: %s", got[0].Detail)
	}
}

func TestLoopUpstreamMACOnTwoPorts(t *testing.T) {
	core, sw1 := loopTopology()
	sw1.Ports[1].IsUplink, sw1.Ports[1].MACCount = true, 40 // 环路时接入口 MAC 数暴涨, 被本机判成上联
	sw1.FDBTable = []MACEntry{{MAC: "70:00:00:00:00:01", VlanID: "10", Port: "Gi0/48", Locations: []string{"Gi0/48", "Gi0/5"}}}
	sw1.MACTable = []MACEntry{{MAC: "70:00:00:00:00:01", VlanID: "10", Port: "Gi0/5", IsUplink: true}}
	all := l2HealthFindings([]DeviceReport{core, sw1}, loopTestCfg())
	var loop *L2Finding
	for i, f := range all {
		if f.Kind == "loop" && strings.Contains(f.Title, "二层环路") {
			loop = &all[i]
		}
		if (f.Kind == "no_lldp" || f.Kind == "rogue") && f.Port == "Gi0/5" {
			t.Errorf("环路口不应再被报成级联 / 私接: %+v", f)
		}
	}
	if loop == nil || loop.Level != "critical" || !strings.Contains(loop.Title, "CORE (VLAN 10)") || loop.Port != "Gi0/48" && loop.Port != "Gi0/5" {
		t.Fatalf("上游设备 MAC 从两个口学到应报 critical 二层环路: %+v", all)
	}
}

func TestLoopUpstreamMACOnPortWithoutLLDP(t *testing.T) {
	core, sw1 := loopTopology()
	sw1.MACTable = []MACEntry{{MAC: "70:00:00:00:00:01", VlanID: "10", Port: "Gi0/5"}}
	got := loopFindingsOf([]DeviceReport{core, sw1})
	if len(got) != 1 || got[0].Level != "warning" || got[0].Port != "Gi0/5" || !strings.Contains(got[0].Title, "LLDP 在 Gi0/48") {
		t.Fatalf("LLDP 在 Gi0/48 的上游设备 MAC 从 Gi0/5 进来应报 warning: %+v", got)
	}
}

func TestLoopIgnoresAggregateMembersAndNonVLANRows(t *testing.T) {
	core, sw1 := loopTopology()
	sw1.Ports[1].AggGroup, sw1.Ports[2].AggGroup = "Ag1", "Ag1"
	sw1.FDBTable = []MACEntry{
		{MAC: "cc:cc:cc:00:00:01", VlanID: "10", Port: "Gi0/5", Locations: []string{"Gi0/5", "Gi0/7"}}, // 同一聚合组
		{MAC: "cc:cc:cc:00:00:02", VlanID: "-", Port: "Gi0/5", Locations: []string{"Gi0/5", "Gi0/48"}}, // 没有确切 VLAN
	}
	if got := loopFindingsOf([]DeviceReport{core, sw1}); len(got) != 0 {
		t.Fatalf("聚合成员口之间 / 没有 VLAN 的行不应判成环路: %+v", got)
	}
}

func TestLoopFewMACsWithoutCorroborationIsInfo(t *testing.T) {
	core, sw1 := loopTopology()
	sw1.FDBTable = []MACEntry{{MAC: "cc:cc:cc:00:00:01", VlanID: "10", Port: "Gi0/5", Locations: []string{"Gi0/5", "Gi0/7"}}}
	got := loopFindingsOf([]DeviceReport{core, sw1})
	if len(got) != 1 || got[0].Level != "info" || !strings.Contains(got[0].Detail, "无线漫游") {
		t.Fatalf("单个 MAC 漂移且无佐证只报 info: %+v", got)
	}

	sw1.STP = &STPInfo{Delta: 10}
	got = loopFindingsOf([]DeviceReport{core, sw1})
	if len(got) != 1 || got[0].Level != "warning" {
		t.Fatalf("有生成树变化佐证时等级上调: %+v", got)
	}
}

func TestLoopStormWithSTPWithoutMACEvidence(t *testing.T) {
	core, sw1 := loopTopology()
	sw1.Ports[1].Ext.InBcastPps = 5000
	sw1.STP = &STPInfo{Delta: 10}
	got := loopFindingsOf([]DeviceReport{core, sw1})
	if len(got) != 1 || got[0].Level != "warning" || !strings.Contains(got[0].Title, "广播风暴的同时生成树") {
		t.Fatalf("同一台设备风暴 + 生成树变化应报疑似环路: %+v", got)
	}
}
