package main

// 3D 机房(顶栏「🗄️ 机柜」→「🏢 3D 机房」)的后端。存 data/datacenter.json:
//
//   - 机房(可以有多个): 名字 + 长宽(按 600mm 地板砖计);
//   - 机柜: 名字、所在机房、地板砖坐标、朝向、高度(U);
//   - 非监控设备: 服务器/防火墙/路由器/存储/UPS/配线架/PDU 等, 指定放在哪个机柜的第几 U;
//   - 冷通道: 两排机柜之间的封闭通道(一块矩形地板区域), 不能跟机柜重叠;
//   - 设备形态覆盖: 自动识别的堆叠台数/框式高度不对时手工指定(见 form.go)。
//
// 被监控的交换机放在哪个机柜仍然用原来的 data/racks.json(机柜名 + 最上面的 U 号),
// 2D 机柜视图和 3D 机房看到的是同一份位置; 3D 里拖动/指定交换机位置也是写 racks.json。
// 设备引用了一个 3D 机房里还没有的机柜名(比如 sysLocation)时, 前端会自动在机房里
// 补一个机柜(标成"自动"), 用户挪动或保存后才写进 datacenter.json。

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type DCRoom struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	W    int    `json:"w"`           // 地板砖列数(每块 600mm)
	D    int    `json:"d"`           // 地板砖行数
	H    int    `json:"h,omitempty"` // 层高(cm, 地板到吊顶), 0 = 默认 300
	// Cuts: 从 W×D 矩形里挖掉的区域(L 形机房、凹进去的墙角、中间的核心筒 / 楼梯间)。墙沿剩下地板的边界自动生成
	Cuts []DCRect `json:"cuts,omitempty"`
}

// DCRect 是一块矩形地板区域(左上角 X/Z, 宽 W 深 D, 单位地板砖)。
type DCRect struct {
	X int `json:"x"`
	Z int `json:"z"`
	W int `json:"w"`
	D int `json:"d"`
	// Type: 空 = 整块挖掉(矩形); 机房角上的这块还可以是 tri = 斜切(沿对角线一面斜墙), arc = 圆角(一段弧形墙)。
	// 贴着一面外墙的这块还可以做成墙中间的弧: arc = 内凹弧(墙往机房里鼓进来半个椭圆),
	// bulge = 外凸弧(墙往外鼓出去半个椭圆, 通常两边再各挖掉一块, 做成半圆形的凸出部分)。
	// Corner: 斜切 / 圆角挖掉的是哪个角 nw / ne / sw / se(必须就是机房的那个角);
	// 墙中间的弧是贴着哪面外墙 n / s / w / e(这块要贴着那面墙)
	Type   string `json:"type,omitempty"`
	Corner string `json:"corner,omitempty"`
	// ID: 墙中间的弧用它当墙的编号(同一面外墙上可以有好几段弧), 挂在上面的物件 DCObject.Wall = 这个 ID
	ID string `json:"id,omitempty"`
}

func (c DCRect) has(x, z int) bool { return x >= c.X && x < c.X+c.W && z >= c.Z && z < c.Z+c.D }

// edge: 墙中间的弧(贴着一面外墙), 不是机房角上的
func (c DCRect) edge() bool { return len(c.Corner) == 1 }

// wallID: 斜墙 / 弧墙的编号(挂在上面的物件用它找墙): 角上的就是角, 墙中间的是 ID
func (c DCRect) wallID() string {
	if c.edge() {
		return c.ID
	}
	return c.Corner
}

// edgeLocal: 墙中间的弧, 点 (px, pz) 换成沿墙的位置 s(0~L) 和离外墙的深度 t(0~H)
func (c DCRect) edgeLocal(px, pz float64) (s, t, l, h float64) {
	x0, z0, x1, z1 := float64(c.X), float64(c.Z), float64(c.X+c.W), float64(c.Z+c.D)
	switch c.Corner {
	case "s":
		return px - x0, z1 - pz, x1 - x0, z1 - z0
	case "w":
		return pz - z0, px - x0, z1 - z0, x1 - x0
	case "e":
		return pz - z0, x1 - px, z1 - z0, x1 - x0
	}
	return px - x0, pz - z0, x1 - x0, z1 - z0
}

