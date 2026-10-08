/*
 * 启动(放在所有模块之后执行)。
 */
// 表格列宽拖拽
['portsTable', 'arpTable', 'flowTopTable', 'flowRecTable'].forEach(initResizableColumns);
// Esc 关最上面的窗口: 最后注册, 各模块自己的 Esc 处理(关菜单 / 下拉 / 抽屉)都先执行过了, 见 core/modal.js
window.addEventListener('keydown', modalEscape);
refreshSession().then(refreshNow);
