# Admin Web 管理员使用手册

[English](admin-console-user-guide.md) | 简体中文

Admin Web 面向运营、客服、发布管理员、基础设施运维和审计人员。它只通过 HTTPS 调用 Go Control Plane，不直接连接 PostgreSQL、Redis、Dedicated Server 或中继节点。

## 登录

1. 从 VPN、Tailscale 或零信任访问代理进入独立管理域名。
2. 输入管理员登录名和密码，完成 Cloudflare Turnstile Managed 校验。
3. 输入当前 TOTP；无法使用认证器时，可使用一枚尚未使用的恢复码。
4. 登录后 Access Token 只保存在页面内存；Refresh Token 位于 `HttpOnly; Secure; SameSite=Strict` Cookie。

Turnstile、密码或账号是否正确不会通过不同错误文案泄露。若登录页提示反自动化校验不可用，不要反复刷新或请求关闭校验，按 [Turnstile Runbook](runbooks/admin-turnstile-login.zh-CN.md) 联系值班人员。

## 常用任务

### 玩家与风险

- 玩家列表可按状态筛选，并在当前结果中搜索玩家 ID、SteamID 或名称。
- 玩家详情可变更 `ACTIVE/BANNED`、VIP，并选择立即撤销全部 Session。
- 封禁、VIP 和 Session 操作必须填写工单、事件或客服原因；内部备注不得包含 Token、密码、Cookie、完整 IP 或游戏 Payload。
- 风险事件详情默认显示友好字段，技术 JSON 折叠展示；处理完成时填写结论。

### 邀请码

- 创建向导可一次生成 1–100 枚邀请码。
- 明文只在成功弹窗出现一次；关闭前复制或导出到受控位置。
- 数据库只保存 SHA-256，之后的列表、详情和使用记录不会恢复明文。
- 已撤销邀请码不应进入新的导出文件。

### 联机资源

- “进入维护”对应后端 Drain；中继节点可选择迁移既有连接。
- 关闭房间、踢出成员、关闭 Connection、停用专服都要求原因。
- Connection 的新中继目标由后端调度器从合格的 `READY` 节点选择，页面不允许填写 IP。
- 节点撤销会停止新分配、断开控制身份并迁移或中断既有连接；需要重新注册才能恢复，并要求 MFA 二次确认。

### 客户端发布

1. 创建 `DRAFT`，填写平台、架构、渠道、版本、最低兼容版本和文件描述。
2. 执行发布前校验，查看路径、SHA-256、大小、压缩方式、服务端对象 `HEAD` 可用性、版本顺序和 Ed25519 签名检查。
3. 仅 `READY` 版本可正式发布。
4. 正式发布和回滚必须填写原因并再次完成 MFA；回滚只退出后续更新目录，不删除历史。
5. `DRAFT`、`READY`、`ROLLED_BACK` 可由具备回滚权限的管理员经 MFA 后归档；`PUBLISHED` 必须先回滚。

更完整的基础设施发布和回滚流程见[发布与回滚](release-and-rollback.zh-CN.md)。

### 管理员、角色和设置

- 新建管理员后，TOTP QR/URI 与十枚恢复码只显示一次。
- 停用管理员、重置 MFA、撤销会话和修改角色都要求 `admins.update` 与 MFA。
- 最后一个有效 `SUPER_ADMIN` 不能被停用或移除该角色。
- `SUPER_ADMIN` 权限集不可编辑；其他角色通过分组复选框维护。
- 系统设置只包含白名单功能开关和 HTTPS Grafana/Runbook 链接，不包含 Secret。

## 审计与错误定位

操作审计支持按管理员、操作、资源类型和目标 ID 筛选，并可导出当前脱敏结果。展开一行可查看 Request ID、来源摘要、User-Agent 和前后差异。

发生错误时记录页面提示、Request ID、资源 ID、本地/UTC 时间和业务操作。不要在工单或聊天中粘贴密码、TOTP、恢复码、Cookie、Authorization Header、Turnstile Token、节点身份文件或私钥。

## 会话退出

从右上角执行“安全退出”。在共享或丢失设备、角色发生变化、怀疑 Cookie 泄露时，进入“我的会话”撤销其他 Session；管理员停用或 MFA 重置会由后端撤销其全部 Session。
