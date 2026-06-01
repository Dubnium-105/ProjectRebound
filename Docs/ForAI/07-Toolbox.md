# 07 — Toolbox (rust-boundary-tool-box)

> 来源：memory Session_Compact_2026-05-15_to_18（Docs 中无此内容）
> 最后更新：2026-05-18
> 注意：Toolbox 是同事维护的独立仓库，位于 `C:\STanJK\Development\Boundary\rust-boundary-tool-box\`

## 架构

```
src/
  main.rs — 入口，slint::include_modules!()
  core.rs — 常量、结构体、APP_VERSION、PROJECT_REBOUND_*、MANAGED_ITEMS、LaunchFiles
  core/
    font.rs — UI 字体检测/下载/安装
    payload.rs — zip 解压、在线文件管理、下载辅助
    install_ops.rs — 完整安装流程（含进度上报）
    process.rs — launch_files()、运行时进程收集
    runtime_ops.rs — launch_pve()、launch_pvp()、launch_login_server()
    util.rs — hidden_command()、ensure_dir()、taskkill 辅助
    github_proxy.rs — 代理列表抓取、速度测试、选择
    filesystem.rs — 复制/删除辅助
    cleanup.rs — engine.ini 清理、旧 mod 移除
  app/
    mod.rs — AppController 结构体、AppMessage 枚举
    controller.rs — 初始化、页面切换、端口刷新
    actions.rs — start_install()、launch、uninstall
    updates.rs — start_update_check()
    update.rs — check_latest_release()、fetch_project_rebound_tag()、对话框文本
    messages.rs — drain_messages()、对话框处理、i18n
    dialogs.rs — 对话框操作处理
    proxy_list.rs — GitHub 代理列表 UI
    target.rs — 路径模式切换（Auto/Manual）
    font.rs — 字体进度上报
    prefs.rs — AppPrefs 加载/保存
    window.rs — 自适应窗口大小
```

## ProjectRebound 集成

### 在线文件

```
PROJECT_REBOUND_ONLINE_FILES:
  Payload.dll
  ServerLauncher/ProjectReboundServerLauncher.exe
  slint_cpp.dll
  BoundaryMetaServer-main/
  project_rebound_version.txt
```

### LaunchFiles 结构

- `wrapper_exe` → `launcher_exe` 指向 `ServerLauncher/ProjectReboundServerLauncher.exe`
- `launch_files()`：`target_win64.join("ServerLauncher").join("ProjectReboundServerLauncher.exe")`
- CWD 设为 `ServerLauncher\`，使得 Launcher 通过 `..\ProjectBoundarySteam-Win64-Shipping.exe` 找到游戏 exe

### 启动函数

```rust
// PVE 离线
launch_pve(): hidden_command(launcher_exe).current_dir(ServerLauncher).arg("-cli").spawn()
// PVE 在线
PVE 游戏启动添加 -match=127.0.0.1

// PVP
launch_pvp(): 类似但传递在线参数
```

## 版本检查

### 流程

```
startup → initialize()
  → start_ui_font_check()
  → 字体安装后, start_update_check(true)
    → check_latest_release(target_win64)
      → GitHub API: toolbox releases (同事的仓库)
      → GitHub API: ProjectRebound releases/latest (我们的仓库)
      → 读取本地 project_rebound_version.txt
      → 比较: is_version_newer(latest_tag, local_tag) || local_tag is None
    → UpdateCheckFinished { pr_is_newer, ... }
      → 对话框: "工具箱版本: x.x.x" / "社区服版本: y.y.y / (未安装)"
      → 按钮: 如果 pr_is_newer → "安装/更新" → start_install()
