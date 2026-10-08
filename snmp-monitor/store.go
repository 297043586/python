package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ----------------- 落盘基础设施 -----------------

// atomicWriteJSON 先写临时文件再 rename, 避免进程在写一半时被杀导致文件损坏。
func atomicWriteJSON(path string, v interface{}, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v interface{}) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, v)
}

// ----------------- 凭据加密 -----------------
//
// 团体名(community)是能直接读取全网设备的凭据, 不能明文落在磁盘上。
// 这里用 data/secret.key 里的随机密钥做 AES-256-GCM 加密, 密钥文件权限 0600。

type secretBox struct {
	aead cipher.AEAD
}

func loadSecretBox(dir string) (*secretBox, error) {
	path := filepath.Join(dir, "secret.key")
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
		fmt.Printf("🔑 已生成新的凭据加密密钥: %s (请随数据目录一起备份, 丢失后需重新录入团体名)\n", path)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

func (s *secretBox) seal(plain string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (s *secretBox) open(blob string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", err
	}
	if len(raw) < s.aead.NonceSize() {
		return "", fmt.Errorf("密文长度异常")
	}
	nonce, body := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败 (secret.key 与数据文件不匹配?)")
	}
	return string(plain), nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ----------------- 设备库 -----------------

// PublicDevice 是给前端看的设备信息。
// 刻意不含 Community —— 团体名只在内存里参与 SNMP 请求, 既不写进接口响应,
// 也不写进日志, 落盘时是密文。
type PublicDevice struct {
	ID                  string         `json:"id"`
	IP                  string         `json:"ip"`
	Brand               string         `json:"brand"`
	Version             string         `json:"version"`
	Note                string         `json:"note"`
	Group               string         `json:"group"`
	Location            string         `json:"location"`
	Contact             string         `json:"contact"`
	Disabled            bool           `json:"disabled"`
	HasCommunity        bool           `json:"has_community"`
	VlanCommunityFormat string         `json:"vlan_community_format"`
	Collect             CollectOptions `json:"collect"`
}

type storedDevice struct {
	ID                  string         `json:"id"`
	IP                  string         `json:"ip"`
	Brand               string         `json:"brand"`
	Version             string         `json:"version"`
	Note                string         `json:"note"`
	Group               string         `json:"group,omitempty"`
	Location            string         `json:"location,omitempty"`
	Contact             string         `json:"contact,omitempty"`
	Disabled            bool           `json:"disabled"`
	VlanCommunityFormat string         `json:"vlan_community_format"`
	Collect             CollectOptions `json:"collect"`
	CommunityEnc        string         `json:"community_enc"`
}

type DeviceStore struct {
	mu      sync.RWMutex
	path    string
	box     *secretBox
	devices []DeviceTarget
}

func newDeviceStore(dir string, box *secretBox) *DeviceStore {
	return &DeviceStore{path: filepath.Join(dir, "devices.json"), box: box}
}

func (d *DeviceStore) load() error {
	var stored []storedDevice
	if err := readJSON(d.path, &stored); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.devices = nil
	for _, sd := range stored {
		community := ""
		if sd.CommunityEnc != "" {
			plain, err := d.box.open(sd.CommunityEnc)
			if err != nil {
				fmt.Printf("⚠️  设备 %s 的团体名解密失败: %v\n", sd.IP, err)
				continue
			}
			community = plain
		}
		d.devices = append(d.devices, DeviceTarget{
			ID: sd.ID, IP: sd.IP, Community: community, Brand: sd.Brand,
			Version: sd.Version, Note: sd.Note, Group: sd.Group, Location: sd.Location, Contact: sd.Contact, Disabled: sd.Disabled,
			VlanCommunityFormat: sd.VlanCommunityFormat, Collect: sd.Collect,
		})
	}
	return nil
}

// saveLocked 调用方必须已持有写锁
func (d *DeviceStore) saveLocked() error {
	stored := make([]storedDevice, 0, len(d.devices))
	for _, dev := range d.devices {
		enc, err := d.box.seal(dev.Community)
		if err != nil {
			return err
		}
		stored = append(stored, storedDevice{
			ID: dev.ID, IP: dev.IP, Brand: dev.Brand, Version: dev.Version,
			Note: dev.Note, Group: dev.Group, Location: dev.Location, Contact: dev.Contact, Disabled: dev.Disabled, CommunityEnc: enc,
			VlanCommunityFormat: dev.VlanCommunityFormat, Collect: dev.Collect,
		})
	}
	return atomicWriteJSON(d.path, stored, 0o600)
}

func (d *DeviceStore) List() []DeviceTarget {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]DeviceTarget(nil), d.devices...)
}

