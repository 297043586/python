/*
 * 3D 数据中心机房(three.js)。入口: 顶栏「🗄️ 机柜」→「🏢 3D 机房」。后端见 datacenter.go。
 *
 *   - 机房: 防静电地板(机柜前面是通风地板)、四面墙(相机在外面时自动"透明", 像娃娃屋)、
 *     机柜上方的走线槽; 可以建多个机房。尺寸按米填(换算成 0.6m 的地板块), 层高可改,
 *     可以按内容自动收缩、整体平移。
 *   - 场景物件: 机房门 / 柱子 / 运维操作台 / 墙挂监控大屏 / 灭火器箱 / 气体灭火钢瓶 / 摄像头,
 *     模型和落地设备(空调 / UPS / 配电柜 / 电池柜)、柜内设备的模型在 dc3d-models.js。
 *   - 机柜: 600×1200mm, 42U(可改), 钢框 + 侧板 + 玻璃前门, 顶部状态灯/灯条(绿正常、
 *     橙离线、红告警), 内部导轨标 U 号。还可以放精密空调 / UPS 柜这类落地设备。
 *   - 设备: 被监控的交换机按真实形态画(几台堆叠 / 框式多高, 前面板按端口状态亮灯);
 *     非监控设备(服务器/防火墙/路由器/存储/配线架/PDU/UPS...)手工添加, 指定 U 位和高度。
 *   - 机柜是双开网孔前门: 选中机柜(或柜里的设备)时门自动打开, 选中的交换机会从柜里"拉出来"
 *     一点, 旁边弹出信息卡(状态 / 前面板 / 端口), 不用跳到设备详情页; 工具栏可以一键全部开门。
 *   - 冷通道: 两排机柜正面相对, 中间一块封闭区域(通风地板 + 玻璃顶 + 两端推拉门 + 冷风粒子)。
 *   - 交互: 拖动旋转、右键平移、滚轮缩放; 悬停看详情, 单击选中(右侧面板), 双击飞到机柜前;
 *     搜索设备/机柜, 只看告警。
 *   - 编辑(需要登录): 添加/拖动/旋转/删除机柜, 往机柜里加设备, 调整被监控交换机的机柜和
 *     U 位, 手工指定设备形态。机房布局点「保存」才写入; 交换机位置和形态是立即保存的
 *     (跟 2D 机柜视图共用 data/racks.json)。
 */
/* ================= 3D 机房 ================= */
var DC_TILE = 60, DC_U = 4.445, DC_PLINTH = 10, DC_TOP = 12, DC_W = 60, DC_D = 120;
var DC_KIND = { server: ['服务器', 2, 70], firewall: ['防火墙', 1, 45], router: ['路由器', 1, 45], storage: ['存储', 2, 70], ups: ['UPS', 2, 60],
  patch: ['配线架', 1, 12], pdu: ['PDU', 1, 20], switch: ['交换机(未监控)', 1, 40], ac: ['行级空调', 4, 90], other: ['其他', 1, 50] };
var DC_STATUS = { ok: [0x22c55e, '正常'], warn: [0xf59e0b, '警告'], bad: [0xef4444, '故障'], off: [0x64748b, '离线'], '': [0x38bdf8, '未标记'] };
var DC = { ctx: null, layout: null, saved: null, devices: [], room: '', edit: false, dirty: false, sel: null, mode: null, timer: null, renames: {},
  search: '', alarmOnly: false, loaded: false, busy: false, doorsAll: false, openRack: null, card: null, rearAll: false, openRear: null };

function dcOpen() {
  var pane = byId('dcPane');
  if (!pane.dataset.built) {
    pane.dataset.built = '1';
    pane.innerHTML = '<div class="table-toolbar" id="dcToolbar"><div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;">' +
      '<select id="dcRoomSel" onchange="dcSwitchRoom(this.value)" style="padding:7px 10px;border-radius:8px;border:1px solid var(--card-border);background:var(--card-bg);color:var(--fg);"></select>' +
      '<input type="text" class="search-box" id="dcSearch" placeholder="搜索机柜 / 设备名 / IP..." oninput="dcDoSearch()" style="width:220px;">' +
      '<label style="font-size:13px;color:var(--fg-dim);"><input type="checkbox" id="dcAlarm" onchange="DC.alarmOnly=this.checked;dcApplyFilter()"> 只突出告警/离线</label>' +
      '</div><div style="display:flex;gap:6px;align-items:center;flex-wrap:wrap;">' +
      '<button class="filter-btn" id="dcDoorBtn" onclick="dcToggleDoors()" title="打开 / 关上所有机柜的前门">🚪 全部开门</button>' +
      '<button class="filter-btn" id="dcRearBtn" onclick="dcToggleRear()" title="打开 / 关上所有机柜的后门(转到机柜背面看交换机的电源 / 风扇)">🚪 全部开后门</button>' +
      '<button class="filter-btn" onclick="dcView(&quot;iso&quot;)">🧭 斜视</button><button class="filter-btn" onclick="dcView(&quot;top&quot;)">⬇ 俯视</button><button class="filter-btn" onclick="dcView(&quot;front&quot;)">👁 正面</button>' +
      '<span id="dcEditBtns" style="display:none;gap:6px;">' +
      '<button class="filter-btn" onclick="dcStartPlace(&quot;rack&quot;)">➕ 机柜</button>' + dcAddSelect() + '<button class="filter-btn" onclick="dcStartPlace(&quot;aisle&quot;)">🧊 冷通道</button>' +
      '<button class="filter-btn" onclick="dcRoomAdd()">🏢 新建机房</button>' +
      '<button class="filter-btn" onclick="dcRevert()">↩ 放弃修改</button><button class="hbtn" onclick="dcSave()">💾 保存机房</button><span class="dc-dirty" id="dcDirty"></span></span>' +
      '<button class="filter-btn" id="dcEditToggle" onclick="dcToggleEdit()">✏️ 编辑机房</button></div></div>' +
      '<div class="dc-wrap" id="dcWrap"><div class="dc-stage" id="dcStage"></div><div class="dc-hud" id="dcHud"></div><div class="dc-mode" id="dcMode"></div>' +
      '<div class="dc-legend"><i style="background:#22c55e;"></i>正常<i style="background:#f59e0b;"></i>离线/警告<i style="background:#ef4444;"></i>告警<i style="background:#38bdf8;"></i>未监控设备' +
      '<br>机柜顶灯 = 柜内最严重的状态 · 交换机面板灯 = 端口状态 · 蓝色玻璃顶 + 通风地板 = 冷通道 · 单击机柜开门</div>' +
      '<div class="dc-side" id="dcSide"></div><div class="dc-tip" id="dcTip"></div>' +
      '<div id="dcLoading" style="position:absolute;inset:0;display:grid;place-items:center;color:#94a3b8;font-size:14px;">正在加载 3D 引擎...</div></div>';
  }
  byId('dcEditToggle').style.display = '';
  if (!glSupported()) { byId('dcLoading').innerText = '这个浏览器不支持 WebGL, 请用 2D 机柜面板'; return; }
  glLoad().then(function () {
    if (!DC.ctx) {
      DC.ctx = dcNewScene(byId('dcStage'), { interactive: true, key: 'dc:' });
      dcBindEditor(DC.ctx);
      dcMountBar();
    }
    var ld = byId('dcLoading'); if (ld) { ld.remove(); }
    return dcLoad(true);
  }).catch(function (e) { var ld = byId('dcLoading'); if (ld) { ld.innerText = '3D 引擎加载失败: ' + e; } });
  if (!DC.timer) {
    DC.timer = setInterval(function () {
      if (byId('rackModal').style.display === 'none' || PNL.rack.view !== '3d' || DC.busy) { return; }
      dcLoad(false);
    }, uiPref('rack_refresh_seconds', 30) * 1000);
  }
}
function dcClose() {
  dcTourStop();
  DC.introDone = false;
  if (DC.timer) { clearInterval(DC.timer); DC.timer = null; }
  DC.card = null;
  if (DC.ctx) { dcDisposeScene(DC.ctx); DC.ctx = null; }
  DC.edit = false; DC.mode = null; DC.sel = null; DC.rearAll = false; DC.openRear = null;
  var pane = byId('dcPane');
  if (pane) { pane.innerHTML = ''; delete pane.dataset.built; }
}
// dcNormLayout 兜底: 任何一个数组是 null(老版本数据 / 新装空机房)都补成 [], 避免 forEach 报错。
function dcNormLayout(l) {
  l = l || {};
  ['rooms', 'racks', 'items', 'aisles', 'objects'].forEach(function (k) { if (!Array.isArray(l[k])) { l[k] = []; } });
  if (!l.rooms.length) { l.rooms.push({ id: 'room1', name: '主机房', w: 20, d: 14 }); }
  if (!l.forms || typeof l.forms !== 'object') { l.forms = {}; }
  return l;
}
function dcLoad(first) {
  return pub('/api/datacenter').then(function (d) {
    DC.devices = (d.devices || []).map(function (x) { x.ports = x.ports || []; return x; });
    d.layout = dcNormLayout(d.layout);
    DC.saved = JSON.parse(JSON.stringify(d.layout));
    if (!DC.dirty || !DC.layout) { DC.layout = JSON.parse(JSON.stringify(d.layout)); DC.renames = {}; }
    if (!DC.room || !DC.layout.rooms.some(function (r) { return r.id === DC.room; })) { DC.room = DC.layout.rooms[0].id; }
    DC.loaded = true;
    dcRefresh(first);
  }).catch(function (e) { byId('dcHud').innerText = '加载失败: ' + e; });
}
function dcRefresh(resetCam) {
  if (!DC.ctx) { return; }
  dcEnsureAutoRacks(DC.layout, DC.devices);
  var sel = byId('dcRoomSel');
  sel.innerHTML = DC.layout.rooms.map(function (r) {
    var n = DC.layout.racks.filter(function (k) { return k.room === r.id; }).length;
    return '<option value="' + esc(r.id) + '"' + (r.id === DC.room ? ' selected' : '') + '>🏢 ' + esc(r.name) + ' (' + n + ' 柜)</option>';
  }).join('');
  dcBuild(DC.ctx, DC.layout, DC.devices, DC.room);
  if (resetCam) {
    // 第一次打开: 机柜从地板升起 + 镜头从高空飞进来; 切换机房时也升一次
    dcRiseStart(DC.ctx);
    dcView(DC.introDone ? 'iso' : 'intro');
    DC.introDone = true;
  }
  dcApplyFilter();
  dcHud();
  dcRenderSide();
  byId('dcDirty').innerText = DC.dirty ? '● 有未保存的修改' : '';
  if (DC.card && byId('dcCard')) { dcFillCard(DC.card.ip); }
}
function dcSwitchRoom(id) { DC.room = id; DC.sel = null; dcRefresh(true); }

/* ---------- 布局计算 ---------- */
function dcRoomOf(layout, id) { return layout.rooms.filter(function (r) { return r.id === id; })[0] || layout.rooms[0]; }
function dcCells(k) { return (k.rot === 90 || k.rot === 270) ? [[k.x, k.z], [k.x + 1, k.z]] : [[k.x, k.z], [k.x, k.z + 1]]; }
function dcAisleCells(a) { var out = []; for (var x = a.x; x < a.x + a.w; x++) { for (var z = a.z; z < a.z + a.d; z++) { out.push([x, z]); } } return out; }
// 机柜和冷通道都占地板; ignoreId 是正在挪动的那个自己
function dcUsedCells(layout, room, ignoreId) {
  var used = {};
  layout.racks.forEach(function (o) { if (o.room === room.id && o.id !== ignoreId) { dcCells(o).forEach(function (c) { used[c[0] + ',' + c[1]] = o.name; }); } });
  (layout.aisles || []).forEach(function (a) { if (a.room === room.id && a.id !== ignoreId) { dcAisleCells(a).forEach(function (c) { used[c[0] + ',' + c[1]] = a.name; }); } });
  (layout.objects || []).forEach(function (o) { if (o.room === room.id && o.id !== ignoreId && (DC_OBJ[o.kind] || {}).solid) { dcObjCells(o).forEach(function (c) { used[c[0] + ',' + c[1]] = dcObjLabel(o); }); } });
  return used;
}
function dcFree(layout, room, k, ignoreId) {
  var used = dcUsedCells(layout, room, ignoreId);
  return dcCells(k).every(function (c) { return dcIn(room, c[0], c[1]) && !used[c[0] + ',' + c[1]]; });
}
function dcAisleFree(layout, room, a, ignoreId) {
  var used = dcUsedCells(layout, room, ignoreId);
  return a.w >= 1 && a.d >= 1 && dcAisleCells(a).every(function (c) { return dcIn(room, c[0], c[1]) && !used[c[0] + ',' + c[1]]; });
}
// 机柜正面前面那块地板(判断机柜是不是朝着冷通道)
function dcFrontCell(k) {
  var r = k.rot || 0;
  return r === 90 ? [k.x + 2, k.z] : r === 180 ? [k.x, k.z - 1] : r === 270 ? [k.x - 1, k.z] : [k.x, k.z + 2];
}
function dcAisleRacks(a) {
  return DC.layout.racks.filter(function (k) {
    if (k.room !== a.room || (k.type && k.type !== 'rack')) { return false; }
    var f = dcFrontCell(k);
    return f[0] >= a.x && f[0] < a.x + a.w && f[1] >= a.z && f[1] < a.z + a.d;
  });
}
// 设备引用了机房里还没有的机柜名(sysLocation 或 2D 视图里分配的) → 自动补一个, 排在机房后排
function dcEnsureAutoRacks(layout, devices) {
  var names = {};
  layout.racks.forEach(function (k) { names[k.name] = true; });
  var need = [];
  devices.forEach(function (d) { if (d.rack && !names[d.rack]) { names[d.rack] = true; need.push(d.rack); } });
  need.sort(naturalCmp);
  var room = layout.rooms[0];
  need.forEach(function (nm) {
    var k = { id: 'auto:' + nm, name: nm, room: room.id, x: 0, z: 0, rot: 0, height: 42, type: 'rack', auto: true };
    var placed = false;
    for (var z = 1; z + 1 < room.d && !placed; z += 4) {
      for (var x = 1; x < room.w - 1 && !placed; x++) {
        k.x = x; k.z = z;
        if (dcFree(layout, room, k)) { placed = true; }
      }
    }
    if (!placed) { room.w += 2; k.x = room.w - 2; k.z = 1; }
    layout.racks.push(k);
  });
}
// 被监控交换机在机柜里的位置: 手工指定 U 号的先占位, 其余从上往下排, 设备之间空 1U(跟 2D 视图一致)
function dcPlace(rack, devs, items) {
  var occ = {}, out = [], over = [];
  items.forEach(function (it) { for (var u = it.u; u < it.u + it.h; u++) { occ[u] = true; } });
  var ok = function (top, h) { for (var u = top; u > top - h; u--) { if (u < 1 || u > rack.height || occ[u]) { return false; } } return true; };
  devs.filter(function (d) { return d.u > 0; }).forEach(function (d) {
    var h = pnlUnits(d);
    out.push({ d: d, top: Math.min(d.u, rack.height), h: h, conflict: !ok(d.u, h) });
    for (var u = d.u; u > d.u - h; u--) { occ[u] = true; }
  });
  var cursor = rack.height - 2;
  devs.filter(function (d) { return !(d.u > 0); }).sort(function (a, b) { return naturalCmp(a.name, b.name); }).forEach(function (d) {
    var h = pnlUnits(d);
    while (cursor >= h && !ok(cursor, h)) { cursor--; }
    if (cursor < h) {
      // 从上面放不下了, 再从最上面找一遍空位
      var t = rack.height;
      while (t >= h && !ok(t, h)) { t--; }
      if (t < h) { over.push(d); return; }
      cursor = t;
    }
    out.push({ d: d, top: cursor, h: h });
    for (var u = cursor; u > cursor - h; u--) { occ[u] = true; }
    cursor -= h + 1;
  });
  return { list: out, overflow: over };
}
function dcDevStatus(d) { return !d.alive ? 'off' : (d.alerts > 0 ? 'bad' : 'ok'); }
function dcRackStatus(rack, layout, devices) {
  layout = layout || DC.layout; devices = devices || DC.devices;
  var rank = { bad: 3, off: 2, warn: 2, ok: 1, '': 0 }, worst = '';
  (devices || []).forEach(function (d) { if (d.rack !== rack.name) { return; } var s = dcDevStatus(d); if (rank[s] > rank[worst]) { worst = s; } });
  (layout.items || []).forEach(function (it) { if (it.rack === rack.id && it.status && rank[it.status] > rank[worst]) { worst = it.status; } });
  return worst;
}
function DC_devsIn(rack) { return (DC.devices || []).filter(function (d) { return d.rack === rack.name; }); }

