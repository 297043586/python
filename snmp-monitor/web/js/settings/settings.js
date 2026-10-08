/*
 * 系统设置窗口。
 */
/* ================= 系统设置窗口 ================= */
var SET = { cfg: null, orig: null, def: null, sec: 0, l7: false, l7orig: false, flowOK: false, restartPending: [] };
var SET_THEMES = [['', '(不指定, 浅色)'], ['theme-light', '🌤️ 浅色'], ['theme-dark', '🌙 深色'], ['theme-slate', '🌊 靛蓝'], ['theme-paper', '📜 纸感'], ['theme-nord', '🧊 北欧极地'],
  ['theme-terminal', '💻 终端绿'], ['theme-aurora', '🔮 极光紫'], ['style-minimal', '◻️ 极简'], ['style-swiss', '🟥 瑞士国际主义'], ['style-flat', '🟦 扁平化'],
  ['style-bento', '🍱 便当格'], ['style-magazine', '📰 杂志'], ['style-glass', '🪟 玻璃拟态'], ['style-blackgold', '🏆 黑金'], ['style-layered', '🗂️ 维度分层'], ['style-scifi', '🛸 科幻 HUD']];
var SET_BS_STYLES = [['tech', '科技蓝 · 经典三栏(默认)'], ['cyber', '赛博霓虹 · 全屏 HUD'], ['aurora', '极光 · 便当格玻璃卡片'], ['gold', '指挥中心 · 黑金铭牌'], ['mono', '军绿雷达 · 雷达屏 + 终端'], ['redalert', '红色警戒 · 事件处置模式']];
var SET_BS_CENTER = [['map', '站点态势地图(飞线)'], ['topo3d', '3D 网络拓扑(three.js)'], ['dc3d', '3D 数据中心机房(three.js)'], ['carousel', '以上三个轮播']];
var SET_SCHEMA = [
  { name: '基本', icon: '🖥️', desc: '网页服务和登录相关。标了「重启生效」的项只写进 config.json, 需要重启程序才会用新值。', items: [
    { k: 'ui.site_title', t: '页面标题', type: 'text', h: '首页左上角和浏览器标签页上显示的名字' },
    { k: 'ui.default_theme', t: '默认主题', type: 'select', opts: SET_THEMES, h: '浏览器还没选过主题时用这个; 每台电脑自己在右上角选的主题优先' },
    { k: 'ui.page_refresh_seconds', t: '首页自动刷新(秒)', type: 'int', min: 5, max: 600, h: '同上, 浏览器自己改过的优先' },
    { k: 'session_hours', t: '登录有效期(小时)', type: 'int', min: 1, max: 720, h: '有操作会自动续期; 改了对之后的登录/续期生效' },
    { k: 'bind_address', t: '监听地址', type: 'text', restart: 1, h: '0.0.0.0 = 所有网卡; 只想本机访问填 127.0.0.1' },
    { k: 'web_port', t: 'Web 端口', type: 'int', min: 1, max: 65535, restart: 1 },
    { k: 'data_dir', t: '数据目录', type: 'text', restart: 1, h: '设备库/告警/趋势/备份等都存在这里; 改了之后要把旧目录里的文件搬过去' },
    { k: 'admin_user', t: '管理员用户名', type: 'text', restart: 1, h: '口令在右上角「🔑 修改口令」里改' }
  ] },
  { name: 'SNMP 采集', icon: '📡', desc: '巡检调度和 SNMP 协议参数, 保存后下一轮巡检开始生效(改了巡检间隔会立即触发一轮)。单台设备的团体名/版本/采集范围在「设备管理」里。', items: [
    { k: 'check_interval_seconds', t: '巡检间隔(秒)', type: 'int', min: 30, max: 86400, h: '每隔多久采一轮全部设备' },
    { k: 'snmp_port', t: 'SNMP 端口', type: 'int', min: 1, max: 65535 },
    { k: 'timeout_seconds', t: '单次请求超时(秒)', type: 'int', min: 1, max: 60 },
    { k: 'retries', t: '超时重试次数', type: 'int', min: 0, max: 5 },
    { k: 'max_concurrency', t: '同时巡检的设备数', type: 'int', min: 1, max: 500, h: '设备多、服务器性能好可以调大' },
    { k: 'walk_parallel', t: '单台设备并行会话数', type: 'int', min: 1, max: 16, h: '老旧/低端设备扛不住就调到 1~2' },
    { k: 'max_repetitions', t: 'GETBULK 批大小', type: 'int', min: 1, max: 200, h: '老交换机容易超时就调小(10~20)' },
    { k: 'heavy_every_n_rounds', t: 'MAC/ARP 大表每 N 轮采一次', type: 'int', min: 1, max: 100, h: 'MAC 表很大时调成 2~5 可以明显减轻设备负担' },
    { k: 'uplink_mac_threshold', t: '上联口判定: 学到的 MAC 数 ≥', type: 'int', min: 1, max: 10000, h: '一个口学到这么多 MAC 就认为后面还挂着交换机' },
    { k: 'trend_retention_hours', t: '趋势数据保留(小时)', type: 'int', min: 1, max: 2160 },
    { k: 'vlan_community_format', t: '按 VLAN 团体名格式', type: 'text', wide: 1, h: '部分 Cisco 类设备要用 "团体名@VLAN" 才能看全 MAC 表, 这里填 %s@%s; 不需要就留空' }
  ] },
  { name: '告警', icon: '🚨', desc: '每类告警可以单独关闭。阈值改了下一轮巡检生效。', items: [
    { k: 'alerts.cpu', t: 'CPU 使用率 ≥ (%)', type: 'alert', h: '≥90% 固定升级为严重' },
    { k: 'alerts.mem', t: '内存使用率 ≥ (%)', type: 'alert' },
    { k: 'alerts.bandwidth', t: '端口带宽利用率 ≥ (%)', type: 'alert' },
    { k: 'alerts.port_error', t: '单轮错包增量 ≥', type: 'alert' },
    { k: 'alerts.optical', t: '收光功率 ≤ (dBm)', type: 'alert', h: '负数, 比如 -25' },
    { k: 'alerts.broadcast', t: '端口入方向广播 ≥ (包/秒)', type: 'alert', h: '广播风暴 / 环路的信号; 5 倍以上升级为严重。「🩺 二层体检」也用这个阈值' },
    { k: 'alerts.stp', t: '生成树拓扑变化 ≥ (次/轮)', type: 'alert', h: '两轮巡检之间变化的次数; 5 倍以上升级为严重' },
    { k: 'alert_history_days', t: '告警历史保留(天)', type: 'int', min: 7, max: 3650, h: '告警中心「历史记录」里能查多久以前的; 数据按月存在 data/alert_history/, 过期的整月删除' },
    { k: 'alert_history_max_records', t: '告警历史总条数上限', type: 'int', min: 1000, max: 2000000, h: '设备/告警特别多时, 时间还没到但条数先超了, 超出的按时间顺序丢最老的; 立即生效' },
    { k: 'webhook_url', t: 'Webhook 推送地址', type: 'text', wide: 1, h: '每轮有新告警时 POST 一份 JSON(企业微信/钉钉机器人等), 留空不推送' }
  ] },
  { name: '被动采集', icon: '📥', desc: '交换机主动推过来的数据。端口 = 0 表示不启用; 端口改了需要重启程序。', items: [
    { k: 'collectors.syslog_port', t: 'Syslog 接收端口', type: 'int', min: 0, max: 65535, restart: 1, h: '标准 514 需要管理员权限, 也可以用 1514 再在交换机上配对应端口' },
    { k: 'collectors.netflow_port', t: 'NetFlow v5 端口', type: 'int', min: 0, max: 65535, restart: 1, h: '常用 2055 / 9995' },
    { k: 'collectors.sflow_port', t: 'sFlow v5 端口', type: 'int', min: 0, max: 65535, restart: 1, h: '常用 6343' },
    { k: '$l7', t: '解析应用层域名(L7)', type: 'l7', h: '从 sFlow 报文头里解析 DNS 查询 / HTTPS SNI / HTTP Host, 用于应用识别; 有合规要求可以关掉。立即生效' },
    { k: 'syslog_max_lines', t: '每台设备 syslog 保留条数', type: 'int', min: 50, max: 20000, h: '超过滚掉最旧的; 立即生效' },
    { k: 'flow_ring_size', t: '每台设备流量记录保留条数', type: 'int', min: 500, max: 100000, restart: 1, h: '1/1000 抽样下, 高流量口几秒钟就能刷满; 调大能看更长的时间窗口, 也更占内存(每条约 300 字节)' }
  ] },
  { name: '大屏', icon: '📺', desc: '所有打开大屏的浏览器共用(值班室电视不用单独设置)。大屏右上角也可以临时切换样式。', items: [
    { k: 'ui.bigscreen_title', t: '大屏标题', type: 'text' },
    { k: 'ui.bigscreen_style', t: '样式', type: 'select', opts: SET_BS_STYLES },
    { k: 'ui.bigscreen_center', t: '中间主画面', type: 'select', opts: SET_BS_CENTER, h: '3D 画面需要浏览器支持 WebGL(近几年的电脑基本都支持)' },
    { k: 'ui.bigscreen_refresh_seconds', t: '数据刷新间隔(秒)', type: 'int', min: 5, max: 3600 },
    { k: 'ui.bigscreen_carousel_seconds', t: '轮播间隔(秒)', type: 'int', min: 10, max: 3600, h: '中间主画面选「轮播」时每个画面停留多久' },
    { k: 'ui.bigscreen_scroll_speed', t: '告警列表滚动速度', type: 'int', min: 0, max: 300, h: '像素/秒, 0 = 不自动滚动(鼠标滚轮/拖动随时可以手动滚)' },
    { k: 'ui.bigscreen_risk_count', t: '风险设备排行显示几台', type: 'int', min: 1, max: 30 },
    { k: 'ui.bigscreen_hot_count', t: '最忙端口显示几条', type: 'int', min: 1, max: 30 },
    { k: 'ui.bigscreen_animations', t: '动画效果(飞线/粒子/扫描)', type: 'bool', h: '投影仪/老电脑卡顿时可以关掉' }
  ] },
  { name: '拓扑 / 机柜', icon: '🗺️', desc: '拓扑图、3D 视图、机柜/机房视图的显示参数。', items: [
    { k: 'ui.topo_refresh_seconds', t: '拓扑自动刷新(秒)', type: 'int', min: 10, max: 3600 },
    { k: 'ui.topo3d_auto_rotate', t: '3D 拓扑默认自动旋转', type: 'bool', h: '浏览器里手动切换过的以浏览器为准' },
    { k: 'ui.topo3d_particles', t: '3D 拓扑默认显示流量粒子', type: 'bool' },
    { k: 'ui.rack_refresh_seconds', t: '机柜 / 3D 机房自动刷新(秒)', type: 'int', min: 10, max: 3600 },
    { k: 'ui.weak_optical_dbm', t: '面板上"收光偏弱"标记线(dBm)', type: 'num', h: '只影响面板/端口表的橙色标记; 告警阈值在「告警」里' },
    { k: 'ui.busy_util_percent', t: '"高负载"标记线(%)', type: 'num' }
  ] },
  { name: '配置备份命令', icon: '💾', desc: '「配置备份」里厂商命令集选「自动」或指定厂商时用的默认命令, 每行一条, 最后一条命令的输出保存为配置。前面的命令用来关闭分页(否则输出会停在 ---- More ----)。单台设备在凭据里填了自定义命令的以设备为准。', items: [
    { k: 'backup_commands.huawei', t: '华为', type: 'cmds', h: '常用: display current-configuration (简写 dis cu)' },
    { k: 'backup_commands.h3c', t: 'H3C', type: 'cmds', h: '常用: display current-configuration (简写 dis cu)' },
    { k: 'backup_commands.ruijie', t: '锐捷', type: 'cmds', h: '常用: show running-config (简写 show run)' },
    { k: 'backup_commands.cisco', t: 'Cisco', type: 'cmds', h: '常用: show running-config (简写 show run)' },
    { k: 'backup_commands.generic', t: '其他 / 识别不出品牌', type: 'cmds', h: '按 Cisco 风格处理' }
  ] },
  { name: '存储', icon: '💽', desc: '数据目录所在磁盘的占用, 以及各类数据存多少天 / 多少份。按天清理每 6 小时执行一次, 保存后立即执行一次。', custom: 'storage', items: [] },
  { name: '清理工具', icon: '🧹', desc: '', custom: 'cleanup', items: [] },
  { name: '其他', icon: '📌', desc: '', items: [] }
];
function setGet(o, path) { return path.split('.').reduce(function (x, k) { return x === undefined || x === null ? undefined : x[k]; }, o); }
function setPut(o, path, v) {
  var ks = path.split('.'), last = ks.pop();
  ks.forEach(function (k) { if (!o[k] || typeof o[k] !== 'object') { o[k] = {}; } o = o[k]; });
  o[last] = v;
}
function openSettings() {
  if (!authenticated) { openAuthGate(function () { openSettings(); }); return; }
  byId('settingsModal').style.display = 'flex';
  loadSettings();
}
function closeSettings() { byId('settingsModal').style.display = 'none'; }
function loadSettings() {
  byId('setMsg').innerText = '';
  if (typeof VEN !== 'undefined') { VEN.list = null; }
  return authedApi('/api/settings').then(function (d) {
    SET.cfg = JSON.parse(JSON.stringify(d.config));
    SET.orig = JSON.parse(JSON.stringify(d.config));
    SET.def = d.defaults || {};
    SET.l7 = SET.l7orig = !!d.l7_enabled;
    SET.flowOK = !!d.flow_available;
    SET.restartPending = d.restart_pending || [];
    byId('setFile').innerText = '配置文件: ' + (d.file || 'config.json') + ' · 保存时自动备份为 config.json.bak; 大部分设置立即生效, 标「重启生效」的需要重启程序。';
    renderSettings();
  }).catch(function (e) { byId('setBody').innerHTML = '<div class="empty">加载失败: ' + esc(String(e)) + '</div>'; });
}
function renderSettings() {
  byId('setNav').innerHTML = SET_SCHEMA.map(function (s, i) {
    return '<button class="' + (i === SET.sec ? 'active' : '') + '" onclick="SET.sec=' + i + ';renderSettings()">' + s.icon + ' ' + esc(s.name) + '</button>';
  }).join('');
  var sec = SET_SCHEMA[SET.sec];
  var html = '<div class="set-sec"><h3>' + sec.icon + ' ' + esc(sec.name) + '</h3>' + (sec.desc ? '<div class="set-desc">' + esc(sec.desc) + '</div>' : '');
  if (SET.restartPending.length && SET.sec === 0) {
    html += '<div class="agg-issue" style="margin-bottom:10px;">⚠️ 这些设置已写入 config.json, 重启程序后生效: ' + SET.restartPending.map(esc).join(', ') + '</div>';
  }
  if (sec.name === '其他') {
    html += settingsOtherHTML();
  } else if (sec.custom) {
    html += window['settings_' + sec.custom + 'HTML']();
  } else {
    html += '<div class="set-grid">' + sec.items.map(settingsItemHTML).join('') + '</div>';
  }
  byId('setBody').innerHTML = html + '</div>';
}
function setChanged(it) {
  if (it.type === 'l7') { return SET.l7 !== SET.l7orig; }
  return JSON.stringify(setGet(SET.cfg, it.k)) !== JSON.stringify(setGet(SET.orig, it.k));
}
function settingsItemHTML(it) {
  var v = it.type === 'l7' ? SET.l7 : setGet(SET.cfg, it.k), dv = setGet(SET.def, it.k);
  var id = 'set_' + it.k.replace(/[^a-z0-9]/gi, '_');
  var badge = it.restart ? ' <span class="set-badge">重启生效</span>' : '';
  var key = it.k.charAt(0) === '$' ? '' : ' <span class="k">' + esc(it.k) + '</span>';
  var input = '', defText = '';
  var on = 'onchange="settingsInput(&quot;' + it.k + '&quot;,this)"';
  switch (it.type) {
    case 'int': case 'num':
      input = '<input type="number" id="' + id + '" value="' + esc(v === undefined || v === null ? '' : v) + '"' + (it.min !== undefined ? ' min="' + it.min + '"' : '') + (it.max !== undefined ? ' max="' + it.max + '"' : '') + ' step="' + (it.type === 'num' ? 'any' : '1') + '" data-type="' + it.type + '" ' + on + '>';
      defText = dv !== undefined ? '默认 ' + dv : '';
      break;
    case 'text':
      input = '<input type="text" id="' + id + '" value="' + esc(v || '') + '" data-type="text" ' + on + '>';
      defText = dv ? '默认 ' + dv : '默认为空';
      break;
    case 'select':
      input = '<select id="' + id + '" data-type="text" ' + on + '>' + it.opts.map(function (o) { return '<option value="' + esc(o[0]) + '"' + (o[0] === (v || '') ? ' selected' : '') + '>' + esc(o[1]) + '</option>'; }).join('') + '</select>';
      var dn = it.opts.filter(function (o) { return o[0] === (dv || ''); })[0];
      defText = dn ? '默认 ' + dn[1] : '';
      break;
    case 'bool': case 'l7':
      input = '<div class="set-row"><label class="set-switch"><input type="checkbox" id="' + id + '" data-type="bool"' + (v ? ' checked' : '') + ' ' + on + '><span></span></label><span class="h">' + (v ? '开启' : '关闭') + '</span></div>';
      if (it.type === 'l7' && !SET.flowOK) { input += '<div class="d">当前没有启用 NetFlow/sFlow 采集, 这个开关暂时用不到</div>'; }
      defText = it.type === 'l7' ? '' : '默认 ' + (dv ? '开启' : '关闭');
      break;
    case 'alert':
      var a = v || {};
      input = '<div class="set-row"><label class="set-switch" title="启用这类告警"><input type="checkbox" data-type="alert-on"' + (a.disabled ? '' : ' checked') + ' ' + on + '><span></span></label>' +
        '<input type="number" step="any" value="' + esc(a.threshold) + '" data-type="alert-th" ' + on + '><span class="h">' + (a.disabled ? '已关闭' : '已启用') + '</span></div>';
      defText = dv ? '默认 ' + dv.threshold : '';
      break;
    case 'cmds':
      input = '<textarea id="' + id + '" rows="3" data-type="text" ' + on + '>' + esc(v || '') + '</textarea>';
      defText = dv ? '默认: ' + String(dv).replace(/\n/g, ' ⏎ ') : '';
      break;
  }
  return '<div class="set-item' + (it.wide || it.type === 'cmds' ? ' wide' : '') + (setChanged(it) ? ' changed' : '') + '"><label class="t">' + esc(it.t) + badge + key + '</label>' + input +
    (it.h ? '<div class="h">' + esc(it.h) + '</div>' : '') + (defText ? '<div class="d">' + esc(defText) + '</div>' : '') + '</div>';
}
function settingsInput(k, el) {
  var t = el.getAttribute('data-type');
  if (k === '$l7') { SET.l7 = el.checked; renderSettings(); return; }
  if (t === 'int') { setPut(SET.cfg, k, el.value === '' ? 0 : parseInt(el.value, 10)); }
  else if (t === 'num') { setPut(SET.cfg, k, el.value === '' ? 0 : parseFloat(el.value)); }
  else if (t === 'bool') { setPut(SET.cfg, k, el.checked); }
  else if (t === 'alert-on') { setPut(SET.cfg, k + '.disabled', !el.checked); }
  else if (t === 'alert-th') { setPut(SET.cfg, k + '.threshold', parseFloat(el.value)); }
  else { setPut(SET.cfg, k, el.value); }
  var item = el.closest('.set-item');
  if (item) {
    var it = null;
    SET_SCHEMA.forEach(function (s) { s.items.forEach(function (x) { if (x.k === k) { it = x; } }); });
    if (it) { item.classList.toggle('changed', setChanged(it)); }
    if (t === 'bool' || t === 'alert-on') { renderSettings(); }
  }
}
function settingsResetSection() {
  var sec = SET_SCHEMA[SET.sec];
  if (sec.custom === 'vendors') { venReset(); return; }
  sec.items.forEach(function (it) {
    if (it.type === 'l7' || it.restart) { return; }
    var dv = setGet(SET.def, it.k);
    if (dv !== undefined) { setPut(SET.cfg, it.k, JSON.parse(JSON.stringify(dv))); }
  });
  renderSettings();
  byId('setMsg').style.color = 'var(--fg-dim)';
  byId('setMsg').innerText = '已把本页恢复为默认值(需要重启的项不动), 点「保存设置」才会生效';
}
function saveSettings() {
  var msg = byId('setMsg');
  msg.style.color = 'var(--fg-dim)'; msg.innerText = '保存中...';
  var body = { config: SET.cfg };
  if (SET.l7 !== SET.l7orig) { body.l7_enabled = SET.l7; }
  // 厂商规则单独一个接口; 先存它(校验失败就整个不保存, 免得只存了一半)
  var pre = typeof venDirty === 'function' && venDirty() ? venSave() : Promise.resolve();
  pre.then(function () { return authedApi('/api/settings', body); }).then(function (d) {
    msg.style.color = 'var(--good)';
    var p = d.restart_pending || [];
    msg.innerText = '✅ 已保存并生效' + (p.length ? '; 以下设置需要重启程序: ' + p.join(', ') : '');
    SET.orig = JSON.parse(JSON.stringify(SET.cfg));
    SET.l7orig = SET.l7;
    SET.restartPending = p;
    renderSettings();
    loadUISettings();
  }).catch(function (e) { msg.style.color = 'var(--bad)'; msg.innerText = '❌ ' + e; });
}
// 💽 存储: 磁盘 / 各类数据占用 + 保留天数份数(配置备份、巡检报告、下发审计日志在这里改; 告警历史 / 趋势只显示)
var SET_STORAGE_FIELDS = [
  { k: 'backup_keep', t: '配置备份: 每台保留份数', min: 1, max: 1000, h: '超过的删最旧的; 调小后立即删掉多出来的' },
  { k: 'backup_keep_days', t: '配置备份: 保留天数', min: 0, max: 36500, h: '0 = 不按天清; 每台最新的一份不管多旧都保留' },
  { k: 'report_keep', t: '巡检报告: 最多保留份数', min: 1, max: 100000 },
  { k: 'report_keep_days', t: '巡检报告: 保留天数', min: 0, max: 36500, h: '0 = 不按天清' },
  { k: 'push_log_days', t: '配置下发审计日志: 保留天数', min: 0, max: 36500, h: 'data/push_log.jsonl, 谁在什么时候对哪台设备下发了什么; 0 = 永久保留' }
];
function settings_storageHTML() {
  setTimeout(settingsLoadStorage, 0);
  return '<div id="setStorage"><div class="set-desc">正在统计…</div></div>';
}
function settingsLoadStorage() {
  authedApi('/api/settings/storage').then(function (d) {
    var box = byId('setStorage'); if (!box) { return; }
    var hb = function (n) { var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0; n = +n || 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; } return (i ? n.toFixed(1) : n) + ' ' + u[i]; };
    var h = '<div class="set-desc">数据目录: <code>' + esc(d.data_dir || '') + '</code>, 共占用 <b>' + hb(d.total) + '</b></div>';
    if (d.disk && d.disk.total) {
      var used = d.disk.total - d.disk.free, pct = used / d.disk.total * 100;
      var col = pct >= 95 ? 'var(--bad)' : pct >= 85 ? 'var(--warn)' : 'var(--good)';
      h += '<div class="set-item wide" style="margin-bottom:12px;"><label class="t">所在磁盘 <span style="font-weight:400;color:var(--fg-dim);">已用 ' + hb(used) + ' / 共 ' + hb(d.disk.total) + ', 剩余 <b style="color:' + col + '">' + hb(d.disk.free) + '</b> (' + pct.toFixed(1) + '%)</span></label>' +
        '<div style="height:10px;border-radius:5px;background:var(--card-border);overflow:hidden;"><div style="height:100%;width:' + Math.min(100, pct).toFixed(1) + '%;background:' + col + ';"></div></div>' +
        (pct >= 90 ? '<div class="h" style="color:var(--bad);">磁盘快满了: 可以调小下面的保留天数 / 份数, 或在「🧹 清理工具」里清空历史数据</div>' : '') + '</div>';
    }
    var max = Math.max.apply(null, (d.parts || []).map(function (p) { return p.size; }).concat([1]));
    h += '<table class="set-fixed" style="margin-bottom:14px;"><tbody>' + (d.parts || []).map(function (p) {
      return '<tr><td>' + esc(p.name) + '</td><td style="width:40%;"><div style="height:8px;border-radius:4px;background:var(--accent);opacity:.7;width:' + Math.max(1, p.size / max * 100).toFixed(1) + '%;"></div></td><td style="text-align:right;white-space:nowrap;">' + hb(p.size) + ' · ' + p.files + ' 个文件</td></tr>';
    }).join('') + '</tbody></table>';
    var r = d.retention || {};
    h += '<div class="set-grid">' + SET_STORAGE_FIELDS.map(function (f) {
      return '<div class="set-item"><label class="t">' + esc(f.t) + '</label><input type="number" id="stg_' + f.k + '" min="' + f.min + '" max="' + f.max + '" step="1" value="' + esc(r[f.k] === undefined ? '' : r[f.k]) + '">' + (f.h ? '<div class="h">' + esc(f.h) + '</div>' : '') + '</div>';
    }).join('');
    var c = SET.cfg || {};
    h += '<div class="set-item"><label class="t">告警历史保留(天)</label><div>' + esc(c.alert_history_days || '-') + ' 天 <button class="btn sm" onclick="SET.sec=SET_SCHEMA.findIndex(function(s){return s.name===\x27告警\x27});renderSettings()">去「告警」里改</button></div></div>';
    h += '<div class="set-item"><label class="t">趋势数据保留(小时)</label><div>' + esc(c.trend_retention_hours || '-') + ' 小时 <span class="h">在「📡 SNMP 采集」里改</span></div></div>';
    h += '</div><div style="margin-top:12px;"><button class="hbtn" onclick="settingsSaveStorage()">💾 保存保留设置并立即清理</button> <span id="stgMsg" style="font-size:12.5px;"></span></div>';
    box.innerHTML = h;
  }).catch(function (e) { var box = byId('setStorage'); if (box) { box.innerHTML = '<div class="set-desc" style="color:var(--bad);">读取失败: ' + esc(String(e)) + '</div>'; } });
}
function settingsSaveStorage() {
  var body = {};
  SET_STORAGE_FIELDS.forEach(function (f) { body[f.k] = parseInt(byId('stg_' + f.k).value, 10) || 0; });
  var m = byId('stgMsg');
  m.style.color = 'var(--fg-dim)'; m.innerText = '保存中…';
  authedApi('/api/settings/storage', body).then(function (d) {
    settingsLoadStorage();
    setTimeout(function () { var m2 = byId('stgMsg'); if (m2) { m2.style.color = 'var(--good)'; m2.innerText = '✅ 已保存' + (d.pruned ? ', 已清理: ' + d.pruned : ''); } }, 300);
  }).catch(function (e) { m.style.color = 'var(--bad)'; m.innerText = '❌ ' + e; });
}
var SET_CLEANUP_TARGETS = [
  { k: 'syslog', t: '设备日志(Syslog)', h: '清空所有设备当前保留在内存里的 syslog 缓存(不影响接收端口配置, 之后新收到的照常保留)' },
  { k: 'flows', t: '流量记录', h: '清空所有设备的 NetFlow/sFlow 原始记录、10 秒粒度趋势桶、接口计数器基线(下一轮采集会重新热身)' },
  { k: 'trend', t: '性能趋势曲线', h: '清空 CPU / 内存 / 带宽这些历史趋势点, 不影响当前实时数据; 不影响端口速率基线' },
  { k: 'alert_history', t: '告警历史', h: '清空「告警中心 → 历史记录」里的全部事件记录(按月存的文件一起删); 进行中还没恢复的告警不受影响' }
];
function settings_cleanupHTML() {
  return '<div class="set-desc">立即执行, 不可撤销; 只清缓存/历史数据, 不影响设备列表和当前配置。</div><div class="set-links" style="flex-direction:column;align-items:flex-start;gap:10px;">' +
    SET_CLEANUP_TARGETS.map(function (t) {
      return '<div class="set-item" style="width:100%;"><label class="t">' + esc(t.t) + '</label>' +
        '<button class="filter-btn" onclick="settingsCleanup(&quot;' + t.k + '&quot;,&quot;' + esc(t.t) + '&quot;)">🧹 清空</button>' +
        '<div class="h">' + esc(t.h) + '</div></div>';
    }).join('') + '</div>';
}
function settingsCleanup(target, label) {
  if (!confirm('确定要清空「' + label + '」吗? 这个操作立即生效, 不可撤销。')) { return; }
  var msg = byId('setMsg');
  msg.style.color = 'var(--fg-dim)'; msg.innerText = '正在清空…';
  authedApi('/api/settings/cleanup', { target: target }).then(function (d) {
    msg.style.color = 'var(--good)';
    msg.innerText = '✅ ' + (d.message || '已完成');
  }).catch(function (e) { msg.style.color = 'var(--bad)'; msg.innerText = '❌ ' + e; });
}
function settingsOtherHTML() {
  var go = function (fn, t) { return '<button class="filter-btn" onclick="closeSettings();' + fn + '()">' + t + '</button>'; };
  return '<div class="set-desc">下面这些设置有各自的窗口, 点按钮直接打开:</div><div class="set-links">' +
    go('openDeviceManager', '⚙️ 设备管理(团体名/品牌/采集范围)') + go('openReports', '📑 巡检报告(定时/AI 接口/游客可见)') + go('openBackups', '💾 配置备份(定时/保留份数/SSH)') + go('openPush', '🛠️ 配置下发(sFlow / syslog)') +
    go('openRacks', '🗄️ 机柜 / 3D 机房布局') + go('openPassword', '🔑 修改口令') + (userRole === 'admin' ? go('openUsers', '👥 账号权限') : '') + '</div>' +
    '<div class="set-desc">以下参数写死在程序里, 不开放修改(改了容易出问题, 或基本没有调整的需要):</div>' +
    '<table class="set-fixed"><tbody>' +
    '<tr><td>端口"近期抖动"判定窗口</td><td>10 分钟内 UP/DOWN 变化过</td></tr>' +
    '<tr><td>CPU 严重告警</td><td>≥ 90% 固定升级为严重(警告阈值可调)</td></tr>' +
    '<tr><td>趋势采样点上限</td><td>20000 点 / 设备(由保留时长和巡检间隔自动换算)</td></tr>' +
    '<tr><td>登录失败锁定</td><td>同一来源连续失败会临时锁定, 防暴力破解</td></tr>' +
    '<tr><td>页面手动刷新频率限制</td><td>「立即刷新」有最小间隔, 防止连点把交换机打满</td></tr>' +
    '<tr><td>设备清单</td><td>config.json 里的 devices 只在首次启动时导入一次, 之后以「设备管理」为准</td></tr>' +
    '</tbody></table>';
}
modalRegister('settingsModal', closeSettings);
