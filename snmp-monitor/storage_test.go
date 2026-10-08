package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoragePrune(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -40).Format("2006-01-02 15:04:05")
	now := time.Now().Format("2006-01-02 15:04:05")

	// 配置备份: 超期的删掉, 但每台最新一份即使超期也保留
	b := newBackupStore(dir, nil)
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	b.data.Settings.KeepDays = 30
	b.data.Metas = map[string][]BackupMeta{
		"d1": {{ID: "a", Time: now}, {ID: "b", Time: old}, {ID: "c", Time: old}},
		"d2": {{ID: "x", Time: old}},
	}
	if n := b.pruneDays(); n != 2 {
		t.Fatalf("backup pruned %d, want 2", n)
	}
	if len(b.data.Metas["d1"]) != 1 || len(b.data.Metas["d2"]) != 1 {
		t.Fatalf("unexpected metas %+v", b.data.Metas)
	}

	// 下发审计日志
	p := filepath.Join(dir, "push_log.jsonl")
	_ = os.WriteFile(p, []byte(`{"time":"`+old+`","ip":"1.1.1.1"}`+"\n"+`{"time":"`+now+`","ip":"2.2.2.2"}`+"\n"), 0o600)
	if n := prunePushLog(p, 30); n != 1 {
		t.Fatalf("push log pruned %d, want 1", n)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "1.1.1.1") || !strings.Contains(string(raw), "2.2.2.2") {
		t.Fatalf("push log = %s", raw)
	}

	parts, total := storageUsage(dir)
	if total <= 0 || len(parts) == 0 {
		t.Fatalf("usage = %v %d", parts, total)
	}
	if free, size, ok := diskSpace(dir); !ok || size == 0 || free > size {
		t.Fatalf("diskSpace = %d %d %v", free, size, ok)
	}
}
