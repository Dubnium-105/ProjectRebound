# ProjectRebound legacy NAT punch test

[English](README.md) | 简体中文

> [!WARNING]
> 本工具验证的是旧 Go 单进程兼容入口中的 guest、房间、NAT rendezvous 和内嵌 UDP Relay；它不验证当前独立 Edge Relay，不能作为生产部署验收。

## 适用环境

从 `Backend/` 运行旧兼容入口时，默认地址为：

```text
HTTP  http://127.0.0.1:5000
UDP   5001 rendezvous
UDP   5002 embedded relay
```

当前 `cmd/control-plane` + `cmd/edge-relay` 分离架构使用不同协议和端口。验证当前架构请改用 `Backend/tests/netem/` 和 `Backend/api/relay-protocol.md`。

## 本机冒烟

```powershell
Tools\NatPunchTest\run-loopback.bat --backend http://127.0.0.1:5000
```

成功时以 `PASS: received pong ...` 结束。这只能证明本机兼容 HTTP/UDP 链路，不代表跨 NAT 可达。

## 两机直连测试

主机 A：

```powershell
Tools\NatPunchTest\run-host.bat --backend http://LEGACY_SERVER:5000 --port 27777
```

记录输出的 `ROOM_ID`。主机 B：

```powershell
Tools\NatPunchTest\run-client.bat --backend http://LEGACY_SERVER:5000 --room-id ROOM_ID --port 27778
```

两端增加 `--relay` 可以验证旧内嵌 UDP 5002 Relay。不要把该选项用于当前 Edge Relay。

## 失败含义

- `UDP rendezvous timed out`：旧 UDP 5001 未往返；
- `NAT_BINDING_NOT_READY`：兼容后端没有观察到对应 binding；
- `FAIL: no pong`：打洞、路由或防火墙阻止了数据包；
- `--relay` 失败：旧 UDP 5002 未运行、被阻断或注册失败。

脚本仅依赖 Python 标准库。它保留在仓库中是为了兼容回归，不应驱动新的客户端或部署设计。
