/*
 * 设备面板 + 2D 机柜面板(前端)。
 *
 * v12 重写: 以前是一堆 div + CSS 拼出来的, 端口尺寸要按像素反算、框式竖插板卡、3D 倾斜
 * 这些逻辑缠在一起。现在整台机柜是一张 SVG(19 英寸面板 = 1000 个单位宽), 浏览器负责缩放,
 * 不再需要按像素反算; 框式一律横插(板卡上下叠放), 空槽画成窄盲板并可以压缩。
 *
 *   - 端口按真实端口表"画"出来: 端口名拆成 前缀 / 槽位路径 / 端口号, 路径第一段不同
 *     (1/0/x、2/0/x)是堆叠成员或板卡; 同一行按介质分块(管理口 / 电口 / 光口);
 *   - 每个口两盏灯(链路 / 活动), 错包、半双工、收光偏弱、近期抖动画外框, 聚合成员底部同色条,
 *     上联口有 ▲; 端口着色可以切换: 状态 / 利用率 / 速率 / VLAN;
 *   - 机柜和 3D 机房是同一份数据(/api/datacenter): 多个机房、机柜高度、手工放的服务器 /
 *     配线架 / PDU 都能看到; 编辑模式下直接拖动设备换 U 位、换机柜;
 *   - 显示设置(主题 / U 高度 / 着色 / 每行几个机柜 / 端口号 / 备注标签 / 空 U 盲板 / 压缩空槽)
 *     存在浏览器里, 每个人可以不一样。
 *
 * 3D 视图(gl.go / dc3d.go)还在用这里的 pnlSplit / pnlLayout / pnlChassisCards / pnlBrand /
 * pnlUnits / pnlFormEditorHTML 等函数, 改的时候注意。
 */
/* ================= 设备面板 / 2D 机柜面板 ================= */
var PNL = { ports: {}, flash: '', forms: {}, rack: { view: '', timer: null } };
var PV = { layout: null, saved: null, devices: [], room: '', edit: false, drag: null, dragEnd: 0, occ: {}, prefs: null };
var PV_W = 1000, PV_EAR = 26, PV_LBL = 178, PV_R = 42, PV_T = 26, PV_B = 32;
// zoom: 2D 机柜里单台设备的显示高度倍数 { ip: 1.5 }(只放大显示, 不改占用的 U 位); dpanel: 设备详情「面板」页的宽度 / 每 U 高度 { ip: { w: 150, uh: 90 } }
var PV_DEF = { theme: 'dark', uh: 'std', color: 'status', cols: 'auto', nums: true, note: true, blanks: true, fold: true, hideEmpty: false, trim: true, stretch: true, zoom: {}, dpanel: {} };
var PV_GAPH = 24;
var PV_UH = { compact: 36, std: 46, tall: 60, real: 92 };

// 把详情接口的 PortDetail 转成面板用的精简结构(跟 /api/panels 一致)
function pnlFromDetail(p) {
  var x = p.ext || {};
  return { i: p.index, n: p.name, al: p.alias, s: p.status, sp: p.speed_mbps, iu: p.in_usage_percent, ou: p.out_usage_percent,
    im: p.in_speed_mbps, om: p.out_speed_mbps, e: p.delta_errors, rx: p.optical_rx_dbm, tx: p.optical_tx_dbm, d: p.duplex,
    ag: p.agg_group, up: p.is_uplink, pe: (p.lldp_remote_dev && p.lldp_remote_dev !== '-') ? p.lldp_remote_dev : '', pp: p.lldp_remote_port,
    m: p.mac_count, v: pnlVlanText(p), dc: (x.delta_in_discards || 0) + (x.delta_out_discards || 0) + (x.delta_out_errors || 0),
    fl: x.last_change_sec >= 0 && x.last_change_sec < 600 };
}
function pnlVlanText(p) {
  if (p.ip_addr) { return 'L3 ' + p.ip_addr; }
  if (p.vlan_mode === 'access') { return 'access VLAN ' + p.vlan_id; }
  if (p.vlan_mode === 'trunk') { return 'trunk native ' + (p.vlan_id || '-') + (p.vlan_tagged ? ' 允许 ' + p.vlan_tagged : ''); }
  if (p.vlan_mode === 'hybrid') { return 'hybrid untag ' + p.vlan_untagged + ' tag ' + p.vlan_tagged; }
  return p.vlan_id ? 'PVID ' + p.vlan_id : '';
}

/* ---------- 端口名解析 / 分组(3D 视图也在用) ---------- */
var PNL_OPT_PREFIX = /^(xge|xgigabitethernet|tengigabitethernet|ten-gigabitethernet|tengige|te|tfgigabitethernet|tf|10ge|25ge|twentyfivegige|twe|40ge|fortygigabitethernet|fortygige|fo|fge|100ge|hundredgige|hundredgigabitethernet|hge|hu|200ge|400ge|sfp|fiber)/i;
// 前缀允许数字开头(10GE1/0/1、25GE2/0/3、100GE1/0/1), 跟 form.go 的 portNameRe 一致
function pnlSplit(name) {
  var n = String(name || '').replace(/\s+/g, '');
  var m = n.match(/^(\d*[A-Za-z][A-Za-z_\-]*|)((?:\d+[\/:.])*)(\d+)$/);
  if (!m) { return { prefix: n, path: '', num: 0, ok: false }; }
  return { prefix: m[1], path: m[2], num: parseInt(m[3], 10), ok: true };
}
function pnlKind(p, sp) {
  // H3C 的管理口叫 M-GigabitEthernet0/0/0, 以前没认出来, 被当成成员 0 单独画了一行
  if (/^(meth|mgmt|management|me|mng|m-gigabitethernet|m-ge|m-eth|m-fastethernet)/i.test(sp.prefix)) { return 'mgmt'; }
  if ((p.rx !== null && p.rx !== undefined) || (p.tx !== null && p.tx !== undefined)) { return 'sfp'; }
  if (PNL_OPT_PREFIX.test(sp.prefix)) { return 'sfp'; }
  if (p.sp >= 10000) { return 'sfp'; }
  return 'cu';
}
// pnlLayout: 端口 -> 机箱行(堆叠成员) -> 端口块(管理口 / 电口 / 光口)
function pnlLayout(ports) {
  var chassis = {}, order = [];
  ports.forEach(function (p) {
    var sp = pnlSplit(p.n);
    var parts = sp.path.split(/[\/:.]/).filter(function (x) { return x !== ''; });
    var ck = parts.length >= 2 ? parts[0] : '0';
    if (!sp.ok) { ck = 'x'; }
    if (!chassis[ck]) { chassis[ck] = { key: ck, groups: {} }; order.push(ck); }
    var kind = pnlKind(p, sp);
    var gk = (kind === 'mgmt' ? '0' : kind === 'cu' ? '1' : '2') + '|' + sp.path;
    var g = chassis[ck].groups[gk] || (chassis[ck].groups[gk] = { kind: kind, path: sp.path, ports: [] });
    g.ports.push({ p: p, num: sp.num });
  });
  order.sort(naturalCmp);
  // 只有管理口的一行(MEth0/0/1 这种路径跟业务口不一样), 以及只有一两个口的零散行
  // (Cisco 的 FastEthernet0 管理口之类)并到第一台机箱里, 不单独占一行
  var mgmtOnly = order.filter(function (k) {
    var gks = Object.keys(chassis[k].groups), n = 0;
    gks.forEach(function (gk) { n += chassis[k].groups[gk].ports.length; });
    return order.length > 1 && (n <= 2 || gks.every(function (gk) { return chassis[k].groups[gk].kind === 'mgmt'; }));
  });
  var host = order.filter(function (k) { return mgmtOnly.indexOf(k) < 0; })[0];
  if (host !== undefined) {
    mgmtOnly.forEach(function (k) {
      Object.keys(chassis[k].groups).forEach(function (gk) { chassis[host].groups[gk] = chassis[k].groups[gk]; });
    });
    order = order.filter(function (k) { return mgmtOnly.indexOf(k) < 0; });
  }
  return order.map(function (k) {
    var c = chassis[k];
    var gs = Object.keys(c.groups).sort(naturalCmp).map(function (gk) {
      var g = c.groups[gk];
      g.ports.sort(function (a, b) { return a.num - b.num; });
      return g;
    });
    return { key: k, groups: gs };
  });
}
// 框式: 端口按槽位号分到各块板卡上; 只有管理口的是主控板(MPU, 放中间);
// 槽位号之间缺的、以及型号槽位数比实际多出来的是空槽(opt.hideEmpty 时不画)。
//
// 槽位号在端口名里的位置跟厂商 / 是否组了 VSU、IRF 有关(路径 = 端口号前面那几段):
//   一段  3/1              槽位/端口                     → 第一段
//   两段  1/3/1            机框/槽位/端口(锐捷 VSU 等)    → 第二段(多台机框时带上机框号)
//         3/0/1            槽位/子卡/端口(华为 S7700 等) → 第一段
//     两种写法靠"哪一段变化得多"区分: 第二段取值比第一段多 → 第二段是槽位
//   三段  1/3/0/1          机框/槽位/子卡/端口(H3C IRF)  → 第二段(多台机框时带上机框号)
function pnlChassisCards(dev, opt) {
  var f = dev.form || {}, cards = {}, mpu = null;
  var parsed = (dev.ports || []).map(function (p) {
    var sp = pnlSplit(p.n);
    return { p: p, sp: sp, kind: pnlKind(p, sp), parts: sp.path.split(/[\/:.]/).filter(function (x) { return x !== ''; }) };
  });
  // 三段端口名有两种格式: 机框/槽位/端口, 或 槽位/子卡/端口。
  // 统计两段路径在每个第一段下的第二段集合; 多机框且每框板卡数相同时,
  // 不能只比较两个字段的不同值数量(例如 1/3、1/8、2/3、2/8 会打平)。
  var d0 = {}, d1 = {}, frames = {}, slotsByFrame = {};
  parsed.forEach(function (x) {
    if (x.kind === 'mgmt' || x.parts.length < 2) { return; }
    if (x.parts.length === 2) {
      d0[x.parts[0]] = true; d1[x.parts[1]] = true;
      (slotsByFrame[x.parts[0]] = slotsByFrame[x.parts[0]] || {})[x.parts[1]] = true;
    } else {
      frames[x.parts[0]] = true;
    }
  });
  var n0 = Object.keys(d0).length, n1 = Object.keys(d1).length;
  var maxSlotsPerFrame = 0, repeatedSlots = {}, secondHasZero = !!d1['0'];
  Object.keys(slotsByFrame).forEach(function (fr) {
    var slots = Object.keys(slotsByFrame[fr]);
    maxSlotsPerFrame = Math.max(maxSlotsPerFrame, slots.length);
    slots.forEach(function (slot) { repeatedSlots[slot] = (repeatedSlots[slot] || 0) + 1; });
  });
  var hasRepeatedSlot = Object.keys(repeatedSlots).some(function (slot) { return repeatedSlots[slot] > 1; });
  var secondIsSlot = n1 > n0 && !secondHasZero; // 1/1/x、1/3/x、2/1/x ...
  if (!secondIsSlot && !secondHasZero && n0 > 1 && n1 > 1 && maxSlotsPerFrame > 1 && hasRepeatedSlot) {
    secondIsSlot = true;
  }
  // 已知是框式时, 1/3/x 这样的单框单槽也应以第二段显示槽号; 3/0/x 的 0
  // 则仍按华为 S7700 一类的 槽位/子卡/端口 解释。
  if (!secondIsSlot && f.kind === 'chassis' && !secondHasZero && n1 === 1 && (n0 === 1 || hasRepeatedSlot)) {
    secondIsSlot = true;
  }
  var multiFrame = Object.keys(frames).length > 1 || (secondIsSlot && n0 > 1);
  parsed.forEach(function (x) {
    var parts = x.parts;
    if (x.kind === 'mgmt') { mpu = mpu || { key: 'MPU', mpu: true, ports: [] }; mpu.ports.push({ p: x.p, kind: x.kind, num: x.sp.num, path: x.sp.path }); return; }
    var key, sub = '';
    if (parts.length >= 3 || (parts.length === 2 && secondIsSlot)) {
      key = multiFrame ? parts[0] + '/' + parts[1] : parts[1];
      sub = parts.slice(2).join('/');
    } else if (parts.length === 2) {
      key = parts[0]; sub = parts[1];
    } else {
      key = parts.length === 1 ? parts[0] : '?';
    }
    (cards[key] = cards[key] || { key: key, ports: [] }).ports.push({ p: x.p, kind: x.kind, num: x.sp.num, path: x.sp.path, sub: sub });
  });
  var keys = Object.keys(cards).sort(naturalCmp), list = [];
  if (multiFrame) {
    // 多台机框: 每台机框各自补空槽(1/1 ~ 1/N, 2/1 ~ 2/N)
    var byFrame = {};
    keys.forEach(function (k) { var fr = k.split('/')[0]; (byFrame[fr] = byFrame[fr] || []).push(k); });
    Object.keys(byFrame).sort(naturalCmp).forEach(function (fr) {
      var nums = byFrame[fr].map(function (k) { return +k.split('/')[1]; }).filter(function (v) { return !isNaN(v); });
      var lo = nums.length ? Math.min.apply(null, nums) : 1, hi = nums.length ? Math.max.apply(null, nums) : 0;
      if (f.slots) { lo = Math.min(lo, 1); } // 型号给了槽位数: 从 1 号槽开始补
      hi = Math.max(hi, lo + (f.slots || 0) - 1);
      for (var s = lo; s <= hi; s++) { var k = fr + '/' + s; list.push(cards[k] || { key: k, empty: true, ports: [] }); }
      byFrame[fr].filter(function (k) { return isNaN(+k.split('/')[1]); }).forEach(function (k) { list.push(cards[k]); });
    });
  } else {
    var nums = keys.filter(function (k) { return /^\d+$/.test(k); }).map(Number);
    var lo = nums.length ? Math.min.apply(null, nums) : 1, hi = nums.length ? Math.max.apply(null, nums) : 0;
    if (f.slots) { lo = Math.min(lo, 1); }
    hi = Math.max(hi, lo + (f.slots || 0) - 1);
    for (var s = lo; s <= hi; s++) { list.push(cards[String(s)] || { key: String(s), empty: true, ports: [] }); }
    keys.filter(function (k) { return !/^\d+$/.test(k); }).forEach(function (k) { list.push(cards[k]); });
  }
  if (opt && opt.hideEmpty) { list = list.filter(function (c) { return !c.empty; }); }
  if (mpu) { var mid = Math.floor(list.length / 2); list.splice(mid, 0, mpu); }
  list.forEach(function (c) { c.ports.sort(function (a, b) { return naturalCmp(a.path, b.path) || a.num - b.num; }); });
  return list;
}
// 一块板卡上按介质 + 子卡分块
function pnlCardGroups(c) {
  var gs = {}, order = [];
  c.ports.forEach(function (x) {
    var k = x.kind + '|' + x.sub;
    if (!gs[k]) { gs[k] = { kind: x.kind, path: x.path, ports: [] }; order.push(k); }
    gs[k].ports.push({ p: x.p, num: x.num });
  });
  return order.sort(naturalCmp).map(function (k) { return gs[k]; });
}
var PNL_AGG_COLORS = ['#f472b6', '#38bdf8', '#a3e635', '#fbbf24', '#c084fc', '#2dd4bf', '#fb7185', '#818cf8'];
function pnlAggColor(name, map) {
  if (!name) { return ''; }
  if (!map[name]) { map[name] = PNL_AGG_COLORS[Object.keys(map).length % PNL_AGG_COLORS.length]; }
  return map[name];
}
function pnlProblem(p) {
  return p.e > 0 || p.dc > 0 || (p.d === '半双工' && p.s === 'UP') || (p.rx !== null && p.rx !== undefined && p.rx <= -20 && p.s === 'UP') || p.fl;
}
function pnlBrand(b) {
  var m = { huawei: ['HUAWEI', '#e11d48'], h3c: ['H3C', '#3b82f6'], ruijie: ['Ruijie', '#f97316'], cisco: ['CISCO', '#22d3ee'] };
  return m[b] || [String(b || 'SWITCH').toUpperCase(), '#94a3b8'];
}
function pnlHwLed(s) {
  if (!s || s.indexOf('未') === 0) { return ''; }
  return s.indexOf('异常') === 0 ? 'bad blink' : 'ok';
}
// 一台设备占几 U: 按后端识别的形态(form.go), 老数据没有 form 时退回"每个堆叠成员一行"
function pnlUnits(dev) {
  if (dev.form && dev.form.total_u) { return Math.max(1, dev.form.total_u); }
  return Math.max(1, pnlLayout(dev.ports || []).length);
}
function pnlTipHTML(k) {
  var x = PNL.ports[k];
  if (!x) { return ''; }
  var p = x.p;
  var st = p.s === 'UP' ? '<span style="color:#4ade80;">● UP</span>' : (p.s === 'ADMIN-DOWN' ? '<span style="color:#fbbf24;">● 管理 DOWN</span>' : '<span style="color:#94a3b8;">● DOWN</span>');
  var sp = p.sp >= 1000 ? (p.sp / 1000) + 'G' : (p.sp ? p.sp + 'M' : '-');
  var lines = ['<b>' + esc(p.n) + '</b> ' + st + (p.up ? ' <span style="color:#38bdf8;">▲上联</span>' : '')];
  if (p.al) { lines.push('<span style="color:#94a3b8;">' + esc(p.al) + '</span>'); }
  lines.push('速率 ' + sp + (p.d ? ' · ' + esc(p.d) : '') + (p.v ? ' · ' + esc(p.v) : ''));
  if (p.s === 'UP') { lines.push('入 ' + (p.im || 0).toFixed(2) + ' Mbps (' + (p.iu || 0).toFixed(1) + '%) · 出 ' + (p.om || 0).toFixed(2) + ' Mbps (' + (p.ou || 0).toFixed(1) + '%)'); }
  if (p.rx !== null && p.rx !== undefined) { lines.push('收光 ' + p.rx.toFixed(2) + ' dBm' + (p.tx !== null && p.tx !== undefined ? ' · 发光 ' + p.tx.toFixed(2) + ' dBm' : '') + (p.rx <= -20 ? ' <span style="color:#fb923c;">偏弱</span>' : '')); }
  if (p.pe) { lines.push('对端 ➔ ' + esc(p.pe) + ' (' + esc(p.pp || '') + ')'); }
  if (p.ag) { lines.push('聚合组 🧩 ' + esc(p.ag)); }
  if (p.m) { lines.push('学到 MAC ' + p.m + ' 个'); }
  if (p.e > 0) { lines.push('<span style="color:#f87171;">本轮错包 +' + p.e + '</span>'); }
  if (p.dc > 0) { lines.push('<span style="color:#f87171;">本轮丢包/出错 +' + p.dc + '</span>'); }
  if (p.fl) { lines.push('<span style="color:#60a5fa;">10 分钟内状态变化过</span>'); }
  return lines.join('<br>');
}
function pvPlaceTip(container, tip, e, html) {
  tip.innerHTML = html;
  tip.style.display = 'block';
  var rect = container.getBoundingClientRect();
  var x = e.clientX - rect.left + 14, y = e.clientY - rect.top + 14;
  if (x + tip.offsetWidth > rect.width) { x = e.clientX - rect.left - tip.offsetWidth - 14; }
  tip.style.left = Math.max(0, x) + 'px'; tip.style.top = Math.max(0, y) + 'px';
}
function pnlBindTip(container, tip, onClick) {
  if (container.dataset.tipBound) { return; }
  container.dataset.tipBound = '1';
  container.addEventListener('mousemove', function (e) {
    var el = e.target.closest ? e.target.closest('[data-k]') : null;
    if (!el) { tip.style.display = 'none'; return; }
    pvPlaceTip(container, tip, e, pnlTipHTML(el.getAttribute('data-k')));
  });
  container.addEventListener('mouseleave', function () { tip.style.display = 'none'; });
  container.addEventListener('click', function (e) {
    var el = e.target.closest ? e.target.closest('[data-k]') : null;
    if (el) { tip.style.display = 'none'; onClick(PNL.ports[el.getAttribute('data-k')]); }
  });
}

