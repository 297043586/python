/*
 * 首页自动刷新 / 立即刷新。
 */
/* ---------- 自动刷新 ---------- */
var interval = 30, timeLeft = 30, paused = false;
try {
  var saved = parseInt(localStorage.getItem('refreshInterval'));
  if (saved >= 5 && saved <= 600) { interval = saved; }
} catch (e) {}
timeLeft = interval;
byId('intervalInput').value = interval;
byId('countdown').innerText = timeLeft;

function updateInterval(v) {
  var n = parseInt(v);
  if (n >= 5 && n <= 600) {
    interval = n; timeLeft = n;
    try { localStorage.setItem('refreshInterval', String(n)); } catch (e) {}
    byId('countdown').innerText = timeLeft;
  }
}
function toggleAuto() {
  paused = !paused;
  var b = byId('toggleBtn');
  b.innerText = paused ? '继续' : '暂停';
}
setInterval(function () {
  if (paused) { return; }
  timeLeft--;
  byId('countdown').innerText = timeLeft > 0 ? timeLeft : 0;
  if (timeLeft <= 0) { timeLeft = interval; refreshNow(); }
}, 1000);

function findSummary(ip) {
  for (var i = 0; i < summaryData.length; i++) {
    if (summaryData[i].ip === ip) { return summaryData[i]; }
  }
  return null;
}

function refreshNow() {
  pub('/api/data').then(function (d) {
    if (!d || !d.devices) { return; }
    summaryData = d.devices;
    byId('checkTime').innerText = d.check_time || '-';
    App.emit('summary', summaryData);
  }).catch(function () {});
  if (currentIP) {
    pub('/api/device?ip=' + encodeURIComponent(currentIP)).then(function (d) {
      currentDetail = d;
      fillModal(d);
    }).catch(function () {});
  }
}

// forceRefresh 是"立即刷新"按钮真正调用的函数。
// 之前这个按钮名不副实, 只是重新拉一遍服务端缓存好的数据——如果上一轮
// 巡检还没到点, 点了就跟没点一样, 看起来像坏了。现在先请求后端提前
// 触发一轮真正的 SNMP 巡检, 再轮询直到这一轮跑完(以 check_time 变化
// 为准), 期间按钮会显示"巡检中..."。
var forceRefreshBusy = false;
// forceRefresh 是"立即刷新"按钮的入口, 行为按登录状态分两种:
//   - 未登录的访客: 只重新拉一遍服务端已经算好的数据, 不触发新的 SNMP
//     巡检, 也不会弹登录框打断——点个刷新不该被要求先登录。
//   - 已登录的管理员: 真正提前触发一轮 SNMP 巡检, 轮询到这一轮跑完
//     (以 check_time 变化为准)才停, 期间按钮显示"巡检中..."。
function forceRefresh() {
  if (forceRefreshBusy) { return; }
  if (!authenticated) {
    refreshNow();
    return;
  }
  forceRefreshBusy = true;
  var btn = document.querySelector('.header .hbtn[onclick="forceRefresh()"]');
  var label = btn ? btn.innerHTML : '';
  var beforeTime = byId('checkTime').innerText;
  if (btn) { btn.innerText = '巡检中...'; btn.disabled = true; }

  rawApi('/api/refresh', {}).then(function (res) {
    if (res.status === 401) {
      // 会话恰好在这个节骨眼过期: 静默退回访客态, 不弹登录框打断操作
      authenticated = false; updateAuthUI();
      refreshNow();
      finishForceRefresh(btn, label);
      return;
    }
    if (res.data && res.data.status === 'cooldown') {
      // 冷却期内: 不强制触发新一轮, 但还是把当前已有数据刷新一次,
      // 好歹不让按钮看起来完全没反应。
      refreshNow();
      finishForceRefresh(btn, label);
      return;
    }
    pollUntilRoundDone(beforeTime, 0, btn, label);
  }).catch(function () { finishForceRefresh(btn, label); });
}
function pollUntilRoundDone(beforeTime, attempt, btn, label) {
  pub('/api/data').then(function (d) {
    if (d && d.devices) {
      summaryData = d.devices;
      byId('checkTime').innerText = d.check_time || '-';
      App.emit('summary', summaryData);
    }
    if (currentIP) {
      pub('/api/device?ip=' + encodeURIComponent(currentIP)).then(function (dd) {
        currentDetail = dd; fillModal(dd);
      }).catch(function () {});
    }
    var done = !d || d.check_time !== beforeTime || attempt >= 240; // 最多等 2 分钟(大网络一轮要跑很久)
    if (!done) {
      setTimeout(function () { pollUntilRoundDone(beforeTime, attempt + 1, btn, label); }, 500);
    } else {
      finishForceRefresh(btn, label);
    }
  }).catch(function () { finishForceRefresh(btn, label); });
}
function finishForceRefresh(btn, label) {
  forceRefreshBusy = false;
  if (btn) { btn.innerHTML = label || '⚡ 立即巡检'; btn.disabled = false; }
}

// 告警处理后只重新拉一次首页摘要(批量确认时合并成一次)
function refreshSummary() {
  return pub('/api/data').then(function (d) {
    if (!d || !d.devices) { return; }
    summaryData = d.devices;
    byId('checkTime').innerText = d.check_time || '-';
    App.emit('summary', summaryData);
  }).catch(function () {});
}
App.on('alert', debounce(refreshSummary, 400));
