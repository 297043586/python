/*
 * 首页设备总览(v13 重写)。
 *
 *   - 搜索: 设备名 / IP / 备注 / 型号 / 分组 / 位置, 输入即筛;
 *   - 状态芯片: 全部 / 有告警 / 离线 / 正常, 带数量, 点一下只看这一类;
 *   - 分组: 自定义分组(设备管理里填) / 状态 / 厂商 / 型号 / 网段 / 位置; 每组一个标题条, 显示
 *     在线 / 离线 / 告警数量和健康度, 点标题折叠(折叠状态记在浏览器里);
 *   - 显示: 卡片 / 紧凑(设备多时一屏看更多) / 列表(可点表头排序);
 *   - 所有选择都记在浏览器里, 下次打开还是这样。
 *
 * 数据刷新后 app/refresh.js 发 'summary' 事件, 这里重画(见 core/events.js)。
 */
const HOME = prefs('homePrefs', { view: 'cards', group: '', sort: 'ip', state: '', q: '', fold: [] });
(function migrateHomePrefs() {
  // v12 只记了卡片 / 列表
  try { if (!localStorage.getItem('homePrefs') && localStorage.getItem('homeView') === 'list') { HOME.view = 'list'; } } catch (e) { /* 忽略 */ }
})();
var homeView = HOME.view; // 兼容旧脚本

const HOME_GROUPS = {
  group: { name: '自定义分组', key: (d) => d.group || '', empty: '(未分组)' },
  state: { name: '状态', key: (d) => devStateName(d), order: (a, b) => HOME_STATE_ORDER.indexOf(a) - HOME_STATE_ORDER.indexOf(b) },
  brand: { name: '厂商', key: (d) => pnlBrand(d.brand)[0] },
  model: { name: '型号', key: (d) => d.model || '', empty: '(型号未知)' },
  subnet: { name: '网段', key: (d) => { const p = String(d.ip).split('.'); return p.length === 4 ? p.slice(0, 3).join('.') + '.0/24' : d.ip; }, order: (a, b) => cmpNatural(ipSortKey(a.split('/')[0]), ipSortKey(b.split('/')[0])) },
  location: { name: '位置', key: (d) => d.location || '', empty: '(未填写位置)' }
};
const HOME_STATE_ORDER = ['离线', '有告警', '正常'];

function devStateName(d) { return !d.is_alive ? '离线' : d.open_alerts > 0 ? '有告警' : '正常'; }
function devStateCls(d) {
  if (!d.is_alive) { return 'error'; }
  if (d.open_alerts > 0) { return 'warn'; }
  if ((d.critical_count || 0) > 0 || (d.warning_count || 0) > 0) { return 'muted'; }
  return '';
}
function devTemp(d) { const v = parseFloat((d.hardware || {}).temperature); return isNaN(v) ? -1 : v; }

const HOME_SORTS = {
  ip: (a, b) => cmpNatural(ipSortKey(a.ip), ipSortKey(b.ip)),
  name: (a, b) => cmpNatural(a.sys_name, b.sys_name),
  state: (a, b) => ((a.is_alive ? 1 : 0) - (b.is_alive ? 1 : 0)) || ((b.open_alerts || 0) - (a.open_alerts || 0)),
  alerts: (a, b) => (b.open_alerts || 0) - (a.open_alerts || 0),
  cpu: (a, b) => (b.cpu_usage || 0) - (a.cpu_usage || 0),
  mem: (a, b) => (b.mem_usage || 0) - (a.mem_usage || 0),
  temp: (a, b) => devTemp(b) - devTemp(a)
};

function homeMatch(d, q) {
  if (!q) { return true; }
  return (d.sys_name + ' ' + d.ip + ' ' + (d.note || '') + ' ' + (d.model || '') + ' ' + (d.group || '') + ' ' + (d.location || '') + ' ' + (d.brand || '')).toLowerCase().indexOf(q) >= 0;
}

