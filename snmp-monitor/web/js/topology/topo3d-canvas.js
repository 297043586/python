/*
 * 3D 拓扑视图。
 *
 * 环境是内网离线, 也不想为了一个视图把 600KB 的 three.js 塞进程序, 所以这里用
 * Canvas 2D 自己做了一个很小的 3D 渲染: 透视投影 + 轨道相机 + 按深度排序绘制
 * (画家算法)。几十到几百台交换机的规模完全够用, 不依赖 WebGL, 老电脑/虚拟机
 * 里也能跑。
 *
 *   - 分层: 从"连接最多、带宽最大"的设备做 BFS, 跳数 0/1/2/3+ 分别放在
 *     核心 / 汇聚 / 接入 / 边缘 四层平面上; 层内按径向树排布, 子节点靠近父节点。
 *     未被监控的 LLDP 邻居挂在所属设备下方一层, 画成小空心球。
 *   - 链路: 粗细 = 端口速率, 颜色 = 利用率(绿→黄→红), 聚合链路画成一束;
 *     两个方向各有一串流动粒子, 数量和速度随实时流量变化。
 *   - 设备: 核心/汇聚/接入用不同颜色, 有未处理告警的发红色脉冲光, 离线为灰色。
 *   - 交互: 左键拖动旋转、右键(或 Shift+左键)拖动平移、滚轮缩放、悬停看详情、
 *     单击打开设备详情、双击把视角飞到该设备; 自动旋转/粒子动画可关(低配电脑用),
 *     系统开启"减少动态效果"时默认关闭。每 30 秒自动刷新一次链路流量, 视角不变。
 */
