package main

// 厂商识别规则和私有 OID —— 页面上可编辑(系统设置 →「厂商 / OID」)。
//
// 以前 cpuOIDs / memOIDs / tempOIDs / opticalSources / detectBrand 全写死在 engine.go,
// 遇到中兴、迈普、TP-Link 之类没见过的品牌只能改代码重新编译。现在:
//   - 内置 华为 / H3C / 锐捷 / Cisco 四套(就是原来写死的那些 OID, Cisco 是新补的),
//     可以改, 可以「恢复默认」, 不能删;
//   - 可以新增品牌: 填 sysObjectID 里的企业号(1.3.6.1.4.1.<N>) 或 sysDescr 关键字用来自动识别,
//     再填 CPU / 内存 / 温度 / 光功率 / 风扇电源状态 的 OID;
//   - 页面上有「在设备上试一下」: 选一台设备对某个 OID 做 walk, 马上看到返回值, 不用等下一轮巡检。
//
// 只有和默认不同的内置品牌、以及新增品牌会写进 data/vendors.json, 程序升级后内置 OID 的
// 修正能自动生效。保存后下一轮巡检开始用新规则。
//
// 仍然写死在代码里的少数特殊处理(和具体 MIB 的编码方式绑定, 做成配置反而难懂):
// 锐捷老固件光功率"整数位 + 小数位 + 符号位"三列拼装、H3C 老固件用 MinTXPower 顶替发光功率。

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
)

// OpticalOID: 收/发光功率两列(索引 = ifIndex 或 entPhysicalIndex), 原始值 ÷ Scale = dBm。
type OpticalOID struct {
	RX    string  `json:"rx"`
	TX    string  `json:"tx"`
	Scale float64 `json:"scale"`
}

// StatusOID: 风扇/电源状态表。Kind:
//   - fan / power: 这张表每一行都是风扇 / 电源;
//   - entity: 通用实体状态表(比如华为 hwEntityOperStatus), 用 ENTITY-MIB 的
//     entPhysicalClass(6=电源 7=风扇)挑出属于风扇/电源的行。
type StatusOID struct {
	OID    string  `json:"oid"`
	Kind   string  `json:"kind"`
	OK     []int64 `json:"ok"`               // 这些值 = 正常
	Absent []int64 `json:"absent,omitempty"` // 这些值 = 不在位(不告警)
}

// MemPoolOID: 内存不是直接给百分比的设备(比如 Cisco), 用"已用 / (已用+空闲)"或"已用 / 总量"算。
type MemPoolOID struct {
	Used  string `json:"used"`
	Free  string `json:"free,omitempty"`
	Total string `json:"total,omitempty"`
}

type VendorProfile struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Enterprises        []string     `json:"enterprises"`     // sysObjectID 企业号
	Keywords           []string     `json:"keywords"`        // sysDescr 关键字(不区分大小写)
	CPU                []string     `json:"cpu"`             // 候选 OID, 取表里 0~100 的最大值
	Mem                []string     `json:"mem"`             // 同上, 直接是百分比
	MemPool            []MemPoolOID `json:"mem_pool"`        // 需要换算的内存
	Temp               []string     `json:"temp"`            // 摄氏度
	TempWarningC       int          `json:"temp_warning_c"`  // 温度预警阈值(℃), >= 触发 warning
	TempCriticalC      int          `json:"temp_critical_c"` // 温度严重阈值(℃), >= 触发 critical
	Optical            []OpticalOID `json:"optical"`
	OpticalCopper      []string     `json:"optical_copper"`                 // 电口介质枚举 OID, 值 4 表示 copper(4)
	OpticalCopperValue int64        `json:"optical_copper_value,omitempty"` // 上述 OID 中表示铜缆电口的枚举值
	OpticalCopperMAU   []string     `json:"optical_copper_mau,omitempty"`   // MAU-MIB ifMauType OID, 值是 dot3MauType 对象标识符
	Status             []StatusOID  `json:"status"`
	BackupCmd          string       `json:"backup_cmd,omitempty"` // 配置备份命令(新增品牌用; 内置品牌在「设置 → 配置备份命令」)
	Note               string       `json:"note,omitempty"`

	Builtin  bool `json:"builtin"`  // 只读: 内置品牌
	Modified bool `json:"modified"` // 只读: 内置品牌改过
}

