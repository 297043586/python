/*
 * 事件总线(v13): 模块之间通过事件通知, 不再互相直接调用对方的渲染函数。
 *
 *   App.on('summary', fn)   首页摘要数据(summaryData)刷新了                 ← app/refresh.js
 *   App.on('detail', fn)    设备详情(currentDetail)刷新了, 参数是详情数据  ← device/detail.js
 *   App.on('alert', fn)     有人确认 / 关闭 / 重新打开了告警 { id, action }   ← alerts/device-alerts.js
 *   App.on('session', fn)   登录状态 / 权限变了                            ← core/base.js
 *
 * 监听函数出错只打日志, 不影响别的监听者。
 */
const App = {
  handlers: {},
  on(ev, fn) { (this.handlers[ev] = this.handlers[ev] || []).push(fn); return fn; },
  off(ev, fn) { this.handlers[ev] = (this.handlers[ev] || []).filter((x) => x !== fn); },
  emit(ev, payload) {
    (this.handlers[ev] || []).slice().forEach((fn) => {
      try { fn(payload); } catch (e) { if (window.console) { console.error('[App.' + ev + ']', e); } }
    });
  }
};