/* ================= 3D 拓扑 ================= */
var T3 = {
  on: false, data: null, nodes: [], links: [], byId: {}, layerInfo: [],
  yaw: 0.6, pitch: 0.55, dist: 1400, tx: 0, ty: 0, tz: 0,
  auto: true, particles: true, raf: 0, last: 0, time: 0,
  drag: null, hover: null, hoverLink: null, proj: [], projLinks: [], idleUntil: 0,
  fly: null, refreshTimer: null, colors: null, search: ''
};
(function initT3Prefs() {
  var reduce = false;
  try { reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch (e) {}
  var get = function (k, d) { try { var v = localStorage.getItem(k); return v === null ? d : v === '1'; } catch (e) { return d; } };
  T3.on = get('topo3d', false);
  T3.auto = get('topo3dAuto', !reduce);
  T3.particles = get('topo3dParticles', !reduce);
})();
function t3Save(k, v) { try { localStorage.setItem(k, v ? '1' : '0'); } catch (e) {} }

// 3D (Canvas 兼容模式)的缩放 / 搜索定位; 入口调度见 topology/view.js
function t3ZoomBy(f) { T3.dist = Math.max(250, Math.min(6000, T3.dist / f)); t3Poke(); }
function t3Highlight() {
  T3.search = (byId('topoSearch').value || '').trim().toLowerCase();
  if (T3.search) {
    var hit = T3.nodes.filter(function (n) { return t3Match(n); })[0];
    if (hit) { t3FlyTo(hit); }
  }
  t3Poke();
}

function toggleTopo3D() {
  T3.on = !T3.on;
  t3Save('topo3d', T3.on);
  if (T3.on && topoEditMode) { toggleTopoEdit(); }
  t3SyncButtons();
  if (topoData) { renderTopology(topoData); }
}
function t3SyncButtons() {
  if (typeof tpSyncSeg === 'function') { tpSyncSeg(); }
  var ctl = byId('topo3dCtl');
  if (ctl) { ctl.style.display = T3.on ? 'inline-flex' : 'none'; }
  if (byId('t3Auto')) { byId('t3Auto').checked = T3.auto; byId('t3Particles').checked = T3.particles; }
  var hint = byId('topoHint');
  if (hint) {
    if (!hint.dataset.orig) { hint.dataset.orig = hint.innerText; }
    hint.innerText = T3.on ? '3D 视图: 左键拖动旋转, 右键或 Shift+拖动平移, 滚轮缩放; 悬停看详情, 单击打开设备, 双击飞到该设备; 挨得太近可以点「✋ 拖动摆放」手动调整。' : hint.dataset.orig;
  }
}

/* ---------- 布局 ---------- */
function t3Layout(data) {
  var nodes = [], byIdm = {}, adj = {};
  (data.devices || []).forEach(function (d) {
    var n = { id: d.ip, dev: d, ext: false, name: d.name || d.ip, deg: 0, bw: 0 };
    nodes.push(n); byIdm[n.id] = n; adj[n.id] = [];
  });
  // 手工节点(防火墙/路由器/运营商...): dev 给一个假对象, 画法见 tgBuildScene
  (data.manual_nodes || []).forEach(function (m) {
    var n = { id: 'm:' + m.id, man: true, mdata: m, dev: { ip: m.ip || '', name: m.name, alive: true, open_alerts: 0, form: { kind: 'box', members: 1 } }, ext: false, name: m.name, deg: 0, bw: 0 };
    nodes.push(n); byIdm[n.id] = n; adj[n.id] = [];
  });
  var links = [];
  // v13: 跟 2D 一样, 同一对设备之间捆成一根线, 跟自动链路重复的手工链路不画
  tpBundleLinks((data.links || []).filter(function (l) { return byIdm[l.a] && byIdm[l.b]; })).forEach(function (l) {
    var a = byIdm[l.a], b = byIdm[l.b];
    if (!a || !b) { return; }
    links.push({ l: l, a: a, b: b, ext: false });
    a.deg++; b.deg++;
    var sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0);
    a.bw += sp; b.bw += sp;
    adj[a.id].push(b.id); adj[b.id].push(a.id);
  });
  // 按连通分量分别选根(核心)。只看"连接数最多"会选错: 汇聚交换机下挂的接入
  // 交换机多, 连接数往往比核心还多。这里取图的中心(到本分量内所有节点最大
  // 跳数最小的点), 其次名字像核心(core/核心/hx)的, 再其次总带宽、连接数。
  var ecc = {};
  nodes.forEach(function (n) {
    var dist = {}, q = [n.id], far = 0;
    dist[n.id] = 0;
    while (q.length) {
      var c = q.shift();
      adj[c].forEach(function (nb) {
        if (dist[nb] === undefined) { dist[nb] = dist[c] + 1; if (dist[nb] > far) { far = dist[nb]; } q.push(nb); }
      });
    }
    ecc[n.id] = far;
  });
  var coreName = /core|核心|^hx|-hx/i;
  var layer = {}, parent = {}, roots = [];
  var order = nodes.slice().sort(function (x, y) {
    return ((x.man ? 1 : 0) - (y.man ? 1 : 0)) || (ecc[x.id] - ecc[y.id]) || ((coreName.test(y.name) ? 1 : 0) - (coreName.test(x.name) ? 1 : 0)) ||
      (y.bw - x.bw) || (y.deg - x.deg) || naturalCmp(x.name, y.name);
  });
  order.forEach(function (r) {
    if (layer[r.id] !== undefined) { return; }
    roots.push(r);
    layer[r.id] = 0; parent[r.id] = null;
    var q = [r.id];
    while (q.length) {
      var cur = q.shift();
      adj[cur].slice().sort(function (x, y) { return (byIdm[y].deg - byIdm[x].deg); }).forEach(function (nb) {
        if (layer[nb] === undefined) { layer[nb] = layer[cur] + 1; parent[nb] = cur; q.push(nb); }
      });
    }
  });
  nodes.forEach(function (n) { n.layer = Math.min(layer[n.id], 3); n.parent = parent[n.id]; });
  // 孤立设备(没有任何设备间链路)单独成组, 放到接入层, 不要每台都当成核心
  var isolated = roots.filter(function (r) { return r.deg === 0; });
  if (isolated.length && isolated.length < nodes.length) {
    isolated.forEach(function (n) { n.layer = 2; n.isolated = true; });
  }
  // 未被监控的邻居: 挂在所属设备下一层
  (data.externals || []).forEach(function (e) {
    var p = byIdm[e.device];
    if (!p) { return; }
    var n = { id: 'ext:' + e.id, ext: true, extData: e, name: e.name, layer: Math.min(p.layer + 1, 4), parent: p.id, deg: 1, bw: 0 };
    nodes.push(n); byIdm[n.id] = n;
    links.push({ l: null, a: p, b: n, ext: true, port: e.port });
  });

  // 径向树: 每个节点分到的角度范围 ∝ 子树叶子数
  var children = {};
  nodes.forEach(function (n) { children[n.id] = []; });
  nodes.forEach(function (n) { if (n.parent && children[n.parent]) { children[n.parent].push(n); } });
  Object.keys(children).forEach(function (k) {
    children[k].sort(function (x, y) { return (x.ext - y.ext) || naturalCmp(x.name, y.name); });
  });
  var leaves = function (n) {
    if (n._leaves) { return n._leaves; }
    var c = children[n.id];
    n._leaves = c.length ? c.reduce(function (s, x) { return s + leaves(x); }, 0) : 1;
    return n._leaves;
  };
  var treeRoots = nodes.filter(function (n) { return !n.parent && !n.isolated; });
  var isoNodes = nodes.filter(function (n) { return n.isolated; });
  var total = treeRoots.reduce(function (s, r) { return s + leaves(r); }, 0) + isoNodes.length;
  var place = function (n, a0, a1) {
    n.angle = (a0 + a1) / 2;
    var c = children[n.id], acc = a0, sum = leaves(n);
    c.forEach(function (ch) {
      var span = (a1 - a0) * leaves(ch) / sum;
      place(ch, acc, acc + span);
      acc += span;
    });
  };
  var acc = 0;
  treeRoots.forEach(function (r) {
    var span = Math.PI * 2 * leaves(r) / Math.max(total, 1);
    place(r, acc, acc + span);
    acc += span;
  });
  isoNodes.forEach(function (n) { var span = Math.PI * 2 / Math.max(total, 1); n.angle = acc + span / 2; acc += span; });

  var maxLayer = 0;
  nodes.forEach(function (n) { if (n.layer > maxLayer) { maxLayer = n.layer; } });
  var gap = 250;
  var count = {};
  nodes.forEach(function (n) { count[n.layer] = (count[n.layer] || 0) + 1; });
  var radiusOf = function (L) {
    if (L === 0 && (count[0] || 0) <= 1) { return 0; }
    // 层越往下半径越大, 节点多时再撑开, 保证同层相邻节点不挤在一起
    return Math.max(150 + L * 170, (count[L] || 1) * 38 / (Math.PI * 2) * 2.2);
  };
  nodes.forEach(function (n) {
    var r = radiusOf(n.layer);
    if (n.ext) {
      // 邻居围着父设备的正下方排一小圈, 不拉到大圆上去
      var p = byIdm[n.parent], sib = children[p.id].filter(function (x) { return x.ext; });
      var i = sib.indexOf(n), k = sib.length;
      var rr = 40 + Math.min(k, 30) * 3;
      var a = (i / Math.max(k, 1)) * Math.PI * 2;
      n.x = p.x + Math.cos(a) * rr; n.z = p.z + Math.sin(a) * rr;
    } else {
      n.x = Math.cos(n.angle) * r; n.z = Math.sin(n.angle) * r;
    }
    n.y = (maxLayer / 2 - n.layer) * gap;
    n.size = n.ext ? 6 : [24, 18, 14, 12][Math.min(n.layer, 3)];
  });
  // v12: 同层相邻设备之间至少隔开 T3_MIN_ARC 的弧长, 放不下就把这一圈撑大。以前按子树叶子数分角度,
  // 两台设备角度挨得很近时(比如核心下面同时挂着汇聚和防火墙)模型和名字牌会叠在一起。
  for (var LL = 0; LL <= maxLayer; LL++) {
    var ring = nodes.filter(function (n) { return !n.ext && n.layer === LL; });
    if (ring.length < 2) { continue; }
    var r0 = Math.sqrt(ring[0].x * ring[0].x + ring[0].z * ring[0].z);
    if (r0 < 1) { continue; }
    var R = Math.max(r0, ring.length * T3_MIN_ARC / (Math.PI * 2) * 1.08), minA = T3_MIN_ARC / R;
    var tau = Math.PI * 2;
    ring.forEach(function (n) { n.angle = ((n.angle % tau) + tau) % tau; });
    ring.sort(function (a, b) { return a.angle - b.angle; });
    for (var it = 0; it < 80; it++) {
      var moved = false;
      for (var i = 0; i < ring.length; i++) {
        var a = ring[i], b = ring[(i + 1) % ring.length], d = b.angle - a.angle + (i === ring.length - 1 ? tau : 0);
        if (d < minA - 1e-6) { var push = (minA - d) / 2; a.angle -= push; b.angle += push; moved = true; }
      }
      if (!moved) { break; }
    }
    ring.forEach(function (n) { n.x = Math.cos(n.angle) * R; n.z = Math.sin(n.angle) * R; });
  }
  // 外部邻居依赖父节点坐标, 上面按顺序处理时父节点一定先于子节点? 不一定, 再算一遍
  nodes.forEach(function (n) {
    if (!n.ext) { return; }
    var p = byIdm[n.parent], sib = children[p.id].filter(function (x) { return x.ext; });
    var i = sib.indexOf(n), k = sib.length, rr = 40 + Math.min(k, 30) * 3, a = (i / Math.max(k, 1)) * Math.PI * 2;
    n.x = p.x + Math.cos(a) * rr; n.z = p.z + Math.sin(a) * rr;
  });

  var info = [];
  for (var L = 0; L <= maxLayer; L++) {
    if (!count[L]) { continue; }
    var devs = nodes.filter(function (n) { return n.layer === L && !n.ext; }).length;
    var nm = devs ? T3_LAYER_NAMES[Math.min(L, 3)] + ' · ' + devs + ' 台' : T3_LAYER_NAMES[4];
    if (devs && devs < count[L]) { nm += ' + ' + (count[L] - devs) + ' 个未监控邻居'; }
    if (!devs) { nm += ' · ' + count[L]; }
    info.push({ L: L, y: (maxLayer / 2 - L) * gap, r: 160, name: nm });
  }
  var lay = { nodes: nodes, links: links, byId: byIdm, layers: info, extent: 0, maxLayer: maxLayer };
  t3LayerMeta(lay, 1);
  return lay;
}
var T3_MIN_ARC = 200;
var T3_LAYER_NAMES = ['核心层', '汇聚层', '接入层', '边缘层', '未监控邻居'];
// 按节点实际位置重算每层圆盘半径、层名牌的位置(放在这一层最大的空当里, 不压着设备)和整体范围;
// 手工拖动过位置以后也要重算。k = 坐标缩放倍数(3D WebGL 版整体放大过)
function t3LayerMeta(lay, k) {
  var ext = 0, angs = [];
  lay.layers.forEach(function (L) {
    var rMax = 0;
    lay.nodes.forEach(function (n) {
      if (n.layer !== L.L) { return; }
      var d = Math.sqrt(n.x * n.x + n.z * n.z);
      rMax = Math.max(rMax, d + (n.size || 12) * 3 * k);
    });
    L.r = Math.max(rMax + 60 * k, 160 * k);
  });
  // 层名牌: 找所有层的设备(从上往下看)之间最大的那个扇形空当, 各层名牌在空当里错开摆, 不压设备也不互相压
  lay.nodes.forEach(function (n) { if (!n.ext && (n.x || n.z)) { angs.push(Math.atan2(n.z, n.x)); } });
  var g0 = Math.PI * 0.75 - 0.6, gw = 1.2;
  if (angs.length) {
    angs.sort(function (a, b) { return a - b; });
    gw = -1;
    angs.forEach(function (a, i) {
      var b = i + 1 < angs.length ? angs[i + 1] : angs[0] + Math.PI * 2;
      if (b - a > gw) { gw = b - a; g0 = a; }
    });
  }
  lay.layers.forEach(function (L, i) { L.la = g0 + gw * (i + 1) / (lay.layers.length + 1); });
  lay.nodes.forEach(function (n) { ext = Math.max(ext, Math.sqrt(n.x * n.x + n.z * n.z)); });
  lay.extent = ext;
}

