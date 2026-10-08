/*
 * 「👥 账号权限」窗口(只有管理员能打开)+ 按权限置灰顶栏按钮。后端见 users.go。
 */
/* ================= 账号权限 ================= */
var USR = { list: [], perms: [], me: '', cur: null };
var USR_PRESETS = [
  ['值班', ['alerts']],
  ['运维', ['devices', 'alerts', 'topology', 'datacenter', 'backup_view']],
  ['网络管理员', ['devices', 'alerts', 'topology', 'datacenter', 'backup_view', 'backup', 'reports']],
  ['全部', null], ['清空', []]
];
function uiToast(msg, kind) {
  var t = byId('uiToast'); if (!t) { return; }
  t.innerText = msg; t.className = 'ui-toast show ' + (kind || '');
  clearTimeout(t._h); t._h = setTimeout(function () { t.className = 'ui-toast ' + (kind || ''); }, 3200);
}
function permName(k) {
  var names = { devices: '设备管理', alerts: '告警处置', topology: '拓扑编辑', datacenter: '机房 / 机柜', backup_view: '查看配置备份', backup: '执行配置备份', reports: '巡检报告', settings: '系统设置' };
  return names[k] || k;
}
// 顶栏上没权限的按钮置灰; 点了给个提示, 不再弹一个注定 403 的窗口
function applyPermUI() {
  var ub = byId('usersBtn'); if (ub) { ub.style.display = authenticated && userRole === 'admin' ? '' : 'none'; }
  document.querySelectorAll('[data-perm]').forEach(function (b) {
    var p = b.getAttribute('data-perm'), deny = authenticated && p !== '*' && !can(p);
    b.classList.toggle('noperm', deny);
    if (deny) { b.setAttribute('title', '当前账号没有「' + permName(p) + '」权限'); } else { b.removeAttribute('title'); }
  });
}
document.addEventListener('click', function (e) {
  var b = e.target.closest && e.target.closest('[data-perm].noperm');
  if (!b) { return; }
  e.preventDefault(); e.stopPropagation();
  uiToast('当前账号没有「' + permName(b.getAttribute('data-perm')) + '」权限, 请联系管理员分配', 'err');
}, true);

