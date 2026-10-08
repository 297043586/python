/*
 * 大屏总览(前端)。顶栏「📺 大屏」进入, 也可以直接打开 http://服务器:端口/#bigscreen
 * 给值班室/投影用(打开即进入大屏, 按 F11 或页面上的「全屏」按钮铺满)。
 *
 *   - 按 1920x1080 设计, 整体等比缩放到当前窗口, 投影仪/电视/笔记本都不会变形。
 *   - 6 套样式: 科技蓝 / 赛博霓虹 / 极光 / 指挥中心·黑金 / 军绿雷达 / 红色警戒,
 *     每套有自己的配色、背景动效和标题特效。
 *   - 中间主画面可选: 站点态势地图(飞线 + 流量粒子) / 3D 网络拓扑 / 3D 数据中心机房 /
 *     三者轮播; 3D 用内置的 three.js。
 *   - 所有列表都能用鼠标滚轮/拖动/触屏滑动, 内容多时自动慢慢滚动, 鼠标放上去或手动滚过
 *     之后暂停, 几秒不动再继续。
 *   - 右上角「⚙」可以临时改样式/主画面/刷新间隔/滚动速度/动画开关(只记在这台电脑的
 *     浏览器里); 全局默认值在「🛠️ 设置 → 大屏」里, 值班室电视不用单独设置。
 *   - 大屏关闭或浏览器切到后台时停止绘制。
 */
/* ================= 大屏总览 ================= */
var BS = { data: null, timer: null, clock: null, raf: 0, t0: 0, nodes: [], links: [], img: null, imgVer: -1, hover: null, next: 0,
  prefs: {}, view: 'map', carousel: null, topo: null, dc: null, seenAlerts: {}, scrollers: [], pal: null };
var BS_STYLES = [['tech', '科技蓝<br><small>经典三栏</small>', '#0e7490'], ['cyber', '赛博霓虹<br><small>全屏 HUD</small>', '#a21caf'], ['aurora', '极光<br><small>便当格</small>', '#047857'],
  ['gold', '指挥中心<br><small>黑金铭牌</small>', '#a16207'], ['mono', '军绿雷达<br><small>雷达屏</small>', '#3f6212'], ['redalert', '红色警戒<br><small>事件处置</small>', '#b91c1c']];
var BS_VIEWS = [['map', '站点地图'], ['topo3d', '3D 拓扑'], ['dc3d', '3D 机房']];
// 生效的大屏参数 = 全局设置(设置 → 大屏) 叠加 本机浏览器临时改的
function bsPref(k) {
  var local = BS.prefs[k];
  if (local !== undefined && local !== null && local !== '') { return local; }
  return uiPref('bigscreen_' + k, { refresh_seconds: 15, style: 'tech', center: 'map', carousel_seconds: 30, scroll_speed: 30, risk_count: 8, hot_count: 6, animations: true }[k]);
}
(function () { try { BS.prefs = JSON.parse(localStorage.getItem('bsPrefs') || '{}') || {}; } catch (e) { BS.prefs = {}; } })();
function bsSavePrefs() { try { localStorage.setItem('bsPrefs', JSON.stringify(BS.prefs)); } catch (e) {} }

