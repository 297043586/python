/*
 * 告警中心(v13, 替换原来的"全网告警总表")。三个页签:
 *
 *   🔴 当前告警  全网正在发生 / 24 小时内恢复还没处理的告警; 可按设备 / 类别 / 级别 / 状态分组,
 *               一键确认当前筛选出来的全部未处理告警。
 *   📜 历史记录  每一次告警从出现到恢复是一条记录(后端 alert_history.go), 带持续时长、谁在什么时候
 *               确认 / 关闭、同一个对象反复出现了几次; 时间范围 + 多条件筛选, 滚到底自动加载更多,
 *               点一行在右侧看完整处理过程; 可以导出 CSV。
 *   📊 统计分析  跟历史记录同一套筛选: 每天告警数、按小时分布、按类别、最多的设备、反复抖动的对象、
 *               平均恢复时长(MTTR)、平均响应时长(MTTA)。
 *
 * 其他模块用到的名字保持不变: openAlertTable / closeAlertTable / renderGlobalAlertTable / globalAlertData /
 * ALERT_CATEGORY_CN / alertCatName。
 */
var ALERT_CATEGORY_CN = {
  unreachable: '设备离线/不可达', reboot: '设备重启', internal: '采集程序异常',
  cpu: 'CPU 负载', mem: '内存占用', fan: '风扇故障', psu: '电源故障', temp: '温度异常',
  port_err: '端口错包', optical: '光功率异常', bandwidth: '带宽跑满', duplex: '双工不匹配',
  broadcast: '广播风暴', stp: '生成树频繁变化'
};
function alertCatName(c) { return ALERT_CATEGORY_CN[c] || c || '-'; }
var globalAlertData = [];
App.on('alert', function (ev) {
  globalAlertData.forEach(function (a) { if (a.id === ev.id) { applyAlertPatch(a, ev.action); if (ev.action === 'ack' || ev.action === 'close') { a.acked_by = currentUser; a.acked_at = new Date().toLocaleString('sv-SE').slice(0, 19); } } });
  if (byId('alertModal').style.display === 'flex') { renderGlobalAlertTable(); }
});

const AC = {
  tab: 'now', now: null, hist: null, syslog: null, syslogData: null, syslogLoadedAt: 0, loadedAt: 0,
  h: { items: [], total: 0, stats: null, loading: false, sort: '-start', retention: 180 },
  prefs: prefs('alertCenterPrefs', { tab: 'now', nowGroup: '', histGroup: '', range: '7d' })
};

function acLevel(l) { return l === 'critical' ? raw('<span class="lv crit">🚨 致命</span>') : raw('<span class="lv warn">⚠️ 隐患</span>'); }
function acParseTime(s) { const t = s ? Date.parse(String(s).replace(' ', 'T')) : NaN; return isNaN(t) ? 0 : t / 1000; }

/* ================= 打开 / 关闭 / 页签 ================= */
// openAlertTable(force)  打开当前告警; openAlertCenter('hist', { device: ip }) 直接打开某台设备的历史
modalRegister('alertModal', closeAlertTable, { esc: () => { if (byId('acDrawer').classList.contains('open')) { acCloseDrawer(); return true; } return false; } });
function openAlertTable(force) { openAlertCenter(force === true ? AC.tab : AC.prefs.tab || 'now', null, force === true); }
function openAlertCenter(tab, filter, force) {
  byId('alertModal').style.display = 'flex';
  acInit();
  if (filter && filter.device) {
    msSetValues('acHDev', [filter.device]);
    msOptions('acHDev', acDeviceOptions());
    msSetValues('acHDev', [filter.device]);
  }
  if (filter) { AC.h.stats = null; }
  acSetTab(tab || 'now');
  if (force || !AC.loadedAt || Date.now() - AC.loadedAt > 20000) { acLoadNow(); } else { renderGlobalAlertTable(); }
  if (filter && AC.h.loading === false && !AC.h.stats) { acHistReload(); }
}
function closeAlertTable() {
  byId('alertModal').style.display = 'none';
  acCloseDrawer();
  fsExitWithin(byId('alertModal'));
}
function acSetTab(t) {
  if (t === 'syslog' && (!canView('flows') || (authenticated && !can('flows')))) { t = 'now'; }
  AC.tab = t; AC.prefs.tab = t; AC.prefs.save();
  document.querySelectorAll('#acTabs button').forEach((b) => b.classList.toggle('on', b.getAttribute('data-t') === t));
  byId('acNow').style.display = t === 'now' ? '' : 'none';
  byId('acSyslog').style.display = t === 'syslog' ? '' : 'none';
  byId('acHist').style.display = t === 'hist' || t === 'stat' ? '' : 'none';
  byId('acHist').classList.toggle('stat-mode', t === 'stat');
  acCloseDrawer();
  if (t === 'syslog' && (!AC.syslogLoadedAt || Date.now() - AC.syslogLoadedAt > 15000)) { acSyslogLoad(); }
  if ((t === 'hist' || t === 'stat') && !AC.h.stats && !AC.h.loading) { acHistReload(); }
  if (t === 'stat') { acRenderStats(); }
  if (AC.hist) { AC.hist.refresh(); }
  if (AC.now) { AC.now.refresh(); }
}

