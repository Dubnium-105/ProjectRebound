import { localeTag, tr, turnstileLanguage } from "../i18n";
import { ArrowLeftOutlined, CheckCircleFilled, CloudOutlined, LockOutlined, SafetyCertificateOutlined, UserOutlined } from "@ant-design/icons";
import { Turnstile, type TurnstileInstance } from "@marsidev/react-turnstile";
import { Alert, App, Button, Card, Divider, Form, Input, Space, Spin, Steps, Typography } from "antd";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { ApiError, authClient } from "../api/client";
import { LanguageSwitcher } from "../components/LanguageSwitcher";
import type { TurnstileConfig } from "../types";
type Credentials = {
    username: string;
    password: string;
};
export function LoginPage() {
    const navigate = useNavigate();
    const { message } = App.useApp();
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
                setConfigError(error instanceof Error ? error.message : tr("\u65E0\u6CD5\u8BFB\u53D6\u7BA1\u7406\u5458\u767B\u5F55\u914D\u7F6E\u3002"));
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
            message.warning(tr("\u8BF7\u5148\u5B8C\u6210\u5B89\u5168\u9A8C\u8BC1\u3002"));
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
            message.success(tr("\u8D26\u53F7\u5BC6\u7801\u5DF2\u786E\u8BA4\uFF0C\u8BF7\u5B8C\u6210\u52A8\u6001\u9A8C\u8BC1\u7801\u9A8C\u8BC1\u3002"));
        }
        catch (error) {
            resetTurnstile();
            message.error(loginError(error));
        }
        finally {
            setSubmitting(false);
        }
    };
    const verifyMFA = async ({ code }: {
        code: string;
    }) => {
        setSubmitting(true);
        try {
            await authClient.verifyMFA({
                challenge_token: challengeToken,
                code
            });
            message.success(tr("\u8EAB\u4EFD\u9A8C\u8BC1\u5B8C\u6210\u3002"));
            navigate("/", { replace: true });
        }
        catch (error) {
            message.error(loginError(error));
        }
        finally {
            setSubmitting(false);
        }
    };
    return (<main className="login-page">
      <div className="login-language-switcher"><LanguageSwitcher /></div>
      <section className="login-story" aria-label={tr("\u7BA1\u7406\u5165\u53E3\u5B89\u5168\u8BF4\u660E")}>
        <div className="login-story-inner">
          <div className="login-brand">
            <div className="brand-mark large">R</div>
            <div>
              <div className="brand-name light">ProjectRebound</div>
              <div className="brand-subtitle light">CONTROL PLANE</div>
            </div>
          </div>
          <div className="login-kicker">{tr("\u53D7\u4FDD\u62A4\u7684\u8FD0\u8425\u5165\u53E3")}</div>
          <Typography.Title level={1}>{tr("\u8BA9\u6BCF\u4E00\u6B21\u7BA1\u7406\u64CD\u4F5C")}<br />{tr("\u90FD\u6709\u8EAB\u4EFD\u3001\u6709\u8FB9\u754C\u3001\u6709\u8BB0\u5F55")}</Typography.Title>
          <Typography.Paragraph className="login-lead">{tr("\u7BA1\u7406\u5458\u8BA4\u8BC1\u4E0E\u73A9\u5BB6\u7CFB\u7EDF\u5B8C\u5168\u9694\u79BB\u3002\u767B\u5F55\u9700\u8981\u901A\u8FC7\u6D4F\u89C8\u5668\u53CD\u81EA\u52A8\u5316\u6821\u9A8C\u3001\u8D26\u53F7\u5BC6\u7801\u548C\u52A8\u6001\u9A8C\u8BC1\u7801\uFF0C\u5199\u64CD\u4F5C\u7531\u63A7\u5236\u9762\u7EDF\u4E00\u6388\u6743\u5E76\u8BB0\u5F55\u5BA1\u8BA1\u3002")}</Typography.Paragraph>
          <div className="security-chain">
            {[
            ["01", tr("\u53EF\u4FE1\u7F51\u7EDC"), tr("VPN / \u96F6\u4FE1\u4EFB\u8BBF\u95EE\u4EE3\u7406")],
            ["02", tr("\u53CD\u81EA\u52A8\u5316"), "Cloudflare Turnstile"],
            ["03", tr("\u5F3A\u8EAB\u4EFD"), tr("\u5BC6\u7801 + TOTP / \u6062\u590D\u7801")],
            ["04", tr("\u6700\u5C0F\u6743\u9650"), tr("\u670D\u52A1\u7AEF RBAC + \u5BA1\u8BA1")]
        ].map(([index, title, description]) => (<div className="security-chain-item" key={index}>
                <span>{index}</span>
                <div>
                  <strong>{title}</strong>
                  <small>{description}</small>
                </div>
                <CheckCircleFilled />
              </div>))}
          </div>
          <div className="login-footnote">
            <SafetyCertificateOutlined />{tr("\u6B64\u5165\u53E3\u4E0D\u63A5\u53D7\u73A9\u5BB6 Access Token \u6216\u673A\u5668\u8FD0\u7EF4 Token")}</div>
        </div>
      </section>
      <section className="login-form-column">
        <Card className="login-card" variant="borderless">
          <div className="login-card-heading">
            <Typography.Text type="secondary">
              {challengeToken ? tr("\u7B2C\u4E8C\u6B65\uFF0C\u5171\u4E24\u6B65") : tr("\u7B2C\u4E00\u6B65\uFF0C\u5171\u4E24\u6B65")}
            </Typography.Text>
            <Typography.Title level={2}>
              {challengeToken ? tr("\u9A8C\u8BC1\u52A8\u6001\u9A8C\u8BC1\u7801") : tr("\u767B\u5F55\u7BA1\u7406\u63A7\u5236\u53F0")}
            </Typography.Title>
            <Typography.Paragraph type="secondary">
              {challengeToken
            ? tr("\u8F93\u5165\u8BA4\u8BC1\u5668\u4E2D\u7684\u516D\u4F4D\u9A8C\u8BC1\u7801\uFF0C\u6216\u4F7F\u7528\u4E00\u679A\u672A\u4F7F\u7528\u7684\u6062\u590D\u7801\u3002") : tr("\u4F7F\u7528\u72EC\u7ACB\u7BA1\u7406\u5458\u8D26\u53F7\u767B\u5F55\u3002\u8FDE\u7EED\u5931\u8D25\u4F1A\u89E6\u53D1\u8D26\u53F7\u548C\u6765\u6E90\u5730\u5740\u9650\u6D41\u3002")}
            </Typography.Paragraph>
          </div>
          <Steps size="small" current={challengeToken ? 1 : 0} items={[{ title: tr("\u8D26\u53F7\u9A8C\u8BC1") }, { title: tr("\u52A8\u6001\u9A8C\u8BC1\u7801") }]} className="login-steps"/>
          <Divider />
          {configError && <Alert type="error" showIcon title={configError}/>}
          {!config && !configError && (<div className="login-loading">
              <Spin />
              <span>{tr("\u6B63\u5728\u52A0\u8F7D\u5B89\u5168\u914D\u7F6E\u2026")}</span>
            </div>)}
          {config && !config.configured && (<Alert type="warning" showIcon title={tr("\u7BA1\u7406\u5458\u767B\u5F55\u5C1A\u672A\u542F\u7528")} description={tr("\u63A7\u5236\u9762\u7F3A\u5C11 Turnstile Sitekey \u6216 Secret\u3002\u914D\u7F6E\u5B8C\u6210\u524D\u767B\u5F55\u4F1A\u5931\u8D25\u5173\u95ED\u3002")}/>)}
          {!challengeToken ? (<Form<Credentials> layout="vertical" requiredMark={false} onFinish={beginLogin} className="login-form">
              <Form.Item label={tr("\u7BA1\u7406\u5458\u8D26\u53F7")} name="username" rules={[{ required: true, message: tr("\u8BF7\u8F93\u5165\u7BA1\u7406\u5458\u8D26\u53F7\u3002") }]}>
                <Input size="large" autoComplete="username" prefix={<UserOutlined />} placeholder="operator@example.com"/>
              </Form.Item>
              <Form.Item label={tr("\u5BC6\u7801")} name="password" rules={[{ required: true, message: tr("\u8BF7\u8F93\u5165\u5BC6\u7801\u3002") }]}>
                <Input.Password size="large" autoComplete="current-password" prefix={<LockOutlined />} placeholder={tr("\u8F93\u5165\u7BA1\u7406\u5458\u5BC6\u7801")}/>
              </Form.Item>
              {config?.configured && (<div className="turnstile-block">
                  <div className="turnstile-label">
                    <Space>
                      <CloudOutlined />{tr("\u767B\u5F55\u5B89\u5168\u9A8C\u8BC1")}</Space>
                    <Typography.Text type="secondary">{tr("\u7531 Cloudflare \u63D0\u4F9B")}</Typography.Text>
                  </div>
                  <Turnstile ref={turnstileRef} siteKey={config.site_key} onSuccess={setTurnstileToken} onExpire={() => setTurnstileToken("")} onTimeout={() => setTurnstileToken("")} onError={() => {
                    setTurnstileToken("");
                    message.error(tr("\u5B89\u5168\u9A8C\u8BC1\u52A0\u8F7D\u5931\u8D25\uFF0C\u8BF7\u68C0\u67E5\u7F51\u7EDC\u540E\u91CD\u8BD5\u3002"));
                }} options={{
                    action: config.action,
                    appearance: "interaction-only",
                    language: turnstileLanguage(),
                    size: "flexible",
                    theme: "light",
                    refreshExpired: "auto",
                    refreshTimeout: "auto"
                }}/>
                </div>)}
              <Button type="primary" htmlType="submit" size="large" block loading={submitting} disabled={!config?.configured || !turnstileToken}>{tr("\u7EE7\u7EED\u9A8C\u8BC1")}</Button>
            </Form>) : (<Form<{
            code: string;
        }> layout="vertical" requiredMark={false} onFinish={verifyMFA} className="login-form">
              <Form.Item label={tr("\u52A8\u6001\u9A8C\u8BC1\u7801\u6216\u6062\u590D\u7801")} name="code" rules={[{ required: true, message: tr("\u8BF7\u8F93\u5165\u9A8C\u8BC1\u7801\u6216\u6062\u590D\u7801\u3002") }]}>
                <Input size="large" autoComplete="one-time-code" inputMode="numeric" prefix={<SafetyCertificateOutlined />} placeholder="000000" maxLength={32}/>
              </Form.Item>
              <Typography.Paragraph type="secondary" className="challenge-expiry">{tr("\u672C\u6B21\u9A8C\u8BC1\u8BF7\u6C42\u5C06\u5728")}{" "}
                {new Date(challengeExpiresAt).toLocaleTimeString(localeTag(), {
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit"
            })}{" "}{tr("\u5931\u6548\u3002")}</Typography.Paragraph>
              <Button type="primary" htmlType="submit" size="large" block loading={submitting}>{tr("\u9A8C\u8BC1\u5E76\u8FDB\u5165\u63A7\u5236\u53F0")}</Button>
              <Button type="text" block icon={<ArrowLeftOutlined />} onClick={() => {
                setChallengeToken("");
                setChallengeExpiresAt("");
                resetTurnstile();
            }}>{tr("\u8FD4\u56DE\u8D26\u53F7\u9A8C\u8BC1")}</Button>
            </Form>)}
        </Card>
        <Typography.Text type="secondary" className="login-help">{tr("\u65E0\u6CD5\u767B\u5F55\uFF1F\u8BF7\u901A\u8FC7\u5185\u90E8\u503C\u73ED\u6E20\u9053\u8054\u7CFB\u5E73\u53F0\u7BA1\u7406\u5458\uFF0C\u4E0D\u8981\u53D1\u9001\u5BC6\u7801\u3001\u9A8C\u8BC1\u7801\u6216 Token\u3002")}</Typography.Text>
      </section>
    </main>);
}
function loginError(error: unknown) {
    if (error instanceof ApiError) {
        const requestID = error.requestId ? tr(`（请求编号：${error.requestId}）`) : "";
        return `${error.message}${requestID}`;
    }
    return error instanceof Error ? error.message : tr("\u767B\u5F55\u5931\u8D25\uFF0C\u8BF7\u7A0D\u540E\u91CD\u8BD5\u3002");
}
