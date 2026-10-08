package main

import "testing"

func TestParseLLDPMgmtAddrIdx(t *testing.T) {
	// 真实设备上报的一行大致长这样(timeMark=12345, localPortNum=3, remIndex=1,
	// 地址类型 1=IPv4, 地址长度 4, 后面 4 段是 192.168.1.1)。
	remIdx, ip, ok := parseLLDPMgmtAddrIdx("12345.3.1.1.4.192.168.1.1")
	if !ok || remIdx != "12345.3.1" || ip != "192.168.1.1" {
		t.Fatalf("got remIdx=%q ip=%q ok=%v", remIdx, ip, ok)
	}
	// IPv6(类型 2)或其他管理地址类型应该被跳过, 不当成 IPv4 硬解。
	if _, _, ok := parseLLDPMgmtAddrIdx("12345.3.1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.1"); ok {
		t.Fatalf("IPv6 地址不应该被当成 IPv4 解出来")
	}
	// 索引太短(残缺数据)应该安全返回 false, 不 panic。
	if _, _, ok := parseLLDPMgmtAddrIdx("12345.3.1"); ok {
		t.Fatalf("索引不完整时应该返回 ok=false")
	}
}
