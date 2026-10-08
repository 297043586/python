package main

// 配置下发(v14)。
//
// 以前要让交换机把 sFlow / syslog 推到本机, 只能一台台登录上去敲命令, 各家语法还不一样。
// 现在复用「配置备份」里保存的登录凭据和 SSH/Telnet 会话(backup.go: cliLogin / cliViaSSH):
//
//   1. 页面上选设备 + 填参数(采集器 IP/端口、抽样比、要开 sFlow 的端口...)
//   2. /api/push/preview 按每台设备的厂商生成命令, 页面上可以逐台查看、修改
//   3. /api/push/run 按修改后的命令逐台执行(并发 4), 返回任务号; 前端轮询 /api/push/job
//   4. 某条命令被设备拒绝(Error: / % Invalid input ...)就停在那一台, 不保存;
//      已经执行的命令已经进了运行配置, 结果里会写清楚, 让人去核对
//
// kind=show 是只读命令(拉设备本地日志缓冲区 display logbuffer / show logging),
// 不改配置, 只要求「查看配置备份」权限; 其余都要求「配置下发」权限。
// 每次执行都追加一行审计记录到 data/push_log.jsonl(谁、什么时候、对哪台、执行了什么、结果)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pushParams struct {
	Kind      string              `json:"kind"`      // sflow / syslog / netflow / show / custom
	Collector string              `json:"collector"` // 采集器 / 日志服务器 IP(一般就是本机)
	Port      int                 `json:"port"`
	Rate      int                 `json:"rate"`     // sFlow 抽样比 1:N
	Interval  int                 `json:"interval"` // sFlow 计数器上报间隔(秒)
	Level     string              `json:"level"`    // syslog 级别
	Ports     map[string][]string `json:"ports"`    // 设备 IP -> 要开 sFlow/NetFlow 的端口
	Custom    string              `json:"custom"`   // kind=custom 时的命令(每行一条)
	Save      bool                `json:"save"`     // 执行完保存配置
}

type pushPlan struct {
	DeviceID string   `json:"device_id"`
	IP       string   `json:"ip"`
	Name     string   `json:"name"`
	Vendor   string   `json:"vendor"`
	Commands []string `json:"commands"`
	Note     string   `json:"note,omitempty"`
	Error    string   `json:"error,omitempty"` // 生成不了(没选端口 / 厂商不支持)
	// NoCred: 没配登录凭据。命令照样生成(可以预览 / 复制去手工敲), 但不会下发。
	NoCred bool `json:"no_cred,omitempty"`
}

type pushResult struct {
	DeviceID   string `json:"device_id"`
	IP         string `json:"ip"`
	Name       string `json:"name"`
	State      string `json:"state"` // pending / running / ok / failed
	Error      string `json:"error,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Executed   int    `json:"executed"` // 成功执行了几条
	Total      int    `json:"total"`
}

type pushJob struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"`
	By      string       `json:"by"`
	Started string       `json:"started"`
	Done    bool         `json:"done"`
	Results []pushResult `json:"results"`
}

var validSyslogLevels = []string{"emergencies", "alerts", "critical", "errors", "warnings", "notifications", "informational", "debugging"}

// pushVendorFamily 把厂商归到四种命令风格之一; 自定义厂商按 Cisco 风格处理并提示。
func pushVendorFamily(v string) (family string, guessed bool) {
	switch strings.ToLower(v) {
	case "huawei", "h3c", "ruijie", "cisco":
		return strings.ToLower(v), false
	}
	return "cisco", true
}

// pushSaveCommands: 各家保存配置的命令(华为 save 会问 [Y/N], 执行时自动回答 y)。
func pushSaveCommands(family string) []string {
	switch family {
	case "huawei":
		return []string{"save"}
	case "h3c":
		return []string{"save force"}
	case "cisco":
		return []string{"write memory"}
	default:
		return []string{"write"}
	}
}