/* ---------- 挂载 / 刷新 ---------- */
function t3Mount(data) {
  var lay = t3Layout(data);
  var sameSet = T3.nodes.length && lay.nodes.length === T3.nodes.length && lay.nodes.every(function (n) { return T3.byId[n.id]; });
  T3.data = data; T3.nodes = lay.nodes; T3.links = lay.links; T3.byId = lay.byId; T3.layerInfo = lay.layers; T3.extent = lay.extent;
  var wrap = byId('topoWrap');
  if (!byId('topo3dCanvas')) {
    wrap.innerHTML = '<div id="topo3dWrap"><canvas id="topo3dCanvas"></canvas><div class="tip" id="topoTip"></div>' +
      '<div class="topo3d-stats" id="topo3dStats"></div>' +
      '<div class="topo3d-legend"><div><i style="background:#38bdf8;"></i>核心 <i style="background:#a78bfa;margin-left:8px;"></i>汇聚 <i style="background:#34d399;margin-left:8px;"></i>接入 ' +
      '<i style="background:transparent;border:1px solid #94a3b8;margin-left:8px;"></i>未监控邻居</div>' +
      '<div><i style="background:#ef4444;box-shadow:0 0 6px #ef4444;"></i>有告警 <i style="background:#64748b;margin-left:8px;"></i>离线</div>' +
      '<div>链路利用率 <b class="bar" style="background:#22c55e;"></b>&lt;50% <b class="bar" style="background:#eab308;"></b>50-80% <b class="bar" style="background:#ef4444;"></b>&gt;80%</div>' +
      '<div>粒子 = 流量方向, 越密越快流量越大; 粗线 = 高速口, 成束 = 聚合</div></div></div>';
    t3Bind(byId('topo3dCanvas'));
    sameSet = false;
  }
  if (!sameSet) { t3ResetCamera(true); }
  t3UpdateStats();
  t3Start();
  if (!T3.refreshTimer) {
    T3.refreshTimer = setInterval(function () {
      if (!T3.on || byId('topoModal').style.display === 'none') { return; }
      pub('/api/topology').then(function (d) { topoData = d; t3Mount(d); }).catch(function () {});
    }, 30000);
  }
}
function t3UpdateStats() {
  var dev = T3.nodes.filter(function (n) { return !n.ext; });
  var off = dev.filter(function (n) { return !n.dev.alive; }).length;
  var al = dev.filter(function (n) { return n.dev.open_alerts > 0; }).length;
  var tot = 0;
  T3.links.forEach(function (x) { if (x.l) { tot += (x.l.a_in_mbps || 0) + (x.l.a_out_mbps || 0); } });
  byId('topo3dStats').innerHTML = '设备 ' + dev.length + ' · 链路 ' + T3.links.filter(function (x) { return !x.ext; }).length +
    ' · 离线 <b style="color:' + (off ? '#f87171' : '#4ade80') + ';">' + off + '</b> · 告警设备 <b style="color:' + (al ? '#f87171' : '#4ade80') + ';">' + al + '</b>' +
    ' · 设备间流量 ' + (tot >= 1000 ? (tot / 1000).toFixed(2) + ' Gbps' : tot.toFixed(1) + ' Mbps');
}
function t3ResetCamera(instant) {
  var span = Math.max(T3.extent || 300, (T3.layerInfo.length - 1) * 130);
  var target = { yaw: 0.6, pitch: 0.38, dist: Math.max(700, span * 2.1 + 350), tx: 0, ty: 0, tz: 0 };
  if (instant) { for (var k in target) { T3[k] = target[k]; } } else { T3.fly = { from: t3Cam(), to: target, t: 0 }; }
  t3Poke();
}
function t3Cam() { return { yaw: T3.yaw, pitch: T3.pitch, dist: T3.dist, tx: T3.tx, ty: T3.ty, tz: T3.tz }; }
function t3FlyTo(n) {
  T3.fly = { from: t3Cam(), to: { yaw: T3.yaw, pitch: Math.max(T3.pitch, 0.35), dist: Math.max(420, Math.min(T3.dist, 900)), tx: n.x, ty: n.y, tz: n.z }, t: 0 };
  T3.idleUntil = performance.now() + 8000;
  t3Poke();
}
function t3Stop() {
  if (T3.raf) { cancelAnimationFrame(T3.raf); T3.raf = 0; }
  if (T3.refreshTimer) { clearInterval(T3.refreshTimer); T3.refreshTimer = null; }
  hideTopoTip();
}
function t3Start() { if (!T3.raf) { T3.last = performance.now(); T3.raf = requestAnimationFrame(t3Frame); } }
// 动画关掉时不持续刷新, 只在交互时重绘一帧
function t3Poke() { if (T3.on && byId('topo3dCanvas')) { t3Start(); } }

