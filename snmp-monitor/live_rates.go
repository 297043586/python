package main

// 端口实时速率(v14)。
//
// 端口表里的速率/利用率是"两轮巡检之间计数器的差 ÷ 间隔", 巡检间隔一般 1~5 分钟,
// 看到的是这段时间的平均值: 刚开始拷大文件, 页面上要等下一轮才涨; 突发流量被摊平, 看着"不真实"。
//
// 这里给设备详情页加一个实时模式: 页面开着并且打开了「实时」开关时, 每隔几秒只 walk 这台
// 设备的两张计数器表(ifHCInOctets / ifHCOutOctets), 跟上一次取的值相减算速率。
//   - 基线单独存(liveLast), 不碰巡检用的 portHistory, 两边互不干扰;
//   - 同一台设备 liveMinGap 内的重复请求直接返回上次算好的结果(多个人同时看也只轮询一份);
//   - 要求登录: 公开页面不能让任何人都能让服务器去戳交换机。

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const liveMinGap = 2 * time.Second

type liveSample struct {
	in, out     map[string]uint64
	inAt, outAt int64 // Unix 毫秒, 两张表各自读到的时刻
	hc          bool
	result      *livePayload
	fetchedAt   time.Time
}

type livePort struct {
	Index   string  `json:"index"`
	Name    string  `json:"name"`
	InMbps  float64 `json:"in_mbps"`
	OutMbps float64 `json:"out_mbps"`
	InPct   float64 `json:"in_pct"`
	OutPct  float64 `json:"out_pct"`
}

type livePayload struct {
	IP       string     `json:"ip"`
	Warming  bool       `json:"warming,omitempty"` // 第一次取样, 还没有速率
	Interval float64    `json:"interval_sec"`
	At       string     `json:"at"`
	Ports    []livePort `json:"ports"`
}

var (
	liveMu   sync.Mutex
	liveLast = map[string]*liveSample{}
	liveBusy = map[string]*sync.Mutex{} // 同一台设备同时只跑一个取样
)

// computeLiveRates 用两次取样算每个口的速率; speed 是端口速率(Mbps, 来自最近一轮巡检)。
func computeLiveRates(prev, cur *liveSample, names map[string]string, speed map[string]int64) []livePort {
	out := make([]livePort, 0, len(names))
	dtIn := float64(cur.inAt-prev.inAt) / 1000
	dtOut := float64(cur.outAt-prev.outAt) / 1000
	for idx, name := range names {
		p := livePort{Index: idx, Name: name}
		if a, ok := prev.in[idx]; ok && dtIn > 0 {
			if b, ok := cur.in[idx]; ok {
				p.InMbps = float64(counterDelta(b, a, !cur.hc)) * 8 / dtIn / 1e6
			}
		}
		if a, ok := prev.out[idx]; ok && dtOut > 0 {
			if b, ok := cur.out[idx]; ok {
				p.OutMbps = float64(counterDelta(b, a, !cur.hc)) * 8 / dtOut / 1e6
			}
		}
		if sp := speed[idx]; sp > 0 {
			p.InPct = minF(100, p.InMbps/float64(sp)*100)
			p.OutPct = minF(100, p.OutMbps/float64(sp)*100)
		}
		out = append(out, p)
	}
	return out
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func (s *server) liveRates(ip string) (*livePayload, error) {
	var target DeviceTarget
	found := false
	for _, d := range s.devices.Active() {
		if d.IP == ip {
			target, found = d, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("设备不存在或已暂停巡检")
	}
	if target.Collect.SkipRates {
		return nil, fmt.Errorf("这台设备在「设备管理」里关闭了端口速率采集")
	}
	key := target.stateKey()

	liveMu.Lock()
	busy := liveBusy[key]
	if busy == nil {
		busy = &sync.Mutex{}
		liveBusy[key] = busy
	}
	liveMu.Unlock()
	busy.Lock()
	defer busy.Unlock()

	liveMu.Lock()
	prev := liveLast[key]
	liveMu.Unlock()
	if prev != nil && prev.result != nil && time.Since(prev.fetchedAt) < liveMinGap {
		return prev.result, nil
	}

	// 端口名 / 速率用最近一轮巡检的结果(只要物理口, 跟端口表一致)
	names, speed := map[string]string{}, map[string]int64{}
	s.hub.mu.RLock()
	if i, ok := s.hub.byIP[ip]; ok && i < len(s.hub.reports) {
		for _, p := range s.hub.reports[i].Ports {
			names[p.Index] = p.Name
			speed[p.Index] = p.SpeedMbps
		}
	}
	s.hub.mu.RUnlock()
	if len(names) == 0 {
		return nil, fmt.Errorf("还没有这台设备的端口数据(等第一轮巡检跑完)")
	}

	pool, err := newSessionPool(target, s.conf(), 2)
	if err != nil {
		return nil, fmt.Errorf("无法建立 SNMP 会话: %v", err)
	}
	defer pool.close()
	cur := &liveSample{hc: true, fetchedAt: time.Now()}
	pool.run(
		func(ss *snmpSession) { cur.inAt = timedWalk(func() { cur.in = ss.walkUints(oidIfHCInOctets) }) },
		func(ss *snmpSession) { cur.outAt = timedWalk(func() { cur.out = ss.walkUints(oidIfHCOutOcts) }) },
	)
	if len(cur.in) == 0 {
		cur.hc = false
		pool.run(
			func(ss *snmpSession) { cur.inAt = timedWalk(func() { cur.in = ss.walkUints(oidIfInOctets32) }) },
			func(ss *snmpSession) { cur.outAt = timedWalk(func() { cur.out = ss.walkUints(oidIfOutOctets32) }) },
		)
	}
	if len(cur.in) == 0 && len(cur.out) == 0 {
		return nil, fmt.Errorf("SNMP 没有返回端口计数器(设备不可达或超时)")
	}

	res := &livePayload{IP: ip, At: cur.fetchedAt.Format("15:04:05")}
	if prev == nil || prev.hc != cur.hc || time.Since(prev.fetchedAt) > 2*time.Minute {
		res.Warming = true
	} else {
		res.Interval = float64(cur.inAt-prev.inAt) / 1000
		res.Ports = computeLiveRates(prev, cur, names, speed)
	}
	cur.result = res
	liveMu.Lock()
	liveLast[key] = cur
	liveMu.Unlock()
	return res, nil
}

// pruneLive: 设备删掉 / 很久没人看的取样丢掉(巡检每轮调一次)。
func pruneLive() {
	liveMu.Lock()
	defer liveMu.Unlock()
	for k, v := range liveLast {
		if time.Since(v.fetchedAt) > 10*time.Minute {
			delete(liveLast, k)
			delete(liveBusy, k)
		}
	}
}

func (s *server) handleDeviceLive(w http.ResponseWriter, r *http.Request, sess *session) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		s.fail(w, http.StatusBadRequest, "缺少 ip 参数")
		return
	}
	res, err := s.liveRates(ip)
	if err != nil {
		s.fail(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, 200, res)
}
