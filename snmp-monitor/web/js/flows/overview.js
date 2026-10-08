/* 平台级 NetFlow / sFlow 总览与回传覆盖率。 */
var flowOverviewReady = false;
var flowOverviewData = null;

modalRegister('flowOverviewModal', closeFlowOverview);

function openFlowOverview() {
  byId('flowOverviewModal').style.display = 'flex';
  if (!flowOverviewReady) {
    flowOverviewReady = true;
    byId('flowOverviewWindow').addEventListener('change', loadFlowOverview);
    byId('flowOverviewDevices').addEventListener('click', function (e) {
      var row = e.target.closest('[data-device-ip]');
      if (row) { closeFlowOverview(); openDeviceAt(row.getAttribute('data-device-ip'), 8); }
    });
    fsButton(byId('flowOverviewBox'), byId('flowOverviewFs'));
  }
  loadFlowOverview();
}

function closeFlowOverview() {
  byId('flowOverviewModal').style.display = 'none';
  fsExitWithin(byId('flowOverviewModal'));
}

function loadFlowOverview() {
  var win = byId('flowOverviewWindow').value || '300';
  byId('flowOverviewHint').innerHTML = '<span class="spin">⟳</span> 正在汇总全平台数据…';
  return pub('/api/flows?window=' + encodeURIComponent(win)).then(function (d) {
    flowOverviewData = d;
    if (!d.enabled) {
      byId('flowOverviewHint').innerText = '流量采集端口未开启。请在设置中配置 NetFlow 或 sFlow 监听端口，并在设备上将导出目标指向本平台。';
      byId('flowOverviewCoverage').innerHTML = '';
      byId('flowOverviewSummary').innerHTML = '';
      byId('flowOverviewTimeline').innerHTML = '<div class="muted">采集未开启</div>';
      byId('flowOverviewCoverageNote').innerText = '';
      byId('flowOverviewDevices').innerHTML = '<tr><td colspan="8" class="muted">采集未开启</td></tr>';
      byId('flowOverviewTalkers').innerHTML = '<tr><td colspan="7" class="muted">采集未开启</td></tr>';
      return;
    }
    var s = d.summary || {}, c = d.coverage || {}, devs = c.devices || [];
    byId('flowOverviewHint').innerText = '统计窗口 ' + flowWindowLabel(d.window) + ' · NetFlow UDP ' + (d.netflow_port || '未配置') + ' · sFlow UDP ' + (d.sflow_port || '未配置');
    var total = c.total_devices || 0, seen = c.data_devices || 0;
    var pct = total ? (c.coverage_percent || 0).toFixed(1) : '—';
    byId('flowOverviewCoverage').innerHTML = '<b>设备回传覆盖率：' + seen + ' / ' + total + ' 台 (' + pct + '%)</b>' +
      ' · 近 5 分钟有流记录 <b>' + (c.flow_devices || 0) + '</b> 台 · 近 10 分钟有 sFlow 计数/设备指标 <b>' + (c.counter_devices || 0) + '</b> 台' +
      '<div class="muted" style="margin-top:4px;">分母为启用监控的设备；“未回传”只表示当前时间范围没有收到数据，不代表设备故障，也可能是设备尚未配置导出。</div>';
    byId('flowOverviewSummary').innerHTML = [
      flowOverviewMetric('窗口内估算流量', fmtBytes(s.bytes || 0)),
      flowOverviewMetric('平均速率', (s.avg_mbps || 0).toFixed(2) + ' Mbps'),
      flowOverviewMetric('流记录 / 样本', (s.samples || 0).toLocaleString()),
      flowOverviewMetric('估算数据包', (s.packets || 0).toLocaleString()),
      flowOverviewMetric('源 / 目的 IP', (s.src_ips || 0) + ' / ' + (s.dst_ips || 0)),
      flowOverviewMetric('缓冲区记录', (s.buffered || 0).toLocaleString())
    ].join('');
    var trend = d.timeline || [];
    if (Number(d.window) > 0) {
      var cutoff = Math.floor(Date.now() / 1000) - Number(d.window) - 10;
      trend = trend.filter(function (p) { return p.ts >= cutoff; });
    }
    flowOverviewRenderTrend(trend);
    byId('flowOverviewCoverageNote').innerText = '覆盖率以“近 5 分钟流记录或近 10 分钟 sFlow 计数/设备指标”计算；计数器接口数为已形成速率差值的接口。';
    byId('flowOverviewDevices').innerHTML = devs.length ? devs.map(flowOverviewDeviceRow).join('') : '<tr><td colspan="8" class="muted">当前没有启用监控的设备</td></tr>';
    var talkers = d.top_talkers || [];
    byId('flowOverviewTalkers').innerHTML = talkers.length ? talkers.slice(0, 20).map(function (t) {
      return '<tr><td><code>' + esc(t.src_ip) + '</code></td><td><code>' + esc(t.dst_ip) + '</code></td><td>' + (t.dst_port || '-') + '</td><td>' + esc(t.protocol) + '</td><td>' + fmtBytes(t.bytes) + '</td><td>' + (t.packets || 0).toLocaleString() + '</td><td>' + (t.flows || 0).toLocaleString() + '</td></tr>';
    }).join('') : '<tr><td colspan="7" class="muted">所选窗口内没有流记录</td></tr>';
  }).catch(function (e) {
    byId('flowOverviewHint').innerHTML = '<span style="color:var(--bad)">加载失败：' + esc(String(e)) + '</span>';
  });
}

