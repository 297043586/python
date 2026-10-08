/*
 * 设备管理(需要登录)。
 */
/* ---------- 设备管理 (需要登录) ---------- */
// 品牌下拉: 内置 + 「设置 → 厂商 / OID」里新增的品牌
function loadBrandOptions() {
  return fetch('/api/vendors/list', { credentials: 'same-origin' }).then(function (r) { return r.json(); }).then(function (d) {
    var sel = byId('fBrand'), cur = sel.value;
    sel.innerHTML = '<option value="auto">自动识别 (推荐)</option>' + (d.vendors || []).map(function (v) {
      return '<option value="' + esc(v.id) + '">' + esc(v.name) + '</option>';
    }).join('');
    sel.value = cur || 'auto';
    if (!sel.value) { sel.value = 'auto'; }
  }).catch(function () {});
}
function openDeviceManager() {
  if (!authenticated) {
    openAuthGate(function () { openDeviceManager(); });
    return;
  }
  resetForm();
  byId('mgrModal').style.display = 'flex';
  loadBrandOptions();
  loadDevices();
}
function closeMgr() { byId('mgrModal').style.display = 'none'; }
modalRegister('mgrModal', closeMgr);

// 从端口详情里"这个口 LLDP 对端还没被监控"直接打开新增表单并带上 IP,
// 省得再手动去查/去问网管这台设备的管理 IP 是多少。
function quickAddFromLLDP(ip, name) {
  if (byId('mgrModal').style.display !== 'flex') { openDeviceManager(); }
  setTimeout(function () {
    resetForm();
    byId('fIP').value = ip || '';
    if (name) { byId('fNote').value = name; }
    byId(ip ? 'fCommunity' : 'fIP').focus();
    byId('formTitle').scrollIntoView({ block: 'nearest' });
  }, 60);
}

// 🔍 LLDP 发现: 全网 LLDP 看得到但还没加入监控的邻居, 带管理 IP 的可以直接添加
function mgrToggleLLDP() {
  var box = byId('mgrLLDP');
  if (box.style.display === 'block') { box.style.display = 'none'; return; }
  box.style.display = 'block';
  box.innerHTML = '<div class="empty"><span class="spin">⟳</span> 加载中…</div>';
  authedApi('/api/devices/lldp').then(function (d) {
    var list = d.neighbors || [];
    if (!list.length) { box.innerHTML = '<div class="empty">LLDP 看到的邻居都已经在监控里了</div>'; return; }
    var withIP = list.filter(function (x) { return x.ip; }).length;
    box.innerHTML = '<div style="font-size:12.5px;color:var(--fg-dim);margin-bottom:6px;">LLDP 发现 ' + list.length + ' 台未纳管的邻居, 其中 ' + withIP + ' 台知道管理 IP。点「添加」会把 IP 和名字填进上面的表单, 填团体名后保存即可。</div>' +
      '<table><thead><tr><th>邻居名</th><th>管理 IP</th><th>接在</th><th></th></tr></thead><tbody>' + list.map(function (x) {
        var q = function (v) { return '&quot;' + esc(v || '') + '&quot;'; };
        return '<tr><td><b>' + esc(x.name) + '</b>' + (x.mac ? '<div class="sub2">' + esc(x.mac) + '</div>' : '') + '</td>' +
          '<td>' + (x.ip ? esc(x.ip) + (x.ip_src === 'arp' ? ' <span class="sub2" style="display:inline" title="对端 LLDP 没带管理地址, 这是按它的机框 MAC 在全网 ARP 里查到的">(ARP 推断)</span>' : '') : '<span class="muted">对端没上报</span>') + '</td>' +
          '<td style="font-size:12px;">' + x.seen.slice(0, 3).map(esc).join('<br>') + (x.seen.length > 3 ? '<br>… 共 ' + x.seen.length + ' 处' : '') + '</td>' +
          '<td><button class="filter-btn" onclick="quickAddFromLLDP(' + q(x.ip) + ', ' + q(x.name) + ')">＋ 添加</button></td></tr>';
      }).join('') + '</tbody></table>';
  }).catch(function (e) { box.innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '</div>'; });
}

