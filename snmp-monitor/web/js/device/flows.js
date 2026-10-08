/*
 * 设备详情: NetFlow / sFlow 流量。
 */
/* ---------- NetFlow / sFlow 流量 ---------- */
detailPart({ tab: 8, show: function () { loadFlows(); } });
var FLOW_PIE_COLORS = ['#2563eb', '#10b981', '#f59e0b', '#8b5cf6', '#ec4899', '#06b6d4', '#f43f5e', '#84cc16', '#94a3b8'];

function dirLabelOf(isPrivate) { return isPrivate ? '<span class="pill pill-access">私网</span>' : '<span class="pill pill-uplink">公网</span>'; }

var flowLastRecords = [];
var flowAutoTimer = null;
var FLOW_EMPTY = function (cols, text) {
  return '<tr><td colspan="' + cols + '" style="text-align:center;color:var(--fg-faint);padding:16px;">' + (text || '这个时间窗口里没有数据') + '</td></tr>';
};

function loadFlows() {
  if (!currentIP) { return; }
  var hint = byId('flowHint');
  var win = byId('flowWindow').value;
  pub('/api/flows?ip=' + encodeURIComponent(currentIP) + '&window=' + encodeURIComponent(win)).then(function (d) {
    if (!d.enabled) {
      hint.innerHTML = '流量采集未开启。在 config.json 的 collectors.netflow_port / sflow_port 里配一个端口, 并在交换机/路由器上把导出目标指向本机对应端口, 重启程序后即可采集。';
      ['flowTopSrcBody', 'flowTopDstBody', 'flowDomainBody', 'flowMatrixBody', 'flowVlanBody', 'flowSynBody', 'flowIfBody', 'flowTopBody', 'flowRecBody'].forEach(function (id) {
        byId(id).innerHTML = FLOW_EMPTY(11, '未开启');
      });
      ['flowSummary', 'flowTimeline', 'flowDirBox', 'flowPortPie', 'flowTcpBox', 'flowCongestionBox', 'flowCatPie', 'flowProtoBox', 'flowL2Box'].forEach(function (id) { byId(id).innerHTML = ''; });
      byId('flowAppBody').innerHTML = FLOW_EMPTY(5, '未开启');
      flowLastRecords = [];
      return;
    }
    hint.innerText = 'NetFlow v5 端口 ' + (d.netflow_port || '-') + ' / sFlow v5 端口 ' + (d.sflow_port || '-') + ' 已开启, 只展示这台设备上报的数据。';
    if (!(d.summary && d.summary.samples) && authenticated) {
      // 采集已开、但这台设备一条都没发过来: 多半是交换机上还没配 sFlow, 给个一键下发的入口
      hint.innerHTML = esc(hint.innerText) + ' <b>这台设备还没有发来任何流量数据</b> — ' +
        '<button class="btn sm" onclick="openPush({ kind: &quot;sflow&quot;, ips: [currentIP] })">🛠️ 一键下发 sFlow 配置</button>';
    }
    byId('flowL7').checked = !!d.l7_enabled;

    renderFlowSummary(d.summary || {});
    renderFlowTimeline(d.timeline || []);
    renderFlowDirBox(d.direction_stats || []);
    renderFlowPortPie(d.port_buckets || []);
    renderFlowApps(d);

    var ipRow = function (t) {
      return '<tr><td><code>' + esc(t.ip) + '</code></td><td>' + dirLabelOf(t.is_private) + '</td><td>' + fmtBytes(t.bytes) + '</td><td>' + t.flows + '</td></tr>';
    };
    var topSrc = d.top_src_ips || [], topDst = d.top_dst_ips || [];
    byId('flowTopSrcBody').innerHTML = topSrc.length ? topSrc.map(ipRow).join('') : FLOW_EMPTY(4);
    byId('flowTopDstBody').innerHTML = topDst.length ? topDst.map(ipRow).join('') : FLOW_EMPTY(4);

    renderFlowDomains(d);

    var matrix = d.subnet_matrix || [];
    byId('flowMatrixBody').innerHTML = matrix.length ? matrix.map(function (c) {
      var kind = c.kind === '东西向' ? '<span class="pill pill-access">东西向</span>' : '<span class="pill pill-uplink">南北向</span>';
      return '<tr><td><code>' + esc(c.src_net) + '</code></td><td><code>' + esc(c.dst_net) + '</code></td><td>' + kind + '</td><td>' + fmtBytes(c.bytes) + '</td><td>' + c.flows + '</td></tr>';
    }).join('') : FLOW_EMPTY(5);

    var vlans = d.top_vlans || [];
    byId('flowVlanBody').innerHTML = vlans.length ? vlans.map(function (v) {
      return '<tr><td>VLAN ' + v.vlan + '</td><td>' + fmtBytes(v.bytes) + '</td><td>' + v.flows + '</td></tr>';
    }).join('') : FLOW_EMPTY(3, '没有带 VLAN 信息的记录 (NetFlow v5 不带 VLAN; sFlow 需要报文带 802.1Q 标签或设备上报 extended_switch)');

    renderFlowTcp(d.tcp_stats || {}, d.syn_suspects || []);
    renderFlowCongestion(d.congestion || []);
    renderFlowIfCounters(d.if_counters || [], d.sflow_host);

    var top = d.top_talkers || [];
    byId('flowTopBody').innerHTML = top.length ? top.map(function (t) {
      return '<tr><td>' + esc(t.src_ip) + '</td><td>' + esc(t.dst_ip) + '</td><td>' + (t.dst_port || '-') + '</td><td>' + esc(t.protocol) + '</td>' +
        '<td>' + fmtBytes(t.bytes) + '</td><td>' + t.packets + '</td><td>' + t.flows + '</td></tr>';
    }).join('') : FLOW_EMPTY(7);

    byId('flowSuspiciousHint').innerText = d.suspicious_count > 0
      ? ('· 🔴 ' + d.suspicious_count + ' 条疑似扫描/探测') : '';
    flowLastRecords = d.records || [];
    renderFlowRecords();
  }).catch(function (e) {
    hint.innerText = '加载失败: ' + String(e);
  });
}

