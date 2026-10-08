package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type globalSyslogLine struct {
	LogLine
	DeviceIP   string `json:"device_ip"`
	DeviceName string `json:"device_name"`
}

type syslogDeviceCoverage struct {
	IP       string `json:"ip"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Count    int    `json:"count"`
	LastTime string `json:"last_time,omitempty"`
}

// handleSyslog 同时服务设备详情(ip 参数)和平台级日志汇总(不传 ip)。
func (s *server) handleSyslog(w http.ResponseWriter, r *http.Request) {
	port := s.conf().Collectors.SyslogPort
	if s.syslog == nil {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": port > 0, "port": port, "lines": []LogLine{}, "devices": []syslogDeviceCoverage{},
		})
		return
	}

	if ip := strings.TrimSpace(r.URL.Query().Get("ip")); ip != "" {
		// 保持设备详情页面的既有响应格式。
		s.writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": port > 0, "port": port, "lines": s.syslog.get(ip)})
		return
	}

	logs := s.syslog.all()
	devNames := make(map[string]string)
	if s.hub != nil {
		s.hub.mu.RLock()
		for _, rep := range s.hub.reports {
			devNames[rep.IP] = rep.SysName
		}
		s.hub.mu.RUnlock()
	}
	registeredDevices := []DeviceTarget{}
	if s.devices != nil {
		registeredDevices = s.devices.List()
		for _, dev := range registeredDevices {
			if devNames[dev.IP] == "" {
				devNames[dev.IP] = dev.Note
			}
		}
	}

	deviceCounts := make(map[string]*syslogDeviceCoverage)
	for _, dev := range registeredDevices {
		name := devNames[dev.IP]
		if name == "" {
			name = dev.IP
		}
		deviceCounts[dev.IP] = &syslogDeviceCoverage{IP: dev.IP, Name: name, Disabled: dev.Disabled}
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	severity := -1
	if raw := strings.TrimSpace(r.URL.Query().Get("severity")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 && v <= 7 {
			severity = v
		}
	}

	entries := make([]globalSyslogLine, 0)
	var retained int
	for ip, lines := range logs {
		retained += len(lines)
		cov := deviceCounts[ip]
		name := devNames[ip]
		if name == "" {
			name = ip
		}
		for _, line := range lines {
			if cov != nil {
				cov.Count++
				if line.Time > cov.LastTime {
					cov.LastTime = line.Time
				}
			}
			if severity >= 0 && line.Severity != severity {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(ip+" "+name+" "+line.Raw), q) {
				continue
			}
			entries = append(entries, globalSyslogLine{LogLine: line, DeviceIP: ip, DeviceName: name})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].receivedAt != entries[j].receivedAt {
			return entries[i].receivedAt > entries[j].receivedAt
		}
		if entries[i].Time != entries[j].Time {
			return entries[i].Time > entries[j].Time
		}
		return entries[i].DeviceIP < entries[j].DeviceIP
	})

	total := len(entries)
	limit := 5000
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 10000 {
		limit = 10000
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	coverage := make([]syslogDeviceCoverage, 0, len(deviceCounts))
	for _, d := range deviceCounts {
		coverage = append(coverage, *d)
	}
	sort.Slice(coverage, func(i, j int) bool { return ipLess(coverage[i].IP, coverage[j].IP) })

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": port > 0, "port": port, "lines": entries, "devices": coverage,
		"total": total, "retained": retained, "returned": len(entries), "per_device_cap": s.syslog.cap.Load(),
	})
}
