/*
 * 全局状态 / 转义 / 主题 / 网络封装 / 登录状态。
 *
 * ----------------- 登录页 -----------------
 *
 * 只在直接访问 /login 时用得到(比如收藏了这个地址)。日常登录走的是
 * 仪表盘页面右上角弹出的内嵌登录框, 不需要跳转离开仪表盘。
 */
/* ---------- 全局状态 ---------- */
var summaryData = [];
var currentDetail = null, currentIP = null, currentTab = 0;
var deviceList = [];               /* 设备管理弹窗里的列表(需要登录) */
var editingID = '';
var authenticated = false, currentUser = '', csrfToken = '', userRole = '', userPerms = {};
// can('devices') 等: 当前账号有没有某类写操作的权限(后端每个接口还会再校验一次)
function can(p) { return authenticated && (userRole === 'admin' || !!userPerms[p]); }
// 游客(未登录)能看哪些页面: 管理员在「👥 账号权限 → 游客访问」里设置, 见后端 guest.go
var guestPolicy = { enabled: true, views: null };
function canView(k) {
  if (authenticated) { return true; }
  if (!guestPolicy.enabled) { return false; }
  return !guestPolicy.views || guestPolicy.views.indexOf(k) >= 0;
}
// 打开某个查看类页面前调用: 游客没开放时给提示并引导登录, 返回 false 表示别继续打开
function viewGate(k, reopen) {
  if (canView(k)) { return true; }
  openAuthGate(reopen || null);
  return false;
}
var gateResolve = null;
var portFilters = {}, sortField = 'name', sortAsc = true;

function esc(v) {
  if (v === null || v === undefined) { return ''; }
  return String(v).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
                  .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
function byId(id) { return document.getElementById(id); }

/* ---------- 主题 ---------- */
function setTheme(name) {
  document.body.className = name;
  try { localStorage.setItem('theme', name); } catch (e) {}
}
(function initTheme() {
  var saved = 'theme-light';
  try { saved = localStorage.getItem('theme') || 'theme-light'; } catch (e) {}
  document.body.className = saved;
  byId('themeSelect').value = saved;
})();

/* ---------- 网络封装 ----------
 * pub(): 公开只读接口, 不需要登录。
 * authedApi(): 会改数据的接口。收到 401 时弹出内嵌登录框, 登录成功后
 * 自动重放这次请求, 调用方感知不到"中途还登录了一次"。
 */
function pub(path) {
  return fetch(path, { cache: 'no-store' }).then(function (r) {
    return r.json().then(function (d) {
      if (!r.ok) { return Promise.reject(d.error || ('HTTP ' + r.status)); }
      return d;
    });
  });
}
function rawApi(path, body) {
  var opt = { headers: {}, cache: 'no-store' };
  if (body !== undefined) {
    opt.method = 'POST';
    opt.headers['Content-Type'] = 'application/json';
    opt.headers['X-CSRF-Token'] = csrfToken;
    opt.body = JSON.stringify(body);
  }
  return fetch(path, opt).then(function (r) {
    return r.json().then(function (d) { return { status: r.status, ok: r.ok, data: d }; });
  });
}
function authedApi(path, body) {
  return rawApi(path, body).then(function (res) {
    if (res.status === 401) {
      return new Promise(function (resolve, reject) {
        openAuthGate(function () {
          rawApi(path, body).then(function (res2) {
            if (res2.ok) { resolve(res2.data); } else { reject(res2.data.error || ('HTTP ' + res2.status)); }
          }).catch(function () { reject('网络错误'); });
        }, function () { reject('已取消登录'); });
      });
    }
    if (!res.ok) { return Promise.reject(res.data.error || ('HTTP ' + res.status)); }
    return res.data;
  });
}

/* ---------- 登录状态 ---------- */
function refreshSession() {
  return pub('/api/session').then(function (s) {
    authenticated = !!s.authenticated;
    currentUser = s.user || '';
    csrfToken = s.csrf || '';
    userRole = s.role || ''; userPerms = s.perms || {};
    if (s.guest) { guestPolicy = s.guest; }
    updateAuthUI();
  }).catch(function () { authenticated = false; userRole = ''; userPerms = {}; updateAuthUI(); });
}
function updateAuthUI() {
  var box = byId('whoamiBox'), btn = byId('authBtn');
  if (authenticated) {
    box.innerText = '已登录: ' + currentUser + (userRole === 'admin' ? ' (管理员)' : '');
    btn.innerText = '退出';
    btn.onclick = doLogout;
  } else {
    box.innerText = guestPolicy.enabled ? '游客模式(未登录)' : '未登录(游客访问已关闭)';
    btn.innerText = '登录';
    btn.onclick = function () { openAuthGate(null); };
  }
  // data-auth: 只有登录后才有意义的按钮(立即巡检 / 修改口令 ...), 未登录时直接隐藏
  document.querySelectorAll('[data-auth]').forEach(function (b) { b.style.display = authenticated ? '' : 'none'; });
  // data-view: 查看类页面入口, 游客没开放的直接隐藏
  document.querySelectorAll('[data-view]').forEach(function (b) { b.style.display = canView(b.getAttribute('data-view')) ? '' : 'none'; });
  var gl = byId('guestLock');
  if (gl) { gl.style.display = !authenticated && !guestPolicy.enabled ? 'flex' : 'none'; }
  App.emit('session');
}
function doLogout() {
  authenticated = false; currentUser = ''; csrfToken = ''; userRole = ''; userPerms = {};
  updateAuthUI();
  // 等会话真的注销了, 再按游客策略重新拉一次(游客访问关闭时会出现登录遮罩)
  pub('/api/logout').catch(function () {}).then(refreshSession).then(function () {
    if (canView('home')) { refreshNow(); } else { summaryData = []; App.emit('summary', summaryData); }
  });
}

function openAuthGate(onSuccess, onCancel) {
  gateResolve = { onSuccess: onSuccess, onCancel: onCancel };
  byId('gateNotice').style.display = 'none';
  byId('gateUser').value = ''; byId('gatePass').value = '';
  byId('authModal').style.display = 'flex';
  byId('gateUser').focus();
}
function closeAuthGate() {
  byId('authModal').style.display = 'none';
  if (gateResolve && gateResolve.onCancel) { gateResolve.onCancel(); }
  gateResolve = null;
}
function doGateLogin() {
  var n = byId('gateNotice');
  n.style.display = 'none';
  fetch('/api/login', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user: byId('gateUser').value, password: byId('gatePass').value })
  }).then(function (r) { return r.json().then(function (d) { return { ok: r.ok, d: d }; }); })
    .then(function (res) {
      if (!res.ok) {
        n.className = 'notice notice-err'; n.innerText = res.d.error || '登录失败'; n.style.display = 'block';
        return;
      }
      return refreshSession().then(function () {
        byId('authModal').style.display = 'none';
        var cb = gateResolve && gateResolve.onSuccess;
        gateResolve = null;
        if (cb) { cb(); }
      });
    }).catch(function () {
      n.className = 'notice notice-err'; n.innerText = '网络错误, 请重试'; n.style.display = 'block';
    });
}
document.addEventListener('keydown', function (e) {
  if (e.key === 'Enter' && byId('authModal').style.display === 'flex') { doGateLogin(); }
});
modalRegister('authModal', closeAuthGate);
