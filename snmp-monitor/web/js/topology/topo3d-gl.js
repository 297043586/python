/*
 * 3D 拓扑的 WebGL 版本(three.js)。布局沿用 topo3d.go 的 t3Layout(核心/汇聚/接入分层 +
 * 径向树), 画法换成真正的三维场景:
 *
 *   - 设备画成交换机实物: 盒式 1U/2U 机箱、N 台堆叠就叠 N 台(背面有发光的堆叠线缆)、
 *     框式画成高机框; 形态来自后端 form.go 的识别结果, 不对可以在机柜/机房里手工指定。
 *     前面板按真实端口状态画灯(数据来自 /api/panels)。
 *   - 链路是三维管道: 粗细 = 速率, 颜色 = 利用率, 聚合链路带金色外皮;
 *     两个方向各有一串流动光点, 数量/速度随实时流量变化。
 *   - 每层一张半透明圆盘 + 层名; 有告警的设备脚下红色脉冲光环, 离线设备灰色。
 *   - 交互跟 Canvas 版一样: 拖动旋转、右键平移、滚轮缩放、悬停详情(只高亮相关链路)、
 *     单击打开设备、双击飞过去、搜索定位、自动旋转/粒子开关。
 *
 * 做成了实例(tgNew), 拓扑窗口和大屏各用一个。浏览器不支持 WebGL 或在工具栏里选了
 * 「兼容模式」时, 拓扑窗口自动用 topo3d-canvas.js 的 Canvas 版(由 topology/view.js 调度)。
 */
/* ================= 3D 拓扑 (WebGL / three.js) ================= */
var TG = null; // 拓扑窗口里的实例
var TGE = { engine: 'webgl', timer: null };
(function () { try { TGE.engine = localStorage.getItem('topo3dEngine') || 'webgl'; } catch (e) {} })();
function tgWanted() { return TGE.engine !== 'canvas' && glSupported(); }
function tgActive() { return T3.on && TG && TG.st; }

// 3D (WebGL)的搜索定位; 入口调度见 topology/view.js
function tgHighlight() {
  T3.search = (byId('topoSearch').value || '').trim().toLowerCase();
  TG.search = T3.search;
  var hit = null;
  TG.nodes.forEach(function (n) { if (!hit && TG.search && tgMatch(TG, n)) { hit = n; } });
  tgApplyHighlight(TG);
  if (hit) { tgFlyTo(TG, hit); }
}

function tgSyncEngineCtl() {
  var ctl = byId('topo3dCtl');
  if (!ctl) { return; }
  if (!byId('t3Engine')) {
    var sp = document.createElement('label');
    sp.title = 'WebGL: three.js 真 3D(需要显卡支持); 兼容模式: 老电脑/虚拟机用';
    sp.innerHTML = '渲染 <select id="t3Engine" onchange="tgSetEngine(this.value)" style="padding:3px 6px;border-radius:6px;border:1px solid var(--card-border);background:var(--card-bg);color:var(--fg);">' +
      '<option value="webgl">WebGL (three.js)</option><option value="canvas">兼容模式</option></select>';
    ctl.appendChild(sp);
  }
  if (!byId('t3EditBtn')) {
    var eb = document.createElement('span');
    eb.innerHTML = '<button class="filter-btn" id="t3EditBtn" onclick="tgToggleEdit()" title="按住设备拖到想要的位置, 松手自动保存">✋ 拖动摆放</button> <button class="filter-btn" onclick="tgAutoArrange()" title="清除手工拖过的位置, 全部自动排列">⟲ 自动排列</button>';
    ctl.appendChild(eb);
  }
  var eon = !!(TG && TG.editOn), ebtn = byId('t3EditBtn');
  ebtn.innerText = eon ? '✅ 完成摆放' : '✋ 拖动摆放'; ebtn.classList.toggle('active', eon); ebtn.style.display = tgWanted() ? '' : 'none';
  byId('t3Engine').value = tgWanted() ? 'webgl' : 'canvas';
  byId('t3Engine').disabled = !glSupported();
}
function tgSetEngine(v) {
  TGE.engine = v;
  try { localStorage.setItem('topo3dEngine', v); } catch (e) {}
  topoRemount();
}
function tgTopoDestroy() {
  if (TGE.timer) { clearInterval(TGE.timer); TGE.timer = null; }
  if (TG) { tgTourStop(TG); }
  if (TG) { tgDispose(TG); TG = null; }
  hideTopoTip();
}
function tgTopoMount(data) {
  var wrap = byId('topoWrap');
  if (TG && TG.st && byId('tgWrap')) { tgSetData(TG, data, false); return; }
  tgTopoDestroy();
  wrap.innerHTML = '<div id="topo3dWrap"><div id="tgWrap" style="width:100%;height:66vh;border-radius:8px;border:1px solid var(--card-border);overflow:hidden;position:relative;cursor:grab;"></div>' +
    '<div class="tip" id="topoTip"></div><div class="topo3d-stats" id="topo3dStats"></div>' +
    '<div class="topo3d-legend"><div><i style="background:#38bdf8;"></i>核心 <i style="background:#a78bfa;margin-left:8px;"></i>汇聚 <i style="background:#34d399;margin-left:8px;"></i>接入 ' +
    '<i style="background:transparent;border:1px solid #94a3b8;margin-left:8px;"></i>未监控邻居</div>' +
    '<div>▭ 盒式 · <b style="color:#22d3ee;">▤</b> N 台堆叠(背面青色线 = 堆叠线缆) · ▯ 框式(高机框)</div>' +
    '<div><i style="background:#ef4444;box-shadow:0 0 6px #ef4444;"></i>有告警(脚下红色光环) <i style="background:#64748b;margin-left:8px;"></i>离线</div>' +
    '<div>链路利用率 <b class="bar" style="background:#22c55e;"></b>&lt;50% <b class="bar" style="background:#eab308;"></b>50-80% <b class="bar" style="background:#ef4444;"></b>&gt;80% · 金边 = 聚合</div>' +
    '<div>面板灯: 绿 UP · 黄 高负载 · 红 错包/半双工 · 橙 收光弱/管理DOWN</div></div>' +
    '<div id="tgLoading" style="position:absolute;inset:0;display:grid;place-items:center;color:var(--fg-dim);font-size:14px;">正在加载 3D 引擎...</div></div>';
  glLoad().then(function () {
    if (!T3.on || !byId('tgWrap')) { return; }
    TG = tgNew(byId('tgWrap'), {
      auto: T3.auto, particles: T3.particles,
      showTip: showTopoTip, hideTip: hideTopoTip,
      onOpen: function (ip, tab) { openModal(ip, tab); },
      card: true, cardHost: byId('topo3dWrap'),
      onMoved: tgSave3D,
      onBuilt: function () { t3UpdateStats(); }
    });
    var ld = byId('tgLoading'); if (ld) { ld.remove(); }
    tgMountBar(TG, byId('topo3dWrap'));
    tgSetData(TG, data, true);
    TGE.timer = setInterval(function () {
      if (!tgActive() || byId('topoModal').style.display === 'none') { return; }
      pub('/api/topology').then(function (d) { topoData = d; if (TG) { tgSetData(TG, d, false); } }).catch(function () {});
    }, uiPref('topo_refresh_seconds', 30) * 1000);
  }).catch(function (e) {
    // three.js 加载失败(比如很老的浏览器不支持动态 import) → 退回 Canvas 版
    if (window.console) { console.warn('three.js 加载失败, 改用兼容模式', e); }
    TGE.engine = 'canvas';
    topoRemount();
  });
}

