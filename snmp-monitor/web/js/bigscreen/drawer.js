/*
 * 大屏里的详情抽屉(v14)。点风险设备 / 告警 / 最忙端口 / 地图节点 / 3D 拓扑节点 / 3D 机房里的设备或机柜 /
 * KPI 数字, 在大屏右侧滑出一个面板, 不再退出大屏。
 *
 *   - 设备: 在线状态、健康分、CPU / 内存 / 温度 / 运行时间、未处理告警、最忙端口、近 24 小时流量曲线;
 *     只有点「完整详情 ↗」才退出大屏打开设备详情窗口。
 *   - 列表: KPI(在线 / 告警 / 端口 / 健康)、站点、机柜点开是设备或告警列表, 点一行进到设备, 「←」返回。
 *   - 大屏每次刷新时抽屉跟着刷新; 一段时间没人动自动收起, 值班室电视不会一直停在某台设备上。
 *   - 新出现的严重告警在顶部弹一条横幅(点它看设备), 几秒后自己消失。
 */
const BSD = { stack: [], idle: 0, seq: 0, devAt: 0, dev: null, toastTimer: 0 };
const BSD_IDLE_MS = 90000;

function bsdIsOpen() { return byId('bsDrawer').classList.contains('open'); }
// view: { t: 'dev', ip, port } | { t: 'list', kind: 'devices' | 'alerts' | 'ports' | 'risk', title, filter }
function bsdOpen(view, keepStack) {
  if (!keepStack) { BSD.stack = []; }
  const top = BSD.stack[BSD.stack.length - 1];
  if (top && top.t === view.t && top.ip === view.ip && top.kind === view.kind && top.title === view.title) { top.port = view.port; } else { BSD.stack.push(view); }
  byId('bsDrawer').classList.add('open');
  bsHideTip();
  bsdRender(true);
  bsdPoke();
}
function bsdClose() {
  const el = byId('bsDrawer');
  el.classList.remove('open');
  BSD.stack = []; BSD.dev = null;
  clearTimeout(BSD.idle);
}
function bsdBack() { if (BSD.stack.length > 1) { BSD.stack.pop(); bsdRender(true); } }
function bsdPoke() {
  clearTimeout(BSD.idle);
  BSD.idle = setTimeout(() => { if (bsdIsOpen()) { bsdClose(); } }, BSD_IDLE_MS);
}
// 大屏刷新时调用: 列表直接重画, 设备重新拉一次数据(至少隔 10 秒)
function bsdRefresh() {
  if (!bsdIsOpen()) { return; }
  const v = BSD.stack[BSD.stack.length - 1];
  if (v && v.t === 'dev' && Date.now() - BSD.devAt < 10000) { return; }
  bsdRender(false);
}
function bsdRender(fresh) {
  const v = BSD.stack[BSD.stack.length - 1];
  if (!v) { bsdClose(); return; }
  if (v.t === 'dev') { bsdDevice(v, fresh); } else { bsdList(v); }
}
function bsdShell(title, sub, body, extraBtns) {
  const back = BSD.stack.length > 1 ? html`<button class="bs-btn" onclick="bsdBack()" title="返回上一层">←</button>` : '';
  return html`<div class="bsd-h">${back}<div class="bsd-t"><b>${raw(title)}</b><small>${raw(sub || '')}</small></div>${raw(extraBtns || '')}<button class="bs-btn" onclick="bsdClose()" title="收起 (Esc)">✕</button></div>
    <div class="bsd-b">${raw(body)}</div>`;
}

