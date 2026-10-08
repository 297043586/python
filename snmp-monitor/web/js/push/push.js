/*
 * 配置下发(v14): 选设备 → 选模板填参数 → 生成预览(每台可改) → 执行 → 看每台的结果和会话记录。
 * 后端见 push.go; 登录凭据复用「配置备份」里的。
 */
var PUSH = { devices: [], sel: {}, ports: {}, portList: {}, plans: [], meta: null, poll: null };

// openPush({ kind: 'sflow', ips: ['10.0.1.1'] }) — 可以从设备详情 / 配置备份 / 系统设置打开
function openPush(opts) {
  opts = opts || {};
  if (!authenticated) { openAuthGate(function () { openPush(opts); }); return; }
  byId('pushModal').style.display = 'flex';
  if (!PUSH.fsInit) { fsButton(byId('pushBox'), byId('pushFs')); PUSH.fsInit = true; }
  if (opts.kind) { byId('pushKind').value = opts.kind; }
  PUSH.plans = [];
  byId('pushRunBtn').disabled = true;
  byId('pushMsg').innerText = '';
  byId('pushPlans').innerHTML = '<div class="empty">左边选设备, 填好参数后点「生成预览」</div>';
  Promise.all([authedApi('/api/push/meta'), authedApi('/api/backups')]).then(function (res) {
    PUSH.meta = res[0];
    PUSH.devices = (res[1].devices || []).slice().sort(function (a, b) { return cmpNatural(ipSortKey(a.ip), ipSortKey(b.ip)); });
    byId('pushIPs').innerHTML = (PUSH.meta.ips || []).map(function (ip) { return '<option value="' + esc(ip) + '">'; }).join('');
    if (!byId('pushCollector').value) {
      var h = location.hostname, ips = PUSH.meta.ips || [];
      byId('pushCollector').value = ips.indexOf(h) >= 0 ? h : (ips[0] || '');
    }
    if (opts.ips) {
      PUSH.sel = {};
      PUSH.devices.forEach(function (d) { if (opts.ips.indexOf(d.ip) >= 0) { PUSH.sel[d.device_id] = true; } });
    }
    pushKindChanged();
  }).catch(function (e) {
    byId('pushDevList').innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '<br>列设备需要「查看配置备份」权限, 执行需要「配置下发」权限</div>';
  });
}
modalRegister('pushModal', closePush);
function closePush() {
  byId('pushModal').style.display = 'none';
  fsExitWithin(byId('pushModal'));
  if (PUSH.poll) { clearTimeout(PUSH.poll); PUSH.poll = null; }
}

function pushKind() { return byId('pushKind').value; }
function pushNeedsPorts() { var k = pushKind(); return k === 'sflow' || k === 'netflow'; }

function pushKindChanged() {
  var k = pushKind(), m = PUSH.meta || {};
  document.querySelectorAll('#pushForm .pk').forEach(function (el) { el.style.display = el.classList.contains('pk-' + k) ? '' : 'none'; });
  var port = { sflow: m.sflow_port, syslog: m.syslog_port, netflow: m.netflow_port }[k];
  var def = { sflow: 6343, syslog: 514, netflow: 2055 }[k];
  if (def) { byId('pushPort').value = port > 0 ? port : def; }
  var name = { sflow: 'sFlow', syslog: 'Syslog', netflow: 'NetFlow' }[k];
  byId('pushPortWarn').innerHTML = name && !(port > 0)
    ? '⚠️ 本机还没有启用 ' + name + ' 接收(「🛠️ 设置 → 被动采集」里填端口, 重启程序生效), 下发之后交换机发过来也收不到。'
    : '';
  byId('pushSave').checked = k !== 'custom';
  PUSH.plans = [];
  byId('pushRunBtn').disabled = true;
  renderPushDevices();
  if (pushNeedsPorts()) { Object.keys(PUSH.sel).forEach(function (id) { var d = pushDev(id); if (d) { pushLoadPorts(d.ip); } }); }
}

function pushDev(id) { return PUSH.devices.filter(function (d) { return d.device_id === id; })[0]; }

