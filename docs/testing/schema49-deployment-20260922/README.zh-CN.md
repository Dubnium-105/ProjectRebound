# Schema 49 部署准备

[English](README.md) | 简体中文

状态：**BLOCKED — 仅准备阶段。** 尚未发起 schema 49 部署。

## 候选与 CI

- 候选提交：`5fb54ce2832e152b5ea4d84c5656f2ad8bb11f37`。
- [CI 运行 35627019586](https://github.com/Dubnium-105/ProjectRebound/actions/runs/35627019586) 已全部成功：六项测试任务和五个镜像任务均通过，镜像、SBOM、来源证明及高危/严重漏洞扫描已完成。
- 上一轮 V1.1（`33d4bc6`）已成功。本轮只修正 fixture 的 `expires_at` 值。
- 现有的 [match-lifecycle 交付回执](../match-lifecycle-20260921/DELIVERY.zh-CN.md) 和 [验证记录](../match-lifecycle-20260921/VERIFICATION.zh-CN.md) 记录了流程修复证据。

## 生产门禁

生产仍使用 schema `48` 和运行版本 `8069d5e1126b8a585610b232e721aee45c57ee88`；尚未发起 schema 49 部署。

公网主入口当前阻塞：HAProxy 失败，TCP 80/443 未监听。部署用户没有非交互 sudo 权限，默认 root SSH 密钥被拒绝。正在等待管理员 SSH 入口或手动恢复 HAProxy。

迁移前备份为 `/opt/projectrebound-deploy/backups/projectrebound-20260921T160028Z.dump`，SHA-256 为 `263bbd5006d731f1c21591e9045933642f069795cd49169af108878e24e1d47b`，大小 11,017,457 字节，权限 `0600`。保留历史 P2P 对局，其清理门禁为 `MATCH_ATTEMPT_CLEANUP_PENDING`，影响两名玩家；不要强行清除原生清理门禁。活动连接为零不能证明原生资源已清理。记录来源为 `.tmp/schema49-deploy-20260921/preflight-extended.json`。

## 部署条件

1. CI 与镜像已就绪；提交和摘要见[部署回执](deployment-receipt.json)。
2. 恢复并验证公网入口、健康检查/SNI 路由和 Relay 后再发起部署。
3. 确认备份后执行 migration 49。
4. 验证 schema 49 部署成功后再进行实机验收。

schema 49 之后不能只回滚旧 schema 48 镜像；恢复需要完整数据库恢复或向前修复。

## 验收边界

四种模式下的完整原生对局、清理和下一局仍为 **NOT_RUN**。`release_ready=false`。
