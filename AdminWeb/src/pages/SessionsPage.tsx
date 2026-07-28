import { localeTag, tr } from "../i18n";
import { DeleteOutlined, LaptopOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, App, Button, Card, List, Popconfirm, Space, Tag, Typography } from "antd";
import { useCallback, useEffect, useState } from "react";
import { ApiError, apiRequest } from "../api/client";
import type { AdminSession } from "../types";
export function SessionsPage() {
    const { message } = App.useApp();
    const [items, setItems] = useState<AdminSession[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState("");
    const load = useCallback(async () => {
        setLoading(true);
        setError("");
        try {
            const result = await apiRequest<{
                items: AdminSession[];
            }>("/v1/admin/auth/sessions");
            setItems(result.items);
        }
        catch (caught) {
            setError(caught instanceof Error ? caught.message : tr("\u65E0\u6CD5\u8BFB\u53D6\u7BA1\u7406\u5458\u4F1A\u8BDD\u3002"));
        }
        finally {
            setLoading(false);
        }
    }, []);
    useEffect(() => {
        void load();
    }, [load]);
    const revoke = async (sessionID: string) => {
        try {
            await apiRequest(`/v1/admin/auth/sessions/${encodeURIComponent(sessionID)}`, { method: "DELETE" });
            message.success(tr("\u7BA1\u7406\u5458\u4F1A\u8BDD\u5DF2\u64A4\u9500\u3002"));
            await load();
        }
        catch (caught) {
            const requestID = caught instanceof ApiError && caught.requestId
                ? tr(`（请求编号：${caught.requestId}）`) : "";
            message.error(`${caught instanceof Error ? caught.message : tr("\u64A4\u9500\u5931\u8D25\u3002")}${requestID}`);
        }
    };
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / SESSIONS</Typography.Text>
          <Typography.Title level={2}>{tr("\u6211\u7684\u7BA1\u7406\u5458\u4F1A\u8BDD")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("Refresh Token \u4E0D\u4F1A\u663E\u793A\u5728\u6B64\u9875\u9762\u3002\u64A4\u9500\u5F53\u524D\u4F1A\u8BDD\u540E\u9700\u8981\u91CD\u65B0\u5B8C\u6210 Turnstile\u3001\u5BC6\u7801\u548C TOTP \u767B\u5F55\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>{tr("\u5237\u65B0")}</Button>
      </section>
      {error && <Alert type="error" showIcon message={error}/>}
      <Card className="dashboard-card">
        <List<AdminSession> loading={loading} dataSource={items} locale={{ emptyText: tr("\u6CA1\u6709\u53EF\u7528\u7684\u7BA1\u7406\u5458\u4F1A\u8BDD\u3002") }} renderItem={(item) => (<List.Item actions={[
                <Popconfirm key="revoke" title={tr("\u64A4\u9500\u8BE5\u7BA1\u7406\u5458\u4F1A\u8BDD\uFF1F")} description={item.is_current ? tr("\u8FD9\u662F\u5F53\u524D\u4F1A\u8BDD\uFF0C\u64A4\u9500\u540E\u9700\u8981\u91CD\u65B0\u767B\u5F55\u3002") : tr("\u8BE5\u8BBE\u5907\u5C06\u65E0\u6CD5\u7EE7\u7EED\u5237\u65B0\u7BA1\u7406\u5458\u51ED\u636E\u3002")} okText={tr("\u786E\u8BA4\u64A4\u9500")} cancelText={tr("\u53D6\u6D88")} onConfirm={() => revoke(item.session_id)}>
                  <Button danger type="text" icon={<DeleteOutlined />}>{tr("\u64A4\u9500")}</Button>
                </Popconfirm>
            ]}>
              <List.Item.Meta avatar={<div className="session-device"><LaptopOutlined /></div>} title={<Space>
                    <span>{browserName(item.user_agent)}</span>
                    {item.is_current && <Tag color="green">{tr("\u5F53\u524D\u4F1A\u8BDD")}</Tag>}
                  </Space>} description={<Space direction="vertical" size={2}>
                    <Typography.Text type="secondary">
                      {item.ip_address || tr("\u672A\u77E5\u6765\u6E90\u5730\u5740")}{tr("\u00B7 \u521B\u5EFA\u4E8E")}{formatTime(item.created_at)}
                    </Typography.Text>
                    <Typography.Text type="secondary">{tr("\u6700\u8FD1\u4F7F\u7528")}{item.last_used_at ? formatTime(item.last_used_at) : tr("\u5C1A\u672A\u8BB0\u5F55")}{tr("\u00B7\n                      \u5230\u671F")}{formatTime(item.expires_at)}
                    </Typography.Text>
                  </Space>}/>
            </List.Item>)}/>
      </Card>
    </div>);
}
function browserName(userAgent: string) {
    if (/Edg\//.test(userAgent))
        return "Microsoft Edge";
    if (/Chrome\//.test(userAgent))
        return "Google Chrome";
    if (/Firefox\//.test(userAgent))
        return "Mozilla Firefox";
    if (/Safari\//.test(userAgent))
        return "Safari";
    return userAgent || tr("\u672A\u77E5\u6D4F\u89C8\u5668");
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