/* ---------- 场景 ---------- */
function dcNewScene(box, opts) {
  var st = glStage(box, { fov: 40, near: 5, far: 40000, shadows: !opts.noShadow, exposure: 1.12, bloom: { strength: 0.55, threshold: 0.92 } });
  var THREE = st.THREE;
  var c = { st: st, opts: opts, key: opts.key || 'dc:', world: new THREE.Group(), rackObjs: {}, pickables: [], beacons: [], hover: null, doorRacks: [], aisleFx: [], aisleObjs: {}, halos: [], rise: null };
  st.scene.add(c.world);
  // 阴影只在场景里有东西动(开门 / 拉出交换机 / 编辑拖动 / 重建)时重算, 光转镜头不重算:
  // 机柜多的机房每帧重画一遍阴影贴图是卡顿的主要来源
  st.renderer.shadowMap.autoUpdate = false; st.renderer.shadowMap.needsUpdate = true;
  var poke0 = st.poke; st.poke = function () { st.renderer.shadowMap.needsUpdate = true; poke0(); };
  var tmpV = new THREE.Vector3();
  st.scene.background = new THREE.Color(0x0a1120);
  st.scene.fog = new THREE.Fog(0x0a1120, 4500, 15000);
  st.scene.add(new THREE.HemisphereLight(0xe0ecff, 0x273248, 0.95));
  var sun = new THREE.DirectionalLight(0xffffff, 1.1);
  sun.position.set(800, 1600, 1200);
  if (!opts.noShadow) {
    sun.castShadow = true; sun.shadow.mapSize.set(2048, 2048);
    sun.shadow.camera.near = 100; sun.shadow.camera.far = 6000; sun.shadow.bias = -0.0005;
  }
  c.sun = sun;
  st.scene.add(sun); st.scene.add(sun.target);
  var fill = new THREE.DirectionalLight(0x93c5fd, 0.35); fill.position.set(-900, 700, -700); st.scene.add(fill);
  st.controls.maxPolarAngle = Math.PI * 0.495;
  st.controls.minDistance = 60; st.controls.maxDistance = 12000;
  // v13: 悬停 / 选中用会呼吸的发光框(泛光下是一圈光晕), 替换原来的细线框
  c.hoverBox = glPulseBox(0x38bdf8, { fill: 0.04, speed: 6 }); st.scene.add(c.hoverBox);
  c.selBox = glPulseBox(0xfbbf24, { fill: 0.08 }); st.scene.add(c.selBox);
  st.frames.push(function (dt, t) {
    var any = false, moved = false;
    // 机柜门开关 + 选中交换机拉出(缓动)
    var step = function (o, key, curKey, speed) {
      var tg = o.userData[key] || 0, cur = o.userData[curKey] || 0;
      if (cur === tg) { return false; }
      cur = tg > cur ? Math.min(tg, cur + dt * speed) : Math.max(tg, cur - dt * speed);
      o.userData[curKey] = cur;
      return true;
    };
    // 选中的机柜: 镜头转到机柜背面时自动开后门(不用再去点「开后门」), 转回正面就关上
    var og = DC.openRack && c === DC.ctx && c.rackObjs[DC.openRack];
    if (og && og.userData.rears) {
      var want = DC.rearAll || DC.openRear === DC.openRack || dcCamBehind(c, og) ? 1 : 0;
      if (og.userData.rearT !== want) { og.userData.rearT = want; }
    }
    (c.doorRacks || []).forEach(function (g) { var a = step(g, 'doorT', 'doorCur', 1.7), b = step(g, 'rearT', 'rearCur', 1.7); if (a || b) { dcApplyDoor(g); any = true; moved = true; } });
    // 机房门: 点一下开 / 关; 镜头走到门附近(3 米内)自动开
    (c.roomDoors || []).forEach(function (g) {
      g.getWorldPosition(tmpV);
      var cam = st.camera.position, near = Math.hypot(cam.x - tmpV.x, cam.z - tmpV.z) < 300 && cam.y < (c.wallH || 300) + 60;
      g.userData.openT = g.userData.manual || near ? 1 : 0;
      if (step(g, 'openT', 'openCur', 1.2)) { dcApplyRoomDoor(g); any = true; moved = true; }
    });
    Object.keys(c.devObjs || {}).forEach(function (ip) {
      var sw = c.devObjs[ip];
      if (step(sw, 'pullT', 'pullCur', 2.4)) { var e = sw.userData.pullCur; sw.position.z = sw.userData.baseZ + 26 * e * (2 - e); any = true; moved = true; }
    });
    (c.aisleFx || []).forEach(function (fx) {
      var p = fx.pts.geometry.attributes.position, arr = p.array;
      for (var i = 1; i < arr.length; i += 3) { arr[i] += dt * fx.v[(i - 1) / 3]; if (arr[i] > fx.h) { arr[i] = 2; } }
      p.needsUpdate = true; any = true;
    });
    c.beacons.forEach(function (b) {
      if (b.userData.blink) { var k = 0.5 + 0.5 * Math.sin(t * 5 + b.userData.phase); b.material.emissiveIntensity = 0.6 + k * 3; any = true; }
    });
    if (c.opts.spin) { c.world.rotation.y += dt * 0.05; any = true; moved = true; }
    c.halos.forEach(function (h) { var k = (t * 0.7 + h.userData.phase) % 1; h.scale.setScalar(0.8 + k * 0.9); h.material.opacity = 0.55 * (1 - k); });
    if (c.halos.length) { any = true; }
    if (dcRiseStep(c)) { any = true; moved = true; }
    if (moved) { st.renderer.shadowMap.needsUpdate = true; }
    if (c.lights) { var lv = st.camera.position.y < (c.wallH || 300) + 40; if (c.lights.visible !== lv) { c.lights.visible = lv; any = true; } }
    if (c.hoverBox.tick(t) | c.selBox.tick(t)) { any = true; }
    return any;
  });
  st.start();
  return c;
}
function dcDisposeScene(c) {
  if (c.st) { c.st.dispose(); c.st = null; }
  Object.keys(GL.panels || {}).forEach(function (k) { if (k.indexOf(c.key) === 0) { GL.panels[k].tex.dispose(); delete GL.panels[k]; } });
}
function dcTex(name, draw, w, h, rep) {
  GL.dcTex = GL.dcTex || {};
  if (GL.dcTex[name]) { return GL.dcTex[name]; }
  var THREE = GL.THREE, cv = document.createElement('canvas');
  cv.width = w; cv.height = h;
  draw(cv.getContext('2d'), w, h);
  var t = new THREE.CanvasTexture(cv);
  t.colorSpace = THREE.SRGBColorSpace; t.anisotropy = 8;
  if (rep) { t.wrapS = t.wrapT = THREE.RepeatWrapping; }
  t.userData.shared = true;
  GL.dcTex[name] = t;
  return t;
}
function dcFloorTex(perf) {
  return dcTex(perf ? 'floorPerf' : 'floor', function (ctx, w, h) {
    ctx.fillStyle = perf ? '#8fa6bd' : '#a3acb9'; ctx.fillRect(0, 0, w, h);
    var g = ctx.createLinearGradient(0, 0, w, h); g.addColorStop(0, 'rgba(255,255,255,.18)'); g.addColorStop(1, 'rgba(0,0,0,.08)');
    ctx.fillStyle = g; ctx.fillRect(0, 0, w, h);
    if (perf) {
      ctx.fillStyle = '#51657c';
      for (var y = 14; y < h - 10; y += 11) { for (var x = 14; x < w - 10; x += 11) { ctx.beginPath(); ctx.arc(x, y, 3, 0, 7); ctx.fill(); } }
    } else {
      ctx.fillStyle = 'rgba(0,0,0,.05)';
      for (var i = 0; i < 90; i++) { ctx.fillRect(Math.random() * w, Math.random() * h, 2, 2); }
    }
    ctx.strokeStyle = '#6b7684'; ctx.lineWidth = 3; ctx.strokeRect(1.5, 1.5, w - 3, h - 3);
    ctx.fillStyle = '#8a95a3'; [[8, 8], [w - 8, 8], [8, h - 8], [w - 8, h - 8]].forEach(function (p) { ctx.beginPath(); ctx.arc(p[0], p[1], 3, 0, 7); ctx.fill(); });
  }, 128, 128, true);
}
function dcRailTex(height) {
  return dcTex('rail' + height, function (ctx, w, h) {
    ctx.clearRect(0, 0, w, h);
    var uh = h / height;
    ctx.font = Math.max(9, Math.min(15, uh * 0.62)) + 'px Consolas, monospace'; ctx.textBaseline = 'middle';
    for (var u = 1; u <= height; u++) {
      var y = h - u * uh;
      ctx.fillStyle = 'rgba(148,163,184,.10)'; ctx.fillRect(34, y + uh - 1, w - 68, 1);
      ctx.fillStyle = '#9ca3af'; ctx.textAlign = 'left'; ctx.fillText(u, 3, y + uh / 2);
      ctx.textAlign = 'right'; ctx.fillText(u, w - 3, y + uh / 2);
      ctx.fillStyle = '#6b7280';
      [22, w - 26].forEach(function (x) { ctx.fillRect(x, y + uh * 0.2, 4, 4); ctx.fillRect(x, y + uh * 0.66, 4, 4); });
    }
  }, 360, Math.min(4096, height * 28), false);
}
function dcPerfTex() {
  return dcTex('perfDoor', function (ctx, w, h) {
    ctx.fillStyle = '#1b2230'; ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = '#0c1018';
    for (var y = 6; y < h; y += 9) { for (var x = (y / 9) % 2 ? 6 : 10; x < w; x += 9) { ctx.fillRect(x, y, 4, 4); } }
  }, 128, 128, true);
}
function dcDoorTex() {
  return dcTex('doorMesh', function (ctx, w, h) {
    ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = '#000';
    for (var y = 0; y < 4; y++) { for (var x = 0; x < 4; x++) { ctx.beginPath(); ctx.arc(x * 16 + (y % 2 ? 12 : 4) , y * 16 + 8, 5.6, 0, 7); ctx.fill(); } }
  }, 64, 64, true);
}
// 镜头在机柜背面(机柜正面朝 +z, 跟着机柜旋转)
function dcCamBehind(c, g) {
  var cam = c.st.camera.position, p = g.position, r = g.rotation.y + (c.world.rotation.y || 0);
  var fx = Math.sin(r), fz = Math.cos(r);
  var wp = g.getWorldPosition ? g.getWorldPosition(new c.st.THREE.Vector3()) : p;
  return (cam.x - wp.x) * fx + (cam.z - wp.z) * fz < -20;
}
// 机房门: openCur 0 = 关, 1 = 开(往里开约 95°)
function dcApplyRoomDoor(g) {
  var e = g.userData.openCur || 0, a = (e < .5 ? 2 * e * e : 1 - Math.pow(-2 * e + 2, 2) / 2) * 1.65;
  (g.userData.doorLeaves || []).forEach(function (d) { d.p.rotation.y = d.s * a; });
}
// 门: doorCur 0 = 关, 1 = 开(约 105°)
function dcApplyDoor(g) {
  var ease = function (e) { return (e < .5 ? 2 * e * e : 1 - Math.pow(-2 * e + 2, 2) / 2) * 1.83; };
  var a = ease(g.userData.doorCur || 0), b = ease(g.userData.rearCur || 0);
  (g.userData.doors || []).forEach(function (d) { d.p.rotation.y = d.s * a; });
  // 后门往后(-z)开, 转的方向跟前门相反
  (g.userData.rears || []).forEach(function (d) { d.p.rotation.y = -d.s * b; });
}
function dcNoRay(o) { o.raycast = function () {}; return o; }
// 非监控设备前面板
function dcItemTex(it) {
  var key = 'item:' + it.kind + ':' + it.h + ':' + (it.status || '') + ':' + it.name;
  return dcTex(key, function (ctx, W, H) {
    var base = { server: '#2b2f36', storage: '#23272e', firewall: '#7f1d1d', router: '#1e3a8a', ups: '#111827', patch: '#1f2937', pdu: '#141414', switch: '#374151', ac: '#d1d5db', other: '#334155' }[it.kind] || '#334155';
    var g = ctx.createLinearGradient(0, 0, 0, H); g.addColorStop(0, base); g.addColorStop(1, '#0b0f14');
    ctx.fillStyle = g; ctx.fillRect(0, 0, W, H);
    ctx.fillStyle = 'rgba(255,255,255,.1)'; ctx.fillRect(0, 0, W, 2);
    ctx.fillStyle = '#4b5563'; ctx.fillRect(0, 0, 14, H); ctx.fillRect(W - 14, 0, 14, H);
    var uh = H / it.h;
    var st = DC_STATUS[it.status || ''] || DC_STATUS[''];
    var led = '#' + ('000000' + st[0].toString(16)).slice(-6);
    ctx.fillStyle = it.kind === 'ac' ? '#111827' : '#e5e7eb'; ctx.font = '600 ' + Math.min(22, uh * 0.34) + 'px "Microsoft YaHei", sans-serif'; ctx.textBaseline = 'middle';
    ctx.fillText(truncateLabel(it.name || DC_KIND[it.kind][0], 14), 24, Math.min(uh * 0.3, 20));
    if (it.model) { ctx.fillStyle = '#9ca3af'; ctx.font = Math.min(15, uh * 0.24) + 'px Arial'; ctx.fillText(truncateLabel(it.model, 20), 24, Math.min(uh * 0.3, 20) + Math.min(18, uh * 0.3)); }
    ctx.fillStyle = led; ctx.shadowColor = led; ctx.shadowBlur = 10;
    ctx.beginPath(); ctx.arc(W - 34, Math.min(uh * 0.3, 20), Math.min(6, uh * 0.1), 0, 7); ctx.fill(); ctx.shadowBlur = 0;
    var x0 = 230, x1 = W - 60;
    var i, j;
    if (it.kind === 'server' || it.kind === 'storage') {
      var per = it.kind === 'storage' ? 12 : 8, rows = it.kind === 'storage' ? Math.max(1, it.h * 1) : Math.max(1, Math.round(it.h));
      var bw = (x1 - x0) / per, bh = (H - 12) / rows;
      for (j = 0; j < rows; j++) {
        for (i = 0; i < per; i++) {
          var bx = x0 + i * bw + 2, by = 6 + j * bh + 2;
          ctx.fillStyle = '#4b5563'; ctx.fillRect(bx, by, bw - 4, bh - 4);
          ctx.fillStyle = '#111827'; ctx.fillRect(bx + 3, by + 3, bw - 10, bh - 10);
          ctx.fillStyle = it.status === 'off' ? '#374151' : (Math.random() < 0.85 ? '#22c55e' : '#38bdf8'); ctx.fillRect(bx + bw - 9, by + 4, 3, 3);
        }
      }
    } else if (it.kind === 'patch' || it.kind === 'switch') {
      var n = it.kind === 'patch' ? 24 : 24, pw = (x1 - x0) / (n / 2 + 1);
      for (i = 0; i < n; i++) {
        var px = x0 + Math.floor(i / 2) * pw, py = i % 2 ? H * 0.55 : H * 0.15;
        ctx.fillStyle = '#0a0d12'; ctx.fillRect(px, py, pw * 0.8, H * 0.3);
        if (it.kind === 'switch') { ctx.fillStyle = Math.random() < 0.6 ? '#22c55e' : '#273244'; ctx.fillRect(px + 2, py + 2, pw * 0.25, 3); }
        else if (Math.random() < 0.55) { ctx.fillStyle = ['#3b82f6', '#eab308', '#22c55e', '#e5e7eb'][i % 4]; ctx.fillRect(px + pw * 0.2, py + H * 0.08, pw * 0.4, H * 0.14); }
      }
    } else if (it.kind === 'pdu') {
      for (i = 0; i < 10; i++) { var ox = x0 + i * (x1 - x0) / 10; ctx.fillStyle = '#0a0a0a'; ctx.fillRect(ox, H * 0.2, (x1 - x0) / 12, H * 0.6); ctx.fillStyle = '#374151'; ctx.fillRect(ox + 6, H * 0.35, 4, H * 0.25); ctx.fillRect(ox + 16, H * 0.35, 4, H * 0.25); }
    } else if (it.kind === 'ups') {
      ctx.fillStyle = '#0f766e'; ctx.fillRect(x0, H * 0.18, 160, H * 0.5);
      ctx.fillStyle = '#99f6e4'; ctx.font = Math.min(20, H * 0.25) + 'px Consolas'; ctx.fillText('LOAD 42%', x0 + 10, H * 0.43);
      for (i = 0; i < 5; i++) { ctx.fillStyle = i < 4 ? '#22c55e' : '#374151'; ctx.fillRect(x0 + 190 + i * 22, H * 0.3, 16, H * 0.3); }
    } else if (it.kind === 'ac') {
      for (j = 0; j < 14; j++) { ctx.fillStyle = 'rgba(0,0,0,.18)'; ctx.fillRect(x0, 10 + j * (H - 20) / 14, x1 - x0, 3); }
    } else {
      for (i = 0; i < 8; i++) { ctx.fillStyle = '#0a0d12'; ctx.fillRect(x1 - (i + 1) * 30, H * 0.3, 24, H * 0.4); ctx.fillStyle = it.status === 'off' ? '#273244' : '#22c55e'; ctx.fillRect(x1 - (i + 1) * 30 + 2, H * 0.3 + 2, 7, 3); }
      ctx.fillStyle = 'rgba(255,255,255,.55)'; ctx.font = '700 ' + Math.min(26, H * 0.4) + 'px Arial';
      ctx.fillText(it.kind === 'firewall' ? 'FIREWALL' : it.kind === 'router' ? 'ROUTER' : '', x0, H / 2);
    }
  }, 1024, Math.max(64, Math.min(640, Math.round(it.h * 64))), false);
}
function dcStatusHex(s) { return (DC_STATUS[s] || DC_STATUS[''])[0]; }
function dcBuild(c, layout, devices, roomId) {
  var st = c.st, THREE = st.THREE;
  glClear(c.world);
  c.rackObjs = {}; c.pickables = []; c.beacons = []; c.devObjs = {}; c.itemObjs = {}; c.doorRacks = []; c.aisleFx = []; c.aisleObjs = {}; c.halos = []; c.objObjs = {}; c._wallTex = null; c.roomDoors = [];
  st.lod = [];
  var main = c === DC.ctx;
  c.layout = layout; c.devices = devices;
  var room = dcRoomOf(layout, roomId);
  c.room = room;
  var RW = room.w * DC_TILE, RD = room.d * DC_TILE, ox = -RW / 2, oz = -RD / 2;
  c.ox = ox; c.oz = oz;
  // 地板 / 架空层 / 墙: 按机房形状生成(挖掉的区域没有地板, 墙沿边界走, 门那一段开门洞), 见 dc3d-models.js
  var wallH = room.h || 300;
  c.wallH = wallH;
  dcBuildShell(c, room, wallH);
  // 房间名写在地上
  var rl = glLabel(room.name, { size: 70, color: '#e2e8f0', bg: 'rgba(15,23,42,.6)', border: '#38bdf8' });
  rl.position.set(0, wallH + 60, oz); c.world.add(rl);
  if (c.sun) {
    var sc = c.sun.shadow.camera, R = Math.max(RW, RD) * 0.75;
    sc.left = -R; sc.right = R; sc.top = R; sc.bottom = -R; sc.updateProjectionMatrix();
  }
  var steel = new THREE.MeshStandardMaterial({ color: 0x2e3645, metalness: 0.85, roughness: 0.3, envMapIntensity: 1.3 });
  var glass = new THREE.MeshStandardMaterial({ color: 0x9fd3ff, metalness: 0.9, roughness: 0.08, transparent: true, opacity: 0.13, depthWrite: false });
  var perfT = dcPerfTex().clone(); perfT.needsUpdate = true; perfT.userData.shared = false;
  var back = new THREE.MeshStandardMaterial({ map: perfT, metalness: 0.5, roughness: 0.6 });
  var tray = new THREE.MeshStandardMaterial({ color: 0xca8a04, metalness: 0.6, roughness: 0.45 });
  var perfFloor = new THREE.MeshStandardMaterial({ map: dcFloorTex(true), roughness: 0.8 });
  var doorMap = dcDoorTex().clone(); doorMap.needsUpdate = true; doorMap.userData.shared = false; doorMap.repeat.set(3, 26);
  var doorMat = new THREE.MeshStandardMaterial({ color: 0x151b25, metalness: 0.75, roughness: 0.4, alphaMap: doorMap, transparent: true, side: THREE.DoubleSide, depthWrite: false });
  var frameMat = new THREE.MeshStandardMaterial({ color: 0x2a3140, metalness: 0.8, roughness: 0.35 });
  var handleMat = new THREE.MeshStandardMaterial({ color: 0xc0c7d2, metalness: 0.95, roughness: 0.2 });
  layout.racks.filter(function (k) { return k.room === room.id; }).forEach(function (k) {
    var g = new THREE.Group();
    var fw = (k.rot === 90 || k.rot === 270) ? 2 : 1, fd = 3 - fw;
    g.position.set(ox + (k.x + fw / 2) * DC_TILE, 0, oz + (k.z + fd / 2) * DC_TILE);
    g.rotation.y = (k.rot || 0) * Math.PI / 180;
    g.userData.pick = true; g.userData.rack = k;
    var H = DC_PLINTH + 3 + k.height * DC_U + DC_TOP;
    var status = dcRackStatus(k, layout, devices);
    var col = k.color ? new THREE.Color(k.color) : new THREE.Color(0x343d4d);
    if (dcIsUnit(k)) {
      H = 200;
      g.add(dcUnitModel(c, k));
    } else {
      // 底座
      var plinth = new THREE.Mesh(new THREE.BoxGeometry(DC_W, DC_PLINTH, DC_D), steel); plinth.position.y = DC_PLINTH / 2; plinth.castShadow = true; g.add(plinth);
      // 立柱
      [[-1, -1], [-1, 1], [1, -1], [1, 1]].forEach(function (p) {
        var post = new THREE.Mesh(new THREE.BoxGeometry(3, H - DC_PLINTH, 3), steel);
        post.position.set(p[0] * (DC_W / 2 - 1.5), DC_PLINTH + (H - DC_PLINTH) / 2, p[1] * (DC_D / 2 - 1.5));
        g.add(post);
      });
      // 侧板
      var sideMat = new THREE.MeshStandardMaterial({ color: col, metalness: 0.7, roughness: 0.3, envMapIntensity: 1.4 });
      [-1, 1].forEach(function (sx) {
        var sp = new THREE.Mesh(new THREE.BoxGeometry(1.2, H - DC_PLINTH - 2, DC_D - 4), sideMat);
        sp.position.set(sx * (DC_W / 2 - 0.6), DC_PLINTH + (H - DC_PLINTH) / 2, 0); sp.castShadow = true; sp.receiveShadow = true;
        g.add(sp);
      });
      // 顶盖 + 前沿状态灯条
      var top = new THREE.Mesh(new THREE.BoxGeometry(DC_W, DC_TOP, DC_D), new THREE.MeshStandardMaterial({ color: col.clone().multiplyScalar(1.25), metalness: 0.65, roughness: 0.35 }));
      top.position.y = H - DC_TOP / 2; top.castShadow = true; g.add(top);
      var sHex = dcStatusHex(status || '');
      var strip = new THREE.Mesh(new THREE.BoxGeometry(DC_W - 8, 2, 1), new THREE.MeshStandardMaterial({ color: sHex, emissive: sHex, emissiveIntensity: 2.2 }));
      strip.position.set(0, H - DC_TOP + 3, DC_D / 2 + 0.6); g.add(strip);
      if (status === 'bad') { strip.userData.blink = true; strip.userData.phase = Math.random() * 6; c.beacons.push(strip); }
      // 后门: 双开网孔门, 铰链在两侧往后开(看交换机背面的电源 / 风扇); 开着时点得到里面的设备
      var rH = H - DC_PLINTH - DC_TOP, rW = (DC_W - 4) / 2, rears = [];
      [-1, 1].forEach(function (sx) {
        var pv = new THREE.Group();
        pv.position.set(sx * (DC_W - 4) / 2, DC_PLINTH + rH / 2, -DC_D / 2 + 1);
        var rl = new THREE.Mesh(new THREE.BoxGeometry(rW - 0.4, rH, 1), back);
        rl.position.x = -sx * rW / 2;
        rl.raycast = function (rc, out) { if ((g.userData.rearCur || 0) < 0.35) { THREE.Mesh.prototype.raycast.call(rl, rc, out); } };
        pv.add(rl);
        var rh = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(1.3, 18, 2.2), handleMat)); rh.position.set(-sx * (rW - 3.2), 6, -1.3); pv.add(rh);
        g.add(pv); rears.push({ p: pv, s: sx });
      });
      g.userData.rears = rears;
      g.userData.rearT = g.userData.rearCur = main && (DC.rearAll || DC.openRear === k.id) ? 1 : 0;
      // 导轨 + U 号
      var rail = dcNoRay(new THREE.Mesh(new THREE.PlaneGeometry(DC_W - 4, k.height * DC_U), new THREE.MeshBasicMaterial({ map: dcRailTex(k.height), transparent: true, depthWrite: false })));
      rail.position.set(0, DC_PLINTH + 3 + k.height * DC_U / 2, DC_D / 2 - 7.5); g.add(rail);
      // 双开网孔前门: 铰链在两侧, 选中机柜时打开; 门开着时点门的位置能点到里面的设备
      var dH = H - DC_PLINTH - DC_TOP, dW = (DC_W - 3) / 2, doors = [];
      [-1, 1].forEach(function (sx) {
        var pivot = new THREE.Group();
        pivot.position.set(sx * (DC_W - 3) / 2, DC_PLINTH + dH / 2, DC_D / 2 + 0.3);
        var leaf = new THREE.Mesh(new THREE.BoxGeometry(dW - 0.6, dH - 2, 0.6), doorMat);
        leaf.position.x = -sx * dW / 2; leaf.renderOrder = 5; leaf.userData.door = true;
        leaf.raycast = function (rc, out) { if ((g.userData.doorCur || 0) < 0.35) { THREE.Mesh.prototype.raycast.call(leaf, rc, out); } };
        pivot.add(leaf);
        [[-sx * 0.8, 0, 1.6, dH], [-sx * (dW - 0.9), 0, 1.8, dH], [-sx * dW / 2, dH / 2 - 0.8, dW, 1.6], [-sx * dW / 2, -dH / 2 + 0.8, dW, 1.6]].forEach(function (b) {
          var fr = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(b[2], b[3], 1.4), frameMat)); fr.position.set(b[0], b[1], 0); pivot.add(fr);
        });
        var hd = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(1.3, 20, 2.4), handleMat)); hd.position.set(-sx * (dW - 3.4), 6, 1.4); pivot.add(hd);
        g.add(pivot); doors.push({ p: pivot, s: sx });
      });
      g.userData.doors = doors;
      g.userData.doorT = g.userData.doorCur = main && (DC.doorsAll || DC.openRack === k.id) ? 1 : 0;
      dcApplyDoor(g);
      c.doorRacks.push(g);
      // 走线槽
      var tr = new THREE.Mesh(new THREE.BoxGeometry(DC_W, 4, 30), tray); tr.position.set(0, H + 38, -DC_D / 4); g.add(tr);
      // 前面的通风地板(冷通道)
      var pf = new THREE.Mesh(new THREE.PlaneGeometry(DC_TILE - 2, DC_TILE - 2), perfFloor);
      pf.rotation.x = -Math.PI / 2; pf.position.set(0, 0.3, DC_D / 2 + DC_TILE / 2); pf.receiveShadow = true;
      g.add(pf);
      // 柜内设备
      var items = (layout.items || []).filter(function (it) { return it.rack === k.id; });
      items.forEach(function (it) {
        var depth = (DC_KIND[it.kind] || DC_KIND.other)[2];
        var mesh = dcItemModel(it);
        mesh.position.set(0, DC_PLINTH + 3 + (it.u - 1) * DC_U + it.h * DC_U / 2, DC_D / 2 - 8 - depth / 2);
        mesh.castShadow = true;
        mesh.userData.pick = true; mesh.userData.item = it; mesh.userData.rack = k;
        g.add(mesh);
        c.itemObjs[it.id] = mesh;
      });
      var place = dcPlace(k, (devices || []).filter(function (d) { return d.rack === k.name; }), items);
      g.userData.place = place;
      place.list.forEach(function (x) {
        var sw = glSwitchModel(x.d, { w: 48, uh: DC_U, d: 44 }, { keyPrefix: c.key, gap: 0, inset: 0.35 });
        var bottom = x.top - x.h + 1;
        sw.userData.baseZ = DC_D / 2 - 8 - 22;
        sw.userData.pullT = sw.userData.pullCur = main && DC.sel && DC.sel.t === 'dev' && DC.sel.ip === x.d.ip ? 1 : 0;
        sw.position.set(0, DC_PLINTH + 3 + (bottom - 1) * DC_U, sw.userData.baseZ + 26 * sw.userData.pullCur);
        sw.userData.pick = true; sw.userData.dev = x.d; sw.userData.rack = k; sw.userData.slot = x;
        g.add(sw);
        c.devObjs[x.d.ip] = sw;
      });
    }
    // 机柜顶上的名字和状态球
    var cnt = (layout.items || []).filter(function (it) { return it.rack === k.id; }).length + (devices || []).filter(function (d) { return d.rack === k.name; }).length;
    var lab = glLabel(k.name, { size: c.opts.labelSize || 30, color: '#f8fafc', sub: !dcIsUnit(k) ? cnt + ' 台设备 · ' + k.height + 'U' : dcUnitName(k),
      bg: 'rgba(15,23,42,.72)', border: status === 'bad' ? '#ef4444' : status === 'off' ? '#f59e0b' : '#38bdf8' });
    lab.position.set(0, H + 70, 0); g.add(lab);
    glLod(st, lab, Math.max(1400, Math.max(room.w, room.d) * DC_TILE * 1.1), Math.max(2600, Math.max(room.w, room.d) * DC_TILE * 2.2));
    if (status === 'bad' || status === 'off') {
      // 告警 / 离线的机柜脚下一圈往外扩散的光环, 远远就能看到
      var halo = new THREE.Mesh(new THREE.RingGeometry(DC_D * 0.52, DC_D * 0.6, 48), new THREE.MeshBasicMaterial({ color: status === 'bad' ? 0xff3b3b : 0xf59e0b, transparent: true, opacity: 0.5, side: THREE.DoubleSide, depthWrite: false }));
      halo.rotation.x = -Math.PI / 2; halo.position.y = 1.2; halo.userData.phase = Math.random(); halo.userData.noBox = true; halo.raycast = function () {};
      g.add(halo); c.halos.push(halo);
    }
    var bHex = dcStatusHex(status || '');
    var bc = new THREE.Mesh(new THREE.SphereGeometry(3.2, 16, 12), new THREE.MeshStandardMaterial({ color: bHex, emissive: bHex, emissiveIntensity: 2 }));
    bc.position.set(DC_W / 2 - 8, H + 3, DC_D / 2 - 8); g.add(bc);
    if (status === 'bad') { bc.userData.blink = true; bc.userData.phase = Math.random() * 6; c.beacons.push(bc); }
    g.userData.height = H;
    c.world.add(g);
    c.rackObjs[k.id] = g;
    c.pickables.push(g);
  });
  dcCeilingLights(c, room, RW, RD);
  (layout.aisles || []).filter(function (a) { return a.room === room.id; }).forEach(function (a) {
    var ag = dcAisleObj(c, a);
    c.world.add(ag); c.aisleObjs[a.id] = ag; c.pickables.push(ag);
  });
  (layout.objects || []).filter(function (o) { return o.room === room.id; }).forEach(function (o) {
    var og = dcObjModel(c, o);
    if (!og) { return; }
    dcObjPlace(c, og, o);
    og.userData.pick = true; og.userData.obj = o;
    c.world.add(og); c.objObjs[o.id] = og; c.pickables.push(og);
  });
  st.poke();
}
// 冷通道: 通风地板 + 淡蓝色冷气 + 玻璃顶 + 两端推拉门 + 往上飘的冷风粒子
function dcAisleObj(c, a) {
  var THREE = c.st.THREE, W = a.w * DC_TILE, D = a.d * DC_TILE, Hh = 226, g = new THREE.Group();
  g.position.set(c.ox + (a.x + a.w / 2) * DC_TILE, 0, c.oz + (a.z + a.d / 2) * DC_TILE);
  g.userData.pick = true; g.userData.aisle = a;
  var ft = dcFloorTex(true).clone(); ft.needsUpdate = true; ft.userData.shared = false; ft.repeat.set(a.w, a.d);
  var fl = new THREE.Mesh(new THREE.PlaneGeometry(W - 2, D - 2), new THREE.MeshStandardMaterial({ map: ft, roughness: 0.8, emissive: 0x0ea5e9, emissiveIntensity: 0.12 }));
  fl.rotation.x = -Math.PI / 2; fl.position.y = 0.7; fl.receiveShadow = true; g.add(fl);
  var vol = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(W - 4, Hh - 2, D - 4), new THREE.MeshBasicMaterial({ color: 0x38bdf8, transparent: true, opacity: 0.05, depthWrite: false })));
  vol.position.y = Hh / 2; g.add(vol);
  var roof = new THREE.Mesh(new THREE.BoxGeometry(W, 1.2, D), new THREE.MeshStandardMaterial({ color: 0x7dd3fc, transparent: true, opacity: 0.2, metalness: 0.9, roughness: 0.1, depthWrite: false }));
  roof.position.y = Hh; roof.renderOrder = 6; dcNoRay(roof); g.add(roof);
  var glow = new THREE.MeshStandardMaterial({ color: 0x38bdf8, emissive: 0x38bdf8, emissiveIntensity: 1.3 });
  [[W, 2, 0, -D / 2], [W, 2, 0, D / 2], [2, D, -W / 2, 0], [2, D, W / 2, 0]].forEach(function (b) {
    var bar = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(b[0], 2, b[1]), glow)); bar.position.set(b[2], Hh + 1, b[3]); g.add(bar);
  });
  // 两端推拉门(在长边方向的两头)
  var alongZ = a.d >= a.w, span = alongZ ? W : D, glassM = new THREE.MeshStandardMaterial({ color: 0xbae6fd, transparent: true, opacity: 0.16, metalness: 0.9, roughness: 0.05, depthWrite: false, side: THREE.DoubleSide });
  var frameM = new THREE.MeshStandardMaterial({ color: 0x94a3b8, metalness: 0.85, roughness: 0.3 });
  [-1, 1].forEach(function (sg) {
    var dg = new THREE.Group();
    if (alongZ) { dg.position.set(0, 0, sg * D / 2); } else { dg.position.set(sg * W / 2, 0, 0); dg.rotation.y = Math.PI / 2; }
    [-1, 1].forEach(function (half) {
      var p = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(span / 2 - 2, Hh - 4, 0.8), glassM)); p.position.set(half * span / 4, Hh / 2, 0); p.renderOrder = 6; dg.add(p);
      var fr = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(1.6, Hh - 4, 1.6), frameM)); fr.position.set(half * (span / 2 - 1), Hh / 2, 0); dg.add(fr);
      var hd = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(1.2, 26, 2.2), frameM)); hd.position.set(half * 4, 110, 1.2); dg.add(hd);
    });
    var top = dcNoRay(new THREE.Mesh(new THREE.BoxGeometry(span, 5, 3), frameM)); top.position.y = Hh - 2; dg.add(top);
    var sign = dcNoRay(new THREE.Mesh(new THREE.PlaneGeometry(Math.min(span - 8, 60), 12), new THREE.MeshBasicMaterial({ color: 0x0ea5e9 })));
    sign.position.set(0, Hh - 14, sg * 1.2 * (alongZ ? 1 : 1)); if (sg < 0) { sign.rotation.y = Math.PI; } dg.add(sign);
    g.add(dg);
  });
  // 冷风粒子
  var n = Math.min(160, 14 * a.w * a.d), pos = new Float32Array(n * 3), v = [];
  for (var i = 0; i < n; i++) { pos[i * 3] = (Math.random() - 0.5) * (W - 10); pos[i * 3 + 1] = Math.random() * Hh; pos[i * 3 + 2] = (Math.random() - 0.5) * (D - 10); v.push(18 + Math.random() * 30); }
  var geo = new THREE.BufferGeometry(); geo.setAttribute('position', new THREE.BufferAttribute(pos, 3));
  var pts = dcNoRay(new THREE.Points(geo, new THREE.PointsMaterial({ color: 0xbae6fd, size: 2.6, transparent: true, opacity: 0.65, depthWrite: false })));
  g.add(pts); c.aisleFx.push({ pts: pts, v: v, h: Hh - 4 });
  var racks = c === DC.ctx ? dcAisleRacks(a).length : 0;
  // 名字挂在通道一头的门楣上方, 镜头进到通道里时不挡视线
  var lab = glLabel('🧊 ' + a.name, { size: 15, color: '#e0f2fe', sub: (a.w * 0.6).toFixed(1) + ' × ' + (a.d * 0.6).toFixed(1) + ' m' + (racks ? ' · ' + racks + ' 柜' : ''), bg: 'rgba(8,47,73,.72)', border: '#38bdf8' });
  if (alongZ) { lab.position.set(0, Hh + 22, -D / 2); } else { lab.position.set(-W / 2, Hh + 22, 0); }
  g.add(lab);
  return g;
}
function dcView(kind, instant) {
  var c = DC.ctx;
  if (!c || !c.room) { return; }
  var RW = c.room.w * DC_TILE, RD = c.room.d * DC_TILE, R = Math.max(RW, RD);
  var pos, tgt = { x: 0, y: 80, z: 0 };
  if (kind === 'top') { pos = { x: 0.01, y: R * 1.35, z: 0.01 }; tgt = { x: 0, y: 0, z: 0 }; }
  else if (kind === 'front') { pos = { x: 0, y: 190, z: RD / 2 + R * 0.55 }; tgt = { x: 0, y: 110, z: 0 }; }
  else {
    // 斜视: 对准机柜实际所在的区域(机房大、机柜只占一角时不再把大半个画面给空地板)
    var b = dcRackBounds(c), S = Math.max(b.w, b.d, DC_TILE * 7);
    tgt = { x: b.cx, y: 70, z: b.cz };
    pos = { x: b.cx + S * 0.62 + 120, y: S * 0.72 + 230, z: b.cz + S * 0.9 + 260 };
  }
  if (kind === 'intro') {
    c.st.camera.position.set(tgt.x - RW * 0.9, R * 2.4, tgt.z + RD / 2 + R * 1.5); c.st.controls.target.set(tgt.x, 0, tgt.z);
    c.st.flyTo(pos, tgt, 2200);
    return;
  }
  if (instant) { c.st.camera.position.set(pos.x, pos.y, pos.z); c.st.controls.target.set(tgt.x, tgt.y, tgt.z); c.st.poke(); } else { c.st.flyTo(pos, tgt, 900); }
}
// 当前机房里机柜 / 冷通道占的范围(世界坐标)
function dcRackBounds(c) {
  var x0 = Infinity, z0 = Infinity, x1 = -Infinity, z1 = -Infinity;
  var add = function (x, z) { x0 = Math.min(x0, x); z0 = Math.min(z0, z); x1 = Math.max(x1, x + 1); z1 = Math.max(z1, z + 1); };
  (c.layout ? c.layout.racks : []).forEach(function (k) { if (k.room === c.room.id) { dcCells(k).forEach(function (cc) { add(cc[0], cc[1]); }); } });
  (c.layout && c.layout.aisles || []).forEach(function (a) { if (a.room === c.room.id) { add(a.x, a.z); add(a.x + a.w - 1, a.z + a.d - 1); } });
  if (!isFinite(x0)) { return { cx: 0, cz: 0, w: c.room.w * DC_TILE, d: c.room.d * DC_TILE }; }
  return { cx: c.ox + (x0 + x1) / 2 * DC_TILE, cz: c.oz + (z0 + z1) / 2 * DC_TILE, w: (x1 - x0) * DC_TILE, d: (z1 - z0) * DC_TILE };
}
function dcFlyToRack(k) {
  var c = DC.ctx, g = c && c.rackObjs[k.id];
  if (!g) { return; }
  var dir = new c.st.THREE.Vector3(0, 0, 1).applyAxisAngle(new c.st.THREE.Vector3(0, 1, 0), g.rotation.y);
  var p = g.position, h = g.userData.height || 200;
  // 正前方到对面一排机柜(或墙)有多少空地: 冷通道只有一两块地板宽, 镜头退不远, 就升高往下看
  var racksAt = {};
  DC.layout.racks.forEach(function (o) { if (o.room === k.room && o.id !== k.id) { dcCells(o).forEach(function (cc) { racksAt[cc[0] + ',' + cc[1]] = true; }); } });
  var fc = dcFrontCell(k), dx = Math.round(dir.x), dz = Math.round(dir.z), n = 0;
  while (n < 8 && fc[0] >= 0 && fc[1] >= 0 && fc[0] < c.room.w && fc[1] < c.room.d && !racksAt[fc[0] + ',' + fc[1]]) { n++; fc = [fc[0] + dx, fc[1] + dz]; }
  // 前面空地够就平视, 不够(冷通道对面就是机柜)就退远一点、抬高从对面机柜顶上往下看; 稍微偏一点角度, 能看到机柜侧面的立体感
  var open = n >= 4, dist = open ? 360 : 300, y = open ? h * 0.72 : h * 1.1 + 70;
  var sx = dir.z, sz = -dir.x;
  c.st.flyTo({ x: p.x + dir.x * dist + sx * dist * 0.3, y: y, z: p.z + dir.z * dist + sz * dist * 0.3 }, { x: p.x, y: h * 0.42, z: p.z });
}
function dcHud() {
  var c = DC.ctx;
  if (!c) { return; }
  var racks = DC.layout.racks.filter(function (k) { return k.room === DC.room; });
  var devs = 0, bad = 0, off = 0, items = 0, usedU = 0, totalU = 0;
  racks.forEach(function (k) {
    if (k.type && k.type !== 'rack') { return; }
    totalU += k.height;
    DC_devsIn(k).forEach(function (d) { devs++; usedU += pnlUnits(d); if (!d.alive) { off++; } else if (d.alerts > 0) { bad++; } });
    (DC.layout.items || []).forEach(function (it) { if (it.rack === k.id) { items++; usedU += it.h; } });
  });
  byId('dcHud').innerHTML = '机柜 <b>' + racks.filter(function (k) { return !k.type || k.type === 'rack'; }).length + '</b> · 监控交换机 <b>' + devs + '</b>' +
    (bad ? ' · <b style="color:#f87171;">告警 ' + bad + '</b>' : '') + (off ? ' · <b style="color:#fbbf24;">离线 ' + off + '</b>' : '') +
    ' · 其他设备 <b>' + items + '</b> · U 位使用 <b>' + usedU + '</b> / ' + totalU + (totalU ? ' (' + Math.round(usedU / totalU * 100) + '%)' : '') +
    (DC.layout.aisles.filter(function (a) { return a.room === DC.room; }).length ? ' · 冷通道 <b>' + DC.layout.aisles.filter(function (a) { return a.room === DC.room; }).length + '</b>' : '');
}

