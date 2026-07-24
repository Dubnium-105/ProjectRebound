import {
  ArrowLeftOutlined,
  CheckCircleFilled,
  CloudOutlined,
  LockOutlined,
  SafetyCertificateOutlined,
  UserOutlined
} from "@ant-design/icons";
import {
  Turnstile,
  type TurnstileInstance
} from "@marsidev/react-turnstile";
import {
  Alert,
  App,
  Button,
  Card,
  Divider,
  Form,
  Input,
  Space,
  Spin,
  Steps,
  Typography
} from "antd";
import {useEffect, useRef, useState} from "react";
import {useNavigate} from "react-router";
import {ApiError, authClient} from "../api/client";
import type {TurnstileConfig} from "../types";

type Credentials = {
  username: string;
  password: string;
};

export function LoginPage() {
  const navigate = useNavigate();
  const {message} = App.useApp();
  const turnstileRef = useRef<TurnstileInstance | undefined>(undefined);
  const [config, setConfig] = useState<TurnstileConfig | null>(null);
  const [configError, setConfigError] = useState("");
  const [turnstileToken, setTurnstileToken] = useState("");
  const [challengeToken, setChallengeToken] = useState("");
  const [challengeExpiresAt, setChallengeExpiresAt] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let active = true;
    authClient
      .config()
      .then((value) => {
        if (active) {
          setConfig(value);
        }
      })
      .catch((error) => {
        if (active) {
          setConfigError(
            error instanceof Error ? error.message : "无法读取管理员登录配置。"
          );
        }
      });
    return () => {
      active = false;
    };
  }, []);

  const resetTurnstile = () => {
    setTurnstileToken("");
    turnstileRef.current?.reset();
  };

  const beginLogin = async (values: Credentials) => {
    if (!turnstileToken) {
      message.warning("请先完成安全验证。");
      return;
    }
    setSubmitting(true);
    try {
      const result = await authClient.beginLogin({
        username: values.username,
        password: values.password,
        turnstile_token: turnstileToken
      });
      setChallengeToken(result.challenge_token);
      setChallengeExpiresAt(result.expires_at);
      message.success("账号密码已确认，请完成动态验证码验证。");
    } catch (error) {
      resetTurnstile();
      message.error(loginError(error));
    } finally {
      setSubmitting(false);
    }
  };

  const verifyMFA = async ({code}: {code: string}) => {
    setSubmitting(true);
    try {
      await authClient.verifyMFA({
        challenge_token: challengeToken,
        code
      });
      message.success("身份验证完成。");
      navigate("/", {replace: true});
    } catch (error) {
      message.error(loginError(error));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className="login-page">
      <section className="login-story" aria-label="管理入口安全说明">
        <div className="login-story-inner">
          <div className="login-brand">
            <div className="brand-mark large">R</div>
            <div>
              <div className="brand-name light">ProjectRebound</div>
              <div className="brand-subtitle light">CONTROL PLANE</div>
            </div>
          </div>
          <div className="login-kicker">受保护的运营入口</div>
          <Typography.Title level={1}>
            让每一次管理操作
            <br />
            都有身份、有边界、有记录
          </Typography.Title>
          <Typography.Paragraph className="login-lead">
            管理员认证与玩家系统完全隔离。登录需要通过浏览器反自动化校验、账号密码和动态验证码，写操作由控制面统一授权并记录审计。
          </Typography.Paragraph>
          <div className="security-chain">
            {[
              ["01", "可信网络", "VPN / 零信任访问代理"],
              ["02", "反自动化", "Cloudflare Turnstile"],
              ["03", "强身份", "密码 + TOTP / 恢复码"],
              ["04", "最小权限", "服务端 RBAC + 审计"]
            ].map(([index, title, description]) => (
              <div className="security-chain-item" key={index}>
                <span>{index}</span>
                <div>
                  <strong>{title}</strong>
                  <small>{description}</small>
                </div>
                <CheckCircleFilled />
              </div>
            ))}
          </div>
          <div className="login-footnote">
            <SafetyCertificateOutlined />
            此入口不接受玩家 Access Token 或机器运维 Token
          </div>
        </div>
      </section>
      <section className="login-form-column">
        <Card className="login-card" variant="borderless">
          <div className="login-card-heading">
            <Typography.Text type="secondary">
              {challengeToken ? "第二步，共两步" : "第一步，共两步"}
            </Typography.Text>
            <Typography.Title level={2}>
              {challengeToken ? "验证动态验证码" : "登录管理控制台"}
            </Typography.Title>
            <Typography.Paragraph type="secondary">
              {challengeToken
                ? "输入认证器中的六位验证码，或使用一枚未使用的恢复码。"
                : "使用独立管理员账号登录。连续失败会触发账号和来源地址限流。"}
            </Typography.Paragraph>
          </div>
          <Steps
            size="small"
            current={challengeToken ? 1 : 0}
            items={[{title: "账号验证"}, {title: "动态验证码"}]}
            className="login-steps"
          />
          <Divider />
          {configError && <Alert type="error" showIcon title={configError} />}
          {!config && !configError && (
            <div className="login-loading">
              <Spin />
              <span>正在加载安全配置…</span>
            </div>
          )}
          {config && !config.configured && (
            <Alert
              type="warning"
              showIcon
              title="管理员登录尚未启用"
              description="控制面缺少 Turnstile Sitekey 或 Secret。配置完成前登录会失败关闭。"
            />
          )}
          {!challengeToken ? (
            <Form<Credentials>
              layout="vertical"
              requiredMark={false}
              onFinish={beginLogin}
              className="login-form"
            >
              <Form.Item
                label="管理员账号"
                name="username"
                rules={[{required: true, message: "请输入管理员账号。"}]}
              >
                <Input
                  size="large"
                  autoComplete="username"
                  prefix={<UserOutlined />}
                  placeholder="operator@example.com"
                />
              </Form.Item>
              <Form.Item
                label="密码"
                name="password"
                rules={[{required: true, message: "请输入密码。"}]}
              >
                <Input.Password
                  size="large"
                  autoComplete="current-password"
                  prefix={<LockOutlined />}
                  placeholder="输入管理员密码"
                />
              </Form.Item>
              {config?.configured && (
                <div className="turnstile-block">
                  <div className="turnstile-label">
                    <Space>
                      <CloudOutlined />
                      登录安全验证
                    </Space>
                    <Typography.Text type="secondary">由 Cloudflare 提供</Typography.Text>
                  </div>
                  <Turnstile
                    ref={turnstileRef}
                    siteKey={config.site_key}
                    onSuccess={setTurnstileToken}
                    onExpire={() => setTurnstileToken("")}
                    onTimeout={() => setTurnstileToken("")}
                    onError={() => {
                      setTurnstileToken("");
                      message.error("安全验证加载失败，请检查网络后重试。");
                    }}
                    options={{
                      action: config.action,
                      appearance: "interaction-only",
                      language: "zh-cn",
                      size: "flexible",
                      theme: "light",
                      refreshExpired: "auto",
                      refreshTimeout: "auto"
                    }}
                  />
                </div>
              )}
              <Button
                type="primary"
                htmlType="submit"
                size="large"
                block
                loading={submitting}
                disabled={!config?.configured || !turnstileToken}
              >
                继续验证
              </Button>
            </Form>
          ) : (
            <Form<{code: string}>
              layout="vertical"
              requiredMark={false}
              onFinish={verifyMFA}
              className="login-form"
            >
              <Form.Item
                label="动态验证码或恢复码"
                name="code"
                rules={[{required: true, message: "请输入验证码或恢复码。"}]}
              >
                <Input
                  size="large"
                  autoComplete="one-time-code"
                  inputMode="numeric"
                  prefix={<SafetyCertificateOutlined />}
                  placeholder="000000"
                  maxLength={32}
                />
              </Form.Item>
              <Typography.Paragraph type="secondary" className="challenge-expiry">
                本次验证请求将在{" "}
                {new Date(challengeExpiresAt).toLocaleTimeString("zh-CN", {
                  hour: "2-digit",
                  minute: "2-digit",
                  second: "2-digit"
                })}{" "}
                失效。
              </Typography.Paragraph>
              <Button
                type="primary"
                htmlType="submit"
                size="large"
                block
                loading={submitting}
              >
                验证并进入控制台
              </Button>
              <Button
                type="text"
                block
                icon={<ArrowLeftOutlined />}
                onClick={() => {
                  setChallengeToken("");
                  setChallengeExpiresAt("");
                  resetTurnstile();
                }}
              >
                返回账号验证
              </Button>
            </Form>
          )}
        </Card>
        <Typography.Text type="secondary" className="login-help">
          无法登录？请通过内部值班渠道联系平台管理员，不要发送密码、验证码或 Token。
        </Typography.Text>
      </section>
    </main>
  );
}

function loginError(error: unknown) {
  if (error instanceof ApiError) {
    const requestID = error.requestId ? `（请求编号：${error.requestId}）` : "";
    return `${error.message}${requestID}`;
  }
  return error instanceof Error ? error.message : "登录失败，请稍后重试。";
}
