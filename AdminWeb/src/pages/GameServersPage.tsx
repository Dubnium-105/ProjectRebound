import { localeTag, tr } from "../i18n";
import { CopyOutlined, PauseCircleOutlined, PlusOutlined, ReloadOutlined, StopOutlined, SyncOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Alert, App, Button, Card, Form, Input, InputNumber, Modal, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { GameServer } from "../types";
type ServerOperation = "drain" | "resume" | "disable";
type RegistrationValues = {
    instance_id: string;
    expires_in_hours: number;
    reason: string;
    mfa_code: string;
};
type CreatedRegistration = {
    registration_id: string;
    instance_id: string;
    registration_token: string;
    expires_at: string;
};
export function GameServersPage() {
    const { message } = App.useApp();
    const [registrationForm] = Form.useForm<RegistrationValues>();
    const [registrationOpen, setRegistrationOpen] = useState(false);
    const [createdRegistration, setCreatedRegistration] = useState<CreatedRegistration | null>(null);
    const [target, setTarget] = useState<{
        server: GameServer;
        operation: ServerOperation;
    } | null>(null);
    const [working, setWorking] = useState(false);
    const { query, result } = useList<GameServer>({
        resource: "game-servers",
        pagination: { pageSize: 100 },
        queryOptions: { refetchInterval: 10000 }
    });
    const permissions = authClient.permissions();
    const openRegistration = () => {
        registrationForm.resetFields();
        registrationForm.setFieldsValue({ expires_in_hours: 24, reason: "", mfa_code: "" });
        setRegistrationOpen(true);
    };
    const createRegistration = async (values: RegistrationValues) => {
        setWorking(true);
        try {
            await authClient.stepUp(values.mfa_code);
            const result = await apiRequest<CreatedRegistration>("/v1/admin/game-servers/registration-tokens", {
                method: "POST",
                body: JSON.stringify({
                    instance_id: values.instance_id,
                    expires_in_hours: values.expires_in_hours,
                    reason: values.reason
                })
            });
            setRegistrationOpen(false);
            setCreatedRegistration(result);
            message.success(tr("已生成单次使用的专服注册 Token。"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const columns: TableColumnsType<GameServer> = [
        {
            title: tr("\u4E13\u670D"),
            dataIndex: "display_name",
            render: (value: string, item) => (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.server_id}</span>
        </div>)
        },
        { title: tr("\u533A\u57DF"), dataIndex: "region", width: 110 },
        { title: tr("\u6A21\u5F0F"), dataIndex: "mode", width: 120 },
        { title: tr("\u7248\u672C"), dataIndex: "version", width: 120 },
        {
            title: tr("\u4EBA\u6570"),
            key: "players",
            width: 110,
            render: (_, item) => `${item.player_count} / ${item.max_players}`
        },
        {
            title: tr("\u72B6\u6001"),
            dataIndex: "state",
            width: 130,
            render: (value: GameServer["state"]) => (<Tag color={value === "READY" || value === "RUNNING" ? "green" : value === "OFFLINE" ? "default" : "orange"}>
          {value}
        </Tag>)
        },
        {
            title: tr("凭证"),
            key: "credential",
            width: 170,
            render: (_, item) => (<Space direction="vertical" size={0}>
          <Tag color={item.certificate_fingerprint ? "green" : "orange"}>{item.certificate_fingerprint ? tr("签名凭证") : tr("旧版 Token")}</Tag>
          <Typography.Text type="secondary">{tr("凭证代数")} {item.credential_generation ?? "—"}</Typography.Text>
          {item.certificate_expires_at && <Typography.Text type="secondary">{tr("证书到期")} {formatTime(item.certificate_expires_at)}</Typography.Text>}
        </Space>)
        },
        { title: tr("\u6700\u540E\u5FC3\u8DF3"), dataIndex: "last_heartbeat_at", width: 180, render: formatTime },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            width: 270,
            fixed: "right",
            render: (_, item) => (<Space size={2}>
          {permissions.includes("game_servers.drain") && item.state !== "DRAINING" && item.state !== "OFFLINE" && (<Button type="link" icon={<PauseCircleOutlined />} onClick={() => setTarget({ server: item, operation: "drain" })}>{tr("\u8FDB\u5165\u7EF4\u62A4")}</Button>)}
          {permissions.includes("game_servers.drain") && (item.state === "DRAINING" || item.state === "UNHEALTHY") && (<Button type="link" icon={<SyncOutlined />} onClick={() => setTarget({ server: item, operation: "resume" })}>{tr("\u6062\u590D\u63A5\u5165")}</Button>)}
          {permissions.includes("game_servers.disable") && item.state !== "OFFLINE" && (<Button danger type="link" icon={<StopOutlined />} onClick={() => setTarget({ server: item, operation: "disable" })}>{tr("\u505C\u7528")}</Button>)}
        </Space>)
        }
    ];
    const execute = async ({ reason }: {
        reason: string;
    }) => {
        if (!target) {
            return;
        }
        setWorking(true);
        try {
            await apiRequest(`/v1/admin/game-servers/${encodeURIComponent(target.server.server_id)}/${target.operation}`, { method: "POST", body: JSON.stringify({ reason }) });
            message.success(target.operation === "drain" ? tr("\u4E13\u670D\u5DF2\u8FDB\u5165\u7EF4\u62A4\u6A21\u5F0F\u3002") : target.operation === "resume" ? tr("\u4E13\u670D\u5DF2\u6062\u590D\u63A5\u5165\u3002") : tr("\u4E13\u670D\u5DF2\u505C\u7528\u5E76\u64A4\u9500\u6CE8\u518C\u51ED\u636E\u3002"));
            setTarget(null);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">ONLINE / DEDICATED SERVERS</Typography.Text>
          <Typography.Title level={2}>Dedicated Server</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u7EF4\u62A4\u6A21\u5F0F\u505C\u6B62\u63A5\u6536\u65B0\u4EFB\u52A1\uFF1B\u505C\u7528\u4F1A\u64A4\u9500\u670D\u52A1\u5668\u6CE8\u518C\u51ED\u636E\u3002\u9875\u9762\u4E0D\u63D0\u4F9B\u4EFB\u610F Shell \u547D\u4EE4\u3002")}</Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
          {permissions.includes("game_servers.register") && (<Button type="primary" icon={<PlusOutlined />} onClick={openRegistration}>{tr("添加服务器")}</Button>)}
        </Space>
      </section>
      <Card className="table-card">
        <Table<GameServer> rowKey="server_id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1200 }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709\u5DF2\u6CE8\u518C\u7684 Dedicated Server\u3002") }}/>
      </Card>
      <OperationReasonModal open={target !== null} title={operationTitle(target)} consequence={operationConsequence(target)} confirmLabel={target?.operation === "disable" ? tr("\u786E\u8BA4\u505C\u7528") : tr("\u786E\u8BA4\u6267\u884C")} danger={target?.operation === "disable"} loading={working} onCancel={() => setTarget(null)} onConfirm={execute}/>
      <Modal open={registrationOpen} title={tr("添加 Dedicated Server")} okText={tr("生成注册 Token")} cancelText={tr("取消")} confirmLoading={working} onCancel={() => !working && setRegistrationOpen(false)} onOk={() => registrationForm.submit()} destroyOnHidden>
        <Alert type="info" showIcon message={tr("Token 仅绑定一台服务器并且只能成功使用一次")} description={tr("为同一实例重新生成 Token 会立即撤销之前尚未使用的 Token。注册成功后，Token 将在同一个数据库事务中失效。")} style={{ marginBottom: 16 }}/>
        <Form form={registrationForm} layout="vertical" onFinish={createRegistration} requiredMark="optional">
          <Form.Item name="instance_id" label={tr("实例 ID")} rules={[
            { required: true, whitespace: true, message: tr("请输入服务器实例 ID。") },
            { pattern: /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/, message: tr("实例 ID 只能包含字母、数字、点、下划线、冒号和连字符。") }
        ]}>
            <Input autoComplete="off" placeholder="hk-prod-001" maxLength={128}/>
          </Form.Item>
          <Form.Item name="expires_in_hours" label={tr("有效期（小时）")} rules={[{ required: true, message: tr("请输入有效期。") }]}>
            <InputNumber min={1} max={168} precision={0} style={{ width: "100%" }}/>
          </Form.Item>
          <Form.Item name="reason" label={tr("操作原因")} rules={reasonRules()}>
            <Input.TextArea rows={3} maxLength={500} showCount placeholder={tr("例如：为工单 OPS-4812 部署香港专服")}/>
          </Form.Item>
          <Form.Item name="mfa_code" label={tr("二次 MFA 验证")} rules={[{ required: true, whitespace: true, message: tr("请输入动态验证码或恢复码。") }]}>
            <Input.Password autoComplete="one-time-code" maxLength={32}/>
          </Form.Item>
        </Form>
      </Modal>
      <Modal open={createdRegistration !== null} title={tr("专服注册 Token 已生成")} closable={false} maskClosable={false} footer={[
            <Button key="copy" icon={<CopyOutlined />} onClick={() => copyRegistrationToken(createdRegistration, message)}>{tr("复制 Token")}</Button>,
            <Button key="close" type="primary" onClick={() => setCreatedRegistration(null)}>{tr("我已安全保存")}</Button>
        ]} width={720}>
        <Alert type="warning" showIcon message={tr("此明文 Token 只显示本次")} description={tr("关闭后无法从服务器恢复。请立即保存到对应 Dedicated Server 的密钥存储中，不要写入 Git、日志或工单正文。")} style={{ marginBottom: 16 }}/>
        {createdRegistration && (<Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <div>
            <Typography.Text type="secondary">{tr("实例 ID")}</Typography.Text>
            <Typography.Paragraph copyable>{createdRegistration.instance_id}</Typography.Paragraph>
          </div>
          <div>
            <Typography.Text type="secondary">Registration Token</Typography.Text>
            <Typography.Paragraph code copyable={{ text: createdRegistration.registration_token }}>{createdRegistration.registration_token}</Typography.Paragraph>
          </div>
          <Typography.Text type="secondary">{tr(`有效期至：${formatTime(createdRegistration.expires_at)}`)}</Typography.Text>
        </Space>)}
      </Modal>
    </div>);
}

function reasonRules() {
    return [
        { required: true, whitespace: true, message: tr("请填写可供审计追溯的操作原因。") },
        { max: 500, message: tr("操作原因不能超过 500 个字符。") }
    ];
}

async function copyRegistrationToken(registration: CreatedRegistration | null, message: ReturnType<typeof App.useApp>["message"]) {
    if (!registration)
        return;
    try {
        await navigator.clipboard.writeText(registration.registration_token);
        message.success(tr("注册 Token 已复制。"));
    }
    catch {
        message.error(tr("浏览器未允许访问剪贴板，请使用 Token 旁的复制按钮。"));
    }
}
function operationTitle(target: {
    server: GameServer;
    operation: ServerOperation;
} | null) {
    if (!target)
        return tr("\u786E\u8BA4\u4E13\u670D\u64CD\u4F5C");
    if (target.operation === "drain")
        return tr(`让 ${target.server.display_name} 进入维护模式？`);
    if (target.operation === "resume")
        return tr(`恢复 ${target.server.display_name} 接收任务？`);
    return tr(`停用 ${target.server.display_name}？`);
}
function operationConsequence(target: {
    server: GameServer;
    operation: ServerOperation;
} | null) {
    if (target?.operation === "drain")
        return tr("\u4E13\u670D\u5C06\u505C\u6B62\u63A5\u6536\u65B0\u4EFB\u52A1\uFF0C\u73B0\u6709\u4F1A\u8BDD\u4E0D\u4F1A\u7531\u6B64\u6309\u94AE\u5F3A\u5236\u7EC8\u6B62\u3002");
    if (target?.operation === "resume")
        return tr("\u4E13\u670D\u5C06\u6062\u590D\u4E3A READY \u5E76\u53EF\u518D\u6B21\u63A5\u6536\u4EFB\u52A1\u3002");
    return tr("\u4E13\u670D\u5C06\u6807\u8BB0 OFFLINE\uFF0C\u6CE8\u518C Token \u88AB\u64A4\u9500\uFF1B\u6062\u590D\u9700\u8981\u91CD\u65B0\u6CE8\u518C\u3002");
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
function errorMessage(error: unknown) {
    if (error instanceof ApiError)
        return error.requestId ? tr(`${error.message}（请求编号：${error.requestId}）`) : error.message;
    return error instanceof Error ? error.message : tr("\u64CD\u4F5C\u5931\u8D25\u3002");
}