```

### UpdateCheckResult

```rust
struct UpdateCheckResult {
    // Toolbox 字段
    toolbox_tag, toolbox_local_tag, toolbox_is_newer,
    toolbox_published_at, toolbox_url,
    // ProjectRebound 字段
    project_rebound_tag, project_rebound_local_tag, pr_is_newer,
    project_rebound_published_at, project_rebound_url,
}
```

## 安装流程（简化版）

1. 从 GitHub 下载 Release.zip（动态 URL 带最新 tag）
2. `extract_zip_to_dir()` → 全量解压到 Win64\
3. 按需下载 BoundaryMetaServer（在线文件）
4. 按需下载 Node.js（在线文件，须 v24.14.0）
5. 写入 `project_rebound_version.txt`
6. 写入 `state.json`
7. ServerLauncher 以 `-cli` 启动，CWD = `Win64\ServerLauncher\`

## 关键文件路径

| 路径 | 内容 |
|------|------|
| `Win64\installer_tool\state.json` | 安装状态/元数据 |
| `Win64\installer_tool\app_config.json` | 用户偏好（语言、代理） |
| `Win64\project_rebound_version.txt` | 已安装 PR 版本 |
| `Win64\serverconfig.json` | ServerLauncher 配置（地图、模式、端口、serverId） |
| `Win64\ServerLauncher\ProjectReboundServerLauncher.exe` | Launcher 二进制 |
| `Win64\ServerLauncher\slint_cpp.dll` | Slint 运行时 |

## 已修复 Bug

| Bug | 修复 |
|-----|------|
| `file_name()` 路径匹配错误 — `ServerLauncher/ProjectReboundServerLauncher.exe` 被匹配为仅 `ProjectReboundServerLauncher.exe` | 改用精确路径匹配 `name.eq_ignore_ascii_case(item_name)` |
| 缺少 User-Agent header — GitHub 返回 403 | 添加 `.header("User-Agent", format!("boundary-toolbox/{APP_VERSION}"))` |
| Payload 未找到 — `find_payload_root` 缺 MapleMono 字体 zip | 从 build.rs MANAGED_ITEMS 移除字体 |
| 双对话框弹窗 | 添加日志追踪 UpdateCheckFinished 事件 |
| Metaserver TCP 端口 6968 → 6969 | 修改 server.listen(6969) |
| 离线模式默认 | `OfflineMode = true`, `CurrentMode = "pve"` |

## 构建

```powershell
cd C:\STanJK\Development\Boundary\rust-boundary-tool-box
$env:BOUNDARY_PAYLOAD_ROOT = "C:\STanJK\Development\Boundary\rust-boundary-tool-box\payload"
cargo build --release
```

- 输出：`target/release/boundary_toolbox.exe`
- 重新构建 ~60s，clean 构建 ~5min
- `PAYLOAD_ZIP_BYTES` 通过 `include_bytes!` 在编译时嵌入
- 必须在构建时设置 `BOUNDARY_PAYLOAD_ROOT` 环境变量

## Release.zip 结构

```
Release.zip
├── Payload.dll
├── ServerLauncher/
│   ├── ProjectReboundServerLauncher.exe
│   └── slint_cpp.dll
├── BoundaryMetaServer-main/
│   ├── index.js（端口 6969 TCP 修复）
│   ├── package.json / package-lock.json
│   ├── game/（loadoutStore.js, definitionIndex.js, definitions/）
│   ├── data/loadouts/
│   └── node_modules/
└── project_rebound_version.txt
```

## 剩余 TODO

- [ ] 分离安装/更新流程（不要每次重新下载 Node.js）
- [ ] 启动进度条 + 端口验证
- [ ] 关闭守卫：退出前检测所有进程（wrapper、游戏、node）
- [ ] 部分机器上 Wrapper 背景图片不显示
- [ ] 字体/Node.js 嵌入 payload（被同事坚持在线下载阻塞）
- [ ] PR 自动下载（目前只弹对话框）
- [ ] GitHub API 代理兼容性（已用 `.no_proxy()` 测试，更多测试待做）

## 相关文档

- `01-System-Overview.md` — 系统全景
- `06-Infrastructure.md` — 部署与运维
