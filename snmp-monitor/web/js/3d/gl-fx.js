/*
 * 3D 效果(v13): 泛光 / 环境反射 / 标签远近淡出 / 选中高亮框 / 画质设置。3D 拓扑、3D 机房、大屏共用。
 *
 * 泛光(Bloom)自己实现, 没有引入 three.js 的后处理库(内网离线, 不想再多塞文件):
 *   场景先画到一张半精度浮点纹理 → 取出亮的部分 → 5 级逐级缩小的高斯模糊 → 叠回原图
 *   → 色调映射(ACES) + sRGB 输出。指示灯、流量光带、告警光环这些自发光的东西就会真的"亮"。
 * 只在 WebGL2 上开; 画质选"中 / 低"或者自动降级时关掉, 画面回到普通渲染。
 */
const GLQ_KEY = 'glQuality';
const GLQ_NAMES = { auto: '画质: 自动', high: '画质: 高', mid: '画质: 中(无泛光)', low: '画质: 低(省电)' };
function glQuality() { return lsGet(GLQ_KEY) || 'auto'; }
function glSetQuality(v) { try { localStorage.setItem(GLQ_KEY, v); } catch (e) { /* 忽略 */ } }
// 画质下拉; 改了之后调 onChange() 让调用方重建舞台
function glQualitySelect(host, onChange, st) {
  const s = document.createElement('select');
  s.className = 'gl-q';
  s.title = '自动: 电脑跟不上时自动关掉泛光 / 降低分辨率';
  s.innerHTML = Object.keys(GLQ_NAMES).map((k) => '<option value="' + k + '">' + GLQ_NAMES[k] + '</option>').join('');
  s.value = glQuality();
  s.addEventListener('change', () => { glSetQuality(s.value); onChange(s.value); });
  host.appendChild(s);
  // 自动降级了: 在下拉框上标出来(鼠标悬停看原因)
  const mark = (why) => {
    if (!why) { return; }
    const o = s.options[s.selectedIndex] || s.options[0];
    o.text = GLQ_NAMES[s.value] + ' ⚡';
    s.title = why + (s.value === 'auto' ? '; 电脑性能够的话可以手动选「高」' : '');
  };
  if (st) { mark(st.degraded); st.onDegrade = mark; }
  return s;
}

/* ---------- 泛光 ---------- */
// v14: 有些显卡(实测部分核显 / 独显驱动)在"高"画质下整块画面发黑或出现一个个黑框。两个原因:
//   1. 场景里个别像素算出 NaN / Inf(金属高光、半精度溢出), 模糊时一扩散就成了黑块;
//   2. 多重采样 + 半精度浮点的渲染目标在一些驱动上不完整, 画出来什么都没有。
// 现在: 着色器里把 NaN / Inf / 负数清掉并限幅; 建好以后先拿一张灰色测试图跑一遍整条管线,
// 读回来不对就换成不带多重采样、8 位的渲染目标再试, 还不行就关掉泛光(下拉框上标 ⚡ 说明原因)。
function glBloomPass(st, o) {
  const R = st.renderer;
  if (!R.capabilities.isWebGL2) { return null; }
  const ext = R.extensions;
  const gl = R.getContext();
  const half = ext.has('EXT_color_buffer_float') || ext.has('EXT_color_buffer_half_float');
  const tries = [
    { type: st.THREE.HalfFloatType, samples: gl.getParameter(gl.MAX_SAMPLES) >= 4 ? 4 : 0, ok: half },
    { type: st.THREE.HalfFloatType, samples: 0, ok: half },
    { type: st.THREE.UnsignedByteType, samples: 0, ok: true }
  ];
  for (let i = 0; i < tries.length; i++) {
    if (!tries[i].ok) { continue; }
    let fx = null;
    try { fx = glBloomBuild(st, o, tries[i].type, tries[i].samples); } catch (e) { fx = null; }
    if (fx && glBloomSelfTest(st, fx)) { return fx; }
    if (fx) { fx.dispose(); }
  }
  st.degraded = '这块显卡上泛光效果画不出来, 已自动关闭(画面其余部分不受影响)';
  return null;
}
// 灰色测试图跑一遍: 清成中灰 → 提亮 → 模糊 → 合成到一张 8 位小图, 读回来应该还是灰的(不是黑、不是 NaN)
function glBloomSelfTest(st, fx) {
  const THREE = st.THREE, R = st.renderer;
  const out = new THREE.WebGLRenderTarget(8, 8, { type: THREE.UnsignedByteType });
  const px = new Uint8Array(8 * 8 * 4);
  const cc = R.getClearColor(new THREE.Color()), ca = R.getClearAlpha();
  let ok = false;
  try {
    fx.setSize(8, 8);
    R.setClearColor(0x808080, 1);
    fx.render(out, true);
    R.readRenderTargetPixels(out, 0, 0, 8, 8, px);
    let sum = 0;
    for (let i = 0; i < px.length; i += 4) { sum += px[i] + px[i + 1] + px[i + 2]; }
    ok = sum / (px.length / 4) > 60;
  } catch (e) { ok = false; }
  R.setClearColor(cc, ca);
  R.setRenderTarget(null);
  out.dispose();
  return ok;
}
// 着色器片段: 把 NaN / Inf / 负数清掉并限幅(个别驱动会把 x != x 优化掉, 所以再加一道范围判断)
const GL_SANE = 'vec3 glSane(vec3 c) { if (!(c.r == c.r && c.g == c.g && c.b == c.b)) { return vec3(0.0); }\n' +
  '  if (c.r > 1e4 || c.g > 1e4 || c.b > 1e4) { return vec3(0.0); } return clamp(c, 0.0, 32.0); }\n';
