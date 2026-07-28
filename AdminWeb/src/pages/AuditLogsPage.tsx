import { localeTag, tr } from "../i18n";
import { DownloadOutlined, ReloadOutlined, SearchOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Button, Card, Descriptions, Input, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import type { AuditLog } from "../types";
function auditLogColumns(): TableColumnsType<AuditLog> {
    return [
    {
        title: tr("\u64CD\u4F5C"),
        dataIndex: "action",
        render: (value: string, item) => (<div className="primary-cell">
        <strong>{value}</strong>
        <span>{item.id}</span>
      </div>)
    },
    {
        title: tr("\u76EE\u6807"),
        key: "target",
        render: (_, item) => (<div className="primary-cell">
        <strong>{item.target_type}</strong>
        <span>{item.target_id}</span>
      </div>)
    },
    {
        title: tr("\u7BA1\u7406\u5458"),
        dataIndex: "admin_id",
        width: 170
    },
    {
        title: tr("\u7ED3\u679C"),
        dataIndex: "result",
        width: 120,
        render: (value: AuditLog["result"]) => (<Tag color={value === "SUCCEEDED" ? "green" : value === "DENIED" ? "gold" : "red"}>
        {value}
      </Tag>)
    },
    {
        title: tr("\u539F\u56E0"),
        dataIndex: "reason",
        ellipsis: true
    },
    {
        title: tr("\u65F6\u95F4"),
        dataIndex: "created_at",
        width: 180,
        render: formatTime
    }
    ];
}
export function AuditLogsPage() {
    const columns = auditLogColumns();
    const [adminID, setAdminID] = useState("");
    const [action, setAction] = useState("");
    const [targetType, setTargetType] = useState("");
    const [targetID, setTargetID] = useState("");
    const { query, result } = useList<AuditLog>({
        resource: "audit-logs",
        pagination: { pageSize: 100 },
        filters: [
            { field: "admin_id", operator: "eq", value: adminID },
            { field: "action", operator: "eq", value: action },
            { field: "target_type", operator: "eq", value: targetType },
            { field: "target_id", operator: "eq", value: targetID }
        ]
    });
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / AUDIT</Typography.Text>
          <Typography.Title level={2}>{tr("\u64CD\u4F5C\u5BA1\u8BA1")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u8BB0\u5F55\u540E\u7AEF\u5B9E\u9645\u6267\u884C\u7684\u5199\u64CD\u4F5C\u3001\u539F\u56E0\u3001\u8BF7\u6C42\u7F16\u53F7\u548C\u5B57\u6BB5\u5DEE\u5F02\u3002\u51ED\u636E\u7C7B\u5B57\u6BB5\u5728\u8FD4\u56DE\u524D\u81EA\u52A8\u8131\u654F\u3002")}</Typography.Paragraph>
        </div>
        <Space wrap>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
          <Button icon={<DownloadOutlined />} disabled={result.data.length === 0} onClick={() => exportAuditCSV(result.data)}>{tr("\u5BFC\u51FA\u5F53\u524D\u7ED3\u679C")}</Button>
        </Space>
      </section>
      <Card className="table-card">
        <div className="table-toolbar audit-filters">
          <Input allowClear prefix={<SearchOutlined />} placeholder={tr("\u7BA1\u7406\u5458 ID")} value={adminID} onChange={(event) => setAdminID(event.target.value)}/>
          <Input allowClear placeholder={tr("\u64CD\u4F5C\uFF0C\u4F8B\u5982 PLAYER_UPDATED")} value={action} onChange={(event) => setAction(event.target.value)}/>
          <Input allowClear placeholder={tr("\u8D44\u6E90\u7C7B\u578B")} value={targetType} onChange={(event) => setTargetType(event.target.value)}/>
          <Input allowClear placeholder={tr("\u76EE\u6807 ID")} value={targetID} onChange={(event) => setTargetID(event.target.value)}/>
        </div>
        <Table<AuditLog> rowKey="id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1200 }} expandable={{
            expandedRowRender: (item) => (<Space direction="vertical" size="middle" className="audit-detail">
                <Descriptions bordered size="small" column={{ xs: 1, lg: 2 }}>
                  <Descriptions.Item label={tr("\u8BF7\u6C42\u7F16\u53F7")}>
                    <Typography.Text copyable>{item.request_id || "—"}</Typography.Text>
                  </Descriptions.Item>
                  <Descriptions.Item label={tr("\u6765\u6E90\u5730\u5740")}>{item.ip_address || "—"}</Descriptions.Item>
                  <Descriptions.Item label="User-Agent" span={2}>
                    {item.user_agent || "—"}
                  </Descriptions.Item>
                </Descriptions>
                <div className="audit-diff-grid">
                  <div>
                    <Typography.Text strong>{tr("\u4FEE\u6539\u524D")}</Typography.Text>
                    <pre className="json-block">{JSON.stringify(item.old_value, null, 2)}</pre>
                  </div>
                  <div>
                    <Typography.Text strong>{tr("\u4FEE\u6539\u540E")}</Typography.Text>
                    <pre className="json-block">{JSON.stringify(item.new_value, null, 2)}</pre>
                  </div>
                </div>
              </Space>)
        }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709\u53EF\u663E\u793A\u7684\u64CD\u4F5C\u5BA1\u8BA1\u8BB0\u5F55\u3002") }}/>
      </Card>
    </div>);
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
function exportAuditCSV(items: AuditLog[]) {
    const header = [
        "id", "admin_id", "action", "target_type", "target_id", "result",
        "reason", "request_id", "ip_address", "user_agent", "created_at",
        "old_value", "new_value"
    ];
    const rows = items.map((item) => [
        item.id,
        item.admin_id,
        item.action,
        item.target_type,
        item.target_id,
        item.result,
        item.reason,
        item.request_id,
        item.ip_address,
        item.user_agent,
        item.created_at,
        JSON.stringify(item.old_value),
        JSON.stringify(item.new_value)
    ]);
    const contents = [header, ...rows]
        .map((row) => row.map(csvCell).join(","))
        .join("\r\n");
    const blob = new Blob([`\uFEFF${contents}`], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `projectrebound-admin-audit-${new Date().toISOString().slice(0, 10)}.csv`;
    anchor.click();
    URL.revokeObjectURL(url);
}
function csvCell(value: unknown) {
    let text = String(value ?? "");
    if (/^[=+\-@]/.test(text)) {
        text = `'${text}`;
    }
    return `"${text.replaceAll("\"", "\"\"")}"`;
}