function openUsers() {
  if (!authenticated) { openAuthGate(function () { openUsers(); }); return; }
  if (userRole !== 'admin') { uiToast('只有管理员可以管理账号', 'err'); return; }
  byId('usersModal').style.display = 'flex';
  byId('usrBody').innerHTML = '<tr><td colspan="6" class="empty">加载中...</td></tr>';
  usrLoad().then(function () { usrEdit(null); });
}
function closeUsers() { byId('usersModal').style.display = 'none'; }
function usrLoad() {
  return authedApi('/api/users').then(function (d) {
    USR.list = d.users || []; USR.perms = d.perms || []; USR.me = d.me || ''; USR.guest = d.guest || { enabled: true, views: [] }; USR.views = d.views || [];
    usrRenderList(); usrRenderGuest();
  })
    .catch(function (e) { byId('usrBody').innerHTML = '<tr><td colspan="6" class="empty">加载失败: ' + esc(String(e)) + '</td></tr>'; });
}
function usrRenderList() {
  byId('usrCount').innerText = '共 ' + USR.list.length + ' 个账号';
  byId('usrBody').innerHTML = USR.list.map(function (u, i) {
    var perms = u.role === 'admin' ? '<span class="usr-chip admin">👑 管理员 · 全部权限</span>'
      : (u.perms.length ? u.perms.map(function (p) { return '<span class="usr-chip">' + esc(permName(p)) + '</span>'; }).join('') : '<span class="usr-chip">只能登录, 无写权限</span>');
    var me = u.user.toLowerCase() === USR.me.toLowerCase();
    return '<tr class="' + (USR.cur && USR.cur.user === u.user ? 'cur' : '') + '"><td><b>' + esc(u.user) + '</b>' + (me ? ' <span class="usr-chip">我</span>' : '') + '</td><td>' + perms + '</td>' +
      '<td>' + (u.disabled ? '<span class="usr-chip off">已停用</span>' : '<span style="color:var(--good);">● 启用</span>') + '</td>' +
      '<td style="font-size:12px;color:var(--fg-dim);">' + esc(u.last_login || '从未') + '</td><td style="font-size:12.5px;">' + esc(u.note || '') + '</td>' +
      '<td style="white-space:nowrap;"><button class="filter-btn" onclick="usrEdit(' + i + ')">编辑</button>' + (me ? '' : ' <button class="filter-btn" style="color:var(--bad);" onclick="usrDelete(' + i + ')">删除</button>') + '</td></tr>';
  }).join('');
}
function usrEdit(i) {
  var u = i === null ? null : USR.list[i];
  USR.cur = u ? JSON.parse(JSON.stringify(u)) : { user: '', role: 'user', perms: ['alerts'], disabled: false, note: '', isNew: true };
  usrRenderList(); usrRenderForm();
}
function usrRenderForm() {
  var u = USR.cur, isNew = !!u.isNew, me = !isNew && u.user.toLowerCase() === USR.me.toLowerCase();
  var h = '<h4>' + (isNew ? '＋ 新建账号' : '编辑账号 · ' + esc(u.user)) + '</h4>' +
    '<label class="t">用户名</label><input type="text" id="usrName" value="' + esc(u.user) + '"' + (isNew ? ' placeholder="2-32 位, 字母/数字/汉字/_.-@"' : ' readonly') + '>' +
    '<label class="t">' + (isNew ? '初始口令(至少 8 位)' : '重置口令(留空 = 不改)') + '</label><input type="password" id="usrPass" autocomplete="new-password" placeholder="' + (isNew ? '' : '不改就留空') + '">' +
    '<label class="t">角色</label><div class="usr-roles">' +
    '<label class="' + (u.role === 'admin' ? 'on' : '') + '"><input type="radio" name="usrRole" value="admin"' + (u.role === 'admin' ? ' checked' : '') + (me ? ' disabled' : '') + ' onchange="usrSetRole(this.value)"> 👑 管理员<small>全部权限 + 管理账号</small></label>' +
    '<label class="' + (u.role !== 'admin' ? 'on' : '') + '"><input type="radio" name="usrRole" value="user"' + (u.role !== 'admin' ? ' checked' : '') + (me ? ' disabled' : '') + ' onchange="usrSetRole(this.value)"> 🙂 子账号<small>只有勾选的权限</small></label></div>';
  if (u.role !== 'admin') {
    h += '<label class="t">权限</label><div class="usr-presets">' + USR_PRESETS.map(function (p, i) { return '<button onclick="usrPreset(' + i + ')">' + p[0] + '</button>'; }).join('') + '</div><div class="usr-perms">' +
      USR.perms.map(function (p) {
        return '<label class="usr-perm" title="' + esc(p.desc) + '"><input type="checkbox" value="' + p.key + '"' + (u.perms.indexOf(p.key) >= 0 ? ' checked' : '') + ' onchange="usrTogglePerm(this)"><span>' + esc(p.name) + '<small>' + esc(p.desc) + '</small></span></label>';
      }).join('') + '</div>';
  }
  h += '<label class="t">备注</label><input type="text" id="usrNote" value="' + esc(u.note || '') + '" maxlength="60" placeholder="比如: 张三 · 值班组">' +
    '<label class="usr-perm" style="border:0;margin-top:6px;"><input type="checkbox" id="usrDisabled"' + (u.disabled ? ' checked' : '') + (me ? ' disabled' : '') + '><span>停用这个账号<small>停用后不能登录, 已登录的立即下线; 可以随时再启用</small></span></label>' +
    '<div style="display:flex;gap:8px;margin-top:12px;"><button class="hbtn" style="flex:1;" onclick="usrSave()">' + (isNew ? '创建' : '保存') + '</button>' + (isNew ? '' : '<button class="hbtn ghost" style="color:var(--fg);border:1px solid var(--card-border);" onclick="usrEdit(null)">取消</button>') + '</div>' +
    '<div class="notice" id="usrMsg" style="margin-top:10px;"></div>';
  byId('usrForm').innerHTML = h;
}
function usrSyncForm() {
  var u = USR.cur; if (!u) { return; }
  if (byId('usrName') && u.isNew) { u.user = byId('usrName').value; }
  if (byId('usrNote')) { u.note = byId('usrNote').value; }
  if (byId('usrDisabled')) { u.disabled = byId('usrDisabled').checked; }
}
function usrSetRole(r) { usrSyncForm(); USR.cur.role = r; usrRenderForm(); }
function usrTogglePerm(el) {
  var p = USR.cur.perms, i = p.indexOf(el.value);
  if (el.checked && i < 0) { p.push(el.value); } else if (!el.checked && i >= 0) { p.splice(i, 1); }
}
function usrPreset(i) {
  usrSyncForm();
  var ps = USR_PRESETS[i][1];
  USR.cur.perms = ps === null ? USR.perms.map(function (p) { return p.key; }) : ps.slice();
  usrRenderForm();
}
function usrSave() {
  usrSyncForm();
  var u = USR.cur, msg = byId('usrMsg');
  var body = { user: u.user.trim(), 'new': !!u.isNew, password: byId('usrPass').value, role: u.role, perms: u.role === 'admin' ? [] : u.perms, disabled: !!u.disabled, note: (u.note || '').trim() };
  if (body['new'] && body.password.length < 8) { msg.className = 'notice notice-err'; msg.innerText = '初始口令至少 8 位'; msg.style.display = 'block'; return; }
  authedApi('/api/users/save', body).then(function (d) {
    USR.list = d.users || USR.list;
    var idx = -1; USR.list.forEach(function (x, i) { if (x.user.toLowerCase() === body.user.toLowerCase()) { idx = i; } });
    uiToast((body['new'] ? '已创建账号 ' : '已保存 ') + body.user, 'ok');
    if (idx >= 0) { usrEdit(idx); } else { usrEdit(null); }
  }).catch(function (e) { msg.className = 'notice notice-err'; msg.innerText = '❌ ' + e; msg.style.display = 'block'; });
}
function usrDelete(i) {
  var u = USR.list[i];
  if (!confirm('删除账号「' + u.user + '」? 它当前的登录会立即失效, 删除后不能恢复。')) { return; }
  authedApi('/api/users/delete', { user: u.user }).then(function (d) { USR.list = d.users || []; uiToast('已删除 ' + u.user, 'ok'); usrEdit(null); })
    .catch(function (e) { uiToast('删除失败: ' + e, 'err'); });
}
modalRegister('usersModal', closeUsers);
App.on('session', applyPermUI);