/* ---------- 实例 ---------- */
// opts: { auto, particles, showTip(html, e), hideTip(), onOpen(ip), onBuilt(), bigscreen, transparent, key }
//
// 画面: 深空背景 + 星点, 发光网格地面(中心向外扫描的脉冲环), 每层一圈全息平台(发光外环 +
// 反向旋转的刻度环), 设备脚下发光底座, 有告警的设备头顶红色光柱; 链路 = 实心细管 + 外层
// 流光管(贴图沿管子流动, 流速 ∝ 流量, 方向 = 主要流向), 手工节点 = 旋转的紫色全息多面体。
function tgNew(box, opts) {
  opts = opts || {};
  var c = { box: box, opts: opts, key: opts.key || 'tg:', data: null, panels: {}, nodes: [], links: [], meshes: {}, linkObjs: [], pickables: [], hover: null, hoverLink: null, badges: [], intro: null, sel: null,
    particles: null, pdata: [], rings: [], spins: [], beams: [], flows: [], sig: '', down: null, search: '', auto: !!opts.auto, showParticles: opts.particles !== false };
  var st = c.st = glStage(box, { fov: 42, near: 5, far: 60000, transparent: !!opts.transparent, exposure: 1.15, env: false, bloom: { strength: 0.6, threshold: 0.82 } });
  var THREE = st.THREE;
  c.dark = true;
  if (!opts.transparent) {
    st.scene.background = new THREE.Color(0x020617);
    st.scene.fog = new THREE.FogExp2(0x030a1c, 0.00011);
    c.sky = tgSky(THREE);
    st.scene.add(c.sky);
  }
  st.scene.add(new THREE.HemisphereLight(0xbfdbfe, 0x1e293b, 1.5));
  st.scene.add(new THREE.AmbientLight(0x94a3b8, 0.9)); // 深色背景上交换机机身别黑成一块
  var dl = new THREE.DirectionalLight(0xffffff, 1.15);
  dl.position.set(600, 1200, 900); st.scene.add(dl);
  var dl2 = new THREE.DirectionalLight(0x38bdf8, 0.55);
  dl2.position.set(-800, 300, -600); st.scene.add(dl2);
  c.world = new THREE.Group(); st.scene.add(c.world);
  st.controls.autoRotate = c.auto;
  st.controls.autoRotateSpeed = opts.bigscreen ? 0.35 : 0.5;
  st.controls.maxDistance = 20000; st.controls.minDistance = 150;
  st.controls.maxPolarAngle = Math.PI * 0.49; // 不钻到地面下面
  var idle = null;
  st.controls.addEventListener('start', function () { st.controls.autoRotate = false; if (idle) { clearTimeout(idle); } box.style.cursor = 'grabbing'; });
  st.controls.addEventListener('end', function () { box.style.cursor = 'grab'; idle = setTimeout(function () { if (c.st) { c.st.controls.autoRotate = c.auto; } }, 8000); });
  var cv = st.renderer.domElement;
  cv.addEventListener('pointerdown', function (e) { c.down = { x: e.clientX, y: e.clientY, b: e.button }; });
  cv.addEventListener('pointermove', function (e) { if (!c.down || e.buttons === 0) { tgHover(c, e); } });
  cv.addEventListener('pointerup', function (e) {
    var d = c.down; c.down = null;
    if (!d || Math.abs(e.clientX - d.x) + Math.abs(e.clientY - d.y) > 4 || d.b !== 0) { return; }
    var h = st.pick(e, c.pickables), n = h && h.obj.userData.node;
    if (c.editOn) { return; }
    // v13: 拓扑窗口里单击 = 选中(镜头飞过去 + 信息卡), 双击才打开设备详情; 大屏沿用单击打开
    if (c.opts.card) { tgSelect(c, n && !n.ext ? n : null, true); return; }
    if (n && !n.ext && !n.man && c.opts.onOpen) { c.opts.onOpen(n.id); }
  });
  cv.addEventListener('dblclick', function (e) {
    var h = st.pick(e, c.pickables), n = h && h.obj.userData.node;
    if (!n) { return; }
    if (c.opts.card && !n.ext && !n.man && c.opts.onOpen) { c.opts.onOpen(n.id); return; }
    tgFlyTo(c, n);
  });
  // 手动操作视角时停止巡航
  ['pointerdown', 'wheel'].forEach(function (ev) { cv.addEventListener(ev, function () { if (c.tour) { tgTourStop(c); } }, { passive: true }); });
  c.selBox = glPulseBox(0x38bdf8, { fill: 0.05 }); st.scene.add(c.selBox);
  c.hovBox = glPulseBox(0xe2e8f0, { fill: 0.02, speed: 7 }); st.scene.add(c.hovBox);
  cv.addEventListener('pointerleave', function () { c.hover = null; c.hoverLink = null; tgApplyHighlight(c); if (c.opts.hideTip) { c.opts.hideTip(); } });
  // v12:「✋ 拖动摆放」模式下按住设备拖, 在它所在那一层的水平面上移动。
  // 在外层容器的捕获阶段先拿到事件并截住, OrbitControls 就不会同时开始旋转视角。
  box.addEventListener('pointerdown', function (e) {
    if (!c.editOn || e.button !== 0 || !c.st) { return; }
    var h = st.pick(e, c.pickables), n = h && h.obj.userData.node;
    if (!n || n.ext) { return; }
    e.stopPropagation(); e.preventDefault();
    var hit = tgPlaneHit(c, e, n.y);
    c.drag = { n: n, ox: hit ? hit.x - n.x : 0, oz: hit ? hit.z - n.z : 0, x0: n.x, z0: n.z, moved: false };
    st.controls.enabled = false; st.controls.autoRotate = false;
    if (c.opts.hideTip) { c.opts.hideTip(); }
    try { box.setPointerCapture(e.pointerId); } catch (err) {}
  }, true);
  box.addEventListener('pointermove', function (e) { if (c.drag) { tgDragMove(c, e); } });
  var endDrag = function () { if (c.drag) { tgDragEnd(c); } };
  box.addEventListener('pointerup', endDrag);
  box.addEventListener('pointercancel', endDrag);
  var tmp = new THREE.Vector3();
  st.frames.push(function (dt, t) {
    var anim = c.showParticles; // 关掉"流量粒子"时整个画面静止, 只在操作时重画(省电)
    if (c.ground) { c.ground.material.uniforms.uTime.value = t; }
    if (c.sky) { c.sky.rotation.y = t * 0.004; }
    if (c.particles && c.particles.visible && c.pdata.length) {
      var pos = c.particles.geometry.attributes.position;
      for (var i = 0; i < c.pdata.length; i++) {
        var p = c.pdata[i];
        var s = (t * p.speed + p.off) % 1;
        p.curve.getPointAt(p.dir > 0 ? s : 1 - s, tmp);
        pos.setXYZ(i, tmp.x, tmp.y, tmp.z);
      }
      pos.needsUpdate = true;
    }
    if (anim) {
      c.flows.forEach(function (f) { f.tex.offset.x -= f.speed * dt; });
      c.spins.forEach(function (o) { o.rotation.y += o.userData.spin * dt; });
    }
    c.rings.forEach(function (r) {
      var k = (t * 0.8 + r.userData.phase) % 1;
      r.scale.setScalar(1 + k * 1.6);
      r.material.opacity = 0.7 * (1 - k);
    });
    c.beams.forEach(function (b) { b.material.opacity = 0.25 + 0.2 * Math.sin(t * 4 + b.userData.phase); });
    c.badges.forEach(function (b) { b.position.y = b.userData.y0 + Math.sin(t * 2.4 + b.userData.phase) * 4 / (b.parent ? b.parent.scale.y : 1); });
    var busy = tgIntroStep(c) | tgEaseScales(c, dt);
    var boxes = c.selBox.tick(t) | c.hovBox.tick(t);
    return anim || busy || boxes || c.rings.length > 0 || c.beams.length > 0;
  });
  st.start();
  return c;
}
// 深空背景: 竖直渐变天球 + 星点
function tgSky(THREE) {
  var g = new THREE.Group();
  var sky = new THREE.Mesh(new THREE.SphereGeometry(30000, 32, 16), new THREE.ShaderMaterial({
    side: THREE.BackSide, depthWrite: false, fog: false,
    uniforms: { top: { value: new THREE.Color(0x010409) }, mid: { value: new THREE.Color(0x061633) }, bot: { value: new THREE.Color(0x0b2a4a) } },
    vertexShader: 'varying vec3 vP; void main(){ vP = normalize(position); gl_Position = projectionMatrix * modelViewMatrix * vec4(position,1.0); }',
    fragmentShader: 'uniform vec3 top; uniform vec3 mid; uniform vec3 bot; varying vec3 vP; void main(){ float h = vP.y; vec3 c = h > 0.0 ? mix(mid, top, smoothstep(0.0, 0.6, h)) : mix(mid, bot, smoothstep(0.0, -0.25, h)); gl_FragColor = vec4(c, 1.0); }'
  }));
  g.add(sky);
  var n = 1600, pos = new Float32Array(n * 3), col = new Float32Array(n * 3);
  for (var i = 0; i < n; i++) {
    var u = Math.random() * 2 - 1, th = Math.random() * Math.PI * 2, r = 22000, s = Math.sqrt(1 - u * u);
    var y = Math.abs(u) * 0.9 + 0.05; // 只放在上半球
    pos[i * 3] = r * s * Math.cos(th); pos[i * 3 + 1] = r * y; pos[i * 3 + 2] = r * s * Math.sin(th);
    var b = 0.5 + Math.random() * 0.5, blue = Math.random() < 0.3;
    col[i * 3] = b * (blue ? 0.7 : 1); col[i * 3 + 1] = b * (blue ? 0.85 : 1); col[i * 3 + 2] = b;
  }
  var sg = new THREE.BufferGeometry();
  sg.setAttribute('position', new THREE.BufferAttribute(pos, 3));
  sg.setAttribute('color', new THREE.BufferAttribute(col, 3));
  g.add(new THREE.Points(sg, new THREE.PointsMaterial({ size: 2.2, sizeAttenuation: false, vertexColors: true, transparent: true, opacity: 0.85, depthWrite: false, fog: false })));
  return g;
}
// 发光网格地面: 细网格 + 粗网格, 离中心越远越淡, 一圈脉冲从中心往外扫
function tgGround(THREE, size, y) {
  var m = new THREE.Mesh(new THREE.PlaneGeometry(size, size, 1, 1), new THREE.ShaderMaterial({
    transparent: true, depthWrite: false, blending: THREE.AdditiveBlending,
    uniforms: { uTime: { value: 0 }, uSize: { value: size * 0.5 }, uCol: { value: new THREE.Color(0x0ea5e9) }, uPulse: { value: new THREE.Color(0x22d3ee) } },
    vertexShader: 'varying vec2 vW; void main(){ vW = position.xy; gl_Position = projectionMatrix * modelViewMatrix * vec4(position,1.0); }',
    fragmentShader: [
      'uniform float uTime; uniform float uSize; uniform vec3 uCol; uniform vec3 uPulse; varying vec2 vW;',
      'float gridLine(vec2 p, float s, float w){ vec2 g = abs(fract(p / s - 0.5) - 0.5) * s; vec2 fw = fwidth(p) * w; vec2 l = 1.0 - smoothstep(vec2(0.0), fw, g); return max(l.x, l.y); }',
      'void main(){',
      '  float d = length(vW); float fade = 1.0 - smoothstep(uSize * 0.25, uSize, d);',
      '  float g = gridLine(vW, 60.0, 1.0) * 0.1 + gridLine(vW, 300.0, 1.6) * 0.28;',
      '  float R = mod(uTime * 900.0, uSize * 1.1); float pulse = exp(-pow((d - R) / 60.0, 2.0)) * (1.0 - R / (uSize * 1.1));',
      '  vec3 c = uCol * g + uPulse * pulse * (0.35 + g * 1.5);',
      '  gl_FragColor = vec4(c * fade, 1.0);',
      '}'].join('\n'),
    extensions: { derivatives: true }
  }));
  m.rotation.x = -Math.PI / 2; m.position.y = y;
  m.renderOrder = -1;
  return m;
}
function tgRadialTexture(stops) {
  var THREE = GL.THREE, cv = document.createElement('canvas');
  cv.width = cv.height = 128;
  var ctx = cv.getContext('2d'), g = ctx.createRadialGradient(64, 64, 0, 64, 64, 64);
  stops.forEach(function (s) { g.addColorStop(s[0], s[1]); });
  ctx.fillStyle = g; ctx.fillRect(0, 0, 128, 128);
  var t = new THREE.CanvasTexture(cv); t.userData.shared = true;
  return t;
}
function tgGlowTex() {
  if (!GL.glowTex) { GL.glowTex = tgRadialTexture([[0, 'rgba(255,255,255,1)'], [0.25, 'rgba(255,255,255,.55)'], [0.6, 'rgba(255,255,255,.12)'], [1, 'rgba(255,255,255,0)']]); }
  return GL.glowTex;
}
// 流光贴图: 一段亮一段暗, 沿管子 u 方向重复, 移动 offset 就是流动效果; dashed = 推断链路(断续)
function tgFlowTex(dashed) {
  var k = dashed ? 'flowTexD' : 'flowTex';
  if (!GL[k]) {
    var THREE = GL.THREE, cv = document.createElement('canvas');
    cv.width = 128; cv.height = 8;
    var ctx = cv.getContext('2d'), g = ctx.createLinearGradient(0, 0, 128, 0);
    if (dashed) { g.addColorStop(0, 'rgba(255,255,255,0)'); g.addColorStop(0.2, 'rgba(255,255,255,.9)'); g.addColorStop(0.4, 'rgba(255,255,255,0)'); g.addColorStop(1, 'rgba(255,255,255,0)'); }
    else { g.addColorStop(0, 'rgba(255,255,255,.08)'); g.addColorStop(0.55, 'rgba(255,255,255,.25)'); g.addColorStop(0.85, 'rgba(255,255,255,1)'); g.addColorStop(1, 'rgba(255,255,255,.08)'); }
    ctx.fillStyle = g; ctx.fillRect(0, 0, 128, 8);
    var t = new THREE.CanvasTexture(cv);
    t.wrapS = THREE.RepeatWrapping; t.wrapT = THREE.ClampToEdgeWrapping; t.userData.shared = true;
    GL[k] = t;
  }
  return GL[k];
}
function tgDispose(c) {
  if (c.st) { c.st.dispose(); c.st = null; }
  Object.keys(GL.panels || {}).forEach(function (k) { if (k.indexOf(c.key || 'tg:') === 0) { GL.panels[k].tex.dispose(); delete GL.panels[k]; } });
}
function tgMatch(c, n) {
  if (!c.search) { return false; }
  return (n.name + ' ' + (n.dev ? n.dev.ip : '')).toLowerCase().indexOf(c.search) >= 0;
}
function tgZoom(c, f) {
  var st = c.st, t = st.controls.target;
  var v = st.camera.position.clone().sub(t).multiplyScalar(1 / f);
  st.camera.position.copy(t).add(v); st.poke();
}

