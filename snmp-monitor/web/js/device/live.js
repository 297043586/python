/*
 * 设备详情 → 端口表的「⚡ 实时速率」: 开着时每 5 秒读一次这台设备的端口计数器(后端 live_rates.go),
 * 用算出来的速率/利用率覆盖表里的巡检平均值。窗口关了 / 换了设备 / 切走端口页就自动停。
 */
var PORT_LIVE = { ip: null, byIndex: {}, at: 0, timer: null };

function portLiveFor(ip, index) {
  if (!PORT_LIVE.timer || PORT_LIVE.ip !== ip || Date.now() - PORT_LIVE.at > 20000) { return null; }
  return PORT_LIVE.byIndex[index] || null;
}

function togglePortLive(on) {
  stopPortLive(true);
  if (!on || !currentIP) { renderPorts(); return; }
  PORT_LIVE.ip = currentIP;
  PORT_LIVE.byIndex = {};
  byId('portLiveStat').innerText = '(首次取样…)';
  PORT_LIVE.timer = setTimeout(portLiveTick, 0);
}

function stopPortLive(keepToggle) {
  if (PORT_LIVE.timer) { clearTimeout(PORT_LIVE.timer); }
  PORT_LIVE.timer = null;
  PORT_LIVE.byIndex = {};
  var st = byId('portLiveStat');
  if (st) { st.innerText = ''; }
  if (!keepToggle && byId('portLiveToggle')) { byId('portLiveToggle').checked = false; }
}

function portLiveTick() {
  var ip = PORT_LIVE.ip;
  var tab1 = byId('tab1');
  // 窗口关了 / 换了设备 / 不在端口页: 停止轮询, 不再打扰交换机
  if (byId('devModal').style.display === 'none' || currentIP !== ip || !tab1 || !tab1.classList.contains('active') || document.hidden) {
    stopPortLive(false);
    renderPorts();
    return;
  }
  authedApi('/api/device/live?ip=' + encodeURIComponent(ip)).then(function (d) {
    if (PORT_LIVE.ip !== ip || !PORT_LIVE.timer) { return; }
    if (d.warming) {
      byId('portLiveStat').innerText = '(已取第一次样, 几秒后出速率)';
    } else {
      var m = {};
      (d.ports || []).forEach(function (p) { m[p.index] = p; });
      PORT_LIVE.byIndex = m;
      PORT_LIVE.at = Date.now();
      byId('portLiveStat').innerText = '(' + d.at + ', 最近 ' + (d.interval_sec || 0).toFixed(1) + ' 秒)';
      renderPorts();
    }
    PORT_LIVE.timer = setTimeout(portLiveTick, 5000);
  }).catch(function (e) {
    if (PORT_LIVE.ip !== ip) { return; }
    byId('portLiveStat').innerText = '(读取失败: ' + String(e) + ', 10 秒后重试)';
    PORT_LIVE.timer = setTimeout(portLiveTick, 10000);
  });
}
