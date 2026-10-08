/*
 * 2D 网络拓扑(v12 重写)。
 *
 * 设计:
 *   - 直接操作: 节点随时可以拖(不用先进"编辑模式"), 右上角 🔒 可以锁定布局防误触;
 *     空白处拖动 = 平移, Shift+拖动(或切到「⬚ 框选」)= 框选, 滚轮 = 以鼠标为中心缩放,
 *     Ctrl/Shift+单击 = 加选, 拖动任意一个选中的节点整组移动, 方向键微调。
 *   - 单击节点 = 右侧信息面板(状态/备注/链路, 不跳走); 双击 = 打开设备详情。
 *   - 右键菜单: 节点(详情/连线/选中邻居/编辑/删除)、链路(查看/隐藏/转手工/删除)、空白(在这里加节点/编排)。
 *   - 自动编排: 分层 / 径向 / 力导向 / 网格, 选中 2 个以上时只编排选中的;「✨ 整理重叠」只把挤在一起的
 *     节点推开、其余不动。所有位置改动都能撤销/重做(Ctrl+Z / Ctrl+Y)。
 *   - 画布不再限死在底图大小里, 节点多了可以往外摆; 右下角小地图可以点击跳转。
 *   - 显示选项: 设备备注、链路两端端口名、流量流动动画、未监控邻居(显示 / 折叠成 +N / 隐藏)。
 *   - 保存: 有「拓扑编辑」权限时自动保存(批量); 没登录也能拖着看, 会提示登录后保存。
 *
 * 对外的入口(renderTopology / topoZoom / topoResetView / topoHighlight / closeTopology)在 topology/view.js,
 * 按当前是 2D 还是 3D 分派到这里的 tp2Render / tp2Zoom / tp2Reset / tp2Highlight / tp2Close。
 */
/* ================= 2D 网络拓扑 (v12) ================= */
var TP = {
  data: null, W: 1600, H: 1000, nodes: [], byKey: {}, links: [], extByDev: {}, extCount: {}, roles: {},
  pos: {}, extPos: {}, view: { s: 1, tx: 0, ty: 0 }, sel: {}, selLink: null, drag: null,
  undo: [], redo: [], pend: {}, pendClear: {}, saveTimer: null, timer: null, keep: false,
  side: null, menu: false, linkMode: null, tool: 'pan', extOpen: {}, raf: 0, bannerShown: false,
  opt: { note: true, ports: false, flow: true, ext: 'auto', locked: false, bgAlpha: 1, groups: true }
};
var TP_KIND_ICON = { firewall: '🔥', router: '🛰', server: '🖥', cloud: '☁', isp: '🌐', ap: '📶', wireless: '📡', camera: '📷', switch: '🔀', other: '◆' };
var TP_KIND_NAME = { firewall: '防火墙', router: '路由器', server: '服务器', cloud: '云 / 外网', isp: '运营商线路', ap: '无线 AP', wireless: '无线控制器', camera: '摄像头', switch: '交换机(非网管)', other: '其他' };
var TP_SRC_NAME = { lldp: 'LLDP', cdp: 'CDP', mac: 'MAC 表推断', manual: '手工' };
var TP_ROLE_COLOR = { core: '#0ea5e9', agg: '#8b5cf6', access: '#10b981', lone: '#64748b' };
var TP_ROLE_NAME = { core: '核心', agg: '汇聚', access: '接入', lone: '孤立' };
var TP_NODE_W = 132, TP_NODE_H = 78; // 节点(含文字)占位, 编排和整理重叠按这个算
// 这几个全局变量 3D 视图 / 大屏 / 旧的提示框代码还在读
var topoData = null, topoEditMode = false, topoImgVersion = 0, topoW = 1600, topoH = 1000;
(function () { try { var o = JSON.parse(localStorage.getItem('tp2opt') || '{}'); Object.keys(o).forEach(function (k) { if (k in TP.opt) { TP.opt[k] = o[k]; } }); } catch (e) {} })();
function tpSaveOpt() { try { localStorage.setItem('tp2opt', JSON.stringify(TP.opt)); } catch (e) {} }
function truncateLabel(s, n) { s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; }
function toggleTopoEdit() { tpToggleLock(); } // 兼容旧入口

/* ---------- 打开 / 关闭 / 拉数据 ---------- */
function openTopology() {
  byId('topoModal').style.display = 'flex';
  // v13: 整个拓扑窗口(2D / 3D 都算)可以全屏; 3D 画面里还有单独只全屏画布的按钮
  if (!byId('topoFs').firstChild) { fsButton(byId('topoModal').querySelector('.tpm'), byId('topoFs')); fsHotkeyZone(byId('topoModal').querySelector('.tpm')); }
  byId('topoWrap').innerHTML = '<div class="empty">加载中...</div>';
  byId('topoNotice').style.display = 'none';
  TP.pos = {}; TP.sel = {}; TP.selLink = null; TP.undo = []; TP.redo = []; TP.side = null; TP.linkMode = null; TP.extOpen = {};
  tpSyncSeg();
  tpFetch(true);
  if (TP.timer) { clearInterval(TP.timer); }
  TP.timer = setInterval(function () {
    if (byId('topoModal').style.display === 'none' || T3.on) { return; }
    if (TP.drag || TP.linkMode || (TP.side && TP.side.form) || TP.menu) { return; }
    tpFetch(false);
  }, Math.max(10, uiPref('topo_refresh_seconds', 30)) * 1000);
}
function tp2Close() {
  tpFlushSave();
  byId('topoModal').style.display = 'none';
  if (TP.timer) { clearInterval(TP.timer); TP.timer = null; }
  tpHideMenu();
}
function tpFetch(first) {
  return pub('/api/topology').then(function (data) {
    data.devices = data.devices || []; data.manual_nodes = data.manual_nodes || []; data.links = data.links || []; data.externals = data.externals || [];
    topoData = data;
    if (!first) { TP.keep = true; }
    if (data.has_background) {
      var img = new Image();
      img.onload = function () {
        var w = img.naturalWidth || 1600, h = img.naturalHeight || 1000, m = 3000;
        if (w > m || h > m) { var k = Math.min(m / w, m / h); w = Math.round(w * k); h = Math.round(h * k); }
        TP.W = topoW = w; TP.H = topoH = h; renderTopology(data);
      };
      img.onerror = function () { TP.W = topoW = 1600; TP.H = topoH = 1000; renderTopology(data); };
      img.src = '/api/topology/background?v=' + topoImgVersion;
    } else { TP.W = topoW = 1600; TP.H = topoH = 1000; renderTopology(data); }
  }).catch(function (e) { if (first) { byId('topoWrap').innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '</div>'; } });
}
function tpSyncSeg() {
  var on3 = T3.on;
  var a = byId('tpSeg2d'), b = byId('topo3dBtn');
  if (a) { a.classList.toggle('on', !on3); } if (b) { b.classList.toggle('on', on3); }
}
function tpSet3D(on) { if (T3.on === on) { tpSyncSeg(); return; } toggleTopo3D(); tpSyncSeg(); }

/* ---------- 数据整理 ---------- */
function tpBuild(data) {
  var nodes = [], byKey = {};
  data.devices.forEach(function (d) { var n = { key: d.ip, t: 'dev', d: d, name: d.name || d.ip }; nodes.push(n); byKey[n.key] = n; });
  data.manual_nodes.forEach(function (m) { var n = { key: 'm:' + m.id, t: 'man', d: m, name: m.name }; nodes.push(n); byKey[n.key] = n; });
  var links = tpBundleLinks(data.links.filter(function (l) { return byKey[l.a] && byKey[l.b]; }));
  links.forEach(function (l, i) { l._i = i; });
  var extByDev = {}, extCount = {};
  data.externals.forEach(function (e, i) { e._i = i; (extByDev[e.device] = extByDev[e.device] || []).push(e); extCount[e.device] = (extCount[e.device] || 0) + 1; });
  TP.nodes = nodes; TP.byKey = byKey; TP.links = links; TP.extByDev = extByDev; TP.extCount = extCount;
  TP.roles = tpRoles(nodes.map(function (n) { return n.key; }), links);
  if (TP.opt.ext === 'auto') { TP.extMode = data.externals.length > 40 ? 'fold' : 'show'; } else { TP.extMode = TP.opt.ext; }
}
function tpPair(a, b) { return a < b ? a + '|' + b : b + '|' + a; }

// v13: 同一对设备之间只画一根线。
// 以前两台设备之间有几条链路就画几根弧线; 加上后端 LLDP 两端配不上对时一条物理链路会被算成两条,
// 看起来就是两根线。现在后端已经把两端上报合并好, 前端再把同一对设备之间的所有链路(多条独立
// 物理链路 / 手工链路)捆成一根线: 粗细按总带宽, 线上标 ×N, 点开信息面板逐条列出, 每条都能单独
// 隐藏 / 编辑 / 删除。跟自动发现重复的手工链路(dup_of)不画。
function tpBundleLinks(list) {
  var by = {}, order = [];
  list.forEach(function (l) {
    if (l.dup_of) { return; }
    var k = tpPair(l.a, l.b);
    if (!by[k]) { by[k] = []; order.push(k); }
    by[k].push(l);
  });
  var rank = { lldp: 0, cdp: 1, manual: 2, mac: 3 };
  return order.map(function (k) {
    var m = by[k];
    if (m.length === 1) { return m[0]; }
    var a = m[0].a, b = m[0].b;
    var out = { a: a, b: b, _bundle: m, source: m[0].source, a_speed_mbps: 0, b_speed_mbps: 0, a_in_mbps: 0, a_out_mbps: 0, b_in_mbps: 0, b_out_mbps: 0, is_agg: false, key: '' };
    var ap = [], bp = [];
    m.forEach(function (l) {
      var o = tpOrient(l, a);
      out.a_speed_mbps += o.aSp; out.b_speed_mbps += o.bSp; out.a_in_mbps += o.aIn; out.a_out_mbps += o.aOut; out.b_in_mbps += o.bIn; out.b_out_mbps += o.bOut;
      if (o.aPort) { ap.push(o.aPort); } if (o.bPort) { bp.push(o.bPort); }
      if (rank[l.source] < rank[out.source]) { out.source = l.source; }
      if (l.is_agg) { out.is_agg = true; }
    });
    out.a_port = ap.join(', '); out.b_port = bp.join(', ');
    return out;
  });
}
// 以 a 为本端看一条链路(链路里的 a/b 可能是反的)
function tpOrient(l, a) {
  var s = l.a === a;
  return { aPort: s ? l.a_port : l.b_port, bPort: s ? l.b_port : l.a_port, aSp: (s ? l.a_speed_mbps : l.b_speed_mbps) || 0, bSp: (s ? l.b_speed_mbps : l.a_speed_mbps) || 0,
    aIn: (s ? l.a_in_mbps : l.b_in_mbps) || 0, aOut: (s ? l.a_out_mbps : l.b_out_mbps) || 0, bIn: (s ? l.b_in_mbps : l.a_in_mbps) || 0, bOut: (s ? l.b_out_mbps : l.a_out_mbps) || 0 };
}
function tpMembers(l) { return l._bundle || [l]; }
function tpAdj(keys, links) {
  var adj = {};
  keys.forEach(function (k) { adj[k] = []; });
  links.forEach(function (l) {
    if (!adj[l.a] || !adj[l.b] || l.a === l.b) { return; }
    var w = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0, 1000) * (l.is_agg ? 2 : 1);
    adj[l.a].push({ k: l.b, w: w }); adj[l.b].push({ k: l.a, w: w });
  });
  return adj;
}
// 每个连通分量选一个根(名字像核心的优先, 其次加权度数), BFS 得到层级
function tpLevels(keys, links) {
  var adj = tpAdj(keys, links), level = {}, comps = [], seen = {};
  var score = function (k) {
    var s = 0; adj[k].forEach(function (x) { s += Math.log(x.w) + 1; });
    var n = TP.byKey[k]; if (!n) { return s; }
    if (n.t === 'man') { return s - 6; }
    var nm = String(n.name).toLowerCase();
    if (/core|核心|spine|^hx|-hx/.test(nm)) { s += 12; } else if (/agg|汇聚|dist/.test(nm)) { s += 4; } else if (/acc|接入|access/.test(nm)) { s -= 4; }
    return s;
  };
  keys.slice().sort(function (a, b) { return score(b) - score(a) || (a < b ? -1 : 1); }).forEach(function (root) {
    if (seen[root]) { return; }
    var comp = [], q = [root]; seen[root] = true; level[root] = 0;
    while (q.length) {
      var k = q.shift(); comp.push(k);
      adj[k].slice().sort(function (a, b) { return b.w - a.w; }).forEach(function (x) { if (!seen[x.k]) { seen[x.k] = true; level[x.k] = level[k] + 1; q.push(x.k); } });
    }
    comps.push(comp);
  });
  return { level: level, comps: comps, adj: adj };
}
function tpRoles(keys, links) {
  var L = tpLevels(keys, links), roles = {};
  keys.forEach(function (k) { var deg = L.adj[k].length, lv = L.level[k]; roles[k] = deg === 0 ? 'lone' : lv === 0 ? 'core' : (deg > 1 && lv === 1) ? 'agg' : 'access'; });
  return roles;
}
function tpNameOf(k) { var n = TP.byKey[k]; return n ? n.name : k; }

