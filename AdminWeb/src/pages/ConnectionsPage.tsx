import {
  EyeOutlined,
  ReloadOutlined,
  StopOutlined,
  SwapOutlined
} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  App,
  Button,
  Card,
  Descriptions,
  Drawer,
  Form,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Timeline,
  Typography,
  type TableColumnsType
} from "antd";
import {useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import {OperationReasonModal} from "../components/OperationReasonModal";
import type {Connection} from "../types";

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
  const {message} = App.useApp();
  const [filterForm] = Form.useForm<Filters>();
  const [filters, setFilters] = useState<Filters>({});
  const [operation, setOperation] = useState<Operation | null>(null);
  const [detail, setDetail] = useState<Connection | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [working, setWorking] = useState(false);
  const permissions = authClient.permissions();
  const {query, result} = useList<Connection>({
    resource: "connections",
    pagination: {pageSize: 100},
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
      const response = await apiRequest<Connection>(
        `/v1/admin/connections/${encodeURIComponent(item.connection_id)}`
      );
      setDetail(response);
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setDetailLoading(false);
    }
  };

  const execute = async ({reason}: {reason: string}) => {
    if (!operation) return;
    setWorking(true);
    try {
      if (operation.operation === "close") {
        const response = await apiRequest<{relay_cleanup_complete: boolean}>(
          `/v1/admin/connections/${encodeURIComponent(operation.item.connection_id)}/close`,
          {method: "POST", body: JSON.stringify({reason})}
        );
        if (response.relay_cleanup_complete) {
          message.success("Connection 已关闭，Relay Allocation 已清理。");
        } else {
          message.warning("Connection 已关闭，但 Relay 清理尚未确认，请按 Runbook 复核。");
        }
      } else {
        const response = await apiRequest<{
          previous_relay_node_id: string;
          new_relay_node_id: string;
        }>(
          `/v1/admin/connections/${encodeURIComponent(operation.item.connection_id)}/migrate-relay`,
          {method: "POST", body: JSON.stringify({reason})}
        );
        message.success(`迁移已开始：${response.previous_relay_node_id} → ${response.new_relay_node_id}`);
      }
      setOperation(null);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const columns: TableColumnsType<Connection> = [
    {
      title: "Connection",
      dataIndex: "connection_id",
      render: (value: string, item) => (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>房间 {item.room_id}</span>
        </div>
      )
    },
    {
      title: "参与者",
      key: "players",
      width: 230,
      render: (_, item) => (
        <div className="primary-cell">
          <strong>HOST {item.host_player_id}</strong>
          <span>PEER {item.peer_player_id}</span>
        </div>
      )
    },
    {
      title: "状态",
      dataIndex: "state",
      width: 160,
      render: (value: Connection["state"]) => (
        <Tag color={value === "CONNECTED" ? "green" : value === "FAILED" ? "red" : isTerminal(value) ? "default" : "blue"}>
          {value}
        </Tag>
      )
    },
    {
      title: "当前路径",
      dataIndex: "selected_path",
      width: 150,
      render: (value: string) => value || "协商中"
    },
    {
      title: "Relay",
      dataIndex: "relay_node_id",
      width: 200,
      render: (value: string, item) => value ? (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.allocation_state}</span>
        </div>
      ) : "—"
    },
    {title: "最近活动", dataIndex: "updated_at", width: 180, render: formatTime},
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 280,
      render: (_, item) => (
        <Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => showDetail(item)}>详情</Button>
          {permissions.includes("connections.migrate") &&
            item.state === "CONNECTED" &&
            ["UDP_RELAY", "TCP_TLS_RELAY"].includes(item.selected_path) && (
              <Button type="link" icon={<SwapOutlined />} onClick={() => setOperation({item, operation: "migrate"})}>
                迁移 Relay
              </Button>
            )}
          {permissions.includes("connections.close") && !isTerminal(item.state) && (
            <Button danger type="link" icon={<StopOutlined />} onClick={() => setOperation({item, operation: "close"})}>
              关闭
            </Button>
          )}
        </Space>
      )
    }
  ];

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">ONLINE / CONNECTIONS</Typography.Text>
          <Typography.Title level={2}>活动连接</Typography.Title>
          <Typography.Paragraph type="secondary">
            查看连接状态与网络路径。候选地址、Relay Token 和加密材料不会返回管理浏览器。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>刷新</Button>
      </section>

      <Card className="filter-card">
        <Form
          form={filterForm}
          layout="inline"
          onFinish={(values) => setFilters(Object.fromEntries(
            Object.entries(values).filter(([, value]) => value)
          ))}
        >
          <Form.Item name="state">
            <Select
              allowClear
              placeholder="状态"
              style={{width: 190}}
              options={connectionStates.map((value) => ({label: value, value}))}
            />
          </Form.Item>
          <Form.Item name="room_id"><Input allowClear placeholder="房间 ID" /></Form.Item>
          <Form.Item name="player_id"><Input allowClear placeholder="玩家 ID" /></Form.Item>
          <Form.Item name="relay_node_id"><Input allowClear placeholder="Relay 节点 ID" /></Form.Item>
          <Form.Item>
            <Space>
              <Button type="primary" htmlType="submit">筛选</Button>
              <Button onClick={() => { filterForm.resetFields(); setFilters({}); }}>重置</Button>
            </Space>
          </Form.Item>
        </Form>
      </Card>

      <Card className="table-card">
        <Table<Connection>
          rowKey="connection_id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1500}}
          locale={{emptyText: "没有符合条件的 Connection。"}}
        />
      </Card>

      <Drawer
        open={detail !== null}
        title={`Connection · ${detail?.connection_id ?? ""}`}
        width={760}
        loading={detailLoading}
        onClose={() => setDetail(null)}
      >
        {detail && (
          <Space direction="vertical" size="large" style={{width: "100%"}}>
            <Descriptions bordered column={2} size="small">
              <Descriptions.Item label="状态"><Tag>{detail.state}</Tag></Descriptions.Item>
              <Descriptions.Item label="路径">{detail.selected_path || "—"}</Descriptions.Item>
              <Descriptions.Item label="房间">{detail.room_id}</Descriptions.Item>
              <Descriptions.Item label="Relay">{detail.relay_node_id || "—"}</Descriptions.Item>
              <Descriptions.Item label="HOST">{detail.host_player_id}</Descriptions.Item>
              <Descriptions.Item label="PEER">{detail.peer_player_id}</Descriptions.Item>
              <Descriptions.Item label="创建时间">{formatTime(detail.created_at)}</Descriptions.Item>
              <Descriptions.Item label="最近活动">{formatTime(detail.updated_at)}</Descriptions.Item>
              <Descriptions.Item label="失败原因" span={2}>{detail.failure_reason || "—"}</Descriptions.Item>
            </Descriptions>
            <div>
              <Typography.Title level={4}>Relay 迁移记录</Typography.Title>
              <Timeline
                items={(detail.migration_history ?? []).map((migration) => ({
                  color: migration.state === "COMPLETED" ? "green" : migration.state === "FAILED" ? "red" : "blue",
                  children: (
                    <div>
                      <strong>{migration.previous_node_id} → {migration.new_node_id}</strong>
                      <div>{migration.state} · 第 {migration.attempt} 次 · {formatTime(migration.created_at)}</div>
                      {migration.failure_reason && <Typography.Text type="danger">{migration.failure_reason}</Typography.Text>}
                    </div>
                  )
                }))}
              />
              {(detail.migration_history ?? []).length === 0 && (
                <Typography.Text type="secondary">暂无迁移记录。</Typography.Text>
              )}
            </div>
          </Space>
        )}
      </Drawer>

      <OperationReasonModal
        open={operation !== null}
        title={operation?.operation === "migrate"
          ? `迁移 ${operation?.item.connection_id ?? ""} 的 Relay？`
          : `关闭 ${operation?.item.connection_id ?? ""}？`}
        consequence={operation?.operation === "migrate"
          ? "后端会从合格的 READY 节点中自动选择目标；页面不接受任意 IP 或节点地址。迁移期间连接状态会变为 MIGRATING_RELAY。"
          : "Connection 将进入 CLOSED，活动 Relay Allocation 会被撤销；此操作不能从页面恢复。"}
        confirmLabel={operation?.operation === "migrate" ? "确认开始迁移" : "确认关闭"}
        danger={operation?.operation === "close"}
        loading={working}
        onCancel={() => setOperation(null)}
        onConfirm={execute}
      />
    </div>
  );
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
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError) return error.requestId ? `${error.message}（请求编号：${error.requestId}）` : error.message;
  return error instanceof Error ? error.message : "操作失败。";
}
