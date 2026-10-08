/*
 * 3D 机房的场景物件和设备模型(v14)。dc3d.js 管布局和交互, 这里只管"长什么样"。
 *
 *   - 场景物件(layout.objects): 机房门、柱子、运维操作台、墙挂监控大屏、灭火器箱、气体灭火钢瓶组、摄像头。
 *     门 / 大屏 / 摄像头贴墙放(放置和拖动时自动吸到最近的那面墙), 柱子 / 操作台 / 钢瓶占地板。
 *   - 落地设备: 精密空调 / UPS 柜 / 配电柜 / 电池柜, 带门板、把手、格栅、显示屏、仪表。
 *   - 柜内的非监控设备: 机箱 + 前面板 + 挂耳 + 把手, 配线架前面垂着跳线。
 *   - 单位是厘米(一块地板 60cm)。物件的类型和占地跟后端 datacenter.go 的 dcObjectKinds 保持一致。
 */
const DC_OBJ = {
  door: { name: '机房门', icon: '🚪', w: 2, d: 1, solid: false, wall: true },
  pillar: { name: '柱子', icon: '🏛', w: 1, d: 1, solid: true, wall: false },
  desk: { name: '运维操作台', icon: '🖥', w: 3, d: 2, solid: true, wall: false },
  screen: { name: '监控大屏', icon: '📺', w: 4, d: 1, solid: false, wall: true },
  extinguisher: { name: '灭火器箱', icon: '🧯', w: 1, d: 1, solid: false, wall: false },
  gas: { name: '气体灭火钢瓶', icon: '🛢', w: 2, d: 1, solid: true, wall: false },
  camera: { name: '摄像头', icon: '📷', w: 1, d: 1, solid: false, wall: true }
};
// 落地设备(占机柜的位置, 里面不能放东西): 名称, 图标, 自动命名前缀
const DC_UNIT = { ac: ['精密空调', '❄', '空调-'], ups: ['UPS 柜', '🔋', 'UPS-'], pdc: ['配电柜', '⚡', '配电柜-'], battery: ['电池柜', '🔋', '电池柜-'] };
function dcIsUnit(k) { return !!(k && k.type && k.type !== 'rack'); }
function dcUnitName(k) { return (DC_UNIT[k.type] || ['落地设备'])[0]; }
function dcUnitIcon(k) { return dcIsUnit(k) ? (DC_UNIT[k.type] || ['', '▪'])[1] : '🗄️'; }

/* ---------- 场景物件的占地 / 贴墙 ---------- */
function dcObjFoot(o) {
  const k = DC_OBJ[o.kind] || DC_OBJ.pillar, r = o.rot || 0;
  return r === 90 || r === 270 ? { fx: k.d, fz: k.w } : { fx: k.w, fz: k.d };
}
function dcObjCells(o) {
  if (o.wall) { return []; }
  const f = dcObjFoot(o), out = [];
  for (let x = 0; x < f.fx; x++) { for (let z = 0; z < f.fz; z++) { out.push([o.x + x, o.z + z]); } }
  return out;
}
// 贴墙物件背对的那面墙: 朝向 0 = 背靠北面的墙, 180 = 南面, 90 = 西面, 270 = 东面。
// 占的每块地板背后那一块都在机房外面(外墙, 或挖掉的区域形成的内墙)才算贴墙
function dcObjOnWall(room, o) {
  if (o.wall) { return dcShapedFits(room, o); }
  const b = DC_BACK[o.rot || 0];
  // 背后是直墙(外墙 / 整块挖掉的区域); 斜墙 / 弧墙上暂时不能挂
  return dcObjCells(o).every((c) => dcIn(room, c[0], c[1]) && !dcInShaped(room, c[0], c[1]) && dcHardOut(room, c[0] + b[0], c[1] + b[1]));
}
function dcObjFree(layout, room, o, ignoreId) {
  const k = DC_OBJ[o.kind], f = dcObjFoot(o);
  if (o.wall) {
    // 斜墙 / 弧墙: 放得下, 跟同一面墙上的别的东西(按沿墙区间)不重叠
    if (!k || !k.wall || !dcShapedFits(room, o)) { return false; }
    const line = dcShapedLine(dcShapedCut(room, o.wall)), me = dcShapedSpan(line, o);
    return !(layout.objects || []).some((x) => x.room === room.id && x.id !== ignoreId && x.id !== o.id && x.wall === o.wall && (DC_OBJ[x.kind] || {}).wall &&
      (() => { const sp = dcShapedSpan(line, x); return me[0] < sp[1] - 0.02 && sp[0] + 0.02 < me[1]; })());
  }
  if (!k || !dcObjCells(o).every((c) => dcIn(room, c[0], c[1]))) { return false; }
  if (k.wall && !dcObjOnWall(room, o)) { return false; }
  // 贴墙的东西(门 / 大屏 / 摄像头)之间不能叠在一起
  if (k.wall) {
    const mine = {};
    dcObjCells(o).forEach((c) => { mine[c[0] + ',' + c[1]] = true; });
    const clash = (layout.objects || []).some((x) => x.room === room.id && x.id !== ignoreId && x.id !== o.id && (DC_OBJ[x.kind] || {}).wall && (x.rot || 0) === (o.rot || 0) && dcObjCells(x).some((c) => mine[c[0] + ',' + c[1]]));
    if (clash) { return false; }
  }
  if (!k.solid) { return true; }
  const used = dcUsedCells(layout, room, ignoreId);
  return dcObjCells(o).every((c) => !used[c[0] + ',' + c[1]]);
}
// 点在地板上的位置 -> 物件摆放: 贴墙的吸到最近那面墙(沿墙以点击处为中心), 其他的以点击处为左上角
// ok(o): 可选, 只考虑满足条件的位置(比如墙上那一段已经有门了就找旁边空着的)
function dcObjAt(room, cell, kind, rot, ok) {
  const k = DC_OBJ[kind], clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
  if (!k.wall) { return { kind, rot: rot || 0, x: cell.x, z: cell.z }; }
  // 在点击处附近找能贴墙的位置(外墙和挖掉区域的内墙都算), 取占地中心离点击处最近的
  let best = null, bd = Infinity;
  const cx = clamp(cell.x, 0, room.w - 1), cz = clamp(cell.z, 0, room.d - 1), R = 12;
  [0, 90, 180, 270].forEach((r) => {
    const f = dcObjFoot({ kind, rot: r });
    for (let x = cx - R; x <= cx + R; x++) {
      for (let z = cz - R; z <= cz + R; z++) {
        const o = { kind, rot: r, x, z }, d = Math.hypot(x + f.fx / 2 - (cell.x + 0.5), z + f.fz / 2 - (cell.z + 0.5));
        if (d < bd && dcObjOnWall(room, o) && (!ok || ok(o))) { bd = d; best = o; }
      }
    }
  });
  // 斜墙 / 弧墙: 找墙上离点击处最近的点, 放不下或被占了就沿墙往两边找
  const px = cell.x + 0.5, pz = cell.z + 0.5;
  (room.cuts || []).filter(dcShaped).forEach((c) => {
    const line = dcShapedLine(c), half = k.w / 2;
    if (line.len < k.w - 0.05) { return; }
    let s0 = 0, dmin = Infinity;
    for (let i = 0; i + 1 < line.pts.length; i++) {
      const a = line.pts[i], b = line.pts[i + 1], dx = b[0] - a[0], dz = b[1] - a[1], L2 = dx * dx + dz * dz || 1;
      const t = Math.max(0, Math.min(1, ((px - a[0]) * dx + (pz - a[1]) * dz) / L2)), qx = a[0] + dx * t, qz = a[1] + dz * t, dd = Math.hypot(px - qx, pz - qz);
      if (dd < dmin) { dmin = dd; s0 = line.cum[i] + Math.sqrt(L2) * t; }
    }
    if (dmin >= bd) { return; }
    for (let step = 0; step <= 40; step++) {
      for (const sg of step ? [1, -1] : [1]) {
        const sp = Math.max(half, Math.min(line.len - half, s0 + sg * step * 0.1));
        const o = Object.assign({ kind, wall: line.wall, t: sp / line.len }, dcShapedApprox(room, { kind, wall: line.wall, t: sp / line.len }));
        if (!ok || ok(o)) { if (dmin + step * 0.1 < bd) { bd = dmin + step * 0.1; best = o; } return; }
      }
    }
  });
  return best || { kind, rot: 0, x: clamp(cell.x, 0, room.w - 1), z: 0 };
}
// 机房改尺寸后, 靠南墙 / 东墙的贴墙物件跟着墙走
function dcObjResnap(room, o) {
  const k = DC_OBJ[o.kind], f = dcObjFoot(o);
  if (!k || !k.wall) { return; }
  if (o.wall) {
    if (dcShapedFits(room, o)) { return; }
    // 那面斜墙 / 弧墙没了(改回矩形 / 删掉)或放不下了: 在原位置附近另找一面空着的墙
    const n = dcObjAt(room, { x: o.x, z: o.z }, o.kind, o.rot, dcFreeFor(room, o));
    dcObjSetPlace(o, n);
    return;
  }
  if (o.rot === 180) { o.z = room.d - f.fz; }
  if (o.rot === 270) { o.x = room.w - f.fx; }
  o.x = Math.max(0, Math.min(room.w - f.fx, o.x)); o.z = Math.max(0, Math.min(room.d - f.fz, o.z));
  // 异形机房: 还没贴上墙就在附近找一面墙
  if (!dcObjOnWall(room, o)) { dcObjSetPlace(o, dcObjAt(room, { x: o.x, z: o.z }, o.kind, o.rot, dcFreeFor(room, o))); }
}
// 给 dcObjAt 用: 候选位置对物件 o 来说空着(不跟别的物件重叠, 忽略它自己)
function dcFreeFor(room, o) { return (x) => { x.id = o.id; return !DC.layout || dcObjFree(DC.layout, room, x, o.id); }; }
function dcObjLabel(o) { const k = DC_OBJ[o.kind] || {}; return o.name || k.name || o.kind; }