func builtinVendors() []VendorProfile {
	return []VendorProfile{
		{
			ID: "huawei", Name: "华为 Huawei", Enterprises: []string{"2011"}, Keywords: []string{"huawei", "vrp"},
			CPU: []string{
				".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.5",  // hwEntityCpuUsage
				".1.3.6.1.4.1.2011.6.3.4.1.2",          // hwAvgDuty1min (老 VRP)
				".1.3.6.1.4.1.2011.2.23.1.18.1.3.1.13", // 部分 S 系列
				".1.3.6.1.4.1.2011.6.3.4.1.3",          // hwAvgDuty1min (HUAWEI-CPU-MIB)
				".1.3.6.1.4.1.2011.6.1.1.1.3",          // hwAvgDuty1min (HUAWEI-DEVICE-MIB, VRP3 老 S2000/S3000)
				".1.3.6.1.4.1.2011.6.1.1.1.2",          // hwCpuDevDuty 5 秒
			},
			Mem: []string{".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.7", ".1.3.6.1.4.1.2011.6.3.5.1.1.2"},
			// 老 VRP 只有 总量 / 空闲 两列
			MemPool: []MemPoolOID{
				{Total: ".1.3.6.1.4.1.2011.6.1.2.1.1.2", Free: ".1.3.6.1.4.1.2011.6.1.2.1.1.3"},
				{Total: ".1.3.6.1.4.1.2011.6.3.5.1.1.2", Free: ".1.3.6.1.4.1.2011.6.3.5.1.1.3"},
			},
			Temp:         []string{".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.11"},
			TempWarningC: 60, TempCriticalC: 70,
			Optical:       []OpticalOID{{RX: ".1.3.6.1.4.1.2011.5.25.31.1.1.3.1.8", TX: ".1.3.6.1.4.1.2011.5.25.31.1.1.3.1.9", Scale: 100}},
			OpticalCopper: []string{oidHwEthernetPortMode}, OpticalCopperValue: 1,
			// hwEntityOperStatus: 1=notSupported 2=disabled 3=enabled 4=offline
			Status: []StatusOID{{OID: oidHwEntityOperStatus, Kind: "entity", OK: []int64{1, 3}, Absent: []int64{2}}},
		},
		{
			ID: "h3c", Name: "华三 H3C", Enterprises: []string{"25506", "43", "2011.10"}, Keywords: []string{"h3c", "comware", "huawei-3com"},
			CPU: []string{
				".1.3.6.1.4.1.25506.2.6.1.1.1.1.6",   // hh3cEntityExtCpuUsage
				".1.3.6.1.4.1.25506.8.35.18.4.2",     // 老 Comware
				".1.3.6.1.4.1.2011.10.2.6.1.1.1.1.6", // 老 H3C(华为-3Com 时代, 挂在 2011.10 下)
				".1.3.6.1.4.1.2011.6.1.1.1.3",        // 更老的 Comware 3 沿用华为 VRP3 的表
			},
			Mem:          []string{".1.3.6.1.4.1.25506.2.6.1.1.1.1.8", ".1.3.6.1.4.1.25506.8.35.18.4.1", ".1.3.6.1.4.1.2011.10.2.6.1.1.1.1.8"},
			MemPool:      []MemPoolOID{{Total: ".1.3.6.1.4.1.2011.6.1.2.1.1.2", Free: ".1.3.6.1.4.1.2011.6.1.2.1.1.3"}},
			Temp:         []string{".1.3.6.1.4.1.25506.2.6.1.1.1.1.12", ".1.3.6.1.4.1.2011.10.2.6.1.1.1.1.12"},
			TempWarningC: 60, TempCriticalC: 70,
			Optical: []OpticalOID{
				{RX: ".1.3.6.1.4.1.25506.2.70.1.1.1.12", TX: ".1.3.6.1.4.1.25506.2.70.1.1.1.9", Scale: 100},
				{RX: ".1.3.6.1.4.1.2011.10.2.70.1.1.1.12", TX: ".1.3.6.1.4.1.2011.10.2.70.1.1.1.9", Scale: 100},
			},
			OpticalCopper: []string{oidH3cTransceiverFiberDiameterType, oidH3cLegacyTransceiverFiberDiameterType}, OpticalCopperValue: 4,
			// hh3cEntityExtErrorStatus: 1=notSupported 2=normal 3=postFailure(不在位) 其余=各种故障
			Status: []StatusOID{
				{OID: oidH3cEntityErrStatus, Kind: "entity", OK: []int64{1, 2}, Absent: []int64{3}},
				{OID: ".1.3.6.1.4.1.2011.10.2.6.1.1.1.1.19", Kind: "entity", OK: []int64{1, 2}, Absent: []int64{3}}, // 老 H3C
			},
		},
		{
			ID: "ruijie", Name: "锐捷 Ruijie", Enterprises: []string{"4881", "52642"}, Keywords: []string{"ruijie", "rgos", "锐捷"},
			CPU: []string{
				".1.3.6.1.4.1.4881.1.1.10.2.36.1.1.2", // 1 分钟平均
				".1.3.6.1.4.1.4881.1.1.10.2.36.1.1.3", // 5 分钟平均
				".1.3.6.1.4.1.4881.1.1.10.2.36.1.1.1", // 5 秒瞬时
			},
			Mem:          []string{".1.3.6.1.4.1.4881.1.1.10.2.35.1.1.1.3"},
			Temp:         []string{".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.17"},
			TempWarningC: 60, TempCriticalC: 70,
			Optical:          []OpticalOID{{RX: ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.76", TX: ".1.3.6.1.4.1.4881.1.1.10.2.105.1.1.1.81", Scale: 100}},
			OpticalCopperMAU: []string{oidIfMauType},
			Status: []StatusOID{
				{OID: oidRuijieFanState, Kind: "fan", OK: []int64{1}},                            // 1=work 2=stop
				{OID: oidRuijiePowerState, Kind: "power", OK: []int64{3, 4}, Absent: []int64{1}}, // 1=noLink 3=ready 4=linkAndPower 5=abnormal
			},
		},
		{
			ID: "cisco", Name: "思科 Cisco", Enterprises: []string{"9"}, Keywords: []string{"cisco ios", "cisco nx-os", "cisco"},
			CPU: []string{
				".1.3.6.1.4.1.9.9.109.1.1.1.1.7", // cpmCPUTotal1minRev
				".1.3.6.1.4.1.9.9.109.1.1.1.1.8", // cpmCPUTotal5minRev
				".1.3.6.1.4.1.9.9.109.1.1.1.1.4", // cpmCPUTotal1min(老 IOS, 没有 Rev 列)
				".1.3.6.1.4.1.9.2.1.57.0",        // avgBusy1(OLD-CISCO-CPU-MIB, 很老的 IOS)
			},
			// ciscoMemoryPoolUsed / Free, 字节数
			MemPool:      []MemPoolOID{{Used: ".1.3.6.1.4.1.9.9.48.1.1.1.5", Free: ".1.3.6.1.4.1.9.9.48.1.1.1.6"}},
			Temp:         []string{".1.3.6.1.4.1.9.9.13.1.3.1.3"}, // ciscoEnvMonTemperatureStatusValue
			TempWarningC: 60, TempCriticalC: 70,
			// ciscoEnvMonFanState / SupplyState: 1=normal 2=warning 3=critical 4=shutdown 5=notPresent 6=notFunctioning
			Status: []StatusOID{
				{OID: ".1.3.6.1.4.1.9.9.13.1.4.1.3", Kind: "fan", OK: []int64{1}, Absent: []int64{5}},
				{OID: ".1.3.6.1.4.1.9.9.13.1.5.1.3", Kind: "power", OK: []int64{1}, Absent: []int64{5}},
			},
			Note: "光功率走 ENTITY-SENSOR-MIB 通用兜底",
		},
	}
}

// ----------------- 当前生效的规则(巡检线程读, 保存时整体替换) -----------------

type vendorSet struct {
	list []VendorProfile
	byID map[string]*VendorProfile
}

var liveVendors atomic.Pointer[vendorSet]

func newVendorSet(list []VendorProfile) *vendorSet {
	vs := &vendorSet{list: list, byID: make(map[string]*VendorProfile, len(list))}
	for i := range vs.list {
		vs.byID[vs.list[i].ID] = &vs.list[i]
	}
	return vs
}

func vendors() *vendorSet {
	if vs := liveVendors.Load(); vs != nil {
		return vs
	}
	vs := newVendorSet(builtinVendors())
	liveVendors.CompareAndSwap(nil, vs)
	return liveVendors.Load()
}

// vendorProfile 返回品牌的规则; 不认识的品牌(generic)返回一个只有标准 MIB 兜底的空规则。
func vendorProfile(brand string) *VendorProfile {
	if p, ok := vendors().byID[brand]; ok {
		return p
	}
	return &VendorProfile{ID: brand, CPU: []string{".1.3.6.1.2.1.25.3.3.1.2"}, TempWarningC: 60, TempCriticalC: 70} // hrProcessorLoad + 通用温度阈值
}

func knownBrand(brand string) bool {
	_, ok := vendors().byID[brand]
	return ok
}

func brandDisplayName(brand string) string {
	if p, ok := vendors().byID[brand]; ok && p.Name != "" {
		return p.Name
	}
	return brand
}

var enterpriseRe = regexp.MustCompile(`1\.3\.6\.1\.4\.1\.(\d+)`)

// detectBrand 先按 sysObjectID 的企业号认, 认不出再看 sysDescr 关键字。
// 同一企业号出现在多个品牌里时, 新增的品牌优先(方便用户覆盖内置规则, 比如把 OEM 设备单独建一个品牌)。
func detectBrand(sysObjectID, sysDescr string) string {
	vs := vendors()
	order := make([]*VendorProfile, 0, len(vs.list))
	for i := range vs.list {
		if !vs.list[i].Builtin {
			order = append(order, &vs.list[i])
		}
	}
	for i := range vs.list {
		if vs.list[i].Builtin {
			order = append(order, &vs.list[i])
		}
	}
	// 企业号可以写成更长的前缀(比如 "2011.10" = 华为-3Com / 老 H3C, 挂在华为的 2011 下面),
	// 按最长匹配认: 以前老 H3C(S3100/S3600/S5600/S3900 这一代)全被认成华为, CPU/内存都采不到。
	if i := strings.Index(sysObjectID, "1.3.6.1.4.1."); i >= 0 {
		rest := strings.TrimSuffix(sysObjectID[i+len("1.3.6.1.4.1."):], ".") + "."
		best, bestLen := "", 0
		for _, p := range order {
			for _, e := range p.Enterprises {
				e = strings.Trim(strings.TrimSpace(e), ".")
				if e != "" && strings.HasPrefix(rest, e+".") && len(e) > bestLen {
					best, bestLen = p.ID, len(e)
				}
			}
		}
		if best != "" {
			return best
		}
	}
	d := strings.ToLower(sysDescr)
	for _, p := range order {
		for _, k := range p.Keywords {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" && strings.Contains(d, k) {
				return p.ID
			}
		}
	}
	return ""
}

// ----------------- 持久化 -----------------

type VendorStore struct {
	mu   sync.Mutex
	path string
}

type vendorFile struct {
	Vendors []VendorProfile `json:"vendors"`
	Updated string          `json:"updated,omitempty"`
	By      string          `json:"by,omitempty"`
}

func newVendorStore(dir string) *VendorStore {
	return &VendorStore{path: filepath.Join(dir, "vendors.json")}
}

func (v *VendorStore) load() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	var f vendorFile
	if err := readJSON(v.path, &f); err != nil && !os.IsNotExist(err) {
		liveVendors.Store(newVendorSet(mergeVendors(nil)))
		return err
	}
	liveVendors.Store(newVendorSet(mergeVendors(f.Vendors)))
	return nil
}