/* ---------- 设备 ---------- */
function bsdOvDev(ip) { return ((BS.data && BS.data.devices) || []).find((x) => x.ip === ip); }
function bsdScore(ip) { const r = ((BS.data && BS.data.risks) || []).find((x) => x.ip === ip); return r ? r.score : null; }
function bsdDevice(v, fresh) {
  const box = byId('bsDrawer'), seq = ++BSD.seq, ov = bsdOvDev(v.ip);
  if (fresh || !BSD.dev || BSD.dev.ip !== v.ip) {
    box.innerHTML = bsdShell(esc(ov ? ov.name : v.ip), esc(v.ip), '<div class="bs-empty" style="padding-top:80px;">加载中…</div>');
  }
  pub('/api/device?ip=' + encodeURIComponent(v.ip)).then((d) => {
    if (seq !== BSD.seq || !bsdIsOpen()) { return; }
    BSD.dev = d; BSD.devAt = Date.now();
    const keep = box.querySelector('.bsd-b'), top = keep ? keep.scrollTop : 0;
    box.innerHTML = bsdShell(esc((ov && ov.name) || d.sys_name || d.ip), bsdDevSub(d), bsdDevBody(d, v),
      html`<button class="bs-btn" onclick="bsdOpenFull()" title="退出大屏, 打开完整的设备详情">完整详情 ↗</button>`.s);
    const b = box.querySelector('.bsd-b'); if (b && !fresh) { b.scrollTop = top; }
    bsdTrend(box.querySelector('.bsd-trend canvas'), d.trend_data || []);
  }).catch((e) => {
    if (seq !== BSD.seq) { return; }
    box.innerHTML = bsdShell(esc(ov ? ov.name : v.ip), esc(v.ip), html`<div class="bs-empty" style="padding-top:80px;">读取失败: ${String(e)}</div>`.s);
  });
}
function bsdDevSub(d) {
  const inv = d.inventory || {};
  const parts = [d.ip, pnlBrand(d.brand)[0], inv.model, inv.location].filter(Boolean);
  return esc(parts.join(' · '));
}
function bsdDevBody(d, v) {
  const P = BS.pal || {}, score = bsdScore(d.ip), hw = d.hardware || {};
  // 有的设备 total_ports 取不到, 按端口表自己数
  const totN = d.total_ports || (d.ports || []).length, upN = d.total_ports ? d.up_ports : (d.ports || []).filter((p) => p.status === 'UP').length;
  const pct = (n) => Math.max(0, Math.min(100, n || 0));
  const barCls = (n) => (n >= 85 ? 'hot' : n >= 70 ? 'warn' : '');
  const stat = (label, val, sub, color) => html`<div class="bsd-stat"><span>${label}</span><b style="${color ? 'color:' + color : ''}">${raw(val)}</b>${sub ? html`<small>${raw(sub)}</small>` : ''}</div>`;
  const meter = (label, n) => html`<div class="bsd-meter"><span>${label}</span><div class="bs-bar"><i class="${barCls(n)}" style="width:${pct(n)}%"></i></div><b>${n > 0 ? n + '%' : '-'}</b></div>`;
  let out = html`<div class="bsd-state ${d.is_alive ? 'on' : 'off'}">${d.is_alive ? '● 在线' : '● 离线 / 不可达'}${d.timestamp ? html`<small>数据时间 ${d.timestamp}</small>` : ''}</div>`;
  out += html`<div class="bsd-stats">
    ${stat('健康评分', score == null ? '-' : String(score), score == null ? '' : score >= 90 ? '优' : score >= 75 ? '良' : score >= 60 ? '中' : '差', score == null ? '' : bsHealthColor(score))}
    ${stat('端口在用', upN + '<em>/' + totN + '</em>', d.total_ports ? d.port_usage_ratio || '' : '')}
    ${stat('运行时间', esc(d.uptime_str || '-'), '')}
    ${stat('温度', esc(hw.temperature || '-'), esc([hw.fan_status && '风扇 ' + hw.fan_status, hw.power_status && '电源 ' + hw.power_status].filter(Boolean).join(' · ')))}
  </div>`;
  out += html`<div class="bsd-meters">${meter('CPU', d.cpu_usage)}${meter('内存', d.mem_usage)}</div>`;
  if (d.note) { out += html`<div class="bsd-note">📝 ${d.note}</div>`; }

  const alerts = [].concat(d.criticals || [], d.warnings || []).filter((a) => !a.closed);
  out += html`<div class="bsd-sec">未处理告警<small>${alerts.length ? alerts.length + ' 条' : ''}</small></div>`;
  out += alerts.length ? alerts.slice(0, 12).map((a) => html`<div class="bs-alert ${a.level}"><div class="t"><b>${a.level === 'critical' ? '● 严重' : '● 警告'} · ${alertCatName(a.category)}${a.subject ? ' · ' + a.subject : ''}</b><span>${(a.last_seen || '').slice(5, 16)}</span></div><div>${a.text}${a.acked ? html` <em class="bsd-ack">已确认${a.acked_by ? ' · ' + a.acked_by : ''}</em>` : ''}</div></div>`).join('') +
    (alerts.length > 12 ? html`<div class="bs-empty">…还有 ${alerts.length - 12} 条, 在完整详情里看</div>` : '')
    : '<div class="bs-empty">✅ 没有未处理告警</div>';

  // 最忙端口: 点进来时指定的端口排第一
  const util = (p) => Math.max(p.in_usage_percent || 0, p.out_usage_percent || 0);
  let ports = (d.ports || []).filter((p) => p.status === 'UP');
  ports.sort((a, b) => util(b) - util(a) || (b.in_speed_mbps + b.out_speed_mbps) - (a.in_speed_mbps + a.out_speed_mbps));
  if (v.port) { const i = ports.findIndex((p) => p.name === v.port); if (i > 0) { ports.unshift(ports.splice(i, 1)[0]); } }
  ports = ports.slice(0, 8);
  const busy = uiPref('busy_util_percent', 70);
  out += html`<div class="bsd-sec">最忙端口<small>本轮巡检平均速率</small></div>`;
  out += ports.length ? ports.map((p) => {
    const u = util(p), peer = p.lldp_remote_dev ? '➔ ' + p.lldp_remote_dev + (p.lldp_remote_port ? ' ' + p.lldp_remote_port : '') : (p.alias || '');
    return html`<div class="bs-row${p.name === v.port ? ' bsd-hl' : ''}"><span class="nm"><span style="color:${P.accent}">${p.name}</span>${p.speed_mbps ? html` <em class="bsd-spd">${fmtSpeedShort(p.speed_mbps)}</em>` : ''}<small>${peer ? peer + ' · ' : ''}入 ${bsFmtMbps(p.in_speed_mbps)} · 出 ${bsFmtMbps(p.out_speed_mbps)}</small></span>
      <div class="bs-bar" style="max-width:110px;"><i class="${u >= busy ? 'hot' : ''}" style="width:${Math.min(100, u)}%"></i></div><span class="bs-num">${u.toFixed(1)}%</span></div>`;
  }).join('') : '<div class="bs-empty">没有 UP 的端口</div>';

  out += html`<div class="bsd-sec">近 24 小时流量<small>设备所有端口入 / 出之和</small></div><div class="bsd-trend"><canvas></canvas></div>`;
  return String(out);
}
function fmtSpeedShort(m) { return m >= 1000 ? (m / 1000 % 1 ? (m / 1000).toFixed(1) : m / 1000) + 'G' : m + 'M'; }
function bsdTrend(cv, pts) {
  if (!cv) { return; }
  const now = Date.now() / 1000;
  pts = pts.filter((p) => p.ts > now - 86400).map((p) => ({ ts: p.ts, in: p.in_mbps || 0, out: p.out_mbps || 0 }));
  const W = cv.clientWidth, H = cv.clientHeight;
  if (!W || !H) { return; }
  const P = BS.pal || {}, dpr = Math.min(2, window.devicePixelRatio || 1);
  cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
  const ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  if (pts.length < 2) {
    ctx.fillStyle = P.dim; ctx.font = '13px "Microsoft YaHei"'; ctx.textAlign = 'center';
    ctx.fillText('趋势数据积累中', W / 2, H / 2); return;
  }
  const L = 64, R = 8, T = 22, B = 20;
  let max = 1;
  pts.forEach((p) => { max = Math.max(max, p.in, p.out); });
  max *= 1.15;
  const t0 = pts[0].ts, t1 = pts[pts.length - 1].ts || t0 + 1;
  const x = (ts) => L + (ts - t0) / Math.max(1, t1 - t0) * (W - L - R), y = (val) => T + (1 - val / max) * (H - T - B);
  ctx.strokeStyle = bsHexA(P.accent, 0.12); ctx.fillStyle = P.dim; ctx.font = '11px Consolas, monospace'; ctx.textAlign = 'right';
  for (let g = 0; g <= 3; g++) {
    const gy = T + g / 3 * (H - T - B);
    ctx.beginPath(); ctx.moveTo(L, gy); ctx.lineTo(W - R, gy); ctx.stroke();
    ctx.fillText(bsFmtMbps(max * (1 - g / 3)), L - 6, gy + 4);
  }
  ctx.textAlign = 'center';
  for (let k = 0; k <= 4; k++) { const ts = t0 + k / 4 * (t1 - t0), dt = new Date(ts * 1000); ctx.fillText(pad2(dt.getHours()) + ':' + pad2(dt.getMinutes()), x(ts), H - 5); }
  [['in', P.accent], ['out', P.accent2]].forEach(([key, color]) => {
    const grad = ctx.createLinearGradient(0, T, 0, H - B);
    grad.addColorStop(0, bsHexA(color, 0.35)); grad.addColorStop(1, bsHexA(color, 0));
    ctx.beginPath(); pts.forEach((p, i) => (i ? ctx.lineTo(x(p.ts), y(p[key])) : ctx.moveTo(x(p.ts), y(p[key]))));
    ctx.lineTo(x(t1), H - B); ctx.lineTo(x(t0), H - B); ctx.closePath(); ctx.fillStyle = grad; ctx.fill();
    ctx.beginPath(); pts.forEach((p, i) => (i ? ctx.lineTo(x(p.ts), y(p[key])) : ctx.moveTo(x(p.ts), y(p[key]))));
    ctx.strokeStyle = color; ctx.lineWidth = 1.8; ctx.stroke();
  });
  const last = pts[pts.length - 1];
  ctx.textAlign = 'left'; ctx.font = '12px "Microsoft YaHei"';
  ctx.fillStyle = P.accent; ctx.fillText('● 入 ' + bsFmtMbps(last.in), L + 6, 13);
  ctx.fillStyle = P.accent2; ctx.fillText('● 出 ' + bsFmtMbps(last.out), L + 130, 13);
}
function bsdOpenFull() {
  const v = BSD.stack[BSD.stack.length - 1];
  if (!v || v.t !== 'dev') { return; }
  closeBigScreen();
  if (v.port) { openDeviceAt(v.ip, 1, v.port); } else { openModal(v.ip); }
}

