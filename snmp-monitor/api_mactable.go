package main

// 全网 MAC 总表。
//
// 之前把 MAC 总表放在单台设备的详情窗口里, 但一台设备的 MAC 表本来就
// 是"不全"的——同一个终端可能出现在好几台接入交换机的转发表里(尤其是
// 有冗余链路的时候), 而且真正常见的用法是"我有这个 MAC/IP, 帮我在全网
// 找出它接在哪台设备的哪个口", 不是先猜它在哪台设备上再点进去看。
// 所以单独做一个跨设备的全网视图, 按需拉取(不进 /api/data 的常规轮询)。
//
// v13: 一两万条的时候"打开很卡", 慢在三处, 分别处理:
//   - 每次打开都把全部设备的 MAC 表重新汇总去重一遍 → 按巡检轮次缓存, 一轮只算一次;
//   - 每条记录是一个带长字段名的 JSON 对象, 两万条就是好几 MB → ?fmt=compact 列式输出,
//     设备名只出现一次, 体积减一半以上(再加 gzip);
//   - 前端一次把两万行塞进 DOM → 前端改成虚拟滚动, 只画看得见的几十行(见 web/js/mac/)。

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

type globalMACEntry struct {
	DeviceIP   string   `json:"device_ip"`
	DeviceName string   `json:"device_name"`
	MAC        string   `json:"mac"`
	HostIP     string   `json:"host_ip,omitempty"` // 终端自己的 IP(从全网 ARP/host_mappings 关联出来的, 不一定有)
	VlanID     string   `json:"vlan_id"`
	Port       string   `json:"port"`
	PortKind   string   `json:"port_kind"`
	IsUplink   bool     `json:"is_uplink"`
	Ambiguous  bool     `json:"ambiguous"`
	Locations  []string `json:"locations"`

	// TransitCount>0 表示这个 MAC 在全网被去重掉的"只是路过"的记录数——
	// 同一个终端会被沿途每一台交换机的上联口学到, 单看总表会像是"这个
	// MAC 接在好几十台设备上", 见 globalMACEntries 里的去重说明。
	TransitCount int             `json:"transit_count,omitempty"`
	TransitVia   []macTransitHop `json:"transit_via,omitempty"`

	// CrossDeviceFlap: 同一个 MAC 同时在两台不同设备的接入口上出现——
	// 二层环路/终端漂移的直接信号, 见 globalMACEntries 里的说明。
	CrossDeviceFlap bool `json:"cross_device_flap,omitempty"`

	// OwnerDevice: 这个 MAC 是某台被监控设备自己的(桥 MAC / 三层口 MAC), 值是设备名。
	OwnerDevice string `json:"owner_device,omitempty"`
}

type macTransitHop struct {
	DeviceIP   string `json:"device_ip"`
	DeviceName string `json:"device_name"`
	Port       string `json:"port"`
}

// globalMACEntries 汇总全网 MAC 表并去重。
//
// 一个终端的 MAC 会被沿途每一台交换机学到(核心的下联口、汇聚的下联口、接入交换机的接入口),
// 总表只留它真正接入的那一条, 其余算"路过"。判断顺序:
//  1. 这个口朝向另一台被监控设备(LLDP 邻居 / 聚合成员口的邻居 / 学到了那台设备自己的 MAC),
//     而那台设备也学到了这个 MAC —— 这条是路过, 真正的位置在下游;
//  2. 剩下的里面有接入口就只留接入口; 分布在多台设备上时, 层级更深(更靠近终端)的设备优先,
//     同一层级还分不开才算"跨设备漂移";
//  3. 全都是上联口(下游设备没采到 MAC 表): 挑层级最深的那台, 而不是按 IP 挑(以前经常挑中核心)。
func globalMACEntries(reports []DeviceReport) []globalMACEntry {
	return globalMACEntriesCtx(reports, buildNetCtx(reports))
}

