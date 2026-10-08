/*
 * 设备详情窗口 + 设备日志(syslog)。
 */
/* ---------- 详情窗口 ---------- */
// v13: 详情窗口的各个部分由各自的模块注册, 这里不再写死 "切到第 3 页画图、第 6 页拉日志 ...",
// 面板页也不用再覆盖 switchTab / fillModal:
//   detailPart({ fill(d) })          每次拿到这台设备的数据都调(计数、表格这类轻量的)
//   detailPart({ tab: 3, show(d) })  切到这个标签页时才调(画图 / 拉 syslog / 拉流量 / 画面板这类重的)
// back: 在详情窗口里从一台设备跳到另一台(LLDP 邻居、聚合对端、面板 ...)时记下来源, 可以一级级返回
const DETAIL = { parts: [], pendingTab: null, back: [] };
function detailPart(p) { DETAIL.parts.push(p); }

// openModal(ip)          打开设备详情
// openModal(ip, tab)     打开后直接切到某个标签页(比如 9 = 面板)
function openModal(ip, tab, fromBack) {
  if (!fromBack && currentIP && currentIP !== ip && modalIsOpen(byId('devModal'))) {
    DETAIL.back.push({ ip: currentIP, tab: currentTab, name: currentDetail ? currentDetail.sys_name : currentIP });
    if (DETAIL.back.length > 20) { DETAIL.back.shift(); }
  } else if (!fromBack && !modalIsOpen(byId('devModal'))) {
    DETAIL.back = [];
  }
  devBackSync();
  DETAIL.pendingTab = tab === undefined || tab === null ? null : tab;
  currentIP = ip;
  currentDetail = null;
  stopPortLive(false);
  portFilters = {}; sortField = 'name'; sortAsc = true;
  syncPortFilterButtons();
  byId('portSearch').value = ''; byId('arpSearch').value = '';
  if (byId('arpIncludeRelated')) { byId('arpIncludeRelated').checked = false; }
  msClearAll(['arpPortFilter', 'arpKindFilter', 'arpVlanFilter']);
  byId('devModal').style.display = 'flex';
  switchTab(0);
  byId('modalTitle').innerText = '加载中...';
  pub('/api/device?ip=' + encodeURIComponent(ip)).then(function (d) {
    currentDetail = d;
    fillModal(d);
  }).catch(function (e) {
    byId('modalTitle').innerText = '加载失败';
    byId('modalSub').innerText = String(e);
  });
}
function closeModal() { byId('devModal').style.display = 'none'; currentIP = null; currentDetail = null; DETAIL.back = []; devBackSync(); }
// 「← 上一台设备」
function devBack() {
  var p = DETAIL.back.pop();
  if (p) { openModal(p.ip, p.tab, true); }
}
function devBackSync() {
  var b = byId('devBackBtn'), p = DETAIL.back[DETAIL.back.length - 1];
  if (!b) { return; }
  b.style.display = p ? '' : 'none';
  if (p) { b.innerText = '← ' + p.name; b.title = '回到上一台设备「' + p.name + '」(共 ' + DETAIL.back.length + ' 级)'; }
}

// 详情窗口里的"⚡ 立即巡检": 只对这一台设备重新采集, 不用等下一轮全网巡检。
function refreshCurrentDevice() {
  if (!currentIP) { return; }
  var ip = currentIP, btn = byId('devRefreshBtn'), old = btn.innerHTML;
  btn.disabled = true; btn.innerHTML = '<span class="spin">⟳</span> 正在采集…';
  authedApi('/api/device/refresh?ip=' + encodeURIComponent(ip)).then(function () {
    return pub('/api/device?ip=' + encodeURIComponent(ip));
  }).then(function (d) {
    if (currentIP === ip) { currentDetail = d; fillModal(d); }
  }).catch(function (e) {
    var msg = String(e || '').indexOf('429') >= 0 ? '太快了, 稍等几秒再试' : String(e);
    byId('modalSub').innerText = '立即巡检失败: ' + msg;
  }).finally(function () {
    if (byId('devRefreshBtn')) { btn.disabled = false; btn.innerHTML = old; }
  });
}