/* ---------- v12: 拖动摆放 ---------- */
function tgSig(lay) {
  return lay.nodes.map(function (n) { return n.id + '@' + n.layer + '@' + Math.round(n.x) + ',' + Math.round(n.z); }).join('|') + '#' + lay.links.map(function (lk) { return lk.l ? lk.l.source : 'e'; }).join('');
}
// 链路曲线: 两端设备半高处连一条二次贝塞尔, 同层的往上拱高一点免得穿过别的设备
function tgLinkCurve(c, lk) {
  var THREE = c.st.THREE, a = c.meshes[lk.a.id], b = c.meshes[lk.b.id];
  if (!a || !b) { return null; }
  var pa = new THREE.Vector3(lk.a.x, lk.a.y + (a.userData.height || 10) * 0.5, lk.a.z);
  var pb = new THREE.Vector3(lk.b.x, lk.b.y + (b.userData.height || 10) * 0.5, lk.b.z);
  var mid = pa.clone().add(pb).multiplyScalar(0.5), dist = pa.distanceTo(pb);
  mid.y += lk.a.layer === lk.b.layer ? Math.min(160, dist * 0.35) : dist * 0.08;
  return new THREE.QuadraticBezierCurve3(pa, mid, pb);
}
function tgPlaneHit(c, e, y) {
  var st = c.st, THREE = st.THREE, r = st.renderer.domElement.getBoundingClientRect();
  var ndc = new THREE.Vector2((e.clientX - r.left) / r.width * 2 - 1, -(e.clientY - r.top) / r.height * 2 + 1);
  var rc = c._rc || (c._rc = new THREE.Raycaster());
  rc.setFromCamera(ndc, st.camera);
  var p = new THREE.Vector3();
  return rc.ray.intersectPlane(new THREE.Plane(new THREE.Vector3(0, 1, 0), -y), p) ? p : null;
}
// 拖动中: 只挪模型和相关链路的几何体, 松手后整体重建一次(层圆盘/层名牌按新位置重算)
function tgDragMove(c, e) {
  var d = c.drag, n = d.n, hit = tgPlaneHit(c, e, n.y);
  if (!hit) { return; }
  var nx = hit.x - d.ox, nz = hit.z - d.oz;
  if (!d.moved && Math.abs(nx - d.x0) + Math.abs(nz - d.z0) < 4) { return; }
  d.moved = true;
  var dx = nx - n.x, dz = nz - n.z, moved = [n], set = {};
  c.nodes.forEach(function (x) { if (x.ext && x.parent === n.id) { x.x += dx; x.z += dz; moved.push(x); } });
  n.x = nx; n.z = nz;
  moved.forEach(function (x) {
    set[x.id] = true;
    var g = c.meshes[x.id]; if (!g) { return; }
    g.position.x = x.x; g.position.z = x.z;
    if (!x.ext && !x.man && (x.x || x.z)) { g.rotation.y = Math.atan2(x.x, x.z); }
  });
  c.linkObjs.forEach(function (obj) { var lk = obj.userData.link; if (set[lk.a.id] || set[lk.b.id]) { tgRelink(c, obj); } });
  c.st.poke();
}
function tgRelink(c, obj) {
  var THREE = c.st.THREE, lk = obj.userData.link, old = obj.userData.curve, curve = tgLinkCurve(c, lk);
  if (!curve) { return; }
  var seg = Math.max(24, Math.min(80, Math.round(curve.v0.distanceTo(curve.v2) / 12)));
  obj.geometry.dispose();
  if (lk.ext) { obj.geometry = new THREE.BufferGeometry().setFromPoints(curve.getPoints(24)); obj.computeLineDistances(); }
  else {
    obj.geometry = new THREE.TubeGeometry(curve, seg, obj.userData.r || 2, 8, false);
    obj.children.forEach(function (ch) {
      if (ch.userData.tube) { ch.geometry.dispose(); ch.geometry = new THREE.TubeGeometry(curve, seg, (obj.userData.r || 2) * ch.userData.tube[0], ch.userData.tube[1], false); }
      else if (ch.userData.linkTag) { ch.position.copy(curve.getPoint(0.5)); ch.position.y += 14; }
    });
  }
  obj.userData.curve = curve;
  c.pdata.forEach(function (p) { if (p.curve === old) { p.curve = curve; } });
}
function tgDragEnd(c) {
  var d = c.drag; c.drag = null;
  if (c.st) { c.st.controls.enabled = true; }
  if (!d || !d.moved || !c.st) { return; }
  var n = d.n, p3 = { x: Math.round(n.x * 10) / 10, y: Math.round(n.z * 10) / 10 };
  n.pinned = true;
  if (n.man) { n.mdata.p3 = p3; } else { n.dev.p3 = p3; }
  c.local3d = c.local3d || {}; c.local3d[n.id] = p3;
  t3LayerMeta(c.lay, TG_SX);
  tgBuildScene(c, c.lay); c.sig = tgSig(c.lay);
  tgApplyHighlight(c);
  if (c.opts.onMoved) { c.opts.onMoved(n, p3); }
}
// 拓扑窗口里的实例: 顶栏的「✋ 拖动摆放」「⟲ 自动排列」
function tgToggleEdit() {
  if (!TG || !TG.st) { return; }
  TG.editOn = !TG.editOn;
  if (TG.editOn) { TG.st.controls.autoRotate = false; TG.auto = false; }
  else { TG.auto = T3.auto; TG.st.controls.autoRotate = T3.auto; }
  tgSyncEngineCtl();
  topoMsg(TG.editOn ? '拖动摆放: 按住设备拖到想要的位置(只在它所在那一层的平面上移动), 松手自动保存; 拖空白处仍然是旋转视角' : '已退出拖动摆放', true);
}
function tgSave3D(n, p3) {
  if (!(typeof can === 'function' ? can('topology') : authenticated)) {
    topoMsg(authenticated ? '当前账号没有「拓扑编辑」权限, 位置只在本机临时生效' : '未登录, 位置只在本机临时生效; 登录后再拖一次即可保存', false);
    return;
  }
  var body = {}; body[n.id] = p3;
  authedApi('/api/topology/positions', { mode: '3d', positions: body }).then(function () { topoMsg('已保存「' + n.name + '」的 3D 位置', true); })
    .catch(function (e) { topoMsg('保存失败: ' + e, false); });
}
function tgAutoArrange() {
  if (!TG || !TG.st) { return; }
  if (!confirm('清除 3D 拓扑里手工拖过的位置, 全部按自动布局重新排列?')) { return; }
  var redo = function (d) {
    TG.local3d = {};
    (d.devices || []).forEach(function (x) { delete x.p3; }); (d.manual_nodes || []).forEach(function (x) { delete x.p3; });
    topoData = d; tgSetData(TG, d, false); topoMsg('已按自动布局重新排列', true);
  };
  if (typeof can === 'function' && can('topology')) {
    authedApi('/api/topology/positions', { mode: '3d', reset: true }).then(function () { return pub('/api/topology'); }).then(redo).catch(function (e) { topoMsg('操作失败: ' + e, false); });
  } else { redo(TG.data); }
}