func globalMACEntriesCtx(reports []DeviceReport, nc *netCtx) []globalMACEntry {
	// 全网 ARP/host_mappings 汇总出 MAC -> 终端 IP 的映射, 用来给 MAC 总表
	// 补上一列"这个 MAC 到底是谁"——单看 MAC 表本身只知道端口, 不知道 IP。
	macToIP := make(map[string]string, 1024)
	for _, rep := range reports {
		for _, h := range rep.HostMappings {
			if h.MAC == "" || h.IP == "" {
				continue
			}
			if _, exists := macToIP[h.MAC]; !exists {
				macToIP[h.MAC] = h.IP
			}
		}
	}

	byMAC := make(map[string][]globalMACEntry, 1024)
	for _, rep := range reports {
		for _, m := range rep.MACTable {
			e := globalMACEntry{
				DeviceIP: rep.IP, DeviceName: rep.SysName, MAC: m.MAC, HostIP: macToIP[m.MAC], VlanID: m.VlanID,
				Port: m.Port, PortKind: m.PortKind, IsUplink: m.IsUplink,
				Ambiguous: m.Ambiguous, Locations: m.Locations,
			}
			if !e.IsUplink && len(nc.faces(rep.IP, m.Port)) > 0 {
				// 本机按 MAC 数没认出来的级联口(常见: 聚合口、下面终端不多的接入交换机)
				e.IsUplink, e.PortKind = true, "上联/级联口"
			}
			byMAC[m.MAC] = append(byMAC[m.MAC], e)
		}
	}

	depth := func(ip string) int {
		if d, ok := nc.depthOf(ip); ok {
			return d
		}
		return -1
	}
	out := make([]globalMACEntry, 0, len(byMAC))
	for mac, list := range byMAC {
		owner := nc.ownerOfMAC(mac)
		seenOn := map[string]bool{}
		for _, e := range list {
			seenOn[e.DeviceIP] = true
		}
		// 1. 朝向"也学到了这个 MAC 的下游设备"的口 = 路过
		var keep, transit []globalMACEntry
		for _, e := range list {
			pass := false
			for peer := range nc.faces(e.DeviceIP, e.Port) {
				if seenOn[peer] || peer == owner {
					pass = true
					break
				}
			}
			if pass {
				transit = append(transit, e)
			} else {
				keep = append(keep, e)
			}
		}
		if len(keep) == 0 { // 互相朝向(环): 退回全部
			keep, transit = list, nil
		}

		access := make([]globalMACEntry, 0, len(keep))
		var up []globalMACEntry
		for _, e := range keep {
			if e.IsUplink {
				up = append(up, e)
			} else {
				access = append(access, e)
			}
		}
		var chosen []globalMACEntry
		if len(access) > 0 {
			transit = append(transit, up...)
			// 2. 多台设备都当成接入口: 层级更深的优先
			maxD := -1
			for _, e := range access {
				if d := depth(e.DeviceIP); d > maxD {
					maxD = d
				}
			}
			for _, e := range access {
				if d := depth(e.DeviceIP); maxD >= 0 && d >= 0 && d < maxD {
					transit = append(transit, e)
				} else {
					chosen = append(chosen, e)
				}
			}
			devs := map[string]bool{}
			for _, e := range chosen {
				devs[e.DeviceIP] = true
			}
			if len(devs) > 1 {
				for i := range chosen {
					chosen[i].CrossDeviceFlap = true
				}
			}
		} else {
			// 3. 全是上联口: 层级最深的一条当代表, 同层按 IP 稳定排序
			best := 0
			for i := 1; i < len(up); i++ {
				di, db := depth(up[i].DeviceIP), depth(up[best].DeviceIP)
				if di > db || (di == db && up[i].DeviceIP < up[best].DeviceIP) {
					best = i
				}
			}
			chosen = []globalMACEntry{up[best]}
			for i, e := range up {
				if i != best {
					transit = append(transit, e)
				}
			}
		}
		if len(transit) > 0 {
			via := make([]macTransitHop, 0, len(transit))
			for _, e := range transit {
				via = append(via, macTransitHop{DeviceIP: e.DeviceIP, DeviceName: e.DeviceName, Port: e.Port})
			}
			sort.Slice(via, func(i, j int) bool {
				di, dj := depth(via[i].DeviceIP), depth(via[j].DeviceIP)
				if di != dj {
					return di < dj
				}
				return via[i].DeviceName < via[j].DeviceName
			})
			for i := range chosen {
				chosen[i].TransitCount = len(transit) + 1
				chosen[i].TransitVia = via
			}
		}
		if owner != "" {
			for i := range chosen {
				chosen[i].OwnerDevice = nc.name(owner)
			}
		}
		out = append(out, chosen...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MAC != out[j].MAC {
			return out[i].MAC < out[j].MAC
		}
		return out[i].DeviceIP < out[j].DeviceIP
	})
	return out
}