function acInit() {
  if (AC.now) { return; }
  byId('acTabs').addEventListener('click', (e) => { const b = e.target.closest('button[data-t]'); if (b) { acSetTab(b.getAttribute('data-t')); } });
  fsButton(byId('alertBox'), byId('acFs'));

  /* ---- 当前告警表 ---- */
  AC.now = new VTable(byId('galertTable'), {
    rowHeight: 50,
    columns: [
      { key: 'level', title: '级别', width: '96px', sort: (a) => (a.level === 'critical' ? 0 : 1), render: (a) => acLevel(a.level) },
      { key: 'dev', title: '设备', width: 'minmax(150px,1fr)', sort: (a) => a.device_name, render: (a) => html`<span class="lnk" data-act="dev">${a.device_name}</span><span class="sub2">${a.device_ip}</span>` },
      { key: 'cat', title: '类别 / 对象', width: 'minmax(130px,.9fr)', sort: (a) => alertCatName(a.category), render: (a) => html`${alertCatName(a.category)}<span class="sub2">${a.subject || ''}</span>` },
      { key: 'text', title: '内容', width: 'minmax(220px,2fr)', render: (a) => html`<span title="${a.text}">${a.text}</span>` },
      { key: 'dur', title: '已持续', width: '104px', sort: (a) => acParseTime(a.first_seen), render: (a) => html`${fmtDur(Date.now() / 1000 - acParseTime(a.first_seen))}<span class="sub2">自 ${a.first_seen ? a.first_seen.slice(5, 16) : '-'}</span>` },
      { key: 'last', title: '最近出现', width: '112px', sort: (a) => a.last_seen, render: (a) => html`${(a.last_seen || '').slice(5, 16)}` },
      { key: 'st', title: '状态', width: 'minmax(120px,.8fr)', sort: (a) => acNowState(a), render: acNowStateHTML },
      { key: 'op', title: '操作', width: '176px', render: acNowOps }
    ],
    sort: { key: 'level', asc: true },
    rowClass: (a) => (a.acked || a.closed ? 'ac-done' : a.recovered ? 'ac-rec' : a.level === 'critical' ? 'ac-crit' : 'ac-warn'),
    empty: '加载中…',
    onAction: (act, a) => {
      if (act === 'dev') { openDeviceAt(a.device_ip, 4); return; }
      if (act === 'hist') { acShowHistoryOf(a); return; }
      alertAction(a.id, act);
    }
  });
  byId('galertSearch').addEventListener('input', debounce(renderGlobalAlertTable, 120));
  byId('acNowGroup').value = AC.prefs.nowGroup;
  byId('acNowGroup').addEventListener('change', (e) => { AC.prefs.nowGroup = e.target.value; AC.prefs.save(); acApplyNowGroup(); });
  acApplyNowGroup(true);

  /* ---- 历史表 ---- */
  AC.hist = new VTable(byId('acHistTable'), {
    rowHeight: 50,
    columns: [
      { key: 'start', title: '开始时间', width: '124px', sort: (x) => x.start, render: (x) => html`${fmtTime(x.start)}<span class="sub2">${fmtAgo(x.start)}</span>` },
      { key: 'duration', title: '持续', width: '112px', sort: (x) => x.duration, render: (x) => (x.end ? html`${fmtDur(x.duration)}<span class="sub2">${fmtTime(x.end)} 恢复</span>` : html`<b class="ac-live">● ${fmtDur(x.duration)}</b><span class="sub2">仍在告警</span>`) },
      { key: 'level', title: '级别', width: '92px', sort: (x) => (x.level === 'critical' ? 0 : 1), render: (x) => acLevel(x.level) },
      { key: 'device', title: '设备', width: 'minmax(140px,1fr)', sort: (x) => x.device_name, render: (x) => html`<span class="lnk" data-act="dev">${x.device_name || x.device_ip}</span><span class="sub2">${x.device_ip}</span>` },
      { key: 'cat', title: '类别 / 对象', width: 'minmax(130px,.9fr)', render: (x) => html`${alertCatName(x.category)}<span class="sub2">${x.subject || ''}</span>` },
      { key: 'text', title: '内容', width: 'minmax(200px,2fr)', render: (x) => html`<span title="${x.text}">${x.text}</span>${x.note ? html`<span class="sub2">${x.note}</span>` : ''}` },
      { key: 'handle', title: '处理', width: 'minmax(110px,.8fr)', render: (x) => (x.acked_by ? html`<span class="lv info">${x.closed ? '已关闭' : '已确认'}</span><span class="sub2">${x.acked_by} · ${fmtTime(x.acked_at)}</span>` : raw('<span class="lv mute">未处理</span>')) },
      { key: 'flaps', title: '次数', width: '70px', align: 'center', sort: (x) => x.flaps, tip: '查询范围内同一个对象出现了几次, ≥3 次标为抖动', render: (x) => (x.flaps >= 3 ? html`<span class="lv warn" title="反复出现">↻ ${x.flaps}</span>` : String(x.flaps || 1)) }
    ],
    sort: { key: 'start', asc: false },
    rowClass: (x) => 'click' + (x.end ? '' : ' ac-crit') + (AC.h.sel === x.id ? ' sel' : ''),
    empty: '这个时间范围内没有告警记录 🎉',
    onSort: (s) => {
      const m = { start: 'start', duration: 'duration', level: 'level', device: 'device', flaps: 'flaps' };
      AC.h.sort = (s.asc ? '' : '-') + (m[s.key] || 'start');
      acHistReload();
    },
    onRow: (x) => acOpenIncident(x.id),
    onAction: (act, x) => { if (act === 'dev') { openDeviceAt(x.device_ip, 4); } },
    onEnd: () => acHistMore()
  });

  /* ---- 全平台 Syslog 事件 ---- */
  AC.syslog = new VTable(byId('acSyslogTable'), {
    rowHeight: 46,
    columns: [
      { key: 'time', title: '收到时间', width: '142px', sort: (x) => acParseTime(x.time), render: (x) => html`${x.time || '-'}<span class="sub2">${fmtAgo(acParseTime(x.time))}</span>` },
      { key: 'severity', title: '级别', width: '92px', sort: (x) => x.severity, render: (x) => acSyslogSeverity(x.severity) },
      { key: 'device', title: '设备', width: 'minmax(145px,.8fr)', sort: (x) => x.device_name, render: (x) => html`<span class="lnk" data-act="dev">${x.device_name || x.device_ip}</span><span class="sub2">${x.device_ip}</span>` },
      { key: 'facility', title: 'Facility', width: '94px', sort: (x) => x.facility, render: (x) => acSyslogFacility(x.facility) },
      { key: 'raw', title: '日志内容', width: 'minmax(260px,2fr)', render: (x) => html`<span title="${x.raw}">${x.raw}</span>` }
    ],
    sort: { key: 'time', asc: false },
    empty: '当前没有 Syslog 日志',
    onAction: (act, x) => { if (act === 'dev') { openDeviceAt(x.device_ip, 6); } }
  });
  byId('acSyslogSearch').addEventListener('input', debounce(acSyslogRender, 120));
  byId('acSyslogSeverity').addEventListener('change', acSyslogRender);
  msOptions('acSyslogDevice', []);
  byId('acHistGroup').value = AC.prefs.histGroup;
  byId('acHistGroup').addEventListener('change', (e) => { AC.prefs.histGroup = e.target.value; AC.prefs.save(); acApplyHistGroup(); });
  acApplyHistGroup(true);
  byId('acRange').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-r]');
    if (!b) { return; }
    AC.prefs.range = b.getAttribute('data-r'); AC.prefs.save();
    acSyncRange();
    acHistReload();
  });
  ['acFrom', 'acTo'].forEach((id) => byId(id).addEventListener('change', acHistReload));
  byId('acHQ').addEventListener('input', debounce(acHistReload, 250));
  msOptions('acHLevel', [{ v: 'critical', t: '🚨 致命故障' }, { v: 'warning', t: '⚠️ 风险隐患' }]);
  msOptions('acHStatus', [{ v: 'active', t: '仍在告警' }, { v: 'recovered', t: '已恢复' }, { v: 'handled', t: '已确认 / 关闭' }, { v: 'unhandled', t: '没人处理' }]);
  msOptions('acHCat', Object.keys(ALERT_CATEGORY_CN).map((c) => ({ v: c, t: ALERT_CATEGORY_CN[c] })));
  acSyncRange();
  byId('acStatBox').addEventListener('click', (e) => {
    const el = e.target.closest('[data-filter]');
    if (!el) { return; }
    const [k, v] = el.getAttribute('data-filter').split('=');
    if (k === 'device') { msSetValues('acHDev', [v]); }
    if (k === 'category') { msSetValues('acHCat', [v]); }
    if (k === 'q') { byId('acHQ').value = v; }
    acSetTab('hist');
    acHistReload();
  });
}

