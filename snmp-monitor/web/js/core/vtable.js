/*
 * VTable: 虚拟滚动表格(v13)。
 *
 * MAC 总表 / 告警表动辄上万行, 一次性塞进 DOM 会卡死(打开要好几秒, 滚动掉帧)。这里只画
 * 可视区域附近的几十行, 其余用一个等高的占位撑开滚动条; 数据再多滚动也是流畅的。
 *
 *   const t = new VTable(host, {
 *     columns: [{ key, title, width: '160px' | '1fr' | 'minmax(120px,2fr)', sort: row => 值 | true, render: row => html, cls, align, tip }],
 *     rowHeight: 40,                 // 行高固定(虚拟滚动的前提); 两行内容的行给 46 左右
 *     rowClass: row => '',           // 整行附加 class
 *     onRow(row, ev),                // 点行
 *     onAction(act, row, el, ev),    // 点行里带 data-act 的元素
 *     group: { by: row => key, label: (key, rows) => html, order: (a, b) => n } | null,
 *     sort: { key, asc },            // 初始排序
 *     empty: 'html',                 // 没有数据时显示
 *     onEnd()                        // 滚到底(无限加载)
 *   });
 *   t.setRows(rows); t.setGroup(g); t.setSort(key, asc); t.collapseAll(true); t.refresh();
 */