// MAC 总表紧凑格式里每行的 flags 位
const (
	macFlagUplink    = 1
	macFlagAmbiguous = 2
	macFlagLocal     = 4 // 本地管理地址(第一个字节的 bit1): 手机 / 电脑的随机 MAC、虚拟机
	macFlagFlap      = 8 // 同一个 MAC 同时在两台不同设备的接入口上出现: 环路/漂移嫌疑
)

// macIsLocal: MAC 第一个字节的"本地管理"位, 手机/电脑的隐私随机 MAC 都会置位。
func macIsLocal(mac string) bool {
	h := strings.TrimLeft(strings.ToLower(mac), " ")
	if len(h) < 2 {
		return false
	}
	switch h[1] {
	case '2', '6', 'a', 'e':
		return true
	}
	return false
}

// compactMACTable 列式输出: devices 只出现一次, 每行是
// [mac, 设备下标, 终端IP, vlan, 端口, flags, 路过台数, 原始出现位置("a, b"), 路过明细, 所属网管设备]。
func compactMACTable(list []globalMACEntry) map[string]interface{} {
	devIdx := map[string]int{}
	devices := [][2]string{}
	rows := make([][]interface{}, 0, len(list))
	for _, e := range list {
		di, ok := devIdx[e.DeviceIP]
		if !ok {
			di = len(devices)
			devIdx[e.DeviceIP] = di
			devices = append(devices, [2]string{e.DeviceIP, e.DeviceName})
		}
		flags := 0
		if e.IsUplink {
			flags |= macFlagUplink
		}
		if e.Ambiguous {
			flags |= macFlagAmbiguous
		}
		if macIsLocal(e.MAC) {
			flags |= macFlagLocal
		}
		if e.CrossDeviceFlap {
			flags |= macFlagFlap
		}
		via := ""
		if len(e.TransitVia) > 0 {
			parts := make([]string, 0, len(e.TransitVia))
			for _, h := range e.TransitVia {
				name := h.DeviceName
				if name == "" {
					name = h.DeviceIP
				}
				parts = append(parts, name+" · "+h.Port)
			}
			via = strings.Join(parts, "\n")
		}
		rows = append(rows, []interface{}{e.MAC, di, e.HostIP, e.VlanID, e.Port, flags, e.TransitCount, strings.Join(e.Locations, ", "), via, e.OwnerDevice})
	}
	return map[string]interface{}{"fmt": "compact", "devices": devices, "rows": rows, "count": len(rows)}
}

func (s *server) handleGlobalMACTable(w http.ResponseWriter, r *http.Request) {
	compact := r.URL.Query().Get("fmt") == "compact"
	blob := s.hub.cached("mac:"+map[bool]string{true: "c", false: "f"}[compact], func(reports []DeviceReport) interface{} {
		list := globalMACEntries(reports)
		if compact {
			out := compactMACTable(list)
			out["check_time"] = s.hub.checkTimeLocked()
			return out
		}
		return map[string]interface{}{"entries": list, "count": len(list)}
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}

// cached 把按全部巡检结果算出来的 JSON 缓存到下一轮巡检为止(MAC 总表这类又大又慢的汇总)。
func (h *dataHub) cached(key string, build func([]DeviceReport) interface{}) []byte {
	h.mu.RLock()
	if b, ok := h.cache[key]; ok {
		h.mu.RUnlock()
		return b
	}
	reports, ver := h.reports, h.version
	h.mu.RUnlock()
	h.mu.RLock()
	v := build(reports)
	h.mu.RUnlock()
	blob, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	h.mu.Lock()
	if h.version == ver {
		if h.cache == nil {
			h.cache = map[string][]byte{}
		}
		h.cache[key] = blob
	}
	h.mu.Unlock()
	return blob
}

// checkTimeLocked 调用方已持有读锁。
func (h *dataHub) checkTimeLocked() string { return h.checkTime }
