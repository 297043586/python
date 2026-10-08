/*
 * 全局显示参数(设置里的 ui 段)。
 */
/* ================= 全局显示参数(设置里的 ui 段) ================= */
var UIS = { site_title: '', page_refresh_seconds: 30, bigscreen_refresh_seconds: 15, bigscreen_style: 'tech', bigscreen_center: 'map',
  bigscreen_carousel_seconds: 30, bigscreen_scroll_speed: 30, bigscreen_risk_count: 8, bigscreen_hot_count: 6, bigscreen_animations: true,
  topo_refresh_seconds: 30, topo3d_particles: true, topo3d_auto_rotate: true, rack_refresh_seconds: 30, weak_optical_dbm: -20, busy_util_percent: 70 };
var UIS_LOADED = false, UIS_WAIT = [];
function uiPref(k, d) { return (UIS && UIS[k] !== undefined && UIS[k] !== null) ? UIS[k] : d; }
function uiReady(fn) { if (UIS_LOADED) { fn(); } else { UIS_WAIT.push(fn); } }
function loadUISettings() {
  return pub('/api/ui-settings').then(function (d) {
    var u = d.ui || {};
    Object.keys(u).forEach(function (k) { UIS[k] = u[k]; });
    applyUISettings();
  }).catch(function () {}).then(function () {
    UIS_LOADED = true;
    var w = UIS_WAIT; UIS_WAIT = [];
    w.forEach(function (fn) { try { fn(); } catch (e) {} });
  });
}
function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
function applyUISettings() {
  if (UIS.site_title) {
    var h = document.querySelector('.header h1');
    if (h) { h.innerText = '🚀 ' + UIS.site_title; }
    document.title = UIS.site_title;
  }
  // 浏览器自己没选过的, 用全局默认值
  if (!lsGet('theme') && UIS.default_theme) {
    document.body.className = UIS.default_theme;
    if (byId('themeSelect')) { byId('themeSelect').value = UIS.default_theme; }
  }
  if (!lsGet('refreshInterval') && UIS.page_refresh_seconds) {
    interval = UIS.page_refresh_seconds; timeLeft = Math.min(timeLeft, interval);
    byId('intervalInput').value = interval;
  }
  if (typeof T3 !== 'undefined') {
    if (lsGet('topo3dAuto') === null) { T3.auto = !!UIS.topo3d_auto_rotate; }
    if (lsGet('topo3dParticles') === null) { T3.particles = !!UIS.topo3d_particles; }
  }
}
loadUISettings();
