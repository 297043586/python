package main

// 交换机配置备份。
//
// SNMP 本身读不到配置文件(只有 Cisco/华为/H3C 的私有 MIB 能"让设备把配置推到
// TFTP 服务器", 需要可写团体名 + TFTP 服务, 而且锐捷不支持), 所以这里走最通用
// 的办法: 登录设备 CLI 执行"显示当前配置"命令, 把输出存下来。
//
//   - SSH: 调用系统自带的 ssh 客户端(Windows 10 1809+ 自带 OpenSSH, Linux 基本
//     都有)。密码通过 SSH_ASKPASS 机制交给 ssh——SSH_ASKPASS 指向本程序自己,
//     本程序发现自己是以 askpass 身份被拉起时, 只把环境变量里的密码打印出来就
//     退出(见 main.go 开头)。这样不需要引入第三方 SSH 库, 密码也不会出现在命令行里。
//     老设备只支持旧算法(diffie-hellman-group1/14-sha1、ssh-rsa、aes-cbc)时,
//     第一次连接失败会自动带上兼容参数重试。
//   - Telnet: 标准库直接实现(拒绝所有 Telnet 选项协商, 按用户名/密码提示登录)。
//   - 手工上传: 不方便开远程登录的设备, 管理员可以把配置文本贴进来归档。
//
// 登录后先识别提示符(<HUAWEI> / Switch# / Ruijie>), 按厂商关闭分页
// (screen-length 0 temporary / screen-length disable / terminal length 0),
// 再执行 display current-configuration / show running-config, 碰到 "More"
// 分页提示自动翻页, 直到提示符再次出现。
//
// 每台设备的配置按版本保存(默认保留 30 份), 跟上一份比较有没有变化(忽略
// 时间戳之类每次都变的行), 页面上可以查看任意两个版本的差异。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BackupCred 是一台设备的登录凭据(落盘时密码加密)。
type BackupCred struct {
	Method      string `json:"method"` // ssh / telnet
	Port        int    `json:"port"`
	Username    string `json:"username"`
	PasswordEnc string `json:"password_enc,omitempty"`
	EnableEnc   string `json:"enable_enc,omitempty"` // Cisco/锐捷 enable 密码, 可空
	Vendor      string `json:"vendor"`               // auto / huawei / h3c / ruijie / cisco
	Commands    string `json:"commands"`             // 自定义命令(每行一条, 最后一条的输出作为配置), 空 = 按厂商默认
}

