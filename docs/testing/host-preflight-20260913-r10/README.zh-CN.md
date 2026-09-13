# r10：修复 authority 就绪后的管道占用

[English](README.md) | 简体中文

r9 双机实测失败：房主先到达 `authority_ready`，随后打开命名管道报 `os error 231`；从机随后收到战局终止并取消启动。源码复核发现，房主确认连接仍占用 Payload 的唯一管道实例，后续等待流程又尝试连接同一管道。用户日志没有原始管道句柄跟踪，首因诊断来自日志与源码的对应关系，不能当作独立采集到的原生调用轨迹。详见[失败证据](evidence/native/reported-r9-failure-and-source-cause.json)。

r10 将管道使用收束为短事务：每次请求返回或报错即释放句柄，然后进行后端请求或下一次原生调用。等待原生连接证明和 Playable 时继续处理准入授权与连接回执，避免同步启动阻塞这部分工作。严格名单、有效凭据、进程 PID、取消、作用域和清理所有权检查继续执行。

Toolbox `642760be87439cea10f4b57f8b7368197e845d45`; Backend `8069d5e1126b8a585610b232e721aee45c57ee88`; Payload source `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. 本轮只修改 Toolbox。后端只读复核确认上一局最终为 `ABORTED/CLEARED`，P2P transport 为 `CLOSED`，从机仍为 `RESERVED`；这些后台状态不证明原生出生或移动射击成功。Main 的后续依赖/CI 修复没有部署到现有服务。详见[后端回执](evidence/backend.json)。

实跑结果：Rust 362 项、Tauri 9 项，共 371 项通过，失败 0、忽略 0；两处格式检查通过。新增 Windows 单实例管道测试真实复现 231，并验证断开后重连、错误后释放和取消。协议帧为合成数据，不能算 Boundary 原生验收。生产资产构建、45 项文件预检、包字节比对及签名 Toolbox 的[桌面启动](evidence/desktop/ui-observation.json)均通过。桌面仍显示“版本检查失败”，与旧 r9 相同；版本可用性检查未计为通过。[测试收据](evidence/rust/execution-receipt.json)与[源码绑定](evidence/committed-source-binding.json)记录实际版本。

Toolbox 精确提交的 GitHub Actions、checks、commit statuses 均为 0，记为 [NOT_RUN](evidence/ci-toolbox.json)，不以本地测试替代 CI。Main 文档提交的 CI 在推送后另行查询。

分发包 `rebound-hardware-test-20260913-r10-windows-x64.zip`：23,538,523 字节，SHA-256 `98d5e2ab6706fdcbecc0909475be5ef961385277142b437b186499b48631c802`。签名仅用于测试，系统不信任测试根；`release_ready=false`、`native_authority_admission_verified=false`。详见[包收据](evidence/distribution/package-build-receipt.json)、[52 项追加记录](52-item-r10-append-delta.json)与[证据哈希索引](evidence-index.json)。

下一轮请至少三台机器退出旧 Toolbox，使用同一 r10 包通过文件预检后创建全新大厅，核对队伍并全部准备，再由房主点击“采集原生准入证据”。记录房主准入、成员进入、Playable、实际出生和移动射击，以及清理后第二局。r10 的这些实机步骤均为 NOT_RUN；禁止用手工 open、空 Token/Grant 或关闭严格名单继续。
