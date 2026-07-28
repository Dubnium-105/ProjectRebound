import { localeTag, tr } from "../i18n";
import { PauseCircleOutlined, ReloadOutlined, StopOutlined, SyncOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { App, Button, Card, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { GameServer } from "../types";
type ServerOperation = "drain" | "resume" | "disable";
export function GameServersPage() {
    const { message } = App.useApp();
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
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
      </section>
      <Card className="table-card">
        <Table<GameServer> rowKey="server_id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1200 }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709\u5DF2\u6CE8\u518C\u7684 Dedicated Server\u3002") }}/>
      </Card>
      <OperationReasonModal open={target !== null} title={operationTitle(target)} consequence={operationConsequence(target)} confirmLabel={target?.operation === "disable" ? tr("\u786E\u8BA4\u505C\u7528") : tr("\u786E\u8BA4\u6267\u884C")} danger={target?.operation === "disable"} loading={working} onCancel={() => setTarget(null)} onConfirm={execute}/>
    </div>);
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
