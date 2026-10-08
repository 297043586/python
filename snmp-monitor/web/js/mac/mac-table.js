/*
 * 全网 MAC 总表(v13 重写)。
 *
 * 以前两万条记录一次性拼成 <tr> 塞进表格, 打开要卡好几秒、打字搜索也卡。现在:
 *   - 后端按巡检轮次缓存汇总结果, 用列式紧凑格式返回(体积减一半以上);
 *   - 前端用 VTable 虚拟滚动, 只画看得见的几十行, 数据再多也流畅;
 *   - 每行的搜索串提前算好, 搜索 / 筛选只是一次数组过滤;
 *   - MAC 任意写法都能搜: 00:1A:2B / 001a.2b3c / 00-1a-2b-3c 都能命中;
 *   - 可以按 设备 / 设备端口 / VLAN / 终端网段 / 类型 分组折叠, 导出当前筛选结果。
 */
var globalMACData = [];
const GMAC = { t: null, loadedAt: 0, prefs: prefs('gmacPrefs', { group: '', fold: [] }) };
const GMAC_UP = 1, GMAC_AMB = 2, GMAC_LOCAL = 4, GMAC_FLAP = 8;

function openMACTable(force) {
  byId('macModal').style.display = 'flex';
  if (!GMAC.t) {
    GMAC.t = new VTable(byId('gmacTable'), {
      rowHeight: 46,
      columns: [
        { key: 'mac', title: 'MAC 地址', width: 'minmax(170px,1.1fr)', sort: (r) => r.mac, render: (r) => html`<code>${r.mac}</code>${r.flags & GMAC_LOCAL ? raw(' <span class="lv mute" title="本地管理地址: 手机 / 电脑的隐私随机 MAC、虚拟机等">随机</span>') : ''}` },
        { key: 'ip', title: '终端 IP', width: 'minmax(120px,.8fr)', sort: (r) => (r.ip ? ipSortKey(r.ip) : '~'), render: (r) => (r.ip ? html`<b>${r.ip}</b>` : raw('<span class="muted">-</span>')) },
        { key: 'dev', title: '所在设备', width: 'minmax(170px,1.2fr)', sort: (r) => r.devName, render: (r) => html`<span class="lnk" data-act="dev">${r.devName}</span><span class="sub2">${r.devIP}</span>` },
        { key: 'port', title: '端口', width: 'minmax(150px,1fr)', sort: (r) => r.devName + ' ' + r.port, render: (r) => html`<span class="lnk" data-act="port" title="看这个口上的全部终端">${r.port}</span>` },
        { key: 'vlan', title: 'VLAN', width: '70px', sort: (r) => +r.vlan || 0, render: (r) => r.vlan || '-' },
        { key: 'kind', title: '类型', width: 'minmax(150px,1fr)', sort: (r) => r.flags & 3, render: gmacKindHTML },
        { key: 'loc', title: '原始出现位置', width: 'minmax(140px,1.2fr)', render: (r) => html`<span class="muted" title="${r.loc}">${r.loc || '-'}</span>` }
      ],
      sort: { key: 'mac', asc: true },
      empty: '加载中…',
      collapsed: GMAC.prefs.fold,
      onCollapse: (keys) => { GMAC.prefs.fold = keys; GMAC.prefs.save(); },
      onAction: (act, r) => {
        if (act === 'port') { openDeviceAt(r.devIP, 2, r.port); } else { openModal(r.devIP); }
      }
    });
    byId('gmacSearch').addEventListener('input', debounce(renderGlobalMACTable, 120));
    byId('gmacGroup').value = GMAC.prefs.group;
    byId('gmacGroup').addEventListener('change', (e) => { GMAC.prefs.group = e.target.value; GMAC.prefs.save(); gmacApplyGroup(); });
    byId('gmacFold').addEventListener('click', () => { GMAC.t.collapseAll(!GMAC.t.collapsed.size); });
    fsButton(byId('macBox'), byId('gmacFs'));
    msOptions('gmacKindFilter', [{ v: 'access', t: '接入口' }, { v: 'uplink', t: '上联/级联口' }, { v: 'ambiguous', t: '有歧义' },
      { v: 'flap', t: '⚠ 跨设备漂移(疑似环路)' }, { v: 'transit', t: '有"路过"记录' }, { v: 'local', t: '随机 / 本地管理 MAC' }, { v: 'noip', t: '没有对应 IP' }]);
    gmacApplyGroup(true);
  }
  // 30 秒内重新打开直接用上次的数据(巡检间隔通常更长)
  if (!force && globalMACData.length && Date.now() - GMAC.loadedAt < 30000) { renderGlobalMACTable(); return; }
  byId('gmacStat').innerHTML = '<span class="spin">⟳</span> 正在加载…';
  latest('gmac', pub('/api/mactable?fmt=compact')).then((d) => {
    const devs = d.devices || [];
    globalMACData = (d.rows || []).map((r) => {
      const dv = devs[r[1]] || ['', ''];
      const row = { mac: r[0], devIP: dv[0], devName: dv[1] || dv[0], ip: r[2] || '', vlan: r[3] || '', port: r[4] || '', flags: r[5] | 0, transit: r[6] | 0, loc: r[7] || '', transitVia: r[8] || '' };
      row.hay = (row.mac + ' ' + row.mac.replace(/[^0-9a-f]/gi, '') + ' ' + row.devName + ' ' + row.devIP + ' ' + row.ip + ' ' + row.port).toLowerCase();
      return row;
    });
    GMAC.loadedAt = Date.now();
    GMAC.checkTime = d.check_time || '';
    byId('gmacTitle').innerText = '🗂️ 全网 MAC 总表 (共 ' + globalMACData.length + ' 条)';
    gmacFillFilters();
    GMAC.t.opt.empty = '没有匹配的记录';
    renderGlobalMACTable();
  }).catch((e) => { byId('gmacStat').innerHTML = '<span style="color:var(--bad)">加载失败: ' + esc(String(e)) + '</span>'; });
}
modalRegister('macModal', closeMACTable);
function closeMACTable() { byId('macModal').style.display = 'none'; fsExitWithin(byId('macModal')); }