// 自动刷新只在「流量」标签页打开着的时候真的去拉数据, 切走或关掉弹窗后
// 定时器仍在但什么也不做, 重新切回来立刻生效, 不用重新勾选。
function toggleFlowAuto(on) {
  if (flowAutoTimer) { clearInterval(flowAutoTimer); flowAutoTimer = null; }
  if (on) {
    flowAutoTimer = setInterval(function () {
      if (currentIP && currentTab === 8 && byId('devModal').style.display === 'flex') { loadFlows(); }
    }, 10000);
  }
}

function setFlowL7(on) {
  authedApi('/api/flows/settings', { l7_enabled: on }).then(function () {
    loadFlows();
  }).catch(function (e) {
    byId('flowL7').checked = !on;
    alert('切换失败: ' + e);
  });
}

function renderFlowSummary(s) {
  var item = function (label, val) {
    return '<div class="hw-item"><h4>' + label + '</h4><div class="val">' + val + '</div></div>';
  };
  byId('flowSummary').innerHTML =
    item('平均速率 (估算)', (s.avg_mbps || 0).toFixed(2) + ' Mbps') +
    item('总流量 (估算)', fmtBytes(s.bytes)) +
    item('样本/流记录', (s.samples || 0) + ' 条') +
    item('源 IP 数', s.src_ips || 0) +
    item('目的 IP 数', s.dst_ips || 0) +
    item('缓冲区记录', (s.buffered || 0) + ' 条');
}