/* ---------- 搜索 / 过滤 ---------- */
function dcDoSearch() {
  DC.search = (byId('dcSearch').value || '').trim().toLowerCase();
  dcApplyFilter();
  if (!DC.search) { return; }
  var hit = null;
  DC.layout.racks.forEach(function (k) {
    if (hit || k.room !== DC.room) { return; }
    if (dcRackMatch(k)) { hit = k; }
  });
  if (!hit) {
    // 在别的机房?
    DC.layout.racks.forEach(function (k) { if (!hit && dcRackMatch(k)) { hit = k; } });
    if (hit && hit.room !== DC.room) { DC.room = hit.room; dcRefresh(false); }
  }
  if (hit) { dcFlyToRack(hit); }
}
function dcRackMatch(k) {
  var s = DC.search;
  if (!s) { return false; }
  if (k.name.toLowerCase().indexOf(s) >= 0) { return true; }
  return DC_devsIn(k).some(function (d) { return (d.name + ' ' + d.ip).toLowerCase().indexOf(s) >= 0; }) ||
    (DC.layout.items || []).some(function (it) { return it.rack === k.id && (it.name + ' ' + (it.ip || '') + ' ' + (it.model || '')).toLowerCase().indexOf(s) >= 0; });
}
function dcApplyFilter() {
  var c = DC.ctx;
  if (!c) { return; }
  var s = DC.search, alarm = DC.alarmOnly;
  var fade = function (obj, on) {
    obj.traverse(function (o) {
      if (!o.material || o.userData.door) { return; }
      (Array.isArray(o.material) ? o.material : [o.material]).forEach(function (m) {
        if (m.userData.bo === undefined) { m.userData.bo = m.opacity; m.userData.bt = m.transparent; }
        m.transparent = !on || m.userData.bt; m.opacity = on ? m.userData.bo : Math.min(m.userData.bo, 0.16);
      });
    });
  };
  Object.keys(c.devObjs || {}).forEach(function (ip) {
    var g = c.devObjs[ip], d = g.userData.dev;
    var on = (!s || (d.name + ' ' + d.ip).toLowerCase().indexOf(s) >= 0) && (!alarm || !d.alive || d.alerts > 0);
    fade(g, on);
  });
  Object.keys(c.itemObjs || {}).forEach(function (id) {
    var m = c.itemObjs[id], it = m.userData.item;
    var on = (!s || (it.name + ' ' + (it.ip || '') + ' ' + (it.model || '')).toLowerCase().indexOf(s) >= 0) && (!alarm || it.status === 'bad' || it.status === 'warn' || it.status === 'off');
    fade(m, on);
  });
  c.st.poke();
}