/* ---------- 初始坐标 ---------- */
function tpInitPos(data, keepOld) {
  var pos = {}, missing = [], W = TP.W, H = TP.H;
  TP.nodes.forEach(function (n) {
    if (keepOld && TP.pos[n.key]) { pos[n.key] = TP.pos[n.key]; return; }
    if (n.d.has_pos) { pos[n.key] = { x: n.d.x / 100 * W, y: n.d.y / 100 * H }; } else { missing.push(n.key); }
  });
  if (missing.length) {
    if (!Object.keys(pos).length) {
      var lay = tpLayout('tree', missing); missing.forEach(function (k) { pos[k] = lay[k]; });
    } else {
      // 新加的设备: 放到已经摆好的邻居旁边, 没有邻居就放到现有布局下方一排
      var adj = tpAdj(TP.nodes.map(function (n) { return n.key; }), TP.links), b = tpBBoxOf(pos), row = 0;
      missing.forEach(function (k) {
        var nb = adj[k].filter(function (x) { return pos[x.k]; })[0];
        if (nb) { var p = pos[nb.k]; pos[k] = { x: p.x + (Math.random() - 0.5) * 60, y: p.y + TP_NODE_H * 1.6 }; }
        else { pos[k] = { x: b.x0 + (row % 10) * TP_NODE_W * 1.15, y: b.y1 + TP_NODE_H * (1.6 + Math.floor(row / 10) * 1.2) }; row++; }
      });
      tpDeclutterInto(pos, missing);
    }
  }
  TP.pos = pos;
}
function tpExtAuto(e, i, list) {
  var dp = TP.pos[e.device]; if (!dp) { return null; }
  // 叶子排成扇形, 朝向远离这台设备其他连线的方向
  var sx = 0, sy = 0, c = 0;
  TP.links.forEach(function (l) { var o = l.a === e.device ? l.b : l.b === e.device ? l.a : null; if (o && TP.pos[o]) { sx += TP.pos[o].x - dp.x; sy += TP.pos[o].y - dp.y; c++; } });
  var base = c ? Math.atan2(-sy, -sx) : Math.PI / 2;
  var per = 14, ring = Math.floor(i / per), idx = i % per, n = Math.min(per, list.length - ring * per);
  var spread = Math.min(Math.PI * 1.3, 0.35 + n * 0.17);
  var ang = base - spread / 2 + (n > 1 ? spread * idx / (n - 1) : spread / 2), dist = 78 + ring * 30;
  return { x: dp.x + dist * Math.cos(ang), y: dp.y + dist * Math.sin(ang) };
}
function tpComputeExtPos() {
  TP.extPos = {};
  Object.keys(TP.extByDev).forEach(function (dev) {
    if (!tpExtVisible(dev)) { return; }
    TP.extByDev[dev].forEach(function (e, i, list) { TP.extPos[e.key] = e.has_pos && !e._auto ? { x: e.x / 100 * TP.W, y: e.y / 100 * TP.H } : tpExtAuto(e, i, list); });
  });
}
function tpExtVisible(dev) { return TP.extMode === 'show' || (TP.extMode === 'fold' && TP.extOpen[dev]); }