/* ---------- 交互 ---------- */
function t3Bind(cv) {
  cv.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  cv.addEventListener('mousedown', function (e) {
    T3.drag = { x: e.clientX, y: e.clientY, pan: e.button === 2 || e.shiftKey, moved: false };
    cv.classList.add('dragging');
    T3.idleUntil = performance.now() + 6000;
  });
  window.addEventListener('mousemove', function (e) {
    if (!T3.on) { return; }
    if (T3.drag) {
      var dx = e.clientX - T3.drag.x, dy = e.clientY - T3.drag.y;
      if (Math.abs(dx) + Math.abs(dy) > 3) { T3.drag.moved = true; }
      T3.drag.x = e.clientX; T3.drag.y = e.clientY;
      if (T3.drag.pan) {
        var s = T3.dist / 900;
        var cy = Math.cos(T3.yaw), sy = Math.sin(T3.yaw);
        T3.tx -= (dx * cy) * s; T3.tz -= (-dx * sy) * s;
        T3.ty += dy * s;
      } else {
        T3.yaw += dx * 0.006;
        T3.pitch = Math.max(0.05, Math.min(1.45, T3.pitch + dy * 0.005));
      }
      T3.fly = null;
      T3.idleUntil = performance.now() + 6000;
      hideTopoTip();
      t3Poke();
      return;
    }
    if (e.target === cv) { t3Pick(e); }
  });
  window.addEventListener('mouseup', function (e) {
    if (!T3.drag) { return; }
    var wasClick = !T3.drag.moved;
    T3.drag = null;
    cv.classList.remove('dragging');
    if (wasClick && e.target === cv && T3.hover && !T3.hover.ext && !T3.hover.man && e.button === 0) {
      openModal(T3.hover.id);
    }
  });
  cv.addEventListener('mouseleave', function () { if (!T3.drag) { T3.hover = null; T3.hoverLink = null; hideTopoTip(); t3Poke(); } });
  cv.addEventListener('dblclick', function (e) { e.preventDefault(); if (T3.hover) { t3FlyTo(T3.hover); } });
  cv.addEventListener('wheel', function (e) {
    e.preventDefault();
    T3.dist = Math.max(250, Math.min(6000, T3.dist * (e.deltaY > 0 ? 1.12 : 0.89)));
    T3.fly = null;
    T3.idleUntil = performance.now() + 4000;
    t3Poke();
  }, { passive: false });
}
function t3Pick(e) {
  var cv = byId('topo3dCanvas'), rect = cv.getBoundingClientRect();
  var mx = e.clientX - rect.left, my = e.clientY - rect.top;
  var best = null, bd = 1e9;
  // proj 是按远到近排的, 倒着找, 优先命中前面的球
  for (var i = T3.proj.length - 1; i >= 0; i--) {
    var p = T3.proj[i], d = Math.hypot(p.sx - mx, p.sy - my);
    if (d <= Math.max(p.sr, 6) + 3 && d < bd) { best = p.n; bd = d; break; }
  }
  var link = null;
  if (!best) {
    var ld = 7;
    T3.projLinks.forEach(function (pl) {
      var d = t3SegDist(mx, my, pl.x1, pl.y1, pl.x2, pl.y2);
      if (d < ld) { ld = d; link = pl.k; }
    });
  }
  var changed = best !== T3.hover || link !== T3.hoverLink;
  T3.hover = best; T3.hoverLink = link;
  if (best) {
    showTopoTip(best.ext ? externalTipHTML(best.extData) : best.man ? tpManualTip(best.mdata) : deviceTipHTML(best.dev), e);
  } else if (link) {
    if (link.ext) {
      showTopoTip('<b>' + esc(link.b.name) + '</b><br><span style="color:#94a3b8;">未被监控的 LLDP 邻居</span><br>' + esc(link.a.name) + ' 端口: ' + esc(link.port || '-'), e);
    } else {
      var devByIP = {};
      (T3.data.devices || []).forEach(function (d) { devByIP[d.ip] = d; });
      showTopoTip(linkTipHTML(link.l, devByIP), e);
    }
  } else {
    hideTopoTip();
  }
  cv.style.cursor = best && !best.ext ? 'pointer' : '';
  if (changed) { t3Poke(); }
}
function t3SegDist(px, py, x1, y1, x2, y2) {
  var dx = x2 - x1, dy = y2 - y1, l2 = dx * dx + dy * dy;
  var t = l2 ? Math.max(0, Math.min(1, ((px - x1) * dx + (py - y1) * dy) / l2)) : 0;
  return Math.hypot(px - (x1 + t * dx), py - (y1 + t * dy));
}
function t3Match(n) {
  if (!T3.search) { return false; }
  var s = (n.name + ' ' + (n.dev ? n.dev.ip : '')).toLowerCase();
  return s.indexOf(T3.search) >= 0;
}