/* ---------- 显示设置 ---------- */
function pvPrefs() {
  if (!PV.prefs) {
    var p = {};
    try { p = JSON.parse(localStorage.getItem('pvPrefs') || '{}') || {}; } catch (e) {}
    PV.prefs = {};
    Object.keys(PV_DEF).forEach(function (k) { PV.prefs[k] = p[k] !== undefined ? p[k] : PV_DEF[k]; });
    ['zoom', 'dpanel'].forEach(function (k) { if (!PV.prefs[k] || typeof PV.prefs[k] !== 'object') { PV.prefs[k] = {}; } });
  }
  return PV.prefs;
}
function pvSavePrefs() { try { localStorage.setItem('pvPrefs', JSON.stringify(PV.prefs)); } catch (e) {} }
function pvSetPref(k, v) {
  pvPrefs()[k] = v;
  pvSavePrefs();
  byId('pvCfg').innerHTML = pvCfgHTML();
  renderRacks();
  if (byId('panelBox') && currentTab === 9) { renderDevicePanel(); }
}
function pvOpts() {
  var p = pvPrefs();
  return { uh: typeof p.uh === 'number' ? pvClamp(p.uh, 24, 140) : (PV_UH[p.uh] || PV_UH.std), zoom: p.zoom || {}, color: p.color, nums: !!p.nums, note: !!p.note, blanks: !!p.blanks, fold: !!p.fold, hideEmpty: !!p.hideEmpty, trim: !!p.trim, stretch: !!p.stretch, maxS: 40, aggMap: {} };
}
function pvApplyTheme() {
  var t = 'pvt-' + (pvPrefs().theme || 'dark');
  ['rack2dPane', 'panelBox'].forEach(function (id) {
    var el = byId(id); if (!el) { return; }
    el.classList.remove('pvt-dark', 'pvt-silver', 'pvt-light'); el.classList.add(t);
  });
}
function pvCfgHTML() {
  var p = pvPrefs();
  var seg = function (k, opts) {
    return '<div class="pv-seg">' + opts.map(function (o) {
      return '<button class="' + (String(p[k]) === String(o[0]) ? 'on' : '') + '" onclick="pvSetPref(&quot;' + k + '&quot;,' + (typeof o[0] === 'number' ? o[0] : '&quot;' + o[0] + '&quot;') + ')">' + o[1] + '</button>';
    }).join('') + '</div>';
  };
  var chk = function (k, t) { return '<label><input type="checkbox"' + (p[k] ? ' checked' : '') + ' onchange="pvSetPref(&quot;' + k + '&quot;,this.checked)"> ' + t + '</label>'; };
  return '<h5>面板主题</h5>' + seg('theme', [['dark', '深色金属'], ['silver', '银灰'], ['light', '浅色']]) +
    '<h5>每 U 高度</h5>' + seg('uh', [['compact', '紧凑'], ['std', '标准'], ['tall', '宽松'], ['real', '真实比例']]) +
    '<div class="pv-slider"><input type="range" min="24" max="140" step="2" value="' + pvOpts().uh + '" oninput="this.nextElementSibling.innerText=this.value+\' px\'" onchange="pvSetPref(&quot;uh&quot;,+this.value)"><span>' + pvOpts().uh + ' px</span></div>' +
    '<div class="sub faint">单台设备太窄、端口挤在一起: 点设备 → 「显示高度」单独放大它。</div>' +
    '<h5>端口着色</h5>' + seg('color', [['status', '链路状态'], ['util', '利用率'], ['speed', '速率'], ['vlan', 'VLAN']]) +
    '<h5>每行机柜数</h5>' + seg('cols', [['auto', '自动'], [1, '1'], [2, '2'], [3, '3'], [4, '4']]) +
    '<h5>显示</h5>' + chk('stretch', '端口铺满面板宽度(关掉 = 按真实比例居中)') + chk('nums', '端口号(端口够大时)') + chk('note', '设备备注标签(设备管理里填的备注)') +
    chk('trim', '折叠连续 3U 以上的空位(编辑时自动展开)') + chk('blanks', '空 U 画成盲板') + chk('fold', '框式交换机压缩空槽位') + chk('hideEmpty', '框式交换机隐藏空槽位(只画有板卡的槽)') +
    '<div class="sub faint" style="margin-top:8px;">这些设置只保存在当前浏览器。机柜高度 / 颜色在「📐 编辑机柜」里改, 所有人看到的一样。</div>';
}
function pvToggleCfg(e) {
  if (e) { e.stopPropagation(); }
  var c = byId('pvCfg');
  if (!c.classList.contains('show')) { c.innerHTML = pvCfgHTML(); }
  c.classList.toggle('show');
}
document.addEventListener('click', function (e) {
  var c = byId('pvCfg');
  if (c && c.classList.contains('show') && !c.contains(e.target) && e.target.id !== 'pvCfgBtn') { c.classList.remove('show'); }
});

