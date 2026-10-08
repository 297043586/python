package main

// SNMP 会话封装(按 OID 前缀切索引的 walk)和单台设备内部的并行会话池。

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
)

// ----------------- SNMP 会话封装 -----------------

type snmpSession struct {
	c      *gosnmp.GoSNMP
	noBulk atomic.Bool
}

// walk 统一按 "OID 前缀" 切出索引后缀。
// 旧代码用 strings.Split(pdu.Name, ".") 取最后一段当索引, 对
// lldpRemTable(timeMark.localPort.remIndex)、dot1qTpFdbTable(vlan.mac)、
// 锐捷光模块通道表(ifIndex.channel) 这类多级索引统统取错。
// walkItemCap 是单次 walk 的防御性上限。
// 正常情况下用不到(一台设备的接口/MAC 表再大也到不了这个数量级); 这层
// 纯粹是为了防止一个视图配置错乱、指针 OID 判断失效的设备把 walk 拖进
// 事实上的死循环, 一直吃内存吃到 OOM。触发时记一条诊断, 而不是让进程
// 硬扛到被系统杀掉。
const walkItemCap = 200000

func (s *snmpSession) walk(base string, fn func(idx string, pdu gosnmp.SnmpPDU)) error {
	base = "." + strings.Trim(base, ".")
	prefix := base + "."
	n := 0
	handler := func(pdu gosnmp.SnmpPDU) error {
		switch pdu.Type {
		case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
			return nil
		}
		name := pdu.Name
		if !strings.HasPrefix(name, ".") {
			name = "." + name
		}
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		n++
		if n > walkItemCap {
			return fmt.Errorf("walk 条目超过防御上限 %d, 已中止: %s", walkItemCap, base)
		}
		fn(strings.TrimPrefix(name, prefix), pdu)
		return nil
	}
	if s.c.Version == gosnmp.Version1 || s.noBulk.Load() {
		return s.c.Walk(base, handler)
	}
	err := s.c.BulkWalk(base, handler)
	if err != nil && n == 0 && !strings.Contains(err.Error(), "防御上限") {
		// 有些老设备号称支持 v2c, 但对 GETBULK 回错误包 / 超时 / 直接不理。
		// 这张表用普通 GETNEXT 再走一遍, 成功了就记下来, 以后这台设备都不用 GETBULK。
		if err2 := s.c.Walk(base, handler); err2 == nil && n > 0 {
			s.noBulk.Store(true)
			noBulkHosts.Store(s.c.Target, true)
			return nil
		}
	}
	return err
}

// noBulkHosts 记住 GETBULK 不好使的设备, 下一轮新建会话时直接用 GETNEXT, 不用每轮先超时一次。
var noBulkHosts sync.Map

func (s *snmpSession) walkStrings(base string) map[string]string {
	out := make(map[string]string)
	_ = s.walk(base, func(idx string, pdu gosnmp.SnmpPDU) {
		out[idx] = decodeText(pdu.Value)
	})
	return out
}

func (s *snmpSession) walkInts(base string) map[string]int64 {
	out := make(map[string]int64)
	_ = s.walk(base, func(idx string, pdu gosnmp.SnmpPDU) {
		out[idx] = toInt64(pdu.Value)
	})
	return out
}

func (s *snmpSession) walkUints(base string) map[string]uint64 {
	out := make(map[string]uint64)
	_ = s.walk(base, func(idx string, pdu gosnmp.SnmpPDU) {
		out[idx] = toUint64(pdu.Value)
	})
	return out
}

func (s *snmpSession) get(oid string) (gosnmp.SnmpPDU, bool) {
	res, err := s.c.Get([]string{oid})
	if err != nil || res == nil || len(res.Variables) == 0 {
		return gosnmp.SnmpPDU{}, false
	}
	v := res.Variables[0]
	switch v.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return gosnmp.SnmpPDU{}, false
	}
	if v.Value == nil {
		return gosnmp.SnmpPDU{}, false
	}
	return v, true
}

func (s *snmpSession) getString(oid string) string {
	if pdu, ok := s.get(oid); ok {
		return decodeText(pdu.Value)
	}
	return ""
}

