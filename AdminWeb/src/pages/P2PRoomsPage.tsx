import {EyeOutlined, ReloadOutlined, StopOutlined, UserDeleteOutlined} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  App,
  Button,
  Card,
  Descriptions,
  Drawer,
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
import type {P2PRoom, P2PRoomMember} from "../types";

const roomStateColor: Record<P2PRoom["state"], string> = {
  LOBBY: "blue",
  CONNECTING: "gold",
  RUNNING: "green",
  STALE: "orange",
  CLOSED: "default"
};

export function P2PRoomsPage() {
  const {message} = App.useApp();
  const [selected, setSelected] = useState<P2PRoom | null>(null);
  const [detail, setDetail] = useState<{room: P2PRoom; members: P2PRoomMember[]} | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [memberTarget, setMemberTarget] = useState<P2PRoomMember | null>(null);
  const [working, setWorking] = useState(false);
  const {query, result} = useList<P2PRoom>({
    resource: "p2p-rooms",
    pagination: {pageSize: 100}
  });
  const canClose = authClient.permissions().includes("rooms.close");
  const canRemoveMember = authClient.permissions().includes("rooms.remove_member");

  const columns: TableColumnsType<P2PRoom> = [
    {
      title: "房间",
      dataIndex: "display_name",
      render: (value: string, item) => (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.room_id}</span>
        </div>
      )
    },
    {title: "区域", dataIndex: "region", width: 110},
    {title: "模式", dataIndex: "mode", width: 130},
    {title: "版本", dataIndex: "version", width: 120},
    {
      title: "人数",
      key: "capacity",
      width: 160,
      render: (_, item) => (
        <div className="usage-cell">
          <span>{item.player_count} / {item.max_players}</span>
          <Progress
            percent={Math.round((item.player_count / Math.max(1, item.max_players)) * 100)}
            showInfo={false}
            size="small"
          />
        </div>
      )
    },
    {
      title: "状态",
      dataIndex: "state",
      width: 130,
      render: (value: P2PRoom["state"]) => <Tag color={roomStateColor[value]}>{value}</Tag>
    },
    {
      title: "最后心跳",
      dataIndex: "last_heartbeat_at",
      width: 180,
      render: formatTime
    },
    {
      title: "操作",
      key: "actions",
      width: 210,
      fixed: "right",
      render: (_, item) => (
        <Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => showDetail(item)}>成员</Button>
          {canClose && item.state !== "CLOSED" && (
            <Button danger type="link" icon={<StopOutlined />} onClick={() => setSelected(item)}>
              关闭房间
            </Button>
          )}
        </Space>
      )
    }
  ];

  const showDetail = async (room: P2PRoom) => {
    setDetail({room, members: []});
    setDetailLoading(true);
    try {
      const response = await apiRequest<{items: P2PRoomMember[]}>(
        `/v1/admin/p2p-rooms/${encodeURIComponent(room.room_id)}/members`
      );
      setDetail({room, members: response.items});
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setDetailLoading(false);
    }
  };

  const closeRoom = async ({reason}: {reason: string}) => {
    if (!selected) {
      return;
    }
    setWorking(true);
    try {
      const result = await apiRequest<{connections_cleanup_complete: boolean}>(
        `/v1/admin/p2p-rooms/${encodeURIComponent(selected.room_id)}/close`,
        {method: "POST", body: JSON.stringify({reason})}
      );
      if (result.connections_cleanup_complete) {
        message.success("房间及其活动连接已关闭。");
      } else {
        message.warning("房间已关闭，但连接清理尚未完全确认，请按 Runbook 复核。");
      }
      setSelected(null);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const removeMember = async ({reason}: {reason: string}) => {
    if (!memberTarget || !detail) return;
    setWorking(true);
    try {
      const result = await apiRequest<{connections_cleanup_complete: boolean}>(
        `/v1/admin/p2p-rooms/${encodeURIComponent(detail.room.room_id)}/members/${encodeURIComponent(memberTarget.player_id)}/remove`,
        {method: "POST", body: JSON.stringify({reason})}
      );
      if (result.connections_cleanup_complete) {
        message.success("成员已移出房间，关联 Connection 已关闭。");
      } else {
        message.warning("成员已移出，但 Connection 清理尚未完全确认，请按 Runbook 复核。");
      }
      setMemberTarget(null);
      await Promise.all([showDetail(detail.room), query.refetch()]);
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
          <Typography.Text className="eyebrow">ONLINE / P2P ROOMS</Typography.Text>
          <Typography.Title level={2}>P2P 房间</Typography.Title>
          <Typography.Paragraph type="secondary">
            查看房间状态、容量和心跳；关闭操作会同时触发连接与 Relay 清理。
          </Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>
            刷新
          </Button>
        </Space>
      </section>
      <Card className="table-card">
        <Table<P2PRoom>
          rowKey="room_id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1150}}
          locale={{emptyText: "当前没有 P2P 房间。"}}
        />
      </Card>
      <OperationReasonModal
        open={selected !== null}
        title={`关闭房间 ${selected?.display_name ?? ""}？`}
        consequence="房间将进入 CLOSED，所有成员离开，活动连接和 Relay Allocation 会被清理。此操作不能从页面撤销。"
        confirmLabel="确认关闭房间"
        danger
        loading={working}
        onCancel={() => setSelected(null)}
        onConfirm={closeRoom}
      />
      <Drawer
        open={detail !== null}
        title={`房间成员 · ${detail?.room.display_name ?? ""}`}
        width={820}
        loading={detailLoading}
        onClose={() => setDetail(null)}
      >
        {detail && (
          <>
            <Descriptions bordered size="small" column={2} style={{marginBottom: 20}}>
              <Descriptions.Item label="房间 ID">{detail.room.room_id}</Descriptions.Item>
              <Descriptions.Item label="状态"><Tag>{detail.room.state}</Tag></Descriptions.Item>
              <Descriptions.Item label="区域">{detail.room.region}</Descriptions.Item>
              <Descriptions.Item label="人数">{detail.room.player_count} / {detail.room.max_players}</Descriptions.Item>
            </Descriptions>
            <Table<P2PRoomMember>
              rowKey="player_id"
              dataSource={detail.members}
              pagination={false}
              columns={[
                {
                  title: "玩家",
                  render: (_, member) => (
                    <div className="primary-cell">
                      <strong>{member.persona_name}</strong>
                      <span>{member.player_id} · Steam {member.steam_id}</span>
                    </div>
                  )
                },
                {title: "角色", dataIndex: "role", width: 100, render: (value: string) => <Tag>{value}</Tag>},
                {title: "状态", dataIndex: "status", width: 100, render: (value: string) => <Tag color={value === "ACTIVE" ? "green" : "default"}>{value}</Tag>},
                {title: "加入时间", dataIndex: "joined_at", width: 180, render: formatTime},
                {
                  title: "操作",
                  width: 110,
                  render: (_, member) =>
                    canRemoveMember && member.role !== "HOST" && member.status === "ACTIVE" ? (
                      <Button danger type="link" icon={<UserDeleteOutlined />} onClick={() => setMemberTarget(member)}>
                        踢出
                      </Button>
                    ) : "—"
                }
              ]}
            />
          </>
        )}
      </Drawer>
      <OperationReasonModal
        open={memberTarget !== null}
        title={`将 ${memberTarget?.persona_name ?? ""} 移出房间？`}
        consequence="成员状态将变为 LEFT，其关联 Connection 与 Relay Allocation 会被清理。房主不能单独踢出；如需处理房主请关闭房间。"
        confirmLabel="确认踢出成员"
        danger
        loading={working}
        onCancel={() => setMemberTarget(null)}
        onConfirm={removeMember}
      />
    </div>
  );
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError) {
    return error.requestId ? `${error.message}（请求编号：${error.requestId}）` : error.message;
  }
  return error instanceof Error ? error.message : "操作失败。";
}
