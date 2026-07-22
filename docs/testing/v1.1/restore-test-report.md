# V1.1 恢复演练报告

状态：`PASS`

演练时间：2026-07-21 09:46:29Z ～ 09:46:42Z

演练环境：Debian 13 隔离 Docker 网络、两个全新 PostgreSQL 17 容器、Redis 7、按当前源码构建的 Control Plane 与 Edge Relay。所有容器、网络、卷、临时镜像标签、明文恢复文件和演练目录均由退出陷阱删除；未连接或修改生产数据库。

执行命令：

```bash
cd Backend/tests/integration
sudo env \
  RESTORE_DRILL_I_UNDERSTAND=disposable-postgres-containers \
  ./run-restore-drill.sh
```

## 结果

| 项目 | 结果 |
| --- | --- |
| PostgreSQL 版本 / Schema | PostgreSQL 17；schema version 16；22 张要求表全部存在 |
| 数据库备份 | `pg_dump` custom format、压缩、`age` 加密、SHA-256、`pg_restore --list` 校验均 PASS |
| 数据库备份 SHA-256 | `5df3db6eb238881f37da473db2e9c4922052ce9296542fe21404fededb8017a1` |
| 独立密钥包 | Access Token、Relay Token、Manifest、Relay CA 与恢复凭据独立 `age` 加密；SHA-256 `bfb6415301afe9a78495920ac588edbfef8412858be429dca5d8bdfb2f974837` |
| 数据库恢复耗时 | 515 ms |
| 应用 RTO | 4417 ms（从开始恢复到 Control Plane READY、管理员验证、Manifest 连续性和 Relay READY） |
| 总演练时间 | 13 秒（包含镜像缓存命中后的建栈、备份、校验、恢复和应用验证，不把镜像构建计入 RTO） |
| player_id | `restore-player-0001` 经恢复后数据库与管理员 API 双重验证 |
| 管理员恢复凭据 | 恢复后的 Control Plane 管理员 API 鉴权及玩家查询 PASS |
| 旧 Manifest | 恢复前后使用恢复出的同一 Manifest 私钥；归一化请求 ID 后响应逐字节一致，签名与 hash 字段存在 |
| Relay | 新环境 Relay 使用恢复出的 CA/Relay Token 密钥向恢复后的 Control Plane 注册并进入 READY |
| 旧活动状态 | 房间 `CLOSED`、connection/allocation `FAILED`、旧 Relay `OFFLINE`、活动 member/allocation 均为 0 |
| 迁移幂等 | 恢复后再次运行 schema migrator PASS |
| 备份/校验/恢复指标 | backup success、verification success、restore drill timestamp 三类指标均生成并校验 |

## 恢复语义修正

`postgres-restore.sh` 在数据库恢复完成后执行单个事务，禁止快照中的易失状态复活：未结束的 Relay migration、allocation、connection、房间和成员被终止，非撤销节点被置为 OFFLINE，容量、带宽和 lease 清零。玩家、认证 Session、邀请码、审计和发布数据不在该清理范围内。

该演练证明单份数据库备份和独立密钥包可在全新环境恢复；生产操作仍必须满足离机多副本、保留周期、访问审批和正式变更窗口要求。
