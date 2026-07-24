import {PauseCircleOutlined, ReloadOutlined, StopOutlined, SyncOutlined} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {App, Button, Card, Space, Table, Tag, Typography, type TableColumnsType} from "antd";
import {useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import {OperationReasonModal} from "../components/OperationReasonModal";
import type {GameServer} from "../types";

type ServerOperation = "drain" | "resume" | "disable";

export function GameServersPage() {
  const {message} = App.useApp();
  const [target, setTarget] = useState<{server: GameServer; operation: ServerOperation} | null>(null);
  const [working, setWorking] = useState(false);
  const {query, result} = useList<GameServer>({
    resource: "game-servers",
    pagination: {pageSize: 100},
    queryOptions: {refetchInterval: 10_000}
  });
  const permissions = authClient.permissions();

  const columns: TableColumnsType<GameServer> = [
    {
      title: "专服",
      dataIndex: "display_name",
      render: (value: string, item) => (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.server_id}</span>
        </div>
      )
    },
    {title: "区域", dataIndex: "region", width: 110},
    {title: "模式", dataIndex: "mode", width: 120},
    {title: "版本", dataIndex: "version", width: 120},
    {
      title: "人数",
      key: "players",
      width: 110,
      render: (_, item) => `${item.player_count} / ${item.max_players}`
    },
    {
      title: "状态",
      dataIndex: "state",
      width: 130,
      render: (value: GameServer["state"]) => (
        <Tag color={value === "READY" || value === "RUNNING" ? "green" : value === "OFFLINE" ? "default" : "orange"}>
          {value}
        </Tag>
      )
    },
    {title: "最后心跳", dataIndex: "last_heartbeat_at", width: 180, render: formatTime},
    {
      title: "操作",
      key: "actions",
      width: 270,
      fixed: "right",
      render: (_, item) => (
        <Space size={2}>
          {permissions.includes("game_servers.drain") && item.state !== "DRAINING" && item.state !== "OFFLINE" && (
            <Button type="link" icon={<PauseCircleOutlined />} onClick={() => setTarget({server: item, operation: "drain"})}>
              进入维护
            </Button>
          )}
          {permissions.includes("game_servers.drain") && (item.state === "DRAINING" || item.state === "UNHEALTHY") && (
            <Button type="link" icon={<SyncOutlined />} onClick={() => setTarget({server: item, operation: "resume"})}>
              恢复接入
            </Button>
          )}
          {permissions.includes("game_servers.disable") && item.state !== "OFFLINE" && (
            <Button danger type="link" icon={<StopOutlined />} onClick={() => setTarget({server: item, operation: "disable"})}>
              停用
            </Button>
          )}
        </Space>
      )
    }
  ];

  const execute = async ({reason}: {reason: string}) => {
    if (!target) {
      return;
    }
    setWorking(true);
    try {
      await apiRequest(
        `/v1/admin/game-servers/${encodeURIComponent(target.server.server_id)}/${target.operation}`,
        {method: "POST", body: JSON.stringify({reason})}
      );
      message.success(target.operation === "drain" ? "专服已进入维护模式。" : target.operation === "resume" ? "专服已恢复接入。" : "专服已停用并撤销注册凭据。");
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
          <Typography.Text className="eyebrow">ONLINE / DEDICATED SERVERS</Typography.Text>
          <Typography.Title level={2}>Dedicated Server</Typography.Title>
          <Typography.Paragraph type="secondary">
            维护模式停止接收新任务；停用会撤销服务器注册凭据。页面不提供任意 Shell 命令。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>刷新</Button>
      </section>
      <Card className="table-card">
        <Table<GameServer>
          rowKey="server_id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1200}}
          locale={{emptyText: "当前没有已注册的 Dedicated Server。"}}
        />
      </Card>
      <OperationReasonModal
        open={target !== null}
        title={operationTitle(target)}
        consequence={operationConsequence(target)}
        confirmLabel={target?.operation === "disable" ? "确认停用" : "确认执行"}
        danger={target?.operation === "disable"}
        loading={working}
        onCancel={() => setTarget(null)}
        onConfirm={execute}
      />
    </div>
  );
}

function operationTitle(target: {server: GameServer; operation: ServerOperation} | null) {
  if (!target) return "确认专服操作";
  if (target.operation === "drain") return `让 ${target.server.display_name} 进入维护模式？`;
  if (target.operation === "resume") return `恢复 ${target.server.display_name} 接收任务？`;
  return `停用 ${target.server.display_name}？`;
}

function operationConsequence(target: {server: GameServer; operation: ServerOperation} | null) {
  if (target?.operation === "drain") return "专服将停止接收新任务，现有会话不会由此按钮强制终止。";
  if (target?.operation === "resume") return "专服将恢复为 READY 并可再次接收任务。";
  return "专服将标记 OFFLINE，注册 Token 被撤销；恢复需要重新注册。";
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError) return error.requestId ? `${error.message}（请求编号：${error.requestId}）` : error.message;
  return error instanceof Error ? error.message : "操作失败。";
}
