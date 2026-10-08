package main

// 告警接口: 确认 / 关闭 / 全网告警总表 / 告警历史(见 alert_history.go)。

import (
	"net/http"
	"sort"
	"time"
)

// ----------------- 告警确认 -----------------

func (s *server) handleAlertAction(w http.ResponseWriter, r *http.Request, sess *session) {
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"` // ack / unack / close / reopen
	}
	if !s.decode(w, r, &body) {
		return
	}
	var acked, closed bool
	switch body.Action {
	case "ack":
		acked, closed = true, false
	case "close":
		acked, closed = true, true
	case "unack", "reopen":
		acked, closed = false, false
	default:
		s.fail(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err := s.alerts.setAck(body.ID, acked, closed, sess.user); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.alerts.save()
	at := ""
	if acked || closed {
		at = time.Now().Format("2006-01-02 15:04:05")
	}
	s.hub.applyAlert(body.ID, acked, closed, sess.user, at)
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ----------------- 全网告警总表 -----------------
//
// 告警原来只挂在每台设备详情弹窗里的 Criticals/Warnings, 网络大了以后
// 想知道"现在全网到底有哪些没处理的告警"得一台台点开看, 很繁琐。跟全网
// MAC 总表一个思路: 单独做一个跨设备的汇总视图, 按需拉取。

type globalAlertEntry struct {
	Alert
	DeviceName string `json:"device_name"`
}

func (s *server) handleGlobalAlerts(w http.ResponseWriter, r *http.Request) {
	s.hub.mu.RLock()
	reports := s.hub.reports
	s.hub.mu.RUnlock()

	out := make([]globalAlertEntry, 0, 64)
	for _, rep := range reports {
		for _, a := range rep.Criticals {
			out = append(out, globalAlertEntry{Alert: a, DeviceName: rep.SysName})
		}
		for _, a := range rep.Warnings {
			out = append(out, globalAlertEntry{Alert: a, DeviceName: rep.SysName})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		// 未处理的排前面, 同状态内按最近出现时间倒序——最需要关注的排在最上面。
		oi := !out[i].Acked && !out[i].Closed
		oj := !out[j].Acked && !out[j].Closed
		if oi != oj {
			return oi
		}
		if out[i].Level != out[j].Level {
			return out[i].Level == "critical"
		}
		return out[i].LastSeen > out[j].LastSeen
	})
	open := 0
	for _, a := range out {
		if !a.Acked && !a.Closed && !a.Recovered {
			open++
		}
	}
	s.writeJSON(w, 200, map[string]interface{}{"entries": out, "count": len(out), "open_count": open})
}