function glBloomBuild(st, o, type, samples) {
  const THREE = st.THREE, R = st.renderer;
  const fx = { enabled: true, strength: o.strength !== undefined ? o.strength : 0.85, levels: [] };
  const rt = (samples) => {
    const t = new THREE.WebGLRenderTarget(4, 4, { type, samples: samples || 0, depthBuffer: !!samples });
    t.texture.generateMipmaps = false;
    return t;
  };
  fx.sceneRT = new THREE.WebGLRenderTarget(4, 4, { type, samples: samples || 0, depthBuffer: true });
  fx.sceneRT.texture.generateMipmaps = false;
  for (let i = 0; i < 5; i++) { fx.levels.push([rt(0), rt(0)]); }
  const vs = 'varying vec2 vUv; void main() { vUv = uv; gl_Position = vec4(position.xy, 0.0, 1.0); }';
  const base = { vertexShader: vs, depthTest: false, depthWrite: false };
  const bright = new THREE.ShaderMaterial(Object.assign({
    uniforms: { tDiffuse: { value: null }, threshold: { value: o.threshold !== undefined ? o.threshold : 0.75 }, knee: { value: 0.3 } },
    fragmentShader: 'uniform sampler2D tDiffuse; uniform float threshold; uniform float knee; varying vec2 vUv;\n' + GL_SANE +
      'void main() { vec3 c = glSane(texture2D(tDiffuse, vUv).rgb); float l = max(c.r, max(c.g, c.b));\n' +
      '  gl_FragColor = vec4(c * smoothstep(threshold - knee, threshold + knee, l), 1.0); }',
    toneMapped: false
  }, base));
  const blur = new THREE.ShaderMaterial(Object.assign({
    uniforms: { tDiffuse: { value: null }, dir: { value: new THREE.Vector2() } },
    fragmentShader: 'uniform sampler2D tDiffuse; uniform vec2 dir; varying vec2 vUv;\n' +
      'void main() { vec3 c = texture2D(tDiffuse, vUv).rgb * 0.2270270270;\n' +
      '  c += (texture2D(tDiffuse, vUv + dir * 1.3846153846).rgb + texture2D(tDiffuse, vUv - dir * 1.3846153846).rgb) * 0.3162162162;\n' +
      '  c += (texture2D(tDiffuse, vUv + dir * 3.2307692308).rgb + texture2D(tDiffuse, vUv - dir * 3.2307692308).rgb) * 0.0702702703;\n' +
      '  gl_FragColor = vec4(c, 1.0); }',
    toneMapped: false
  }, base));
  const comp = new THREE.ShaderMaterial(Object.assign({
    uniforms: { tScene: { value: null }, b0: { value: null }, b1: { value: null }, b2: { value: null }, b3: { value: null }, b4: { value: null }, strength: { value: fx.strength } },
    fragmentShader: 'uniform sampler2D tScene; uniform sampler2D b0; uniform sampler2D b1; uniform sampler2D b2; uniform sampler2D b3; uniform sampler2D b4;\n' +
      'uniform float strength; varying vec2 vUv;\n' + GL_SANE +
      'void main() { vec3 c = glSane(texture2D(tScene, vUv).rgb);\n' +
      '  vec3 b = texture2D(b0, vUv).rgb + texture2D(b1, vUv).rgb * 0.85 + texture2D(b2, vUv).rgb * 0.7 + texture2D(b3, vUv).rgb * 0.55 + texture2D(b4, vUv).rgb * 0.4;\n' +
      '  gl_FragColor = vec4(c + glSane(b) * strength, 1.0);\n' +
      '  #include <tonemapping_fragment>\n  #include <colorspace_fragment>\n}'
  }, base));
  const quad = new THREE.Mesh(new THREE.PlaneGeometry(2, 2), bright);
  quad.frustumCulled = false;
  const qs = new THREE.Scene(); qs.add(quad);
  const qc = new THREE.OrthographicCamera(-1, 1, 1, -1, 0, 1);
  const pass = (mat, target) => { quad.material = mat; R.setRenderTarget(target); R.render(qs, qc); };
  fx.setSize = (w, h) => {
    w = Math.max(1, Math.floor(w)); h = Math.max(1, Math.floor(h));
    fx.sceneRT.setSize(w, h);
    fx.levels.forEach((L, i) => { const lw = Math.max(1, w >> (i + 1)), lh = Math.max(1, h >> (i + 1)); L[0].setSize(lw, lh); L[1].setSize(lw, lh); });
  };
  // target: 画到哪(默认屏幕); test: 自检用, 不画场景, 只用清屏色
  fx.render = (target, test) => {
    R.setRenderTarget(fx.sceneRT);
    R.clear();
    if (!test) { R.render(st.scene, st.camera); }
    bright.uniforms.tDiffuse.value = fx.sceneRT.texture;
    pass(bright, fx.levels[0][0]);
    for (let i = 0; i < fx.levels.length; i++) {
      const L = fx.levels[i], src = i === 0 ? L[0] : fx.levels[i - 1][0];
      blur.uniforms.tDiffuse.value = src.texture; blur.uniforms.dir.value.set(1 / src.width, 0); pass(blur, L[1]);
      blur.uniforms.tDiffuse.value = L[1].texture; blur.uniforms.dir.value.set(0, 1 / L[1].height); pass(blur, L[0]);
    }
    comp.uniforms.tScene.value = fx.sceneRT.texture;
    fx.levels.forEach((L, i) => { comp.uniforms['b' + i].value = L[0].texture; });
    comp.uniforms.strength.value = fx.strength;
    pass(comp, target || null);
  };
  fx.dispose = () => {
    fx.sceneRT.dispose();
    fx.levels.forEach((L) => { L[0].dispose(); L[1].dispose(); });
    [bright, blur, comp].forEach((m) => m.dispose());
    quad.geometry.dispose();
  };
  return fx;
}