// probeMetric 依次尝试候选 OID, 取表内落在 [lo,hi] 的最大值。
// 取最大值是有意为之: 华为/H3C 的 entity 表会给每个实体都返回一行,
// 非 CPU 实体返回 0, 旧版 "取第一个非零" 的写法会挑到从板甚至 0。
func (s *snmpSession) probeMetric(oids []string, lo, hi int64) (int, bool) {
	for _, oid := range oids {
		var best int64 = -1
		if pdu, ok := s.get(oid); ok {
			v := toInt64(pdu.Value)
			if v >= lo && v <= hi {
				best = v
			}
		}
		if best < 0 {
			for _, v := range s.walkInts(oid) {
				if v >= lo && v <= hi && v > best {
					best = v
				}
			}
		}
		if best >= 0 {
			return int(best), true
		}
	}
	return 0, false
}

// ----------------- 会话池 (单台设备内部并行) -----------------
//
// gosnmp 的一个会话只能串行使用, 而一台 48 口交换机要走 20 多张表。
// 旧版把这些 walk 一个接一个地排队, 加上每台设备还要重试, 首轮全网采集
// 动辄几分钟。这里给每台设备开 N 条独立的 UDP 会话并行取表。
type sessionPool struct {
	idle chan *snmpSession
	all  []*snmpSession
}

func newSessionPool(target DeviceTarget, cfg *Config, n int) (*sessionPool, error) {
	if n < 1 {
		n = 1
	}
	p := &sessionPool{idle: make(chan *snmpSession, n)}
	for i := 0; i < n; i++ {
		s, err := dialSNMP(target, cfg)
		if err != nil {
			p.close()
			return nil, err
		}
		p.all = append(p.all, s)
		p.idle <- s
	}
	return p, nil
}

func dialSNMP(target DeviceTarget, cfg *Config) (*snmpSession, error) {
	version := gosnmp.Version2c
	if target.Version == "1" {
		version = gosnmp.Version1
	}
	c := &gosnmp.GoSNMP{
		Target:         target.IP,
		Port:           uint16(cfg.SNMPPort),
		Community:      target.Community,
		Version:        version,
		Timeout:        time.Duration(cfg.TimeoutSeconds) * time.Second,
		Retries:        cfg.Retries,
		MaxOids:        gosnmp.MaxOids,
		MaxRepetitions: uint32(cfg.MaxRepetitions),
		// 每次重试把超时窗口翻倍, 而不是原地重复同一个大概率还会超时的请求。
		// 这是应对"设备偶尔卡一下但本身可达"这种超时的最简单有效的手段。
		ExponentialTimeout: true,
	}
	if err := c.Connect(); err != nil {
		return nil, err
	}
	ss := &snmpSession{c: c}
	if _, ok := noBulkHosts.Load(target.IP); ok {
		ss.noBulk.Store(true)
	}
	return ss, nil
}

// primary 返回池里的第一条会话, 供串行阶段使用。
func (p *sessionPool) primary() *snmpSession { return p.all[0] }

// run 把若干个互不依赖的采集任务分发到池里并行执行。
func (p *sessionPool) run(fns ...func(*snmpSession)) {
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func(f func(*snmpSession), stagger int) {
			defer wg.Done()
			// 错开各协程发第一个 GETBULK 的时刻。同一时间戳发出的 N 路
			// 大批量请求会在弱 CPU 的老交换机上叠成一次瞬时峰值, 表现
			// 就是"平时没事, 偶尔集中超时"。错开几毫秒几乎不影响总耗时,
			// 却能显著缓解这种拥塞。
			if stagger > 0 {
				time.Sleep(time.Duration(stagger) * 15 * time.Millisecond)
			}
			s := <-p.idle
			defer func() { p.idle <- s }()
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("⚠️  采集任务异常: %v\n", r)
				}
			}()
			f(s)
		}(fn, i)
	}
	wg.Wait()
}

func (p *sessionPool) close() {
	for _, s := range p.all {
		if s != nil && s.c != nil && s.c.Conn != nil {
			_ = s.c.Conn.Close()
		}
	}
}
