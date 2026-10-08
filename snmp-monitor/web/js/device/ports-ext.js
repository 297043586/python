/*
 * 设备详情: 端口扩展列 / 硬件页扩展信息。
 */
/* ---------- 端口扩展列 (丢包/广播组播/MAC 数/双工/状态持续) ---------- */
detailPart({ fill: function () { renderHwExt(); } });
// 显示开关只影响页面, 记在浏览器里; 采不采这些数据在「设备管理」里按设备设置。
function portExtVisible() {
  try { return localStorage.getItem('showPortExt') !== '0'; } catch (e) { return true; }
}
function togglePortExt(on) {
  try { localStorage.setItem('showPortExt', on ? '1' : '0'); } catch (e) { /* 存不了就只对本次生效 */ }
  renderPorts();
  renderHwExt();
}

// renderHwExt: 硬件页的"资产信息 + 端口健康摘要"。摘要是从端口表里现算的,
// 把"要一行行翻端口表才看得出来"的问题口直接数出来: 有错包/丢包的、半双工的、
// 刚抖动过的、利用率高的, 以及长期没用的 DOWN 口(做扩容/回收规划时有用)。
function renderHwExt() {
  var d = currentDetail;
  var box = byId('hwExtBox');
  if (!box) { return; }
  var show = portExtVisible();
  byId('hwExtToggle').checked = show;
  if (!show || !d) { box.innerHTML = ''; return; }
  if (d.collect && d.collect.skip_extended) {
    box.innerHTML = '<div style="color:var(--fg-faint);font-size:13px;">这台设备在「设备管理」里关闭了扩展信息采集。</div>';
    return;
  }
  var inv = d.inventory || {};
  var item = function (label, val, color) {
    return '<div class="hw-item"><h4>' + label + '</h4><div class="val" style="font-size:14px;' + (color ? 'color:' + color + ';' : '') + '">' + (val === '' || val === undefined || val === null ? '<span style="color:var(--fg-faint);">未上报</span>' : esc(String(val))) + '</div></div>';
  };
  var html = '<div class="kv-grid">' +
    item('型号', inv.model) + item('序列号', inv.serial) + item('软件版本', inv.soft_ver) +
    item('硬件版本', inv.hard_ver) + item('位置', inv.location) + item('联系人', inv.contact) +
    '</div>';

  var ports = d.ports || [];
  var errPorts = [], discPorts = [], halfPorts = [], flapPorts = [], hotPorts = [], idlePorts = [], stormPorts = [];
  ports.forEach(function (p) {
    var x = p.ext || {};
    if (p.delta_errors > 0 || x.delta_out_errors > 0) { errPorts.push(p.name); }
    if (x.delta_in_discards > 0 || x.delta_out_discards > 0) { discPorts.push(p.name); }
    if (x.duplex === '半双工' && p.status === 'UP') { halfPorts.push(p.name); }
    if (x.last_change_sec >= 0 && x.last_change_sec < 600) { flapPorts.push(p.name); }
    if (p.in_usage_percent >= 70 || p.out_usage_percent >= 70) { hotPorts.push(p.name); }
    if (p.status !== 'UP' && x.last_change_sec >= 30 * 86400) { idlePorts.push(p.name); }
    if (x.in_bcast_pps >= 1000) { stormPorts.push(p.name); }
  });
  var stat = function (label, list, badColor, hint) {
    var n = list.length;
    var tip = n ? list.slice(0, 30).join(', ') + (n > 30 ? ' ...' : '') : hint;
    return '<div class="hw-item" title="' + esc(tip) + '"><h4>' + label + '</h4><div class="val" style="color:' + (n ? badColor : 'var(--fg-faint)') + ';">' + n + ' 个</div></div>';
  };
  html += '<div class="kv-grid">' +
    stat('本轮有错包的口', errPorts, 'var(--bad)', '无') +
    stat('本轮有丢包的口', discPorts, 'var(--bad)', '无') +
    stat('半双工运行的口', halfPorts, 'var(--bad)', '无') +
    stat('广播风暴嫌疑口', stormPorts, 'var(--bad)', '入方向广播 ≥ 1000 pps') +
    stat('10 分钟内状态变化', flapPorts, 'var(--warn)', '无') +
    stat('利用率 ≥ 70% 的口', hotPorts, 'var(--warn)', '无') +
    stat('闲置超 30 天的口', idlePorts, 'var(--info)', 'DOWN 且 30 天没变化过, 可回收') +
    stat('聚合组', (d.agg_groups || []).map(function (g) { return g.name; }), 'var(--accent)', '无') +
    '</div><div style="font-size:11.5px;color:var(--fg-faint);">鼠标悬停在数字上可以看到具体是哪些端口。</div>';
  box.innerHTML = html;
}
function extNum(p, f) {
  var x = p.ext;
  if (!x) { return -1; }
  switch (f) {
    case 'disc': return (x.delta_in_discards || 0) + (x.delta_out_discards || 0);
    case 'bcast': return (x.in_bcast_pps || 0) + (x.in_mcast_pps || 0);
    case 'change': return x.last_change_sec < 0 ? Number.MAX_SAFE_INTEGER : x.last_change_sec;
  }
  return 0;
}
function fmtDuration(sec) {
  if (sec < 0) { return '-'; }
  if (sec < 60) { return sec + ' 秒'; }
  if (sec < 3600) { return Math.floor(sec / 60) + ' 分'; }
  if (sec < 86400) { return Math.floor(sec / 3600) + ' 小时 ' + Math.floor(sec % 3600 / 60) + ' 分'; }
  return Math.floor(sec / 86400) + ' 天 ' + Math.floor(sec % 86400 / 3600) + ' 小时';
}
function portExtCells(p, extOff) {
  var faint = function (t) { return '<td class="col-ext"><span style="color:var(--fg-faint);">' + t + '</span></td>'; };
  var macCell = '<td class="col-ext">' + (p.mac_count > 0 ? p.mac_count : '<span style="color:var(--fg-faint);">0</span>') + '</td>';
  if (extOff || !p.ext) {
    var t = extOff ? '未采集' : '-';
    return faint(t) + faint(t) + macCell + faint(t);
  }
  var x = p.ext;
  var di = x.delta_in_discards || 0, dout = x.delta_out_discards || 0, oe = x.delta_out_errors || 0;
  var disc = (di + dout > 0)
    ? '<b style="color:var(--bad);">' + di + ' / ' + dout + '</b>'
    : '<span style="color:var(--fg-faint);">0 / 0</span>';
  if (oe > 0) { disc += '<div style="font-size:12px;color:var(--bad);">出错包 +' + oe + '</div>'; }
  // 普通接入口每秒几十个广播很正常; 上千个基本就是环路/广播风暴了
  var bc = x.in_bcast_pps || 0, mc = x.in_mcast_pps || 0;
  var bcColor = bc >= 1000 ? 'var(--bad)' : (bc >= 200 ? 'var(--warn)' : 'var(--fg)');
  var bcast = '<span style="color:' + bcColor + ';">' + bc + '</span> / ' + mc;
  var change = fmtDuration(x.last_change_sec);
  if (x.last_change_sec >= 0 && x.last_change_sec < 600) {
    change = '<b style="color:var(--warn);" title="10 分钟内刚变化过, 留意是否抖动">' + change + '</b>';
  }
  return '<td class="col-ext">' + disc + '</td><td class="col-ext">' + bcast + '</td>' + macCell +
    '<td class="col-ext">' + change + '</td>';
}
