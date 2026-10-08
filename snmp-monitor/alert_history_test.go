package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAlertHistoryLifecycle(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	h := newAlertHistory(dir, 30)
	h.now = func() time.Time { return clock }
	if err := h.load(); err != nil {
		t.Fatal(err)
	}
	store := newAlertStore(dir)
	store.hist = h

	round := func(alerts ...Alert) DeviceReport {
		rep := DeviceReport{IP: "10.0.0.1", SysName: "SW1", stateKey: "dev1", Criticals: []Alert{}, Warnings: []Alert{}}
		for _, a := range alerts {
			a.DeviceID, a.DeviceIP = "dev1", "10.0.0.1"
			if a.Level == "critical" {
				rep.Criticals = append(rep.Criticals, a)
			} else {
				rep.Warnings = append(rep.Warnings, a)
			}
		}
		store.Sync(&rep)
		store.save()
		return rep
	}
	cpu := Alert{ID: "a-cpu", Level: "warning", Category: "cpu", Text: "CPU 82%"}
	port := Alert{ID: "a-port", Level: "warning", Category: "port_err", Subject: "Gi0/1", Text: "Gi0/1 错包 +12"}

	round(cpu, port) // 10:00 两条告警出现
	clock = clock.Add(5 * time.Minute)
	cpu2 := cpu
	cpu2.Level, cpu2.Text = "critical", "CPU 95%"
	round(cpu2) // 10:05 CPU 升级为严重, 端口恢复
	if err := store.setAck("a-cpu", true, false, "alice"); err != nil {
		t.Fatal(err)
	}
	store.save()
	clock = clock.Add(10 * time.Minute)
	round() // 10:15 CPU 恢复
	clock = clock.Add(time.Hour)
	round(port) // 11:15 端口又出错了 = 新事件
	clock = clock.Add(time.Minute)
	round(port) // 还在

	items, total, st := h.query(alertHistQuery{})
	if total != 3 {
		t.Fatalf("应该是 3 个事件(CPU 1 次, 端口 2 次), 实际 %d: %+v", total, items)
	}
	var cpuInc, portOld, portNew *histItem
	for i := range items {
		it := &items[i]
		switch {
		case it.AlertID == "a-cpu":
			cpuInc = it
		case it.End == 0:
			portNew = it
		default:
			portOld = it
		}
	}
	if cpuInc == nil || cpuInc.Level != "critical" || cpuInc.AckedBy != "alice" || cpuInc.Duration != 15*60 || cpuInc.Text != "CPU 95%" {
		t.Errorf("CPU 事件不对: %+v", cpuInc)
	}
	if portOld == nil || portOld.Duration != 5*60 || portNew == nil || portNew.Flaps != 2 {
		t.Errorf("端口事件不对: old=%+v new=%+v", portOld, portNew)
	}
	if st.Active != 1 || st.Recovered != 2 || st.Critical != 1 || st.Handled != 1 || st.MTTR != (15*60+5*60)/2 || st.MTTA != 5*60 {
		t.Errorf("统计不对: %+v", st)
	}
	_, evs, sib, ok := h.detail(cpuInc.ID)
	if !ok || len(sib) != 1 {
		t.Fatalf("detail: %v %v", ok, sib)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "open,level,ack,recover" {
		t.Errorf("处理过程不对: %v", types)
	}

	// 重启: 从文件回放, 进行中的事件接着用, 不会拆成两段
	h2 := newAlertHistory(dir, 30)
	h2.now = func() time.Time { return clock }
	if err := h2.load(); err != nil {
		t.Fatal(err)
	}
	if _, total2, _ := h2.query(alertHistQuery{}); total2 != 3 {
		t.Fatalf("回放后应该还是 3 个事件, 实际 %d", total2)
	}
	store.hist = h2
	h = h2
	clock = clock.Add(time.Minute)
	round(port)
	clock = clock.Add(time.Minute)
	round()
	if _, total3, st3 := h2.query(alertHistQuery{}); total3 != 3 || st3.Active != 0 {
		t.Errorf("重启后同一次告警不应拆成两个事件: total=%d active=%d", total3, st3.Active)
	}

	// 筛选: 状态两个维度 = 且, 同一维度 = 或
	if _, n, _ := h2.query(alertHistQuery{Status: map[string]bool{"recovered": true, "handled": true}}); n != 1 {
		t.Errorf("已恢复 + 已处理 应该只有 CPU 那一条, 实际 %d", n)
	}
	if _, n, _ := h2.query(alertHistQuery{Cats: map[string]bool{"port_err": true}, Q: "gi0/1"}); n != 2 {
		t.Errorf("按类别 + 关键字应该 2 条, 实际 %d", n)
	}
	// 删除设备: 进行中的事件结束
	round(cpu)
	store.dropDevice("dev1")
	store.save()
	if _, _, st4 := h2.query(alertHistQuery{}); st4.Active != 0 {
		t.Errorf("删掉设备后不应该还有进行中的事件: %+v", st4)
	}
}

func TestAlertHistoryRetentionPurgesOldMonths(t *testing.T) {
	dir := t.TempDir()
	hd := filepath.Join(dir, "alert_history")
	_ = os.MkdirAll(hd, 0o700)
	old := `{"k":"inc","inc":{"id":"x-1","alert_id":"x","device_ip":"1.1.1.1","level":"warning","category":"cpu","text":"old","start":1600000000,"end":1600000100,"last_seen":1600000100}}` + "\n"
	_ = os.WriteFile(filepath.Join(hd, "2020-09.jsonl"), []byte(old), 0o600)
	_ = os.WriteFile(filepath.Join(hd, "2026-09.jsonl"), []byte(strings.Replace(old, "1600000", "1788000", -1)+"{broken line\n"), 0o600)
	h := newAlertHistory(dir, 30)
	h.now = func() time.Time { return time.Unix(1788100000, 0) }
	if err := h.load(); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := h.query(alertHistQuery{}); n != 1 {
		t.Errorf("保留期内只剩 1 条, 实际 %d", n)
	}
	if _, err := os.Stat(filepath.Join(hd, "2020-09.jsonl")); !os.IsNotExist(err) {
		t.Error("超过保留期的月文件应该删掉")
	}
}

func TestAlertHistoryAPI(t *testing.T) {
	dir := t.TempDir()
	h := newAlertHistory(dir, 30)
	_ = h.load()
	h.observe(Alert{ID: "a1", DeviceID: "d", DeviceIP: "10.0.0.9", Level: "critical", Category: "psu", Text: "电源故障"}, "SW9")
	h.flush()
	s := &server{history: h, cfg: &Config{AlertHistoryDays: 30}}
	w := httptest.NewRecorder()
	s.handleAlertHistory(w, httptest.NewRequest("GET", "/api/alerts/history?level=critical&device=10.0.0.9", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":1`) || !strings.Contains(w.Body.String(), "电源故障") {
		t.Fatalf("查询接口不对: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleAlertHistoryExport(w, httptest.NewRequest("GET", "/api/alerts/history/export", nil))
	if !strings.HasPrefix(w.Body.String(), "\xef\xbb\xbf开始时间") || !strings.Contains(w.Body.String(), "电源故障") || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("导出不对: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleAlertHistoryDetail(w, httptest.NewRequest("GET", "/api/alerts/history/detail?id=nope", nil))
	if w.Code != 404 {
		t.Errorf("不存在的记录应该 404, got %d", w.Code)
	}
}
