/*
 * 系统设置 →「厂商 / OID」页: 编辑品牌识别规则和私有 OID(后端见 vendors.go)。
 */
/* ================= 设置 → 厂商 / OID ================= */
var VEN = { list: null, defs: {}, orig: '', sel: 0, devices: [], test: { dev: '', oid: '', busy: false, res: null } };
SET_SCHEMA.splice(SET_SCHEMA.length - 1, 0, { name: '厂商 / OID', icon: '🏭', custom: 'vendors', items: [],
  desc: '按品牌配置自动识别规则、私有 OID 和温度告警阈值。内置的华为 / H3C / 锐捷 / Cisco 可以改(随时恢复默认), 其他品牌(中兴、迈普、TP-Link、神州数码...)点「新增品牌」添加。保存后下一轮巡检生效。标准 MIB(接口/LLDP/MAC/ARP/ENTITY-SENSOR 光功率与温度)对所有品牌通用；收发器介质类型 OID 可在下方配置。' });
function venLoad() {
  VEN.list = null;
  return Promise.all([authedApi('/api/vendors'), authedApi('/api/devices')]).then(function (r) {
    VEN.list = r[0].vendors || [];
    VEN.defs = {};
    (r[0].defaults || []).forEach(function (d) { VEN.defs[d.id] = d; });
    VEN.orig = JSON.stringify(VEN.list);
    VEN.devices = (r[1].devices || []).filter(function (d) { return !d.disabled; });
    if (!VEN.test.dev && VEN.devices.length) { VEN.test.dev = VEN.devices[0].id; }
    if (VEN.sel >= VEN.list.length) { VEN.sel = 0; }
    if (SET_SCHEMA[SET.sec].custom === 'vendors') { renderSettings(); }
  }).catch(function (e) { byId('setBody').innerHTML = '<div class="empty">加载厂商规则失败: ' + esc(String(e)) + '</div>'; });
}
function venDirty() { return VEN.list && JSON.stringify(VEN.list) !== VEN.orig; }
function settings_vendorsHTML() {
  if (!VEN.list) { venLoad(); return '<div class="empty">加载中...</div>'; }
  var p = VEN.list[VEN.sel];
  var nav = VEN.list.map(function (v, i) {
    var tag = v.builtin ? (v.modified ? '已修改' : '内置') : '自定义';
    return '<button class="' + (i === VEN.sel ? 'active' : '') + '" onclick="VEN.sel=' + i + ';renderSettings()">' + (v.builtin && !v.modified ? '' : '<span class="dot"></span>') + esc(v.name || v.id) + '<small>' + tag + '</small></button>';
  }).join('') + '<button class="add" onclick="venAdd()">＋ 新增品牌</button>';
  return '<div class="ven-wrap"><div class="ven-list">' + nav + '</div><div class="ven-ed">' + (p ? venEditorHTML(p) : '') + venTestHTML() + '</div></div>';
}
function venLines(a) { return (a || []).join('\n'); }
function venAttr(s) { return esc(s === undefined || s === null ? '' : String(s)); }
function venField(label, key, val, hint, opts) {
  opts = opts || {};
  var input = opts.area
    ? '<textarea rows="' + (opts.rows || 3) + '" onchange="venSet(&quot;' + key + '&quot;,this.value)"' + (opts.ro ? ' readonly' : '') + '>' + esc(val) + '</textarea>'
    : '<input type="' + (opts.type || 'text') + '" value="' + venAttr(val) + '" onchange="venSet(&quot;' + key + '&quot;,this.value)"' + (opts.ro ? ' readonly' : '') + (opts.ph ? ' placeholder="' + venAttr(opts.ph) + '"' : '') + '>';
  return '<div class="set-item' + (opts.wide ? ' wide' : '') + '"><label class="t">' + esc(label) + '</label>' + input + (hint ? '<div class="h">' + esc(hint) + '</div>' : '') + '</div>';
}
function venOIDRows(key, list, hint) {
  var rows = (list || []).map(function (o, i) {
    return '<div class="ven-row"><input class="oid" value="' + venAttr(o) + '" placeholder=".1.3.6.1.4.1...." onchange="venArr(&quot;' + key + '&quot;,' + i + ',this.value)">' +
      '<button class="try" title="在设备上试一下" onclick="venTry(' + venQ(o) + ')">🔬</button><button class="x" title="删除" onclick="venArrDel(&quot;' + key + '&quot;,' + i + ')">✕</button></div>';
  }).join('');
  return '<div class="ven-rows">' + rows + '<button class="ven-addrow" onclick="venArrAdd(&quot;' + key + '&quot;,&quot;&quot;)">＋ 添加 OID</button>' + (hint ? '<div class="h">' + esc(hint) + '</div>' : '') + '</div>';
}
function venQ(s) { return '&quot;' + esc(String(s || '').replace(/[^0-9.]/g, '')) + '&quot;'; }
function venEditorHTML(p) {
  var h = '';
  h += '<div class="set-grid">' +
    venField('品牌代码', 'id', p.id, p.builtin ? '内置品牌的代码不能改' : '小写字母开头, 如 zte / maipu / dcn; 设备管理里选品牌、配置备份都用它', { ro: p.builtin }) +
    venField('显示名称', 'name', p.name, '') +
    venField('sysObjectID 企业号', 'enterprises', (p.enterprises || []).join(', '), 'sysObjectID 里 1.3.6.1.4.1. 后面那一段数字, 多个用逗号隔开。例: 华为 2011, 中兴 3902, 迈普 5651, 神州数码 6339, TP-Link 11863', { ph: '例如 3902' }) +
    venField('sysDescr 关键字', 'keywords', (p.keywords || []).join(', '), '企业号认不出时, sysDescr 里包含这些字(不区分大小写)也算这个品牌', { ph: '例如 zxr10, zte' }) +
    '</div>';
  h += '<div class="set-grid">' +
    '<div class="set-item"><label class="t">CPU 使用率 (%)</label>' + venOIDRows('cpu', p.cpu, '按顺序尝试, 取表里 0~100 之间的最大值(多引擎/多板卡时就是最忙的那个)') + '</div>' +
    '<div class="set-item"><label class="t">内存使用率 (%)</label>' + venOIDRows('mem', p.mem, '直接给百分比的 OID') + '</div>' +
    '<div class="set-item"><label class="t">温度 (℃)</label>' + venOIDRows('temp', p.temp, '取不到时自动用 ENTITY-SENSOR-MIB 的摄氏度传感器') + '</div>' +
    '</div>';
  h += '<div class="set-grid">' +
    '<div class="set-item"><label class="t">温度预警阈值 (℃)</label><input type="number" min="1" max="119" step="1" value="' + venAttr(p.temp_warning_c || 60) + '" onchange="venSet(&quot;temp_warning_c&quot;,this.value)"><div class="h">达到此温度时产生风险告警</div></div>' +
    '<div class="set-item"><label class="t">温度严重阈值 (℃)</label><input type="number" min="2" max="120" step="1" value="' + venAttr(p.temp_critical_c || 70) + '" onchange="venSet(&quot;temp_critical_c&quot;,this.value)"><div class="h">达到此温度时产生严重告警，必须高于预警阈值</div></div>' +
    '<div class="set-item wide"><div class="h">每个品牌可单独设置；当前内置值均沿用旧阈值 60/70 ℃以保持升级前行为。请按设备型号手册或设备告警规范调整，不会自动猜测厂商阈值。保存后下一轮巡检生效。</div></div>' +
    '</div>';
  h += '<div class="set-item wide"><label class="t">内存换算 <span class="k">设备不直接给百分比时(比如 Cisco 只给已用/空闲字节数)</span></label><div class="ven-rows">' +
    (p.mem_pool || []).map(function (m, i) {
      return '<div class="ven-row">已用 <input class="oid" value="' + venAttr(m.used) + '" onchange="venSub(&quot;mem_pool&quot;,' + i + ',&quot;used&quot;,this.value)">' +
        '空闲 <input class="oid" value="' + venAttr(m.free) + '" onchange="venSub(&quot;mem_pool&quot;,' + i + ',&quot;free&quot;,this.value)">' +
        '或总量 <input class="oid" value="' + venAttr(m.total) + '" onchange="venSub(&quot;mem_pool&quot;,' + i + ',&quot;total&quot;,this.value)">' +
        '<button class="x" onclick="venArrDel(&quot;mem_pool&quot;,' + i + ')">✕</button></div>';
    }).join('') + '<button class="ven-addrow" onclick="venArrAdd(&quot;mem_pool&quot;,{used:&quot;&quot;,free:&quot;&quot;,total:&quot;&quot;})">＋ 添加</button></div></div>';
  h += '<div class="set-item wide"><label class="t">光功率 <span class="k">原始值 ÷ 倍数 = dBm; 表的索引是 ifIndex 或 entPhysicalIndex 都行</span></label><div class="ven-rows">' +
    (p.optical || []).map(function (o, i) {
      return '<div class="ven-row">收光 <input class="oid" value="' + venAttr(o.rx) + '" onchange="venSub(&quot;optical&quot;,' + i + ',&quot;rx&quot;,this.value)"><button class="try" onclick="venTry(' + venQ(o.rx) + ')">🔬</button>' +
        '发光 <input class="oid" value="' + venAttr(o.tx) + '" onchange="venSub(&quot;optical&quot;,' + i + ',&quot;tx&quot;,this.value)"><button class="try" onclick="venTry(' + venQ(o.tx) + ')">🔬</button>' +
        '÷ <input class="num" type="number" value="' + venAttr(o.scale) + '" onchange="venSub(&quot;optical&quot;,' + i + ',&quot;scale&quot;,parseFloat(this.value)||1)">' +
        '<button class="x" onclick="venArrDel(&quot;optical&quot;,' + i + ')">✕</button></div>';
    }).join('') + '<button class="ven-addrow" onclick="venArrAdd(&quot;optical&quot;,{rx:&quot;&quot;,tx:&quot;&quot;,scale:100})">＋ 添加</button>' +
    '<div class="h">取不到时自动用 ENTITY-SENSOR-MIB 里单位为 dBm 的传感器' + (p.id === 'ruijie' ? '; 锐捷老固件的"整数位+小数位+符号位"拼装是内置的' : '') + (p.id === 'h3c' ? '; H3C 老固件取不到实时发光时用模块规格下限顶替(会标注)是内置的' : '') + '</div></div></div>';
  h += '<div class="set-item wide"><label class="t">电口介质类型 OID <span class="k">用于识别没有光收发功率的铜缆模块</span></label>' +
    venOIDRows('optical_copper', p.optical_copper, '整数枚举型私有 MIB，表索引通常是 ifIndex；命中铜口枚举值后跳过该端口 RX/TX 光功率。H3C 默认包含当前及旧版 MIB OID，华为使用 hwEthernetPortMode。') +
    venField('私有 MIB 铜口枚举值', 'optical_copper_value', p.optical_copper_value || '', 'H3C copper=4；华为 hwEthernetPortMode copper=1。', { type: 'number', ph: '如 4' }) + '</div>';
  h += '<div class="set-item wide"><label class="t">标准 MAU-MIB ifMauType OID <span class="k">返回类型 OID，可识别铜缆 / 光纤介质</span></label>' +
    venOIDRows('optical_copper_mau', p.optical_copper_mau, 'OID 值为 dot3MauType 对象标识符；例如 1000BASE-T 的末段 29/30 表示铜缆。锐捷默认查询标准 ifMauType；设备若未实现该表会自动跳过。锐捷另读取 RuijieFiberMIB .165(收发器类型文本) / .166(连接器文本)，识别 1000BASE-GT-SFP、RJ45 等电口模块。') + '</div>';
  var kinds = [['fan', '风扇表'], ['power', '电源表'], ['entity', '通用实体表(按 ENTITY-MIB 分风扇/电源)']];
  h += '<div class="set-item wide"><label class="t">风扇 / 电源状态 <span class="k">值用逗号隔开; 既不是"正常"也不是"不在位"的值会告警</span></label><div class="ven-rows">' +
    (p.status || []).map(function (s, i) {
      return '<div class="ven-row"><input class="oid" value="' + venAttr(s.oid) + '" onchange="venSub(&quot;status&quot;,' + i + ',&quot;oid&quot;,this.value)"><button class="try" onclick="venTry(' + venQ(s.oid) + ')">🔬</button>' +
        '<select onchange="venSub(&quot;status&quot;,' + i + ',&quot;kind&quot;,this.value)">' + kinds.map(function (k) { return '<option value="' + k[0] + '"' + (k[0] === s.kind ? ' selected' : '') + '>' + k[1] + '</option>'; }).join('') + '</select>' +
        '正常 <input class="vals" value="' + venAttr((s.ok || []).join(',')) + '" onchange="venSub(&quot;status&quot;,' + i + ',&quot;ok&quot;,venNums(this.value))">' +
        '不在位 <input class="vals" value="' + venAttr((s.absent || []).join(',')) + '" onchange="venSub(&quot;status&quot;,' + i + ',&quot;absent&quot;,venNums(this.value))">' +
        '<button class="x" onclick="venArrDel(&quot;status&quot;,' + i + ')">✕</button></div>';
    }).join('') + '<button class="ven-addrow" onclick="venArrAdd(&quot;status&quot;,{oid:&quot;&quot;,kind:&quot;fan&quot;,ok:[1],absent:[]})">＋ 添加</button></div></div>';
  h += '<div class="set-grid">' +
    (p.builtin ? '' : venField('配置备份命令', 'backup_cmd', p.backup_cmd || '', '每行一条, 最后一条的输出保存为配置; 留空按 Cisco 风格(terminal length 0 / show running-config)', { area: 1, wide: 1 })) +
    venField('备注', 'note', p.note || '', '', { wide: 1 }) + '</div>';
  h += '<div class="set-row" style="gap:10px;">' +
    (p.builtin ? '<button class="filter-btn" onclick="venReset()"' + (p.modified || venDiffers(p) ? '' : ' disabled') + '>↺ 恢复内置默认</button>' : '<button class="filter-btn" style="color:var(--bad);" onclick="venDel()">🗑 删除这个品牌</button>') +
    '<span class="h">改完点下面的「保存设置」</span></div>';
  return h;
}
function venDiffers(p) { var d = VEN.defs[p.id]; if (!d) { return false; } var a = JSON.parse(JSON.stringify(p)), b = JSON.parse(JSON.stringify(d)); a.modified = b.modified = false; a.builtin = b.builtin = true; return JSON.stringify(a) !== JSON.stringify(b); }
function venTestHTML() {
  var t = VEN.test;
  var devs = VEN.devices.map(function (d) { return '<option value="' + esc(d.id) + '"' + (d.id === t.dev ? ' selected' : '') + '>' + esc(d.ip + (d.note ? ' · ' + d.note : '') + (d.brand && d.brand !== 'auto' ? ' (' + d.brand + ')' : '')) + '</option>'; }).join('');
  var res = '';
  if (t.busy) { res = '<div class="ven-msg">查询中...</div>'; }
  else if (t.res) {
    res = '<div class="ven-msg" style="color:' + (t.res.ok ? 'var(--good)' : 'var(--bad)') + ';">' + esc(t.res.message || '') + '</div>';
    if (t.res.rows && t.res.rows.length) { res += '<table><tbody>' + t.res.rows.map(function (r) { return '<tr><td>' + esc(r.index) + '</td><td>' + esc(r.value) + '</td></tr>'; }).join('') + '</tbody></table>' + (t.res.total > t.res.rows.length ? '<div class="h">只显示前 ' + t.res.rows.length + ' 行</div>' : ''); }
  }
  return '<div class="ven-test"><label class="t" style="font-weight:600;font-size:13.5px;">🔬 在设备上试一下 <span class="k" style="font-weight:400;">对一个 OID 做 walk, 马上看到设备返回什么(点上面任意 OID 旁的 🔬 会自动填进来)</span></label>' +
    '<div class="ven-row" style="margin-top:6px;">' + (devs ? '<select id="venTestDev" onchange="VEN.test.dev=this.value">' + devs + '</select>' : '<span class="h">设备管理里还没有设备</span>') +
    '<input class="oid" id="venTestOid" value="' + venAttr(t.oid) + '" placeholder=".1.3.6.1.4.1.xxx" onchange="VEN.test.oid=this.value">' +
    '<button class="filter-btn" onclick="venTry()"' + (devs ? '' : ' disabled') + '>查询</button></div>' + res + '</div>';
}
function venCur() { return VEN.list[VEN.sel]; }
function venNums(s) { return String(s).split(/[,，\s]+/).filter(Boolean).map(function (x) { return parseInt(x, 10); }).filter(function (x) { return !isNaN(x); }); }
function venSet(k, v) {
  var p = venCur();
  if (k === 'enterprises' || k === 'keywords') { p[k] = String(v).split(/[,，]+/).map(function (x) { return x.trim(); }).filter(Boolean); }
  else if (k === 'optical_copper_value') { p[k] = parseInt(v, 10); if (isNaN(p[k])) { p[k] = 0; } }
  else if (k === 'temp_warning_c' || k === 'temp_critical_c') { p[k] = parseInt(v, 10); if (isNaN(p[k])) { p[k] = 0; } }
  else { p[k] = v; }
  if (k === 'name' || k === 'id') { renderSettings(); }
}
function venArr(k, i, v) { venCur()[k][i] = String(v).trim(); }
function venArrDel(k, i) { venCur()[k].splice(i, 1); renderSettings(); }
function venArrAdd(k, v) { var p = venCur(); p[k] = p[k] || []; p[k].push(v); renderSettings(); }
function venSub(k, i, f, v) { venCur()[k][i][f] = typeof v === 'string' ? v.trim() : v; }
function venAdd() {
  var n = 1; while (VEN.list.some(function (v) { return v.id === 'brand' + n; })) { n++; }
  VEN.list.push({ id: 'brand' + n, name: '新品牌 ' + n, enterprises: [], keywords: [], cpu: [], mem: [], mem_pool: [], temp: [], temp_warning_c: 60, temp_critical_c: 70, optical: [], optical_copper: [], status: [], backup_cmd: '', note: '', builtin: false, modified: false });
  VEN.sel = VEN.list.length - 1;
  renderSettings();
}
function venDel() {
  var p = venCur();
  if (!confirm('删除品牌「' + (p.name || p.id) + '」? 已经指定成这个品牌的设备会按通用规则采集(保存后生效)。')) { return; }
  VEN.list.splice(VEN.sel, 1); VEN.sel = 0; renderSettings();
}
function venReset() {
  var p = venCur(), d = VEN.defs[p.id];
  if (!d) { return; }
  VEN.list[VEN.sel] = JSON.parse(JSON.stringify(d));
  renderSettings();
  byId('setMsg').style.color = 'var(--fg-dim)'; byId('setMsg').innerText = '已恢复「' + d.name + '」的内置 OID, 点「保存设置」生效';
}
function venTry(oid) {
  var t = VEN.test;
  if (oid !== undefined) { t.oid = oid; }
  var el = byId('venTestOid'); if (el && oid === undefined) { t.oid = el.value; }
  var sel = byId('venTestDev'); if (sel) { t.dev = sel.value; }
  if (!t.oid || !t.dev) { t.res = { ok: false, message: '先选设备、填 OID' }; renderSettings(); return; }
  t.busy = true; t.res = null; renderSettings();
  authedApi('/api/vendors/test', { device_id: t.dev, oid: t.oid }).then(function (d) { t.res = d; }).catch(function (e) { t.res = { ok: false, message: String(e) }; })
    .then(function () { t.busy = false; if (SET_SCHEMA[SET.sec].custom === 'vendors') { renderSettings(); } });
}
function venSave() {
  return authedApi('/api/vendors', { vendors: VEN.list }).then(function (d) {
    VEN.list = d.vendors || VEN.list;
    VEN.orig = JSON.stringify(VEN.list);
    if (VEN.sel >= VEN.list.length) { VEN.sel = 0; }
  });
}
