package main

// 告警历史接口(数据见 alert_history.go): 查询 + 统计 / 单条详情(处理过程) / 导出 CSV。
// 跟告警总表一样是只读的公开接口。

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// parseHistQuery 解析历史查询参数:
//
//	from / to   Unix 秒, 或 2006-01-02 / 2006-01-02 15:04(to 只写日期时算到当天结束); 默认最近 7 天
//	device / level / category / status   逗号分隔多选(status: active / recovered / handled / unhandled)
//	q           关键字(设备 / 对象 / 内容 / 处理人)
//	sort        start / -start / duration / -duration / device / level / flaps
//	offset / limit
func parseHistQuery(r *http.Request, now time.Time) alertHistQuery {
	v := r.URL.Query()
	parseT := func(s string, endOfDay bool) int64 {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				if layout == "2006-01-02" && endOfDay {
					t = t.AddDate(0, 0, 1).Add(-time.Second)
				}
				return t.Unix()
			}
		}
		return 0
	}
	set := func(k string) map[string]bool {
		out := map[string]bool{}
		for _, x := range strings.Split(v.Get(k), ",") {
			if x = strings.TrimSpace(x); x != "" {
				out[x] = true
			}
		}
		return out
	}
	q := alertHistQuery{
		From: parseT(v.Get("from"), false), To: parseT(v.Get("to"), true),
		Devices: set("device"), Levels: set("level"), Cats: set("category"), Status: set("status"),
		Q: v.Get("q"), Sort: v.Get("sort"),
	}
	if q.From == 0 && v.Get("from") == "" {
		q.From = now.AddDate(0, 0, -7).Unix()
	}
	q.Offset, _ = strconv.Atoi(v.Get("offset"))
	q.Limit, _ = strconv.Atoi(v.Get("limit"))
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Limit <= 0 || q.Limit > 5000 {
		q.Limit = 200
	}
	return q
}

func (s *server) handleAlertHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		s.writeJSON(w, 200, map[string]interface{}{"items": []histItem{}, "total": 0})
		return
	}
	q := parseHistQuery(r, time.Now())
	items, total, st := s.history.query(q)
	if items == nil {
		items = []histItem{}
	}
	s.writeJSON(w, 200, map[string]interface{}{
		"items": items, "total": total, "stats": st, "from": q.From, "to": q.To, "offset": q.Offset, "limit": q.Limit,
		"retention_days": s.conf().AlertHistoryDays,
	})
}

func (s *server) handleAlertHistoryDetail(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		s.fail(w, 404, "没有告警历史")
		return
	}
	inc, evs, sib, ok := s.history.detail(r.URL.Query().Get("id"))
	if !ok {
		s.fail(w, 404, "这条记录不存在(可能已超过保留期)")
		return
	}
	if evs == nil {
		evs = []AlertEvent{}
	}
	s.writeJSON(w, 200, map[string]interface{}{"incident": inc, "events": evs, "occurrences": sib})
}

// handleAlertHistoryExport 按当前筛选条件导出 CSV(带 BOM, Excel 直接打开不乱码)。
func (s *server) handleAlertHistoryExport(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		s.fail(w, 404, "没有告警历史")
		return
	}
	q := parseHistQuery(r, time.Now())
	q.Offset, q.Limit = 0, 100000
	items, _, _ := s.history.query(q)
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // BOM
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"开始时间", "恢复时间", "持续", "级别", "设备", "设备IP", "类别", "对象", "内容", "状态", "处理", "处理人", "处理时间", "同类出现次数", "备注"})
	ts := func(v int64) string {
		if v == 0 {
			return ""
		}
		return time.Unix(v, 0).Format("2006-01-02 15:04:05")
	}
	for _, it := range items {
		level, state, handled := "风险隐患", "已恢复", "未处理"
		if it.Level == "critical" {
			level = "致命故障"
		}
		if it.End == 0 {
			state = "告警中"
		}
		if it.AckedBy != "" {
			handled = "已确认"
			if it.Closed {
				handled = "已关闭"
			}
		}
		_ = cw.Write([]string{ts(it.Start), ts(it.End), fmtDuration(it.Duration), level, it.DeviceName, it.DeviceIP, alertCategoryName(it.Category),
			it.Subject, it.Text, state, handled, it.AckedBy, ts(it.AckedAt), strconv.Itoa(it.Flaps), it.Note})
	}
	cw.Flush()
	name := "告警历史_" + time.Unix(q.From, 0).Format("20060102") + "-" + time.Now().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8"+`''`+url.PathEscape(name))
	_, _ = w.Write(buf.Bytes())
}

// alertCategoryName 告警类别的中文名(跟前端 ALERT_CATEGORY_CN 一致)。
func alertCategoryName(c string) string {
	if n, ok := map[string]string{
		"unreachable": "设备离线/不可达", "reboot": "设备重启", "internal": "采集程序异常",
		"cpu": "CPU 负载", "mem": "内存占用", "fan": "风扇故障", "psu": "电源故障", "temp": "温度异常",
		"port_err": "端口错包", "optical": "光功率异常", "bandwidth": "带宽跑满", "duplex": "双工不匹配",
		"broadcast": "广播风暴", "stp": "生成树频繁变化",
	}[c]; ok {
		return n
	}
	return c
}