/* ---------- 列表 ---------- */
function bsdList(v) {
  const d = BS.data || {}, P = BS.pal || {};
  let rows = '', sub = '';
  if (v.kind === 'devices') {
    let devs = (d.devices || []).slice();
    if (v.filter && v.filter.site) { devs = devs.filter((x) => x.site === v.filter.site); }
    if (v.filter && v.filter.ips) { devs = devs.filter((x) => v.filter.ips.includes(x.ip)); }
    devs.sort((a, b) => (a.alive - b.alive) || (b.alerts - a.alerts) || naturalCmp(a.name, b.name));
    const off = devs.filter((x) => !x.alive).length;
    sub = devs.length + ' 台' + (off ? ' · ' + off + ' 台离线' : ' · 全部在线');
    rows = devs.map((x) => html`<div class="bs-row bs-click" data-dev="${x.ip}"><i class="bsd-dot ${!x.alive ? 'off' : x.alerts ? 'bad' : 'on'}"></i><span class="nm">${x.name}<small>${x.ip}${x.site && !(v.filter && v.filter.site) ? ' · ' + x.site : ''}${x.note ? ' · 📝 ' + x.note : ''}</small></span>
      <span class="bsd-mini">${x.alive ? 'CPU ' + x.cpu + '% · 内存 ' + (x.mem > 0 ? x.mem + '%' : '-') : '离线'}</span>${x.alerts ? html`<span class="bsd-badge">${x.alerts}</span>` : ''}</div>`).join('');
  } else if (v.kind === 'alerts') {
    const list = (d.alerts || []).filter((a) => !v.filter || !v.filter.level || a.level === v.filter.level);
    sub = list.length + ' 条未处理';
    rows = list.map((a) => html`<div class="bs-click" data-dev="${a.ip}">${raw(bsFeedItem(a, false))}</div>`).join('');
  } else if (v.kind === 'ports') {
    const list = d.hot_ports || [], busy = uiPref('busy_util_percent', 70);
    sub = '全网利用率最高的 ' + list.length + ' 个端口';
    rows = list.map((p) => {
      const u = Math.max(p.in_u, p.out_u);
      return html`<div class="bs-row bs-click" data-dev="${p.ip || ''}" data-port="${p.port}"><span class="nm">${p.device} <span style="color:${P.accent}">${p.port}</span><small>${p.peer ? '➔ ' + p.peer + ' · ' : ''}${bsFmtMbps(p.mbps)}</small></span>
        <div class="bs-bar" style="max-width:110px;"><i class="${u >= busy ? 'hot' : ''}" style="width:${Math.min(100, u)}%"></i></div><span class="bs-num">${u.toFixed(1)}%</span></div>`;
    }).join('');
  } else if (v.kind === 'risk') {
    const list = d.risks || [];
    sub = '健康评分由低到高 · 全网 ' + Math.round(d.health || 0) + ' 分';
    rows = list.map((r, i) => html`<div class="bs-row bs-click" data-dev="${r.ip}"><span class="bs-rank">${i + 1}</span><span class="nm">${r.name}<small>${r.issue || '无明显问题'}</small></span><span class="bs-num" style="color:${bsHealthColor(r.score)}">${r.score} 分</span></div>`).join('');
  }
  const box = byId('bsDrawer'), keep = box.querySelector('.bsd-b'), top = keep ? keep.scrollTop : 0;
  box.innerHTML = bsdShell(esc(v.title), esc(sub), rows || '<div class="bs-empty" style="padding-top:60px;">没有内容</div>');
  box.querySelector('.bsd-b').scrollTop = top;
}