/* ---------- 材质 / 贴图小工具 ---------- */
// 共享材质: 一个场景里同样的材质只建一份(userData.shared, 清场景时不释放)
function dcMat(key, make) {
  GL.dcMat = GL.dcMat || {};
  if (!GL.dcMat[key]) { const m = make(GL.THREE); m.userData.shared = true; GL.dcMat[key] = m; }
  return GL.dcMat[key];
}
function dcStd(key, p) { return dcMat(key, (T) => new T.MeshStandardMaterial(p)); }
function dcBox(w, h, d, mat, x, y, z) {
  const m = new GL.THREE.Mesh(new GL.THREE.BoxGeometry(w, h, d), mat);
  m.position.set(x || 0, y || 0, z || 0); m.castShadow = true; m.receiveShadow = true;
  return m;
}
function dcCyl(rt, rb, h, mat, x, y, z, seg) {
  const m = new GL.THREE.Mesh(new GL.THREE.CylinderGeometry(rt, rb, h, seg || 20), mat);
  m.position.set(x || 0, y || 0, z || 0); m.castShadow = true;
  return m;
}
// 正面贴图的面板: 只有 +z 面贴图, 其他面用 side 材质
function dcFaced(w, h, d, faceMat, sideMat) {
  return new GL.THREE.Mesh(new GL.THREE.BoxGeometry(w, h, d), [sideMat, sideMat, sideMat, sideMat, faceMat, sideMat]);
}
function dcCanvasTex(w, h, draw) {
  const T = GL.THREE, cv = document.createElement('canvas');
  cv.width = w; cv.height = h;
  draw(cv.getContext('2d'), w, h);
  const t = new T.CanvasTexture(cv);
  t.colorSpace = T.SRGBColorSpace; t.anisotropy = 8;
  return t;
}
function dcHex(n) { return '#' + ('000000' + n.toString(16)).slice(-6); }

/* ---------- 场景物件 ---------- */
function dcObjModel(c, o) {
  const T = c.st.THREE, g = new T.Group(), k = DC_OBJ[o.kind];
  if (!k) { return null; }
  const W = k.w * DC_TILE, D = k.d * DC_TILE, z0 = -D / 2, wallH = c.wallH || 300;
  const steel = dcStd('o:steel', { color: 0x9aa4b2, metalness: 0.85, roughness: 0.3 });
  const dark = dcStd('o:dark', { color: 0x1f2530, metalness: 0.6, roughness: 0.45 });
  const chrome = dcStd('o:chrome', { color: 0xd8dee8, metalness: 1, roughness: 0.15 });
  if (o.kind === 'door') {
    // 双开门: 门框 + 两扇带玻璃窗的门 + 推杆 + 门禁读卡器 + 门楣标牌 + 地垫
    const frame = dcStd('o:doorFrame', { color: 0x4b5563, metalness: 0.8, roughness: 0.35 });
    const leaf = dcStd('o:doorLeaf', { color: 0xaab4c2, metalness: 0.25, roughness: 0.5 });
    const glass = dcStd('o:doorGlass', { color: 0x9fd3ff, metalness: 0.9, roughness: 0.05, transparent: true, opacity: 0.35 });
    // 门洞高度 DH(默认 2.3 米, 可在右侧面板改), 墙上的洞跟门框一样高
    const DH = o.h || DC_DOOR_H, LH = DH - 12;
    [-1, 1].forEach((sx) => g.add(dcBox(7, DH - 8, 12, frame, sx * (W / 2 - 3.5), (DH - 8) / 2, z0 + 6)));
    g.add(dcBox(W, 9, 12, frame, 0, DH - 4.5, z0 + 6));
    // 两扇门各挂在一个铰链(门框内侧)上, 能往里开: 点门开 / 关, 镜头走近自动开(dc3d.js)
    const leaves = [];
    [-1, 1].forEach((sx) => {
      const pv = new T.Group(), hx = sx * (W / 2 - 7), cx = sx * (W / 4 - 1.8) - hx;
      pv.position.set(hx, 0, z0 + 4);
      pv.add(dcBox(W / 2 - 7.5, LH, 3.5, leaf, cx, LH / 2 + 1, 0));
      pv.add(dcBox(W / 2 - 22, LH * 0.41, 1, glass, cx, LH * 0.69, 2.3));
      pv.add(dcBox(26, 2.6, 2.6, chrome, cx, 104, 4));
      pv.add(dcBox(W / 2 - 12, 0.6, 1, dark, cx, 30, 2));
      g.add(pv); leaves.push({ p: pv, s: sx });
    });
    g.userData.doorLeaves = leaves;
    if (c.roomDoors) { c.roomDoors.push(g); }
    const reader = dcBox(8, 13, 2.5, dark, W / 2 + 8, 128, z0 + 1.3); g.add(reader);
    g.add(dcBox(2, 2, 0.6, dcStd('o:ledG', { color: 0x22c55e, emissive: 0x22c55e, emissiveIntensity: 2.5 }), W / 2 + 8, 132, z0 + 2.8));
    const sign = new T.Mesh(new T.PlaneGeometry(96, 20), new T.MeshStandardMaterial({ map: dcTex('o:doorSign', (ctx, w, h) => {
      ctx.fillStyle = '#f8fafc'; ctx.fillRect(0, 0, w, h); ctx.strokeStyle = '#b91c1c'; ctx.lineWidth = 10; ctx.strokeRect(5, 5, w - 10, h - 10);
      ctx.fillStyle = '#b91c1c'; ctx.font = '700 64px "Microsoft YaHei", sans-serif'; ctx.textAlign = 'center'; ctx.textBaseline = 'middle'; ctx.fillText('机房重地 · 闲人免进', w / 2, h / 2 + 2);
    }, 768, 160, false), roughness: 0.6 }));
    sign.position.set(0, Math.min(DH + 16, wallH - 12), z0 + 0.6); if (DH + 6 < wallH - 22) { g.add(sign); }
    const mat = new T.Mesh(new T.PlaneGeometry(W - 12, 46), dcStd('o:mat', { color: 0x111827, roughness: 1 }));
    mat.rotation.x = -Math.PI / 2; mat.position.set(0, 0.5, z0 + 30); mat.receiveShadow = true; g.add(mat);
  } else if (o.kind === 'pillar') {
    // 承重柱: 到顶, 下面一圈黄黑警示条, 四角不锈钢护角
    const conc = dcStd('o:concrete', { color: 0xc3c8d0, roughness: 0.92, metalness: 0.02 });
    g.add(dcBox(56, wallH, 56, conc, 0, wallH / 2, 0));
    const stripe = new T.MeshStandardMaterial({ map: dcTex('o:stripe', (ctx, w, h) => {
      ctx.fillStyle = '#facc15'; ctx.fillRect(0, 0, w, h); ctx.fillStyle = '#111';
      for (let x = -h; x < w + h; x += 48) { ctx.beginPath(); ctx.moveTo(x, h); ctx.lineTo(x + 24, h); ctx.lineTo(x + 24 + h, 0); ctx.lineTo(x + h, 0); ctx.fill(); }
    }, 256, 32, true), roughness: 0.7 });
    stripe.userData.shared = true;
    g.add(dcBox(57, 18, 57, stripe, 0, 9, 0));
    [[-1, -1], [-1, 1], [1, -1], [1, 1]].forEach((p) => g.add(dcBox(4, 120, 4, chrome, p[0] * 27, 78, p[1] * 27)));
  } else if (o.kind === 'desk') {
    // 运维操作台: 桌面 + 两侧柜 + 背板 + 三台显示器 + 键盘 + 转椅(坐在 +z 一侧, 面朝显示器)
    const top = dcStd('o:deskTop', { color: 0xd6d3cd, roughness: 0.55 });
    const body = dcStd('o:deskBody', { color: 0x3a4250, metalness: 0.4, roughness: 0.5 });
    const dz = z0 + 40;
    g.add(dcBox(W - 6, 4, 74, top, 0, 75, dz));
    g.add(dcBox(W - 10, 66, 3, body, 0, 38, z0 + 5));
    [-1, 1].forEach((sx) => { g.add(dcBox(40, 73, 68, body, sx * (W / 2 - 25), 36.5, dz)); g.add(dcBox(36, 1, 1, chrome, sx * (W / 2 - 25), 58, dz + 34.5)); });
    const scr = new T.MeshStandardMaterial({ map: dcDeskScreenTex(), emissiveMap: dcDeskScreenTex(), emissive: 0xffffff, emissiveIntensity: 0.9, roughness: 0.3 });
    scr.userData.shared = true;
    [-1, 0, 1].forEach((i) => {
      const m = new T.Group();
      m.add(dcBox(20, 1.5, 14, dark, 0, 77.8, 0));
      m.add(dcBox(4, 20, 3, dark, 0, 88, -2));
      m.add(dcBox(56, 34, 2.5, dark, 0, 110, 0));
      const face = new T.Mesh(new T.PlaneGeometry(53, 31), scr); face.position.set(0, 110, 1.3); m.add(face);
      m.position.set(i * 57, 0, z0 + 24 - Math.abs(i) * 6); m.rotation.y = -i * 0.28;
      g.add(m);
    });
    g.add(dcBox(44, 1.6, 14, dark, 0, 77.8, z0 + 56));
    g.add(dcBox(8, 1.4, 11, dark, 30, 77.8, z0 + 56));
    const chair = new T.Group(), seatM = dcStd('o:chair', { color: 0x1f2937, roughness: 0.8 });
    chair.add(dcBox(48, 8, 46, seatM, 0, 48, 0));
    chair.add(dcBox(46, 54, 7, seatM, 0, 82, 22));
    chair.add(dcCyl(2.5, 2.5, 38, chrome, 0, 26, 0, 12));
    for (let i = 0; i < 5; i++) { const leg = dcBox(30, 3, 4, chrome, 0, 5, 0); leg.rotation.y = i / 5 * Math.PI * 2; leg.translateX(14); chair.add(leg); }
    [-1, 1].forEach((sx) => chair.add(dcBox(4, 3, 30, seatM, sx * 25, 62, 4)));
    chair.position.set(8, 0, z0 + 96); chair.rotation.y = 0.18;
    g.add(chair);
  } else if (o.kind === 'screen') {
    // 墙挂监控大屏: 内容是这个机房的实时概况(机柜数 / 告警 / 离线 + 每个机柜一个色块)
    const y = Math.min(200, wallH - 90);
    g.add(dcBox(30, 30, 6, dark, 0, y, z0 + 3));
    g.add(dcBox(W - 16, 132, 7, dark, 0, y, z0 + 9));
    const face = new T.Mesh(new T.PlaneGeometry(W - 24, 124), new T.MeshStandardMaterial({ map: dcWallScreenTex(c), emissiveMap: dcWallScreenTex(c), emissive: 0xffffff, emissiveIntensity: 1.1, roughness: 0.25 }));
        face.position.set(0, y, z0 + 12.6); g.add(face);
  } else if (o.kind === 'extinguisher') {
    // 灭火器箱: 红色箱体 + 白字, 顶上露两个瓶头
    const red = dcStd('o:red', { color: 0xc81e1e, metalness: 0.3, roughness: 0.45 });
    const front = new T.MeshStandardMaterial({ map: dcTex('o:extFront', (ctx, w, h) => {
      ctx.fillStyle = '#c81e1e'; ctx.fillRect(0, 0, w, h);
      ctx.fillStyle = 'rgba(255,255,255,.14)'; ctx.fillRect(18, 18, w - 36, h - 36);
      ctx.fillStyle = '#fff'; ctx.textAlign = 'center'; ctx.font = '700 58px "Microsoft YaHei", sans-serif';
      ctx.fillText('灭', w / 2, h * 0.3); ctx.fillText('火', w / 2, h * 0.47); ctx.fillText('器', w / 2, h * 0.64);
      ctx.font = '600 30px Arial'; ctx.fillText('FIRE', w / 2, h * 0.8); ctx.fillStyle = '#e5e7eb'; ctx.fillRect(w - 44, h * 0.4, 14, 60);
    }, 256, 420, false), roughness: 0.45 });
    front.userData.shared = true;
    const box = dcFaced(44, 72, 22, front, red); box.position.set(0, 38, z0 + 11); box.castShadow = true; g.add(box);
    g.add(dcBox(46, 3, 24, dark, 0, 1.5, z0 + 11));
  } else if (o.kind === 'gas') {
    // 气体灭火(七氟丙烷)钢瓶组: 三个红钢瓶 + 瓶头阀 + 集流管 + 固定架 + 标识牌
    const red = dcStd('o:gasRed', { color: 0xd11f1f, metalness: 0.55, roughness: 0.28 });
    g.add(dcBox(W - 4, 8, 46, dark, 0, 4, -2));
    [-38, 0, 38].forEach((x) => {
      g.add(dcCyl(14, 14, 138, red, x, 8 + 69, -4, 28));
      const dome = new T.Mesh(new T.SphereGeometry(14, 24, 12, 0, Math.PI * 2, 0, Math.PI / 2), red); dome.scale.y = 0.55; dome.position.set(x, 146, -4); g.add(dome);
      g.add(dcCyl(4, 4, 12, chrome, x, 158, -4, 12));
      g.add(dcCyl(1.4, 1.4, 12, chrome, x, 170, -4, 8));
    });
    const pipe = dcCyl(2.6, 2.6, W - 16, chrome, 0, 176, -4, 12); pipe.rotation.z = Math.PI / 2; g.add(pipe);
    [40, 112].forEach((y) => g.add(dcBox(W - 6, 4, 4, steel, 0, y, -20)));
    const lab = new T.Mesh(new T.PlaneGeometry(26, 44), new T.MeshStandardMaterial({ map: dcTex('o:gasLabel', (ctx, w, h) => {
      ctx.fillStyle = '#f8fafc'; ctx.fillRect(0, 0, w, h); ctx.fillStyle = '#b91c1c'; ctx.fillRect(0, 0, w, 50);
      ctx.fillStyle = '#fff'; ctx.font = '700 32px "Microsoft YaHei"'; ctx.textAlign = 'center'; ctx.fillText('灭火剂', w / 2, 36);
      ctx.fillStyle = '#111'; ctx.font = '700 30px "Microsoft YaHei"'; ctx.fillText('七氟丙烷', w / 2, 100); ctx.font = '26px Arial'; ctx.fillText('HFC-227ea', w / 2, 140); ctx.fillText('70L', w / 2, 180);
    }, 180, 300, false), roughness: 0.6 }));
    lab.position.set(0, 90, 10.3); g.add(lab);
  } else if (o.kind === 'camera') {
    // 枪式摄像头: 墙座 + 支臂 + 机身 + 遮阳罩, 低头约 20°, 红色工作灯
    const white = dcStd('o:camWhite', { color: 0xe5e7eb, metalness: 0.3, roughness: 0.4 });
    const y = wallH - 28;
    g.add(dcBox(12, 12, 2, white, 0, y, z0 + 1));
    g.add(dcBox(3.5, 3.5, 16, white, 0, y, z0 + 9));
    const body = new T.Group();
    const cyl = dcCyl(5.5, 5.5, 26, white, 0, 0, 0, 16); cyl.rotation.x = Math.PI / 2; body.add(cyl);
    body.add(dcBox(13, 1.2, 30, white, 0, 6.5, 1));
    const lens = new T.Mesh(new T.CircleGeometry(4.2, 20), dcStd('o:lens', { color: 0x05070a, metalness: 0.9, roughness: 0.05 })); lens.position.z = 13.1; body.add(lens);
    const led = dcBox(1.4, 1.4, 0.6, dcStd('o:ledR', { color: 0xef4444, emissive: 0xef4444, emissiveIntensity: 3 }), 0, -3.5, 13.2); body.add(led);
    body.position.set(0, y - 4, z0 + 26); body.rotation.x = 0.35;
    g.add(body);
  }
  // 单击 / 悬停用: 底下一块看不见的拾取盒(细小的物件也好点)
  const hit = new T.Mesh(new T.BoxGeometry(W - 4, o.kind === 'camera' ? 40 : o.kind === 'pillar' ? wallH : 200, D - 4), new T.MeshBasicMaterial({ visible: false }));
  hit.position.set(0, o.kind === 'camera' ? wallH - 32 : o.kind === 'pillar' ? wallH / 2 : 100, 0); hit.userData.noBox = true;
  g.add(hit);
  return g;
}
function dcDeskScreenTex() {
  return dcTex('o:deskScreen', (ctx, w, h) => {
    ctx.fillStyle = '#071427'; ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = '#0e2a4d'; ctx.fillRect(0, 0, w, 26);
    ctx.fillStyle = '#7dd3fc'; ctx.font = '600 16px "Microsoft YaHei"'; ctx.fillText('网络监控 · 巡检总览', 10, 18);
    for (let i = 0; i < 6; i++) {
      ctx.fillStyle = '#0b1f3a'; ctx.fillRect(10 + i * 50, 36, 44, 36);
      ctx.fillStyle = i === 4 ? '#f87171' : '#34d399'; ctx.fillRect(14 + i * 50, 62, 36 * (0.4 + (i * 37 % 60) / 100), 5);
      ctx.fillStyle = '#cbd5e1'; ctx.font = '14px Consolas'; ctx.fillText(String(90 + i * 7 % 10), 16 + i * 50, 54);
    }
    ctx.strokeStyle = '#22d3ee'; ctx.lineWidth = 2; ctx.beginPath();
    for (let x = 0; x <= 290; x += 10) { const y = 140 - 30 * Math.sin(x / 30) - (x * 13 % 17); if (x) { ctx.lineTo(10 + x, y); } else { ctx.moveTo(10, y); } }
    ctx.stroke();
    ctx.strokeStyle = '#a78bfa'; ctx.beginPath();
    for (let x = 0; x <= 290; x += 10) { const y = 160 - 18 * Math.cos(x / 22); if (x) { ctx.lineTo(10 + x, y); } else { ctx.moveTo(10, y); } }
    ctx.stroke();
  }, 320, 190, false);
}
function dcWallScreenTex(c) {
  if (c._wallTex) { return c._wallTex; }
  const layout = c.layout || {}, room = c.room || {}, devices = c.devices || [];
  const racks = (layout.racks || []).filter((k) => k.room === room.id && !dcIsUnit(k));
  const devs = devices.filter((d) => racks.some((k) => k.name === d.rack));
  const bad = devs.filter((d) => d.alive && d.alerts > 0).length, off = devs.filter((d) => !d.alive).length;
  c._wallTex = dcCanvasTex(1024, 560, (ctx, w, h) => {
    const g = ctx.createLinearGradient(0, 0, 0, h); g.addColorStop(0, '#061a33'); g.addColorStop(1, '#020a16');
    ctx.fillStyle = g; ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = '#0c2d57'; ctx.fillRect(0, 0, w, 70);
    ctx.fillStyle = '#e0f2fe'; ctx.font = '700 40px "Microsoft YaHei"'; ctx.fillText((room.name || '机房') + ' · 运行监控', 28, 48);
    ctx.fillStyle = '#7dd3fc'; ctx.font = '26px Consolas'; ctx.textAlign = 'right'; ctx.fillText(new Date().toTimeString().slice(0, 5), w - 28, 46); ctx.textAlign = 'left';
    const kpi = [['机柜', racks.length, '#38bdf8'], ['交换机', devs.length, '#34d399'], ['告警', bad, bad ? '#f87171' : '#34d399'], ['离线', off, off ? '#fbbf24' : '#34d399']];
    kpi.forEach((k, i) => {
      const x = 28 + i * 246;
      ctx.fillStyle = 'rgba(56,189,248,.08)'; ctx.fillRect(x, 96, 226, 120);
      ctx.fillStyle = '#94a3b8'; ctx.font = '26px "Microsoft YaHei"'; ctx.fillText(k[0], x + 18, 134);
      ctx.fillStyle = k[2]; ctx.font = '700 64px Consolas'; ctx.fillText(String(k[1]), x + 18, 198);
    });
    // 每个机柜一个色块
    const n = Math.max(1, racks.length), cols = Math.min(16, n), cw = (w - 56) / cols, rows = Math.ceil(n / cols), ch = Math.min(60, (h - 260) / Math.max(1, rows));
    racks.forEach((k, i) => {
      const st = dcRackStatus(k, layout, devices), col = st === 'bad' ? '#ef4444' : st === 'off' ? '#f59e0b' : st === 'ok' ? '#22c55e' : '#38bdf8';
      const x = 28 + (i % cols) * cw, y = 244 + Math.floor(i / cols) * ch;
      ctx.fillStyle = col; ctx.globalAlpha = 0.85; ctx.fillRect(x + 4, y + 4, cw - 8, ch - 8); ctx.globalAlpha = 1;
      ctx.fillStyle = '#04111f'; ctx.font = '600 ' + Math.min(22, ch * 0.4) + 'px Consolas'; ctx.fillText(truncateLabel(k.name, 6), x + 10, y + ch / 2 + 6);
    });
    if (!racks.length) { ctx.fillStyle = '#64748b'; ctx.font = '28px "Microsoft YaHei"'; ctx.fillText('还没有机柜', 28, 300); }
  });
  return c._wallTex;
}