function openBigScreen() {
  var root = byId('bsRoot');
  root.style.display = 'block';
  uiReady(function () {
    bsApplyStyle();
    bsScale();
    bsTick();
    if (!BS.clock) { BS.clock = setInterval(bsTick, 1000); }
    bsLoad();
    bsRestartTimer();
    bsSetView(bsPref('center') === 'carousel' ? 'map' : bsPref('center'), true);
    bsSetupCarousel();
    if (!BS.scrollRaf) { BS.scrollLast = 0; BS.scrollRaf = setInterval(bsScrollFrame, 40); }
  });
  if (location.hash !== '#bigscreen') { try { history.replaceState(null, '', '#bigscreen'); } catch (e) {} }
}
function closeBigScreen() {
  byId('bsRoot').style.display = 'none';
  if (BS.timer) { clearInterval(BS.timer); BS.timer = null; }
  if (BS.clock) { clearInterval(BS.clock); BS.clock = null; }
  if (BS.carousel) { clearInterval(BS.carousel); BS.carousel = null; }
  if (BS.raf) { cancelAnimationFrame(BS.raf); BS.raf = 0; }
  if (BS.scrollRaf) { clearInterval(BS.scrollRaf); BS.scrollRaf = 0; }
  bs3DDispose();
  bsdClose();
  if (document.fullscreenElement) { try { document.exitFullscreen(); } catch (e) {} }
  if (location.hash === '#bigscreen') { try { history.replaceState(null, '', location.pathname + location.search); } catch (e) {} }
}
function bsFullscreen() {
  var el = byId('bsRoot');
  if (document.fullscreenElement) { document.exitFullscreen(); return; }
  if (el.requestFullscreen) { el.requestFullscreen().catch(function () {}); }
}
function bsScale() {
  var W = window.innerWidth, H = window.innerHeight;
  var s = Math.min(W / 1920, H / 1080);
  var st = byId('bsStage');
  st.style.transform = 'scale(' + s + ')';
  st.style.left = ((W - 1920 * s) / 2) + 'px';
  st.style.top = ((H - 1080 * s) / 2) + 'px';
  BS.scale = s;
}
window.addEventListener('resize', function () { if (byId('bsRoot').style.display === 'block') { bsScale(); } });
function bsTick() {
  var d = new Date(), p = function (n) { return ('0' + n).slice(-2); };
  byId('bsClock').innerText = p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  byId('bsDate').innerText = d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' 星期' + '日一二三四五六'.charAt(d.getDay());
  if (BS.next) { var left = Math.max(0, Math.round((BS.next - Date.now()) / 1000)); byId('bsNext').innerText = '下次刷新 ' + left + 's'; }
}
function bsRestartTimer() {
  if (BS.timer) { clearInterval(BS.timer); }
  var sec = Math.max(5, parseInt(bsPref('refresh_seconds'), 10) || 15);
  BS.next = Date.now() + sec * 1000;
  BS.timer = setInterval(function () { BS.next = Date.now() + sec * 1000; bsLoad(); }, sec * 1000);
}
// 样式: 切换 CSS 变量, 并把颜色读出来给 Canvas 用
function bsApplyStyle() {
  var root = byId('bsRoot');
  BS_STYLES.forEach(function (s) { root.classList.remove('st-' + s[0]); });
  var st = bsPref('style');
  if (st !== 'tech') { root.classList.add('st-' + st); }
  root.classList.toggle('noanim', !bsPref('animations'));
  var cs = getComputedStyle(root), v = function (k) { return cs.getPropertyValue(k).trim(); };
  BS.pal = { accent: v('--bs-accent'), accent2: v('--bs-accent2'), text: v('--bs-text'), dim: v('--bs-dim'), strong: v('--bs-strong'), hot: v('--bs-hot'), warn: v('--bs-warn'), ok: v('--bs-ok'), bg: v('--bs-bg') };
  var t = uiPref('bigscreen_title', '') || '网络运行态势总览';
  byId('bsTitle').innerText = t; byId('bsTitle').setAttribute('data-t', t);
  if (BS.data) { bsRender(BS.data); }
}
function bsLoad() {
  pub('/api/overview').then(function (d) {
    BS.data = d;
    bsRender(d);
  }).catch(function (e) { byId('bsSub').innerText = '数据加载失败: ' + e; });
  if (BS.view === 'topo3d' && BS.topo) { pub('/api/topology').then(function (d) { if (BS.topo) { tgSetData(BS.topo, d, false); } }).catch(function () {}); }
  if (BS.view === 'dc3d' && BS.dc) { bsDcLoad(false); }
}
function bsFmtMbps(v) {
  v = v || 0;
  if (v >= 1000) { return (v / 1000).toFixed(2) + ' Gbps'; }
  return v.toFixed(v >= 100 ? 0 : 1) + ' Mbps';
}
function bsRing(pct, color, text) {
  pct = Math.max(0, Math.min(100, pct || 0));
  return '<div class="bs-ring" style="background:conic-gradient(' + color + ' ' + pct + '%, rgba(128,128,128,.14) 0);"><div style="color:' + color + ';">' + text + '</div></div>';
}
function bsHealthColor(v) { var P = BS.pal || {}; return v >= 90 ? (P.ok || '#22d3ee') : v >= 75 ? '#4ade80' : v >= 60 ? (P.warn || '#fbbf24') : (P.hot || '#f87171'); }
// 数字从旧值滚到新值
function bsCount(el, to, fmt) {
  if (!el) { return; }
  var from = parseFloat(el.getAttribute('data-v') || '0') || 0;
  el.setAttribute('data-v', to);
  if (!bsPref('animations') || from === to) { el.innerText = fmt(to); return; }
  var t0 = performance.now();
  var step = function (now) {
    var k = Math.min(1, (now - t0) / 900), e = 1 - Math.pow(1 - k, 3);
    el.innerText = fmt(from + (to - from) * e);
    if (k < 1) { requestAnimationFrame(step); }
  };
  requestAnimationFrame(step);
}
function bsRender(d) {
  var P = BS.pal;
  var sec = Math.max(5, parseInt(bsPref('refresh_seconds'), 10) || 15);
  byId('bsSub').innerText = '巡检时间 ' + (d.check_time || '-') + '  ·  监控设备 ' + d.devices_total + ' 台  ·  ' + d.sites.length + ' 个站点  ·  每 ' + sec + ' 秒刷新';
  var onPct = d.devices_total ? d.devices_online / d.devices_total * 100 : 0;
  var portPct = d.ports_total ? d.ports_up / d.ports_total * 100 : 0;
  var openAlerts = d.alerts_critical + d.alerts_warning;
  var int0 = function (x) { return String(Math.round(x)); };
  // 其他样式的 KPI 各有各的画法(见 bigscreen_styles.go), 科技蓝是下面的圆环
  if (bsKpiStyled(d)) {
    // 已画好
  } else {
  if (!byId('bsK1')) {
    byId('bsKpis').innerHTML =
      '<div class="bs-kpi" id="bsKr1"></div><div class="bs-kpi" id="bsKr2"></div><div class="bs-kpi" id="bsKr3"></div>' +
      '<div class="bs-kpi"><div class="bs-kv" style="padding-left:6px;"><span>端口总吞吐 入 / 出</span><b style="font-size:22px;" id="bsKin"></b><b style="font-size:22px;color:' + P.accent + ';" id="bsKout"></b></div></div>' +
      '<span id="bsK1" style="display:none;"></span>';
  }
  byId('bsKr1').innerHTML = bsRing(onPct, onPct >= 100 ? P.ok : P.warn, Math.round(onPct) + '%') + '<div class="bs-kv"><span>设备在线</span><b><span id="bsKon"></span><em> / ' + d.devices_total + '</em></b></div>';
  byId('bsKr2').innerHTML = bsRing(openAlerts ? Math.min(100, d.alerts_critical / Math.max(openAlerts, 1) * 100 + 20) : 0, openAlerts ? P.hot : P.ok, openAlerts) +
    '<div class="bs-kv"><span>未处理告警</span><b style="color:' + (d.alerts_critical ? P.hot : P.strong) + ';"><span id="bsKcrit"></span><em> 严重</em></b><em>' + d.alerts_warning + ' 条警告</em></div>';
  byId('bsKr3').innerHTML = bsRing(portPct, P.accent, Math.round(portPct) + '%') + '<div class="bs-kv"><span>物理口使用</span><b><span id="bsKup"></span><em> / ' + d.ports_total + '</em></b></div>';
  bsCount(byId('bsKon'), d.devices_online, int0);
  bsCount(byId('bsKcrit'), d.alerts_critical, int0);
  bsCount(byId('bsKup'), d.ports_up, int0);
  bsCount(byId('bsKin'), d.throughput_in, bsFmtMbps);
  bsCount(byId('bsKout'), d.throughput_out, bsFmtMbps);
  }

  var hc = bsHealthColor(d.health);
  byId('bsHealth').innerHTML = '<b style="color:' + hc + ';" id="bsHv"></b><span>全网健康评分</span>';
  bsCount(byId('bsHv'), d.health, int0);

  var risks = (d.risks || []).slice(0, parseInt(bsPref('risk_count'), 10) || 8);
  byId('bsRiskN').innerText = risks.length ? '共 ' + risks.length + ' 台' : '';
  bsKeepScroll(byId('bsRisk'), risks.length ? risks.map(function (r, i) {
    return '<div class="bs-row bs-click' + (i < 3 && r.score < 90 ? ' top3' : '') + '" data-ip="' + esc(r.ip) + '"><span class="bs-rank">' + (i + 1) + '</span><span class="nm">' + esc(r.name) + '<small>' + esc(r.issue || '无明显问题') + (r.note ? ' · 📝 ' + esc(r.note) : '') + '</small></span>' +
      '<span class="bs-num" style="color:' + bsHealthColor(r.score) + ';">' + r.score + ' 分</span></div>';
  }).join('') : '<div class="bs-empty">暂无设备</div>');

  var cats = d.alert_cats || [], cmax = 1;
  cats.forEach(function (c) { cmax = Math.max(cmax, c.count); });
  bsKeepScroll(byId('bsCats'), cats.length ? cats.map(function (c) {
    return '<div class="bs-row" style="border:0;padding:5px 0;"><span style="width:110px;flex-shrink:0;">' + esc(c.name) + '</span><div class="bs-bar"><i class="hot" style="width:' + (c.count / cmax * 100) + '%;"></i></div><span class="bs-num">' + c.count + '</span></div>';
  }).join('') : '<div class="bs-empty">✅ 当前没有未处理告警</div>');

  var alerts = d.alerts || [];
  byId('bsAlertCount').innerText = openAlerts ? '共 ' + openAlerts + ' 条未处理' : '';
  var seen = {}, fresh = Object.keys(BS.seenAlerts).length > 0;
  bsKeepScroll(byId('bsFeed'), alerts.length ? alerts.map(function (a) {
    var key = a.ip + '|' + a.category + '|' + a.text;
    seen[key] = true;
    var isNew = fresh && !BS.seenAlerts[key];
    return '<div class="bs-click" data-ip="' + esc(a.ip) + '">' + bsFeedItem(a, isNew) + '</div>';
  }).join('') : '<div class="bs-empty" style="padding-top:60px;">✅ 全网运行正常, 没有未处理告警</div>');
  var newCrit = fresh ? alerts.filter(function (a) { return a.level === 'critical' && !BS.seenAlerts[a.ip + '|' + a.category + '|' + a.text]; }) : [];
  if (newCrit.length) { bsdToast(newCrit[0]); }
  BS.seenAlerts = seen;

  var hot = (d.hot_ports || []).slice(0, parseInt(bsPref('hot_count'), 10) || 6);
  var busy = uiPref('busy_util_percent', 70);
  bsKeepScroll(byId('bsHot'), hot.length ? hot.map(function (p) {
    var u = Math.max(p.in_u, p.out_u);
    return '<div class="bs-row bs-click" data-ip="' + esc(p.ip || '') + '" data-port="' + esc(p.port) + '"><span class="nm">' + esc(p.device) + ' <span style="color:' + P.accent + ';">' + esc(p.port) + '</span><small>' + (p.peer ? '➔ ' + esc(p.peer) + ' · ' : '') + bsFmtMbps(p.mbps) + '</small></span>' +
      '<div class="bs-bar" style="max-width:120px;"><i class="' + (u >= busy ? 'hot' : '') + '" style="width:' + Math.min(100, u) + '%;"></i></div><span class="bs-num">' + u.toFixed(1) + '%</span></div>';
  }).join('') : '<div class="bs-empty">暂无端口流量数据</div>');

  if (d.flow_enabled) {
    byId('bsAppTitle').innerHTML = '应用流量 Top<small>最近 5 分钟 · sFlow/NetFlow</small>';
    var apps = d.apps || [], amax = 1;
    apps.forEach(function (a) { amax = Math.max(amax, a.bytes); });
    bsKeepScroll(byId('bsApps'), apps.length ? apps.map(function (a) {
      return '<div class="bs-row" style="border:0;padding:5px 0;"><span style="width:130px;flex-shrink:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">' + esc(a.app) + '</span><div class="bs-bar"><i style="width:' + (a.bytes / amax * 100) + '%;"></i></div><span class="bs-num">' + fmtBytes(a.bytes) + '</span></div>';
    }).join('') : '<div class="bs-empty">最近 5 分钟没有流量样本</div>');
  } else {
    byId('bsAppTitle').innerHTML = '设备品牌分布<small>未开启流量采集</small>';
    var br = d.brands || [], bmax = 1;
    br.forEach(function (b) { bmax = Math.max(bmax, b.count); });
    bsKeepScroll(byId('bsApps'), br.map(function (b) {
      return '<div class="bs-row" style="border:0;padding:5px 0;"><span style="width:96px;flex-shrink:0;">' + esc(b.name) + '</span><div class="bs-bar"><i style="width:' + (b.count / bmax * 100) + '%;"></i></div><span class="bs-num">' + b.count + ' 台</span></div>';
    }).join(''));
  }

  bsBuildMap(d);
  bsDrawTrend(d.trend || []);
  bsdRefresh();
  if (!BS.raf) { BS.t0 = performance.now(); BS.raf = requestAnimationFrame(bsFrame); }
}

