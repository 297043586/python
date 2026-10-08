package main

// 存储占用与保留策略(v14)。
//
// 系统设置「💽 存储」页: 看数据目录所在磁盘还剩多少、data/ 下各类数据各占多少,
// 并统一设置各类数据"存多少天 / 多少份":
//   - 配置备份: 每台保留份数(原来就有) + 保留天数(新, 0 = 不按天清; 每台最新一份永远保留)
//   - 巡检报告: 最多保留份数(原来就有) + 保留天数(新)
//   - 配置下发审计日志: 保留天数(新, data/push_log.jsonl)
//   - 告警历史 / 趋势曲线: 仍在「告警」「采集」页里改, 这里只显示
// 按天清理由 storageJanitor 每 6 小时跑一次, 保存设置后也立即跑一次。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// pushLogMu: 审计日志追加(pushAudit)和按天裁剪(prunePushLog)互斥。
var pushLogMu sync.Mutex

type storagePart struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Files int    `json:"files"`
}

// storageParts: data/ 下第一层的名字 → 分类。没列出来的都算"其他"。
var storagePartNames = map[string]string{
	"alert_history":  "告警历史",
	"backups":        "配置备份",
	"reports":        "巡检报告",
	"history.json":   "趋势曲线 / 速率基线",
	"routes.json":    "已保存路由表",
	"push_log.jsonl": "配置下发审计日志",
	"crash.log":      "崩溃日志",
}

func storageUsage(dataDir string) (parts []storagePart, total int64) {
	by := map[string]*storagePart{}
	_ = filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dataDir, p)
		first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		key := first
		if _, ok := storagePartNames[key]; !ok {
			key = "other"
		}
		sp := by[key]
		if sp == nil {
			sp = &storagePart{Key: key, Name: storagePartNames[key]}
			if key == "other" {
				sp.Name = "其他(设备列表 / 拓扑 / 机房布局 / 账号等)"
			}
			by[key] = sp
		}
		sp.Size += info.Size()
		sp.Files++
		total += info.Size()
		return nil
	})
	for _, sp := range by {
		parts = append(parts, *sp)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Size > parts[j].Size })
	return parts, total
}

// ----------------- 按天清理 -----------------

func olderThan(ts string, cutoff time.Time) bool {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local)
	return err == nil && t.Before(cutoff)
}

// pruneDays: 每台设备删掉超过 KeepDays 天的版本, 最新一份不删(否则长期不变的配置会被清空)。
func (b *BackupStore) pruneDays() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	days := b.data.Settings.KeepDays
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	n := 0
	for id, list := range b.data.Metas {
		keep := list[:0:0]
		for i, m := range list {
			if i > 0 && olderThan(m.Time, cutoff) {
				_ = os.Remove(b.cfgPath(id, m.ID))
				n++
				continue
			}
			keep = append(keep, m)
		}
		b.data.Metas[id] = keep
	}
	if n > 0 {
		_ = b.saveLocked()
	}
	return n
}

func (s *ReportStore) pruneDays() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	days := s.settings.KeepDays
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	keep := s.index[:0:0]
	n := 0
	for _, m := range s.index {
		if olderThan(m.CreatedAt, cutoff) {
			_ = os.Remove(s.reportPath(m.ID))
			n++
			continue
		}
		keep = append(keep, m)
	}
	s.index = keep
	return n
}

