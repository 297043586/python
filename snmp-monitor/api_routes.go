package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// handleDeviceRoutes returns the last saved route snapshot. A missing snapshot
// (or one saved by an older decoder) is collected on first access; refresh=1
// explicitly replaces the saved copy.
func (s *server) handleDeviceRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.fail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		s.fail(w, http.StatusBadRequest, "missing ip")
		return
	}
	var target DeviceTarget
	for _, d := range s.devices.Active() {
		if d.IP == ip {
			target = d
			break
		}
	}
	if target.IP == "" {
		s.fail(w, http.StatusNotFound, "device not found or disabled")
		return
	}
	key := routeCacheKey(target)
	refresh := r.URL.Query().Get("refresh") == "1" || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if !refresh {
		if snapshot, ok := s.routeCache.get(key); ok && snapshot.current() {
			s.writeRouteSnapshot(w, snapshot, true, false, snapshot.Message)
			return
		}
	}

	unlock := s.routeCache.lockRefresh(key)
	defer unlock()
	// Another request may have completed the first read while this request waited.
	if !refresh {
		if snapshot, ok := s.routeCache.get(key); ok && snapshot.current() {
			s.writeRouteSnapshot(w, snapshot, true, false, snapshot.Message)
			return
		}
	}
	previous, hasPrevious := s.routeCache.get(key)
	keepPrevious := func(reason string) bool {
		if !hasPrevious || len(previous.Routes) == 0 {
			return false
		}
		msg := reason + "，显示上次保存的数据"
		if !previous.current() {
			msg += "（该数据由旧版本解码，协议/类型可能不准确）"
		}
		s.writeRouteSnapshot(w, previous, true, true, msg)
		return true
	}

	cfg := s.conf()
	pool, err := newSessionPool(target, cfg, 4)
	if err != nil {
		if !keepPrevious("SNMP 连接失败: " + err.Error()) {
			s.fail(w, http.StatusBadGateway, "SNMP connect failed: "+err.Error())
		}
		return
	}
	defer pool.close()

	result, err := collectRouteTable(pool)
	if err != nil {
		if !keepPrevious("路由表读取失败: " + err.Error()) {
			s.fail(w, http.StatusBadGateway, "SNMP route walk failed: "+err.Error())
		}
		return
	}
	if len(result.Routes) == 0 {
		if keepPrevious("本次读取返回 0 条路由") {
			return
		}
		result.Notes = append(result.Notes, "没有从支持的标准 SNMP 路由表解码到路由；可能表为空或 SNMP view 未放行 IP-FORWARD-MIB")
	}
	snapshot := newRouteSnapshot(target.IP, result.Source, result.Routes)
	snapshot.Message = strings.Join(result.Notes, "；")
	message := snapshot.Message
	if err := s.routeCache.put(key, snapshot); err != nil {
		message = joinNotes(message, "缓存文件写入失败: "+err.Error())
	}
	s.writeRouteSnapshot(w, snapshot, false, false, message)
}

func (s *server) writeRouteSnapshot(w http.ResponseWriter, snapshot RouteSnapshot, cached, stale bool, message string) {
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"routes": snapshot.Routes, "source": snapshot.Source, "timestamp": snapshot.Timestamp,
		"cached": cached, "stale": stale, "message": message,
	})
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}

// ----------------- 路由表采集 -----------------

// routeTableResult 是一次路由表采集的结果。Notes 是需要显示给用户的诊断说明。
type routeTableResult struct {
	Routes []RouteEntry
	Source string
	Notes  []string
}

// routeColumnSpec 描述一张路由表要读取的列。
type routeColumnSpec struct {
	name   string
	ints   map[string]string // 列名 -> OID, 按整数读
	values map[string]string // 列名 -> OID, 按原始值读(地址类)
}

// routeColumns 是读回来的一张路由表(按行索引)。
type routeColumns struct {
	ifIndex map[string]int64
	ints    map[string]map[string]int64
	values  map[string]map[string]interface{}
	failed  []string // 读取失败(重试后仍失败)的列名
}