/* ---------- 游客访问(未登录能看什么) ---------- */
function usrRenderGuest() {
  var g = USR.guest || { enabled: true, views: [] }, on = {};
  (g.views || []).forEach(function (v) { on[v] = true; });
  byId('usrGuest').innerHTML = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;"><b>🙈 游客访问(未登录)</b>' +
    '<label style="font-size:13px;display:inline-flex;gap:4px;align-items:center;"><input type="checkbox" id="usrGuestOn" style="width:auto;"' + (g.enabled ? ' checked' : '') + ' onchange="usrGuestToggle()"> 允许不登录查看</label>' +
    '<span class="grow"></span><button class="filter-btn" onclick="usrGuestPreset(\x27all\x27)">全部</button><button class="filter-btn" onclick="usrGuestPreset(\x27screen\x27)">只看大屏</button><button class="filter-btn" onclick="usrGuestPreset(\x27basic\x27)">不含终端信息</button>' +
    '<button class="hbtn" onclick="usrGuestSave()">保存</button></div>' +
    '<div class="views">' + USR.views.map(function (v) {
      var fixed = v.key === 'home';
      return '<label title="' + esc(v.desc) + '"' + (g.enabled ? '' : ' class="off"') + '><input type="checkbox" data-gv="' + esc(v.key) + '"' + (on[v.key] || fixed ? ' checked' : '') + (fixed || !g.enabled ? ' disabled' : '') + '> ' + esc(v.name) + '</label>';
    }).join('') + '</div>' +
    '<div class="hint" style="font-size:12px;color:var(--fg-dim);">关掉 = 打开页面必须先登录; 登录后的账号查看不受限制, 写操作仍按下面每个账号的权限。「终端信息」不勾时, 游客看设备详情里的 IP-MAC-端口 / MAC 表是空的。</div>';
}
function usrGuestToggle() { USR.guest.enabled = byId('usrGuestOn').checked; USR.guest.views = usrGuestPicked(); usrRenderGuest(); }
function usrGuestPicked() {
  var out = [];
  document.querySelectorAll('#usrGuest [data-gv]').forEach(function (c) { if (c.checked) { out.push(c.getAttribute('data-gv')); } });
  return out;
}
function usrGuestPreset(k) {
  var all = USR.views.map(function (v) { return v.key; });
  USR.guest.enabled = true;
  USR.guest.views = k === 'all' ? all : k === 'screen' ? ['home', 'bigscreen'] : all.filter(function (v) { return ['hosts', 'mactable', 'arptable'].indexOf(v) < 0; });
  usrRenderGuest();
}
function usrGuestSave() {
  var body = { enabled: byId('usrGuestOn').checked, views: usrGuestPicked() };
  authedApi('/api/users/guest', body).then(function (d) {
    USR.guest = d.guest; usrRenderGuest();
    uiToast(d.guest.enabled ? '已保存: 游客可以看 ' + d.guest.views.length + ' 类页面' : '已关闭游客访问, 不登录看不到任何数据', 'ok');
  }).catch(function (e) { uiToast('保存失败: ' + e, 'err'); });
}
