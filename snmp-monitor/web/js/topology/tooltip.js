/*
 * 拓扑悬停提示(2D/3D/大屏共用)。
 */
/* ---------- 网络拓扑: 渲染/编排/编辑见 topo2d.go, 这里只留 2D/3D 共用的提示框 ---------- */
/* ---------- 悬停提示 (设备 / 链路 都用这一个浮动框) ---------- */
function topoTipEl() { return byId('topoTip'); }
function showTopoTip(html, evt) {
  var tip = topoTipEl(), wrap = byId('topoWrap');
  if (!tip || !wrap) { return; }
  tip.innerHTML = html;
  tip.style.display = 'block';
  positionTopoTip(evt);
}
function positionTopoTip(evt) {
  var tip = topoTipEl(), wrap = byId('topoWrap');
  if (!tip || !wrap) { return; }
  var rect = wrap.getBoundingClientRect();
  var x = evt.clientX - rect.left + 16, y = evt.clientY - rect.top + 16;
  if (x + tip.offsetWidth > rect.width) { x = evt.clientX - rect.left - tip.offsetWidth - 16; }
  tip.style.left = Math.max(0, x) + 'px';
  tip.style.top = Math.max(0, y) + 'px';
}
function hideTopoTip() {
  var tip = topoTipEl();
  if (tip) { tip.style.display = 'none'; }
}

function fmtMbps(v) {
  if (v === undefined || v === null) { return '-'; }
  return (Math.round(v * 100) / 100) + ' Mbps';
}
function fmtSpeed(v) {
  if (!v) { return '-'; }
  return v >= 1000 ? (v / 1000) + ' Gbps 口' : v + ' Mbps 口';
}

function deviceTipHTML(d) {
  var alertHtml = d.open_alerts > 0
    ? '<span style="color:#f87171;">⚠️ ' + d.open_alerts + ' 条未处理告警</span>'
    : '<span style="color:#4ade80;">✅ 无未处理告警</span>';
  return '<b style="font-size:13px;">' + esc(d.name) + '</b><br>' +
    '<span style="color:#94a3b8;">' + esc(d.ip) + ' · ' + esc(d.brand || '-') + '</span><br>' +
    (d.alive ? '<span style="color:#4ade80;">● 在线</span>' : '<span style="color:#f87171;">● 离线</span>') +
    ' · 运行 ' + esc(d.uptime_str || '-') + '<br>' +
    'CPU ' + d.cpu_usage + '% · 内存 ' + (d.mem_usage > 0 ? d.mem_usage + '%' : '未采集') + '<br>' +
    '接入率 ' + esc(d.port_usage_ratio || '-') + '<br>' + alertHtml +
    (d.note ? '<div style="margin-top:4px;color:#fbbf24;">📝 ' + esc(d.note) + '</div>' : '<br>') +
    '<span style="color:#94a3b8;">' + (typeof T3 !== 'undefined' && T3.on ? '单击打开设备详情' : '单击看信息 · 双击打开详情 · 可直接拖动') + '</span>';
}
function externalTipHTML(e) {
  return '<b>' + esc(e.name) + '</b><br><span style="color:#94a3b8;">未被监控的 LLDP 邻居</span><br>经本机端口: ' + esc(e.port) + (e.ip ? '<br>管理 IP: ' + esc(e.ip) + ' <span style="color:#94a3b8;">(点击可添加到监控)</span>' : '');
}
function linkTipHTML(l, devByIP) {
  var an = devByIP[l.a] ? devByIP[l.a].name : l.a, bn = devByIP[l.b] ? devByIP[l.b].name : l.b;
  var aUtil = l.a_speed_mbps ? Math.max(l.a_in_mbps, l.a_out_mbps) / l.a_speed_mbps * 100 : null;
  var bUtil = l.b_speed_mbps ? Math.max(l.b_in_mbps, l.b_out_mbps) / l.b_speed_mbps * 100 : null;
  var aggBadge = l.is_agg ? ' <span style="color:#f59e0b;">⛓ 聚合链路 · ' + (l.a_members || []).length + ' 条成员</span>' : '';
  var html = aggBadge ? ('<div style="margin-bottom:4px;">' + aggBadge + '</div>') : '';
  html += '<b>' + esc(an) + '</b> <span style="color:#94a3b8;">(' + esc(l.a_port) + ', ' + fmtSpeed(l.a_speed_mbps) + (l.is_agg ? ' 合计' : '') + ')</span><br>' +
    '　↓收 ' + fmtMbps(l.a_in_mbps) + ' ／ ↑发 ' + fmtMbps(l.a_out_mbps) +
    (aUtil !== null ? ' <span style="color:#94a3b8;">(' + aUtil.toFixed(1) + '%)</span>' : '') + '<br>' +
    '<span style="color:#60a5fa;">↕ 链路</span><br>' +
    '<b>' + esc(bn) + '</b> <span style="color:#94a3b8;">(' + esc(l.b_port) + ', ' + fmtSpeed(l.b_speed_mbps) + (l.is_agg ? ' 合计' : '') + ')</span><br>' +
    '　↓收 ' + fmtMbps(l.b_in_mbps) + ' ／ ↑发 ' + fmtMbps(l.b_out_mbps) +
    (bUtil !== null ? ' <span style="color:#94a3b8;">(' + bUtil.toFixed(1) + '%)</span>' : '');
  if (l.is_agg) {
    html += '<div style="margin-top:6px;color:#94a3b8;font-size:12px;">成员口: ';
    for (var i = 0; i < l.a_members.length; i++) {
      html += (i > 0 ? ', ' : '') + esc(l.a_members[i]) + '↔' + esc(l.b_members[i]);
    }
    html += '</div>';
  }
  return html;
}

function cssEscape(s) {
  return String(s).replace(/[^a-zA-Z0-9_\-]/g, function (c) { return '\\' + c; });
}