// renderFlowTimeline: 纯 SVG 画一条 10 秒粒度的速率曲线, 不引入图表库。
// 没数据的 10 秒桶按 0 画, 曲线断崖一眼就能看出"那段时间设备没推数据/没流量"。
function renderFlowTimeline(points) {
  var box = byId('flowTimeline');
  if (!points.length) { box.innerHTML = '<div style="color:var(--fg-faint);font-size:12px;padding:8px 0;">还没有数据</div>'; return; }
  var first = points[0].ts, last = points[points.length - 1].ts;
  var byTs = {};
  points.forEach(function (p) { byTs[p.ts] = p; });
  var series = [];
  for (var t = first; t <= last; t += 10) { series.push(byTs[t] || { ts: t, mbps: 0, pps: 0 }); }
  var max = 0;
  series.forEach(function (p) { if (p.mbps > max) { max = p.mbps; } });
  if (max <= 0) { max = 1; }
  var W = 1000, H = 160, padL = 56, padB = 22, padT = 10;
  var n = series.length;
  var x = function (i) { return padL + (n <= 1 ? 0 : i * (W - padL - 10) / (n - 1)); };
  var y = function (v) { return padT + (H - padT - padB) * (1 - v / max); };
  var line = series.map(function (p, i) { return x(i).toFixed(1) + ',' + y(p.mbps).toFixed(1); }).join(' ');
  var area = x(0).toFixed(1) + ',' + y(0).toFixed(1) + ' ' + line + ' ' + x(n - 1).toFixed(1) + ',' + y(0).toFixed(1);
  var fmtT = function (ts) { var dt = new Date(ts * 1000); return ('0' + dt.getHours()).slice(-2) + ':' + ('0' + dt.getMinutes()).slice(-2) + ':' + ('0' + dt.getSeconds()).slice(-2); };
  var dots = series.map(function (p, i) {
    return '<rect x="' + (x(i) - 4).toFixed(1) + '" y="' + padT + '" width="8" height="' + (H - padT - padB) + '" fill="transparent"><title>' +
      fmtT(p.ts) + '  ' + p.mbps.toFixed(2) + ' Mbps  ' + Math.round(p.pps) + ' pps</title></rect>';
  }).join('');
  box.innerHTML = '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;height:160px;display:block;" preserveAspectRatio="none">' +
    '<line x1="' + padL + '" y1="' + y(max) + '" x2="' + (W - 10) + '" y2="' + y(max) + '" stroke="var(--card-border)" stroke-dasharray="3,3"/>' +
    '<line x1="' + padL + '" y1="' + y(0) + '" x2="' + (W - 10) + '" y2="' + y(0) + '" stroke="var(--card-border)"/>' +
    '<text x="4" y="' + (y(max) + 4) + '" font-size="11" fill="var(--fg-faint)">' + max.toFixed(1) + 'M</text>' +
    '<text x="4" y="' + (y(0) + 4) + '" font-size="11" fill="var(--fg-faint)">0</text>' +
    '<text x="' + padL + '" y="' + (H - 4) + '" font-size="11" fill="var(--fg-faint)">' + fmtT(first) + '</text>' +
    '<text x="' + (W - 10) + '" y="' + (H - 4) + '" font-size="11" fill="var(--fg-faint)" text-anchor="end">' + fmtT(last) + '</text>' +
    '<polygon points="' + area + '" fill="var(--info)" fill-opacity="0.12"/>' +
    '<polyline points="' + line + '" fill="none" stroke="var(--info)" stroke-width="1.6"/>' + dots + '</svg>';
}

function renderFlowDomains(d) {
  var l7 = d.l7_stats || {};
  var hint = '';
  if (!d.l7_enabled) {
    hint = '· 已关闭';
  } else if (l7.tls > 0 && l7.tls_no_sni * 2 > l7.tls) {
    hint = '· ⚠️ ' + l7.tls_no_sni + '/' + l7.tls + ' 个 HTTPS 握手包的 SNI 被截断: 交换机 sFlow 报文头截取长度太短(常见默认 128 字节), 调到 256 或更大才能看到大部分 HTTPS 域名';
  } else if (l7.dns + l7.tls + l7.http > 0) {
    hint = '· DNS ' + (l7.dns || 0) + ' / TLS ' + (l7.tls || 0) + ' / HTTP ' + (l7.http || 0) + ' 个样本';
  }
  byId('flowL7Hint').innerText = hint;
  var rows = d.top_domains || [];
  if (!d.l7_enabled) {
    byId('flowDomainBody').innerHTML = FLOW_EMPTY(5, 'L7 域名解析已关闭 (右上角开关)');
    return;
  }
  var protoPill = function (p) {
    var c = p === 'TLS' ? 'pill-access' : (p === 'DNS' ? 'pill-uplink' : 'pill-amb');
    return '<span class="pill ' + c + '">' + (p === 'TLS' ? 'HTTPS' : esc(p)) + '</span>';
  };
  byId('flowDomainBody').innerHTML = rows.length ? rows.map(function (r) {
    return '<tr><td><code>' + esc(r.host) + '</code></td><td>' + protoPill(r.proto) + '</td><td>' + r.clients + '</td><td>' + r.flows + '</td><td>' + fmtBytes(r.bytes) + '</td></tr>';
  }).join('') : FLOW_EMPTY(5, '还没有识别到域名 (只有 sFlow 原始报文头能做 L7 识别, NetFlow 没有报文内容)');
}