/* ---------- 可以手动滚动的列表 + 自动滚动 ---------- */
// 替换内容时保留滚动位置, 不会每次刷新都跳回顶部
function bsKeepScroll(el, html) {
  var top = el.scrollTop;
  el.innerHTML = html;
  el.scrollTop = top;
  bsBindScroller(el);
}
function bsBindScroller(el) {
  if (el.dataset.scr) { return; }
  el.dataset.scr = '1';
  var s = { el: el, hover: false, userUntil: 0, pauseUntil: 0, acc: 0 };
  BS.scrollers.push(s);
  el.addEventListener('mouseenter', function () { s.hover = true; });
  el.addEventListener('mouseleave', function () { s.hover = false; s.userUntil = performance.now() + 3000; });
  el.addEventListener('wheel', function () { s.userUntil = performance.now() + 6000; }, { passive: true });
  el.addEventListener('touchstart', function () { s.userUntil = performance.now() + 6000; }, { passive: true });
  // 鼠标按住拖动滚动(大屏常见的触控一体机/遥控鼠标)
  var drag = null;
  el.addEventListener('pointerdown', function (e) {
    if (e.pointerType === 'touch' || e.button !== 0) { return; }
    drag = { y: e.clientY, top: el.scrollTop, moved: false };
  });
  window.addEventListener('pointermove', function (e) {
    if (!drag) { return; }
    var dy = (e.clientY - drag.y) / (BS.scale || 1);
    if (Math.abs(dy) > 3) { drag.moved = true; el.classList.add('dragging'); }
    el.scrollTop = drag.top - dy;
    s.userUntil = performance.now() + 6000;
  });
  window.addEventListener('pointerup', function () { if (drag) { drag = null; el.classList.remove('dragging'); } });
}
// 用定时器而不是 requestAnimationFrame: 部分电视/投影的浏览器在后台或低功耗时会停掉 rAF
function bsScrollFrame() {
  if (byId('bsRoot').style.display !== 'block') { return; }
  var now = performance.now();
  var dt = BS.scrollLast ? Math.min(0.1, (now - BS.scrollLast) / 1000) : 0;
  BS.scrollLast = now;
  var speed = parseInt(bsPref('scroll_speed'), 10) || 0;
  if (!speed || document.hidden) { return; }
  // 先把所有列表的尺寸读完再统一写 scrollTop: 读写交替会让浏览器每个列表都强制重排一次
  var jobs = [];
  BS.scrollers.forEach(function (s) {
    if (s.hover || now < s.userUntil || now < s.pauseUntil) { return; }
    var el = s.el, max = el.scrollHeight - el.clientHeight;
    if (max <= 2 || !el.offsetParent) { return; }
    s.acc += speed * dt;
    jobs.push({ s: s, max: max, top: el.scrollTop });
  });
  jobs.forEach(function (j) {
    var s = j.s, el = s.el, top = j.top;
    if (s.acc >= 1) {
      var step = Math.floor(s.acc); s.acc -= step;
      top = Math.min(j.max, top + step);
      el.scrollTop = top;
    }
    // 到底了停 3 秒再回到顶部
    if (top >= j.max - 1) {
      s.pauseUntil = now + 3000;
      setTimeout(function () { if (el.scrollTo) { el.scrollTo({ top: 0, behavior: 'smooth' }); } else { el.scrollTop = 0; } s.pauseUntil = performance.now() + 2000; }, 3000);
    }
  });
}