/* ---------- 新严重告警横幅 ---------- */
function bsdToast(a) {
  const el = byId('bsToast');
  el.innerHTML = html`<b>⚠ 新的严重告警</b><span>${a.device}</span>${a.category}: ${a.text}<em>${(a.time || '').slice(11, 19)}</em>`.s;
  el.setAttribute('data-dev', a.ip);
  el.classList.remove('show'); void el.offsetWidth; el.classList.add('show');
  clearTimeout(BSD.toastTimer);
  BSD.toastTimer = setTimeout(() => el.classList.remove('show'), 12000);
}

/* ---------- 事件 ---------- */
(function () {
  const dr = byId('bsDrawer');
  ['pointerdown', 'wheel', 'pointermove', 'keydown'].forEach((ev) => dr.addEventListener(ev, bsdPoke, { passive: true }));
  dr.addEventListener('click', (e) => {
    const row = e.target.closest('[data-dev]');
    if (row && row.getAttribute('data-dev')) { bsdOpen({ t: 'dev', ip: row.getAttribute('data-dev'), port: row.getAttribute('data-port') || '' }, true); }
  });
  byId('bsToast').addEventListener('click', (e) => {
    const el = e.currentTarget;
    el.classList.remove('show');
    if (el.getAttribute('data-dev')) { bsdOpen({ t: 'dev', ip: el.getAttribute('data-dev') }); }
  });
  // KPI: 不同样式的 KPI 结构不一样, 按里面的数字 id 判断点的是哪一个
  const kpiView = (el) => {
    const id = (el.querySelector('[id^="bsK"]') || el).id || '';
    const d = BS.data || {};
    if (el.classList.contains('last')) { const a = (d.alerts || []).find((x) => x.level === 'critical'); return a ? { t: 'dev', ip: a.ip } : null; }
    if (id === 'bsKon') { return { t: 'list', kind: 'devices', title: '设备在线情况' }; }
    if (id === 'bsKcrit') { return { t: 'list', kind: 'alerts', title: d.alerts_critical ? '严重告警' : '未处理告警', filter: d.alerts_critical ? { level: 'critical' } : null }; }
    if (id === 'bsKwarn') { return { t: 'list', kind: 'alerts', title: '警告', filter: { level: 'warning' } }; }
    if (id === 'bsKup' || id === 'bsKin' || id === 'bsKout') { return { t: 'list', kind: 'ports', title: '最忙端口' }; }
    if (id === 'bsKh') { return { t: 'list', kind: 'risk', title: '设备健康排行' }; }
    if (/离线/.test(el.textContent)) { return { t: 'list', kind: 'devices', title: '设备在线情况' }; }
    return null;
  };
  byId('bsKpis').addEventListener('click', (e) => {
    const el = e.target.closest('.bs-kpi, .hk, .bs-tile, .bs-plate, .bs-readout > div, .bs-incident > div');
    const v = el && kpiView(el);
    if (v) { bsdOpen(v); }
  });
  byId('bsHealth').addEventListener('click', () => bsdOpen({ t: 'list', kind: 'risk', title: '设备健康排行' }));
})();