function renderFlowTcp(t, suspects) {
  var total = t.total || 0;
  var pct = function (n) { return total ? ' (' + (n / total * 100).toFixed(1) + '%)' : ''; };
  var warn = total >= 20 && t.syn_only / total > 0.5
    ? ' <b style="color:var(--bad);">纯 SYN 占比过半, 留意 SYN Flood / 扫描</b>' : '';
  var rstWarn = total >= 20 && t.rst / total > 0.3
    ? ' <b style="color:var(--warn);">RST 占比偏高, 可能有大量连接被拒绝/重置</b>' : '';
  byId('flowTcpBox').innerHTML = total
    ? ('带标志位的 TCP 样本 ' + total + ' 个: 纯 SYN ' + t.syn_only + pct(t.syn_only) + ' · SYN+ACK ' + t.syn_ack + pct(t.syn_ack) +
      ' · RST ' + t.rst + pct(t.rst) + ' · FIN ' + t.fin + pct(t.fin) + warn + rstWarn)
    : '没有带 TCP 标志位的样本';
  byId('flowSynBody').innerHTML = suspects.length ? suspects.map(function (s) {
    return '<tr style="background:var(--bad-bg);"><td><code>' + esc(s.src_ip) + '</code></td><td>' + s.syn_only + '</td><td><b>' + s.targets + '</b></td><td style="font-size:11.5px;">' + (s.sample || []).map(esc).join('<br>') + '</td></tr>';
  }).join('') : FLOW_EMPTY(4, '没有发现对大量不同目标发纯 SYN 的源 (阈值: 窗口内 ≥ 10 个不同 目的IP:端口)');
}

function renderFlowCongestion(list) {
  var box = byId('flowCongestionBox');
  if (!list.length) {
    box.innerHTML = '<div style="color:var(--fg-faint);font-size:12.5px;">当前窗口里没有接口出现丢包/错包/利用率 ≥ 80% (需要交换机开启 sFlow counter sampling / polling-interval)。</div>';
    return;
  }
  box.innerHTML = list.map(function (c) {
    var talkers = (c.talkers || []).map(function (t) {
      return '<tr><td>' + esc(t.src_ip) + '</td><td>' + esc(t.dst_ip) + ':' + (t.dst_port || '-') + '</td><td>' + esc(t.protocol) + '</td><td>' + fmtBytes(t.bytes) + '</td></tr>';
    }).join('');
    return '<div style="background:var(--bad-bg);border:1px solid var(--card-border);border-radius:8px;padding:10px 12px;margin-bottom:10px;">' +
      '<div style="font-weight:600;margin-bottom:6px;">⚠️ ' + esc(c.if_name || ('ifIndex ' + c.if_index)) +
      ' <span style="font-weight:400;color:var(--fg-dim);font-size:12px;">' + esc(c.reason) + '</span></div>' +
      (talkers
        ? '<table><thead><tr><th style="cursor:default;">源</th><th style="cursor:default;">目的</th><th style="cursor:default;">协议</th><th style="cursor:default;">流量</th></tr></thead><tbody>' + talkers + '</tbody></table>'
        : '<div style="font-size:12px;color:var(--fg-dim);">这个时间窗口里没有抽样到经过该接口的流量</div>') +
      '</div>';
  }).join('');
}