/* ---------- 环境反射: 一个有顶灯和侧窗的"房间", 预滤波成 PMREM 给金属材质反射用 ---------- */
function glRoomEnv(renderer) {
  if (renderer.__envTex) { return renderer.__envTex; }
  const THREE = GL.THREE, pm = new THREE.PMREMGenerator(renderer), s = new THREE.Scene();
  const geos = [], mats = [];
  const box = (w, h, d, color, x, y, z, side) => {
    const g = new THREE.BoxGeometry(w, h, d), m = new THREE.MeshBasicMaterial({ color, side: side || THREE.FrontSide });
    geos.push(g); mats.push(m);
    const o = new THREE.Mesh(g, m); o.position.set(x, y, z); s.add(o); return m;
  };
  box(20, 8, 20, 0x151b27, 0, 2, 0, THREE.BackSide);
  // 顶灯(HDR 亮度 > 1 才有明显高光)
  [[-5, -5], [0, -5], [5, -5], [-5, 0], [0, 0], [5, 0], [-5, 5], [0, 5], [5, 5]].forEach((p) => box(2.4, 0.1, 0.5, 0xffffff, p[0], 5.9, p[1]).color.setRGB(5, 5, 5.4));
  box(0.1, 3, 8, 0x7dd3fc, -9.9, 2, 0).color.setRGB(1.2, 1.8, 2.6);
  box(0.1, 3, 8, 0xfff1d6, 9.9, 2, 0).color.setRGB(2.2, 2, 1.7);
  box(20, 0.1, 20, 0x2a3140, 0, -1.9, 0);
  const tex = pm.fromScene(s, 0.04).texture;
  pm.dispose(); geos.forEach((g) => g.dispose()); mats.forEach((m) => m.dispose());
  renderer.__envTex = tex;
  return tex;
}

