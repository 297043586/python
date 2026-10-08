/*
 * 设备详情: IP-MAC-端口。
 */
/* ---------- IP-MAC-端口 ---------- */
detailPart({ fill: function () { populateArpPortFilter(); renderHosts(); } });
// populateArpPortFilter 把当前设备的端口名铺进精确筛选下拉框——独立于
// 上面的模糊搜索框, 避免 "Gi0/1" 这种端口名作为子串命中 "Gi0/10" 之类的
// 误判(点击某个端口跳转过来时尤其需要精确, 不能是模糊匹配)。
function populateArpPortFilter() {
  var d = currentDetail;
  var cnt = {}, vlans = {};
  (d && d.host_mappings || []).forEach(function (h) {
    cnt[h.real_port] = (cnt[h.real_port] || 0) + 1;
    if (h.vlan_id) { vlans[h.vlan_id] = (vlans[h.vlan_id] || 0) + 1; }
  });
  (d && d.own_addrs || []).forEach(function (a) {
    var match = /^vlan(?:if|[- ]interface)?\s*(\d+)$/i.exec(String(a.if || '').trim());
    if (match && !Object.prototype.hasOwnProperty.call(vlans, match[1])) { vlans[match[1]] = 0; }
  });
  var names = (d && d.ports || []).map(function (p) { return p.name; });
  names.sort(naturalCmp);
  msOptions('arpPortFilter', names.map(function (n) { return { v: n, t: n, s: cnt[n] ? cnt[n] + ' 条' : '' }; }));
  msOptions('arpKindFilter', [{ v: '接入口', t: '接入口' }, { v: '上联/级联口', t: '上联/级联口' }, { v: '', t: '未定位' }]);
  msOptions('arpVlanFilter', Object.keys(vlans).sort(naturalCmp).map(function (v) { return { v: v, t: 'VLAN ' + v, s: vlans[v] + ' 条' }; }));
}

// jumpPortHosts 是端口表里点击端口名的入口: 切到 IP-MAC-端口标签页,
// 并把精确筛选框设成这个端口——跟搜索框的模糊匹配是两码事, 避免
// "Gi0/1" 把 "Gi0/10" 也一起筛出来。
function jumpPortHosts(port) {
  switchTab(2);
  msClearAll(['arpKindFilter', 'arpVlanFilter']);
  msSetValues('arpPortFilter', [port]);
  renderHosts();
}

function renderHosts() {
  var d = currentDetail;
  if (!d) { return; }
  var kw = byId('arpSearch').value.trim().toLowerCase();
  var includeRelated = !!(byId('arpIncludeRelated') && byId('arpIncludeRelated').checked);
  var portF = msSet('arpPortFilter'), kindF = msSet('arpKindFilter'), vlanF = msSet('arpVlanFilter');
  var clearBtn = byId('arpPortClear');
  if (clearBtn) { clearBtn.style.display = (portF || kindF || vlanF) ? 'inline-block' : 'none'; }
  var body = byId('arpBody');
  body.innerHTML = '';
  var n = 0;
  (d.host_mappings || []).forEach(function (h) {
    if (h.ip_source && !includeRelated) { return; }
    if (portF && !portF[h.real_port]) { return; }
    if (kindF && !kindF[h.port_kind === '接入口' || h.port_kind === '上联/级联口' ? h.port_kind : '']) { return; }
    if (vlanF && !vlanF[h.vlan_id]) { return; }
    if (kw) {
      var hay = (h.ip + ' ' + h.mac + ' ' + h.real_port + ' ' + h.vlan_if).toLowerCase();
      if (hay.indexOf(kw) < 0 && !macSearchMatches(h.mac, kw)) { return; }
    }
    n++;
    var pill = '<span class="pill pill-none">未定位</span>';
    var portHtml = '<span style="color:var(--fg-faint);">' + esc(h.real_port) + '</span>';
    if (h.port_kind === '接入口') {
      pill = '<span class="pill pill-access">接入口</span>';
      portHtml = '<b style="color:var(--good);">' + esc(h.real_port) + '</b>';
    } else if (h.port_kind === '上联/级联口') {
      pill = '<span class="pill pill-uplink">上联/级联口</span>';
      portHtml = '<b style="color:var(--accent);">' + esc(h.real_port) + '</b>';
    }
    var vlanCell = h.vlan_exact
      ? '<b>' + esc(h.vlan_id) + '</b>'
      : '<span style="color:var(--fg-faint);" title="猜测值, 非 FDB 精确 VLAN">' + esc(h.vlan_id) + ' ?</span>';
    var ipCell = '<b>' + esc(h.ip) + '</b>';
    if (h.ip_source) {
      ipCell += '<div class="hint" style="margin-top:2px;" title="本机 ARP 表没有这个 IP; 是从 ' + esc(h.ip_source) + ' 的 ARP 表跨设备关联过来的">🔗 来自 ' + esc(h.ip_source) + '</div>';
    }
    var tr = document.createElement('tr');
    tr.innerHTML = '<td>' + ipCell + '</td>' +
      '<td><code style="background:var(--track);padding:2px 6px;border-radius:4px;">' + esc(h.mac) + '</code></td>' +
      '<td>' + vlanCell + '</td>' +
      '<td><span style="color:var(--fg-dim);">' + esc(h.vlan_if) + '</span></td>' +
      '<td>' + portHtml + ' ' + pill + '</td>';
    body.appendChild(tr);
  });
  if (n === 0) {
    body.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--fg-faint);padding:24px;">没有匹配的记录</td></tr>';
  }
}
