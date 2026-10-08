/*
 * 拓扑视图调度(v13)。
 *
 * 以前 2D / 3D 是层层"覆盖全局函数"接起来的: topo3d 把 2D 的 renderTopology / topoZoom / ...
 * 存一份再换成自己的, WebGL 版又把 3D 的换一遍; 加载顺序一变就乱, 也看不出一个调用最后
 * 走到哪里。现在三种画法是三个独立的渲染器, 由这里按当前模式明确分派:
 *
 *   2d          topo2d.js          tp2Render / tp2Zoom / tp2Reset / tp2Highlight / tp2Close
 *   3d-canvas   topo3d-canvas.js   t3Mount / t3ZoomBy / t3ResetCamera / t3Highlight / t3Stop   (兼容模式, 不需要 WebGL)
 *   3d-gl       topo3d-gl.js       tgTopoMount / tgZoom / tgResetCamera / tgHighlight / tgTopoDestroy
 *
 * 对外的入口(页面按钮、搜索框、其他模块在用)只有这里的几个: renderTopology / topoZoom /
 * topoResetView / topoHighlight / closeTopology / topoRemount, 以及 3D 的两个开关 t3SetAuto / t3SetParticles。
 */
const TOPO_VIEWS = {
  '2d': { render: tp2Render, zoom: tp2Zoom, reset: tp2Reset, highlight: tp2Highlight, stop() {} },
  '3d-canvas': { render: t3Mount, zoom: t3ZoomBy, reset: () => t3ResetCamera(), highlight: t3Highlight, stop: t3Stop },
  '3d-gl': {
    render: tgTopoMount,
    zoom: (f) => { if (TG && TG.st) { tgZoom(TG, f); } },
    reset: () => { if (TG && TG.st) { tgSelect(TG, null); tgResetCamera(TG, false); } },
    highlight: () => { if (TG && TG.st) { tgHighlight(); } },
    stop: tgTopoDestroy
  }
};
let topoCur = null; // 当前挂着的渲染器

function topoViewKind() { return !T3.on ? '2d' : tgWanted() ? '3d-gl' : '3d-canvas'; }
function topoView() { return TOPO_VIEWS[topoCur || topoViewKind()]; }

function renderTopology(data) {
  t3SyncButtons();
  if (T3.on) { tgSyncEngineCtl(); }
  const kind = topoViewKind();
  if (topoCur && topoCur !== kind) { TOPO_VIEWS[topoCur].stop(); }
  topoCur = kind;
  TOPO_VIEWS[kind].render(data);
}
// 换了渲染方式 / 画质: 拆掉当前画面重新挂
function topoRemount() {
  if (topoCur) { TOPO_VIEWS[topoCur].stop(); }
  topoCur = null;
  byId('topoWrap').innerHTML = '';
  if (topoData) { renderTopology(topoData); }
}
function topoZoom(f) { topoView().zoom(f); }
function topoResetView() { topoView().reset(); }
function topoHighlight() { topoView().highlight(); }
function closeTopology() {
  fsExitWithin(byId('topoModal'));
  if (topoCur) { TOPO_VIEWS[topoCur].stop(); }
  topoCur = null;
  tp2Close();
}

modalRegister('topoModal', closeTopology);

// 3D 的两个开关: Canvas 版和 WebGL 版共用一份设置
function t3SetAuto(on) {
  T3.auto = on; t3Save('topo3dAuto', on);
  if (TG && TG.st) { TG.auto = on; TG.st.controls.autoRotate = on; }
  t3Poke();
}
function t3SetParticles(on) {
  T3.particles = on; t3Save('topo3dParticles', on);
  if (TG && TG.st) { TG.showParticles = on; if (TG.particles) { TG.particles.visible = on; } TG.st.poke(); }
  t3Poke();
}
