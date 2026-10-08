/*
 * 浮动小窗: 机柜 / 3D 机房里点设备弹出来的方框以前固定在点击位置, 不能挪、不能放大。
 * floatBox(el, container, key) 给它加上:
 *   - 顶部拖动条(按住拖动, 限制在 container 里);
 *   - 右下角拖拽改大小(CSS resize), 改过的大小按 key 记在浏览器里, 下次打开还是这么大;
 *   - ⤢ 按钮: 放大到几乎铺满 container / 还原。
 * 内容被整块重写(innerHTML)后拖动条会丢, 再调一次 floatBox 就补上。
 */
function floatBox(el, container, key) {
  if (!el) { return; }
  if (!el.querySelector(':scope > .fb-grip')) { floatGrip(el, container); }
  if (el.dataset.fb) { return; }
  el.dataset.fb = '1';
  el.classList.add('fb');
  try {
    var sz = JSON.parse(localStorage.getItem('fb:' + key) || 'null');
    if (sz && sz.w > 200 && sz.h > 100) { el.style.width = sz.w + 'px'; el.style.height = sz.h + 'px'; }
  } catch (e) { /* 读不到就用默认大小 */ }
  // 用户拖右下角改了大小(浏览器会写 inline 的 width/height)才记下来; 内容变化撑高不算
  if (typeof ResizeObserver !== 'undefined') {
    new ResizeObserver(debounce(function () {
      if (el.classList.contains('fb-big') || !el.style.height) { return; }
      try { localStorage.setItem('fb:' + key, JSON.stringify({ w: el.offsetWidth, h: el.offsetHeight })); } catch (e) { /* 存不了就算了 */ }
    }, 300)).observe(el);
  }
}

function floatGrip(el, container) {
  var grip = document.createElement('div');
  grip.className = 'fb-grip';
  grip.innerHTML = '<span title="按住拖动">⋮⋮</span><button type="button" class="fb-max" title="放大 / 还原">⤢</button>';
  el.insertBefore(grip, el.firstChild);
  grip.addEventListener('pointerdown', function (e) {
    if (e.target.closest('.fb-max')) { return; }
    e.preventDefault(); e.stopPropagation();
    var r = container.getBoundingClientRect(), sx = e.clientX, sy = e.clientY, ox = el.offsetLeft, oy = el.offsetTop;
    var mv = function (ev) {
      el.style.left = Math.max(0, Math.min(ox + ev.clientX - sx, r.width - 80)) + 'px';
      el.style.top = Math.max(0, Math.min(oy + ev.clientY - sy, r.height - 40)) + 'px';
    };
    var up = function () { document.removeEventListener('pointermove', mv); document.removeEventListener('pointerup', up); };
    document.addEventListener('pointermove', mv);
    document.addEventListener('pointerup', up);
  });
  grip.querySelector('.fb-max').addEventListener('click', function (e) {
    e.stopPropagation();
    if (el.classList.contains('fb-big')) {
      el.classList.remove('fb-big');
      var s = el._fbPrev || {};
      el.style.left = s.left || ''; el.style.top = s.top || ''; el.style.width = s.width || ''; el.style.height = s.height || '';
    } else {
      el._fbPrev = { left: el.style.left, top: el.style.top, width: el.style.width, height: el.style.height };
      var r = container.getBoundingClientRect();
      el.classList.add('fb-big');
      el.style.left = '8px'; el.style.top = '8px';
      el.style.width = Math.max(300, r.width - 16) + 'px'; el.style.height = Math.max(200, r.height - 16) + 'px';
    }
  });
}
