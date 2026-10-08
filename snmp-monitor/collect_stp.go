package main

// 采集: 生成树拓扑变化(BRIDGE-MIB 两个标量, 各家都支持, 一次 Get 就够)。
//
// 二层环路 / 端口反复抖动时, 生成树会不停重算: dot1dStpTopChanges 计数器往上涨,
// dot1dStpTimeSinceTopologyChange 一直很小。两轮巡检之间涨了几次, 比看任何一张大表都
// 直接; 配合「🩺 二层体检」里多台设备的对比, 能看出是哪一片区域在震荡。

import (
	"github.com/gosnmp/gosnmp"
)

const (
	oidStpTimeSinceTopChange = ".1.3.6.1.2.1.17.2.3.0" // TimeTicks, 1/100 秒
	oidStpTopChanges         = ".1.3.6.1.2.1.17.2.4.0" // Counter32
)

// STPInfo 是设备生成树的拓扑变化情况; 设备没开 STP / 不支持 BRIDGE-MIB 时整个为空。
type STPInfo struct {
	TopChanges     uint64 `json:"top_changes"`      // 设备启动以来累计变化次数
	SinceChangeSec int64  `json:"since_change_sec"` // 距最近一次变化多少秒
	Delta          int64  `json:"delta"`            // 跟上一轮巡检相比新增的次数(第一轮 / 计数器被清零时为 0)
}

func collectSTP(s *snmpSession) *STPInfo {
	res, err := s.c.Get([]string{oidStpTimeSinceTopChange, oidStpTopChanges})
	if err != nil || res == nil {
		return nil
	}
	info := &STPInfo{}
	got := 0
	for _, v := range res.Variables {
		switch v.Type {
		case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
			continue
		}
		n := gosnmp.ToBigInt(v.Value).Uint64()
		switch "." + trimDot(v.Name) {
		case oidStpTimeSinceTopChange:
			info.SinceChangeSec = int64(n / 100)
			got++
		case oidStpTopChanges:
			info.TopChanges = n
			got++
		}
	}
	if got < 2 {
		return nil
	}
	return info
}

func trimDot(s string) string {
	for len(s) > 0 && s[0] == '.' {
		s = s[1:]
	}
	return s
}

var stpLast = map[string]uint64{} // stateKey -> 上一轮的 TopChanges, 受 stateLock 保护

// applySTPDelta 算出跟上一轮相比新增的拓扑变化次数。
func applySTPDelta(key string, info *STPInfo) {
	stateLock.Lock()
	defer stateLock.Unlock()
	if prev, ok := stpLast[key]; ok && info.TopChanges >= prev {
		info.Delta = int64(info.TopChanges - prev)
	}
	stpLast[key] = info.TopChanges
}