/* ---------- 落地设备: 精密空调 / UPS / 配电柜 / 电池柜 ---------- */
function dcUnitModel(c, k) {
  const T = c.st.THREE, g = new T.Group(), H = 200, W = DC_W - 2, D = DC_D - 4;
  const bodyCol = { ac: 0xe3e6ea, ups: 0x2b3240, pdc: 0xc9ccd1, battery: 0x3a414d }[k.type] || 0x3a414d;
  const body = dcStd('u:body:' + k.type, { color: bodyCol, metalness: 0.45, roughness: 0.45 });
  const dark = dcStd('o:dark', { color: 0x1f2530, metalness: 0.6, roughness: 0.45 });
  const chrome = dcStd('o:chrome', { color: 0xd8dee8, metalness: 1, roughness: 0.15 });
  g.add(dcBox(W, 8, D, dark, 0, 4, 0));
  g.add(dcBox(W, H - 11, D - 2, body, 0, 8 + (H - 11) / 2, -1));
  g.add(dcBox(W + 1, 3, D + 1, dcStd('u:cap:' + k.type, { color: new T.Color(bodyCol).multiplyScalar(0.8), metalness: 0.5, roughness: 0.4 }), 0, H - 1.5, 0));
  const face = new T.MeshStandardMaterial({ map: dcUnitFrontTex(k), emissiveMap: dcUnitFrontTex(k), emissive: 0xffffff, emissiveIntensity: 0.28, roughness: 0.5, metalness: 0.25 });
  const door = dcFaced(W - 1, H - 13, 1.4, face, body);
  door.position.set(0, 8 + (H - 13) / 2 + 1, D / 2 + 0.2); door.castShadow = true; g.add(door);
  const leaves = k.type === 'battery' ? [-1, 1] : [1];
  leaves.forEach((sx) => g.add(dcBox(1.6, 24, 2.6, chrome, leaves.length > 1 ? sx * 2.4 : W / 2 - 6, 110, D / 2 + 1.8)));
  if (leaves.length > 1) { g.add(dcBox(0.5, H - 14, 0.4, dark, 0, 8 + (H - 13) / 2 + 1, D / 2 + 1)); }
  if (k.type === 'ac') {
    // 顶部回风格栅 + 前面地上一片淡蓝色的冷风
    g.add(dcBox(W - 10, 0.8, D - 12, dcStd('u:grille', { color: 0x4b5563, metalness: 0.6, roughness: 0.6 }), 0, H + 0.4, 0));
    for (let i = 0; i < 9; i++) { g.add(dcBox(W - 12, 1.2, 1.2, chrome, 0, H + 0.9, -D / 2 + 12 + i * (D - 24) / 8)); }
    const wind = new T.Mesh(new T.PlaneGeometry(DC_W - 6, 60), new T.MeshBasicMaterial({ color: 0x7dd3fc, transparent: true, opacity: 0.12, side: T.DoubleSide, depthWrite: false }));
    wind.rotation.x = -Math.PI / 2; wind.position.set(0, 2, DC_D / 2 + 30); wind.raycast = function () {}; g.add(wind);
  }
  return g;
}
function dcUnitFrontTex(k) {
  const key = 'u:front:' + k.type + ':' + k.name;
  return dcTex(key, (ctx, w, h) => {
    const base = { ac: '#e3e6ea', ups: '#2b3240', pdc: '#c9ccd1', battery: '#3a414d' }[k.type] || '#3a414d';
    const lightBody = k.type === 'ac' || k.type === 'pdc';
    ctx.fillStyle = base; ctx.fillRect(0, 0, w, h);
    const sh = ctx.createLinearGradient(0, 0, w, 0); sh.addColorStop(0, 'rgba(0,0,0,.12)'); sh.addColorStop(0.5, 'rgba(255,255,255,.06)'); sh.addColorStop(1, 'rgba(0,0,0,.12)');
    ctx.fillStyle = sh; ctx.fillRect(0, 0, w, h);
    ctx.strokeStyle = lightBody ? 'rgba(0,0,0,.25)' : 'rgba(255,255,255,.12)'; ctx.lineWidth = 3; ctx.strokeRect(10, 10, w - 20, h - 20);
    const slots = (y0, y1, gap, inset) => { ctx.fillStyle = lightBody ? 'rgba(20,24,30,.55)' : 'rgba(0,0,0,.55)'; for (let y = y0; y < y1; y += gap) { ctx.fillRect(inset, y, w - inset * 2, gap * 0.45); } };
    ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
    if (k.type === 'ac') {
      slots(40, 380, 16, 34);
      ctx.fillStyle = '#0b1220'; glRoundRect(ctx, 70, 440, w - 140, 150, 12); ctx.fill();
      ctx.fillStyle = '#67e8f9'; ctx.font = '700 64px Consolas'; ctx.fillText('22.5℃', w / 2, 500);
      ctx.fillStyle = '#a5f3fc'; ctx.font = '30px Consolas'; ctx.fillText('RH 45%  RUN', w / 2, 560);
      [['#22c55e', 110], ['#22c55e', 150], ['#f59e0b', 190]].forEach((l) => { ctx.fillStyle = l[0]; ctx.beginPath(); ctx.arc(l[1], 630, 8, 0, 7); ctx.fill(); });
      ctx.fillStyle = 'rgba(20,24,30,.45)';
      for (let y = 700; y < 1120; y += 18) { for (let x = 40; x < w - 40; x += 18) { ctx.beginPath(); ctx.arc(x, y, 5, 0, 7); ctx.fill(); } }
      ctx.fillStyle = '#475569'; ctx.font = '600 26px Arial'; ctx.fillText('PRECISION A/C', w / 2, 670);
    } else if (k.type === 'ups') {
      ctx.fillStyle = '#062a2a'; glRoundRect(ctx, 60, 120, w - 120, 190, 10); ctx.fill();
      ctx.fillStyle = '#5eead4'; ctx.font = '600 36px Consolas'; ctx.fillText('ONLINE', w / 2, 165); ctx.font = '30px Consolas'; ctx.fillText('LOAD  42%', w / 2, 218); ctx.fillText('BAT  100%', w / 2, 262);
      ['#22c55e', '#22c55e', '#22c55e', '#374151', '#374151'].forEach((c2, i) => { ctx.fillStyle = c2; ctx.fillRect(74 + i * 44, 340, 32, 12); });
      [0, 1, 2].forEach((i) => { ctx.fillStyle = '#111827'; ctx.beginPath(); ctx.arc(110 + i * 70, 400, 18, 0, 7); ctx.fill(); ctx.strokeStyle = '#6b7280'; ctx.lineWidth = 3; ctx.stroke(); });
      slots(760, 1150, 20, 40);
      ctx.fillStyle = '#cbd5e1'; ctx.font = '700 40px Arial'; ctx.fillText('UPS', w / 2, 60);
    } else if (k.type === 'pdc') {
      // 上面三块指针表 + 指示灯, 中间玻璃窗里一排排空开, 下面电闪警示
      [0, 1, 2].forEach((i) => {
        const x = 75 + i * 105, y = 110;
        ctx.fillStyle = '#f8fafc'; ctx.fillRect(x - 42, y - 42, 84, 84); ctx.strokeStyle = '#334155'; ctx.lineWidth = 4; ctx.strokeRect(x - 42, y - 42, 84, 84);
        ctx.strokeStyle = '#111'; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(x, y + 14, 34, Math.PI * 1.15, Math.PI * 1.85); ctx.stroke();
        ctx.strokeStyle = '#dc2626'; ctx.lineWidth = 3; ctx.beginPath(); ctx.moveTo(x, y + 14); ctx.lineTo(x + Math.cos(-1.1 - i * 0.3) * 30, y + 14 + Math.sin(-1.1 - i * 0.3) * 30); ctx.stroke();
      });
      [['#ef4444', 90], ['#22c55e', 180], ['#facc15', 270]].forEach((l) => { ctx.fillStyle = l[0]; ctx.beginPath(); ctx.arc(l[1], 200, 13, 0, 7); ctx.fill(); });
      ctx.fillStyle = '#1e293b'; ctx.fillRect(40, 250, w - 80, 620);
      ctx.fillStyle = 'rgba(148,197,255,.12)'; ctx.fillRect(40, 250, w - 80, 620);
      for (let r = 0; r < 6; r++) {
        const y = 290 + r * 98;
        ctx.fillStyle = '#94a3b8'; ctx.fillRect(56, y + 30, w - 112, 8);
        for (let i = 0; i < 9; i++) { ctx.fillStyle = '#f1f5f9'; ctx.fillRect(62 + i * 28, y, 22, 66); ctx.fillStyle = (r * 9 + i) % 7 === 3 ? '#16a34a' : '#dc2626'; ctx.fillRect(66 + i * 28, y + 14, 14, 14); }
      }
      ctx.fillStyle = '#facc15'; ctx.beginPath(); ctx.moveTo(w / 2, 920); ctx.lineTo(w / 2 + 60, 1025); ctx.lineTo(w / 2 - 60, 1025); ctx.closePath(); ctx.fill();
      ctx.fillStyle = '#111'; ctx.font = '700 64px Arial'; ctx.fillText('⚡', w / 2, 990);
      ctx.font = '600 30px "Microsoft YaHei"'; ctx.fillText('有电危险', w / 2, 1070);
    } else {
      ctx.fillStyle = 'rgba(0,0,0,.5)';
      for (let y = 60; y < 1120; y += 14) { for (let x = (y / 14) % 2 ? 36 : 43; x < w - 30; x += 14) { ctx.fillRect(x, y, 6, 6); } }
      // 标识放在把手上方, 不被双开门的把手挡住
      ctx.fillStyle = base; ctx.fillRect(60, 200, w - 120, 150);
      ctx.strokeStyle = '#22c55e'; ctx.lineWidth = 6; ctx.strokeRect(w / 2 - 60, 228, 120, 56); ctx.fillStyle = '#22c55e'; ctx.fillRect(w / 2 + 60, 244, 10, 24); ctx.fillRect(w / 2 - 50, 238, 70, 36);
      ctx.fillStyle = '#e5e7eb'; ctx.font = '700 32px Arial'; ctx.fillText('BATTERY', w / 2, 320);
    }
    ctx.fillStyle = lightBody ? '#1f2937' : '#e5e7eb'; ctx.font = '600 34px "Microsoft YaHei"';
    ctx.fillText(truncateLabel(k.name, 10), w / 2, h - 56);
  }, 360, 1200, false);
}