/* ---------- 中间主画面: 站点地图 / 3D 拓扑 / 3D 机房 ---------- */
function bsSetupCarousel() {
  if (BS.carousel) { clearInterval(BS.carousel); BS.carousel = null; }
  var views = byId('bsViews');
  views.innerHTML = BS_VIEWS.map(function (v) { return '<button data-v="' + v[0] + '" onclick="bsPickView(&quot;' + v[0] + '&quot;)">' + v[1] + '</button>'; }).join('');
  bsMarkView();
  if (bsPref('center') !== 'carousel') { return; }
  var sec = Math.max(10, parseInt(bsPref('carousel_seconds'), 10) || 30);
  BS.carousel = setInterval(function () {
    var order = glSupported() ? ['map', 'topo3d', 'dc3d'] : ['map'];
    var i = order.indexOf(BS.view);
    bsSetView(order[(i + 1) % order.length]);
  }, sec * 1000);
}
function bsPickView(v) {
  // 手动点了某个画面: 轮播暂停一轮
  if (BS.carousel) { bsSetupCarousel(); }
  bsSetView(v);
}
function bsMarkView() { document.querySelectorAll('#bsViews button').forEach(function (b) { b.classList.toggle('on', b.getAttribute('data-v') === BS.view); }); }
function bsSetView(v, first) {
  if ((v === 'topo3d' || v === 'dc3d') && !glSupported()) { v = 'map'; }
  BS.view = v;
  bsMarkView();
  byId('bsMap').style.display = v === 'map' ? '' : 'none';
  byId('bsTopo3d').style.display = v === 'topo3d' ? 'block' : 'none';
  byId('bsDc3d').style.display = v === 'dc3d' ? 'block' : 'none';
  byId('bsMapTip').style.display = 'none';
  bsLegend();
  if (v === 'map') { if (BS.data) { bsBuildMap(BS.data); } return; }
  glLoad().then(function () {
    if (byId('bsRoot').style.display !== 'block') { return; }
    if (v === 'topo3d') {
      if (!BS.topo) {
        BS.topo = tgNew(byId('bsTopo3d'), { bigscreen: true, transparent: true, auto: !!bsPref('animations'), particles: !!bsPref('animations'), key: 'bs:', showTip: bsShowTip, hideTip: bsHideTip, onOpen: function (ip) { bsdOpen({ t: 'dev', ip: ip }); } });
      }
      byId('bsMapTitle').innerHTML = '3D 网络拓扑<small>核心 / 汇聚 / 接入分层 · 线色 = 利用率 · 光点 = 实时流量</small>';
      pub('/api/topology').then(function (d) { if (BS.topo) { tgSetData(BS.topo, d, true); } }).catch(function () {});
    } else {
      if (!BS.dc) {
        BS.dc = dcNewScene(byId('bsDc3d'), { key: 'bsdc:', noShadow: false });
        BS.dc.st.controls.autoRotate = !!bsPref('animations');
        BS.dc.st.controls.autoRotateSpeed = 0.4;
        BS.dc.st.renderer.domElement.addEventListener('pointermove', function (e) {
          var h = BS.dc.st.pick(e, BS.dc.pickables);
          if (h) { bsShowTip(dcTipHTMLFor(h.obj.userData, BS.dc), e); } else { bsHideTip(); }
        });
        BS.dc.st.renderer.domElement.addEventListener('pointerleave', bsHideTip);
        // 单击设备 = 设备详情抽屉, 单击机柜 = 柜里的设备列表(拖动旋转不算单击)
        var dn = null;
        BS.dc.st.renderer.domElement.addEventListener('pointerdown', function (e) { dn = { x: e.clientX, y: e.clientY }; });
        BS.dc.st.renderer.domElement.addEventListener('pointerup', function (e) {
          if (!dn || Math.abs(e.clientX - dn.x) + Math.abs(e.clientY - dn.y) > 5 || e.button !== 0) { dn = null; return; }
          dn = null;
          var h = BS.dc.st.pick(e, BS.dc.pickables), u = h ? h.obj.userData : null;
          if (!u) { return; }
          if (u.dev) { bsdOpen({ t: 'dev', ip: u.dev.ip }); return; }
          if (u.rack && (u.rack.type || 'rack') === 'rack') {
            var ips = (BS.dc.devices || []).filter(function (x) { return x.rack === u.rack.name; }).map(function (x) { return x.ip; });
            if (ips.length) { bsdOpen({ t: 'list', kind: 'devices', title: '机柜 ' + u.rack.name, filter: { ips: ips } }); }
          }
        });
      }
      bsDcLoad(true);
    }
  }).catch(function () { bsSetView('map'); });
}
function bsLegend() {
  var P = BS.pal || {};
  var html = BS.view === 'map'
    ? '<i style="background:' + P.accent + ';"></i>正常<i style="background:' + P.warn + ';"></i>部分离线<i style="background:' + P.hot + ';"></i>有告警<br>飞线 = 站点间链路, 粗细 = 带宽, 颜色 = 利用率, 光点 = 实时流量方向'
    : BS.view === 'topo3d' ? '拖动旋转 · 滚轮缩放 · 堆叠画成多台叠放, 框式画成高机框<br>面板灯 = 端口状态 · 红色光环 = 有告警'
      : '机柜顶灯: <i style="background:#22c55e;"></i>正常<i style="background:#f59e0b;"></i>离线<i style="background:#ef4444;"></i>告警<br>拖动旋转 · 滚轮缩放 · 机房布局在「机柜 → 3D 机房」里编辑';
  byId('bsLegend').innerHTML = html;
}
function bsDcLoad(first) {
  pub('/api/datacenter').then(function (d) {
    if (!BS.dc) { return; }
    var lay = dcNormLayout(d.layout), devs = d.devices || [];
    dcEnsureAutoRacks(lay, devs);
    // 大屏上显示机柜最多的那个机房
    var best = lay.rooms[0], cnt = {};
    lay.racks.forEach(function (k) { cnt[k.room] = (cnt[k.room] || 0) + 1; });
    lay.rooms.forEach(function (r) { if ((cnt[r.id] || 0) > (cnt[best.id] || 0)) { best = r; } });
    dcBuild(BS.dc, lay, devs, best.id);
    var bad = devs.filter(function (x) { return x.alive && x.alerts > 0; }).length, off = devs.filter(function (x) { return !x.alive; }).length;
    byId('bsMapTitle').innerHTML = '3D 数据中心 · ' + esc(best.name) + '<small>' + (cnt[best.id] || 0) + ' 个机柜 · ' + devs.length + ' 台交换机' + (bad ? ' · 告警 ' + bad : '') + (off ? ' · 离线 ' + off : '') + '</small>';
    if (first) {
      // 镜头对准机柜所在的区域(机房很大但机柜集中在一角时不会显得很小)
      var THREE = GL.THREE, box = new THREE.Box3();
      Object.keys(BS.dc.rackObjs).forEach(function (id) { box.expandByObject(BS.dc.rackObjs[id]); });
      var ctr = new THREE.Vector3(), size = new THREE.Vector3();
      if (box.isEmpty()) { box.set(new THREE.Vector3(-300, 0, -300), new THREE.Vector3(300, 200, 300)); }
      box.getCenter(ctr); box.getSize(size);
      var R = Math.max(500, size.x, size.z) * 1.15;
      BS.dc.st.camera.position.set(ctr.x + R * 0.55, R * 0.6, ctr.z + R * 0.95);
      BS.dc.st.controls.target.set(ctr.x, 80, ctr.z);
      BS.dc.st.poke();
    }
  }).catch(function () {});
}
function dcTipHTMLFor(u, c) {
  // 大屏上机房的悬停提示: 不依赖 DC(编辑器)的状态
  var saveL = DC.layout, saveD = DC.devices;
  DC.layout = c.layout; DC.devices = c.devices;
  var html = dcTipHTML(u).replace(/<br><span style="color:#94a3b8;">单击[^<]*<\/span>/, u.dev || u.rack ? '<br><span style="color:#94a3b8;">单击查看详情</span>' : '');
  DC.layout = saveL; DC.devices = saveD;
  return html;
}
function bsShowTip(html, e) {
  var tip = byId('bsMapTip'), panel = byId('bsMapPanel'), r = panel.getBoundingClientRect(), s = BS.scale || 1;
  tip.innerHTML = html; tip.style.display = 'block';
  var x = (e.clientX - r.left) / s + 16, y = (e.clientY - r.top) / s + 16;
  if (x + tip.offsetWidth > panel.clientWidth) { x = x - tip.offsetWidth - 32; }
  tip.style.left = Math.max(0, x) + 'px'; tip.style.top = Math.max(0, Math.min(y, panel.clientHeight - tip.offsetHeight)) + 'px';
}
function bsHideTip() { byId('bsMapTip').style.display = 'none'; }
function bs3DDispose() {
  if (BS.topo) { tgDispose(BS.topo); BS.topo = null; }
  if (BS.dc) { dcDisposeScene(BS.dc); BS.dc = null; }
}

