/*
 * 大屏的 6 套样式 —— 不只是换颜色, 每套是一种不同的大屏: 布局、面板外观、标题栏、
 * KPI 的表现形式、告警列表的写法、中间地图和趋势图的画法都不一样。
 *
 *   tech     科技蓝      经典三栏: 左概况/风险, 中地图, 右告警; 圆环 KPI
 *   cyber    赛博霓虹    HUD: 地图铺满全屏做底, 半透明切角面板浮在上面; 顶部 KPI 跑马条;
 *                        告警是终端日志; 地图下半部是合成波透视网格
 *   aurora   极光        便当格: 左侧大地图, 右侧圆角玻璃卡片; 大号数字 KPI 磁贴
 *   gold     指挥中心    顶部一排金属铭牌 KPI; 标题两侧花饰; 地图节点是六边形; 底部四联面板
 *   mono     军绿雷达    中间是圆形雷达屏(扫描线扫过时设备才亮); 两侧是终端读数; 趋势是柱状图
 *   redalert 红色警戒    事件处置模式: 顶部巨型严重告警计数横幅, 告警列表做主角占左半屏,
 *                        面板带警戒黄黑条
 *
 * 数据和交互(滚动、刷新、3D 主画面)都是 bigscreen.go 里那一套, 这里只管"长什么样"。
 */
