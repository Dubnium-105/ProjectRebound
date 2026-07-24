import {DeleteOutlined, LaptopOutlined, ReloadOutlined} from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Card,
  List,
  Popconfirm,
  Space,
  Tag,
  Typography
} from "antd";
import {useCallback, useEffect, useState} from "react";
import {ApiError, apiRequest} from "../api/client";
import type {AdminSession} from "../types";

export function SessionsPage() {
  const {message} = App.useApp();
  const [items, setItems] = useState<AdminSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const result = await apiRequest<{items: AdminSession[]}>(
        "/v1/admin/auth/sessions"
      );
      setItems(result.items);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "无法读取管理员会话。");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const revoke = async (sessionID: string) => {
    try {
      await apiRequest(
        `/v1/admin/auth/sessions/${encodeURIComponent(sessionID)}`,
        {method: "DELETE"}
      );
      message.success("管理员会话已撤销。");
      await load();
    } catch (caught) {
      const requestID =
        caught instanceof ApiError && caught.requestId
          ? `（请求编号：${caught.requestId}）`
          : "";
      message.error(
        `${caught instanceof Error ? caught.message : "撤销失败。"}${requestID}`
      );
    }
  };

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / SESSIONS</Typography.Text>
          <Typography.Title level={2}>我的管理员会话</Typography.Title>
          <Typography.Paragraph type="secondary">
            Refresh Token 不会显示在此页面。撤销当前会话后需要重新完成 Turnstile、密码和 TOTP 登录。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>
          刷新
        </Button>
      </section>
      {error && <Alert type="error" showIcon message={error} />}
      <Card className="dashboard-card">
        <List<AdminSession>
          loading={loading}
          dataSource={items}
          locale={{emptyText: "没有可用的管理员会话。"}}
          renderItem={(item) => (
            <List.Item
              actions={[
                <Popconfirm
                  key="revoke"
                  title="撤销该管理员会话？"
                  description={item.is_current ? "这是当前会话，撤销后需要重新登录。" : "该设备将无法继续刷新管理员凭据。"}
                  okText="确认撤销"
                  cancelText="取消"
                  onConfirm={() => revoke(item.session_id)}
                >
                  <Button danger type="text" icon={<DeleteOutlined />}>
                    撤销
                  </Button>
                </Popconfirm>
              ]}
            >
              <List.Item.Meta
                avatar={<div className="session-device"><LaptopOutlined /></div>}
                title={
                  <Space>
                    <span>{browserName(item.user_agent)}</span>
                    {item.is_current && <Tag color="green">当前会话</Tag>}
                  </Space>
                }
                description={
                  <Space direction="vertical" size={2}>
                    <Typography.Text type="secondary">
                      {item.ip_address || "未知来源地址"} · 创建于 {formatTime(item.created_at)}
                    </Typography.Text>
                    <Typography.Text type="secondary">
                      最近使用 {item.last_used_at ? formatTime(item.last_used_at) : "尚未记录"} ·
                      到期 {formatTime(item.expires_at)}
                    </Typography.Text>
                  </Space>
                }
              />
            </List.Item>
          )}
        />
      </Card>
    </div>
  );
}

function browserName(userAgent: string) {
  if (/Edg\//.test(userAgent)) return "Microsoft Edge";
  if (/Chrome\//.test(userAgent)) return "Google Chrome";
  if (/Firefox\//.test(userAgent)) return "Mozilla Firefox";
  if (/Safari\//.test(userAgent)) return "Safari";
  return userAgent || "未知浏览器";
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}
