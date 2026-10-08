package main

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ----------------- Syslog 接收 -----------------
//
// "SNMP 能不能看到交换机的 log" —— 老实说, 不能, 至少锐捷/华三/华为这
// 三家都没有一张可靠、公开、能直接 SNMP walk 出历史日志的 MIB 表(日志
// 存在本地 flash/buffer 里, 不是设计给 SNMP 轮询读的)。真正能拿到日志
// 的办法是让交换机把日志"推"过来 —— 也就是配置 `info-center loghost`
// (华为/H3C) 或 `logging host`(锐捷)指向这台服务器, 用标准 syslog 协议
// (UDP 514, RFC3164 格式为主)接收。
//
// 这里实现一个极简的 syslog 接收器: 监听一个 UDP 端口, 按来源 IP 分类,
// 每台设备只保留最近 N 条(有界, 不会无限吃内存), 且默认只接受来自
// "已配置监控"的设备 IP —— 避免网络里随便什么设备都能把日志灌进来占用
// 内存/构成信息泄露面。

type LogLine struct {
	Time     string `json:"time"`     // 收到时的本地时间
	Facility int    `json:"facility"` // RFC3164 PRI 拆出来的 facility
	Severity int    `json:"severity"` // 0=emerg ... 7=debug
	Raw      string `json:"raw"`      // 原始文本(去掉 PRI 前缀), 清洗过控制字符
	receivedAt int64 // 毫秒时间戳仅用于跨设备排序，不暴露给 API
}

const syslogRingSizeDefault = 300 // 每台设备最多保留这么多条, 超过滚掉最旧的; 可在系统设置里改, 立即生效

type syslogRing struct {
	mu    sync.RWMutex
	lines map[string][]LogLine // ip -> 环形保留的日志(简单用切片+裁剪实现)
	cap   atomic.Int64         // 每台设备保留条数上限, 见 (*syslogRing).setCap
}

func newSyslogRing() *syslogRing {
	r := &syslogRing{lines: make(map[string][]LogLine)}
	r.cap.Store(syslogRingSizeDefault)
	return r
}

// setCap 从系统设置改保留条数, 立即对之后新收到的日志生效(已存的不会
// 被截断到新上限以下, 除非之后又有新日志把旧的挤出去)。
func (r *syslogRing) setCap(n int) {
	if n <= 0 {
		n = syslogRingSizeDefault
	}
	r.cap.Store(int64(n))
}

func (r *syslogRing) add(ip string, line LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := append(r.lines[ip], line)
	if n := int(r.cap.Load()); n > 0 && len(list) > n {
		list = list[len(list)-n:]
	}
	r.lines[ip] = list
}

func (r *syslogRing) get(ip string) []LogLine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := r.lines[ip]
	out := make([]LogLine, len(list))
	copy(out, list)
	return out
}

// all 返回当前保留日志的快照，供平台级事件中心汇总展示。
func (r *syslogRing) all() map[string][]LogLine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]LogLine, len(r.lines))
	for ip, list := range r.lines {
		cp := make([]LogLine, len(list))
		copy(cp, list)
		out[ip] = cp
	}
	return out
}

func (r *syslogRing) drop(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lines, ip)
}

// clearAll: 系统设置「清理工具」用, 清空所有设备的 syslog 缓存。
func (r *syslogRing) clearAll() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, list := range r.lines {
		n += len(list)
	}
	r.lines = make(map[string][]LogLine)
	return n
}

var priRe = regexp.MustCompile(`^<(\d{1,3})>`)

func parseSyslogLine(raw []byte) LogLine {
	text := decodeText(string(raw))
	facility, severity := 23, 6 // 缺省当 local7.info, 不影响展示
	if m := priRe.FindStringSubmatch(text); len(m) == 2 {
		var pri int
		fmt.Sscanf(m[1], "%d", &pri)
		if pri >= 0 && pri < 192 {
			facility = pri / 8
			severity = pri % 8
		}
		text = text[len(m[0]):]
	}
	// RFC3164 常见前缀是一段时间戳+主机名, 这里不强行解析(各厂商格式差异大),
	// 只做清洗、不重排, 保留原始可读性。
	text = strings.TrimSpace(text)
	if len(text) > 2000 { // 防御性截断, 避免单条异常长的消息占太多内存
		text = text[:2000] + "…(截断)"
	}
	now := time.Now()
	return LogLine{
		Time: now.Format("2006-01-02 15:04:05"), Facility: facility, Severity: severity, Raw: text, receivedAt: now.UnixMilli(),
	}
}

// startSyslogServer 监听 UDP。source 为空表示接受任何来源(不建议在公网
// 暴露); allow 非空时只接受列表内 IP 的日志, 其余静默丢弃。
func startSyslogServer(port int, ring *syslogRing, allow func(ip string) bool) {
	if port <= 0 {
		return
	}
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Printf("⚠️  Syslog 接收未启动 (监听 UDP %d 失败: %v; 1024 以下端口通常需要 root 或 setcap): %v\n", port, err, err)
		return
	}
	fmt.Printf("📝 Syslog 接收已开启: UDP %d (交换机上执行 loghost/logging host 指向这台服务器即可采集日志)\n", port)

	buf := make([]byte, 8192)
	go func() {
		defer conn.Close()
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				// 单次读取失败不该杀死整个接收器(比如收到畸形包), 继续循环。
				time.Sleep(50 * time.Millisecond)
				continue
			}
			ip := src.IP.String()
			if allow != nil && !allow(ip) {
				continue
			}
			line := parseSyslogLine(buf[:n])
			ring.add(ip, line)
		}
	}()
}