// keeps 判断斜切 / 圆角 / 墙中间的弧这块里, 点 (px, pz)(地板砖坐标, 可以是小数)是否还留在机房里。
// 斜切: 留下对角线靠里的那个三角形; 圆角: 留下以内侧角为圆心、半径 W×D 的四分之一椭圆;
// 内凹弧: 挖掉以外墙上中点为圆心的半个椭圆; 外凸弧: 只留下以靠里那条边的中点为圆心的半个椭圆
func (c DCRect) keeps(px, pz float64) bool {
	const eps = 1e-6
	if c.edge() {
		s, t, l, h := c.edgeLocal(px, pz)
		u := (s - l/2) / (l / 2)
		if c.Type == "bulge" {
			v := (h - t) / h
			return u*u+v*v <= 1+eps
		}
		v := t / h
		return u*u+v*v >= 1-eps
	}
	x0, z0, x1, z1 := float64(c.X), float64(c.Z), float64(c.X+c.W), float64(c.Z+c.D)
	// qx, qz: 内侧角(被挖掉的机房角的对角)
	qx, qz := x0, z1
	switch c.Corner {
	case "nw":
		qx, qz = x1, z1
	case "ne":
		qx, qz = x0, z1
	case "sw":
		qx, qz = x1, z0
	case "se":
		qx, qz = x0, z0
	}
	u, v := (px-qx)/float64(c.W), (pz-qz)/float64(c.D)
	if u < 0 {
		u = -u
	}
	if v < 0 {
		v = -v
	}
	if c.Type == "arc" {
		return u*u+v*v <= 1+eps
	}
	return u+v <= 1+eps
}

// wallLen: 斜墙 / 弧墙的长度(地板砖为单位)。弧墙按 400 段折线算, 跟前端的折线长度差不到千分之一
func (c DCRect) wallLen() float64 {
	w, d := float64(c.W), float64(c.D)
	if c.Type == "tri" {
		return math.Hypot(w, d)
	}
	// 四分之一椭圆(半轴 w, d); 墙中间的弧是半个椭圆(沿墙半轴 L/2, 深 H), 按 800 段算
	a, b, n, span := w, d, 400, math.Pi/2
	if c.edge() {
		_, _, l, h := c.edgeLocal(0, 0)
		a, b, n, span = l/2, h, 800, math.Pi
	}
	l, px, pz := 0.0, a*math.Cos(0), 0.0
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n) * span
		x, z := a*math.Cos(t), b*math.Sin(t)
		l += math.Hypot(x-px, z-pz)
		px, pz = x, z
	}
	return l
}

// shaped: 斜切 / 圆角 / 墙中间的弧(只挖掉一部分)
func (c DCRect) shaped() bool { return c.Type == "tri" || c.Type == "arc" || c.Type == "bulge" }

// inside 判断地板砖 (x, z) 能不能用(放机柜 / 物件): 在 W×D 范围内, 没被整块挖掉,
// 在斜切 / 圆角那块里的话要整块都留在墙内。
func (r DCRoom) inside(x, z int) bool {
	if x < 0 || z < 0 || x >= r.W || z >= r.D {
		return false
	}
	for _, c := range r.Cuts {
		if !c.has(x, z) {
			continue
		}
		if !c.shaped() {
			return false
		}
		fx, fz := float64(x), float64(z)
		if !c.keeps(fx, fz) || !c.keeps(fx+1, fz) || !c.keeps(fx, fz+1) || !c.keeps(fx+1, fz+1) {
			return false
		}
	}
	return true
}

// hardOut: 这块地板在机房外面, 而且中间是一面直墙(外墙或整块挖掉的区域)。贴墙物件只能贴这种墙
func (r DCRoom) hardOut(x, z int) bool {
	if x < 0 || z < 0 || x >= r.W || z >= r.D {
		return true
	}
	for _, c := range r.Cuts {
		if c.has(x, z) && !c.shaped() {
			return true
		}
	}
	return false
}