// 坐标: t3Layout 的单位直接当世界单位, 设备模型按层大小缩放
var TG_SX = 1.7;
var TG_SIZE =[{ w: 170, uh: 19, d: 110 }, { w: 145, uh: 16, d: 92 }, { w: 120, uh: 13.5, d: 76 }, { w: 105, uh: 12, d: 66 }];
var TG_MAN_COLOR = 0xc084fc;
function tgSetData(c, data, first) {
  if (!c.st) { return; }
  var lay = t3Layout(data);
  // 画成交换机实物以后比 Canvas 版的小球占地方, 整体拉开一点
  var SX = TG_SX, SY = 1.35;
  lay.nodes.forEach(function (n) { n.x *= SX; n.z *= SX; n.y *= SY; });
  lay.layers.forEach(function (L) { L.y *= SY; });
  // v12: 3D 里手工拖过的位置(世界坐标, 存在 topology.json 的 pos3d 里), 挂在它下面的未监控邻居跟着平移
  lay.nodes.forEach(function (n) {
    var p3 = !n.ext && ((c.local3d && c.local3d[n.id]) || (n.man ? n.mdata && n.mdata.p3 : n.dev && n.dev.p3));
    if (p3) { n._dx = p3.x - n.x; n._dz = p3.y - n.z; n.x = p3.x; n.z = p3.y; n.pinned = true; }
  });
  lay.nodes.forEach(function (n) { var p = n.ext && lay.byId[n.parent]; if (p && p.pinned) { n.x += p._dx; n.z += p._dz; } });
  t3LayerMeta(lay, SX);
  c.data = data; c.nodes = lay.nodes; c.links = lay.links; c.lay = lay;
  if (c === TG) { T3.data = data; T3.nodes = lay.nodes; T3.links = lay.links; T3.byId = lay.byId; T3.layerInfo = lay.layers; T3.extent = lay.extent; }
  var setSig = lay.nodes.map(function (n) { return n.id + '@' + n.layer; }).join('|') + '#' + lay.links.map(function (lk) { return lk.l ? lk.l.source : 'e'; }).join('');
  var sig = tgSig(lay), rebuild = sig !== c.sig, reset = setSig !== c.setSig;
  c.sig = sig; c.setSig = setSig;
  // 面板数据(端口灯)单独拉一次, 失败不影响拓扑本身
  pub('/api/panels').then(function (d) {
    c.panels = {};
    (d.devices || []).forEach(function (x) { c.panels[x.ip] = x; });
  }).catch(function () {}).then(function () {
    if (!c.st) { return; }
    if (rebuild) { c.wantIntro = !!first; tgBuildScene(c, lay); } else { tgUpdateScene(c, lay); }
    if (first) { tgIntro(c); } else if (reset) { tgResetCamera(c, true); }
    if (c.opts.onBuilt) { c.opts.onBuilt(); }
    tgApplyHighlight(c);
    c.st.poke();
  });
}
function tgPanelDev(c, n) {
  var p = c.panels[n.id] || {};
  var d = n.dev || {};
  return { ip: n.id, name: d.name || n.name, brand: d.brand || p.brand, model: d.model || p.model, alive: d.alive, alerts: d.open_alerts || 0,
    power: p.power, fan: p.fan, ports: p.ports || [], form: d.form || p.form || { kind: 'box', members: 1, u_each: 1, total_u: 1 } };
}
function tgParticleSet(c, THREE, ppos, pcol) {
  if (c.particles) { c.world.remove(c.particles); c.particles.geometry.dispose(); c.particles.material.dispose(); c.particles = null; }
  if (!ppos.length) { return; }
  var pg = new THREE.BufferGeometry();
  pg.setAttribute('position', new THREE.Float32BufferAttribute(ppos, 3));
  pg.setAttribute('color', new THREE.Float32BufferAttribute(pcol, 3));
  c.particles = new THREE.Points(pg, new THREE.PointsMaterial({ size: c.opts.bigscreen ? 26 : 22, map: glDotTexture(), vertexColors: true, transparent: true, depthWrite: false, blending: THREE.AdditiveBlending }));
  c.particles.frustumCulled = false;
  c.particles.visible = c.showParticles;
  c.world.add(c.particles);
}
function tgLinkDots(c, THREE, lk, curve, ppos, pcol) {
  var l = lk.l, sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0);
  [[l.a_out_mbps || 0, 1, 0xe0f2fe], [l.a_in_mbps || 0, -1, 0xf0abfc]].forEach(function (x) {
    if (!(x[0] > 0.01)) { return; }
    var cnt = Math.max(1, Math.min(14, Math.round(Math.log(1 + x[0]) * 1.6))), speed = 0.08 + Math.min(1, sp ? x[0] / sp : 0.1) * 0.6, col = new THREE.Color(x[2]);
    for (var i = 0; i < cnt; i++) { c.pdata.push({ curve: curve, off: i / cnt, speed: speed, dir: x[1] }); ppos.push(0, 0, 0); pcol.push(col.r, col.g, col.b); }
  });
}
function tgLinkUtil(l) {
  var sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0);
  return sp ? Math.max(l.a_in_mbps || 0, l.a_out_mbps || 0, l.b_in_mbps || 0, l.b_out_mbps || 0) / sp : 0;
}
function tgLinkColor(l) {
  if (l.source === 'manual' && !(Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0) > 0)) { return TG_MAN_COLOR; }
  var u = tgLinkUtil(l);
  return u >= 0.8 ? 0xef4444 : u >= 0.5 ? 0xfacc15 : u >= 0.01 ? 0x22d3ee : 0x3b82f6;
}
// 流光速度: 有流量越大越快, 方向按 A→B / B→A 哪边发得多
function tgFlowSpeed(l) {
  var ab = l.a_out_mbps || 0, ba = l.b_out_mbps || l.a_in_mbps || 0, sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0);
  var v = Math.max(ab, ba), u = sp ? v / sp : 0;
  var s = v > 0.01 ? 0.25 + Math.min(1, u) * 1.8 + Math.log(1 + v) * 0.05 : 0.08;
  return ab >= ba ? s : -s;
}
function tgBuildScene(c, lay) {
  var st = c.st, THREE = st.THREE;
  glClear(c.world);
  c.meshes = {}; c.linkObjs = []; c.pickables = []; c.rings = []; c.spins = []; c.beams = []; c.flows = []; c.pdata = []; c.particles = null; c.ground = null; c.badges = [];
  st.lod = [];
  var lodFar = Math.max(2600, (lay.extent || 800) * 3.2), lodNear = lodFar * 0.55;
  var minY = Math.min.apply(null, lay.layers.map(function (L) { return L.y; }).concat([0]));
  c.ground = tgGround(THREE, Math.max(7000, lay.extent * 7), minY - 110);
  c.world.add(c.ground);
  // 每层一圈全息平台
  lay.layers.forEach(function (L, li) {
    var col = new THREE.Color(T3_LAYER_COLORS[Math.min(L.L, 3)] || '#94a3b8'), hex = '#' + col.getHexString();
    var R = Math.max(L.r, 110);
    if (!GL.discTex) { GL.discTex = tgRadialTexture([[0, 'rgba(255,255,255,.0)'], [0.7, 'rgba(255,255,255,.05)'], [0.97, 'rgba(255,255,255,.22)'], [1, 'rgba(255,255,255,0)']]); }
    var disc = new THREE.Mesh(new THREE.CircleGeometry(R * 1.05, 72), new THREE.MeshBasicMaterial({ map: GL.discTex,
      color: col, transparent: true, opacity: 0.55, side: THREE.DoubleSide, depthWrite: false, blending: THREE.AdditiveBlending }));
    disc.rotation.x = -Math.PI / 2; disc.position.y = L.y - 6;
    c.world.add(disc);
    var ring = new THREE.Mesh(new THREE.RingGeometry(R - 2, R + 2, 128), new THREE.MeshBasicMaterial({ color: col, transparent: true, opacity: 0.6, side: THREE.DoubleSide, depthWrite: false, blending: THREE.AdditiveBlending }));
    ring.rotation.x = -Math.PI / 2; ring.position.y = L.y - 5;
    c.world.add(ring);
    // 刻度环: 一圈短线, 反向慢慢转
    var ticks = [], n = 120, R2 = R * 1.08;
    for (var i = 0; i < n; i++) {
      var a = i / n * Math.PI * 2, len = i % 10 === 0 ? 18 : 7;
      ticks.push(Math.cos(a) * R2, 0, Math.sin(a) * R2, Math.cos(a) * (R2 + len), 0, Math.sin(a) * (R2 + len));
    }
    var tg = new THREE.BufferGeometry(); tg.setAttribute('position', new THREE.Float32BufferAttribute(ticks, 3));
    var tick = new THREE.LineSegments(tg, new THREE.LineBasicMaterial({ color: col, transparent: true, opacity: 0.55, depthWrite: false, blending: THREE.AdditiveBlending }));
    tick.position.y = L.y - 5; tick.userData.spin = (li % 2 ? 1 : -1) * 0.06;
    c.world.add(tick); c.spins.push(tick);
    // 虚线外环
    var arc = new THREE.Line(new THREE.BufferGeometry().setFromPoints(new THREE.EllipseCurve(0, 0, R * 1.18, R * 1.18, 0, Math.PI * 1.6).getPoints(96).map(function (p) { return new THREE.Vector3(p.x, 0, p.y); })),
      new THREE.LineDashedMaterial({ color: col, dashSize: 14, gapSize: 10, transparent: true, opacity: 0.4, depthWrite: false }));
    arc.computeLineDistances(); arc.position.y = L.y - 5; arc.userData.spin = (li % 2 ? -1 : 1) * 0.12;
    c.world.add(arc); c.spins.push(arc);
    // 只有未监控邻居的那一圈不挂名牌(数量在右上角统计里有), 免得和接入层的名牌叠在一起
    if (L.name.indexOf(T3_LAYER_NAMES[4]) !== 0) {
      var lb = glLabel(L.name, { screen: 0.03, color: hex, bg: 'rgba(2,8,23,.55)', border: hex });
      var la = L.la === undefined ? Math.PI * 0.75 : L.la, next = lay.layers[li + 1]; // 放在设备之间最大的空当里
      var lr = Math.max(R * 1.1, next ? next.r * 0.55 : 0);
      lb.position.set(Math.cos(la) * lr, L.y + 18, Math.sin(la) * lr);
      c.world.add(lb);
    }
  });
  var glowTex = tgGlowTex();
  lay.nodes.forEach(function (n) {
    var g, dev = n.dev || {};
    if (n.ext) {
      g = new THREE.Group();
      var oc = new THREE.Mesh(new THREE.OctahedronGeometry(11), new THREE.MeshBasicMaterial({ color: 0x94a3b8, wireframe: true, transparent: true, opacity: 0.75 }));
      oc.position.y = 10; oc.userData.spin = 0.6; c.spins.push(oc);
      g.add(oc);
      var eg = new THREE.Sprite(new THREE.SpriteMaterial({ map: glowTex, color: 0x64748b, transparent: true, opacity: 0.6, depthWrite: false, blending: THREE.AdditiveBlending }));
      eg.scale.set(40, 40, 1); eg.position.y = 10; g.add(eg);
      g.userData.height = 20;
    } else if (n.man) {
      g = new THREE.Group();
      var m = n.mdata || {};
      var ico = new THREE.Mesh(new THREE.IcosahedronGeometry(34, 0), new THREE.MeshBasicMaterial({ color: TG_MAN_COLOR, wireframe: true, transparent: true, opacity: 0.9 }));
      var core = new THREE.Mesh(new THREE.IcosahedronGeometry(18, 1), new THREE.MeshStandardMaterial({ color: 0x7c3aed, emissive: 0xa855f7, emissiveIntensity: 0.9, roughness: 0.3, metalness: 0.2 }));
      ico.position.y = core.position.y = 42; ico.userData.spin = 0.5; core.userData.spin = -0.8;
      g.add(ico); g.add(core); c.spins.push(ico, core);
      var mg = new THREE.Sprite(new THREE.SpriteMaterial({ map: glowTex, color: TG_MAN_COLOR, transparent: true, opacity: 0.55, depthWrite: false, blending: THREE.AdditiveBlending }));
      mg.scale.set(150, 150, 1); mg.position.y = 42; g.add(mg);
      g.userData.height = 84;
      var ml = glLabel((TP_KIND_ICON[m.kind] || '◆') + ' ' + n.name, { screen: c.opts.bigscreen ? 0.034 : 0.04, color: '#f5d0fe', sub: (TP_KIND_NAME[m.kind] || '手工节点') + (m.ip ? ' · ' + m.ip : ''), subColor: '#d8b4fe', bg: 'rgba(24,6,40,.6)', border: '#a855f7' });
      ml.center.set(0.5, 0); ml.position.y = 92; g.add(ml); glLod(st, ml, lodNear, lodFar);
    } else {
      var sz = TG_SIZE[Math.min(n.layer, 3)];
      g = glSwitchModel(tgPanelDev(c, n), sz, { keyPrefix: c.key || 'tg:' });
      var h = g.userData.height;
      if (h > 220) { g.scale.setScalar(220 / h); g.userData.height = 220; }
      var f = dev.form || {}, lc = T3_LAYER_COLORS[Math.min(n.layer, 3)];
      var lb2 = glLabel(n.name, { screen: c.opts.bigscreen ? 0.03 : 0.033, color: dev.alive ? '#e2e8f0' : '#94a3b8',
        sub: n.id + (f.kind === 'stack' ? ' · ' + f.members + '台堆叠' : f.kind === 'chassis' ? ' · 框式' : ''),
        subColor: f.kind === 'stack' ? '#67e8f9' : '#94a3b8', bg: 'rgba(2,8,23,.6)', border: dev.alive ? lc : '#64748b' });
      lb2.scale.divideScalar(g.scale.x); // 父级按高度缩小过, 标签要抵消掉
      lb2.center.set(0.5, 0);
      lb2.position.y = (g.userData.height + 16) / g.scale.y;
      g.add(lb2); glLod(st, lb2, lodNear * (n.layer <= 1 ? 1.6 : 1), lodFar * (n.layer <= 1 ? 1.6 : 1));
      // 发光底座: 实心圆台 + 地面光晕
      var base = new THREE.Mesh(new THREE.CylinderGeometry(sz.w * 0.5, sz.w * 0.58, 4, 40), new THREE.MeshStandardMaterial({ color: lc, emissive: lc, emissiveIntensity: 0.8, transparent: true, opacity: 0.75 }));
      base.position.y = -3; g.add(base);
      var pad = new THREE.Mesh(new THREE.PlaneGeometry(sz.w * 2.6, sz.w * 2.6), new THREE.MeshBasicMaterial({ map: glowTex, color: dev.alive ? lc : '#475569', transparent: true, opacity: 0.22, depthWrite: false, blending: THREE.AdditiveBlending }));
      pad.rotation.x = -Math.PI / 2; pad.position.y = -4.5; pad.userData.noBox = true; g.add(pad);
      if (dev.open_alerts > 0 || !dev.alive) {
        var rg = new THREE.Mesh(new THREE.RingGeometry(sz.w * 0.55, sz.w * 0.62, 48), new THREE.MeshBasicMaterial({ color: dev.alive ? 0xef4444 : 0x64748b, transparent: true, opacity: 0.6, side: THREE.DoubleSide, depthWrite: false }));
        rg.rotation.x = -Math.PI / 2; rg.position.y = 1;
        rg.userData.phase = Math.random(); rg.userData.noBox = true;
        g.add(rg);
        if (dev.alive) {
          c.rings.push(rg);
          // 告警光柱: 从设备顶上射向天空
          var beam = new THREE.Mesh(new THREE.CylinderGeometry(sz.w * 0.035, sz.w * 0.11, 220, 20, 1, true), new THREE.ShaderMaterial({
            transparent: true, depthWrite: false, side: THREE.DoubleSide, blending: THREE.AdditiveBlending,
            uniforms: { opacity: { value: 0.5 }, col: { value: new THREE.Color(0xef4444) } },
            vertexShader: 'varying float vY; void main(){ vY = uv.y; gl_Position = projectionMatrix * modelViewMatrix * vec4(position,1.0); }',
            fragmentShader: 'uniform float opacity; uniform vec3 col; varying float vY; void main(){ gl_FragColor = vec4(col, opacity * pow(1.0 - vY, 1.6)); }'
          }));
          beam.material.opacity = 0.5;
          Object.defineProperty(beam.material, 'opacity', { get: function () { return this.uniforms.opacity.value; }, set: function (v) { if (this.uniforms) { this.uniforms.opacity.value = v; } } });
          beam.position.y = g.userData.height / g.scale.y + 110; beam.userData.phase = Math.random() * 6;
          beam.userData.noBox = true;
          g.add(beam); c.beams.push(beam);
          // 告警徽标: 设备头顶飘着的 ⚠ N, 数量一眼可见
          var bd = glLabel('⚠ ' + dev.open_alerts, { screen: c.opts.bigscreen ? 0.026 : 0.03, color: '#fff', bg: 'rgba(220,38,38,.92)', border: '#fecaca', depthTest: false, renderOrder: 40 });
          bd.center.set(0.5, -1.55); bd.scale.divideScalar(g.scale.x); // 名牌正上方(都是按屏幕高度算大小, 偏移也按屏幕算)
          bd.userData.y0 = bd.position.y = (g.userData.height + 16) / g.scale.y; bd.userData.phase = Math.random() * 6;
          g.add(bd); c.badges.push(bd);
        }
      }
    }
    g.position.set(n.x, n.y, n.z);
    if (!n.ext && !n.man && (n.x || n.z)) { g.rotation.y = Math.atan2(n.x, n.z); }
    g.userData.pick = true; g.userData.node = n;
    g.userData.baseScale = g.scale.x; g.userData.curScale = g.scale.x;
    c.world.add(g);
    c.meshes[n.id] = g;
    c.pickables.push(g);
  });
  var ppos = [], pcol = [];
  lay.links.forEach(function (lk) {
    var curve = tgLinkCurve(c, lk);
    if (!curve) { return; }
    var dist = curve.v0.distanceTo(curve.v2);
    var obj;
    if (lk.ext) {
      obj = new THREE.Line(new THREE.BufferGeometry().setFromPoints(curve.getPoints(24)), new THREE.LineDashedMaterial({ color: 0x64748b, dashSize: 6, gapSize: 5, transparent: true, opacity: 0.55 }));
      obj.computeLineDistances();
    } else {
      var l = lk.l, sp = Math.max(l.a_speed_mbps || 0, l.b_speed_mbps || 0), col = tgLinkColor(l);
      var r = 1.6 + Math.log(1 + sp / 1000) * 1.1 + (l.is_agg ? 1.5 : 0);
      var seg = Math.max(24, Math.min(80, Math.round(dist / 12)));
      obj = new THREE.Mesh(new THREE.TubeGeometry(curve, seg, r, 8, false), new THREE.MeshBasicMaterial({ color: col, transparent: true, opacity: l.source === 'mac' ? 0.35 : 0.95 }));
      obj.userData.r = r;
      // 外层流光
      var tex = tgFlowTex(l.source === 'mac').clone();
      tex.userData = {}; // 克隆的贴图随链路一起释放(图片源是共享的)
      tex.repeat.set(Math.max(1, Math.round(dist / 90)), 1); tex.needsUpdate = true;
      var glow = new THREE.Mesh(new THREE.TubeGeometry(curve, seg, r * 2.6, 10, false), new THREE.MeshBasicMaterial({ map: tex, color: col, transparent: true, opacity: 0.6, depthWrite: false, blending: THREE.AdditiveBlending }));
      glow.userData.tube = [2.6, 10];
      obj.add(glow);
      c.flows.push({ tex: tex, speed: tgFlowSpeed(l) });
      if (l.is_agg) {
        var aggM = new THREE.Mesh(new THREE.TubeGeometry(curve, seg, r * 4, 8, false), new THREE.MeshBasicMaterial({ color: 0xf59e0b, transparent: true, opacity: 0.12, depthWrite: false, blending: THREE.AdditiveBlending }));
        aggM.userData.tube = [4, 8]; obj.add(aggM);
      }
      if (l.source === 'mac' || l.source === 'cdp' || (l.source === 'manual' && l.note)) {
        var tl = glLabel(l.source === 'mac' ? 'MAC 推断' : l.source === 'cdp' ? 'CDP' : l.note, { size: 18, color: l.source === 'manual' ? '#e9d5ff' : '#cbd5e1', bg: 'rgba(2,8,23,.55)' });
        tl.position.copy(curve.getPoint(0.5)); tl.position.y += 14;
        tl.userData.linkTag = true;
        obj.add(tl);
      }
      tgLinkDots(c, THREE, lk, curve, ppos, pcol);
    }
    obj.userData.pick = true; obj.userData.link = lk; obj.userData.curve = curve;
    c.world.add(obj);
    c.linkObjs.push(obj);
    obj.userData.layerDelay = Math.max(lk.a.layer || 0, lk.b.layer || 0);
    c.pickables.push(obj);
  });
  tgParticleSet(c, THREE, ppos, pcol);
  if (c.wantIntro) { c.wantIntro = false; tgIntroStart(c); }
  if (c.sel) { var sn = lay.byId[c.sel.id]; c.sel = sn || null; if (sn) { c.selBox.fit(c.meshes[sn.id], 10); } else { c.selBox.visible = false; tgCardHide(c); } }
}
// 同一批设备: 只刷新面板贴图 / 链路颜色 / 粒子 / 流光速度
function tgUpdateScene(c, lay) {
  var THREE = c.st.THREE;
  lay.nodes.forEach(function (n) {
    var g = c.meshes[n.id];
    if (!g || n.ext || n.man) { return; }
    g.userData.node = n;
    if (g.userData.members) { glRefreshSwitch(g, tgPanelDev(c, n), { keyPrefix: c.key || 'tg:' }); }
  });
  var keyOf = function (lk) { return lk.a.id + '>' + lk.b.id + (lk.l ? '|' + lk.l.a_port + '|' + lk.l.source : '|' + lk.port); };
  var byKey = {};
  lay.links.forEach(function (lk) { byKey[keyOf(lk)] = lk; });
  c.pdata = [];
  var ppos = [], pcol = [], fi = 0;
  c.linkObjs.forEach(function (obj) {
    var lk = byKey[keyOf(obj.userData.link)] || obj.userData.link;
    obj.userData.link = lk;
    if (lk.ext || !lk.l) { return; }
    var col = tgLinkColor(lk.l);
    obj.material.color.setHex(col);
    if (obj.children[0] && obj.children[0].material) { obj.children[0].material.color.setHex(col); }
    if (c.flows[fi]) { c.flows[fi].speed = tgFlowSpeed(lk.l); }
    fi++;
    tgLinkDots(c, THREE, lk, obj.userData.curve, ppos, pcol);
  });
  tgParticleSet(c, THREE, ppos, pcol);
}
function glDotTexture() {
  if (GL.dotTex) { return GL.dotTex; }
  var THREE = GL.THREE, cv = document.createElement('canvas');
  cv.width = cv.height = 64;
  var ctx = cv.getContext('2d'), g = ctx.createRadialGradient(32, 32, 0, 32, 32, 32);
  g.addColorStop(0, 'rgba(255,255,255,1)'); g.addColorStop(0.3, 'rgba(255,255,255,.8)'); g.addColorStop(1, 'rgba(255,255,255,0)');
  ctx.fillStyle = g; ctx.fillRect(0, 0, 64, 64);
  GL.dotTex = new THREE.CanvasTexture(cv);
  GL.dotTex.userData.shared = true;
  return GL.dotTex;
}
function tgHover(c, e) {
  var st = c.st;
  if (!st) { return; }
  var h = st.pick(e, c.pickables);
  var node = h && h.obj.userData.node, link = h && h.obj.userData.link;
  var changed = node !== c.hover || link !== c.hoverLink;
  c.hover = node || null; c.hoverLink = node ? null : (link || null);
  var show = c.opts.showTip, hide = c.opts.hideTip || function () {};
  if (show && node) {
    var dev = node.dev;
    show(node.ext ? externalTipHTML(node.extData) : node.man ? tpManualTip(node.mdata) : deviceTipHTML(dev) + (dev.form ? '<br><span style="color:#67e8f9;">' + esc(glFormText(dev.form)) + '</span><br><span style="color:#94a3b8;font-size:11px;">依据: ' + esc(dev.form.source || '') + '</span>' : ''), e);
  } else if (show && link) {
    if (link.ext) {
      show('<b>' + esc(link.b.name) + '</b><br><span style="color:#94a3b8;">未被监控的 LLDP 邻居</span><br>' + esc(link.a.name) + ' 端口: ' + esc(link.port || '-'), e);
    } else {
      var names = {};
      (c.data.devices || []).forEach(function (d) { names[d.ip] = d; });
      (c.data.manual_nodes || []).forEach(function (m) { names['m:' + m.id] = { name: m.name }; });
      show('<div style="margin-bottom:4px;color:#93c5fd;">来源: ' + (TP_SRC_NAME[link.l.source] || 'LLDP') + (link.l.note ? ' · <span style="color:#cbd5e1;white-space:normal;">' + esc(link.l.note) + '</span>' : '') + '</div>' + linkTipHTML(link.l, names), e);
    }
  } else { hide(); }
  st.renderer.domElement.style.cursor = c.editOn ? (node && !node.ext ? 'move' : '') : node && !node.ext && !node.man ? 'pointer' : '';
  if (changed) {
    tgApplyHighlight(c);
    c.hovBox.fit(node && !node.ext && node !== c.sel ? c.meshes[node.id] : null, 6);
  }
}
// 悬停/搜索时只突出相关设备和链路, 其余变淡
function tgFade(o, on, dim) {
  o.traverse(function (x) {
    if (!x.material) { return; }
    (Array.isArray(x.material) ? x.material : [x.material]).forEach(function (m) {
      if (m.userData.baseOpacity === undefined) { m.userData.baseOpacity = m.opacity; m.userData.baseTransparent = m.transparent; }
      m.transparent = !on || m.userData.baseTransparent;
      m.opacity = on ? m.userData.baseOpacity : Math.min(m.userData.baseOpacity, x.userData.label ? 0.3 : dim);
    });
  });
}
function tgApplyHighlight(c) {
  if (!c.st) { return; }
  var focus = c.hover || c.focus, fl = c.hoverLink, rel = {}, searching = !!c.search;
  if (focus) {
    rel[focus.id] = true;
    c.links.forEach(function (lk) { if (lk.a === focus || lk.b === focus) { rel[lk.a.id] = rel[lk.b.id] = true; } });
  }
  if (fl) { rel[fl.a.id] = rel[fl.b.id] = true; }
  var any = focus || fl;
  Object.keys(c.meshes).forEach(function (id) {
    var g = c.meshes[id], n = g.userData.node;
    var on = any ? rel[id] : (searching ? tgMatch(c, n) : true);
    tgFade(g, on, 0.18);
    if (g.userData.baseScale === undefined) { g.userData.baseScale = g.scale.x; }
    // 缩放交给 tgEaseScales 平滑过渡
  });
  c.linkObjs.forEach(function (obj) {
    var lk = obj.userData.link;
    var on = any ? (fl ? lk === fl : (lk.a === focus || lk.b === focus)) : true;
    tgFade(obj, on, 0.06);
  });
  c.st.poke();
}
function tgCamPos(c) {
  var span = Math.max(c.lay.extent || 300, (c.lay.layers.length - 1) * 130);
  var d = Math.max(700, span * 2.1 + 350) * (c.opts.bigscreen ? 0.8 : 1);
  return { x: d * 0.62, y: d * 0.5, z: d * 0.62 };
}
function tgResetCamera(c, instant) {
  var st = c.st;
  if (!st || !c.lay) { return; }
  var pos = tgCamPos(c);
  if (instant) { st.camera.position.set(pos.x, pos.y, pos.z); st.controls.target.set(0, 0, 0); st.poke(); } else { st.flyTo(pos, { x: 0, y: 0, z: 0 }, 900); }
}
// 首次打开: 从高空俯冲下来
function tgIntro(c) {
  var st = c.st;
  if (!st || !c.lay) { return; }
  var pos = tgCamPos(c);
  st.camera.position.set(-pos.x * 1.6, pos.y * 4, pos.z * 1.2); st.controls.target.set(0, 0, 0);
  st.flyTo(pos, { x: 0, y: 0, z: 0 }, 1800);
}
function tgFlyTo(c, n) {
  var st = c.st;
  if (!st) { return; }
  var g = c.meshes[n.id], h = g ? (g.userData.height || 20) : 20;
  var dir = st.camera.position.clone().sub(st.controls.target).normalize();
  var out = new st.THREE.Vector3(n.x, 0, n.z);
  if (out.length() > 1) { out.normalize(); dir = new st.THREE.Vector3(out.x * 0.85, 0.45, out.z * 0.85).normalize(); }
  var tgt = { x: n.x, y: n.y + h / 2, z: n.z };
  // 镜头距离按设备实际大小算(宽的核心交换机 / 高的框式都能完整入镜)
  var bs = g ? glBoxOf(g).getBoundingSphere(new st.THREE.Sphere()) : null, R = bs && isFinite(bs.radius) ? bs.radius : 60;
  var D = Math.max(360, R * 4.4);
  st.flyTo({ x: tgt.x + dir.x * D, y: tgt.y + dir.y * D, z: tgt.z + dir.z * D }, tgt);
  st.controls.autoRotate = false;
}