/* ---------- 悬停 / 选中 / 编辑 ---------- */
function dcTipHTML(u) {
  if (u.obj) {
    var ok = DC_OBJ[u.obj.kind] || {};
    return '<b>' + (ok.icon || '') + ' ' + esc(dcObjLabel(u.obj)) + '</b>' + (u.obj.name ? ' <span style="color:#94a3b8;">' + esc(ok.name) + '</span>' : '') + '<br><span style="color:#94a3b8;">场景物件' + (DC.edit ? ' · 拖动换位置' : '') + '</span>';
  }
  if (u.aisle) {
    var a = u.aisle, rk = dcAisleRacks(a);
    return '<b>🧊 ' + esc(a.name) + '</b><br>' + (a.w * 0.6).toFixed(1) + ' × ' + (a.d * 0.6).toFixed(1) + ' m (' + a.w + ' × ' + a.d + ' 块地板)' +
      '<br>朝向这里的机柜: ' + (rk.length ? rk.map(function (k) { return esc(k.name); }).join(', ') : '<span style="color:#fbbf24;">没有(机柜正面没有对着这个通道)</span>');
  }
  if (u.dev) {
    var d = u.dev, sl = u.slot;
    return '<b>' + esc(d.name) + '</b> <span style="color:#94a3b8;">' + esc(d.ip) + '</span><br>' +
      (d.alive ? '<span style="color:#4ade80;">● 在线</span>' : '<span style="color:#f87171;">● 离线</span>') + (d.alerts ? ' · <span style="color:#fca5a5;">' + d.alerts + ' 条告警</span>' : '') +
      '<br>' + esc(pnlBrand(d.brand)[0]) + (d.model ? ' ' + esc(d.model) : '') + '<br><span style="color:#67e8f9;">' + esc(glFormText(d.form)) + '</span>' +
      (sl ? '<br>U' + (sl.top - sl.h + 1) + ' ~ U' + sl.top + (d.manual && d.u ? ' (手工指定)' : ' (自动排列)') : '') +
      '<br>CPU ' + d.cpu + '% · 内存 ' + (d.mem > 0 ? d.mem + '%' : '-') + (d.note ? '<br><span style="color:#fbbf24;">📝 ' + esc(d.note) + '</span>' : '') + '<br><span style="color:#94a3b8;">单击拉出来看信息卡</span>';
  }
  if (u.item) {
    var it = u.item, st = DC_STATUS[it.status || ''] || DC_STATUS[''];
    return '<b>' + esc(it.name || DC_KIND[it.kind][0]) + '</b> <span style="color:#94a3b8;">' + esc(DC_KIND[it.kind][0]) + '</span><br>' +
      (it.model ? esc(it.model) + '<br>' : '') + 'U' + it.u + ' ~ U' + (it.u + it.h - 1) + ' · ' + it.h + 'U' + (it.ip ? '<br>' + esc(it.ip) : '') +
      '<br>状态: ' + st[1] + (it.note ? '<br><span style="color:#94a3b8;">' + esc(it.note) + '</span>' : '');
  }
  if (u.rack) {
    var k = u.rack;
    if (dcIsUnit(k)) { return '<b>' + dcUnitIcon(k) + ' ' + esc(k.name) + '</b><br>' + dcUnitName(k); }
    var used = 0;
    DC_devsIn(k).forEach(function (d) { used += pnlUnits(d); });
    (DC.layout.items || []).forEach(function (it) { if (it.rack === k.id) { used += it.h; } });
    return '<b>🗄️ ' + esc(k.name) + '</b>' + (k.auto ? ' <span style="color:#fbbf24;">(自动生成, 保存后固定)</span>' : '') +
      '<br>' + k.height + 'U · 已用 ' + used + 'U · 空闲 ' + Math.max(0, k.height - used) + 'U<br>监控交换机 ' + DC_devsIn(k).length + ' 台 · 其他设备 ' +
      (DC.layout.items || []).filter(function (it) { return it.rack === k.id; }).length + ' 台<br><span style="color:#94a3b8;">单击开门 · 双击飞到机柜前</span>';
  }
  return '';
}
function dcBindEditor(c) {
  var cv = c.st.renderer.domElement, THREE = c.st.THREE;
  var tip = byId('dcTip'), wrap = byId('dcWrap');
  var plane = new THREE.Plane(new THREE.Vector3(0, 1, 0), 0), hitP = new THREE.Vector3();
  var down = null, drag = null;
  var floorCell = function (e) {
    var r = cv.getBoundingClientRect();
    c.st.mouse.x = ((e.clientX - r.left) / r.width) * 2 - 1;
    c.st.mouse.y = -((e.clientY - r.top) / r.height) * 2 + 1;
    c.st.raycaster.setFromCamera(c.st.mouse, c.st.camera);
    if (!c.st.raycaster.ray.intersectPlane(plane, hitP)) { return null; }
    return { x: Math.floor((hitP.x - c.ox) / DC_TILE), z: Math.floor((hitP.z - c.oz) / DC_TILE) };
  };
  var showBox = function (helper, obj) { helper.fit(obj, 1.5); c.st.poke(); };
  var setGhost = function (cell) {
    var ghost = c.ghost;
    if (!ghost) {
      ghost = c.ghost = new THREE.Mesh(new THREE.BoxGeometry(DC_W, 200, DC_D), new THREE.MeshBasicMaterial({ color: 0x22c55e, transparent: true, opacity: 0.25, depthWrite: false }));
      c.st.scene.add(ghost);
    }
    if (!cell) { ghost.visible = false; c.st.poke(); return; }
    var isA = DC.mode && DC.mode.type === 'aisle', ok, k, ok2 = DC.mode && DC_OBJ[DC.mode.type];
    ghost.rotation.y = 0;
    if (DC.mode && DC.mode.type === 'cut') {
      // 框选挖掉: 第一下之前只标一块, 之后从第一下到鼠标位置拉出一个红框
      var a0 = DC.mode.a || cell, x0 = Math.min(a0.x, cell.x), z0 = Math.min(a0.z, cell.z), cw = Math.abs(a0.x - cell.x) + 1, cd = Math.abs(a0.z - cell.z) + 1;
      ghost.scale.set(cw, 0.05, cd / 2);
      ghost.position.set(c.ox + (x0 + cw / 2) * DC_TILE, 5, c.oz + (z0 + cd / 2) * DC_TILE);
      ghost.material.color.setHex(0xef4444); ghost.visible = true; c.st.poke();
      return;
    }
    if (ok2) {
      k = dcObjAt(c.room, cell, DC.mode.type, 0, function (x) { return dcObjFree(DC.layout, c.room, x); }) ;
      ok = dcObjFree(DC.layout, c.room, k);
      var fg = dcObjFoot(k), gh = ok2.wall ? 0.5 : ok2.solid ? 0.8 : 0.35, gy = DC.mode.type === 'camera' ? (c.wallH || 300) - 40 : 100 * gh;
      if (k.wall) {
        // 斜墙 / 弧墙: 虚影跟着墙转
        dcObjPlace(c, ghost, k); ghost.position.y = gy; ghost.scale.set(ok2.w, gh, ok2.d / 2);
      } else {
        ghost.rotation.y = 0;
        ghost.scale.set(fg.fx, gh, fg.fz / 2);
        ghost.position.set(c.ox + (k.x + fg.fx / 2) * DC_TILE, gy, c.oz + (k.z + fg.fz / 2) * DC_TILE);
      }
    } else if (isA) {
      k = dcAisleFit(cell);
      ok = !!k;
      k = k || { x: cell.x, z: cell.z, w: 1, d: 4 };
      ghost.scale.set(k.w, 0.12, k.d / 2);
      ghost.position.set(c.ox + (k.x + k.w / 2) * DC_TILE, 12, c.oz + (k.z + k.d / 2) * DC_TILE);
    } else {
      k = { x: cell.x, z: cell.z, rot: 0 };
      ok = dcFree(DC.layout, c.room, k);
      ghost.scale.set(1, 1, 1);
      ghost.position.set(c.ox + (cell.x + 0.5) * DC_TILE, 100, c.oz + (cell.z + 1) * DC_TILE);
    }
    ghost.material.color.setHex(ok ? 0x22c55e : 0xef4444);
    ghost.visible = true; ghost.userData.ok = ok; ghost.userData.cell = cell;
    c.st.poke();
  };
  cv.addEventListener('pointerdown', function (e) {
    down = { x: e.clientX, y: e.clientY, b: e.button };
    if (DC.edit && !DC.mode && e.button === 0) {
      var h = c.st.pick(e, c.pickables);
      var rackG = h ? (function (o) { while (o && !(o.userData && (o.userData.rack || o.userData.aisle || o.userData.obj) && o.parent === c.world)) { o = o.parent; } return o; })(h.obj) : null;
      if (rackG) { var dk = rackG.userData.aisle || rackG.userData.obj || rackG.userData.rack; drag = { g: rackG, k: dk, aisle: !!rackG.userData.aisle, obj: !!rackG.userData.obj, x0: dk.x, z0: dk.z, rot0: dk.rot || 0, wall0: dk.wall, t0: dk.t, moved: false }; }
    }
  });
  window.addEventListener('pointermove', function (e) {
    if (!DC.ctx || DC.ctx !== c) { return; }
    if (drag && down && (Math.abs(e.clientX - down.x) + Math.abs(e.clientY - down.y) > 5)) {
      c.st.controls.enabled = false;
      drag.moved = true;
      var cell = floorCell(e);
      if (cell) {
        var k = drag.k;
        if (drag.obj) {
          var np = dcObjAt(c.room, cell, k.kind, k.rot, function (x) { x.id = k.id; return dcObjFree(DC.layout, c.room, x, k.id); });
          if (!(DC_OBJ[k.kind] || {}).wall) { var ff = dcObjFoot(k); np.x = cell.x - Math.floor((ff.fx - 1) / 2); np.z = cell.z - Math.floor((ff.fz - 1) / 2); }
          dcObjSetPlace(k, np);
          dcObjPlace(c, drag.g, k);
          drag.ok = dcObjFree(DC.layout, c.room, k, k.id);
        } else if (drag.aisle) {
          k.x = cell.x - Math.floor(k.w / 2); k.z = cell.z - Math.floor(k.d / 2);
          drag.g.position.set(c.ox + (k.x + k.w / 2) * DC_TILE, 0, c.oz + (k.z + k.d / 2) * DC_TILE);
          drag.ok = dcAisleFree(DC.layout, c.room, k, k.id);
        } else {
          var fw = (k.rot === 90 || k.rot === 270) ? 2 : 1, fd = 3 - fw;
          k.x = cell.x; k.z = cell.z;
          drag.g.position.set(c.ox + (k.x + fw / 2) * DC_TILE, 0, c.oz + (k.z + fd / 2) * DC_TILE);
          drag.ok = dcFree(DC.layout, c.room, k, k.id);
        }
        c.selBox.setColor(drag.ok ? 0x22c55e : 0xef4444);
        showBox(c.selBox, drag.g);
        c.st.poke();
      }
      tip.style.display = 'none';
      return;
    }
    if (e.target !== cv) { return; }
    if (DC.mode) { setGhost(floorCell(e)); return; }
    if (down && e.buttons) { return; }
    // 悬停拾取每帧最多做一次(鼠标事件一秒能来一两百次, 每次都对整个机房做射线求交会卡)
    c.hoverEvt = { clientX: e.clientX, clientY: e.clientY };
    if (!c.hoverRaf) { c.hoverRaf = requestAnimationFrame(function () { c.hoverRaf = 0; if (DC.ctx === c && c.st) { hoverAt(c.hoverEvt); } }); }
  });
  var hoverAt = function (e) {
    var h = c.st.pick(e, c.pickables);
    var obj = h ? h.obj : null;
    if (obj !== c.hover) { c.hover = obj; showBox(c.hoverBox, obj); c.st.poke(); }
    if (obj) {
      if (c.tipObj !== obj) { c.tipObj = obj; tip.innerHTML = dcTipHTML(obj.userData); }
      tip.style.display = 'block';
      var r = wrap.getBoundingClientRect(), x = e.clientX - r.left + 16, y = e.clientY - r.top + 16;
      if (x + tip.offsetWidth > r.width - 340) { x = e.clientX - r.left - tip.offsetWidth - 16; }
      tip.style.left = Math.max(0, x) + 'px'; tip.style.top = Math.max(0, Math.min(y, r.height - tip.offsetHeight - 4)) + 'px';
      cv.style.cursor = 'pointer';
    } else { c.tipObj = null; tip.style.display = 'none'; cv.style.cursor = DC.mode ? 'crosshair' : ''; }
  };
  window.addEventListener('pointerup', function (e) {
    if (!DC.ctx || DC.ctx !== c) { return; }
    var d = down; down = null;
    c.st.controls.enabled = true;
    if (drag) {
      var dr = drag; drag = null;
      if (dr.moved) {
        if (!dr.ok) { dr.k.x = dr.x0; dr.k.z = dr.z0; if (dr.obj) { dr.k.rot = dr.rot0; if (dr.wall0) { dr.k.wall = dr.wall0; dr.k.t = dr.t0; } else { delete dr.k.wall; delete dr.k.t; } } dcMsg('那里放不下(超出机房或跟别的机柜重叠), 已恢复原位', true); }
        else { dcMarkDirty(); }
        dcRefresh(false);
        return;
      }
    }
    if (!d || e.target !== cv || Math.abs(e.clientX - d.x) + Math.abs(e.clientY - d.y) > 5 || d.b !== 0) { return; }
    if (DC.mode) {
      var cell = floorCell(e);
      if (cell) { dcPlaceNew(cell, e.shiftKey); }
      return;
    }
    var h = c.st.pick(e, c.pickables);
    if (!h) { DC.sel = null; showBox(c.selBox, null); dcCloseCard(); dcRenderSide(); return; }
    var u = h.obj.userData;
    if (u.obj && u.obj.kind === 'door' && !DC.edit) { h.obj.userData.manual = !h.obj.userData.manual; c.st.poke(); }
    DC.sel = u.dev ? { t: 'dev', ip: u.dev.ip } : u.item ? { t: 'item', id: u.item.id } : u.aisle ? { t: 'aisle', id: u.aisle.id } : u.obj ? { t: 'obj', id: u.obj.id } : { t: 'rack', id: u.rack.id };
    c.selBox.setColor(0xfbbf24);
    showBox(c.selBox, u.dev || u.item || u.obj ? h.obj : null);
    if (u.dev && !DC.edit) { dcOpenCard(u.dev.ip, e); } else { dcCloseCard(); }
    dcRenderSide();
  });
  cv.addEventListener('dblclick', function (e) {
    var h = c.st.pick(e, c.pickables);
    if (!h) { return; }
    var u = h.obj.userData;
    if (u.dev) { dcFlyToDev(u.dev.ip); return; }
    if (u.rack) { dcFlyToRack(u.rack); }
  });
  cv.addEventListener('pointerleave', function () { tip.style.display = 'none'; c.hover = null; showBox(c.hoverBox, null); setGhost(null); c.st.poke(); });
  window.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && DC.mode) { e.stopPropagation(); dcCancelPlace(); return; }
    if (e.key === 'Escape' && byId('dcCard')) { e.stopPropagation(); dcCloseCard(); }
  }, true);
}
function dcMarkDirty() { DC.dirty = true; var el = byId('dcDirty'); if (el) { el.innerText = '● 有未保存的修改'; } }
function dcMsg(t, bad) { var el = byId('dcSideMsg'); if (el) { el.style.color = bad ? '#fca5a5' : '#86efac'; el.innerText = t; } else if (bad) { alert(t); } }
function dcToggleEdit() {
  if (!DC.edit && !authenticated) { openAuthGate(function () { dcToggleEdit(); }); return; }
  if (DC.edit && DC.dirty && !confirm('机房布局有未保存的修改, 退出编辑会丢弃这些修改。继续?')) { return; }
  DC.edit = !DC.edit;
  if (!DC.edit) { dcCancelPlace(); if (DC.dirty) { DC.dirty = false; DC.layout = JSON.parse(JSON.stringify(DC.saved)); DC.renames = {}; dcRefresh(false); } }
  byId('dcEditToggle').innerText = DC.edit ? '✅ 完成编辑' : '✏️ 编辑机房';
  byId('dcEditBtns').style.display = DC.edit ? 'inline-flex' : 'none';
  dcRenderSide();
  dcFsSync();
}
// 全屏时的菜单: 全屏的只是 3D 画面(dcWrap), 上面那排工具条在它外面 —— 以前编辑机房时一全屏就看不到
// 「➕ 机柜 / 💾 保存」了。现在: 编辑中全屏 → 工具条搬进全屏画面顶部; 只是看(不编辑)全屏 →
// 工具条和右侧面板都收起来, 画面干净, 右上角「☰ 菜单」可以临时展开。
function dcFsSync() {
  var wrap = byId('dcWrap'), tb = byId('dcToolbar'), pane = byId('dcPane');
  if (!wrap || !tb || !pane) { return; }
  var fs = typeof fsActive === 'function' && fsActive(wrap), show = fs && (DC.edit || DC.fsMenu);
  if (show && tb.parentNode !== wrap) { wrap.insertBefore(tb, wrap.firstChild); tb.classList.add('dc-fs-toolbar'); }
  if (!show && tb.parentNode === wrap) { tb.classList.remove('dc-fs-toolbar'); pane.insertBefore(tb, pane.firstChild); }
  wrap.classList.toggle('dc-fs-clean', fs && !DC.edit && !DC.fsMenu);
  if (DC.menuBtn) { DC.menuBtn.style.display = fs && !DC.edit ? '' : 'none'; DC.menuBtn.classList.toggle('on', !!DC.fsMenu); }
}
function dcAddSelect() {
  var o = '<option value="">➕ 落地设备 / 场景…</option><optgroup label="落地设备(占一个机柜位)">' +
    Object.keys(DC_UNIT).map(function (k) { return '<option value="' + k + '">' + DC_UNIT[k][1] + ' ' + DC_UNIT[k][0] + '</option>'; }).join('') + '</optgroup><optgroup label="场景物件">' +
    Object.keys(DC_OBJ).map(function (k) { return '<option value="' + k + '">' + DC_OBJ[k].icon + ' ' + DC_OBJ[k].name + '</option>'; }).join('') + '</optgroup>';
  return '<select class="filter-btn" id="dcAddSel" onchange="if(this.value){dcStartPlace(this.value);}this.value=&quot;&quot;">' + o + '</select>';
}
function dcStartPlace(type) {
  DC.mode = { type: type };
  var m = byId('dcMode');
  m.style.display = 'block';
  var ob = DC_OBJ[type];
  m.innerText = type === 'cut' ? '在地板上点两下, 框出要挖掉的矩形(对角两块地板) · Esc 取消' : type === 'aisle' ? '点击两排机柜之间的地板放冷通道(自动沿空地延伸, 放好后在右侧改大小) · Esc 取消'
    : ob ? '点击地板放置' + ob.name + (ob.wall ? '(会贴到最近的那面墙上)' : '') + ' · 按住 Shift 连续放 · Esc 取消'
    : '点击地板放置' + (DC_UNIT[type] ? DC_UNIT[type][0] : '机柜') + '(绿色 = 可以放, 红色 = 被占用) · Esc 取消';
}
function dcCancelPlace() {
  DC.mode = null;
  var m = byId('dcMode'); if (m) { m.style.display = 'none'; }
  if (DC.ctx && DC.ctx.ghost) { DC.ctx.ghost.visible = false; DC.ctx.st.poke(); }
}
function dcNewId(p) { return p + Date.now().toString(36) + Math.floor(Math.random() * 1e6).toString(36); }
function dcNextName(type) {
  var names = {};
  DC.layout.racks.forEach(function (k) { names[k.name] = true; });
  if (DC_UNIT[type]) {
    for (var i = 1; i < 999; i++) { var n = DC_UNIT[type][2] + i; if (!names[n]) { return n; } }
  }
  var rows = 'ABCDEFGHIJ';
  for (var r = 0; r < rows.length; r++) { for (var j = 1; j < 100; j++) { var nm = rows[r] + ('0' + j).slice(-2); if (!names[nm]) { return nm; } } }
  return '机柜-' + Date.now();
}
// 从点击的地板往两个方向延伸, 找一块能放下的长条(优先顺着机柜排的方向)
function dcAisleFit(cell) {
  var room = DC.ctx.room, used = dcUsedCells(DC.layout, room), free = function (x, z) { return dcIn(room, x, z) && !used[x + ',' + z]; };
  if (!free(cell.x, cell.z)) { return null; }
  var run = function (dx, dz) { var x0 = cell.x, z0 = cell.z, x1 = cell.x, z1 = cell.z; while (free(x0 - dx, z0 - dz)) { x0 -= dx; z0 -= dz; } while (free(x1 + dx, z1 + dz)) { x1 += dx; z1 += dz; } return { x0: x0, z0: z0, n: Math.max(x1 - x0, z1 - z0) + 1 }; };
  var hx = run(1, 0), vz = run(0, 1);
  // 两边都是机柜的那个方向就是通道方向: 横向能延伸得多就横着放
  var horiz = hx.n >= vz.n, len = Math.min(horiz ? hx.n : vz.n, 12);
  var a = horiz ? { x: Math.max(hx.x0, Math.min(cell.x, hx.x0 + hx.n - len)), z: cell.z, w: len, d: 1 } : { x: cell.x, z: Math.max(vz.z0, Math.min(cell.z, vz.z0 + vz.n - len)), w: 1, d: len };
  if (horiz) { a.x = hx.x0; } else { a.z = vz.z0; }
  return dcAisleFree(DC.layout, room, a) ? a : null;
}
function dcPlaceNew(cell, keep) {
  var type = DC.mode.type;
  if (type === 'cut') {
    if (!DC.mode.a) { DC.mode.a = cell; byId('dcMode').innerText = '再点一下框的另一个角 · Esc 取消'; return; }
    if (dcCutAdd(DC.mode.a, cell) || !keep) { dcCancelPlace(); }
    return;
  }
  if (DC_OBJ[type]) {
    var o = dcObjAt(DC.ctx.room, cell, type, 0, function (x) { return dcObjFree(DC.layout, DC.ctx.room, x); });
    if (!dcObjFree(DC.layout, DC.ctx.room, o)) { return; }
    o.id = dcNewId('o'); o.room = DC.room; o.name = '';
    DC.layout.objects.push(o);
    DC.sel = { t: 'obj', id: o.id };
    dcMarkDirty(); dcRefresh(false);
    if (!keep) { dcCancelPlace(); }
    return;
  }
  if (type === 'aisle') {
    var a = dcAisleFit(cell);
    if (!a) { return; }
    a.id = dcNewId('a'); a.room = DC.room; a.name = '冷通道 ' + (DC.layout.aisles.filter(function (o) { return o.room === DC.room; }).length + 1);
    DC.layout.aisles.push(a);
    DC.sel = { t: 'aisle', id: a.id };
    dcMarkDirty(); dcRefresh(false);
    if (!keep) { dcCancelPlace(); }
    return;
  }
  var k = { id: dcNewId('r'), name: dcNextName(type), room: DC.room, x: cell.x, z: cell.z, rot: 0, height: 42, type: type };
  if (!dcFree(DC.layout, DC.ctx.room, k)) { return; }
  DC.layout.racks.push(k);
  DC.sel = { t: 'rack', id: k.id };
  dcMarkDirty();
  dcRefresh(false);
  // 按住 Shift 可以连续放
  if (!keep) { dcCancelPlace(); }
}
function dcRevert() {
  if (DC.dirty && !confirm('放弃所有未保存的机房修改?')) { return; }
  DC.dirty = false; DC.layout = JSON.parse(JSON.stringify(DC.saved)); DC.renames = {}; DC.sel = null;
  dcRefresh(false);
}
function dcSave() {
  var lay = JSON.parse(JSON.stringify(DC.layout));
  // 自动生成的机柜: 换成正式 ID 一起保存
  var idMap = {};
  lay.racks.forEach(function (k) { if (k.auto) { var nid = dcNewId('r'); idMap[k.id] = nid; k.id = nid; delete k.auto; } });
  (lay.items || []).forEach(function (it) { if (idMap[it.rack]) { it.rack = idMap[it.rack]; } });
  // 设备形态是单独立即保存的, 用服务器上最新的那份, 免得被本地旧数据覆盖
  lay.forms = (DC.saved && DC.saved.forms) || {};
  var renames = DC.renames;
  byId('dcDirty').innerText = '保存中...';
  authedApi('/api/datacenter/save', { layout: lay }).then(function () {
    // 改了名字的机柜: 里面的交换机跟着改到新名字(包括按 sysLocation 自动归入的, 改名后固定到新机柜)
    var jobs = [];
    Object.keys(renames).forEach(function (old) {
      var nw = renames[old];
      DC.devices.filter(function (d) { return d.rack === old; }).forEach(function (d) {
        jobs.push(authedApi('/api/racks/assign', { ip: d.ip, rack: nw, u: d.manual ? d.u : 0 }));
      });
    });
    return Promise.all(jobs);
  }).then(function () {
    DC.dirty = false; DC.renames = {};
    DC.layout = null;
    if (DC.sel && idMap[DC.sel.id]) { DC.sel.id = idMap[DC.sel.id]; }
    return dcLoad(false);
  }).then(function () { dcMsg('✅ 机房布局已保存'); })
    .catch(function (e) { byId('dcDirty').innerText = '● 保存失败: ' + e; alert('保存失败: ' + e); });
}