function flowWindowLabel(v) {
  var n = Number(v);
  return n === 0 ? '缓冲区全部' : (n < 60 ? n + ' 秒' : (n % 60 ? Math.floor(n / 60) + ' 分 ' + (n % 60) + ' 秒' : n / 60 + ' 分钟'));
}

function flowOverviewMetric(label, value) {
  return '<div class="hw-item"><h4>' + esc(label) + '</h4><div class="val">' + value + '</div></div>';
}

function flowOverviewDeviceRow(d) {
  var status = d.flow_recent && d.counter_recent ? '流量 + 计数器' : (d.flow_recent ? '有流记录' : (d.counter_recent ? '仅计数器' : '未回传'));
  var cls = d.has_recent_data ? 'lv ok' : 'lv mute';
  return '<tr data-device-ip="' + esc(d.device_ip) + '" style="cursor:pointer;"><td><span class="lnk">' + esc(d.device_name || d.device_ip) + '</span><span class="sub2">' + esc(d.device_ip) + '</span></td>' +
    '<td><span class="' + cls + '">' + status + '</span></td><td>' + (d.flow_samples || 0).toLocaleString() + ' · ' + fmtBytes(d.flow_bytes || 0) + '</td>' +
    '<td>' + (d.sflow_samples || 0).toLocaleString() + '</td><td>' + (d.netflow_samples || 0).toLocaleString() + '</td>' +
    '<td>' + (d.counter_interfaces || 0) + ' 个</td><td>' + esc(d.last_flow_at || '-') + '</td><td>' + esc(d.last_counter_at || '-') + '</td></tr>';
}

function flowOverviewRenderTrend(points) {
  var box = byId('flowOverviewTimeline');
  if (!points.length) { box.innerHTML = '<div class="muted" style="padding:10px 0;">暂时没有流量趋势数据</div>'; return; }
  var first = points[0].ts, last = points[points.length - 1].ts, byTS = {};
  points.forEach(function (p) { byTS[p.ts] = p; });
  var series = [];
  for (var t = first; t <= last; t += 10) { series.push(byTS[t] || { ts: t, mbps: 0, pps: 0 }); }
  var max = Math.max.apply(null, series.map(function (p) { return p.mbps || 0; })) || 1;
  var W = 1000, H = 150, left = 56, bottom = 22, top = 10;
  var x = function (i) { return left + (series.length < 2 ? 0 : i * (W - left - 10) / (series.length - 1)); };
  var y = function (v) { return top + (H - top - bottom) * (1 - v / max); };
  var line = series.map(function (p, i) { return x(i).toFixed(1) + ',' + y(p.mbps || 0).toFixed(1); }).join(' ');
  var times = function (v) { var d = new Date(v * 1000); return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2) + ':' + ('0' + d.getSeconds()).slice(-2); };
  var tips = series.map(function (p, i) { return '<rect x="' + (x(i) - 4) + '" y="' + top + '" width="8" height="' + (H - top - bottom) + '" fill="transparent"><title>' + times(p.ts) + ' · ' + (p.mbps || 0).toFixed(2) + ' Mbps · ' + Math.round(p.pps || 0) + ' pps</title></rect>'; }).join('');
  box.innerHTML = '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;height:150px;display:block" preserveAspectRatio="none">' +
    '<line x1="' + left + '" y1="' + y(max) + '" x2="' + (W - 10) + '" y2="' + y(max) + '" stroke="var(--card-border)" stroke-dasharray="3,3"/>' +
    '<line x1="' + left + '" y1="' + y(0) + '" x2="' + (W - 10) + '" y2="' + y(0) + '" stroke="var(--card-border)"/>' +
    '<text x="4" y="' + (y(max) + 4) + '" font-size="11" fill="var(--fg-faint)">' + max.toFixed(1) + ' Mbps</text>' +
    '<text x="' + left + '" y="' + (H - 4) + '" font-size="11" fill="var(--fg-faint)">' + times(first) + '</text>' +
    '<text x="' + (W - 10) + '" y="' + (H - 4) + '" text-anchor="end" font-size="11" fill="var(--fg-faint)">' + times(last) + '</text>' +
    '<polyline points="' + line + '" fill="none" stroke="var(--info)" stroke-width="1.8"/>' + tips + '</svg>';
}