/* ================= 当前告警 ================= */
function acNowState(a) { return a.acked || a.closed ? 'done' : a.recovered ? 'recovered' : 'open'; }
function acNowStateHTML(a) {
  if (a.acked || a.closed) { return html`<span class="lv ok">✅ ${a.closed ? '已关闭' : '已确认'}</span><span class="sub2">${a.acked_by || '-'} · ${(a.acked_at || '').slice(5, 16)}</span>`; }
  if (a.recovered) { return html`<span class="lv info">🟢 已恢复</span><span class="sub2">${(a.recovered_at || '').slice(5, 16)} · 24h 后归档</span>`; }
  return raw('<span class="lv crit">未处理</span>');
}
function acNowOps(a) {
  const hist = '<button class="abtn" data-act="hist" title="这个对象的历史告警">历史</button>';
  if (a.acked || a.closed) { return raw('<div class="alert-actions"><button class="abtn" data-act="unack">重新打开</button>' + hist + '</div>'); }
  if (a.recovered) { return raw('<div class="alert-actions"><button class="abtn" data-act="ack">知道了</button>' + hist + '</div>'); }
  return raw('<div class="alert-actions"><button class="abtn" data-act="ack">确认</button><button class="abtn" data-act="close">关闭</button>' + hist + '</div>');
}
function acApplyNowGroup(quiet) {
  const k = AC.prefs.nowGroup;
  const G = {
    device: { by: (a) => a.device_name + ' ' + a.device_ip },
    category: { by: (a) => alertCatName(a.category) },
    level: { by: (a) => (a.level === 'critical' ? '🚨 致命故障' : '⚠️ 风险隐患'), order: (x, y) => (x < y ? 1 : -1) },
    state: { by: (a) => ({ open: '未处理', recovered: '已恢复未确认', done: '已处理' })[acNowState(a)], order: (x, y) => ['未处理', '已恢复未确认', '已处理'].indexOf(x) - ['未处理', '已恢复未确认', '已处理'].indexOf(y) }
  }[k];
  const g = G ? {
    by: G.by, order: G.order,
    label: (key, rows) => {
      let crit = 0, open = 0;
      rows.forEach((a) => { if (a.level === 'critical') { crit++; } if (acNowState(a) === 'open') { open++; } });
      return vtGroupLabel(key, rows, html`${crit ? html`<span style="color:var(--bad)">致命 ${crit}</span>` : ''}<span>未处理 ${open}</span>`);
    }
  } : null;
  if (quiet) { AC.now.group = g; } else { AC.now.setGroup(g); }
}
function acLoadNow() {
  byId('acNowStat').innerHTML = '<span class="spin">⟳</span> 正在加载…';
  return latest('acNow', pub('/api/alerts')).then((d) => {
    globalAlertData = d.entries || [];
    AC.loadedAt = Date.now();
    populateGalertFilters();
    AC.now.opt.empty = '🎉 没有符合条件的告警';
    renderGlobalAlertTable();
  }).catch((e) => { byId('acNowStat').innerHTML = '<span style="color:var(--bad)">加载失败: ' + esc(String(e)) + '</span>'; });
}

