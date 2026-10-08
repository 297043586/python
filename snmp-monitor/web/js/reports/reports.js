/*
 * v9 新增的页面部分: 风格主题、巡检报告、配置备份。
 * 单独放一个文件, 在 buildDashboardPage 里拼进主模板(见 main.go), 免得
 * dashboard.go 越来越难改。注意这里整段是 Go 的反引号字符串, JS 里不能出现
 */
/* ================= 巡检报告 ================= */
var rptState = { list: [], admin: false, guestVisible: false, current: null, poll: null };
function openReports() {
  byId('reportModal').style.display = 'flex';
  byId('rptSettings').style.display = 'none';
  fillReportTargets();
  loadReports();
}
function closeReports() {
  byId('reportModal').style.display = 'none';
  if (rptState.poll) { clearInterval(rptState.poll); rptState.poll = null; }
}
function fillReportTargets() {
  var sel = byId('rptTarget');
  var html = '<option value="network|">🌐 全网巡检报告</option><option value="device|">🖥️ 每台设备各一份</option>';
  (summaryData || []).slice().sort(function (a, b) { return naturalCmp(a.sys_name || a.ip, b.sys_name || b.ip); }).forEach(function (d) {
    html += '<option value="device|' + esc(d.ip) + '">设备: ' + esc(d.sys_name || d.ip) + ' (' + esc(d.ip) + ')</option>';
  });
  sel.innerHTML = html;
}
function loadReports(selectID) {
  return pub('/api/reports').then(function (d) {
    rptState.list = d.reports || [];
    rptState.admin = !!d.admin;
    rptState.guestVisible = !!d.guest_visible;
    byId('rptAdminBar').style.display = rptState.admin ? 'flex' : 'none';
    renderReportList();
    var want = selectID || (rptState.current && rptState.current.id);
    if (want) { viewReport(want); }
    var running = rptState.list.some(function (r) { return r.ai_status === 'running'; });
    if (running && !rptState.poll) {
      rptState.poll = setInterval(function () { loadReports(); }, 4000);
    } else if (!running && rptState.poll) {
      clearInterval(rptState.poll); rptState.poll = null;
    }
  }).catch(function (e) { byId('rptList').innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '</div>'; });
}
function gradeColor(g) {
  return g === '优秀' ? 'var(--good)' : g === '良好' ? 'var(--info)' : g === '需关注' ? 'var(--warn)' : 'var(--bad)';
}
function renderReportList() {
  var box = byId('rptList');
  if (!rptState.admin && !rptState.guestVisible) {
    box.innerHTML = '<div class="empty">管理员尚未开放巡检报告。<br>登录后可生成和管理报告。</div>';
    return;
  }
  if (!rptState.list.length) {
    box.innerHTML = '<div class="empty">' + (rptState.admin ? '还没有报告, 在上方选择范围后点「生成报告」' : '暂时没有公开的报告') + '</div>';
    return;
  }
  var cur = rptState.current && rptState.current.id;
  box.innerHTML = rptState.list.map(function (r) {
    var ai = r.ai_status === 'running' ? '<span class="tag tag-trunk">AI 生成中…</span>' : (r.ai_status === 'done' ? '<span class="tag tag-access">AI</span>' : (r.ai_status === 'error' ? '<span class="tag" style="background:var(--bad-bg);color:var(--bad-fg);">AI 失败</span>' : ''));
    return '<div class="rpt-item' + (r.id === cur ? ' active' : '') + '" onclick="viewReport(&quot;' + r.id + '&quot;)">' +
      '<div class="t">' + (r.kind === 'network' ? '🌐 ' : '🖥️ ') + esc(r.title) + '</div>' +
      '<div class="m"><b style="color:' + gradeColor(r.grade) + ';">' + r.score + ' 分 · ' + esc(r.grade) + '</b>' +
      '<span>' + esc(r.created_at) + '</span>' + (r.auto ? '<span class="tag">定时</span>' : '') +
      (rptState.admin ? (r.public ? '<span class="tag tag-access">已公开</span>' : '<span class="tag">未公开</span>') : '') + ai + '</div></div>';
  }).join('');
}
function generateReport() {
  var v = byId('rptTarget').value.split('|');
  var msg = byId('rptMsg');
  msg.innerText = '生成中…';
  authedApi('/api/reports/generate', { kind: v[0], ip: v[1] || '', ai: byId('rptAI').checked, public: byId('rptPublic').checked }).then(function (d) {
    msg.innerText = '已生成 ' + (d.ids || []).length + ' 份' + (byId('rptAI').checked ? ', AI 分析在后台生成(通常 20~90 秒)' : '');
    loadReports((d.ids || [])[0]);
  }).catch(function (e) { msg.innerText = '失败: ' + e; });
}
function viewReport(id) {
  pub('/api/reports/get?id=' + encodeURIComponent(id)).then(function (r) {
    rptState.current = r;
    renderReportList();
    renderReportDoc(r);
  }).catch(function (e) { byId('rptView').innerHTML = '<div class="empty">' + esc(String(e)) + '</div>'; });
}
function renderReportDoc(r) {
  var color = gradeColor(r.grade);
  var html = '<div class="rpt-doc">';
  if (rptState.admin) {
    html += '<div class="no-print" style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:12px;">' +
      '<button class="filter-btn" onclick="reportAction(&quot;' + r.id + '&quot;,&quot;' + (r.public ? 'unpublish' : 'publish') + '&quot;)">' + (r.public ? '🔒 取消公开' : '🌍 公开给游客') + '</button>' +
      '<button class="filter-btn" onclick="reportAction(&quot;' + r.id + '&quot;,&quot;ai&quot;)">🤖 ' + (r.ai_text ? '重新生成' : '生成') + ' AI 分析</button>' +
      '<button class="filter-btn" onclick="printReport()">🖨️ 打印 / 导出 PDF</button>' +
      '<button class="filter-btn" onclick="exportReportHTML()" title="单个 HTML 文件, 带图表, 可以直接发邮件 / 归档">📄 导出 HTML</button>' +
      '<button class="filter-btn" style="color:var(--bad);" onclick="if(confirm(&quot;确定删除这份报告?&quot;)){reportAction(&quot;' + r.id + '&quot;,&quot;delete&quot;)}">🗑 删除</button></div>';
  } else {
    html += '<div class="no-print" style="margin-bottom:12px;"><button class="filter-btn" onclick="printReport()">🖨️ 打印 / 导出 PDF</button> <button class="filter-btn" onclick="exportReportHTML()">📄 导出 HTML</button></div>';
  }
  html += '<div class="rpt-head"><div class="rpt-score" style="background:conic-gradient(' + color + ' ' + r.score + '%, var(--track) 0);"><div><b style="color:' + color + ';">' + r.score + '</b><span>' + esc(r.grade) + '</span></div></div>' +
    '<div><h2 style="margin:0 0 6px;font-size:19px;">' + esc(r.title) + '</h2>' +
    '<div class="sub">生成: ' + esc(r.created_at) + ' · ' + esc(r.created_by || '') + ' · 数据来自 ' + esc(r.data_time || '-') + ' 的巡检</div></div></div>';
  html += '<div class="rpt-sec"><h3>摘要</h3>' + (r.summary || []).map(function (s) { return '<div class="rpt-li info"><i></i><span>' + esc(s) + '</span></div>'; }).join('') + '</div>';
  (r.sections || []).forEach(function (s) {
    html += '<div class="rpt-sec"><h3>' + esc(s.title) + '</h3>' + (s.note ? '<div class="sub" style="margin:-2px 0 6px;">' + esc(s.note) + '</div>' : '');
    if (s.chart) { html += reportChartSVG(s.chart); }
    (s.items || []).forEach(function (it) { html += '<div class="rpt-li ' + esc(it.level) + '"><i></i><span>' + esc(it.text) + '</span></div>'; });
    if (s.table) {
      html += '<div style="overflow:auto;"><table><thead><tr>' + s.table.headers.map(function (h) { return '<th style="cursor:default;position:static;">' + esc(h) + '</th>'; }).join('') + '</tr></thead><tbody>' +
        (s.table.rows || []).map(function (row) { return '<tr>' + row.map(function (c) { return '<td>' + esc(c) + '</td>'; }).join('') + '</tr>'; }).join('') + '</tbody></table></div>';
    }
    html += '</div>';
  });
  if ((r.advice || []).length) {
    html += '<div class="rpt-sec"><h3>处置建议(规则生成)</h3><ol style="margin:0;padding-left:20px;font-size:13px;line-height:1.8;">' +
      r.advice.map(function (a) { return '<li>' + esc(a) + '</li>'; }).join('') + '</ol></div>';
  }
  html += '<div class="rpt-sec"><h3>🤖 AI 分析</h3>';
  if (r.ai_status === 'running') {
    html += '<div class="rpt-ai">AI 正在分析, 完成后自动显示…</div>';
  } else if (r.ai_status === 'error') {
    html += '<div class="rpt-ai" style="color:var(--bad-fg);background:var(--bad-bg);">生成失败: ' + esc(r.ai_error || '') + '</div>';
  } else if (r.ai_text) {
    html += '<div class="rpt-ai">' + renderMarkdown(r.ai_text) + '<div class="sub" style="margin-top:10px;">由 ' + esc(r.ai_model || '') + ' 于 ' + esc(r.ai_at || '') + ' 生成, 仅供参考, 请结合现场情况判断。</div></div>';
  } else {
    html += '<div class="sub">这份报告没有 AI 分析。' + (rptState.admin ? '可以点上方「生成 AI 分析」。' : '') + '</div>';
  }
  html += '</div></div>';
  byId('rptView').innerHTML = html;
}
function reportAction(id, action) {
  authedApi('/api/reports/action', { id: id, action: action }).then(function () {
    if (action === 'delete') { rptState.current = null; byId('rptView').innerHTML = '<div class="empty">已删除</div>'; }
    loadReports(action === 'delete' ? '' : id);
  }).catch(function (e) { alert('操作失败: ' + e); });
}
function printReport() {
  document.body.classList.add('printing-report');
  setTimeout(function () {
    window.print();
    document.body.classList.remove('printing-report');
  }, 50);
}
// 极简 Markdown 渲染(标题/列表/粗体/行内代码/代码块/引用/表格), 先转义再加标签, 不会注入 HTML。
function renderMarkdown(src) {
  var BT = String.fromCharCode(96);
  var lines = String(src || '').replace(/\r/g, '').split('\n');
  var out = [], inCode = false, list = null, table = null;
  var inline = function (s) {
    s = esc(s);
    s = s.replace(new RegExp(BT + '([^' + BT + ']+)' + BT, 'g'), '<code>$1</code>');
    s = s.replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>');
    return s;
  };
  var closeList = function () { if (list) { out.push('</' + list + '>'); list = null; } };
  var closeTable = function () { if (table) { out.push('</tbody></table>'); table = null; } };
  lines.forEach(function (l) {
    if (l.indexOf(BT + BT + BT) === 0) {
      closeList(); closeTable();
      out.push(inCode ? '</pre>' : '<pre>');
      inCode = !inCode;
      return;
    }
    if (inCode) { out.push(esc(l) + '\n'); return; }
    var m;
    if (/^\s*\|.*\|\s*$/.test(l)) {
      closeList();
      var cells = l.trim().replace(/^\||\|$/g, '').split('|').map(function (c) { return c.trim(); });
      if (cells.every(function (c) { return /^:?-{2,}:?$/.test(c); })) { return; }
      if (!table) { out.push('<table><tbody>'); table = true; }
      out.push('<tr>' + cells.map(function (c) { return '<td>' + inline(c) + '</td>'; }).join('') + '</tr>');
      return;
    }
    closeTable();
    if ((m = l.match(/^(#{1,6})\s+(.*)$/))) { closeList(); var n = Math.min(m[1].length + 1, 4); out.push('<h' + n + '>' + inline(m[2]) + '</h' + n + '>'); return; }
    if ((m = l.match(/^\s*[-*+]\s+(.*)$/))) { if (list !== 'ul') { closeList(); out.push('<ul>'); list = 'ul'; } out.push('<li>' + inline(m[1]) + '</li>'); return; }
    if ((m = l.match(/^\s*\d+[.)]\s+(.*)$/))) { if (list !== 'ol') { closeList(); out.push('<ol>'); list = 'ol'; } out.push('<li>' + inline(m[1]) + '</li>'); return; }
    closeList();
    if ((m = l.match(/^>\s?(.*)$/))) { out.push('<blockquote>' + inline(m[1]) + '</blockquote>'); return; }
    if (l.trim() === '') { return; }
    if (/^-{3,}$/.test(l.trim())) { out.push('<hr>'); return; }
    out.push('<p style="margin:6px 0;">' + inline(l) + '</p>');
  });
  closeList(); closeTable();
  if (inCode) { out.push('</pre>'); }
  return out.join('');
}
var RS_DEFAULTS = {
  anthropic: { base: 'https://api.anthropic.com', model: 'claude-opus-5', hint: 'Claude API: Base URL 留空即官方地址; 国内网络如需代理, 启动程序前设置 HTTPS_PROXY 环境变量。' },
  openai: { base: 'https://api.deepseek.com/v1', model: 'deepseek-chat', hint: 'OpenAI 兼容接口: Base URL 填到 /v1 为止(程序会拼 /chat/completions)。例: DeepSeek https://api.deepseek.com/v1 · 通义千问 https://dashscope.aliyuncs.com/compatible-mode/v1 · 本地 Ollama http://127.0.0.1:11434/v1(可不填 Key)。' }
};
function rsProviderHint() {
  var p = byId('rsProvider').value;
  byId('rsHint').innerText = RS_DEFAULTS[p].hint;
  byId('rsBase').placeholder = RS_DEFAULTS[p].base;
  if (!byId('rsModel').value) { byId('rsModel').value = RS_DEFAULTS[p].model; }
}
function toggleReportSettings() {
  var box = byId('rptSettings');
  if (box.style.display !== 'none') { box.style.display = 'none'; return; }
  authedApi('/api/reports/settings').then(function (s) {
    box.style.display = 'flex';
    byId('rsGuest').checked = !!s.guest_visible;
    byId('rsAuto').checked = !!s.auto_enabled;
    byId('rsHour').value = s.auto_hour;
    byId('rsPerDev').checked = !!s.auto_per_device;
    byId('rsAutoAI').checked = !!s.auto_ai;
    byId('rsAutoPub').checked = !!s.auto_public;
    byId('rsKeep').value = s.keep || 200;
    var ai = s.ai || {};
    byId('rsProvider').value = ai.provider || 'anthropic';
    byId('rsBase').value = ai.base_url || '';
    byId('rsModel').value = ai.model || '';
    byId('rsMaxTok').value = ai.max_tokens || 8000;
    byId('rsExtra').value = ai.extra_prompt || '';
    byId('rsKey').value = '';
    byId('rsKeyState').innerText = s.has_key ? '(已保存)' : '(未设置)';
    byId('rsMsg').innerText = '';
    rsProviderHint();
  }).catch(function (e) { alert(e); });
}
function collectReportSettings() {
  return {
    guest_visible: byId('rsGuest').checked, auto_enabled: byId('rsAuto').checked, auto_hour: parseInt(byId('rsHour').value, 10) || 0,
    auto_per_device: byId('rsPerDev').checked, auto_ai: byId('rsAutoAI').checked, auto_public: byId('rsAutoPub').checked,
    keep: parseInt(byId('rsKeep').value, 10) || 200,
    ai: { provider: byId('rsProvider').value, base_url: byId('rsBase').value.trim(), model: byId('rsModel').value.trim(),
      max_tokens: parseInt(byId('rsMaxTok').value, 10) || 8000, extra_prompt: byId('rsExtra').value },
    api_key: byId('rsKey').value
  };
}
function saveReportSettings(test) {
  var body = collectReportSettings();
  body.test = !!test;
  var msg = byId('rsMsg');
  msg.style.color = 'var(--fg-dim)';
  msg.innerText = test ? '正在测试(最多约 50 秒)…' : '保存中…';
  authedApi('/api/reports/settings', body).then(function (d) {
    msg.style.color = 'var(--good)';
    msg.innerText = test ? ('✅ 连接正常, 模型回复: ' + (d.reply || '')) : '✅ 已保存';
    if (!test) { byId('rsKey').value = ''; loadReports(); toggleReportSettings(); toggleReportSettings(); }
  }).catch(function (e) { msg.style.color = 'var(--bad)'; msg.innerText = '❌ ' + e; });
}
function clearReportKey() {
  if (!confirm('确定清除已保存的 API Key?')) { return; }
  var body = collectReportSettings();
  body.clear_key = true;
  body.api_key = '';
  authedApi('/api/reports/settings', body).then(function () { byId('rsKeyState').innerText = '(未设置)'; byId('rsMsg').innerText = '已清除'; })
    .catch(function (e) { alert(e); });
}