/* ---------- SVG 基础 ---------- */
function pvN(n) { return Math.round(n * 10) / 10; }
function pvClamp(v, a, b) { return Math.max(a, Math.min(b, v)); }
function pvR(cls, x, y, w, h, rx, style) {
  return '<rect class="' + cls + '" x="' + pvN(x) + '" y="' + pvN(y) + '" width="' + pvN(Math.max(0, w)) + '" height="' + pvN(Math.max(0, h)) + '"' +
    (rx ? ' rx="' + pvN(rx) + '"' : '') + (style ? ' style="' + style + '"' : '') + '/>';
}
function pvT(cls, x, y, fs, txt, style, anchor) {
  return '<text class="' + cls + '" x="' + pvN(x) + '" y="' + pvN(y) + '" font-size="' + pvN(fs) + '"' + (anchor ? ' text-anchor="' + anchor + '"' : '') +
    (style ? ' style="' + style + '"' : '') + '>' + esc(txt) + '</text>';
}
function pvTextW(s, fs) { var w = 0; for (var i = 0; i < s.length; i++) { w += s.charCodeAt(i) > 255 ? fs : fs * .58; } return w; }
function pvTrunc(s, maxW, fs) {
  s = String(s || '');
  if (pvTextW(s, fs) <= maxW) { return s; }
  while (s.length > 1 && pvTextW(s + '…', fs) > maxW) { s = s.slice(0, -1); }
  return s + '…';
}
function pvSpeedTab(sp) { return sp >= 40000 ? '#f472b6' : sp >= 25000 ? '#a78bfa' : sp >= 10000 ? '#60a5fa' : '#d6c7a1'; }
// 端口着色模式: 返回填充色, 空串 = 不着色
function pvTint(p, mode) {
  if (mode === 'util') {
    if (p.s !== 'UP') { return ''; }
    var u = Math.max(p.iu || 0, p.ou || 0);
    return u >= 80 ? '#ef4444' : u >= 50 ? '#f59e0b' : u >= 10 ? '#22c55e' : '#166534';
  }
  if (mode === 'speed') {
    if (p.s !== 'UP') { return ''; }
    var sp = p.sp || 0;
    return sp >= 40000 ? '#ec4899' : sp >= 25000 ? '#a855f7' : sp >= 10000 ? '#3b82f6' : sp >= 1000 ? '#22c55e' : '#94a3b8';
  }
  if (mode === 'vlan') {
    var v = p.v || '';
    if (!v) { return ''; }
    if (/^L3/.test(v)) { return '#f1f5f9'; }
    if (/^trunk/.test(v)) { return '#f59e0b'; }
    var m = v.match(/\d+/), n = m ? parseInt(m[0], 10) : 0;
    return 'hsl(' + ((n * 67) % 360) + ',70%,55%)';
  }
  return '';
}

/* ---------- 端口 ---------- */
// 一组同介质端口的形状: 每 chunk 个一小块, 每块 rows 行; A = 以端口尺寸 s 为单位的总宽度
function pvGroupShape(g) {
  var n = g.ports.length, rows = g.kind === 'mgmt' || n <= 1 || (g.kind === 'sfp' && n <= 2) ? 1 : 2, chunk = g.kind === 'cu' ? 12 : 8;
  var pw = g.kind === 'sfp' ? 1.25 : 1, ph = g.kind === 'sfp' ? .66 : .8, chunks = [], A = 0;
  for (var i = 0; i < n; i += chunk) { chunks.push(Math.ceil(Math.min(chunk, n - i) / rows)); }
  chunks.forEach(function (c, ci) { A += c * pw + (c - 1) * .16 + (ci ? .55 : 0); });
  return { g: g, rows: rows, chunk: chunk, pw: pw, ph: ph, chunks: chunks, A: A };
}
function pvShapesW(shapes, s) { var w = 0; shapes.forEach(function (sh, i) { w += (sh.A + (i ? 1 : 0)) * s; }); return w; }
// 一行端口能用的最大尺寸: 宽度方向 availW、高度方向 availH 都要放得下
function pvFit(shapes, availW, availH, maxS) {
  var A = 0, hf = 0;
  shapes.forEach(function (sh, i) { A += sh.A + (i ? 1 : 0); hf = Math.max(hf, sh.rows * sh.ph + (sh.rows - 1) * .3); });
  var s = maxS;
  if (A > 0) { s = Math.min(s, availW / A); }
  if (hf > 0) { s = Math.min(s, availH / hf); }
  return Math.max(3, s);
}
function pvPortsSVG(devKey, shapes, x, yMid, s, o, kx) {
  kx = kx || 1;
  var out = '', cx = x;
  shapes.forEach(function (sh, gi) {
    if (gi) { cx += s * kx; }
    var ports = sh.g.ports, idx = 0, bh = sh.rows * sh.ph * s + (sh.rows - 1) * .3 * s, y0 = yMid - bh / 2;
    sh.chunks.forEach(function (cols, ci) {
      if (ci) { cx += s * .55 * kx; }
      var cnt = Math.min(sh.chunk, ports.length - idx);
      for (var j = 0; j < cnt; j++) {
        var c = Math.floor(j / sh.rows), r = j % sh.rows;
        out += pvPortSVG(devKey, ports[idx + j].p, sh.g.kind, cx + c * (sh.pw + .16) * s * kx, y0 + r * (sh.ph + .3) * s, s, o, kx);
      }
      idx += cnt;
      cx += (cols * sh.pw * s + (cols - 1) * .16 * s) * kx;
    });
  });
  return out;
}
function pvPortSVG(devKey, p, kind, x, y, s, o, kx) {
  var k = devKey + '|' + p.i;
  PNL.ports[k] = { dev: devKey, p: p };
  var sfp = kind === 'sfp', w = s * (sfp ? 1.25 : 1) * (kx || 1), h = s * (sfp ? .66 : .8);
  var cls = 'pp k-' + kind + (p.s === 'UP' ? ' st-up' : (p.s === 'ADMIN-DOWN' ? ' st-admin' : ' st-down')), st = '';
  if (p.s === 'UP' && ((p.im || 0) + (p.om || 0)) > 0.05 && !o.offline) {
    cls += ' act';
    st = '--blink:' + Math.max(0.12, 1.1 - Math.max(p.iu || 0, p.ou || 0) / 100).toFixed(2) + 's;';
  }
  if (p.e > 0 || p.dc > 0) { cls += ' f-err'; }
  else if (p.d === '半双工' && p.s === 'UP') { cls += ' f-half'; }
  else if (p.rx !== null && p.rx !== undefined && p.rx <= -20 && p.s === 'UP') { cls += ' f-weak'; }
  else if (p.fl) { cls += ' f-flap'; }
  if (o.match && !o.match(p)) { cls += ' dim'; }
  if (o.flash && o.flash === p.n) { cls += ' flash'; }
  var out = '<g class="' + cls + '" data-k="' + esc(k) + '"' + (st ? ' style="' + st + '"' : '') + '>' + pvR('pb', x, y, w, h, s * .06);
  if (sfp) {
    var i = s * .1;
    out += pvR('ph', x + i, y + i, w - 2 * i, h - 2 * i);
    if (p.s === 'UP' || (p.rx !== null && p.rx !== undefined)) {
      out += pvR('pm', x + i * 1.6, y + i * 1.6, w - i * 3.2, h - i * 3.2) + pvR('pt', x + w * .3, y + h * .6, w * .4, h * .26, 1, 'fill:' + pvSpeedTab(p.sp || 0));
    }
  } else {
    var i2 = s * .12;
    out += pvR('ph', x + i2, y + i2, w - 2 * i2, h - 2 * i2 - s * .06) + pvR('pk', x + w * .38, y + h - i2 - s * .1, w * .24, s * .1);
  }
  var tint = pvTint(p, o.color);
  if (tint) { out += pvR('pc', x + s * .16, y + s * .15, w - s * .32, h - s * .3, 1, 'fill:' + tint); }
  var lw = Math.max(1.2, w * .2), lh = Math.max(1, s * .1), ly = sfp ? y - lh - s * .04 : y + s * .14;
  out += pvR('l1', x + w * .1, ly, lw, lh) + pvR('l2', x + w * .9 - lw, ly, lw, lh);
  if (o.nums && s >= 15) {
    var sp2 = pnlSplit(p.n);
    if (sp2.ok) { out += '<text class="pnum" x="' + pvN(x + w / 2) + '" y="' + pvN(y + h * (sfp ? .45 : .48)) + '" font-size="' + pvN(s * .34) + '">' + sp2.num + '</text>'; }
  }
  var fw = Math.max(1.2, s * .1);
  out += pvR('pf', x - s * .08, y - s * .08, w + s * .16, h + s * .16, s * .1, 'stroke-width:' + pvN(fw));
  if (p.ag) { out += pvR('pa', x, y + h + s * .04, w, Math.max(1, s * .08), 0, 'fill:' + pnlAggColor(p.ag, o.aggMap || {})); }
  if (p.up) { var cx = x + w / 2; out += '<path class="pv-upm" d="M' + pvN(cx - s * .16) + ' ' + pvN(y - s * .05) + 'L' + pvN(cx + s * .16) + ' ' + pvN(y - s * .05) + 'L' + pvN(cx) + ' ' + pvN(y - s * .28) + 'Z"/>'; }
  return out + '</g>';
}

