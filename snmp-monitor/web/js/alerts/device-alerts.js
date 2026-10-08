/*
 * 设备详情里的告警列表 + 告警确认/关闭操作。
 */
/* ---------- 告警中心 ---------- */
function renderAlerts() {
  var d = currentDetail;
  if (!d) { return; }
  var hide = byId('hideAcked').checked;
  var all = [];
  (d.criticals || []).forEach(function (a) { all.push(a); });
  (d.warnings || []).forEach(function (a) { all.push(a); });

  var shown = all.filter(function (a) { return !(hide && (a.acked || a.closed)); });
  if (shown.length === 0) {
    byId('alertList').innerHTML = all.length === 0
      ? '<div style="color:var(--good);padding:24px;text-align:center;font-size:15px;">🎉 未发现故障告警与光衰/错包隐患。</div>'
      : '<div style="color:var(--fg-dim);padding:24px;text-align:center;">全部告警都已处理完毕。</div>';
    return;
  }

  var html = '';
  shown.forEach(function (a) {
    var done = a.acked || a.closed;
    // 已恢复(故障不再触发, 但还在 24 小时保留期内)跟"已处理"视觉上都
    // 归为柔和的一类, 但保留期内不确认/关闭的话到期会自动清掉——之前
    // 的行为是下一轮巡检不再命中就直接消失, 用户根本看不到发生过什么。
    var recovered = a.recovered && !done;
    var cls = (done || recovered) ? 'alert-done' : (a.level === 'critical' ? 'alert-crit' : 'alert-warn');
    var icon = done ? '✅' : (recovered ? '🟢' : (a.level === 'critical' ? '🚨' : '⚠️'));
    var tag = a.level === 'critical' ? '【致命故障】' : '【风险隐患】';
    var meta = '首次出现 ' + esc(a.first_seen) + ' · 最近 ' + esc(a.last_seen);
    if (done) {
      meta += ' · 由 ' + esc(a.acked_by || '-') + ' 于 ' + esc(a.acked_at || '-') + (a.closed ? ' 关闭' : ' 确认');
    } else if (recovered) {
      meta += ' · 已于 ' + esc(a.recovered_at || '-') + ' 恢复, 24 小时内未处理会自动归档';
    }
    var actions;
    if (done) {
      actions = '<button class="abtn" onclick="alertAction(&quot;' + a.id + '&quot;,&quot;unack&quot;)">重新打开</button>';
    } else if (recovered) {
      actions = '<button class="abtn" onclick="alertAction(&quot;' + a.id + '&quot;,&quot;ack&quot;)">知道了</button>';
    } else {
      actions = '<button class="abtn" onclick="alertAction(&quot;' + a.id + '&quot;,&quot;ack&quot;)">确认</button>' +
                '<button class="abtn" onclick="alertAction(&quot;' + a.id + '&quot;,&quot;close&quot;)">关闭</button>';
    }
    html += '<div class="alert-row ' + cls + '"><div>' + icon + ' <b>' + tag + '</b> ' + esc(a.text) +
            '<div class="alert-meta">' + meta + '</div></div>' +
            '<div class="alert-actions">' + actions + '</div></div>';
  });
  byId('alertList').innerHTML = html;
}

function applyAlertPatch(a, action) {
  a.acked = (action === 'ack' || action === 'close');
  a.closed = (action === 'close');
  if (!a.acked) { a.acked_by = ''; a.acked_at = ''; }
}

// 确认 / 关闭 / 重新打开; 成功后发 'alert' 事件, 详情窗口、告警中心、首页各自更新
function alertAction(id, action) {
  return authedApi('/api/alerts/action', { id: id, action: action })
    .then(function () { App.emit('alert', { id: id, action: action }); })
    .catch(function (e) { alert('操作失败: ' + e); });
}
// 详情窗口里这台设备的告警列表跟着更新
App.on('alert', function (ev) {
  if (!currentDetail) { return; }
  var open = 0;
  [currentDetail.criticals || [], currentDetail.warnings || []].forEach(function (list) {
    list.forEach(function (a) { if (a.id === ev.id) { applyAlertPatch(a, ev.action); } if (!a.acked && !a.closed && !a.recovered) { open++; } });
  });
  currentDetail.open_alerts = open;
  renderAlerts();
  byId('alertCount').innerText = open;
});
detailPart({ fill: function () { renderAlerts(); } });