func (c routeColumns) intAt(column, index string) (int64, bool) {
	v, ok := c.ints[column][index]
	return v, ok
}

// collectRouteTable 读取设备的活动路由:
//   - IPv4: 优先 inetCidrRouteTable;它不存在、没有 IPv4 行,或部分行没有有效
//     的协议/类型枚举时,再读 ipCidrRouteTable / ipRouteTable 补全。
//   - IPv6: 优先 inetCidrRouteTable,没有 IPv6 行时回退 RFC 2465 ipv6RouteTable。
func collectRouteTable(pool *sessionPool) (routeTableResult, error) {
	var res routeTableResult
	var sources []string
	ifs := routeInterfaces(pool)

	inet, inetErr := readRouteColumns(pool, routeColumnSpec{
		name: "inetCidrRouteTable",
		ints: map[string]string{
			"type": oidInetCidrRouteType, "proto": oidInetCidrRouteProto, "age": oidInetCidrRouteAge,
			"metric": oidInetCidrRouteMetric1, "status": oidInetCidrRouteStatus,
		},
	}, oidInetCidrRouteIfIndex)
	var v4, v6 []RouteEntry
	for index, ifIndex := range inet.ifIndex {
		if status, ok := inet.intAt("status", index); ok && status != 1 { // RowStatus active(1)
			continue
		}
		destination, nextHop, ok := parseInetRouteIndex(index)
		if !ok {
			continue
		}
		entry := newRouteEntry("inetCidrRouteTable", destination, nextHop, ifIndex, inet, index, ifs)
		if strings.Contains(destination, ":") {
			v6 = append(v6, entry)
		} else {
			v4 = append(v4, entry)
		}
	}
	if len(v4) > 0 || len(v6) > 0 {
		sources = append(sources, "inetCidrRouteTable")
		res.Notes = append(res.Notes, failedColumnNote(inet)...)
	}

	needV4Fallback := len(v4) == 0
	for _, e := range v4 {
		if !e.enumValid() {
			needV4Fallback = true
			break
		}
	}
	var legacyErr error
	if needV4Fallback {
		legacy, source, notes, err := collectIPv4LegacyRoutes(pool, ifs)
		legacyErr = err
		switch {
		case len(legacy) == 0:
		case len(v4) == 0:
			v4 = legacy
			sources = append(sources, source)
			res.Notes = append(res.Notes, notes...)
		default:
			if n := repairRouteEnums(v4, legacy); n > 0 {
				sources = append(sources, source+"(补全 "+strconv.Itoa(n)+" 条协议/类型)")
				res.Notes = append(res.Notes, notes...)
			}
		}
	}

	var v6Err error
	if len(v6) == 0 {
		var notes []string
		v6, notes, v6Err = collectIPv6LegacyRoutes(pool, ifs)
		if len(v6) > 0 {
			sources = append(sources, "ipv6RouteTable")
			res.Notes = append(res.Notes, notes...)
		}
	}

	res.Routes = append(v4, v6...)
	if len(res.Routes) == 0 {
		// 所有表都不可读时才返回错误;"表存在但为空"不算错误。
		for _, err := range []error{inetErr, legacyErr, v6Err} {
			if err != nil {
				return res, err
			}
		}
	}
	sortRouteEntries(res.Routes)
	res.Source = strings.Join(sources, " + ")
	if n := countInvalidRouteEnums(res.Routes); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d 条路由设备未返回有效的协议/类型枚举（显示为“未上报”），鼠标悬停可查看原始值", n))
	}
	return res, nil
}