/* ---------- 设备 ---------- */
// 面板底板 + 两侧挂耳
function pvPlate(y, h, cls) {
  return pvR(cls || 'pv-plate', PV_EAR - 1, y + .6, PV_W - 2 * PV_EAR + 2, h - 1.2, 2) + pvR('pv-hl', PV_EAR, y + .8, PV_W - 2 * PV_EAR, 1.2) +
    pvEar(0, y, h) + pvEar(PV_W - PV_EAR, y, h);
}
function pvEar(x, y, h) {
  var out = pvR('pv-ear', x, y + .6, PV_EAR, h - 1.2, 1.5), n = Math.max(1, Math.round(h / 60)), r = pvClamp(h / 10, 1.8, 4.2);
  for (var i = 0; i < n; i++) { out += '<circle class="pv-screw" cx="' + (x + PV_EAR / 2) + '" cy="' + pvN(y + h * (i + .5) / n) + '" r="' + pvN(r) + '"/>'; }
  return out;
}
// 左侧铭牌: 一列状态灯 + 品牌 / 名称 / 型号
function pvLabelSVG(br, name, extra, sub, leds, x, y, w, h) {
  var r = pvClamp(h / 14, 1.5, 3.4), gap = r * 2.8, out = '', ly = y + h / 2 - gap * (leds.length - 1) / 2;
  leds.forEach(function (l, i) {
    out += '<circle class="pv-led ' + l[0] + '" cx="' + pvN(x + r) + '" cy="' + pvN(ly + i * gap) + '" r="' + pvN(r) + '"><title>' + l[1] + '</title></circle>';
  });
  var tx = x + r * 2 + 6, tw = w - (tx - x);
  var fsB = pvClamp(h * .2, 6, 12), fsN = pvClamp(h * .28, 8, 16), fsS = pvClamp(h * .17, 6, 11), three = sub && h >= 44;
  out += pvT('pv-t pv-b', tx, y + h * (three ? .24 : .32), fsB, pvTrunc(br[0], tw, fsB), 'fill:' + br[1]);
  out += pvT('pv-t pv-nm', tx, y + h * (three ? .52 : .68), fsN, pvTrunc(name + (extra || ''), tw, fsN));
  if (three) { out += pvT('pv-t pv-d', tx, y + h * .8, fsS, pvTrunc(sub, tw, fsS)); }
  return out;
}
// 备注标签贴(设备管理里填的备注)
function pvTape(text, x, cy, w, h) {
  var fs = pvClamp(h * .56, 7, 12);
  return '<g><title>' + esc(text) + '</title>' + pvR('pv-tape', x, cy - h / 2, w, h, 1.5) + pvT('pv-t pv-tapet', x + 5, cy, fs, pvTrunc(text, w - 10, fs)) + '</g>';
}
function pvDevLeds(d) {
  return [[d.alive ? 'ok' : 'bad blink', 'SYS 运行'], [pnlHwLed(d.power), 'PWR 电源: ' + (d.power || '未知')], [pnlHwLed(d.fan), 'FAN 风扇: ' + (d.fan || '未知')], [d.alerts > 0 ? 'bad blink' : '', 'ALM 告警: ' + (d.alerts || 0)]];
}
function pvPortOpts(d, o) {
  return { match: o.devHit && o.devHit(d) ? o.matchHit : o.match, flash: o.flash, color: o.color, nums: o.nums, aggMap: o.aggMap, offline: !d.alive };
}
// 一台被监控设备(盒式 / 堆叠 / 框式), 画在 (x, y), 高 H 个单位, 宽度固定 PV_W
function pvDevSVG(d, x, y, H, o) {
  var body = (d.form && d.form.kind === 'chassis') ? pvChassisBody(d, H, o) : pvBoxBody(d, H, o);
  var cls = 'pv-dev' + (d.alive ? '' : ' off') + (o.devMatch && !o.devMatch(d) ? ' nomatch' : '');
  return '<g class="' + cls + '" data-dev="' + esc(d.ip) + '" transform="translate(' + pvN(x) + ',' + pvN(y) + ')">' + body +
    (d.alive && d.alerts > 0 ? pvR('pv-hot', 1, 1, PV_W - 2, H - 2, 3) : '') + '</g>';
}
function pvBoxBody(d, H, o) {
  var rows = pnlLayout(d.ports || []);
  if (!rows.length) { rows = [{ key: '0', groups: [] }]; }
  var n = rows.length, rh = H / n, multi = n > 1, br = pnlBrand(d.brand), leds = pvDevLeds(d);
  var note = o.note && d.note ? d.note : '', tapeW = note ? 118 : 0;
  var px = PV_EAR + PV_LBL, availW = PV_W - PV_EAR - 14 - px - (tapeW ? tapeW + 12 : 0), padY = Math.max(3, rh * .15);
  var shapes = rows.map(function (r) { return r.groups.map(pvGroupShape); }), s = o.maxS;
  shapes.forEach(function (sh) { if (sh.length) { s = Math.min(s, pvFit(sh, availW, rh - 2 * padY, o.maxS)); } });
  var po = pvPortOpts(d, o), out = '';
  // 端口尺寸受 1U 高度限制, 48 口交换机画出来只占面板中间一截、两边空着。铺满模式下把端口
  // 横向拉宽(最多 1.7 倍), 所有行用同一个系数, 堆叠成员之间端口对齐
  var kx = 1;
  if (o.stretch) {
    kx = 1.7;
    shapes.forEach(function (sh) { var w = pvShapesW(sh, s); if (w > 0) { kx = Math.min(kx, availW / w); } });
    kx = Math.max(1, kx);
  }
  rows.forEach(function (r, i) {
    var y = i * rh;
    out += pvPlate(y, rh);
    var sub = (d.model || d.ip) + (multi ? ' · ' + (/^\d+$/.test(r.key) ? '成员 ' + r.key : '其他') : '');
    out += pvLabelSVG(br, d.name, multi ? ' #' + r.key : '', sub, leds, PV_EAR + 8, y, PV_LBL - 14, rh);
    var wUsed = pvShapesW(shapes[i], s) * kx;
    out += pvPortsSVG(d.ip, shapes[i], px + Math.max(0, (availW - wUsed) / 2), y + rh / 2, s, po, kx);
  });
  if (note) { out += pvTape(note, PV_W - PV_EAR - 10 - tapeW, rh / 2, tapeW, pvClamp(rh * .5, 12, 26)); }
  return out;
}
// 框式: 板卡一律横插(上下叠放)。空槽画成窄盲板(可在显示设置里关掉压缩), 右侧风扇 / 电源位
function pvChassisBody(d, H, o) {
  var cards = pnlChassisCards(d, { hideEmpty: o.hideEmpty }), br = pnlBrand(d.brand), out = pvPlate(0, H);
  var lblH = Math.min(H - 8, 92), real = cards.filter(function (c) { return !c.empty && !c.mpu; }).length, total = cards.filter(function (c) { return !c.mpu; }).length;
  out += pvLabelSVG(br, d.name, '', d.model || d.ip, pvDevLeds(d), PV_EAR + 8, 4, PV_LBL - 14, lblH);
  if (H > lblH + 26) { out += pvT('pv-t pv-d', PV_EAR + 20, lblH + 16, 11, '框式 · ' + real + '/' + total + ' 槽在用'); }
  var note = o.note && d.note ? d.note : '';
  if (note && H > lblH + 60) { out += pvTape(note, PV_EAR + 12, lblH + 42, PV_LBL - 22, 22); }
  var bayW = 40, ax = PV_EAR + PV_LBL, aw = PV_W - PV_EAR - 10 - bayW - 6 - ax, ay = 6, ah = H - 12, gap = 3, wsum = 0;
  cards.forEach(function (c) { c._w = c.empty ? (o.fold ? .42 : 1) : (c.mpu ? .8 : 1); wsum += c._w; });
  // 端口尺寸跟同一机柜里的盒式设备差不多大, 不因为板卡高就画得特别大
  var usable = ah - gap * Math.max(0, cards.length - 1), s = Math.min(o.maxS, o.uh * .5), lblW = 62;
  cards.forEach(function (c) {
    c._h = usable * c._w / wsum;
    if (c.ports.length) { c._sh = pnlCardGroups(c).map(pvGroupShape); s = Math.min(s, pvFit(c._sh, aw - lblW - 26, c._h - Math.max(4, c._h * .22), o.maxS)); }
  });
  var po = pvPortOpts(d, o);
  out += pvR('pv-slotbg', ax - 3, ay - 3, aw + 6, ah + 6, 2);
  var cy = ay;
  cards.forEach(function (c) {
    var ch = c._h;
    if (c.empty) {
      out += pvR('pv-blankcard', ax, cy, aw, ch, 1.5);
      if (ch >= 8) {
        var r0 = Math.min(2.4, ch / 5);
        out += '<circle class="pv-screw" cx="' + pvN(ax + 8) + '" cy="' + pvN(cy + ch / 2) + '" r="' + pvN(r0) + '"/><circle class="pv-screw" cx="' + pvN(ax + aw - 8) + '" cy="' + pvN(cy + ch / 2) + '" r="' + pvN(r0) + '"/>';
        out += pvT('pv-t pv-d', ax + aw / 2, cy + ch / 2, Math.min(ch * .55, 11), '空槽 ' + c.key, 'opacity:.7', 'middle');
      }
    } else {
      var lh = Math.min(ch - 4, 18);
      out += pvR('pv-card' + (c.mpu ? ' mpu' : ''), ax, cy, aw, ch, 1.5) + pvR('pv-lever', ax + 2, cy + ch * .18, 5, ch * .64, 1) + pvR('pv-lever', ax + aw - 7, cy + ch * .18, 5, ch * .64, 1) +
        pvR('pv-cardlbl', ax + 11, cy + ch / 2 - lh / 2, lblW - 14, lh, 2) +
        pvT('pv-cardlt', ax + 11 + (lblW - 14) / 2, cy + ch / 2, Math.min(lh * .6, 10.5), c.mpu ? 'MPU' : 'SLOT ' + c.key, c.mpu ? 'fill:#fbbf24' : '');
      if (c._sh) {
        var cw = aw - lblW - 26, kc = o.stretch ? pvClamp(cw / Math.max(1, pvShapesW(c._sh, s)), 1, 1.7) : 1;
        out += pvPortsSVG(d.ip, c._sh, ax + lblW + 10, cy + ch / 2, s, po, kc);
      }
    }
    cy += ch + gap;
  });
  // 风扇 / 电源
  var bx = PV_W - PV_EAR - 10 - bayW, fanH = ah * .62, nf = pvClamp(Math.floor(fanH / 46), 1, 4), fr = Math.min(bayW / 2 - 5, fanH / nf / 2 - 4);
  out += pvR('pv-bay', bx, ay, bayW, ah, 2);
  for (var i = 0; i < nf; i++) {
    var fy = ay + fanH * (i + .5) / nf, fx = bx + bayW / 2;
    out += '<circle class="pv-fan" cx="' + pvN(fx) + '" cy="' + pvN(fy) + '" r="' + pvN(fr) + '"/>' +
      '<path class="pv-fanb" d="M' + pvN(fx - fr * .7) + ' ' + pvN(fy) + 'H' + pvN(fx + fr * .7) + 'M' + pvN(fx) + ' ' + pvN(fy - fr * .7) + 'V' + pvN(fy + fr * .7) + '"/>';
  }
  var psH = (ah - fanH - 8) / 2;
  for (var j = 0; j < 2; j++) {
    var py = ay + fanH + 4 + j * psH;
    out += pvR('pv-bay2', bx + 4, py + 2, bayW - 8, psH - 4, 2) + '<circle class="pv-led ' + (pnlHwLed(d.power) || 'ok') + '" cx="' + pvN(bx + bayW / 2) + '" cy="' + pvN(py + 8) + '" r="2.2"/>';
  }
  return out;
}
// 3D 机房里手工放的非监控设备(服务器 / 配线架 / PDU / 防火墙...)
function pvItemSVG(it, x, y, H, o) {
  var kind = (typeof DC_KIND !== 'undefined' && DC_KIND[it.kind]) ? DC_KIND[it.kind][0] : it.kind;
  var led = { ok: 'ok', warn: 'warn', bad: 'bad blink', off: '' }[it.status || ''];
  if (led === undefined) { led = 'info'; }
  var out = '<g class="pv-dev pv-item" data-item="' + esc(it.id) + '" transform="translate(' + pvN(x) + ',' + pvN(y) + ')">' + pvPlate(0, H);
  out += pvLabelSVG([kind, '#38bdf8'], it.name || kind, '', it.model || it.ip || '', [[led, '状态']], PV_EAR + 8, 0, PV_LBL - 14, Math.min(H, 60));
  var note = o.note && it.note ? it.note : '', tapeW = note ? 118 : 0;
  var ax = PV_EAR + PV_LBL, aw = PV_W - PV_EAR - 14 - ax - (tapeW ? tapeW + 12 : 0);
  out += pvItemDecor(it.kind, ax, aw, H);
  if (note) { out += pvTape(note, PV_W - PV_EAR - 10 - tapeW, Math.min(H, 60) / 2, tapeW, pvClamp(H * .4, 12, 24)); }
  return out + '</g>';
}
function pvVents(x, w, H) {
  var out = '', n = Math.max(1, Math.floor(w / 11));
  for (var i = 0; i < n; i++) { out += pvR('pv-vent', x + i * 11, H * .28, 5, H * .44, 2); }
  return out;
}
function pvItemDecor(k, ax, aw, H) {
  var out = '', i;
  if (k === 'server' || k === 'storage') {
    var rows = Math.max(1, Math.round(H / 26)), cols = k === 'storage' ? 12 : 8, bw = aw * (k === 'storage' ? 1 : .6) / cols, bh = (H - 8) / rows;
    for (var r = 0; r < rows; r++) {
      for (var c = 0; c < cols; c++) {
        out += pvR('pv-bay2', ax + c * bw + 1.5, 4 + r * bh + 1.5, bw - 3, bh - 3, 1.5) +
          '<circle class="pv-led ok" cx="' + pvN(ax + c * bw + bw - 7) + '" cy="' + pvN(4 + r * bh + bh / 2) + '" r="' + pvN(Math.min(1.8, bh / 8)) + '"/>';
      }
    }
    if (k === 'server') { out += pvVents(ax + aw * .64, aw * .36, H); }
  } else if (k === 'patch') {
    var n = 24, step = aw / n, pw = Math.min(H * .5, step * .76);
    for (i = 0; i < n; i++) {
      out += pvR('pv-patch', ax + i * step + (step - pw) / 2, H / 2 - pw * .4, pw, pw * .8, 1);
      if (pw >= 12) { out += pvT('pv-t pv-d', ax + i * step + step / 2, H / 2 + pw * .55, pw * .32, String(i + 1), '', 'middle'); }
    }
  } else if (k === 'pdu') {
    var m = 12, st = aw / m, ow = Math.min(H * .55, st * .7);
    for (i = 0; i < m; i++) {
      var ox = ax + i * st + (st - ow) / 2;
      out += pvR('pv-outlet', ox, H / 2 - ow / 2, ow, ow, 2) + pvR('pv-vent', ox + ow * .25, H / 2 - ow * .15, ow * .12, ow * .3) + pvR('pv-vent', ox + ow * .63, H / 2 - ow * .15, ow * .12, ow * .3);
    }
  } else if (k === 'ups') {
    var lw = Math.min(140, aw * .3), lh = Math.min(H * .6, 40);
    out += pvR('pv-lcd', ax, H / 2 - lh / 2, lw, lh, 3) + pvT('pv-lcdt', ax + 8, H / 2, Math.min(lh * .5, 14), 'UPS ONLINE') + pvVents(ax + lw + 20, aw - lw - 20, H);
  } else if (k === 'firewall' || k === 'router' || k === 'switch') {
    var np = k === 'switch' ? 24 : 8, ps = Math.min(H * .34, 22), pg = ps * 1.18, rowsP = k === 'switch' ? 2 : 1, colsP = Math.ceil(np / rowsP);
    out += pvVents(ax, Math.max(0, aw - colsP * pg - 20), H);
    for (i = 0; i < np; i++) {
      var cI = Math.floor(i / rowsP), rI = i % rowsP;
      out += pvR('pv-outlet', ax + aw - colsP * pg + cI * pg, H / 2 - (rowsP * ps * .8 + (rowsP - 1) * ps * .25) / 2 + rI * ps * 1.05, ps, ps * .8, 1);
    }
  } else {
    out += pvVents(ax, aw, H);
  }
  return out;
}

