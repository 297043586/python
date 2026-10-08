package main

// 机柜 / 面板视图的后端: 每台设备放在哪个机柜、从第几 U 开始(管理员在页面上
// 调整, 存 data/racks.json), 以及给机柜视图用的精简端口数据 /api/panels。
// 没有手工分配过的设备按 sysLocation 自动归到同名机柜, 没有位置信息的归到
// "未分配机柜", 在机柜里从上往下依次排。

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type RackSlot struct {
	Rack string `json:"rack"`
	U    int    `json:"u"` // 设备最上面一台所在的 U 号(1~60), 0 = 自动排
}

type RackStore struct {
	mu    sync.RWMutex
	path  string
	slots map[string]RackSlot // 设备 IP -> 位置
}

func newRackStore(dir string) *RackStore {
	return &RackStore{path: filepath.Join(dir, "racks.json"), slots: map[string]RackSlot{}}
}

func (r *RackStore) load() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := readJSON(r.path, &r.slots); err != nil && !os.IsNotExist(err) {
		return err
	}
	if r.slots == nil {
		r.slots = map[string]RackSlot{}
	}
	return nil
}

func (r *RackStore) Snapshot() map[string]RackSlot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]RackSlot, len(r.slots))
	for k, v := range r.slots {
		out[k] = v
	}
	return out
}

func (r *RackStore) Set(ip string, s RackSlot) error {
	s.Rack = strings.TrimSpace(s.Rack)
	if utf8.RuneCountInString(s.Rack) > 40 {
		return errors.New("机柜名太长(最多 40 个字)")
	}
	if s.U < 0 || s.U > 60 {
		return errors.New("U 位需要在 0~60 之间(0 = 自动排)")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Rack == "" && s.U == 0 {
		delete(r.slots, ip)
	} else {
		r.slots[ip] = s
	}
	return atomicWriteJSON(r.path, r.slots, 0o600)
}

// panelPort 是面板视图需要的端口字段(比 PortDetail 精简, 全网几十台设备一起传也不大)。
type panelPort struct {
	Index   string   `json:"i"`
	Name    string   `json:"n"`
	Alias   string   `json:"al,omitempty"`
	Status  string   `json:"s"`
	Speed   int64    `json:"sp"`
	InU     float64  `json:"iu"`
	OutU    float64  `json:"ou"`
	InM     float64  `json:"im"`
	OutM    float64  `json:"om"`
	Err     int64    `json:"e,omitempty"`
	Rx      *float64 `json:"rx,omitempty"`
	Tx      *float64 `json:"tx,omitempty"`
	Duplex  string   `json:"d,omitempty"`
	Agg     string   `json:"ag,omitempty"`
	Uplink  bool     `json:"up,omitempty"`
	Peer    string   `json:"pe,omitempty"`
	PeerP   string   `json:"pp,omitempty"`
	MACs    int      `json:"m,omitempty"`
	Vlan    string   `json:"v,omitempty"`
	Disc    int64    `json:"dc,omitempty"`
	Flapped bool     `json:"fl,omitempty"`
}

type panelDevice struct {
	IP       string      `json:"ip"`
	Name     string      `json:"name"`
	Note     string      `json:"note,omitempty"` // 设备管理里填的备注, 面板上画成标签贴
	Brand    string      `json:"brand"`
	Model    string      `json:"model,omitempty"`
	Location string      `json:"location,omitempty"`
	Alive    bool        `json:"alive"`
	Alerts   int         `json:"alerts"`
	CPU      int         `json:"cpu"`
	Mem      int         `json:"mem"`
	Power    string      `json:"power"`
	Fan      string      `json:"fan"`
	Temp     string      `json:"temp"`
	Rack     string      `json:"rack"`
	U        int         `json:"u"`
	Manual   bool        `json:"manual"`
	Form     DeviceForm  `json:"form"`
	Ports    []panelPort `json:"ports"`
}

func toPanelPort(p PortDetail) panelPort {
	pp := panelPort{Index: p.Index, Name: p.Name, Alias: p.Alias, Status: p.Status, Speed: p.SpeedMbps,
		InU: p.InUsagePercent, OutU: p.OutUsagePercent, InM: p.InSpeedMbps, OutM: p.OutSpeedMbps, Err: p.DeltaErrors,
		Rx: p.OpticalRxDbm, Tx: p.OpticalTxDbm, Duplex: p.Duplex, Agg: p.AggGroup, Uplink: p.IsUplink, MACs: p.MACCount,
		Vlan: portVlanText(p)}
	if p.LLDPRemoteDev != "" && p.LLDPRemoteDev != "-" {
		pp.Peer, pp.PeerP = p.LLDPRemoteDev, p.LLDPRemotePort
	}
	if p.Ext != nil {
		pp.Disc = p.Ext.DeltaInDiscard + p.Ext.DeltaOutDiscrd + p.Ext.DeltaOutErrors
		pp.Flapped = p.Ext.LastChangeSec >= 0 && p.Ext.LastChangeSec < 600
	}
	return pp
}

func (s *server) handlePanels(w http.ResponseWriter, r *http.Request) {
	devs := s.panelDevices()
	// ?ip=x 只要一台(设备详情里的面板页用, 取形态 / 板卡方向)
	if ip := r.URL.Query().Get("ip"); ip != "" {
		one := []panelDevice{}
		for _, d := range devs {
			if d.IP == ip {
				one = append(one, d)
			}
		}
		devs = one
	}
	s.writeJSON(w, 200, map[string]interface{}{"devices": devs})
}

// panelDevices 是机柜视图 / 3D 机房共用的设备列表(精简端口 + 位置 + 形态)。
func (s *server) panelDevices() []panelDevice {
	s.hub.mu.RLock()
	reports := s.hub.reports
	s.hub.mu.RUnlock()
	slots := map[string]RackSlot{}
	if s.racks != nil {
		slots = s.racks.Snapshot()
	}
	out := make([]panelDevice, 0, len(reports))
	for _, rep := range reports {
		d := panelDevice{IP: rep.IP, Name: rep.SysName, Note: rep.Note, Brand: rep.Brand, Alive: rep.IsAlive, CPU: rep.CPUUsage, Mem: rep.MemUsage,
			Power: rep.Hardware.PowerStatus, Fan: rep.Hardware.FanStatus, Temp: rep.Hardware.Temperature, Alerts: rep.OpenAlerts,
			Form: detectDeviceForm(rep, s.formOverride(rep.IP))}
		if d.Name == "" {
			d.Name = rep.IP
		}
		if rep.Inventory != nil {
			d.Model, d.Location = rep.Inventory.Model, rep.Inventory.Location
		}
		if sl, ok := slots[rep.IP]; ok && sl.Rack != "" {
			d.Rack, d.U, d.Manual = sl.Rack, sl.U, true
		} else if d.Location != "" {
			d.Rack = d.Location
		} else {
			d.Rack = "未分配机柜"
		}
		d.Ports = make([]panelPort, 0, len(rep.Ports))
		for _, p := range rep.Ports {
			d.Ports = append(d.Ports, toPanelPort(p))
		}
		out = append(out, d)
	}
	return out
}

func (s *server) handleRackAssign(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		IP   string `json:"ip"`
		Rack string `json:"rack"`
		U    int    `json:"u"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	known := false
	for _, d := range s.devices.List() {
		if d.IP == req.IP {
			known = true
			break
		}
	}
	if !known {
		s.fail(w, 404, "设备不存在")
		return
	}
	if err := s.racks.Set(req.IP, RackSlot{Rack: req.Rack, U: req.U}); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true})
}