function renderFlowIfCounters(list, host) {
  var hh = '';
  if (host) {
    var cpu = host.cpu_1m >= 0 ? host.cpu_1m.toFixed(1) + '%' : '-';
    var mem = host.mem_total > 0 ? ((1 - host.mem_free / host.mem_total) * 100).toFixed(1) + '%' : '-';
    hh = '· sFlow 上报 CPU(1 分钟) ' + cpu + ' / 内存 ' + mem + ' (' + esc(host.updated_at) + ')';
  }
  byId('flowHostHint').innerHTML = hh;
  var body = byId('flowIfBody');
  if (!list.length) {
    body.innerHTML = FLOW_EMPTY(9, '还没收到接口计数器 (交换机上要配置 sFlow counter 采样, 如锐捷 sflow counter interval / 华为 sflow counter-sampling interval)');
    return;
  }
  body.innerHTML = list.map(function (c) {
    var speed = c.speed_mbps >= 1000 ? (c.speed_mbps / 1000) + 'G' : (c.speed_mbps > 0 ? c.speed_mbps + 'M' : '-');
    var bad = function (a, b) {
      var s = a + ' / ' + b;
      return (a + b > 0) ? '<b style="color:var(--bad);">' + s + '</b>' : '<span style="color:var(--fg-faint);">' + s + '</span>';
    };
    var util = function (mbps, u) {
      var col = u >= 80 ? 'var(--bad)' : (u >= 50 ? 'var(--warn)' : 'var(--fg)');
      return mbps.toFixed(2) + ' <span style="color:' + col + ';">(' + u.toFixed(1) + '%)</span>';
    };
    return '<tr><td><b>' + esc(c.if_name || ('ifIndex ' + c.if_index)) + '</b></td>' +
      '<td>' + (c.up ? '<span style="color:var(--good);">● UP</span>' : '<span style="color:var(--fg-faint);">● DOWN</span>') + '</td>' +
      '<td>' + speed + '</td><td>' + util(c.in_mbps, c.in_util) + '</td><td>' + util(c.out_mbps, c.out_util) + '</td>' +
      '<td>' + bad(c.in_discards, c.out_discards) + '</td><td>' + bad(c.in_errors, c.out_errors) + '</td>' +
      '<td>' + c.in_bcast_pps + '</td><td style="font-size:11.5px;color:var(--fg-dim);">' + esc(c.updated_at) + '</td></tr>';
  }).join('');
}

function tcpFlagsText(f) {
  if (!f) { return ''; }
  var out = [];
  if (f & 0x02) { out.push('SYN'); }
  if (f & 0x10) { out.push('ACK'); }
  if (f & 0x01) { out.push('FIN'); }
  if (f & 0x04) { out.push('RST'); }
  if (f & 0x08) { out.push('PSH'); }
  return out.join(',');
}

function renderFlowRecords() {
  var kw = (byId('flowRecSearch').value || '').toLowerCase();
  var recs = flowLastRecords.filter(function (r) {
    if (!kw) { return true; }
    var hay = [r.src_ip, r.dst_ip, r.src_port, r.dst_port, r.protocol, r.app_host, r.app_path, r.app, r.app_cat, r.src_mac, r.dst_mac,
      r.vlan ? 'vlan' + r.vlan : '', r.in_if_name, r.out_if_name].join(' ').toLowerCase();
    return hay.indexOf(kw) >= 0;
  });
  byId('flowRecBody').innerHTML = recs.length ? recs.map(function (r) {
    var rowStyle = r.suspicious ? ' style="background:var(--bad-bg);"' : '';
    var mark = r.suspicious ? '<span class="pill" style="background:var(--bad-bg);color:var(--bad-fg);" title="' + esc(r.suspicious_reason) + '">🔴 疑似扫描</span>' : '';
    var app = r.app ? '<b>' + esc(r.app) + '</b> ' : '';
    if (r.app_proto) {
      app += '<span class="pill pill-access">' + (r.app_proto === 'TLS' ? 'HTTPS' : esc(r.app_proto)) + '</span> ' +
        (r.app_host ? '<code>' + esc(r.app_host) + '</code>' : '<span style="color:var(--fg-faint);">(SNI 被截断)</span>') +
        (r.app_path ? '<div style="font-size:12px;color:var(--fg-dim);">' + esc(r.app_path) + '</div>' : '');
    }
    var macTip = (r.src_mac || r.dst_mac) ? ' title="' + esc((r.src_mac || '?') + ' → ' + (r.dst_mac || '?')) + '"' : '';
    // 二层帧没有 IP, 显示 MAC
    var srcTxt = r.src_ip ? esc(r.src_ip) + ':' + (r.src_port || '-') : '<code>' + esc(r.src_mac || '-') + '</code>';
    var dstTxt = r.dst_ip ? esc(r.dst_ip) + ':' + (r.dst_port || '-') : '<code>' + esc(r.dst_mac || '-') + '</code>';
    return '<tr' + rowStyle + '><td style="white-space:nowrap;">' + esc(r.time) + '</td><td' + macTip + '>' + srcTxt + '</td>' +
      '<td>' + dstTxt + '</td><td>' + esc(r.protocol) + '</td>' +
      '<td>' + (r.vlan || '-') + '</td><td>' + (app || '<span style="color:var(--fg-faint);">-</span>') + '</td>' +
      '<td style="font-size:11.5px;">' + esc(tcpFlagsText(r.tcp_flags)) + '</td>' +
      '<td>' + fmtBytes(r.bytes) + '</td><td>' + esc(r.in_if_name || r.in_ifindex || '-') + '</td><td>' + esc(r.out_if_name || r.out_ifindex || '-') + '</td><td>' + mark + '</td></tr>';
  }).join('') : FLOW_EMPTY(11);
}

