package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// fakeHuaweiCLI 模拟一台已经 SSH 登录好的华为交换机: 提示符在用户视图 <SW1> /
// 系统视图 [SW1] / 接口视图 [SW1-GigabitEthernet0/0/1] 之间切换, save 会问 [Y/N],
// 不认识的命令回 Error。
func fakeHuaweiCLI(t *testing.T) (*cliSession, func()) {
	devInR, devInW := io.Pipe()   // 会话 -> 设备
	devOutR, devOutW := io.Pipe() // 设备 -> 会话
	go func() {
		defer devOutW.Close()
		mode := "<SW1>"
		_, _ = io.WriteString(devOutW, "\r\nInfo: The max number of VTY users is 5.\r\n"+mode)
		sc := bufio.NewScanner(devInR)
		saving := false
		for sc.Scan() {
			cmd := strings.TrimSpace(sc.Text())
			reply := "\r\n"
			switch {
			case saving && strings.EqualFold(cmd, "y"):
				saving = false
				reply += "Save the configuration successfully.\r\n"
			case cmd == "system-view":
				mode = "[SW1]"
			case strings.HasPrefix(cmd, "interface "):
				mode = "[SW1-" + strings.TrimPrefix(cmd, "interface ") + "]"
			case cmd == "quit":
				if strings.HasPrefix(mode, "[SW1-") {
					mode = "[SW1]"
				} else {
					return
				}
			case cmd == "return":
				mode = "<SW1>"
			case cmd == "save":
				saving = true
				_, _ = io.WriteString(devOutW, reply+"The current configuration will be written to the device.\r\nAre you sure to continue?[Y/N]:")
				continue
			case strings.HasPrefix(cmd, "sflow ") || strings.HasPrefix(cmd, "info-center "):
			default:
				reply += "              ^\r\nError: Unrecognized command found at '^' position.\r\n"
			}
			_, _ = io.WriteString(devOutW, reply+mode)
		}
	}()
	c := newCLISession(devOutR, devInW)
	return c, func() { devInW.Close(); devInR.Close() }
}

func TestRunCLIPushHuaweiSaveConfirm(t *testing.T) {
	c, stop := fakeHuaweiCLI(t)
	defer stop()
	cmds := []string{"system-view", "sflow collector 1 ip 10.0.0.9 udp-port 6343 description netmon",
		"interface GigabitEthernet0/0/1", "sflow sampling rate 1000", "quit", "return", "save"}
	out, n, err := runCLIPush(c, BackupCred{Username: "admin"}, "pw", "", "huawei", cmds)
	if err != nil {
		t.Fatalf("不该失败: %v\n%s", err, out)
	}
	if n != len(cmds) {
		t.Fatalf("应该执行 %d 条, 实际 %d", len(cmds), n)
	}
	if !strings.Contains(out, "Save the configuration successfully") {
		t.Fatalf("save 的 [Y/N] 确认没有自动回答:\n%s", out)
	}
}

func TestRunCLIPushStopsOnError(t *testing.T) {
	c, stop := fakeHuaweiCLI(t)
	defer stop()
	cmds := []string{"system-view", "sflow agent ip 10.0.0.1", "bogus command", "sflow collector 1 ip 10.0.0.9"}
	_, n, err := runCLIPush(c, BackupCred{Username: "admin"}, "pw", "", "huawei", cmds)
	if err == nil || !strings.Contains(err.Error(), "bogus command") {
		t.Fatalf("设备报错时应该停下并指出是哪条命令, got err=%v", err)
	}
	if n != 2 {
		t.Fatalf("出错前应该成功执行 2 条, 实际 %d", n)
	}
}

func TestPushCommandsTemplates(t *testing.T) {
	p := pushParams{Kind: "sflow", Collector: "10.0.0.9", Port: 6343, Rate: 1000, Interval: 20, Save: true}
	ports := []string{"GigabitEthernet0/0/1"}
	cases := map[string][]string{
		"huawei": {"system-view", "sflow collector 1 ip 10.0.0.9 udp-port 6343 description netmon", "sflow sampling rate 1000", "return", "save"},
		"h3c":    {"sflow collector 1 ip 10.0.0.9 port 6343 description netmon", "sflow flow collector 1", "save force"},
		"ruijie": {"configure terminal", "sflow collector 1 destination 10.0.0.9 6343", "sflow enable ingress", "end", "write"},
		"cisco":  {"feature sflow", "sflow data-source interface GigabitEthernet0/0/1", "write memory"},
	}
	for vendor, want := range cases {
		cmds, _, err := pushCommands(vendor, "10.0.0.1", p, ports)
		if err != nil {
			t.Fatalf("%s: %v", vendor, err)
		}
		joined := "\n" + strings.Join(cmds, "\n") + "\n"
		for _, w := range want {
			if !strings.Contains(joined, "\n"+w+"\n") {
				t.Errorf("%s 的 sFlow 模板缺少「%s」:\n%s", vendor, w, strings.Join(cmds, "\n"))
			}
		}
	}
	if _, _, err := pushCommands("huawei", "10.0.0.1", p, nil); err == nil {
		t.Error("没选端口时 sFlow 应该报错")
	}
	if _, _, err := pushCommands("huawei", "10.0.0.1", pushParams{Kind: "netflow", Collector: "10.0.0.9", Port: 2055}, ports); err == nil {
		t.Error("华为不该生成 NetFlow v5 模板")
	}
	if _, note, _ := pushCommands("zte", "10.0.0.1", p, ports); note == "" {
		t.Error("没有内置模板的厂商应该提示按 Cisco 风格生成")
	}
	show, _, _ := pushCommands("h3c", "10.0.0.1", pushParams{Kind: "show", Save: true}, nil)
	if strings.Join(show, "|") != "screen-length disable|display logbuffer" {
		t.Errorf("只读的 show 不该带保存命令: %v", show)
	}
	sys, _, _ := pushCommands("huawei", "10.0.0.1", pushParams{Kind: "syslog", Collector: "10.0.0.9", Port: 1514, Level: "informational"}, nil)
	if !strings.Contains(strings.Join(sys, "\n"), "info-center loghost 10.0.0.9 port 1514") {
		t.Errorf("非 514 端口的 syslog 应该带上 port: %v", sys)
	}
}

func TestNormalizePushParamsRejectsBadCollector(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	p := pushParams{Kind: "sflow", Collector: "10.0.0.9; reload"}
	if err := normalizePushParams(&p, cfg); err == nil {
		t.Fatal("采集器地址里夹带命令应该被拒绝")
	}
	p = pushParams{Kind: "syslog", Collector: " 10.0.0.9 "}
	if err := normalizePushParams(&p, cfg); err != nil || p.Port != 514 || p.Level != "informational" || p.Collector != "10.0.0.9" {
		t.Fatalf("默认值没补上: %+v err=%v", p, err)
	}
}