/* ================= 全平台 Syslog 事件 ================= */
const AC_SYSLOG_LEVELS = ['紧急', '警报', '严重', '错误', '警告', '通知', '信息', '调试'];
const AC_SYSLOG_FACILITIES = ['kern', 'user', 'mail', 'daemon', 'auth', 'syslog', 'lpr', 'news', 'uucp', 'cron', 'authpriv', 'ftp', 'ntp', 'audit', 'alert', 'clock', 'local0', 'local1', 'local2', 'local3', 'local4', 'local5', 'local6', 'local7'];
function acSyslogSeverity(n) {
  n = Number(n);
  const label = AC_SYSLOG_LEVELS[n] || '未知';
  const cls = n <= 3 ? 'crit' : (n === 4 ? 'warn' : (n <= 6 ? 'info' : 'mute'));
  return raw('<span class="lv ' + cls + '">' + label + '</span>');
}
function acSyslogFacility(n) {
  n = Number(n);
  return AC_SYSLOG_FACILITIES[n] || ('facility ' + n);
}
function acSyslogLoad(force) {
  if (!AC.syslog) { return Promise.resolve(); }
  byId('acSyslogStat').innerHTML = '<span class="spin">⟳</span> 正在汇总设备日志…';
  return pub('/api/syslog?limit=10000').then((d) => {
    AC.syslogData = d;
    AC.syslogLoadedAt = Date.now();
    const devices = d.devices || [];
    msOptions('acSyslogDevice', devices.map((x) => ({ v: x.ip, t: (x.disabled ? '已停用 · ' : '') + (x.name || x.ip), s: x.count + ' 条' })));
    byId('acSyslogHint').innerText = (d.enabled ? ('接收端口 UDP ' + d.port + ' 已配置。') : '当前未配置 Syslog 接收端口。') +
      '日志只保留在运行内存中，平台重启后缓存清空；单设备最多保留 ' + (d.per_device_cap || 0) + ' 条。严重级别按 Syslog 标准 0(紧急) 至 7(调试)。';
    acSyslogRender();
  }).catch((e) => {
    byId('acSyslogStat').innerHTML = '<span style="color:var(--bad)">加载失败：' + esc(String(e)) + '</span>';
  });
}
function acSyslogRender() {
  if (!AC.syslog || !AC.syslogData) { return; }
  const d = AC.syslogData;
  const kw = byId('acSyslogSearch').value.trim().toLowerCase();
  const devF = msSet('acSyslogDevice');
  const sev = byId('acSyslogSeverity').value;
  const rows = (d.lines || []).filter((x) => {
    if (devF && !devF[x.device_ip]) { return false; }
    if (sev !== '' && Number(sev) !== Number(x.severity)) { return false; }
    if (!kw) { return true; }
    return (x.device_name + ' ' + x.device_ip + ' ' + x.raw).toLowerCase().indexOf(kw) >= 0;
  });
  AC.syslog.setRows(rows);
  byId('acSyslogN').innerText = d.total ? String(d.total) : '';
  const devices = d.devices || [];
  const withLogs = devices.filter((x) => x.count > 0).length;
  byId('acSyslogStat').innerHTML = '保留 <b>' + (d.retained || 0).toLocaleString() + '</b> 条 · 当前加载 <b>' + (d.returned || 0).toLocaleString() + '</b> 条 · 有日志设备 <b>' + withLogs + ' / ' + devices.length + '</b> 台（已登记设备）' +
    (rows.length !== (d.lines || []).length ? ' · 当前筛选 <b>' + rows.length + '</b> 条' : '') + (d.total > d.returned ? ' · 命中共 ' + d.total.toLocaleString() + ' 条，列表受加载上限限制' : '');
}
function acSyslogClear() {
  msClearAll(['acSyslogDevice']);
  byId('acSyslogSearch').value = '';
  byId('acSyslogSeverity').value = '';
  acSyslogRender();
}