/* ---------- 渲染 ---------- */
function t3ThemeColors() {
  var cs = getComputedStyle(document.body);
  var v = function (k, d) { var x = cs.getPropertyValue(k); return x && x.trim() ? x.trim() : d; };
  var bg = v('--bg', '#0f172a');
  // 根据背景亮度决定用深色舞台还是浅色舞台
  var dark = true;
  var m = bg.match(/^#([0-9a-f]{6})$/i);
  if (m) {
    var n = parseInt(m[1], 16), lum = (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
    dark = lum < 0.5;
  }
  return dark
    ? { stageA: '#0b1224', stageB: '#030712', plane: 'rgba(56,189,248,', text: '#dbe4f3', textDim: 'rgba(219,228,243,.55)', accent: v('--accent', '#38bdf8'), dark: true }
    : { stageA: '#f8fafc', stageB: '#dbe3ee', plane: 'rgba(37,99,235,', text: '#1e293b', textDim: 'rgba(30,41,59,.55)', accent: v('--accent', '#2563eb'), dark: false };
}
var T3_LAYER_COLORS = ['#38bdf8', '#a78bfa', '#34d399', '#2dd4bf'];

function t3Frame(now) {
  T3.raf = 0;
  if (!T3.on || byId('topoModal').style.display === 'none' || !byId('topo3dCanvas')) { return; }
  var dt = Math.min(0.1, (now - T3.last) / 1000);
  T3.last = now;
  var animating = false;
  if (T3.fly) {
    T3.fly.t = Math.min(1, T3.fly.t + dt * 1.6);
    var e = 1 - Math.pow(1 - T3.fly.t, 3);
    for (var k in T3.fly.to) { T3[k] = T3.fly.from[k] + (T3.fly.to[k] - T3.fly.from[k]) * e; }
    if (T3.fly.t >= 1) { T3.fly = null; }
    animating = true;
  }
  if (T3.auto && !T3.drag && now > T3.idleUntil && !T3.hover && !T3.hoverLink) { T3.yaw += dt * 0.12; animating = true; }
  if (T3.particles) { T3.time += dt; animating = true; }
  var hasAlert = T3.nodes.some(function (n) { return n.dev && n.dev.open_alerts > 0; });
  if (hasAlert && (T3.auto || T3.particles)) { animating = true; }
  if (!document.hidden) { t3Draw(now); }
  if (animating || T3.drag) { T3.raf = requestAnimationFrame(t3Frame); }
}

function t3Draw(now) {
  var cv = byId('topo3dCanvas');
  var dpr = window.devicePixelRatio || 1;
  var W = cv.clientWidth, H = cv.clientHeight;
  if (cv.width !== Math.round(W * dpr) || cv.height !== Math.round(H * dpr)) { cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr); }
  var ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  var C = T3.colors = t3ThemeColors();
  var g = ctx.createRadialGradient(W / 2, H * 0.35, 10, W / 2, H / 2, Math.max(W, H) * 0.8);
  g.addColorStop(0, C.stageA); g.addColorStop(1, C.stageB);
  ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);

  var cyw = Math.cos(T3.yaw), syw = Math.sin(T3.yaw), cp = Math.cos(T3.pitch), sp = Math.sin(T3.pitch);
  var f = Math.min(W, H) * 1.25, cx = W / 2, cy = H / 2;
  // 世界坐标 -> 屏幕: 平移到目标点, 绕 Y 轴转 yaw, 绕 X 轴转 pitch, 透视除法
  var P = function (x, y, z) {
    x -= T3.tx; y -= T3.ty; z -= T3.tz;
    var x1 = x * cyw - z * syw, z1 = x * syw + z * cyw;
    var y2 = y * cp - z1 * sp, z2 = y * sp + z1 * cp + T3.dist;
    if (z2 < 30) { z2 = 30; }
    var s = f / z2;
    return { x: cx + x1 * s, y: cy - y2 * s, s: s, z: z2 };
  };

  // 层平面: 由远到近画半透明圆盘 + 网格圈 + 层名
  T3.layerInfo.slice().sort(function (a, b) { return P(0, b.y, 0).z - P(0, a.y, 0).z; }).forEach(function (L) {
    var pts = [];
    for (var i = 0; i <= 72; i++) {
      var a = i / 72 * Math.PI * 2;
      pts.push(P(Math.cos(a) * L.r, L.y, Math.sin(a) * L.r));
    }
    ctx.beginPath();
    pts.forEach(function (p, i) { if (i) { ctx.lineTo(p.x, p.y); } else { ctx.moveTo(p.x, p.y); } });
    ctx.fillStyle = C.plane + (C.dark ? '0.05)' : '0.06)'); ctx.fill();
    ctx.strokeStyle = C.plane + '0.35)'; ctx.lineWidth = 1.2; ctx.stroke();
    ctx.strokeStyle = C.plane + '0.10)'; ctx.lineWidth = 1;
    for (var ring = 1; ring <= 3; ring++) {
      ctx.beginPath();
      for (var j = 0; j <= 48; j++) {
        var b = j / 48 * Math.PI * 2, q = P(Math.cos(b) * L.r * ring / 4, L.y, Math.sin(b) * L.r * ring / 4);
        if (j) { ctx.lineTo(q.x, q.y); } else { ctx.moveTo(q.x, q.y); }
      }
      ctx.stroke();
    }
    // 层名标在圆盘最靠左的点
    var lp = pts.reduce(function (m, p) { return p.x < m.x ? p : m; }, pts[0]);
    ctx.fillStyle = C.textDim; ctx.font = '600 12px ' + getComputedStyle(document.body).fontFamily;
    // 标在圆盘外侧, 免得压住层上的设备
    ctx.textAlign = 'right';
    ctx.fillText(L.name, lp.x - 8, lp.y + 4);
    ctx.textAlign = 'left';
  });

  var hov = T3.hover, hovLink = T3.hoverLink, searching = !!T3.search;
  var related = {};
  if (hov) {
    related[hov.id] = true;
    T3.links.forEach(function (k) { if (k.a === hov || k.b === hov) { related[k.a.id] = true; related[k.b.id] = true; } });
  }
  var focusDim = function (n) {
    if (searching) { return t3Match(n) ? 1 : 0.25; }
    if (hov) { return related[n.id] ? 1 : 0.22; }
    if (hovLink) { return (n === hovLink.a || n === hovLink.b) ? 1 : 0.3; }
    return 1;
  };

  // 投影所有节点
  var proj = T3.nodes.map(function (n) {
    var p = P(n.x, n.y + n.size, n.z), base = P(n.x, n.y, n.z);
    return { n: n, sx: p.x, sy: p.y, sr: Math.max(2, n.size * p.s), z: p.z, bx: base.x, by: base.y, bs: base.s };
  });
  var pm = {};
  proj.forEach(function (p) { pm[p.n.id] = p; });

  // 链路(远的先画)
  var projLinks = [];
  var linkList = T3.links.map(function (k) {
    var a = pm[k.a.id], b = pm[k.b.id];
    return { k: k, a: a, b: b, z: (a.z + b.z) / 2 };
  }).sort(function (x, y) { return y.z - x.z; });
  linkList.forEach(function (it) {
    var k = it.k, a = it.a, b = it.b;
    var alpha = Math.min(focusDim(k.a), focusDim(k.b));
    if (hovLink === k || (hov && (k.a === hov || k.b === hov))) { alpha = 1; }
    ctx.save();
    ctx.globalAlpha = alpha;
    projLinks.push({ k: k, x1: a.sx, y1: a.sy, x2: b.sx, y2: b.sy });
    if (k.ext) {
      ctx.setLineDash([3, 4]);
      ctx.strokeStyle = C.dark ? 'rgba(148,163,184,.45)' : 'rgba(71,85,105,.45)';
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(a.sx, a.sy); ctx.lineTo(b.sx, b.sy); ctx.stroke();
      ctx.restore();
      return;
    }
    var l = k.l;
    var speed = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0);
    var util = speed ? Math.max(l.a_in_mbps || 0, l.a_out_mbps || 0, l.b_in_mbps || 0, l.b_out_mbps || 0) / speed : 0;
    var col = t3UtilColor(util);
    var offline = !(k.a.dev.alive && k.b.dev.alive);
    var zs = Math.max(0.35, Math.min(2.2, ((a.sr / k.a.size) + (b.sr / k.b.size)) / 2 * 1.2));
    var w = (1 + Math.log(1 + speed / 1000) * 1.6) * zs;
    var members = l.is_agg ? Math.min((l.a_members || []).length || 2, 8) : 1;
    // 聚合链路: 屏幕上垂直于连线方向平移出若干条细线, 看起来是一束
    var dx = b.sx - a.sx, dy = b.sy - a.sy, len = Math.hypot(dx, dy) || 1, nx = -dy / len, ny = dx / len;
    var spacing = Math.max(1.6, w * 0.9);
    if (hovLink === k) {
      ctx.strokeStyle = col; ctx.globalAlpha = 0.25; ctx.lineWidth = w * members + 10;
      ctx.beginPath(); ctx.moveTo(a.sx, a.sy); ctx.lineTo(b.sx, b.sy); ctx.stroke();
      ctx.globalAlpha = alpha;
    }
    ctx.strokeStyle = offline ? '#64748b' : col;
    if (offline) { ctx.setLineDash([6, 5]); }
    ctx.lineWidth = members > 1 ? Math.max(1, w * 0.6) : w;
    for (var m = 0; m < members; m++) {
      var off = (m - (members - 1) / 2) * spacing;
      ctx.beginPath();
      ctx.moveTo(a.sx + nx * off, a.sy + ny * off);
      ctx.lineTo(b.sx + nx * off, b.sy + ny * off);
      ctx.stroke();
    }
    ctx.setLineDash([]);
    // 流动粒子: A->B 用 A 端的发送速率, B->A 用 B 端的发送速率
    if (T3.particles && !offline) {
      t3Particles(ctx, a, b, l.a_out_mbps || 0, speed, '#7dd3fc', zs, nx, ny, 1.5);
      t3Particles(ctx, b, a, l.b_out_mbps || 0, speed, '#f0abfc', zs, nx, ny, -1.5);
    }
    ctx.restore();
  });
  T3.projLinks = projLinks;

  // 节点(远的先画)
  proj.sort(function (x, y) { return y.z - x.z; });
  var labelFont = getComputedStyle(document.body).fontFamily;
  var pulse = 0.5 + 0.5 * Math.sin(now / 320);
  proj.forEach(function (p) {
    var n = p.n, alpha = focusDim(n);
    ctx.save();
    ctx.globalAlpha = alpha;
    // 地面投影阴影, 增强立体感
    ctx.fillStyle = C.dark ? 'rgba(0,0,0,.35)' : 'rgba(15,23,42,.12)';
    ctx.beginPath(); ctx.ellipse(p.bx, p.by, p.sr * 1.1, p.sr * 0.35 * Math.max(0.2, Math.sin(T3.pitch)), 0, 0, Math.PI * 2); ctx.fill();
    if (n.ext) {
      ctx.strokeStyle = C.dark ? 'rgba(148,163,184,.8)' : 'rgba(71,85,105,.8)';
      ctx.lineWidth = 1.2;
      ctx.beginPath(); ctx.arc(p.sx, p.sy, p.sr, 0, Math.PI * 2); ctx.stroke();
    } else {
      var d = n.dev, base = T3_LAYER_COLORS[Math.min(n.layer, 3)];
      if (!d.alive) { base = '#64748b'; } else if (d.open_alerts > 0) { base = '#ef4444'; } else if (d.cpu_usage >= 80) { base = '#f59e0b'; }
      if (d.alive && d.open_alerts > 0) {
        var hr = p.sr * (1.6 + pulse * 0.9);
        var hg = ctx.createRadialGradient(p.sx, p.sy, p.sr * 0.8, p.sx, p.sy, hr);
        hg.addColorStop(0, 'rgba(239,68,68,' + (0.45 * (1 - pulse * 0.5)) + ')'); hg.addColorStop(1, 'rgba(239,68,68,0)');
        ctx.fillStyle = hg; ctx.beginPath(); ctx.arc(p.sx, p.sy, hr, 0, Math.PI * 2); ctx.fill();
      }
      var sg = ctx.createRadialGradient(p.sx - p.sr * 0.35, p.sy - p.sr * 0.4, p.sr * 0.1, p.sx, p.sy, p.sr);
      sg.addColorStop(0, '#ffffff'); sg.addColorStop(0.25, base); sg.addColorStop(1, t3Shade(base, 0.45));
      ctx.fillStyle = sg;
      ctx.beginPath(); ctx.arc(p.sx, p.sy, p.sr, 0, Math.PI * 2); ctx.fill();
      if (hov === n || t3Match(n)) {
        ctx.strokeStyle = C.accent; ctx.lineWidth = 2.5;
        ctx.beginPath(); ctx.arc(p.sx, p.sy, p.sr + 4, 0, Math.PI * 2); ctx.stroke();
      }
    }
    // 标签: 未监控邻居围成小圈, 名字会叠在一起, 只在悬停它或它的父设备、或者拉近看时显示
    var showLabel = !n.ext || hov === n || (hov && hov.id === n.parent) || p.sr >= 9 || t3Match(n);
    if (showLabel) {
      var fs = Math.max(10, Math.min(14, 9 + p.sr * 0.25));
      ctx.font = (n.ext ? '' : '600 ') + fs + 'px ' + labelFont;
      ctx.textAlign = 'center';
      var label = truncateLabel(n.name, n.ext ? 14 : 20);
      ctx.lineWidth = 3; ctx.strokeStyle = C.dark ? 'rgba(3,7,18,.8)' : 'rgba(248,250,252,.9)';
      ctx.strokeText(label, p.sx, p.sy + p.sr + fs + 2);
      ctx.fillStyle = n.ext ? C.textDim : C.text;
      ctx.fillText(label, p.sx, p.sy + p.sr + fs + 2);
    }
    ctx.restore();
  });
  // 拾取用: 按近到远保存
  T3.proj = proj;
}
function t3Particles(ctx, a, b, mbps, speed, color, zs, nx, ny, side) {
  if (mbps <= 0.01) { return; }
  var util = speed ? Math.min(1, mbps / speed) : 0.1;
  var count = Math.max(1, Math.min(14, Math.round(Math.log(1 + mbps) * 1.8)));
  var v = 0.12 + util * 0.9;
  var r = Math.max(1.2, 2.2 * zs);
  ctx.fillStyle = color;
  ctx.shadowColor = color; ctx.shadowBlur = 6;
  for (var i = 0; i < count; i++) {
    var t = (T3.time * v + i / count) % 1;
    var x = a.sx + (b.sx - a.sx) * t + nx * side * r, y = a.sy + (b.sy - a.sy) * t + ny * side * r;
    ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.fill();
  }
  ctx.shadowBlur = 0;
}
function t3UtilColor(u) {
  // 0 -> 绿, 0.5 -> 黄, 0.8+ -> 红
  var lerp = function (c1, c2, t) {
    return 'rgb(' + [0, 1, 2].map(function (i) { return Math.round(c1[i] + (c2[i] - c1[i]) * t); }).join(',') + ')';
  };
  var G = [34, 197, 94], Y = [234, 179, 8], R = [239, 68, 68];
  if (u <= 0.5) { return lerp(G, Y, u / 0.5); }
  return lerp(Y, R, Math.min(1, (u - 0.5) / 0.3));
}
function t3Shade(hex, k) {
  var n = parseInt(hex.slice(1), 16);
  var r = Math.round((n >> 16) * k), g = Math.round(((n >> 8) & 255) * k), b = Math.round((n & 255) * k);
  return 'rgb(' + r + ',' + g + ',' + b + ')';
}
window.addEventListener('resize', function () { t3Poke(); });
t3SyncButtons();