/* ---------- 站点地图(Canvas) ---------- */
function bsBuildMap(d) {
  var siteMode = (d.sites || []).length >= 2;
  if (BS.view === 'map') {
    byId('bsMapTitle').innerHTML = siteMode ? '站点态势<small>' + d.sites.length + ' 个站点 · ' + (d.site_links || []).length + ' 条站点间链路</small>'
      : '设备态势<small>只有一个站点, 按设备展示; 给设备设置 sysLocation 或在机柜视图里分配机柜后按站点展示</small>';
  }
  var nodes = [], byKey = {};
  if (siteMode) {
    d.sites.forEach(function (s) {
      var n = { key: s.name, name: s.name, devices: s.devices, online: s.online, alerts: s.alerts, x: s.x, y: s.y, hasPos: s.has_pos, members: s.names || [] };
      nodes.push(n); byKey[n.key] = n;
    });
  } else {
    (d.devices || []).forEach(function (v) {
      var n = { key: v.ip, name: v.name, devices: 1, online: v.alive ? 1 : 0, alerts: v.alerts, x: v.x, y: v.y, hasPos: v.has_pos, members: [v.ip + ' · CPU ' + v.cpu + '% · 内存 ' + v.mem + '%'].concat(v.note ? ['📝 ' + v.note] : []) };
      nodes.push(n); byKey[n.key] = n;
    });
  }
  var links = (siteMode ? d.site_links : d.dev_links) || [];
  links = links.filter(function (l) { return byKey[l.a] && byKey[l.b]; }).map(function (l) {
    return { a: byKey[l.a], b: byKey[l.b], ab: l.ab_mbps || 0, ba: l.ba_mbps || 0, speed: l.speed || 0, count: l.count || 1 };
  });
  var usePos = d.has_background && nodes.length && nodes.every(function (n) { return n.hasPos; });
  if (!usePos) {
    var deg = {};
    links.forEach(function (l) { deg[l.a.key] = (deg[l.a.key] || 0) + 1; deg[l.b.key] = (deg[l.b.key] || 0) + 1; });
    var sorted = nodes.slice().sort(function (a, b) { return (deg[b.key] || 0) - (deg[a.key] || 0) || naturalCmp(a.name, b.name); });
    var center = sorted.length > 2 && (deg[sorted[0].key] || 0) >= 2 ? sorted[0] : null;
    var ring = sorted.filter(function (n) { return n !== center; });
    ring.sort(function (a, b) { return naturalCmp(a.name, b.name); });
    if (center) { center.x = 50; center.y = 50; }
    ring.forEach(function (n, i) {
      var a = -Math.PI / 2 + i / Math.max(ring.length, 1) * Math.PI * 2;
      n.x = 50 + Math.cos(a) * (ring.length > 1 ? 36 : 0);
      n.y = 52 + Math.sin(a) * (ring.length > 1 ? 32 : 0);
    });
  }
  BS.usePos = usePos;
  BS.siteMode = siteMode;
  BS.nodes = nodes; BS.links = links;
  if (usePos && BS.imgVer !== topoImgVersion) {
    BS.imgVer = topoImgVersion;
    BS.img = new Image();
    BS.img.src = '/api/topology/background?v=' + topoImgVersion;
  }
  if (!usePos) { BS.img = null; BS.imgVer = -1; }
}
function bsUtilColor(u) {
  var P = BS.pal || {};
  if (u >= 0.8) { return P.hot || '#ef4444'; }
  if (u >= 0.5) { return P.warn || '#f59e0b'; }
  return P.accent || '#22d3ee';
}
function bsFrame(now) {
  BS.raf = 0;
  if (byId('bsRoot').style.display !== 'block') { return; }
  if (!document.hidden && BS.view === 'map') { bsDrawMap(now); }
  BS.raf = requestAnimationFrame(bsFrame);
}
function bsHexA(c, a) {
  // '#rrggbb' + 透明度
  var m = String(c || '').match(/^#([0-9a-f]{6})$/i);
  if (!m) { return c; }
  return c + ('0' + Math.round(a * 255).toString(16)).slice(-2);
}
function bsDrawMap(now) {
  var cv = byId('bsMap'), W = cv.clientWidth, H = cv.clientHeight;
  if (!W || !H) { return; }
  var P = BS.pal || {}, anim = !!bsPref('animations');
  var dpr = Math.min(2, window.devicePixelRatio || 1);
  if (cv.width !== Math.round(W * dpr)) { cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr); }
  var ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, W, H);
  // rAF 的时间戳是这一帧开始的时间, 可能比刚设的 BS.t0 还早一点, 负数会让脉冲圈半径变负(arc 报错)
  var t = anim ? Math.max(0, now - BS.t0) / 1000 : 0;
  // 地图区域: 赛博样式地图铺满全屏(面板浮在上面), 节点只摆在中间露出来的那块
  var sty = bsStyle(), ins = sty === 'cyber' ? { l: 440, r: 480, t: 150, b: 250 } : { l: 40, r: 40, t: 60, b: 50 };
  var px = function (n) { return { x: ins.l + n.x / 100 * (W - ins.l - ins.r), y: ins.t + n.y / 100 * (H - ins.t - ins.b) }; };
  var cx = ins.l + (W - ins.l - ins.r) / 2, cy = ins.t + (H - ins.t - ins.b) / 2, R = Math.min(W - ins.l - ins.r, H - ins.t - ins.b) * 0.5;
  BS.sweep = anim ? (t * 0.6) % (Math.PI * 2) : -1;
  if (BS.img && BS.img.complete && BS.img.naturalWidth) {
    ctx.globalAlpha = 0.35;
    ctx.drawImage(BS.img, ins.l, ins.t, W - ins.l - ins.r, H - ins.t - ins.b);
    ctx.globalAlpha = 1;
  } else {
    bsMapBackdrop(ctx, sty, W, H, cx, cy, R, t, anim, P);
  }
  BS.mapC = { x: cx, y: cy, R: R };
  var Pp = {};
  BS.nodes.forEach(function (n) { Pp[n.key] = px(n); });
  BS.links.forEach(function (l) {
    var a = Pp[l.a.key], b = Pp[l.b.key];
    var mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2, dx = b.x - a.x, dy = b.y - a.y, dist = Math.hypot(dx, dy) || 1;
    var bend = Math.min(120, dist * 0.22);
    var nx = -dy / dist, ny = dx / dist;
    if (ny > 0) { nx = -nx; ny = -ny; }
    var c = { x: mx + nx * bend, y: my + ny * bend };
    var util = l.speed ? Math.max(l.ab, l.ba) / l.speed : 0;
    var col = bsUtilColor(util);
    var w = 1.2 + Math.log(1 + l.speed / 1000) * 1.1;
    ctx.strokeStyle = col; ctx.globalAlpha = 0.18; ctx.lineWidth = w + 6;
    ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.quadraticCurveTo(c.x, c.y, b.x, b.y); ctx.stroke();
    ctx.globalAlpha = 0.85; ctx.lineWidth = w;
    ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.quadraticCurveTo(c.x, c.y, b.x, b.y); ctx.stroke();
    ctx.globalAlpha = 1;
    var dot = function (from, mbps, color) {
      if (mbps <= 0.01 || !anim) { return; }
      var cnt = Math.max(1, Math.min(10, Math.round(Math.log(1 + mbps) * 1.4)));
      var sp = 0.12 + Math.min(1, l.speed ? mbps / l.speed : 0.1) * 0.7;
      ctx.fillStyle = color; ctx.shadowColor = color; ctx.shadowBlur = 10;
      for (var i = 0; i < cnt; i++) {
        var s = (t * sp + i / cnt) % 1, u = from === a ? s : 1 - s;
        var qx = (1 - u) * (1 - u) * a.x + 2 * (1 - u) * u * c.x + u * u * b.x;
        var qy = (1 - u) * (1 - u) * a.y + 2 * (1 - u) * u * c.y + u * u * b.y;
        ctx.beginPath(); ctx.arc(qx, qy, 2.6, 0, Math.PI * 2); ctx.fill();
      }
      ctx.shadowBlur = 0;
    };
    dot(a, l.ab, P.strong || '#e0f2fe');
    dot(b, l.ba, P.accent2 || '#f0abfc');
    var total = l.ab + l.ba;
    if (total > 0.05) {
      ctx.font = '12px Consolas, monospace'; ctx.textAlign = 'center';
      ctx.fillStyle = 'rgba(0,0,0,.6)';
      var label = bsFmtMbps(total), tw = ctx.measureText(label).width + 10;
      var lx = 0.25 * a.x + 0.5 * c.x + 0.25 * b.x, ly = 0.25 * a.y + 0.5 * c.y + 0.25 * b.y;
      ctx.fillRect(lx - tw / 2, ly - 15, tw, 17);
      ctx.fillStyle = col; ctx.fillText(label, lx, ly - 2);
    }
  });
  BS.nodes.forEach(function (n) {
    var p = Pp[n.key];
    var r = 10 + Math.min(18, Math.sqrt(n.devices) * 5);
    var col = n.alerts > 0 ? (P.hot || '#ef4444') : (n.online < n.devices ? (P.warn || '#f59e0b') : (P.accent || '#22d3ee'));
    n._p = p; n._r = r;
    if ((n.alerts > 0 || n.online < n.devices) && anim) {
      for (var k2 = 0; k2 < 2; k2++) {
        var ph = ((t * 0.8) + k2 * 0.5) % 1;
        ctx.strokeStyle = col; ctx.globalAlpha = 1 - ph; ctx.lineWidth = 2;
        ctx.beginPath(); ctx.arc(p.x, p.y, r + ph * 34, 0, Math.PI * 2); ctx.stroke();
      }
      ctx.globalAlpha = 1;
    }
    bsMapNode(ctx, sty, p, r, col, P);
    ctx.textAlign = 'center';
    ctx.font = sty === 'mono' ? '600 14px Consolas, monospace' : sty === 'gold' ? '600 15px SimSun, Georgia, serif' : '600 15px "Microsoft YaHei", sans-serif';
    ctx.fillStyle = P.strong || '#e0f2fe'; ctx.shadowColor = '#000'; ctx.shadowBlur = 6;
    ctx.fillText(truncateLabel(n.name, 16), p.x, p.y + r + 20);
    ctx.font = '12px "Microsoft YaHei", sans-serif'; ctx.fillStyle = P.dim || '#7fb2dc';
    var sub = n.devices > 1 || BS.links.length === 0 ? n.online + '/' + n.devices + ' 在线' : (n.online ? '在线' : '离线');
    if (n.alerts > 0) { sub += ' · 告警 ' + n.alerts; }
    ctx.fillText(sub, p.x, p.y + r + 37);
    ctx.shadowBlur = 0;
  });
}
function bsMapHover(e) {
  if (BS.view !== 'map') { return; }
  var cv = byId('bsMap'), rect = cv.getBoundingClientRect();
  var s = rect.width / cv.clientWidth;
  var mx = (e.clientX - rect.left) / s, my = (e.clientY - rect.top) / s;
  var hit = null;
  BS.nodes.forEach(function (n) { if (n._p && Math.hypot(n._p.x - mx, n._p.y - my) <= n._r + 6) { hit = n; } });
  cv.classList.toggle('clickable', !!hit);
  if (!hit) { bsHideTip(); return; }
  bsShowTip('<b>' + esc(hit.name) + '</b><br>' + hit.online + '/' + hit.devices + ' 台在线' + (hit.alerts ? ' · <span style="color:#fca5a5;">' + hit.alerts + ' 条告警</span>' : '') +
    '<br><span style="color:#94a3b8;">' + hit.members.slice(0, 8).map(esc).join('<br>') + (hit.members.length > 8 ? '<br>…' : '') + '</span><br><span style="color:#94a3b8;">单击查看' + (BS.siteMode ? '站点设备' : '设备详情') + '</span>', e);
}
byId('bsMap').addEventListener('click', function (e) {
  if (BS.view !== 'map') { return; }
  var cv = byId('bsMap'), rect = cv.getBoundingClientRect(), s = rect.width / cv.clientWidth;
  var mx = (e.clientX - rect.left) / s, my = (e.clientY - rect.top) / s, hit = null;
  BS.nodes.forEach(function (n) { if (n._p && Math.hypot(n._p.x - mx, n._p.y - my) <= n._r + 6) { hit = n; } });
  if (!hit) { return; }
  if (BS.siteMode) { bsdOpen({ t: 'list', kind: 'devices', title: '站点 ' + hit.name, filter: { site: hit.key } }); } else { bsdOpen({ t: 'dev', ip: hit.key }); }
});
byId('bsMap').addEventListener('mousemove', bsMapHover);
// 点风险设备 / 告警 / 最忙端口: 在大屏里滑出这台设备的详情抽屉(见 drawer.js)
byId('bsBody').addEventListener('click', function (e) {
  var el = e.target.closest ? e.target.closest('.bs-click[data-ip]') : null;
  if (!el || !el.getAttribute('data-ip') || (e.target.closest('.bs-scroll') || {}).classList && byId('bsBody').querySelector('.dragging')) { return; }
  bsdOpen({ t: 'dev', ip: el.getAttribute('data-ip'), port: el.getAttribute('data-port') || '' });
});
byId('bsMap').addEventListener('mouseleave', bsHideTip);