function fillModal(d) {
  if (!d) { return; }
  byId('modalTitle').innerText = d.sys_name + ' (' + d.ip + ')';
  byId('modalSub').innerText = '厂商: ' + d.brand + ' | 运行时长: ' + (d.uptime_str || '-') + ' | 采集于 ' + d.timestamp +
    (d.note ? ' | 备注: ' + d.note : '');
  byId('hwPower').innerText = d.hardware.power_status;
  byId('hwFan').innerText = d.hardware.fan_status;
  byId('hwTemp').innerText = d.hardware.temperature;
  byId('hwRatio').innerText = d.port_usage_ratio || '-';
  byId('hwTotal').innerText = d.total_ports + ' 个';
  byId('hwUp').innerText = d.up_ports + ' 个';
  byId('hwDown').innerText = d.down_ports + ' 个';
  byId('hwUptime').innerText = d.uptime_str || '-';
  byId('hwBrand').innerText = d.brand + (d.brand_detected ? (' (sysObjectID 识别: ' + d.brand_detected + ')') : '');
  byId('hwCpu').innerText = d.cpu_usage + '%';
  byId('hwMem').innerText = d.mem_usage > 0 ? (d.mem_usage + '%') : '未采集';
  byId('hwCost').innerText = d.cost_ms + ' ms';
  byId('hwDescr').innerText = d.sys_descr || '-';

  var c = d.collect || {};
  var on = [], off = [];
  (c.skip_optical ? off : on).push('光衰'); (c.skip_lldp ? off : on).push('LLDP');
  (c.skip_rates ? off : on).push('端口速率'); (c.skip_hosts ? off : on).push('MAC/ARP');
  (c.skip_extended ? off : on).push('扩展信息');
  byId('hwCollect').innerText = '采集: ' + (on.join('、') || '无') + (off.length ? (' | 已关闭: ' + off.join('、')) : '');

  byId('portCount').innerText = d.ports ? d.ports.length : 0;
  var localArpCount = (d.host_mappings || []).filter(function (h) { return !h.ip_source; }).length;
  var arpTabBtn = document.querySelector('#devModal .tab-btn[data-tab="2"]');
  if (arpTabBtn) { arpTabBtn.innerHTML = '🌐 ARP 表 (<span id="arpCount">' + localArpCount + '</span>)'; }
  byId('aggCount').innerText = d.agg_groups ? d.agg_groups.length : 0;
  byId('alertCount').innerText = d.open_alerts || 0;

  var diag = '';
  var seenDiagnostics = Object.create(null);
  (d.diagnostics || []).forEach(function (t) {
    if (seenDiagnostics[t]) { return; }
    seenDiagnostics[t] = true;
    diag += '<div class="diag">🔎 ' + esc(t) + '</div>';
  });
  byId('diagList').innerHTML = diag;

  var ph = '';
  (d.phases || []).forEach(function (p) {
    var bar = Math.min(100, p.ms / 10);
    ph += '<tr><td>' + esc(p.name) + '</td><td><b>' + p.ms + ' ms</b>' +
          '<div class="progress-bar-wrap" style="height:5px;width:220px;"><div class="progress-bar" style="width:' + bar + '%;background:var(--info);"></div></div></td></tr>';
  });
  byId('phaseBody').innerHTML = ph || '<tr><td colspan="2" style="color:var(--fg-faint);">无</td></tr>';

  var logs = '';
  (d.logs || []).forEach(function (l) { logs += '<div>&gt; ' + esc(l) + '</div>'; });
  byId('logBox').innerHTML = logs || '<div>&gt; 暂无事件记录</div>';

  DETAIL.parts.forEach(function (p) { if (p.fill) { p.fill(d); } });
  if (DETAIL.pendingTab !== null) { var t = DETAIL.pendingTab; DETAIL.pendingTab = null; switchTab(t); return; }
  DETAIL.parts.forEach(function (p) { if (p.show && p.tab === currentTab) { p.show(d); } });
  App.emit('detail', d);
}

function switchTab(i) {
  currentTab = i;
  // 按钮和内容区都按"编号"匹配, 不能按它们在页面里的先后位置: 聚合口
  // 按钮(编号 7)排在第 4 个, 之前按位置高亮, 点它会把蓝色下划线标到
  // "设备日志"上, 点"性能趋势"又会标到"聚合口"上, 看起来就是高亮乱跳。
  var btns = document.querySelectorAll('#devModal .tab-btn');
  for (var a = 0; a < btns.length; a++) { btns[a].classList.toggle('active', btns[a].getAttribute('data-tab') === String(i)); }
  var tabs = document.querySelectorAll('#devModal .tab-content');
  for (var b = 0; b < tabs.length; b++) { tabs[b].classList.toggle('active', tabs[b].id === 'tab' + i); }
  if (currentDetail) { DETAIL.parts.forEach(function (p) { if (p.show && p.tab === i) { p.show(currentDetail); } }); }
}
modalRegister('devModal', closeModal);
detailPart({ tab: 6, show: function () { loadSyslog(); } });

/* ---------- 设备日志 (syslog) ---------- */
function loadSyslog() {
  if (!currentIP) { return; }
  var box = byId('syslogBox');
  pub('/api/syslog?ip=' + encodeURIComponent(currentIP)).then(function (d) {
    if (!d.enabled) {
      box.innerHTML = '<div>&gt; Syslog 接收未开启。在 config.json 的 collectors.syslog_port 里配一个端口后重启程序即可启用。</div>';
      return;
    }
    if (!d.lines || d.lines.length === 0) {
      box.innerHTML = '<div>&gt; Syslog 接收已开启, 但还没收到这台设备的日志。请检查交换机是否已配置 loghost 指向本机, 以及网络是否连通(UDP)。</div>';
      return;
    }
    var html = '';
    d.lines.forEach(function (l) {
      var sevColor = l.severity <= 3 ? '#f87171' : (l.severity <= 4 ? '#fbbf24' : 'var(--log-fg)');
      html += '<div><span style="color:var(--fg-faint);">' + esc(l.time) + '</span> ' +
              '<span style="color:' + sevColor + ';">[sev' + l.severity + ']</span> ' + esc(l.raw) + '</div>';
    });
    box.innerHTML = html;
    box.scrollTop = box.scrollHeight;
  }).catch(function (e) { box.innerHTML = '<div>&gt; 加载失败: ' + esc(String(e)) + '</div>'; });
}