function renderPushDevices() {
  var q = (byId('pushQ').value || '').trim().toLowerCase();
  var list = PUSH.devices.filter(function (d) { return !q || ((d.name || '') + ' ' + d.ip + ' ' + (d.brand || '')).toLowerCase().indexOf(q) >= 0; });
  if (!list.length) { byId('pushDevList').innerHTML = '<div class="empty">没有设备</div>'; return; }
  var ports = pushNeedsPorts();
  byId('pushDevList').innerHTML = list.map(function (d) {
    var id = d.device_id, on = !!PUSH.sel[id];
    var h = '<div class="push-dev' + (on ? ' on' : '') + (d.has_cred ? '' : ' nocred') + '">' +
      '<label><input type="checkbox" ' + (on ? 'checked ' : '') + 'onchange="pushToggle(&quot;' + id + '&quot;,this.checked)"> ' +
      '<b>' + esc(d.name || d.ip) + '</b> <span class="sub2">' + esc(d.ip) + ' · ' + esc(d.vendor && d.vendor !== 'auto' ? d.vendor : (d.brand || '?')) + '</span></label>' +
      (d.has_cred ? '' : '<div class="sub2">没有登录凭据: 可以生成命令预览 / 复制去手工执行, 不会下发(凭据在「💾 配置备份」里设置)</div>');
    if (on && ports) { h += pushPortsHTML(d.ip); }
    return h + '</div>';
  }).join('');
}

function pushPortsHTML(ip) {
  var list = PUSH.portList[ip];
  if (!list) { return '<div class="push-ports"><span class="sub2">正在读取端口…</span></div>'; }
  var sel = PUSH.ports[ip] || {};
  var n = Object.keys(sel).filter(function (k) { return sel[k]; }).length;
  var q = '&quot;' + esc(ip) + '&quot;';
  return '<div class="push-ports"><div class="tb"><span class="sub2">已选 ' + n + ' 个口</span>' +
    '<button class="btn sm" onclick="pushPick(' + q + ',&quot;uplink&quot;)" title="有 LLDP 邻居或 MAC 很多的口, 流量都要经过它们">上联口</button>' +
    '<button class="btn sm" onclick="pushPick(' + q + ',&quot;up&quot;)">全部 UP 口</button>' +
    '<button class="btn sm" onclick="pushPick(' + q + ',&quot;none&quot;)">清空</button></div><div class="push-chips">' +
    list.map(function (p) {
      var on = !!sel[p.name];
      return '<label class="chip' + (on ? ' on' : '') + (p.status === 'UP' ? '' : ' down') + '" title="' + esc(p.status + (p.lldp_remote_dev && p.lldp_remote_dev !== '-' ? ' → ' + p.lldp_remote_dev : '')) + '">' +
        '<input type="checkbox" ' + (on ? 'checked ' : '') + 'onchange="pushPortToggle(' + q + ',&quot;' + esc(p.name) + '&quot;,this.checked)">' +
        esc(p.name) + (p.is_uplink ? ' ⬆' : '') + '</label>';
    }).join('') + '</div></div>';
}

function pushToggle(id, on) {
  if (on) { PUSH.sel[id] = true; } else { delete PUSH.sel[id]; }
  var d = pushDev(id);
  if (on && d && pushNeedsPorts()) { pushLoadPorts(d.ip); }
  renderPushDevices();
}
function pushSelectAll(on) {
  PUSH.sel = {};
  if (on) {
    PUSH.devices.forEach(function (d) { if (d.has_cred) { PUSH.sel[d.device_id] = true; if (pushNeedsPorts()) { pushLoadPorts(d.ip); } } });
  }
  renderPushDevices();
}
function pushLoadPorts(ip) {
  if (PUSH.portList[ip]) { return; }
  pub('/api/device?ip=' + encodeURIComponent(ip)).then(function (d) {
    PUSH.portList[ip] = (d.ports || []).slice().sort(function (a, b) { return cmpNatural(a.name, b.name); });
    if (!PUSH.ports[ip]) { pushPick(ip, 'uplink', true); }
    renderPushDevices();
  }).catch(function () { PUSH.portList[ip] = []; renderPushDevices(); });
}
function pushPick(ip, mode, quiet) {
  var sel = {};
  (PUSH.portList[ip] || []).forEach(function (p) {
    if ((mode === 'uplink' && p.is_uplink && p.status === 'UP') || (mode === 'up' && p.status === 'UP')) { sel[p.name] = true; }
  });
  PUSH.ports[ip] = sel;
  if (!quiet) { renderPushDevices(); }
}
function pushPortToggle(ip, name, on) {
  var sel = PUSH.ports[ip] || (PUSH.ports[ip] = {});
  if (on) { sel[name] = true; } else { delete sel[name]; }
  renderPushDevices();
}

