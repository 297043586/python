/*
 * 表格列宽拖拽。
 */
/* ---------- 表格列宽拖拽 ----------
 * 弹窗本身早就能 resize:both 拖伸缩了(见 .modal-content), 但里面的表格
 * 一直是整列跟着内容自动分配宽度, 单列拉不动。这里补一个通用工具:
 * 给表头右边缘加一个 6px 的拖拽热区, 第一次拖拽时才把表格切到
 * table-layout:fixed 并把所有列当前的实际宽度定住(不这样做的话只固定被
 * 拖的那一列, 其余列会被 auto 布局重新按比例分配, 视觉上全部跳动),
 * 之后只改被拖的那一列。宽度按 localStorage 记住, 下次打开同一张表还在。
 * tbody 每次渲染都是整体替换 innerHTML, 但 thead/th 不会被替换, 所以
 * 只需要在表格第一次出现时初始化一次(用 dataset 标记防重复绑定)。 */
function initResizableColumns(tableId) {
  var table = byId(tableId);
  if (!table || table.dataset.resizableInit) { return; }
  table.dataset.resizableInit = '1';
  var ths = table.querySelectorAll('thead tr:first-child th');
  var saved = {};
  try { saved = JSON.parse(localStorage.getItem('colw:' + tableId) || '{}'); } catch (e) { saved = {}; }
  if (Object.keys(saved).length) {
    table.style.tableLayout = 'fixed';
    ths.forEach(function (th, i) { if (saved[i]) { th.style.width = saved[i] + 'px'; } });
  }
  ths.forEach(function (th, i) {
    var handle = document.createElement('div');
    handle.className = 'col-resizer';
    handle.addEventListener('click', function (e) { e.stopPropagation(); }); // 别误触发表头排序
    th.appendChild(handle);
    var startX = 0, widths = null;
    function onMove(e) {
      var dx = e.clientX - startX;
      th.style.width = Math.max(64, widths[i] + dx) + 'px';
    }
    function onUp() {
      handle.classList.remove('resizing');
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      var out = {};
      ths.forEach(function (t, j) { out[j] = t.offsetWidth; });
      try { localStorage.setItem('colw:' + tableId, JSON.stringify(out)); } catch (e) { /* 存不了就算了, 不影响拖拽本身 */ }
    }
    handle.addEventListener('mousedown', function (e) {
      e.preventDefault(); e.stopPropagation();
      if (table.style.tableLayout !== 'fixed') {
        var cur = {};
        ths.forEach(function (t, j) { cur[j] = t.offsetWidth; });
        ths.forEach(function (t, j) { t.style.width = cur[j] + 'px'; });
        table.style.tableLayout = 'fixed';
      }
      widths = {};
      ths.forEach(function (t, j) { widths[j] = t.offsetWidth; });
      startX = e.clientX;
      handle.classList.add('resizing');
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
  });
}

function renderPorts() {
  var d = currentDetail;
  if (!d) { return; }
  var kw = byId('portSearch').value.toLowerCase();
  var body = byId('portBody');

  var showExt = portExtVisible();
  byId('portsTable').classList.toggle('hide-ext', !showExt);
  byId('portExtToggle').checked = showExt;

  ['name', 'status', 'speed', 'lldp', 'rx', 'tx', 'in', 'out', 'err', 'disc', 'bcast', 'mac', 'change'].forEach(function (f) {
    var ic = byId('ic-' + f);
    if (!ic) { return; }
    ic.innerText = (f === sortField) ? (sortAsc ? '▲' : '▼') : '↕';
    ic.style.color = (f === sortField) ? 'var(--accent)' : 'var(--fg-faint)';
  });

  var rows = (d.ports || []).filter(function (p) {
    // 多选: UP / DOWN 属于同一组(选了两个 = 都显示), 其他条件之间是"同时满足"
    var f = portFilters;
    if ((f.UP || f.DOWN) && !((f.UP && p.status === 'UP') || (f.DOWN && p.status !== 'UP'))) { return false; }
    if (f.OPT && p.optical_rx_dbm === null && p.optical_tx_dbm === null) { return false; }
    if (f.UPLINK && !p.is_uplink) { return false; }
    if (f.AGG && !p.agg_group) { return false; }
    if (f.BUSY && Math.max(p.in_usage_percent || 0, p.out_usage_percent || 0) < 70) { return false; }
    if (f.PROB) {
      var x = p.ext || {};
      var bad = p.delta_errors > 0 || (x.delta_in_discards || 0) + (x.delta_out_discards || 0) > 0 ||
        (p.duplex === '半双工' && p.status === 'UP') || (p.optical_rx_dbm !== null && p.optical_rx_dbm !== undefined && p.optical_rx_dbm <= uiPref('weak_optical_dbm', -20) && p.status === 'UP');
      if (!bad) { return false; }
    }
    if (kw) {
      var hay = (p.name + ' ' + (p.alias || '') + ' ' + (p.vlan_id || '') + ' ' + (p.vlan_mode || '') + ' ' + (p.vlan_tagged || '') + ' ' + (p.duplex || '') +
                 ' ' + (p.ip_addr || '') + ' ' + p.lldp_remote_dev + ' ' + p.lldp_remote_port).toLowerCase();
      if (hay.indexOf(kw) < 0) { return false; }
    }
    return true;
  });

  rows.sort(function (a, b) {
    var r = 0;
    switch (sortField) {
      case 'name': r = naturalCmp(a.name, b.name); break;
      case 'status': r = naturalCmp(a.status, b.status); break;
      case 'speed': r = a.speed_mbps - b.speed_mbps; break;
      case 'lldp': r = naturalCmp(a.lldp_remote_dev, b.lldp_remote_dev); break;
      case 'rx': r = num(a.optical_rx_dbm, -999) - num(b.optical_rx_dbm, -999); break;
      case 'tx': r = num(a.optical_tx_dbm, -999) - num(b.optical_tx_dbm, -999); break;
      case 'in': r = a.in_speed_mbps - b.in_speed_mbps; break;
      case 'out': r = a.out_speed_mbps - b.out_speed_mbps; break;
      case 'err': r = a.delta_errors - b.delta_errors; break;
      case 'disc': r = extNum(a, 'disc') - extNum(b, 'disc'); break;
      case 'bcast': r = extNum(a, 'bcast') - extNum(b, 'bcast'); break;
      case 'mac': r = (a.mac_count || 0) - (b.mac_count || 0); break;
      case 'change': r = extNum(a, 'change') - extNum(b, 'change'); break;
    }
    return sortAsc ? r : -r;
  });

  var ratesOff = d.collect && d.collect.skip_rates;
  var extOff = d.collect && d.collect.skip_extended;
  var html = [];
  rows.forEach(function (p) {
    var inU = p.in_usage_percent.toFixed(1), outU = p.out_usage_percent.toFixed(1);
    var lldp = '<span style="color:var(--fg-faint);">-</span>';
    if (p.lldp_remote_dev !== '-') {
      lldp = '<b class="lldp-dev" title="' + esc(p.lldp_remote_dev) + '">➔ ' + esc(p.lldp_remote_dev) + '</b><div class="sub dim">' + esc(p.lldp_remote_port) + '</div>';
      // 对端已经被监控(按管理 IP 或设备名对上)就做成链接跳过去; 没监控就给一个 "+监控",
      // 带着对端上报的管理 IP 和名字打开新增节点表单(团体名还是要人填)。
      var nm = String(p.lldp_remote_dev).toLowerCase();
      var mon = (p.lldp_remote_ip && findSummary(p.lldp_remote_ip)) || summaryData.filter(function (x) { return String(x.sys_name || '').toLowerCase() === nm; })[0];
      var q = function (v) { return '&quot;' + esc(v || '') + '&quot;'; };
      if (mon) {
        lldp += '<div class="sub"><span class="lnk" onclick="openModal(' + q(mon.ip) + ')" title="已在监控, 点击查看">' + esc(mon.ip) + '</span></div>';
      } else if (p.lldp_remote_ip) {
        lldp += '<div class="sub">' + esc(p.lldp_remote_ip) + (p.lldp_remote_ip_src === 'arp' ? ' <span class="dim" title="对端 LLDP 没带管理地址, 按它的机框 MAC 在全网 ARP 里查到的">(ARP)</span>' : '') +
          ' <a href="javascript:void(0)" class="lnk" data-perm="devices" onclick="quickAddFromLLDP(' + q(p.lldp_remote_ip) + ', ' + q(p.lldp_remote_dev) + ')" title="用这个 IP 打开新增节点表单">+监控</a></div>';
      } else {
        lldp += '<div class="sub dim">对端没上报管理 IP <a href="javascript:void(0)" class="lnk" data-perm="devices" onclick="quickAddFromLLDP(' + q('') + ', ' + q(p.lldp_remote_dev) + ')" title="手工填 IP 添加">+监控</a></div>';
      }
    }
    var speed = p.speed_mbps >= 1000 ? (p.speed_mbps / 1000) + 'G' : (p.speed_mbps > 0 ? p.speed_mbps + 'M' : '-');
    var color = p.status === 'UP' ? 'var(--good)' : (p.status === 'ADMIN-DOWN' ? 'var(--warn)' : 'var(--fg-faint)');
    var err = p.delta_errors > 0 ? '<b style="color:var(--bad);">+' + p.delta_errors + '</b>' : '<span style="color:var(--fg-faint);">0</span>';

    // 物理接口这一列现在把描述和层级/VLAN 都以小字带出来, 不再各占一整列;
    // 端口名本身是个链接, 点击跳到 IP-MAC-端口标签页并按这个端口精确筛选。
    var nameCell = '<a href="javascript:void(0)" onclick="jumpPortHosts(&quot;' + esc(p.name) + '&quot;)" style="color:var(--fg);text-decoration:none;font-weight:bold;" title="点击查看这个端口上的 MAC/ARP"><b>' + esc(p.name) + '</b> 🔍</a>';
    if (p.is_uplink) { nameCell += ' <span class="pill pill-uplink">上联</span>'; }
    if (p.agg_group) { nameCell += ' <span class="pill pill-amb" title="聚合组成员">🧩 ' + esc(p.agg_group) + '</span>'; }
    if (p.alias) { nameCell += '<div class="sub dim" title="' + esc(p.alias) + '">' + esc(p.alias) + '</div>'; }
    var layerText = portLayerSmallText(p);
    if (layerText) { nameCell += '<div class="sub">' + layerText + '</div>'; }

    var inCell = '<span style="color:var(--fg-faint);">未采集</span>', outCell = inCell, errCell = inCell;
    if (!ratesOff) {
      var inM = p.in_speed_mbps, outM = p.out_speed_mbps, lv = portLiveFor(d.ip, p.index);
      if (lv) { inM = lv.in_mbps; outM = lv.out_mbps; inU = lv.in_pct.toFixed(1); outU = lv.out_pct.toFixed(1); }
      var tag = lv ? ' <span class="lv-tag" title="实时速率(最近几秒); 关掉「实时速率」恢复巡检平均值">⚡</span>' : '';
      inCell = '<div style="font-size:11.5px;">' + inU + '% (' + inM.toFixed(2) + ' Mbps)' + tag + '</div>' +
        '<div class="progress-bar-wrap" style="height:4px;"><div class="progress-bar" style="width:' + inU + '%;background:var(--info);"></div></div>';
      outCell = '<div style="font-size:11.5px;">' + outU + '% (' + outM.toFixed(2) + ' Mbps)' + tag + '</div>' +
        '<div class="progress-bar-wrap" style="height:4px;"><div class="progress-bar" style="width:' + outU + '%;background:var(--good);"></div></div>';
      errCell = err;
    }

    html.push('<tr><td>' + nameCell + '</td>' +
      '<td><span style="color:' + color + ';font-weight:600;">● ' + esc(p.status) + '</span></td>' +
      '<td>' + speed + '<div>' + duplexBadge(p) + '</div></td>' +
      '<td>' + lldp + '</td>' +
      '<td>' + opticalBadge(p.optical_rx_power, p.optical_rx_dbm) + '</td>' +
      '<td>' + opticalBadge(p.optical_tx_power, p.optical_tx_dbm) + '</td>' +
      '<td>' + inCell + '</td>' +
      '<td>' + outCell + '</td>' +
      '<td>' + errCell + '</td>' + portExtCells(p, extOff) + '</tr>');
  });

  body.innerHTML = html.length
    ? html.join('')
    : '<tr><td colspan="13" style="text-align:center;color:var(--fg-faint);padding:24px;">没有匹配的端口</td></tr>';
}