// collectIPv4LegacyRoutes 依次尝试 ipCidrRouteTable(RFC 2096) 和 ipRouteTable(RFC 1213)。
func collectIPv4LegacyRoutes(pool *sessionPool, ifs map[string]*ifInfo) ([]RouteEntry, string, []string, error) {
	cidr, cidrErr := readRouteColumns(pool, routeColumnSpec{
		name: "ipCidrRouteTable",
		ints: map[string]string{
			"type": oidIpCidrRouteType, "proto": oidIpCidrRouteProto, "age": oidIpCidrRouteAge,
			"metric": oidIpCidrRouteMetric1, "status": oidIpCidrRouteStatus,
		},
	}, oidIpCidrRouteIfIndex)
	rows := make([]RouteEntry, 0, len(cidr.ifIndex))
	for index, ifIndex := range cidr.ifIndex {
		if status, ok := cidr.intAt("status", index); ok && status != 1 {
			continue
		}
		destination, nextHop, ok := parseIPv4CIDRRouteIndex(index)
		if !ok {
			continue
		}
		rows = append(rows, newRouteEntry("ipCidrRouteTable", destination, nextHop, ifIndex, cidr, index, ifs))
	}
	if len(rows) > 0 {
		return rows, "ipCidrRouteTable", failedColumnNote(cidr), nil
	}

	legacy, err := readRouteColumns(pool, routeColumnSpec{
		name: "ipRouteTable",
		ints: map[string]string{
			"type": oidIpRouteType, "proto": oidIpRouteProto, "age": oidIpRouteAge, "metric": oidIpRouteMetric1,
		},
		values: map[string]string{"nexthop": oidIpRouteNextHop, "mask": oidIpRouteMask},
	}, oidIpRouteIfIndex)
	if err != nil {
		if cidrErr != nil {
			return nil, "", nil, cidrErr
		}
		return nil, "", nil, err
	}
	rows = rows[:0]
	for index, ifIndex := range legacy.ifIndex {
		if t, _ := legacy.intAt("type", index); t == 2 { // ipRouteType=invalid: an inactive legacy row
			continue
		}
		destinationIP, ok := parseIPv4OIDAddress(index)
		if !ok {
			continue
		}
		mask := routeIPValue(legacy.values["mask"][index])
		if mask == nil || mask.To4() == nil {
			continue
		}
		prefix, bits := net.IPMask(mask.To4()).Size()
		if bits == 0 { // 非连续掩码
			continue
		}
		destination := fmt.Sprintf("%s/%d", destinationIP, prefix)
		nextHop := normalizeNextHop(routeIPValue(legacy.values["nexthop"][index]))
		rows = append(rows, newRouteEntry("ipRouteTable", destination, nextHop, ifIndex, legacy, index, ifs))
	}
	if len(rows) == 0 {
		return nil, "", nil, nil
	}
	return rows, "ipRouteTable", failedColumnNote(legacy), nil
}

func collectIPv6LegacyRoutes(pool *sessionPool, ifs map[string]*ifInfo) ([]RouteEntry, []string, error) {
	// RFC 2465 IPv6-MIB is still implemented by some Huawei VRP releases
	// that do not expose the newer inetCidrRouteTable.
	cols, err := readRouteColumns(pool, routeColumnSpec{
		name: "ipv6RouteTable",
		ints: map[string]string{
			"type": oidIPv6RouteType, "proto": oidIPv6RouteProto, "age": oidIPv6RouteAge,
			"metric": oidIPv6RouteMetric, "valid": oidIPv6RouteValid,
		},
		values: map[string]string{"nexthop": oidIPv6RouteNextHop},
	}, oidIPv6RouteIfIndex)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]RouteEntry, 0, len(cols.ifIndex))
	for index, ifIndex := range cols.ifIndex {
		if valid, ok := cols.intAt("valid", index); ok && valid != 1 { // TruthValue true(1)
			continue
		}
		destination, ok := parseIPv6RouteIndex(index)
		if !ok {
			continue
		}
		nextHop := normalizeNextHop(routeIPValue(cols.values["nexthop"][index]))
		rows = append(rows, newRouteEntry("ipv6RouteTable", destination, nextHop, ifIndex, cols, index, ifs))
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}
	return rows, failedColumnNote(cols), nil
}