func prunePushLog(path string, days int) int {
	if days <= 0 {
		return 0
	}
	pushLogMu.Lock()
	defer pushLogMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	var out bytes.Buffer
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var rec struct {
			Time string `json:"time"`
		}
		if json.Unmarshal(line, &rec) == nil && olderThan(rec.Time, cutoff) {
			n++
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if n == 0 || sc.Err() != nil {
		return 0
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, out.Bytes(), 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		return 0
	}
	return n
}

func (s *server) runStorageJanitor() string {
	var msgs []string
	if n := s.backups.pruneDays(); n > 0 {
		msgs = append(msgs, fmt.Sprintf("配置备份 %d 份", n))
	}
	if n := s.reports.pruneDays(); n > 0 {
		msgs = append(msgs, fmt.Sprintf("巡检报告 %d 份", n))
	}
	if n := prunePushLog(filepath.Join(s.conf().DataDir, "push_log.jsonl"), s.backups.Settings().PushLogDays); n > 0 {
		msgs = append(msgs, fmt.Sprintf("下发审计日志 %d 条", n))
	}
	if len(msgs) > 0 {
		fmt.Printf("🧹 按保留天数清理: %s\n", strings.Join(msgs, ", "))
	}
	if free, total, ok := diskSpace(s.conf().DataDir); ok && total > 0 && (float64(free)/float64(total) < 0.05 || free < 1<<30) {
		fmt.Printf("⚠️  数据目录所在磁盘剩余空间不足: 剩 %s / 共 %s\n", humanBytes(free), humanBytes(total))
	}
	return strings.Join(msgs, ", ")
}

func (s *server) storageJanitor() {
	time.Sleep(time.Minute)
	for {
		s.runStorageJanitor()
		time.Sleep(6 * time.Hour)
	}
}

func humanBytes(n uint64) string {
	f, u := float64(n), []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, u[i])
}

// ----------------- API -----------------

type storageRetention struct {
	BackupKeep     int `json:"backup_keep"`
	BackupKeepDays int `json:"backup_keep_days"`
	ReportKeep     int `json:"report_keep"`
	ReportKeepDays int `json:"report_keep_days"`
	PushLogDays    int `json:"push_log_days"`
}

// handleStorage: GET 看占用和当前保留设置; POST 保存保留设置并立即清理一次。
func (s *server) handleStorage(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodPost {
		var req storageRetention
		if !s.decode(w, r, &req) {
			return
		}
		switch {
		case req.BackupKeep < 1 || req.BackupKeep > 1000:
			s.fail(w, 400, "配置备份每台保留份数需要在 1~1000")
			return
		case req.ReportKeep < 1 || req.ReportKeep > 100000:
			s.fail(w, 400, "巡检报告保留份数需要在 1~100000")
			return
		case req.BackupKeepDays < 0 || req.ReportKeepDays < 0 || req.PushLogDays < 0 ||
			req.BackupKeepDays > 36500 || req.ReportKeepDays > 36500 || req.PushLogDays > 36500:
			s.fail(w, 400, "保留天数需要在 0~36500(0 = 不按天清理)")
			return
		}
		b := s.backups
		b.mu.Lock()
		b.data.Settings.Keep, b.data.Settings.KeepDays, b.data.Settings.PushLogDays = req.BackupKeep, req.BackupKeepDays, req.PushLogDays
		// 份数调小了: 现在就把多出来的删掉, 不用等下次备份
		for id, list := range b.data.Metas {
			for len(list) > req.BackupKeep {
				_ = os.Remove(b.cfgPath(id, list[len(list)-1].ID))
				list = list[:len(list)-1]
			}
			b.data.Metas[id] = list
		}
		err := b.saveLocked()
		b.mu.Unlock()
		if err == nil {
			err = s.reports.UpdateSettings(func(x *ReportSettings) { x.Keep, x.KeepDays = req.ReportKeep, req.ReportKeepDays })
		}
		if err != nil {
			s.fail(w, 500, "保存失败: "+err.Error())
			return
		}
		s.reports.trimKeep()
		msg := s.runStorageJanitor()
		fmt.Printf("🛠️  %s 修改了数据保留设置\n", sess.user)
		s.writeJSON(w, 200, map[string]interface{}{"ok": true, "pruned": msg})
		return
	}
	dir := s.conf().DataDir
	abs, _ := filepath.Abs(dir)
	parts, total := storageUsage(dir)
	out := map[string]interface{}{"data_dir": abs, "parts": parts, "total": total}
	if free, size, ok := diskSpace(dir); ok {
		out["disk"] = map[string]uint64{"free": free, "total": size}
	}
	bs, rs := s.backups.Settings(), s.reports.Settings()
	out["retention"] = storageRetention{BackupKeep: bs.Keep, BackupKeepDays: bs.KeepDays, ReportKeep: rs.Keep, ReportKeepDays: rs.KeepDays, PushLogDays: bs.PushLogDays}
	s.writeJSON(w, 200, out)
}