// inShaped: 这块地板在某个斜切 / 圆角的范围里(它贴着的外墙已经换成斜墙 / 弧墙了)
func (r DCRoom) inShaped(x, z int) bool {
	for _, c := range r.Cuts {
		if c.shaped() && c.has(x, z) {
			return true
		}
	}
	return false
}

type DCRack struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Room   string `json:"room"`
	X      int    `json:"x"` // 地板砖坐标(左上角为 0,0)
	Z      int    `json:"z"`
	Rot    int    `json:"rot"` // 0 / 90 / 180 / 270: 正面朝向
	Height int    `json:"height"`
	Color  string `json:"color,omitempty"`
	// rack = 机柜(默认); 落地设备(不能放东西): ac = 精密空调, ups = UPS 柜, pdc = 配电柜 / 列头柜, battery = 电池柜
	Type string `json:"type,omitempty"`
}

// rackCells 返回机柜占用的地板砖: 机柜 600×1200mm, 朝向 0/180 时纵向占两块, 90/270 时横向占两块。
func rackCells(k DCRack) [][2]int {
	if k.Rot == 90 || k.Rot == 270 {
		return [][2]int{{k.X, k.Z}, {k.X + 1, k.Z}}
	}
	return [][2]int{{k.X, k.Z}, {k.X, k.Z + 1}}
}

type DCItem struct {
	ID     string `json:"id"`
	Rack   string `json:"rack"` // 机柜 ID
	U      int    `json:"u"`    // 最下面一格的 U 号(从 1 开始)
	H      int    `json:"h"`    // 占几 U
	Kind   string `json:"kind"` // server / firewall / router / storage / ups / patch / pdu / switch / other
	Name   string `json:"name"`
	Model  string `json:"model,omitempty"`
	IP     string `json:"ip,omitempty"` // 仅供展示(比如服务器的管理地址), 不参与监控
	Note   string `json:"note,omitempty"`
	Status string `json:"status,omitempty"` // ok / warn / bad / off: 手工标记的状态灯
}

// DCAisle 是冷通道: 机房里一块矩形地板区域(两排机柜正面相对, 中间封闭起来送冷风)。
type DCAisle struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Room string `json:"room"`
	X    int    `json:"x"` // 左上角地板砖
	Z    int    `json:"z"`
	W    int    `json:"w"` // 占几块地板砖(东西向)
	D    int    `json:"d"` // 占几块地板砖(南北向)
}

// DCObject 是机房里的场景物件: 门、柱子、操作台、墙挂大屏、灭火器、气体灭火钢瓶、摄像头。
// 只是让 3D 机房更像真实现场, 不参与监控。
type DCObject struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Room string `json:"room"`
	X    int    `json:"x"` // 占用范围左上角的地板砖
	Z    int    `json:"z"`
	Rot  int    `json:"rot"`         // 0 / 90 / 180 / 270: 正面朝向(贴墙的物件 = 背对哪面墙)
	H    int    `json:"h,omitempty"` // 门: 门洞高度(cm), 0 = 默认 230
	// 挂在斜墙 / 弧墙上: Wall = 那面墙的编号(斜切 / 圆角是所在的角 nw/ne/sw/se, 墙中间的弧是它的 ID), T = 物件中心沿墙的位置(0~1, 按墙长算);
	// 这时 X/Z/Rot 只是近似位置, 不参与校验
	Wall string  `json:"wall,omitempty"`
	T    float64 `json:"t,omitempty"`
}

// dcObjectKinds: 类型 -> 占地(宽 × 深, 地板砖, 朝向 0 时), 是否占地板(别的东西不能放在上面), 是否必须贴墙
var dcObjectKinds = map[string]struct {
	W, D        int
	Solid, Wall bool
}{
	"door":         {2, 1, false, true},
	"pillar":       {1, 1, true, false},
	"desk":         {3, 2, true, false},
	"screen":       {4, 1, false, true},
	"extinguisher": {1, 1, false, false},
	"gas":          {2, 1, true, false},
	"camera":       {1, 1, false, true},
}

