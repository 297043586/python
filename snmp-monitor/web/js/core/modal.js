/*
 * 弹窗管理(v13)。
 *
 * 以前每个窗口各自往 window 上挂"点遮罩关闭""按 Esc 关闭"的监听, boot 里还有一个按 Esc
 * 把所有窗口一起关掉的: 在拓扑里按 Esc 想关个右键菜单, 结果整个拓扑窗口都没了。
 *
 * 现在每个窗口注册一次:
 *   modalRegister('topoModal', closeTopology, { esc: () => 已经自己处理了返回 true })
 * 管理器按打开的先后维护一个栈(监听窗口的 display 变化, 各模块照旧用 style.display 开关):
 *   - Esc 只关最上面那个; 窗口里有下拉框 / 抽屉 / 菜单这类东西开着时先关它们(esc 回调或 preventDefault);
 *   - 点遮罩(窗口外面的暗色区域)关闭; 在窗口里按下鼠标拖到外面松开不算。
 *
 * v15 "返回上一级": 从一个窗口跳到另一个窗口(拓扑 → 设备详情、告警中心 → 设备 ...)时不再先把来源
 * 窗口关掉, 而是新窗口叠在上面(后打开的 z-index 更高), 标题栏自动出现「← 返回 xxx」;
 * 点 × / 按 Esc / 点返回都是关掉最上面这一层, 露出来的就是上一级。来源窗口是全屏的话先退出全屏,
 * 不然新窗口被挡在全屏元素后面看不见。
 */
const MODALS = new Map();
const modalStack = [];
let modalZ = 0;

// 注意: 这个文件在所有模块之前加载(别的模块加载时就会调 modalRegister), 这里只能依赖 byId 这种函数声明
function modalIsOpen(el) { return !!el && el.style.display !== 'none' && el.style.display !== ''; }
function modalRegister(id, close, opt) {
  const el = byId(id);
  if (!el || MODALS.has(id)) { return; }
  MODALS.set(id, { el, close, esc: opt && opt.esc });
  const sync = () => {
    const i = modalStack.indexOf(id), open = modalIsOpen(el);
    if (open && i < 0) {
      modalStack.push(id);
      el.style.zIndex = String(1000 + (++modalZ));
      // 全屏的是别的窗口(或它里面的画布)时, 新窗口会被挡住: 先退出全屏
      const cur = typeof fsElement === 'function' ? fsElement() : null;
      if (cur && !el.contains(cur) && modalStack.length > 1) { fsExit(); }
      modalBackSync();
    }
    if (!open && i >= 0) { modalStack.splice(i, 1); if (!modalStack.length) { modalZ = 0; } modalBackSync(); }
  };
  new MutationObserver(sync).observe(el, { attributes: true, attributeFilter: ['style'] });
  sync();
  if (!(opt && opt.noOverlay)) {
    let downOnOverlay = false;
    el.addEventListener('mousedown', (e) => { downOnOverlay = e.target === el; });
    el.addEventListener('click', (e) => { if (e.target === el && downOnOverlay) { close(); } });
  }
}
// 每个窗口标题栏里的「← 返回 上一个窗口」: 只有叠在别的窗口上面时才显示
function modalTitleOf(m) {
  const h = m && m.el.querySelector('.modal-header h2, .tpm-head h2, .modal-header h3, h2');
  let t = h ? h.innerText.replace(/\s+/g, ' ').trim() : '';
  t = t.replace(/^[^\u4e00-\u9fa5A-Za-z0-9]+/, '').replace(/\s*[(（].*$/, '');
  return t.length > 14 ? t.slice(0, 13) + '…' : (t || '上一级');
}
const MODAL_NO_BACK = { authModal: 1, pwModal: 1 }; // 登录框 / 改口令这种小对话框不需要"返回"
function modalBackSync() {
  modalStack.forEach((id, i) => {
    const m = MODALS.get(id), hdr = m.el.querySelector('.modal-header, .tpm-head');
    if (!hdr || MODAL_NO_BACK[id]) { return; }
    let b = hdr.querySelector(':scope > .modal-back');
    if (i === 0) { if (b) { b.remove(); } return; }
    if (!b) {
      b = document.createElement('button');
      b.type = 'button'; b.className = 'modal-back';
      b.addEventListener('click', (e) => { e.stopPropagation(); const top = MODALS.get(id); if (top) { top.close(); } });
      hdr.insertBefore(b, hdr.firstChild);
    }
    const prev = MODALS.get(modalStack[i - 1]);
    b.innerText = '← 返回' + modalTitleOf(prev);
    b.title = '关掉这个窗口, 回到「' + modalTitleOf(prev) + '」(Esc 也可以)';
  });
}
function modalTop() { return modalStack.length ? MODALS.get(modalStack[modalStack.length - 1]) : null; }
// 在 boot.js 里最后注册(window 冒泡阶段), 这样各模块自己的 Esc 处理都先执行过了
function modalEscape(e) {
  if (e.key !== 'Escape' || e.defaultPrevented) { return; }
  if (document.querySelector('.ms.open')) { return; } // 多选下拉开着: 这次 Esc 只关下拉
  const m = modalTop();
  if (!m) { return; }
  if (m.esc && m.esc(e)) { return; }
  m.close();
}