function gmacKindHTML(r) {
  let h = r.flags & GMAC_UP ? '<span class="pill pill-uplink">上联/级联</span>' : '<span class="pill pill-access">接入</span>';
  if (r.flags & GMAC_AMB) { h += ' <span class="pill pill-amb" title="原始表里在多个端口都学到过, 已自动裁决出最可能的接入口">有歧义</span>'; }
  if (r.flags & GMAC_FLAP) { h += ' <span class="pill pill-bad" title="这个 MAC 同时被这台和另外至少一台设备的接入口学到——正常情况下一个终端只会插在一个口上, 这通常是二层环路(生成树没收敛/私接了网线)或终端在两个口之间快速漂移">⚠ 跨设备漂移</span>'; }
  if (r.transit > 0) {
    const detail = r.transitVia ? '路过的设备和端口:\n' + r.transitVia : '沿途还有 ' + (r.transit - 1) + ' 台设备在上联口见过它, 只保留这一条';
    h += ' <span class="pill pill-uplink" title="' + esc(detail) + '">路过 ' + r.transit + ' 台</span>';
  }
  return raw(h);
}

function gmacFillFilters() {
  const vl = {}, dv = {};
  globalMACData.forEach((m) => {
    if (m.vlan) { vl[m.vlan] = (vl[m.vlan] || 0) + 1; }
    const d = dv[m.devIP] || (dv[m.devIP] = { ip: m.devIP, name: m.devName, n: 0 });
    d.n++;
  });
  msOptions('gmacVlanFilter', Object.keys(vl).sort(cmpNatural).map((v) => ({ v, t: 'VLAN ' + v, s: vl[v] + ' 条' })));
  msOptions('gmacDeviceFilter', Object.values(dv).sort((a, b) => cmpNatural(a.name, b.name)).map((d) => ({ v: d.ip, t: d.name, s: d.n + ' 条' })));
}

const GMAC_GROUPS = {
  device: { by: (r) => r.devName + '\u0001' + r.devIP, title: (k) => { const p = k.split('\u0001'); return p[0] + '  ' + p[1]; } },
  port: { by: (r) => r.devName + ' · ' + r.port, title: (k) => k },
  vlan: { by: (r) => (r.vlan ? 'VLAN ' + r.vlan : ''), title: (k) => k || '(VLAN 未知)', order: (a, b) => cmpNatural(a, b) },
  subnet: { by: (r) => (r.ip ? r.ip.split('.').slice(0, 3).join('.') + '.0/24' : ''), title: (k) => k || '(没有 IP)', order: (a, b) => (a === '') - (b === '') || cmpNatural(ipSortKey(a.split('/')[0]), ipSortKey(b.split('/')[0])) },
  kind: { by: (r) => (r.flags & GMAC_UP ? '上联 / 级联口' : '接入口'), title: (k) => k }
};
function gmacApplyGroup(quiet) {
  const g = GMAC_GROUPS[GMAC.prefs.group];
  byId('gmacFold').style.display = g ? '' : 'none';
  const grp = g ? {
    by: g.by, order: g.order,
    label: (k, rows) => {
      let up = 0, ip = 0;
      rows.forEach((r) => { if (r.flags & GMAC_UP) { up++; } if (r.ip) { ip++; } });
      return vtGroupLabel(g.title(k), rows, html`<span>有 IP ${ip}</span>${up ? html`<span>上联 ${up}</span>` : ''}`);
    }
  } : null;
  if (quiet) { GMAC.t.group = grp; } else { GMAC.t.setGroup(grp); }
}