function renderCards() {
  const all = summaryData || [];
  byId('devCount').innerText = all.length;
  let totalOpen = 0;
  all.forEach((d) => { totalOpen += d.open_alerts || 0; });
  byId('openAlerts').innerText = totalOpen;
  byId('emptyHint').style.display = all.length ? 'none' : 'block';
  byId('homeTb').style.display = all.length ? '' : 'none';

  const q = HOME.q.trim().toLowerCase();
  const byQ = all.filter((d) => homeMatch(d, q));
  const cnt = { '': byQ.length, alarm: 0, off: 0, ok: 0 };
  byQ.forEach((d) => { if (!d.is_alive) { cnt.off++; } else if (d.open_alerts > 0) { cnt.alarm++; } else { cnt.ok++; } });
  const stChip = (k, t, c) => '<span class="chip ' + c + (HOME.state === k ? ' on' : '') + '" data-st="' + k + '">' + t + ' <b>' + cnt[k] + '</b></span>';
  byId('homeState').innerHTML = stChip('', '全部', '') + stChip('alarm', '有告警', 'c-warn') + stChip('off', '离线', 'c-bad') + stChip('ok', '正常', 'c-good');

  const rows = byQ.filter((d) => !HOME.state || (HOME.state === 'off' ? !d.is_alive : HOME.state === 'alarm' ? d.is_alive && d.open_alerts > 0 : d.is_alive && !(d.open_alerts > 0)));
  rows.sort(HOME_SORTS[HOME.sort] || HOME_SORTS.ip);

  document.querySelectorAll('#homeView button').forEach((b) => b.classList.toggle('on', b.getAttribute('data-v') === HOME.view));
  byId('homeGroup').value = HOME.group;
  byId('homeSort').value = HOME.sort;
  byId('homeFold').style.display = HOME.group ? '' : 'none';

  const body = byId('homeBody');
  if (!all.length) { body.innerHTML = ''; return; }
  if (!rows.length) { body.innerHTML = '<div class="empty">没有符合条件的设备' + (q ? ': "' + esc(HOME.q) + '"' : '') + '</div>'; return; }

  // 分组
  const g = HOME_GROUPS[HOME.group];
  let groups;
  if (g) {
    const m = new Map();
    rows.forEach((d) => { const k = g.key(d) || ''; if (!m.has(k)) { m.set(k, []); } m.get(k).push(d); });
    groups = Array.from(m.entries());
    groups.sort((a, b) => (a[0] === '') - (b[0] === '') || (g.order ? g.order(a[0], b[0]) : cmpNatural(a[0], b[0])));
  } else {
    groups = [['', rows]];
  }
  const fold = new Set(HOME.fold);
  const view = HOME.view;
  let out = '';
  if (view === 'list') {
    out = '<div class="dev-list"><table><thead><tr>' + homeListHead() + '</tr></thead>';
    groups.forEach(([k, list]) => {
      const fk = HOME.group + ':' + k;
      if (g) { out += '<tbody class="hg-row"><tr><td colspan="11">' + homeGroupHead(k || g.empty || '(空)', list, fold.has(fk), fk, g) + '</td></tr></tbody>'; }
      if (!g || !fold.has(fk)) { out += '<tbody>' + list.map(homeRowHTML).join('') + '</tbody>'; }
    });
    out += '</table></div>';
  } else {
    groups.forEach(([k, list]) => {
      const fk = HOME.group + ':' + k;
      const folded = g && fold.has(fk);
      out += '<section class="hgrp' + (folded ? ' folded' : '') + '">' + (g ? homeGroupHead(k || g.empty || '(空)', list, folded, fk, g) : '') +
        (folded ? '' : '<div class="card-grid' + (view === 'compact' ? ' compact' : '') + '">' + list.map(view === 'compact' ? homeMiniHTML : homeCardHTML).join('') + '</div>') + '</section>';
    });
  }
  body.innerHTML = out;
}