/* ---------- 柜内非监控设备 ---------- */
function dcItemModel(it) {
  const T = GL.THREE, g = new T.Group();
  const depth = (DC_KIND[it.kind] || DC_KIND.other)[2], hh = it.h * DC_U - 0.35;
  const face = new T.MeshStandardMaterial({ map: dcItemTex(it), emissiveMap: dcItemTex(it), emissive: 0xffffff, emissiveIntensity: 0.55, roughness: 0.5, metalness: 0.3 });
  const side = dcStd('i:side', { color: 0x2a2f38, metalness: 0.7, roughness: 0.4 });
  const chrome = dcStd('o:chrome', { color: 0xd8dee8, metalness: 1, roughness: 0.15 });
  const box = dcBox(44, hh, depth, side, 0, 0, 0); g.add(box);
  const plate = dcFaced(48, hh, 1.6, face, side); plate.position.z = depth / 2 + 0.8; plate.castShadow = true; g.add(plate);
  // 背面: 电源模块 + 风扇(开后门时看得到), 跟被监控交换机共用一张贴图
  if (it.kind !== 'patch' && it.kind !== 'pdu') {
    const bt = glBackTexture(hh / 44, false), bm = new T.MeshStandardMaterial({ map: bt, emissiveMap: bt, emissive: 0xffffff, emissiveIntensity: it.status === 'off' ? 0.06 : 0.25, roughness: 0.6 });
    const rp = new T.Mesh(new T.PlaneGeometry(44, hh), bm); rp.rotation.y = Math.PI; rp.position.z = -depth / 2 - 0.05; rp.raycast = function () {}; g.add(rp);
  }
  if (it.kind === 'server' || it.kind === 'storage' || it.kind === 'firewall' || it.kind === 'router') {
    [-1, 1].forEach((sx) => g.add(dcBox(1.2, Math.min(hh - 1.2, 7), 2.2, chrome, sx * 22.2, 0, depth / 2 + 2.6)));
  }
  if (it.kind === 'patch') {
    // 配线架: 前面垂下一束彩色跳线, 往两侧理线
    const cols = [0x3b82f6, 0xeab308, 0x22c55e, 0xe5e7eb, 0x3b82f6, 0xf97316];
    cols.forEach((col, i) => {
      const x0 = -14 + i * 5.5, side2 = i < 3 ? -1 : 1;
      const curve = new T.CatmullRomCurve3([new T.Vector3(x0, 0.6, depth / 2 + 2), new T.Vector3(x0 + side2 * 3, -3, depth / 2 + 5), new T.Vector3(side2 * 21, -6, depth / 2 + 4), new T.Vector3(side2 * 23.5, -14, depth / 2 + 2)]);
      const cab = new T.Mesh(new T.TubeGeometry(curve, 16, 0.32, 5), dcStd('i:cable:' + col, { color: col, roughness: 0.6 }));
      cab.raycast = function () {}; g.add(cab);
    });
  }
  return g;
}