/* ---------- 趋势 ---------- */
function bsDrawTrend(pts) {
  var cv = byId('bsTrend'), W = cv.clientWidth, H = cv.clientHeight;
  if (!W || !H) { return; }
  var P = BS.pal || {};
  var dpr = Math.min(2, window.devicePixelRatio || 1);
  cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
  var ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, W, H);
  if (pts.length < 2) {
    ctx.fillStyle = P.dim || '#5b87ad'; ctx.font = '14px "Microsoft YaHei"'; ctx.textAlign = 'center';
    ctx.fillText('趋势数据积累中(需要至少两轮巡检)', W / 2, H / 2);
    return;
  }
  var L = 70, R = 20, T = 12, B = 28;
  var max = 1;
  pts.forEach(function (p) { max = Math.max(max, p.in, p.out); });
  max *= 1.15;
  var x = function (i) { return L + i / (pts.length - 1) * (W - L - R); };
  var y = function (v) { return T + (1 - v / max) * (H - T - B); };
  ctx.strokeStyle = bsHexA(P.accent, 0.13); ctx.fillStyle = P.dim; ctx.font = '12px Consolas, monospace'; ctx.textAlign = 'right';
  for (var g = 0; g <= 4; g++) {
    var gy = T + g / 4 * (H - T - B);
    ctx.beginPath(); ctx.moveTo(L, gy); ctx.lineTo(W - R, gy); ctx.stroke();
    ctx.fillText(bsFmtMbps(max * (1 - g / 4)), L - 8, gy + 4);
  }
  ctx.textAlign = 'center';
  var fmt = function (ts) { var dt = new Date(ts * 1000); return ('0' + dt.getHours()).slice(-2) + ':' + ('0' + dt.getMinutes()).slice(-2); };
  for (var k = 0; k <= 6; k++) {
    var i = Math.round(k / 6 * (pts.length - 1));
    ctx.fillText(fmt(pts[i].ts), x(i), H - 8);
  }
  var series = function (key, color) {
    var grad = ctx.createLinearGradient(0, T, 0, H - B);
    grad.addColorStop(0, bsHexA(color, 0.4)); grad.addColorStop(1, bsHexA(color, 0));
    ctx.beginPath();
    pts.forEach(function (p, i) { if (i) { ctx.lineTo(x(i), y(p[key])); } else { ctx.moveTo(x(i), y(p[key])); } });
    ctx.lineTo(x(pts.length - 1), H - B); ctx.lineTo(x(0), H - B); ctx.closePath();
    ctx.fillStyle = grad; ctx.fill();
    ctx.beginPath();
    pts.forEach(function (p, i) { if (i) { ctx.lineTo(x(i), y(p[key])); } else { ctx.moveTo(x(i), y(p[key])); } });
    ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.shadowColor = color; ctx.shadowBlur = 8; ctx.stroke(); ctx.shadowBlur = 0;
  };
  if (bsStyle() === 'mono') {
    // 雷达样式: 分段柱状图(像老式示波器/频谱仪)
    var nb = Math.min(pts.length, 96), bw = (W - L - R) / nb;
    for (var bi = 0; bi < nb; bi++) {
      var pi = Math.floor(bi / nb * pts.length), pv = pts[pi], seg = 5;
      [['in', P.accent, 0], ['out', P.accent2, 0.5]].forEach(function (s2) {
        var hh = (pv[s2[0]] / max) * (H - T - B), x0 = L + bi * bw + s2[2] * bw * 0.5;
        ctx.fillStyle = s2[1];
        for (var yy = 0; yy < hh; yy += seg) { ctx.fillRect(x0 + 1, H - B - yy - seg + 1, bw * 0.5 - 2, seg - 2); }
      });
    }
  } else {
    series('in', P.accent || '#22d3ee');
    series('out', P.accent2 || '#a78bfa');
  }
  var last = pts[pts.length - 1];
  ctx.textAlign = 'left'; ctx.font = '13px "Microsoft YaHei"';
  ctx.fillStyle = P.accent; ctx.fillText('● 入 ' + bsFmtMbps(last.in), L + 10, T + 14);
  ctx.fillStyle = P.accent2; ctx.fillText('● 出 ' + bsFmtMbps(last.out), L + 150, T + 14);
}

