/*
 * 设备详情: 性能趋势图。
 */
/* ---------- 趋势图 (支持悬停查看数值) ---------- */
detailPart({ tab: 3, show: function () { drawCharts(); } });
function drawCharts() {
  var d = currentDetail;
  if (!d) { return; }
  var data = d.trend_data || [];
  var labels = data.map(function (x) { return x.time; });
  drawLine(byId('cpuCanvas'), [
    { name: 'CPU', values: data.map(function (x) { return x.cpu; }), color: '#3b82f6' }
  ], labels, '%', 100, -1);
  drawLine(byId('memCanvas'), [
    { name: '内存', values: data.map(function (x) { return x.mem; }), color: '#8b5cf6' }
  ], labels, '%', 100, -1);
  drawLine(byId('macCanvas'), [
    { name: 'MAC 数', values: data.map(function (x) { return x.mac_count; }), color: '#f59e0b' }
  ], labels, '', 0, -1);
  drawLine(byId('bwCanvas'), [
    { name: '入向', values: data.map(function (x) { return x.in_mbps; }), color: '#3b82f6' },
    { name: '出向', values: data.map(function (x) { return x.out_mbps; }), color: '#10b981' }
  ], labels, 'M', 0, -1);
}

function drawLine(canvas, series, labels, unit, fixedMax, highlight) {
  if (!canvas) { return; }
  var parent = canvas.parentElement;
  var cssW = (parent && parent.clientWidth ? parent.clientWidth : 640) - 32;
  if (cssW < 240) { cssW = 240; }
  var cssH = 170;
  var dpr = window.devicePixelRatio || 1;
  canvas.width = Math.floor(cssW * dpr);
  canvas.height = Math.floor(cssH * dpr);
  canvas.style.width = cssW + 'px';
  canvas.style.height = cssH + 'px';
  var ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, cssW, cssH);

  var gridColor = getComputedStyle(document.body).getPropertyValue('--card-border') || '#e2e8f0';
  var dimColor = getComputedStyle(document.body).getPropertyValue('--fg-faint') || '#94a3b8';
  var pad = { top: 18, right: 14, bottom: 24, left: 46 };
  var w = cssW - pad.left - pad.right, h = cssH - pad.top - pad.bottom;

  var clean = series.map(function (s) {
    return {
      name: s.name, color: s.color,
      values: (s.values || []).map(function (v) { return (typeof v === 'number' && isFinite(v)) ? v : 0; })
    };
  });
  var count = clean.length ? clean[0].values.length : 0;

  canvas.chartMeta = {
    series: clean, labels: labels, unit: unit, fixedMax: fixedMax,
    pad: pad, w: w, h: h, count: count, cssW: cssW, step: 0
  };

  if (count === 0) {
    ctx.fillStyle = dimColor; ctx.font = '12px sans-serif'; ctx.textAlign = 'center';
    ctx.fillText('暂无采样数据', cssW / 2, cssH / 2);
    return;
  }

  var max = fixedMax > 0 ? fixedMax : 0;
  clean.forEach(function (s) {
    s.values.forEach(function (v) { if (v > max) { max = v; } });
  });
  if (max <= 0) { max = 10; }
  max = Math.ceil(max * 1.1);

  ctx.strokeStyle = gridColor; ctx.lineWidth = 1;
  for (var i = 0; i <= 4; i++) {
    var y = pad.top + (h / 4) * i;
    ctx.beginPath(); ctx.moveTo(pad.left, y); ctx.lineTo(cssW - pad.right, y); ctx.stroke();
    ctx.fillStyle = dimColor; ctx.font = '10px sans-serif'; ctx.textAlign = 'right';
    ctx.fillText(Math.round(max - (max / 4) * i) + unit, pad.left - 6, y + 3);
  }

  var step = count > 1 ? w / (count - 1) : 0;
  canvas.chartMeta.step = step;
  function px(i) { return pad.left + (count > 1 ? step * i : w / 2); }
  function py(v) { return pad.top + h - (v / max) * h; }

  if (highlight >= 0 && highlight < count) {
    ctx.strokeStyle = gridColor; ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(px(highlight), pad.top); ctx.lineTo(px(highlight), pad.top + h); ctx.stroke();
  }

  clean.forEach(function (s) {
    ctx.strokeStyle = s.color; ctx.lineWidth = 2.5; ctx.beginPath();
    s.values.forEach(function (v, i) {
      if (i === 0) { ctx.moveTo(px(i), py(v)); } else { ctx.lineTo(px(i), py(v)); }
    });
    ctx.stroke();
    if (count <= 60) {
      s.values.forEach(function (v, i) {
        ctx.fillStyle = getComputedStyle(document.body).getPropertyValue('--card-bg') || '#fff';
        ctx.beginPath(); ctx.arc(px(i), py(v), 3, 0, Math.PI * 2); ctx.fill();
        ctx.strokeStyle = s.color; ctx.lineWidth = 2; ctx.stroke();
      });
    }
    if (highlight >= 0 && highlight < count) {
      ctx.fillStyle = s.color; ctx.beginPath();
      ctx.arc(px(highlight), py(s.values[highlight]), 4.5, 0, Math.PI * 2); ctx.fill();
      ctx.strokeStyle = '#fff'; ctx.lineWidth = 2; ctx.stroke();
    }
  });

  ctx.fillStyle = dimColor; ctx.font = '10px sans-serif';
  ctx.textAlign = 'left'; ctx.fillText(labels[0] || '', pad.left, cssH - 8);
  if (count > 1) {
    ctx.textAlign = 'right'; ctx.fillText(labels[count - 1] || '', cssW - pad.right, cssH - 8);
  } else {
    ctx.textAlign = 'center';
    ctx.fillText('已采集 1 个点, 下一轮巡检后自动连成折线', cssW / 2, pad.top + h / 2);
  }
}

function bindHover(id) {
  var canvas = byId(id), tip = byId('tip-' + id);
  if (!canvas || !tip) { return; }
  canvas.addEventListener('mousemove', function (e) {
    var c = canvas.chartMeta;
    if (!c || !c.count) { tip.style.display = 'none'; return; }
    var rect = canvas.getBoundingClientRect();
    var x = e.clientX - rect.left;
    var idx = c.count > 1 ? Math.round((x - c.pad.left) / c.step) : 0;
    if (idx < 0) { idx = 0; }
    if (idx > c.count - 1) { idx = c.count - 1; }
    drawLine(canvas, c.series, c.labels, c.unit, c.fixedMax, idx);

    var html = '<b>' + esc(c.labels[idx] || '') + '</b>';
    c.series.forEach(function (s) {
      html += '<br><span style="color:' + s.color + ';">●</span> ' + esc(s.name) + ': <b>' +
              (Math.round(s.values[idx] * 100) / 100) + c.unit + '</b>';
    });
    tip.innerHTML = html;
    tip.style.display = 'block';
    var left = x + 18;
    if (left + tip.offsetWidth > c.cssW) { left = x - tip.offsetWidth - 14; }
    tip.style.left = Math.max(0, left) + 'px';
    tip.style.top = (e.clientY - rect.top - 10) + 'px';
  });
  canvas.addEventListener('mouseleave', function () {
    tip.style.display = 'none';
    var c = canvas.chartMeta;
    if (c) { drawLine(canvas, c.series, c.labels, c.unit, c.fixedMax, -1); }
  });
}
['cpuCanvas', 'memCanvas', 'macCanvas', 'bwCanvas'].forEach(bindHover);
window.addEventListener('resize', function () {
  if (currentTab === 3 && currentIP) { drawCharts(); }
});