// readRouteColumns 先读 ifIndex 列确定表是否存在,再用会话池并行读其余列。
// 并行读取中失败的列(常见于设备限速/超时)会在主会话上串行重试一次;
// 仍失败的列记入 failed,而不是静默变成"未上报"后被长期缓存。
func readRouteColumns(pool *sessionPool, spec routeColumnSpec, ifIndexOID string) (routeColumns, error) {
	cols := routeColumns{
		ints:   make(map[string]map[string]int64, len(spec.ints)),
		values: make(map[string]map[string]interface{}, len(spec.values)),
	}
	ifIndex, err := routeIntColumn(pool.primary(), ifIndexOID)
	cols.ifIndex = ifIndex
	if len(ifIndex) == 0 {
		return cols, err
	}

	type job struct {
		name, oid string
		isInt     bool
	}
	jobs := make([]job, 0, len(spec.ints)+len(spec.values))
	for name, oid := range spec.ints {
		jobs = append(jobs, job{name, oid, true})
	}
	for name, oid := range spec.values {
		jobs = append(jobs, job{name, oid, false})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].name < jobs[j].name })
	intOut := make([]map[string]int64, len(jobs))
	valOut := make([]map[string]interface{}, len(jobs))
	errs := make([]error, len(jobs))
	run := func(i int, s *snmpSession) {
		if jobs[i].isInt {
			intOut[i], errs[i] = routeIntColumn(s, jobs[i].oid)
		} else {
			valOut[i], errs[i] = routeValueColumn(s, jobs[i].oid)
		}
	}
	tasks := make([]func(*snmpSession), len(jobs))
	for i := range jobs {
		i := i
		tasks[i] = func(s *snmpSession) { run(i, s) }
	}
	pool.run(tasks...)
	for i := range jobs {
		if errs[i] != nil {
			run(i, pool.primary())
		}
		if errs[i] != nil {
			cols.failed = append(cols.failed, spec.name+"."+jobs[i].name)
		}
		if jobs[i].isInt {
			cols.ints[jobs[i].name] = intOut[i]
		} else {
			cols.values[jobs[i].name] = valOut[i]
		}
	}
	return cols, nil
}

func failedColumnNote(cols routeColumns) []string {
	if len(cols.failed) == 0 {
		return nil
	}
	return []string{"以下列读取超时或出错，相应字段可能缺失，可稍后点“刷新路由表”重试: " + strings.Join(cols.failed, ", ")}
}

// routeInterfaces 并行读取 ifDescr / ifName,用于把路由出接口 ifIndex 显示为名称。
func routeInterfaces(pool *sessionPool) map[string]*ifInfo {
	var descr, name map[string]string
	pool.run(
		func(s *snmpSession) { descr = s.walkStrings(oidIfDescr) },
		func(s *snmpSession) { name = s.walkStrings(oidIfName) },
	)
	ifs := make(map[string]*ifInfo, len(descr)+len(name))
	for index, value := range descr {
		ifs[index] = &ifInfo{Index: index, Descr: value}
	}
	for index, value := range name {
		e := ifs[index]
		if e == nil {
			e = &ifInfo{Index: index}
			ifs[index] = e
		}
		e.Name = value
	}
	return ifs
}

func routeIntColumn(s *snmpSession, oid string) (map[string]int64, error) {
	out := make(map[string]int64)
	err := s.walk(oid, func(index string, pdu gosnmp.SnmpPDU) {
		out[index] = toInt64(pdu.Value)
	})
	return out, err
}

func routeValueColumn(s *snmpSession, oid string) (map[string]interface{}, error) {
	out := make(map[string]interface{})
	err := s.walk(oid, func(index string, pdu gosnmp.SnmpPDU) {
		out[index] = pdu.Value
	})
	return out, err
}