function pushParamsBody() {
  var ports = {};
  Object.keys(PUSH.sel).forEach(function (id) {
    var d = pushDev(id);
    if (d) { ports[d.ip] = Object.keys(PUSH.ports[d.ip] || {}).filter(function (k) { return PUSH.ports[d.ip][k]; }); }
  });
  return {
    device_ids: Object.keys(PUSH.sel),
    params: {
      kind: pushKind(), collector: byId('pushCollector').value.trim(), port: parseInt(byId('pushPort').value, 10) || 0,
      rate: parseInt(byId('pushRate').value, 10) || 0, interval: parseInt(byId('pushInterval').value, 10) || 0,
      level: byId('pushLevel').value, custom: byId('pushCustom').value, save: byId('pushSave').checked, ports: ports
    }
  };
}

function pushPreview() {
  var body = pushParamsBody();
  if (!body.device_ids.length) { pushSay('先在左边选设备', false); return; }
  pushSay('生成中…', null);
  authedApi('/api/push/preview', body).then(function (d) {
    PUSH.plans = d.plans || [];
    var ok = PUSH.plans.filter(function (p) { return !p.error && !p.no_cred; }).length;
    var pv = PUSH.plans.filter(function (p) { return !p.error && p.no_cred; }).length;
    var bad = PUSH.plans.length - ok - pv;
    byId('pushRunBtn').disabled = ok === 0;
    pushSay(ok + ' 台可以执行' + (pv ? ', ' + pv + ' 台没凭据只生成预览' : '') + (bad ? ', ' + bad + ' 台有问题(见红字)' : '') + '; 命令可以直接改', ok + pv > 0);
    renderPushPlans();
  }).catch(function (e) { pushSay(String(e), false); });
}

function renderPushPlans() {
  byId('pushPlans').innerHTML = PUSH.plans.map(function (p) {
    return '<div class="push-plan"><div class="hd"><b>' + esc(p.name || p.ip) + '</b> <span class="sub2">' + esc(p.ip) + ' · ' + esc(p.vendor || '?') + '</span></div>' +
      (p.note ? '<div class="push-note">💡 ' + esc(p.note) + '</div>' : '') +
      (p.error ? '<div class="push-err">✖ ' + esc(p.error) + '</div>' : '') +
      (p.no_cred && !p.error ? '<div class="push-note">👁 没有登录凭据, 只是预览: 不会下发, 可以复制去设备上手工执行 <button class="btn sm" onclick="pushCopy(this)">📋 复制</button></div>' : '') +
      ((p.commands || []).length ? '<textarea ' + (p.no_cred ? 'data-preview="1" ' : '') + 'data-id="' + esc(p.device_id) + '" rows="' + Math.min(16, Math.max(4, p.commands.length + 1)) + '" spellcheck="false">' + esc(p.commands.join('\n')) + '</textarea>' : '') +
      '</div>';
  }).join('') || '<div class="empty">没有可生成的设备</div>';
}

function pushCopy(btn) {
  var t = btn.closest('.push-plan').querySelector('textarea');
  if (!t) { return; }
  var done = function () { uiToast('已复制', 'ok'); };
  if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(t.value).then(done, function () { t.select(); document.execCommand('copy'); done(); }); }
  else { t.select(); document.execCommand('copy'); done(); }
}