type BackupMeta struct {
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`
	DeviceIP string `json:"device_ip"`
	Time     string `json:"time"`
	Method   string `json:"method"` // ssh / telnet / upload
	By       string `json:"by"`
	Size     int    `json:"size"`
	Lines    int    `json:"lines"`
	Hash     string `json:"hash"`
	Changed  bool   `json:"changed"` // 跟上一份相比有实际变化
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Note     string `json:"note,omitempty"`
}

// BackupStatus 记录每台设备最近一次备份尝试的结果(包括失败的)。
type BackupStatus struct {
	Time  string `json:"time"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type BackupSettings struct {
	AutoEnabled bool   `json:"auto_enabled"`
	AutoHour    int    `json:"auto_hour"`
	Keep        int    `json:"keep"`                    // 每台设备保留份数
	KeepDays    int    `json:"keep_days,omitempty"`     // 保留天数, 0 = 不按天清(每台最新一份永远保留), 见 storage.go
	PushLogDays int    `json:"push_log_days,omitempty"` // 配置下发审计日志保留天数, 0 = 不清
	SSHPath     string `json:"ssh_path"`                // ssh 可执行文件, 空 = 从 PATH 找
	OnlyChanged bool   `json:"only_changed"`
	LastAutoDay string `json:"last_auto_day,omitempty"`
}

type backupFile struct {
	Settings BackupSettings          `json:"settings"`
	Creds    map[string]BackupCred   `json:"creds"`
	Metas    map[string][]BackupMeta `json:"metas"` // deviceID -> 新的在前
	Status   map[string]BackupStatus `json:"status"`
}

type BackupStore struct {
	mu      sync.RWMutex
	dir     string
	box     *secretBox
	data    backupFile
	running map[string]bool
}

func newBackupStore(dataDir string, box *secretBox) *BackupStore {
	return &BackupStore{dir: filepath.Join(dataDir, "backups"), box: box, running: map[string]bool{}}
}

func (b *BackupStore) indexPath() string { return filepath.Join(b.dir, "index.json") }

func (b *BackupStore) load() error {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	b.data = backupFile{Settings: BackupSettings{AutoHour: 2, Keep: 30, OnlyChanged: true}}
	_ = readJSON(b.indexPath(), &b.data)
	if b.data.Creds == nil {
		b.data.Creds = map[string]BackupCred{}
	}
	if b.data.Metas == nil {
		b.data.Metas = map[string][]BackupMeta{}
	}
	if b.data.Status == nil {
		b.data.Status = map[string]BackupStatus{}
	}
	if b.data.Settings.Keep <= 0 {
		b.data.Settings.Keep = 30
	}
	return nil
}

func (b *BackupStore) saveLocked() error { return atomicWriteJSON(b.indexPath(), b.data, 0o600) }

func (b *BackupStore) cfgPath(deviceID, id string) string {
	return filepath.Join(b.dir, deviceID, id+".cfg")
}

func (b *BackupStore) Settings() BackupSettings {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.data.Settings
}

// normalizeConfig 去掉每次导出都会变的行(时间戳/运行时长/NTP 漂移), 用于比较是否"真的改过配置"。
var volatileLine = regexp.MustCompile(`(?i)^\s*(!\s*(time|last configuration change|nvram config last updated|configuration last)|ntp clock-period|#?\s*(current time|uptime is)|! *generated|!software version)`)

func normalizeConfig(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" || volatileLine.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// ----------------- CLI 会话 -----------------

var (
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b[()][A-Z0-9]|\x1b[=>]`)
	moreRe       = regexp.MustCompile(`(?i)(-+ ?more ?-+|--more--|<--- more --->|press any key|\(q\)uit)[^\n]*$`)
	promptRe     = regexp.MustCompile(`(?:^|\n)([^\n]{1,80}?[>#\]])\s*$`)
	loginUserRe  = regexp.MustCompile(`(?i)(username|user name|login|user)\s*:\s*$`)
	pressEnterRe = regexp.MustCompile(`(?i)press (return|enter)[^\n]*$`)
	yesNoRe      = regexp.MustCompile(`(?i)\[y/n\]\s*:?\s*$`)
	passRe       = regexp.MustCompile(`(?i)password\s*:\s*$`)
)

// cleanTerm 去掉终端控制字符和分页提示留下的退格。
func cleanTerm(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// 处理退格: "---- More ----\b\b\b..." 之类
	if strings.ContainsRune(s, '\b') {
		var out []rune
		for _, r := range s {
			if r == '\b' {
				if len(out) > 0 {
					out = out[:len(out)-1]
				}
				continue
			}
			out = append(out, r)
		}
		s = string(out)
	}
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

type cliSession struct {
	w      io.Writer
	chunks chan []byte
	buf    bytes.Buffer // 当前这条命令累计的输出(已清洗)
	closed bool
	log    *bytes.Buffer // 完整会话记录, 失败时截取末尾给用户看
	onMore func()
}

func newCLISession(r io.Reader, w io.Writer) *cliSession {
	c := &cliSession{w: w, chunks: make(chan []byte, 64), log: &bytes.Buffer{}}
	go func() {
		defer close(c.chunks)
		for {
			p := make([]byte, 4096)
			n, err := r.Read(p)
			if n > 0 {
				c.chunks <- p[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *cliSession) send(line string) error {
	_, err := io.WriteString(c.w, line+"\r\n")
	return err
}

// waitFor 读输出直到清洗后的累计内容匹配 re(且之后 quiet 时间内没有新输出), 或超时。
func (c *cliSession) waitFor(re *regexp.Regexp, timeout, quiet time.Duration) (string, error) {
	deadline := time.After(timeout)
	for {
		txt := cleanTerm(c.buf.String())
		if moreRe.MatchString(txt) {
			// 自动翻页, 并把分页提示从内容里去掉
			loc := moreRe.FindStringIndex(txt)
			c.buf.Reset()
			c.buf.WriteString(txt[:loc[0]])
			_, _ = io.WriteString(c.w, " ")
			continue
		}
		if re.MatchString(txt) {
			select {
			case p, ok := <-c.chunks:
				if !ok {
					return txt, nil
				}
				c.log.Write(p)
				c.buf.Write(p)
				continue
			case <-time.After(quiet):
				return txt, nil
			}
		}
		select {
		case p, ok := <-c.chunks:
			if !ok {
				return txt, errors.New("连接被对端关闭")
			}
			c.log.Write(p)
			c.buf.Write(p)
		case <-deadline:
			return txt, errors.New("等待设备响应超时")
		}
	}
}

func (c *cliSession) tail() string {
	t := cleanTerm(c.log.String())
	if len(t) > 600 {
		t = t[len(t)-600:]
	}
	return strings.TrimSpace(t)
}

// vendorCommands 返回某厂商的备份命令和退出命令。命令本身在「设置 → 配置备份命令」
// 里可改(华为/H3C 默认 display current-configuration, 锐捷/Cisco 默认
// show running-config), 见 settings.go 的 builtinBackupCommands。
func vendorCommands(vendor string) (cmds []string, exit string) {
	switch vendor {
	case "huawei", "h3c":
		return backupCommandsFor(vendor), "quit"
	default: // ruijie / cisco 及其他 Cisco 风格
		return backupCommandsFor(vendor), "exit"
	}
}

// runCLIBackup 在已登录(或正等待登录)的会话上执行备份命令, 返回配置文本。
func runCLIBackup(c *cliSession, cred BackupCred, password, enable, vendor string) (string, error) {
	hostPrompt, err := cliLogin(c, cred, password, enable, vendor)
	if err != nil {
		return "", err
	}

	cmds, exitCmd := vendorCommands(vendor)
	if strings.TrimSpace(cred.Commands) != "" {
		cmds = nil
		for _, l := range strings.Split(cred.Commands, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				cmds = append(cmds, l)
			}
		}
	}
	var out string
	for i, cmd := range cmds {
		c.buf.Reset()
		if err := c.send(cmd); err != nil {
			return "", err
		}
		timeout := 15 * time.Second
		if i == len(cmds)-1 {
			timeout = 3 * time.Minute // 大配置
		}
		t, err := c.waitFor(hostPrompt, timeout, 1200*time.Millisecond)
		if err != nil {
			return "", fmt.Errorf("执行「%s」失败: %v; 设备输出: %s", cmd, err, c.tail())
		}
		out = t
	}
	_ = c.send(exitCmd)

	// 去掉第一行(命令回显)和最后一行(提示符)
	lines := strings.Split(out, "\n")
	start := 0
	for i, l := range lines {
		if strings.Contains(l, cmds[len(cmds)-1]) {
			start = i + 1
			break
		}
	}
	end := len(lines)
	for end > start && (strings.TrimSpace(lines[end-1]) == "" || hostPrompt.MatchString("\n"+lines[end-1])) {
		end--
	}
	cfg := strings.Join(lines[start:end], "\n")
	if strings.Count(cfg, "\n") < 5 {
		return "", fmt.Errorf("拿到的配置只有 %d 行, 可能是命令不对或权限不足(需要能执行 %s 的级别); 设备输出: %s", strings.Count(cfg, "\n")+1, cmds[len(cmds)-1], truncate(cfg, 300))
	}
	low := strings.ToLower(cfg[:min(len(cfg), 200)])
	if strings.Contains(low, "unrecognized command") || strings.Contains(low, "invalid input") || strings.Contains(low, "% unknown command") {
		return "", fmt.Errorf("设备不认识这条命令: %s", truncate(cfg, 200))
	}
	return cfg + "\n", nil
}

// cliLogin 完成登录(Telnet 要自己应答用户名/密码, SSH 这一步 ssh 已经做完)
// 和 Cisco/锐捷的 enable, 返回"这台设备的提示符"正则: 只认带主机名的提示符,
// 比通用正则更不容易被命令输出内容误触发; 配置模式下的 [HOST-xxx] /
// HOST(config-if)# 也能匹配。备份和配置下发共用。
func cliLogin(c *cliSession, cred BackupCred, password, enable, vendor string) (*regexp.Regexp, error) {
	// Telnet 需要自己处理登录; SSH 这一步 ssh 已经做完, 直接等提示符。
	// 除了用户名/密码, Telnet 登录过程中常见的几种交互也要应答:
	//   - "Press RETURN/ENTER to get started"(H3C/Cisco 控制台风格)→ 回车
	//   - 华为首次登录/密码过期 "Change now? [Y/N]:" → N
	//   - 只配了 line vty password 的锐捷/Cisco 只问密码不问用户名 → 直接给密码
	anyRe := regexp.MustCompile(loginUserRe.String() + "|" + passRe.String() + "|" + pressEnterRe.String() + "|" + yesNoRe.String() + "|" + promptRe.String())
	passSent := 0
login:
	for i := 0; i < 8; i++ {
		txt, err := c.waitFor(anyRe, 20*time.Second, 400*time.Millisecond)
		if err != nil {
			return nil, fmt.Errorf("%v; 设备输出: %s", err, c.tail())
		}
		switch {
		case loginUserRe.MatchString(txt):
			if passSent > 0 {
				return nil, errors.New("登录失败: 用户名或密码错误(设备再次要求输入用户名)")
			}
			c.buf.Reset()
			_ = c.send(cred.Username)
			continue
		case passRe.MatchString(txt):
			if passSent >= 1 {
				return nil, errors.New("登录失败: 用户名或密码错误")
			}
			passSent++
			c.buf.Reset()
			_ = c.send(password)
			continue
		case yesNoRe.MatchString(txt):
			c.buf.Reset()
			_ = c.send("N")
			continue
		case pressEnterRe.MatchString(txt):
			c.buf.Reset()
			_ = c.send("")
			continue
		}
		break login
	}
	txt := cleanTerm(c.buf.String())
	m := promptRe.FindStringSubmatch(txt)
	if m == nil {
		return nil, fmt.Errorf("没有识别到命令提示符, 设备输出: %s", c.tail())
	}
	if strings.Contains(strings.ToLower(txt), "fail") && strings.Contains(strings.ToLower(txt), "auth") {
		return nil, errors.New("认证失败")
	}
	prompt := strings.TrimSpace(m[1])

	// 用户模式(>)的 Cisco/锐捷设备进 enable
	if strings.HasSuffix(prompt, ">") && !strings.HasPrefix(prompt, "<") && (vendor == "cisco" || vendor == "ruijie") {
		c.buf.Reset()
		_ = c.send("enable")
		t, err := c.waitFor(regexp.MustCompile(passRe.String()+"|"+promptRe.String()), 10*time.Second, 300*time.Millisecond)
		if err == nil && passRe.MatchString(t) {
			c.buf.Reset()
			pw := enable
			if pw == "" {
				pw = password
			}
			_ = c.send(pw)
			t, err = c.waitFor(promptRe, 10*time.Second, 300*time.Millisecond)
		}
		if err == nil {
			if m := promptRe.FindStringSubmatch(t); m != nil {
				prompt = strings.TrimSpace(m[1])
			}
		}
	}

	// 等"这个提示符"再次出现, 比通用正则更不容易被配置内容误触发
	host := strings.TrimRight(strings.TrimLeft(prompt, "<["), ">#]")
	hostPrompt := regexp.MustCompile(`(?:^|\n)[<\[]?` + regexp.QuoteMeta(host) + `[^\n]{0,40}[>#\]]\s*$`)

	return hostPrompt, nil
}

// ----------------- Telnet -----------------

// telnetConn 包一层 net.Conn, 剥掉 IAC 协商并统一拒绝所有选项。
type telnetConn struct {
	net.Conn
	pending []byte
}

func (t *telnetConn) Read(p []byte) (int, error) {
	buf := make([]byte, len(p))
	for {
		n, err := t.Conn.Read(buf)
		out := p[:0]
		for i := 0; i < n; i++ {
			c := buf[i]
			if c != 255 {
				out = append(out, c)
				continue
			}
			if i+1 >= n {
				break
			}
			cmd := buf[i+1]
			switch cmd {
			case 251, 252, 253, 254: // WILL WONT DO DONT
				if i+2 < n {
					opt := buf[i+2]
					var reply byte
					switch cmd {
					case 251:
						reply = 254               // DONT
						if opt == 1 || opt == 3 { // 允许对端回显 / 抑制 GA, 否则部分设备不出提示符
							reply = 253
						}
					case 253:
						reply = 252 // WONT
					}
					if reply != 0 {
						_, _ = t.Conn.Write([]byte{255, reply, opt})
					}
					i += 2
				} else {
					i = n
				}
			case 250: // SB ... SE
				j := i + 2
				for j+1 < n && !(buf[j] == 255 && buf[j+1] == 240) {
					j++
				}
				i = j + 1
			case 255:
				out = append(out, 255)
				i++
			default:
				i++
			}
		}
		if len(out) > 0 || err != nil {
			return len(out), err
		}
	}
}

func backupViaTelnet(ctx context.Context, ip string, cred BackupCred, password, enable, vendor string) (string, error) {
	return cliViaTelnet(ctx, ip, cred, func(c *cliSession) (string, error) {
		return runCLIBackup(c, cred, password, enable, vendor)
	})
}

// cliViaTelnet 建立 Telnet 会话后交给 run 处理(登录 + 执行命令); 备份和配置下发共用。
func cliViaTelnet(ctx context.Context, ip string, cred BackupCred, run func(*cliSession) (string, error)) (string, error) {
	port := cred.Port
	if port == 0 {
		port = 23
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("Telnet 连接失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	tc := &telnetConn{Conn: conn}
	return run(newCLISession(tc, tc))
}

// ----------------- SSH(系统 ssh 客户端) -----------------

const askpassEnvFlag = "NETMON_ASKPASS_MODE"
const askpassEnvSecret = "NETMON_ASKPASS_SECRET"

// runAskpassIfRequested: 本程序被 ssh 当作 SSH_ASKPASS 拉起时, 打印密码后退出。
func runAskpassIfRequested() {
	if os.Getenv(askpassEnvFlag) == "1" {
		fmt.Println(os.Getenv(askpassEnvSecret))
		os.Exit(0)
	}
}

var legacySSHOpts = []string{
	"-o", "KexAlgorithms=+diffie-hellman-group14-sha1,diffie-hellman-group1-sha1,diffie-hellman-group-exchange-sha1",
	"-o", "HostKeyAlgorithms=+ssh-rsa,ssh-dss",
	"-o", "PubkeyAcceptedAlgorithms=+ssh-rsa",
	"-o", "Ciphers=+aes128-cbc,aes256-cbc,3des-cbc",
}

func (b *BackupStore) sshBinary() (string, error) {
	p := strings.TrimSpace(b.Settings().SSHPath)
	if p == "" {
		p = "ssh"
	}
	path, err := exec.LookPath(p)
	if err != nil {
		return "", fmt.Errorf("找不到 ssh 客户端(%s)。Windows 请在「设置 → 应用 → 可选功能」里安装 OpenSSH 客户端, 或在备份设置里填写 ssh.exe 的完整路径", p)
	}
	return path, nil
}

func (b *BackupStore) backupViaSSH(ctx context.Context, ip string, cred BackupCred, password, enable, vendor string) (string, error) {
	return b.cliViaSSH(ctx, ip, cred, password, func(c *cliSession) (string, error) {
		return runCLIBackup(c, cred, password, enable, vendor)
	})
}

// cliViaSSH 用系统 ssh 客户端建立会话后交给 run 处理; 协商失败会自动用兼容旧算法的参数重试一次。
func (b *BackupStore) cliViaSSH(ctx context.Context, ip string, cred BackupCred, password string, run func(*cliSession) (string, error)) (string, error) {
	sshPath, err := b.sshBinary()
	if err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	port := cred.Port
	if port == 0 {
		port = 22
	}
	knownHosts := filepath.Join(b.dir, "known_hosts")
	try := func(legacy bool) (string, error) {
		args := []string{"-tt",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile=" + knownHosts,
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=15",
			"-o", "NumberOfPasswordPrompts=1",
			"-o", "PubkeyAuthentication=no",
			"-o", "PreferredAuthentications=password,keyboard-interactive",
			"-o", "LogLevel=ERROR",
			"-p", strconv.Itoa(port),
		}
		if legacy {
			args = append(args, legacySSHOpts...)
		}
		args = append(args, "-l", cred.Username, ip)
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, sshPath, args...)
		cmd.Env = append(os.Environ(),
			"SSH_ASKPASS="+self, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=netmon:0",
			askpassEnvFlag+"=1", askpassEnvSecret+"="+password)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return "", err
		}
		pr, pw := io.Pipe()
		cmd.Stdout, cmd.Stderr = pw, pw
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("启动 ssh 失败: %v", err)
		}
		go func() { _ = cmd.Wait(); pw.Close() }()
		sess := newCLISession(pr, stdin)
		out, err := run(sess)
		stdin.Close()
		cancel()
		if err != nil {
			return "", fmt.Errorf("%v", err)
		}
		return out, nil
	}
	out, err := try(false)
	if err != nil && isSSHNegotiationError(err.Error()) {
		out, err = try(true)
		if err != nil {
			err = fmt.Errorf("(已尝试兼容旧算法) %v", err)
		}
	}
	return out, err
}

func isSSHNegotiationError(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "no matching") || strings.Contains(s, "unable to negotiate")
}

// ----------------- 备份流程 -----------------

func (b *BackupStore) cred(deviceID string) (BackupCred, string, string, bool) {
	b.mu.RLock()
	c, ok := b.data.Creds[deviceID]
	b.mu.RUnlock()
	if !ok {
		return c, "", "", false
	}
	pw, _ := b.box.open(c.PasswordEnc)
	en := ""
	if c.EnableEnc != "" {
		en, _ = b.box.open(c.EnableEnc)
	}
	return c, pw, en, true
}

// store 保存一份配置, 返回元信息。onlyChanged=true 且跟上一份一样时不保存新文件, 只更新状态。
func (b *BackupStore) store(dev DeviceTarget, cfg, method, by, note string, onlyChanged bool) (BackupMeta, bool, error) {
	norm := normalizeConfig(cfg)
	sum := sha256.Sum256([]byte(strings.Join(norm, "\n")))
	hash := hex.EncodeToString(sum[:])

	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.data.Metas[dev.ID]
	meta := BackupMeta{ID: time.Now().Format("20060102150405") + randomID()[:4], DeviceID: dev.ID, DeviceIP: dev.IP,
		Time: time.Now().Format("2006-01-02 15:04:05"), Method: method, By: by, Size: len(cfg), Lines: len(norm), Hash: hash, Note: note}
	if len(list) > 0 {
		prevRaw, err := os.ReadFile(b.cfgPath(dev.ID, list[0].ID))
		if list[0].Hash == hash {
			if onlyChanged && method != "upload" {
				b.data.Status[dev.ID] = BackupStatus{Time: meta.Time, OK: true, Error: "配置无变化, 未新增版本"}
				_ = b.saveLocked()
				return list[0], false, nil
			}
		} else {
			meta.Changed = true
			if err == nil {
				ops := diffLines(normalizeConfig(string(prevRaw)), norm)
				for _, o := range ops {
					switch o.Op {
					case "+":
						meta.Added++
					case "-":
						meta.Removed++
					}
				}
			}
		}
	} else {
		meta.Changed = true
	}
	if err := os.MkdirAll(filepath.Join(b.dir, dev.ID), 0o700); err != nil {
		return meta, false, err
	}
	if err := os.WriteFile(b.cfgPath(dev.ID, meta.ID), []byte(cfg), 0o600); err != nil {
		return meta, false, err
	}
	list = append([]BackupMeta{meta}, list...)
	for len(list) > b.data.Settings.Keep {
		old := list[len(list)-1]
		list = list[:len(list)-1]
		_ = os.Remove(b.cfgPath(dev.ID, old.ID))
	}
	b.data.Metas[dev.ID] = list
	b.data.Status[dev.ID] = BackupStatus{Time: meta.Time, OK: true}
	return meta, true, b.saveLocked()
}

func (b *BackupStore) fail(deviceID, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data.Status[deviceID] = BackupStatus{Time: time.Now().Format("2006-01-02 15:04:05"), Error: msg}
	_ = b.saveLocked()
}

// backupDevice 对一台设备执行一次在线备份。
func (s *server) backupDevice(dev DeviceTarget, by string) (BackupMeta, bool, error) {
	b := s.backups
	b.mu.Lock()
	if b.running[dev.ID] {
		b.mu.Unlock()
		return BackupMeta{}, false, errors.New("这台设备正在备份中")
	}
	b.running[dev.ID] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.running, dev.ID)
		b.mu.Unlock()
	}()

	cred, pw, en, ok := b.cred(dev.ID)
	if !ok || cred.Username == "" {
		return BackupMeta{}, false, errors.New("还没有配置这台设备的登录凭据")
	}
	vendor := cred.Vendor
	if vendor == "" || vendor == "auto" {
		vendor = dev.Brand
		s.hub.mu.RLock()
		if i, ok := s.hub.byIP[dev.IP]; ok && i < len(s.hub.reports) {
			vendor = s.hub.reports[i].Brand
		}
		s.hub.mu.RUnlock()
	}
	ctx := context.Background()
	var cfg string
	var err error
	if cred.Method == "telnet" {
		cfg, err = backupViaTelnet(ctx, dev.IP, cred, pw, en, vendor)
	} else {
		cfg, err = b.backupViaSSH(ctx, dev.IP, cred, pw, en, vendor)
	}
	if err != nil {
		b.fail(dev.ID, err.Error())
		return BackupMeta{}, false, err
	}
	method := cred.Method
	if method == "" {
		method = "ssh"
	}
	return b.store(dev, cfg, method, by, "", b.Settings().OnlyChanged)
}

// backupAll 依次备份所有配了凭据的设备(并发 4)。
func (s *server) backupAll(by string) (ok, failed, unchanged int) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, dev := range s.devices.List() {
		if _, _, _, has := s.backups.cred(dev.ID); !has {
			continue
		}
		wg.Add(1)
		go func(d DeviceTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, created, err := s.backupDevice(d, by)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed++
			case created:
				ok++
			default:
				unchanged++
			}
		}(dev)
	}
	wg.Wait()
	return
}

func (s *server) backupScheduler() {
	for {
		time.Sleep(time.Minute)
		st := s.backups.Settings()
		now := time.Now()
		day := now.Format("2006-01-02")
		if !st.AutoEnabled || now.Hour() != st.AutoHour || st.LastAutoDay == day {
			continue
		}
		s.backups.mu.Lock()
		s.backups.data.Settings.LastAutoDay = day
		_ = s.backups.saveLocked()
		s.backups.mu.Unlock()
		ok, failed, same := s.backupAll("定时任务")
		fmt.Printf("💾 定时配置备份: 新版本 %d 台, 无变化 %d 台, 失败 %d 台\n", ok, same, failed)
	}
}

// ----------------- 差异比较(Myers) -----------------

type diffOp struct {
	Op   string `json:"op"` // " " / "+" / "-"
	Text string `json:"text"`
}

// diffLines 返回 a -> b 的逐行差异。差异太大(>4000 处编辑)时退化为整体替换。
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	max := n + m
	if max == 0 {
		return nil
	}
	const limit = 4000
	offset := max
	v := make([]int, 2*max+2)
	var trace [][]int
	found := false
	for d := 0; d <= max && d <= limit; d++ {
		cp := make([]int, len(v))
		copy(cp, v)
		trace = append(trace, cp)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		ops := make([]diffOp, 0, n+m)
		for _, l := range a {
			ops = append(ops, diffOp{"-", l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{"+", l})
		}
		return ops
	}
	// 回溯
	var ops []diffOp
	x, y := n, m
	for d := len(trace) - 1; d >= 0 && (x > 0 || y > 0); d-- {
		vv := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vv[offset+k-1] < vv[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vv[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			ops = append(ops, diffOp{" ", a[x-1]})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				ops = append(ops, diffOp{"+", b[y-1]})
			} else {
				ops = append(ops, diffOp{"-", a[x-1]})
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// compactDiff 只保留改动行及上下各 3 行上下文, 省略号代替中间未改动的大段。
func compactDiff(ops []diffOp) []diffOp {
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.Op != " " {
			for j := i - 3; j <= i+3; j++ {
				if j >= 0 && j < len(ops) {
					keep[j] = true
				}
			}
		}
	}
	var out []diffOp
	skipped := 0
	for i, o := range ops {
		if keep[i] {
			if skipped > 0 {
				out = append(out, diffOp{"@", fmt.Sprintf("…… 省略 %d 行未改动内容 ……", skipped)})
				skipped = 0
			}
			out = append(out, o)
		} else {
			skipped++
		}
	}
	if skipped > 0 && len(out) > 0 {
		out = append(out, diffOp{"@", fmt.Sprintf("…… 省略 %d 行未改动内容 ……", skipped)})
	}
	return out
}

// ----------------- HTTP -----------------

type backupDeviceView struct {
	DeviceID  string        `json:"device_id"`
	IP        string        `json:"ip"`
	Name      string        `json:"name"`
	Brand     string        `json:"brand"`
	HasCred   bool          `json:"has_cred"`
	Method    string        `json:"method,omitempty"`
	Port      int           `json:"port,omitempty"`
	Username  string        `json:"username,omitempty"`
	Vendor    string        `json:"vendor,omitempty"`
	Commands  string        `json:"commands,omitempty"`
	HasEnable bool          `json:"has_enable"`
	Running   bool          `json:"running"`
	Status    *BackupStatus `json:"status,omitempty"`
	Versions  []BackupMeta  `json:"versions"`
}

func (s *server) handleBackupList(w http.ResponseWriter, r *http.Request, sess *session) {
	names := map[string]string{}
	s.hub.mu.RLock()
	for _, rep := range s.hub.reports {
		names[rep.IP] = rep.SysName
	}
	s.hub.mu.RUnlock()
	b := s.backups
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := []backupDeviceView{}
	for _, d := range s.devices.List() {
		v := backupDeviceView{DeviceID: d.ID, IP: d.IP, Name: names[d.IP], Brand: d.Brand, Running: b.running[d.ID], Versions: b.data.Metas[d.ID]}
		if v.Versions == nil {
			v.Versions = []BackupMeta{}
		}
		if c, ok := b.data.Creds[d.ID]; ok {
			v.HasCred, v.Method, v.Port, v.Username, v.Vendor, v.Commands, v.HasEnable = true, c.Method, c.Port, c.Username, c.Vendor, c.Commands, c.EnableEnc != ""
		}
		if st, ok := b.data.Status[d.ID]; ok {
			stCopy := st
			v.Status = &stCopy
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return ipLess(out[i].IP, out[j].IP) })
	st := b.data.Settings
	st.LastAutoDay = ""
	_, sshErr := b.sshBinaryLocked()
	vc := map[string]string{}
	for _, v := range backupVendors {
		vc[v] = strings.Join(backupCommandsFor(v), "\n")
	}
	// 页面上新增、自带备份命令的品牌
	vnames := map[string]string{}
	for _, p := range vendors().list {
		if !p.Builtin && p.BackupCmd != "" {
			vc[p.ID] = strings.Join(backupCommandsFor(p.ID), "\n")
			vnames[p.ID] = p.Name
		}
	}
	// 各设备"自动"命令集实际会用哪个厂商(按识别到的品牌)
	brands := map[string]string{}
	s.hub.mu.RLock()
	for _, rep := range s.hub.reports {
		brands[rep.IP] = rep.Brand
	}
	s.hub.mu.RUnlock()
	for i := range out {
		if br := brands[out[i].IP]; br != "" && br != "auto" {
			out[i].Brand = br
		}
	}
	s.writeJSON(w, 200, map[string]interface{}{"devices": out, "settings": st, "ssh_ok": sshErr == nil, "ssh_error": errString(sshErr), "vendor_commands": vc, "vendor_names": vnames})
}

func (b *BackupStore) sshBinaryLocked() (string, error) {
	p := strings.TrimSpace(b.data.Settings.SSHPath)
	if p == "" {
		p = "ssh"
	}
	return exec.LookPath(p)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *server) handleBackupGet(w http.ResponseWriter, r *http.Request, sess *session) {
	q := r.URL.Query()
	devID, id, against := q.Get("device"), q.Get("id"), q.Get("against")
	if !validBackupID(devID) || !validBackupID(id) {
		s.fail(w, 400, "参数错误")
		return
	}
	raw, err := os.ReadFile(s.backups.cfgPath(devID, id))
	if err != nil {
		s.fail(w, 404, "备份不存在")
		return
	}
	if q.Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"config_"+id+".txt\"")
		_, _ = w.Write(raw)
		return
	}
	resp := map[string]interface{}{"content": string(raw)}
	if against != "" && validBackupID(against) {
		old, err := os.ReadFile(s.backups.cfgPath(devID, against))
		if err == nil {
			resp["diff"] = compactDiff(diffLines(normalizeConfig(string(old)), normalizeConfig(string(raw))))
		}
	}
	s.writeJSON(w, 200, resp)
}

func validBackupID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *server) handleBackupRun(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		DeviceID string `json:"device_id"` // 空 = 全部
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.DeviceID == "" {
		go s.backupAll(sess.user)
		s.writeJSON(w, 200, map[string]interface{}{"ok": true, "async": true})
		return
	}
	dev, ok := s.devices.Get(req.DeviceID)
	if !ok {
		s.fail(w, 404, "设备不存在")
		return
	}
	if _, _, _, has := s.backups.cred(dev.ID); !has {
		s.fail(w, 400, "还没有配置这台设备的登录凭据")
		return
	}
	// 大配置 + 慢设备可能要一两分钟, 超过 HTTP 写超时; 后台执行, 页面轮询状态
	go func() { _, _, _ = s.backupDevice(dev, sess.user) }()
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "async": true})
}