// renderFlowDirBox: 东西向/南北向流量构成, 横向堆叠条 + 数字。同时回答
// "为什么看不到私网到私网的流量"——大多数交换机的 NetFlow/sFlow 只在报文
// 经过三层路由(网关/SVI)时才记账(sFlow 在接入口上开的话二层流量也会被抽样),
// 同一网段内两台私网机器之间纯二层转发如果没被计入, 不一定是采集端漏了。
function renderFlowDirBox(dirs) {
  var box = byId('flowDirBox');
  var total = 0;
  dirs.forEach(function (d) { total += d.bytes; });
  if (total === 0) { box.innerHTML = ''; return; }
  var colors = ['#10b981', '#2563eb', '#f59e0b', '#94a3b8']; // 顺序跟后端 direction_stats 一致
  var bar = '<div style="display:flex;height:10px;border-radius:5px;overflow:hidden;margin-bottom:8px;">';
  var legend = '';
  dirs.forEach(function (d, i) {
    if (d.bytes === 0) { return; }
    var pct = (d.bytes / total * 100);
    var c = colors[i % colors.length];
    bar += '<div style="width:' + pct.toFixed(2) + '%;background:' + c + ';" title="' + esc(d.label) + ' ' + pct.toFixed(1) + '%"></div>';
    legend += '<span style="display:inline-flex;align-items:center;gap:5px;margin-right:16px;font-size:12px;color:var(--fg-dim);">' +
      '<span style="width:9px;height:9px;border-radius:2px;background:' + c + ';display:inline-block;"></span>' +
      esc(d.label) + ' ' + fmtBytes(d.bytes) + ' (' + pct.toFixed(1) + '%, ' + d.flows + ' 条)</span>';
  });
  bar += '</div>';
  box.innerHTML = '<div class="chart-title" style="margin:6px 0 8px;">流量方向构成 (东西向 = 内网互访, 南北向 = 进出公网)</div>' + bar +
    '<div>' + legend + '</div>' +
    '<div style="font-size:11.5px;color:var(--fg-faint);margin-top:6px;">NetFlow 通常只在流量经过三层路由(网关/SVI)时才记账, 同一网段内的纯二层转发不会出现; sFlow 如果只在上联口开启, 同样看不到接入层内部互访。"东西向"很低不一定是漏采。</div>';
}