/* ---------- 标签远近淡出 ---------- */
// glLod(st, sprite, near, far): 相机距离 < near 正常显示, near~far 逐渐缩小, > far 隐藏
function glLod(st, o, near, far) {
  o.userData.lodBase = o.scale.clone();
  st.lod.push({ o, near, far });
  return o;
}
const glLodTmp = { v: null };
function glLodUpdate(st) {
  if (!st.lod.length) { return; }
  const v = glLodTmp.v || (glLodTmp.v = new st.THREE.Vector3());
  const cam = st.camera.position;
  st.lod = st.lod.filter((x) => x.o.parent);
  st.lod.forEach((x) => {
    x.o.getWorldPosition(v);
    const d = v.distanceTo(cam), k = d <= x.near ? 1 : d >= x.far ? 0 : 1 - (d - x.near) / (x.far - x.near);
    x.o.visible = k > 0.02 && !x.o.userData.hidden;
    if (x.o.userData.lodBase) { x.o.scale.copy(x.o.userData.lodBase).multiplyScalar(0.55 + 0.45 * k); }
  });
}

/* ---------- 选中 / 悬停高亮框: 发光的边框 + 淡淡的填充, 会呼吸 ---------- */
function glBoxOf(obj) {
  const THREE = GL.THREE, box = new THREE.Box3(), tmp = new THREE.Box3();
  obj.updateMatrixWorld(true);
  obj.traverse((o) => {
    if (!o.isMesh || o.isSprite || o.userData.noBox || !o.geometry || !o.visible) { return; }
    if (!o.geometry.boundingBox) { o.geometry.computeBoundingBox(); }
    tmp.copy(o.geometry.boundingBox).applyMatrix4(o.matrixWorld);
    box.union(tmp);
  });
  return box;
}
function glPulseBox(color, opt) {
  const THREE = GL.THREE;
  opt = opt || {};
  const g = new THREE.Group();
  const edges = new THREE.LineSegments(new THREE.EdgesGeometry(new THREE.BoxGeometry(1, 1, 1)), new THREE.LineBasicMaterial({ color, transparent: true, opacity: 1, depthTest: false }));
  const fill = new THREE.Mesh(new THREE.BoxGeometry(1, 1, 1), new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 0.07, depthWrite: false }));
  edges.renderOrder = 30; fill.renderOrder = 29;
  edges.raycast = fill.raycast = function () {};
  g.add(edges); g.add(fill);
  g.visible = false;
  g.userData.noBox = true;
  const c = new THREE.Vector3(), s = new THREE.Vector3();
  g.fit = (obj, pad) => {
    if (!obj) { g.visible = false; return; }
    const b = glBoxOf(obj);
    if (b.isEmpty()) { g.visible = false; return; }
    b.getCenter(c); b.getSize(s);
    pad = pad === undefined ? 2 : pad;
    g.position.copy(c); g.scale.set(s.x + pad, s.y + pad, s.z + pad);
    g.visible = true; g.userData.t0 = performance.now();
  };
  g.setColor = (col) => { edges.material.color.set(col); fill.material.color.set(col); };
  g.tick = (t) => {
    if (!g.visible) { return false; }
    const k = 0.5 + 0.5 * Math.sin(t * (opt.speed || 4));
    edges.material.opacity = 0.55 + 0.45 * k;
    fill.material.opacity = (opt.fill || 0.06) * (0.6 + 0.4 * k);
    // 刚选中时从大到小"吸附"一下
    const e = Math.min(1, (performance.now() - (g.userData.t0 || 0)) / 260);
    const pop = 1 + (1 - e) * 0.12;
    if (pop !== 1) { g.children.forEach((x) => x.scale.setScalar(pop)); } else { g.children.forEach((x) => x.scale.setScalar(1)); }
    return true;
  };
  return g;
}

/* ---------- 缓动 ---------- */
function glEaseOut(k) { return 1 - Math.pow(1 - Math.min(1, Math.max(0, k)), 3); }
function glEaseBack(k) { k = Math.min(1, Math.max(0, k)); const c = 1.7; return 1 + (c + 1) * Math.pow(k - 1, 3) + c * Math.pow(k - 1, 2); }

/* ---------- 3D 视图上的浮动工具条(全屏 / 画质 / 巡航 ...) ---------- */
function glOverlayBar(host, cls) {
  let bar = host.querySelector(':scope > .gl-bar');
  if (!bar) {
    bar = document.createElement('div');
    bar.className = 'gl-bar' + (cls ? ' ' + cls : '');
    ['pointerdown', 'wheel', 'dblclick'].forEach((t) => bar.addEventListener(t, (e) => e.stopPropagation()));
    host.appendChild(bar);
  }
  return bar;
}
function glBarButton(bar, label, title, fn) {
  const b = document.createElement('button');
  b.type = 'button'; b.className = 'fs-btn glass'; b.innerHTML = label; b.title = title || '';
  b.addEventListener('click', (e) => { e.stopPropagation(); fn(b); });
  bar.appendChild(b);
  return b;
}
