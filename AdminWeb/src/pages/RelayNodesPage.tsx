import {PauseCircleOutlined, ReloadOutlined, StopOutlined, SyncOutlined} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  App,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Modal,
  Progress,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import {OperationReasonModal} from "../components/OperationReasonModal";
import type {RelayNode} from "../types";

type RelayOperation = "drain" | "resume" | "revoke";
type DrainForm = {reason: string; deadline_seconds: number; migrate_existing: boolean};

export function RelayNodesPage() {
  const {message} = App.useApp();
  const [drainForm] = Form.useForm<DrainForm>();
  const [target, setTarget] = useState<{node: RelayNode; operation: RelayOperation} | null>(null);
  const [working, setWorking] = useState(false);
  const {query, result} = useList<RelayNode>({
    resource: "relay-nodes",
    pagination: {pageSize: 100},
    queryOptions: {refetchInterval: 10_000}
  });
  const permissions = authClient.permissions();

  const columns: TableColumnsType<RelayNode> = [
    {
      title: "中继节点",
      dataIndex: "display_name",
      render: (value: string, item) => (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.node_id} · {item.region}/{item.zone}</span>
        </div>
      )
    },
    {title: "提供商", dataIndex: "provider", width: 120},
    {title: "版本", dataIndex: "software_version", width: 120},
    {
      title: "Allocation",
      key: "capacity",
      width: 190,
      render: (_, item) => (
        <div className="usage-cell">
          <span>{item.active_allocations} / {item.max_allocations}</span>
          <Progress percent={Math.round((item.active_allocations / Math.max(1, item.max_allocations)) * 100)} showInfo={false} size="small" />
        </div>
      )
    },
    {
      title: "状态",
      dataIndex: "state",
      width: 130,
      render: (value: RelayNode["state"]) => (
        <Tag color={value === "READY" ? "green" : value === "REVOKED" || value === "OFFLINE" ? "default" : "orange"}>{value}</Tag>
      )
    },
    {title: "证书到期", dataIndex: "certificate_expires_at", width: 180, render: formatTime},
    {title: "最后心跳", dataIndex: "last_heartbeat_at", width: 180, render: (value?: string) => value ? formatTime(value) : "—"},
    {
      title: "操作",
      key: "actions",
      width: 270,
      fixed: "right",
      render: (_, item) => (
        <Space size={2}>
          {permissions.includes("relay_nodes.drain") && item.state === "READY" && (
            <Button type="link" icon={<PauseCircleOutlined />} onClick={() => {
              drainForm.setFieldsValue({reason: "", deadline_seconds: 300, migrate_existing: true});
              setTarget({node: item, operation: "drain"});
            }}>进入维护</Button>
          )}
          {permissions.includes("relay_nodes.resume") && ["DRAINING", "UNHEALTHY", "OFFLINE"].includes(item.state) && (
            <Button type="link" icon={<SyncOutlined />} onClick={() => setTarget({node: item, operation: "resume"})}>恢复接入</Button>
          )}
          {permissions.includes("relay_nodes.revoke") && item.state !== "REVOKED" && (
            <Button danger type="link" icon={<StopOutlined />} onClick={() => setTarget({node: item, operation: "revoke"})}>撤销节点</Button>
          )}
        </Space>
      )
    }
  ];

  const executeSimple = async ({reason}: {reason: string}) => {
    if (!target || target.operation === "drain") return;
    await execute(target.operation, {reason});
  };

  const executeDrain = async (values: DrainForm) => {
    await execute("drain", values);
  };

  const execute = async (operation: RelayOperation, body: Record<string, unknown>) => {
    if (!target) return;
    setWorking(true);
    try {
      await apiRequest(`/v1/admin/relay-nodes/${encodeURIComponent(target.node.node_id)}/${operation}`, {
        method: "POST",
        body: JSON.stringify(body)
      });
      message.success(operation === "drain" ? "中继节点已进入维护模式。" : operation === "resume" ? "中继节点已恢复接收连接。" : "中继节点身份已撤销。");
      setTarget(null);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">ONLINE / RELAY FLEET</Typography.Text>
          <Typography.Title level={2}>中继节点</Typography.Title>
          <Typography.Paragraph type="secondary">
            查看 Fleet 容量、心跳与证书状态。节点 Token、证书私钥和 Allocation Token 不返回浏览器。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>刷新</Button>
      </section>
      <Card className="table-card">
        <Table<RelayNode>
          rowKey="node_id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1400}}
          locale={{emptyText: "当前没有已注册的中继节点。"}}
        />
      </Card>

      <Modal
        open={target?.operation === "drain"}
        title={`让 ${target?.node.display_name ?? ""} 进入维护模式？`}
        okText="确认进入维护"
        cancelText="取消"
        confirmLoading={working}
        onCancel={() => !working && setTarget(null)}
        onOk={() => drainForm.submit()}
        destroyOnHidden
      >
        <Typography.Paragraph type="secondary">
          节点将停止新分配；可选择迁移现有连接，并设置维护期限。
        </Typography.Paragraph>
        <Form form={drainForm} layout="vertical" requiredMark={false} onFinish={executeDrain}>
          <Form.Item label="维护期限（秒）" name="deadline_seconds" rules={[{required: true}]}>
            <InputNumber min={30} max={86400} style={{width: "100%"}} />
          </Form.Item>
          <Form.Item name="migrate_existing" valuePropName="checked">
            <Checkbox>迁移现有连接到其他 READY 节点</Checkbox>
          </Form.Item>
          <Form.Item label="操作原因" name="reason" rules={[{required: true, whitespace: true}, {max: 500}]}>
            <Input.TextArea rows={4} maxLength={500} showCount />
          </Form.Item>
        </Form>
      </Modal>

      <OperationReasonModal
        open={target?.operation === "resume" || target?.operation === "revoke"}
        title={target?.operation === "revoke" ? `撤销节点 ${target?.node.display_name ?? ""}？` : `恢复节点 ${target?.node.display_name ?? ""}？`}
        consequence={target?.operation === "revoke"
          ? "节点将停止新分配、断开控制通道，并迁移或中断现有连接；重新恢复必须重新注册。"
          : "节点将恢复为 READY 并重新接收连接。"}
        confirmLabel={target?.operation === "revoke" ? "确认撤销节点" : "确认恢复"}
        danger={target?.operation === "revoke"}
        requireMFA={target?.operation === "revoke"}
        loading={working}
        onCancel={() => setTarget(null)}
        onConfirm={executeSimple}
      />
    </div>
  );
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError) return error.requestId ? `${error.message}（请求编号：${error.requestId}）` : error.message;
  return error instanceof Error ? error.message : "操作失败。";
}