// mergeVendors: 内置品牌(有保存过的改动就用改动) + 新增品牌。
func mergeVendors(saved []VendorProfile) []VendorProfile {
	byID := map[string]VendorProfile{}
	for _, p := range saved {
		byID[p.ID] = p
	}
	var out []VendorProfile
	for _, b := range builtinVendors() {
		b.Builtin = true
		if p, ok := byID[b.ID]; ok {
			// 老版本的 vendors.json 没有介质类型字段; 仅当字段缺失(nil)时继承新内置默认值,
			// 允许用户显式保存空列表来关闭该规则。
			if p.OpticalCopper == nil {
				p.OpticalCopper = append([]string(nil), b.OpticalCopper...)
			}
			if p.OpticalCopperMAU == nil {
				p.OpticalCopperMAU = append([]string(nil), b.OpticalCopperMAU...)
			}
			if p.OpticalCopperValue == 0 {
				p.OpticalCopperValue = b.OpticalCopperValue
			}
			if p.TempWarningC <= 0 {
				p.TempWarningC = b.TempWarningC
			}
			if p.TempCriticalC <= 0 {
				p.TempCriticalC = b.TempCriticalC
			}
			p.Builtin, p.Modified = true, !vendorEqual(p, b)
			out = append(out, p)
			delete(byID, b.ID)
			continue
		}
		out = append(out, b)
	}
	var custom []VendorProfile
	for _, p := range byID {
		if p.TempWarningC <= 0 {
			p.TempWarningC = 60
		}
		if p.TempCriticalC <= 0 {
			p.TempCriticalC = 70
		}
		p.Builtin, p.Modified = false, false
		custom = append(custom, p)
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].ID < custom[j].ID })
	return append(out, custom...)
}