/* ---------- 右侧面板 ---------- */
function dcRackById(id) { return DC.layout.racks.filter(function (k) { return k.id === id; })[0]; }
function dcRenderSide() {
  var side = byId('dcSide');
  if (!side) { return; }
  var sel = DC.sel, html = '';
  if (sel && sel.t === 'rack' && dcRackById(sel.id)) { html = dcSideRack(dcRackById(sel.id)); }
  else if (sel && sel.t === 'dev') {
    var d = DC.devices.filter(function (x) { return x.ip === sel.ip; })[0], dk = d && DC.layout.racks.filter(function (k) { return k.name === d.rack; })[0];
    html = !d ? '' : (DC.edit || !dk ? dcSideDev(d) : dcSideRack(dk));
  }
  else if (sel && sel.t === 'item') { var it = (DC.layout.items || []).filter(function (x) { return x.id === sel.id; })[0]; html = it ? dcSideItem(it) : ''; }
  else if (sel && sel.t === 'obj') { var ob = dcObjById(sel.id); html = ob ? dcSideObject(ob) : ''; }
  else if (sel && sel.t === 'aisle') { var ai = DC.layout.aisles.filter(function (x) { return x.id === sel.id; })[0]; html = ai ? dcSideAisle(ai) : ''; }
  if (!html) { DC.sel = null; html = dcSideRoom(); }
  side.innerHTML = (DC.sel ? '<span class="x" onclick="DC.sel=null;DC.ctx.selBox.visible=false;dcCloseCard();DC.ctx.st.poke();dcRenderSide()">✕</span>' : '') + html + '<div class="msg" id="dcSideMsg"></div>';
  dcSyncDoors();
}
function dcSideRoom() {
  var room = dcRoomOf(DC.layout, DC.room);
  var racks = DC.layout.racks.filter(function (k) { return k.room === room.id; }).sort(function (a, b) { return naturalCmp(a.name, b.name); });
  var area = dcRoomArea(room) * 0.36;
  var html = '<h4>🏢 ' + esc(room.name) + '</h4><div class="sub">' + (room.w * 0.6).toFixed(1) + ' m × ' + (room.d * 0.6).toFixed(1) + ' m · 层高 ' + ((room.h || 300) / 100).toFixed(1) + ' m · ' + area.toFixed(0) + ' ㎡ · ' + room.w + ' × ' + room.d + ' 块地板</div>';
  if (DC.edit) {
    html += '<div class="grid2"><div style="grid-column:1/-1;"><label>机房名</label><input id="dcRoomName" value="' + esc(room.name) + '"></div>' +
      '<div><label>宽 东西向(米)</label><input id="dcRoomW" type="number" min="2.4" max="90" step="0.6" value="' + (room.w * 0.6).toFixed(1) + '" oninput="dcRoomHint()"></div>' +
      '<div><label>深 南北向(米)</label><input id="dcRoomD" type="number" min="2.4" max="90" step="0.6" value="' + (room.d * 0.6).toFixed(1) + '" oninput="dcRoomHint()"></div>' +
      '<div><label>层高(米, 地板到吊顶)</label><input id="dcRoomH" type="number" min="2.4" max="8" step="0.1" value="' + ((room.h || 300) / 100).toFixed(1) + '"></div><div class="sub" id="dcRoomHint" style="align-self:end;"></div></div>' +
      '<div class="btns"><button class="pri" onclick="dcRoomApply()">应用</button><button onclick="dcRoomFit()" title="把机房缩到刚好装下机柜 / 冷通道 / 物件, 四周留 1.2 米">📦 按内容收缩</button><button onclick="dcRoomAdd()">➕ 新建机房</button>' +
      (DC.layout.rooms.length > 1 ? '<button class="bad" onclick="dcRoomDelete()">删除这个机房</button>' : '') + '</div>' +
      '<div class="sub" style="margin-top:6px;">整体平移机房里的东西(放大机房后想往左 / 往上留空间时用): ' +
      '<button class="dc-nudge" onclick="dcRoomShift(-1,0)">←</button><button class="dc-nudge" onclick="dcRoomShift(1,0)">→</button><button class="dc-nudge" onclick="dcRoomShift(0,-1)">↑</button><button class="dc-nudge" onclick="dcRoomShift(0,1)">↓</button></div>' +
      dcCutHTML(room) +
      '<div class="sub" style="margin-top:8px;">工具栏「➕ 机柜」后点地板放置(按住 Shift 连续放); 编辑模式下直接拖动机柜换位置。改完点「💾 保存机房」。</div><hr>';
  } else { html += '<hr>'; }
  var aisles = DC.layout.aisles.filter(function (a) { return a.room === room.id; });
  if (aisles.length) {
    html += '<div class="sub" style="margin-bottom:4px;">冷通道</div><table>' + aisles.map(function (a) {
      return '<tr class="dc-row" onclick="DC.sel={t:&quot;aisle&quot;,id:&quot;' + esc(a.id) + '&quot;};dcRenderSide()"><td style="width:14px;color:#38bdf8;">🧊</td><td>' + esc(a.name) + '</td><td style="text-align:right;color:#94a3b8;">' +
        dcAisleRacks(a).length + ' 柜</td></tr>';
    }).join('') + '</table><hr>';
  }
  html += '<div class="sub" style="margin-bottom:4px;">机柜列表(点击飞过去)</div><table>' + (racks.length ? racks.map(function (k) {
    var st = dcRackStatus(k), dot = st === 'bad' ? '#ef4444' : st === 'off' ? '#f59e0b' : st === 'ok' ? '#22c55e' : '#38bdf8';
    var n = DC_devsIn(k).length + (DC.layout.items || []).filter(function (it) { return it.rack === k.id; }).length;
    return '<tr class="dc-row" onclick="DC.sel={t:&quot;rack&quot;,id:&quot;' + esc(k.id) + '&quot;};dcRenderSide();dcFlyToRack(dcRackById(&quot;' + esc(k.id) + '&quot;))"><td style="width:14px;"><span style="color:' + dot + ';">●</span></td><td>' + esc(k.name) +
      (k.auto ? ' <span class="chip">自动</span>' : '') + '</td><td style="text-align:right;color:#94a3b8;">' + (dcIsUnit(k) ? dcUnitName(k) : n + ' 台') + '</td></tr>';
  }).join('') : '<tr><td class="sub">还没有机柜' + (DC.edit ? ', 点工具栏「➕ 机柜」添加' : ', 登录后点「✏️ 编辑机房」添加') + '</td></tr>') + '</table>';
  return html;
}
function dcSideAisle(a) {
  var rk = dcAisleRacks(a), bad = 0;
  rk.forEach(function (k) { var s = dcRackStatus(k); if (s === 'bad' || s === 'off') { bad++; } });
  var html = '<h4>🧊 ' + esc(a.name) + '</h4><div class="sub">冷通道 · ' + (a.w * 0.6).toFixed(1) + ' × ' + (a.d * 0.6).toFixed(1) + ' m · 位置 (' + a.x + ', ' + a.z + ')</div>' +
    '<div style="margin-top:8px;">朝向这个通道的机柜 <b>' + rk.length + '</b> 个' + (bad ? ' · <span style="color:#fca5a5;">' + bad + ' 个有告警/离线</span>' : '') + '</div>' +
    '<table>' + rk.map(function (k) {
      var st = dcRackStatus(k), dot = st === 'bad' ? '#ef4444' : st === 'off' ? '#f59e0b' : st === 'ok' ? '#22c55e' : '#38bdf8';
      return '<tr class="dc-row" onclick="DC.sel={t:&quot;rack&quot;,id:&quot;' + esc(k.id) + '&quot;};dcRenderSide();dcFlyToRack(dcRackById(&quot;' + esc(k.id) + '&quot;))"><td style="width:14px;color:' + dot + ';">●</td><td>' + esc(k.name) + '</td><td style="text-align:right;color:#94a3b8;">' +
        (DC_devsIn(k).length + DC.layout.items.filter(function (it) { return it.rack === k.id; }).length) + ' 台</td></tr>';
    }).join('') + '</table>' + (rk.length ? '' : '<div class="sub">机柜正面要对着通道才算(在机柜上「⟳ 旋转 90°」调整朝向)。</div>');
  if (DC.edit) {
    html += '<hr><div class="grid2"><div style="grid-column:1/-1;"><label>名称</label><input id="dcAiName" value="' + esc(a.name) + '"></div>' +
      '<div><label>X(左上角)</label><input id="dcAiX" type="number" min="0" value="' + a.x + '"></div><div><label>Z(左上角)</label><input id="dcAiZ" type="number" min="0" value="' + a.z + '"></div>' +
      '<div><label>宽(东西, 块)</label><input id="dcAiW" type="number" min="1" max="100" value="' + a.w + '"></div><div><label>深(南北, 块)</label><input id="dcAiD" type="number" min="1" max="100" value="' + a.d + '"></div></div>' +
      '<div class="btns"><button class="pri" onclick="dcAisleApply(&quot;' + esc(a.id) + '&quot;)">应用</button><button onclick="dcAisleRotate(&quot;' + esc(a.id) + '&quot;)">⟳ 横竖切换</button>' +
      '<button class="bad" onclick="dcAisleDelete(&quot;' + esc(a.id) + '&quot;)">删除</button></div><div class="sub" style="margin-top:6px;">编辑模式下也可以直接拖动冷通道。</div>';
  }
  return html;
}
function dcAisleById(id) { return DC.layout.aisles.filter(function (x) { return x.id === id; })[0]; }
function dcAisleSet(id, nx) {
  var a = dcAisleById(id), room = dcRoomOf(DC.layout, a.room), t = { id: id, x: nx.x, z: nx.z, w: nx.w, d: nx.d, room: a.room };
  if (!(t.w >= 1 && t.d >= 1)) { dcMsg('宽和深至少 1 块', true); return; }
  if (!dcAisleFree(DC.layout, room, t, id)) { dcMsg('超出机房或跟机柜 / 别的冷通道重叠了', true); return; }
  a.x = t.x; a.z = t.z; a.w = t.w; a.d = t.d; if (nx.name !== undefined) { a.name = nx.name || a.name; }
  dcMarkDirty(); dcRefresh(false);
}
function dcAisleApply(id) {
  dcAisleSet(id, { name: byId('dcAiName').value.trim(), x: parseInt(byId('dcAiX').value, 10) || 0, z: parseInt(byId('dcAiZ').value, 10) || 0, w: parseInt(byId('dcAiW').value, 10) || 1, d: parseInt(byId('dcAiD').value, 10) || 1 });
}
function dcAisleRotate(id) { var a = dcAisleById(id); dcAisleSet(id, { x: a.x, z: a.z, w: a.d, d: a.w }); }
function dcAisleDelete(id) {
  DC.layout.aisles = DC.layout.aisles.filter(function (x) { return x.id !== id; });
  DC.sel = null; dcMarkDirty(); dcRefresh(false);
}
// 米 -> 地板块(每块 0.6m, 四舍五入)
function dcM2T(v) { return Math.round(parseFloat(v) / 0.6); }
function dcRoomHint() {
  var el = byId('dcRoomHint'); if (!el) { return; }
  var w = dcM2T(byId('dcRoomW').value), d = dcM2T(byId('dcRoomD').value);
  el.innerText = w > 0 && d > 0 ? '= ' + w + ' × ' + d + ' 块地板, 实际 ' + (w * 0.6).toFixed(1) + ' × ' + (d * 0.6).toFixed(1) + ' m' : '';
}
function dcRoomApply() {
  var room = dcRoomOf(DC.layout, DC.room);
  var w = dcM2T(byId('dcRoomW').value), d = dcM2T(byId('dcRoomD').value), h = Math.round(parseFloat(byId('dcRoomH').value) * 100);
  if (!(w >= 4 && w <= 150 && d >= 4 && d <= 150)) { dcMsg('宽和深需要在 2.4~90 米之间', true); return; }
  if (!(h >= 240 && h <= 800)) { dcMsg('层高需要在 2.4~8 米之间', true); return; }
  var tmp = { w: w, d: d, cuts: room.cuts ? JSON.parse(JSON.stringify(room.cuts)) : null };
  if (tmp.cuts) { dcCutClip(tmp); }
  var ct = dcRoomContent(room), out = function (c) { return !dcIn(tmp, c[0], c[1]); };
  var tooSmall = ct.racks.some(function (k) { return dcCells(k).some(out); }) || ct.aisles.some(function (a) { return dcAisleCells(a).some(out); }) ||
    ct.objs.some(function (o) { return !(DC_OBJ[o.kind] || {}).wall && dcObjCells(o).some(out); });
  if (tooSmall) { dcMsg('有机柜 / 冷通道 / 物件会落到机房外面, 先挪进来再缩小(或用「📦 按内容收缩」)', true); return; }
  if (dcRoomArea(tmp) < 16) { dcMsg('剩下的地板太少了', true); return; }
  if (ct.objs.some(function (o) { return o.kind === 'door' && (o.h || DC_DOOR_H) > h - 10; })) { dcMsg('有门的门洞比新层高还高, 先把门洞改低', true); return; }
  room.name = byId('dcRoomName').value.trim() || room.name; room.w = w; room.d = d; room.h = h === 300 ? 0 : h;
  if (tmp.cuts && tmp.cuts.length) { room.cuts = tmp.cuts; } else { delete room.cuts; }
  if (!room.h) { delete room.h; }
  ct.objs.forEach(function (o) { dcObjResnap(room, o); });
  dcMarkDirty(); dcRefresh(true);
}
function dcRoomAdd() {
  if (!DC.edit) { dcToggleEdit(); if (!DC.edit) { return; } }
  var name = prompt('新机房的名字', '机房 ' + (DC.layout.rooms.length + 1));
  if (name === null) { return; }
  var r = { id: dcNewId('room'), name: name.trim() || '机房 ' + (DC.layout.rooms.length + 1), w: 16, d: 12 };
  DC.layout.rooms.push(r); DC.room = r.id; DC.sel = null;
  dcMarkDirty(); dcRefresh(true);
  dcMsg('已新建「' + r.name + '」, 在右侧改尺寸, 用工具栏添加机柜 / 冷通道, 最后「💾 保存机房」');
}
function dcRoomDelete() {
  var room = dcRoomOf(DC.layout, DC.room);
  if (DC.layout.racks.some(function (k) { return k.room === room.id; })) { dcMsg('机房里还有机柜, 先删掉或挪到别的机房', true); return; }
  if (!confirm('删除机房「' + room.name + '」?' + (DC.layout.objects.some(function (o) { return o.room === room.id; }) ? '\n里面的门 / 柱子等场景物件会一起删除。' : ''))) { return; }
  DC.layout.rooms = DC.layout.rooms.filter(function (r) { return r.id !== room.id; });
  DC.layout.aisles = DC.layout.aisles.filter(function (a) { return a.room !== room.id; });
  DC.layout.objects = DC.layout.objects.filter(function (o) { return o.room !== room.id; });
  DC.room = DC.layout.rooms[0].id;
  dcMarkDirty(); dcRefresh(true);
}
function dcRackRows(k) {
  // 从上往下列出每个 U 位上是什么
  var g = DC.ctx && DC.ctx.rackObjs[k.id], place = g && g.userData.place ? g.userData.place : { list: [], overflow: [] };
  var byTop = {};
  place.list.forEach(function (x) { byTop[x.top] = { t: 'dev', x: x }; });
  (DC.layout.items || []).filter(function (it) { return it.rack === k.id; }).forEach(function (it) { byTop[it.u + it.h - 1] = { t: 'item', it: it }; });
  var rows = '', free = 0, u = k.height;
  var flushFree = function () { if (free) { rows += '<tr><td class="u">U' + (u + free) + (free > 1 ? '-' + (u + 1) : '') + '</td><td class="sub">空 ' + free + 'U</td></tr>'; free = 0; } };
  while (u >= 1) {
    var e = byTop[u];
    if (!e) { free++; u--; continue; }
    flushFree();
    if (e.t === 'dev') {
      var d = e.x.d, s = dcDevStatus(d);
      rows += '<tr class="dc-row" style="' + (DC.sel && DC.sel.t === 'dev' && DC.sel.ip === d.ip ? 'background:rgba(251,191,36,.16);' : '') + '" onclick="dcSelDev(&quot;' + esc(d.ip) + '&quot;)"><td class="u">U' + e.x.top + (e.x.h > 1 ? '-' + (e.x.top - e.x.h + 1) : '') + '</td><td><span style="color:' +
        (s === 'bad' ? '#ef4444' : s === 'off' ? '#f59e0b' : '#22c55e') + ';">●</span> ' + esc(d.name) + ' <span class="sub">' + esc(glFormText(d.form)) + '</span>' + (e.x.conflict ? ' <span style="color:#fca5a5;">U 位冲突</span>' : '') + '</td></tr>';
      u -= e.x.h;
    } else {
      var it = e.it;
      rows += '<tr class="dc-row" onclick="DC.sel={t:&quot;item&quot;,id:&quot;' + esc(it.id) + '&quot;};dcRenderSide()"><td class="u">U' + (it.u + it.h - 1) + (it.h > 1 ? '-' + it.u : '') + '</td><td><span style="color:#38bdf8;">■</span> ' + esc(it.name || DC_KIND[it.kind][0]) + ' <span class="sub">' + esc(DC_KIND[it.kind][0]) + '</span></td></tr>';
      u -= it.h;
    }
  }
  u = 0; flushFree();
  if (place.overflow.length) { rows += '<tr><td class="u">!</td><td style="color:#fca5a5;">放不下: ' + place.overflow.map(function (d) { return esc(d.name); }).join(', ') + '</td></tr>'; }
  return rows;
}
function dcSideRack(k) {
  var isRack = !k.type || k.type === 'rack';
  var html = '<h4>' + dcUnitIcon(k) + ' ' + esc(k.name) + '</h4><div class="sub">' + (isRack ? k.height + 'U 机柜' : dcUnitName(k)) +
    ' · 位置 (' + k.x + ', ' + k.z + ') · 朝向 ' + ({ 0: '南', 90: '东', 180: '北', 270: '西' }[k.rot || 0]) + (k.auto ? ' · <span style="color:#fbbf24;">自动生成</span>' : '') + '</div>' +
    '<div class="btns"><button onclick="dcFlyToRack(dcRackById(&quot;' + esc(k.id) + '&quot;))">👁 飞过去</button>' +
    (isRack ? '<button onclick="dcRackRear(&quot;' + esc(k.id) + '&quot;)" title="打开后门, 镜头转到背面看交换机的电源 / 风扇">' + (DC.openRear === k.id ? '🚪 关后门' : '🚪 开后门 · 看背面') + '</button>' : '') + '</div>';
  if (DC.edit) {
    html += '<hr><div class="grid2"><div><label>名称</label><input id="dcRkName" value="' + esc(k.name) + '"></div>' +
      '<div><label>高度(U)</label><input id="dcRkH" type="number" min="6" max="60" value="' + k.height + '"' + (isRack ? '' : ' disabled') + '></div>' +
      '<div><label>侧板颜色</label><input id="dcRkColor" type="color" value="' + esc(k.color || '#222a36') + '"></div>' +
      '<div><label>所在机房</label><select id="dcRkRoom">' + DC.layout.rooms.map(function (r) { return '<option value="' + esc(r.id) + '"' + (r.id === k.room ? ' selected' : '') + '>' + esc(r.name) + '</option>'; }).join('') + '</select></div></div>' +
      '<div class="btns"><button class="pri" onclick="dcRackApply(&quot;' + esc(k.id) + '&quot;)">应用</button><button onclick="dcRackRotate(&quot;' + esc(k.id) + '&quot;)">⟳ 旋转 90°</button>' +
      '<button class="bad" onclick="dcRackDelete(&quot;' + esc(k.id) + '&quot;)">删除</button></div>' +
      (isRack && DC_devsIn(k).some(function (d) { return !d.manual; }) ? '<div class="sub" style="margin-top:6px;">这个机柜里有按 sysLocation 自动归入的交换机; 改名保存后它们会固定到新名字。</div>' : '');
  }
  if (!isRack) { return html; }
  html += '<hr><div class="sub" style="margin-bottom:4px;">U 位(从上往下, 点击选中)</div><table>' + dcRackRows(k) + '</table>';
  if (DC.edit) {
    var kinds = Object.keys(DC_KIND).filter(function (x) { return x !== 'ac'; });
    html += '<hr><b>➕ 添加设备</b><div class="grid2"><div><label>类型</label><select id="dcNiKind" onchange="dcNiKind()">' + kinds.map(function (x) { return '<option value="' + x + '">' + DC_KIND[x][0] + '</option>'; }).join('') + '</select></div>' +
      '<div><label>名称</label><input id="dcNiName" placeholder="如 Web-01"></div>' +
      '<div><label>最下面的 U 号</label><input id="dcNiU" type="number" min="1" max="' + k.height + '" value="' + dcFirstFree(k, 2) + '"></div>' +
      '<div><label>高度(U)</label><input id="dcNiH" type="number" min="1" max="20" value="2"></div>' +
      '<div><label>型号</label><input id="dcNiModel"></div><div><label>管理地址</label><input id="dcNiIP"></div></div>' +
      '<div class="btns"><button class="pri" onclick="dcItemAdd(&quot;' + esc(k.id) + '&quot;)">添加到这个机柜</button></div>';
    var others = DC.devices.filter(function (d) { return d.rack !== k.name; }).sort(function (a, b) { return naturalCmp(a.name, b.name); });
    html += '<hr><b>⬆ 放入被监控的交换机</b><div class="sub">立即保存(跟 2D 机柜视图共用位置)</div><div class="grid2"><div style="grid-column:1/-1;"><label>交换机</label><select id="dcMvDev">' +
      others.map(function (d) { return '<option value="' + esc(d.ip) + '">' + esc(d.name) + ' (' + esc(d.ip) + ') · 现在在 ' + esc(d.rack) + '</option>'; }).join('') + '</select></div>' +
      '<div><label>最上面的 U 号(0 = 自动)</label><input id="dcMvU" type="number" min="0" max="' + k.height + '" value="0"></div></div>' +
      '<div class="btns"><button class="pri" onclick="dcMoveDev(byId(&quot;dcMvDev&quot;).value, &quot;' + esc(k.id) + '&quot;, parseInt(byId(&quot;dcMvU&quot;).value,10)||0)"' + (others.length ? '' : ' disabled') + '>放进来</button></div>';
    if (k.auto) { html += '<div class="sub" style="margin-top:6px;">⚠️ 自动生成的机柜要先「💾 保存机房」才能往里加非监控设备。</div>'; }
  }
  return html;
}
function dcNiKind() { var k = byId('dcNiKind').value; byId('dcNiH').value = DC_KIND[k][1]; }
function dcFirstFree(k, h) {
  var occ = {};
  var g = DC.ctx && DC.ctx.rackObjs[k.id];
  if (g && g.userData.place) { g.userData.place.list.forEach(function (x) { for (var u = x.top - x.h + 1; u <= x.top; u++) { occ[u] = true; } }); }
  (DC.layout.items || []).forEach(function (it) { if (it.rack === k.id) { for (var u = it.u; u < it.u + it.h; u++) { occ[u] = true; } } });
  for (var b = 1; b + h - 1 <= k.height; b++) { var ok = true; for (var u2 = b; u2 < b + h; u2++) { if (occ[u2]) { ok = false; break; } } if (ok) { return b; } }
  return 1;
}
function dcRackApply(id) {
  var k = dcRackById(id);
  var name = byId('dcRkName').value.trim(), h = parseInt(byId('dcRkH').value, 10), room = byId('dcRkRoom').value;
  if (!name) { dcMsg('名称不能为空', true); return; }
  if (DC.layout.racks.some(function (o) { return o.id !== id && o.name === name; })) { dcMsg('已经有叫「' + name + '」的机柜了', true); return; }
  if ((!k.type || k.type === 'rack') && !(h >= 6 && h <= 60)) { dcMsg('高度需要在 6~60U', true); return; }
  if ((DC.layout.items || []).some(function (it) { return it.rack === id && it.u + it.h - 1 > h; })) { dcMsg('有设备超出新的高度, 先挪一下', true); return; }
  if (room !== k.room) {
    var r = dcRoomOf(DC.layout, room), test = { id: k.id, x: k.x, z: k.z, rot: k.rot, room: room };
    if (!dcFree(DC.layout, r, test, k.id)) { dcMsg('目标机房同一位置已经有东西了, 先在当前机房挪个空位', true); return; }
  }
  if (name !== k.name) {
    var orig = k.name;
    // 连续改名时记最早的名字
    Object.keys(DC.renames).forEach(function (o) { if (DC.renames[o] === orig) { orig = o; } });
    DC.renames[orig] = name;
    DC.devices.forEach(function (d) { if (d.rack === k.name) { d.rack = name; } });
  }
  k.name = name; if (!k.type || k.type === 'rack') { k.height = h; }
  k.color = byId('dcRkColor').value; k.room = room;
  if (room !== DC.room) { DC.room = room; }
  dcMarkDirty(); dcRefresh(false);
}
function dcRackRotate(id) {
  var k = dcRackById(id), old = k.rot || 0;
  k.rot = (old + 90) % 360;
  if (!dcFree(DC.layout, dcRoomOf(DC.layout, k.room), k, k.id)) { k.rot = old; dcMsg('转过去会跟别的机柜重叠或超出机房', true); return; }
  dcMarkDirty(); dcRefresh(false);
}
function dcRackDelete(id) {
  var k = dcRackById(id);
  var devs = DC_devsIn(k), items = (DC.layout.items || []).filter(function (it) { return it.rack === id; });
  if (!confirm('删除「' + k.name + '」?' + (items.length ? '\n里面的 ' + items.length + ' 台非监控设备会一起删除。' : '') + (devs.length ? '\n里面的 ' + devs.length + ' 台被监控交换机会退回按 sysLocation 自动归类。' : ''))) { return; }
  DC.layout.racks = DC.layout.racks.filter(function (o) { return o.id !== id; });
  DC.layout.items = (DC.layout.items || []).filter(function (it) { return it.rack !== id; });
  var jobs = devs.filter(function (d) { return d.manual; }).map(function (d) { return authedApi('/api/racks/assign', { ip: d.ip, rack: '', u: 0 }); });
  DC.sel = null;
  dcMarkDirty();
  Promise.all(jobs).then(function () { return jobs.length ? dcLoad(false) : dcRefresh(false); }).catch(function (e) { dcMsg(String(e), true); });
}
function dcItemAdd(rackId) {
  var k = dcRackById(rackId);
  if (k.auto) { dcMsg('先「💾 保存机房」把这个自动生成的机柜固定下来', true); return; }
  var it = { id: dcNewId('i'), rack: rackId, kind: byId('dcNiKind').value, name: byId('dcNiName').value.trim(), u: parseInt(byId('dcNiU').value, 10) || 1,
    h: parseInt(byId('dcNiH').value, 10) || 1, model: byId('dcNiModel').value.trim(), ip: byId('dcNiIP').value.trim(), status: 'ok' };
  if (!it.name) { it.name = DC_KIND[it.kind][0]; }
  var err = dcItemCheck(it);
  if (err) { dcMsg(err, true); return; }
  DC.layout.items = DC.layout.items || [];
  DC.layout.items.push(it);
  dcMarkDirty(); dcRefresh(false);
  dcMsg('已添加「' + it.name + '」, 记得保存');
}
// 检查 U 位: 不越界, 不跟别的非监控设备、被监控交换机重叠
function dcItemCheck(it) {
  var k = dcRackById(it.rack);
  if (it.h < 1 || it.h > 20) { return '高度需要在 1~20U'; }
  if (it.u < 1 || it.u + it.h - 1 > k.height) { return 'U 位超出机柜范围(1~' + k.height + ')'; }
  var clash = null;
  (DC.layout.items || []).forEach(function (o) { if (o.rack === it.rack && o.id !== it.id && o.u <= it.u + it.h - 1 && it.u <= o.u + o.h - 1) { clash = o.name; } });
  var g = DC.ctx && DC.ctx.rackObjs[it.rack];
  if (g && g.userData.place) {
    g.userData.place.list.forEach(function (x) {
      var b = x.top - x.h + 1;
      // 自动排列的交换机会自动让开, 只检查手工指定了 U 号的
      if (x.d.manual && x.d.u > 0 && b <= it.u + it.h - 1 && it.u <= x.top) { clash = x.d.name; }
    });
  }
  return clash ? 'U' + it.u + '~U' + (it.u + it.h - 1) + ' 跟「' + clash + '」重叠了' : '';
}
function dcSideItem(it) {
  var k = dcRackById(it.rack);
  var html = '<h4>■ ' + esc(it.name || DC_KIND[it.kind][0]) + '</h4><div class="sub">' + esc(DC_KIND[it.kind][0]) + ' · ' + esc(k ? k.name : '') + ' · U' + it.u + '~U' + (it.u + it.h - 1) + '</div>';
  if (!DC.edit) {
    return html + (it.model ? '<div>型号: ' + esc(it.model) + '</div>' : '') + (it.ip ? '<div>地址: ' + esc(it.ip) + '</div>' : '') +
      '<div>状态: ' + (DC_STATUS[it.status || ''] || DC_STATUS[''])[1] + '</div>' + (it.note ? '<div class="sub">' + esc(it.note) + '</div>' : '') +
      '<div class="sub" style="margin-top:8px;">非监控设备, 状态是手工标记的</div>';
  }
  var kinds = Object.keys(DC_KIND).filter(function (x) { return x !== 'ac'; });
  html += '<div class="grid2"><div><label>类型</label><select id="dcItKind">' + kinds.map(function (x) { return '<option value="' + x + '"' + (x === it.kind ? ' selected' : '') + '>' + DC_KIND[x][0] + '</option>'; }).join('') + '</select></div>' +
    '<div><label>名称</label><input id="dcItName" value="' + esc(it.name) + '"></div>' +
    '<div><label>最下面的 U 号</label><input id="dcItU" type="number" min="1" max="' + (k ? k.height : 60) + '" value="' + it.u + '"></div>' +
    '<div><label>高度(U)</label><input id="dcItH" type="number" min="1" max="20" value="' + it.h + '"></div>' +
    '<div><label>型号</label><input id="dcItModel" value="' + esc(it.model || '') + '"></div><div><label>管理地址</label><input id="dcItIP" value="' + esc(it.ip || '') + '"></div>' +
    '<div><label>状态灯</label><select id="dcItStatus">' + ['ok', 'warn', 'bad', 'off', ''].map(function (s) { return '<option value="' + s + '"' + (s === (it.status || '') ? ' selected' : '') + '>' + DC_STATUS[s][1] + '</option>'; }).join('') + '</select></div>' +
    '<div><label>移到机柜</label><select id="dcItRack">' + DC.layout.racks.filter(function (o) { return (!o.type || o.type === 'rack') && !o.auto; }).map(function (o) { return '<option value="' + esc(o.id) + '"' + (o.id === it.rack ? ' selected' : '') + '>' + esc(o.name) + '</option>'; }).join('') + '</select></div>' +
    '<div style="grid-column:1/-1;"><label>备注</label><input id="dcItNote" value="' + esc(it.note || '') + '"></div></div>' +
    '<div class="btns"><button class="pri" onclick="dcItemApply(&quot;' + esc(it.id) + '&quot;)">应用</button><button class="bad" onclick="dcItemDelete(&quot;' + esc(it.id) + '&quot;)">删除</button></div>';
  return html;
}
function dcItemApply(id) {
  var it = (DC.layout.items || []).filter(function (x) { return x.id === id; })[0];
  var nx = { id: id, rack: byId('dcItRack').value, kind: byId('dcItKind').value, name: byId('dcItName').value.trim(), u: parseInt(byId('dcItU').value, 10) || 1,
    h: parseInt(byId('dcItH').value, 10) || 1, model: byId('dcItModel').value.trim(), ip: byId('dcItIP').value.trim(), status: byId('dcItStatus').value, note: byId('dcItNote').value.trim() };
  var err = dcItemCheck(nx);
  if (err) { dcMsg(err, true); return; }
  Object.keys(nx).forEach(function (key) { it[key] = nx[key]; });
  dcMarkDirty(); dcRefresh(false);
}
function dcItemDelete(id) {
  DC.layout.items = (DC.layout.items || []).filter(function (x) { return x.id !== id; });
  DC.sel = null; dcMarkDirty(); dcRefresh(false);
}
function dcSideDev(d) {
  var g = DC.ctx && DC.ctx.devObjs[d.ip], sl = g && g.userData.slot;
  var html = '<h4>' + (d.alive ? (d.alerts ? '🔴' : '🟢') : '🟠') + ' ' + esc(d.name) + '</h4><div class="sub">' + esc(d.ip) + ' · ' + esc(pnlBrand(d.brand)[0]) + (d.model ? ' ' + esc(d.model) : '') + '</div>' +
    '<div style="margin-top:6px;">' + esc(glFormText(d.form)) + '<div class="sub">识别依据: ' + esc(d.form ? d.form.source : '-') + '</div></div>' +
    '<div style="margin-top:6px;">机柜: <b>' + esc(d.rack) + '</b>' + (sl ? ' · U' + sl.top + (sl.h > 1 ? '~U' + (sl.top - sl.h + 1) : '') : '') + (d.manual && d.u ? ' (手工指定)' : ' (自动排列)') + '</div>' +
    '<div>CPU ' + d.cpu + '% · 内存 ' + (d.mem > 0 ? d.mem + '%' : '-') + ' · 告警 ' + d.alerts + '</div>' +
    '<div class="btns"><button class="pri" onclick="openModal(&quot;' + esc(d.ip) + '&quot;,9)">🗄️ 打开面板</button><button onclick="openModal(&quot;' + esc(d.ip) + '&quot;)">设备详情</button></div>';
  if (DC.edit) {
    var racks = DC.layout.racks.filter(function (k) { return !k.type || k.type === 'rack'; }).sort(function (a, b) { return naturalCmp(a.name, b.name); });
    html += '<hr><b>位置</b><div class="grid2"><div><label>机柜</label><select id="dcDvRack">' + racks.map(function (k) { return '<option value="' + esc(k.id) + '"' + (k.name === d.rack ? ' selected' : '') + '>' + esc(k.name) + '</option>'; }).join('') + '</select></div>' +
      '<div><label>最上面的 U 号(0 = 自动)</label><input id="dcDvU" type="number" min="0" max="60" value="' + (d.manual ? d.u || 0 : 0) + '"></div></div>' +
      '<div class="btns"><button class="pri" onclick="dcMoveDev(&quot;' + esc(d.ip) + '&quot;, byId(&quot;dcDvRack&quot;).value, parseInt(byId(&quot;dcDvU&quot;).value,10)||0)">保存位置</button>' +
      (d.manual ? '<button onclick="dcUnassign(&quot;' + esc(d.ip) + '&quot;)">恢复按 sysLocation 自动归类</button>' : '') + '</div>' +
      '<hr><b>设备形态</b><div style="color:#dbe4f3;">' + pnlFormEditorHTML(d, 'dcf') + '</div><div class="btns"><button class="pri" onclick="dcSaveForm(&quot;' + esc(d.ip) + '&quot;)">保存形态</button></div>' +
      '<div class="sub">堆叠被识别成一台、框式被画成 1U、2U 盒式被画成 1U 时在这里改, 3D 拓扑和机柜视图都会按这里的来。</div>';
  }
  return html;
}
function dcMoveDev(ip, rackId, u) {
  var k = dcRackById(rackId);
  if (!k) { return; }
  if (u < 0 || u > k.height) { dcMsg('U 号需要在 0~' + k.height, true); return; }
  authedApi('/api/racks/assign', { ip: ip, rack: k.name, u: u }).then(function () {
    DC.sel = { t: 'dev', ip: ip };
    return dcLoad(false);
  }).then(function () { dcMsg('✅ 已保存位置'); }).catch(function (e) { dcMsg('❌ ' + e, true); });
}
function dcUnassign(ip) {
  authedApi('/api/racks/assign', { ip: ip, rack: '', u: 0 }).then(function () { return dcLoad(false); }).then(function () { dcMsg('✅ 已恢复自动归类'); }).catch(function (e) { dcMsg('❌ ' + e, true); });
}
function dcSaveForm(ip) {
  var d = DC.devices.filter(function (x) { return x.ip === ip; })[0];
  var body = pnlFormBody(d, 'dcf');
  if (!body.kind && !(d.form && d.form.manual)) { dcMsg('本来就是自动识别'); return; }
  authedApi('/api/datacenter/save', { form_ip: ip, form: body }).then(function () { return dcLoad(false); })
    .then(function () { dcMsg('✅ 形态已保存'); }).catch(function (e) { dcMsg('❌ ' + e, true); });
}
/* ---------- 机柜门 / 交换机信息卡 ---------- */
function dcToggleRear() {
  DC.rearAll = !DC.rearAll;
  byId('dcRearBtn').innerText = DC.rearAll ? '🚪 全部关后门' : '🚪 全部开后门';
  dcSyncDoors();
}
// 单个机柜开 / 关后门, 开的时候镜头转到机柜背面
function dcRackRear(id) {
  DC.openRear = DC.openRear === id ? null : id;
  dcSyncDoors(); dcRenderSide();
  var c = DC.ctx, g = c && c.rackObjs[id];
  if (!g || DC.openRear !== id) { return; }
  var dir = new c.st.THREE.Vector3(0, 0, -1).applyAxisAngle(new c.st.THREE.Vector3(0, 1, 0), g.rotation.y), p = g.position, h = g.userData.height || 200;
  c.st.flyTo({ x: p.x + dir.x * 330 + dir.z * 90, y: h * 0.75, z: p.z + dir.z * 330 - dir.x * 90 }, { x: p.x, y: h * 0.45, z: p.z });
}
function dcToggleDoors() {
  DC.doorsAll = !DC.doorsAll;
  byId('dcDoorBtn').innerText = DC.doorsAll ? '🚪 全部关门' : '🚪 全部开门';
  dcSyncDoors();
}
// 选中的机柜(或选中设备所在的机柜)开门, 其他关门; 选中的交换机拉出来
function dcSyncDoors() {
  var c = DC.ctx;
  if (!c) { return; }
  var open = null, sel = DC.sel;
  if (sel && sel.t === 'rack') { open = sel.id; }
  else if (sel && sel.t === 'dev' && c.devObjs[sel.ip]) { open = c.devObjs[sel.ip].userData.rack.id; }
  else if (sel && sel.t === 'item' && c.itemObjs[sel.id]) { open = c.itemObjs[sel.id].userData.rack.id; }
  DC.openRack = open;
  Object.keys(c.rackObjs).forEach(function (id) {
    var g = c.rackObjs[id];
    if (g.userData.doors) { g.userData.doorT = DC.doorsAll || id === open ? 1 : 0; }
    if (g.userData.rears) { g.userData.rearT = DC.rearAll || id === DC.openRear ? 1 : 0; }
  });
  Object.keys(c.devObjs).forEach(function (ip) { c.devObjs[ip].userData.pullT = sel && sel.t === 'dev' && sel.ip === ip ? 1 : 0; });
  c.st.poke();
}
function dcCloseCard() { DC.card = null; var el = byId('dcCard'); if (el) { el.remove(); } }
function dcOpenCard(ip, e) {
  var wrap = byId('dcWrap');
  if (!wrap) { return; }
  var card = byId('dcCard');
  if (!card) {
    card = document.createElement('div');
    card.id = 'dcCard'; card.className = 'dc-card pvt-dark';
    wrap.appendChild(card);
    var tip = byId('dcTip');
    card.addEventListener('mousemove', function (ev) {
      var el = ev.target.closest ? ev.target.closest('[data-k]') : null;
      if (!el) { tip.style.display = 'none'; return; }
      tip.innerHTML = pnlTipHTML(el.getAttribute('data-k')); tip.style.display = 'block';
      var r = wrap.getBoundingClientRect();
      tip.style.left = Math.max(0, Math.min(ev.clientX - r.left + 14, r.width - tip.offsetWidth - 4)) + 'px';
      tip.style.top = Math.max(0, ev.clientY - r.top + 14) + 'px';
    });
    card.addEventListener('mouseleave', function () { tip.style.display = 'none'; });
    card.addEventListener('click', function (ev) {
      var el = ev.target.closest ? ev.target.closest('[data-k]') : null;
      if (!el || !DC.card) { return; }
      DC.card.port = el.getAttribute('data-k');
      var box = byId('dcCardPort');
      if (box) { box.style.display = ''; box.innerHTML = pnlTipHTML(DC.card.port); }
    });
    ['pointerdown', 'wheel'].forEach(function (t) { card.addEventListener(t, function (ev) { ev.stopPropagation(); }); });
  }
  DC.card = { ip: ip, port: '' };
  dcFillCard(ip);
  floatBox(card, wrap, 'dcCard');
  var r = wrap.getBoundingClientRect(), w = card.offsetWidth, h = card.offsetHeight, maxX = r.width - 350 - w;
  var x = e.clientX - r.left + 24;
  if (x > maxX) { x = e.clientX - r.left - w - 24; }
  card.style.left = Math.max(8, Math.min(x, Math.max(8, maxX))) + 'px';
  card.style.top = Math.max(8, Math.min(e.clientY - r.top - 60, r.height - h - 8)) + 'px';
}
function dcFillCard(ip) {
  var card = byId('dcCard'), d = DC.devices.filter(function (x) { return x.ip === ip; })[0];
  if (!card) { return; }
  if (!d) { dcCloseCard(); return; }
  var g = DC.ctx && DC.ctx.devObjs[ip], sl = g && g.userData.slot;
  var ports = d.ports || [], up = ports.filter(function (p) { return p.s === 'UP'; }).length, prob = ports.filter(pnlProblem);
  var ups = ports.filter(function (p) { return p.up; }).map(function (p) { return p.n + (p.pe ? ' → ' + p.pe : ''); });
  var o = pvOpts(), U = pnlUnits(d), isCh = d.form && d.form.kind === 'chassis';
  o.uh = isCh ? Math.max(24, Math.min(40, 320 / U)) : 44; o.maxS = 30; o.nums = true;
  PNL.ports = PNL.ports || {};
  var chip = function (t) { return '<span>' + t + '</span>'; };
  var html = '<span class="x" onclick="dcCloseCard()">✕</span><h4>' + (d.alive ? (d.alerts ? '🔴' : '🟢') : '🟠') + ' ' + esc(d.name) + '</h4>' +
    '<div class="sub">' + esc(d.ip) + ' · ' + esc(pnlBrand(d.brand)[0]) + (d.model ? ' ' + esc(d.model) : '') + ' · ' + esc(glFormText(d.form)) + '</div>' +
    '<div class="chips">' + chip(d.alive ? '在线' : '<b style="color:#fbbf24;">离线</b>') + (d.alerts ? chip('<b style="color:#fca5a5;">' + d.alerts + ' 条告警</b>') : '') +
    (d.alive ? chip('CPU ' + d.cpu + '%') + chip('内存 ' + (d.mem > 0 ? d.mem + '%' : '-')) + (d.temp && d.temp !== '-' ? chip('温度 ' + esc(d.temp)) : '') : '') +
    chip('端口 ' + up + '/' + ports.length + ' UP') + (prob.length ? chip('<b style="color:#fca5a5;">' + prob.length + ' 个问题口</b>') : '') +
    chip(esc(d.rack) + (sl ? ' · U' + (sl.top - sl.h + 1) + (sl.h > 1 ? '~' + sl.top : '') : '')) + '</div>' +
    (d.note ? '<div class="note">📝 ' + esc(d.note) + '</div>' : '') +
    '<svg class="pv-dsvg" viewBox="0 -8 ' + PV_W + ' ' + (U * o.uh + 16) + '">' + pvDevSVG(d, 0, 0, U * o.uh, o) + '</svg>' +
    '<div class="sub">悬停端口看详情, 点端口固定显示</div>' +
    (ups.length ? '<div class="sub" style="margin-top:4px;">上联: ' + ups.slice(0, 4).map(esc).join(' · ') + (ups.length > 4 ? ' …' : '') + '</div>' : '') +
    (prob.length ? '<div class="sub" style="margin-top:2px;color:#fca5a5;">问题口: ' + prob.slice(0, 6).map(function (p) { return esc(p.n); }).join(', ') + (prob.length > 6 ? ' …' : '') + '</div>' : '') +
    '<div class="port" id="dcCardPort" style="' + (DC.card && DC.card.port && PNL.ports[DC.card.port] ? '' : 'display:none;') + '">' + (DC.card && DC.card.port ? pnlTipHTML(DC.card.port) : '') + '</div>' +
    '<div class="btns"><button class="pri" onclick="openModal(&quot;' + esc(ip) + '&quot;)">📋 设备详情</button>' +
    '<button onclick="openModal(&quot;' + esc(ip) + '&quot;,9)">🔌 大图面板</button>' +
    '<button onclick="dcFlyToDev(&quot;' + esc(ip) + '&quot;)">🎯 镜头对准</button>' +
    '<button onclick="DC.sel={t:&quot;rack&quot;,id:DC.ctx.devObjs[&quot;' + esc(ip) + '&quot;].userData.rack.id};dcCloseCard();dcRenderSide()">🗄️ 看整个机柜</button></div>';
  card.innerHTML = html;
  floatBox(card, byId('dcWrap'), 'dcCard');
}
// 镜头飞到交换机正前方
function dcFlyToDev(ip) {
  var c = DC.ctx, sw = c && c.devObjs[ip];
  if (!sw) { return; }
  var THREE = c.st.THREE, p = new THREE.Vector3();
  sw.updateMatrixWorld(); sw.getWorldPosition(p);
  var g = sw.userData.rack && c.rackObjs[sw.userData.rack.id], dir = new THREE.Vector3(0, 0, 1).applyAxisAngle(new THREE.Vector3(0, 1, 0), g ? g.rotation.y : 0);
  var hh = (sw.userData.slot ? sw.userData.slot.h : 1) * DC_U / 2;
  c.st.flyTo({ x: p.x + dir.x * 150, y: p.y + hh + 30, z: p.z + dir.z * 150 }, { x: p.x, y: p.y + hh, z: p.z }, 800);
}
// 从列表里选中交换机: 开门、拉出来、弹信息卡(放在画面左侧)
function dcSelDev(ip) {
  DC.sel = { t: 'dev', ip: ip };
  dcRenderSide();
  if (!DC.edit) {
    var r = byId('dcWrap').getBoundingClientRect();
    dcOpenCard(ip, { clientX: r.left + 20, clientY: r.top + 120 });
  }
}

