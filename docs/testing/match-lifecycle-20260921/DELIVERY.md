# r14b-flowfix 交付清单

两个仓库的实现与组件验证已完成，以下为本地测试制品；没有推送、部署或执行实机对局。

- Backend / Payload 源提交：`101e15902a2a0bcfb6ae76e5c096e00ec78bba21`。
- Toolbox 源提交：`8a641dc77d8e377abdbf139ed7c210960db2f149`。
- Backend 要求 schema **49**；严格准入仍为 `strict-roster-v2`，新增生命周期为 `match-lifecycle-v1`。
- 两个压缩包都经过逐文件字节回读验证；`release_ready=false`。

## [ProjectRebound-r14b-flowfix-20260921-windows-x64.zip](../../../artifacts/hardware-test-20260921-r14b-flowfix/ProjectRebound-r14b-flowfix-20260921-windows-x64.zip)

大小：23,643,317 字节；文件数：23。

SHA-256：`4b9cb706743f5d4db0da0c2bdff77a8223b47d77de148db773c255671a9a9389`。

## [ProjectRebound-r14b-flowfix-20260921-server-linux-amd64.tar.gz](../../../artifacts/hardware-test-20260921-r14b-flowfix/ProjectRebound-r14b-flowfix-20260921-server-linux-amd64.tar.gz)

大小：47,763,969 字节；文件数：57。

SHA-256：`996496a945fca9d86f2c2d7f0737933160b68dc2a8504e03b42e96b8cbbe4959`。

Windows 包包含配套 EXE / DLL、安装与回退脚本、检查脚本、许可证和测试说明。Linux 包包含 control-plane、meta-server、edge-relay 及迁移 SQL，供部署人员使用。

测试结果见 [VERIFICATION.md](VERIFICATION.md)，四模式实机验收矩阵见 [README.md](README.md)。部署匹配后端及三个独立 Steam 账号的完整战局验收仍为 **NOT_RUN**。