/* ---------- 机柜 ---------- */
function pvRackSVG(rack, place, items, o) {
  var U = rack.height || 42, uh = o.uh, W = PV_W + 2 * PV_R, occ = {}, u;
  place.list.forEach(function (x) { for (u = x.top; u > x.top - x.h; u--) { occ[u] = 'd:' + x.d.ip; } });
  items.forEach(function (it) { for (u = it.u; u < it.u + it.h; u++) { occ[u] = 'i:' + it.id; } });
  PV.occ[rack.name] = occ;
  // 每个 U 的纵坐标; 连续 3U 以上的空位折叠成一条(编辑时不折叠, 拖动按等高 U 计算)
  var ys = {}, hs = {}, segs = [], y = PV_T, trim = o.trim && !PV.edit;
  // 单独放大显示的设备: 它占的那几 U 按倍数加高(编辑时不放大, 拖动按等高 U 计算)
  var zAt = {};
  if (!PV.edit) { place.list.forEach(function (x) { var z = +(o.zoom || {})[x.d.ip] || 1; if (z > 1) { for (var q = x.top; q > x.top - x.h; q--) { zAt[q] = z; } } }); }
  u = U;
  while (u >= 1) {
    if (trim && !occ[u]) {
      var e = u;
      while (e - 1 >= 1 && !occ[e - 1]) { e--; }
      if (u - e + 1 >= 3) {
        segs.push({ gap: true, from: u, to: e, y: y });
        for (var k = u; k >= e; k--) { ys[k] = y; }
        y += PV_GAPH; u = e - 1;
        continue;
      }
    }
    var hu = uh * (zAt[u] || 1);
    ys[u] = y; hs[u] = hu; segs.push({ u: u, y: y, h: hu }); y += hu; u--;
  }
  // 从 top 往下 h 个 U 的总高度
  var spanH = function (top, h) { var t = 0; for (var q = top; q > top - h; q--) { t += hs[q] || uh; } return t; };
  var IH = y - PV_T, H = y + PV_B;
  var out = '<svg class="pv-rsvg" viewBox="0 0 ' + W + ' ' + pvN(H) + '" data-rack="' + esc(rack.name) + '" data-rid="' + esc(rack.id) + '" data-u="' + U + '" data-uh="' + uh + '">';
  out += pvR('pv-frame', 0, 0, W, H, 8) + pvR('pv-cap', 4, 4, W - 8, PV_T - 6, 4) + pvR('pv-cap', 4, H - PV_B + 3, W - 8, PV_B - 7, 4);
  if (rack.color) { out += pvR('', 4, 4, W - 8, 5, 2, 'fill:' + rack.color); }
  out += '<g transform="translate(0,' + pvN(H - PV_B + 2) + ')">' + pvVents(W / 2 - 110, 220, PV_B - 4) + '</g>';
  out += pvR('pv-inner', PV_R, PV_T, PV_W, IH) + pvR('pv-rail', PV_R - 14, PV_T, 14, IH) + pvR('pv-rail', PV_R + PV_W, PV_T, 14, IH);
  var fsU = Math.min(uh * .36, 13), holes = uh >= 30 ? [.2, .5, .8] : [.5];
  segs.forEach(function (sg) {
    if (sg.gap) {
      var n = sg.from - sg.to + 1;
      out += pvR('pv-blank', PV_R + 1, sg.y + 2, PV_W - 2, PV_GAPH - 4, 3, 'opacity:.55') +
        pvT('pv-t pv-d', PV_R + PV_W / 2, sg.y + PV_GAPH / 2, 11, '⋯ U' + sg.to + ' ~ U' + sg.from + ' 空 ' + n + 'U ⋯', '', 'middle') +
        pvT('pv-unum', (PV_R - 14) / 2, sg.y + PV_GAPH / 2, 9, '⋮') + pvT('pv-unum', W - (PV_R - 14) / 2, sg.y + PV_GAPH / 2, 9, '⋮');
      return;
    }
    var yy = sg.y, hh = sg.h;
    out += pvT('pv-unum', (PV_R - 14) / 2, yy + hh / 2, fsU, String(sg.u)) + pvT('pv-unum', W - (PV_R - 14) / 2, yy + hh / 2, fsU, String(sg.u));
    holes.forEach(function (f) { out += pvR('pv-hole', PV_R - 10, yy + hh * f - 2.5, 5, 5) + pvR('pv-hole', PV_R + PV_W + 5, yy + hh * f - 2.5, 5, 5); });
    if (!occ[sg.u] && o.blanks) {
      out += pvR('pv-blank', PV_R + 1, yy + .8, PV_W - 2, hh - 1.6, 1.5) + pvR('pv-blankl', PV_R + 60, yy + hh * .42, PV_W - 120, hh * .16, 1);
    }
  });
  place.list.forEach(function (x) {
    var y0 = ys[x.top], dh = spanH(x.top, x.h);
    out += pvDevSVG(x.d, PV_R, y0, dh, o);
    if (x.conflict) { out += pvR('pv-conf', PV_R + 2, y0 + 2, PV_W - 4, dh - 4, 3); }
  });
  items.forEach(function (it) { out += pvItemSVG(it, PV_R, ys[it.u + it.h - 1], spanH(it.u + it.h - 1, it.h), o); });
  return out + '<g class="pv-ghostl"></g></svg>';
}

/* ---------- 机柜视图: 打开 / 加载 / 渲染 ---------- */
// 3D 机房(dc3d.go) 和 2D 机柜面板两种看法, 记住上次选的; 不支持 WebGL 时只有 2D
function rackSetView(v) {
  if (v === '3d' && !glSupported()) { v = '2d'; }
  PNL.rack.view = v;
  try { localStorage.setItem('rackView', v); } catch (e) {}
  byId('rackView3d').classList.toggle('active', v === '3d');
  byId('rackView2d').classList.toggle('active', v === '2d');
  byId('rackView3d').disabled = !glSupported();
  byId('rackView3d').title = glSupported() ? '' : '这个浏览器不支持 WebGL, 只能用 2D 面板';
  byId('dcPane').style.display = v === '3d' ? '' : 'none';
  byId('rack2dPane').style.display = v === '3d' ? 'none' : '';
  pvHint();
  if (v === '3d') { pvClosePop(); dcOpen(); } else { dcClose(); loadRacks(); }
}
function pvHint() {
  byId('rackHint').innerText = PNL.rack.view === '3d'
    ? '数据中心机房 3D 视图: 左键拖动旋转, 右键拖动平移, 滚轮缩放; 悬停看详情, 单击机柜/设备查看, 双击飞到机柜前。登录后可以「✏️ 编辑机房」添加机柜和设备。'
    : (PV.edit ? '编辑中: 按住设备拖到任意机柜的空位(绿色 = 放得下), 松手即保存; 点设备改机柜 / U 位 / 形态, 点机柜标题的 ⚙ 改高度和颜色。'
      : '跟 3D 机房是同一份机柜数据。悬停看详情, 点设备或端口弹出信息卡; 「🎨 显示设置」可以换主题、U 高度、端口着色。');
}
function openRacks() {
  byId('rackModal').style.display = 'flex';
  var v = 'x';
  try { v = localStorage.getItem('rackView') || 'x'; } catch (e) {}
  if (v === 'x') { v = glSupported() ? '3d' : '2d'; }
  rackSetView(v);
  if (!PNL.rack.timer) {
    PNL.rack.timer = setInterval(function () {
      if (byId('rackModal').style.display !== 'none' && PNL.rack.view !== '3d' && !PV.drag && !byId('pvPop')) { loadRacks(); }
    }, uiPref('rack_refresh_seconds', 30) * 1000);
  }
  pvBindStage();
}
function closeRacks() {
  fsExitWithin(byId('rackModal'));
  if (byId('rackModal').style.display === 'none') { return; }
  if (typeof dcClose === 'function') { dcClose(); }
  byId('rackModal').style.display = 'none';
  if (PNL.rack.timer) { clearInterval(PNL.rack.timer); PNL.rack.timer = null; }
  pvClosePop();
}
function loadRacks() {
  return pub('/api/datacenter').then(function (d) {
    PV.devices = (d.devices || []).map(function (x) { x.ports = x.ports || []; return x; });
    var saved = dcNormLayout(d.layout);
    PV.saved = JSON.parse(JSON.stringify(saved));
    // 设备引用了机房里没有的机柜名(sysLocation) → 跟 3D 一样补一个自动机柜(不保存)
    PV.layout = JSON.parse(JSON.stringify(saved));
    dcEnsureAutoRacks(PV.layout, PV.devices);
    if (!PV.room || !PV.layout.rooms.some(function (r) { return r.id === PV.room; })) { PV.room = PV.layout.rooms[0].id; }
    if (!PV.drag) { renderRacks(); }
  }).catch(function (e) { byId('rackStage').innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '</div>'; });
}
function pvDevByIp(ip) { return PV.devices.filter(function (x) { return x.ip === ip; })[0]; }
function pvItemById(id) { return PV.layout ? PV.layout.items.filter(function (x) { return x.id === id; })[0] : null; }
function pvRackById(id) { return PV.layout ? PV.layout.racks.filter(function (x) { return x.id === id; })[0] : null; }
function renderRacks() {
  var stage = byId('rackStage');
  if (!stage) { return; }
  pvApplyTheme();
  if (!PV.layout) { stage.innerHTML = '<div class="empty">加载中...</div>'; return; }
  var o = pvOpts(), L = PV.layout, p = pvPrefs();
  PNL.ports = {}; PV.occ = {};
  var rs = byId('pvRoom');
  rs.innerHTML = L.rooms.map(function (r) {
    var n = L.racks.filter(function (k) { return k.room === r.id && (k.type || 'rack') === 'rack'; }).length;
    return '<option value="' + esc(r.id) + '"' + (r.id === PV.room ? ' selected' : '') + '>🏢 ' + esc(r.name) + ' (' + n + ' 柜)</option>';
  }).join('');
  rs.style.display = L.rooms.length > 1 ? '' : 'none';
  var racks = L.racks.filter(function (k) { return (k.type || 'rack') === 'rack' && k.room === PV.room; }).sort(function (a, b) { return naturalCmp(a.name, b.name); });
  var sel = byId('rackSel'), only = sel.value;
  if (only && !racks.some(function (k) { return k.name === only; })) { only = ''; }
  sel.innerHTML = '<option value="">全部机柜 (' + racks.length + ')</option>' + racks.map(function (k) { return '<option value="' + esc(k.name) + '">' + esc(k.name) + '</option>'; }).join('');
  sel.value = only;
  var kw = (byId('rackSearch').value || '').trim().toLowerCase(), probOnly = byId('rackProblems').checked;
  var portMatch = function (x) {
    if (probOnly && !pnlProblem(x)) { return false; }
    return !kw || (x.n + ' ' + (x.al || '') + ' ' + (x.pe || '')).toLowerCase().indexOf(kw) >= 0;
  };
  var devHit = function (d) { return (d.name + ' ' + d.ip + ' ' + (d.note || '') + ' ' + (d.model || '')).toLowerCase().indexOf(kw) >= 0; };
  if (kw) { o.devMatch = function (d) { return devHit(d) || (d.ports || []).some(portMatch); }; }
  var list = racks.filter(function (k) { return !only || k.name === only; });
  if (!list.length) {
    stage.innerHTML = '<div class="empty">这个机房还没有机柜。切到「🏢 3D 机房」→「✏️ 编辑机房」→「➕ 机柜」添加, 或者在设备的 sysLocation 里写上机柜名。</div>';
  } else {
    stage.innerHTML = list.map(function (k) {
      k.height = k.height || 42;
      var devs = PV.devices.filter(function (d) { return d.rack === k.name; });
      var items = L.items.filter(function (it) { return it.rack === k.id; });
      var place = dcPlace(k, devs, items);
      var po = {};
      Object.keys(o).forEach(function (x) { po[x] = o[x]; });
      // 端口压暗: 名字 / 备注命中的设备只看"有问题"过滤, 其他设备按端口名 / 对端匹配
      po.match = (kw || probOnly) ? portMatch : null;
      if (kw) { po.devHit = devHit; po.matchHit = probOnly ? pnlProblem : null; }
      var used = 0, bad = 0;
      place.list.forEach(function (x) { used += x.h; if (!x.d.alive || x.d.alerts > 0) { bad++; } });
      items.forEach(function (it) { used += it.h; if (it.status === 'bad' || it.status === 'warn') { bad++; } });
      var svg = pvRackSVG(k, place, items, po);
      return '<div class="pv-rack" data-rack="' + esc(k.name) + '"><div class="pv-rhead"><b>🗄️ ' + esc(k.name) + '</b>' +
        '<span class="sub">' + (place.list.length + items.length) + ' 台 · ' + used + '/' + k.height + 'U</span><span class="pv-bar"><i style="width:' + Math.min(100, Math.round(used * 100 / k.height)) + '%;"></i></span>' +
        (bad ? '<b style="color:var(--bad);font-size:12px;">' + bad + ' 台异常</b>' : '') +
        (k.auto ? '<span class="sub" title="按设备的 sysLocation 自动生成的机柜, 在 3D 机房里放置或在这里改高度后会变成正式机柜">· 自动</span>' : '') +
        (PV.edit ? '<button class="filter-btn" onclick="pvRackPop(event,&quot;' + esc(k.id) + '&quot;)">⚙ 机柜设置</button>' : '') + '</div>' + svg +
        (place.overflow.length ? '<div class="pv-over">⚠ 放不下(机柜太矮或 U 位冲突): ' + place.overflow.map(function (d) { return esc(d.name); }).join('、') + '</div>' : '') + '</div>';
    }).join('');
  }
  stage.className = 'pv-stage' + (PV.edit ? ' pv-edit' : '');
  var cols = p.cols === 'auto' ? 0 : parseInt(p.cols, 10) || 0;
  stage.style.gridTemplateColumns = cols ? 'repeat(' + cols + ', minmax(0, 1fr))' : 'repeat(auto-fill, minmax(min(100%, 560px), 1fr))';
  byId('rackLegend').innerHTML = pvLegendHTML(o.color);
  byId('rackEditBtn').innerText = PV.edit ? '✅ 完成编辑' : '📐 编辑机柜';
}
function pvLegendHTML(mode) {
  var n = 0;
  var sw = function (p, kind) {
    var q = { i: 'lg' + (n++), n: '', s: 'UP' };
    Object.keys(p).forEach(function (k) { q[k] = p[k]; });
    return '<svg class="pv-sw" viewBox="-4 -6 ' + (kind === 'sfp' ? 33 : 28) + ' 26" width="' + (kind === 'sfp' ? 30 : 26) + '" height="22">' + pvPortSVG('_lg', q, kind || 'cu', 0, 0, 20, { color: mode, aggMap: { a: '#f472b6' } }) + '</svg>';
  };
  var chip = function (c, t) { return '<span><i class="pv-chip" style="background:' + c + ';"></i>' + t + '</span>'; };
  var html = '<div class="pv-legend">';
  if (mode === 'util') {
    html += '<b>利用率:</b>' + chip('#166534', '&lt;10%') + chip('#22c55e', '10~50%') + chip('#f59e0b', '50~80%') + chip('#ef4444', '≥80%') + '<span>不着色 = DOWN</span>';
  } else if (mode === 'speed') {
    html += '<b>速率:</b>' + chip('#94a3b8', '100M') + chip('#22c55e', '1G') + chip('#3b82f6', '10G') + chip('#a855f7', '25G') + chip('#ec4899', '40G+');
  } else if (mode === 'vlan') {
    html += '<b>VLAN:</b><span>access 口按 VLAN 号着色(同色 = 同 VLAN)</span>' + chip('#f59e0b', 'trunk') + chip('#f1f5f9', '三层口') + '<span>不着色 = 没取到 VLAN</span>';
  } else {
    html += '<span>' + sw({}) + 'UP</span><span>' + sw({ im: 5, iu: 60 }) + '有流量(越快越忙)</span><span>' + sw({ s: 'DOWN' }) + 'DOWN</span><span>' + sw({ s: 'ADMIN-DOWN' }) + '管理 DOWN</span>' +
      '<span>' + sw({ sp: 10000, rx: -3 }, 'sfp') + '光模块(拉环颜色 = 速率)</span>';
  }
  html += '<span>' + sw({ e: 3 }) + '错包/丢包</span><span>' + sw({ d: '半双工' }) + '半双工</span><span>' + sw({ sp: 1000, rx: -24 }, 'sfp') + '收光偏弱</span>' +
    '<span>' + sw({ fl: true }) + '近期抖动</span><span>' + sw({ ag: 'a' }) + '聚合成员</span><span>' + sw({ up: true }) + '上联口</span>' +
    '<span><i class="pv-chip" style="background:#fde68a;border:1px solid #d97706;"></i>设备备注</span></div>';
  return html;
}