/* ================= v13: 入场动画 / 顶灯 / 巡检漫游 / 工具条 ================= */
// 入场: 机柜从地板里"升"起来, 从前往后依次出现
function dcRiseStart(c) {
  if (!c || (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches)) { return; }
  var items = [];
  Object.keys(c.rackObjs).forEach(function (id) {
    var g = c.rackObjs[id], k = g.userData.rack || {};
    items.push({ g: g, delay: ((k.x || 0) + (k.z || 0) * 1.3) * 38 });
    g.scale.y = 0.001;
  });
  Object.keys(c.aisleObjs || {}).forEach(function (id) {
    var g = c.aisleObjs[id], a = g.userData.aisle || {};
    items.push({ g: g, delay: 300 + ((a.x || 0) + (a.z || 0)) * 38 });
    g.scale.y = 0.001;
  });
  c.rise = { t0: performance.now(), items: items, dur: 620 };
}
function dcRiseStep(c) {
  var r = c.rise;
  if (!r) { return false; }
  var now = performance.now(), done = true;
  r.items.forEach(function (x) {
    var k = Math.max(0, Math.min(1, (now - r.t0 - x.delay) / r.dur));
    if (k < 1) { done = false; }
    x.g.scale.y = Math.max(0.001, glEaseBack(k));
  });
  if (done) { r.items.forEach(function (x) { x.g.scale.y = 1; }); c.rise = null; }
  return true;
}