/* ---------- 右侧面板: 场景物件 ---------- */
function dcObjById(id) { return (DC.layout.objects || []).filter((o) => o.id === id)[0]; }
function dcSideObject(o) {
  const k = DC_OBJ[o.kind] || {}, dir = { 0: '南', 90: '东', 180: '北', 270: '西' }[o.rot || 0];
  let h = html`<h4>${k.icon || ''} ${dcObjLabel(o)}</h4><div class="sub">${k.name}${o.kind === 'door' ? ' · 门洞 ' + ((o.h || DC_DOOR_H) / 100).toFixed(2) + ' m' : ''}${o.wall ? '' : ' · 位置 (' + o.x + ', ' + o.z + ')'} · ${o.wall ? '挂在' + dcWallName(dcRoomOf(DC.layout, o.room), o.wall) + '上' : k.wall ? '贴' + { 0: '北', 90: '西', 180: '南', 270: '东' }[o.rot || 0] + '墙' : '朝' + dir} · 占 ${k.w} × ${k.d} 块地板${k.solid ? '' : '(不占地)'}</div>`;
  if (DC.edit) {
    h += html`<hr><div class="grid2"><div style="grid-column:1/-1;"><label>名称(可不填)</label><input id="dcObName" value="${o.name || ''}" placeholder="${k.name}"></div>
      ${o.kind === 'door' ? html`<div><label>门洞高度(米)</label><input id="dcObH" type="number" min="1.8" max="${((dcRoomOf(DC.layout, o.room).h || 300) - 10) / 100}" step="0.05" value="${((o.h || DC_DOOR_H) / 100).toFixed(2)}"></div><div class="sub" style="align-self:end;">常见 2.1 ~ 2.4 米, 墙上的洞跟着变</div>` : ''}</div>
      <div class="btns"><button class="pri" onclick="dcObjApply('${o.id}')">应用</button>${k.wall ? '' : raw(html`<button onclick="dcObjRotate('${o.id}')">⟳ 旋转 90°</button>`.s)}<button onclick="dcObjCopy('${o.id}')">⧉ 复制</button><button class="bad" onclick="dcObjDelete('${o.id}')">删除</button></div>
      <div class="sub" style="margin-top:6px;">编辑模式下直接拖动换位置${k.wall ? '; 拖到哪面墙附近就贴到哪面墙' : ''}。</div>`;
  }
  return String(h);
}
function dcObjApply(id) {
  const o = dcObjById(id);
  if (o.kind === 'door' && byId('dcObH')) {
    const h = Math.round(parseFloat(byId('dcObH').value) * 100), maxH = (dcRoomOf(DC.layout, o.room).h || 300) - 10;
    if (!(h >= 180 && h <= maxH)) { dcMsg('门洞高度需要在 1.8 ~ ' + (maxH / 100).toFixed(1) + ' 米之间(不能超过层高)', true); return; }
    if (h === DC_DOOR_H) { delete o.h; } else { o.h = h; }
  }
  o.name = byId('dcObName').value.trim();
  dcMarkDirty(); dcRefresh(false);
}
function dcObjRotate(id) {
  const o = dcObjById(id), old = o.rot || 0;
  o.rot = (old + 90) % 360;
  if (!dcObjFree(DC.layout, dcRoomOf(DC.layout, o.room), o, o.id)) { o.rot = old; dcMsg('转过去会跟别的东西重叠或超出机房', true); return; }
  dcMarkDirty(); dcRefresh(false);
}
function dcObjCopy(id) {
  const o = dcObjById(id), room = dcRoomOf(DC.layout, o.room);
  // 在旁边找个能放的位置: 贴墙的沿墙挪, 其他的往右 / 往下找
  for (let step = 1; step < 40; step++) {
    for (const d of [[step, 0], [0, step], [-step, 0], [0, -step]]) {
      const n = { id: dcNewId('o'), kind: o.kind, name: '', room: o.room, rot: o.rot, x: o.x + d[0], z: o.z + d[1] };
      if (dcObjFree(DC.layout, room, n)) { DC.layout.objects.push(n); DC.sel = { t: 'obj', id: n.id }; dcMarkDirty(); dcRefresh(false); return; }
    }
  }
  dcMsg('附近没有空位了', true);
}
function dcObjDelete(id) { DC.layout.objects = DC.layout.objects.filter((o) => o.id !== id); DC.sel = null; dcMarkDirty(); dcRefresh(false); }

/* ---------- 机房: 按内容收缩 / 整体平移 ---------- */
// 机房里所有占位的东西(机柜 / 冷通道 / 不贴墙的物件)
function dcRoomContent(room) {
  const L = DC.layout;
  return {
    racks: L.racks.filter((k) => k.room === room.id),
    aisles: (L.aisles || []).filter((a) => a.room === room.id),
    objs: (L.objects || []).filter((o) => o.room === room.id)
  };
}
function dcRoomShift(dx, dz, quiet) {
  const room = dcRoomOf(DC.layout, DC.room), ct = dcRoomContent(room);
  const moved = [];
  const mv = (o, x, z) => { moved.push([o, o.x, o.z]); o.x += x; o.z += z; };
  ct.racks.forEach((k) => mv(k, dx, dz));
  ct.aisles.forEach((a) => mv(a, dx, dz));
  ct.objs.forEach((o) => {
    const k = DC_OBJ[o.kind];
    if (!k || !k.wall) { mv(o, dx, dz); return; }
    if (o.wall) { return; }
    // 贴墙的只沿墙挪
    if (o.rot === 0 || o.rot === 180) { mv(o, dx, 0); } else { mv(o, 0, dz); }
  });
  const inRoom = (c) => dcIn(room, c[0], c[1]);
  const inside = ct.racks.every((k) => dcCells(k).every(inRoom)) && ct.aisles.every((a) => dcAisleCells(a).every(inRoom)) &&
    ct.objs.every((o) => (DC_OBJ[o.kind] || {}).wall ? dcObjOnWall(room, o) : dcObjCells(o).every(inRoom));
  if (!inside) {
    moved.forEach((m) => { m[0].x = m[1]; m[0].z = m[2]; });
    if (!quiet) { dcMsg('再挪就出机房了(先把机房放大)', true); }
    return false;
  }
  dcMarkDirty(); dcRefresh(false);
  return true;
}
// 按内容收缩: 四周留 margin 块地板(默认 2 块 = 1.2 米)
function dcRoomFit() {
  const room = dcRoomOf(DC.layout, DC.room), ct = dcRoomContent(room), m = 2;
  let x0 = Infinity, z0 = Infinity, x1 = -Infinity, z1 = -Infinity;
  const add = (c) => { x0 = Math.min(x0, c[0]); z0 = Math.min(z0, c[1]); x1 = Math.max(x1, c[0]); z1 = Math.max(z1, c[1]); };
  ct.racks.forEach((k) => dcCells(k).forEach(add));
  ct.aisles.forEach((a) => dcAisleCells(a).forEach(add));
  ct.objs.forEach((o) => { if (!(DC_OBJ[o.kind] || {}).wall) { dcObjCells(o).forEach(add); } });
  if (!isFinite(x0)) { dcMsg('机房里还没有东西', true); return; }
  const nw = Math.max(4, x1 - x0 + 1 + m * 2), nd = Math.max(4, z1 - z0 + 1 + m * 2);
  const dx = m - x0, dz = m - z0;
  ct.racks.forEach((k) => { k.x += dx; k.z += dz; });
  ct.aisles.forEach((a) => { a.x += dx; a.z += dz; });
  room.w = nw; room.d = nd;
  if (room.cuts) { room.cuts.forEach((c) => { c.x += dx; c.z += dz; }); dcCutClip(room); }
  ct.objs.forEach((o) => {
    const k = DC_OBJ[o.kind];
    if (k && k.wall) { if (o.wall) { return; } if (o.rot === 0 || o.rot === 180) { o.x += dx; } else { o.z += dz; } dcObjResnap(room, o); } else { o.x += dx; o.z += dz; }
  });
  dcMarkDirty(); dcRefresh(true);
  dcMsg('机房已收缩到 ' + (nw * 0.6).toFixed(1) + ' × ' + (nd * 0.6).toFixed(1) + ' m');
}

