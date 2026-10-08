/*
 * VLANIF / 三层接口、VLAN 粒度 MAC 表和按需读取的 IP 路由表。
 */
var deviceRouteState = { key: '', loading: '', rows: [], source: '', message: '', timestamp: '', cached: false, stale: false, request: 0 };

detailPart({
  fill: function (d) {
    renderVlanIfTable(d);
    renderFDBTable(d);
    var key = d.ip;
    if (deviceRouteState.key && deviceRouteState.key !== key) {
      deviceRouteState.request++;
      deviceRouteState.key = '';
      deviceRouteState.loading = '';
      deviceRouteState.rows = [];
      deviceRouteState.source = '';
      deviceRouteState.message = '';
      deviceRouteState.timestamp = '';
      deviceRouteState.cached = false;
      deviceRouteState.stale = false;
    }
  }
});

function renderVlanIfTable(d) {
  d = d || currentDetail;
  if (!d) { return; }
  var grouped = {};
  (d.own_addrs || []).forEach(function (a) {
    var name = a.if || '未知接口';
    var row = grouped[name] || (grouped[name] = {
      name: name, ips: [], admin_status: a.admin_status || 0,
      oper_status: a.oper_status || 0, admin_down: !!a.admin_down, oper_down: !!a.oper_down
    });
    var value = a.ip + (a.prefix ? '/' + a.prefix : '');
    if (row.ips.indexOf(value) < 0) { row.ips.push(value); }
    if (!row.admin_status && a.admin_status) { row.admin_status = a.admin_status; }
    if (!row.oper_status && a.oper_status) { row.oper_status = a.oper_status; }
    row.admin_down = row.admin_down || !!a.admin_down;
    row.oper_down = row.oper_down || !!a.oper_down;
  });
  var rows = Object.keys(grouped).map(function (name) {
    grouped[name].ips.sort(function (a, b) { return a.localeCompare(b, undefined, { numeric: true }); });
    return grouped[name];
  }).sort(function (a, b) { return naturalCmp(a.name, b.name); });
  var count = byId('vlanIfCount');
  if (count) { count.innerText = rows.length; }
  var body = byId('vlanIfBody');
  var kw = (byId('vlanIfSearch').value || '').trim().toLowerCase();
  body.innerHTML = '';
  rows.forEach(function (r) {
    var ipText = r.ips.join(' ');
    if (kw && (r.name + ' ' + ipText).toLowerCase().indexOf(kw) < 0) { return; }
    var status = ownIfStatus(r);
    var statusColor = status === 'UP' ? 'var(--good)' : (status === 'DOWN' || status === 'ADMIN-DOWN' ? 'var(--bad)' : 'var(--fg-faint)');
    var vlanID = vlanIDFromIfName(r.name);
    var vlanActions = vlanID === '' ? '' :
      ' <button class="filter-btn" style="padding:2px 6px;margin-left:5px;" title="查看 VLAN ' + vlanID + ' 的 ARP 记录" onclick="jumpDeviceVlan(\'' + vlanID + '\',2)">ARP</button>' +
      ' <button class="filter-btn" style="padding:2px 6px;margin-left:3px;" title="查看 VLAN ' + vlanID + ' 的 MAC 转发表" onclick="jumpDeviceVlan(\'' + vlanID + '\',11)">MAC</button>';
    var primary = r.ips.length ? '<code>' + esc(r.ips[0]) + '</code>' : '<span style="color:var(--fg-faint);">-</span>';
    var extra = r.ips.length > 1 ? r.ips.slice(1).map(function (ip) { return '<code style="margin-right:6px;">' + esc(ip) + '</code>'; }).join('') : '<span style="color:var(--fg-faint);">-</span>';
    var tr = document.createElement('tr');
    tr.innerHTML = '<td><b>' + esc(r.name) + '</b>' + vlanActions + '</td>' +
      '<td><b style="color:' + statusColor + ';">' + esc(status) + '</b></td>' +
      '<td>' + primary + '</td><td>' + extra + '</td>';
    body.appendChild(tr);
  });
  byId('vlanIfSummary').innerText = rows.length + ' 个接口';
  if (!body.children.length) {
    body.innerHTML = '<tr><td colspan="4" style="text-align:center;color:var(--fg-faint);padding:24px;">没有匹配的三层接口地址</td></tr>';
  }
}

