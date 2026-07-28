import { localeTag, tr } from "../i18n";
import { ArrowLeftOutlined, CrownOutlined, DisconnectOutlined, SafetyOutlined, StopOutlined } from "@ant-design/icons";
import { useOne } from "@refinedev/core";
import { Alert, App, Button, Card, Checkbox, Descriptions, Form, Input, Modal, Skeleton, Space, Table, Tag, Tabs, Typography } from "antd";
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ApiError, apiRequest, authClient } from "../api/client";
import type { Player, PlayerLoginEvent, PlayerSession, RiskEvent } from "../types";
type PlayerAction = "activate" | "ban" | "vip" | "revoke";
type ActionForm = {
    reason: string;
    internal_note?: string;
    revoke_sessions?: boolean;
};
export function PlayerShowPage() {
    const { id = "" } = useParams();
    const navigate = useNavigate();
    const { message } = App.useApp();
    const [actionForm] = Form.useForm<ActionForm>();
    const [working, setWorking] = useState(false);
    const [action, setAction] = useState<PlayerAction | null>(null);
    const [activityLoading, setActivityLoading] = useState(true);
    const [sessions, setSessions] = useState<PlayerSession[]>([]);
    const [riskEvents, setRiskEvents] = useState<RiskEvent[]>([]);
    const [loginEvents, setLoginEvents] = useState<PlayerLoginEvent[]>([]);
    const { query, result: player } = useOne<Player>({
        resource: "players",
        id
    });
    const permissions = authClient.permissions();
    useEffect(() => {
        if (!id) {
            return;
        }
        let active = true;
        setActivityLoading(true);
        Promise.all([
            apiRequest<{
                items: PlayerSession[];
            }>(`/v1/admin/players/${encodeURIComponent(id)}/sessions`),
            apiRequest<{
                items: RiskEvent[];
            }>(`/v1/admin/players/${encodeURIComponent(id)}/risk-events`),
            apiRequest<{
                items: PlayerLoginEvent[];
            }>(`/v1/admin/players/${encodeURIComponent(id)}/login-events`)
        ])
            .then(([sessionResult, riskResult, loginResult]) => {
            if (active) {
                setSessions(sessionResult.items);
                setRiskEvents(riskResult.items);
                setLoginEvents(loginResult.items);
            }
        })
            .catch((error) => {
            if (active) {
                message.error(apiMessage(error));
            }
        })
            .finally(() => {
            if (active) {
                setActivityLoading(false);
            }
        });
        return () => {
            active = false;
        };
    }, [id, message]);
    const patch = async (values: Partial<Pick<Player, "account_status" | "is_vip">> & {
        revoke_sessions?: boolean;
        reason: string;
        internal_note?: string;
    }, success: string) => {
        setWorking(true);
        try {
            await apiRequest(`/v1/admin/players/${encodeURIComponent(id)}`, {
                method: "PATCH",
                body: JSON.stringify(values)
            });
            message.success(success);
            await query.refetch();
        }
        catch (error) {
            message.error(apiMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const openAction = (nextAction: PlayerAction) => {
        actionForm.resetFields();
        if (nextAction === "ban") {
            actionForm.setFieldValue("revoke_sessions", true);
        }
        setAction(nextAction);
    };
    const submitAction = async (values: ActionForm) => {
        if (!action || !player) {
            return;
        }
        if (action === "revoke") {
            setWorking(true);
            try {
                await apiRequest(`/v1/admin/players/${encodeURIComponent(id)}/revoke-sessions`, {
                    method: "POST",
                    body: JSON.stringify({ reason: values.reason })
                });
                message.success(tr("\u73A9\u5BB6\u4F1A\u8BDD\u5DF2\u64A4\u9500\u3002"));
                setAction(null);
            }
            catch (error) {
                message.error(apiMessage(error));
            }
            finally {
                setWorking(false);
            }
            return;
        }
        const common = {
            reason: values.reason,
            internal_note: values.internal_note
        };
        if (action === "ban") {
            await patch({
                ...common,
                account_status: "BANNED",
                revoke_sessions: values.revoke_sessions
            }, values.revoke_sessions ? tr("\u73A9\u5BB6\u8D26\u53F7\u5DF2\u5C01\u7981\u5E76\u64A4\u9500\u5168\u90E8\u4F1A\u8BDD\u3002") : tr("\u73A9\u5BB6\u8D26\u53F7\u5DF2\u5C01\u7981\u3002"));
        }
        else if (action === "activate") {
            await patch({ ...common, account_status: "ACTIVE" }, tr("\u73A9\u5BB6\u8D26\u53F7\u5DF2\u6062\u590D\u3002"));
        }
        else {
            await patch({ ...common, is_vip: !player.is_vip }, player.is_vip ? tr("VIP \u5DF2\u53D6\u6D88\u3002") : tr("VIP \u5DF2\u542F\u7528\u3002"));
        }
        setAction(null);
    };
    if (query.isLoading) {
        return <Card><Skeleton active/></Card>;
    }
    if (!player || query.isError) {
        return (<Alert type="error" showIcon message={tr("\u65E0\u6CD5\u8BFB\u53D6\u73A9\u5BB6\u8BE6\u60C5")} description={tr("\u8BE5\u73A9\u5BB6\u53EF\u80FD\u4E0D\u5B58\u5728\uFF0C\u6216\u5F53\u524D\u7BA1\u7406\u5458\u6CA1\u6709\u67E5\u770B\u6743\u9650\u3002")} action={<Button onClick={() => navigate("/players")}>{tr("\u8FD4\u56DE\u5217\u8868")}</Button>}/>);
    }
    return (<div className="page-stack">
      <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate("/players")} className="back-button">{tr("\u8FD4\u56DE\u73A9\u5BB6\u5217\u8868")}</Button>
      <section className="player-profile-header">
        <div className="player-avatar">{(player.persona_name || "P").slice(0, 1).toUpperCase()}</div>
        <div>
          <Space wrap>
            <Typography.Title level={2}>{player.persona_name || tr("\u672A\u547D\u540D\u73A9\u5BB6")}</Typography.Title>
            <Tag color={player.account_status === "ACTIVE" ? "green" : "red"}>
              {player.account_status}
            </Tag>
            {player.is_vip && <Tag color="gold">VIP</Tag>}
          </Space>
          <Typography.Text type="secondary" copyable>
            {player.player_id}
          </Typography.Text>
        </div>
      </section>

      <Card title={tr("\u8D26\u53F7\u8D44\u6599")} className="dashboard-card">
        <Descriptions column={{ xs: 1, md: 2, xl: 3 }} bordered>
          <Descriptions.Item label="SteamID">{player.steam_id}</Descriptions.Item>
          <Descriptions.Item label={tr("\u8BA4\u8BC1\u63D0\u4F9B\u65B9")}>{player.auth_provider}</Descriptions.Item>
          <Descriptions.Item label={tr("\u8BA4\u8BC1\u7B49\u7EA7")}>{player.auth_level}</Descriptions.Item>
          <Descriptions.Item label={tr("\u6CE8\u518C\u65F6\u95F4")}>{formatTime(player.created_at)}</Descriptions.Item>
          <Descriptions.Item label={tr("\u6700\u540E\u767B\u5F55")}>{formatTime(player.last_login_at)}</Descriptions.Item>
          <Descriptions.Item label={tr("\u66F4\u65B0\u65F6\u95F4")}>{formatTime(player.updated_at)}</Descriptions.Item>
        </Descriptions>
      </Card>

      <Card title={tr("\u7BA1\u7406\u64CD\u4F5C")} className="dashboard-card">
        <Typography.Paragraph type="secondary">{tr("\u8D26\u53F7\u72B6\u6001\u4E0E\u4F1A\u8BDD\u64CD\u4F5C\u4F1A\u7531\u63A7\u5236\u9762\u5199\u5165\u5BA1\u8BA1\u65E5\u5FD7\u3002\u524D\u7AEF\u6743\u9650\u4EC5\u7528\u4E8E\u6539\u5584\u4F53\u9A8C\uFF0C\u540E\u7AEF\u4ECD\u4F1A\u518D\u6B21\u6821\u9A8C\u3002")}</Typography.Paragraph>
        <Space wrap>
          {permissions.includes("players.update_status") && (player.account_status === "BANNED" ? (<Button icon={<SafetyOutlined />} loading={working} onClick={() => openAction("activate")}>{tr("\u6062\u590D\u4E3A\u6B63\u5E38")}</Button>) : (<Button danger icon={<StopOutlined />} loading={working} onClick={() => openAction("ban")}>{tr("\u5C01\u7981\u73A9\u5BB6")}</Button>))}
          {permissions.includes("players.update_vip") && (<Button icon={<CrownOutlined />} loading={working} onClick={() => openAction("vip")}>
              {player.is_vip ? tr("\u53D6\u6D88 VIP") : tr("\u8BBE\u4E3A VIP")}
            </Button>)}
          {permissions.includes("players.revoke_sessions") && (<Button danger icon={<DisconnectOutlined />} loading={working} onClick={() => openAction("revoke")}>{tr("\u64A4\u9500\u5168\u90E8\u4F1A\u8BDD")}</Button>)}
        </Space>
      </Card>

      <Card title={tr("\u8BA4\u8BC1\u6D3B\u52A8\u4E0E\u98CE\u9669")} className="dashboard-card">
        <Tabs items={[
            {
                key: "sessions",
                label: `Session (${sessions.length})`,
                children: (<Table<PlayerSession> rowKey="session_id" size="small" loading={activityLoading} pagination={false} dataSource={sessions} scroll={{ x: 900 }} columns={[
                        {
                            title: tr("\u72B6\u6001"),
                            dataIndex: "active",
                            render: (value: boolean) => (<Tag color={value ? "green" : "default"}>{value ? tr("\u6709\u6548") : tr("\u5DF2\u5931\u6548")}</Tag>)
                        },
                        { title: tr("\u8BBE\u5907\u6458\u8981"), dataIndex: "device_id_suffix", render: (value: string) => value ? `…${value}` : "—" },
                        { title: tr("IP \u6458\u8981"), dataIndex: "ip_address", render: (value: string) => value || "—" },
                        { title: tr("\u521B\u5EFA\u65F6\u95F4"), dataIndex: "created_at", render: formatTime },
                        { title: tr("\u6700\u8FD1\u6D3B\u52A8"), dataIndex: "last_used_at", render: (value: string | null) => value ? formatTime(value) : "—" },
                        { title: tr("\u64A4\u9500\u539F\u56E0"), dataIndex: "revoked_reason", render: (value: string) => value || "—" }
                    ]} locale={{ emptyText: tr("\u8BE5\u73A9\u5BB6\u6CA1\u6709\u53EF\u663E\u793A\u7684 Session\u3002") }}/>)
            },
            {
                key: "logins",
                label: tr(`登录历史 (${loginEvents.length})`),
                children: (<Table<PlayerLoginEvent> rowKey="id" size="small" loading={activityLoading} pagination={false} dataSource={loginEvents} scroll={{ x: 900 }} columns={[
                        {
                            title: tr("\u7ED3\u679C"),
                            dataIndex: "result",
                            render: (value: PlayerLoginEvent["result"]) => (<Tag color={value === "SUCCESS" ? "green" : "red"}>{value}</Tag>)
                        },
                        { title: tr("\u5931\u8D25\u539F\u56E0"), dataIndex: "failure_code", render: (value: string) => value || "—" },
                        { title: tr("IP \u6458\u8981"), dataIndex: "ip_address", render: (value: string) => value || "—" },
                        { title: "User-Agent", dataIndex: "user_agent", ellipsis: true },
                        { title: tr("\u65F6\u95F4"), dataIndex: "created_at", render: formatTime }
                    ]} locale={{ emptyText: tr("\u8BE5\u73A9\u5BB6\u6CA1\u6709\u53EF\u663E\u793A\u7684\u767B\u5F55\u5386\u53F2\u3002") }}/>)
            },
            {
                key: "risks",
                label: tr(`风险事件 (${riskEvents.length})`),
                children: (<Table<RiskEvent> rowKey="id" size="small" loading={activityLoading} pagination={false} dataSource={riskEvents} scroll={{ x: 800 }} columns={[
                        { title: tr("\u4E8B\u4EF6"), dataIndex: "event_type" },
                        { title: tr("\u4E25\u91CD\u5EA6"), dataIndex: "severity", render: (value: string) => <Tag>{value}</Tag> },
                        { title: tr("IP \u6458\u8981"), dataIndex: "ip_address", render: (value: string) => value || "—" },
                        { title: tr("\u65F6\u95F4"), dataIndex: "created_at", render: formatTime },
                        { title: tr("\u72B6\u6001"), dataIndex: "resolved_at", render: (value: string | null) => value ? tr("\u5DF2\u5904\u7406") : tr("\u5F85\u5904\u7406") }
                    ]} locale={{ emptyText: tr("\u8BE5\u73A9\u5BB6\u6CA1\u6709\u5173\u8054\u98CE\u9669\u4E8B\u4EF6\u3002") }}/>)
            }
        ]}/>
      </Card>

      <Modal open={action !== null} title={actionTitle(action, player.is_vip)} okText={action === "revoke" ? tr("\u786E\u8BA4\u64A4\u9500") : tr("\u786E\u8BA4\u6267\u884C")} cancelText={tr("\u53D6\u6D88")} confirmLoading={working} okButtonProps={{ danger: action === "ban" || action === "revoke" }} onCancel={() => !working && setAction(null)} onOk={() => actionForm.submit()} destroyOnHidden>
        <Alert type={action === "ban" || action === "revoke" ? "warning" : "info"} showIcon message={actionConsequence(action, player.is_vip)} className="action-alert"/>
        <Form<ActionForm> form={actionForm} layout="vertical" requiredMark={false} onFinish={submitAction}>
          <Form.Item label={tr("\u64CD\u4F5C\u539F\u56E0")} name="reason" rules={[
            { required: true, whitespace: true, message: tr("\u8BF7\u586B\u5199\u53EF\u4F9B\u5BA1\u8BA1\u8FFD\u6EAF\u7684\u64CD\u4F5C\u539F\u56E0\u3002") },
            { max: 500, message: tr("\u64CD\u4F5C\u539F\u56E0\u4E0D\u80FD\u8D85\u8FC7 500 \u4E2A\u5B57\u7B26\u3002") }
        ]}>
            <Input.TextArea rows={3} maxLength={500} showCount placeholder={tr("\u4F8B\u5982\uFF1A\u5BA2\u670D\u5DE5\u5355 CS-4812 \u5DF2\u786E\u8BA4\u8D26\u53F7\u8FDD\u89C4")}/>
          </Form.Item>
          {action !== "revoke" && (<Form.Item label={tr("\u5185\u90E8\u5907\u6CE8\uFF08\u53EF\u9009\uFF09")} name="internal_note" rules={[{ max: 2000, message: tr("\u5185\u90E8\u5907\u6CE8\u4E0D\u80FD\u8D85\u8FC7 2000 \u4E2A\u5B57\u7B26\u3002") }]}>
              <Input.TextArea rows={2} maxLength={2000} placeholder={tr("\u8BB0\u5F55\u590D\u6838\u4EBA\u3001\u5DE5\u5355\u6216\u8865\u5145\u80CC\u666F\uFF1B\u8BF7\u52FF\u586B\u5199\u5BC6\u7801\u3001Token \u6216\u9690\u79C1\u6570\u636E")}/>
            </Form.Item>)}
          {action === "ban" && (<Form.Item name="revoke_sessions" valuePropName="checked">
              <Checkbox>{tr("\u7ACB\u5373\u64A4\u9500\u8BE5\u73A9\u5BB6\u7684\u5168\u90E8\u4F1A\u8BDD\uFF0C\u4F7F\u73B0\u6709 Token \u5931\u6548")}</Checkbox>
            </Form.Item>)}
        </Form>
      </Modal>
    </div>);
}
function actionTitle(action: PlayerAction | null, isVIP: boolean) {
    switch (action) {
        case "activate":
            return tr("\u6062\u590D\u8BE5\u73A9\u5BB6\u8D26\u53F7\uFF1F");
        case "ban":
            return tr("\u5C01\u7981\u8BE5\u73A9\u5BB6\uFF1F");
        case "vip":
            return isVIP ? tr("\u53D6\u6D88\u8BE5\u73A9\u5BB6\u7684 VIP\uFF1F") : tr("\u5C06\u8BE5\u73A9\u5BB6\u8BBE\u4E3A VIP\uFF1F");
        case "revoke":
            return tr("\u64A4\u9500\u8BE5\u73A9\u5BB6\u7684\u5168\u90E8\u4F1A\u8BDD\uFF1F");
        default:
            return tr("\u786E\u8BA4\u7BA1\u7406\u64CD\u4F5C");
    }
}
function actionConsequence(action: PlayerAction | null, isVIP: boolean) {
    switch (action) {
        case "activate":
            return tr("\u8D26\u53F7\u5C06\u6062\u590D\u767B\u5F55\u548C\u4F7F\u7528\u6743\u9650\u3002");
        case "ban":
            return tr("\u8D26\u53F7\u5C06\u65E0\u6CD5\u7EE7\u7EED\u4F7F\u7528\uFF1B\u53EF\u540C\u65F6\u64A4\u9500\u5168\u90E8\u4F1A\u8BDD\u4EE5\u7ACB\u5373\u4E0B\u7EBF\u3002");
        case "vip":
            return isVIP ? tr("\u73A9\u5BB6\u5C06\u5931\u53BB VIP \u6743\u76CA\u3002") : tr("\u73A9\u5BB6\u5C06\u7ACB\u5373\u83B7\u5F97\u5F53\u524D\u914D\u7F6E\u7684 VIP \u6743\u76CA\u3002");
        case "revoke":
            return tr("\u6240\u6709\u73B0\u6709 Access Token \u548C Refresh Token \u5C06\u7ACB\u5373\u5931\u6548\uFF0C\u73A9\u5BB6\u9700\u8981\u91CD\u65B0\u767B\u5F55\u3002");
        default:
            return "";
    }
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
function apiMessage(error: unknown) {
    if (error instanceof ApiError) {
        return error.requestId ? tr(`${error.message}（请求编号：${error.requestId}）`) : error.message;
    }
    return error instanceof Error ? error.message : tr("\u64CD\u4F5C\u5931\u8D25\u3002");
}