function populateGalertFilters() {
  const devs = {}, cats = {};
  globalAlertData.forEach((a) => {
    if (a.device_ip) { devs[a.device_ip] = a.device_name || a.device_ip; }
    if (a.category) { cats[a.category] = (cats[a.category] || 0) + 1; }
  });
  msOptions('galertDeviceFilter', Object.keys(devs).sort((x, y) => cmpNatural(devs[x], devs[y])).map((ip) => ({ v: ip, t: devs[ip], s: ip })));
  msOptions('galertCategoryFilter', Object.keys(cats).sort((x, y) => cmpNatural(alertCatName(x), alertCatName(y))).map((c) => ({ v: c, t: alertCatName(c), s: cats[c] + ' 条' })));
}
function acNowFiltered() {
  const kw = byId('galertSearch').value.trim().toLowerCase();
  const devF = msSet('galertDeviceFilter'), levelF = msSet('galertLevelFilter'), catF = msSet('galertCategoryFilter'), statusF = msSet('galertStatusFilter');
  return globalAlertData.filter((a) => {
    if (devF && !devF[a.device_ip]) { return false; }
    if (levelF && !levelF[a.level]) { return false; }
    if (catF && !catF[a.category]) { return false; }
    if (statusF) { const st = acNowState(a) === 'done' ? 'done' : acNowState(a); if (!statusF[st]) { return false; } }
    if (!kw) { return true; }
    return (a.device_name + ' ' + a.device_ip + ' ' + alertCatName(a.category) + ' ' + (a.subject || '') + ' ' + a.text).toLowerCase().indexOf(kw) >= 0;
  });
}
function renderGlobalAlertTable() {
  if (!AC.now) { return; }
  const rows = acNowFiltered();
  AC.now.setRows(rows);
  let open = 0, crit = 0, rec = 0;
  globalAlertData.forEach((a) => { const s = acNowState(a); if (s === 'open') { open++; if (a.level === 'critical') { crit++; } } if (s === 'recovered') { rec++; } });
  byId('acNowN').innerText = open ? String(open) : '';
  byId('acNowStat').innerHTML = '全网 <b>' + globalAlertData.length + '</b> 条 · 未处理 <b style="color:var(--bad)">' + open + '</b>' + (crit ? ' (致命 ' + crit + ')' : '') +
    ' · 已恢复未确认 <b>' + rec + '</b>' + (rows.length !== globalAlertData.length ? ' · 当前筛选 <b>' + rows.length + '</b> 条' : '');
  const pending = rows.filter((a) => acNowState(a) !== 'done').length;
  byId('acAckAll').disabled = !pending;
  byId('acAckAll').innerText = '✔ 全部确认' + (pending ? ' (' + pending + ')' : '');
}
function acNowClear() {
  msClearAll(['galertDeviceFilter', 'galertLevelFilter', 'galertCategoryFilter', 'galertStatusFilter']);
  byId('galertSearch').value = '';
  renderGlobalAlertTable();
}
function queueGlobalAlertRender() { renderGlobalAlertTable(); }
// 批量确认: 当前筛选出来的未处理 / 已恢复未确认告警
function acAckFiltered() {
  const list = acNowFiltered().filter((a) => acNowState(a) !== 'done');
  if (!list.length || !confirm('确认当前筛选出来的 ' + list.length + ' 条告警?\n(确认 = 已知悉, 故障没恢复的仍会显示)')) { return; }
  let chain = Promise.resolve(), ok = 0;
  list.forEach((a) => {
    chain = chain.then(() => authedApi('/api/alerts/action', { id: a.id, action: 'ack' }).then(() => { ok++; App.emit('alert', { id: a.id, action: 'ack' }); }));
  });
  chain.catch((e) => alert('确认中断: ' + e)).then(() => { uiToast('已确认 ' + ok + ' 条告警', 'ok'); });
}
function acShowHistoryOf(a) {
  acSetTab('hist');
  msSetValues('acHDev', [a.device_ip]);
  msSetValues('acHCat', [a.category]);
  byId('acHQ').value = a.subject || '';
  AC.prefs.range = '30d'; acSyncRange();
  acHistReload();
}