function vlanIDFromIfName(name) {
  var match = /^vlan(?:if|[- ]interface)?\s*(\d+)$/i.exec(String(name || '').trim());
  return match ? match[1] : '';
}

function jumpDeviceVlan(vlan, tab) {
  vlan = String(vlan || '').trim();
  if (!/^\d+$/.test(vlan)) { return; }
  if (tab === 2) {
    switchTab(2);
    byId('arpSearch').value = '';
    msClearAll(['arpPortFilter', 'arpKindFilter', 'arpVlanFilter']);
    msSetValues('arpVlanFilter', [vlan]);
    renderHosts();
    return;
  }
  if (tab === 11) {
    switchTab(11);
    byId('fdbSearch').value = '';
    byId('fdbVlanFilter').value = vlan;
    renderFDBTable();
  }
}

function ownIfStatus(r) {
  if (r.admin_down || r.admin_status === 2) { return 'ADMIN-DOWN'; }
  if (r.oper_status === 1) { return 'UP'; }
  if (r.oper_down || r.oper_status === 2 || r.oper_status === 7) { return 'DOWN'; }
  return 'UNKNOWN';
}

function renderFDBTable(d) {
  d = d || currentDetail;
  if (!d) { return; }
  var rows = Array.isArray(d.fdb_table) ? d.fdb_table : (d.mac_table || []);
  var unique = {};
  rows.forEach(function (r) { unique[r.mac] = true; });
  var count = byId('fdbCount');
  if (count) { count.innerText = rows.length; }
  var vlans = {};
  rows.forEach(function (r) { if (r.vlan_id && r.vlan_id !== '-') { vlans[r.vlan_id] = true; } });
  var vlanSelect = byId('fdbVlanFilter');
  var selectedVlan = vlanSelect.value;
  if (selectedVlan) { vlans[selectedVlan] = true; }
  vlanSelect.innerHTML = '<option value="">全部 VLAN</option>' +
    Object.keys(vlans).sort(naturalCmp).map(function (v) { return '<option value="' + esc(v) + '">VLAN ' + esc(v) + '</option>'; }).join('');
  vlanSelect.value = selectedVlan;
  var kw = (byId('fdbSearch').value || '').trim().toLowerCase();
  var filterVlan = vlanSelect.value;
  var body = byId('fdbBody');
  body.innerHTML = '';
  var visible = 0;
  rows.forEach(function (r) {
    if (filterVlan && r.vlan_id !== filterVlan) { return; }
    var hay = [r.mac, r.vlan_id, r.port, r.port_kind, (r.locations || []).join(' ')].join(' ').toLowerCase();
    if (kw && hay.indexOf(kw) < 0 && !macSearchMatches(r.mac, kw)) { return; }
    visible++;
    var role = r.port_kind || '-';
    if (r.ambiguous && r.locations && r.locations.length) {
      role += ' · 多位置: ' + r.locations.join(', ');
    }
    var tr = document.createElement('tr');
    tr.innerHTML = '<td><code>' + esc(r.mac) + '</code></td>' +
      '<td><b>' + esc(r.vlan_id || '-') + '</b></td>' +
      '<td>' + esc(r.port || '-') + '</td><td>' + esc(role) + '</td>';
    body.appendChild(tr);
  });
  var source = d.fdb_source || 'SNMP 转发表';
  byId('fdbSummary').innerText = visible + ' / ' + rows.length + ' 条 VLAN-MAC 项 · ' +
    Object.keys(unique).length + ' 个唯一 MAC · 来源 ' + source;
  if (!body.children.length) {
    body.innerHTML = '<tr><td colspan="4" style="text-align:center;color:var(--fg-faint);padding:24px;">暂无转发表项；可立即巡检后再查看</td></tr>';
  }
}

