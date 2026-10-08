/*
 * 通用小工具(v13)。新代码都用这里的, 不再各写一份。
 *
 *   html`<b>${x}</b>`        模板里的插值自动转义; 已经是 HTML 的片段用 raw(s) 包一下
 *   debounce(fn, ms)          防抖(搜索框)
 *   rafThrottle(fn)           每帧最多执行一次(滚动 / 拖动)
 *   prefs(key, defaults)      记在浏览器里的界面偏好(读写都容错, 隐私模式下也不报错)
 *   fmtDur / fmtTime / fmtAgo 时长 / 时间格式化(速率格式化 fmtMbps / fmtSpeed 在 topology/tooltip.js)
 *   ipSortKey / cmpNatural    排序
 *   downloadText(name, text)  下载一个文本文件(CSV 等)
 */

class RawHTML {
  constructor(s) { this.s = String(s); }
  toString() { return this.s; }
}
function raw(s) { return new RawHTML(s == null ? '' : s); }
function htmlVal(v) {
  if (v instanceof RawHTML) { return v.s; }
  if (Array.isArray(v)) { return v.map(htmlVal).join(''); }
  if (v === false || v === null || v === undefined) { return ''; }
  return esc(v);
}
function html(strings, ...vals) {
  let out = strings[0];
  for (let i = 0; i < vals.length; i++) { out += htmlVal(vals[i]) + strings[i + 1]; }
  return raw(out);
}

function debounce(fn, ms) {
  let t = 0;
  const d = function (...a) { clearTimeout(t); t = setTimeout(() => fn.apply(this, a), ms); };
  d.cancel = () => clearTimeout(t);
  return d;
}
function rafThrottle(fn) {
  let pending = false, args = null;
  return function (...a) {
    args = a;
    if (pending) { return; }
    pending = true;
    requestAnimationFrame(() => { pending = false; fn.apply(this, args); });
  };
}

// 界面偏好: 读出来是 defaults 的一份拷贝, 改完调 save()
function prefs(key, defaults) {
  let v = {};
  try { v = JSON.parse(localStorage.getItem(key) || '{}') || {}; } catch (e) { v = {}; }
  const obj = Object.assign({}, defaults, v);
  Object.defineProperty(obj, 'save', {
    enumerable: false,
    value() { try { localStorage.setItem(key, JSON.stringify(obj)); } catch (e) { /* 隐私模式 */ } }
  });
  return obj;
}

function fmtDur(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  if (sec < 60) { return sec + ' 秒'; }
  if (sec < 3600) { return Math.floor(sec / 60) + ' 分钟'; }
  if (sec < 86400) { const h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60); return h + ' 小时' + (m ? ' ' + m + ' 分' : ''); }
  const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600);
  return d + ' 天' + (h ? ' ' + h + ' 小时' : '');
}
function pad2(n) { return (n < 10 ? '0' : '') + n; }
// unix 秒 -> "09-28 13:05"(今年) / "2025-09-28 13:05"
function fmtTime(ts, withSec) {
  if (!ts) { return '-'; }
  const d = new Date(ts * 1000), now = new Date();
  const hm = pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + (withSec ? ':' + pad2(d.getSeconds()) : '');
  const md = pad2(d.getMonth() + 1) + '-' + pad2(d.getDate());
  return (d.getFullYear() === now.getFullYear() ? md : d.getFullYear() + '-' + md) + ' ' + hm;
}
function fmtDay(d) { return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()); }
function fmtAgo(ts) {
  const s = Date.now() / 1000 - ts;
  if (s < 60) { return '刚刚'; }
  if (s < 3600) { return Math.floor(s / 60) + ' 分钟前'; }
  if (s < 86400) { return Math.floor(s / 3600) + ' 小时前'; }
  return Math.floor(s / 86400) + ' 天前';
}

const ipSortCache = new Map();
function ipSortKey(ip) {
  let k = ipSortCache.get(ip);
  if (k === undefined) {
    const p = String(ip || '').split('.');
    k = p.length === 4 && p.every((x) => /^\d+$/.test(x)) ? p.map((x) => ('00' + x).slice(-3)).join('.') : String(ip || '');
    if (ipSortCache.size > 5000) { ipSortCache.clear(); }
    ipSortCache.set(ip, k);
  }
  return k;
}
const natCollator = new Intl.Collator('zh-Hans-CN', { numeric: true, sensitivity: 'base' });
function cmpNatural(a, b) { return natCollator.compare(String(a == null ? '' : a), String(b == null ? '' : b)); }

// MAC 搜索统一忽略大小写和常见分隔符(: . - 空格)，但不把 IPv4 地址当成 MAC。
function macSearchKey(value) {
  const s = String(value == null ? '' : value).trim().toLowerCase();
  if (!s || /^\d{1,3}(?:\.\d{1,3}){3}$/.test(s) || !/^[0-9a-f:.\-\s]+$/.test(s)) { return ''; }
  const key = s.replace(/[^0-9a-f]/g, '');
  return key.length >= 4 && key.length <= 12 ? key : '';
}
function macSearchMatches(mac, query) {
  const q = macSearchKey(query), m = macSearchKey(mac);
  return !!(q && m && m.indexOf(q) >= 0);
}

function downloadText(name, text, type) {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([text], { type: type || 'text/csv;charset=utf-8;' }));
  a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 2000);
}
// CSV: 带 BOM, Excel 直接打开不乱码
function toCSV(header, rows) {
  const cell = (v) => '"' + String(v === null || v === undefined ? '' : v).replace(/"/g, '""') + '"';
  return String.fromCharCode(0xFEFF) + [header].concat(rows).map((r) => r.map(cell).join(',')).join('\n') + '\n';
}

// 同一个 key 的请求还在飞的时候, 后发的覆盖先发的(先发的结果丢掉), 快速切换筛选时不会闪回旧数据
const latestReq = {};
function latest(key, promise) {
  const id = (latestReq[key] || 0) + 1;
  latestReq[key] = id;
  return promise.then((v) => {
    if (latestReq[key] !== id) { return new Promise(() => {}); }
    return v;
  });
}

// 状态点 / 小标签
function dot(color, title) { return raw('<i class="sdot" style="background:' + color + '"' + (title ? ' title="' + esc(title) + '"' : '') + '></i>'); }
