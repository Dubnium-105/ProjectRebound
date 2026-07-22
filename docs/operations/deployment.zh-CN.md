# V1.1 部署入口

[English](deployment.md) | 简体中文

ProjectRebound 包含三个独立部署角色：

1. 私网控制面主机通过独立 Compose 运行 PostgreSQL、Redis、Control Plane、Caddy、Prometheus 和 Grafana；
2. 公网网关转发 HTTP 和原始 TCP mTLS，不终止 Relay 客户端证书；
3. 每个 Edge Relay 仅在 Linux host network 上运行 Relay 进程，并向公网开放游戏 UDP 端口。

主机准备、防火墙、FRP/HAProxy、证书、DNS 和首次注册的权威流程见 [Debian 部署与运维](deployment-guide.zh-CN.md)。CI/CD 发布不可变 `sha-<commit>`、语义版本镜像、provenance 和构建记录；生产主机拉取这些产物，不在目标机编译。见 [CI/CD](ci-cd.zh-CN.md)。

V1.1 生产变更使用：

```bash
# 控制面：加密备份、预检、兼容迁移、部署、冒烟测试、
# 观察，并在失败时自动恢复前一个镜像。
cd Backend
scripts/release/control-plane.sh

# Relay 每次一台：Drain/迁移、等待 allocation 清零、部署、
# 重连并恢复 READY。
scripts/release/rolling-edge-relay.sh
```

发布环境和回滚行为见 [V1.1 发布与回滚](release-and-rollback.zh-CN.md)。完成 [Release Checklist](../testing/v1.1/release-checklist.zh-CN.md)，当[测试报告](../testing/v1.1/test-report.zh-CN.md)或[恢复报告](../testing/v1.1/restore-test-report.zh-CN.md)仍有必须执行的 `NOT_RUN`/`FAIL` 门禁时，不得提升版本。

新 Relay 维护者只需要节点专用 Bootstrap Token、最小 `.env` 和 Relay YAML。首次注册后移除 Bootstrap Token；身份与证书轮换复用现有 mTLS 控制链路。Relay 不需要 PostgreSQL、Redis、Cloudflare Zero Trust 或公网指标监听端口。

健康 Relay 必须连续在线，绝不按定时器重启。计划变更必须逐台执行，并先 Drain 到零 allocation；非计划重启仅用于进程已经停止的节点。见 [Relay 连续在线与恢复](relay-continuity.zh-CN.md)。