/* ================= v13: 入场动画 / 选中信息卡 / 巡航 ================= */
// 入场: 设备按层从下往上"长"出来(核心先出现), 然后链路从两端往中间生长, 最后流量粒子出现
function tgIntroStart(c) {
  if (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) { return; }
  var items = [], now = performance.now();
  Object.keys(c.meshes).forEach(function (id, i) {
    var g = c.meshes[id], n = g.userData.node;
    var delay = (n.ext ? 4 : Math.min(n.layer || 0, 3)) * 220 + (i % 12) * 28;
    items.push({ g: g, delay: delay, dur: 520 });
    g.scale.setScalar(0.0001);
  });
  c.linkObjs.forEach(function (o) {
    var geos = [];
    o.traverse(function (x) { if (x.geometry && x.geometry.index && !x.isSprite) { geos.push(x.geometry); x.geometry.setDrawRange(0, 0); } else if (x.geometry && x.isLine) { geos.push(x.geometry); x.geometry.setDrawRange(0, 0); } });
    items.push({ link: o, geos: geos, delay: 380 + (o.userData.layerDelay || 0) * 220, dur: 700 });
  });
  if (c.particles) { c.particles.userData.introHidden = true; c.particles.visible = false; }
  c.intro = { t0: now, items: items, end: now + 2600 };
}
function tgIntroStep(c) {
  var it = c.intro;
  if (!it) { return false; }
  var now = performance.now(), done = true;
  it.items.forEach(function (x) {
    var k = Math.max(0, Math.min(1, (now - it.t0 - x.delay) / x.dur));
    if (k < 1) { done = false; }
    if (x.g) {
      var s = x.g.userData.baseScale * glEaseBack(k);
      x.g.scale.setScalar(Math.max(0.0001, s));
      x.g.userData.curScale = x.g.scale.x;
    } else {
      x.geos.forEach(function (g) {
        var full = g.index ? g.index.count : (g.attributes.position ? g.attributes.position.count : 0);
        // 管道的索引按 tubularSegments 顺序排列, 截取前面一段就是从 A 端往 B 端"生长"
        g.setDrawRange(0, k >= 1 ? Infinity : Math.floor(full * glEaseOut(k) / 6) * 6);
      });
    }
  });
  if (done || now > it.end + 1500) {
    it.items.forEach(function (x) {
      if (x.g) { x.g.scale.setScalar(x.g.userData.baseScale); x.g.userData.curScale = x.g.userData.baseScale; } else { x.geos.forEach(function (g) { g.setDrawRange(0, Infinity); }); }
    });
    if (c.particles && c.particles.userData.introHidden) { c.particles.userData.introHidden = false; c.particles.visible = c.showParticles; }
    c.intro = null;
  }
  return true;
}
// 悬停 / 选中时设备平滑放大一点, 松开平滑回去
function tgEaseScales(c, dt) {
  if (c.intro) { return false; }
  var moving = false;
  Object.keys(c.meshes).forEach(function (id) {
    var g = c.meshes[id], n = g.userData.node, base = g.userData.baseScale;
    if (base === undefined) { return; }
    var target = base * ((c.hover === n || c.sel === n) && !n.ext ? 1.12 : 1) * (c.search && tgMatch(c, n) ? 1.2 : 1);
    var cur = g.userData.curScale || base;
    if (Math.abs(cur - target) < base * 0.002) { if (cur !== target) { g.scale.setScalar(target); g.userData.curScale = target; } return; }
    cur += (target - cur) * Math.min(1, dt * 12);
    g.scale.setScalar(cur); g.userData.curScale = cur; moving = true;
  });
  return moving;
}

