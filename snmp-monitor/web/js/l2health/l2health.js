/*
 * 🩺 二层体检: 后端 l2health.go 按全网汇总出来的风暴 / 环路 / ARP 欺骗等结论;
 * 每条结论自带针对这台设备这个口的排查步骤(按厂商给命令), 不再放一篇通用说明。
 */
var L2 = { data: null, filter: '', loadedAt: 0 };
var L2_KINDS = {
  storm: ['🌪️', '广播风暴'], loop: ['🔁', 'MAC 漂移 / 环路'], stp: ['🌲', '生成树震荡'], rogue: ['🔌', '疑似私接'],
  no_lldp: ['📡', '已纳管·没开 LLDP'], unmanaged: ['➕', '未纳管设备'],
  arp_spoof: ['🕵️', 'ARP 欺骗'], arp_multi: ['🎭', '一 MAC 多 IP'], ip_conflict: ['⚔️', 'IP 冲突'],
  flap: ['📶', '端口抖动'], duplex: ['↔️', '半双工'], congestion: ['🚧', '出方向丢包']
};
var L2_LEVEL = { critical: ['严重', 'pill-bad'], warning: ['警告', 'pill-amb'], info: ['提示', 'pill-none'] };

function openL2Health(force) {
  byId('l2Modal').style.display = 'flex';
  if (!L2.fsInit) { fsButton(byId('l2Box'), byId('l2Fs')); L2.fsInit = true; }
  if (!force && L2.data && Date.now() - L2.loadedAt < 30000) { renderL2Health(); return; }
  byId('l2List').innerHTML = '<div class="empty"><span class="spin">⟳</span> 正在分析…</div>';
  pub('/api/l2health').then(function (d) {
    L2.data = d; L2.loadedAt = Date.now();
    renderL2Health();
  }).catch(function (e) { byId('l2List').innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '</div>'; });
}
modalRegister('l2Modal', closeL2Health);
function closeL2Health() { byId('l2Modal').style.display = 'none'; fsExitWithin(byId('l2Modal')); }
function l2SetFilter(k) { L2.filter = L2.filter === k ? '' : k; renderL2Health(); }

function renderL2Health() {
  var d = L2.data;
  if (!d) { return; }
  var f = d.findings || [], showInfo = byId('l2ShowInfo').checked;
  // 每类的最高级别, 决定摘要卡片的颜色
  var worst = {};
  f.forEach(function (x) { var w = worst[x.kind]; if (!w || (x.level === 'critical') || (x.level === 'warning' && w === 'info')) { worst[x.kind] = x.level; } });
  byId('l2Summary').innerHTML = Object.keys(L2_KINDS).map(function (k) {
    var n = (d.counts || {})[k] || 0, lv = worst[k] || 'ok';
    return '<button class="l2-card lv-' + lv + (L2.filter === k ? ' on' : '') + '" onclick="l2SetFilter(&quot;' + k + '&quot;)">' +
      '<span class="ic">' + L2_KINDS[k][0] + '</span><span class="nm">' + L2_KINDS[k][1] + '</span><b>' + (n ? n : '✓') + '</b></button>';
  }).join('');
  var list = f.filter(function (x) { return (showInfo || x.level !== 'info') && (!L2.filter || x.kind === L2.filter); });
  var lv = d.levels || {};
  byId('l2Stat').innerText = '严重 ' + (lv.critical || 0) + ' · 警告 ' + (lv.warning || 0) + ' · 提示 ' + (lv.info || 0) +
    ' · ' + d.devices + ' 台设备, ' + (d.stp_devices || 0) + ' 台上报了生成树' + (d.check_time ? ' · 数据来自 ' + d.check_time + ' 的巡检' : '');
  if (!list.length) {
    byId('l2List').innerHTML = '<div class="empty">' + (f.length ? '当前筛选下没有结论(可以勾上「显示提示级」)' : '✅ 没有发现二层异常') + '</div>';
    return;
  }
  byId('l2List').innerHTML = list.map(function (x) {
    var l = L2_LEVEL[x.level] || [x.level, 'pill-none'], k = L2_KINDS[x.kind] || ['•', x.kind];
    var go = '';
    if (x.device_ip) {
      var ip = '&quot;' + esc(x.device_ip) + '&quot;';
      go = '<span class="lnk" onclick="openModal(' + ip + ')">查看设备</span>' +
        (x.port ? ' · <span class="lnk" onclick="openDeviceAt(' + ip + ',2,&quot;' + esc(x.port) + '&quot;)">这个口上的终端</span>' : '');
    }
    if (x.kind === 'unmanaged') {
      go += (go ? ' · ' : '') + '<span class="lnk" data-perm="devices" onclick="quickAddFromLLDP(&quot;' + esc(x.add_ip || '') + '&quot;, &quot;' + esc(x.add_name || '') + '&quot;)">＋ 添加到监控' + (x.add_ip ? '(' + esc(x.add_ip) + ')' : '') + '</span>';
    }
    var steps = (x.steps || []).length ? '<details class="l2-steps"' + (x.level !== 'info' ? ' open' : '') + '><summary>🧭 排查步骤</summary><ol>' +
      x.steps.map(function (st) { return '<li>' + l2StepHTML(st) + '</li>'; }).join('') + '</ol></details>' : '';
    return '<div class="l2-item lv-' + x.level + '"><div class="hd"><span class="pill ' + l[1] + '">' + l[0] + '</span> <span title="' + esc(k[1]) + '">' + k[0] + '</span> <b>' + esc(x.title) + '</b></div>' +
      (x.detail ? '<div class="dt">' + esc(x.detail) + '</div>' : '') +
      ((x.evidence || []).length ? '<details><summary>证据 (' + x.evidence.length + ')</summary><ul>' + x.evidence.map(function (e) { return '<li><code>' + esc(e) + '</code></li>'; }).join('') + '</ul></details>' : '') +
      steps + (go ? '<div class="go">' + go + '</div>' : '') + '</div>';
  }).join('');
}

// 步骤里 "设备名: 命令" 这种, 命令部分用等宽字体并给个复制按钮
function l2StepHTML(st) {
  var m = /^([^:：]{1,60}): ((?:display|show|interface|lldp|arp|ip |undo|stp|port-security|switchport|spanning-tree|storm|loopback|VLAN 下).*)$/.exec(st);
  if (!m) { return esc(st); }
  var cmd = m[2], note = '', i = cmd.indexOf('   (');
  if (i > 0) { note = cmd.slice(i); cmd = cmd.slice(0, i); }
  return '<b>' + esc(m[1]) + '</b>: <code class="l2-cmd-c">' + esc(cmd) + '</code>' + (note ? '<span class="sub2" style="display:inline">' + esc(note) + '</span>' : '') +
    ' <span class="lnk" title="复制命令" onclick="l2Copy(this)">⧉</span>';
}
function l2Copy(el) {
  var t = el.parentNode.querySelector('code').innerText;
  var done = function () { el.innerText = '✓'; setTimeout(function () { el.innerText = '⧉'; }, 1200); };
  if (navigator.clipboard && navigator.clipboard.writeText) { navigator.clipboard.writeText(t).then(done).catch(function () {}); return; }
  var ta = document.createElement('textarea'); ta.value = t; document.body.appendChild(ta); ta.select();
  try { document.execCommand('copy'); done(); } catch (e) {} document.body.removeChild(ta);
}