function mgrMsg(text, ok) {
  var n = byId('mgrNotice');
  n.className = 'notice ' + (ok ? 'notice-ok' : 'notice-err');
  n.innerText = text;
  n.style.display = 'block';
}

function loadDevices() {
  authedApi('/api/devices').then(function (d) {
    deviceList = d.devices || [];
    mgrSel = {};
    var groups = {};
    deviceList.forEach(function (x) { if (x.group) { groups[x.group] = true; } });
    byId('fGroupList').innerHTML = Object.keys(groups).sort(cmpNatural).map(function (g) { return '<option value="' + esc(g) + '">'; }).join('');
    renderDeviceTable();
  }).catch(function (e) { mgrMsg('读取设备列表失败: ' + e, false); });
  loadRackSlots();
}

// 机柜位置跟「🗄️ 机柜」/ 3D 机房共用 racks.json; 这里只是多一个入口
var mgrRacks = {};
function loadRackSlots() {
  Promise.all([pub('/api/panels'), pub('/api/datacenter').catch(function () { return {}; })]).then(function (res) {
    mgrRacks = {};
    var names = {};
    (res[0].devices || []).forEach(function (d) { mgrRacks[d.ip] = d; if (d.rack) { names[d.rack] = true; } });
    ((res[1].layout || res[1]).racks || []).forEach(function (k) { if (!k.type || k.type === 'rack') { names[k.name] = true; } });
    byId('fRackList').innerHTML = Object.keys(names).sort(cmpNatural).map(function (n) { return '<option value="' + esc(n) + '">'; }).join('');
  }).catch(function () {});
}