// Active 返回本轮要巡检的设备(跳过被停用的)
func (d *DeviceStore) Active() []DeviceTarget {
	out := make([]DeviceTarget, 0)
	for _, dev := range d.List() {
		if !dev.Disabled {
			out = append(out, dev)
		}
	}
	return out
}

func (d *DeviceStore) Public() []PublicDevice {
	list := d.List()
	sort.Slice(list, func(i, j int) bool { return ipLess(list[i].IP, list[j].IP) })
	out := make([]PublicDevice, 0, len(list))
	for _, dev := range list {
		out = append(out, PublicDevice{
			ID: dev.ID, IP: dev.IP, Brand: dev.Brand, Version: dev.Version,
			Note: dev.Note, Group: dev.Group, Location: dev.Location, Contact: dev.Contact, Disabled: dev.Disabled, HasCommunity: dev.Community != "",
			VlanCommunityFormat: dev.VlanCommunityFormat, Collect: dev.Collect,
		})
	}
	return out
}

// groupsByIP: 设备 IP -> 分组名(没分组的不在里面)。拓扑 / 大屏按分组着色时用, 改了分组立即生效。
func (d *DeviceStore) groupsByIP() map[string]string {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]string, len(d.devices))
	for _, dev := range d.devices {
		if dev.Group != "" {
			out[dev.IP] = dev.Group
		}
	}
	return out
}

func (d *DeviceStore) Get(id string) (DeviceTarget, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, dev := range d.devices {
		if dev.ID == id {
			return dev, true
		}
	}
	return DeviceTarget{}, false
}

var hostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.]{0,61}[a-zA-Z0-9])?$`)

func validateDevice(dev *DeviceTarget) error {
	dev.IP = strings.TrimSpace(dev.IP)
	dev.Brand = strings.ToLower(strings.TrimSpace(dev.Brand))
	dev.Version = strings.TrimSpace(dev.Version)
	dev.Note = strings.TrimSpace(dev.Note)
	dev.Group = strings.TrimSpace(dev.Group)

	if dev.IP == "" {
		return fmt.Errorf("设备地址不能为空")
	}
	if net.ParseIP(dev.IP) == nil && !hostRe.MatchString(dev.IP) {
		return fmt.Errorf("设备地址格式不合法")
	}
	if len(dev.Note) > 120 {
		return fmt.Errorf("备注过长")
	}
	if len([]rune(dev.Group)) > 40 {
		return fmt.Errorf("分组名过长(最多 40 个字)")
	}
	// 品牌: 自动 / 通用 / 厂商规则里有的(包括页面上新增的品牌, 见 vendors.go)
	if dev.Brand != "" && dev.Brand != "auto" && dev.Brand != "generic" && !knownBrand(dev.Brand) {
		return fmt.Errorf("厂商 %q 不认识: 请选「自动识别」, 或先在「设置 → 厂商 / OID」里添加这个品牌", dev.Brand)
	}
	if dev.Brand == "" {
		dev.Brand = "auto"
	}
	if dev.Version != "1" {
		dev.Version = "2c"
	}
	if len(dev.Community) > 128 {
		return fmt.Errorf("团体名过长")
	}
	return nil
}

func (d *DeviceStore) Add(dev DeviceTarget) (DeviceTarget, error) {
	if err := validateDevice(&dev); err != nil {
		return dev, err
	}
	if dev.Community == "" {
		return dev, fmt.Errorf("团体名不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.devices {
		if x.IP == dev.IP {
			return dev, fmt.Errorf("该地址已存在")
		}
	}
	if len(d.devices) >= 2000 {
		return dev, fmt.Errorf("设备数量已达上限")
	}
	dev.ID = randomID()
	d.devices = append(d.devices, dev)
	return dev, d.saveLocked()
}

// Update 中 community 为空表示"不修改", 这样前端永远不需要把现有团体名回传。
func (d *DeviceStore) Update(id string, patch DeviceTarget) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.devices {
		if x.ID != id {
			continue
		}
		next := x
		next.IP = patch.IP
		next.Brand = patch.Brand
		next.Version = patch.Version
		next.Note = patch.Note
		next.Group = patch.Group
		next.Location = patch.Location
		next.Contact = patch.Contact
		next.Disabled = patch.Disabled
		next.VlanCommunityFormat = patch.VlanCommunityFormat
		next.Collect = patch.Collect
		if patch.Community != "" {
			next.Community = patch.Community
		}
		if err := validateDevice(&next); err != nil {
			return err
		}
		for j, y := range d.devices {
			if j != i && y.IP == next.IP {
				return fmt.Errorf("该地址已存在")
			}
		}
		d.devices[i] = next
		return d.saveLocked()
	}
	return fmt.Errorf("设备不存在")
}

func (d *DeviceStore) Delete(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.devices {
		if x.ID == id {
			d.devices = append(d.devices[:i], d.devices[i+1:]...)
			return d.saveLocked()
		}
	}
	return fmt.Errorf("设备不存在")
}

// importFromConfig 只在设备库为空时把 config.json 里的 devices 导入一次。
// 之后以设备库为准, 免得在线添加的节点被配置文件覆盖掉。
func (d *DeviceStore) importFromConfig(devices []DeviceTarget) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.devices) > 0 {
		return 0, nil
	}
	n := 0
	seenIP := make(map[string]bool, len(devices))
	for _, dev := range devices {
		copied := dev
		if err := validateDevice(&copied); err != nil {
			fmt.Printf("⚠️  配置里的设备 %s 被跳过: %v\n", dev.IP, err)
			continue
		}
		// 同一个 IP 在 devices 数组里重复出现(配置文件手误/复制粘贴导致),
		// 只留第一条; 重复地址在数据模型里没法区分成两台不同设备, 悄悄
		// 导进去只会让"点开卡片打开哪一台"变得不确定。
		if seenIP[copied.IP] {
			fmt.Printf("⚠️  配置里的设备 %s 地址重复, 已跳过后面这条\n", copied.IP)
			continue
		}
		seenIP[copied.IP] = true
		copied.ID = randomID()
		d.devices = append(d.devices, copied)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, d.saveLocked()
}

// ----------------- 告警状态 -----------------

type AlertStore struct {
	mu    sync.RWMutex
	path  string
	state map[string]Alert
	dirty bool
	hist  *AlertHistory // 告警历史(可为 nil), 见 alert_history.go
}

func newAlertStore(dir string) *AlertStore {
	return &AlertStore{path: filepath.Join(dir, "alerts.json"), state: map[string]Alert{}}
}

func (a *AlertStore) load() error {
	var m map[string]Alert
	if err := readJSON(a.path, &m); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if m != nil {
		a.state = m
	}
	return nil
}

func (a *AlertStore) save() {
	a.hist.flush()
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	snapshot := make(map[string]Alert, len(a.state))
	for k, v := range a.state {
		snapshot[k] = v
	}
	a.dirty = false
	a.mu.Unlock()
	if err := atomicWriteJSON(a.path, snapshot, 0o600); err != nil {
		fmt.Printf("⚠️  告警状态落盘失败: %v\n", err)
	}
}

// alertRecoveryGrace: 已恢复的告警继续保留给用户看的时长, 超过之后才
// 彻底清掉。不然一个只出现了一轮就恢复的小问题会在下一轮巡检后悄无
// 声息地消失, 用户根本没机会看到"曾经发生过什么"。
const alertRecoveryGrace = 24 * time.Hour

// Sync 把本轮算出的告警和已保存的确认状态合并。
//
// 规则:
//   - 同一个 ID 再次出现 -> 沿用 FirstSeen 和确认/关闭状态, 若之前标记过
//     "已恢复"则清掉该标记(说明问题又犯了, 按新一次故障对待)
//   - 本轮没再出现 -> 说明故障已恢复, 不会立刻丢弃, 而是标记 Recovered
//     并继续在本轮结果里带出去, 保留 24 小时(或直到用户手动确认/关闭)
//     之后才彻底清掉; 这期间再次触发会覆盖掉恢复标记, 重新按活跃告警处理
func (a *AlertStore) Sync(rep *DeviceReport) {
	now := time.Now().Format("2006-01-02 15:04:05")
	a.mu.Lock()
	defer a.mu.Unlock()

	active := make(map[string]bool)
	fix := func(list []Alert) []Alert {
		for i := range list {
			cur := list[i]
			active[cur.ID] = true
			// old.Recovered=true 说明这个 ID 中间恢复过、现在是重新犯了——
			// 按新一次故障对待(不沿用旧的确认/关闭状态), 跟旧版" 恢复后
			// 再次发生按新告警提醒"的语义保持一致; 只有从未恢复过、连续
			// 活跃的告警才沿用确认状态。
			if old, ok := a.state[cur.ID]; ok && !old.Recovered {
				cur.FirstSeen = old.FirstSeen
				cur.Acked, cur.AckedBy, cur.AckedAt = old.Acked, old.AckedBy, old.AckedAt
				cur.Closed = old.Closed
			} else {
				cur.FirstSeen = now
				a.dirty = true
			}
			cur.LastSeen = now
			cur.Recovered, cur.RecoveredAt = false, ""
			a.state[cur.ID] = cur
			list[i] = cur
			a.hist.observe(cur, rep.SysName)
		}
		return list
	}
	rep.Criticals = fix(rep.Criticals)
	rep.Warnings = fix(rep.Warnings)
	a.hist.settleDevice(rep.stateKey, active)

	// 本设备本轮没再出现的告警: 标记/更新恢复状态, 过了保留期才真正清掉;
	// 保留期内的继续带回本轮结果里, 这样详情页的告警列表才能看到它们。
	for id, al := range a.state {
		if al.DeviceID != rep.stateKey || active[id] {
			continue
		}
		if al.Recovered {
			recoveredAt, err := time.ParseInLocation("2006-01-02 15:04:05", al.RecoveredAt, time.Local)
			if err != nil || time.Since(recoveredAt) > alertRecoveryGrace {
				delete(a.state, id)
				a.dirty = true
				continue
			}
		} else {
			al.Recovered = true
			al.RecoveredAt = now
			a.state[id] = al
			a.dirty = true
		}
		if !al.Acked && !al.Closed {
			if al.Level == "critical" {
				rep.Criticals = append(rep.Criticals, al)
			} else {
				rep.Warnings = append(rep.Warnings, al)
			}
		}
	}

	open := 0
	for _, l := range [][]Alert{rep.Criticals, rep.Warnings} {
		for _, al := range l {
			if !al.Acked && !al.Closed && !al.Recovered {
				open++
			}
		}
	}
	rep.OpenAlerts = open
}

func (a *AlertStore) setAck(id string, acked, closed bool, user string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	al, ok := a.state[id]
	if !ok {
		return fmt.Errorf("该告警已恢复或不存在")
	}
	action := "unack"
	switch {
	case acked && closed:
		action = "close"
	case acked:
		action = "ack"
	case al.Closed:
		action = "reopen"
	}
	a.hist.action(id, action, user)
	al.Acked = acked
	al.Closed = closed
	if acked || closed {
		al.AckedBy = user
		al.AckedAt = time.Now().Format("2006-01-02 15:04:05")
	} else {
		al.AckedBy, al.AckedAt = "", ""
	}
	a.state[id] = al
	a.dirty = true
	return nil
}

// dropDevice 删除设备时一并清掉它的告警
func (a *AlertStore) dropDevice(deviceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hist.removed(deviceID, "")
	for id, al := range a.state {
		if al.DeviceID == deviceID {
			delete(a.state, id)
			a.dirty = true
		}
	}
}

// ----------------- 趋势与计数器基线 -----------------

type persistedState struct {
	SavedAt  int64                   `json:"saved_at"`
	Trends   map[string][]TrendPoint `json:"trends"`
	Counters map[string]PortSnapshot `json:"counters"`
}

func stateFilePath(dir string) string { return filepath.Join(dir, "history.json") }

// saveHistory 把趋势数据和速率基线落盘。
// 这样重启之后历史曲线还在, 而且第一轮就能算出速率, 不用重新热身。
func saveHistory(dir string, cap int) {
	stateLock.RLock()
	st := persistedState{
		SavedAt:  time.Now().Unix(),
		Trends:   make(map[string][]TrendPoint, len(deviceTrendMap)),
		Counters: make(map[string]PortSnapshot, len(portHistory)),
	}
	for k, v := range deviceTrendMap {
		if len(v) > cap {
			v = v[len(v)-cap:]
		}
		st.Trends[k] = append([]TrendPoint(nil), v...)
	}
	for k, v := range portHistory {
		st.Counters[k] = v
	}
	stateLock.RUnlock()

	if err := atomicWriteJSON(stateFilePath(dir), st, 0o600); err != nil {
		fmt.Printf("⚠️  历史数据落盘失败: %v\n", err)
	}
}

func loadHistory(dir string) (int, int) {
	var st persistedState
	if err := readJSON(stateFilePath(dir), &st); err != nil {
		return 0, 0
	}
	stateLock.Lock()
	defer stateLock.Unlock()
	points := 0
	for k, v := range st.Trends {
		deviceTrendMap[k] = append([]TrendPoint(nil), v...)
		points += len(v)
	}
	for k, v := range st.Counters {
		portHistory[k] = v
	}
	return len(st.Trends), points
}

// clearAllTrend 系统设置「清理工具」用: 清空所有设备的性能趋势曲线数据
// (CPU/内存/带宽这些历史点), 不影响速率计数器基线(portHistory, 清了下一轮
// 算出来的带宽会不准)。调用方负责随后调 saveHistory 落盘, 否则重启又回来。
func clearAllTrend() int {
	stateLock.Lock()
	defer stateLock.Unlock()
	n := 0
	for _, v := range deviceTrendMap {
		n += len(v)
	}
	deviceTrendMap = make(map[string][]TrendPoint)
	return n
}

// pruneHistory 清掉已删除设备留下的数据
func pruneHistory(alive map[string]bool) {
	stateLock.Lock()
	defer stateLock.Unlock()
	for k := range deviceTrendMap {
		if !alive[k] {
			delete(deviceTrendMap, k)
		}
	}
	for k := range portHistory {
		if i := strings.LastIndex(k, ":"); i > 0 && !alive[k[:i]] {
			delete(portHistory, k)
		}
	}
	for k := range lastHeavy {
		if !alive[k] {
			delete(lastHeavy, k)
		}
	}
	for k := range stpLast {
		if !alive[k] {
			delete(stpLast, k)
		}
	}
}

// ----------------- 拓扑布局 (背景图 + 节点位置) -----------------
//
// 节点位置存成图片宽高的百分比(0-100), 不是绝对像素——这样背景图换了
// 分辨率、或者在不同大小的浏览器窗口里看, 节点还是落在图片的同一个
// 相对位置上, 不用因为换个屏幕就重新摆一遍。

type NodePosition struct {
	X float64 `json:"x"` // 0-100, 相对背景图宽度的百分比
	Y float64 `json:"y"` // 0-100, 相对背景图高度的百分比
}

type topologyLayout struct {
	Positions map[string]NodePosition `json:"positions"` // key = 设备 IP / 外部邻居 key / "m:<手工节点>"
	ImageExt  string                  `json:"image_ext"` // 空 = 没有背景图
	// 手工节点 / 手工链路 / 隐藏的链路(见 topo_links.go)
	Nodes  []ManualNode `json:"nodes,omitempty"`
	Links  []ManualLink `json:"links,omitempty"`
	Hidden []string     `json:"hidden,omitempty"`
	// 3D 拓扑里手工拖过的位置(v12): X/Y = 地面上的 x/z 世界坐标, 跟 2D 坐标互不影响
	Pos3D map[string]NodePosition `json:"pos3d,omitempty"`
}

type TopologyStore struct {
	mu       sync.RWMutex
	jsonPath string
	imgDir   string
	data     topologyLayout
}

func newTopologyStore(dir string) *TopologyStore {
	return &TopologyStore{
		jsonPath: filepath.Join(dir, "topology.json"),
		imgDir:   dir,
		data:     topologyLayout{Positions: map[string]NodePosition{}},
	}
}

func (t *TopologyStore) load() error {
	var l topologyLayout
	if err := readJSON(t.jsonPath, &l); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if l.Positions == nil {
		l.Positions = map[string]NodePosition{}
	}
	t.data = l
	return nil
}

func (t *TopologyStore) saveLocked() error {
	return atomicWriteJSON(t.jsonPath, t.data, 0o600)
}

// Snapshot3D 返回 3D 拓扑里手工摆过的位置。
func (t *TopologyStore) Snapshot3D() map[string]NodePosition {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]NodePosition, len(t.data.Pos3D))
	for k, v := range t.data.Pos3D {
		out[k] = v
	}
	return out
}

func (t *TopologyStore) Snapshot() (map[string]NodePosition, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]NodePosition, len(t.data.Positions))
	for k, v := range t.data.Positions {
		out[k] = v
	}
	return out, t.data.ImageExt != ""
}

func (t *TopologyStore) SetPosition(ip string, x, y float64) error {
	if x < 0 {
		x = 0
	}
	if x > 100 {
		x = 100
	}
	if y < 0 {
		y = 0
	}
	if y > 100 {
		y = 100
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Positions[ip] = NodePosition{X: x, Y: y}
	return t.saveLocked()
}

// DropDevice 清掉被删除设备残留的坐标, 跟趋势/告警数据一个套路。
// 顺带清掉挂在这台设备身上的外部邻居坐标(key 形如 "ip|邻居名")。
func (t *TopologyStore) DropDevice(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	if _, ok := t.data.Positions[ip]; ok {
		delete(t.data.Positions, ip)
		changed = true
	}
	prefix := ip + "|"
	for k := range t.data.Positions {
		if strings.HasPrefix(k, prefix) {
			delete(t.data.Positions, k)
			changed = true
		}
	}
	if changed {
		_ = t.saveLocked()
	}
}

func (t *TopologyStore) imagePath() (string, bool) {
	t.mu.RLock()
	ext := t.data.ImageExt
	t.mu.RUnlock()
	if ext == "" {
		return "", false
	}
	return filepath.Join(t.imgDir, "topology_bg"+ext), true
}

// SetImage 校验并落盘背景图。size 上限在调用方(HTTP handler)已经通过
// MaxBytesReader 卡过一次, 这里再兜底校验一次实际解码出来的字节数和
// 文件类型, 防止有人绕过前端直接怼一个几十 MB 的东西把磁盘写爆,
// 或者传一个根本不是图片的文件进来。
func (t *TopologyStore) SetImage(raw []byte, declaredType string) error {
	const maxImageBytes = 8 * 1024 * 1024
	if len(raw) == 0 {
		return fmt.Errorf("图片数据为空")
	}
	if len(raw) > maxImageBytes {
		return fmt.Errorf("图片过大 (上限 8MB)")
	}
	// 只信服务端从字节内容里嗅探出来的类型, declaredType(客户端声称的)
	// 不参与校验——刚才那个 fallback 一度写反了, 会让人伪造 Content-Type
	// 把任意文件当图片存进来, 单测已经把这个漏洞堵上。declaredType 参数
	// 保留只是为了将来万一要打日志, 现在用不上。
	ext := extForImageType(http.DetectContentType(raw))
	if ext == "" {
		return fmt.Errorf("不是受支持的图片格式 (仅支持 PNG / JPEG / WebP / GIF)")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	// 换图片之前把旧文件清掉, 避免不同格式的旧文件堆积在数据目录里
	if t.data.ImageExt != "" {
		_ = os.Remove(filepath.Join(t.imgDir, "topology_bg"+t.data.ImageExt))
	}
	path := filepath.Join(t.imgDir, "topology_bg"+ext)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	t.data.ImageExt = ext
	return t.saveLocked()
}

func (t *TopologyStore) ClearImage() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data.ImageExt != "" {
		_ = os.Remove(filepath.Join(t.imgDir, "topology_bg"+t.data.ImageExt))
	}
	t.data.ImageExt = ""
	return t.saveLocked()
}

func extForImageType(contentType string) string {
	switch strings.ToLower(strings.SplitN(contentType, ";", 2)[0]) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ""
}
