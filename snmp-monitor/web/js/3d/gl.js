/*
 * three.js 公共部分: 按需加载内置的 three.js(见 static.go)、舞台(渲染器/相机/轨道控制/
 * 自适应尺寸/后台暂停/释放)、文字标签、交换机 3D 模型(前面板按真实端口状态画灯)。
 * 3D 拓扑(topo3d_gl.go)、3D 机房(dc3d.go)、大屏(bigscreen.go)共用。
 *
 * 不支持 WebGL 的浏览器: 3D 拓扑自动退回 topo3d.go 的 Canvas 版本, 机房/大屏给出提示。
 */
/* ================= three.js 公共 ================= */
var GL = { THREE: null, OC: null, loading: null, ok: null };
function glSupported() {
  if (GL.ok !== null) { return GL.ok; }
  try {
    var c = document.createElement('canvas');
    GL.ok = !!(window.WebGLRenderingContext && (c.getContext('webgl2') || c.getContext('webgl') || c.getContext('experimental-webgl')));
  } catch (e) { GL.ok = false; }
  return GL.ok;
}
function glLoad() {
  if (GL.THREE) { return Promise.resolve(GL); }
  if (!GL.loading) {
    GL.loading = Promise.all([import('/static/three.module.min.js'), import('/static/OrbitControls.js')]).then(function (m) {
      GL.THREE = m[0]; GL.OC = m[1].OrbitControls;
      return GL;
    }).catch(function (e) { GL.loading = null; throw e; });
  }
  return GL.loading;
}
function glIsDarkTheme() {
  var bg = getComputedStyle(document.body).getPropertyValue('--bg').trim();
  var m = bg.match(/^#([0-9a-f]{6})$/i);
  if (!m) { return true; }
  var n = parseInt(m[1], 16);
  return (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255 < 0.5;
}

// 软件渲染(SwiftShader / llvmpipe / 微软基本显示): 泛光这类全屏效果会慢到不能用
function glIsSoftware(renderer) {
  try {
    var gl = renderer.getContext(), ext = gl.getExtension('WEBGL_debug_renderer_info');
    var name = ext ? gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER);
    return /swiftshader|llvmpipe|softpipe|basic render|software/i.test(String(name || ''));
  } catch (e) { return false; }
}
// glStage 在 container 里建一个 3D 舞台。
// opts: { fov, near, far, transparent, exposure, shadows, bloom: { strength, threshold } | false, env: true }
//
// v13:
//   - 泛光(Bloom): 屏幕上发光的东西(指示灯、流量光带、告警光环、机柜灯条)真的"亮"起来, 见 gl-fx.js;
//   - 环境反射(env): 金属机柜 / 机箱有真实的高光反射, 不再是一块块黑色;
//   - 画质自适应: 画质选"自动"时, 连续掉帧会先关泛光、再降分辨率, 老电脑 / 远程桌面也不卡;
//   - 镜头飞行: 缓入缓出 + 远距离时先拉高再落下, 不会"平移穿模";
//   - 标签远近淡出(st.lod): 拉远看全局时次要的标签自动隐藏, 不再糊成一片。
function glStage(container, opts) {
  opts = opts || {};
  var THREE = GL.THREE;
  var q = glQuality();
  var st = { THREE: THREE, container: container, frames: [], raf: 0, alive: true, t: 0, last: 0, need: 3, hoverObjs: [], lod: [], opts: opts, perf: { n: 0, sum: 0, slow: 0 } };
  var renderer = new THREE.WebGLRenderer({ antialias: true, alpha: !!opts.transparent, powerPreference: 'high-performance', preserveDrawingBuffer: !!opts.preserve });
  // 画质"自动"遇到软件渲染(虚拟机 / 远程桌面 / 没装显卡驱动): 直接按低画质, 不等掉帧再降
  st.soft = glIsSoftware(renderer);
  if (q === 'auto' && st.soft) { q = 'low'; st.degraded = '检测到软件渲染(没有显卡加速), 已用低画质'; }
  st.dpr = function () { return Math.min(q === 'low' ? 1 : 1.5, window.devicePixelRatio || 1) * (st.lowRes ? 0.75 : 1); };
  renderer.setPixelRatio(st.dpr());
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = opts.exposure || 1.05;
  if (opts.shadows && q !== 'low') { renderer.shadowMap.enabled = true; renderer.shadowMap.type = THREE.PCFSoftShadowMap; }
  renderer.domElement.style.display = 'block';
  renderer.domElement.style.width = '100%';
  renderer.domElement.style.height = '100%';
  renderer.domElement.style.outline = 'none';
  container.appendChild(renderer.domElement);
  var scene = new THREE.Scene();
  var camera = new THREE.PerspectiveCamera(opts.fov || 45, 1, opts.near || 1, opts.far || 50000);
  var controls = new GL.OC(camera, renderer.domElement);
  controls.enableDamping = true;
  controls.dampingFactor = 0.08;
  controls.screenSpacePanning = true;
  controls.zoomSpeed = 1.1;
  st.renderer = renderer; st.scene = scene; st.camera = camera; st.controls = controls;
  st.raycaster = new THREE.Raycaster();
  st.mouse = new THREE.Vector2();
  if (opts.env !== false && !opts.transparent) { scene.environment = glRoomEnv(renderer); }
  // 泛光: 透明背景(大屏叠在别的画面上)不做, 低画质不做, WebGL1 不支持
  if (opts.bloom !== false && !opts.transparent && q !== 'low' && q !== 'mid') { st.fx = glBloomPass(st, opts.bloom || {}); }
  controls.addEventListener('change', function () { st.need = 2; st.camMoved = true; });
  var resize = function () {
    var w = container.clientWidth, h = container.clientHeight;
    if (!w || !h) { return; }
    renderer.setPixelRatio(st.dpr());
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
    if (st.fx) { st.fx.setSize(w * renderer.getPixelRatio(), h * renderer.getPixelRatio()); }
    st.need = 2; st.camMoved = true;
  };
  st.resize = resize;
  if (window.ResizeObserver) { st.ro = new ResizeObserver(resize); st.ro.observe(container); } else { window.addEventListener('resize', resize); }
  resize();
  st.render = function () {
    if (st.fx && st.fx.enabled) { st.fx.render(); } else { renderer.setRenderTarget(null); renderer.render(scene, camera); }
  };
  // 画质"自动"时的降级: 连续 3 秒平均一帧超过 45ms → 先关泛光, 还慢就降分辨率
  var governor = function (ms) {
    if (glQuality() !== 'auto') { return; }
    var p = st.perf;
    p.n++; p.sum += ms;
    if (p.n < 30) { return; }
    var avg = p.sum / p.n;
    p.n = 0; p.sum = 0;
    if (avg < 45) { p.slow = 0; return; }
    if (++p.slow < 1) { return; }
    if (st.fx && st.fx.enabled) { st.fx.enabled = false; st.degraded = '已自动关闭泛光(显卡较慢)'; }
    else if (!st.lowRes) { st.lowRes = true; resize(); st.degraded = '已自动降低分辨率(显卡较慢)'; }
    if (st.onDegrade) { st.onDegrade(st.degraded); }
  };
  var loop = function (now) {
    st.raf = 0;
    if (!st.alive) { return; }
    st.raf = requestAnimationFrame(loop);
    // 窗口隐藏 / 切到后台 / 容器不可见时不画, 不占 CPU/GPU
    if (document.hidden || !container.offsetParent) { st.last = now; return; }
    var dt = st.last ? Math.min(0.1, (now - st.last) / 1000) : 0.016;
    st.last = now; st.t += dt;
    var animated = false, moving = st.need > 0 || !!st.fly;
    st.frames.forEach(function (fn) { if (fn(dt, st.t)) { animated = true; } });
    if (controls.update()) { moving = true; }
    // 只有粒子流动/灯闪烁/自动旋转这类"氛围动画"时限到 30 帧: 肉眼几乎看不出差别,
    // 集显/远程桌面上 GPU 占用直接减半; 拖动/缩放/飞行时仍然满帧。
    if (!moving && (animated || controls.autoRotate) && now - (st.lastDraw || 0) < 32) { return; }
    if (moving || animated || controls.autoRotate) {
      if (st.camMoved || controls.autoRotate || st.fly) { glLodUpdate(st); st.camMoved = false; }
      st.render();
      // 用相邻两次绘制的间隔衡量(GPU 慢的时候 rAF 间隔会被拉长); 中间闲置过的不算
      if (st.lastDraw && now - st.lastDraw < 500) { governor(now - st.lastDraw); }
      st.lastDraw = now;
      if (st.need > 0) { st.need--; }
    }
  };
  st.start = function () { if (!st.raf && st.alive) { st.last = 0; st.raf = requestAnimationFrame(loop); } };
  st.poke = function () { st.need = 2; };
  // 取鼠标下的第一个对象(只在 objs 里找, 沿 parent 找到带 userData.pick 的那一层)
  st.pick = function (e, objs) {
    var r = renderer.domElement.getBoundingClientRect();
    st.mouse.x = ((e.clientX - r.left) / r.width) * 2 - 1;
    st.mouse.y = -((e.clientY - r.top) / r.height) * 2 + 1;
    st.raycaster.setFromCamera(st.mouse, camera);
    var hits = st.raycaster.intersectObjects(objs || scene.children, true);
    for (var i = 0; i < hits.length; i++) {
      var o = hits[i].object;
      while (o && !(o.userData && o.userData.pick)) { o = o.parent; }
      if (o && o.visible !== false) { return { obj: o, point: hits[i].point, hit: hits[i] }; }
    }
    return null;
  };
  // 平滑飞到某个视角: 三次缓动; 飞得远的时候中途先拉高一点, 像无人机航拍一样越过障碍
  st.flyTo = function (pos, target, ms, done) {
    var p0 = camera.position.clone(), t0 = controls.target.clone(), start = performance.now();
    var P = new THREE.Vector3(pos.x, pos.y, pos.z), T = new THREE.Vector3(target.x, target.y, target.z);
    var dist = p0.distanceTo(P), lift = Math.min(dist * 0.25, 900);
    var dur = ms || Math.max(700, Math.min(1600, 500 + dist * 0.35));
    if (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) { dur = 1; }
    st.fly = function () {
      var k = Math.min(1, (performance.now() - start) / dur);
      var e = k < 0.5 ? 4 * k * k * k : 1 - Math.pow(-2 * k + 2, 3) / 2;
      camera.position.lerpVectors(p0, P, e);
      camera.position.y += Math.sin(Math.PI * e) * lift;
      controls.target.lerpVectors(t0, T, e);
      st.camMoved = true;
      if (k >= 1) { st.fly = null; if (done) { done(); } }
      return true;
    };
  };
  st.frames.push(function () { return st.fly ? st.fly() : false; });
  st.dispose = function () {
    st.alive = false;
    if (st.raf) { cancelAnimationFrame(st.raf); st.raf = 0; }
    if (st.ro) { st.ro.disconnect(); } else { window.removeEventListener('resize', resize); }
    controls.dispose();
    if (st.fx) { st.fx.dispose(); }
    glDisposeTree(scene);
    renderer.dispose();
    if (renderer.forceContextLoss) { try { renderer.forceContextLoss(); } catch (e) {} }
    if (renderer.domElement.parentNode) { renderer.domElement.parentNode.removeChild(renderer.domElement); }
  };
  return st;
}
function glDisposeTree(obj) {
  obj.traverse(function (o) {
    if (o.geometry && !o.geometry.userData.shared) { o.geometry.dispose(); }
    var mats = o.material ? (Array.isArray(o.material) ? o.material : [o.material]) : [];
    mats.forEach(function (m) {
      if (m.userData && m.userData.shared) { return; }
      ['map', 'emissiveMap', 'alphaMap'].forEach(function (k) { if (m[k] && !(m[k].userData && m[k].userData.shared)) { m[k].dispose(); } });
      m.dispose();
    });
  });
}
function glClear(group) {
  while (group.children.length) {
    var c = group.children[0];
    group.remove(c);
    glDisposeTree(c);
  }
}

/* ---------- 文字标签(Canvas 贴图 Sprite, 始终面向相机) ---------- */
// opts: { size(世界单位高度), color, bg, border, sub, subColor, bold, maxWidth(字符) }
function glLabel(text, opts) {
  var THREE = GL.THREE;
  opts = opts || {};
  var fs = 44, pad = 16, sub = opts.sub || '';
  var cv = document.createElement('canvas'), ctx = cv.getContext('2d');
  var font = (opts.bold === false ? '' : '600 ') + fs + 'px "Microsoft YaHei","PingFang SC","Segoe UI",sans-serif';
  var sfont = Math.round(fs * 0.68) + 'px "Microsoft YaHei","PingFang SC",Consolas,sans-serif';
  text = truncateLabel(text, opts.maxWidth || 22);
  sub = truncateLabel(sub, (opts.maxWidth || 22) + 8);
  ctx.font = font;
  var w = ctx.measureText(text).width;
  if (sub) { ctx.font = sfont; w = Math.max(w, ctx.measureText(sub).width); }
  cv.width = Math.ceil(w + pad * 2);
  cv.height = Math.ceil(fs * (sub ? 1.9 : 1.25) + pad);
  ctx = cv.getContext('2d');
  if (opts.bg) {
    ctx.fillStyle = opts.bg;
    glRoundRect(ctx, 2, 2, cv.width - 4, cv.height - 4, 14); ctx.fill();
    if (opts.border) { ctx.strokeStyle = opts.border; ctx.lineWidth = 3; ctx.stroke(); }
  }
  ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
  ctx.font = font; ctx.fillStyle = opts.color || '#e2e8f0';
  if (!opts.bg) { ctx.shadowColor = 'rgba(0,0,0,.85)'; ctx.shadowBlur = 8; }
  ctx.fillText(text, cv.width / 2, pad / 2 + fs * 0.62);
  if (sub) { ctx.font = sfont; ctx.fillStyle = opts.subColor || '#94a3b8'; ctx.fillText(sub, cv.width / 2, pad / 2 + fs * 1.42); }
  var tex = new THREE.CanvasTexture(cv);
  tex.colorSpace = THREE.SRGBColorSpace;
  tex.anisotropy = 4;
  // opts.screen: 固定屏幕大小(占画面高度的比例), 拉远看全局时标签也看得清
  var mat = new THREE.SpriteMaterial({ map: tex, transparent: true, depthWrite: false, depthTest: opts.depthTest !== false, sizeAttenuation: !opts.screen });
  var sp = new THREE.Sprite(mat);
  var h = opts.screen || opts.size || 10;
  sp.scale.set(h * cv.width / cv.height, h, 1);
  sp.renderOrder = opts.renderOrder || 10;
  sp.userData.label = true;
  return sp;
}
function glRoundRect(ctx, x, y, w, h, r) {
  ctx.beginPath();
  ctx.moveTo(x + r, y); ctx.lineTo(x + w - r, y); ctx.quadraticCurveTo(x + w, y, x + w, y + r);
  ctx.lineTo(x + w, y + h - r); ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  ctx.lineTo(x + r, y + h); ctx.quadraticCurveTo(x, y + h, x, y + h - r);
  ctx.lineTo(x, y + r); ctx.quadraticCurveTo(x, y, x + r, y); ctx.closePath();
}

/* ---------- 交换机前面板贴图 ---------- */
// 按真实端口画: 电口 1 上 2 下, 光口画成 SFP 笼, 灯色 = 状态(绿 UP / 橙 管理DOWN / 暗 DOWN / 红 有问题)。
// rows: pnlLayout() 的结果里属于这台(或这个堆叠成员/板卡)的行; kind: box / chassis
function glPortColor(p, offline) {
  if (offline) { return '#1f2937'; }
  if (p.s === 'UP') {
    if (p.e > 0 || p.dc > 0 || (p.d === '半双工')) { return '#ef4444'; }
    if (p.rx !== null && p.rx !== undefined && p.rx <= uiPref('weak_optical_dbm', -20)) { return '#fb923c'; }
    var u = Math.max(p.iu || 0, p.ou || 0);
    return u >= uiPref('busy_util_percent', 70) ? '#facc15' : '#22c55e';
  }
  if (p.s === 'ADMIN-DOWN') { return '#b45309'; }
  return '#273244';
}
function glDrawPanel(ctx, W, H, dev, rows, o) {
  o = o || {};
  var br = pnlBrand(dev.brand);
  var off = !dev.alive;
  // 机箱面板底色 + 高光
  var g = ctx.createLinearGradient(0, 0, 0, H);
  g.addColorStop(0, off ? '#2a2d33' : '#3a4150'); g.addColorStop(0.5, off ? '#1f2227' : '#262c37'); g.addColorStop(1, off ? '#17191d' : '#1a1f28');
  ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
  ctx.fillStyle = 'rgba(255,255,255,.08)'; ctx.fillRect(0, 0, W, 2);
  // 两侧挂耳
  ctx.fillStyle = '#4b5563'; ctx.fillRect(0, 0, 18, H); ctx.fillRect(W - 18, 0, 18, H);
  ctx.fillStyle = '#111827';
  for (var yy = 10; yy < H; yy += 44) { ctx.beginPath(); ctx.arc(9, yy + 6, 3.5, 0, 7); ctx.arc(W - 9, yy + 6, 3.5, 0, 7); ctx.fill(); }
  var left = 26, right = W - 26;
  // 品牌 / 名字 / 状态灯
  var infoW = Math.min(230, W * 0.2);
  ctx.fillStyle = br[1]; ctx.font = '700 ' + Math.min(26, H * 0.32) + 'px Arial, sans-serif'; ctx.textBaseline = 'top';
  ctx.fillText(br[0], left, 8);
  ctx.fillStyle = '#cbd5e1'; ctx.font = Math.min(19, H * 0.22) + 'px "Microsoft YaHei", sans-serif';
  ctx.fillText(truncateLabel(o.title || dev.name || dev.ip, 16), left, 8 + Math.min(30, H * 0.36));
  var leds = [['SYS', off ? '#ef4444' : '#22c55e'], ['PWR', pnlHwLed(dev.power) === 'ok' ? '#22c55e' : (pnlHwLed(dev.power) ? '#ef4444' : '#374151')],
    ['FAN', pnlHwLed(dev.fan) === 'ok' ? '#22c55e' : (pnlHwLed(dev.fan) ? '#ef4444' : '#374151')], ['ALM', dev.alerts > 0 ? '#ef4444' : '#374151']];
  if (H >= 60) {
    ctx.font = '12px Arial'; ctx.textBaseline = 'middle';
    leds.forEach(function (l, i) {
      var lx = left + i * 50, ly = H - 16;
      ctx.fillStyle = l[1]; ctx.shadowColor = l[1]; ctx.shadowBlur = l[1] === '#374151' ? 0 : 8;
      ctx.beginPath(); ctx.arc(lx + 5, ly, 4.5, 0, 7); ctx.fill(); ctx.shadowBlur = 0;
      ctx.fillStyle = '#9ca3af'; ctx.fillText(l[0], lx + 13, ly);
    });
  }
  var x0 = left + infoW, avail = right - x0;
  if (!rows || !rows.length) {
    ctx.fillStyle = '#64748b'; ctx.font = '18px "Microsoft YaHei"'; ctx.textBaseline = 'middle';
    ctx.fillText(off ? '离线' : '无端口数据', x0 + 10, H / 2);
    return;
  }
  // 竖插板卡的框式(见 form.go chassisOrient): 板卡左右并排, 端口从上往下, 跟 2D 面板一致
  if (dev.form && dev.form.kind === 'chassis' && dev.form.orient === 'v') { glDrawVCards(ctx, x0, avail, H, dev, off, o); return; }
  // 每一行(板卡)一条横带
  var bandH = H / rows.length;
  rows.forEach(function (row, ri) {
    var by = ri * bandH;
    if (rows.length > 1) {
      ctx.fillStyle = ri % 2 ? 'rgba(255,255,255,.025)' : 'rgba(0,0,0,.12)';
      ctx.fillRect(x0 - 8, by + 2, avail + 8, bandH - 4);
      if (o.slotLabels) { ctx.fillStyle = '#6b7280'; ctx.font = '12px Consolas'; ctx.textBaseline = 'top'; ctx.fillText('SLOT ' + row.key, x0 - 4, by + 4); }
    }
    var groups = row.groups || [];
    var total = 0;
    groups.forEach(function (gp) {
      var n = gp.ports.length, r2 = gp.kind === 'mgmt' ? 1 : (gp.kind === 'sfp' && n <= 2 ? 1 : 2);
      gp._cols = Math.ceil(n / r2); gp._rows = r2; total += gp._cols + 1.2;
    });
    var cell = Math.min(34, avail / Math.max(total, 1));
    var ph = Math.min(bandH * 0.36, cell * 0.95);
    var gx = x0;
    groups.forEach(function (gp) {
      var cols = gp._cols, rr = gp._rows;
      var gyTop = by + (bandH - rr * ph - (rr - 1) * 3) / 2;
      gp.ports.forEach(function (x, i) {
        var col = rr === 2 ? Math.floor(i / 2) : i, row2 = rr === 2 ? i % 2 : 0;
        var px = gx + col * cell, py = gyTop + row2 * (ph + 3);
        var pw = cell * 0.82;
        var c = glPortColor(x.p, off);
        if (gp.kind === 'sfp') {
          ctx.fillStyle = '#0b0f14'; ctx.fillRect(px, py, pw, ph);
          ctx.strokeStyle = '#9ca3af'; ctx.lineWidth = 1.5; ctx.strokeRect(px + 0.5, py + 0.5, pw - 1, ph - 1);
          if (x.p.s === 'UP' || (x.p.rx !== null && x.p.rx !== undefined)) {
            var sp = x.p.sp || 0;
            ctx.fillStyle = sp >= 40000 ? '#f472b6' : sp >= 25000 ? '#a78bfa' : sp >= 10000 ? '#60a5fa' : '#d6c7a1';
            ctx.fillRect(px + pw * 0.2, py + ph * 0.3, pw * 0.6, ph * 0.4);
          }
          ctx.fillStyle = c; ctx.fillRect(px + pw * 0.15, py + 2, pw * 0.7, Math.max(2, ph * 0.12));
        } else {
          ctx.fillStyle = '#0a0d12'; ctx.fillRect(px, py, pw, ph);
          ctx.fillStyle = '#1f2937'; ctx.fillRect(px + pw * 0.25, py + ph * 0.55, pw * 0.5, ph * 0.3);
          ctx.fillStyle = c;
          ctx.fillRect(px + 2, py + 2, pw * 0.3, Math.max(2, ph * 0.18));
          if (x.p.s === 'UP' && ((x.p.im || 0) + (x.p.om || 0)) > 0.05) { ctx.fillStyle = '#fbbf24'; ctx.fillRect(px + pw * 0.66, py + 2, pw * 0.3, Math.max(2, ph * 0.18)); }
        }
        if (o.highlight && o.highlight(x.p)) { ctx.strokeStyle = '#38bdf8'; ctx.lineWidth = 3; ctx.strokeRect(px - 2, py - 2, pw + 4, ph + 4); }
      });
      gx += (cols + 1.2) * cell;
    });
  });
}
function glDrawVCards(ctx, x0, avail, H, dev, off, o) {
  var cards = pnlChassisCards(dev), n = Math.max(1, cards.length), cw = avail / n, top = 8, bh = H - 16;
  cards.forEach(function (c, ci) {
    var cx = x0 + ci * cw;
    ctx.fillStyle = c.empty ? '#16181d' : c.mpu ? '#27354a' : '#2a303b';
    ctx.fillRect(cx + 2, top, cw - 4, bh);
    ctx.strokeStyle = c.empty ? '#262a31' : '#4b5563'; ctx.lineWidth = 1.5; ctx.strokeRect(cx + 2.5, top + 0.5, cw - 5, bh - 1);
    ctx.fillStyle = c.mpu ? '#fbbf24' : c.empty ? '#4b5563' : '#cbd5e1';
    ctx.font = '700 ' + Math.max(9, Math.min(14, cw * 0.18)) + 'px Consolas'; ctx.textBaseline = 'top'; ctx.textAlign = 'center';
    ctx.fillText(c.mpu ? 'MPU' : c.key, cx + cw / 2, top + 4);
    ctx.textAlign = 'left';
    var cnt = c.ports.length; if (!cnt) { return; }
    var areaW = cw - 12, areaH = bh - 26, nc = 1, cell = 0;
    for (nc = 1; nc <= 8; nc++) { cell = Math.min(areaW / nc, 28); if (Math.ceil(cnt / nc) * cell <= areaH) { break; } }
    cell = Math.min(areaW / nc, areaH / Math.ceil(cnt / nc));
    c.ports.forEach(function (x, i) {
      var px = cx + 6 + (i % nc) * cell + (areaW - nc * cell) / 2, py = top + 22 + Math.floor(i / nc) * cell, pw = cell * 0.82;
      ctx.fillStyle = '#0a0d12'; ctx.fillRect(px, py, pw, pw);
      if (x.kind === 'sfp') { ctx.strokeStyle = '#9ca3af'; ctx.lineWidth = 1; ctx.strokeRect(px + 0.5, py + 0.5, pw - 1, pw - 1); }
      ctx.fillStyle = glPortColor(x.p, off); ctx.fillRect(px + 2, py + 2, pw * 0.35, Math.max(2, pw * 0.2));
      if (o && o.highlight && o.highlight(x.p)) { ctx.strokeStyle = '#38bdf8'; ctx.lineWidth = 3; ctx.strokeRect(px - 2, py - 2, pw + 4, pw + 4); }
    });
  });
}
// 生成(或刷新)一块前面板贴图。cache: 同一个 key 复用 canvas, 只重画
// ratio = 面板实际的 高 / 宽: 贴图按这个比例生成, 端口不会被横向压扁(以前一律 1024×64/U, 1U 面板横向被压了三分之一);
// 逻辑尺寸宽 1024, 实际像素 ×1.5 更清晰
function glPanelTexture(key, dev, rows, uHeight, o, ratio) {
  var THREE = GL.THREE;
  GL.panels = GL.panels || {};
  var W = 1024, H = ratio ? Math.max(40, Math.min(1400, Math.round(W * ratio))) : Math.max(64, Math.min(1024, Math.round(uHeight * 64)));
  var K = 1.5, ent = GL.panels[key];
  if (!ent || ent.cv.height !== Math.round(H * K)) {
    if (ent) { ent.tex.dispose(); }
    var cv = document.createElement('canvas'); cv.width = Math.round(W * K); cv.height = Math.round(H * K);
    var tex = new THREE.CanvasTexture(cv);
    tex.colorSpace = THREE.SRGBColorSpace; tex.anisotropy = 8;
    ent = GL.panels[key] = { cv: cv, tex: tex };
  }
  var ctx = ent.cv.getContext('2d');
  ctx.setTransform(K, 0, 0, K, 0, 0);
  glDrawPanel(ctx, W, H, dev, rows, o);
  ent.tex.needsUpdate = true;
  return ent.tex;
}
// 交换机背面: 左边两个电源模块(把手 + 状态灯 + 电源插座), 右边一排风扇框; 按高度缓存
function glBackTexture(ratio, chassis) {
  GL.swBack = GL.swBack || {};
  var W = 512, H = Math.max(24, Math.min(700, Math.round(W * ratio))), key = H + (chassis ? 'c' : '');
  if (GL.swBack[key]) { return GL.swBack[key]; }
  var THREE = GL.THREE, cv = document.createElement('canvas'); cv.width = W; cv.height = H;
  var ctx = cv.getContext('2d');
  ctx.fillStyle = '#1b1f27'; ctx.fillRect(0, 0, W, H);
  ctx.fillStyle = 'rgba(255,255,255,.05)'; ctx.fillRect(0, 0, W, 1.5);
  var rows = chassis ? Math.max(1, Math.floor(H / 60)) : 1, rh = H / rows;
  for (var r = 0; r < rows; r++) {
    var y = r * rh + rh * 0.12, h = rh * 0.76;
    [8, 70].forEach(function (x) {
      ctx.fillStyle = '#3f4756'; ctx.fillRect(x, y, 56, h); ctx.strokeStyle = '#6b7280'; ctx.lineWidth = 1.5; ctx.strokeRect(x + 0.75, y + 0.75, 54.5, h - 1.5);
      ctx.fillStyle = '#0b0e13'; ctx.fillRect(x + 6, y + h * 0.25, 18, h * 0.5);
      ctx.fillStyle = '#22c55e'; ctx.fillRect(x + 44, y + h * 0.2, 5, Math.max(2, h * 0.12));
      ctx.fillStyle = '#6b7280'; ctx.fillRect(x + 30, y + h * 0.35, 3, h * 0.3); ctx.fillRect(x + 36, y + h * 0.35, 3, h * 0.3);
    });
    for (var i = 0; i < 4; i++) {
      var fx = W - 12 - (i + 1) * (h + 10), cx = fx + h / 2, cy = y + h / 2;
      ctx.fillStyle = '#323946'; ctx.fillRect(fx, y, h, h);
      ctx.strokeStyle = '#8b95a5'; ctx.lineWidth = 1.4;
      for (var k = 1; k <= 3; k++) { ctx.beginPath(); ctx.arc(cx, cy, h * 0.14 * k, 0, 7); ctx.stroke(); }
      ctx.beginPath(); ctx.moveTo(fx + 2, cy); ctx.lineTo(fx + h - 2, cy); ctx.moveTo(cx, y + 2); ctx.lineTo(cx, y + h - 2); ctx.stroke();
    }
    ctx.fillStyle = '#0b0e13'; ctx.fillRect(150, y + h * 0.3, 26, h * 0.4); ctx.fillRect(184, y + h * 0.3, 26, h * 0.4);
  }
  var tex = new THREE.CanvasTexture(cv);
  tex.colorSpace = THREE.SRGBColorSpace; tex.anisotropy = 4; tex.userData.shared = true;
  GL.swBack[key] = tex;
  return tex;
}
function glDropPanelTextures() {
  Object.keys(GL.panels || {}).forEach(function (k) { GL.panels[k].tex.dispose(); });
  GL.panels = {};
}
// 按形态把端口行分到每个堆叠成员(stack)或整机(框式 / 盒式)
function glMemberRows(dev) {
  var layout = pnlLayout(dev.ports || []);
  var f = dev.form || { kind: 'box', members: 1, u_each: 1 };
  if (f.kind === 'stack') {
    var out = [], keys = f.keys && f.keys.length ? f.keys : layout.map(function (r) { return r.key; });
    for (var i = 0; i < f.members; i++) {
      var k = keys[i];
      out.push(layout.filter(function (r) { return r.key === k; }));
    }
    // 端口名成员号比堆叠台数少(比如手工指定了 2 台但只有一台的端口), 剩下的留空
    return out;
  }
  return [layout];
}

/* ---------- 交换机 3D 模型 ---------- */
// dev: /api/panels 里的一台(含 form/ports); s: 缩放 { w, uh, d } 世界单位
// 返回 Group, 原点在底面中心; userData = { dev, height, members:[mesh] }
function glSwitchModel(dev, s, o) {
  var THREE = GL.THREE;
  o = o || {};
  var f = dev.form || { kind: 'box', members: 1, u_each: 1 };
  var g = new THREE.Group();
  var off = !dev.alive;
  var rowsPer = glMemberRows(dev);
  var n = f.kind === 'stack' ? Math.max(1, f.members) : 1;
  var each = Math.max(1, f.u_each || 1);
  // o.gap / o.inset: 机柜里按 U 位严丝合缝地排(gap=0), 拓扑里堆叠成员之间留一点缝更好认
  var gap = f.kind === 'stack' ? (o.gap !== undefined ? o.gap : s.uh * 0.35) : 0;
  var y = 0;
  var side = new THREE.MeshStandardMaterial({ color: off ? 0x3a3d44 : 0x2d3440, metalness: 0.7, roughness: 0.38 });
  var top = new THREE.MeshStandardMaterial({ color: off ? 0x33363c : 0x3b4454, metalness: 0.75, roughness: 0.3 });
  var back = new THREE.MeshStandardMaterial({ color: 0x1b1f27, metalness: 0.4, roughness: 0.7 });
  g.userData.members = [];
  var pitched = o.inset !== undefined;
  // 从下往上摆, 成员 #1 在最上面(跟机柜里的习惯一致)
  for (var k = 0; k < n; k++) {
    var i = n - 1 - k;
    var h = each * s.uh - (pitched ? o.inset : (f.kind === 'stack' ? 0 : s.uh * 0.06));
    var ratio = h / s.w;
    var tex = glPanelTexture((o.keyPrefix || '') + dev.ip + '#' + i, dev, rowsPer[i] || [], each,
      { title: (dev.name || dev.ip) + (n > 1 ? ' #' + (i + 1) : ''), slotLabels: f.kind === 'chassis', highlight: o.highlight }, ratio);
    var front = new THREE.MeshStandardMaterial({ map: tex, emissiveMap: tex, emissive: 0xffffff, emissiveIntensity: off ? 0.15 : 0.55, metalness: 0.2, roughness: 0.55 });
    // 背面略带自发光: 机柜里很暗, 开了后门也要看得清电源灯 / 风扇
    var rearTex = glBackTexture(ratio, f.kind === 'chassis');
    var rear = new THREE.MeshStandardMaterial({ map: rearTex, emissiveMap: rearTex, emissive: 0xffffff, emissiveIntensity: off ? 0.08 : 0.3, metalness: 0.4, roughness: 0.65 });
    var geo = new THREE.BoxGeometry(s.w, h, s.d);
    var mesh = new THREE.Mesh(geo, [side, side, top, back, front, rear]);
    mesh.userData.ratio = ratio;
    // 挂耳: 前面两侧各一块略宽出机身的金属片, 上面两颗螺丝(机柜里看得出是上架设备)
    var ew = s.w * 0.035, earMat = GL.earMat || (GL.earMat = new THREE.MeshStandardMaterial({ color: 0x8b95a5, metalness: 0.85, roughness: 0.3 }));
    earMat.userData.shared = true;
    [-1, 1].forEach(function (sx) {
      var ear = new THREE.Mesh(new THREE.BoxGeometry(ew, h * 0.96, s.w * 0.006), earMat);
      ear.position.set(sx * (s.w / 2 + ew * 0.35), 0, s.d / 2 + s.w * 0.004);
      ear.raycast = function () {};
      mesh.add(ear);
    });
    mesh.position.y = y + (pitched ? o.inset / 2 : 0) + h / 2;
    mesh.castShadow = !!o.shadow; mesh.receiveShadow = !!o.shadow;
    mesh.userData.member = i;
    g.add(mesh);
    g.userData.members[i] = mesh;
    y += pitched ? each * s.uh : h + gap;
  }
  if (pitched) { gap = 0; }
  // 堆叠线缆: 背面两根发光的线把成员串起来
  if (n > 1) {
    var cableMat = new THREE.MeshBasicMaterial({ color: off ? 0x475569 : 0x22d3ee });
    [-0.3, 0.3].forEach(function (fx) {
      var cg = new THREE.CylinderGeometry(s.uh * 0.07, s.uh * 0.07, y - gap, 6);
      var c = new THREE.Mesh(cg, cableMat);
      c.position.set(fx * s.w, (y - gap) / 2, -s.d / 2 - s.uh * 0.12);
      g.add(c);
    });
  }
  // 框式: 顶部加一条散热格栅, 看起来更像机框
  if (f.kind === 'chassis') {
    var vent = new THREE.Mesh(new THREE.BoxGeometry(s.w * 0.98, s.uh * 0.3, s.d * 0.02), new THREE.MeshStandardMaterial({ color: 0x111827, metalness: 0.3, roughness: 0.8 }));
    vent.position.set(0, y - s.uh * 0.25, s.d / 2 + 0.01);
    g.add(vent);
  }
  g.userData.height = y - gap;
  g.userData.dev = dev;
  return g;
}
// 刷新已有模型的前面板贴图(端口状态变化), 不重建几何体
function glRefreshSwitch(g, dev, o) {
  var f = dev.form || { kind: 'box', u_each: 1 };
  var rowsPer = glMemberRows(dev);
  (g.userData.members || []).forEach(function (m, i) {
    glPanelTexture((o && o.keyPrefix || '') + dev.ip + '#' + i, dev, rowsPer[i] || [], Math.max(1, f.u_each || 1),
      { title: (dev.name || dev.ip) + (g.userData.members.length > 1 ? ' #' + (i + 1) : ''), slotLabels: f.kind === 'chassis', highlight: o && o.highlight }, m.userData.ratio);
    m.material[4].emissiveIntensity = dev.alive ? 0.55 : 0.15;
  });
  g.userData.dev = dev;
}
function glFormText(f) {
  if (!f) { return ''; }
  var t = f.kind === 'stack' ? f.members + ' 台堆叠 · 每台 ' + f.u_each + 'U' : (f.kind === 'chassis' ? '框式 ' + f.total_u + 'U' + (f.slots ? ' · 约 ' + f.slots + ' 槽' : '') : '盒式 ' + f.total_u + 'U');
  return t + (f.manual ? ' (手工指定)' : '');
}
function glUtilColor(u) { return u >= 0.8 ? 0xef4444 : (u >= 0.5 ? 0xeab308 : 0x22c55e); }