func vendorEqual(a, b VendorProfile) bool {
	a.Builtin, a.Modified, b.Builtin, b.Modified = false, false, false, false
	return fmt.Sprintf("%#v", normVendor(a)) == fmt.Sprintf("%#v", normVendor(b))
}

// normVendor 把 nil 和空切片统一, 方便比较/输出(前端也不用判 null)。
func normVendor(p VendorProfile) VendorProfile {
	fix := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	p.Enterprises, p.Keywords, p.CPU, p.Mem, p.Temp = fix(p.Enterprises), fix(p.Keywords), fix(p.CPU), fix(p.Mem), fix(p.Temp)
	if p.MemPool == nil {
		p.MemPool = []MemPoolOID{}
	}
	if p.Optical == nil {
		p.Optical = []OpticalOID{}
	}
	if p.OpticalCopper == nil {
		p.OpticalCopper = []string{}
	}
	if p.OpticalCopperMAU == nil {
		p.OpticalCopperMAU = []string{}
	}
	if p.Status == nil {
		p.Status = []StatusOID{}
	}
	for i := range p.Status {
		if p.Status[i].OK == nil {
			p.Status[i].OK = []int64{}
		}
	}
	return p
}

var (
	vendorIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,19}$`)
	oidRe      = regexp.MustCompile(`^\.?1(\.\d+)+$`)
	entNumRe   = regexp.MustCompile(`^\d{1,7}$`)
)

func normOID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !oidRe.MatchString(s) {
		return "", fmt.Errorf("OID 格式不对: %q (应该像 .1.3.6.1.4.1.2011.5.25.31.1.1.1.1.5)", s)
	}
	return "." + strings.TrimPrefix(s, "."), nil
}

func normOIDList(list []string, what string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range list {
		o, err := normOID(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if o != "" && !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	if len(out) > 20 {
		return nil, fmt.Errorf("%s 最多 20 个", what)
	}
	return out, nil
}

func validateVendor(p *VendorProfile) error {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	if !vendorIDRe.MatchString(p.ID) {
		return fmt.Errorf("品牌代码 %q 不合法: 小写字母开头, 2~20 位字母/数字/-/_, 比如 zte、maipu", p.ID)
	}
	if inList(p.ID, []string{"auto", "generic"}) {
		return fmt.Errorf("品牌代码不能用 %s", p.ID)
	}
	var err error
	if p.Name, err = dcText(p.Name, 30, "品牌名称"); err != nil {
		return err
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.TempWarningC == 0 {
		p.TempWarningC = 60
	}
	if p.TempCriticalC == 0 {
		p.TempCriticalC = 70
	}
	if p.TempWarningC < 1 || p.TempWarningC >= 120 {
		return fmt.Errorf("%s 温度预警阈值需要在 1~119 ℃", p.Name)
	}
	if p.TempCriticalC <= p.TempWarningC || p.TempCriticalC > 120 {
		return fmt.Errorf("%s 温度严重阈值需要大于预警阈值且不超过 120 ℃", p.Name)
	}
	if p.Note, err = dcText(p.Note, 200, "备注"); err != nil {
		return err
	}
	ents := []string{}
	for _, e := range p.Enterprises {
		e = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e), "1.3.6.1.4.1."))
		if e == "" {
			continue
		}
		if !entNumRe.MatchString(e) {
			return fmt.Errorf("企业号 %q 应该是纯数字(sysObjectID 里 1.3.6.1.4.1. 后面那一段, 比如华为 2011)", e)
		}
		ents = append(ents, e)
	}
	p.Enterprises = ents
	kws := []string{}
	for _, k := range p.Keywords {
		if k = strings.TrimSpace(k); k != "" {
			if len([]rune(k)) < 2 {
				return fmt.Errorf("sysDescr 关键字 %q 太短, 容易误判", k)
			}
			kws = append(kws, k)
		}
	}
	p.Keywords = kws
	if len(p.Enterprises) == 0 && len(p.Keywords) == 0 {
		return fmt.Errorf("品牌「%s」至少要填一个企业号或 sysDescr 关键字, 否则没法自动识别(手工指定品牌的设备除外)", p.Name)
	}
	if p.CPU, err = normOIDList(p.CPU, "CPU OID"); err != nil {
		return err
	}
	if p.Mem, err = normOIDList(p.Mem, "内存 OID"); err != nil {
		return err
	}
	if p.Temp, err = normOIDList(p.Temp, "温度 OID"); err != nil {
		return err
	}
	pools := []MemPoolOID{}
	for _, m := range p.MemPool {
		if m.Used, err = normOID(m.Used); err != nil {
			return fmt.Errorf("内存(已用): %w", err)
		}
		if m.Free, err = normOID(m.Free); err != nil {
			return fmt.Errorf("内存(空闲): %w", err)
		}
		if m.Total, err = normOID(m.Total); err != nil {
			return fmt.Errorf("内存(总量): %w", err)
		}
		if m.Used == "" && m.Free == "" && m.Total == "" {
			continue
		}
		if m.Used == "" || (m.Free == "" && m.Total == "") {
			return errors.New("内存换算要填「已用」, 再加「空闲」或「总量」其中一个")
		}
		pools = append(pools, m)
	}
	p.MemPool = pools
	opts := []OpticalOID{}
	for _, o := range p.Optical {
		if o.RX, err = normOID(o.RX); err != nil {
			return fmt.Errorf("收光 OID: %w", err)
		}
		if o.TX, err = normOID(o.TX); err != nil {
			return fmt.Errorf("发光 OID: %w", err)
		}
		if o.RX == "" && o.TX == "" {
			continue
		}
		if o.Scale == 0 {
			o.Scale = 1
		}
		if o.Scale < 0 || o.Scale > 100000 {
			return errors.New("光功率换算倍数不合理(常见 1 / 10 / 100 / 1000)")
		}
		opts = append(opts, o)
	}
	p.Optical = opts
	if p.OpticalCopper, err = normOIDList(p.OpticalCopper, "电口介质类型 OID"); err != nil {
		return err
	}
	if p.OpticalCopperMAU, err = normOIDList(p.OpticalCopperMAU, "MAU-MIB 电口介质类型 OID"); err != nil {
		return err
	}
	if p.OpticalCopperValue < 0 || p.OpticalCopperValue > 65535 {
		return errors.New("电口介质类型枚举值需在 0~65535 之间")
	}
	sts := []StatusOID{}
	for _, s := range p.Status {
		if s.OID, err = normOID(s.OID); err != nil {
			return fmt.Errorf("风扇/电源状态: %w", err)
		}
		if s.OID == "" {
			continue
		}
		if !inList(s.Kind, []string{"fan", "power", "entity"}) {
			return fmt.Errorf("状态表 %s 的类型只能是 风扇 / 电源 / 通用实体表", s.OID)
		}
		if len(s.OK) == 0 {
			return fmt.Errorf("状态表 %s 要填哪些值算正常", s.OID)
		}
		sts = append(sts, s)
	}
	p.Status = sts
	if len(p.BackupCmd) > 2000 {
		return errors.New("备份命令太长")
	}
	p.BackupCmd = strings.TrimSpace(strings.ReplaceAll(p.BackupCmd, "\r\n", "\n"))
	return nil
}

// Save 校验并保存整份列表(页面每次提交全部品牌), 立即替换生效的规则。
func (v *VendorStore) Save(list []VendorProfile, by string) ([]VendorProfile, error) {
	if len(list) > 100 {
		return nil, errors.New("品牌数量超过上限(100)")
	}
	builtin := map[string]VendorProfile{}
	for _, b := range builtinVendors() {
		builtin[b.ID] = b
	}
	seen := map[string]bool{}
	var keep []VendorProfile
	for i := range list {
		p := list[i]
		if err := validateVendor(&p); err != nil {
			return nil, err
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("品牌代码 %s 重复了", p.ID)
		}
		seen[p.ID] = true
		p.Builtin, p.Modified = false, false
		if b, ok := builtin[p.ID]; ok && vendorEqual(p, b) {
			continue // 和内置默认一样的不落盘
		}
		keep = append(keep, normVendor(p))
	}
	if keep == nil {
		keep = []VendorProfile{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.path != "" {
		if err := atomicWriteJSON(v.path, vendorFile{Vendors: keep, Updated: time.Now().Format("2006-01-02 15:04:05"), By: by}, 0o600); err != nil {
			return nil, err
		}
	}
	merged := mergeVendors(keep)
	liveVendors.Store(newVendorSet(merged))
	return merged, nil
}

func vendorList() []VendorProfile {
	vs := vendors()
	out := make([]VendorProfile, len(vs.list))
	for i, p := range vs.list {
		out[i] = normVendor(p)
	}
	return out
}

// ----------------- 采集侧 -----------------

// probeMemPool 用"已用/(已用+空闲)"或"已用/总量"算内存百分比; 多行时按总量加权。
func (s *snmpSession) probeMemPool(pools []MemPoolOID) (int, bool) {
	for _, m := range pools {
		var used map[string]uint64
		if m.Used == "" && m.Total != "" && m.Free != "" {
			// 只有"总量 + 空闲"两列的老设备(华为老 VRP hwMemoryDevSize/Free): 已用 = 总量 - 空闲
			total, free := s.walkUints(m.Total), s.walkUints(m.Free)
			used = map[string]uint64{}
			for idx, t := range total {
				if f, ok := free[idx]; ok && f <= t {
					used[idx] = t - f
				}
			}
		} else if m.Used != "" {
			used = s.walkUints(m.Used)
		}
		if len(used) == 0 {
			continue
		}
		var other map[string]uint64
		if m.Total != "" {
			other = s.walkUints(m.Total)
		} else {
			other = s.walkUints(m.Free)
		}
		var u, t float64
		for idx, uv := range used {
			ov, ok := other[idx]
			if !ok {
				continue
			}
			u += float64(uv)
			if m.Total != "" {
				t += float64(ov)
			} else {
				t += float64(uv + ov)
			}
		}
		if t > 0 && u <= t {
			return int(u/t*100 + 0.5), true
		}
	}
	return 0, false
}

// ----------------- 接口 -----------------

// GET /api/vendors: 公开的只是 ID 和名称(设备管理下拉用); 登录后返回完整规则。
func (s *server) handleVendorsPublic(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := []item{}
	for _, p := range vendors().list {
		out = append(out, item{p.ID, p.Name})
	}
	s.writeJSON(w, 200, map[string]interface{}{"vendors": out})
}

func (s *server) handleVendors(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodPost {
		var req struct {
			Vendors []VendorProfile `json:"vendors"`
		}
		if !s.decode(w, r, &req) {
			return
		}
		if s.vendors == nil {
			s.vendors = &VendorStore{} // 没有数据目录(测试里): 只改内存, 不落盘
		}
		list, err := s.vendors.Save(req.Vendors, sess.user)
		if err != nil {
			s.fail(w, 400, err.Error())
			return
		}
		for i := range list {
			list[i] = normVendor(list[i])
		}
		s.writeJSON(w, 200, map[string]interface{}{"ok": true, "vendors": list})
		return
	}
	defs := builtinVendors()
	for i := range defs {
		defs[i] = normVendor(defs[i])
		defs[i].Builtin = true
	}
	s.writeJSON(w, 200, map[string]interface{}{"vendors": vendorList(), "defaults": defs})
}

// POST /api/vendors/test {device_id, oid}: 在一台已添加的设备上 walk 一个 OID, 返回前 50 行。
func (s *server) handleVendorTest(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		DeviceID string `json:"device_id"`
		OID      string `json:"oid"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	oid, err := normOID(req.OID)
	if err != nil || oid == "" {
		s.fail(w, 400, "请填写正确的 OID")
		return
	}
	dev, ok := s.devices.Get(req.DeviceID)
	if !ok {
		s.fail(w, 400, "设备不存在")
		return
	}
	cfg := *s.conf()
	if cfg.TimeoutSeconds > 5 {
		cfg.TimeoutSeconds = 5
	}
	cfg.Retries = 0
	sn, err := dialSNMP(dev, &cfg)
	if err != nil {
		s.writeJSON(w, 200, map[string]interface{}{"ok": false, "message": "无法建立连接"})
		return
	}
	defer func() {
		if sn.c.Conn != nil {
			_ = sn.c.Conn.Close()
		}
	}()
	type row struct {
		Index string `json:"index"`
		Value string `json:"value"`
	}
	rows := []row{}
	total := 0
	t0 := time.Now()
	werr := sn.walk(oid, func(idx string, pdu gosnmp.SnmpPDU) {
		total++
		if len(rows) < 50 {
			rows = append(rows, row{idx, pduText(pdu)})
		}
	})
	if total == 0 {
		// 可能是标量(xxx.0), walk 不到就直接 GET
		if pdu, ok := sn.get(oid); ok {
			total = 1
			rows = append(rows, row{"(标量)", pduText(pdu)})
		}
	}
	msg := fmt.Sprintf("共 %d 行, 用时 %dms", total, time.Since(t0).Milliseconds())
	if total == 0 {
		msg = "没有返回任何数据: 设备不支持这个 OID, 或者 SNMP view 没放通"
		if werr != nil {
			msg += " (" + werr.Error() + ")"
		}
	}
	s.writeJSON(w, 200, map[string]interface{}{"ok": total > 0, "message": msg, "rows": rows, "total": total, "device": dev.IP})
}

func pduText(p gosnmp.SnmpPDU) string {
	switch v := p.Value.(type) {
	case []byte:
		return decodeText(v)
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