/* ================= 大屏: 各样式的 KPI / 告警条目 ================= */
function bsStyle() { return bsPref('style') || 'tech'; }
function bsKpiData(d) {
  var open = d.alerts_critical + d.alerts_warning;
  return { on: d.devices_online, total: d.devices_total, onPct: d.devices_total ? d.devices_online / d.devices_total * 100 : 0,
    up: d.ports_up, ports: d.ports_total, portPct: d.ports_total ? d.ports_up / d.ports_total * 100 : 0,
    crit: d.alerts_critical, warn: d.alerts_warning, open: open, off: d.devices_total - d.devices_online, health: d.health };
}
// 返回 true 表示已经画好(不走默认的圆环 KPI)
function bsKpiStyled(d) {
  var st = bsStyle(), k = bsKpiData(d), box = byId('bsKpis'), int0 = function (x) { return String(Math.round(x)); };
  if (st === 'tech') { if (box.hasAttribute('data-st')) { box.removeAttribute('data-st'); box.innerHTML = ''; } return false; }
  if (box.getAttribute('data-st') !== st) { box.innerHTML = ''; box.setAttribute('data-st', st); }
  var pct = function (v) { return Math.max(0, Math.min(100, v || 0)).toFixed(1) + '%'; };
  if (st === 'cyber') {
    var hk = function (label, id, sub, bar, bad) { return '<div class="hk' + (bad ? ' bad' : '') + '"><span>' + label + '</span><b><span id="' + id + '"></span>' + (sub ? '<em>' + sub + '</em>' : '') + '</b><i><u style="width:' + pct(bar) + ';"></u></i></div>'; };
    box.innerHTML = '<div class="bs-hud">' + hk('ONLINE · 在线', 'bsKon', ' / ' + k.total, k.onPct, k.off > 0) + hk('CRITICAL · 严重', 'bsKcrit', '', k.crit ? 100 : 0, k.crit > 0) +
      hk('WARNING · 警告', 'bsKwarn', '', Math.min(100, k.warn * 5), false) + hk('PORTS · 端口', 'bsKup', ' / ' + k.ports, k.portPct, false) +
      hk('IN · 入', 'bsKin', '', 60, false) + hk('OUT · 出', 'bsKout', '', 40, false) + hk('HEALTH · 健康', 'bsKh', '', k.health, k.health < 60) + '</div>';
  } else if (st === 'aurora') {
    var tile = function (label, id, unit, sub, bar, bad) { return '<div class="bs-tile' + (bad ? ' bad' : '') + '"><span>' + label + '</span><b><span id="' + id + '"></span><em>' + unit + '</em></b><div class="pb"><u style="width:' + pct(bar) + ';"></u></div><small>' + sub + '</small></div>'; };
    box.innerHTML = '<div class="bs-tiles">' + tile('设备在线', 'bsKon', '/ ' + k.total, k.off ? k.off + ' 台离线' : '全部在线', k.onPct, k.off > 0) +
      tile('未处理告警', 'bsKcrit', '严重', k.warn + ' 条警告', k.open ? Math.min(100, k.crit * 20 + 10) : 0, k.crit > 0) +
      tile('物理口使用', 'bsKup', '/ ' + k.ports, pct(k.portPct) + ' 在用', k.portPct, false) +
      tile('全网健康', 'bsKh', '分', '入 ' + bsFmtMbps(d.throughput_in) + ' · 出 ' + bsFmtMbps(d.throughput_out), k.health, k.health < 60) + '</div>';
  } else if (st === 'gold') {
    var plate = function (label, id, unit, sub, bad) { return '<div class="bs-plate' + (bad ? ' bad' : '') + '"><span>' + label + '</span><b><span id="' + id + '"></span><em>' + unit + '</em></b><small>' + sub + '</small></div>'; };
    box.innerHTML = '<div class="bs-plates">' + plate('在 线 设 备', 'bsKon', '/' + k.total, pct(k.onPct), k.off > 0) + plate('严 重 告 警', 'bsKcrit', '', '需立即处置', k.crit > 0) +
      plate('一 般 告 警', 'bsKwarn', '', '待确认', false) + plate('端 口 在 用', 'bsKup', '/' + k.ports, pct(k.portPct), false) +
      plate('入 向 吞 吐', 'bsKin', '', '全网端口合计', false) + plate('健 康 评 分', 'bsKh', '', k.health >= 90 ? '优' : k.health >= 75 ? '良' : k.health >= 60 ? '中' : '差', k.health < 60) + '</div>';
  } else if (st === 'mono') {
    var gauge = function (v) { var n = Math.round(Math.max(0, Math.min(100, v)) / 10); return '[' + new Array(n + 1).join('█') + new Array(11 - n).join('░') + ']'; };
    var line = function (key, id, tail, bad) { return '<div class="' + (bad ? 'bad' : '') + '"><span class="k">' + key + '</span><span class="dots">' + new Array(40).join('.') + '</span><span class="v" id="' + id + '"></span><span class="g">' + (tail || '') + '</span></div>'; };
    box.innerHTML = '<div class="bs-readout">' + line('DEV.ONLINE', 'bsKon', '/' + k.total + ' ' + gauge(k.onPct), k.off > 0) + line('ALM.CRIT', 'bsKcrit', '', k.crit > 0) +
      line('ALM.WARN', 'bsKwarn', '') + line('PORT.UP', 'bsKup', '/' + k.ports + ' ' + gauge(k.portPct)) + line('THRU.IN', 'bsKin', '') + line('THRU.OUT', 'bsKout', '') + line('HEALTH', 'bsKh', ' ' + gauge(k.health), k.health < 60) + '</div>';
  } else if (st === 'redalert') {
    var alerts = (d.alerts || []).filter(function (a) { return a.level === 'critical'; });
    var last = alerts[0];
    box.innerHTML = '<div class="bs-incident"><div class="big' + (k.crit ? ' flash' : ' calm') + '"><b id="bsKcrit"></b><span>严重告警</span></div>' +
      '<div class="sm"><span>警告</span><b id="bsKwarn"></b><small>未处理</small></div>' +
      '<div class="sm"><span>离线设备</span><b>' + k.off + '<small> / ' + k.total + '</small></b><small>健康评分 ' + Math.round(k.health) + '</small></div>' +
      '<div class="last"><span class="st' + (k.crit ? '' : ' calm') + '">' + (k.crit ? '● 事件处置中' : '● 态势平稳') + '</span>' +
      '<div>' + (last ? esc(last.device) + ': ' + esc(last.text) + '<br><span style="color:#d58f8f;font-size:14px;">' + esc(last.time || '') + ' · 共 ' + alerts.length + ' 条严重告警</span>' : '当前没有严重告警' + (k.warn ? ', 还有 ' + k.warn + ' 条警告待处理' : '')) + '</div></div></div>';
  }
  bsCount(byId('bsKon'), k.on, int0); bsCount(byId('bsKcrit'), k.crit, int0); bsCount(byId('bsKwarn'), k.warn, int0);
  bsCount(byId('bsKup'), k.up, int0); bsCount(byId('bsKin'), d.throughput_in, bsFmtMbps); bsCount(byId('bsKout'), d.throughput_out, bsFmtMbps); bsCount(byId('bsKh'), k.health, int0);
  return true;
}
/* ---------- 中间地图: 各样式的底图和节点 ---------- */
function bsMapBackdrop(ctx, sty, W, H, cx, cy, R, t, anim, P) {
  var i, a;
  if (sty === 'mono') {
    // 雷达屏: 实心圆 + 距离环 + 方位刻度 + 扫描扇区(带余辉)
    ctx.save();
    ctx.fillStyle = 'rgba(0,30,8,.85)'; ctx.beginPath(); ctx.arc(cx, cy, R, 0, Math.PI * 2); ctx.fill();
    ctx.strokeStyle = 'rgba(74,222,128,.35)'; ctx.lineWidth = 1;
    for (i = 1; i <= 5; i++) { ctx.beginPath(); ctx.arc(cx, cy, R * i / 5, 0, Math.PI * 2); ctx.stroke(); }
    for (i = 0; i < 12; i++) { a = i / 12 * Math.PI * 2; ctx.beginPath(); ctx.moveTo(cx, cy); ctx.lineTo(cx + Math.cos(a) * R, cy + Math.sin(a) * R); ctx.stroke(); }
    ctx.fillStyle = 'rgba(134,239,172,.7)'; ctx.font = '11px Consolas'; ctx.textAlign = 'center';
    for (i = 0; i < 36; i++) {
      a = i / 36 * Math.PI * 2 - Math.PI / 2;
      ctx.beginPath(); ctx.moveTo(cx + Math.cos(a) * R, cy + Math.sin(a) * R); ctx.lineTo(cx + Math.cos(a) * (R + (i % 3 ? 5 : 10)), cy + Math.sin(a) * (R + (i % 3 ? 5 : 10))); ctx.stroke();
      if (i % 3 === 0) { ctx.fillText(String(i * 10).padStart(3, '0'), cx + Math.cos(a) * (R + 22), cy + Math.sin(a) * (R + 22) + 4); }
    }
    if (anim) {
      var sw = BS.sweep;
      for (i = 0; i < 24; i++) {
        ctx.fillStyle = 'rgba(74,222,128,' + (0.22 * (1 - i / 24)).toFixed(3) + ')';
        ctx.beginPath(); ctx.moveTo(cx, cy); ctx.arc(cx, cy, R, sw - (i + 1) * 0.04, sw - i * 0.04); ctx.closePath(); ctx.fill();
      }
      ctx.strokeStyle = '#86efac'; ctx.lineWidth = 2; ctx.beginPath(); ctx.moveTo(cx, cy); ctx.lineTo(cx + Math.cos(sw) * R, cy + Math.sin(sw) * R); ctx.stroke();
    }
    ctx.restore();
    return;
  }
  if (sty === 'cyber') {
    // 合成波: 地平线 + 往近处流动的透视网格 + 条纹太阳
    var hz = H * 0.52;
    var sun = ctx.createLinearGradient(0, hz - R * 0.9, 0, hz);
    sun.addColorStop(0, 'rgba(255,214,10,.28)'); sun.addColorStop(1, 'rgba(255,43,214,.28)');
    ctx.save(); ctx.beginPath(); ctx.arc(W / 2, hz, R * 0.55, Math.PI, 0); ctx.clip();
    ctx.fillStyle = sun; ctx.fillRect(W / 2 - R, hz - R, R * 2, R);
    ctx.fillStyle = 'rgba(7,1,15,.9)';
    for (i = 0; i < 7; i++) { ctx.fillRect(W / 2 - R, hz - i * i * 3.2 - 6, R * 2, 2 + i * 0.8); }
    ctx.restore();
    ctx.strokeStyle = 'rgba(255,43,214,.45)'; ctx.lineWidth = 1.2;
    ctx.beginPath(); ctx.moveTo(0, hz); ctx.lineTo(W, hz); ctx.stroke();
    for (i = -24; i <= 24; i++) { ctx.beginPath(); ctx.moveTo(W / 2 + i * 12, hz); ctx.lineTo(W / 2 + i * 160, H); ctx.stroke(); }
    var off = anim ? (t * 0.35) % 1 : 0;
    for (i = 0; i < 16; i++) {
      var k = (i + off) / 16, y = hz + (H - hz) * k * k;
      ctx.globalAlpha = Math.min(1, k * 1.6);
      ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
    }
    ctx.globalAlpha = 1;
    return;
  }
  if (sty === 'aurora') {
    // 极光: 不画网格, 几团慢慢漂的柔光
    [[0.3, 0.35, '#10b981'], [0.7, 0.6, '#8b5cf6'], [0.5, 0.8, '#0ea5e9']].forEach(function (b, bi) {
      var bx = W * b[0] + Math.sin(t * 0.15 + bi) * 60, by = H * b[1] + Math.cos(t * 0.12 + bi) * 40;
      var g = ctx.createRadialGradient(bx, by, 0, bx, by, R * 0.9);
      g.addColorStop(0, bsHexA(b[2], 0.18)); g.addColorStop(1, bsHexA(b[2], 0));
      ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
    });
    ctx.strokeStyle = 'rgba(167,243,208,.10)'; ctx.setLineDash([2, 8]);
    for (i = 1; i <= 3; i++) { ctx.beginPath(); ctx.arc(cx, cy, R * i / 3, 0, Math.PI * 2); ctx.stroke(); }
    ctx.setLineDash([]);
    return;
  }
  if (sty === 'gold') {
    // 指挥中心: 罗盘 —— 细金环 + 八个方位 + 六边形底纹
    ctx.strokeStyle = 'rgba(245,196,81,.10)'; ctx.lineWidth = 1;
    var hs = 34;
    for (var yy = 0; yy < H + hs; yy += hs * 1.5) {
      for (var xx = 0; xx < W + hs; xx += hs * Math.sqrt(3)) {
        var ox = xx + ((Math.round(yy / (hs * 1.5)) % 2) ? hs * Math.sqrt(3) / 2 : 0);
        ctx.beginPath();
        for (i = 0; i < 6; i++) { a = Math.PI / 3 * i + Math.PI / 6; ctx[i ? 'lineTo' : 'moveTo'](ox + Math.cos(a) * hs * 0.55, yy + Math.sin(a) * hs * 0.55); }
        ctx.closePath(); ctx.stroke();
      }
    }
    ctx.strokeStyle = 'rgba(245,196,81,.4)';
    [1, 0.72].forEach(function (k) { ctx.beginPath(); ctx.arc(cx, cy, R * k, 0, Math.PI * 2); ctx.stroke(); });
    ctx.fillStyle = 'rgba(245,196,81,.7)'; ctx.font = '14px SimSun, Georgia, serif'; ctx.textAlign = 'center';
    ['北', '东北', '东', '东南', '南', '西南', '西', '西北'].forEach(function (s, si) {
      a = si / 8 * Math.PI * 2 - Math.PI / 2;
      ctx.fillText(s, cx + Math.cos(a) * (R + 18), cy + Math.sin(a) * (R + 18) + 5);
    });
    return;
  }
  // 科技蓝 / 红色警戒: 距离环 + 旋转刻度 + 扫描扇区
  ctx.strokeStyle = bsHexA(P.accent, 0.13); ctx.lineWidth = 1;
  for (i = 1; i <= 4; i++) { ctx.beginPath(); ctx.arc(cx, cy, R * 0.92 * i / 4, 0, Math.PI * 2); ctx.stroke(); }
  ctx.beginPath(); ctx.moveTo(cx - R, cy); ctx.lineTo(cx + R, cy); ctx.moveTo(cx, cy - R); ctx.lineTo(cx, cy + R); ctx.stroke();
  ctx.strokeStyle = bsHexA(P.accent, 0.35);
  for (i = 0; i < 72; i++) {
    a = i / 72 * Math.PI * 2 + t * 0.05; var l = i % 6 ? 6 : 14, r0 = R * 0.92 + 6;
    ctx.beginPath(); ctx.moveTo(cx + Math.cos(a) * r0, cy + Math.sin(a) * r0); ctx.lineTo(cx + Math.cos(a) * (r0 + l), cy + Math.sin(a) * (r0 + l)); ctx.stroke();
  }
  if (anim) {
    var g2 = ctx.createRadialGradient(cx, cy, 0, cx, cy, R);
    g2.addColorStop(0, bsHexA(P.accent, sty === 'redalert' ? 0.3 : 0.2)); g2.addColorStop(1, bsHexA(P.accent, 0));
    ctx.fillStyle = g2;
    ctx.beginPath(); ctx.moveTo(cx, cy); ctx.arc(cx, cy, R * 0.92, BS.sweep - 0.5, BS.sweep); ctx.closePath(); ctx.fill();
  }
}
function bsMapNode(ctx, sty, p, r, col, P) {
  var i, a;
  if (sty === 'mono') {
    // 雷达光点: 扫描线刚扫过最亮, 之后慢慢变暗
    var bright = 1;
    if (BS.sweep >= 0 && BS.mapC) {
      var ang = Math.atan2(p.y - BS.mapC.y, p.x - BS.mapC.x); if (ang < 0) { ang += Math.PI * 2; }
      var d = BS.sweep - ang; while (d < 0) { d += Math.PI * 2; }
      bright = Math.max(0.3, 1 - d / (Math.PI * 2) * 1.3);
    }
    ctx.globalAlpha = bright;
    ctx.fillStyle = col; ctx.shadowColor = col; ctx.shadowBlur = 14 * bright;
    ctx.fillRect(p.x - r * 0.45, p.y - r * 0.45, r * 0.9, r * 0.9);
    ctx.shadowBlur = 0; ctx.strokeStyle = col; ctx.lineWidth = 1;
    ctx.strokeRect(p.x - r, p.y - r, r * 2, r * 2);
    ctx.globalAlpha = 1;
    return;
  }
  var g2 = ctx.createRadialGradient(p.x, p.y, 0, p.x, p.y, r * 2.2);
  g2.addColorStop(0, bsHexA(col, sty === 'aurora' ? 0.7 : 0.55)); g2.addColorStop(1, bsHexA(col, 0));
  ctx.fillStyle = g2; ctx.beginPath(); ctx.arc(p.x, p.y, r * (sty === 'aurora' ? 2.8 : 2.2), 0, Math.PI * 2); ctx.fill();
  ctx.fillStyle = P.bg || '#041528'; ctx.strokeStyle = col; ctx.lineWidth = 2.5;
  ctx.beginPath();
  if (sty === 'gold') {
    for (i = 0; i < 6; i++) { a = Math.PI / 3 * i; ctx[i ? 'lineTo' : 'moveTo'](p.x + Math.cos(a) * r * 1.1, p.y + Math.sin(a) * r * 1.1); }
    ctx.closePath();
  } else if (sty === 'cyber') {
    ctx.moveTo(p.x, p.y - r * 1.2); ctx.lineTo(p.x + r * 1.2, p.y); ctx.lineTo(p.x, p.y + r * 1.2); ctx.lineTo(p.x - r * 1.2, p.y); ctx.closePath();
  } else { ctx.arc(p.x, p.y, r, 0, Math.PI * 2); }
  ctx.fill(); ctx.stroke();
  ctx.fillStyle = col; ctx.beginPath(); ctx.arc(p.x, p.y, r * 0.45, 0, Math.PI * 2); ctx.fill();
}
// 告警条目: 赛博 / 雷达是终端日志, 红色警戒突出设备名, 其余是卡片
function bsFeedItem(a, isNew) {
  var st = bsStyle(), lv = a.level === 'critical';
  if (st === 'cyber' || st === 'mono') {
    return '<div class="bs-term ' + esc(a.level) + (isNew ? ' new' : '') + '"><span class="ts">[' + esc((a.time || '').slice(11, 19) || '--:--:--') + ']</span> <span class="lv">' + (lv ? 'CRIT' : 'WARN') + '</span> ' +
      '<span class="dv">' + esc(a.device) + '</span>' + (a.note ? ' <span class="ts">(' + esc(a.note) + ')</span>' : '') + ' ' + esc(a.category) + ': ' + esc(a.text) + '</div>';
  }
  return '<div class="bs-alert ' + esc(a.level) + (isNew ? ' new' : '') + '"><div class="t"><b>' + (lv ? '● 严重' : '● 警告') + ' · ' + esc(a.category) + '</b><span>' + esc((a.time || '').slice(5)) + '</span></div>' +
    '<div>' + (st === 'redalert' ? '<span class="dev">' + esc(a.device) + '</span>' : esc(a.device) + ': ') + esc(a.text) + '</div>' +
    (a.note ? '<div style="opacity:.7;font-size:.9em;">📝 ' + esc(a.note) + '</div>' : '') + '</div>';
}
