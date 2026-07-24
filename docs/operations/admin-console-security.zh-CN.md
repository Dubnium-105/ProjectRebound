# Admin Web 安全手册

[English](admin-console-security.md) | 简体中文

## 安全边界

生产入口必须依次具备：

```text
VPN / 零信任代理
→ 独立 admin 域名与 HTTPS
→ Cloudflare Turnstile
→ 管理员密码
→ TOTP / 恢复码
→ 后端 RBAC
→ 高风险 MFA Step-up
→ 后端审计
```

Turnstile 仅降低撞库和脚本化登录，不替代其他层。Admin Web 容器只加入 edge 网络，使用只读文件系统、无 Linux capabilities、`no-new-privileges`，不得获得数据库、Redis、Relay 控制网或任何服务端 Secret。

## Secret 边界

| 数据 | 位置 | 禁止位置 |
| --- | --- | --- |
| `TURNSTILE_SITE_KEY` | 浏览器配置响应 | — |
| `TURNSTILE_SECRET_KEY` | Control Plane Secret | Admin Web、Git、日志、浏览器响应 |
| MFA 加密 Key | Control Plane Secret | 数据库明文、Admin Web |
| Admin Refresh Token | HttpOnly Cookie | localStorage、页面状态、日志 |
| Admin Access/Step-up Token | 页面内存 | localStorage、审计 |
| 密码、TOTP、恢复码 | 输入或一次性交付 | 日志、审计前后值、工单 |

生产、预发布、测试和开发必须使用不同 Turnstile Widget/密钥。生产 Widget 只允许生产管理域名。

## 管理员生命周期

- 首个管理员通过 `adminctl` 在受控终端创建；密码从环境变量读取，终端历史不得保存密码。
- 每名管理员必须启用 MFA，禁止共享账号。
- 角色按最小权限分配，至少每季度复核一次。
- 离职、转岗或设备失窃时立即停用账号并确认 Session 已撤销。
- MFA 重置需由具备 `admins.update` 的另一名管理员执行；新配置只交付给已验证身份的本人。
- 始终保留至少两个受不同人员控制的 `SUPER_ADMIN`，但后端仍只保证最后一个不会被移除。

## 高风险操作

正式发布/回滚、Relay 撤销、管理员治理、角色改权和设置变更必须经过短时、Session 绑定的 MFA Step-up。操作原因应包含工单或事件编号和业务理由。对影响范围不明确的操作先 Drain、观察和验证，不直接 Revoke。

页面隐藏按钮不是安全边界；每个 API 都必须由后端校验权限。Player Access Token 和机器静态 Admin Token 不能替代人类管理员 Session。

## 日志与隐私

审计保留操作者、操作、目标、前后值、原因、Request ID、来源摘要、User-Agent、结果和时间。响应和审计递归脱敏凭据类键。禁止记录 Turnstile Token、密码、Cookie、Authorization Header、私钥、完整节点身份或完整游戏 Payload。

## 定期检查

- Turnstile hostname/action 与生产域名一致；
- 登录限流、异常告警和 Siteverify 延迟正常；
- 管理员、角色和活跃 Session 无异常；
- MFA 加密 Key、Admin JWT Key 和部署 Secret 轮换计划有效；
- Caddy CSP 仅放行所需 Turnstile script/frame，页面禁止被 iframe 嵌入；
- Admin Web 构建产物不含 `TURNSTILE_SECRET_KEY` 或其他服务端 Secret；
- 审计写入失败会使关键写操作失败关闭；
- 数据库备份与恢复演练覆盖管理员表和审计表。

登录反自动化故障见 [Turnstile Runbook](runbooks/admin-turnstile-login.zh-CN.md)，Relay 身份事件见[密钥与证书轮换](key-and-certificate-rotation.zh-CN.md)。
