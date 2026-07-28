import { localeTag, tr } from "../i18n";
import { ReloadOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { App, Button, Card, Collapse, Form, Input, Modal, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import type { RiskEvent } from "../types";
const severityColor: Record<string, string> = {
    LOW: "blue",
    MEDIUM: "gold",
    HIGH: "orange",
    CRITICAL: "red"
};
function riskEventColumns(): TableColumnsType<RiskEvent> {
    return [
    {
        title: tr("\u4E8B\u4EF6"),
        dataIndex: "event_type",
        render: (value: string, item) => (<div className="primary-cell">
        <strong>{value}</strong>
        <span>{item.id}</span>
      </div>)
    },
    {
        title: tr("\u4E25\u91CD\u5EA6"),
        dataIndex: "severity",
        width: 120,
        render: (value: string) => <Tag color={severityColor[value] ?? "default"}>{value}</Tag>
    },
    {
        title: tr("\u73A9\u5BB6"),
        key: "player",
        width: 190,
        render: (_, item) => item.player_id || item.steam_id || tr("\u672A\u5173\u8054")
    },
    {
        title: tr("IP \u6458\u8981"),
        dataIndex: "ip_address",
        width: 150,
        render: (value?: string) => value || "—"
    },
    {
        title: tr("\u65F6\u95F4"),
        dataIndex: "created_at",
        width: 180,
        render: (value: string) => formatTime(value)
    },
    {
        title: tr("\u5904\u7406\u72B6\u6001"),
        dataIndex: "resolved_at",
        width: 120,
        render: (value: string | null) => (<Tag color={value ? "green" : "red"}>{value ? tr("\u5DF2\u5904\u7406") : tr("\u5F85\u5904\u7406")}</Tag>)
    }
    ];
}
export function RiskEventsPage() {
    const { message } = App.useApp();
    const [resolutionForm] = Form.useForm<{
        reason: string;
    }>();
    const [selected, setSelected] = useState<RiskEvent | null>(null);
    const [resolving, setResolving] = useState(false);
    const { query, result } = useList<RiskEvent>({
        resource: "risk-events",
        pagination: { pageSize: 100 }
    });
    const canResolve = authClient.permissions().includes("risk_events.resolve");
    const tableColumns: TableColumnsType<RiskEvent> = [
        ...riskEventColumns(),
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            width: 110,
            fixed: "right",
            render: (_, item) => canResolve && !item.resolved_at ? (<Button type="link" onClick={() => {
                    resolutionForm.resetFields();
                    setSelected(item);
                }}>{tr("\u6807\u8BB0\u5DF2\u5904\u7406")}</Button>) : (<Typography.Text type="secondary">—</Typography.Text>)
        }
    ];
    const resolve = async ({ reason }: {
        reason: string;
    }) => {
        if (!selected) {
            return;
        }
        setResolving(true);
        try {
            await apiRequest(`/v1/admin/risk-events/${encodeURIComponent(selected.id)}/resolve`, {
                method: "POST",
                body: JSON.stringify({ reason })
            });
            message.success(tr("\u98CE\u9669\u4E8B\u4EF6\u5DF2\u6807\u8BB0\u4E3A\u5904\u7406\u5B8C\u6210\u3002"));
            setSelected(null);
            await query.refetch();
        }
        catch (error) {
            message.error(error instanceof ApiError && error.requestId
                ? tr(`${error.message}（请求编号：${error.requestId}）`) : error instanceof Error
                ? error.message
                : tr("\u5904\u7406\u5931\u8D25\u3002"));
        }
        finally {
            setResolving(false);
        }
    };
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / RISK EVENTS</Typography.Text>
          <Typography.Title level={2}>{tr("\u767B\u5F55\u98CE\u9669\u4E8B\u4EF6")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u8BBE\u5907\u54C8\u5E0C\u4E0D\u8FD4\u56DE\u524D\u7AEF\uFF0CIP \u5730\u5740\u7531\u63A7\u5236\u9762\u8131\u654F\u3002\u6280\u672F\u8BE6\u60C5\u6309\u9700\u5C55\u5F00\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
      </section>
      <Card className="table-card">
        <Table<RiskEvent> rowKey="id" columns={tableColumns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1000 }} expandable={{
            expandedRowRender: (item) => (<Collapse size="small" ghost items={[
                    {
                        key: "details",
                        label: tr("\u6280\u672F\u8BE6\u60C5"),
                        children: (<pre className="json-block">
                        {JSON.stringify(item.details, null, 2)}
                      </pre>)
                    }
                ]}/>)
        }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709\u767B\u5F55\u98CE\u9669\u4E8B\u4EF6\u3002") }}/>
      </Card>
      <Modal open={selected !== null} title={tr("\u6807\u8BB0\u98CE\u9669\u4E8B\u4EF6\u5DF2\u5904\u7406")} okText={tr("\u786E\u8BA4\u5904\u7406")} cancelText={tr("\u53D6\u6D88")} confirmLoading={resolving} onCancel={() => !resolving && setSelected(null)} onOk={() => resolutionForm.submit()} destroyOnHidden>
        <Typography.Paragraph type="secondary">{tr("\u8FD9\u4E0D\u4F1A\u81EA\u52A8\u5C01\u7981\u73A9\u5BB6\u6216\u64A4\u9500\u4F1A\u8BDD\u3002\u5904\u7406\u4EBA\u3001\u65F6\u95F4\u548C\u539F\u56E0\u4F1A\u5199\u5165\u64CD\u4F5C\u5BA1\u8BA1\u3002")}</Typography.Paragraph>
        <Form form={resolutionForm} layout="vertical" requiredMark={false} onFinish={resolve}>
          <Form.Item label={tr("\u5904\u7406\u7ED3\u8BBA\u4E0E\u539F\u56E0")} name="reason" rules={[
            { required: true, whitespace: true, message: tr("\u8BF7\u586B\u5199\u5904\u7406\u7ED3\u8BBA\u3002") },
            { max: 500, message: tr("\u5904\u7406\u7ED3\u8BBA\u4E0D\u80FD\u8D85\u8FC7 500 \u4E2A\u5B57\u7B26\u3002") }
        ]}>
            <Input.TextArea rows={4} maxLength={500} showCount/>
          </Form.Item>
        </Form>
      </Modal>
    </div>);
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