// objectCells 返回物件占的地板砖(朝向 90/270 时宽深对调)。
func objectCells(o DCObject) [][2]int {
	k := dcObjectKinds[o.Kind]
	fx, fz := k.W, k.D
	if o.Rot == 90 || o.Rot == 270 {
		fx, fz = fz, fx
	}
	out := make([][2]int, 0, fx*fz)
	for x := 0; x < fx; x++ {
		for z := 0; z < fz; z++ {
			out = append(out, [2]int{o.X + x, o.Z + z})
		}
	}
	return out
}

type Datacenter struct {
	Rooms   []DCRoom                `json:"rooms"`
	Racks   []DCRack                `json:"racks"`
	Items   []DCItem                `json:"items"`
	Aisles  []DCAisle               `json:"aisles"`
	Objects []DCObject              `json:"objects"`
	Forms   map[string]FormOverride `json:"forms"` // 设备 IP -> 手工指定的形态
	Updated string                  `json:"updated,omitempty"`
	By      string                  `json:"by,omitempty"`
}

var dcItemKinds = []string{"server", "firewall", "router", "storage", "ups", "patch", "pdu", "switch", "ac", "other"}

type DatacenterStore struct {
	mu   sync.RWMutex
	path string
	data Datacenter
}

func newDatacenterStore(dir string) *DatacenterStore {
	return &DatacenterStore{path: filepath.Join(dir, "datacenter.json")}
}

func (d *DatacenterStore) load() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	err := readJSON(d.path, &d.data)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	d.normalizeLocked()
	return nil
}

func (d *DatacenterStore) normalizeLocked() {
	if len(d.data.Rooms) == 0 {
		d.data.Rooms = []DCRoom{{ID: "room1", Name: "主机房", W: 20, D: 14}}
	}
	if d.data.Racks == nil {
		d.data.Racks = []DCRack{}
	}
	if d.data.Items == nil {
		d.data.Items = []DCItem{}
	}
	if d.data.Aisles == nil {
		d.data.Aisles = []DCAisle{}
	}
	if d.data.Objects == nil {
		d.data.Objects = []DCObject{}
	}
	if d.data.Forms == nil {
		d.data.Forms = map[string]FormOverride{}
	}
}

func (d *DatacenterStore) Snapshot() Datacenter {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := d.data
	// 必须是 []T{} 而不是 []T(nil): 新装还没放机柜时后者是 nil, JSON 里变成 null,
	// 前端 layout.racks.forEach 直接报 "Cannot read properties of null (reading 'forEach')"。
	out.Rooms = append([]DCRoom{}, d.data.Rooms...)
	out.Racks = append([]DCRack{}, d.data.Racks...)
	out.Items = append([]DCItem{}, d.data.Items...)
	out.Aisles = append([]DCAisle{}, d.data.Aisles...)
	out.Objects = append([]DCObject{}, d.data.Objects...)
	out.Forms = make(map[string]FormOverride, len(d.data.Forms))
	for k, v := range d.data.Forms {
		out.Forms[k] = v
	}
	return out
}

func (d *DatacenterStore) Form(ip string) (FormOverride, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	f, ok := d.data.Forms[ip]
	return f, ok && f.Kind != ""
}

func dcText(s string, max int, what string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		return "", fmt.Errorf("%s太长(最多 %d 个字)", what, max)
	}
	return s, nil
}

