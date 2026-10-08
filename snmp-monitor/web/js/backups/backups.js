/*
 * 配置备份。
 */
/* ================= 配置备份 ================= */
var bkState = { devices: [], poll: null, anim: null };
function openBackups() {
  if (!authenticated) { openAuthGate(function () { openBackups(); }); return; }
  byId('backupModal').style.display = 'flex';
  byId('bkPanel').innerHTML = '';
  bkWatchPanel();
  loadBackups(true);
}
// 下半部分有内容就分屏(表格缩到上半), 清空就恢复铺满; 所有打开面板的按钮都只是往 bkPanel 里写内容
function bkWatchPanel() {
  var p = byId('bkPanel');
  var sync = function () {
    var on = !!p.innerHTML.trim();
    bkSetSplit(on);
    if (!on) { bkState.cur = ''; document.querySelectorAll('#bkBody tr.bk-cur').forEach(function (r) { r.classList.remove('bk-cur'); }); }
    else { p.scrollTop = 0; }
  };
  if (!p.dataset.watch && window.MutationObserver) { p.dataset.watch = '1'; new MutationObserver(sync).observe(p, { childList: true }); }
  sync();
}
// 分屏 / 恢复铺满: 先量出前后两个高度, 再把表格的 flex-basis 从旧高度动画到新高度(下半部分自动占剩下的)
function bkSetSplit(on) {
  var m = byId('backupModal'), t = byId('bkTableWrap');
  if (m.classList.contains('bk-split') === on) { return; }
  var h0 = t.getBoundingClientRect().height;
  m.classList.toggle('bk-split', on);
  bkApplyTableH();
  var h1 = t.getBoundingClientRect().height;
  var still = window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (!t.animate || still || m.style.display === 'none' || !h0 || Math.abs(h1 - h0) < 4) { return; }
  if (bkState.anim) { bkState.anim.cancel(); }
  bkState.anim = t.animate([{ flex: '0 0 ' + h0 + 'px' }, { flex: '0 0 ' + h1 + 'px' }], { duration: 520, easing: 'cubic-bezier(.2,.8,.2,1)' });
}
// 分屏时表格高度可以拖(记住本次会话里的高度), 双击分隔条 / 点「放大」把表格压到最小
function bkApplyTableH() {
  var m = byId('backupModal'), t = byId('bkTableWrap');
  t.style.flex = (m.classList.contains('bk-split') && bkState.tableH) ? '0 0 ' + bkState.tableH + 'px' : '';
}
function bkToggleMax() {
  bkState.tableH = (bkState.tableH && bkState.tableH <= 130) ? bkState.prevH || 0 : (bkState.prevH = bkState.tableH, 120);
  bkApplyTableH();
}
(function () {
  var drag = null;
  document.addEventListener('mousedown', function (e) {
    var sp = e.target.closest && e.target.closest('#bkSplitter');
    if (!sp || e.button !== 0) { return; }
    e.preventDefault();
    drag = { y: e.clientY, h: byId('bkTableWrap').getBoundingClientRect().height, sp: sp };
    sp.classList.add('drag'); document.body.style.cursor = 'row-resize';
  });
  document.addEventListener('mousemove', function (e) {
    if (!drag) { return; }
    var body = byId('backupModal').querySelector('.modal-body').getBoundingClientRect().height;
    bkState.tableH = Math.round(Math.max(90, Math.min(body - 160, drag.h + e.clientY - drag.y)));
    bkApplyTableH();
  });
  document.addEventListener('mouseup', function () {
    if (!drag) { return; }
    drag.sp.classList.remove('drag'); document.body.style.cursor = ''; drag = null;
  });
  document.addEventListener('dblclick', function (e) { if (e.target.closest && e.target.closest('#bkSplitter')) { bkToggleMax(); } });
})();
// 收起: 先让表格长回去、下半部分淡出, 动画结束后再清空内容(期间又打开了别的面板就不清)
function bkCollapse() {
  var p = byId('bkPanel'), keep = p.firstChild;
  bkSetSplit(false);
  setTimeout(function () { if (p.firstChild === keep) { p.innerHTML = ''; } }, 520);
}
// 点了哪一行的按钮就高亮哪一行, 分屏后知道下面显示的是哪台
document.addEventListener('click', function (e) {
  var b = e.target.closest && e.target.closest('#bkBody .abtn');
  if (!b) { return; }
  document.querySelectorAll('#bkBody tr.bk-cur').forEach(function (r) { r.classList.remove('bk-cur'); });
  var tr = b.closest('tr'); if (tr) { tr.classList.add('bk-cur'); }
  var m = (b.getAttribute('onclick') || '').match(/\("([^"]+)"\)/);
  bkState.cur = m ? m[1] : '';
});
function closeBackups() {
  byId('backupModal').style.display = 'none';
  if (bkState.poll) { clearInterval(bkState.poll); bkState.poll = null; }
}
function loadBackups(fillSettings) {
  return authedApi('/api/backups').then(function (d) {
    bkState.devices = d.devices || [];
    bkState.vc = d.vendor_commands || {};
    Object.keys(d.vendor_names || {}).forEach(function (k) { BK_VENDOR_NAMES[k] = d.vendor_names[k]; });
    if (fillSettings) {
      var s = d.settings || {};
      byId('bkAuto').checked = !!s.auto_enabled;
      byId('bkHour').value = s.auto_hour;
      byId('bkKeep').value = s.keep || 30;
      byId('bkOnlyChanged').checked = !!s.only_changed;
      byId('bkSsh').value = s.ssh_path || '';
    }
    byId('bkSshNotice').innerHTML = d.ssh_ok ? '' :
      '<div class="agg-issue">⚠️ 没有找到 ssh 客户端, SSH 方式的备份无法执行(Telnet 和手工上传不受影响): ' + esc(d.ssh_error || '') +
      '<br>Windows 10/11: 设置 → 应用 → 可选功能 → 添加「OpenSSH 客户端」; 或在下方填写 ssh.exe 完整路径。</div>';
    renderBackupTable();
    var running = bkState.devices.some(function (x) { return x.running; });
    if (running && !bkState.poll) { bkState.poll = setInterval(function () { loadBackups(false); }, 3000); }
    if (!running && bkState.poll) { clearInterval(bkState.poll); bkState.poll = null; }
  }).catch(function (e) { byId('bkBody').innerHTML = '<tr><td colspan="5">加载失败: ' + esc(String(e)) + '</td></tr>'; });
}
function bkDev(id) { return bkState.devices.filter(function (x) { return x.device_id === id; })[0]; }
function renderBackupTable() {
  var rows = bkState.devices.map(function (d) {
    var cred = d.has_cred ? '<b>' + esc((d.method || 'ssh').toUpperCase()) + '</b> ' + esc(d.username) + (d.port ? ':' + d.port : '') + '<div class="sub faint">' + esc(d.vendor || 'auto') + '</div>' : '<span class="faint">未配置</span>';
    var st = '<span class="faint">-</span>';
    if (d.running) { st = '<span class="tag tag-trunk">备份中…</span>'; }
    else if (d.status) {
      st = d.status.ok ? '<span class="st-ok">✔ ' + esc(d.status.time) + '</span>' + (d.status.error ? '<div class="sub faint">' + esc(d.status.error) + '</div>' : '')
        : '<span class="st-bad" title="' + esc(d.status.error || '') + '">✖ ' + esc(d.status.time) + '</span><div class="sub bad" style="white-space:normal;max-width:360px;">' + esc((d.status.error || '').slice(0, 160)) + '</div>';
    }
    var id = '&quot;' + d.device_id + '&quot;';
    return '<tr' + (bkState.cur && bkState.cur === d.device_id ? ' class="bk-cur"' : '') + '><td><b>' + esc(d.name || d.ip) + '</b><div class="sub dim">' + esc(d.ip) + '</div></td><td>' + cred + '</td><td>' + st + '</td>' +
      '<td>' + d.versions.length + (d.versions[0] ? '<div class="sub faint">最新 ' + esc(d.versions[0].time) + '</div>' : '') + '</td>' +
      '<td style="white-space:normal;"><button class="abtn" onclick="editBackupCred(' + id + ')">🔑 凭据</button> ' +
      '<button class="abtn" ' + (d.has_cred ? '' : 'disabled title="先配置凭据"') + ' onclick="runBackup(' + id + ')">▶ 立即备份</button> ' +
      '<button class="abtn" onclick="showVersions(' + id + ')">📚 版本</button> ' +
      '<button class="abtn" onclick="uploadBackupForm(' + id + ')">⬆ 上传</button></td></tr>';
  });
  byId('bkBody').innerHTML = rows.length ? rows.join('') : '<tr><td colspan="5" class="empty">还没有设备</td></tr>';
}
function saveBackupSettings() {
  authedApi('/api/backups/settings', { auto_enabled: byId('bkAuto').checked, auto_hour: parseInt(byId('bkHour').value, 10) || 0,
    keep: parseInt(byId('bkKeep').value, 10) || 30, only_changed: byId('bkOnlyChanged').checked, ssh_path: byId('bkSsh').value.trim() })
    .then(function () { byId('bkMsg').innerText = '✅ 已保存'; loadBackups(false); })
    .catch(function (e) { byId('bkMsg').innerText = '❌ ' + e; });
}
function runBackup(id) {
  byId('bkMsg').innerText = id ? '已开始备份…' : '已开始备份全部设备…';
  authedApi('/api/backups/run', { device_id: id }).then(function () {
    setTimeout(function () { loadBackups(false); }, 600);
    if (!bkState.poll) { bkState.poll = setInterval(function () { loadBackups(false); }, 3000); }
  }).catch(function (e) { byId('bkMsg').innerText = '❌ ' + e; });
}
function editBackupCred(id) {
  var d = bkDev(id);
  var v = function (x) { return esc(x === undefined || x === null ? '' : String(x)); };
  byId('bkPanel').innerHTML = '<div class="agg-card"><div class="chart-title">🔑 ' + v(d.name || d.ip) + ' 的登录凭据</div>' +
    '<div class="rpt-form">' +
    '<div><label>登录方式</label><select id="bcMethod"><option value="telnet">Telnet</option><option value="ssh">SSH</option></select></div>' +
    '<div><label>端口(0 = 默认 22/23)</label><input type="number" id="bcPort" value="' + v(d.port || 0) + '"></div>' +
    '<div><label>用户名</label><input type="text" id="bcUser" value="' + v(d.username) + '" autocomplete="off"></div>' +
    '<div><label>密码 ' + (d.has_cred ? '(留空 = 不修改)' : '') + '</label><input type="password" id="bcPass" autocomplete="new-password"></div>' +
    '<div><label>enable 密码(Cisco/锐捷, 可空) ' + (d.has_enable ? '(已保存)' : '') + '</label><input type="password" id="bcEnable" autocomplete="new-password"></div>' +
    '<div><label>厂商命令集</label><select id="bcVendor" onchange="bkVendorHint(&quot;' + d.device_id + '&quot;)">' + bkVendorOptions() + '</select></div>' +
    '<div style="grid-column:1/-1;"><label>自定义命令(可选, 每行一条; 最后一条命令的输出作为配置保存。留空 = 用下面这套厂商默认命令, 默认命令可以在「🛠️ 设置 → 配置备份命令」里统一修改)</label>' +
    '<div id="bcDefault" class="sub" style="margin:2px 0 6px;"></div>' +
    '<textarea id="bcCmds" rows="3">' + v(d.commands) + '</textarea>' +
    '<div style="margin-top:4px;"><a href="javascript:void(0)" onclick="byId(&quot;bcCmds&quot;).value=bkVendorCmds(&quot;' + d.device_id + '&quot;)">把默认命令填进来再改</a></div></div>' +
    '</div><div class="hint">密码用本机 data/secret.key 加密保存。建议在交换机上建一个只读(能看配置即可)的专用账号。SSH 首次连接会自动记录设备指纹到 data/backups/known_hosts。</div>' +
    '<div style="display:flex;gap:8px;margin-top:10px;"><button class="hbtn" onclick="saveBackupCred(&quot;' + d.device_id + '&quot;)">保存</button>' +
    (d.has_cred ? '<button class="filter-btn" style="color:var(--bad);" onclick="deleteBackupCred(&quot;' + d.device_id + '&quot;)">删除凭据</button>' : '') +
    '<button class="filter-btn" onclick="byId(&quot;bkPanel&quot;).innerHTML=&quot;&quot;">取消</button><span id="bcMsg" style="align-self:center;font-size:12.5px;"></span></div></div>';
  byId('bcMethod').value = d.method || 'telnet';
  byId('bcVendor').value = d.vendor || 'auto';
  bkVendorHint(d.device_id);
}
var BK_VENDOR_NAMES = { huawei: '华为', h3c: 'H3C', ruijie: '锐捷', cisco: 'Cisco', generic: '其他(Cisco 风格)' };
function bkVendorOptions() {
  var vc = bkState.vc || {};
  var last = function (v) { var l = (vc[v] || '').split('\n'); return l[l.length - 1]; };
  return '<option value="auto">自动(按识别到的品牌)</option>' + Object.keys(BK_VENDOR_NAMES).filter(function (v) { return v !== 'generic'; }).map(function (v) {
    return '<option value="' + v + '">' + BK_VENDOR_NAMES[v] + (last(v) ? ' (' + esc(last(v)) + ')' : '') + '</option>';
  }).join('');
}
// 选"自动"时按识别到的品牌决定, 识别不出来的按 Cisco 风格
function bkEffVendor(id) {
  var sel = byId('bcVendor'), v = sel ? sel.value : 'auto';
  if (v !== 'auto') { return v; }
  var d = id ? bkDev(id) : null, b = d && d.brand;
  return BK_VENDOR_NAMES[b] ? b : 'generic';
}
function bkVendorCmds(id) { return (bkState.vc || {})[bkEffVendor(id)] || ''; }
function bkVendorHint(id) {
  var box = byId('bcDefault');
  if (!box) { return; }
  var v = bkEffVendor(id);
  box.innerHTML = '将使用 <b>' + esc(BK_VENDOR_NAMES[v] || v) + '</b> 的默认命令: <code style="white-space:pre;">' + esc(bkVendorCmds(id)).replace(/\n/g, ' ⏎ ') + '</code>';
  byId('bcCmds').placeholder = bkVendorCmds(id);
}
// 批量设置凭据: 远程设备多数共用同一套 Telnet 账号, 勾选设备一次配好。
function bulkBackupCred() {
  var list = bkState.devices.map(function (d) {
    return '<label style="display:inline-flex;gap:5px;align-items:center;margin:3px 10px 3px 0;font-size:13px;"><input type="checkbox" class="bkBulkDev" value="' + esc(d.device_id) + '"' + (d.has_cred ? '' : ' checked') + '> ' +
      esc(d.name || d.ip) + ' <span class="faint">' + esc(d.ip) + (d.has_cred ? ' · 已有凭据' : '') + '</span></label>';
  }).join('');
  byId('bkPanel').innerHTML = '<div class="agg-card"><div class="chart-title">🔑 批量设置登录凭据</div>' +
    '<div class="rpt-form">' +
    '<div><label>登录方式</label><select id="bcMethod"><option value="telnet">Telnet</option><option value="ssh">SSH</option></select></div>' +
    '<div><label>端口(0 = 默认 23/22)</label><input type="number" id="bcPort" value="0"></div>' +
    '<div><label>用户名</label><input type="text" id="bcUser" autocomplete="off"></div>' +
    '<div><label>密码(留空 = 已有凭据的设备保持原密码)</label><input type="password" id="bcPass" autocomplete="new-password"></div>' +
    '<div><label>enable 密码(可空)</label><input type="password" id="bcEnable" autocomplete="new-password"></div>' +
    '<div><label>厂商命令集</label><select id="bcVendor">' + bkVendorOptions() + '</select></div>' +
    '</div><div class="sub" style="margin:4px 0;">选「自动」时每台设备按各自识别到的品牌用对应的默认命令(华为/H3C: display current-configuration, 锐捷/Cisco: show running-config), 默认命令可在「🛠️ 设置」里改。</div>' +
    '<div style="margin:6px 0;"><span class="sub">应用到这些设备: </span>' +
    '<a href="javascript:void(0)" onclick="document.querySelectorAll(&quot;.bkBulkDev&quot;).forEach(function(c){c.checked=true})">全选</a> · ' +
    '<a href="javascript:void(0)" onclick="document.querySelectorAll(&quot;.bkBulkDev&quot;).forEach(function(c){c.checked=false})">全不选</a></div>' +
    '<div style="max-height:22vh;overflow:auto;">' + list + '</div>' +
    '<div style="display:flex;gap:8px;margin-top:10px;"><button class="hbtn" onclick="saveBulkBackupCred()">应用</button>' +
    '<button class="filter-btn" onclick="byId(&quot;bkPanel&quot;).innerHTML=&quot;&quot;">取消</button><span id="bcMsg" style="align-self:center;font-size:12.5px;"></span></div></div>';
}
function saveBulkBackupCred() {
  var ids = [];
  document.querySelectorAll('.bkBulkDev').forEach(function (c) { if (c.checked) { ids.push(c.value); } });
  if (!ids.length) { byId('bcMsg').innerText = '请至少勾选一台设备'; return; }
  authedApi('/api/backups/cred', { device_ids: ids, method: byId('bcMethod').value, port: parseInt(byId('bcPort').value, 10) || 0,
    username: byId('bcUser').value, password: byId('bcPass').value, enable: byId('bcEnable').value, vendor: byId('bcVendor').value, commands: '' })
    .then(function (d) { byId('bkPanel').innerHTML = ''; byId('bkMsg').innerText = '✅ 已给 ' + (d.count || ids.length) + ' 台设备设置凭据'; loadBackups(false); })
    .catch(function (e) { byId('bcMsg').innerText = '❌ ' + e; });
}
function saveBackupCred(id) {
  authedApi('/api/backups/cred', { device_id: id, method: byId('bcMethod').value, port: parseInt(byId('bcPort').value, 10) || 0,
    username: byId('bcUser').value, password: byId('bcPass').value, enable: byId('bcEnable').value, vendor: byId('bcVendor').value, commands: byId('bcCmds').value })
    .then(function () { byId('bkPanel').innerHTML = ''; loadBackups(false); })
    .catch(function (e) { byId('bcMsg').innerText = '❌ ' + e; });
}
function deleteBackupCred(id) {
  if (!confirm('删除这台设备的登录凭据? 已有的备份版本不受影响。')) { return; }
  authedApi('/api/backups/cred', { device_id: id, delete: true }).then(function () { byId('bkPanel').innerHTML = ''; loadBackups(false); })
    .catch(function (e) { alert(e); });
}
function showVersions(id) {
  var d = bkDev(id);
  var q = '&quot;';
  var rows = d.versions.map(function (m, i) {
    var prev = d.versions[i + 1];
    var chg = m.changed ? '<span class="tag tag-hybrid">有变化 +' + m.added + ' / -' + m.removed + '</span>' : '<span class="tag">无变化</span>';
    if (!prev) { chg = '<span class="tag">首份</span>'; }
    return '<tr><td>' + esc(m.time) + '</td><td>' + esc(m.method) + '<div class="sub faint">' + esc(m.by || '') + '</div></td><td>' + m.lines + ' 行</td><td>' + chg + (m.note ? '<div class="sub dim">' + esc(m.note) + '</div>' : '') + '</td>' +
      '<td style="white-space:normal;"><button class="abtn" onclick="viewBackup(' + q + id + q + ',' + q + m.id + q + ',' + q + q + ')">查看</button> ' +
      (prev ? '<button class="abtn" onclick="viewBackup(' + q + id + q + ',' + q + m.id + q + ',' + q + prev.id + q + ')">对比上一版</button> ' : '') +
      '<a class="abtn" style="text-decoration:none;" href="/api/backups/get?download=1&device=' + encodeURIComponent(id) + '&id=' + encodeURIComponent(m.id) + '">下载</a> ' +
      '<button class="abtn" style="color:var(--bad);" onclick="deleteBackup(' + q + id + q + ',' + q + m.id + q + ')">删除</button></td></tr>';
  });
  var opts = d.versions.map(function (m) { return '<option value="' + esc(m.id) + '">' + esc(m.time) + '</option>'; }).join('');
  byId('bkPanel').innerHTML = '<div class="agg-card"><div class="chart-title">📚 ' + esc(d.name || d.ip) + ' 的配置版本 (' + d.versions.length + ')</div>' +
    (d.versions.length > 1 ? '<div class="rpt-bar">任意两版对比: <select id="bvA">' + opts + '</select> → <select id="bvB">' + opts + '</select>' +
      '<button class="filter-btn" onclick="viewBackup(' + q + id + q + ',byId(' + q + 'bvB' + q + ').value,byId(' + q + 'bvA' + q + ').value)">对比</button></div>' : '') +
    '<div style="overflow:auto;max-height:26vh;"><table><thead><tr><th style="cursor:default;">时间</th><th style="cursor:default;">方式</th><th style="cursor:default;">大小</th><th style="cursor:default;">变化</th><th style="cursor:default;">操作</th></tr></thead><tbody>' +
    (rows.join('') || '<tr><td colspan="5" class="empty">还没有备份</td></tr>') + '</tbody></table></div><div id="bkView" style="margin-top:10px;"></div></div>';
  if (d.versions.length > 1) { byId('bvA').selectedIndex = 1; }
}
function viewBackup(dev, id, against) {
  var url = '/api/backups/get?device=' + encodeURIComponent(dev) + '&id=' + encodeURIComponent(id) + (against ? '&against=' + encodeURIComponent(against) : '');
  authedApi(url).then(function (d) {
    var box = byId('bkView');
    if (against) {
      var ops = d.diff || [];
      box.innerHTML = ops.length ? '<div class="cfg-pre">' + ops.map(function (o) {
        var cls = o.op === '+' ? 'add' : o.op === '-' ? 'del' : o.op === '@' ? 'gap' : '';
        return '<span class="' + cls + '">' + (o.op === '@' ? '' : esc(o.op) + ' ') + esc(o.text) + '</span>';
      }).join('') + '</div>' : '<div class="sub">两个版本没有差异(已忽略时间戳等每次都会变的行)</div>';
    } else {
      box.innerHTML = '<div class="cfg-pre">' + esc(d.content) + '</div>';
    }
  }).catch(function (e) { alert(e); });
}
function deleteBackup(dev, id) {
  if (!confirm('删除这个备份版本?')) { return; }
  authedApi('/api/backups/delete', { device_id: dev, id: id }).then(function () { loadBackups(false).then(function () { showVersions(dev); }); })
    .catch(function (e) { alert(e); });
}
function uploadBackupForm(id) {
  var d = bkDev(id);
  byId('bkPanel').innerHTML = '<div class="agg-card"><div class="chart-title">⬆ 手工上传 ' + esc(d.name || d.ip) + ' 的配置</div>' +
    '<div class="rpt-form"><div><label>从文件读取</label><input type="file" id="buFile" accept=".txt,.cfg,.conf,.log,text/plain" onchange="readBackupFile(this.files)"></div>' +
    '<div><label>备注</label><input type="text" id="buNote" placeholder="例如: 割接前"></div>' +
    '<div style="grid-column:1/-1;"><label>或直接粘贴配置文本</label><textarea id="buText" rows="10" style="font-family:Consolas,monospace;"></textarea></div></div>' +
    '<button class="hbtn" onclick="submitBackupUpload(&quot;' + id + '&quot;)">上传归档</button> <span id="buMsg" style="font-size:12.5px;"></span></div>';
}
function readBackupFile(files) {
  if (!files || !files[0]) { return; }
  var r = new FileReader();
  r.onload = function () { byId('buText').value = r.result; };
  r.readAsText(files[0]);
}
function submitBackupUpload(id) {
  authedApi('/api/backups/upload', { device_id: id, content: byId('buText').value, note: byId('buNote').value })
    .then(function () { byId('buMsg').innerText = '✅ 已归档'; loadBackups(false); })
    .catch(function (e) { byId('buMsg').innerText = '❌ ' + e; });
}

modalRegister('reportModal', closeReports);
modalRegister('backupModal', closeBackups);