function pushRun() {
  var body = pushParamsBody();
  body.commands = {};
  document.querySelectorAll('#pushPlans textarea[data-id]:not([data-preview])').forEach(function (t) { body.commands[t.getAttribute('data-id')] = t.value.split('\n'); });
  var n = Object.keys(body.commands).length;
  if (!n) { return; }
  if (!confirm('确定要在 ' + n + ' 台设备上执行这些命令吗?\n会直接修改设备的运行配置' + (body.params.save ? '并保存' : '') + '。')) { return; }
  byId('pushRunBtn').disabled = true;
  pushSay('已提交, 正在逐台执行…', null);
  authedApi('/api/push/run', body).then(function (d) {
    return pushWaitJob(d.job, renderPushJob);
  }).then(function (job) {
    var bad = job.results.filter(function (r) { return r.state !== 'ok'; }).length;
    pushSay(bad ? '执行完成, ' + bad + ' 台失败(展开看会话记录)' : '✅ 全部执行成功', !bad);
  }).catch(function (e) { pushSay(String(e), false); byId('pushRunBtn').disabled = false; });
}

// pushWaitJob 轮询任务直到完成; onTick 每次拿到进度都调一次
function pushWaitJob(id, onTick) {
  return new Promise(function (resolve, reject) {
    var tick = function () {
      authedApi('/api/push/job?id=' + encodeURIComponent(id)).then(function (job) {
        if (onTick) { onTick(job); }
        if (job.done) { PUSH.poll = null; resolve(job); return; }
        PUSH.poll = setTimeout(tick, 1500);
      }).catch(function (e) { PUSH.poll = null; reject(e); });
    };
    tick();
  });
}

var PUSH_STATE = { pending: ['排队', 'pill-none'], running: ['执行中…', 'pill-uplink'], ok: ['成功', 'pill-access'], failed: ['失败', 'pill-bad'] };
function renderPushJob(job) {
  byId('pushPlans').innerHTML = job.results.map(function (r) {
    var st = PUSH_STATE[r.state] || [r.state, 'pill-none'];
    return '<div class="push-plan"><div class="hd"><b>' + esc(r.name || r.ip) + '</b> <span class="sub2">' + esc(r.ip) + '</span> ' +
      '<span class="pill ' + st[1] + '">' + st[0] + '</span> <span class="sub2">' + r.executed + ' / ' + r.total + ' 条</span></div>' +
      (r.error ? '<div class="push-err">✖ ' + esc(r.error) + '</div>' : '') +
      (r.transcript ? '<details' + (r.state === 'failed' ? ' open' : '') + '><summary>会话记录</summary><pre class="push-log">' + esc(r.transcript) + '</pre></details>' : '') +
      '</div>';
  }).join('');
}

function pushSay(text, ok) {
  var m = byId('pushMsg');
  m.style.color = ok === true ? 'var(--good)' : (ok === false ? 'var(--bad)' : 'var(--fg-dim)');
  m.innerText = text;
}

// 设备详情 → 设备日志: 登录设备读本地日志缓冲区(display logbuffer / show logging), 不改配置
function pushFetchDeviceLog() {
  if (!currentIP) { return; }
  var ip = currentIP, box = byId('devLogBufBox');
  box.style.display = '';
  box.innerHTML = '<div>&gt; <span class="spin">⟳</span> 正在登录 ' + esc(ip) + ' 读取日志缓冲区…</div>';
  authedApi('/api/push/run', { device_ids: [ip], params: { kind: 'show' } }).then(function (d) {
    return pushWaitJob(d.job);
  }).then(function (job) {
    var r = job.results[0] || {};
    if (currentIP !== ip) { return; }
    box.innerHTML = r.error ? '<div>&gt; 读取失败: ' + esc(r.error) + '</div>' : '<pre class="push-log" style="max-height:none;">' + esc(r.transcript || '') + '</pre>';
  }).catch(function (e) { box.innerHTML = '<div>&gt; ' + esc(String(e)) + '</div>'; });
}