// 顶灯: 机房上方一排排 LED 灯板(自发光, 泛光下会亮), 看起来更像真实机房, 也不会挡住往下看的视线
function dcCeilingLights(c, room, RW, RD) {
  // 灯板用 InstancedMesh 一次画完: 大机房(90 米见方)也只有一个绘制调用
  var THREE = c.st.THREE, pos = [];
  for (var z = 2; z < room.d; z += 4) { for (var x = 1.5; x < room.w - 0.5; x += 3) { if (dcIn(room, Math.floor(x), z) && dcIn(room, Math.floor(x - 0.9), z) && dcIn(room, Math.floor(x + 0.85), z)) { pos.push([c.ox + x * DC_TILE, c.oz + z * DC_TILE]); } } }
  var g = new THREE.Group();
  if (pos.length) {
    var lit = new THREE.MeshBasicMaterial({ color: new THREE.Color(0xeaf4ff).multiplyScalar(2.6) });
    var im = dcNoRay(new THREE.InstancedMesh(new THREE.BoxGeometry(DC_TILE * 1.7, 1, 8), lit, pos.length)), m = new THREE.Matrix4();
    pos.forEach(function (p, i) { m.makeTranslation(p[0], (c.wallH || 300) - 3.8, p[1]); im.setMatrixAt(i, m); });
    im.instanceMatrix.needsUpdate = true;
    g.add(im);
  }
  g.userData.noBox = true;
  g.visible = c.st.camera.position.y < (c.wallH || 300) + 40;
  c.lights = g;
  c.world.add(g);
}