/* ---------- 交互: 悬停 / 点击 / 拖动 ---------- */
function pvDevTip(d) {
  if (!d) { return ''; }
  var f = d.form || {};
  var lines = ['<b>' + esc(d.name) + '</b> ' + (d.alive ? (d.alerts > 0 ? '<span style="color:#f87171;">● ' + d.alerts + ' 条告警</span>' : '<span style="color:#4ade80;">● 正常</span>') : '<span style="color:#fb923c;">● 离线</span>'),
    esc(d.ip) + ' · ' + esc(pnlBrand(d.brand)[0]) + (d.model ? ' ' + esc(d.model) : ''), esc(glFormText(f))];
  if (d.alive) { lines.push('CPU ' + d.cpu + '% · 内存 ' + d.mem + '%' + (d.temp && d.temp !== '-' ? ' · 温度 ' + esc(d.temp) : '')); }
  var up = (d.ports || []).filter(function (p) { return p.s === 'UP'; }).length;
  lines.push('端口 ' + up + '/' + (d.ports || []).length + ' UP');
  if (d.note) { lines.push('<span style="color:#fbbf24;">📝 ' + esc(d.note) + '</span>'); }
  lines.push('<span style="color:#94a3b8;">' + (PV.edit ? '按住拖动换位置 · 单击编辑' : '单击查看信息卡') + '</span>');
  return lines.join('<br>');
}
function pvItemTip(it) {
  if (!it) { return ''; }
  var k = typeof DC_KIND !== 'undefined' && DC_KIND[it.kind] ? DC_KIND[it.kind][0] : it.kind;
  var st = (typeof DC_STATUS !== 'undefined' && DC_STATUS[it.status || '']) ? DC_STATUS[it.status || ''][1] : '';
  return '<b>' + esc(it.name || k) + '</b> <span style="color:#38bdf8;">' + esc(k) + ' · 未监控</span><br>' + (it.model ? esc(it.model) + '<br>' : '') + (it.ip ? esc(it.ip) + '<br>' : '') +
    'U' + it.u + (it.h > 1 ? '~' + (it.u + it.h - 1) : '') + ' · 状态: ' + esc(st) + (it.note ? '<br><span style="color:#fbbf24;">📝 ' + esc(it.note) + '</span>' : '');
}
function pvBindStage() {
  var wrap = byId('rackWrap');
  if (wrap.dataset.pvBound) { return; }
  wrap.dataset.pvBound = '1';
  var tip = byId('rackTip');
  wrap.addEventListener('mousemove', function (e) {
    if (PV.drag || !e.target.closest || e.target.closest('.pv-pop')) { tip.style.display = 'none'; return; }
    var pk = e.target.closest('[data-k]'), dv = e.target.closest('[data-dev]'), itm = e.target.closest('[data-item]'), html = '';
    if (pk && !PV.edit) { html = pnlTipHTML(pk.getAttribute('data-k')); }
    else if (dv) { html = pvDevTip(pvDevByIp(dv.getAttribute('data-dev'))); }
    else if (itm) { html = pvItemTip(pvItemById(itm.getAttribute('data-item'))); }
    if (!html) { tip.style.display = 'none'; return; }
    pvPlaceTip(wrap, tip, e, html);
  });
  wrap.addEventListener('mouseleave', function () { tip.style.display = 'none'; });
  wrap.addEventListener('mousedown', pvDragStart);
  wrap.addEventListener('click', function (e) {
    if (Date.now() - PV.dragEnd < 350 || !e.target.closest || e.target.closest('.pv-pop') || e.target.closest('button')) { return; }
    tip.style.display = 'none';
    var pk = e.target.closest('[data-k]'), dv = e.target.closest('[data-dev]'), itm = e.target.closest('[data-item]');
    if (dv) { pvDevPop(dv.getAttribute('data-dev'), e, pk && !PV.edit ? pk.getAttribute('data-k') : ''); return; }
    if (itm) { pvItemPop(itm.getAttribute('data-item'), e); return; }
    pvClosePop();
  });
  window.addEventListener('mousemove', pvDragMove);
  window.addEventListener('mouseup', pvDragEnd);
}
function pvClosePop() { var p = byId('pvPop'); if (p) { p.remove(); } }
function pvShowPop(e, html, w) {
  pvClosePop();
  var wrap = byId('rackWrap'), rect = wrap.getBoundingClientRect(), pop = document.createElement('div');
  pop.id = 'pvPop'; pop.className = 'pv-pop'; pop.innerHTML = '<button class="pv-x" onclick="pvClosePop()">×</button>' + html;
  if (w) { pop.style.width = w + 'px'; }
  wrap.appendChild(pop);
  var pw = pop.offsetWidth, x = e.clientX - rect.left + 16;
  if (x + pw > rect.width - 8) { x = e.clientX - rect.left - pw - 16; }
  pop.style.left = Math.max(4, x) + 'px';
  pop.style.top = Math.max(4, e.clientY - rect.top - 30) + 'px';
  floatBox(pop, wrap, 'pvPop');
  return pop;
}
function pvPosText(d) {
  var k = PV.layout && PV.layout.racks.filter(function (r) { return r.name === d.rack; })[0];
  var occ = PV.occ[d.rack] || {}, us = Object.keys(occ).filter(function (u) { return occ[u] === 'd:' + d.ip; }).map(Number).sort(function (a, b) { return a - b; });
  return esc(d.rack) + (us.length ? ' · U' + us[0] + (us.length > 1 ? '~' + us[us.length - 1] : '') : '') + (d.manual && d.u > 0 ? '' : ' (自动排)') + (k && k.auto ? '' : '');
}
function pvDevPop(ip, e, pk) {
  var d = pvDevByIp(ip);
  if (!d) { return; }
  var up = d.ports.filter(function (p) { return p.s === 'UP'; }).length, prob = d.ports.filter(pnlProblem).length;
  var kv = function (k, v) { return '<span>' + k + '</span><span>' + v + '</span>'; };
  var html = '<h4>' + (d.alive ? (d.alerts ? '🔴' : '🟢') : '🟠') + ' ' + esc(d.name) + '</h4><div class="sub">' + esc(d.ip) + ' · ' + esc(pnlBrand(d.brand)[0]) + (d.model ? ' ' + esc(d.model) : '') + '</div>' +
    '<div class="pv-kv">' + kv('状态', d.alive ? (d.alerts ? '<b style="color:var(--bad);">' + d.alerts + ' 条未恢复告警</b>' : '在线') : '<b style="color:var(--warn);">离线</b>') +
    (d.alive ? kv('CPU / 内存', d.cpu + '% / ' + d.mem + '%') + (d.temp && d.temp !== '-' ? kv('温度', esc(d.temp)) : '') : '') +
    kv('电源 / 风扇', esc(d.power || '-') + ' / ' + esc(d.fan || '-')) +
    kv('端口', up + '/' + d.ports.length + ' UP' + (prob ? ' · <b style="color:var(--bad);">' + prob + ' 个有问题</b>' : '')) +
    kv('形态', esc(glFormText(d.form))) + kv('位置', pvPosText(d)) + '</div>' +
    pvZoomHTML(ip) +
    (d.note ? '<div class="pv-note">📝 ' + esc(d.note) + '</div>' : '') +
    (pk ? '<div class="pv-port">' + pnlTipHTML(pk) + '</div>' : '') +
    '<div class="pv-btns"><button class="hbtn" onclick="pvGoDetail(&quot;' + esc(ip) + '&quot;,0)">📋 设备详情</button>' +
    '<button class="filter-btn" onclick="pvGoDetail(&quot;' + esc(ip) + '&quot;,9,' + (pk ? '&quot;' + esc(PNL.ports[pk] ? PNL.ports[pk].p.n : '') + '&quot;' : '&quot;&quot;') + ')">🔌 端口面板</button>' +
    (pk ? '<button class="filter-btn" onclick="pvGoDetail(&quot;' + esc(ip) + '&quot;,1,&quot;' + esc(PNL.ports[pk] ? PNL.ports[pk].p.n : '') + '&quot;)">📄 端口表</button>' : '') +
    '<button class="filter-btn" data-perm="datacenter" onclick="pvDevEdit(&quot;' + esc(ip) + '&quot;)">📐 位置 / 形态</button></div><div id="pvEditBox"></div>';
  pvShowPop(e, html);
  applyPermUI();
  if (PV.edit) { pvDevEdit(ip); }
}
// 2D 机柜里单独放大一台设备(识别成 1U 但端口很多、挤得看不清时用); 只存在这台电脑的浏览器里
function pvZoomHTML(ip) {
  var z = +(pvPrefs().zoom || {})[ip] || 1;
  return '<div class="pv-zoom"><span>显示高度</span>' + [1, 1.5, 2, 3].map(function (v) {
    return '<button class="' + (z === v ? 'on' : '') + '" onclick="pvSetZoom(&quot;' + esc(ip) + '&quot;,' + v + ')">' + v + '×</button>';
  }).join('') + '<span class="faint" title="只放大这台设备在 2D 机柜里的显示, 不改它占的 U 位; 只保存在当前浏览器">只放大显示</span></div>';
}
function pvSetZoom(ip, v) {
  var p = pvPrefs();
  if (v > 1) { p.zoom[ip] = v; } else { delete p.zoom[ip]; }
  pvSavePrefs();
  pvClosePop();
  renderRacks();
  if (PV.edit && v > 1) { uiToast('编辑机柜时按等高 U 显示, 完成编辑后才会放大', 'info'); }
}
function pvGoDetail(ip, tab, port) {
  if (port && tab === 9) { PNL.flash = port; }
  openModal(ip, tab || null);
  if (port && tab === 1) {
    setTimeout(function () { var ps = byId('portSearch'); if (ps) { ps.value = port; renderPorts(); } }, 900);
  }
}
function pvDevEdit(ip) {
  if (!authenticated) { openAuthGate(function () { pvDevEdit(ip); }); return; }
  var d = pvDevByIp(ip), box = byId('pvEditBox');
  if (!d || !box) { return; }
  var names = PV.layout.racks.filter(function (k) { return (k.type || 'rack') === 'rack'; }).map(function (k) { return k.name; }).sort(naturalCmp);
  box.innerHTML = '<hr style="border:0;border-top:1px solid var(--card-border);margin:10px 0;">' +
    '<div class="sub">机柜(留空 = 按 sysLocation 自动归类)</div><input id="rpRack" list="rpRackList" value="' + esc(d.manual ? d.rack : '') + '" placeholder="' + esc(d.rack) + '">' +
    '<datalist id="rpRackList">' + names.map(function (n) { return '<option value="' + esc(n) + '">'; }).join('') + '</datalist>' +
    '<div class="sub">最上面一台所在的 U 号(0 = 自动排)</div><input id="rpU" type="number" min="0" max="60" value="' + (d.u || 0) + '">' +
    pnlFormEditorHTML(d, 'rp') +
    '<div style="display:flex;gap:8px;align-items:center;"><button class="hbtn" onclick="rackSaveDevice(&quot;' + esc(ip) + '&quot;)">💾 保存</button>' +
    '<span id="rpMsg" style="font-size:12px;"></span></div>';
}
function rackSaveDevice(ip) {
  var d = pvDevByIp(ip);
  authedApi('/api/racks/assign', { ip: ip, rack: byId('rpRack').value, u: parseInt(byId('rpU').value, 10) || 0 }).then(function () {
    return pnlSaveForm(ip, d, 'rp');
  }).then(function () {
    pvClosePop();
    uiToast('已保存 ' + (d ? d.name : ip) + ' 的位置', 'ok');
    loadRacks();
  }).catch(function (e) { byId('rpMsg').innerText = '❌ ' + e; });
}
function pvItemPop(id, e) {
  var it = pvItemById(id);
  if (!it) { return; }
  var rk = pvRackById(it.rack);
  pvShowPop(e, '<h4>' + esc(it.name || it.kind) + '</h4>' + '<div class="sub">未监控设备 · 在 3D 机房里添加 / 修改</div><div style="margin:8px 0;line-height:1.7;">' + pvItemTip(it) +
    (rk ? '<br>机柜: ' + esc(rk.name) : '') + '</div><div class="pv-btns"><button class="filter-btn" onclick="rackSetView(&quot;3d&quot;)">🏢 去 3D 机房编辑</button></div>');
}
function toggleRackEdit() {
  if (!authenticated) { openAuthGate(function () { toggleRackEdit(); }); return; }
  if (typeof can === 'function' && !can('datacenter')) { uiToast('当前账号没有「机房 / 机柜」权限', 'err'); return; }
  PV.edit = !PV.edit;
  pvClosePop();
  pvHint();
  renderRacks();
}
// 机柜设置: 高度 / 颜色; 自动机柜(sysLocation 生成的)保存后变成正式机柜
function pvRackPop(e, rid) {
  e.stopPropagation();
  var k = pvRackById(rid);
  if (!k) { return; }
  pvShowPop(e, '<h4>⚙ ' + esc(k.name) + '</h4>' + (k.auto ? '<div class="sub">这是按 sysLocation 自动生成的机柜, 保存后会变成正式机柜(3D 机房里也能看到)。</div>' : '') +
    '<label class="sub" style="display:block;margin-top:8px;">高度(U, 6~60)<input id="pvRkH" type="number" min="6" max="60" value="' + (k.height || 42) + '"></label>' +
    '<label class="sub" style="display:block;">标识色<input id="pvRkC" type="color" value="' + esc(k.color || '#38bdf8') + '" style="height:34px;padding:2px;"></label>' +
    '<label style="font-size:12px;display:flex;gap:6px;align-items:center;margin-bottom:8px;"><input type="checkbox" id="pvRkNoC" style="width:auto;margin:0;"' + (k.color ? '' : ' checked') + '> 不用标识色</label>' +
    '<div style="display:flex;gap:8px;align-items:center;"><button class="hbtn" onclick="pvSaveRack(&quot;' + esc(rid) + '&quot;)">💾 保存</button><span id="pvRkMsg" style="font-size:12px;"></span></div>', 280);
}
function pvSaveRack(rid) {
  var h = parseInt(byId('pvRkH').value, 10), color = byId('pvRkNoC').checked ? '' : byId('pvRkC').value;
  if (!(h >= 6 && h <= 60)) { byId('pvRkMsg').innerText = '❌ 高度需要在 6~60U'; return; }
  var L = JSON.parse(JSON.stringify(PV.saved)), k = L.racks.filter(function (x) { return x.id === rid; })[0];
  if (!k) {
    var a = pvRackById(rid);
    if (!a) { return; }
    k = { id: 'r' + Date.now().toString(36), name: a.name, room: a.room, x: a.x, z: a.z, rot: a.rot || 0, height: 42, type: 'rack' };
    L.racks.push(k);
  }
  k.height = h; k.color = color;
  pvSaveLayout(L, '机柜「' + k.name + '」已保存').then(function (ok) { if (ok) { pvClosePop(); } });
}
function pvSaveLayout(L, msg) {
  return authedApi('/api/datacenter/save', { layout: L }).then(function () {
    uiToast(msg, 'ok');
    loadRacks();
    return true;
  }).catch(function (e) { uiToast('保存失败: ' + e, 'err'); loadRacks(); return false; });
}
function pvSvgPt(svg, e) {
  var pt = svg.createSVGPoint();
  pt.x = e.clientX; pt.y = e.clientY;
  return pt.matrixTransform(svg.getScreenCTM().inverse());
}
function pvDragStart(e) {
  if (!PV.edit || e.button !== 0 || !e.target.closest) { return; }
  var g = e.target.closest('[data-dev]') || e.target.closest('[data-item]');
  var svg = g && g.ownerSVGElement;
  if (!svg || !svg.classList.contains('pv-rsvg')) { return; }
  var isItem = g.hasAttribute('data-item'), id = g.getAttribute(isItem ? 'data-item' : 'data-dev');
  var occ = PV.occ[svg.getAttribute('data-rack')] || {}, me = (isItem ? 'i:' : 'd:') + id;
  var us = Object.keys(occ).filter(function (u) { return occ[u] === me; }).map(Number);
  if (!us.length) { return; }
  var top = Math.max.apply(null, us), h = us.length, U = +svg.getAttribute('data-u'), uh = +svg.getAttribute('data-uh');
  var grabU = U - Math.floor((pvSvgPt(svg, e).y - PV_T) / uh);
  PV.drag = { item: isItem, id: id, me: me, h: h, off: top - grabU, x: e.clientX, y: e.clientY, on: false, g: g, target: null };
  e.preventDefault();
}
function pvClearGhost() { document.querySelectorAll('#rackStage .pv-ghostl').forEach(function (l) { l.innerHTML = ''; }); }
function pvDragMove(e) {
  var dr = PV.drag;
  if (!dr) { return; }
  if (!dr.on) {
    if (Math.abs(e.clientX - dr.x) + Math.abs(e.clientY - dr.y) < 6) { return; }
    dr.on = true; dr.g.classList.add('pv-dragging'); byId('rackStage').classList.add('dragging'); pvClosePop();
  }
  // 拖到边缘时滚动
  var mb = byId('rackModal').querySelector('.modal-body'), r = mb.getBoundingClientRect();
  if (e.clientY > r.bottom - 50) { mb.scrollTop += 20; } else if (e.clientY < r.top + 50) { mb.scrollTop -= 20; }
  pvClearGhost();
  var el = document.elementFromPoint(e.clientX, e.clientY), svg = el && el.closest ? el.closest('svg.pv-rsvg') : null;
  if (!svg) { dr.target = null; return; }
  var U = +svg.getAttribute('data-u'), uh = +svg.getAttribute('data-uh');
  var top = pvClamp(U - Math.floor((pvSvgPt(svg, e).y - PV_T) / uh) + dr.off, dr.h, U);
  var occ = PV.occ[svg.getAttribute('data-rack')] || {}, ok = true, why = '';
  for (var u = top; u > top - dr.h; u--) { if (occ[u] && occ[u] !== dr.me) { ok = false; why = '有设备'; } }
  var rid = svg.getAttribute('data-rid');
  if (dr.item && rid.indexOf('auto:') === 0) { ok = false; why = '自动机柜, 先在 ⚙ 里保存成正式机柜'; }
  dr.target = { rack: svg.getAttribute('data-rack'), rid: rid, top: top, ok: ok };
  var y = PV_T + (U - top) * uh, label = (ok ? '放到 ' : '放不下: ') + svg.getAttribute('data-rack') + ' U' + (top - dr.h + 1) + (dr.h > 1 ? '~' + top : '') + (why ? ' (' + why + ')' : '');
  svg.querySelector('.pv-ghostl').innerHTML = pvR('pv-ghost ' + (ok ? 'ok' : 'bad'), PV_R + 1, y + 1, PV_W - 2, dr.h * uh - 2, 3) +
    '<text class="pv-ghostt" x="' + (PV_R + PV_W / 2) + '" y="' + pvN(y + dr.h * uh / 2) + '" text-anchor="middle" font-size="' + pvN(Math.min(18, uh * .5)) + '" fill="' + (ok ? '#22c55e' : '#ef4444') + '" stroke="rgba(0,0,0,.7)" stroke-width="3" paint-order="stroke">' + esc(label) + '</text>';
}
function pvDragEnd() {
  var dr = PV.drag;
  if (!dr) { return; }
  PV.drag = null;
  pvClearGhost();
  byId('rackStage').classList.remove('dragging');
  if (!dr.on) { return; }
  PV.dragEnd = Date.now();
  dr.g.classList.remove('pv-dragging');
  var t = dr.target;
  if (!t) { return; }
  if (!t.ok) { uiToast('这个位置放不下', 'err'); return; }
  if (!dr.item) {
    var d = pvDevByIp(dr.id);
    authedApi('/api/racks/assign', { ip: dr.id, rack: t.rack, u: t.top }).then(function () {
      uiToast((d ? d.name : dr.id) + ' → ' + t.rack + ' U' + t.top, 'ok');
      loadRacks();
    }).catch(function (e) { uiToast('保存失败: ' + e, 'err'); });
  } else {
    var L = JSON.parse(JSON.stringify(PV.saved)), it = L.items.filter(function (x) { return x.id === dr.id; })[0];
    if (!it) { return; }
    it.rack = t.rid; it.u = t.top - it.h + 1;
    pvSaveLayout(L, (it.name || '设备') + ' → ' + t.rack + ' U' + it.u);
  }
}