// repairRouteEnums 用旧版 IPv4 表中同一路由的有效枚举替换新版表里的无效值
// (部分固件的 inetCidrRouteTable 只填了索引,协议/类型返回 -1 或 0)。
// 只替换无效字段,不覆盖设备已在新版表中给出的有效值。返回被补全的行数。
func repairRouteEnums(rows, legacy []RouteEntry) int {
	byRoute := make(map[string]RouteEntry, len(legacy))
	byDest := make(map[string][]RouteEntry, len(legacy))
	for _, e := range legacy {
		byRoute[e.Destination+"|"+e.NextHop] = e
		byDest[e.Destination] = append(byDest[e.Destination], e)
	}
	repaired := 0
	for i := range rows {
		e := &rows[i]
		if e.enumValid() {
			continue
		}
		src, ok := byRoute[e.Destination+"|"+e.NextHop]
		if !ok && len(byDest[e.Destination]) == 1 {
			// ipRouteTable 每个目的地址只有一行(ECMP 只保留一条),按目的地址匹配。
			src, ok = byDest[e.Destination][0], true
		}
		if !ok {
			continue
		}
		changed := false
		if !routeProtocolValid(e.ProtocolEnum, e.ProtocolCode) && routeProtocolValid(src.ProtocolEnum, src.ProtocolCode) {
			e.Protocol, e.ProtocolCode, e.ProtocolEnum = src.Protocol, src.ProtocolCode, src.ProtocolEnum
			changed = true
		}
		if !routeTypeValid(e.TypeEnum, e.TypeCode) && routeTypeValid(src.TypeEnum, src.TypeCode) {
			e.Type, e.TypeCode, e.TypeEnum = src.Type, src.TypeCode, src.TypeEnum
			changed = true
		}
		if changed {
			repaired++
		}
	}
	return repaired
}

func countInvalidRouteEnums(rows []RouteEntry) int {
	n := 0
	for _, e := range rows {
		if !e.enumValid() {
			n++
		}
	}
	return n
}
func parseInetRouteIndex(index string) (destination, nextHop string, ok bool) {
	parts := strings.Split(index, ".")
	values := make([]int, len(parts))
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 {
			return "", "", false
		}
		values[i] = v
	}
	i := 0
	readAddress := func() (string, int, bool) {
		if i+1 >= len(values) {
			return "", 0, false
		}
		kind, length := values[i], values[i+1]
		i += 2
		if length < 0 || i+length > len(values) {
			return "", 0, false
		}
		octets := make([]byte, length)
		for j := range octets {
			if values[i+j] > 255 {
				return "", 0, false
			}
			octets[j] = byte(values[i+j])
		}
		i += length
		return indexedInetAddress(kind, octets), kind, true
	}
	destination, destType, ok := readAddress()
	if !ok || destination == "" || i+2 > len(values) {
		return "", "", false
	}
	prefix := values[i]
	i++
	policyLength := values[i]
	i++
	if policyLength < 0 || i+policyLength > len(values) {
		return "", "", false
	}
	i += policyLength
	nextHop, _, ok = readAddress()
	if !ok || i != len(values) {
		return "", "", false
	}
	maxPrefix := 32
	if destType == 2 || destType == 4 {
		maxPrefix = 128
	}
	if prefix < 0 || prefix > maxPrefix {
		return "", "", false
	}
	destination += "/" + strconv.Itoa(prefix)
	if nextHop == "" {
		nextHop = "-"
	}
	return destination, nextHop, true
}

func indexedInetAddress(kind int, octets []byte) string {
	switch kind {
	case 0:
		return ""
	case 1:
		if len(octets) == net.IPv4len {
			return net.IP(octets).String()
		}
	case 2:
		if len(octets) == net.IPv6len {
			return net.IP(octets).String()
		}
	case 3:
		if len(octets) == 8 {
			zone := binary.BigEndian.Uint32(octets[4:])
			return fmt.Sprintf("%s%%%d", net.IP(octets[:4]).String(), zone)
		}
	case 4:
		if len(octets) == 20 {
			zone := binary.BigEndian.Uint32(octets[16:])
			return fmt.Sprintf("%s%%%d", net.IP(octets[:16]).String(), zone)
		}
	}
	return ""
}