// 设备表: 可搜索、可按分组显示、可勾选后批量设置分组(v13)
var mgrSel = {};
function renderDeviceTable() {
  var body = byId('mgrBody');
  if (deviceList.length === 0) {
    body.innerHTML = '<tr><td colspan="8" style="text-align:center;color:var(--fg-faint);padding:20px;">还没有节点</td></tr>';
    mgrSyncSel();
    return;
  }
  var q = (byId('mgrQ').value || '').trim().toLowerCase();
  var list = deviceList.filter(function (x) { return !q || (x.ip + ' ' + (x.note || '') + ' ' + (x.group || '') + ' ' + x.brand + ' ' + (x.location || '') + ' ' + (x.contact || '')).toLowerCase().indexOf(q) >= 0; });
  list.sort(function (a, b) { return cmpNatural(ipSortKey(a.ip), ipSortKey(b.ip)); });
  var rowHTML = function (x) {
    return '<tr><td><input type="checkbox" data-sel="' + esc(x.id) + '"' + (mgrSel[x.id] ? ' checked' : '') + '></td><td><b>' + esc(x.ip) + '</b></td>' +
      '<td>' + esc(x.brand) + '</td><td>' + esc(x.version) + '</td><td>' + (x.group ? '<span class="lv info">' + esc(x.group) + '</span>' : '<span class="muted">-</span>') + '</td>' +
      '<td>' + esc(x.note || '-') + (x.location ? '<div class="sub2" title="位置">📍 ' + esc(x.location) + '</div>' : '') + (x.contact ? '<div class="sub2" title="联系人">👤 ' + esc(x.contact) + '</div>' : '') + '</td>' +
      '<td>' + (x.disabled ? '<span class="pill pill-none">已暂停</span>' : '<span class="pill pill-access">巡检中</span>') + '</td>' +
      '<td><button class="abtn" onclick="editDevice(&quot;' + x.id + '&quot;)">编辑</button> ' +
      '<button class="abtn" style="color:var(--bad);" onclick="deleteDevice(&quot;' + x.id + '&quot;)">删除</button></td></tr>';
  };
  var html = '';
  if (byId('mgrByGroup').checked) {
    var by = {};
    list.forEach(function (x) { (by[x.group || ''] = by[x.group || ''] || []).push(x); });
    Object.keys(by).sort(function (a, b) { return (a === '') - (b === '') || cmpNatural(a, b); }).forEach(function (g) {
      html += '<tr class="mgr-g"><td colspan="8"><b>' + esc(g || '(未分组)') + '</b> <span class="vt-gn">' + by[g].length + '</span></td></tr>' + by[g].map(rowHTML).join('');
    });
  } else {
    html = list.map(rowHTML).join('');
  }
  body.innerHTML = html || '<tr><td colspan="8" style="text-align:center;color:var(--fg-faint);padding:20px;">没有匹配的节点</td></tr>';
  mgrSyncSel();
}
function mgrSyncSel() {
  var n = Object.keys(mgrSel).length;
  byId('mgrSelInfo').innerText = n ? '已选 ' + n + ' 台' : '';
  byId('mgrBulkBtn').disabled = !n;
  byId('mgrAll').checked = n > 0 && n === deviceList.length;
}
function mgrSelectAll(on) {
  mgrSel = {};
  if (on) { document.querySelectorAll('#mgrBody [data-sel]').forEach(function (cb) { mgrSel[cb.getAttribute('data-sel')] = true; }); }
  renderDeviceTable();
}
byId('mgrBody').addEventListener('change', function (e) {
  var id = e.target.getAttribute && e.target.getAttribute('data-sel');
  if (!id) { return; }
  if (e.target.checked) { mgrSel[id] = true; } else { delete mgrSel[id]; }
  mgrSyncSel();
});
// 批量设置分组: 逐台调更新接口(团体名留空 = 不改)
function bulkSetGroup() {
  var ids = Object.keys(mgrSel);
  if (!ids.length) { return; }
  var name = prompt('把选中的 ' + ids.length + ' 台设备放进哪个分组?(留空 = 移出分组)', '');
  if (name === null) { return; }
  name = name.trim();
  var jobs = ids.map(function (id) {
    var x = lookupDevice(id), c = (x && x.collect) || {};
    if (!x) { return Promise.resolve(); }
    return authedApi('/api/devices/update', { id: x.id, ip: x.ip, community: '', brand: x.brand, version: x.version, note: x.note || '', group: name,
      disabled: !!x.disabled, vlan_community_format: x.vlan_community_format || '', collect_optical: !c.skip_optical, collect_lldp: !c.skip_lldp,
      collect_rates: !c.skip_rates, collect_hosts: !c.skip_hosts, collect_extended: !c.skip_extended });
  });
  Promise.all(jobs).then(function () {
    mgrMsg('已把 ' + ids.length + ' 台设备' + (name ? '放进「' + name + '」' : '移出分组') + ', 下一轮巡检后首页生效', true);
    loadDevices();
    setTimeout(refreshNow, 2500);
  }).catch(function (e) { mgrMsg('设置分组失败: ' + e, false); loadDevices(); });
}

function resetForm() {
  editingID = '';
  byId('formTitle').innerText = '新增节点';
  byId('fIP').value = ''; byId('fCommunity').value = '';
  byId('fBrand').value = 'auto'; byId('fVersion').value = '2c';
  byId('fNote').value = ''; byId('fGroup').value = ''; byId('fDisabled').checked = false;
  byId('fLocation').value = ''; byId('fContact').value = '';
  byId('fRack').value = ''; byId('fRack').placeholder = '如 A02'; byId('fRackU').value = 0;
  byId('fVlanFormat').value = '';
  byId('cOptical').checked = true; byId('cLLDP').checked = true;
  byId('cRates').checked = true; byId('cHosts').checked = true; byId('cExt').checked = true;
  byId('fCommunity').placeholder = '必填';
  byId('fCommunityHint').innerText = '团体名加密保存, 页面与接口都不会回显';
}

function lookupDevice(id) {
  var found = null;
  deviceList.forEach(function (d) { if (d.id === id) { found = d; } });
  return found;
}