// pushCommands 按厂商生成一台设备要执行的命令。devIP 用作 sFlow agent 地址。
func pushCommands(vendor, devIP string, p pushParams, ports []string) ([]string, string, error) {
	fam, guessed := pushVendorFamily(vendor)
	note := ""
	if guessed {
		note = "厂商「" + vendor + "」没有内置模板, 按 Cisco 风格生成, 执行前请核对"
	}
	var c []string
	add := func(s ...string) { c = append(c, s...) }
	port := strconv.Itoa(p.Port)

	switch p.Kind {
	case "sflow":
		if len(ports) == 0 {
			return nil, note, errors.New("没有选择要开启 sFlow 的端口")
		}
		rate, iv := strconv.Itoa(p.Rate), strconv.Itoa(p.Interval)
		switch fam {
		case "huawei":
			add("system-view", "sflow agent ip "+devIP, "sflow collector 1 ip "+p.Collector+" udp-port "+port+" description netmon")
			for _, pt := range ports {
				add("interface "+pt, "sflow sampling collector 1", "sflow sampling rate "+rate,
					"sflow counter-sampling collector 1", "sflow counter-sampling interval "+iv, "quit")
			}
			add("return")
		case "h3c":
			add("system-view", "sflow agent ip "+devIP, "sflow collector 1 ip "+p.Collector+" port "+port+" description netmon")
			for _, pt := range ports {
				add("interface "+pt, "sflow sampling-rate "+rate, "sflow flow collector 1",
					"sflow counter interval "+iv, "sflow counter collector 1", "quit")
			}
			add("return")
		case "ruijie":
			add("configure terminal", "sflow agent ip "+devIP, "sflow collector 1 destination "+p.Collector+" "+port,
				"sflow sampling-rate "+rate, "sflow counter interval "+iv)
			for _, pt := range ports {
				add("interface "+pt, "sflow flow collector 1", "sflow counter collector 1", "sflow enable ingress", "exit")
			}
			add("end")
		default:
			note = strings.TrimSpace(note + " Cisco 只有 NX-OS 和部分 Catalyst 支持 sFlow(下面是 NX-OS 语法); IOS 交换机请改用 NetFlow")
			add("configure terminal", "feature sflow", "sflow agent-ip "+devIP, "sflow collector-ip "+p.Collector+" vrf default",
				"sflow collector-port "+port, "sflow sampling-rate "+rate, "sflow counter-poll-interval "+iv)
			for _, pt := range ports {
				add("sflow data-source interface " + pt)
			}
			add("end")
		}
	case "netflow":
		if fam != "cisco" {
			return nil, note, errors.New("NetFlow v5 模板只有 Cisco IOS 的; 华为/H3C/锐捷请用 sFlow")
		}
		if len(ports) == 0 {
			return nil, note, errors.New("没有选择要开启 NetFlow 的端口")
		}
		add("configure terminal", "ip flow-export version 5", "ip flow-export destination "+p.Collector+" "+port)
		for _, pt := range ports {
			add("interface "+pt, "ip flow ingress", "exit")
		}
		add("end")
	case "syslog":
		lvl := p.Level
		switch fam {
		case "huawei":
			host := "info-center loghost " + p.Collector
			if p.Port != 514 {
				host += " port " + port
			}
			add("system-view", "info-center enable", host, "return")
		case "h3c":
			add("system-view", "info-center enable", "info-center loghost "+p.Collector+" port "+port, "return")
		case "ruijie":
			host := "logging server " + p.Collector
			if p.Port != 514 {
				host += " udp-port " + port
			}
			add("configure terminal", "logging on", host, "logging trap "+lvl, "end")
		default:
			add("configure terminal", "logging host "+p.Collector+" transport udp port "+port, "logging trap "+lvl, "end")
		}
	case "show":
		switch fam {
		case "huawei":
			add("screen-length 0 temporary", "display logbuffer")
		case "h3c":
			add("screen-length disable", "display logbuffer")
		default:
			add("terminal length 0", "show logging")
		}
		return c, note, nil
	case "custom":
		for _, l := range strings.Split(p.Custom, "\n") {
			if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" {
				add(l)
			}
		}
		if len(c) == 0 {
			return nil, note, errors.New("没有填写要执行的命令")
		}
	default:
		return nil, note, errors.New("未知的下发类型: " + p.Kind)
	}
	if p.Save {
		add(pushSaveCommands(fam)...)
	}
	return c, note, nil
}

