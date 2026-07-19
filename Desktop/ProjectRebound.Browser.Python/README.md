# ProjectRebound Python Browser Prototype

> [!WARNING]
> 这是保留用于旧房间/NAT 接口联调和便携打包实验的兼容原型，不是当前生产部署入口。仓库内维护的 .NET 浏览器位于 `Desktop/ProjectRebound.Browser/`。

## 当前限制

- `project_rebound_browser.py` 仍在代码中固定旧测试后端地址；
- 游戏启动流程依赖仓库外部的 Boundary MetaServer/Logic Server；
- UDP Proxy 使用旧 `/v1/nat/*`、`/v1/relay/allocations` 和内嵌 UDP 5001/5002 协议；
- 它不实现当前独立 Edge Relay 的 cookie/token 数据面协议，也不能替代生产验收。

因此，只有在明确维护旧兼容链路时才使用本目录。当前控制面、Edge Relay 和生产部署见 `docs/README.md` 与 `docs/cicd.md`。

## 运行

需要 Python 3.11 和 tkinter：

```powershell
python Desktop\ProjectRebound.Browser.Python\project_rebound_browser.py
```

也可以运行 `run_browser.bat`。启动游戏前仍需自行提供可用的 Logic Server，并检查源码中的 `HARD_CODED_BACKEND_URL` 是否属于预期测试环境。

## 便携包

```powershell
cd Desktop\ProjectRebound.Browser.Python
.\build_portable.ps1
```

脚本默认构建并收集 `dxgi`、`Payload` 和 `ProjectReboundServerWrapper` 的 Release x64 产物。缺少本地 C++ 构建环境时可以使用：

```powershell
.\build_portable.ps1 -SkipNativeBuild
```

输出目录为 `portable/ProjectReboundBrowserPortable`，其中可能包含浏览器、UDP Proxy、Python 运行时和 `runtime/` 原生产物。

## 实验性 UDP Proxy

`project_rebound_udp_proxy.py` 尝试在旧兼容服务上完成 NAT 打洞，并在直连失败后使用旧内嵌 UDP Relay。它只适用于 `Backend/cmd/main.go` 的兼容模式；当前分离式生产拓扑没有暴露这些 UDP 端口。

验证当前 Edge Relay 请使用 `Backend/tests/netem/`、`Backend/api/relay-protocol.md` 和控制面/Edge Relay 集成测试。
