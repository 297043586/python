/*
 * 全屏(v13): 3D 拓扑 / 3D 机房 / 2D 拓扑 / 告警中心都能单独全屏。
 *
 * 优先用浏览器的 Fullscreen API(真全屏, 连浏览器标签栏都隐藏); 被策略禁止或者在 iframe 里
 * 用不了时退回"铺满窗口"(position: fixed)。按 Esc 或再点一次按钮退出。
 *
 *   fsToggle(el)              切换
 *   fsButton(el, host)        在 host 里放一个 ⛶ 按钮, 状态自动同步
 *   fsActive(el)              el 当前是否全屏
 */
const FS = { fallback: null, listeners: new Set() };

function fsElement() { return document.fullscreenElement || document.webkitFullscreenElement || FS.fallback; }
function fsActive(el) { return fsElement() === el; }
function fsNotify() { FS.listeners.forEach((fn) => { try { fn(); } catch (e) { /* 忽略 */ } }); }

function fsEnter(el) {
  const req = el.requestFullscreen || el.webkitRequestFullscreen;
  const fallback = () => { FS.fallback = el; el.classList.add('fs-fallback'); document.body.classList.add('fs-lock'); fsNotify(); };
  if (!req) { fallback(); return; }
  try {
    const p = req.call(el, { navigationUI: 'hide' });
    if (p && p.catch) { p.catch(fallback); }
  } catch (e) { fallback(); }
}
function fsExit() {
  if (FS.fallback) {
    FS.fallback.classList.remove('fs-fallback');
    document.body.classList.remove('fs-lock');
    FS.fallback = null;
    fsNotify();
    return;
  }
  const ex = document.exitFullscreen || document.webkitExitFullscreen;
  if (ex && (document.fullscreenElement || document.webkitFullscreenElement)) { try { ex.call(document); } catch (e) { /* 忽略 */ } }
}
// 关掉某个窗口时, 如果全屏的是它里面的东西, 先退出全屏
function fsExitWithin(el) { const cur = fsElement(); if (cur && el && (el === cur || el.contains(cur))) { fsExit(); } }
function fsToggle(el) { if (fsActive(el)) { fsExit(); } else { if (fsElement()) { fsExit(); } fsEnter(el); } }

['fullscreenchange', 'webkitfullscreenchange'].forEach((t) => document.addEventListener(t, () => {
  document.querySelectorAll('.is-fs').forEach((x) => x.classList.remove('is-fs'));
  const el = fsElement();
  if (el) { el.classList.add('is-fs'); }
  fsNotify();
  // 画布尺寸跟着变
  setTimeout(() => window.dispatchEvent(new Event('resize')), 60);
}));
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && FS.fallback) { e.stopPropagation(); fsExit(); }
}, true);

function fsButton(el, host, opt) {
  opt = opt || {};
  const b = document.createElement('button');
  b.type = 'button';
  b.className = opt.cls || 'fs-btn';
  const sync = () => {
    const on = fsActive(el);
    b.innerHTML = on ? '⤡ <span>退出全屏</span>' : '⛶ <span>全屏</span>';
    b.title = on ? '退出全屏 (Esc)' : '全屏显示 (F)';
    b.classList.toggle('on', on);
    el.classList.toggle('is-fs', on);
  };
  b.addEventListener('click', (e) => { e.stopPropagation(); fsToggle(el); });
  FS.listeners.add(sync);
  sync();
  host.appendChild(b);
  return b;
}

// F 键: 当前鼠标下(或最近点过)的可全屏区域
document.addEventListener('keydown', (e) => {
  if ((e.key !== 'f' && e.key !== 'F') || e.ctrlKey || e.metaKey || e.altKey) { return; }
  const t = e.target;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) { return; }
  const el = FS.hover && document.body.contains(FS.hover) && FS.hover.offsetParent ? FS.hover : null;
  if (el) { e.preventDefault(); fsToggle(el); }
});
// 标记可以用 F 键全屏的区域
function fsHotkeyZone(el) {
  el.addEventListener('pointerenter', () => { FS.hover = el; });
}