// validateDatacenter 校验整份布局: ID 唯一、坐标在机房范围内、机柜不重叠、设备不越界不重叠。
func validateDatacenter(dc *Datacenter) error {
	if len(dc.Rooms) == 0 {
		return errors.New("至少要有一个机房")
	}
	if len(dc.Rooms) > 20 || len(dc.Racks) > 500 || len(dc.Items) > 5000 || len(dc.Aisles) > 200 || len(dc.Objects) > 1000 {
		return errors.New("机房/机柜/设备数量超过上限")
	}
	rooms := map[string]DCRoom{}
	for i := range dc.Rooms {
		r := &dc.Rooms[i]
		var err error
		if r.Name, err = dcText(r.Name, 30, "机房名"); err != nil {
			return err
		}
		if r.ID == "" || rooms[r.ID].ID != "" {
			return errors.New("机房 ID 为空或重复")
		}
		if r.Name == "" {
			r.Name = "机房"
		}
		if r.W < 4 || r.W > 150 || r.D < 4 || r.D > 150 {
			return fmt.Errorf("机房「%s」的尺寸需要在 4~150 块地板砖(2.4~90 米)之间", r.Name)
		}
		if r.H != 0 && (r.H < 240 || r.H > 800) {
			return fmt.Errorf("机房「%s」的层高需要在 2.4~8 米之间", r.Name)
		}
		if len(r.Cuts) > 30 {
			return fmt.Errorf("机房「%s」挖掉的区域太多了(最多 30 块)", r.Name)
		}
		for i, c := range r.Cuts {
			if c.W < 1 || c.D < 1 || c.X < 0 || c.Z < 0 || c.X+c.W > r.W || c.Z+c.D > r.D {
				return fmt.Errorf("机房「%s」挖掉的区域超出了机房范围", r.Name)
			}
			if c.Type == "" {
				r.Cuts[i].Corner = ""
				r.Cuts[i].ID = ""
				continue
			}
			if !c.shaped() {
				return fmt.Errorf("机房「%s」挖掉区域的形状只能是矩形 / 斜切 / 圆角 / 弧形墙", r.Name)
			}
			if c.edge() {
				// 墙中间的弧: 贴着那面外墙; 要有编号(挂在上面的物件靠它找墙), 编号不能重复
				atSide := map[string]bool{"n": c.Z == 0, "s": c.Z+c.D == r.D, "w": c.X == 0, "e": c.X+c.W == r.W}[c.Corner]
				if !atSide || (c.Type != "arc" && c.Type != "bulge") {
					return fmt.Errorf("机房「%s」墙中间的弧要贴着那面外墙放", r.Name)
				}
				// 编号不能跟角的名字一样, 不然跟角上斜墙 / 弧墙的物件分不清
				if c.ID == "" || len(c.ID) > 40 || inList(c.ID, []string{"nw", "ne", "sw", "se"}) {
					return fmt.Errorf("机房「%s」墙中间的弧缺少编号", r.Name)
				}
				for j, o := range r.Cuts {
					if j != i && o.edge() && o.ID == c.ID {
						return fmt.Errorf("机房「%s」墙中间的弧编号重复了", r.Name)
					}
				}
			} else {
				r.Cuts[i].ID = ""
				// 斜切 / 圆角必须就在机房的那个角上, 也不能跟别的挖掉区域重叠(墙是按角算的)
				atCorner := map[string]bool{"nw": c.X == 0 && c.Z == 0, "ne": c.X+c.W == r.W && c.Z == 0,
					"sw": c.X == 0 && c.Z+c.D == r.D, "se": c.X+c.W == r.W && c.Z+c.D == r.D}[c.Corner]
				if !atCorner || c.Type == "bulge" {
					return fmt.Errorf("机房「%s」的斜切 / 圆角要放在机房的角上", r.Name)
				}
			}
			for j, o := range r.Cuts {
				if j != i && o.X < c.X+c.W && c.X < o.X+o.W && o.Z < c.Z+c.D && c.Z < o.Z+o.D {
					return fmt.Errorf("机房「%s」的斜切 / 圆角 / 弧形墙跟别的挖掉区域重叠了", r.Name)
				}
			}
		}
		left := 0
		for x := 0; x < r.W; x++ {
			for z := 0; z < r.D; z++ {
				if r.inside(x, z) {
					left++
				}
			}
		}
		if left < 16 {
			return fmt.Errorf("机房「%s」挖掉太多了, 剩下的地板不到 16 块", r.Name)
		}
		rooms[r.ID] = *r
	}
	racks := map[string]DCRack{}
	occupied := map[string]string{}
	names := map[string]bool{}
	for i := range dc.Racks {
		k := &dc.Racks[i]
		var err error
		if k.Name, err = dcText(k.Name, 40, "机柜名"); err != nil {
			return err
		}
		if k.Name == "" {
			return errors.New("机柜名不能为空")
		}
		if names[k.Name] {
			return fmt.Errorf("机柜名「%s」重复了(被监控交换机按机柜名放置, 名字不能重复)", k.Name)
		}
		names[k.Name] = true
		if k.ID == "" || racks[k.ID].ID != "" {
			return errors.New("机柜 ID 为空或重复")
		}
		room, ok := rooms[k.Room]
		if !ok {
			return fmt.Errorf("机柜「%s」所在的机房不存在", k.Name)
		}
		if k.Rot%90 != 0 || k.Rot < 0 || k.Rot >= 360 {
			return fmt.Errorf("机柜「%s」的朝向只能是 0/90/180/270", k.Name)
		}
		if !inList(k.Type, []string{"", "rack", "ac", "ups", "pdc", "battery"}) {
			k.Type = "rack"
		}
		if k.Type == "" {
			k.Type = "rack"
		}
		if k.Height == 0 {
			k.Height = 42
		}
		if k.Height < 6 || k.Height > 60 {
			return fmt.Errorf("机柜「%s」的高度需要在 6~60U", k.Name)
		}
		if k.Color != "" && (len(k.Color) != 7 || k.Color[0] != '#') {
			k.Color = ""
		}
		for _, c := range rackCells(*k) {
			if !room.inside(c[0], c[1]) {
				return fmt.Errorf("机柜「%s」超出了机房「%s」的范围", k.Name, room.Name)
			}
			cell := fmt.Sprintf("%s|%d|%d", k.Room, c[0], c[1])
			if other, ok := occupied[cell]; ok {
				return fmt.Errorf("机柜「%s」和「%s」放在了同一块地板上", k.Name, other)
			}
			occupied[cell] = k.Name
		}
		racks[k.ID] = *k
	}
	// 冷通道: 在机房范围内, 不跟机柜 / 别的冷通道重叠
	aisleIDs := map[string]bool{}
	for i := range dc.Aisles {
		a := &dc.Aisles[i]
		var err error
		if a.Name, err = dcText(a.Name, 30, "冷通道名"); err != nil {
			return err
		}
		if a.Name == "" {
			a.Name = "冷通道"
		}
		if a.ID == "" || aisleIDs[a.ID] {
			return errors.New("冷通道 ID 为空或重复")
		}
		aisleIDs[a.ID] = true
		room, ok := rooms[a.Room]
		if !ok {
			return fmt.Errorf("冷通道「%s」所在的机房不存在", a.Name)
		}
		if a.W < 1 || a.D < 1 {
			return fmt.Errorf("冷通道「%s」的大小不对", a.Name)
		}
		for x := a.X; x < a.X+a.W; x++ {
			for z := a.Z; z < a.Z+a.D; z++ {
				if !room.inside(x, z) {
					return fmt.Errorf("冷通道「%s」超出了机房「%s」的范围", a.Name, room.Name)
				}
				cell := fmt.Sprintf("%s|%d|%d", a.Room, x, z)
				if other, ok := occupied[cell]; ok {
					return fmt.Errorf("冷通道「%s」跟「%s」重叠了", a.Name, other)
				}
				occupied[cell] = a.Name
			}
		}
	}
	// 场景物件: 在机房范围内; 占地板的不跟机柜 / 冷通道 / 别的物件重叠; 贴墙的必须靠着它背对的那面墙,
	// 同一面墙上的门 / 大屏 / 摄像头之间也不能叠在一起
	objIDs := map[string]bool{}
	onWall := map[string]string{}           // 机房|朝向|x|z -> 物件名
	shapedUsed := map[string][][2]float64{} // 机房|角 -> 斜墙 / 弧墙上已经占用的区间(沿墙, 地板砖)
	for i := range dc.Objects {
		o := &dc.Objects[i]
		var err error
		if o.Name, err = dcText(o.Name, 30, "物件名"); err != nil {
			return err
		}
		kind, ok := dcObjectKinds[o.Kind]
		if !ok {
			return fmt.Errorf("不认识的场景物件类型「%s」", o.Kind)
		}
		if o.ID == "" || objIDs[o.ID] {
			return errors.New("场景物件 ID 为空或重复")
		}
		objIDs[o.ID] = true
		room, ok := rooms[o.Room]
		if !ok {
			return errors.New("场景物件所在的机房不存在")
		}
		if o.Rot%90 != 0 || o.Rot < 0 || o.Rot >= 360 {
			return errors.New("场景物件的朝向只能是 0/90/180/270")
		}
		label := o.Name
		if label == "" {
			label = o.Kind
		}
		if o.Kind != "door" {
			o.H = 0
		} else if o.H != 0 {
			wallH := room.H
			if wallH == 0 {
				wallH = 300
			}
			if o.H < 180 || o.H > wallH-10 {
				return fmt.Errorf("「%s」的门洞高度需要在 1.8 米到层高减 0.1 米之间", label)
			}
		}
		if o.Wall != "" {
			// 挂在斜墙 / 弧墙上: 那面墙存在, 物件放得下, 跟同一面墙上的别的物件不重叠
			if !kind.Wall {
				return fmt.Errorf("「%s」不是贴墙的东西", label)
			}
			var cut *DCRect
			for ci := range room.Cuts {
				if room.Cuts[ci].shaped() && room.Cuts[ci].wallID() == o.Wall {
					cut = &room.Cuts[ci]
				}
			}
			if cut == nil {
				return fmt.Errorf("「%s」挂的那面斜墙 / 弧墙不存在了", label)
			}
			l, half := cut.wallLen(), float64(kind.W)/2
			sPos := o.T * l
			if o.T < 0 || o.T > 1 || sPos < half-0.05 || sPos > l-half+0.05 {
				return fmt.Errorf("「%s」在斜墙 / 弧墙上放不下", label)
			}
			for _, iv := range shapedUsed[o.Room+"|"+o.Wall] {
				if sPos-half < iv[1]-0.02 && iv[0]+0.02 < sPos+half {
					return fmt.Errorf("「%s」跟同一面斜墙 / 弧墙上的东西叠在一起了", label)
				}
			}
			shapedUsed[o.Room+"|"+o.Wall] = append(shapedUsed[o.Room+"|"+o.Wall], [2]float64{sPos - half, sPos + half})
			continue
		}
		o.T = 0
		cells := objectCells(*o)
		for _, c := range cells {
			if !room.inside(c[0], c[1]) {
				return fmt.Errorf("「%s」超出了机房「%s」的范围", label, room.Name)
			}
		}
		if kind.Wall {
			// 贴墙: 占的每块地板, 背后(朝向的反方向)那一块都在机房外面(外墙或挖掉的区域)
			back := map[int][2]int{0: {0, -1}, 180: {0, 1}, 90: {-1, 0}, 270: {1, 0}}[o.Rot]
			atWall := true
			for _, c := range cells {
				// 背后是直墙(斜墙 / 弧墙上的用 Wall + T 表示)
				if !room.hardOut(c[0]+back[0], c[1]+back[1]) || room.inShaped(c[0], c[1]) {
					atWall = false
				}
			}
			if !atWall {
				return fmt.Errorf("「%s」要贴着墙放", label)
			}
			for _, c := range cells {
				key := fmt.Sprintf("%s|%d|%d|%d", o.Room, o.Rot, c[0], c[1])
				if other, ok := onWall[key]; ok {
					return fmt.Errorf("「%s」跟「%s」在墙上叠在一起了", label, other)
				}
				onWall[key] = label
			}
		}
		if !kind.Solid {
			continue
		}
		for _, c := range cells {
			cell := fmt.Sprintf("%s|%d|%d", o.Room, c[0], c[1])
			if other, ok := occupied[cell]; ok {
				return fmt.Errorf("「%s」跟「%s」重叠了", label, other)
			}
			occupied[cell] = label
		}
	}
	ids := map[string]bool{}
	used := map[string]string{} // rack|u -> item name
	for i := range dc.Items {
		it := &dc.Items[i]
		var err error
		if it.Name, err = dcText(it.Name, 40, "设备名"); err != nil {
			return err
		}
		if it.Model, err = dcText(it.Model, 60, "型号"); err != nil {
			return err
		}
		if it.Note, err = dcText(it.Note, 200, "备注"); err != nil {
			return err
		}
		if it.IP, err = dcText(it.IP, 60, "地址"); err != nil {
			return err
		}
		if it.ID == "" || ids[it.ID] {
			return errors.New("设备 ID 为空或重复")
		}
		ids[it.ID] = true
		if !inList(it.Kind, dcItemKinds) {
			it.Kind = "other"
		}
		if !inList(it.Status, []string{"", "ok", "warn", "bad", "off"}) {
			it.Status = ""
		}
		rk, ok := racks[it.Rack]
		if !ok {
			return fmt.Errorf("设备「%s」所在的机柜不存在", it.Name)
		}
		if rk.Type != "rack" {
			return fmt.Errorf("「%s」是落地设备(空调/UPS/配电柜/电池柜), 里面不能放「%s」", rk.Name, it.Name)
		}
		if it.H < 1 || it.H > 20 {
			return fmt.Errorf("设备「%s」的高度需要在 1~20U", it.Name)
		}
		if it.U < 1 || it.U+it.H-1 > rk.Height {
			return fmt.Errorf("设备「%s」超出了机柜「%s」的 U 位范围(1~%d)", it.Name, rk.Name, rk.Height)
		}
		for u := it.U; u < it.U+it.H; u++ {
			key := fmt.Sprintf("%s|%d", it.Rack, u)
			if other, ok := used[key]; ok {
				return fmt.Errorf("机柜「%s」U%d 同时放了「%s」和「%s」", rk.Name, u, other, it.Name)
			}
			used[key] = it.Name
		}
	}
	for ip, f := range dc.Forms {
		if f.Kind == "" {
			delete(dc.Forms, ip)
			continue
		}
		if !inList(f.Kind, []string{"box", "stack", "chassis"}) {
			return fmt.Errorf("设备 %s 的形态 %q 不认识", ip, f.Kind)
		}
		if f.Members < 0 || f.Members > 16 || f.UEach < 0 || f.UEach > 40 {
			return fmt.Errorf("设备 %s 的堆叠台数(最多 16)或高度(最多 40U)不合理", ip)
		}
		if !inList(f.Orient, []string{"", "v", "h"}) {
			return fmt.Errorf("设备 %s 的板卡方向只能是 竖插 / 横插", ip)
		}
	}
	return nil
}