// renderFlowPortPie: 用纯 CSS conic-gradient 画一个协议分布环形图,
// 不引入额外图表库。
function renderFlowApps(d) {
  var apps = d.top_apps || [];
  byId('flowAppBody').innerHTML = apps.length ? apps.map(function (a) {
    var unk = (a.cat || '').indexOf('未识别') === 0;
    return '<tr><td><b' + (unk ? ' style="color:var(--fg-dim);font-weight:500;"' : '') + '>' + esc(a.app) + '</b></td>' +
      '<td><span class="tag">' + esc(a.cat || '-') + '</span></td><td>' + (a.clients || '-') + '</td><td>' + a.flows + '</td><td>' + fmtBytes(a.bytes) + '</td></tr>';
  }).join('') : FLOW_EMPTY(5);
  renderFlowPortPie((d.app_cats || []).map(function (c) { return { name: c.cat, bytes: c.bytes }; }), 'flowCatPie');
  var protos = d.protocols || [];
  var total = 0;
  protos.forEach(function (p) { total += p.bytes; });
  byId('flowProtoBox').innerHTML = protos.length ? protos.map(function (p) {
    var pct = total ? p.bytes / total * 100 : 0;
    return '<div class="proto-row"><span>' + esc(p.name) + (p.l2 ? ' <span class="tag">L2</span>' : '') + '</span>' +
      '<div class="progress-bar-wrap" style="flex:1;height:6px;margin:0 8px;"><div class="progress-bar" style="width:' + pct.toFixed(1) + '%;background:var(--accent);"></div></div>' +
      '<span class="sub" style="margin:0;min-width:92px;text-align:right;">' + pct.toFixed(1) + '% · ' + p.flows + ' 样本</span></div>';
  }).join('') : '<div class="faint" style="font-size:12px;">还没有数据</div>';

  var l2 = d.l2_stats || {};
  var box = byId('flowL2Box');
  if (!l2.total) {
    box.innerHTML = '<div class="faint" style="font-size:12.5px;">窗口内没有二层控制报文样本。NetFlow 和 sFlow sampled_ipv4 格式不带二层帧; sFlow 原始报文头模式下 ARP/STP/LLDP 本身量很小, 抽样率高时可能要等更久。</div>';
    return;
  }
  var counts = Object.keys(l2.counts || {}).sort(function (a, b) { return l2.counts[b] - l2.counts[a]; })
    .map(function (k) { return '<span class="tag" style="margin:2px;">' + esc(k) + ' × ' + l2.counts[k] + '</span>'; }).join('');
  var arpColor = l2.arp_pps >= 500 ? 'var(--bad)' : (l2.arp_pps >= 100 ? 'var(--warn)' : 'var(--fg)');
  var html = '<div style="margin-bottom:8px;">' + counts + '</div>' +
    '<div class="kv-grid"><div class="hw-item"><h4>ARP 估算速率</h4><div class="val" style="color:' + arpColor + ';">' + (l2.arp_pps || 0) + ' pps</div></div>' +
    '<div class="hw-item"><h4>收到 BPDU 的接口</h4><div class="val">' + (l2.bpdu_ifs || []).length + ' 个</div></div></div>';
  if ((l2.arp_top || []).length) {
    html += '<div class="sub" style="margin:6px 0;">发 ARP 最多的 MAC (≥100 pps 持续存在多为 ARP 扫描/病毒/网关冲突, 可到「MAC 总表」查它接在哪个口):</div>' +
      l2.arp_top.slice(0, 8).map(function (m) { return '<span class="tag" style="margin:2px;"><code>' + esc(m.mac) + '</code> ≈' + m.packets + ' 包</span>'; }).join('');
  }
  if ((l2.bpdu_ifs || []).length) {
    html += '<div class="sub" style="margin:10px 0 6px;">收到 STP BPDU 的接口 (红色 = 接入口上出现 BPDU, 下面很可能私接了交换机, 建议开启 BPDU Guard):</div>' +
      l2.bpdu_ifs.map(function (b) {
        return '<span class="tag" style="margin:2px;' + (b.suspicious ? 'background:var(--bad-bg);color:var(--bad-fg);' : '') + '">' +
          esc(b.if_name || ('ifIndex ' + b.if_index)) + ' ← ' + esc(b.src_mac) + '</span>';
      }).join('');
  }
  box.innerHTML = html;
}
function renderFlowPortPie(buckets, boxId) {
  var box = byId(boxId || 'flowPortPie');
  var total = 0;
  buckets.forEach(function (b) { total += b.bytes; });
  if (total === 0) { box.innerHTML = '<div style="color:var(--fg-faint);font-size:12px;padding:8px 0;">还没有数据</div>'; return; }
  var gradient = [];
  var acc = 0;
  var legend = '';
  buckets.forEach(function (b, i) {
    var pct = b.bytes / total * 100;
    var color = FLOW_PIE_COLORS[i % FLOW_PIE_COLORS.length];
    gradient.push(color + ' ' + acc.toFixed(2) + '% ' + (acc + pct).toFixed(2) + '%');
    acc += pct;
    var warn = (b.name === '高位端口' || b.name === 'Other') && pct >= 15;
    legend += '<div style="display:flex;align-items:center;gap:6px;font-size:12px;color:var(--fg-dim);margin-bottom:3px;">' +
      '<span style="width:9px;height:9px;border-radius:2px;background:' + color + ';display:inline-block;flex-shrink:0;"></span>' +
      '<span style="flex:1;">' + esc(b.name) + (warn ? ' ⚠️' : '') + '</span>' +
      '<span>' + pct.toFixed(1) + '%</span></div>';
  });
  box.innerHTML = '<div style="width:120px;height:120px;border-radius:50%;background:conic-gradient(' + gradient.join(',') + ');margin:0 auto 10px;"></div>' +
    '<div>' + legend + '</div>';
}
function fmtBytes(n) {
  n = n || 0;
  if (n >= 1024 * 1024 * 1024) { return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'; }
  if (n >= 1024 * 1024) { return (n / 1024 / 1024).toFixed(2) + ' MB'; }
  if (n >= 1024) { return (n / 1024).toFixed(1) + ' KB'; }
  return n + ' B';
}