// 机房里的浮动工具条: 巡检漫游 / 视角 / 画质 / 全屏; 快捷键 R 回到斜视, T 漫游, F 全屏
function dcMountBar() {
  var wrap = byId('dcWrap');
  if (!wrap || !DC.ctx) { return; }
  var bar = glOverlayBar(wrap, 'dc');
  bar.innerHTML = '';
  DC.tourBtn = glBarButton(bar, '🎬 <span>巡检漫游</span>', '镜头依次走到每个机柜(有告警的优先), 自动开门、拉出有告警的交换机; 动一下鼠标就停。快捷键 T', function () { dcTourToggle(); });
  glBarButton(bar, '⟲ <span>视角</span>', '回到斜视全景; 快捷键 R', function () { dcTourStop(); dcView('iso'); });
  glQualitySelect(bar, function () { var room = DC.room; dcClose(); DC.room = room; dcOpen(); }, DC.ctx.st);
  DC.menuBtn = glBarButton(bar, '☰ <span>菜单</span>', '全屏时展开 / 收起工具条和右侧面板', function () { DC.fsMenu = !DC.fsMenu; dcFsSync(); });
  fsButton(wrap, bar, { cls: 'fs-btn glass' });
  fsHotkeyZone(wrap);
  if (!DC.fsHooked) { DC.fsHooked = true; FS.listeners.add(function () { DC.fsMenu = false; dcFsSync(); }); }
  dcFsSync();
  var cv = DC.ctx.st.renderer.domElement;
  ['pointerdown', 'wheel'].forEach(function (ev) { cv.addEventListener(ev, function () { dcTourStop(); }, { passive: true }); });
  if (!DC.keys) {
    DC.keys = true;
    document.addEventListener('keydown', function (e) {
      if (!DC.ctx || byId('rackModal').style.display === 'none' || PNL.rack.view !== '3d') { return; }
      var t = e.target;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT')) { return; }
      if (e.key === 'r' || e.key === 'R') { dcTourStop(); dcView('iso'); }
      else if (e.key === 't' || e.key === 'T') { dcTourToggle(); }
      else if (e.key === 'Escape' && DC.tour) { e.stopPropagation(); dcTourStop(); }
    }, true);
  }
}
function dcTourToggle() { if (DC.tour) { dcTourStop(); } else { dcTourStart(); } }
function dcTourStart() {
  if (!DC.ctx || DC.edit) { return; }
  var rank = { bad: 0, off: 1, warn: 1, ok: 2, '': 3 };
  var list = DC.layout.racks.filter(function (k) { return k.room === DC.room && (!k.type || k.type === 'rack'); });
  list.sort(function (a, b) { return rank[dcRackStatus(a)] - rank[dcRackStatus(b)] || naturalCmp(a.name, b.name); });
  if (!list.length) { return; }
  DC.tour = { list: list, i: 0, timers: [] };
  if (DC.tourBtn) { DC.tourBtn.classList.add('on'); DC.tourBtn.innerHTML = '⏹ <span>停止漫游</span>'; }
  var step = function () {
    if (!DC.tour || !DC.ctx) { return; }
    var k = DC.tour.list[DC.tour.i % DC.tour.list.length];
    DC.tour.i++;
    dcCloseCard();
    DC.sel = { t: 'rack', id: k.id };
    dcRenderSide();
    var g = DC.ctx.rackObjs[k.id];
    DC.ctx.selBox.visible = false;
    dcFlyToRack(k);
    // 柜里有告警 / 离线的交换机: 门开了之后把它拉出来, 弹信息卡
    var bad = DC_devsIn(k).filter(function (d) { return !d.alive || d.alerts > 0; })[0];
    if (bad) {
      DC.tour.timers.push(setTimeout(function () {
        if (!DC.tour || !DC.ctx || !DC.ctx.devObjs[bad.ip]) { return; }
        dcSelDev(bad.ip);
        DC.ctx.selBox.setColor(0xfbbf24); DC.ctx.selBox.fit(DC.ctx.devObjs[bad.ip], 1.5);
        dcFlyToDev(bad.ip);
      }, 1900));
    }
    DC.tour.timers.push(setTimeout(step, bad ? 8500 : 6000));
  };
  step();
}
function dcTourStop() {
  if (!DC.tour) { return; }
  DC.tour.timers.forEach(clearTimeout);
  DC.tour = null;
  if (DC.ctx && (!DC.sel || DC.sel.t !== 'dev')) { DC.ctx.selBox.visible = false; }
  if (DC.tourBtn) { DC.tourBtn.classList.remove('on'); DC.tourBtn.innerHTML = '🎬 <span>巡检漫游</span>'; }
}