function homeGroupHead(title, list, folded, fk, g) {
  let on = 0, off = 0, alarm = 0, cpu = 0;
  list.forEach((d) => { if (!d.is_alive) { off++; } else { on++; cpu += d.cpu_usage || 0; if (d.open_alerts > 0) { alarm++; } } });
  const health = list.length ? Math.round((on - alarm * 0.5) / list.length * 100) : 0;
  const hc = health >= 90 ? 'var(--good)' : health >= 70 ? 'var(--warn)' : 'var(--bad)';
  return '<div class="hgrp-h" data-fold="' + esc(fk) + '"><span class="vt-caret">' + (folded ? '▸' : '▾') + '</span>' +
    '<span class="hgrp-k">' + esc(g.name) + '</span><b>' + esc(title) + '</b><span class="vt-gn">' + list.length + ' 台</span>' +
    '<span class="hgrp-st">' + (on ? '<span>' + dot('var(--good)') + on + ' 在线</span>' : '') + (off ? '<span>' + dot('var(--bad)') + off + ' 离线</span>' : '') +
    (alarm ? '<span>' + dot('var(--warn)') + alarm + ' 有告警</span>' : '') + (on ? '<span class="muted">平均 CPU ' + Math.round(cpu / on) + '%</span>' : '') + '</span>' +
    '<span class="hgrp-hl" title="健康度: 在线且无告警的比例"><i style="width:' + Math.max(0, health) + '%;background:' + hc + '"></i></span></div>';
}

function homeBar(v, lim, color) {
  return '<div class="progress-bar-wrap"><div class="progress-bar" style="width:' + Math.min(v || 0, 100) + '%;background:' + (v > lim ? 'var(--bad)' : color) + ';"></div></div>';
}
function homeBadges(d) {
  return '<span class="badge ' + (d.is_alive ? 'badge-online">在线' : 'badge-offline">离线') + '</span>' + (d.open_alerts > 0 ? '<span class="badge badge-alert">' + d.open_alerts + ' 未处理</span>' : '');
}
function homeCardHTML(d) {
  const st = devStateCls(d);
  const mem = d.mem_usage > 0 ? d.mem_usage + '%' : '未采集';
  const tags = [d.group ? '🏷️ ' + esc(d.group) : '', d.model ? esc(d.model) : ''].filter(Boolean).join(' · ');
  return '<div class="card' + (st ? ' ' + st : '') + '" data-ip="' + esc(d.ip) + '">' +
    '<div class="card-title"><h3 title="' + esc(d.sys_name) + '">' + esc(d.sys_name) + '</h3><div>' + homeBadges(d) + '</div></div>' +
    '<div class="meta">IP: <b>' + esc(d.ip) + '</b> (' + esc(d.brand) + ') | 运行: ' + esc(d.uptime_str || '-') + '</div>' +
    (tags ? '<div class="meta card-tags">' + tags + '</div>' : '') +
    (d.note ? '<div class="meta card-note">📝 ' + esc(d.note) + '</div>' : '') +
    '<div class="card-m"><div><span>CPU 使用率</span><span>' + (d.cpu_usage || 0) + '%</span></div>' + homeBar(d.cpu_usage, 80, 'var(--info)') + '</div>' +
    '<div class="card-m"><div><span>内存 使用率</span><span>' + mem + '</span></div>' + homeBar(d.mem_usage, 85, 'var(--good)') + '</div>' +
    '<div class="card-foot"><span>接入率: <b style="color:var(--accent);">' + esc(d.port_usage_ratio || '-') + '</b></span>' +
    '<span>温度: <b>' + esc((d.hardware || {}).temperature || '-') + '</b></span></div></div>';
}
function homeMiniHTML(d) {
  const st = devStateCls(d);
  const c = !d.is_alive ? 'var(--bad)' : d.open_alerts > 0 ? 'var(--warn)' : 'var(--good)';
  return '<div class="card mini' + (st ? ' ' + st : '') + '" data-ip="' + esc(d.ip) + '" title="' + esc(d.sys_name + ' ' + d.ip + (d.note ? '\n' + d.note : '')) + '">' +
    '<div class="mini-h">' + dot(c) + '<b>' + esc(d.sys_name) + '</b>' + (d.open_alerts > 0 ? '<span class="badge badge-alert">' + d.open_alerts + '</span>' : '') + '</div>' +
    '<div class="mini-s">' + esc(d.ip) + (d.note ? ' · ' + esc(d.note) : '') + '</div>' +
    (d.is_alive ? '<div class="mini-m"><span>CPU</span>' + homeBar(d.cpu_usage, 80, 'var(--info)') + '<em>' + (d.cpu_usage || 0) + '%</em></div>' +
      '<div class="mini-m"><span>内存</span>' + homeBar(d.mem_usage, 85, 'var(--good)') + '<em>' + (d.mem_usage > 0 ? d.mem_usage + '%' : '-') + '</em></div>'
      : '<div class="mini-off">离线</div>') + '</div>';
}
function homeListHead() {
  const th = (k, t) => '<th data-hsort="' + k + '">' + t + (HOME.sort === k ? ' <span class="sort-icon" style="color:var(--accent)">▼</span>' : '') + '</th>';
  return th('state', '状态') + th('name', '设备') + th('ip', 'IP') + '<th>厂商 / 型号</th><th>分组</th><th>备注</th>' + th('cpu', 'CPU') + th('mem', '内存') + '<th>接入率</th>' + th('temp', '温度') + '<th>运行</th>';
}
function homeRowHTML(d) {
  const mini = (v, lim, color) => '<div class="mini">' + homeBar(v, lim, color) + '<span>' + (v > 0 ? v + '%' : '-') + '</span></div>';
  return '<tr class="' + devStateCls(d) + '" data-ip="' + esc(d.ip) + '"><td>' + homeBadges(d) + '</td>' +
    '<td class="nm" title="' + esc(d.sys_name) + '">' + esc(d.sys_name) + '</td><td>' + esc(d.ip) + '</td>' +
    '<td>' + esc(d.brand) + (d.model ? ' <span class="muted">' + esc(d.model) + '</span>' : '') + '</td><td>' + esc(d.group || '') + '</td>' +
    '<td class="note" title="' + esc(d.note || '') + '">' + (d.note ? '📝 ' + esc(d.note) : '') + '</td>' +
    '<td>' + mini(d.cpu_usage, 80, 'var(--info)') + '</td><td>' + mini(d.mem_usage, 85, 'var(--good)') + '</td>' +
    '<td><b style="color:var(--accent);">' + esc(d.port_usage_ratio || '-') + '</b></td>' +
    '<td>' + esc((d.hardware || {}).temperature || '-') + '</td><td>' + esc(d.uptime_str || '-') + '</td></tr>';
}

