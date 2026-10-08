package main

import (
	"math"
	"testing"
)

func TestComputeLiveRates(t *testing.T) {
	prev := &liveSample{in: map[string]uint64{"1": 0, "2": 0}, out: map[string]uint64{"1": 0}, inAt: 10_000, outAt: 10_500, hc: true}
	// 5 秒里入方向收了 625,000,000 字节 = 1000 Mbps; 出方向 5 秒 62,500,000 字节 = 100 Mbps
	cur := &liveSample{in: map[string]uint64{"1": 625_000_000, "2": 0}, out: map[string]uint64{"1": 62_500_000}, inAt: 15_000, outAt: 15_500, hc: true}
	got := computeLiveRates(prev, cur, map[string]string{"1": "Gi0/1", "2": "Gi0/2"}, map[string]int64{"1": 10000, "2": 1000})
	by := map[string]livePort{}
	for _, p := range got {
		by[p.Index] = p
	}
	if math.Abs(by["1"].InMbps-1000) > 0.01 || math.Abs(by["1"].OutMbps-100) > 0.01 {
		t.Fatalf("速率算错: %+v", by["1"])
	}
	if math.Abs(by["1"].InPct-10) > 0.01 {
		t.Fatalf("万兆口跑 1000M 应该是 10%%: %+v", by["1"])
	}
	if by["2"].InMbps != 0 || by["2"].OutMbps != 0 {
		t.Fatalf("没流量 / 没出方向计数器的口应该是 0: %+v", by["2"])
	}
}
