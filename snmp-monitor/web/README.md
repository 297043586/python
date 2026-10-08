# 前端(web/)

整个页面是一个单页应用, 所有文件用 `go:embed` 编进 netmon.exe, 内网离线可用。

```
web/
  index.html           页面外壳; <!-- @include partials/xxx.html --> 启动时展开
  login.html           独立登录页
  manifest.json        CSS / JS 的加载顺序(漏写、多写都会启动报错, 单元测试也会拦)
  partials/*.html      各个弹窗 / 大屏的结构
  css/*.css            样式, 拼成 /assets/app.css
  js/**/*.js           脚本, 拼成 /assets/app.js(普通脚本, 共用一个全局作用域)
```

`/assets/app.css`、`/assets/app.js` 带内容指纹(`?v=`), 浏览器缓存一年, 升级后指纹变了自动重新拉。

## 开发

```
set NETMON_WEB_DIR=web      # PowerShell: $env:NETMON_WEB_DIR='web'
netmon.exe                  # 或者 go run .
```

设置了 `NETMON_WEB_DIR` 时每次打开首页都从磁盘重新读 web/, 改了 JS / CSS 刷新浏览器就生效, 不用重新编译。

演示服务(假数据, 6 台设备 + 2 万条 MAC + 30 天告警历史): `powershell -File _dev/demo.ps1`, 然后打开
http://127.0.0.1:18080 。`_dev/*.mjs` 是无头 Edge 驱动的场景脚本: `node _dev/drive.mjs _dev/s_smoke.mjs`,
`node _dev/jscheck.mjs` 做语法检查, `node _dev/refcheck.mjs` 检查页面里 onclick 调用的函数都存在。

## 约定

- **新文件要写进 manifest.json**。core/ 里的基础模块排在最前面, `js/app/boot.js` 永远最后。
- 顶层的 `const` / `let` / `class` 在所有文件之间共享一个作用域: 名字不能重复, 也不能在它所在的文件执行之前用到
  (函数声明不受这个限制)。
- 新代码用 `const` / `let` / 箭头函数 / 模板字符串; 拼 HTML 用 `html\`...\``(插值自动转义, 见 core/util.js)。
- 颜色一律用 CSS 变量(css/theme.css), 所有主题和风格下都要能看。

## 基础模块(js/core/)

| 文件 | 用途 |
|---|---|
| events.js | `App.on / App.emit` 事件总线: `summary`(首页数据刷新) `detail`(设备详情刷新) `alert`(告警被确认/关闭) `session`(登录/权限变化) |
| modal.js | `modalRegister(id, close, { esc })`: 点遮罩关闭; Esc 只关最上面的窗口, 窗口里的下拉 / 抽屉 / 菜单先关 |
| base.js | 转义、主题、网络封装(`pub` 公开只读 / `authedApi` 需要登录, 401 时弹登录框后自动重放) |
| util.js | `html\`\`` 转义模板、`debounce`、`prefs`(记在浏览器里的界面偏好)、时间 / 时长格式化、CSV 导出 |
| vtable.js | `VTable` 虚拟滚动表格: 只画看得见的几十行, 支持排序、分组折叠、行内按钮、滚到底加载更多 |
| fullscreen.js | `fsButton(el, host)` 全屏按钮(Fullscreen API, 用不了时退回铺满窗口) |
| multiselect.js | 多选筛选下拉 |

## 功能模块怎么接进来

- **设备详情的标签页**: `detailPart({ fill(d) })` 每次拿到数据调; `detailPart({ tab: 9, show(d) })` 切到那一页时调。
  打开详情并直达某页: `openModal(ip, 9)`。
- **拓扑**: 三个渲染器(2D / 3D Canvas 兼容模式 / 3D WebGL)由 `js/topology/view.js` 分派,
  对外只用 `renderTopology / topoZoom / topoResetView / topoHighlight / closeTopology / topoRemount`。
- **3D**: `js/3d/gl.js` 的 `glStage` 是舞台(渲染器 / 相机 / 控制 / 画质自适应), `gl-fx.js` 是泛光、环境反射、
  高亮框、标签远近淡出、浮动工具条。3D 拓扑和 3D 机房、大屏都用它们。
