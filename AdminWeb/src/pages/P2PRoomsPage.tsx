import { localeTag, tr } from "../i18n";
import { DeleteOutlined, EyeOutlined, ReloadOutlined, StopOutlined, UserDeleteOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { App, Button, Card, Descriptions, Drawer, Progress, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { P2PRoom, P2PRoomMember } from "../types";
const roomStateColor: Record<P2PRoom["state"], string> = {
    LOBBY: "blue",
    CONNECTING: "gold",
    RUNNING: "green",
    STALE: "orange",
    CLOSED: "default"
};
export function P2PRoomsPage() {
    const { message } = App.useApp();
    const [selected, setSelected] = useState<P2PRoom | null>(null);
    const [deleteTarget, setDeleteTarget] = useState<P2PRoom | null>(null);
    const [detail, setDetail] = useState<{
        room: P2PRoom;
        members: P2PRoomMember[];
    } | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [memberTarget, setMemberTarget] = useState<P2PRoomMember | null>(null);
    const [working, setWorking] = useState(false);
    const { query, result } = useList<P2PRoom>({
        resource: "p2p-rooms",
        pagination: { pageSize: 100 }
    });
    const canClose = authClient.permissions().includes("rooms.close");
    const canDelete = authClient.permissions().includes("rooms.delete");
    const canRemoveMember = authClient.permissions().includes("rooms.remove_member");
    const columns: TableColumnsType<P2PRoom> = [
        {
            title: tr("\u623F\u95F4"),
            dataIndex: "display_name",
            render: (value: string, item) => (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.room_id}</span>
        </div>)
        },
        { title: tr("\u533A\u57DF"), dataIndex: "region", width: 110 },
        { title: tr("\u6A21\u5F0F"), dataIndex: "mode", width: 130 },
        { title: tr("\u7248\u672C"), dataIndex: "version", width: 120 },
        {
            title: tr("\u4EBA\u6570"),
            key: "capacity",
            width: 160,
            render: (_, item) => (<div className="usage-cell">
          <span>{item.player_count} / {item.max_players}</span>
          <Progress percent={Math.round((item.player_count / Math.max(1, item.max_players)) * 100)} showInfo={false} size="small"/>
        </div>)
        },
        {
            title: tr("\u72B6\u6001"),
            dataIndex: "state",
            width: 130,
            render: (value: P2PRoom["state"]) => <Tag color={roomStateColor[value]}>{value}</Tag>
        },
        {
            title: tr("\u6700\u540E\u5FC3\u8DF3"),
            dataIndex: "last_heartbeat_at",
            width: 180,
            render: formatTime
        },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            width: 280,
            fixed: "right",
            render: (_, item) => (<Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => showDetail(item)}>{tr("\u6210\u5458")}</Button>
          {canClose && item.state !== "CLOSED" && (<Button danger type="link" icon={<StopOutlined />} onClick={() => setSelected(item)}>{tr("\u5173\u95ED\u623F\u95F4")}</Button>)}
          {canDelete && item.state === "CLOSED" && (<Button danger type="link" icon={<DeleteOutlined />} onClick={() => setDeleteTarget(item)}>{tr("删除")}</Button>)}
        </Space>)
        }
    ];
    const showDetail = async (room: P2PRoom) => {
        setDetail({ room, members: [] });
        setDetailLoading(true);
        try {
            const response = await apiRequest<{
                items: P2PRoomMember[];
            }>(`/v1/admin/p2p-rooms/${encodeURIComponent(room.room_id)}/members`);
            setDetail({ room, members: response.items });
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setDetailLoading(false);
        }
    };
    const closeRoom = async ({ reason }: {
        reason: string;
    }) => {
        if (!selected) {
            return;
        }
        setWorking(true);
        try {
            const result = await apiRequest<{
                connections_cleanup_complete: boolean;
            }>(`/v1/admin/p2p-rooms/${encodeURIComponent(selected.room_id)}/close`, { method: "POST", body: JSON.stringify({ reason }) });
            if (result.connections_cleanup_complete) {
                message.success(tr("\u623F\u95F4\u53CA\u5176\u6D3B\u52A8\u8FDE\u63A5\u5DF2\u5173\u95ED\u3002"));
            }
            else {
                message.warning(tr("\u623F\u95F4\u5DF2\u5173\u95ED\uFF0C\u4F46\u8FDE\u63A5\u6E05\u7406\u5C1A\u672A\u5B8C\u5168\u786E\u8BA4\uFF0C\u8BF7\u6309 Runbook \u590D\u6838\u3002"));
            }
            setSelected(null);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const removeMember = async ({ reason }: {
        reason: string;
    }) => {
        if (!memberTarget || !detail)
            return;
        setWorking(true);
        try {
            const result = await apiRequest<{
                connections_cleanup_complete: boolean;
            }>(`/v1/admin/p2p-rooms/${encodeURIComponent(detail.room.room_id)}/members/${encodeURIComponent(memberTarget.player_id)}/remove`, { method: "POST", body: JSON.stringify({ reason }) });
            if (result.connections_cleanup_complete) {
                message.success(tr("\u6210\u5458\u5DF2\u79FB\u51FA\u623F\u95F4\uFF0C\u5173\u8054 Connection \u5DF2\u5173\u95ED\u3002"));
            }
            else {
                message.warning(tr("\u6210\u5458\u5DF2\u79FB\u51FA\uFF0C\u4F46 Connection \u6E05\u7406\u5C1A\u672A\u5B8C\u5168\u786E\u8BA4\uFF0C\u8BF7\u6309 Runbook \u590D\u6838\u3002"));
            }
            setMemberTarget(null);
            await Promise.all([showDetail(detail.room), query.refetch()]);
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const deleteRoom = async ({ reason }: {
        reason: string;
    }) => {
        if (!deleteTarget)
            return;
        setWorking(true);
        try {
            await apiRequest(`/v1/admin/p2p-rooms/${encodeURIComponent(deleteTarget.room_id)}`, {
                method: "DELETE",
                body: JSON.stringify({ reason })
            });
            if (detail?.room.room_id === deleteTarget.room_id)
                setDetail(null);
            setDeleteTarget(null);
            message.success(tr("已关闭房间已从目录移除，历史记录仍保留。"));
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
          <Typography.Text className="eyebrow">ONLINE / P2P ROOMS</Typography.Text>
          <Typography.Title level={2}>{tr("P2P \u623F\u95F4")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u67E5\u770B\u623F\u95F4\u72B6\u6001\u3001\u5BB9\u91CF\u548C\u5FC3\u8DF3\uFF1B\u5173\u95ED\u64CD\u4F5C\u4F1A\u540C\u65F6\u89E6\u53D1\u8FDE\u63A5\u4E0E Relay \u6E05\u7406\u3002")}</Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
        </Space>
      </section>
      <Card className="table-card">
        <Table<P2PRoom> rowKey="room_id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1150 }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709 P2P \u623F\u95F4\u3002") }}/>
      </Card>
      <OperationReasonModal open={selected !== null} title={tr(`关闭房间 ${selected?.display_name ?? ""}？`)} consequence={tr("\u623F\u95F4\u5C06\u8FDB\u5165 CLOSED\uFF0C\u6240\u6709\u6210\u5458\u79BB\u5F00\uFF0C\u6D3B\u52A8\u8FDE\u63A5\u548C Relay Allocation \u4F1A\u88AB\u6E05\u7406\u3002\u6B64\u64CD\u4F5C\u4E0D\u80FD\u4ECE\u9875\u9762\u64A4\u9500\u3002")} confirmLabel={tr("\u786E\u8BA4\u5173\u95ED\u623F\u95F4")} danger loading={working} onCancel={() => setSelected(null)} onConfirm={closeRoom}/>
      <OperationReasonModal open={deleteTarget !== null} title={tr(`删除房间 ${deleteTarget?.display_name ?? ""}？`)} consequence={tr("房间会从服务器和房间列表中移除；成员、比赛与审计历史不会被物理删除。")} confirmLabel={tr("确认删除")} danger loading={working} onCancel={() => setDeleteTarget(null)} onConfirm={deleteRoom}/>
      <Drawer open={detail !== null} title={tr(`房间成员 · ${detail?.room.display_name ?? ""}`)} width={820} loading={detailLoading} onClose={() => setDetail(null)}>
        {detail && (<>
            <Descriptions bordered size="small" column={2} style={{ marginBottom: 20 }}>
              <Descriptions.Item label={tr("\u623F\u95F4 ID")}>{detail.room.room_id}</Descriptions.Item>
              <Descriptions.Item label={tr("\u72B6\u6001")}><Tag>{detail.room.state}</Tag></Descriptions.Item>
              <Descriptions.Item label={tr("\u533A\u57DF")}>{detail.room.region}</Descriptions.Item>
              <Descriptions.Item label={tr("\u4EBA\u6570")}>{detail.room.player_count} / {detail.room.max_players}</Descriptions.Item>
            </Descriptions>
            <Table<P2PRoomMember> rowKey="player_id" dataSource={detail.members} pagination={false} columns={[
                {
                    title: tr("\u73A9\u5BB6"),
                    render: (_, member) => (<div className="primary-cell">
                      <strong>{member.persona_name}</strong>
                      <span>{member.player_id} · Steam {member.steam_id}</span>
                    </div>)
                },
                { title: tr("\u89D2\u8272"), dataIndex: "role", width: 100, render: (value: string) => <Tag>{value}</Tag> },
                { title: tr("\u72B6\u6001"), dataIndex: "status", width: 100, render: (value: string) => <Tag color={value === "ACTIVE" ? "green" : "default"}>{value}</Tag> },
                { title: tr("\u52A0\u5165\u65F6\u95F4"), dataIndex: "joined_at", width: 180, render: formatTime },
                {
                    title: tr("\u64CD\u4F5C"),
                    width: 110,
                    render: (_, member) => canRemoveMember && member.role !== "HOST" && member.status === "ACTIVE" ? (<Button danger type="link" icon={<UserDeleteOutlined />} onClick={() => setMemberTarget(member)}>{tr("\u8E22\u51FA")}</Button>) : "—"
                }
            ]}/>
          </>)}
      </Drawer>
      <OperationReasonModal open={memberTarget !== null} title={tr(`将 ${memberTarget?.persona_name ?? ""} 移出房间？`)} consequence={tr("\u6210\u5458\u72B6\u6001\u5C06\u53D8\u4E3A LEFT\uFF0C\u5176\u5173\u8054 Connection \u4E0E Relay Allocation \u4F1A\u88AB\u6E05\u7406\u3002\u623F\u4E3B\u4E0D\u80FD\u5355\u72EC\u8E22\u51FA\uFF1B\u5982\u9700\u5904\u7406\u623F\u4E3B\u8BF7\u5173\u95ED\u623F\u95F4\u3002")} confirmLabel={tr("\u786E\u8BA4\u8E22\u51FA\u6210\u5458")} danger loading={working} onCancel={() => setMemberTarget(null)} onConfirm={removeMember}/>
    </div>);
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
function errorMessage(error: unknown) {
    if (error instanceof ApiError) {
        return error.requestId ? tr(`${error.message}（请求编号：${error.requestId}）`) : error.message;
    }
    return error instanceof Error ? error.message : tr("\u64CD\u4F5C\u5931\u8D25\u3002");
}