func (s *server) handleBackupUpload(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		DeviceID string `json:"device_id"`
		Content  string `json:"content"`
		Note     string `json:"note"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		s.fail(w, 400, "请求格式有误或文件超过 8MB")
		return
	}
	dev, ok := s.devices.Get(req.DeviceID)
	if !ok {
		s.fail(w, 404, "设备不存在")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		s.fail(w, 400, "配置内容为空")
		return
	}
	meta, _, err := s.backups.store(dev, strings.ReplaceAll(req.Content, "\r\n", "\n"), "upload", sess.user, truncate(req.Note, 200), false)
	if err != nil {
		s.fail(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "meta": meta})
}

func (s *server) handleBackupDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		DeviceID string `json:"device_id"`
		ID       string `json:"id"`
	}
	if !s.decode(w, r, &req) || !validBackupID(req.DeviceID) || !validBackupID(req.ID) {
		return
	}
	b := s.backups
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.data.Metas[req.DeviceID]
	for i, m := range list {
		if m.ID == req.ID {
			b.data.Metas[req.DeviceID] = append(list[:i], list[i+1:]...)
			_ = os.Remove(b.cfgPath(req.DeviceID, req.ID))
			_ = b.saveLocked()
			s.writeJSON(w, 200, map[string]interface{}{"ok": true})
			return
		}
	}
	s.fail(w, 404, "备份不存在")
}

func (s *server) handleBackupCred(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		DeviceID  string   `json:"device_id"`
		DeviceIDs []string `json:"device_ids"` // 批量设置
		Delete    bool     `json:"delete"`
		Method    string   `json:"method"`
		Port      int      `json:"port"`
		Username  string   `json:"username"`
		Password  string   `json:"password"` // 空 = 保持原密码
		Enable    string   `json:"enable"`
		Vendor    string   `json:"vendor"`
		Commands  string   `json:"commands"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	// 远程设备大多共用同一套 Telnet 账号, 支持一次给多台设备设置
	ids := req.DeviceIDs
	if len(ids) == 0 {
		ids = []string{req.DeviceID}
	}
	for _, id := range ids {
		if _, ok := s.devices.Get(id); !ok {
			s.fail(w, 404, "设备不存在: "+id)
			return
		}
	}
	b := s.backups
	b.mu.Lock()
	defer b.mu.Unlock()
	if req.Delete {
		for _, id := range ids {
			delete(b.data.Creds, id)
		}
		_ = b.saveLocked()
		s.writeJSON(w, 200, map[string]interface{}{"ok": true})
		return
	}
	if req.Method != "ssh" && req.Method != "telnet" {
		s.fail(w, 400, "登录方式只能是 ssh 或 telnet")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		s.fail(w, 400, "请填写用户名")
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		s.fail(w, 400, "端口不合法")
		return
	}
	var passEnc, enableEnc string
	if req.Password != "" {
		enc, err := b.box.seal(req.Password)
		if err != nil {
			s.fail(w, 500, "加密失败")
			return
		}
		passEnc = enc
	}
	if req.Enable != "" {
		enc, err := b.box.seal(req.Enable)
		if err != nil {
			s.fail(w, 500, "加密失败")
			return
		}
		enableEnc = enc
	}
	for _, id := range ids {
		old := b.data.Creds[id]
		c := BackupCred{Method: req.Method, Port: req.Port, Username: strings.TrimSpace(req.Username), Vendor: req.Vendor,
			Commands: req.Commands, PasswordEnc: old.PasswordEnc, EnableEnc: old.EnableEnc}
		if passEnc != "" {
			c.PasswordEnc = passEnc
		}
		if enableEnc != "" {
			c.EnableEnc = enableEnc
		}
		if c.PasswordEnc == "" {
			s.fail(w, 400, "请填写密码")
			return
		}
		b.data.Creds[id] = c
	}
	if err := b.saveLocked(); err != nil {
		s.fail(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true, "count": len(ids)})
}

func (s *server) handleBackupSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	var req BackupSettings
	if !s.decode(w, r, &req) {
		return
	}
	if req.AutoHour < 0 || req.AutoHour > 23 {
		s.fail(w, 400, "定时时刻需要在 0~23 之间")
		return
	}
	if req.Keep <= 0 || req.Keep > 1000 {
		req.Keep = 30
	}
	b := s.backups
	b.mu.Lock()
	defer b.mu.Unlock()
	req.LastAutoDay = b.data.Settings.LastAutoDay
	req.KeepDays, req.PushLogDays = b.data.Settings.KeepDays, b.data.Settings.PushLogDays // 这两项在「系统设置 → 存储」里改
	b.data.Settings = req
	if err := b.saveLocked(); err != nil {
		s.fail(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": true})
}