function editDevice(id) {
  var x = lookupDevice(id);
  if (!x) { return; }
  editingID = id;
  byId('formTitle').innerText = '编辑节点 ' + x.ip;
  byId('fIP').value = x.ip;
  byId('fCommunity').value = '';
  byId('fCommunity').placeholder = '留空表示不修改';
  byId('fCommunityHint').innerText = x.has_community ? '已保存团体名, 留空即保持不变' : '尚未设置团体名';
  byId('fBrand').value = x.brand || 'auto';
  byId('fVersion').value = x.version || '2c';
  byId('fNote').value = x.note || '';
  byId('fGroup').value = x.group || '';
  byId('fLocation').value = x.location || '';
  byId('fContact').value = x.contact || '';
  var rs = mgrRacks[x.ip] || {};
  byId('fRack').value = rs.manual ? rs.rack : '';
  byId('fRack').placeholder = rs.rack && !rs.manual ? '现在自动归到: ' + rs.rack : '如 A02';
  byId('fRackU').value = rs.manual ? (rs.u || 0) : 0;
  byId('fDisabled').checked = !!x.disabled;
  byId('fVlanFormat').value = x.vlan_community_format || '';
  var c = x.collect || {};
  byId('cOptical').checked = !c.skip_optical;
  byId('cLLDP').checked = !c.skip_lldp;
  byId('cRates').checked = !c.skip_rates;
  byId('cHosts').checked = !c.skip_hosts;
  byId('cExt').checked = !c.skip_extended;
}

function formBody() {
  return {
    id: editingID,
    ip: byId('fIP').value,
    community: byId('fCommunity').value,
    brand: byId('fBrand').value,
    version: byId('fVersion').value,
    note: byId('fNote').value,
    group: byId('fGroup').value,
    location: byId('fLocation').value,
    contact: byId('fContact').value,
    disabled: byId('fDisabled').checked,
    vlan_community_format: byId('fVlanFormat').value,
    collect_optical: byId('cOptical').checked,
    collect_lldp: byId('cLLDP').checked,
    collect_rates: byId('cRates').checked,
    collect_hosts: byId('cHosts').checked,
    collect_extended: byId('cExt').checked
  };
}

function saveDevice() {
  var editing = editingID !== '';
  var path = editing ? '/api/devices/update' : '/api/devices/add';
  var ip = byId('fIP').value.trim(), rack = byId('fRack').value.trim(), u = parseInt(byId('fRackU').value, 10) || 0;
  var old = mgrRacks[ip] || {}, rackChanged = rack !== (old.manual ? old.rack : '') || (rack && u !== (old.u || 0));
  authedApi(path, formBody()).then(function () {
    // 机柜位置单独一个接口(要「机房 / 机柜」权限), 没权限只提示, 设备本身已经保存了
    return rackChanged ? authedApi('/api/racks/assign', { ip: ip, rack: rack, u: rack ? u : 0 }).then(function () { return ''; }, function (e) { return '; 机柜位置没保存: ' + e; }) : '';
  }).then(function (rackMsg) {
    mgrMsg((editing ? '已保存, 正在重新巡检' : '已添加, 正在立即巡检该节点') + (rackMsg || ''), !rackMsg);
    resetForm();
    loadDevices();
    setTimeout(refreshNow, 2500);
  }).catch(function (e) { mgrMsg('保存失败: ' + e, false); });
}

function testDevice() {
  mgrMsg('正在测试连接...', true);
  authedApi('/api/devices/test', formBody()).then(function (d) {
    if (d.ok) {
      mgrMsg('连接成功: ' + (d.sys_name || '(未返回设备名)') +
             (d.brand_detected ? (' | 识别为 ' + d.brand_detected) : ' | 未能识别厂商'), true);
    } else {
      mgrMsg('连接失败: ' + d.message, false);
    }
  }).catch(function (e) { mgrMsg('测试失败: ' + e, false); });
}

function deleteDevice(id) {
  var x = lookupDevice(id);
  if (!x) { return; }
  if (!confirm('确定要移除节点 ' + x.ip + ' 吗?\n它的历史趋势和告警记录也会一并清除。')) { return; }
  authedApi('/api/devices/delete', { id: id }).then(function () {
    mgrMsg('已移除 ' + x.ip, true);
    resetForm();
    loadDevices();
    setTimeout(refreshNow, 1500);
  }).catch(function (e) { mgrMsg('删除失败: ' + e, false); });
}