/* ---------- 机房形状: 挖掉的区域(L 形 / 凹角 / 核心筒)、斜切角 / 圆角(斜墙 / 弧形墙)、沿边界生成的墙、门洞 ---------- */
// 挖掉区域 c: { x, z, w, d, type?: 'tri' | 'arc' | 'bulge', corner?: 'nw' | 'ne' | 'sw' | 'se' | 'n' | 's' | 'w' | 'e', id? }。type 为空 = 整块挖掉;
// 斜切 / 圆角只挖掉机房角上的一部分; 贴着外墙的这块可以做成墙中间的弧: arc = 内凹弧(墙往里鼓进来半个椭圆),
// bulge = 外凸弧(墙往外鼓出去半个椭圆), 用 id 当墙的编号。跟后端 datacenter.go 的 DCRect 规则一致
function dcShaped(c) { return c.type === 'tri' || c.type === 'arc' || c.type === 'bulge'; }
function dcCutEdge(c) { return !!c.corner && c.corner.length === 1; }
// 斜墙 / 弧墙的编号(挂在上面的物件 o.wall): 角上的是角, 墙中间的是 id
function dcCutWall(c) { return dcCutEdge(c) ? c.id : c.corner; }
function dcCutHas(c, x, z) { return x >= c.x && x < c.x + c.w && z >= c.z && z < c.z + c.d; }
// 墙中间的弧: 沿墙位置 s(0~L)、离外墙的深度 t(0~H) 跟地板砖坐标互换
function dcEdgeFrame(c) {
  const x0 = c.x, z0 = c.z, x1 = c.x + c.w, z1 = c.z + c.d;
  const f = {
    n: { L: c.w, H: c.d, w: (s, t) => [x0 + s, z0 + t], l: (x, z) => [x - x0, z - z0] },
    s: { L: c.w, H: c.d, w: (s, t) => [x0 + s, z1 - t], l: (x, z) => [x - x0, z1 - z] },
    w: { L: c.d, H: c.w, w: (s, t) => [x0 + t, z0 + s], l: (x, z) => [z - z0, x - x0] },
    e: { L: c.d, H: c.w, w: (s, t) => [x1 - t, z0 + s], l: (x, z) => [z - z0, x1 - x] }
  }[c.corner] || null;
  if (f) {
    // 沿墙 s 处被挖掉的深度: 内凹弧 = 椭圆往里鼓的深度, 外凸弧 = 椭圆外面那部分
    f.cutT = (s) => {
      const u = (s - f.L / 2) / (f.L / 2), r = Math.sqrt(Math.max(0, 1 - u * u));
      return c.type === 'bulge' ? f.H * (1 - r) : f.H * r;
    };
  }
  return f;
}
// 被挖掉的机房角 P、内侧角 Q(圆心 / 三角形直角顶点)
function dcCutPQ(c) {
  const x0 = c.x, z0 = c.z, x1 = c.x + c.w, z1 = c.z + c.d;
  return { nw: [[x0, z0], [x1, z1]], ne: [[x1, z0], [x0, z1]], sw: [[x0, z1], [x1, z0]], se: [[x1, z1], [x0, z0]] }[c.corner] || [[x1, z0], [x0, z1]];
}
// 点 (px, pz) 还在不在墙内
function dcCutKeeps(c, px, pz) {
  if (dcCutEdge(c)) {
    const f = dcEdgeFrame(c), p = f.l(px, pz), u = (p[0] - f.L / 2) / (f.L / 2);
    if (c.type === 'bulge') { const v = (f.H - p[1]) / f.H; return u * u + v * v <= 1 + 1e-6; }
    const v = p[1] / f.H;
    return u * u + v * v >= 1 - 1e-6;
  }
  const q = dcCutPQ(c)[1], u = Math.abs((px - q[0]) / c.w), v = Math.abs((pz - q[1]) / c.d);
  return c.type === 'arc' ? u * u + v * v <= 1 + 1e-6 : u + v <= 1 + 1e-6;
}
// 这块地板能不能用: 在范围内、没被整块挖掉; 在斜切 / 圆角里的要整块在墙内
function dcIn(room, x, z) {
  if (x < 0 || z < 0 || x >= room.w || z >= room.d) { return false; }
  const cuts = room.cuts || [];
  for (let i = 0; i < cuts.length; i++) {
    const c = cuts[i];
    if (!dcCutHas(c, x, z)) { continue; }
    if (!dcShaped(c)) { return false; }
    if (!dcCutKeeps(c, x, z) || !dcCutKeeps(c, x + 1, z) || !dcCutKeeps(c, x, z + 1) || !dcCutKeeps(c, x + 1, z + 1)) { return false; }
  }
  return true;
}
// 在机房外面、中间隔着一面直墙(外墙 / 整块挖掉); 贴墙物件只贴这种墙
function dcHardOut(room, x, z) {
  if (x < 0 || z < 0 || x >= room.w || z >= room.d) { return true; }
  return (room.cuts || []).some((c) => !dcShaped(c) && dcCutHas(c, x, z));
}
function dcInShaped(room, x, z) { return (room.cuts || []).some((c) => dcShaped(c) && dcCutHas(c, x, z)); }
function dcRoomArea(room) { let n = 0; for (let x = 0; x < room.w; x++) { for (let z = 0; z < room.d; z++) { if (dcIn(room, x, z)) { n++; } } } return n; }
// 朝向 -> 背后那块地板的偏移(贴墙物件背对的方向)
const DC_BACK = { 0: [0, -1], 180: [0, 1], 90: [-1, 0], 270: [1, 0] };
const DC_DOOR_H = 230;
// 直墙: 每块地板四个方向上挨着"直墙外面"的边就是墙, 同一直线上连着的合并成一段; 门背后的边单独成段(开门洞, 带门洞高度)。
// 斜切 / 圆角范围里的地板, 它贴着的外墙已经换成斜墙 / 弧墙, 这里跳过。dir 跟贴墙物件的朝向一致: 0 = 北墙(墙面朝南) ...
function dcWallSegments(room, objects) {
  const door = {};
  (objects || []).forEach((o) => {
    if (o.room !== room.id || o.kind !== 'door') { return; }
    dcObjCells(o).forEach((c) => { door[c[0] + ',' + c[1] + ',' + (o.rot || 0)] = o.h || DC_DOOR_H; });
  });
  const gridIn = (x, z) => !dcHardOut(room, x, z);
  const lines = {};
  for (let x = 0; x < room.w; x++) {
    for (let z = 0; z < room.d; z++) {
      if (!gridIn(x, z) || dcInShaped(room, x, z)) { continue; }
      [0, 90, 180, 270].forEach((dir) => {
        const b = DC_BACK[dir];
        if (gridIn(x + b[0], z + b[1])) { return; }
        const along = dir === 0 || dir === 180 ? x : z, line = dir === 0 ? z : dir === 180 ? z + 1 : dir === 90 ? x : x + 1;
        const key = dir + ':' + line;
        (lines[key] = lines[key] || { dir, line, cells: [] }).cells.push({ a: along, door: door[x + ',' + z + ',' + dir] || 0 });
      });
    }
  }
  const segs = [];
  Object.keys(lines).forEach((k) => {
    const L = lines[k];
    L.cells.sort((p, q) => p.a - q.a);
    let cur = null;
    L.cells.forEach((cl) => {
      if (cur && cl.a === cur.a1 && cl.door === cur.door) { cur.a1++; return; }
      cur = { dir: L.dir, line: L.line, a0: cl.a, a1: cl.a + 1, door: cl.door };
      segs.push(cur);
    });
  });
  return segs;
}
// 斜墙 / 弧墙: 每个斜切 / 圆角一条折线(地板砖坐标), q = 墙内侧的参考点(用来判断墙面朝向)
function dcShapedWalls(room) { return (room.cuts || []).filter(dcShaped).map(dcShapedLine); }
function dcShapedCut(room, wall) { return wall ? (room.cuts || []).filter((c) => dcShaped(c) && dcCutWall(c) === wall)[0] : undefined; }
// 一面斜墙 / 弧墙的折线: pts(地板砖坐标, 从墙的一头到另一头), cum = 每个点沿墙的累计长度, len = 总长, q = 墙内侧的参考点;
// away = true 时反过来, q 在墙外(内凹弧往机房里鼓, 墙内侧找不到一个对所有小段都成立的参考点, 就用椭圆圆心)
function dcShapedLine(c) {
  if (dcCutEdge(c)) {
    const f = dcEdgeFrame(c), n = Math.max(24, Math.ceil((f.L + f.H) * 8)), pts = [];
    for (let i = 0; i <= n; i++) { const th = i / n * Math.PI, s = f.L / 2 - f.L / 2 * Math.cos(th); pts.push(f.w(s, f.cutT(s))); }
    const cum = [0];
    for (let i = 1; i < pts.length; i++) { cum.push(cum[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1])); }
    const bulge = c.type === 'bulge';
    return { pts, cum, len: cum[cum.length - 1], q: f.w(f.L / 2, bulge ? f.H : 0), away: !bulge, wall: dcCutWall(c) };
  }
  const pq = dcCutPQ(c), P = pq[0], Q = pq[1], A = [Q[0], P[1]], B = [P[0], Q[1]];
  let pts;
  if (c.type === 'tri') { pts = [A, B]; } else {
    const sx = Math.sign(P[0] - Q[0]), sz = Math.sign(P[1] - Q[1]), n = Math.max(12, Math.ceil((c.w + c.d) * 6));
    pts = [];
    for (let i = 0; i <= n; i++) { const t = i / n * Math.PI / 2; pts.push([Q[0] + sx * c.w * Math.sin(t), Q[1] + sz * c.d * Math.cos(t)]); }
  }
  const cum = [0];
  for (let i = 1; i < pts.length; i++) { cum.push(cum[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1])); }
  return { pts, cum, len: cum[cum.length - 1], q: Q, wall: dcCutWall(c) };
}
// 墙上 (mx, mz) 处这一小段的法线 (nx, nz) 翻到朝机房里的那一侧
function dcLineInward(line, mx, mz, nx, nz) {
  const d = nx * (line.q[0] - mx) + nz * (line.q[1] - mz);
  return (line.away ? d > 0 : d < 0) ? [-nx, -nz] : [nx, nz];
}
// 沿墙走 s 远的那个点
function dcLineAt(line, s) {
  s = Math.max(0, Math.min(line.len, s));
  let i = 0; while (i + 2 < line.pts.length && line.cum[i + 1] < s) { i++; }
  const a = line.pts[i], b = line.pts[i + 1], seg = line.cum[i + 1] - line.cum[i] || 1, t = (s - line.cum[i]) / seg;
  return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t];
}
// 物件在墙上占的区间 [s0, s1](沿墙, 地板砖)
function dcShapedSpan(line, o) { const s = (o.t || 0) * line.len, h = (DC_OBJ[o.kind] || { w: 1 }).w / 2; return [s - h, s + h]; }
function dcShapedFits(room, o) {
  const c = dcShapedCut(room, o.wall), k = DC_OBJ[o.kind];
  if (!c || !k) { return false; }
  const line = dcShapedLine(c), sp = dcShapedSpan(line, o);
  return o.t >= 0 && o.t <= 1 && sp[0] >= -0.05 && sp[1] <= line.len + 0.05;
}
// 挂在斜墙 / 弧墙上的姿态: 物件两头贴墙(弧墙上按弦放), 背面朝墙; 返回物件中心(地板砖坐标)和 rotation.y
function dcShapedPose(room, o) {
  const c = dcShapedCut(room, o.wall), k = DC_OBJ[o.kind];
  if (!c || !k) { return null; }
  const line = dcShapedLine(c), sp = dcShapedSpan(line, o), p0 = dcLineAt(line, sp[0]), p1 = dcLineAt(line, sp[1]);
  const dx = p1[0] - p0[0], dz = p1[1] - p0[1], L = Math.hypot(dx, dz) || 1;
  let mx = (p0[0] + p1[0]) / 2, mz = (p0[1] + p1[1]) / 2;
  const n = dcLineInward(line, mx, mz, -dz / L, dx / L), nx = n[0], nz = n[1];
  // 往机房里鼓的弧墙(内凹弧)中间比弦更靠里: 物件整体往外挪, 背面贴着墙最鼓的地方, 不插进墙里
  let push = 0;
  for (let i = 1; i < 12; i++) { const p = dcLineAt(line, sp[0] + (sp[1] - sp[0]) * i / 12); push = Math.max(push, (p[0] - mx) * nx + (p[1] - mz) * nz); }
  mx += nx * push; mz += nz * push;
  return { x: mx + nx * k.d / 2, z: mz + nz * k.d / 2, rotY: Math.atan2(nx, nz) };
}
// 近似的格子位置 / 朝向(存进 x / z / rot, 只用来显示和找附近的墙)
function dcShapedApprox(room, o) {
  const p = dcShapedPose(room, o);
  if (!p) { return { x: 0, z: 0, rot: 0 }; }
  return { x: Math.floor(p.x), z: Math.floor(p.z), rot: ((Math.round(p.rotY * 2 / Math.PI) * 90) % 360 + 360) % 360 };
}
// 改墙之前: 按斜墙 / 弧墙上物件现在的实际位置刷新近似格子, 墙没了以后就在这附近找直墙
function dcObjFreezeApprox(room) {
  (DC.layout.objects || []).forEach((o) => { if (o.room === room.id && o.wall && dcShapedFits(room, o)) { Object.assign(o, dcShapedApprox(room, o)); } });
}
// 把 dcObjAt 找到的位置写进物件(斜墙 / 弧墙的带 wall / t, 直墙的去掉)
function dcObjSetPlace(o, n) {
  o.x = n.x; o.z = n.z; o.rot = n.rot;
  if (n.wall) { o.wall = n.wall; o.t = Math.round(n.t * 10000) / 10000; } else { delete o.wall; delete o.t; }
}
// 物件模型摆到场景里
function dcObjPlace(c, g, o) {
  if (o.wall) {
    const p = dcShapedPose(c.room, o);
    if (p) { g.position.set(c.ox + p.x * DC_TILE, 0, c.oz + p.z * DC_TILE); g.rotation.y = p.rotY; return; }
  }
  const f = dcObjFoot(o);
  g.position.set(c.ox + (o.x + f.fx / 2) * DC_TILE, 0, c.oz + (o.z + f.fz / 2) * DC_TILE);
  g.rotation.y = (o.rot || 0) * Math.PI / 180;
}
// 凸多边形按半平面裁剪(f(p) >= 0 的一侧留下)
function dcClip(poly, f) {
  const out = [];
  for (let i = 0; i < poly.length; i++) {
    const a = poly[i], b = poly[(i + 1) % poly.length], fa = f(a), fb = f(b);
    if (fa >= 0) { out.push(a); }
    if ((fa >= 0) !== (fb >= 0)) { const t = fa / (fa - fb); out.push([a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t]); }
  }
  return out;
}
// 斜切 / 圆角范围里的一块地板被墙切剩的形状(凸多边形, 地板砖坐标)
function dcTileKept(c, x, z) {
  let poly = [[x, z], [x + 1, z], [x + 1, z + 1], [x, z + 1]];
  if (dcCutEdge(c)) { return dcEdgeTileKept(c, x, z); }
  const pq = dcCutPQ(c), P = pq[0], Q = pq[1];
  if (c.type === 'tri') {
    const A = [Q[0], P[1]], B = [P[0], Q[1]], side = (p) => (B[0] - A[0]) * (p[1] - A[1]) - (B[1] - A[1]) * (p[0] - A[0]), sq = side(Q);
    return dcClip(poly, (p) => side(p) * Math.sign(sq));
  }
  // 圆角: 用 64 边形近似椭圆, 逐边裁剪
  const M = 64, e = [];
  for (let i = 0; i < M; i++) { const t = i / M * Math.PI * 2; e.push([Q[0] + c.w * Math.cos(t), Q[1] + c.d * Math.sin(t)]); }
  for (let i = 0; i < M && poly.length; i++) {
    const a = e[i], b = e[(i + 1) % M];
    poly = dcClip(poly, (p) => (b[0] - a[0]) * (p[1] - a[1]) - (b[1] - a[1]) * (p[0] - a[0]));
  }
  return poly;
}
// 墙中间的弧切过的一块地板: 切剩的部分不一定是凸的(内凹弧), 沿墙方向分成 8 条, 每条按弧线切成一个四边形; 返回多边形数组
function dcEdgeTileKept(c, x, z) {
  const f = dcEdgeFrame(c), a = f.l(x, z), b = f.l(x + 1, z + 1);
  const s0 = Math.min(a[0], b[0]), s1 = Math.max(a[0], b[0]), t0 = Math.min(a[1], b[1]), t1 = Math.max(a[1], b[1]), N = 8, out = [];
  const cl = (t) => Math.max(t0, Math.min(t1, t));
  for (let i = 0; i < N; i++) {
    const sa = s0 + (s1 - s0) * i / N, sb = s0 + (s1 - s0) * (i + 1) / N, ta = cl(f.cutT(sa)), tb = cl(f.cutT(sb));
    if (ta >= t1 - 1e-6 && tb >= t1 - 1e-6) { continue; }
    let p = [f.w(sa, ta), f.w(sb, tb), f.w(sb, t1), f.w(sa, t1)];
    // 统一成俯视顺时针(跟普通地板块一样), 法线才朝上
    let ar = 0;
    for (let k = 0; k < 4; k++) { const u = p[k], v = p[(k + 1) % 4]; ar += u[0] * v[1] - v[0] * u[1]; }
    if (ar < 0) { p = p.reverse(); }
    out.push(p);
  }
  return out;
}
// 地板 + 架空层侧边 + 墙(门洞处只留门楣) + 腰线
function dcBuildShell(c, room, wallH) {
  const T = c.st.THREE, S = DC_TILE, ox = c.ox, oz = c.oz;
  // 地板: 普通地板每行连续的拼成长条; 斜墙 / 弧墙切过的地板按切剩的形状画; 合成一个网格, UV 按地板块算
  const pos = [], uv = [], nrm = [], idx = [];
  const poly = (pts) => {
    if (pts.length < 3) { return; }
    const n = pos.length / 3;
    pts.forEach((p) => { pos.push(ox + p[0] * S, 0, oz + p[1] * S); uv.push(p[0], p[1]); nrm.push(0, 1, 0); });
    // 顶点顺序: 俯视是顺时针(x 向右 z 向下), 法线朝上要反过来连
    for (let i = 1; i + 1 < pts.length; i++) { idx.push(n, n + i + 1, n + i); }
  };
  const shapedAt = (x, z) => (room.cuts || []).filter((cc) => dcShaped(cc) && dcCutHas(cc, x, z))[0];
  for (let z = 0; z < room.d; z++) {
    let x = 0;
    while (x < room.w) {
      if (dcHardOut(room, x, z)) { x++; continue; }
      const sc = shapedAt(x, z);
      if (sc) { const k = dcTileKept(sc, x, z); if (dcCutEdge(sc)) { k.forEach(poly); } else { poly(k); } x++; continue; }
      let x1 = x; while (x1 < room.w && !dcHardOut(room, x1, z) && !shapedAt(x1, z)) { x1++; }
      poly([[x, z], [x1, z], [x1, z + 1], [x, z + 1]]);
      x = x1;
    }
  }
  const fg = new T.BufferGeometry();
  fg.setAttribute('position', new T.Float32BufferAttribute(pos, 3));
  fg.setAttribute('uv', new T.Float32BufferAttribute(uv, 2));
  fg.setAttribute('normal', new T.Float32BufferAttribute(nrm, 3));
  fg.setIndex(idx);
  const ft = dcFloorTex(false).clone(); ft.needsUpdate = true; ft.repeat.set(1, 1); ft.userData.shared = false;
  const floor = new T.Mesh(fg, new T.MeshStandardMaterial({ map: ft, roughness: 0.62, metalness: 0.12, envMapIntensity: 0.6 }));
  floor.receiveShadow = true; floor.userData.floor = true;
  c.world.add(floor); c.floor = floor;
  // 墙
  const wallMat = new T.MeshStandardMaterial({ color: 0x2a3548, roughness: 0.85, side: T.FrontSide, envMapIntensity: 0.4 });
  const skirtMat = new T.MeshStandardMaterial({ color: 0x1e293b, roughness: 0.9, side: T.DoubleSide });
  const trimMat = new T.MeshBasicMaterial({ color: new T.Color(0x38bdf8).multiplyScalar(2.2), transparent: true, opacity: 0.8, side: T.FrontSide });
  // 一段墙: 中点 (px, pz), 长 len, 墙面朝向 rotY(墙面法线 = (sin, cos))
  const wall = (px, pz, len, rotY, doorH) => {
    const add = (h, y, mat, dz) => {
      const m = new T.Mesh(new T.PlaneGeometry(len, h), mat);
      m.position.set(px, y, pz); m.rotation.y = rotY; if (dz) { m.translateZ(dz); }
      m.receiveShadow = true; c.world.add(m); return m;
    };
    if (doorH) {
      if (wallH > doorH) { add(wallH - doorH, doorH + (wallH - doorH) / 2, wallMat); }
    } else {
      add(wallH, wallH / 2, wallMat);
      add(3, 60, trimMat, 0.5);
    }
    add(30, -15.5, skirtMat, -0.2);
  };
  dcWallSegments(room, (c.layout || {}).objects).forEach((sg) => {
    const len = (sg.a1 - sg.a0) * S, mid = (sg.a0 + sg.a1) / 2 * S;
    const rotY = { 0: 0, 180: Math.PI, 90: Math.PI / 2, 270: -Math.PI / 2 }[sg.dir];
    const px = sg.dir === 0 || sg.dir === 180 ? ox + mid : ox + sg.line * S, pz = sg.dir === 0 || sg.dir === 180 ? oz + sg.line * S : oz + mid;
    wall(px, pz, len, rotY, sg.door);
  });
  dcShapedWalls(room).forEach((w) => {
    // 这面墙上的门: 沿墙区间 + 门洞高度; 墙按"折线的点 + 门的两头"切成小段, 落在门区间里的只画门楣
    const doors = ((c.layout || {}).objects || []).filter((o) => o.room === room.id && o.kind === 'door' && o.wall === w.wall).map((o) => { const sp = dcShapedSpan(w, o); return { a: sp[0], b: sp[1], h: o.h || DC_DOOR_H }; });
    const cuts = w.cum.slice();
    doors.forEach((d) => { cuts.push(Math.max(0, d.a), Math.min(w.len, d.b)); });
    cuts.sort((p, q) => p - q);
    for (let i = 0; i + 1 < cuts.length; i++) {
      const s0 = cuts[i], s1 = cuts[i + 1];
      if (s1 - s0 < 1e-4) { continue; }
      const a = dcLineAt(w, s0), b = dcLineAt(w, s1), mx = (a[0] + b[0]) / 2, mz = (a[1] + b[1]) / 2, dx = b[0] - a[0], dz = b[1] - a[1];
      const [nx, nz] = dcLineInward(w, mx, mz, -dz, dx);
      const dr = doors.filter((d) => (s0 + s1) / 2 > d.a && (s0 + s1) / 2 < d.b)[0];
      // 弧墙每段稍微加长一点, 段与段之间不留缝
      wall(ox + mx * S, oz + mz * S, Math.hypot(dx, dz) * S + (w.pts.length > 2 ? 0.8 : 0), Math.atan2(nx, nz), dr ? dr.h : 0);
    }
  });
}
function dcCornerName(k) { return { nw: '西北角', ne: '东北角', sw: '西南角', se: '东南角', n: '北墙', s: '南墙', w: '西墙', e: '东墙' }[k]; }
// 物件挂的那面斜墙 / 弧墙叫什么: 「东北角的弧墙」「北墙中间的内凹弧墙」
function dcWallName(room, wall) {
  const c = dcShapedCut(room, wall);
  if (!c) { return '斜墙 / 弧墙'; }
  if (dcCutEdge(c)) { return dcCornerName(c.corner) + '中间的' + (c.type === 'bulge' ? '外凸弧墙' : '内凹弧墙'); }
  return dcCornerName(c.corner) + '的' + (c.type === 'arc' ? '弧墙' : '斜墙');
}
// 这块挖掉区域正好在机房的哪些角上(只有在角上才能斜切 / 圆角)
function dcCutCorners(room, c) {
  return ['nw', 'ne', 'sw', 'se'].filter((k) => ({ nw: c.x === 0 && c.z === 0, ne: c.x + c.w === room.w && c.z === 0, sw: c.x === 0 && c.z + c.d === room.d, se: c.x + c.w === room.w && c.z + c.d === room.d })[k]);
}
// 这块挖掉区域贴着哪几面外墙(贴着的才能做成墙中间的弧; 横跨整个机房的不行, 两头没有墙可接)
function dcCutSides(room, c) {
  return ['n', 's', 'w', 'e'].filter((k) => ({ n: c.z === 0 && c.w < room.w, s: c.z + c.d === room.d && c.w < room.w, w: c.x === 0 && c.d < room.d, e: c.x + c.w === room.w && c.d < room.d })[k]);
}
function dcCutHTML(room) {
  const cuts = room.cuts || [];
  const shapeSel = (c, i) => {
    const corners = dcCutCorners(room, c), sides = dcCutSides(room, c), cur = dcShaped(c) ? c.type + ':' + c.corner : '';
    if (!corners.length && !sides.length) { return ''; }
    const opts = [['', '矩形']].concat(...corners.map((k) => [['tri:' + k, '斜切 · ' + dcCornerName(k)], ['arc:' + k, '圆弧 · ' + dcCornerName(k)]]),
      ...sides.map((k) => [['arc:' + k, '内凹弧 · ' + dcCornerName(k) + '中间'], ['bulge:' + k, '外凸弧 · ' + dcCornerName(k) + '中间']]));
    return ' <select class="dc-cutsel" onchange="dcCutShape(' + i + ', this.value)" title="机房角上的这块可以做成斜墙或弧形墙; 贴着外墙的这块可以做成墙中间往里鼓 / 往外鼓的弧形墙">' +
      opts.map((o) => '<option value="' + o[0] + '"' + (o[0] === cur ? ' selected' : '') + '>' + o[1] + '</option>').join('') + '</select>';
  };
  return '<div class="sub" style="margin-top:8px;">机房形状: ' + (cuts.length ? '挖掉了 ' + cuts.length + ' 块' : '矩形') + '</div>' +
    (cuts.length ? '<table>' + cuts.map((c, i) => '<tr><td>' + (c.type === 'tri' ? '◸' : c.type === 'bulge' ? '◠' : c.type === 'arc' ? (dcCutEdge(c) ? '◡' : '◜') : '✂') + ' (' + c.x + ', ' + c.z + ') ' + (c.w * 0.6).toFixed(1) + ' × ' + (c.d * 0.6).toFixed(1) + ' m' + shapeSel(c, i) + '</td>' +
      '<td style="text-align:right;"><button class="dc-nudge" onclick="dcCutDelete(' + i + ')" title="把这块补回来">✕</button></td></tr>').join('') + '</table>' : '') +
    '<div class="btns"><button onclick="dcStartPlace(&quot;cut&quot;)" title="在地板上点两下, 框出要挖掉的矩形(L 形机房缺的那个角、中间的核心筒); 框在机房角上的可以再改成斜切 / 圆弧, 贴着外墙的可以改成墙中间的弧">✂ 框选挖掉一块</button></div>' +
    '<div class="sub faint">框在机房角上的那块可以改成「斜切」(一面斜墙)或「圆弧」(弧形墙), 两个角都做成圆弧就是半圆形的一头。' +
    '贴着一面外墙框一块, 可以改成墙中间的「内凹弧」(墙往机房里鼓进来)或「外凸弧」(墙往外鼓出去; 两边再各挖掉一块, 就是半圆形的凸出部分)。门 / 大屏 / 摄像头也能挂在斜墙和弧墙上。</div>';
}
function dcCutShape(i, v) {
  const room = dcRoomOf(DC.layout, DC.room), c = room.cuts[i], old = { type: c.type, corner: c.corner, id: c.id }, oldWall = dcShaped(c) ? dcCutWall(c) : '';
  const restore = () => { ['type', 'corner', 'id'].forEach((k) => { if (old[k]) { c[k] = old[k]; } else { delete c[k]; } }); };
  dcObjFreezeApprox(room);
  if (v) {
    const p = v.split(':');
    if ((room.cuts || []).some((o, j) => j !== i && o.x < c.x + c.w && c.x < o.x + o.w && o.z < c.z + c.d && c.z < o.z + o.d)) { dcMsg('这块跟别的挖掉区域重叠了, 不能做成斜切 / 圆弧', true); dcRenderSide(); return; }
    c.type = p[0]; c.corner = p[1];
    // 墙中间的弧: 给一个编号当墙的名字(挂在上面的物件靠它找墙)
    if (p[1].length === 1) { c.id = c.id || 'arc' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6); } else { delete c.id; }
  } else { delete c.type; delete c.corner; delete c.id; }
  // 挂在这面斜墙 / 弧墙上的东西: 形状变了重新找位置
  const saved = JSON.stringify(DC.layout.objects), newWall = dcShaped(c) ? dcCutWall(c) : '';
  dcRoomContent(room).objs.filter((o) => o.wall && (o.wall === oldWall || o.wall === newWall)).forEach((o) => dcObjResnap(room, o));
  // 改回矩形 = 整块挖掉, 原来斜墙内侧放的东西就在墙外了
  const ct = dcRoomContent(room), inRoom = (cc) => dcIn(room, cc[0], cc[1]);
  const bad = ct.racks.some((k) => !dcCells(k).every(inRoom)) || ct.aisles.some((a) => !dcAisleCells(a).every(inRoom)) ||
    ct.objs.some((o) => (DC_OBJ[o.kind] || {}).wall ? !dcObjOnWall(room, o) : !dcObjCells(o).every(inRoom));
  if (bad) {
    restore();
    DC.layout.objects = JSON.parse(saved);
    dcMsg('墙内侧放了东西, 先挪开再改形状', true); dcRenderSide(); return;
  }
  dcMarkDirty(); dcRefresh(false);
}
function dcCutDelete(i) {
  const room = dcRoomOf(DC.layout, DC.room);
  dcObjFreezeApprox(room);
  room.cuts.splice(i, 1);
  if (!room.cuts.length) { delete room.cuts; }
  dcRoomContent(room).objs.forEach((o) => dcObjResnap(room, o));
  dcMarkDirty(); dcRefresh(false);
}
// 框选的第二下: 检查框里没有东西、不压着斜切 / 圆弧, 然后挖掉
function dcCutAdd(a, b) {
  const room = dcRoomOf(DC.layout, DC.room), cl = (v, n) => Math.max(0, Math.min(n - 1, v));
  const r = { x: cl(Math.min(a.x, b.x), room.w), z: cl(Math.min(a.z, b.z), room.d) };
  r.w = cl(Math.max(a.x, b.x), room.w) - r.x + 1; r.d = cl(Math.max(a.z, b.z), room.d) - r.z + 1;
  const hit = (c) => c[0] >= r.x && c[0] < r.x + r.w && c[1] >= r.z && c[1] < r.z + r.d;
  const ct = dcRoomContent(room);
  if (ct.racks.some((k) => dcCells(k).some(hit)) || ct.aisles.some((a2) => dcAisleCells(a2).some(hit)) || ct.objs.some((o) => dcObjCells(o).some(hit))) {
    dcMsg('框里还有机柜 / 冷通道 / 物件, 先挪开再挖', true); return false;
  }
  if ((room.cuts || []).some((o) => dcShaped(o) && o.x < r.x + r.w && r.x < o.x + o.w && o.z < r.z + r.d && r.z < o.z + o.d)) {
    dcMsg('框跟斜切 / 圆弧那块重叠了', true); return false;
  }
  const test = { w: room.w, d: room.d, cuts: (room.cuts || []).concat([r]) };
  if (dcRoomArea(test) < 16) { dcMsg('挖掉太多了, 剩下的地板不到 16 块', true); return false; }
  room.cuts = test.cuts;
  dcMarkDirty(); dcRefresh(false);
  dcMsg('已挖掉 ' + (r.w * 0.6).toFixed(1) + ' × ' + (r.d * 0.6).toFixed(1) + ' m' + (dcCutCorners(room, r).length ? ', 在机房角上, 可以在右边改成斜切 / 圆弧' : dcCutSides(room, r).length ? ', 贴着外墙, 可以在右边改成墙中间的内凹弧 / 外凸弧' : ', 墙会沿新的边界生成'));
  return true;
}
// 改尺寸 / 收缩后: 斜切 / 圆弧跟着它的角走, 其他的裁到机房范围里
function dcCutClip(room) {
  if (!room.cuts) { return; }
  room.cuts = room.cuts.map((c) => {
    if (dcShaped(c) && dcCutEdge(c)) {
      // 墙中间的弧: 贴着它那面墙走, 沿墙方向裁到机房范围里
      const n = Object.assign({}, c);
      n.w = Math.min(c.w, room.w); n.d = Math.min(c.d, room.d);
      n.x = c.corner === 'w' ? 0 : c.corner === 'e' ? room.w - n.w : Math.max(0, Math.min(room.w - n.w, c.x));
      n.z = c.corner === 'n' ? 0 : c.corner === 's' ? room.d - n.d : Math.max(0, Math.min(room.d - n.d, c.z));
      return n;
    }
    if (dcShaped(c)) {
      const n = Object.assign({}, c);
      n.w = Math.min(c.w, room.w); n.d = Math.min(c.d, room.d);
      n.x = c.corner === 'ne' || c.corner === 'se' ? room.w - n.w : 0;
      n.z = c.corner === 'sw' || c.corner === 'se' ? room.d - n.d : 0;
      return n;
    }
    const x0 = Math.max(0, c.x), z0 = Math.max(0, c.z), x1 = Math.min(room.w, c.x + c.w), z1 = Math.min(room.d, c.z + c.d);
    return { x: x0, z: z0, w: x1 - x0, d: z1 - z0 };
  }).filter((c) => c.w > 0 && c.d > 0);
  if (!room.cuts.length) { delete room.cuts; }
}