// 选中一台设备: 高亮框 + 镜头飞过去 + 信息卡; n = null 取消选中
function tgSelect(c, n, fly) {
  c.sel = n || null;
  c.selBox.fit(n ? c.meshes[n.id] : null, 10);
  c.hovBox.visible = false;
  if (!n) { tgCardHide(c); c.st.poke(); return; }
  if (fly) { tgFlyTo(c, n); }
  tgCardShow(c, n);
  c.st.poke();
}
function tgCardHost(c) { return c.opts.cardHost || c.box.parentNode; }
function tgCardHide(c) { var el = tgCardHost(c) && tgCardHost(c).querySelector('.tg-card'); if (el) { el.remove(); } }
function tgCardShow(c, n) {
  var host = tgCardHost(c);
  if (!host) { return; }
  var el = host.querySelector('.tg-card');
  if (!el) {
    el = document.createElement('div');
    el.className = 'tg-card fade-in';
    ['pointerdown', 'wheel', 'dblclick'].forEach(function (t) { el.addEventListener(t, function (e) { e.stopPropagation(); }); });
    host.appendChild(el);
  }
  var links = c.links.filter(function (lk) { return !lk.ext && (lk.a === n || lk.b === n); });
  var ext = c.nodes.filter(function (x) { return x.ext && x.parent === n.id; }).length;
  var bar = function (v) { var col = v >= 85 ? '#ef4444' : v >= 60 ? '#eab308' : '#22c55e'; return '<i class="tgc-bar"><b style="width:' + Math.min(100, v || 0) + '%;background:' + col + '"></b></i>'; };
  var h = '<span class="x" title="取消选中 (Esc)">×</span>';
  if (n.man) {
    var m = n.mdata || {};
    h += '<h4>' + (TP_KIND_ICON[m.kind] || '◆') + ' ' + esc(n.name) + '</h4><div class="sub">手工节点 · ' + esc(TP_KIND_NAME[m.kind] || '') + (m.ip ? ' · ' + esc(m.ip) : '') + '</div>' + (m.note ? '<div class="note">📝 ' + esc(m.note) + '</div>' : '');
  } else {
    var d = n.dev || {}, f = d.form || {};
    h += '<h4><i class="sdot" style="background:' + (!d.alive ? '#64748b' : d.open_alerts ? '#ef4444' : '#22c55e') + '"></i>' + esc(n.name) + '</h4>' +
      '<div class="sub">' + esc(n.id) + ' · ' + esc(pnlBrand(d.brand)[0]) + (d.model ? ' ' + esc(d.model) : '') + ' · ' + esc(T3_LAYER_NAMES[Math.min(n.layer, 3)] || '') + '</div>' +
      (d.note ? '<div class="note">📝 ' + esc(d.note) + '</div>' : '') +
      '<div class="tgc-kv"><span>状态</span><b style="color:' + (d.alive ? '#4ade80' : '#f87171') + '">' + (d.alive ? '在线' : '离线') + (d.open_alerts ? ' · <span style="color:#fca5a5">' + d.open_alerts + ' 条告警</span>' : '') + '</b>' +
      (d.alive ? '<span>CPU</span><div>' + bar(d.cpu_usage) + (d.cpu_usage || 0) + '%</div><span>内存</span><div>' + bar(d.mem_usage) + (d.mem_usage > 0 ? d.mem_usage + '%' : '-') + '</div>' : '') +
      '<span>形态</span><b>' + esc(glFormText(f) || '-') + '</b><span>运行</span><b>' + esc(d.uptime_str || '-') + '</b></div>';
  }
  h += '<div class="tgc-links"><div class="sub">链路 ' + links.length + ' 条' + (ext ? ' · 未监控邻居 ' + ext + ' 个' : '') + '</div>' + links.slice(0, 6).map(function (lk) {
    var o = lk.a === n ? lk.b : lk.a, l = lk.l, u = tgLinkUtil(l);
    var port = tpOrient(l, n.id).aPort || '';
    return '<div class="tgc-l" data-go="' + esc(o.id) + '"><i style="background:#' + ('000000' + tgLinkColor(l).toString(16)).slice(-6) + '"></i>' + esc(truncateLabel(port, 22)) + ' → <b>' + esc(o.name) + '</b>' +
      (l._bundle ? ' ×' + l._bundle.length : '') + '<em>' + (u > 0 ? (u * 100).toFixed(0) + '%' : '') + '</em></div>';
  }).join('') + (links.length > 6 ? '<div class="sub">…还有 ' + (links.length - 6) + ' 条</div>' : '') + '</div>' +
    '<div class="tgc-btns">' + (!n.man ? '<button class="pri" data-a="open">📋 设备详情</button><button data-a="panel">🔌 面板</button>' : '') + '<button data-a="focus">🎯 只看相邻</button></div>';
  el.innerHTML = h;
  el.onclick = function (e) {
    var a = e.target.closest('[data-a]'), go = e.target.closest('[data-go]');
    if (e.target.closest('.x')) { tgSelect(c, null); return; }
    if (go) { var nn = c.lay.byId[go.getAttribute('data-go')]; if (nn) { tgSelect(c, nn, true); } return; }
    if (!a) { return; }
    var act = a.getAttribute('data-a');
    if (act === 'open' && c.opts.onOpen) { c.opts.onOpen(n.id); }
    if (act === 'panel' && c.opts.onOpen) { c.opts.onOpen(n.id, 9); }
    if (act === 'focus') { c.focus = c.focus === n ? null : n; c.hover = c.focus; tgApplyHighlight(c); a.classList.toggle('on', !!c.focus); }
  };
}

