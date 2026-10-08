package main

import (
	"strings"
	"testing"
	"time"
)

func TestIssueKeyIgnoresChangingNumbers(t *testing.T) {
	a := issueKey("光模块", "Gi0/1 收光 -21.30 dBm 偏弱")
	b := issueKey("光模块", "Gi0/1 收光 -22.05 dBm 偏弱")
	if a != b {
		t.Fatalf("同一个口收光数值变了应该算同一个问题: %q vs %q", a, b)
	}
	if issueKey("x", "Gi0/1 错包 +12") == issueKey("x", "Gi0/2 错包 +12") {
		t.Fatal("端口名里的数字不能被去掉, 不同端口是不同问题")
	}
}

func TestCompareSection(t *testing.T) {
	prev := &InspReport{Score: 80, CreatedAt: "2026-09-27 08:00:00", Snapshot: &reportSnapshot{Score: 80,
		Issues: map[string]string{"离线|10.0.0.9": "设备离线: X", "半双工|10.0.0.1|Gi0/3": "半双工: A Gi0/3"}}}
	cur := &reportSnapshot{Score: 72, Issues: map[string]string{"半双工|10.0.0.1|Gi0/3": "半双工: A Gi0/3", "弱光|10.0.0.2|Gi0/1": "弱光: B Gi0/1"}}
	sec := compareSection(prev, cur)
	var all []string
	for _, it := range sec.Items {
		all = append(all, it.Level+" "+it.Text)
	}
	j := strings.Join(all, "\n")
	for _, want := range []string{"warn 评分 80 → 72, 下降 8 分", "新增问题 1 项, 已解决 1 项, 仍然存在 1 项", "warn 🆕 弱光: B Gi0/1", "ok ✅ 设备离线: X"} {
		if !strings.Contains(j, want) {
			t.Errorf("缺少「%s」:\n%s", want, j)
		}
	}
	if compareSection(nil, cur) != nil {
		t.Error("没有上一份报告时不该有对比章节")
	}
}

func TestEnrichNetworkReport(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	reps := demoReports()
	nr := buildNetworkReport(reps, "2026-09-28 10:00:00")
	enrichNetworkReport(nr, reps, nil, nil, cfg, time.Now())
	titles := map[string]*ReportSection{}
	for i := range nr.Sections {
		titles[nr.Sections[i].Title] = &nr.Sections[i]
	}
	for _, want := range []string{"设备评分分布", "二层健康(环路 / 风暴 / ARP)", "CPU 使用率 TOP 10", "端口容量", "运行时长", "资产清单"} {
		if titles[want] == nil {
			t.Errorf("全网报告缺少章节「%s」", want)
		}
	}
	if c := titles["设备评分分布"].Chart; c == nil || len(c.Values) != 5 {
		t.Errorf("评分分布应该是 5 档的柱状图: %+v", c)
	}
	if nr.Snapshot == nil || len(nr.Snapshot.Issues) == 0 {
		t.Error("报告应该带上问题快照, 下一份才能对比")
	}
	// 第二份报告跟第一份对比
	nr.CreatedAt = "2026-09-27 10:00:00"
	nr2 := buildNetworkReport(reps, "2026-09-28 11:00:00")
	enrichNetworkReport(nr2, reps, nil, nr, cfg, time.Now())
	if nr2.Sections[0].Title != "与上次报告对比" {
		t.Errorf("有上一份报告时, 对比章节应该在最前面, got %q", nr2.Sections[0].Title)
	}
}