// normalizePushParams 补默认值并校验(采集器 IP 必须是合法 IPv4, 端口/抽样比有范围)。
func normalizePushParams(p *pushParams, cfg *Config) error {
	switch p.Kind {
	case "sflow", "netflow", "syslog":
		if net.ParseIP(strings.TrimSpace(p.Collector)).To4() == nil {
			return errors.New("采集器 / 日志服务器地址要填本机能被交换机访问到的 IPv4 地址")
		}
		p.Collector = strings.TrimSpace(p.Collector)
	}
	if p.Port <= 0 || p.Port > 65535 {
		switch p.Kind {
		case "sflow":
			p.Port = cfg.Collectors.SflowPort
			if p.Port <= 0 {
				p.Port = 6343
			}
		case "netflow":
			p.Port = cfg.Collectors.NetflowPort
			if p.Port <= 0 {
				p.Port = 2055
			}
		case "syslog":
			p.Port = cfg.Collectors.SyslogPort
			if p.Port <= 0 {
				p.Port = 514
			}
		}
	}
	if p.Rate < 64 || p.Rate > 1<<20 {
		p.Rate = 1000
	}
	if p.Interval < 5 || p.Interval > 3600 {
		p.Interval = 20
	}
	if !inList(p.Level, validSyslogLevels) {
		p.Level = "informational"
	}
	return nil
}

// pushTargets: 请求里的设备 ID → 设备 + 厂商(凭据里指定了厂商就用它, 否则用巡检识别出来的)。
func (s *server) pushVendor(dev DeviceTarget, cred BackupCred) string {
	v := cred.Vendor
	if v != "" && v != "auto" {
		return v
	}
	v = dev.Brand
	s.hub.mu.RLock()
	if i, ok := s.hub.byIP[dev.IP]; ok && i < len(s.hub.reports) && s.hub.reports[i].Brand != "" {
		v = s.hub.reports[i].Brand
	}
	s.hub.mu.RUnlock()
	return v
}

func (s *server) pushDevName(ip string) string {
	s.hub.mu.RLock()
	defer s.hub.mu.RUnlock()
	if i, ok := s.hub.byIP[ip]; ok && i < len(s.hub.reports) {
		return s.hub.reports[i].SysName
	}
	return ip
}

func (s *server) pushPlans(ids []string, p pushParams) []pushPlan {
	out := make([]pushPlan, 0, len(ids))
	for _, id := range ids {
		dev, ok := s.devices.Get(id)
		if !ok {
			// 设备详情窗口里只知道 IP, 也接受 IP
			for _, d := range s.devices.List() {
				if d.IP == id {
					dev, ok = d, true
					break
				}
			}
		}
		if !ok {
			continue
		}
		id = dev.ID
		pl := pushPlan{DeviceID: id, IP: dev.IP, Name: s.pushDevName(dev.IP)}
		cred, _, _, hasCred := s.backups.cred(id)
		pl.Vendor = s.pushVendor(dev, cred)
		cmds, note, err := pushCommands(pl.Vendor, dev.IP, p, p.Ports[dev.IP])
		pl.Commands, pl.Note = cmds, note
		pl.NoCred = !hasCred || cred.Username == ""
		if err != nil {
			pl.Error = err.Error()
		}
		out = append(out, pl)
	}
	sort.Slice(out, func(i, j int) bool { return ipLess(out[i].IP, out[j].IP) })
	return out
}

// ----------------- 执行 -----------------

var (
	cliErrRe  = regexp.MustCompile(`(?im)^[ \t^]*(error:[^\n]*|% ?(invalid|incomplete|unknown|ambiguous|unrecognized)[^\n]*|unrecognized command[^\n]*|wrong parameter[^\n]*|too many parameters[^\n]*|incomplete command[^\n]*)`)
	confirmRe = regexp.MustCompile(`(?i)(\[y/n\]|\(y/n\)|\[yes/no\]|continue\s*\?|\[confirm\])[^\n]*$`)
)