/* ---------- 设备详情里的面板标签页 ---------- */
detailPart({ tab: 9, show: function () { renderDevicePanel(); } });
function renderDevicePanel() {
  var d = currentDetail, box = byId('panelBox');
  if (!d || !box) { return; }
  PNL.ports = {};
  var ports = (d.ports || []).map(pnlFromDetail);
  // 形态(堆叠 / 框式)在 /api/panels 里, 第一次打开时取一下再重画
  if (PNL.forms[d.ip] === undefined) {
    PNL.forms[d.ip] = null;
    pub('/api/panels?ip=' + encodeURIComponent(d.ip)).then(function (r) {
      var x = (r.devices || [])[0];
      PNL.forms[d.ip] = x ? x.form : null;
      if (currentTab === 9 && currentDetail && currentDetail.ip === d.ip) { renderDevicePanel(); }
    }).catch(function () {});
  }
  var dev = { ip: d.ip, name: d.sys_name || d.ip, brand: d.brand, model: d.inventory && d.inventory.model, alive: d.is_alive, note: d.note,
    alerts: d.open_alerts || 0, power: d.hardware && d.hardware.power_status, fan: d.hardware && d.hardware.fan_status, ports: ports, form: PNL.forms[d.ip] || null };
  var cnt = { up: 0, down: 0, admin: 0, prob: 0, opt: 0, busy: 0 };
  ports.forEach(function (p) {
    if (p.s === 'UP') { cnt.up++; } else if (p.s === 'ADMIN-DOWN') { cnt.admin++; } else { cnt.down++; }
    if (pnlProblem(p)) { cnt.prob++; }
    if (p.rx !== null && p.rx !== undefined) { cnt.opt++; }
    if (Math.max(p.iu || 0, p.ou || 0) >= 70) { cnt.busy++; }
  });
  var chip = function (t, v, c) { return '<span class="tag" style="font-size:12.5px;padding:3px 10px;' + (c ? 'color:' + c + ';' : '') + '">' + t + ' <b>' + v + '</b></span>'; };
  var o = pvOpts();
  o.flash = PNL.flash; PNL.flash = '';
  var U = pnlUnits(dev), isCh = dev.form && dev.form.kind === 'chassis';
  // 单独看一台时画大一点: 盒式每 U 64, 框式整机控制在 600 左右; 宽度 / 每 U 高度可以在上面的滑块里调(按设备记住)
  var dp = pvPrefs().dpanel[d.ip] || {}, defUh = isCh ? Math.round(pvClamp(600 / U, 34, 64)) : 64;
  o.uh = pvClamp(+dp.uh || defUh, 30, 200);
  o.maxS = 34 * Math.max(1, o.uh / 64);
  var wPct = pvClamp(+dp.w || 100, 100, 300);
  PNL.cur = { dev: dev, o: o, U: U, defUh: defUh };
  var sel = function (k, opts) {
    return '<select onchange="pvSetPref(&quot;' + k + '&quot;,this.value)" style="padding:4px 8px;border-radius:6px;border:1px solid var(--card-border);background:var(--card-bg);color:var(--fg);font-size:12.5px;">' +
      opts.map(function (x) { return '<option value="' + x[0] + '"' + (pvPrefs()[k] === x[0] ? ' selected' : '') + '>' + x[1] + '</option>'; }).join('') + '</select>';
  };
  var html = '<div class="pnl-summary">' + chip('UP', cnt.up, 'var(--good)') + chip('DOWN', cnt.down) + chip('管理 DOWN', cnt.admin, 'var(--warn)') +
    chip('有问题的口', cnt.prob, cnt.prob ? 'var(--bad)' : '') + chip('利用率≥70%', cnt.busy, cnt.busy ? 'var(--warn)' : '') + chip('带光模块', cnt.opt) +
    '<span style="margin-left:auto;display:flex;gap:6px;align-items:center;font-size:12.5px;color:var(--fg-dim);">着色 ' +
    sel('color', [['status', '链路状态'], ['util', '利用率'], ['speed', '速率'], ['vlan', 'VLAN']]) + ' 主题 ' + sel('theme', [['dark', '深色金属'], ['silver', '银灰'], ['light', '浅色']]) + '</span></div>' +
    '<div class="pv-dsize"><label>宽度 <input type="range" id="pvDpW" min="100" max="300" step="10" value="' + wPct + '" oninput="pvDpanelSet(&quot;' + esc(d.ip) + '&quot;)"><b id="pvDpWv">' + wPct + '%</b></label>' +
    '<label>每 U 高度 <input type="range" id="pvDpH" min="30" max="200" step="2" value="' + o.uh + '" oninput="pvDpanelSet(&quot;' + esc(d.ip) + '&quot;)"><b id="pvDpHv">' + o.uh + ' px</b></label>' +
    '<button class="filter-btn" onclick="pvDpanelReset(&quot;' + esc(d.ip) + '&quot;)" title="恢复默认大小">↺ 默认</button>' +
    '<span class="faint">端口挤在一起时把宽度 / 高度调大(超出窗口可以左右拖动), 只保存在当前浏览器</span></div>' +
    '<div class="pv-devwrap" id="pvDevWrap">' + pvDevPanelSVG(wPct) + '</div>';
  var aggNames = Object.keys(o.aggMap);
  if (aggNames.length) {
    html += '<div class="pnl-agglist">聚合组: ' + aggNames.map(function (n) { return '<span><i style="background:' + o.aggMap[n] + ';"></i>' + esc(n) + '</span>'; }).join('') + '</div>';
  }
  html += pvLegendHTML(o.color) + '<div class="sub faint" style="margin-top:4px;">面板按端口名自动排布(同一堆叠成员 / 板卡一行, 电口 1 在上 2 在下), 不是厂家实物图; 悬停看详情, 点端口跳到端口表。</div>';
  box.innerHTML = html;
  pvApplyTheme();
  pnlBindTip(byId('panelWrap'), byId('panelTip'), function (x) {
    if (!x) { return; }
    switchTab(1);
    byId('portSearch').value = x.p.n;
    renderPorts();
  });
}