// 巡航: 镜头依次飞到每台设备(有告警的、离线的排在前面, 然后按层级), 每台停几秒并显示信息卡; 动一下鼠标就停
function tgTourStart(c, onStop) {
  var list = c.nodes.filter(function (n) { return !n.ext; });
  list.sort(function (a, b) {
    var ra = a.dev && (!a.dev.alive || a.dev.open_alerts) ? 0 : 1, rb = b.dev && (!b.dev.alive || b.dev.open_alerts) ? 0 : 1;
    return ra - rb || (a.layer || 0) - (b.layer || 0) || cmpNatural(a.name, b.name);
  });
  if (!list.length) { return; }
  tgTourStop(c);
  c.tour = { list: list, i: 0, onStop: onStop };
  c.st.controls.autoRotate = false;
  var step = function () {
    if (!c.tour || !c.st) { return; }
    var n = c.tour.list[c.tour.i % c.tour.list.length];
    c.tour.i++;
    tgSelect(c, n, true);
    c.tour.timer = setTimeout(step, 6500);
  };
  step();
}
function tgTourStop(c) {
  if (!c.tour) { return; }
  clearTimeout(c.tour.timer);
  var cb = c.tour.onStop;
  c.tour = null;
  if (cb) { cb(); }
}

// 拓扑窗口里 3D 视图的浮动工具条 + 快捷键
function tgMountBar(c, host) {
  var bar = glOverlayBar(host);
  bar.innerHTML = '';
  var tourBtn = glBarButton(bar, '🎬 <span>巡航</span>', '镜头依次飞到每台设备(有告警的优先), 每台停几秒; 快捷键 T', function () {
    if (c.tour) { tgTourStop(c); return; }
    tgTourStart(c, function () { tourBtn.classList.remove('on'); tourBtn.innerHTML = '🎬 <span>巡航</span>'; });
    tourBtn.classList.add('on'); tourBtn.innerHTML = '⏹ <span>停止巡航</span>';
  });
  glBarButton(bar, '⟲ <span>视角</span>', '回到全景视角; 快捷键 R', function () { tgSelect(c, null); tgResetCamera(c, false); });
  glQualitySelect(bar, function () { topoRemount(); }, c.st);
  fsButton(host, bar, { cls: 'fs-btn glass' });
  fsHotkeyZone(host);
  c.tourBtn = tourBtn;
  if (!TGE.keys) {
    TGE.keys = true;
    document.addEventListener('keydown', function (e) {
      if (!tgActive() || byId('topoModal').style.display === 'none') { return; }
      var tg = e.target;
      if (tg && (tg.tagName === 'INPUT' || tg.tagName === 'TEXTAREA' || tg.tagName === 'SELECT')) { return; }
      if (e.key === 'r' || e.key === 'R') { tgSelect(TG, null); tgResetCamera(TG, false); }
      else if (e.key === 't' || e.key === 'T') { if (TG.tourBtn) { TG.tourBtn.click(); } }
      else if (e.key === ' ') { e.preventDefault(); T3.auto = !T3.auto; t3SetAuto(T3.auto); if (byId('t3Auto')) { byId('t3Auto').checked = T3.auto; } }
      else if (e.key === 'Escape' && (TG.sel || TG.tour)) { e.stopPropagation(); tgTourStop(TG); tgSelect(TG, null); }
    }, true);
  }
}