// runCLIPush 登录后逐条执行命令; 遇到确认提示自动回答 y, 遇到设备报错立即停止。
// 返回完整会话记录和成功执行的条数。
func runCLIPush(c *cliSession, cred BackupCred, password, enable, vendor string, cmds []string) (string, int, error) {
	hostPrompt, err := cliLogin(c, cred, password, enable, vendor)
	if err != nil {
		return c.tail(), 0, err
	}
	waitRe := regexp.MustCompile(`(?:` + hostPrompt.String() + `)|(?:` + confirmRe.String() + `)`)
	var out strings.Builder
	done := 0
	for _, cmd := range cmds {
		c.buf.Reset()
		if err := c.send(cmd); err != nil {
			return out.String(), done, err
		}
		for step := 0; step < 4; step++ {
			t, err := c.waitFor(waitRe, 90*time.Second, 800*time.Millisecond)
			out.WriteString(t)
			if !strings.HasSuffix(t, "\n") {
				out.WriteString("\n")
			}
			if err != nil {
				return out.String(), done, fmt.Errorf("执行「%s」时%v", cmd, err)
			}
			if m := cliErrRe.FindString(t); m != "" {
				return out.String(), done, fmt.Errorf("设备拒绝了「%s」: %s", cmd, strings.TrimSpace(m))
			}
			if confirmRe.MatchString(t) && !hostPrompt.MatchString(t) {
				c.buf.Reset()
				_ = c.send("y")
				continue
			}
			break
		}
		done++
	}
	fam, _ := pushVendorFamily(vendor)
	if fam == "huawei" || fam == "h3c" {
		_ = c.send("quit")
	} else {
		_ = c.send("exit")
	}
	return out.String(), done, nil
}

type pushJobs struct {
	mu   sync.Mutex
	jobs map[string]*pushJob
}

var pushJobStore = &pushJobs{jobs: map[string]*pushJob{}}

func (j *pushJobs) put(job *pushJob) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for id, old := range j.jobs {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", old.Started, time.Local); err == nil && time.Since(t) > 2*time.Hour {
			delete(j.jobs, id)
		}
	}
	j.jobs[job.ID] = job
}

func (j *pushJobs) update(id string, i int, fn func(*pushResult)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if job, ok := j.jobs[id]; ok && i < len(job.Results) {
		fn(&job.Results[i])
	}
}

func (j *pushJobs) get(id string) (pushJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	if !ok {
		return pushJob{}, false
	}
	cp := *job
	cp.Results = append([]pushResult(nil), job.Results...)
	return cp, true
}

func (s *server) pushAudit(job *pushJob, r pushResult, cmds []string) {
	if s.cfg == nil {
		return
	}
	line := map[string]interface{}{
		"time": time.Now().Format("2006-01-02 15:04:05"), "job": job.ID, "by": job.By, "kind": job.Kind,
		"ip": r.IP, "ok": r.State == "ok", "error": r.Error, "commands": cmds, "transcript": truncate(r.Transcript, 20000),
	}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	pushLogMu.Lock()
	defer pushLogMu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.conf().DataDir, "push_log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func (s *server) runPushJob(job *pushJob, plans []pushPlan) {
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, pl := range plans {
		wg.Add(1)
		go func(i int, pl pushPlan) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pushJobStore.update(job.ID, i, func(r *pushResult) { r.State = "running" })
			var res pushResult
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						res.Error = fmt.Sprintf("执行异常: %v", rec)
					}
				}()
				res = s.pushOne(pl)
			}()
			res.State = "ok"
			if res.Error != "" {
				res.State = "failed"
			}
			pushJobStore.update(job.ID, i, func(r *pushResult) {
				r.Transcript, r.Executed, r.Error, r.State = res.Transcript, res.Executed, res.Error, res.State
			})
			s.pushAudit(job, res, pl.Commands)
		}(i, pl)
	}
	wg.Wait()
	pushJobStore.mu.Lock()
	if j, ok := pushJobStore.jobs[job.ID]; ok {
		j.Done = true
	}
	pushJobStore.mu.Unlock()
}

func (s *server) pushOne(pl pushPlan) pushResult {
	res := pushResult{DeviceID: pl.DeviceID, IP: pl.IP, Name: pl.Name, Total: len(pl.Commands)}
	cred, pw, en, ok := s.backups.cred(pl.DeviceID)
	if !ok || cred.Username == "" {
		res.Error = "还没有配置这台设备的登录凭据"
		return res
	}
	run := func(c *cliSession) (string, error) {
		out, n, err := runCLIPush(c, cred, pw, en, pl.Vendor, pl.Commands)
		res.Executed = n
		res.Transcript = out
		return out, err
	}
	var err error
	if cred.Method == "telnet" {
		_, err = cliViaTelnet(context.Background(), pl.IP, cred, run)
	} else {
		_, err = s.backups.cliViaSSH(context.Background(), pl.IP, cred, pw, run)
	}
	if err != nil {
		res.Error = err.Error()
		if res.Executed > 0 && res.Executed < res.Total {
			res.Error += fmt.Sprintf(" (前 %d 条已经执行并进入运行配置, 未保存; 请登录设备核对)", res.Executed)
		}
	}
	return res
}