function gmacFiltered() {
  const raw0 = byId('gmacSearch').value.trim().toLowerCase();
  // 搜 MAC 时去掉分隔符再比: 001a.2b3c / 00-1a-2b / 00:1a:2b 都能命中
  const vlanF = msSet('gmacVlanFilter'), devF = msSet('gmacDeviceFilter'), kindF = msSet('gmacKindFilter');
  return globalMACData.filter((m) => {
    if (vlanF && !vlanF[m.vlan]) { return false; }
    if (devF && !devF[m.devIP]) { return false; }
    if (kindF && !((kindF.access && !(m.flags & GMAC_UP)) || (kindF.uplink && (m.flags & GMAC_UP)) || (kindF.ambiguous && (m.flags & GMAC_AMB)) ||
      (kindF.flap && (m.flags & GMAC_FLAP)) || (kindF.transit && m.transit > 0) || (kindF.local && (m.flags & GMAC_LOCAL)) || (kindF.noip && !m.ip))) { return false; }
    if (!raw0) { return true; }
    return m.hay.indexOf(raw0) >= 0 || macSearchMatches(m.mac, raw0);
  });
}
function renderGlobalMACTable() {
  if (!GMAC.t) { return; }
  const rows = gmacFiltered();
  GMAC.t.setRows(rows);
  let ip = 0, up = 0;
  const dv = {}, vl = {};
  rows.forEach((r) => { if (r.ip) { ip++; } if (r.flags & GMAC_UP) { up++; } dv[r.devIP] = 1; if (r.vlan) { vl[r.vlan] = 1; } });
  byId('gmacStat').innerHTML = (rows.length === globalMACData.length ? '全部 <b>' + rows.length + '</b> 条' : '筛选出 <b>' + rows.length + '</b> / ' + globalMACData.length + ' 条') +
    ' · 设备 <b>' + Object.keys(dv).length + '</b> 台 · VLAN <b>' + Object.keys(vl).length + '</b> 个 · 有 IP <b>' + ip + '</b> 条 · 上联口 <b>' + up + '</b> 条' +
    (GMAC.checkTime ? ' <span class="muted">· 数据来自 ' + esc(GMAC.checkTime) + ' 的巡检</span>' : '');
}
function gmacClear() {
  msClearAll(['gmacVlanFilter', 'gmacDeviceFilter', 'gmacKindFilter']);
  byId('gmacSearch').value = '';
  renderGlobalMACTable();
}
function gmacExport() {
  const rows = GMAC.t ? GMAC.t.visibleRows() : [];
  const kind = (r) => [r.flags & GMAC_UP ? '上联/级联' : '接入', r.flags & GMAC_AMB ? '有歧义' : '', r.transit ? '路过' + r.transit + '台' : '', r.flags & GMAC_LOCAL ? '随机MAC' : ''].filter(Boolean).join(' ');
  downloadText('全网MAC总表_' + fmtDay(new Date()) + '.csv', toCSV(['MAC', '终端IP', '设备', '设备IP', '端口', 'VLAN', '类型', '原始出现位置'],
    rows.map((r) => [r.mac, r.ip, r.devName, r.devIP, r.port, r.vlan, kind(r), r.loc])));
}
// 打开设备详情并跳到某个标签页(可选: 定位到某个端口)
function openDeviceAt(ip, tab, port) {
  openModal(ip);
  const t0 = Date.now();
  const wait = () => {
    if (currentDetail && currentDetail.ip === ip) {
      if (port && tab === 2) { jumpPortHosts(port); } else { switchTab(tab); }
      return;
    }
    if (Date.now() - t0 < 8000 && currentIP === ip) { setTimeout(wait, 120); }
  };
  wait();
}