/* ---------- 自动编排 ---------- */
// 返回 { key: {x,y} }; 间距按节点占位算, 放不下就往外扩(不再硬塞进一个框里)
function tpLayout(kind, keys) {
  var set = {}; keys.forEach(function (k) { set[k] = true; });
  var links = TP.links.filter(function (l) { return set[l.a] && set[l.b]; });
  var out = {}, gx = TP_NODE_W * 1.12, gy = TP_NODE_H * 1.9;
  if (!keys.length) { return out; }
  var byName = function (a, b) { return naturalCmp(tpNameOf(a), tpNameOf(b)); };
  if (kind === 'group') { return tpGroupLayout(keys); }
  if (kind === 'grid') {
    var s = keys.slice().sort(byName), cols = Math.max(1, Math.round(Math.sqrt(s.length * 1.7)));
    s.forEach(function (k, i) { out[k] = { x: (i % cols) * gx * 1.15, y: Math.floor(i / cols) * TP_NODE_H * 1.5 }; });
    return out;
  }
  var L = tpLevels(keys, links);
  var multi = [], lone = [];
  L.comps.forEach(function (c) { if (c.length === 1) { lone.push(c[0]); } else { multi.push(c); } });
  lone.sort(byName);
  // 每个分量按层分组, 同层按上一层父节点的平均位置排序(两轮上下扫), 减少交叉
  var levelsOf = function (comp) {
    var lv = [], order = {};
    comp.forEach(function (k) { var l = L.level[k]; (lv[l] = lv[l] || []).push(k); });
    lv.forEach(function (row, li) {
      if (li === 0) { row.sort(byName); row.forEach(function (k, i) { order[k] = i; }); return; }
      row.forEach(function (k) { var ps = L.adj[k].filter(function (x) { return L.level[x.k] === li - 1; }); order[k] = ps.length ? ps.reduce(function (s, x) { return s + order[x.k]; }, 0) / ps.length : 1e6; });
      row.sort(function (a, b) { return order[a] - order[b] || byName(a, b); });
      row.forEach(function (k, i) { order[k] = i; });
    });
    for (var li = lv.length - 2; li >= 1; li--) {
      lv[li].forEach(function (k) { var cs = L.adj[k].filter(function (x) { return L.level[x.k] === li + 1; }); if (cs.length) { order[k] = (order[k] + cs.reduce(function (s, x) { return s + order[x.k]; }, 0) / cs.length) / 2; } });
      lv[li].sort(function (a, b) { return order[a] - order[b]; }); lv[li].forEach(function (k, i) { order[k] = i; });
    }
    return lv;
  };
  if (kind === 'radial') {
    multi.sort(function (a, b) { return b.length - a.length; });
    var cx = 0, cy = 0, maxR = 0;
    multi.forEach(function (comp, ci) {
      var lv = levelsOf(comp), R = 0, pts = {};
      lv.forEach(function (row, li) {
        if (li === 0 && row.length === 1) { pts[row[0]] = { x: 0, y: 0 }; return; }
        R = Math.max(R + gy * 0.95, row.length * gx * 0.95 / (2 * Math.PI));
        row.forEach(function (k, i) { var a = -Math.PI / 2 + 2 * Math.PI * (i + (li % 2 ? 0.5 : 0)) / row.length; pts[k] = { x: R * Math.cos(a), y: R * Math.sin(a) }; });
      });
      if (ci > 0) { cx += maxR + R + gx; } else { maxR = R; }
      Object.keys(pts).forEach(function (k) { out[k] = { x: cx + pts[k].x, y: cy + pts[k].y }; });
      if (ci > 0) { maxR = R; }
    });
    var b0 = tpBBoxOf(out); if (b0.x0 === Infinity) { b0 = { x0: 0, x1: 0, y0: 0, y1: 0 }; }
    lone.forEach(function (k, i) { var cols = Math.max(4, Math.round(Math.sqrt(lone.length * 2))); out[k] = { x: b0.x0 + (i % cols) * gx, y: b0.y1 + gy + Math.floor(i / cols) * TP_NODE_H * 1.4 }; });
    return out;
  }
  if (kind === 'tree') {
    var xCur = 0, maxY = 0, maxPer = 14;
    multi.sort(function (a, b) { return b.length - a.length; });
    multi.forEach(function (comp) {
      var lv = levelsOf(comp), widest = 0, y = 0, rows = [];
      lv.forEach(function (row) { for (var s = 0; s < row.length; s += maxPer) { rows.push({ ks: row.slice(s, s + maxPer), sub: s > 0 }); } });
      rows.forEach(function (r) { widest = Math.max(widest, r.ks.length); });
      var w = widest * gx;
      rows.forEach(function (r, ri) {
        var off = (w - r.ks.length * gx) / 2 + gx / 2;
        r.ks.forEach(function (k, i) { out[k] = { x: xCur + off + i * gx, y: y }; });
        y += r.sub || (rows[ri + 1] && rows[ri + 1].sub) ? gy * 0.72 : gy;
      });
      maxY = Math.max(maxY, y); xCur += w + gx * 0.8;
    });
    var cols2 = Math.max(6, Math.round((xCur || gx * 8) / gx));
    lone.forEach(function (k, i) { out[k] = { x: gx / 2 + (i % cols2) * gx, y: maxY + TP_NODE_H * 0.6 + Math.floor(i / cols2) * TP_NODE_H * 1.35 }; });
    return out;
  }
  // 力导向: 以分层结果为初值, 斥力 + 弹簧 + 重叠约束
  var init = tpLayout('tree', keys), p = {}, n = keys.length, kk = gx * 1.25;
  keys.forEach(function (k) { p[k] = { x: init[k].x, y: init[k].y, dx: 0, dy: 0 }; });
  var t = kk * 2;
  for (var it = 0; it < 300; it++) {
    keys.forEach(function (a) { p[a].dx = 0; p[a].dy = 0; });
    for (var i = 0; i < n; i++) {
      for (var j = i + 1; j < n; j++) {
        var A = p[keys[i]], B = p[keys[j]], dx = A.x - B.x, dy = A.y - B.y, d = Math.sqrt(dx * dx + dy * dy) || 0.1;
        if (d > kk * 6) { continue; }
        var f = kk * kk / d; A.dx += dx / d * f; A.dy += dy / d * f; B.dx -= dx / d * f; B.dy -= dy / d * f;
      }
    }
    links.forEach(function (l) {
      var A = p[l.a], B = p[l.b], dx = A.x - B.x, dy = A.y - B.y, d = Math.sqrt(dx * dx + dy * dy) || 0.1, f = d * d / kk;
      A.dx -= dx / d * f; A.dy -= dy / d * f; B.dx += dx / d * f; B.dy += dy / d * f;
    });
    keys.forEach(function (a) { var P = p[a], d = Math.sqrt(P.dx * P.dx + P.dy * P.dy) || 0.1, m = Math.min(d, t); P.x += P.dx / d * m; P.y += P.dy / d * m; });
    t = Math.max(2, t * 0.97);
  }
  keys.forEach(function (k) { out[k] = { x: p[k].x, y: p[k].y }; });
  tpDeclutterInto(out, keys);
  return out;
}
// 把 keys 里互相重叠(或和其它已摆好节点重叠)的推开; 就地修改 pos
function tpDeclutterInto(pos, keys, iters) {
  var mw = TP_NODE_W * 0.98, mh = TP_NODE_H * 0.92, all = Object.keys(pos), movable = {};
  (keys || all).forEach(function (k) { movable[k] = true; });
  for (var it = 0; it < (iters || 80); it++) {
    var moved = false;
    for (var i = 0; i < all.length; i++) {
      for (var j = i + 1; j < all.length; j++) {
        var a = all[i], b = all[j], A = pos[a], B = pos[b];
        if (!A || !B || (!movable[a] && !movable[b])) { continue; }
        var dx = B.x - A.x, dy = B.y - A.y, ox = mw - Math.abs(dx), oy = mh - Math.abs(dy);
        if (ox <= 0 || oy <= 0) { continue; }
        moved = true;
        if (dx === 0 && dy === 0) { dx = (i % 2 ? 1 : -1) * 0.5; }
        // 沿重叠较少的那个轴推开
        var ax = ox / mw < oy / mh, push = (ax ? ox : oy) / 2 + 1, sx = ax ? (dx >= 0 ? 1 : -1) : 0, sy = ax ? 0 : (dy >= 0 ? 1 : -1);
        var wa = movable[a] ? (movable[b] ? 1 : 2) : 0, wb = movable[b] ? (movable[a] ? 1 : 2) : 0;
        A.x -= sx * push * wa / 2; A.y -= sy * push * wa / 2; B.x += sx * push * wb / 2; B.y += sy * push * wb / 2;
      }
    }
    if (!moved) { break; }
  }
  return pos;
}
function tpBBoxOf(pos, keys) {
  var b = { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
  (keys || Object.keys(pos)).forEach(function (k) { var p = pos[k]; if (!p) { return; } b.x0 = Math.min(b.x0, p.x); b.y0 = Math.min(b.y0, p.y); b.x1 = Math.max(b.x1, p.x); b.y1 = Math.max(b.y1, p.y); });
  return b;
}
function tpAutoLayout(kind) {
  if (!TP.data || TP.opt.locked) { if (TP.opt.locked) { tpNotice('布局已锁定, 先点 🔒 解锁', false); } return; }
  var selKeys = Object.keys(TP.sel).filter(function (k) { return TP.byKey[k]; });
  var only = selKeys.length >= 2, keys = only ? selKeys : TP.nodes.map(function (n) { return n.key; });
  var lay = tpLayout(kind, keys);
  // 位置平移: 全部编排 → 放在画布中间; 只编排选中的 → 保持在原来那块区域的中心
  var nb = tpBBoxOf(lay), ob = only ? tpBBoxOf(TP.pos, keys) : { x0: 0, y0: 0, x1: TP.W, y1: TP.H };
  var dx = (ob.x0 + ob.x1) / 2 - (nb.x0 + nb.x1) / 2, dy = (ob.y0 + ob.y1) / 2 - (nb.y0 + nb.y1) / 2;
  if (!only && nb.y1 - nb.y0 < TP.H - 120) { dy = 90 - nb.y0 + Math.max(0, (TP.H - 180 - (nb.y1 - nb.y0)) / 2.4); }
  keys.forEach(function (k) { lay[k] = { x: lay[k].x + dx, y: lay[k].y + dy }; });
  if (only) { var others = {}; Object.keys(TP.pos).forEach(function (k) { others[k] = lay[k] || TP.pos[k]; }); tpDeclutterInto(others, keys, 40); keys.forEach(function (k) { lay[k] = others[k]; }); }
  tpPushUndo();
  if (!only) { TP.data.externals.forEach(function (e) { if (e.has_pos) { e._auto = true; TP.pendClear[e.key] = true; } }); }
  tpAnimateTo(lay, function () {
    tpQueueSave(keys); if (!only) { tpFit(); }
    tpNotice((only ? '已编排选中的 ' + keys.length + ' 个节点' : '已自动编排全部 ' + keys.length + ' 个节点') + ' · Ctrl+Z 撤销', true);
  });
}
function tpDeclutter() {
  if (TP.opt.locked) { tpNotice('布局已锁定, 先点 🔒 解锁', false); return; }
  var tgt = JSON.parse(JSON.stringify(TP.pos)), before = JSON.stringify(tgt);
  tpDeclutterInto(tgt, null, 120);
  if (JSON.stringify(tgt) === before) { tpNotice('没有重叠的节点', true); return; }
  tpPushUndo();
  var changed = Object.keys(tgt).filter(function (k) { return Math.abs(tgt[k].x - TP.pos[k].x) + Math.abs(tgt[k].y - TP.pos[k].y) > 0.5; });
  tpAnimateTo(tgt, function () { tpQueueSave(changed); tpNotice('已把 ' + changed.length + ' 个挤在一起的节点推开, 其他不动', true); });
}
function tpAnimateTo(target, done) {
  var from = {}, keys = Object.keys(target), t0 = performance.now(), dur = 380;
  keys.forEach(function (k) { from[k] = TP.pos[k] ? { x: TP.pos[k].x, y: TP.pos[k].y } : { x: target[k].x, y: target[k].y }; });
  function step(now) {
    var k = Math.min(1, (now - t0) / dur), e = k < 0.5 ? 2 * k * k : 1 - Math.pow(-2 * k + 2, 2) / 2;
    keys.forEach(function (key) { TP.pos[key] = { x: from[key].x + (target[key].x - from[key].x) * e, y: from[key].y + (target[key].y - from[key].y) * e }; });
    tpGeometry();
    if (k < 1) { requestAnimationFrame(step); } else { if (done) { done(); } tpSyncBar(); }
  }
  requestAnimationFrame(step);
}

/* ---------- 撤销 / 重做 ---------- */
function tpPushUndo() { TP.undo.push(JSON.stringify(TP.pos)); if (TP.undo.length > 60) { TP.undo.shift(); } TP.redo = []; tpSyncBar(); }
function tpUndo() { tpHistory(TP.undo, TP.redo, '已撤销'); }
function tpRedo() { tpHistory(TP.redo, TP.undo, '已重做'); }
function tpHistory(from, to, msg) {
  if (!from.length) { return; }
  to.push(JSON.stringify(TP.pos));
  var prev = JSON.parse(from.pop()), keys = Object.keys(prev).filter(function (k) { return TP.byKey[k]; }), tgt = {};
  keys.forEach(function (k) { tgt[k] = prev[k]; });
  tpAnimateTo(tgt, function () { tpQueueSave(keys); tpNotice(msg, true); });
}

/* ---------- 保存 ---------- */
function tpCanSave() { return typeof can === 'function' ? can('topology') : authenticated; }
function tpQueueSave(keys) {
  keys.forEach(function (k) { TP.pend[k] = true; });
  if (!tpCanSave()) { tpShowBanner(); return; }
  var bn = byId('tp2Banner'); if (bn) { bn.style.display = 'none'; }
  if (TP.saveTimer) { clearTimeout(TP.saveTimer); }
  TP.saveTimer = setTimeout(tpFlushSave, 400);
}
function tpFlushSave() {
  if (TP.saveTimer) { clearTimeout(TP.saveTimer); TP.saveTimer = null; }
  if (!tpCanSave()) { return; }
  var keys = Object.keys(TP.pend), clear = Object.keys(TP.pendClear); TP.pend = {}; TP.pendClear = {};
  if (!keys.length && !clear.length) { return; }
  var body = {};
  keys.forEach(function (k) { var p = TP.pos[k] || TP.extPos[k]; if (p) { body[k] = { x: +(p.x / TP.W * 100).toFixed(3), y: +(p.y / TP.H * 100).toFixed(3) }; } });
  authedApi('/api/topology/positions', { positions: body, clear: clear }).then(function () { tpStatus('已保存'); })
    .catch(function (e) { tpNotice('保存位置失败: ' + e, false); keys.forEach(function (k) { TP.pend[k] = true; }); });
}
function tpShowBanner() {
  var b = byId('tp2Banner'); if (!b || b.style.display === 'block') { return; }
  b.innerHTML = authenticated ? '当前账号没有「拓扑编辑」权限, 拖动只在本机临时生效, 不会保存' : '未登录: 可以随意拖动查看, 但不会保存 <button onclick="tpLoginToSave()">登录后保存</button>';
  b.style.display = 'block';
}
function tpLoginToSave() {
  openAuthGate(function () {
    var b = byId('tp2Banner'); if (b) { b.style.display = 'none'; }
    if (tpCanSave()) { tpFlushSave(); tpNotice('已登录, 刚才的布局改动已保存', true); } else { tpShowBanner(); }
    tpSyncBar();
  });
}
// 需要写权限的操作: 没登录先登录, 登录了没权限给提示
function tpNeedEdit(fn) {
  if (tpCanSave()) { fn(); return; }
  if (!authenticated) { openAuthGate(function () { if (tpCanSave()) { fn(); } else { tpNotice('当前账号没有「拓扑编辑」权限', false); } }); return; }
  tpNotice('当前账号没有「拓扑编辑」权限, 请联系管理员分配', false);
}

/* ---------- 渲染 ---------- */
function tp2Render(data) {
  var wrap = byId('topoWrap');
  data.manual_nodes = data.manual_nodes || []; data.links = data.links || []; data.externals = data.externals || []; data.devices = data.devices || [];
  if (data.devices.length === 0 && data.manual_nodes.length === 0) { wrap.innerHTML = '<div class="empty">还没有设备数据, 等第一轮巡检跑完再看看</div>'; return; }
  var keep = TP.keep && byId('topoSvg'); TP.keep = false;
  TP.data = data;
  tpBuild(data);
  tpInitPos(data, !!keep);
  Object.keys(TP.sel).forEach(function (k) { if (!TP.byKey[k]) { delete TP.sel[k]; } });
  if (!keep) {
    wrap.innerHTML = '<div class="tp2"><div class="tp2-bar" id="tpBar"></div>' +
      '<div class="tp2-stage pan" id="tpStage"><svg id="topoSvg" xmlns="http://www.w3.org/2000/svg">' +
      '<defs><linearGradient id="tpGloss" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#fff" stop-opacity=".38"/><stop offset=".55" stop-color="#fff" stop-opacity="0"/></linearGradient>' +
      '<filter id="tpShadow" x="-30%" y="-30%" width="160%" height="170%"><feDropShadow dx="0" dy="2" stdDeviation="2.2" flood-opacity=".25"/></filter></defs>' +
      '<g id="topoViewport"><image id="tpBg" x="0" y="0" preserveAspectRatio="none" style="display:none;"></image>' +
      '<g id="tpGroups"></g><g id="tpLinks"></g><g id="tpExts"></g><g id="tpNodes"></g><line id="tpRubber" style="display:none;"></line><rect id="tpMarquee" style="display:none;"></rect></g></svg>' +
      '<div class="tp2-banner" id="tp2Banner"></div><div class="tp2-side" id="tpSide"></div><div class="tp2-menu" id="tpMenu"></div><div class="tp2-pop" id="tpPop"></div>' +
      '<canvas class="tp2-mini" id="tpMini" width="380" height="240" title="小地图: 点击跳转"></canvas><div class="tp2-status" id="tpStatus"></div><div class="tip" id="topoTip"></div></div>' +
      '<div class="tp2-legend" id="tpLegend"></div></div>';
    tpBind();
  }
  var bg = byId('tpBg');
  if (data.has_background) { bg.setAttribute('href', '/api/topology/background?v=' + topoImgVersion); bg.setAttribute('width', TP.W); bg.setAttribute('height', TP.H); bg.style.display = ''; bg.setAttribute('opacity', TP.opt.bgAlpha); }
  else { bg.style.display = 'none'; }
  tpDrawAll();
  if (!keep) { tpFit(); } else { tpApplyView(); }
  tpSyncBar(); tpLegend();
  if (TP.side) { tpRenderSide(); }
}
function tpDrawAll() {
  var lk = '', nk = '';
  TP.links.forEach(function (l) { lk += tpLinkSVG(l); });
  TP.nodes.forEach(function (n) { nk += tpNodeSVG(n); });
  byId('tpLinks').innerHTML = lk; byId('tpNodes').innerHTML = nk;
  tpDrawExts(); tpGeometry(); tpApplySel(); tpApplySearch();
  var st = byId('tpStage');
  st.classList.toggle('noflow', !TP.opt.flow); st.classList.toggle('unlocked', !TP.opt.locked);
}
function tpUtil(l) {
  var sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0);
  return sp ? Math.max(l.a_in_mbps || 0, l.a_out_mbps || 0, l.b_in_mbps || 0, l.b_out_mbps || 0) / sp * 100 : -1;
}
function tpLinkStyle(l) {
  var sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0), util = tpUtil(l);
  var color = util < 0 ? '#64748b' : util >= 80 ? '#ef4444' : util >= 50 ? '#eab308' : util >= 1 ? '#22c55e' : '#3b82f6';
  if (l.source === 'manual' && util < 0) { color = '#a855f7'; }
  var w = sp >= 40000 ? 6 : sp >= 10000 ? 4.5 : sp >= 1000 ? 3 : 2;
  var dash = l.source === 'mac' ? '7 6' : l.source === 'manual' ? '12 5 3 5' : '';
  var tag = l.source === 'mac' ? '推断' : l.source === 'cdp' ? 'CDP' : l.source === 'manual' && l.note ? truncateLabel(l.note, 12) : '';
  if (l._bundle) { tag = '×' + l._bundle.length + (tag ? ' · ' + tag : ''); dash = l._bundle.every(function (x) { return x.source === 'mac'; }) ? '7 6' : ''; }
  return { color: color, w: w, dash: dash, tag: tag, util: util };
}
function tpLinkSVG(l) {
  var st = tpLinkStyle(l), fwd = (l.a_out_mbps || 0) + (l.b_in_mbps || 0), rev = (l.a_in_mbps || 0) + (l.b_out_mbps || 0);
  var dur = st.util < 1 ? 0 : Math.max(0.5, 3.2 - st.util / 30);
  return '<g class="tpl' + (rev > fwd ? ' rev' : '') + '" data-i="' + l._i + '" style="color:' + st.color + ';">' +
    (l.is_agg ? '<path class="agg" stroke="#f59e0b" stroke-width="' + (st.w + 5) + '" stroke-opacity=".4" stroke-linecap="round"></path>' : '') +
    '<path class="ln" stroke="' + st.color + '" stroke-width="' + st.w + '"' + (st.dash ? ' stroke-dasharray="' + st.dash + '"' : '') + ' stroke-linecap="round"></path>' +
    (dur ? '<path class="fl" stroke-width="' + Math.max(1.5, st.w - 1.5) + '" style="animation-duration:' + dur.toFixed(2) + 's"></path>' : '') +
    '<path class="hit"></path>' + (st.tag ? '<text class="tag" text-anchor="middle">' + esc(st.tag) + '</text>' : '') +
    (TP.opt.ports ? '<text class="pt pa" text-anchor="middle">' + esc(truncateLabel(String(l.a_port || '').split(', ').map(tpShortPort).join(','), 18)) + '</text><text class="pt pb" text-anchor="middle">' + esc(truncateLabel(String(l.b_port || '').split(', ').map(tpShortPort).join(','), 18)) + '</text>' : '') + '</g>';
}
function tpShortPort(p) {
  p = String(p || '');
  return p.replace(/^(Ten-?GigabitEthernet|TenGigabitEthernet|XGigabitEthernet)/i, 'XGE').replace(/^GigabitEthernet/i, 'GE').replace(/^FortyGigE|^FortyGigabitEthernet/i, '40GE')
    .replace(/^HundredGigE|^HundredGigabitEthernet/i, '100GE').replace(/^(Eth-Trunk|Bridge-Aggregation|Port-channel|AggregatePort)/i, 'Agg').replace(/\s+/g, '');
}
function tpNodeSVG(n) {
  var k = esc(n.key), note = TP.opt.note && n.d.note ? '<text class="note" y="__NY__" text-anchor="middle">' + esc(truncateLabel(n.d.note, 18)) + '</text>' : '';
  if (n.t === 'man') {
    return '<g class="tpn man" data-k="' + k + '"><rect class="ring" x="-30" y="-30" width="60" height="60" rx="14"></rect>' +
      '<circle r="22" fill="var(--card-bg)" stroke="#a855f7" stroke-width="2" stroke-dasharray="4 3" filter="url(#tpShadow)"></circle>' +
      '<text y="7" text-anchor="middle" font-size="20" style="pointer-events:none;">' + (TP_KIND_ICON[n.d.kind] || '◆') + '</text>' +
      '<text class="lbl" y="40" text-anchor="middle">' + esc(truncateLabel(n.name, 18)) + '</text>' +
      (n.d.ip ? '<text class="sub" y="53" text-anchor="middle">' + esc(n.d.ip) + '</text>' : '') + note.replace('__NY__', n.d.ip ? 66 : 53) + '</g>';
  }
  var d = n.d, role = TP.roles[n.key] || 'lone', col = d.alive ? TP_ROLE_COLOR[role] : '#64748b';
  var f = d.form || {}, stack = f.kind === 'stack' && f.members > 1 ? Math.min(4, f.members) : 1, chassis = f.kind === 'chassis';
  var bw = 56, bh = chassis ? 40 : 22, body = '', lift = (stack - 1) * 5;
  for (var s = stack - 1; s >= 0; s--) {
    body += '<rect x="' + (-bw / 2 + s * 3) + '" y="' + (-s * 5 - bh / 2) + '" width="' + bw + '" height="' + bh + '" rx="5" fill="' + col + '" stroke="' + (d.alive ? 'rgba(255,255,255,.8)' : '#ef4444') + '" stroke-width="1.6"' + (s === 0 ? ' filter="url(#tpShadow)"' : '') + '></rect>';
  }
  body += '<rect x="' + (-bw / 2) + '" y="' + (-bh / 2) + '" width="' + bw + '" height="' + bh + '" rx="5" fill="url(#tpGloss)" style="pointer-events:none;"></rect>';
  var lights = '', per = chassis ? 7 : 9, rows = chassis ? 2 : 1;
  for (var r = 0; r < rows; r++) {
    for (var i = 0; i < per; i++) {
      var lx = -bw / 2 + 7 + i * ((bw - 14) / (per - 1)) - 1.5, ly = chassis ? (r ? 5 : -9) : 1;
      lights += '<rect x="' + lx.toFixed(1) + '" y="' + ly + '" width="3" height="3" rx="1" fill="' + (d.alive ? '#d9f99d' : '#475569') + '" style="pointer-events:none;"></rect>';
    }
  }
  var more = '', ec = TP.extCount[d.ip] || 0;
  if (ec && TP.extMode === 'fold' && !TP.extOpen[d.ip]) {
    var mw = String(ec).length * 6 + 16;
    more = '<g class="more" data-more="' + k + '" transform="translate(' + (bw / 2 + 3) + ',' + (bh / 2 - 3) + ')"><rect x="0" y="-8" width="' + mw + '" height="16" rx="8" fill="#64748b"></rect><text x="' + (mw / 2) + '" y="3.5" text-anchor="middle">+' + ec + '</text></g>';
  }
  var alert = d.open_alerts > 0 ? '<circle cx="' + (bw / 2 - 1) + '" cy="' + (-bh / 2 - lift) + '" r="7.5" fill="#ef4444" stroke="var(--card-bg)" stroke-width="2" style="pointer-events:none;"></circle><text x="' + (bw / 2 - 1) + '" y="' + (-bh / 2 - lift + 3.5) + '" text-anchor="middle" font-size="9" font-weight="700" fill="#fff" style="pointer-events:none;">' + (d.open_alerts > 9 ? '9+' : d.open_alerts) + '</text>' : '';
  var ly0 = bh / 2 + 16;
  return '<g class="tpn dev" data-k="' + k + '">' +
    (d.open_alerts > 0 ? '<circle class="halo" r="' + (bw / 2 + 6) + '"><animate attributeName="r" values="' + (bw / 2 + 2) + ';' + (bw / 2 + 12) + ';' + (bw / 2 + 2) + '" dur="2.2s" repeatCount="indefinite"/><animate attributeName="opacity" values=".55;.05;.55" dur="2.2s" repeatCount="indefinite"/></circle>' : '') +
    '<rect class="ring" x="' + (-bw / 2 - 7) + '" y="' + (-bh / 2 - 7 - lift) + '" width="' + (bw + 14 + (stack - 1) * 3) + '" height="' + (bh + 14 + lift) + '" rx="9"></rect>' +
    body + lights + alert + more +
    '<text class="lbl" y="' + ly0 + '" text-anchor="middle">' + esc(truncateLabel(n.name, 20)) + '</text>' +
    '<text class="sub" y="' + (ly0 + 13) + '" text-anchor="middle">' + esc(d.ip) + (stack > 1 ? ' · 堆叠×' + f.members : chassis ? ' · 框式' : '') + '</text>' +
    note.replace('__NY__', ly0 + 26) + '</g>';
}
function tpDrawExts() {
  tpComputeExtPos();
  var h = '', lbl = TP.view.s >= 1.1;
  Object.keys(TP.extByDev).forEach(function (dev) {
    if (!tpExtVisible(dev)) { return; }
    TP.extByDev[dev].forEach(function (e) { h += '<g class="tpe" data-e="' + e._i + '"><line></line><circle r="5.5"></circle>' + (lbl ? '<text dy="-8" text-anchor="middle">' + esc(truncateLabel(e.name, 14)) + '</text>' : '') + '</g>'; });
  });
  byId('tpExts').innerHTML = h;
}
function tpLinkPath(l) {
  var a = TP.pos[l.a], b = TP.pos[l.b]; if (!a || !b) { return null; }
  var mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2, dx = b.x - a.x, dy = b.y - a.y, len = Math.sqrt(dx * dx + dy * dy) || 1, d, q = null;
  if (l._pn > 1) {
    var off = (l._pi - (l._pn + 1) / 2) * 26;
    // 按 key 的字典序固定法线方向, 否则 a/b 顺序不同的两条链路会弯到同一边
    var sgn = l.a < l.b ? 1 : -1;
    q = { x: mx - dy / len * off * 2 * sgn, y: my + dx / len * off * 2 * sgn };
    d = 'M' + a.x.toFixed(1) + ',' + a.y.toFixed(1) + ' Q' + q.x.toFixed(1) + ',' + q.y.toFixed(1) + ' ' + b.x.toFixed(1) + ',' + b.y.toFixed(1);
    mx = (a.x + 2 * q.x + b.x) / 4; my = (a.y + 2 * q.y + b.y) / 4;
  } else { d = 'M' + a.x.toFixed(1) + ',' + a.y.toFixed(1) + ' L' + b.x.toFixed(1) + ',' + b.y.toFixed(1); }
  var at = function (t) { if (!q) { return { x: a.x + dx * t, y: a.y + dy * t }; } var u = 1 - t; return { x: u * u * a.x + 2 * u * t * q.x + t * t * b.x, y: u * u * a.y + 2 * u * t * q.y + t * t * b.y }; };
  return { d: d, mx: mx, my: my, at: at, len: len };
}
function tpGeometry() {
  document.querySelectorAll('#tpNodes .tpn').forEach(function (g) { var p = TP.pos[g.getAttribute('data-k')]; if (p) { g.setAttribute('transform', 'translate(' + p.x.toFixed(1) + ',' + p.y.toFixed(1) + ')'); } });
  document.querySelectorAll('#tpLinks .tpl').forEach(function (g) {
    var l = TP.links[+g.getAttribute('data-i')], P = l && tpLinkPath(l); if (!P) { return; }
    g.querySelectorAll('path').forEach(function (p) { p.setAttribute('d', P.d); });
    var t = g.querySelector('.tag'); if (t) { t.setAttribute('x', P.mx.toFixed(1)); t.setAttribute('y', (P.my - 6).toFixed(1)); }
    var off = Math.min(0.42, 74 / P.len);
    var pa = g.querySelector('.pa'), pb = g.querySelector('.pb');
    if (pa) { var A = P.at(off), B = P.at(1 - off); pa.setAttribute('x', A.x.toFixed(1)); pa.setAttribute('y', (A.y - 4).toFixed(1)); pb.setAttribute('x', B.x.toFixed(1)); pb.setAttribute('y', (B.y - 4).toFixed(1)); }
  });
  document.querySelectorAll('#tpExts .tpe').forEach(function (g) {
    var e = TP.data.externals[+g.getAttribute('data-e')], p = e && (TP.extPos[e.key] = e.has_pos && !e._auto ? TP.extPos[e.key] : tpExtAuto(e, TP.extByDev[e.device].indexOf(e), TP.extByDev[e.device])), dp = e && TP.pos[e.device];
    if (!p || !dp) { return; }
    var ln = g.querySelector('line'), c = g.querySelector('circle'), tx = g.querySelector('text');
    ln.setAttribute('x1', dp.x); ln.setAttribute('y1', dp.y); ln.setAttribute('x2', p.x); ln.setAttribute('y2', p.y);
    c.setAttribute('cx', p.x); c.setAttribute('cy', p.y);
    if (tx) { tx.setAttribute('x', p.x); tx.setAttribute('y', p.y); }
  });
  tpDrawGroups();
  tpMiniLater();
}

