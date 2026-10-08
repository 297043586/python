package main

import (
	"fmt"
	"testing"
)

func TestDetectFormFourSegmentChassis(t *testing.T) {
	var ports []PortDetail
	for _, slot := range []int{2, 3, 5} {
		for i := 1; i <= 24; i++ {
			ports = append(ports, PortDetail{Name: fmt.Sprintf("Ten-GigabitEthernet1/%d/0/%d", slot, i)})
		}
	}
	ports = append(ports, PortDetail{Name: "M-GigabitEthernet0/0/0"})
	f := detectDeviceForm(DeviceReport{Ports: ports}, nil)
	if f.Kind != "chassis" || f.Slots != 3 {
		t.Fatalf("四段式端口分布在 3 块板卡上应该认成框式 3 槽, got %+v", f)
	}

	// 普通三段式堆叠不受影响
	var stack []PortDetail
	for _, m := range []int{1, 2} {
		for i := 1; i <= 48; i++ {
			stack = append(stack, PortDetail{Name: fmt.Sprintf("GigabitEthernet%d/0/%d", m, i)})
		}
	}
	if f := detectDeviceForm(DeviceReport{Ports: stack}, nil); f.Kind != "stack" || f.Members != 2 {
		t.Fatalf("三段式两个成员号仍然是堆叠, got %+v", f)
	}
}
