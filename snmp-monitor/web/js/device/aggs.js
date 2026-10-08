/*
 * 设备详情: 聚合口。
 */
/* ---------- 聚合口 ---------- */
detailPart({ fill: function () { renderAggGroups(); } });
function renderAggGroups() {
  var d = currentDetail;
  var box = byId('aggList');
  if (!box) { return; }
  var groups = (d && d.agg_groups) || [];
  if (groups.length === 0) {
    box.innerHTML = '<div style="text-align:center;color:var(--fg-faint);padding:24px;">这台设备没有识别到聚合口(可能确实没配置聚合, 也可能是静态聚合用了未覆盖的私有 MIB)</div>';
    return;
  }
  var statusColor = function (s) { return s === 'UP' ? 'var(--good)' : (s === 'ADMIN-DOWN' ? 'var(--warn)' : 'var(--bad)'); };
  var fmtSp = function (m) { return m >= 1000 ? (m / 1000) + 'G' : (m > 0 ? m + 'M' : '-'); };
  var html = groups.map(function (g) {
    var infos = g.member_info && g.member_info.length ? g.member_info : (g.members || []).map(function (m) { return { name: m }; });
    var peerDev = g.peer_device ? esc(g.peer_device) : '<span class="faint">LLDP 未发现</span>';
    if (g.peer_ip) {
      peerDev = '<a href="javascript:void(0)" onclick="openModal(&quot;' + esc(g.peer_ip) + '&quot;)" title="打开对端设备详情">' + esc(g.peer_device) + ' ↗</a>';
    }
    var head = '<div class="agg-head">' +
      '<div><span class="agg-name">🧩 ' + esc(g.name) + '</span> ' +
      '<span style="color:' + statusColor(g.status) + ';font-weight:600;">● ' + esc(g.status || '-') + '</span> ' +
      '<span class="tag ' + (g.mode === 'LACP' ? 'tag-trunk' : '') + '" title="检测到 LACP 协商状态即为 LACP, 否则按静态聚合处理">' + esc(g.mode || '-') + '</span>' +
      (g.alias ? ' <span class="sub dim" style="display:inline;">' + esc(g.alias) + '</span>' : '') + '</div>' +
      '<div class="agg-kpis">' +
        '<div><span>成员</span><b>' + (g.up_members || 0) + ' / ' + infos.length + ' UP</b></div>' +
        '<div><span>聚合带宽</span><b>' + fmtSp(g.speed_mbps || 0) + '</b></div>' +
        '<div><span>入 / 出</span><b>' + (g.in_mbps || 0).toFixed(1) + ' / ' + (g.out_mbps || 0).toFixed(1) + ' Mbps</b></div>' +
        '<div><span>利用率</span><b>' + (g.in_usage || 0).toFixed(1) + '% / ' + (g.out_usage || 0).toFixed(1) + '%</b></div>' +
        '<div><span>VLAN / IP</span><b title="' + esc(g.ip_addr || g.vlan_text || '') + '">' + esc(g.ip_addr || g.vlan_text || '-') + '</b></div>' +
        '<div><span>对端设备</span><b>' + peerDev + '</b></div>' +
        '<div><span>对端聚合口</span><b>' + (g.peer_agg ? esc(g.peer_agg) : '<span class="faint">' + (g.peer_ip ? '对端未识别到' : '对端未监控') + '</span>') + '</b></div>' +
      '</div></div>';
    var issues = (g.issues || []).map(function (t) { return '<div class="agg-issue">⚠️ ' + esc(t) + '</div>'; }).join('');
    var rows = infos.map(function (m) {
      var lacp = m.lacp_state ? '<span style="color:' + (m.lacp_ok ? 'var(--good)' : 'var(--bad)') + ';">' + esc(m.lacp_state) + '</span>' : '<span class="faint">-</span>';
      var peer = m.peer_dev ? esc(m.peer_dev) + '<div class="sub dim">' + esc(m.peer_port || '') + (m.peer_agg ? ' · 🧩 ' + esc(m.peer_agg) : '') + '</div>' : '<span class="faint">-</span>';
      return '<tr><td><b>' + esc(m.name) + '</b></td>' +
        '<td><span style="color:' + statusColor(m.status) + ';font-weight:600;">● ' + esc(m.status || '-') + '</span></td>' +
        '<td>' + fmtSp(m.speed_mbps || 0) + (m.duplex ? '<div class="sub ' + (m.duplex === '半双工' ? 'bad' : 'faint') + '">' + esc(m.duplex) + '</div>' : '') + '</td>' +
        '<td>' + (m.in_mbps || 0).toFixed(1) + ' / ' + (m.out_mbps || 0).toFixed(1) + '<div class="sub faint">' + (m.in_usage || 0).toFixed(1) + '% / ' + (m.out_usage || 0).toFixed(1) + '%</div></td>' +
        '<td>' + (m.delta_errors > 0 ? '<b style="color:var(--bad);">+' + m.delta_errors + '</b>' : '<span class="faint">0</span>') + '</td>' +
        '<td>' + (m.rx_dbm !== undefined && m.rx_dbm !== null ? m.rx_dbm.toFixed(2) + ' dBm' : '<span class="faint">-</span>') + '</td>' +
        '<td style="white-space:normal;">' + lacp + '</td>' +
        '<td style="white-space:normal;">' + peer + '</td></tr>';
    }).join('');
    return '<div class="agg-card">' + head + issues +
      '<div style="overflow:auto;"><table class="agg-table"><thead><tr>' +
      '<th>成员口</th><th>状态</th><th>速率</th><th>入/出 Mbps</th><th>增量错包</th><th>收光</th><th>LACP 状态</th><th>对端 (LLDP)</th>' +
      '</tr></thead><tbody>' + rows + '</tbody></table></div></div>';
  }).join('');
  box.innerHTML = html;
}