/* ---------- v13: 分组底色 ---------- */
// 设备管理里填了分组的设备, 在拓扑上画一块半透明的圆角底色把同组的圈起来, 左上角写组名。
// 跟着节点位置实时变(拖动时也是), 显示选项里可以关。
var TP_GROUP_COLORS = ['#0ea5e9', '#8b5cf6', '#10b981', '#f59e0b', '#ec4899', '#14b8a6', '#6366f1', '#84cc16'];
function tpGroupOf(n) { return n && n.t === 'dev' ? (n.d.group || '') : ''; }
function tpGroupColor(name) { var h = 0; for (var i = 0; i < name.length; i++) { h = (h * 31 + name.charCodeAt(i)) >>> 0; } return TP_GROUP_COLORS[h % TP_GROUP_COLORS.length]; }
// 按分组编排: 每组内部用分层布局, 各组像卡片一样横向排开, 放不下就换行
function tpGroupLayout(keys) {
  var by = {}, out = {};
  keys.forEach(function (k) { var g = tpGroupOf(TP.byKey[k]) || '\uffff'; (by[g] = by[g] || []).push(k); });
  var names = Object.keys(by).sort(function (a, b) { return (a === '\uffff') - (b === '\uffff') || naturalCmp(a, b); });
  var x = 0, y = 0, rowH = 0, maxW = Math.max(TP_NODE_W * 9, Math.sqrt(keys.length) * TP_NODE_W * 2.2), gap = TP_NODE_W * 1.75; // 组底色两边各留 0.62 个节点宽, 间距要比这个大
  names.forEach(function (g) {
    var lay = tpLayout(by[g].length > 1 ? 'tree' : 'grid', by[g]), b = tpBBoxOf(lay);
    if (!isFinite(b.x0)) { return; }
    var w = b.x1 - b.x0, h = b.y1 - b.y0;
    if (x > 0 && x + w > maxW) { x = 0; y += rowH + gap; rowH = 0; }
    by[g].forEach(function (k) { out[k] = { x: lay[k].x - b.x0 + x, y: lay[k].y - b.y0 + y }; });
    x += w + gap; rowH = Math.max(rowH, h);
  });
  return out;
}
function tpDrawGroups() {
  var layer = byId('tpGroups'); if (!layer) { return; }
  if (!TP.opt.groups) { layer.innerHTML = ''; return; }
  var by = {};
  TP.nodes.forEach(function (n) { var g = tpGroupOf(n), p = TP.pos[n.key]; if (g && p) { (by[g] = by[g] || []).push(p); } });
  var h = '';
  Object.keys(by).sort(naturalCmp).forEach(function (g) {
    var ps = by[g], x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    ps.forEach(function (p) { x0 = Math.min(x0, p.x); y0 = Math.min(y0, p.y); x1 = Math.max(x1, p.x); y1 = Math.max(y1, p.y); });
    x0 -= TP_NODE_W * 0.62; x1 += TP_NODE_W * 0.62; y0 -= TP_NODE_H * 0.75; y1 += TP_NODE_H * 0.85;
    var c = tpGroupColor(g);
    h += '<g class="tpg"><rect x="' + x0.toFixed(1) + '" y="' + y0.toFixed(1) + '" width="' + (x1 - x0).toFixed(1) + '" height="' + (y1 - y0).toFixed(1) + '" rx="18" fill="' + c + '" fill-opacity=".07" stroke="' + c + '" stroke-opacity=".45" stroke-width="1.5" stroke-dasharray="8 5"></rect>' +
      '<text x="' + (x0 + 14).toFixed(1) + '" y="' + (y0 + 20).toFixed(1) + '" fill="' + c + '">🏷️ ' + esc(g) + ' · ' + ps.length + '</text></g>';
  });
  layer.innerHTML = h;
}

/* ---------- 视图 ---------- */
function tpApplyView() {
  var g = byId('topoViewport'), st = byId('tpStage'); if (!g) { return; }
  var v = TP.view;
  g.setAttribute('transform', 'translate(' + v.tx.toFixed(2) + ',' + v.ty.toFixed(2) + ') scale(' + v.s.toFixed(4) + ')');
  var gs = 24 * v.s; while (gs < 12) { gs *= 2; }
  st.style.backgroundSize = gs + 'px ' + gs + 'px'; st.style.backgroundPosition = v.tx + 'px ' + v.ty + 'px';
  var z = byId('tpZoomTxt'); if (z) { z.innerText = Math.round(v.s * 100) + '%'; }
  // 缩放跨过 110% 时邻居名字显示/隐藏
  var lbl = v.s >= 1.1; if (lbl !== TP.extLbl) { TP.extLbl = lbl; if (TP.data) { tpDrawExts(); tpGeometry(); tpApplySearch(); } }
  tpMiniLater(); tpStatus();
}
function tpStageSize() { var st = byId('tpStage'); return st ? { w: st.clientWidth, h: st.clientHeight } : { w: 1200, h: 700 }; }
function tpScreen(e) { var r = byId('tpStage').getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; }
function tpWorld(e) { var s = tpScreen(e), v = TP.view; return { x: (s.x - v.tx) / v.s, y: (s.y - v.ty) / v.s }; }
function tpZoomAt(f, sp) {
  var v = TP.view, s2 = Math.max(0.1, Math.min(5, v.s * f)), sz = tpStageSize();
  sp = sp || { x: sz.w / 2, y: sz.h / 2 };
  v.tx = sp.x - (sp.x - v.tx) * s2 / v.s; v.ty = sp.y - (sp.y - v.ty) * s2 / v.s; v.s = s2;
  tpApplyView();
}
function tp2Zoom(f) { tpZoomAt(f); }
function tpAllBBox() {
  var b = tpBBoxOf(TP.pos), e = tpBBoxOf(TP.extPos || {});
  b = { x0: Math.min(b.x0, e.x0), y0: Math.min(b.y0, e.y0), x1: Math.max(b.x1, e.x1), y1: Math.max(b.y1, e.y1) };
  if (TP.data && TP.data.has_background) { b = { x0: Math.min(b.x0, 0), y0: Math.min(b.y0, 0), x1: Math.max(b.x1, TP.W), y1: Math.max(b.y1, TP.H) }; }
  if (b.x0 === Infinity) { b = { x0: 0, y0: 0, x1: TP.W, y1: TP.H }; }
  return b;
}
function tpFit(keys) {
  var b = keys ? tpBBoxOf(TP.pos, keys) : tpAllBBox(), sz = tpStageSize(), pad = 80;
  var w = Math.max(240, b.x1 - b.x0 + TP_NODE_W), h = Math.max(160, b.y1 - b.y0 + TP_NODE_H * 1.4);
  var s = Math.max(0.1, Math.min(keys ? 1.6 : 1.4, (sz.w - pad) / w, (sz.h - pad) / h));
  TP.view = { s: s, tx: sz.w / 2 - (b.x0 + b.x1) / 2 * s, ty: sz.h / 2 - ((b.y0 + b.y1) / 2 + TP_NODE_H * 0.2) * s };
  tpApplyView();
}
function tpCenterOn(k, zoom) {
  var p = TP.pos[k], sz = tpStageSize(); if (!p) { return; }
  var s = zoom ? Math.max(TP.view.s, 1.1) : TP.view.s, from = JSON.parse(JSON.stringify(TP.view)), to = { s: s, tx: sz.w / 2 - p.x * s - (TP.side ? 150 : 0), ty: sz.h / 2 - p.y * s };
  var t0 = performance.now();
  (function step(now) { var k2 = Math.min(1, (now - t0) / 320), e = 1 - Math.pow(1 - k2, 3); TP.view = { s: from.s + (to.s - from.s) * e, tx: from.tx + (to.tx - from.tx) * e, ty: from.ty + (to.ty - from.ty) * e }; tpApplyView(); if (k2 < 1) { requestAnimationFrame(step); } })(t0);
}
function tp2Reset() { var s = byId('topoSearch'); if (s) { s.value = ''; } tpApplySearch(); tpFit(); }

/* ---------- 小地图 ---------- */
function tpMiniLater() { if (!TP.raf) { TP.raf = requestAnimationFrame(function () { TP.raf = 0; tpMini(); }); } }
function tpMini() {
  var cv = byId('tpMini'); if (!cv || !TP.data) { return; }
  var ctx = cv.getContext('2d'), W = cv.width, H = cv.height, b = tpAllBBox(), sz = tpStageSize(), v = TP.view;
  var vx0 = -v.tx / v.s, vy0 = -v.ty / v.s, vx1 = vx0 + sz.w / v.s, vy1 = vy0 + sz.h / v.s;
  var x0 = Math.min(b.x0, vx0) - 60, y0 = Math.min(b.y0, vy0) - 60, x1 = Math.max(b.x1, vx1) + 60, y1 = Math.max(b.y1, vy1) + 60;
  var k = Math.min(W / (x1 - x0), H / (y1 - y0)), ox = (W - (x1 - x0) * k) / 2, oy = (H - (y1 - y0) * k) / 2;
  var X = function (x) { return ox + (x - x0) * k; }, Y = function (y) { return oy + (y - y0) * k; };
  TP.miniMap = { x0: x0, y0: y0, k: k, ox: ox, oy: oy };
  ctx.clearRect(0, 0, W, H);
  ctx.strokeStyle = 'rgba(100,116,139,.55)'; ctx.lineWidth = 1.5;
  TP.links.forEach(function (l) { var a = TP.pos[l.a], c = TP.pos[l.b]; if (a && c) { ctx.beginPath(); ctx.moveTo(X(a.x), Y(a.y)); ctx.lineTo(X(c.x), Y(c.y)); ctx.stroke(); } });
  TP.nodes.forEach(function (n) {
    var p = TP.pos[n.key]; if (!p) { return; }
    ctx.fillStyle = n.t === 'man' ? '#a855f7' : !n.d.alive ? '#64748b' : n.d.open_alerts > 0 ? '#ef4444' : TP_ROLE_COLOR[TP.roles[n.key]] || '#64748b';
    ctx.fillRect(X(p.x) - 4, Y(p.y) - 3, 8, 6);
  });
  ctx.strokeStyle = '#3b82f6'; ctx.lineWidth = 2; ctx.fillStyle = 'rgba(59,130,246,.08)';
  ctx.fillRect(X(vx0), Y(vy0), (vx1 - vx0) * k, (vy1 - vy0) * k); ctx.strokeRect(X(vx0), Y(vy0), (vx1 - vx0) * k, (vy1 - vy0) * k);
}
function tpMiniJump(e) {
  var m = TP.miniMap, cv = byId('tpMini'); if (!m) { return; }
  var r = cv.getBoundingClientRect(), sx = (e.clientX - r.left) * cv.width / r.width, sy = (e.clientY - r.top) * cv.height / r.height;
  var wx = (sx - m.ox) / m.k + m.x0, wy = (sy - m.oy) / m.k + m.y0, sz = tpStageSize();
  TP.view.tx = sz.w / 2 - wx * TP.view.s; TP.view.ty = sz.h / 2 - wy * TP.view.s; tpApplyView();
}

