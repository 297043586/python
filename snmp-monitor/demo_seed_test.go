package main

// 演示服务的额外假数据(v13): 大 MAC 表(压测 MAC 总表)、30 天告警历史、设备分组。

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"time"
)

var demoGroups = map[string]string{
	"10.0.0.1": "数据中心A", "10.0.0.2": "数据中心A", "10.0.0.3": "数据中心A",
	"10.0.1.1": "1号楼", "10.0.1.2": "1号楼", "10.0.1.3": "2号楼",
}

// demoBigMACTable 给 ACC-RJ-01 / ACC-HW-02 塞上 NETMON_DEMO_MACS 条(默认 20000) MAC, 一部分在两台设备上都学到。
func demoBigMACTable(reps []DeviceReport) {
	n, _ := strconv.Atoi(os.Getenv("NETMON_DEMO_MACS"))
	if n <= 0 {
		n = 20000
	}
	rnd := rand.New(rand.NewSource(7))
	for i := range reps {
		if reps[i].IP != "10.0.1.1" && reps[i].IP != "10.0.1.2" {
			continue
		}
		for k := 0; k < n/2; k++ {
			vlan := strconv.Itoa([]int{10, 20, 30, 40, 100, 200}[rnd.Intn(6)])
			first := byte(rnd.Intn(256)) &^ 1
			if rnd.Intn(5) != 0 {
				first &^= 2 // 大部分是厂商烧录的 MAC, 少数是随机 MAC
			}
			mac := fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", first, rnd.Intn(256), rnd.Intn(256), byte(i), byte(k>>8), byte(k))
			port := fmt.Sprintf("GigabitEthernet 0/%d", 1+rnd.Intn(48))
			kind := "接入口"
			up := rnd.Intn(12) == 0
			if up {
				kind, port = "上联/级联口", "TenGigabitEthernet 0/49"
			}
			reps[i].MACTable = append(reps[i].MACTable, MACEntry{MAC: mac, VlanID: vlan, Port: port, PortKind: kind, IsUplink: up, Ambiguous: rnd.Intn(40) == 0, Locations: []string{port}})
			if rnd.Intn(3) == 0 {
				reps[i].HostMappings = append(reps[i].HostMappings, HostMappingEntry{IP: fmt.Sprintf("192.168.%s.%d", vlan, k%250+1), MAC: mac, VlanID: vlan, RealPort: port, PortKind: kind})
			}
		}
	}
}

// demoSeedHistory 生成最近 30 天的告警历史(固定随机种子, 每次一样)。
func demoSeedHistory(h *AlertHistory, reps []DeviceReport) {
	real := h.now
	defer func() { h.now = real }()
	rnd := rand.New(rand.NewSource(42))
	kinds := []struct{ cat, level, subj, text string }{
		{"cpu", "warning", "", "CPU 使用率 86%"}, {"cpu", "critical", "", "CPU 使用率 97%"}, {"mem", "warning", "", "内存使用率 88%"},
		{"port_err", "warning", "GigabitEthernet0/0/3", "端口 [GigabitEthernet0/0/3] 收包错误持续增加 +37"},
		{"port_err", "warning", "GigabitEthernet0/0/17", "端口 [GigabitEthernet0/0/17] 收包错误持续增加 +12"},
		{"optical", "warning", "10GE1/0/5", "端口 [10GE1/0/5] 收光功率过低: -26.3 dBm, 建议检查光纤/法兰盘"},
		{"bandwidth", "warning", "Ten-GigabitEthernet4/0/1", "端口 [Ten-GigabitEthernet4/0/1] 带宽接近跑满 (入 93.2% / 出 41.0%)"},
		{"unreachable", "critical", "", "设备不可达(SNMP 超时)"}, {"psu", "critical", "PSU2", "电源模块 2 故障"}, {"fan", "critical", "FAN1", "风扇 1 转速异常"},
		{"temp", "warning", "", "板卡温度 71℃"}, {"reboot", "warning", "", "设备重启(运行时长归零)"},
	}
	type ev struct {
		t0, t1, ack time.Time
		rep         DeviceReport
		k           int
	}
	var list []ev
	now := time.Now()
	for i := 0; i < 320; i++ {
		rep := reps[rnd.Intn(len(reps))]
		k := rnd.Intn(len(kinds))
		if rep.IP == "10.0.1.2" && rnd.Intn(2) == 0 {
			k = 3 // 让一个口"反复抖动"
		}
		t0 := now.Add(-time.Duration(rnd.Int63n(int64(30 * 24 * time.Hour))))
		// 白天多、夜里少
		if h := t0.Hour(); h < 7 && rnd.Intn(3) != 0 {
			t0 = t0.Add(time.Duration(8+rnd.Intn(8)) * time.Hour)
		}
		dur := time.Duration(60+rnd.Intn(3*3600)) * time.Second
		if rnd.Intn(8) == 0 {
			dur *= 6
		}
		e := ev{t0: t0, t1: t0.Add(dur), rep: rep, k: k}
		if rnd.Intn(3) != 0 {
			e.ack = t0.Add(time.Duration(30+rnd.Intn(1800)) * time.Second)
		}
		if e.t1.After(now) {
			continue
		}
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t0.Before(list[j].t0) })
	for _, e := range list {
		kd := kinds[e.k]
		id := alertID(e.rep.IP, kd.cat, kd.subj)
		if _, busy := h.open[id]; busy {
			continue
		}
		t0 := e.t0
		h.now = func() time.Time { return t0 }
		h.observe(Alert{ID: id, DeviceID: e.rep.IP, DeviceIP: e.rep.IP, Level: kd.level, Category: kd.cat, Subject: kd.subj, Text: kd.text,
			FirstSeen: t0.Format("2006-01-02 15:04:05")}, e.rep.SysName)
		if !e.ack.IsZero() && e.ack.Before(e.t1) {
			ack := e.ack
			h.now = func() time.Time { return ack }
			h.action(id, []string{"ack", "ack", "close"}[int(ack.Unix())%3], []string{"admin", "noc-zhang", "noc-li"}[int(ack.Unix()/7)%3])
		}
		t1 := e.t1
		h.now = func() time.Time { return t1 }
		act := map[string]bool{}
		for aid, incID := range h.open {
			if inc := h.incidents[incID]; inc != nil && inc.DeviceID == e.rep.IP && aid != id {
				act[aid] = true
			}
		}
		h.settleDevice(e.rep.IP, act)
	}
	h.now = real
	// 现在正在告警的
	for _, rep := range reps {
		for _, a := range append(append([]Alert{}, rep.Criticals...), rep.Warnings...) {
			a.DeviceID = rep.IP
			a.FirstSeen = time.Now().Add(-37 * time.Minute).Format("2006-01-02 15:04:05")
			h.observe(a, rep.SysName)
		}
	}
	h.flush()
}