// ----------------- HTTP -----------------

type pushRequest struct {
	DeviceIDs []string   `json:"device_ids"`
	Params    pushParams `json:"params"`
	// run 时可以带上页面上改过的命令(设备 ID -> 命令), 没带的按参数重新生成
	Commands map[string][]string `json:"commands"`
}

func (s *server) pushAllowed(w http.ResponseWriter, sess *session, kind string) bool {
	perm := "config_push"
	if kind == "show" {
		perm = "backup_view"
	}
	if !s.auth.can(sess.user, perm) {
		s.fail(w, http.StatusForbidden, map[string]string{"config_push": "没有「配置下发」权限", "backup_view": "没有「查看配置备份」权限"}[perm]+", 请联系管理员分配")
		return false
	}
	return true
}

func (s *server) handlePushPreview(w http.ResponseWriter, r *http.Request, sess *session) {
	var req pushRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !s.pushAllowed(w, sess, req.Params.Kind) {
		return
	}
	if err := normalizePushParams(&req.Params, s.conf()); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"plans": s.pushPlans(req.DeviceIDs, req.Params), "params": req.Params})
}

func (s *server) handlePushRun(w http.ResponseWriter, r *http.Request, sess *session) {
	var req pushRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !s.pushAllowed(w, sess, req.Params.Kind) {
		return
	}
	if err := normalizePushParams(&req.Params, s.conf()); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	plans := s.pushPlans(req.DeviceIDs, req.Params)
	runnable := plans[:0]
	for _, pl := range plans {
		if pl.NoCred {
			continue // 没凭据的只能预览
		}
		if edited, ok := req.Commands[pl.DeviceID]; ok && req.Params.Kind != "show" {
			pl.Commands = nil
			for _, l := range edited {
				if strings.TrimSpace(l) != "" {
					pl.Commands = append(pl.Commands, strings.TrimRight(l, " \t\r"))
				}
			}
			if len(pl.Commands) > 0 {
				pl.Error = ""
			}
		}
		if pl.Error == "" && len(pl.Commands) > 0 {
			runnable = append(runnable, pl)
		}
	}
	if len(runnable) == 0 {
		s.fail(w, 400, "没有可以执行的设备(没配凭据 / 没选端口 / 命令为空)")
		return
	}
	job := &pushJob{ID: randomID(), Kind: req.Params.Kind, By: sess.user, Started: time.Now().Format("2006-01-02 15:04:05")}
	for _, pl := range runnable {
		job.Results = append(job.Results, pushResult{DeviceID: pl.DeviceID, IP: pl.IP, Name: pl.Name, State: "pending", Total: len(pl.Commands)})
	}
	pushJobStore.put(job)
	fmt.Printf("🛠️  %s 对 %d 台设备执行「%s」下发 (任务 %s)\n", sess.user, len(runnable), job.Kind, job.ID)
	go s.runPushJob(job, runnable)
	s.writeJSON(w, 200, map[string]interface{}{"job": job.ID, "count": len(runnable)})
}

// handlePushMeta: 页面上的默认值——本机的 IPv4 地址(交换机要把 sFlow/syslog 发到哪)
// 和三个被动采集器实际监听的端口(0 = 没启用, 页面上会提示)。
func (s *server) handlePushMeta(w http.ResponseWriter, r *http.Request, sess *session) {
	var ips []string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				ips = append(ips, n.IP.String())
			}
		}
	}
	c := s.conf()
	s.writeJSON(w, 200, map[string]interface{}{
		"ips": ips, "sflow_port": c.Collectors.SflowPort, "syslog_port": c.Collectors.SyslogPort, "netflow_port": c.Collectors.NetflowPort,
		"can_push": s.auth.can(sess.user, "config_push"), "can_view": s.auth.can(sess.user, "backup_view"),
	})
}

func (s *server) handlePushJob(w http.ResponseWriter, r *http.Request, sess *session) {
	job, ok := pushJobStore.get(r.URL.Query().Get("id"))
	if !ok {
		s.fail(w, 404, "任务不存在或已过期")
		return
	}
	if job.By != sess.user && !s.auth.can(sess.user, "config_push") {
		s.fail(w, 403, "只能查看自己发起的任务")
		return
	}
	s.writeJSON(w, 200, job)
}
