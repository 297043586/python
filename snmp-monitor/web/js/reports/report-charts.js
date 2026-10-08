/*
 * 巡检报告的图表(直接画成 SVG, 打印 / 导出 PDF / 导出 HTML 都清楚)和「导出 HTML」。
 * 后端章节里的 chart 字段见 report.go 的 ReportChart。
 */
function truncLabel(s, n) { s = String(s); return s.length > n ? s.slice(0, n - 1) + '…' : s; }

function reportChartSVG(ch) {
  var labels = ch.labels || [], vals = ch.values || [], extra = ch.extra || [], unit = ch.unit || '', colors = ch.colors || [];
  if (!labels.length) { return ''; }
  var fmt = function (v) { return (Math.round(v * 10) / 10) + unit; };
  if (ch.type === 'hbar') {
    var max = ch.max || Math.max.apply(null, vals.concat([1]));
    var rowH = 22, labW = 170, barW = 420, W = labW + barW + 70, H = labels.length * rowH + 6;
    var out = '<svg class="rpt-chart" viewBox="0 0 ' + W + ' ' + H + '" width="100%" style="max-width:' + W + 'px">';
    labels.forEach(function (l, i) {
      var y = i * rowH + 3, w = Math.max(1, vals[i] / max * barW);
      // 百分比类: ≥85 红, ≥70 橙, 其余蓝
      var c = colors[i] || (ch.max === 100 ? (vals[i] >= 85 ? '#ef4444' : (vals[i] >= 70 ? '#f59e0b' : '#3b82f6')) : '#3b82f6');
      out += '<text x="' + (labW - 6) + '" y="' + (y + 14) + '" text-anchor="end" class="lb">' + esc(truncLabel(l, 22)) + '</text>' +
        '<rect x="' + labW + '" y="' + (y + 3) + '" width="' + barW + '" height="14" rx="3" class="track"/>' +
        '<rect x="' + labW + '" y="' + (y + 3) + '" width="' + w.toFixed(1) + '" height="14" rx="3" fill="' + c + '"/>' +
        '<text x="' + (labW + barW + 6) + '" y="' + (y + 14) + '" class="vl">' + fmt(vals[i]) + '</text>';
    });
    return out + '</svg>';
  }
  // bar(竖柱) / stack(两组堆叠)
  var tot = vals.map(function (v, i) { return v + (extra[i] || 0); });
  var max2 = ch.max || Math.max.apply(null, tot.concat([1]));
  var colW = Math.max(28, Math.min(72, Math.floor(700 / labels.length))), H2 = 170, base = 140, W2 = labels.length * colW + 20;
  var svg = '<svg class="rpt-chart" viewBox="0 0 ' + W2 + ' ' + H2 + '" width="100%" style="max-width:' + W2 + 'px">';
  labels.forEach(function (l, i) {
    var x = 10 + i * colW, bw = colW * 0.6, bx = x + (colW - bw) / 2;
    var h1 = vals[i] / max2 * 110, h2 = (extra[i] || 0) / max2 * 110;
    var c1 = ch.type === 'stack' ? (colors[0] || '#ef4444') : (colors[i] || '#3b82f6');
    svg += '<rect x="' + bx.toFixed(1) + '" y="' + (base - h1).toFixed(1) + '" width="' + bw.toFixed(1) + '" height="' + h1.toFixed(1) + '" fill="' + c1 + '"/>';
    if (ch.type === 'stack' && h2 > 0) {
      svg += '<rect x="' + bx.toFixed(1) + '" y="' + (base - h1 - h2).toFixed(1) + '" width="' + bw.toFixed(1) + '" height="' + h2.toFixed(1) + '" fill="' + (colors[1] || '#f59e0b') + '"/>';
    }
    svg += '<text x="' + (x + colW / 2) + '" y="' + (base - h1 - h2 - 4).toFixed(1) + '" text-anchor="middle" class="vl">' + (tot[i] ? fmt(tot[i]) : '') + '</text>' +
      '<text x="' + (x + colW / 2) + '" y="' + (base + 15) + '" text-anchor="middle" class="lb">' + esc(truncLabel(l, colW >= 60 ? 11 : 6)) + '</text>';
  });
  return svg + '<line x1="6" y1="' + base + '" x2="' + (W2 - 6) + '" y2="' + base + '" class="axis"/></svg>';
}

// 导出成一个独立的 HTML 文件(浅色、内联样式、带图表), 发邮件 / 归档用
function exportReportHTML() {
  var r = rptState.current;
  if (!r) { return; }
  var doc = byId('rptView').cloneNode(true);
  doc.querySelectorAll('.no-print').forEach(function (e) { e.remove(); });
  var rules = Array.prototype.map.call(document.styleSheets, function (sh) {
    try {
      return Array.prototype.filter.call(sh.cssRules, function (x) { return x.selectorText && /\.rpt-/.test(x.selectorText); })
        .map(function (x) { return x.cssText; }).join('\n');
    } catch (e) { return ''; }
  }).join('\n');
  var css = ':root{--good:#10b981;--warn:#f59e0b;--bad:#ef4444;--info:#3b82f6;--fg:#1f2937;--fg-dim:#4b5563;--fg-faint:#9ca3af;' +
    '--card-border:#e5e7eb;--track:#e5e7eb;--hover:#f9fafb;--card-bg:#fff;--log-bg:#0f172a;--log-fg:#e2e8f0;--bad-bg:#fee2e2;--bad-fg:#991b1b}' +
    'body{font-family:-apple-system,"Microsoft YaHei","PingFang SC",sans-serif;color:var(--fg);background:#fff;margin:24px auto;max-width:980px;padding:0 16px}' +
    'table{border-collapse:collapse;width:100%;font-size:12.5px}th,td{border:1px solid var(--card-border);padding:5px 8px;text-align:left}th{background:var(--hover)}' +
    '.sub{font-size:12px;color:var(--fg-dim)}\n' + rules;
  var html = '<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>' +
    esc(r.title) + '</title><style>' + css + '</style></head><body>' + doc.innerHTML + '</body></html>';
  downloadText((r.title || '巡检报告').replace(/[\\/:*?"<>|]/g, '_') + '_' + (r.created_at || '').slice(0, 10) + '.html', html, 'text/html;charset=utf-8');
}