App.on('summary', function () { renderCards(); });
function setHomeView(v) { HOME.view = homeView = v; HOME.save(); renderCards(); }

(function bindHome() {
  const onQ = debounce(() => { HOME.q = byId('homeQ').value; HOME.save(); renderCards(); }, 120);
  byId('homeQ').value = HOME.q;
  byId('homeQ').addEventListener('input', onQ);
  byId('homeGroup').addEventListener('change', (e) => { HOME.group = e.target.value; HOME.save(); renderCards(); });
  byId('homeSort').addEventListener('change', (e) => { HOME.sort = e.target.value; HOME.save(); renderCards(); });
  byId('homeView').addEventListener('click', (e) => { const b = e.target.closest('button[data-v]'); if (b) { setHomeView(b.getAttribute('data-v')); } });
  byId('homeState').addEventListener('click', (e) => { const c = e.target.closest('[data-st]'); if (c) { HOME.state = c.getAttribute('data-st'); HOME.save(); renderCards(); } });
  byId('homeFold').addEventListener('click', () => {
    const keys = Array.from(document.querySelectorAll('#homeBody [data-fold]')).map((x) => x.getAttribute('data-fold'));
    const allFolded = keys.every((k) => HOME.fold.indexOf(k) >= 0);
    HOME.fold = HOME.fold.filter((k) => keys.indexOf(k) < 0).concat(allFolded ? [] : keys);
    HOME.save(); renderCards();
  });
  byId('homeBody').addEventListener('click', (e) => {
    const f = e.target.closest('[data-fold]');
    if (f) {
      const k = f.getAttribute('data-fold'), i = HOME.fold.indexOf(k);
      if (i >= 0) { HOME.fold.splice(i, 1); } else { HOME.fold.push(k); }
      HOME.save(); renderCards();
      return;
    }
    const th = e.target.closest('[data-hsort]');
    if (th) { HOME.sort = th.getAttribute('data-hsort'); HOME.save(); renderCards(); return; }
    const card = e.target.closest('[data-ip]');
    if (card) { openModal(card.getAttribute('data-ip')); }
  });
})();