/* ---------- 搜索 / 选择 ---------- */
function tpApplySearch() {
  var el = byId('topoSearch'), kw = (el ? el.value : '').trim().toLowerCase(), hit = {}, first = null;
  if (!TP.data) { return null; }
  TP.nodes.forEach(function (n) {
    var hay = (n.name + ' ' + (n.d.ip || '') + ' ' + (n.d.model || '') + ' ' + (n.d.note || '') + ' ' + tpGroupOf(n)).toLowerCase();
    if (kw && hay.indexOf(kw) >= 0) { hit[n.key] = true; if (!first) { first = n.key; } }
  });
  document.querySelectorAll('#tpNodes .tpn').forEach(function (g) { g.classList.toggle('dim', !!kw && !hit[g.getAttribute('data-k')]); });
  document.querySelectorAll('#tpLinks .tpl').forEach(function (g) { var l = TP.links[+g.getAttribute('data-i')]; g.classList.toggle('dim', !!kw && !hit[l.a] && !hit[l.b]); });
  document.querySelectorAll('#tpExts .tpe').forEach(function (g) { var e = TP.data.externals[+g.getAttribute('data-e')]; g.classList.toggle('dim', !!kw && !hit[e.device] && (e.name + ' ' + e.port).toLowerCase().indexOf(kw) < 0); });
  return first;
}
function tp2Highlight() { var f = tpApplySearch(); if (f) { tpCenterOn(f, true); } }
function tpApplySel() {
  document.querySelectorAll('#tpNodes .tpn').forEach(function (g) { g.classList.toggle('sel', !!TP.sel[g.getAttribute('data-k')]); });
  document.querySelectorAll('#tpLinks .tpl').forEach(function (g) { g.classList.toggle('sel', TP.selLink !== null && +g.getAttribute('data-i') === TP.selLink); });
  tpSyncBar(); tpStatus();
}
function tpSelCount() { return Object.keys(TP.sel).length; }
function tpSelectAll() { TP.nodes.forEach(function (n) { TP.sel[n.key] = true; }); tpApplySel(); }
function tpClearSel() { TP.sel = {}; TP.selLink = null; tpApplySel(); }
function tpSelectNeighbors(k) {
  TP.sel = {}; TP.sel[k] = true;
  TP.links.forEach(function (l) { if (l.a === k) { TP.sel[l.b] = true; } if (l.b === k) { TP.sel[l.a] = true; } });
  tpApplySel();
}

/* ---------- 工具条 ---------- */
function tpSyncBar() {
  var bar = byId('tpBar'); if (!bar || !TP.data) { return; }
  var n = tpSelCount(), hid = TP.data.hidden_links || 0, ext = TP.data.externals.length, lk = TP.opt.locked;
  var B = function (fn, txt, title, on, dis) { return '<button onclick="' + fn + '"' + (title ? ' title="' + title + '"' : '') + (on ? ' class="on"' : '') + (dis ? ' disabled' : '') + '>' + txt + '</button>'; };
  var h = '<span class="grp"><b>编排' + (n >= 2 ? '(选中 ' + n + ')' : '') + '</b>' +
    B('tpAutoLayout(&quot;tree&quot;)', '🌲 分层', '核心在上, 汇聚、接入逐层往下; 同层按连接关系排序, 交叉最少', false, lk) +
    B('tpAutoLayout(&quot;radial&quot;)', '🎯 径向', '核心在中间, 一层一圈', false, lk) +
    B('tpAutoLayout(&quot;force&quot;)', '🕸 力导向', '按连接关系自然散开, 适合网状结构', false, lk) +
    B('tpAutoLayout(&quot;grid&quot;)', '▦ 网格', '按名称排成网格', false, lk) +
    B('tpAutoLayout(&quot;group&quot;)', '🗂 分组', '按设备管理里填的分组, 每组一块排在一起(组内按分层排); 没分组的排在最后', false, lk) +
    B('tpDeclutter()', '✨ 整理重叠', '只把挤在一起/重叠的节点推开, 其他位置不动', false, lk) +
    B('tpUndo()', '↶', '撤销 (Ctrl+Z)', false, !TP.undo.length) + B('tpRedo()', '↷', '重做 (Ctrl+Y)', false, !TP.redo.length) + '</span>';
  h += '<span class="grp"><b>编辑</b>' +
    B('tpSetTool(&quot;pan&quot;)', '✋', '拖动空白处平移画布(Shift+拖动 = 框选)', TP.tool === 'pan') +
    B('tpSetTool(&quot;box&quot;)', '⬚ 框选', '拖动空白处框选多个节点', TP.tool === 'box') +
    B('tpStartAddNode()', '＋ 节点', '添加不支持 SNMP 的设备: 防火墙、路由器、运营商线路、服务器...') +
    B('tpToggleLinkMode()', '🔗 连线', '依次点两个节点, 手工连一条链路', !!TP.linkMode) +
    (hid ? B('tpUnhideAll()', '👁 恢复隐藏(' + hid + ')', '恢复所有隐藏的链路') : '') +
    B('tpTogglePop(&quot;bg&quot;, this)', '🖼 底图', '上传/清除底图(比如楼层平面图), 调透明度') +
    B('tpToggleLock()', lk ? '🔒 已锁定' : '🔓 可拖动', lk ? '布局已锁定, 点一下解锁' : '点一下锁定布局, 防止误拖', lk) + '</span>';
  if (n >= 2 && !lk) {
    h += '<span class="grp"><b>对齐</b>' + B('tpAlign(&quot;left&quot;)', '⇤', '左对齐') + B('tpAlign(&quot;hcenter&quot;)', '↔', '竖成一列(水平居中)') + B('tpAlign(&quot;right&quot;)', '⇥', '右对齐') +
      B('tpAlign(&quot;top&quot;)', '⤒', '顶对齐') + B('tpAlign(&quot;vcenter&quot;)', '↕', '排成一行(垂直居中)') + B('tpAlign(&quot;bottom&quot;)', '⤓', '底对齐') +
      (n >= 3 ? B('tpAlign(&quot;hdist&quot;)', '⋯', '水平等距') + B('tpAlign(&quot;vdist&quot;)', '⋮', '垂直等距') : '') + '</span>';
  }
  h += '<span class="grp"><b>显示</b>' + B('tpTogglePop(&quot;show&quot;, this)', '👁 选项 ▾', '备注 / 端口名 / 流动动画 / 未监控邻居(' + ext + ')') + '</span>';
  h += '<span class="grp">' + B('tpZoomAt(1/1.25)', '−', '缩小') + '<button class="zoom" id="tpZoomTxt" onclick="tpZoomAt(1/TP.view.s)" title="缩放到 100%">' + Math.round(TP.view.s * 100) + '%</button>' + B('tpZoomAt(1.25)', '＋', '放大') + B('tpFit()', '⤢ 适应', '把整张图缩放到窗口里') + '</span>';
  bar.innerHTML = h;
  var st = byId('tpStage');
  if (st) { st.classList.toggle('unlocked', !lk); st.classList.toggle('box', TP.tool === 'box'); st.classList.toggle('pan', TP.tool === 'pan'); st.classList.toggle('linking', !!TP.linkMode); }
}
function tpSetTool(t) { TP.tool = t; tpSyncBar(); tpStatus(); }
function tpToggleLock() { TP.opt.locked = !TP.opt.locked; tpSaveOpt(); tpSyncBar(); tpNotice(TP.opt.locked ? '布局已锁定: 节点不能拖动, 单击仍可查看' : '已解锁: 可以直接拖动节点', true); }
function tpTogglePop(kind, btn) {
  var pop = byId('tpPop'); if (!pop) { return; }
  if (pop.style.display === 'block' && pop.getAttribute('data-k') === kind) { pop.style.display = 'none'; return; }
  var h = '';
  if (kind === 'show') {
    var cb = function (k, t) { return '<label><input type="checkbox"' + (TP.opt[k] ? ' checked' : '') + ' onchange="tpSetOpt(&quot;' + k + '&quot;, this.checked)"> ' + t + '</label>'; };
    h = cb('note', '显示设备备注(设备管理里填的)') + cb('ports', '显示链路两端端口名') + cb('flow', '流量流动动画(方向 = 主要流向, 越快流量越大)') + cb('groups', '分组底色(设备管理里填的分组, 同组的圈在一起)') +
      '<div class="t">未监控的 LLDP 邻居(' + TP.data.externals.length + ' 个)</div>' +
      [['auto', '自动(多于 40 个时折叠)'], ['show', '全部显示'], ['fold', '折叠成设备上的 +N'], ['hide', '隐藏']].map(function (o) { return '<label><input type="radio" name="tpExtM"' + (TP.opt.ext === o[0] ? ' checked' : '') + ' onchange="tpSetOpt(&quot;ext&quot;, &quot;' + o[0] + '&quot;)"> ' + o[1] + '</label>'; }).join('');
  } else if (kind === 'bg') {
    h = '<div class="t">底图: 楼层平面图 / 机房照片, 节点摆到实际位置上</div>' +
      '<div style="display:flex;gap:6px;margin:6px 0;"><button class="filter-btn" onclick="tpNeedEdit(function(){byId(&quot;topoImgFile&quot;).click();})">上传图片</button>' +
      (TP.data.has_background ? '<button class="filter-btn" style="color:var(--bad);" onclick="tpNeedEdit(clearTopoBackground)">清除底图</button>' : '') + '</div>' +
      (TP.data.has_background ? '<div class="t">透明度</div><input type="range" min="0.1" max="1" step="0.05" value="' + TP.opt.bgAlpha + '" oninput="tpSetOpt(&quot;bgAlpha&quot;, +this.value)" style="width:100%;">' : '') +
      '<input type="file" id="topoImgFile" accept="image/png,image/jpeg,image/webp,image/gif" style="display:none;" onchange="handleTopoImgFile(this.files)">';
  }
  pop.innerHTML = h; pop.setAttribute('data-k', kind); pop.style.display = 'block';
  var r = btn.getBoundingClientRect(), sr = byId('tpStage').getBoundingClientRect();
  pop.style.left = Math.max(6, Math.min(r.left - sr.left, sr.width - pop.offsetWidth - 10)) + 'px'; pop.style.top = '6px';
}
function tpSetOpt(k, v) {
  TP.opt[k] = v; tpSaveOpt();
  if (k === 'ext') { TP.extOpen = {}; tpBuild(TP.data); }
  if (k === 'bgAlpha') { var bg = byId('tpBg'); if (bg) { bg.setAttribute('opacity', v); } return; }
  tpDrawAll(); tpLegend();
}
function tpLegend() {
  var el = byId('tpLegend'); if (!el || !TP.data) { return; }
  var cnt = { lldp: 0, cdp: 0, mac: 0, manual: 0 }, phys = 0; TP.links.forEach(function (l) { tpMembers(l).forEach(function (x) { cnt[x.source] = (cnt[x.source] || 0) + 1; phys++; }); });
  var sw = function (c) { return '<i style="background:' + c + ';"></i>'; };
  el.innerHTML = '<span>' + sw(TP_ROLE_COLOR.core) + '核心' + sw(TP_ROLE_COLOR.agg) + '汇聚' + sw(TP_ROLE_COLOR.access) + '接入' + sw('#64748b') + '离线/孤立' + sw('#a855f7') + '手工节点</span>' +
    '<span>链路: 实线 LLDP ' + cnt.lldp + (cnt.cdp ? ' · CDP ' + cnt.cdp : '') + ' · 虚线 推断 ' + cnt.mac + ' · 点划线 手工 ' + cnt.manual + '</span>' +
    '<span>颜色 = 利用率' + sw('#3b82f6') + '空闲' + sw('#22c55e') + '&lt;50%' + sw('#eab308') + '50-80%' + sw('#ef4444') + '&gt;80% · 粗细 = 速率 · 橙边 = 聚合</span>' +
    '<span>共 ' + TP.data.devices.length + ' 台设备' + (TP.data.manual_nodes.length ? ' + ' + TP.data.manual_nodes.length + ' 个手工节点' : '') + ', ' + TP.links.length + ' 条连线' + (phys > TP.links.length ? '(' + phys + ' 条链路)' : '') + '</span>';
}
function tpStatus(msg) {
  var el = byId('tpStatus'); if (!el) { return; }
  var n = tpSelCount();
  el.innerText = msg || ((n ? '已选 ' + n + ' 个 · ' : '') + (TP.linkMode ? '连线: ' + (TP.linkMode.a ? '再点另一个节点(Esc 取消)' : '点第一个节点') :
    TP.opt.locked ? '布局已锁定 · 单击查看, 双击打开详情' : '拖动节点移动 · ' + (TP.tool === 'box' ? '拖空白框选' : '拖空白平移, Shift+拖动框选') + ' · 右键更多操作'));
  if (msg) { clearTimeout(TP.stT); TP.stT = setTimeout(function () { tpStatus(); }, 1600); }
}
function tpNotice(t, ok) { topoMsg(t, ok); }
function topoMsg(text, ok) {
  var n = byId('topoNotice'); if (!n) { return; }
  n.className = 'notice ' + (ok ? 'notice-ok' : 'notice-err');
  n.innerText = text; n.style.display = 'block';
  clearTimeout(n._h); n._h = setTimeout(function () { n.style.display = 'none'; }, 3800);
}