func (d *DatacenterStore) Save(dc Datacenter, by string) error {
	if dc.Forms == nil {
		dc.Forms = map[string]FormOverride{}
	}
	if err := validateDatacenter(&dc); err != nil {
		return err
	}
	dc.Updated = time.Now().Format("2006-01-02 15:04:05")
	dc.By = by
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := atomicWriteJSON(d.path, dc, 0o600); err != nil {
		return err
	}
	d.data = dc
	d.normalizeLocked()
	return nil
}

// SetForm 只改一台设备的形态(机柜视图的小弹窗用), 不动其他布局。
func (d *DatacenterStore) SetForm(ip string, f FormOverride, by string) error {
	cp := d.Snapshot()
	if f.Kind == "" {
		delete(cp.Forms, ip)
	} else {
		cp.Forms[ip] = f
	}
	return d.Save(cp, by)
}

// ----------------- server 侧 -----------------

func (s *server) formOverride(ip string) *FormOverride {
	if s.dc == nil {
		return nil
	}
	if f, ok := s.dc.Form(ip); ok {
		return &f
	}
	return nil
}

func (s *server) handleDatacenterGet(w http.ResponseWriter, r *http.Request) {
	var layout Datacenter
	if s.dc != nil {
		layout = s.dc.Snapshot()
	} else {
		st := &DatacenterStore{}
		st.normalizeLocked()
		layout = st.data
	}
	s.writeJSON(w, 200, map[string]interface{}{"layout": layout, "devices": s.panelDevices(), "kinds": dcItemKinds})
}

func (s *server) handleDatacenterSave(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Layout *Datacenter `json:"layout"`
		// 只改一台设备的形态
		FormIP string        `json:"form_ip"`
		Form   *FormOverride `json:"form"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if s.dc == nil {
		s.fail(w, 500, "机房布局未初始化")
		return
	}
	var err error
	switch {
	case req.Layout != nil:
		err = s.dc.Save(*req.Layout, sess.user)
	case req.FormIP != "" && req.Form != nil:
		err = s.dc.SetForm(req.FormIP, *req.Form, sess.user)
	default:
		err = errors.New("请求里没有要保存的内容")
	}
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "layout": s.dc.Snapshot()})
}