class VTable {
  constructor(host, opt) {
    this.host = host;
    this.opt = Object.assign({ rowHeight: 40, groupHeight: 38, overscan: 8, empty: '没有数据' }, opt);
    this.rows = [];
    this.items = [];
    this.offsets = [0];
    this.collapsed = new Set(this.opt.collapsed || []);
    this.sort = this.opt.sort || null;
    this.group = this.opt.group || null;
    host.classList.add('vt');
    host.innerHTML = '<div class="vt-body"><div class="vt-head"></div><div class="vt-canvas"><div class="vt-win"></div></div><div class="vt-empty"></div></div>';
    this.body = host.querySelector('.vt-body');
    this.head = host.querySelector('.vt-head');
    this.canvas = host.querySelector('.vt-canvas');
    this.win = host.querySelector('.vt-win');
    this.emptyEl = host.querySelector('.vt-empty');
    this.setColumns(this.opt.columns);
    this.body.addEventListener('scroll', rafThrottle(() => this.paint()), { passive: true });
    this.body.addEventListener('click', (e) => this.onClick(e));
    if (window.ResizeObserver) { this.ro = new ResizeObserver(rafThrottle(() => this.paint(true))); this.ro.observe(this.body); }
  }
  setColumns(cols) {
    this.cols = cols;
    this.tpl = cols.map((c) => c.width || '1fr').join(' ');
    const minW = cols.reduce((s, c) => s + (c.min || parseInt(c.width, 10) || 120), 0);
    this.head.style.gridTemplateColumns = this.tpl;
    this.head.style.minWidth = this.canvas.style.minWidth = minW + 'px';
    this.renderHead();
    this.paint(true);
  }
  renderHead() {
    const s = this.sort;
    this.head.innerHTML = this.cols.map((c) => {
      const on = s && s.key === c.key;
      const ic = c.sort ? '<span class="vt-si">' + (on ? (s.asc ? '▲' : '▼') : '⇅') + '</span>' : '';
      return '<div class="vt-th' + (c.sort ? ' sortable' : '') + (on ? ' on' : '') + (c.align ? ' a-' + c.align : '') + '"' +
        (c.sort ? ' data-sort="' + esc(c.key) + '"' : '') + (c.tip ? ' title="' + esc(c.tip) + '"' : '') + '>' + htmlVal(c.title) + ic + '</div>';
    }).join('');
  }
  setRows(rows) { this.rows = rows || []; this.rebuild(); }
  setGroup(g) { this.group = g; this.rebuild(); }
  setSort(key, asc) { this.sort = key ? { key, asc: asc !== false } : null; this.renderHead(); this.rebuild(); }
  collapseAll(on) {
    this.collapsed.clear();
    if (on) { this.items.forEach((it) => { if (it.t === 'g') { this.collapsed.add(it.key); } }); }
    this.rebuild();
  }
  sorted(list) {
    const s = this.sort;
    if (!s) { return list; }
    const col = this.cols.find((c) => c.key === s.key);
    if (!col || !col.sort) { return list; }
    const fn = col.sort === true ? (r) => r[col.key] : col.sort;
    const keyed = list.map((r, i) => ({ r, k: fn(r), i }));
    keyed.sort((a, b) => {
      let c;
      if (typeof a.k === 'number' && typeof b.k === 'number') { c = a.k - b.k; } else { c = cmpNatural(a.k, b.k); }
      return (c || a.i - b.i) * (s.asc ? 1 : -1);
    });
    return keyed.map((x) => x.r);
  }
  rebuild() {
    const items = [];
    if (this.group) {
      const by = new Map();
      this.rows.forEach((r) => {
        const k = this.group.by(r);
        const key = k === undefined || k === null ? '' : String(k);
        if (!by.has(key)) { by.set(key, []); }
        by.get(key).push(r);
      });
      const keys = Array.from(by.keys());
      keys.sort(this.group.order ? (a, b) => this.group.order(a, b, by.get(a), by.get(b)) : (a, b) => (a === '') - (b === '') || cmpNatural(a, b));
      keys.forEach((k) => {
        const list = by.get(k);
        items.push({ t: 'g', key: k, rows: list });
        if (!this.collapsed.has(k)) { this.sorted(list).forEach((r) => items.push({ t: 'r', row: r })); }
      });
    } else {
      this.sorted(this.rows).forEach((r) => items.push({ t: 'r', row: r }));
    }
    this.items = items;
    const off = new Array(items.length + 1);
    off[0] = 0;
    for (let i = 0; i < items.length; i++) { off[i + 1] = off[i] + (items[i].t === 'g' ? this.opt.groupHeight : this.opt.rowHeight); }
    this.offsets = off;
    this.canvas.style.height = off[items.length] + 'px';
    this.emptyEl.innerHTML = this.rows.length ? '' : htmlVal(raw(this.opt.empty));
    this.emptyEl.style.display = this.rows.length ? 'none' : '';
    this.paint(true);
  }
  // 第一个底边在 y 之下的条目
  indexAt(y) {
    const off = this.offsets;
    let lo = 0, hi = this.items.length;
    while (lo < hi) { const m = (lo + hi) >> 1; if (off[m + 1] <= y) { lo = m + 1; } else { hi = m; } }
    return lo;
  }
  paint(force) {
    const top = Math.max(0, this.body.scrollTop - this.head.offsetHeight), h = this.body.clientHeight;
    const pad = this.opt.overscan * this.opt.rowHeight;
    const a = Math.max(0, this.indexAt(top - pad)), b = Math.min(this.items.length, this.indexAt(top + h + pad) + 1);
    if (!force && a === this.pa && b === this.pb) { return; }
    this.pa = a; this.pb = b;
    let out = '';
    for (let i = a; i < b; i++) { out += this.itemHTML(this.items[i], i); }
    this.win.style.transform = 'translateY(' + this.offsets[a] + 'px)';
    this.win.innerHTML = out;
    if (this.opt.onEnd && this.items.length && b >= this.items.length - 2 && !this.endFired) {
      this.endFired = true;
      Promise.resolve(this.opt.onEnd()).finally(() => { this.endFired = false; });
    }
  }
  itemHTML(it, i) {
    if (it.t === 'g') {
      const col = this.collapsed.has(it.key);
      const label = this.group.label ? htmlVal(this.group.label(it.key, it.rows)) : esc(it.key || '(未分组)') + ' <small>' + it.rows.length + '</small>';
      return '<div class="vt-grp' + (col ? ' col' : '') + '" data-g="' + esc(it.key) + '" style="height:' + this.opt.groupHeight + 'px"><span class="vt-caret">' + (col ? '▸' : '▾') + '</span>' + label + '</div>';
    }
    const r = it.row;
    const cls = this.opt.rowClass ? this.opt.rowClass(r) : '';
    let cells = '';
    for (const c of this.cols) {
      cells += '<div class="vt-td' + (c.cls ? ' ' + c.cls : '') + (c.align ? ' a-' + c.align : '') + '">' + (c.render ? htmlVal(c.render(r)) : esc(r[c.key])) + '</div>';
    }
    return '<div class="vt-row' + (cls ? ' ' + cls : '') + '" data-i="' + i + '" style="grid-template-columns:' + this.tpl + ';height:' + this.opt.rowHeight + 'px">' + cells + '</div>';
  }
  onClick(e) {
    const th = e.target.closest('.vt-th[data-sort]');
    if (th) {
      const k = th.getAttribute('data-sort');
      const s = this.sort;
      this.setSort(k, s && s.key === k ? !s.asc : true);
      if (this.opt.onSort) { this.opt.onSort(this.sort); }
      return;
    }
    const g = e.target.closest('.vt-grp');
    if (g) {
      const k = g.getAttribute('data-g');
      if (this.collapsed.has(k)) { this.collapsed.delete(k); } else { this.collapsed.add(k); }
      const keep = this.body.scrollTop;
      this.rebuild();
      this.body.scrollTop = keep;
      if (this.opt.onCollapse) { this.opt.onCollapse(Array.from(this.collapsed)); }
      return;
    }
    const rowEl = e.target.closest('.vt-row');
    if (!rowEl) { return; }
    const it = this.items[+rowEl.getAttribute('data-i')];
    if (!it) { return; }
    const act = e.target.closest('[data-act]');
    if (act && rowEl.contains(act)) {
      e.stopPropagation();
      if (this.opt.onAction) { this.opt.onAction(act.getAttribute('data-act'), it.row, act, e); }
      return;
    }
    if (this.opt.onRow) { this.opt.onRow(it.row, e, rowEl); }
  }
  scrollToTop() { this.body.scrollTop = 0; }
  // 当前(排序 + 分组 + 折叠之后)看得到的行, 导出 CSV 用
  visibleRows() {
    if (!this.group) { return this.sorted(this.rows); }
    return this.items.filter((it) => it.t === 'r').map((it) => it.row);
  }
  refresh() { this.paint(true); }
  destroy() { if (this.ro) { this.ro.disconnect(); } this.host.innerHTML = ''; }
}

/* ---------- 分组条的小统计: 数量 + 状态计数 ---------- */
function vtGroupLabel(title, rows, extra) {
  return raw('<b>' + esc(title || '(未分组)') + '</b><span class="vt-gn">' + rows.length + '</span>' + (extra ? '<span class="vt-gx">' + htmlVal(extra) + '</span>' : ''));
}
