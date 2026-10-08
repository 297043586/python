/*
 * 修改口令。
 */
/* ---------- 修改口令 (需要登录) ---------- */
function openPassword() {
  if (!authenticated) {
    openAuthGate(function () { openPassword(); });
    return;
  }
  byId('pwModal').style.display = 'flex';
}
function closePassword() {
  byId('pwModal').style.display = 'none';
  byId('pwOld').value = ''; byId('pwNew').value = '';
  byId('pwNotice').style.display = 'none';
}
function savePassword() {
  var n = byId('pwNotice');
  authedApi('/api/password', { old: byId('pwOld').value, new: byId('pwNew').value }).then(function () {
    n.className = 'notice notice-ok'; n.innerText = '口令已更新'; n.style.display = 'block';
    setTimeout(closePassword, 1200);
  }).catch(function (e) {
    n.className = 'notice notice-err'; n.innerText = '修改失败: ' + e; n.style.display = 'block';
  });
}
modalRegister('pwModal', closePassword);
