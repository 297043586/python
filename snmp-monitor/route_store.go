package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// routeSnapshotSchema 在路由解码规则变化时递增。旧版本保存的快照
// (例如 IP-FORWARD-MIB 列号错位时解码出的 -1 协议)不会再被当作有效缓存直接显示,
// 首次打开时自动重新读取;仅在重新读取失败时才作为兜底数据显示。
const routeSnapshotSchema = 2

// RouteSnapshot 是最近一次主动读取并保存的设备路由表。
type RouteSnapshot struct {
	Schema    int          `json:"schema"`
	IP        string       `json:"ip"`
	Routes    []RouteEntry `json:"routes"`
	Source    string       `json:"source"`
	Message   string       `json:"message,omitempty"` // 采集时的诊断说明
	Timestamp string       `json:"timestamp"`
}

func (s RouteSnapshot) current() bool { return s.Schema == routeSnapshotSchema }

type routeSnapshotFile struct {
	Snapshots map[string]RouteSnapshot `json:"snapshots"`
}

// RouteStore 将路由表缓存写入 data/routes.json。缓存键包含凭据指纹，
// 不把 SNMP community 明文写入磁盘，也不会把更换凭据前的数据当成新设备数据。
type RouteStore struct {
	mu        sync.RWMutex
	path      string
	snapshots map[string]RouteSnapshot
	refreshMu map[string]*sync.Mutex
}

func newRouteStore(dir string) *RouteStore {
	return &RouteStore{
		path:      filepath.Join(dir, "routes.json"),
		snapshots: make(map[string]RouteSnapshot),
		refreshMu: make(map[string]*sync.Mutex),
	}
}

func (s *RouteStore) load() error {
	var file routeSnapshotFile
	if err := readJSON(s.path, &file); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if file.Snapshots == nil {
		file.Snapshots = make(map[string]RouteSnapshot)
	}
	s.snapshots = file.Snapshots
	return nil
}

func (s *RouteStore) get(key string) (RouteSnapshot, bool) {
	if s == nil {
		return RouteSnapshot{}, false
	}
	s.mu.RLock()
	snapshot, ok := s.snapshots[key]
	s.mu.RUnlock()
	if !ok {
		return RouteSnapshot{}, false
	}
	snapshot.Routes = append([]RouteEntry(nil), snapshot.Routes...)
	return snapshot, true
}

func (s *RouteStore) put(key string, snapshot RouteSnapshot) error {
	if s == nil {
		return nil
	}
	snapshot.Routes = append([]RouteEntry(nil), snapshot.Routes...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[key] = snapshot
	return atomicWriteJSON(s.path, routeSnapshotFile{Snapshots: s.snapshots}, 0o600)
}

// lockRefresh 将首次读取合并为一次 SNMP 操作，避免多个页面同时进入时重复 walk。
func (s *RouteStore) lockRefresh(key string) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	lock := s.refreshMu[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.refreshMu[key] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func routeCacheKey(target DeviceTarget) string {
	identity := strings.Join([]string{target.ID, target.IP, target.Version, target.Community}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func newRouteSnapshot(ip, source string, routes []RouteEntry) RouteSnapshot {
	return RouteSnapshot{
		Schema: routeSnapshotSchema, IP: ip, Source: source, Routes: append([]RouteEntry(nil), routes...),
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	}
}
