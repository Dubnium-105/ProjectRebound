# ProjectRebound

[English](README.md) | 简体中文

[![CI and Images](https://github.com/Dubnium-105/ProjectRebound/actions/workflows/ci.yml/badge.svg)](https://github.com/Dubnium-105/ProjectRebound/actions/workflows/ci.yml)

ProjectRebound 是一个包含游戏 Payload、启动/浏览工具以及 Go 控制面和独立 Edge Relay 的多组件项目。生产后端以 PostgreSQL、Redis、Caddy 和 GHCR 不可变镜像为基础；控制面与边缘节点可以部署在不同主机。

## 仓库结构

| 路径 | 用途 |
| --- | --- |
| `Backend/` | Go 控制面、Edge Relay、数据库迁移、Compose、监控和测试 |
| `Payload/`、`dxgi/` | 注入 Payload、运行时 Hook 与代理 DLL |
| `ServerWrapper/`、`ServerLauncherGUI/` | 游戏服务器包装器与启动器 |
| `Tools/` | SDK 辅助工具 |
| `docs/` | 当前架构、API、部署、测试与 CI/CD 文档 |

## 快速验证

后端：

```bash
cd Backend
gofmt -l .
go vet ./...
go test ./... -count=1
```

生产环境不应在目标机重新构建后端。CI 为每个提交发布 `sha-<40-character-commit>` 控制面和 Edge Relay 不可变镜像；Deploy 工作流按同一 SHA 拉取并执行健康检查、备份与自动回滚。详见 [CI/CD 指南](docs/operations/ci-cd.zh-CN.md)。

## 文档入口

从[文档中心](docs/README.zh-CN.md)开始。该索引区分当前操作依据、机器可读契约、版本证据和历史归档；`docs/archive/` 中的内容不得作为当前 API 或部署依据。

公开文档采用英文与简体中文成对维护。新增或修改文档前请阅读[文档标准](docs/documentation-standard.zh-CN.md)。

## 许可证

见 [LICENSE.txt](LICENSE.txt)。