/* ---------- 对齐 ---------- */
function tpAlign(mode) {
  var keys = Object.keys(TP.sel).filter(function (k) { return TP.pos[k]; }); if (keys.length < 2 || TP.opt.locked) { return; }
  var b = tpBBoxOf(TP.pos, keys), tgt = {};
  var sx = keys.slice().sort(function (a, c) { return TP.pos[a].x - TP.pos[c].x; }), sy = keys.slice().sort(function (a, c) { return TP.pos[a].y - TP.pos[c].y; });
  keys.forEach(function (k) {
    var p = TP.pos[k], x = p.x, y = p.y;
    if (mode === 'left') { x = b.x0; } else if (mode === 'right') { x = b.x1; } else if (mode === 'hcenter') { x = (b.x0 + b.x1) / 2; }
    else if (mode === 'top') { y = b.y0; } else if (mode === 'bottom') { y = b.y1; } else if (mode === 'vcenter') { y = (b.y0 + b.y1) / 2; }
    else if (mode === 'hdist') { x = b.x0 + (b.x1 - b.x0) * sx.indexOf(k) / (keys.length - 1); }
    else if (mode === 'vdist') { y = b.y0 + (b.y1 - b.y0) * sy.indexOf(k) / (keys.length - 1); }
    tgt[k] = { x: x, y: y };
  });
  // 排成一行/一列后挤在一起的自动拉开
  if (mode !== 'hdist' && mode !== 'vdist') {
    var row = mode === 'vcenter' || mode === 'top' || mode === 'bottom', ord = row ? sx : sy, ax = row ? 'x' : 'y', gap = row ? TP_NODE_W : TP_NODE_H;
    for (var i = 1; i < ord.length; i++) { if (tgt[ord[i]][ax] - tgt[ord[i - 1]][ax] < gap) { tgt[ord[i]][ax] = tgt[ord[i - 1]][ax] + gap; } }
  }
  tpPushUndo();
  tpAnimateTo(tgt, function () { tpQueueSave(keys); });
}

/* ---------- 鼠标 / 键盘 ---------- */
function tpBind() {
  var st = byId('tpStage'), svg = byId('topoSvg');
  svg.addEventListener('wheel', function (e) { e.preventDefault(); tpZoomAt(Math.pow(1.0018, -e.deltaY * (e.deltaMode ? 30 : 1)), tpScreen(e)); }, { passive: false });
  svg.addEventListener('contextmenu', function (e) { e.preventDefault(); if (TP.drag && TP.drag.moved) { return; } tpOpenMenu(e); });
  svg.addEventListener('mousedown', tpDown);
  svg.addEventListener('mousemove', tpHover);
  svg.addEventListener('mouseleave', function () { if (!TP.drag) { hideTopoTip(); } });
  svg.addEventListener('dblclick', function (e) {
    var tg = tpTarget(e);
    if (tg.t === 'node') { var n = TP.byKey[tg.k]; if (n && n.t === 'dev') { tpOpenDevice(n.key); } else if (n) { tpOpenSide({ t: 'node', k: n.key, form: true }); } }
    else if (tg.t === 'blank') { tpZoomAt(1.5, tpScreen(e)); }
  });
  byId('tpMini').addEventListener('mousedown', function (e) { e.preventDefault(); tpMiniJump(e); var mv = function (ev) { tpMiniJump(ev); }, up = function () { window.removeEventListener('mousemove', mv); window.removeEventListener('mouseup', up); }; window.addEventListener('mousemove', mv); window.addEventListener('mouseup', up); });
  st.addEventListener('mousedown', function (e) { if (!e.target.closest('.tp2-menu')) { tpHideMenu(); } if (!e.target.closest('.tp2-pop') && !e.target.closest('.tp2-bar')) { var p = byId('tpPop'); if (p) { p.style.display = 'none'; } } }, true);
  if (window.ResizeObserver) { new ResizeObserver(function () { tpMiniLater(); }).observe(st); }
}
function tpTarget(e) {
  var t = e.target, g;
  if (!t || !t.closest) { return { t: 'blank' }; }
  if ((g = t.closest('.more'))) { return { t: 'more', k: g.getAttribute('data-more') }; }
  if ((g = t.closest('.tpn'))) { return { t: 'node', k: g.getAttribute('data-k') }; }
  if ((g = t.closest('.tpe'))) { return { t: 'ext', e: TP.data.externals[+g.getAttribute('data-e')] }; }
  if ((g = t.closest('.tpl'))) { return { t: 'link', l: TP.links[+g.getAttribute('data-i')] }; }
  return { t: 'blank' };
}
function tpDown(e) {
  var tg = tpTarget(e), w = tpWorld(e), sp = tpScreen(e), multi = e.ctrlKey || e.metaKey || e.shiftKey;
  hideTopoTip(); tpHideMenu();
  if (e.button === 2) { TP.drag = { mode: 'rpan', sx: sp.x, sy: sp.y, tx: TP.view.tx, ty: TP.view.ty, moved: false }; }
  else if (e.button === 1) { e.preventDefault(); TP.drag = { mode: 'pan', sx: sp.x, sy: sp.y, tx: TP.view.tx, ty: TP.view.ty, moved: false }; }
  else if (e.button !== 0) { return; }
  else if (tg.t === 'more') { e.stopPropagation(); TP.extOpen[tg.k] = true; tpDrawAll(); return; }
  else if (TP.linkMode && tg.t === 'node') { tpLinkPick(tg.k); return; }
  else if (tg.t === 'node') {
    e.preventDefault();
    if (multi) { if (TP.sel[tg.k]) { delete TP.sel[tg.k]; } else { TP.sel[tg.k] = true; } TP.selLink = null; tpApplySel(); if (!TP.sel[tg.k]) { return; } }
    else if (!TP.sel[tg.k]) { TP.sel = {}; TP.sel[tg.k] = true; TP.selLink = null; tpApplySel(); }
    var start = {}; Object.keys(TP.sel).forEach(function (k) { if (TP.pos[k]) { start[k] = { x: TP.pos[k].x, y: TP.pos[k].y }; } });
    TP.drag = { mode: 'move', wx: w.x, wy: w.y, start: start, moved: false, key: tg.k, multi: multi, undo: JSON.stringify(TP.pos) };
  } else if (tg.t === 'ext') {
    e.preventDefault();
    var p = TP.extPos[tg.e.key];
    TP.drag = { mode: 'ext', e: tg.e, wx: w.x, wy: w.y, x0: p.x, y0: p.y, moved: false };
  } else if (tg.t === 'link') {
    TP.drag = { mode: 'link', l: tg.l, sx: sp.x, sy: sp.y, tx: TP.view.tx, ty: TP.view.ty, moved: false };
  } else if (TP.tool === 'box' || e.shiftKey) {
    e.preventDefault();
    TP.drag = { mode: 'box', wx: w.x, wy: w.y, add: multi, base: JSON.parse(JSON.stringify(TP.sel)), moved: false };
  } else {
    e.preventDefault();
    TP.drag = { mode: 'pan', sx: sp.x, sy: sp.y, tx: TP.view.tx, ty: TP.view.ty, moved: false, blank: true };
  }
  window.addEventListener('mousemove', tpMove);
  window.addEventListener('mouseup', tpUp);
}
function tpMove(e) {
  var d = TP.drag; if (!d) { return; }
  if (d.mode === 'pan' || d.mode === 'rpan' || d.mode === 'link') {
    var sp = tpScreen(e);
    if (Math.abs(sp.x - d.sx) + Math.abs(sp.y - d.sy) > 3) { d.moved = true; }
    if (d.moved) { TP.view.tx = d.tx + (sp.x - d.sx); TP.view.ty = d.ty + (sp.y - d.sy); tpApplyView(); byId('tpStage').classList.add('panning'); }
  } else if (d.mode === 'move') {
    var w = tpWorld(e), dx = w.x - d.wx, dy = w.y - d.wy;
    if (!d.moved && Math.abs(dx) + Math.abs(dy) < 3 / TP.view.s) { return; }
    if (TP.opt.locked) { if (!d.warned) { d.warned = true; tpStatus('布局已锁定, 点工具条上的 🔒 解锁后才能拖动'); } return; }
    d.moved = true;
    Object.keys(d.start).forEach(function (k) { TP.pos[k] = { x: d.start[k].x + dx, y: d.start[k].y + dy }; });
    tpGeometry();
  } else if (d.mode === 'ext') {
    if (TP.opt.locked) { return; }
    var w2 = tpWorld(e); d.moved = true;
    var p = { x: d.x0 + w2.x - d.wx, y: d.y0 + w2.y - d.wy };
    TP.extPos[d.e.key] = p; d.e.has_pos = true; d.e._auto = false; d.e.x = p.x / TP.W * 100; d.e.y = p.y / TP.H * 100;
    tpGeometry();
  } else if (d.mode === 'box') {
    var w3 = tpWorld(e), r = byId('tpMarquee');
    var x0 = Math.min(d.wx, w3.x), y0 = Math.min(d.wy, w3.y), x1 = Math.max(d.wx, w3.x), y1 = Math.max(d.wy, w3.y);
    r.style.display = ''; r.setAttribute('x', x0); r.setAttribute('y', y0); r.setAttribute('width', x1 - x0); r.setAttribute('height', y1 - y0); r.setAttribute('stroke-width', 1 / TP.view.s);
    var sel = d.add ? JSON.parse(JSON.stringify(d.base)) : {};
    TP.nodes.forEach(function (n) { var p2 = TP.pos[n.key]; if (p2 && p2.x >= x0 && p2.x <= x1 && p2.y >= y0 && p2.y <= y1) { sel[n.key] = true; } });
    TP.sel = sel; d.moved = true;
    document.querySelectorAll('#tpNodes .tpn').forEach(function (g) { g.classList.toggle('sel', !!TP.sel[g.getAttribute('data-k')]); });
  }
}
function tpUp(e) {
  window.removeEventListener('mousemove', tpMove); window.removeEventListener('mouseup', tpUp);
  var d = TP.drag; TP.drag = null; if (!d) { return; }
  var st = byId('tpStage'); if (st) { st.classList.remove('panning'); }
  if (d.mode === 'move') {
    if (d.moved) { TP.undo.push(d.undo); if (TP.undo.length > 60) { TP.undo.shift(); } TP.redo = []; tpQueueSave(Object.keys(d.start)); tpSyncBar(); }
    else if (!d.multi) { TP.sel = {}; TP.sel[d.key] = true; tpApplySel(); tpOpenSide({ t: 'node', k: d.key }); }
  } else if (d.mode === 'ext') { if (d.moved) { tpQueueSave([d.e.key]); } else { tpOpenSide({ t: 'ext', e: d.e }); } }
  else if (d.mode === 'box') { byId('tpMarquee').style.display = 'none'; if (!d.moved && !d.add) { tpClearSel(); tpCloseSide(); } tpApplySel(); }
  else if (d.mode === 'pan' && !d.moved && d.blank) { if (TP.linkMode) { TP.linkMode = null; byId('tpRubber').style.display = 'none'; tpSyncBar(); } tpClearSel(); tpCloseSide(); }
  else if (d.mode === 'link' && !d.moved) { TP.sel = {}; TP.selLink = d.l._i; tpApplySel(); tpOpenSide({ t: 'link', l: d.l }); }
  else if (d.mode === 'rpan' && d.moved) { TP.suppressMenu = true; setTimeout(function () { TP.suppressMenu = false; }, 50); }
  tpStatus();
}
function tpHover(e) {
  if (TP.linkMode && TP.linkMode.a) {
    var a = TP.pos[TP.linkMode.a], w = tpWorld(e), r = byId('tpRubber');
    r.style.display = ''; r.setAttribute('x1', a.x); r.setAttribute('y1', a.y); r.setAttribute('x2', w.x); r.setAttribute('y2', w.y); r.setAttribute('stroke-width', 2 / TP.view.s);
  }
  if (TP.drag) { return; }
  var tg = tpTarget(e);
  document.querySelectorAll('#tpLinks .tpl.hl').forEach(function (g) { g.classList.remove('hl'); });
  document.querySelectorAll('#tpNodes .tpn.hot').forEach(function (g) { g.classList.remove('hot'); });
  if (tg.t === 'node') {
    var n = TP.byKey[tg.k]; if (!n) { return; }
    showTopoTip(n.t === 'dev' ? deviceTipHTML(n.d) : tpManualTip(n.d), e);
    document.querySelectorAll('#tpLinks .tpl').forEach(function (g) {
      var l = TP.links[+g.getAttribute('data-i')];
      if (l.a === tg.k || l.b === tg.k) { g.classList.add('hl'); var o = l.a === tg.k ? l.b : l.a, og = document.querySelector('#tpNodes .tpn[data-k="' + cssEscape(o) + '"]'); if (og) { og.classList.add('hot'); } }
    });
  } else if (tg.t === 'ext') { showTopoTip(externalTipHTML(tg.e), e); }
  else if (tg.t === 'link') { showTopoTip(tpLinkTip(tg.l), e); }
  else if (tg.t === 'more') { showTopoTip('点击展开 ' + (TP.extCount[tg.k] || 0) + ' 个未监控的 LLDP 邻居', e); }
  else { hideTopoTip(); }
}
window.addEventListener('keydown', function (e) {
  if (byId('topoModal').style.display === 'none' || !byId('tpNodes') || T3.on) { return; }
  if (byId('authModal') && byId('authModal').style.display === 'flex') { return; }
  var tag = (document.activeElement || {}).tagName || '';
  if (e.key === 'Escape') {
    if (TP.menu) { tpHideMenu(); } else if (TP.linkMode) { TP.linkMode = null; byId('tpRubber').style.display = 'none'; tpSyncBar(); tpStatus(); }
    else if (TP.side) { tpCloseSide(); } else if (tpSelCount() || TP.selLink !== null) { tpClearSel(); } else { return; }
    e.preventDefault(); e.stopImmediatePropagation(); return;
  }
  if (/INPUT|TEXTAREA|SELECT/.test(tag)) { return; }
  var ctrl = e.ctrlKey || e.metaKey, keys = Object.keys(TP.sel), key = e.key.toLowerCase();
  if (ctrl && key === 'a') { e.preventDefault(); tpSelectAll(); return; }
  if (ctrl && key === 'z' && !e.shiftKey) { e.preventDefault(); tpUndo(); return; }
  if (ctrl && (key === 'y' || (key === 'z' && e.shiftKey))) { e.preventDefault(); tpRedo(); return; }
  if (key === 'f' && !ctrl) { e.preventDefault(); tpFit(keys.length ? keys : null); return; }
  if ((e.key === 'Delete' || e.key === 'Backspace') && TP.side) {
    if (TP.side.t === 'node' && TP.side.k.indexOf('m:') === 0) { e.preventDefault(); tpDelNode(); }
    else if (TP.side.t === 'link' && TP.side.l.source === 'manual') { e.preventDefault(); tpDelLink(); }
    return;
  }
  var dx = { ArrowLeft: -1, ArrowRight: 1 }[e.key] || 0, dy = { ArrowUp: -1, ArrowDown: 1 }[e.key] || 0;
  if ((dx || dy) && keys.length && !TP.opt.locked) {
    e.preventDefault(); var step = e.shiftKey ? 20 : 4;
    if (!e.repeat) { tpPushUndo(); }
    keys.forEach(function (k) { if (TP.pos[k]) { TP.pos[k].x += dx * step; TP.pos[k].y += dy * step; } });
    tpGeometry(); tpQueueSave(keys);
  }
}, true);