func parseIPv4CIDRRouteIndex(index string) (destination, nextHop string, ok bool) {
	parts := strings.Split(index, ".")
	if len(parts) != 13 {
		return "", "", false
	}
	destIP, ok1 := parseIPv4OIDAddress(strings.Join(parts[:4], "."))
	maskIP, ok2 := parseIPv4OIDAddress(strings.Join(parts[4:8], "."))
	if !ok1 || !ok2 {
		return "", "", false
	}
	prefix, _ := net.IPMask(maskIP.To4()).Size()
	if _, err := strconv.Atoi(parts[8]); err != nil {
		return "", "", false
	}
	nextHopIP, ok3 := parseIPv4OIDAddress(strings.Join(parts[9:], "."))
	if !ok3 {
		return "", "", false
	}
	nextHop = nextHopIP.String()
	if nextHop == "0.0.0.0" {
		nextHop = "-"
	}
	return fmt.Sprintf("%s/%d", destIP, prefix), nextHop, true
}

func parseIPv4OIDAddress(index string) (net.IP, bool) {
	parts := strings.Split(index, ".")
	if len(parts) != 4 {
		return nil, false
	}
	b := make([]byte, 4)
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 || v > 255 {
			return nil, false
		}
		b[i] = byte(v)
	}
	return net.IP(b), true
}

func parseIPv6RouteIndex(index string) (destination string, ok bool) {
	parts := strings.Split(index, ".")
	if len(parts) != 18 {
		return "", false
	}
	address := make([]byte, net.IPv6len)
	for i := range address {
		v, err := strconv.Atoi(parts[i])
		if err != nil || v < 0 || v > 255 {
			return "", false
		}
		address[i] = byte(v)
	}
	prefix, err := strconv.Atoi(parts[16])
	if err != nil || prefix < 0 || prefix > 128 {
		return "", false
	}
	if _, err := strconv.ParseUint(parts[17], 10, 32); err != nil {
		return "", false
	}
	return net.IP(address).String() + "/" + strconv.Itoa(prefix), true
}

func routeIPValue(v interface{}) net.IP {
	switch x := v.(type) {
	case string:
		return net.ParseIP(x)
	case []byte:
		if len(x) == net.IPv4len || len(x) == net.IPv6len {
			return net.IP(x)
		}
		return net.ParseIP(string(x))
	default:
		return nil
	}
}

func normalizeNextHop(ip net.IP) string {
	if ip == nil || ip.IsUnspecified() {
		return "-"
	}
	return ip.String()
}

// newRouteEntry 按来源表的枚举定义解码一行路由。缺失或无效的 metric/age 记为 -1,
// 与设备真实返回的 0 区分开。
func newRouteEntry(table, destination, nextHop string, ifIndex int64, cols routeColumns, index string, ifs map[string]*ifInfo) RouteEntry {
	iface := "-"
	if e, ok := ifs[strconv.FormatInt(ifIndex, 10)]; ok {
		iface = e.display()
	} else if ifIndex > 0 {
		iface = "ifIndex-" + strconv.FormatInt(ifIndex, 10)
	}
	switch nextHop {
	case "", "0.0.0.0", "::":
		nextHop = "-"
	}
	typeCode, hasType := cols.intAt("type", index)
	protoCode, hasProto := cols.intAt("proto", index)
	if !hasType {
		typeCode = 0
	}
	if !hasProto {
		protoCode = 0
	}
	metric := int64(-1)
	if v, ok := cols.intAt("metric", index); ok && v >= 0 {
		metric = v
	}
	age := int64(-1)
	// 年龄是 Gauge32/Integer32 秒数;负数或按无符号读回的 0xFFFFFFFF 等都是"未提供"。
	if v, ok := cols.intAt("age", index); ok && v >= 0 && v <= math.MaxInt32 {
		age = v
	}
	return RouteEntry{
		Destination: destination, NextHop: nextHop, IfIndex: ifIndex, Interface: iface,
		Type: routeTypeName(table, typeCode), Protocol: routeProtocolName(table, protoCode),
		TypeCode: typeCode, ProtocolCode: protoCode, TypeEnum: table, ProtocolEnum: table,
		Metric: metric, Age: age,
	}
}

func (e RouteEntry) enumValid() bool {
	return routeTypeValid(e.TypeEnum, e.TypeCode) && routeProtocolValid(e.ProtocolEnum, e.ProtocolCode)
}