/* ================= 历史记录 ================= */
function acSyncRange() {
  const r = AC.prefs.range;
  document.querySelectorAll('#acRange button').forEach((b) => b.classList.toggle('on', b.getAttribute('data-r') === r));
  byId('acCustom').style.display = r === 'custom' ? '' : 'none';
  if (r === 'custom' && !byId('acFrom').value) {
    byId('acFrom').value = fmtDay(new Date(Date.now() - 7 * 86400000));
    byId('acTo').value = fmtDay(new Date());
  }
}
function acRangeParams() {
  const now = Math.floor(Date.now() / 1000), r = AC.prefs.range;
  if (r === 'custom') { return { from: byId('acFrom').value || '', to: byId('acTo').value || '' }; }
  if (r === 'today') { const d = new Date(); d.setHours(0, 0, 0, 0); return { from: Math.floor(d / 1000) }; }
  const sec = { '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400, '90d': 90 * 86400, all: 3650 * 86400 }[r] || 7 * 86400;
  return { from: now - sec };
}
function acHistQuery(offset, limit) {
  const p = acRangeParams();
  const set = (id) => { const s = msSet(id); return s ? Object.keys(s).join(',') : ''; };
  const q = new URLSearchParams();
  q.set('from', p.from);
  if (p.to) { q.set('to', p.to); }
  [['device', 'acHDev'], ['level', 'acHLevel'], ['category', 'acHCat'], ['status', 'acHStatus']].forEach(([k, id]) => { const v = set(id); if (v) { q.set(k, v); } });
  const kw = byId('acHQ').value.trim();
  if (kw) { q.set('q', kw); }
  q.set('sort', AC.h.sort);
  if (offset !== undefined) { q.set('offset', offset); q.set('limit', limit); }
  return q.toString();
}
function acHistReload() {
  if (!AC.hist) { return Promise.resolve(); }
  AC.h.loading = true;
  byId('acHistStat').innerHTML = '<span class="spin">⟳</span> 正在查询…';
  return latest('acHist', pub('/api/alerts/history?' + acHistQuery(0, 500))).then((d) => {
    AC.h.loading = false;
    AC.h.items = d.items || [];
    AC.h.total = d.total || 0;
    AC.h.stats = d.stats || null;
    AC.h.retention = d.retention_days || AC.h.retention;
    AC.hist.setRows(AC.h.items);
    AC.hist.scrollToTop();
    acHistStat();
    acFillHistDevices();
    if (AC.tab === 'stat') { acRenderStats(); }
  }).catch((e) => { AC.h.loading = false; byId('acHistStat').innerHTML = '<span style="color:var(--bad)">查询失败: ' + esc(String(e)) + '</span>'; });
}
function acHistMore() {
  if (AC.h.loading || AC.h.items.length >= AC.h.total) { return Promise.resolve(); }
  AC.h.loading = true;
  return pub('/api/alerts/history?' + acHistQuery(AC.h.items.length, 1000)).then((d) => {
    AC.h.loading = false;
    AC.h.items = AC.h.items.concat(d.items || []);
    const keep = AC.hist.body.scrollTop;
    AC.hist.setRows(AC.h.items);
    AC.hist.body.scrollTop = keep;
    acHistStat();
  }).catch(() => { AC.h.loading = false; });
}
function acHistStat() {
  const s = AC.h.stats || {};
  byId('acHistStat').innerHTML = '共 <b>' + AC.h.total + '</b> 条' + (AC.h.items.length < AC.h.total ? ' (已加载 ' + AC.h.items.length + ', 滚到底自动加载更多)' : '') +
    ' · 致命 <b style="color:var(--bad)">' + (s.critical || 0) + '</b> · 仍在告警 <b>' + (s.active || 0) + '</b> · 平均恢复 <b>' + (s.mttr ? fmtDur(s.mttr) : '-') + '</b>' +
    ' · 已处理 <b>' + (s.handled || 0) + '</b> <span class="muted">· 历史保留 ' + AC.h.retention + ' 天(设置 → 告警 里可改)</span>';
}
function acDeviceOptions() {
  const m = {};
  (summaryData || []).forEach((d) => { m[d.ip] = d.sys_name || d.ip; });
  ((AC.h.stats && AC.h.stats.devices) || []).forEach((c) => { if (!m[c.key]) { m[c.key] = c.name || c.key; } });
  return Object.keys(m).sort((a, b) => cmpNatural(m[a], m[b])).map((ip) => ({ v: ip, t: m[ip], s: ip }));
}
function acFillHistDevices() { msOptions('acHDev', acDeviceOptions()); }
function acHistClear() {
  msClearAll(['acHDev', 'acHLevel', 'acHCat', 'acHStatus']);
  byId('acHQ').value = '';
  acHistReload();
}
function acApplyHistGroup(quiet) {
  const k = AC.prefs.histGroup;
  const G = {
    day: { by: (x) => fmtDay(new Date(x.start * 1000)), order: (a, b) => (a < b ? 1 : -1) },
    device: { by: (x) => (x.device_name || '') + ' ' + x.device_ip },
    category: { by: (x) => alertCatName(x.category) },
    alert: { by: (x) => (x.device_name || x.device_ip) + ' · ' + alertCatName(x.category) + (x.subject ? ' · ' + x.subject : ''), order: null }
  }[k];
  const g = G ? {
    by: G.by, order: G.order,
    label: (key, rows) => {
      let crit = 0, dur = 0;
      rows.forEach((x) => { if (x.level === 'critical') { crit++; } dur += x.duration || 0; });
      return vtGroupLabel(key, rows, html`${crit ? html`<span style="color:var(--bad)">致命 ${crit}</span>` : ''}<span>累计 ${fmtDur(dur)}</span>`);
    }
  } : null;
  if (quiet) { AC.hist.group = g; } else { AC.hist.setGroup(g); }
}
function acHistExport() {
  const a = document.createElement('a');
  a.href = '/api/alerts/history/export?' + acHistQuery();
  document.body.appendChild(a); a.click(); a.remove();
}

/* ---------- 单条记录详情(右侧抽屉) ---------- */
const AC_EV = { open: ['告警出现', '🔴'], level: ['级别升高', '⬆️'], ack: ['确认', '✔'], close: ['关闭', '🔕'], unack: ['取消确认', '↩'], reopen: ['重新打开', '↩'], recover: ['故障恢复', '🟢'], removed: ['设备被删除', '🗑'] };
function acOpenIncident(id) {
  AC.h.sel = id; AC.hist.refresh();
  const dr = byId('acDrawer');
  dr.innerHTML = '<div class="drawer-h"><h3>加载中…</h3><button class="x" onclick="acCloseDrawer()">×</button></div>';
  dr.classList.add('open');
  pub('/api/alerts/history/detail?id=' + encodeURIComponent(id)).then((d) => {
    const x = d.incident, dur = (x.end || Date.now() / 1000) - x.start;
    const occ = d.occurrences || [];
    dr.innerHTML = html`<div class="drawer-h"><h3>${acLevel(x.level)} ${alertCatName(x.category)}${x.subject ? ' · ' + x.subject : ''}<div class="muted" style="font-size:12px;font-weight:400;margin-top:3px;">${x.device_name} (${x.device_ip})</div></h3>
      <button class="x" onclick="acCloseDrawer()">×</button></div>
      <div class="drawer-b"><div class="ac-text">${x.text}</div>
      <div class="kvs"><span>开始</span><span>${fmtTime(x.start, true)}</span><span>恢复</span><span>${x.end ? fmtTime(x.end, true) : raw('<b class="ac-live">● 仍在告警</b>')}</span>
      <span>持续</span><span>${fmtDur(dur)}</span><span>处理</span><span>${x.acked_by ? (x.closed ? '已关闭' : '已确认') + ' · ' + x.acked_by + ' · ' + fmtTime(x.acked_at, true) : '没人处理'}</span>
      ${x.acked_at ? html`<span>响应用时</span><span>${fmtDur(x.acked_at - x.start)}</span>` : ''}${x.note ? html`<span>备注</span><span>${x.note}</span>` : ''}</div>
      <h4 class="ac-h4">处理过程</h4><div class="timeline">${(d.events || []).map((e) => html`<div class="tl-i ${e.type}"><div class="tl-t">${fmtTime(e.ts, true)}</div>
        <div class="tl-x"><b>${(AC_EV[e.type] || [e.type])[0]}</b>${e.user ? ' · ' + e.user : ''}${e.text ? html`<div class="muted">${e.text}</div>` : ''}</div></div>`)}</div>
      <h4 class="ac-h4">同一对象的历次告警 <small class="muted">(${occ.length} 次)</small></h4>
      ${acOccChart(occ)}
      <div class="ac-occ">${occ.map((o) => html`<div class="${o.id === x.id ? 'cur' : ''}" data-occ="${o.id}"><span>${fmtTime(o.start)}</span><span>${o.end ? fmtDur(o.end - o.start) : '进行中'}</span><span>${o.acked_by || '-'}</span></div>`)}</div>
      <div class="btns" style="margin-top:12px;display:flex;gap:8px;flex-wrap:wrap;"><button class="btn sm pri" onclick="openDeviceAt('${x.device_ip}',4)">📋 设备详情</button>
      <button class="btn sm" onclick="acFilterAlert('${x.device_ip}','${x.category}')">🔎 只看这台设备的这类告警</button></div></div>`.toString();
    dr.querySelectorAll('[data-occ]').forEach((el) => el.addEventListener('click', () => acOpenIncident(el.getAttribute('data-occ'))));
  }).catch((e) => { dr.innerHTML = '<div class="drawer-h"><h3>加载失败</h3><button class="x" onclick="acCloseDrawer()">×</button></div><div class="drawer-b">' + esc(String(e)) + '</div>'; });
}
function acCloseDrawer() {
  const dr = byId('acDrawer');
  if (dr) { dr.classList.remove('open'); }
  if (AC.h.sel) { AC.h.sel = null; if (AC.hist) { AC.hist.refresh(); } }
}
function acFilterAlert(ip, cat) { msSetValues('acHDev', [ip]); msSetValues('acHCat', [cat]); acCloseDrawer(); acHistReload(); }
// 历次告警的时间轴(最近 30 次): 每次一段, 长度 = 持续时长
function acOccChart(occ) {
  if (occ.length < 2) { return ''; }
  const t0 = Math.min(...occ.map((o) => o.start)), t1 = Math.max(...occ.map((o) => o.end || Date.now() / 1000)), span = Math.max(1, t1 - t0);
  return raw('<svg class="chart-svg" viewBox="0 0 400 34" preserveAspectRatio="none" style="height:34px;margin:4px 0 8px;"><rect x="0" y="15" width="400" height="4" rx="2" fill="var(--track)"/>' +
    occ.map((o) => { const x = (o.start - t0) / span * 396, w = Math.max(3, ((o.end || Date.now() / 1000) - o.start) / span * 396); return '<rect x="' + x.toFixed(1) + '" y="8" width="' + w.toFixed(1) + '" height="18" rx="3" fill="' + (o.level === 'critical' ? 'var(--bad)' : 'var(--warn)') + '" opacity=".8"/>'; }).join('') + '</svg>');
}

/* ================= 统计分析 ================= */
function acRenderStats() {
  const box = byId('acStatBox'), s = AC.h.stats;
  if (!s) { box.innerHTML = '<div class="empty">' + (AC.h.loading ? '加载中…' : '没有数据') + '</div>'; return; }
  const pct = (a, b) => (b ? Math.round(a / b * 100) + '%' : '-');
  const kpi = (t, v, cls, sub) => '<div class="kpi ' + (cls || '') + '"><span>' + t + '</span><b>' + v + '</b>' + (sub ? '<small>' + sub + '</small>' : '') + '</div>';
  let h = '<div class="kpis">' + kpi('告警总数', s.total) + kpi('致命故障', s.critical, s.critical ? 'bad' : '') + kpi('风险隐患', s.warning, 'warn') +
    kpi('仍在告警', s.active, s.active ? 'bad' : 'good') + kpi('处理率', pct(s.handled, s.total), 'acc', '已确认 / 关闭 ' + s.handled + ' 条') +
    kpi('平均恢复 MTTR', s.mttr ? fmtDur(s.mttr) : '-', '', s.mttr_p50 ? '中位数 ' + fmtDur(s.mttr_p50) : '') + kpi('平均响应 MTTA', s.mtta ? fmtDur(s.mtta) : '-', '', '出现到有人确认') + '</div>';
  h += '<div class="ac-grid"><div class="panel ac-wide"><h4>每天新增告警 <small>红 = 致命, 黄 = 隐患</small></h4>' + acDayChart(s.days || []) + '</div>' +
    '<div class="panel"><h4>一天里的时段分布 <small>按开始时间</small></h4>' + acHourChart(s.hours || []) + '</div>' +
    '<div class="panel"><h4>按类别 <small>点击只看这一类</small></h4>' + acHBars((s.categories || []).map((c) => ({ t: alertCatName(c.key), n: c.count, crit: c.critical, f: 'category=' + c.key }))) + '</div>' +
    '<div class="panel"><h4>告警最多的设备 <small>点击只看这台</small></h4>' + acHBars((s.devices || []).map((c) => ({ t: c.name || c.key, n: c.count, crit: c.critical, f: 'device=' + c.key, sub: '累计 ' + fmtDur(c.duration) }))) + '</div>' +
    '<div class="panel"><h4>反复出现(≥3 次)的对象 <small>多半是抖动的端口 / 临界的阈值</small></h4>' +
    ((s.flapping || []).length ? acHBars(s.flapping.map((c) => ({ t: c.name, n: c.count, crit: c.critical, f: 'q=' + (c.name.split(' · ')[1] || '') }))) : '<div class="muted" style="padding:10px 0;">没有反复出现的告警 👍</div>') + '</div></div>';
  box.innerHTML = h;
}
function acHBars(list) {
  if (!list.length) { return '<div class="muted" style="padding:10px 0;">没有数据</div>'; }
  const max = Math.max(...list.map((x) => x.n));
  return list.map((x) => '<div class="hbar ac-click" data-filter="' + esc(x.f) + '" title="' + esc(x.t + (x.sub ? ' · ' + x.sub : '')) + '"><span>' + esc(x.t) + '</span><span class="tr">' +
    '<i style="width:' + (x.crit / max * 100).toFixed(1) + '%;background:var(--bad)"></i><i style="width:' + ((x.n - x.crit) / max * 100).toFixed(1) + '%;background:var(--warn)"></i></span><b>' + x.n + '</b></div>').join('');
}
function acDayChart(days) {
  if (!days.length) { return '<div class="muted">没有数据</div>'; }
  const W = Math.max(480, (byId('acStatBox').clientWidth || 900) - 40), H = 190, P = { l: 34, r: 8, t: 10, b: 24 }, n = days.length;
  const max = Math.max(1, ...days.map((d) => d.critical + d.warning));
  const step = Math.pow(10, Math.floor(Math.log10(max))) * (max / Math.pow(10, Math.floor(Math.log10(max))) > 5 ? 2 : 1);
  const top = Math.ceil(max / step) * step, cw = (W - P.l - P.r) / n, bw = Math.max(2, Math.min(28, cw * 0.7));
  const y = (v) => P.t + (H - P.t - P.b) * (1 - v / top);
  let g = '';
  for (let v = 0; v <= top; v += step) { g += '<line class="grid" x1="' + P.l + '" x2="' + (W - P.r) + '" y1="' + y(v).toFixed(1) + '" y2="' + y(v).toFixed(1) + '"/><text x="' + (P.l - 5) + '" y="' + (y(v) + 3.5).toFixed(1) + '" text-anchor="end">' + v + '</text>'; }
  const every = Math.ceil(n / 12);
  days.forEach((d, i) => {
    const x = P.l + i * cw + (cw - bw) / 2, yc = y(d.critical), yw = y(d.critical + d.warning);
    g += '<g><title>' + d.day + ': 致命 ' + d.critical + ', 隐患 ' + d.warning + '</title>' +
      '<rect x="' + x.toFixed(1) + '" y="' + yc.toFixed(1) + '" width="' + bw.toFixed(1) + '" height="' + (y(0) - yc).toFixed(1) + '" fill="var(--bad)" rx="2"/>' +
      '<rect x="' + x.toFixed(1) + '" y="' + yw.toFixed(1) + '" width="' + bw.toFixed(1) + '" height="' + (yc - yw).toFixed(1) + '" fill="var(--warn)" rx="2"/></g>';
    if (i % every === 0) { g += '<text x="' + (x + bw / 2).toFixed(1) + '" y="' + (H - 7) + '" text-anchor="middle">' + d.day.slice(5) + '</text>'; }
  });
  return '<svg class="chart-svg" viewBox="0 0 ' + W + ' ' + H + '" style="height:' + H + 'px">' + g + '</svg>';
}
function acHourChart(hours) {
  const box = byId('acStatBox').clientWidth || 1200, cols = box > 1480 ? 4 : box > 1100 ? 3 : box > 740 ? 2 : 1;
  const W = Math.max(260, box / cols - 44), H = 150, max = Math.max(1, ...hours);
  let g = '';
  hours.forEach((v, i) => {
    const bw = W / 24, h = (H - 24) * v / max, x = i * bw;
    const hot = v / max;
    g += '<g><title>' + i + ':00 - ' + (i + 1) + ':00  ' + v + ' 条</title><rect x="' + (x + 2).toFixed(1) + '" y="' + (H - 20 - h).toFixed(1) + '" width="' + (bw - 4).toFixed(1) + '" height="' + Math.max(1, h).toFixed(1) +
      '" rx="2" fill="var(--accent)" opacity="' + (0.3 + hot * 0.7).toFixed(2) + '"/></g>';
    if (i % 3 === 0) { g += '<text x="' + (x + bw / 2).toFixed(1) + '" y="' + (H - 6) + '" text-anchor="middle">' + i + '</text>'; }
  });
  return '<svg class="chart-svg" viewBox="0 0 ' + W + ' ' + H + '" style="height:' + H + 'px">' + g + '</svg>';
}

window.addEventListener('resize', debounce(() => { if (AC.tab === 'stat' && byId('alertModal').style.display === 'flex') { acRenderStats(); } }, 200));
