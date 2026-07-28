import { localeTag, tr } from "../i18n";
import { PauseCircleOutlined, ReloadOutlined, StopOutlined, SyncOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { App, Button, Card, Checkbox, Form, Input, InputNumber, Modal, Progress, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { RelayNode } from "../types";
type RelayOperation = "drain" | "resume" | "revoke";
type DrainForm = {
    reason: string;
    deadline_seconds: number;
    migrate_existing: boolean;
};
export function RelayNodesPage() {
    const { message } = App.useApp();
    const [drainForm] = Form.useForm<DrainForm>();
    const [target, setTarget] = useState<{
        node: RelayNode;
        operation: RelayOperation;
    } | null>(null);
    const [working, setWorking] = useState(false);
    const { query, result } = useList<RelayNode>({
        resource: "relay-nodes",
        pagination: { pageSize: 100 },
        queryOptions: { refetchInterval: 10000 }
    });
    const permissions = authClient.permissions();
    const columns: TableColumnsType<RelayNode> = [
        {
            title: tr("\u4E2D\u7EE7\u8282\u70B9"),
            dataIndex: "display_name",
            render: (value: string, item) => (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.node_id} · {item.region}/{item.zone}</span>
        </div>)
        },
        { title: tr("\u63D0\u4F9B\u5546"), dataIndex: "provider", width: 120 },
        { title: tr("\u7248\u672C"), dataIndex: "software_version", width: 120 },
        {
            title: "Allocation",
            key: "capacity",
            width: 190,
            render: (_, item) => (<div className="usage-cell">
          <span>{item.active_allocations} / {item.max_allocations}</span>
          <Progress percent={Math.round((item.active_allocations / Math.max(1, item.max_allocations)) * 100)} showInfo={false} size="small"/>
        </div>)
        },
        {
            title: tr("\u72B6\u6001"),
            dataIndex: "state",
            width: 130,
            render: (value: RelayNode["state"]) => (<Tag color={value === "READY" ? "green" : value === "REVOKED" || value === "OFFLINE" ? "default" : "orange"}>{value}</Tag>)
        },
        { title: tr("\u8BC1\u4E66\u5230\u671F"), dataIndex: "certificate_expires_at", width: 180, render: formatTime },
        { title: tr("\u6700\u540E\u5FC3\u8DF3"), dataIndex: "last_heartbeat_at", width: 180, render: (value?: string) => value ? formatTime(value) : "—" },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            width: 270,
            fixed: "right",
            render: (_, item) => (<Space size={2}>
          {permissions.includes("relay_nodes.drain") && item.state === "READY" && (<Button type="link" icon={<PauseCircleOutlined />} onClick={() => {
                        drainForm.setFieldsValue({ reason: "", deadline_seconds: 300, migrate_existing: true });
                        setTarget({ node: item, operation: "drain" });
                    }}>{tr("\u8FDB\u5165\u7EF4\u62A4")}</Button>)}
          {permissions.includes("relay_nodes.resume") && ["DRAINING", "UNHEALTHY", "OFFLINE"].includes(item.state) && (<Button type="link" icon={<SyncOutlined />} onClick={() => setTarget({ node: item, operation: "resume" })}>{tr("\u6062\u590D\u63A5\u5165")}</Button>)}
          {permissions.includes("relay_nodes.revoke") && item.state !== "REVOKED" && (<Button danger type="link" icon={<StopOutlined />} onClick={() => setTarget({ node: item, operation: "revoke" })}>{tr("\u64A4\u9500\u8282\u70B9")}</Button>)}
        </Space>)
        }
    ];
    const executeSimple = async ({ reason }: {
        reason: string;
    }) => {
        if (!target || target.operation === "drain")
            return;
        await execute(target.operation, { reason });
    };
    const executeDrain = async (values: DrainForm) => {
        await execute("drain", values);
    };
    const execute = async (operation: RelayOperation, body: Record<string, unknown>) => {
        if (!target)
            return;
        setWorking(true);
        try {
            await apiRequest(`/v1/admin/relay-nodes/${encodeURIComponent(target.node.node_id)}/${operation}`, {
                method: "POST",
                body: JSON.stringify(body)
            });
            message.success(operation === "drain" ? tr("\u4E2D\u7EE7\u8282\u70B9\u5DF2\u8FDB\u5165\u7EF4\u62A4\u6A21\u5F0F\u3002") : operation === "resume" ? tr("\u4E2D\u7EE7\u8282\u70B9\u5DF2\u6062\u590D\u63A5\u6536\u8FDE\u63A5\u3002") : tr("\u4E2D\u7EE7\u8282\u70B9\u8EAB\u4EFD\u5DF2\u64A4\u9500\u3002"));
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
          <Typography.Text className="eyebrow">ONLINE / RELAY FLEET</Typography.Text>
          <Typography.Title level={2}>{tr("\u4E2D\u7EE7\u8282\u70B9")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u67E5\u770B Fleet \u5BB9\u91CF\u3001\u5FC3\u8DF3\u4E0E\u8BC1\u4E66\u72B6\u6001\u3002\u8282\u70B9 Token\u3001\u8BC1\u4E66\u79C1\u94A5\u548C Allocation Token \u4E0D\u8FD4\u56DE\u6D4F\u89C8\u5668\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
      </section>
      <Card className="table-card">
        <Table<RelayNode> rowKey="node_id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1400 }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709\u5DF2\u6CE8\u518C\u7684\u4E2D\u7EE7\u8282\u70B9\u3002") }}/>
      </Card>

      <Modal open={target?.operation === "drain"} title={tr(`让 ${target?.node.display_name ?? ""} 进入维护模式？`)} okText={tr("\u786E\u8BA4\u8FDB\u5165\u7EF4\u62A4")} cancelText={tr("\u53D6\u6D88")} confirmLoading={working} onCancel={() => !working && setTarget(null)} onOk={() => drainForm.submit()} destroyOnHidden>
        <Typography.Paragraph type="secondary">{tr("\u8282\u70B9\u5C06\u505C\u6B62\u65B0\u5206\u914D\uFF1B\u53EF\u9009\u62E9\u8FC1\u79FB\u73B0\u6709\u8FDE\u63A5\uFF0C\u5E76\u8BBE\u7F6E\u7EF4\u62A4\u671F\u9650\u3002")}</Typography.Paragraph>
        <Form form={drainForm} layout="vertical" requiredMark={false} onFinish={executeDrain}>
          <Form.Item label={tr("\u7EF4\u62A4\u671F\u9650\uFF08\u79D2\uFF09")} name="deadline_seconds" rules={[{ required: true }]}>
            <InputNumber min={30} max={86400} style={{ width: "100%" }}/>
          </Form.Item>
          <Form.Item name="migrate_existing" valuePropName="checked">
            <Checkbox>{tr("\u8FC1\u79FB\u73B0\u6709\u8FDE\u63A5\u5230\u5176\u4ED6 READY \u8282\u70B9")}</Checkbox>
          </Form.Item>
          <Form.Item label={tr("\u64CD\u4F5C\u539F\u56E0")} name="reason" rules={[{ required: true, whitespace: true }, { max: 500 }]}>
            <Input.TextArea rows={4} maxLength={500} showCount/>
          </Form.Item>
        </Form>
      </Modal>

      <OperationReasonModal open={target?.operation === "resume" || target?.operation === "revoke"} title={target?.operation === "revoke" ? tr(`撤销节点 ${target?.node.display_name ?? ""}？`) : tr(`恢复节点 ${target?.node.display_name ?? ""}？`)} consequence={target?.operation === "revoke"
            ? tr("\u8282\u70B9\u5C06\u505C\u6B62\u65B0\u5206\u914D\u3001\u65AD\u5F00\u63A7\u5236\u901A\u9053\uFF0C\u5E76\u8FC1\u79FB\u6216\u4E2D\u65AD\u73B0\u6709\u8FDE\u63A5\uFF1B\u91CD\u65B0\u6062\u590D\u5FC5\u987B\u91CD\u65B0\u6CE8\u518C\u3002") : tr("\u8282\u70B9\u5C06\u6062\u590D\u4E3A READY \u5E76\u91CD\u65B0\u63A5\u6536\u8FDE\u63A5\u3002")} confirmLabel={target?.operation === "revoke" ? tr("\u786E\u8BA4\u64A4\u9500\u8282\u70B9") : tr("\u786E\u8BA4\u6062\u590D")} danger={target?.operation === "revoke"} requireMFA={target?.operation === "revoke"} loading={working} onCancel={() => setTarget(null)} onConfirm={executeSimple}/>
    </div>);
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
