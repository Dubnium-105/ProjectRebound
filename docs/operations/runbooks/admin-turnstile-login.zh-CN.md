# Runbook：Admin Turnstile 登录异常

[English](admin-turnstile-login.md) | 简体中文

## 触发条件

- 多名管理员收到统一的“登录安全校验暂时不可用”提示；
- `turnstile_verify_latency_ms` 明显上升；
- Siteverify 不可用、hostname/action mismatch 或 Turnstile failure 登录审计增加；
- Control Plane 无法通过 HTTPS 访问 `challenges.cloudflare.com`。

系统必须失败关闭。禁止通过临时跳过 Turnstile、改为浏览器直接校验、在日志打印 Token/Secret，或放宽生产 hostname 来恢复登录。

## 初步判断

1. 确认管理域名、VPN/零信任代理和 Control Plane 健康。
2. 在登录审计中按时间和失败结果筛选，只查看错误码、hostname、action 和延迟。
3. 区分：
   - 单一来源或账号：优先按攻击、失效 Token、Widget 重置或限流处理；
   - 全部管理员：检查 Cloudflare、DNS、出站 TLS、Secret 注入和预期 hostname/action；
   - 仅一个环境：检查该环境自己的 Widget 与密钥，禁止借用生产密钥。
4. 保存 Request ID、UTC 时间、环境、部署版本和非秘密错误码。

## Widget 与代理检查

- 浏览器只能加载 `https://challenges.cloudflare.com` 的 script/frame；
- CSP 中 `script-src` 和 `frame-src` 已精确放行该来源；
- Widget 使用 Managed、`interaction-only` 和 `action=admin_login`；
- 登录按钮在没有有效 Token 时保持禁用，失败、过期或提交后会重置 Widget；
- 反向代理只从已信任上游解析客户端地址，外部请求不能伪造 `X-Forwarded-For`；
- 生产页面与 API 同源，Cookie 保持 `Secure; SameSite=Strict`。

## Control Plane 检查

- `TURNSTILE_SECRET_KEY` 只存在于 Control Plane Secret；
- `TURNSTILE_EXPECTED_HOSTNAME` 与实际管理域名完全一致；
- `TURNSTILE_EXPECTED_ACTION=admin_login`；
- 服务器 DNS、时间和 CA 信任正常；
- 出站 443 可访问 `challenges.cloudflare.com`，未经过会替换证书的未知代理；
- Siteverify 超时有界，网络/429/5xx 只使用同一个 idempotency key 短重试一次，4xx 不重试；
- 登录限流在 Siteverify 前生效，避免放大外部请求。

不要把生产 Secret 放到命令行参数、Shell history、工单或诊断输出。需要做端到端验证时，在隔离测试环境使用 Cloudflare 测试 Widget/Secret，并确认测试密钥不会进入生产配置。

## 恢复

1. 修正 DNS、出站网络、CSP、环境变量或 Widget 环境映射。
2. 若 Secret 疑似失效或泄露，在 Cloudflare 轮换该环境 Secret，并通过 Secret 管理系统更新 Control Plane。
3. 仅滚动重启 Control Plane；Admin Web 无需获得 Secret。
4. 使用受控测试管理员完成一次 Turnstile → 密码 → TOTP 登录。
5. 确认登录审计为成功、hostname/action 正确、Token/Secret 未进入日志。
6. 观察失败率和延迟至少 15 分钟，再结束事件。

若所有人被锁在系统外，使用 VPN/零信任内的受控终端和预先建立的备用管理员完成验证；这不是跳过 Turnstile 的授权。仍无法恢复时回滚最近的代理/Control Plane 配置，并保持失败关闭。

## 攻击或泄露

- 暂停相关账号，撤销其 Session；
- 轮换 Turnstile Secret、受影响管理员密码/恢复码和怀疑泄露的部署 Secret；
- 检查同一来源、账号摘要、User-Agent、失败码和时间窗口；
- 保留审计和网关日志，但不复制敏感请求体；
- 记录根因、影响、处置、验证和预防措施。
