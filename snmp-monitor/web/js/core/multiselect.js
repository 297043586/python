/*
 * 前端通用扩展: 多选筛选下拉(替换原来的单选 <select>) + 系统设置窗口 + 全局显示参数。
 */
/* ================= 多选筛选下拉 ================= */
// 用法: <div class="ms" id="xxx" data-all="全部设备" data-label="设备" data-change="renderXxx"></div>
// msOptions(id, [{v, t, s}]) 填选项(s = 右侧小字); msSet(id) 取已选(null = 没选 = 全部);
// 同一个下拉里勾多个 = "任一满足", 不同下拉之间 = "同时满足"。
var MSEL = {};
function msInit(el) {
  if (!el || !el.id || MSEL[el.id]) { return; }
  var st = { el: el, opts: [], sel: {}, all: el.getAttribute('data-all') || '全部', label: el.getAttribute('data-label') || '', change: el.getAttribute('data-change') || '' };
  MSEL[el.id] = st;
  el.innerHTML = '<button type="button" class="ms-btn"><span class="ms-text"></span><span class="ms-caret">▾</span></button>' +
    '<div class="ms-pop"><input class="ms-q" placeholder="筛选选项..."><div class="ms-acts"><a data-a="all">全选</a><a data-a="none">清空(=全部)</a><a data-a="inv">反选</a></div><div class="ms-list"></div></div>';
  el.querySelector('.ms-btn').addEventListener('click', function (e) {
    e.stopPropagation();
    var open = !el.classList.contains('open');
    msCloseAll();
    if (open) { el.classList.add('open'); var q = el.querySelector('.ms-q'); q.value = ''; msRenderList(el.id); if (st.opts.length > 8) { q.focus(); } }
  });
  el.querySelector('.ms-pop').addEventListener('click', function (e) { e.stopPropagation(); });
  el.querySelector('.ms-q').addEventListener('input', function () { msRenderList(el.id); });
  el.querySelectorAll('.ms-acts a').forEach(function (a) {
    a.addEventListener('click', function () {
      var act = a.getAttribute('data-a'), vis = msVisible(el.id);
      if (act === 'none') { st.sel = {}; }
      vis.forEach(function (o) {
        if (act === 'all') { st.sel[o.v] = true; }
        if (act === 'inv') { if (st.sel[o.v]) { delete st.sel[o.v]; } else { st.sel[o.v] = true; } }
      });
      msRenderList(el.id); msChanged(el.id);
    });
  });
  el.querySelector('.ms-list').addEventListener('change', function (e) {
    var cb = e.target;
    if (!cb || cb.type !== 'checkbox') { return; }
    var v = cb.getAttribute('data-v');
    if (cb.checked) { st.sel[v] = true; } else { delete st.sel[v]; }
    msChanged(el.id);
  });
  msLabel(el.id);
}
function msCloseAll() { document.querySelectorAll('.ms.open').forEach(function (x) { x.classList.remove('open'); }); }
document.addEventListener('click', msCloseAll);
// 有下拉开着时这次 Esc 只关下拉(preventDefault 告诉弹窗管理器别再关窗口)
document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && document.querySelector('.ms.open')) { msCloseAll(); e.preventDefault(); } });
function msVisible(id) {
  var st = MSEL[id], q = (st.el.querySelector('.ms-q').value || '').trim().toLowerCase();
  return st.opts.filter(function (o) { return !q || (String(o.t) + ' ' + String(o.v) + ' ' + (o.s || '')).toLowerCase().indexOf(q) >= 0; });
}
function msRenderList(id) {
  var st = MSEL[id];
  if (!st) { return; }
  var vis = msVisible(id);
  var shown = vis.slice(0, 600);
  st.el.querySelector('.ms-list').innerHTML = shown.length ? shown.map(function (o) {
    return '<label><input type="checkbox" data-v="' + esc(o.v) + '"' + (st.sel[o.v] ? ' checked' : '') + '>' + esc(o.t) + (o.s ? '<small>' + esc(o.s) + '</small>' : '') + '</label>';
  }).join('') + (vis.length > shown.length ? '<div class="ms-empty">还有 ' + (vis.length - shown.length) + ' 项, 请输入关键字缩小范围</div>' : '')
    : '<div class="ms-empty">' + (st.opts.length ? '没有匹配的选项' : '暂无可选项') + '</div>';
  st.el.querySelector('.ms-q').style.display = st.opts.length > 8 ? '' : 'none';
}
function msLabel(id) {
  var st = MSEL[id];
  if (!st) { return; }
  var keys = Object.keys(st.sel), text = st.all;
  if (keys.length === 1) {
    var o = st.opts.filter(function (x) { return String(x.v) === keys[0]; })[0];
    text = (st.label ? st.label + ': ' : '') + (o ? o.t : keys[0]);
  } else if (keys.length > 1) {
    text = (st.label || '已选') + ': ' + keys.length + ' 项';
  }
  st.el.querySelector('.ms-text').innerText = text;
  st.el.classList.toggle('on', keys.length > 0);
  st.el.querySelector('.ms-btn').title = keys.length ? st.opts.filter(function (x) { return st.sel[x.v]; }).map(function (x) { return x.t; }).join('\n') : st.all;
}
function msChanged(id) {
  var st = MSEL[id];
  msLabel(id);
  if (st.change && typeof window[st.change] === 'function') { window[st.change](); }
}
function msOptions(id, list) {
  var st = MSEL[id];
  if (!st) { msInit(byId(id)); st = MSEL[id]; }
  if (!st) { return; }
  st.opts = list || [];
  var valid = {};
  st.opts.forEach(function (o) { valid[o.v] = true; });
  Object.keys(st.sel).forEach(function (k) { if (!valid[k]) { delete st.sel[k]; } });
  msRenderList(id); msLabel(id);
}
function msSet(id) {
  var st = MSEL[id];
  if (!st) { return null; }
  var keys = Object.keys(st.sel);
  if (!keys.length) { return null; }
  var out = {};
  keys.forEach(function (k) { out[k] = true; });
  return out;
}
function msSetValues(id, vals) {
  var st = MSEL[id];
  if (!st) { return; }
  st.sel = {};
  (vals || []).forEach(function (v) { st.sel[v] = true; });
  msRenderList(id); msLabel(id);
}
function msClearAll(ids) { ids.forEach(function (id) { if (MSEL[id]) { MSEL[id].sel = {}; msRenderList(id); msLabel(id); } }); }
document.querySelectorAll('.ms').forEach(msInit);
// 固定选项的几个下拉
msOptions('galertLevelFilter', [{ v: 'critical', t: '🚨 致命故障' }, { v: 'warning', t: '⚠️ 风险隐患' }]);
msOptions('galertStatusFilter', [{ v: 'open', t: '未处理' }, { v: 'recovered', t: '已恢复未确认' }, { v: 'done', t: '已处理' }]);
msSetValues('galertStatusFilter', ['open']);
