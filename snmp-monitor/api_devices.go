package main

// 设备管理接口: 增删改 / 测试连接。

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ----------------- 设备管理 -----------------

type deviceForm struct {
	ID                  string `json:"id"`
	IP                  string `json:"ip"`
	Community           string `json:"community"`
	Brand               string `json:"brand"`
	Version             string `json:"version"`
	Note                string `json:"note"`
	Group               string `json:"group"`
	Location            string `json:"location"`
	Contact             string `json:"contact"`
	Disabled            bool   `json:"disabled"`
	VlanCommunityFormat string `json:"vlan_community_format"`

	// 前端用正向勾选框("采集光衰" 打钩=true), 内部用 Skip* 语义存储,
	// 这里做一次转换, 别处都不用惦记这个反转。
	CollectOptical bool `json:"collect_optical"`
	CollectLLDP    bool `json:"collect_lldp"`
	CollectRates   bool `json:"collect_rates"`
	CollectHosts   bool `json:"collect_hosts"`
	// 用指针区分"没传"(旧页面缓存, 按采集处理)和"传了 false"。
	CollectExtended *bool `json:"collect_extended"`
}

func (f deviceForm) target() DeviceTarget {
	return DeviceTarget{
		ID: f.ID, IP: f.IP, Community: f.Community, Brand: f.Brand,
		Version: f.Version, Note: f.Note, Group: f.Group, Location: f.Location, Contact: f.Contact, Disabled: f.Disabled,
		VlanCommunityFormat: f.VlanCommunityFormat,
		Collect: CollectOptions{
			SkipOptical: !f.CollectOptical,
			SkipLLDP:    !f.CollectLLDP,
			SkipRates:   !f.CollectRates,
			SkipHosts:   !f.CollectHosts,

			SkipExtended: f.CollectExtended != nil && !*f.CollectExtended,
		},
	}
}

func (s *server) handleDeviceAdd(w http.ResponseWriter, r *http.Request, sess *session) {
	var f deviceForm
	if !s.decode(w, r, &f) {
		return
	}
	dev, err := s.devices.Add(f.target())
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	fmt.Printf("➕ [%s] 新增监控节点 %s (由 %s 添加)\n",
		time.Now().Format("15:04:05"), dev.IP, sess.user)
	s.triggerRound()
	// 响应里只回 ID, 不回显团体名
	s.writeJSON(w, 200, map[string]string{"status": "ok", "id": dev.ID})
}

func (s *server) handleDeviceUpdate(w http.ResponseWriter, r *http.Request, sess *session) {
	var f deviceForm
	if !s.decode(w, r, &f) {
		return
	}
	if err := s.devices.Update(f.ID, f.target()); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.triggerRound()
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) handleDeviceDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	var f deviceForm
	if !s.decode(w, r, &f) {
		return
	}
	dev, ok := s.devices.Get(f.ID)
	if !ok {
		s.fail(w, http.StatusBadRequest, "设备不存在")
		return
	}
	if err := s.devices.Delete(f.ID); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.alerts.dropDevice(dev.stateKey())
	if s.syslog != nil {
		s.syslog.drop(dev.IP)
	}
	if s.flows != nil {
		s.flows.drop(dev.IP)
	}
	s.topology.DropDevice(dev.IP)
	fmt.Printf("➖ [%s] 移除监控节点 %s (由 %s 操作)\n",
		time.Now().Format("15:04:05"), dev.IP, sess.user)
	s.triggerRound()
	s.writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDeviceTest 只做一次 sysName 探测, 不落盘。
// 用于添加前先验证团体名是否正确, 省得加进去才发现全是离线。
func (s *server) handleDeviceTest(w http.ResponseWriter, r *http.Request, sess *session) {
	var f deviceForm
	if !s.decode(w, r, &f) {
		return
	}
	dev := f.target()
	if dev.Community == "" && f.ID != "" {
		if existing, ok := s.devices.Get(f.ID); ok {
			dev.Community = existing.Community
		}
	}
	if err := validateDevice(&dev); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if dev.Community == "" {
		s.fail(w, http.StatusBadRequest, "请填写团体名")
		return
	}

	sess2, err := dialSNMP(dev, s.conf())
	if err != nil {
		s.writeJSON(w, 200, map[string]interface{}{"ok": false, "message": "无法建立连接: 端口不可达"})
		return
	}
	defer func() {
		if sess2.c.Conn != nil {
			_ = sess2.c.Conn.Close()
		}
	}()
	res, err := sess2.c.Get([]string{oidSysName, oidSysObjectID, oidSysDescr})
	if err != nil || res == nil || len(res.Variables) == 0 {
		s.writeJSON(w, 200, map[string]interface{}{"ok": false, "message": "设备无响应, 请检查团体名与 ACL"})
		return
	}
	var name, oid, descr string
	for _, v := range res.Variables {
		val := strings.TrimPrefix(v.Name, ".")
		switch {
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.5"):
			name = shortSysName(decodeText(v.Value))
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.2"):
			oid = fmt.Sprintf("%v", v.Value)
		case strings.HasPrefix(val, "1.3.6.1.2.1.1.1"):
			descr = decodeText(v.Value)
		}
	}
	s.writeJSON(w, 200, map[string]interface{}{
		"ok": true, "sys_name": name, "brand_detected": detectBrand(oid, descr),
	})
}