/* ---------- 右键菜单 ---------- */
function tpOpenMenu(e) {
  if (TP.suppressMenu) { return; }
  var tg = tpTarget(e), w = tpWorld(e), items = [];
  var I = function (txt, fn, dis) { items.push('<div' + (dis ? ' class="dis"' : '') + ' onclick="tpHideMenu();' + fn + '">' + txt + '</div>'); };
  if (tg.t === 'node') {
    var n = TP.byKey[tg.k], q = JSON.stringify(tg.k).replace(/"/g, '&quot;');
    if (!TP.sel[tg.k]) { TP.sel = {}; TP.sel[tg.k] = true; tpApplySel(); }
    items.push('<div class="t">' + esc(n.name) + '</div>');
    if (n.t === 'dev') { I('📋 打开设备详情', 'tpOpenDevice(' + q + ')'); }
    I('ℹ️ 信息面板', 'tpOpenSide({t:&quot;node&quot;,k:' + q + '})');
    I('🔗 从这里连线', 'tpNeedEdit(function(){TP.linkMode={a:' + q + '};tpSyncBar();tpStatus();})');
    I('🎯 选中相连的设备', 'tpSelectNeighbors(' + q + ')');
    if (TP.extCount[tg.k]) { I(TP.extOpen[tg.k] || TP.extMode === 'show' ? '➖ 收起未监控邻居' : '➕ 展开 ' + TP.extCount[tg.k] + ' 个未监控邻居', 'tpToggleExt(' + q + ')'); }
    if (n.t === 'man') { items.push('<hr>'); I('✏️ 编辑节点', 'tpOpenSide({t:&quot;node&quot;,k:' + q + ',form:true})'); I('🗑 删除节点', 'TP.side={t:&quot;node&quot;,k:' + q + '};tpDelNode()'); }
  } else if (tg.t === 'link') {
    var l = tg.l; TP.selLink = l._i; tpApplySel();
    items.push('<div class="t">' + esc(tpNameOf(l.a)) + ' ↔ ' + esc(tpNameOf(l.b)) + '</div>');
    I(l._bundle ? 'ℹ️ 查看这 ' + l._bundle.length + ' 条链路' : 'ℹ️ 查看链路', 'tpOpenSide({t:&quot;link&quot;,l:TP.links[' + l._i + ']})');
    if (l._bundle) { /* 捆在一起的多条: 在信息面板里逐条操作 */ }
    else if (l.source === 'manual') { I('✏️ 编辑', 'tpOpenSide({t:&quot;link&quot;,l:TP.links[' + l._i + '],form:true})'); I('🗑 删除链路', 'TP.side={t:&quot;link&quot;,l:TP.links[' + l._i + ']};tpDelLink()'); }
    else {
      if (l.source === 'mac') { I('📌 转成手工链路', 'TP.side={t:&quot;link&quot;,l:TP.links[' + l._i + ']};tpPromoteLink()'); }
      I('🙈 隐藏这条链路', 'TP.side={t:&quot;link&quot;,l:TP.links[' + l._i + ']};tpHideLink()');
    }
  } else {
    TP.menuAt = w;
    I('＋ 在这里添加节点', 'tpStartAddNode(TP.menuAt)');
    I('⤢ 适应窗口', 'tpFit()');
    I('全选 (Ctrl+A)', 'tpSelectAll()');
    items.push('<hr>');
    I('🌲 自动编排: 分层', 'tpAutoLayout(&quot;tree&quot;)', TP.opt.locked); I('✨ 整理重叠', 'tpDeclutter()', TP.opt.locked);
    I(TP.opt.locked ? '🔓 解锁布局' : '🔒 锁定布局', 'tpToggleLock()');
  }
  var m = byId('tpMenu'), sp = tpScreen(e), sz = tpStageSize();
  m.innerHTML = items.join(''); m.style.display = 'block'; TP.menu = true;
  m.style.left = Math.min(sp.x, sz.w - m.offsetWidth - 6) + 'px'; m.style.top = Math.min(sp.y, sz.h - m.offsetHeight - 6) + 'px';
}
function tpHideMenu() { var m = byId('tpMenu'); if (m) { m.style.display = 'none'; } TP.menu = false; }
function tpToggleExt(k) { if (TP.extMode === 'show') { TP.opt.ext = 'fold'; tpSaveOpt(); tpBuild(TP.data); } TP.extOpen[k] = !TP.extOpen[k]; tpDrawAll(); }

/* ---------- 提示框 / 设备 ---------- */
function tpManualTip(m) {
  return '<b>' + (TP_KIND_ICON[m.kind] || '') + ' ' + esc(m.name) + '</b><br><span style="color:#94a3b8;">手工节点 · ' + esc(TP_KIND_NAME[m.kind] || m.kind) + '</span>' + (m.ip ? '<br>' + esc(m.ip) : '') + (m.note ? '<br>📝 ' + esc(m.note) : '');
}
function tpLinkTip(l) {
  var src = '<div style="margin-bottom:4px;color:#93c5fd;">来源: ' + (TP_SRC_NAME[l.source] || l.source) + (l.note ? ' · <span style="color:#cbd5e1;white-space:normal;">' + esc(l.note) + '</span>' : '') + '</div>';
  var names = {}; names[l.a] = { name: tpNameOf(l.a) }; names[l.b] = { name: tpNameOf(l.b) };
  return src + linkTipHTML(l, names) + '<div style="color:#94a3b8;margin-top:4px;">单击查看 · 右键更多</div>';
}
function tpOpenDevice(ip) { openModal(ip); }

/* ---------- 侧边信息面板 ---------- */
function tpOpenSide(s) { TP.side = s; tpRenderSide(); var st = byId('tpStage'); if (st) { st.classList.toggle('side-open', !!TP.side); } }
function tpCloseSide() { TP.side = null; var el = byId('tpSide'); if (el) { el.style.display = 'none'; } var st = byId('tpStage'); if (st) { st.classList.remove('side-open'); } }
function tpRenderSide() {
  var el = byId('tpSide'), s = TP.side; if (!el || !s) { return; }
  var h = '<button class="x" onclick="tpCloseSide()">&times;</button>';
  if (s.t === 'node' && TP.byKey[s.k] && TP.byKey[s.k].t === 'dev' && !s.form) { h += tpSideDevice(TP.byKey[s.k]); }
  else if (s.t === 'newnode' || (s.t === 'node' && TP.byKey[s.k] && TP.byKey[s.k].t === 'man')) { h += tpSideManual(s); }
  else if (s.t === 'newlink' || s.t === 'link') { h += tpSideLink(s); }
  else if (s.t === 'ext') {
    var e = s.e;
    h += '<h4>◯ ' + esc(e.name) + '</h4><div class="h">未被监控的 LLDP 邻居</div><div class="kv"><span>接在</span><b>' + esc(tpNameOf(e.device)) + '</b><span>端口</span><b>' + esc(e.port) + '</b><span>管理 IP</span><b>' + esc(e.ip || '对端没上报') + '</b></div>' +
      (e.ip ? '<div class="row"><button class="primary" onclick="quickAddFromLLDP(&quot;' + esc(e.ip) + '&quot;, &quot;' + esc(e.name) + '&quot;)">＋ 添加到监控</button></div>' : '') +
      '<div class="h">' + (e.ip ? '' : '对端 LLDP 没带管理地址, 可以在「⚙️ 设备管理 → 🔍 LLDP 发现」里看全部邻居, 或手工添加; ') + '不支持 SNMP 的设备可以右键空白处添加成手工节点再连线。</div>' +
      (e.has_pos && !e._auto ? '<div class="row"><button onclick="tpResetExt()">回到自动位置</button></div>' : '');
  } else { tpCloseSide(); return; }
  el.innerHTML = h; el.style.display = 'block';
  if (s.t === 'newlink' || (s.t === 'link' && s.form)) { tpFillPortLists(s); }
}
function tpSideDevice(n) {
  var d = n.d, role = TP.roles[n.key] || 'lone', q = JSON.stringify(n.key).replace(/"/g, '&quot;');
  var bar = function (v) { var c = v >= 85 ? '#ef4444' : v >= 60 ? '#eab308' : '#22c55e'; return '<div class="bar"><i style="width:' + Math.min(100, v || 0) + '%;background:' + c + ';"></i></div>'; };
  var links = TP.links.filter(function (l) { return l.a === n.key || l.b === n.key; });
  var h = '<h4><span style="width:10px;height:10px;border-radius:3px;background:' + (d.alive ? TP_ROLE_COLOR[role] : '#64748b') + ';display:inline-block;"></span>' + esc(n.name) + '</h4>' +
    '<div class="h">' + esc(d.ip) + ' · ' + esc(d.brand || '-') + (d.model ? ' · ' + esc(d.model) : '') + '</div>' +
    (d.note ? '<div class="note">📝 ' + esc(d.note) + '</div>' : '') +
    '<div class="kv"><span>状态</span><b style="color:' + (d.alive ? 'var(--good)' : 'var(--bad)') + ';">' + (d.alive ? '● 在线' : '● 离线') + '</b>' +
    '<span>角色</span><b>' + TP_ROLE_NAME[role] + (d.form && d.form.kind === 'stack' && d.form.members > 1 ? ' · 堆叠×' + d.form.members : d.form && d.form.kind === 'chassis' ? ' · 框式' : '') + '</b>' +
    '<span>CPU</span><div>' + (d.cpu_usage || 0) + '%' + bar(d.cpu_usage) + '</div>' +
    '<span>内存</span><div>' + (d.mem_usage > 0 ? d.mem_usage + '%' + bar(d.mem_usage) : '未采集') + '</div>' +
    '<span>运行</span><b>' + esc(d.uptime_str || '-') + '</b><span>接入率</span><b>' + esc(d.port_usage_ratio || '-') + '</b>' +
    '<span>告警</span><b style="color:' + (d.open_alerts ? 'var(--bad)' : 'var(--good)') + ';">' + (d.open_alerts ? d.open_alerts + ' 条未处理' : '无') + '</b></div>' +
    '<div class="h" style="margin-top:6px;">链路 ' + links.length + ' 条' + (TP.extCount[n.key] ? ' · 未监控邻居 ' + TP.extCount[n.key] + ' 个' : '') + '</div>';
  links.slice(0, 30).forEach(function (l) {
    var mine = l.a === n.key, st = tpLinkStyle(l);
    h += '<div class="lk" onclick="TP.selLink=' + l._i + ';tpApplySel();tpOpenSide({t:&quot;link&quot;,l:TP.links[' + l._i + ']})"><span style="color:' + st.color + ';">━</span> ' + esc(truncateLabel(mine ? l.a_port || '?' : l.b_port || '?', 26)) + ' → <b>' + esc(tpNameOf(mine ? l.b : l.a)) + '</b>' + (l._bundle ? ' <span class="lv mute">×' + l._bundle.length + '</span>' : '') +
      (st.util >= 0 ? ' <span style="color:var(--fg-dim);">' + st.util.toFixed(0) + '%</span>' : '') + '</div>';
  });
  h += '<div class="row"><button class="primary" onclick="tpOpenDevice(' + q + ')">📋 打开详情</button><button onclick="tpCenterOn(' + q + ', true)">🎯 居中</button></div>' +
    '<div class="row"><button onclick="tpNeedEdit(function(){TP.linkMode={a:' + q + '};tpSyncBar();tpStatus();})">🔗 从这里连线</button><button onclick="tpSelectNeighbors(' + q + ')">选中邻居</button></div>';
  return h;
}
function tpSideManual(s) {
  var isNew = s.t === 'newnode', m = isNew ? { id: '', name: '', kind: 'firewall', ip: '', note: '' } : TP.byKey[s.k].d, form = isNew || s.form;
  var kinds = Object.keys(TP_KIND_NAME).map(function (k) { return '<option value="' + k + '"' + (k === m.kind ? ' selected' : '') + '>' + TP_KIND_ICON[k] + ' ' + TP_KIND_NAME[k] + '</option>'; }).join('');
  if (!form) {
    var q = JSON.stringify(s.k).replace(/"/g, '&quot;');
    return '<h4>' + (TP_KIND_ICON[m.kind] || '◆') + ' ' + esc(m.name) + '</h4><div class="h">手工节点 · ' + esc(TP_KIND_NAME[m.kind] || m.kind) + '</div>' +
      (m.note ? '<div class="note">📝 ' + esc(m.note) + '</div>' : '') + '<div class="kv"><span>地址</span><b>' + esc(m.ip || '-') + '</b></div>' +
      '<div class="row"><button class="primary" onclick="tpNeedEdit(function(){TP.side.form=true;tpRenderSide();})">✏️ 编辑</button><button onclick="tpNeedEdit(function(){TP.linkMode={a:' + q + '};tpSyncBar();tpStatus();})">🔗 连线</button><button class="danger" onclick="tpNeedEdit(tpDelNode)">删除</button></div>';
  }
  return '<h4>' + (isNew ? '＋ 添加节点' : '✏️ ' + esc(m.name)) + '</h4>' +
    '<div class="h">不支持 SNMP 的设备(防火墙、路由器、运营商线路、服务器...)可以手工加到拓扑上, 再用「🔗 连线」连到交换机端口。</div>' +
    '<label>名称</label><input id="tpNName" value="' + esc(m.name) + '" maxlength="40">' +
    '<label>类型</label><select id="tpNKind">' + kinds + '</select>' +
    '<label>地址(选填, 只做展示)</label><input id="tpNIP" value="' + esc(m.ip || '') + '" maxlength="64">' +
    '<label>备注</label><input id="tpNNote" value="' + esc(m.note || '') + '" maxlength="120">' +
    '<div class="row"><button class="primary" onclick="tpNeedEdit(tpSaveNode)">' + (isNew ? '添加' : '保存') + '</button>' + (isNew ? '' : '<button class="danger" onclick="tpNeedEdit(tpDelNode)">删除</button>') + '<button onclick="tpCloseSide()">取消</button></div>';
}
function tpSideLink(s) {
  var isNew = s.t === 'newlink', l = isNew ? { a: s.a, b: s.b, a_port: '', b_port: '', note: '', source: 'manual' } : s.l, form = isNew || (s.form && l.source === 'manual');
  var st = isNew ? null : tpLinkStyle(l);
  if (!form && l._bundle) { return tpSideBundle(l, st); }
  if (!form) {
    var h = (s.back !== undefined ? '<div class="lk" onclick="tpOpenSide({t:&quot;link&quot;,l:TP.links[' + s.back + ']})">← 返回这两台设备之间的全部链路</div>' : '') + '<h4>🔗 链路</h4>' + (l.one_sided ? '<div class="h" style="color:var(--warn)">只有一端上报了这条链路(另一端没开 LLDP 或这个口没发 LLDP)</div>' : '') + '<div class="h">来源: ' + (TP_SRC_NAME[l.source] || l.source) + (l.is_agg ? ' · ⛓ 聚合 ' + (l.a_members || []).length + ' 条成员' : '') + '</div>' +
      (l.note ? '<div class="note">📝 ' + esc(l.note) + '</div>' : '') +
      '<div class="kv"><span>' + esc(truncateLabel(tpNameOf(l.a), 9)) + '</span><b>' + esc(l.a_port || '-') + '</b><span>' + esc(truncateLabel(tpNameOf(l.b), 9)) + '</span><b>' + esc(l.b_port || '-') + '</b>' +
      '<span>速率</span><b>' + fmtSpeed(Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0)) + '</b>' +
      '<span>利用率</span><div>' + (st.util >= 0 ? st.util.toFixed(1) + '%<div class="bar"><i style="width:' + Math.min(100, st.util) + '%;background:' + st.color + ';"></i></div>' : '-') + '</div>' +
      '<span>' + esc(truncateLabel(tpNameOf(l.a), 9)) + '</span><div>↓ ' + fmtMbps(l.a_in_mbps) + '<br>↑ ' + fmtMbps(l.a_out_mbps) + '</div></div>';
    if (l.is_agg) { h += '<div class="h">成员口: ' + (l.a_members || []).map(function (m, i) { return esc(m) + '↔' + esc((l.b_members || [])[i] || ''); }).join(', ') + '</div>'; }
    if (l.source === 'mac') { h += '<div class="h" style="margin-top:8px;">按 MAC 表推断: 两台设备互相在对方的 MAC 表里学到了对方的管理地址, 两个口都没有 LLDP 邻居。确认无误可以转成手工链路固定下来; 推断错了就隐藏。</div>'; }
    if (l.source === 'lldp' || l.source === 'cdp') { h += '<div class="h" style="margin-top:8px;">设备通过 ' + TP_SRC_NAME[l.source] + ' 上报, 不能编辑; 如果是错的(比如经过了不支持 LLDP 的转发设备)可以隐藏。</div>'; }
    h += '<div class="row">' +
      (l.source === 'manual' ? '<button class="primary" onclick="tpNeedEdit(function(){TP.side.form=true;tpRenderSide();})">✏️ 编辑</button><button class="danger" onclick="tpNeedEdit(tpDelLink)">删除</button>' : '') +
      (l.source === 'mac' ? '<button class="primary" onclick="tpNeedEdit(tpPromoteLink)">转成手工链路</button>' : '') +
      (l.source !== 'manual' ? '<button class="danger" onclick="tpNeedEdit(tpHideLink)">隐藏这条</button>' : '') + '</div>' +
      '<div class="row"><button onclick="tpCenterOn(' + JSON.stringify(l.a).replace(/"/g, '&quot;') + ')">定位 ' + esc(truncateLabel(tpNameOf(l.a), 8)) + '</button><button onclick="tpCenterOn(' + JSON.stringify(l.b).replace(/"/g, '&quot;') + ')">定位 ' + esc(truncateLabel(tpNameOf(l.b), 8)) + '</button></div>';
    return h;
  }
  var portIn = function (id, key, cur) { var n = TP.byKey[key]; return '<input id="' + id + '"' + (n && n.t === 'dev' ? ' list="' + id + 'L"' : '') + ' value="' + esc(cur || '') + '" placeholder="可选' + (n && n.t === 'dev' ? ', 比如 GigabitEthernet0/0/1' : '') + '">' + (n && n.t === 'dev' ? '<datalist id="' + id + 'L"></datalist>' : ''); };
  return '<h4>' + (isNew ? '🔗 新建手工链路' : '✏️ 编辑手工链路') + '</h4>' +
    '<label>' + esc(tpNameOf(l.a)) + ' 的端口</label>' + portIn('tpLAP', l.a, l.a_port) +
    '<label>' + esc(tpNameOf(l.b)) + ' 的端口</label>' + portIn('tpLBP', l.b, l.b_port) +
    '<label>备注</label><input id="tpLNote" value="' + esc(l.note || '') + '" maxlength="120" placeholder="比如: 电信 200M 专线">' +
    '<div class="row"><button class="primary" onclick="tpNeedEdit(tpSaveLink)">' + (isNew ? '添加' : '保存') + '</button><button onclick="tpCloseSide()">取消</button></div>';
}
// 两台设备之间有多条链路(捆成一根线)时的信息面板: 汇总 + 逐条列出
function tpSideBundle(l, st) {
  var n = l._bundle.length;
  var h = '<h4>🔗 ' + n + ' 条链路</h4><div class="h">' + esc(tpNameOf(l.a)) + ' ↔ ' + esc(tpNameOf(l.b)) + '</div>' +
    '<div class="kv"><span>总速率</span><b>' + fmtSpeed(Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0)) + '</b>' +
    '<span>利用率</span><div>' + (st.util >= 0 ? st.util.toFixed(1) + '%<div class="bar"><i style="width:' + Math.min(100, st.util) + '%;background:' + st.color + ';"></i></div>' : '-') + '</div>' +
    '<span>' + esc(truncateLabel(tpNameOf(l.a), 9)) + '</span><div>↓ ' + fmtMbps(l.a_in_mbps) + '<br>↑ ' + fmtMbps(l.a_out_mbps) + '</div></div>' +
    '<div class="h">点某一条看详情、隐藏或编辑:</div>';
  l._bundle.forEach(function (x, j) {
    var o = tpOrient(x, l.a), xs = tpLinkStyle(x);
    h += '<div class="lk" onclick="tpOpenSide({t:&quot;link&quot;,l:TP.links[' + l._i + ']._bundle[' + j + '],back:' + l._i + '})"><span style="color:' + xs.color + ';">━</span> ' +
      esc(o.aPort || '?') + ' ↔ ' + esc(o.bPort || '?') + ' <span class="lv mute">' + esc(TP_SRC_NAME[x.source] || x.source) + (x.is_agg ? ' · 聚合' : '') + '</span>' +
      (xs.util >= 0 ? ' <span style="color:var(--fg-dim);">' + xs.util.toFixed(0) + '%</span>' : '') + '</div>';
  });
  h += '<div class="h" style="margin-top:8px;">两台设备之间有多条独立的物理链路(没配成聚合, 或者一部分是手工连的线)时捆成一根线显示, 线上标 ×' + n + '。</div>' +
    '<div class="row"><button onclick="tpCenterOn(' + JSON.stringify(l.a).replace(/"/g, '&quot;') + ')">定位 ' + esc(truncateLabel(tpNameOf(l.a), 8)) + '</button><button onclick="tpCenterOn(' + JSON.stringify(l.b).replace(/"/g, '&quot;') + ')">定位 ' + esc(truncateLabel(tpNameOf(l.b), 8)) + '</button></div>';
  return h;
}
function tpFillPortLists(s) {
  [['tpLAP', s.l ? s.l.a : s.a], ['tpLBP', s.l ? s.l.b : s.b]].forEach(function (x) {
    var dl = byId(x[0] + 'L'); if (!dl) { return; }
    pub('/api/device?ip=' + encodeURIComponent(x[1])).then(function (d) {
      dl.innerHTML = (d.ports || []).map(function (p) { return '<option value="' + esc(p.name) + '">' + esc(p.status + (p.lldp_remote_dev && p.lldp_remote_dev !== '-' ? ' → ' + p.lldp_remote_dev : '')) + '</option>'; }).join('');
    }).catch(function () {});
  });
}
function tpResetExt() { var e = TP.side.e; e._auto = true; TP.pendClear[e.key] = true; tpQueueSave([]); tpGeometry(); tpCloseSide(); }

/* ---------- 手工节点 / 链路 ---------- */
function tpManualState() {
  var d = TP.data;
  return {
    nodes: d.manual_nodes.map(function (m) { return { id: m.id, name: m.name, kind: m.kind, ip: m.ip || '', note: m.note || '' }; }),
    links: d.links.filter(function (l) { return l.source === 'manual'; }).map(function (l) { return { id: l.id, a: l.a, a_port: l.a_port || '', b: l.b, b_port: l.b_port || '', note: l.note || '' }; }),
    hidden: (d.hidden || []).slice()
  };
}
function tpPostManual(st, msg, then) {
  return authedApi('/api/topology/manual', st).then(function () { tpNotice('✅ ' + msg, true); tpCloseSide(); if (then) { then(); } return tpFetch(false); })
    .catch(function (e) { tpNotice('❌ ' + e, false); });
}
function tpNewId(p, list) { var n = 1; while (list.some(function (x) { return x.id === p + n; })) { n++; } return p + n; }
function tpStartAddNode(at) {
  tpNeedEdit(function () {
    var sz = tpStageSize();
    TP.newAt = at || { x: (sz.w / 2 - TP.view.tx) / TP.view.s, y: (sz.h / 2 - TP.view.ty) / TP.view.s };
    tpOpenSide({ t: 'newnode', form: true }); setTimeout(function () { var i = byId('tpNName'); if (i) { i.focus(); } }, 50);
  });
}
function tpSaveNode() {
  var st = tpManualState(), s = TP.side, name = byId('tpNName').value.trim();
  if (!name) { tpNotice('名称不能为空', false); return; }
  var node = { name: name, kind: byId('tpNKind').value, ip: byId('tpNIP').value.trim(), note: byId('tpNNote').value.trim() };
  if (s.t === 'newnode') {
    node.id = tpNewId('n', st.nodes); st.nodes.push(node);
    var key = 'm:' + node.id, at = TP.newAt;
    tpPostManual(st, '已添加「' + name + '」, 右键它 →「从这里连线」连到设备上').then(function () {
      if (at) { TP.pos[key] = at; tpDeclutterInto(TP.pos, [key], 30); tpGeometry(); tpQueueSave([key]); }
      TP.sel = {}; TP.sel[key] = true; tpApplySel();
    });
  } else {
    node.id = s.k.slice(2);
    st.nodes = st.nodes.map(function (x) { return x.id === node.id ? node : x; });
    tpPostManual(st, '已保存');
  }
}
function tpDelNode() {
  var id = TP.side.k.slice(2), st = tpManualState();
  if (!confirm('删除节点「' + tpNameOf(TP.side.k) + '」和连到它的手工链路?')) { return; }
  st.nodes = st.nodes.filter(function (x) { return x.id !== id; });
  st.links = st.links.filter(function (l) { return l.a !== 'm:' + id && l.b !== 'm:' + id; });
  tpPostManual(st, '已删除');
}
function tpToggleLinkMode() {
  if (TP.linkMode) { TP.linkMode = null; byId('tpRubber').style.display = 'none'; tpSyncBar(); tpStatus(); return; }
  tpNeedEdit(function () { TP.linkMode = { a: null }; tpSyncBar(); tpStatus(); tpNotice('连线: 先点第一个节点, 再点另一个节点(Esc 取消)', true); });
}
function tpLinkPick(k) {
  if (!TP.linkMode.a) { TP.linkMode.a = k; TP.sel = {}; TP.sel[k] = true; tpApplySel(); tpStatus(); return; }
  if (TP.linkMode.a === k) { return; }
  var a = TP.linkMode.a; TP.linkMode = null; byId('tpRubber').style.display = 'none'; TP.sel = {}; TP.sel[a] = TP.sel[k] = true; tpApplySel(); tpSyncBar();
  tpOpenSide({ t: 'newlink', a: a, b: k, form: true });
}
function tpSaveLink() {
  var st = tpManualState(), s = TP.side;
  var l = { a_port: byId('tpLAP').value.trim(), b_port: byId('tpLBP').value.trim(), note: byId('tpLNote').value.trim() };
  if (s.t === 'newlink') { l.id = tpNewId('l', st.links); l.a = s.a; l.b = s.b; st.links.push(l); }
  else { l.id = s.l.id; l.a = s.l.a; l.b = s.l.b; st.links = st.links.map(function (x) { return x.id === l.id ? l : x; }); }
  tpPostManual(st, s.t === 'newlink' ? '已添加链路' : '已保存');
}
function tpDelLink() { var st = tpManualState(), id = TP.side.l.id; if (!confirm('删除这条手工链路?')) { return; } st.links = st.links.filter(function (x) { return x.id !== id; }); tpPostManual(st, '已删除链路'); }
function tpPromoteLink() {
  var st = tpManualState(), l = TP.side.l;
  st.links.push({ id: tpNewId('l', st.links), a: l.a, a_port: l.a_port, b: l.b, b_port: l.b_port, note: '由 MAC 表推断确认' });
  st.hidden.push(l.key);
  tpPostManual(st, '已转成手工链路');
}
function tpHideLink() { var st = tpManualState(); st.hidden.push(TP.side.l.key); TP.selLink = null; tpPostManual(st, '已隐藏这条链路, 工具条上可以恢复'); }
function tpUnhideAll() { tpNeedEdit(function () { var st = tpManualState(); st.hidden = []; tpPostManual(st, '已恢复全部隐藏的链路'); }); }

/* ---------- 底图 ---------- */
function handleTopoImgFile(files) {
  if (!files || !files[0]) { return; }
  var f = files[0];
  if (f.size > 8 * 1024 * 1024) { tpNotice('图片超过 8MB 上限', false); return; }
  var reader = new FileReader();
  reader.onload = function () {
    authedApi('/api/topology/background', { data: reader.result }).then(function () {
      tpNotice('底图已上传, 把设备拖到对应的实际位置即可', true); topoImgVersion++; var p = byId('tpPop'); if (p) { p.style.display = 'none'; } tpFetch(false);
    }).catch(function (e) { tpNotice('上传失败: ' + e, false); });
  };
  reader.readAsDataURL(f);
}
function clearTopoBackground() {
  if (!confirm('清除拓扑底图? 节点位置不会丢。')) { return; }
  authedApi('/api/topology/background/clear', {}).then(function () { tpNotice('底图已清除', true); topoImgVersion++; var p = byId('tpPop'); if (p) { p.style.display = 'none'; } tpFetch(false); })
    .catch(function (e) { tpNotice('清除失败: ' + e, false); });
}