/* ---------- 大屏上的快捷设置(只记在本机浏览器) ---------- */
function bsToggleCfg() {
  var box = byId('bsCfg');
  if (box.style.display === 'block') { box.style.display = 'none'; return; }
  var sel = function (id, opts, cur) { return '<select id="' + id + '">' + opts.map(function (o) { return '<option value="' + o[0] + '"' + (String(o[0]) === String(cur) ? ' selected' : '') + '>' + o[1] + '</option>'; }).join('') + '</select>'; };
  box.innerHTML = '<h3>⚙ 大屏设置 <span style="font-size:12px;color:var(--bs-dim);font-weight:400;">只对这台电脑生效</span></h3>' +
    '<label>样式</label><div class="bs-styles">' + BS_STYLES.map(function (s) { return '<button style="background:' + s[2] + ';" class="' + (bsPref('style') === s[0] ? 'on' : '') + '" onclick="bsSetPref(&quot;style&quot;,&quot;' + s[0] + '&quot;)">' + s[1] + '</button>'; }).join('') + '</div>' +
    '<label>中间主画面</label>' + sel('bsCfCenter', [['map', '站点态势地图'], ['topo3d', '3D 网络拓扑'], ['dc3d', '3D 数据中心机房'], ['carousel', '轮播(以上三个)']], bsPref('center')) +
    '<div class="row"><div><label>数据刷新(秒)</label><input type="number" id="bsCfRefresh" min="5" max="3600" value="' + bsPref('refresh_seconds') + '"></div>' +
    '<div><label>轮播间隔(秒)</label><input type="number" id="bsCfCarousel" min="10" max="3600" value="' + bsPref('carousel_seconds') + '"></div></div>' +
    '<div class="row"><div><label>列表滚动速度(0=不滚)</label><input type="number" id="bsCfScroll" min="0" max="300" value="' + bsPref('scroll_speed') + '"></div>' +
    '<div><label>风险 / 端口条数</label><div class="row"><input type="number" id="bsCfRisk" min="1" max="30" value="' + bsPref('risk_count') + '"><input type="number" id="bsCfHot" min="1" max="30" value="' + bsPref('hot_count') + '"></div></div></div>' +
    '<label class="sw"><input type="checkbox" id="bsCfAnim"' + (bsPref('animations') ? ' checked' : '') + '> 动画效果(飞线/粒子/扫描/背景特效)</label>' +
    '<div class="btns"><button class="bs-btn" onclick="bsApplyCfg()">应用</button><button class="bs-btn" onclick="bsResetCfg()">恢复全局设置</button><button class="bs-btn" onclick="byId(&quot;bsCfg&quot;).style.display=&quot;none&quot;">关闭</button></div>' +
    '<div class="hint">全局默认值(所有电脑共用, 值班室电视打开就是)在主页面「🛠️ 设置 → 大屏」里改。</div>';
  box.style.display = 'block';
}
function bsSetPref(k, v) { BS.prefs[k] = v; bsSavePrefs(); bsApplyStyle(); bsLegend(); bsToggleCfg(); bsToggleCfg(); }
function bsApplyCfg() {
  var num = function (id, lo, hi) { var n = parseInt(byId(id).value, 10); return isNaN(n) ? undefined : Math.max(lo, Math.min(hi, n)); };
  var oldCenter = bsPref('center'), oldAnim = bsPref('animations');
  BS.prefs.center = byId('bsCfCenter').value;
  BS.prefs.refresh_seconds = num('bsCfRefresh', 5, 3600);
  BS.prefs.carousel_seconds = num('bsCfCarousel', 10, 3600);
  BS.prefs.scroll_speed = num('bsCfScroll', 0, 300);
  BS.prefs.risk_count = num('bsCfRisk', 1, 30);
  BS.prefs.hot_count = num('bsCfHot', 1, 30);
  BS.prefs.animations = byId('bsCfAnim').checked;
  bsSavePrefs();
  bsAfterPrefs(oldCenter, oldAnim);
  byId('bsCfg').style.display = 'none';
}
function bsResetCfg() {
  var oldCenter = bsPref('center'), oldAnim = bsPref('animations');
  BS.prefs = {}; bsSavePrefs();
  bsAfterPrefs(oldCenter, oldAnim);
  byId('bsCfg').style.display = 'none';
}
function bsAfterPrefs(oldCenter, oldAnim) {
  bsApplyStyle();
  bsRestartTimer();
  if (oldAnim !== bsPref('animations')) { bs3DDispose(); }
  if (oldCenter !== bsPref('center') || oldAnim !== bsPref('animations')) { bsSetView(bsPref('center') === 'carousel' ? 'map' : bsPref('center')); }
  bsSetupCarousel();
  if (BS.data) { bsRender(BS.data); }
}

document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && byId('bsRoot').style.display === 'block' && !document.fullscreenElement) { if (byId('bsCfg').style.display === 'block') { byId('bsCfg').style.display = 'none'; } else if (bsdIsOpen()) { bsdClose(); } else { closeBigScreen(); } } });
if (location.hash === '#bigscreen') { setTimeout(openBigScreen, 300); }