function loadDeviceRoutes(d, force) {
  d = d || currentDetail;
  if (!d || !d.ip) { return; }
  var key = d.ip;
  if (!force && (deviceRouteState.key === key || deviceRouteState.loading === key)) {
    renderRouteTable();
    return;
  }
  var request = ++deviceRouteState.request;
  deviceRouteState.key = key;
  deviceRouteState.loading = key;
  deviceRouteState.message = force ? '正在刷新 SNMP 路由表…' : '正在读取已保存的路由表…';
  byId('routeReload').disabled = true;
  renderRouteTable();
  var path = '/api/device/routes?ip=' + encodeURIComponent(d.ip) + (force ? '&refresh=1' : '');
  pub(path).then(function (result) {
    if (request !== deviceRouteState.request || currentIP !== d.ip) { return; }
    deviceRouteState.rows = result.routes || [];
    deviceRouteState.source = result.source || '';
    deviceRouteState.message = result.message || '';
    deviceRouteState.timestamp = result.timestamp || '';
    deviceRouteState.cached = !!result.cached;
    deviceRouteState.stale = !!result.stale;
    deviceRouteState.key = key;
    deviceRouteState.loading = '';
    byId('routeCount').innerText = deviceRouteState.rows.length;
    renderRouteTable();
  }).catch(function (e) {
    if (request !== deviceRouteState.request || currentIP !== d.ip) { return; }
    deviceRouteState.message = (deviceRouteState.rows.length ? '刷新失败，仍显示上次数据: ' : '读取失败: ') + String(e);
    if (!deviceRouteState.rows.length) { deviceRouteState.key = key; }
    deviceRouteState.loading = '';
    byId('routeCount').innerText = deviceRouteState.rows.length;
    renderRouteTable();
  }).finally(function () {
    if (request === deviceRouteState.request && byId('routeReload')) { byId('routeReload').disabled = false; }
  });
}

// 路由年龄(秒)；负数 / 缺失表示设备未提供。
function routeAgeText(sec) {
  if (typeof sec !== 'number' || sec < 0) { return '-'; }
  var d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
  if (d) { return d + '天' + h + '小时'; }
  if (h) { return h + '小时' + m + '分'; }
  if (m) { return m + '分' + s + '秒'; }
  return s + '秒';
}

function renderRouteTable() {
  var body = byId('routeBody');
  if (!body) { return; }
  var kw = (byId('routeSearch').value || '').trim().toLowerCase();
  var rows = deviceRouteState.rows || [];
  body.innerHTML = '';
  var visible = 0;
  rows.forEach(function (r) {
    var metric = typeof r.metric === 'number' && r.metric >= 0 ? String(r.metric) : '-';
    var age = routeAgeText(r.age);
    var hay = [r.destination, r.next_hop, r.interface, r.protocol, r.type, metric, age].join(' ').toLowerCase();
    if (kw && hay.indexOf(kw) < 0) { return; }
    visible++;
    var tr = document.createElement('tr');
    var protoTitle = 'SNMP 原始值 ' + (r.protocol_code === undefined ? '-' : r.protocol_code) + (r.protocol_enum ? '（' + r.protocol_enum + '）' : '');
    var typeTitle = 'SNMP 原始值 ' + (r.type_code === undefined ? '-' : r.type_code) + (r.type_enum ? '（' + r.type_enum + '）' : '');
    tr.innerHTML = '<td><b>' + esc(r.destination) + '</b></td><td>' + esc(r.next_hop || '-') + '</td>' +
      '<td>' + esc(r.interface || '-') + '</td><td title="' + esc(protoTitle) + '">' + esc(r.protocol || '-') + '</td>' +
      '<td title="' + esc(typeTitle) + '">' + esc(r.type || '-') + '</td><td>' + esc(metric) + '</td>' +
      '<td title="' + esc(typeof r.age === 'number' && r.age >= 0 ? r.age + ' 秒' : '设备未提供') + '">' + esc(age) + '</td>';
    body.appendChild(tr);
  });
  var state = deviceRouteState.message || (deviceRouteState.loading ? deviceRouteState.loading : '');
  var savedAt = deviceRouteState.timestamp ? (deviceRouteState.cached ? '缓存于 ' : '更新于 ') + deviceRouteState.timestamp : '';
  byId('routeSummary').innerText = [visible + ' 条', deviceRouteState.source, savedAt, state].filter(Boolean).join(' · ');
  if (!body.children.length) {
    body.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--fg-faint);padding:24px;">' +
      esc(state || '切换到此页后按需读取路由表') + '</td></tr>';
  }
}

detailPart({
  tab: 12,
  show: function (d) { loadDeviceRoutes(d, false); }
});
