# 联合流程修复验证记录

范围：ProjectRebound / ProjectReboundToolbox，r14b-flowfix。此记录区分源码、组件、数据库和分发文件验证；不能作为四模式真实对局验收。

## 自动化验证

| 项目 | 命令 / 方法 | 结果 |
| --- | --- | --- |
| Rust 核心，产品 vnt 特性 | `cargo test --lib --no-default-features --features vnt --quiet` | 391 通过，0 失败，0 忽略 |
| Tauri 命令与 DTO | `cargo test --manifest-path src-tauri/Cargo.toml --quiet` | 9 通过，0 失败，0 忽略 |
| React 前端 | 项目 frontend 测试脚本 | 84 通过，0 失败，0 跳过 |
| React 生产资产 | 项目 frontend build 脚本 | 通过 |
| Payload C++ | CTest Release | 27 通过，0 失败 |
| Payload DLL | MSBuild Release x64 | 通过，0 警告，0 错误 |
| Backend 普通测试 | `go test ./...` | 通过；此命令未设置数据库，不把它当作 PostgreSQL 验收 |
| MatchLobby PostgreSQL | Linux 测试二进制连接新建独立 PostgreSQL 数据库 | 26 通过，0 失败，0 跳过 |
| Relay registry PostgreSQL | Linux 测试二进制连接独立 PostgreSQL 数据库 | 15 通过，0 失败，0 跳过 |
| Connection / P2P room / Game server PostgreSQL | Linux 测试二进制连接独立测试数据库，顺序执行 | 3 个包全部通过，0 跳过；包含 connection、P2P room、VNT room、game server 的真实数据库生命周期用例 |
| 分发 PowerShell 脚本 | PowerShell Parser 静态解析 | 7 个脚本，0 语法错误；未执行安装 |

Rust 验证包含真实 Windows 命名管道、错误 PID 拒绝、DPAPI 回执重启恢复、独立子进程 exact-handle 终止与等待、Dedicated HTTP outbox 先于清理回退，以及原生 C++ 输出的 wire fixture 消费。子进程测试不启动游戏。

前端验证覆盖建房 / 入房成功但准备失败后保留成员资格、过期重试和退出回调、Backend PENDING 不能被本地完成覆盖、同一 attempt 的旧 operation/run 清理回执不能放行新操作。没有完成浏览器视觉检查。

C++ 验证覆盖两种真实结果回调顺序、单信号不能产生结果、完整作用域、幂等 ACK、RETURN_READY 顺序和 NetDriver 绑定。C++ wire 与 Rust fixture 的共同 SHA-256 为 `a061145083d8745311083bee6cf6ead28a6569ac7f9d97b88fdd38da967ebdf6`。

PostgreSQL 通过项目 Migrator 应用包含 schema 49 的迁移。新增数据库回归覆盖正常结算、禁止提前成功完成、禁止 ENDING 新准入、已保存 RETURN_READY 后完成事务失败的重放、结果确认后进程退出 / 超时仍为 COMPLETED、迟到回执、网络释放失败时保留原生清理记录、重试后清理成功，以及 ENDING 拒绝 VNT bootstrap 但继续接收 presence / heartbeat。Relay 回归要求远端 AllocationClosed 确认后才能完成撤销；原生 Relay runtime 单测覆盖重复撤销和丢失回执后的幂等确认。

初轮数据库测试暴露的旧 scope 快照、错误码契约、未提交 preflight 清理回执，以及新增 VNT 测试的 SQL 参数类型问题均已修正。最终通过日志为 `matchlobby-db-verified.txt` 与 `relayregistry-db-tests-final.txt`；失败轮次不作为通过证据。

## 构建输入与证据范围

- Toolbox 源提交：`8a641dc`。通过项目 `scripts/build-strict-candidate.ps1` 从干净提交构建；生产默认特性为空，依赖仅启用 `vnt`，`lab-testing=false`。
- Payload 未签名 DLL SHA-256：`fbaeda4058a8e24e115879c16e326bc0e1c5e8f5dcd9d137deaee1f82be05c47`。
- Payload 签名 DLL SHA-256：`e73c84f215c67e1cdf6da611157a99caada9c4e1fd2d7bd4f7abb69fdd58bcd0`。
- Toolbox 未签名 EXE SHA-256：`e351277928aa1a3b777ad0d972e68805e7dfc0ceecf9832750e0d35c8a01ac10`。
- Toolbox 签名 EXE SHA-256：`af29cb9cce666ef3ec41ecdef1ee461e9777254d56fe390145450e3dcbb2b370`；49,742,648 字节。Tauri CLI 正式构建通过，保留既有未使用代码警告。
- 签名使用已有测试证书 `B041917B2322ED509435B72356BA5AD9EA053378`，不修改信任存储、不导出私钥。测试根不受默认 Windows 信任，不作为生产签名声明。
- 对应的构建日志、测试日志及签名回执保存在本机 `.tmp/match-lifecycle-20260921/`。原始日志不随公开包分发。

最终 Backend / Payload 源提交、Linux 二进制与压缩包哈希见交付清单及包内 `package-manifest.json`。包构建器逐文件回读压缩包字节；其通过状态仅表示分发文件一致。

## 未执行

线上 Backend 部署与数据库迁移、已安装游戏 DLL 替换、游戏 / Frida 启动、两次冷启动，以及三个独立 Steam 账号在 P2P 直连、Relay、VNT、Dedicated 下完整结算、清理与下一局均为 **NOT_RUN**。`release_ready=false`，准入能力门禁不变。