func routeTypeValid(table string, code int64) bool {
	_, ok := routeTypeNames(table)[code]
	return ok
}

func routeProtocolValid(table string, code int64) bool {
	_, ok := routeProtocolNames(table)[code]
	return ok
}

func routeTypeName(table string, code int64) string {
	if name, ok := routeTypeNames(table)[code]; ok {
		return name
	}
	return "未上报"
}

func routeProtocolName(table string, code int64) string {
	if name, ok := routeProtocolNames(table)[code]; ok {
		return name
	}
	return "未上报"
}

var (
	// inetCidrRouteType (RFC 4292); ipCidrRouteType (RFC 2096) 是其 1..4 子集。
	cidrRouteTypes = map[int64]string{1: "其他", 2: "拒绝", 3: "本地", 4: "远端", 5: "黑洞"}
	// ipRouteType (RFC 1213)。invalid(2) 行在采集时已跳过。
	ipRouteTypes = map[int64]string{1: "其他", 2: "无效", 3: "直连", 4: "间接"}
	// ipv6RouteType (RFC 2465)。
	ipv6RouteTypes = map[int64]string{1: "其他", 2: "丢弃", 3: "本地", 4: "远端"}

	// IANAipRouteProtocol,ipCidrRouteProto / ipRouteProto 使用同一编号的子集。
	ianaRouteProtocols = map[int64]string{
		1: "其他", 2: "本地", 3: "管理配置", 4: "ICMP", 5: "EGP", 6: "GGP",
		7: "Hello", 8: "RIP", 9: "IS-IS", 10: "ES-IS", 11: "Cisco IGRP",
		12: "BBN SPF IGP", 13: "OSPF", 14: "BGP", 15: "IDPR",
		16: "EIGRP", 17: "DVMRP", 18: "RPL", 19: "DHCP", 20: "TTDP",
	}
	// ipv6RouteProtocol (RFC 2465): 4=ndisc(邻居发现/重定向), 6=ospf。
	ipv6RouteProtocols = map[int64]string{
		1: "其他", 2: "本地", 3: "管理配置", 4: "邻居发现", 5: "RIPng",
		6: "OSPFv3", 7: "BGP", 8: "IDRP", 9: "IGRP",
	}
)

func routeTypeNames(table string) map[int64]string {
	switch table {
	case "ipRouteTable":
		return ipRouteTypes
	case "ipv6RouteTable":
		return ipv6RouteTypes
	default:
		return cidrRouteTypes
	}
}

func routeProtocolNames(table string) map[int64]string {
	if table == "ipv6RouteTable" {
		return ipv6RouteProtocols
	}
	return ianaRouteProtocols
}

// sortRouteEntries 按地址数值排序(IPv4 在前),而不是按字符串排序,
// 否则 10.0.0.0/8 会排在 9.0.0.0/8 前面。
func sortRouteEntries(rows []RouteEntry) {
	type key struct {
		ip     net.IP
		prefix int
	}
	parse := func(destination string) key {
		addr, prefix, _ := strings.Cut(destination, "/")
		if i := strings.IndexByte(addr, '%'); i >= 0 {
			addr = addr[:i]
		}
		ip := net.ParseIP(addr)
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		n, _ := strconv.Atoi(prefix)
		return key{ip, n}
	}
	keys := make(map[string]key, len(rows))
	for _, r := range rows {
		if _, ok := keys[r.Destination]; !ok {
			keys[r.Destination] = parse(r.Destination)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := keys[rows[i].Destination], keys[rows[j].Destination]
		if len(a.ip) != len(b.ip) {
			return len(a.ip) < len(b.ip)
		}
		if c := bytes.Compare(a.ip, b.ip); c != 0 {
			return c < 0
		}
		if a.prefix != b.prefix {
			return a.prefix < b.prefix
		}
		if rows[i].Destination != rows[j].Destination {
			return rows[i].Destination < rows[j].Destination
		}
		if rows[i].NextHop != rows[j].NextHop {
			return rows[i].NextHop < rows[j].NextHop
		}
		return rows[i].Interface < rows[j].Interface
	})
}
