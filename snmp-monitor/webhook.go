package main

// 告警外发(Webhook)。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ----------------- 告警外发 -----------------

func pushWebhook(cfg *Config, reports []DeviceReport) {
	if cfg.WebhookURL == "" {
		return
	}
	var lines []string
	stateLock.Lock()
	for _, r := range reports {
		prev := lastCriticals[r.stateKey]
		cur := make(map[string]bool, len(r.Criticals))
		for _, c := range r.Criticals {
			cur[c.ID] = true
			// 只推新增且未被确认的告警, 避免每轮刷屏
			if !prev[c.ID] && !c.Acked && !c.Closed {
				lines = append(lines, fmt.Sprintf("【%s %s】%s", r.SysName, r.IP, c.Text))
			}
		}
		lastCriticals[r.stateKey] = cur
	}
	stateLock.Unlock()
	if len(lines) == 0 {
		return
	}

	body, _ := json.Marshal(map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": "网络巡检新增告警:\n" + strings.Join(lines, "\n")},
	})
	cl := &http.Client{Timeout: 8 * time.Second}
	resp, err := cl.Post(cfg.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("⚠️  告警推送失败: %v\n", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
