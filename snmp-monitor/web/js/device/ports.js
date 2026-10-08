/*
 * 设备详情: 端口表。
 */
/* ---------- 端口表 ---------- */
detailPart({ fill: function () { renderPorts(); } });
// 端口筛选按钮可以多选; 点「全部」清空所有条件。
function setFilter(mode, el) {
  if (mode === 'ALL') { portFilters = {}; } else { portFilters[mode] = !portFilters[mode]; }
  syncPortFilterButtons();
  renderPorts();
}
function syncPortFilterButtons() {
  var any = Object.keys(portFilters).some(function (k) { return portFilters[k]; });
  document.querySelectorAll('#tab1 .pf-btn').forEach(function (b) {
    var f = b.getAttribute('data-f');
    b.classList.toggle('active', f === 'ALL' ? !any : !!portFilters[f]);
  });
}
function sortBy(f) {
  if (sortField === f) { sortAsc = !sortAsc; } else { sortField = f; sortAsc = true; }
  renderPorts();
}
function naturalCmp(a, b) {
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
}
function num(v, fallback) { return (v === null || v === undefined || isNaN(v)) ? fallback : v; }

function opticalBadge(text, val) {
  if (val === null || val === undefined) { return '<span style="color:var(--fg-faint);">-</span>'; }
  var cls = 'optical-good', tag = '正常';
  if (val <= -25) { cls = 'optical-bad'; tag = '极弱'; }
  else if (val <= -20) { cls = 'optical-warn'; tag = '偏低'; }
  return '<span class="optical-badge ' + cls + '">' + esc(text) + ' (' + tag + ')</span>';
}

// portLayerSmallText: 物理接口这一列以外, "层级/VLAN" 不再单独占一整列,
// 而是以小字附在接口名下面——L3 口直接给 IP; L2 口不再直接把 PVID
// 当成"这个口就是这个 VLAN"来显示(trunk 口的 PVID 只是 native VLAN,
// 之前写成 "L2 VLAN 1" 会让人误以为是纯 access 口, 是错的)。这里改成
// 用是否为上联/级联口(有 LLDP 邻居或学到的 MAC 数很多)来猜 trunk 还是
// access —— trunk 口只写 "trunk", access 口写 "access · VLAN x"。
// v9: 后端现在从 Q-BRIDGE VLAN 成员表直接给出 access/trunk/hybrid 和允许 VLAN 列表,
// 不再靠"是不是上联口"去猜; 只有设备不支持成员表、只拿到 PVID 时才退回旧的猜测并标注。
function vlanListShort(s, n) {
  if (!s) { return ''; }
  n = n || 28;
  return s.length > n ? '<span title="' + esc(s) + '">' + esc(s.slice(0, n)) + '…</span>' : esc(s);
}
function portLayerSmallText(p) {
  if (p.layer === 'L3' && p.ip_addr) {
    return '<span class="tag tag-l3">L3</span> <code class="ipcode">' + esc(p.ip_addr) + '</code>';
  }
  var src = p.vlan_source ? ' title="来源: ' + esc(p.vlan_source) + '"' : '';
  switch (p.vlan_mode) {
    case 'access':
      return '<span class="tag tag-access"' + src + '>access</span> VLAN <b>' + esc(p.vlan_id) + '</b>';
    case 'trunk':
      return '<span class="tag tag-trunk"' + src + '>trunk</span> native ' + esc(p.vlan_id || '-') +
        (p.vlan_tagged ? ' · 允许 ' + vlanListShort(p.vlan_tagged) : '');
    case 'hybrid':
      return '<span class="tag tag-hybrid"' + src + '>hybrid</span> untag ' + vlanListShort(p.vlan_untagged, 16) +
        (p.vlan_tagged ? ' · tag ' + vlanListShort(p.vlan_tagged, 16) : '');
  }
  if (p.layer === 'L2' && p.vlan_id) {
    return '<span class="tag" title="设备没有提供 VLAN 成员表, 只拿到端口默认 VLAN(PVID), access/trunk 是按是否上联口推测的">PVID</span> ' +
      esc(p.vlan_id) + (p.is_uplink ? ' (疑似 trunk)' : '');
  }
  return '';
}
function duplexBadge(p) {
  if (!p.duplex) { return p.status === 'UP' ? '<span class="sub faint" title="设备没有上报双工状态">双工未知</span>' : ''; }
  var cls = p.duplex === '半双工' ? 'bad' : 'faint';
  return '<span class="sub ' + cls + '" title="来源: ' + esc(p.duplex_source || '') + '">' + esc(p.duplex) + '</span>';
}
function resetColumnWidths(tableId) {
  try { localStorage.removeItem('colw:' + tableId); } catch (e) {}
  var t = byId(tableId);
  if (!t) { return; }
  t.style.tableLayout = '';
  t.querySelectorAll('thead th').forEach(function (th) { th.style.width = ''; });
}
