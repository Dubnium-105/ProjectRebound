# ProjectRebound Python Browser Prototype

English | [简体中文](README.zh-CN.md)


> [!WARNING]
> This compatibility prototype is retained for debugging legacy room/NAT APIs and experimenting with portable packaging. It is not an entry point for current production deployments. The maintained .NET browser is under `Desktop/ProjectRebound.Browser/`.

## Current limitations

- `project_rebound_browser.py` still hard-codes the legacy test-backend address.
- Game startup depends on a Boundary MetaServer/Logic Server outside this repository.
- The UDP proxy uses the legacy `/v1/nat/*` and `/v1/relay/allocations` APIs and embedded UDP protocols on ports 5001/5002.
- It does not implement the current standalone Edge Relay cookie/token data-plane protocol and cannot replace production acceptance testing.

Use this directory only when explicitly maintaining legacy compatibility. For the current control plane, Edge Relay, and production deployment, see `docs/README.md` and `docs/operations/ci-cd.md`.

## Run

Requires Python 3.11 and tkinter:

```powershell
python Desktop\ProjectRebound.Browser.Python\project_rebound_browser.py
```

You can also run `run_browser.bat`. Before starting the game, provide an available Logic Server and confirm that `HARD_CODED_BACKEND_URL` in the source points to the intended test environment.

## Portable package

```powershell
cd Desktop\ProjectRebound.Browser.Python
.\build_portable.ps1
```

By default, the script builds and collects Release x64 artifacts for `dxgi`, `Payload`, and `ProjectReboundServerWrapper`. Without a native C++ build environment, use:

```powershell
.\build_portable.ps1 -SkipNativeBuild
```

The output directory is `portable/ProjectReboundBrowserPortable`. It may contain the browser, UDP proxy, Python runtime, and native artifacts under `runtime/`.

## Experimental UDP Proxy

`project_rebound_udp_proxy.py` attempts NAT hole punching through the legacy compatibility service and uses the old embedded UDP Relay when the direct connection fails. It applies only to the compatibility mode in `Backend/cmd/main.go`; the current decoupled production topology does not expose these UDP ports.

To verify the current Edge Relay, use `Backend/tests/netem/`, `Backend/api/relay-protocol.md`, and the control-plane/Edge Relay integration tests.
