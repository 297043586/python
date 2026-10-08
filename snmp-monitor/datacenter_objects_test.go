package main

import (
	"strings"
	"testing"
)

func dcObjTestLayout() Datacenter {
	return Datacenter{
		Rooms: []DCRoom{{ID: "r", Name: "机房", W: 10, D: 8}},
		Racks: []DCRack{
			{ID: "k1", Name: "A01", Room: "r", X: 2, Z: 2, Height: 42},
			{ID: "k2", Name: "配电柜-1", Room: "r", X: 4, Z: 2, Type: "pdc"},
			{ID: "k3", Name: "电池柜-1", Room: "r", X: 5, Z: 2, Type: "battery"},
		},
	}
}

func TestDatacenterObjects(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Datacenter)
		err  string // 空 = 应该通过
	}{
		{"门贴北墙", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 3, Z: 0}} }, ""},
		{"门贴南墙", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 3, Z: 7, Rot: 180}} }, ""},
		{"门贴东墙(转过来占纵向两块)", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 9, Z: 4, Rot: 270}} }, ""},
		{"两扇门叠在同一面墙上", func(d *Datacenter) {
			d.Objects = []DCObject{{ID: "o1", Kind: "door", Room: "r", X: 3, Z: 0}, {ID: "o2", Kind: "screen", Room: "r", X: 4, Z: 0}}
		}, "叠在一起"},
		{"两扇门并排", func(d *Datacenter) {
			d.Objects = []DCObject{{ID: "o1", Kind: "door", Room: "r", X: 3, Z: 0}, {ID: "o2", Kind: "door", Room: "r", X: 5, Z: 0}}
		}, ""},
		{"门不靠墙", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 3, Z: 4}} }, "贴着墙"},
		{"柱子压在机柜上", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "pillar", Room: "r", X: 2, Z: 3}} }, "重叠"},
		{"灭火器箱不占地, 可以挨着机柜", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "extinguisher", Room: "r", X: 2, Z: 3}} }, ""},
		{"操作台超出机房", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "desk", Room: "r", X: 8, Z: 0}} }, "超出"},
		{"未知类型", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "sofa", Room: "r"}} }, "不认识"},
		{"层高太低", func(d *Datacenter) { d.Rooms[0].H = 200 }, "层高"},
		{"层高 3.5 米", func(d *Datacenter) { d.Rooms[0].H = 350 }, ""},
		{"大机房 90 米", func(d *Datacenter) { d.Rooms[0].W, d.Rooms[0].D = 150, 150 }, ""},
		{"L 形机房: 机柜放在挖掉的角上", func(d *Datacenter) { d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 3, D: 3}} }, "超出"},
		{"L 形机房: 门贴在凹进去的墙上", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 3}}
			d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 7, Z: 3}}
		}, ""},
		{"L 形机房: 门没贴墙", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 3}}
			d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 7, Z: 4}}
		}, "贴着墙"},
		{"挖掉的区域超出机房", func(d *Datacenter) { d.Rooms[0].Cuts = []DCRect{{X: 8, Z: 0, W: 4, D: 2}} }, "超出"},
		{"挖得只剩一点", func(d *Datacenter) { d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 10, D: 7}} }, "挖掉太多"},
		{"斜切角: 对角线外的机柜", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 4, D: 4, Type: "tri", Corner: "nw"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 0, Z: 1, Height: 42}}
		}, "超出"},
		{"斜切角: 对角线里面整块留着的地板可以放", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 4, D: 4, Type: "tri", Corner: "nw"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 3, Z: 2, Height: 42}}
		}, ""},
		{"圆角: 弧线外的机柜", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 4, Type: "arc", Corner: "ne"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 9, Z: 0, Height: 42}}
		}, "超出"},
		{"圆角不在角上", func(d *Datacenter) { d.Rooms[0].Cuts = []DCRect{{X: 5, Z: 0, W: 4, D: 4, Type: "arc", Corner: "ne"}} }, "角上"},
		{"圆角旁边的直墙上可以放门", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 4, Type: "arc", Corner: "ne"}}
			d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 4, Z: 0}}
		}, ""},
		{"门挂在弧墙上", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 4, Type: "arc", Corner: "ne"}}
			d.Racks = d.Racks[:1]
			d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", Wall: "ne", T: 0.5}}
		}, ""},
		{"弧墙上两个东西叠在一起", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 4, Type: "arc", Corner: "ne"}}
			d.Racks = d.Racks[:1]
			d.Objects = []DCObject{{ID: "o1", Kind: "door", Room: "r", Wall: "ne", T: 0.5}, {ID: "o2", Kind: "camera", Room: "r", Wall: "ne", T: 0.55}}
		}, "叠在一起"},
		{"斜墙两头放不下", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 4, D: 4, Type: "tri", Corner: "nw"}}
			d.Objects = []DCObject{{ID: "o", Kind: "screen", Room: "r", Wall: "nw", T: 0.1}}
		}, "放不下"},
		{"南墙中间内凹弧: 弧里面的机柜", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 2, Z: 5, W: 4, D: 3, Type: "arc", Corner: "s", ID: "a1"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 3, Z: 6, Height: 42}}
		}, "超出"},
		{"南墙中间内凹弧: 弧两边的地板可以放", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 2, Z: 5, W: 4, D: 3, Type: "arc", Corner: "s", ID: "a1"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 1, Z: 6, Height: 42}}
		}, ""},
		{"内凹弧上挂门", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 2, Z: 5, W: 4, D: 3, Type: "arc", Corner: "s", ID: "a1"}}
			d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", Wall: "a1", T: 0.5}}
		}, ""},
		{"北墙中间外凸弧: 半圆形凸出部分里放机柜", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 3, D: 3}, {X: 7, Z: 0, W: 3, D: 3}, {X: 3, Z: 0, W: 4, D: 3, Type: "bulge", Corner: "n", ID: "b1"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 5, Z: 1, Height: 42}}
			d.Objects = []DCObject{{ID: "o", Kind: "screen", Room: "r", Wall: "b1", T: 0.5}}
		}, ""},
		{"外凸弧: 弧外面的角落", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 0, Z: 0, W: 3, D: 3}, {X: 7, Z: 0, W: 3, D: 3}, {X: 3, Z: 0, W: 4, D: 3, Type: "bulge", Corner: "n", ID: "b1"}}
			d.Racks = []DCRack{{ID: "k1", Name: "A01", Room: "r", X: 3, Z: 0, Height: 42}}
		}, "超出"},
		{"墙中间的弧没贴着外墙", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 2, Z: 4, W: 4, D: 3, Type: "arc", Corner: "s", ID: "a1"}}
		}, "外墙"},
		{"墙中间的弧没有编号", func(d *Datacenter) { d.Rooms[0].Cuts = []DCRect{{X: 2, Z: 5, W: 4, D: 3, Type: "arc", Corner: "s"}} }, "编号"},
		{"外凸弧不能放在角上", func(d *Datacenter) {
			d.Rooms[0].Cuts = []DCRect{{X: 6, Z: 0, W: 4, D: 4, Type: "bulge", Corner: "ne"}}
		}, "角上"},
		{"挂的斜墙不存在", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", Wall: "se", T: 0.5}} }, "不存在"},
		{"门洞 2.6 米", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 3, Z: 0, H: 260}} }, ""},
		{"门洞比层高还高", func(d *Datacenter) { d.Objects = []DCObject{{ID: "o", Kind: "door", Room: "r", X: 3, Z: 0, H: 320}} }, "门洞高度"},
		{"配电柜里不能放设备", func(d *Datacenter) { d.Items = []DCItem{{ID: "i", Rack: "k2", U: 1, H: 1, Kind: "server", Name: "x"}} }, "落地设备"},
	}
	for _, c := range cases {
		d := dcObjTestLayout()
		c.edit(&d)
		err := validateDatacenter(&d)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: 应该通过, 却报错 %v", c.name, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: 应该报「%s」, 实际 %v", c.name, c.err, err)
		}
	}
}

func TestDCEdgeArcLen(t *testing.T) {
	// 半径 2 的半圆: 长 2π
	c := DCRect{X: 3, Z: 0, W: 4, D: 2, Type: "arc", Corner: "n", ID: "a1"}
	if l := c.wallLen(); l < 6.28 || l > 6.29 {
		t.Errorf("半圆弧长 %.4f, 应该是 6.2832", l)
	}
	// 东墙: 沿墙是 D 方向
	c = DCRect{X: 8, Z: 2, W: 2, D: 4, Type: "bulge", Corner: "e", ID: "b1"}
	if l := c.wallLen(); l < 6.28 || l > 6.29 {
		t.Errorf("东墙半圆弧长 %.4f, 应该是 6.2832", l)
	}
	// 老的四分之一圆角长度不变: 半径 4 -> 2π
	c = DCRect{X: 6, Z: 0, W: 4, D: 4, Type: "arc", Corner: "ne"}
	if l := c.wallLen(); l < 6.28 || l > 6.29 {
		t.Errorf("圆角弧长 %.4f, 应该是 6.2832", l)
	}
}