// 详情「面板」页里的那块面板: 宽度 wPct% 时整块画得更宽(端口有更多横向空间), 外面可以横向滚动
function pvDevPanelSVG(wPct) {
  var c = PNL.cur, H = c.U * c.o.uh, keep = PV_W;
  PV_W = Math.round(1000 * wPct / 100);
  try {
    return '<svg class="pv-dsvg" style="width:' + wPct + '%;max-width:' + Math.round(1400 * wPct / 100) + 'px;" viewBox="0 -10 ' + PV_W + ' ' + pvN(H + 20) + '">' + pvDevSVG(c.dev, 0, 0, H, c.o) + '</svg>';
  } finally { PV_W = keep; }
}
function pvDpanelSet(ip) {
  var w = +byId('pvDpW').value, h = +byId('pvDpH').value, c = PNL.cur;
  if (!c) { return; }
  byId('pvDpWv').innerText = w + '%'; byId('pvDpHv').innerText = h + ' px';
  var p = pvPrefs();
  if (w === 100 && h === c.defUh) { delete p.dpanel[ip]; } else { p.dpanel[ip] = { w: w, uh: h }; }
  pvSavePrefs();
  c.o.uh = h; c.o.maxS = 34 * Math.max(1, h / 64);
  PNL.ports = {};
  byId('pvDevWrap').innerHTML = pvDevPanelSVG(w);
}
function pvDpanelReset(ip) { delete pvPrefs().dpanel[ip]; pvSavePrefs(); renderDevicePanel(); }

/* ---------- 设备形态编辑(2D 信息卡和 3D 机房共用) ---------- */
function pnlFormEditorHTML(d, pfx) {
  var f = d.form || {};
  var kind = f.manual ? f.kind : '';
  return '<div class="sub">设备形态 <span class="faint">(自动: ' + esc(f.source || '-') + ')</span></div>' +
    '<select id="' + pfx + 'Kind" onchange="pnlFormKindChanged(&quot;' + pfx + '&quot;)" style="width:100%;padding:6px;margin:4px 0 8px;border:1px solid var(--input-border);border-radius:6px;background:var(--card-bg);color:var(--fg);">' +
    [['', '自动识别 (' + glFormText(f.manual ? null : f) + ')'], ['box', '盒式单台'], ['stack', '堆叠(多台叠在一起)'], ['chassis', '框式']].map(function (o) {
      return '<option value="' + o[0] + '"' + (o[0] === kind ? ' selected' : '') + '>' + esc(o[1]) + '</option>';
    }).join('') + '</select>' +
    '<div id="' + pfx + 'FormExtra" style="display:' + (kind ? 'flex' : 'none') + ';gap:8px;">' +
    '<label class="sub" style="flex:1;">堆叠台数<input id="' + pfx + 'Members" type="number" min="2" max="16" value="' + (f.kind === 'stack' ? f.members : 2) + '"' + (kind === 'stack' ? '' : ' disabled') + '></label>' +
    '<label class="sub" style="flex:1;">每台高度(U)<input id="' + pfx + 'UEach" type="number" min="1" max="40" value="' + (f.u_each || 1) + '"></label></div>' +
    // 板卡方向只影响 3D 视图的面板贴图; 2D 面板一律横插
    '<div id="' + pfx + 'OrientBox" data-auto="' + esc(f.manual ? '' : (f.kind || '')) + '" style="display:' + ((kind || f.kind) === 'chassis' ? 'block' : 'none') + ';"><div class="sub">3D 视图里的板卡方向 <span class="faint">(2D 面板一律横插)</span></div>' +
    '<select id="' + pfx + 'Orient" style="width:100%;padding:6px;margin:4px 0 8px;border:1px solid var(--input-border);border-radius:6px;background:var(--card-bg);color:var(--fg);">' +
    [['', '按型号自动' + (f.kind === 'chassis' && !f.orient_manual ? '(当前: ' + (f.orient === 'v' ? '竖插' : '横插') + ')' : '')], ['v', '竖插: 板卡左右并排'], ['h', '横插: 板卡上下叠放']].map(function (o) {
      return '<option value="' + o[0] + '"' + ((f.orient_manual ? f.orient : '') === o[0] ? ' selected' : '') + '>' + esc(o[1]) + '</option>';
    }).join('') + '</select></div>';
}
// 表单 → 保存用的 body; 自动识别是框式、只改了方向时, 按框式 + 原高度保存
function pnlFormBody(d, pfx) {
  var f = d && d.form || {}, k = byId(pfx + 'Kind').value, o = byId(pfx + 'Orient') ? byId(pfx + 'Orient').value : '';
  var body = { kind: k };
  if (k) { body.members = k === 'stack' ? parseInt(byId(pfx + 'Members').value, 10) || 2 : 0; body.u_each = parseInt(byId(pfx + 'UEach').value, 10) || 1; }
  if (o && (k === 'chassis' || (!k && f.kind === 'chassis'))) {
    if (!k) { body = { kind: 'chassis', members: 0, u_each: f.u_each || 10 }; }
    body.orient = o;
  }
  return body;
}
function pnlFormKindChanged(pfx) {
  var k = byId(pfx + 'Kind').value;
  byId(pfx + 'FormExtra').style.display = k ? 'flex' : 'none';
  byId(pfx + 'Members').disabled = k !== 'stack';
  if (k === 'chassis' && parseInt(byId(pfx + 'UEach').value, 10) < 5) { byId(pfx + 'UEach').value = 10; }
  if (k && k !== 'chassis' && parseInt(byId(pfx + 'UEach').value, 10) > 4) { byId(pfx + 'UEach').value = 1; }
  var ob = byId(pfx + 'OrientBox');
  if (ob) { ob.style.display = k === 'chassis' || (!k && ob.dataset.auto === 'chassis') ? 'block' : 'none'; }
}
function pnlSaveForm(ip, d, pfx) {
  var f = d && d.form || {};
  var body = pnlFormBody(d, pfx);
  // 没改就不写
  if (!body.kind && !f.manual) { return Promise.resolve(); }
  if (body.kind && f.manual && f.kind === body.kind && (body.kind !== 'stack' || f.members === body.members) && f.u_each === body.u_each && (body.orient || '') === (f.orient_manual ? f.orient : '')) { return Promise.resolve(); }
  delete PNL.forms[ip];
  return authedApi('/api/datacenter/save', { form_ip: ip, form: body });
}
modalRegister('rackModal', closeRacks, { esc: function () { if (byId('pvPop')) { pvClosePop(); return true; } return false; } });
