import { localeTag, tr } from "../i18n";
import { EyeOutlined, ReloadOutlined, StopOutlined, SwapOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { App, Button, Card, Descriptions, Drawer, Form, Input, Select, Space, Table, Tag, Timeline, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { Connection } from "../types";
type Operation = {
    item: Connection;
    operation: "close" | "migrate";
};
type Filters = {
    state?: string;
    room_id?: string;
    player_id?: string;
    relay_node_id?: string;
};
export function ConnectionsPage() {
    const { message } = App.useApp();
    const [filterForm] = Form.useForm<Filters>();
    const [filters, setFilters] = useState<Filters>({});
    const [operation, setOperation] = useState<Operation | null>(null);
    const [detail, setDetail] = useState<Connection | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [working, setWorking] = useState(false);
    const permissions = authClient.permissions();
    const { query, result } = useList<Connection>({
        resource: "connections",
        pagination: { pageSize: 100 },
        filters: Object.entries(filters).map(([field, value]) => ({
            field,
            operator: "eq" as const,
            value
        }))
    });
    const showDetail = async (item: Connection) => {
        setDetail(item);
        setDetailLoading(true);
        try {
            const response = await apiRequest<Connection>(`/v1/admin/connections/${encodeURIComponent(item.connection_id)}`);
            setDetail(response);
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setDetailLoading(false);
        }
    };
    const execute = async ({ reason }: {
        reason: string;
    }) => {
        if (!operation)
            return;
        setWorking(true);
        try {
            if (operation.operation === "close") {
                const response = await apiRequest<{
                    relay_cleanup_complete: boolean;
                }>(`/v1/admin/connections/${encodeURIComponent(operation.item.connection_id)}/close`, { method: "POST", body: JSON.stringify({ reason }) });
                if (response.relay_cleanup_complete) {
                    message.success(tr("Connection \u5DF2\u5173\u95ED\uFF0CRelay Allocation \u5DF2\u6E05\u7406\u3002"));
                }
                else {
                    message.warning(tr("Connection \u5DF2\u5173\u95ED\uFF0C\u4F46 Relay \u6E05\u7406\u5C1A\u672A\u786E\u8BA4\uFF0C\u8BF7\u6309 Runbook \u590D\u6838\u3002"));
                }
            }
            else {
                const response = await apiRequest<{
                    previous_relay_node_id: string;
                    new_relay_node_id: string;
                }>(`/v1/admin/connections/${encodeURIComponent(operation.item.connection_id)}/migrate-relay`, { method: "POST", body: JSON.stringify({ reason }) });
                message.success(tr(`迁移已开始：${response.previous_relay_node_id} → ${response.new_relay_node_id}`));
            }
            setOperation(null);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const columns: TableColumnsType<Connection> = [
        {
            title: "Connection",
            dataIndex: "connection_id",
            render: (value: string, item) => (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{tr("\u623F\u95F4")}{item.room_id}</span>
        </div>)
        },
        {
            title: tr("\u53C2\u4E0E\u8005"),
            key: "players",
            width: 230,
            render: (_, item) => (<div className="primary-cell">
          <strong>HOST {item.host_player_id}</strong>
          <span>PEER {item.peer_player_id}</span>
        </div>)
        },
        {
            title: tr("\u72B6\u6001"),
            dataIndex: "state",
            width: 160,
            render: (value: Connection["state"]) => (<Tag color={value === "CONNECTED" ? "green" : value === "FAILED" ? "red" : isTerminal(value) ? "default" : "blue"}>
          {value}
        </Tag>)
        },
        {
            title: tr("\u5F53\u524D\u8DEF\u5F84"),
            dataIndex: "selected_path",
            width: 150,
            render: (value: string) => value || tr("\u534F\u5546\u4E2D")
        },
        {
            title: "Relay",
            dataIndex: "relay_node_id",
            width: 200,
            render: (value: string, item) => value ? (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.allocation_state}</span>
        </div>) : "—"
        },
        { title: tr("\u6700\u8FD1\u6D3B\u52A8"), dataIndex: "updated_at", width: 180, render: formatTime },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            fixed: "right",
            width: 280,
            render: (_, item) => (<Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => showDetail(item)}>{tr("\u8BE6\u60C5")}</Button>
          {permissions.includes("connections.migrate") &&
                    item.state === "CONNECTED" &&
                    ["UDP_RELAY", "TCP_TLS_RELAY"].includes(item.selected_path) && (<Button type="link" icon={<SwapOutlined />} onClick={() => setOperation({ item, operation: "migrate" })}>{tr("\u8FC1\u79FB Relay")}</Button>)}
          {permissions.includes("connections.close") && !isTerminal(item.state) && (<Button danger type="link" icon={<StopOutlined />} onClick={() => setOperation({ item, operation: "close" })}>{tr("\u5173\u95ED")}</Button>)}
        </Space>)
        }
    ];
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">ONLINE / CONNECTIONS</Typography.Text>
          <Typography.Title level={2}>{tr("\u6D3B\u52A8\u8FDE\u63A5")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u67E5\u770B\u8FDE\u63A5\u72B6\u6001\u4E0E\u7F51\u7EDC\u8DEF\u5F84\u3002\u5019\u9009\u5730\u5740\u3001Relay Token \u548C\u52A0\u5BC6\u6750\u6599\u4E0D\u4F1A\u8FD4\u56DE\u7BA1\u7406\u6D4F\u89C8\u5668\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
      </section>

      <Card className="filter-card">
        <Form form={filterForm} layout="inline" onFinish={(values) => setFilters(Object.fromEntries(Object.entries(values).filter(([, value]) => value)))}>
          <Form.Item name="state">
            <Select allowClear placeholder={tr("\u72B6\u6001")} style={{ width: 190 }} options={connectionStates.map((value) => ({ label: value, value }))}/>
          </Form.Item>
          <Form.Item name="room_id"><Input allowClear placeholder={tr("\u623F\u95F4 ID")}/></Form.Item>
          <Form.Item name="player_id"><Input allowClear placeholder={tr("\u73A9\u5BB6 ID")}/></Form.Item>
          <Form.Item name="relay_node_id"><Input allowClear placeholder={tr("Relay \u8282\u70B9 ID")}/></Form.Item>
          <Form.Item>
            <Space>
              <Button type="primary" htmlType="submit">{tr("\u7B5B\u9009")}</Button>
              <Button onClick={() => { filterForm.resetFields(); setFilters({}); }}>{tr("\u91CD\u7F6E")}</Button>
            </Space>
          </Form.Item>
        </Form>
      </Card>

      <Card className="table-card">
        <Table<Connection> rowKey="connection_id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1500 }} locale={{ emptyText: tr("\u6CA1\u6709\u7B26\u5408\u6761\u4EF6\u7684 Connection\u3002") }}/>
      </Card>

      <Drawer open={detail !== null} title={`Connection · ${detail?.connection_id ?? ""}`} width={760} loading={detailLoading} onClose={() => setDetail(null)}>
        {detail && (<Space direction="vertical" size="large" style={{ width: "100%" }}>
            <Descriptions bordered column={2} size="small">
              <Descriptions.Item label={tr("\u72B6\u6001")}><Tag>{detail.state}</Tag></Descriptions.Item>
              <Descriptions.Item label={tr("\u8DEF\u5F84")}>{detail.selected_path || "—"}</Descriptions.Item>
              <Descriptions.Item label={tr("\u623F\u95F4")}>{detail.room_id}</Descriptions.Item>
              <Descriptions.Item label="Relay">{detail.relay_node_id || "—"}</Descriptions.Item>
              <Descriptions.Item label="HOST">{detail.host_player_id}</Descriptions.Item>
              <Descriptions.Item label="PEER">{detail.peer_player_id}</Descriptions.Item>
              <Descriptions.Item label={tr("\u521B\u5EFA\u65F6\u95F4")}>{formatTime(detail.created_at)}</Descriptions.Item>
              <Descriptions.Item label={tr("\u6700\u8FD1\u6D3B\u52A8")}>{formatTime(detail.updated_at)}</Descriptions.Item>
              <Descriptions.Item label={tr("\u5931\u8D25\u539F\u56E0")} span={2}>{detail.failure_reason || "—"}</Descriptions.Item>
            </Descriptions>
            <div>
              <Typography.Title level={4}>{tr("Relay \u8FC1\u79FB\u8BB0\u5F55")}</Typography.Title>
              <Timeline items={(detail.migration_history ?? []).map((migration) => ({
                color: migration.state === "COMPLETED" ? "green" : migration.state === "FAILED" ? "red" : "blue",
                children: (<div>
                      <strong>{migration.previous_node_id} → {migration.new_node_id}</strong>
                      <div>{migration.state}{tr("\u00B7 \u7B2C")}{migration.attempt}{tr("\u6B21 \u00B7")}{formatTime(migration.created_at)}</div>
                      {migration.failure_reason && <Typography.Text type="danger">{migration.failure_reason}</Typography.Text>}
                    </div>)
            }))}/>
              {(detail.migration_history ?? []).length === 0 && (<Typography.Text type="secondary">{tr("\u6682\u65E0\u8FC1\u79FB\u8BB0\u5F55\u3002")}</Typography.Text>)}
            </div>
          </Space>)}
      </Drawer>

      <OperationReasonModal open={operation !== null} title={operation?.operation === "migrate"
            ? tr(`迁移 ${operation?.item.connection_id ?? ""} 的 Relay？`) : tr(`关闭 ${operation?.item.connection_id ?? ""}？`)} consequence={operation?.operation === "migrate"
            ? tr("\u540E\u7AEF\u4F1A\u4ECE\u5408\u683C\u7684 READY \u8282\u70B9\u4E2D\u81EA\u52A8\u9009\u62E9\u76EE\u6807\uFF1B\u9875\u9762\u4E0D\u63A5\u53D7\u4EFB\u610F IP \u6216\u8282\u70B9\u5730\u5740\u3002\u8FC1\u79FB\u671F\u95F4\u8FDE\u63A5\u72B6\u6001\u4F1A\u53D8\u4E3A MIGRATING_RELAY\u3002") : tr("Connection \u5C06\u8FDB\u5165 CLOSED\uFF0C\u6D3B\u52A8 Relay Allocation \u4F1A\u88AB\u64A4\u9500\uFF1B\u6B64\u64CD\u4F5C\u4E0D\u80FD\u4ECE\u9875\u9762\u6062\u590D\u3002")} confirmLabel={operation?.operation === "migrate" ? tr("\u786E\u8BA4\u5F00\u59CB\u8FC1\u79FB") : tr("\u786E\u8BA4\u5173\u95ED")} danger={operation?.operation === "close"} loading={working} onCancel={() => setOperation(null)} onConfirm={execute}/>
    </div>);
}
const connectionStates = [
    "CREATED",
    "GATHERING_CANDIDATES",
    "CHECKING_DIRECT",
    "ALLOCATING_RELAY",
    "RELAY_BINDING",
    "MIGRATING_RELAY",
    "CONNECTED",
    "FAILED",
    "EXPIRED",
    "CLOSED"
];
function isTerminal(state: Connection["state"]) {
    return state === "FAILED" || state === "EXPIRED" || state === "CLOSED";
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
