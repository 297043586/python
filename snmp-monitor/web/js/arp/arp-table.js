/*
 * 全网 ARP 总表(仿 MAC 总表的写法: 后端按巡检轮次缓存 + 列式紧凑格式 + 虚拟滚动)。
 *
 * 跟 MAC 总表的区别: 这张表按 IP 为主索引(而不是 MAC), 而且专门标出两类
 * 排查环路/中毒/攻击时最有用的情况——同一个 IP 被不同 MAC 占用(冲突),
 * 或者同一个 MAC 名下挂了好几个 IP(正常情况是多网卡网关, 异常情况是
 * ARP 攻击/中毒: 攻击主机常用一个 MAC 冒充一大片 IP)。
 */
var globalARPData = [];
const GARP = { t: null, loadedAt: 0 };
const GARP_CONF = 1, GARP_MULTI = 2, GARP_RELAY = 4, GARP_DEVMAC = 8, GARP_ROUTER = 16, GARP_OWNIP = 32;

function openARPTable(force) {
  byId('arpModal').style.display = 'flex';
  if (!GARP.t) {
    GARP.t = new VTable(byId('garpTable'), {
      rowHeight: 40,
      columns: [
        { key: 'ip', title: '终端 IP', width: 'minmax(130px,.9fr)', sort: (r) => ipSortKey(r.ip), render: (r) => html`<b>${r.ip}</b>` },
        { key: 'mac', title: 'MAC 地址', width: 'minmax(170px,1.1fr)', sort: (r) => r.mac, render: (r) => html`<code>${r.mac}</code>` },
        { key: 'dev', title: '来源设备', width: 'minmax(170px,1.2fr)', sort: (r) => r.devName, render: (r) => html`<span class="lnk" data-act="dev">${r.devName}</span><span class="sub2">${r.devIP}</span>` },
        { key: 'port', title: '接入口', width: 'minmax(150px,1fr)', sort: (r) => r.devName + ' ' + r.port, render: (r) => r.port ? html`<span class="lnk" data-act="port" title="看这个口上的全部终端">${r.port}</span>` : raw('<span class="muted">-</span>') },
        { key: 'vlan', title: 'VLAN', width: '70px', sort: (r) => +r.vlan || 0, render: (r) => r.vlan || '-' },
        { key: 'kind', title: '类型', width: 'minmax(220px,1.5fr)', sort: (r) => r.flags, render: garpKindHTML }
      ],
      sort: { key: 'ip', asc: true },
      empty: '加载中…',
      onAction: (act, r) => {
        if (act === 'port' && r.port) { openDeviceAt(r.devIP, 2, r.port); } else { openModal(r.devIP); }
      }
    });
    byId('garpSearch').addEventListener('input', debounce(renderGlobalARPTable, 120));
    fsButton(byId('arpBox'), byId('garpFs'));
    msOptions('garpKindFilter', [{ v: 'conflict', t: 'IP 冲突(多个 MAC)' }, { v: 'multi', t: '一MAC多IP: 同网段(可疑)' },
      { v: 'router', t: '一MAC多IP: 路由器/三层多接口' }, { v: 'devmac', t: '被监控设备自己的 MAC' }, { v: 'ownip', t: '设备接口地址(网关/互联/管理)' },
      { v: 'normal', t: '普通终端' }, { v: 'relay', t: '跨设备关联出来的' }]);
  }
  if (!force && globalARPData.length && Date.now() - GARP.loadedAt < 30000) { renderGlobalARPTable(); return; }
  byId('garpStat').innerHTML = '<span class="spin">⟳</span> 正在加载…';
  latest('garp', pub('/api/arptable?fmt=compact')).then((d) => {
    const devs = d.devices || [];
    globalARPData = (d.rows || []).map((r) => {
      const dv = devs[r[2]] || ['', ''];
      const row = { ip: r[0], mac: r[1], devIP: dv[0], devName: dv[1] || dv[0], vlan: r[3] || '', port: r[4] || '', flags: r[5] | 0, other: r[6] | 0, note: r[7] || '' };
      row.hay = (row.ip + ' ' + row.mac + ' ' + row.mac.replace(/[^0-9a-f]/gi, '') + ' ' + row.devName + ' ' + row.devIP + ' ' + row.port + ' ' + row.note).toLowerCase();
      return row;
    });
    GARP.loadedAt = Date.now();
    GARP.checkTime = d.check_time || '';
    byId('garpTitle').innerText = '🌐 全网 ARP 总表 (共 ' + globalARPData.length + ' 条)';
    garpFillFilters();
    GARP.t.opt.empty = '没有匹配的记录';
    renderGlobalARPTable();
  }).catch((e) => { byId('garpStat').innerHTML = '<span style="color:var(--bad)">加载失败: ' + esc(String(e)) + '</span>'; });
}
modalRegister('arpModal', closeARPTable);
function closeARPTable() { byId('arpModal').style.display = 'none'; fsExitWithin(byId('arpBox')); }

function garpKindHTML(r) {
  let h = '';
  if (r.flags & GARP_CONF) { h += ' <span class="pill pill-bad" title="这个 IP 目前被 ' + (r.other + 1) + ' 个不同的 MAC 占用: 可能是 IP 冲突, 也可能是 DHCP 刚换过 MAC 还没过期, 排查环路/私接路由器先看这里">⚠ IP 冲突(' + (r.other + 1) + ')</span>'; }
  if (r.flags & GARP_MULTI) { h += ' <span class="pill pill-amb" title="这个 MAC 在同一个网段里挂了 ' + (r.other + 1) + ' 个 IP: 可能是代理 ARP(私接路由器)、配了从地址, 也可能是 ARP 攻击/中毒(攻击机常用一个 MAC 冒充一大片 IP)">⚠ 一MAC多IP(' + (r.other + 1) + ')</span>'; }
  if (r.flags & GARP_DEVMAC) { h += ' <span class="pill pill-none" title="被监控设备自己的 MAC: 三层交换机每个 VLANIF / 互联口一个 IP, 都用同一个 MAC, 正常">设备自身' + (r.other ? ' · ' + (r.other + 1) + ' 个 IP' : '') + '</span>'; }
  if (r.flags & GARP_ROUTER) { h += ' <span class="pill pill-none" title="这个 MAC 在 ' + (r.other + 1) + ' 个 IP 分别属于不同网段: 路由器 / 防火墙 / 没加入监控的三层交换机的多个接口, 一般正常">三层多接口(' + (r.other + 1) + ')</span>'; }
  if ((r.flags & GARP_OWNIP) && !(r.flags & GARP_DEVMAC)) { h += ' <span class="pill pill-none" title="被监控设备自己接口上的地址">设备接口</span>'; }
  if (r.flags & GARP_RELAY) { h += ' <span class="pill pill-uplink" title="这条记录的 IP 不是来源设备自己解析到的, 是跨设备关联出来的(另一台设备的 ARP + 这台设备的 MAC 表拼出来的)">关联</span>'; }
  if (r.note) { h += ' <span class="sub2" style="display:inline">' + esc(r.note) + '</span>'; }
  return raw(h || '<span class="muted">正常</span>');
}

function garpFillFilters() {
  const vl = {}, dv = {};
  globalARPData.forEach((m) => {
    if (m.vlan) { vl[m.vlan] = (vl[m.vlan] || 0) + 1; }
    const d = dv[m.devIP] || (dv[m.devIP] = { ip: m.devIP, name: m.devName, n: 0 });
    d.n++;
  });
  msOptions('garpVlanFilter', Object.keys(vl).sort(cmpNatural).map((v) => ({ v, t: 'VLAN ' + v, s: vl[v] + ' 条' })));
  msOptions('garpDeviceFilter', Object.values(dv).sort((a, b) => cmpNatural(a.name, b.name)).map((d) => ({ v: d.ip, t: d.name, s: d.n + ' 条' })));
}

function garpFiltered() {
  const raw0 = byId('garpSearch').value.trim().toLowerCase();
  const vlanF = msSet('garpVlanFilter'), devF = msSet('garpDeviceFilter'), kindF = msSet('garpKindFilter');
  return globalARPData.filter((m) => {
    if (vlanF && !vlanF[m.vlan]) { return false; }
    if (devF && !devF[m.devIP]) { return false; }
    if (kindF && !((kindF.conflict && (m.flags & GARP_CONF)) || (kindF.multi && (m.flags & GARP_MULTI)) || (kindF.relay && (m.flags & GARP_RELAY)) ||
      (kindF.router && (m.flags & GARP_ROUTER)) || (kindF.devmac && (m.flags & GARP_DEVMAC)) || (kindF.ownip && (m.flags & GARP_OWNIP)) ||
      (kindF.normal && !(m.flags & (GARP_CONF | GARP_MULTI | GARP_DEVMAC | GARP_ROUTER | GARP_OWNIP))))) { return false; }
    if (!raw0) { return true; }
    return m.hay.indexOf(raw0) >= 0 || macSearchMatches(m.mac, raw0);
  });
}
function renderGlobalARPTable() {
  if (!GARP.t) { return; }
  const rows = garpFiltered();
  GARP.t.setRows(rows);
  let conf = 0, multi = 0;
  rows.forEach((r) => { if (r.flags & GARP_CONF) { conf++; } if (r.flags & GARP_MULTI) { multi++; } });
  byId('garpStat').innerHTML = (rows.length === globalARPData.length ? '全部 <b>' + rows.length + '</b> 条' : '筛选出 <b>' + rows.length + '</b> / ' + globalARPData.length + ' 条') +
    (conf ? ' · <b style="color:var(--bad)">IP 冲突 ' + conf + ' 条</b>' : '') +
    (multi ? ' · <b style="color:var(--warn,#c90)">一MAC多IP ' + multi + ' 条</b>' : '') +
    (GARP.checkTime ? ' <span class="muted">· 数据来自 ' + esc(GARP.checkTime) + ' 的巡检</span>' : '');
}
function garpClear() {
  msClearAll(['garpVlanFilter', 'garpDeviceFilter', 'garpKindFilter']);
  byId('garpSearch').value = '';
  renderGlobalARPTable();
}
function garpExport() {
  const rows = GARP.t ? GARP.t.visibleRows() : [];
  const kind = (r) => [(r.flags & GARP_CONF) ? 'IP冲突' : '', (r.flags & GARP_MULTI) ? '一MAC多IP(同网段)' : '', (r.flags & GARP_ROUTER) ? '三层多接口' : '',
    (r.flags & GARP_DEVMAC) ? '设备自身' : '', (r.flags & GARP_RELAY) ? '关联' : '', r.note].filter(Boolean).join(' ');
  downloadText('全网ARP总表_' + fmtDay(new Date()) + '.csv', toCSV(['终端IP', 'MAC', '来源设备', '设备IP', '接入口', 'VLAN', '类型'],
    rows.map((r) => [r.ip, r.mac, r.devName, r.devIP, r.port, r.vlan, kind(r)])));
}
