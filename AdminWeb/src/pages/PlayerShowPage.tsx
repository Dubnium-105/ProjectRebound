import {
  ArrowLeftOutlined,
  CrownOutlined,
  DisconnectOutlined,
  SafetyOutlined,
  StopOutlined
} from "@ant-design/icons";
import {useOne} from "@refinedev/core";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Descriptions,
  Form,
  Input,
  Modal,
  Skeleton,
  Space,
  Table,
  Tag,
  Tabs,
  Typography
} from "antd";
import {useEffect, useState} from "react";
import {useNavigate, useParams} from "react-router";
import {ApiError, apiRequest, authClient} from "../api/client";
import type {Player, PlayerLoginEvent, PlayerSession, RiskEvent} from "../types";

type PlayerAction = "activate" | "ban" | "vip" | "revoke";

type ActionForm = {
  reason: string;
  internal_note?: string;
  revoke_sessions?: boolean;
};

export function PlayerShowPage() {
  const {id = ""} = useParams();
  const navigate = useNavigate();
  const {message} = App.useApp();
  const [actionForm] = Form.useForm<ActionForm>();
  const [working, setWorking] = useState(false);
  const [action, setAction] = useState<PlayerAction | null>(null);
  const [activityLoading, setActivityLoading] = useState(true);
  const [sessions, setSessions] = useState<PlayerSession[]>([]);
  const [riskEvents, setRiskEvents] = useState<RiskEvent[]>([]);
  const [loginEvents, setLoginEvents] = useState<PlayerLoginEvent[]>([]);
  const {query, result: player} = useOne<Player>({
    resource: "players",
    id
  });
  const permissions = authClient.permissions();

  useEffect(() => {
    if (!id) {
      return;
    }
    let active = true;
    setActivityLoading(true);
    Promise.all([
      apiRequest<{items: PlayerSession[]}>(`/v1/admin/players/${encodeURIComponent(id)}/sessions`),
      apiRequest<{items: RiskEvent[]}>(`/v1/admin/players/${encodeURIComponent(id)}/risk-events`),
      apiRequest<{items: PlayerLoginEvent[]}>(`/v1/admin/players/${encodeURIComponent(id)}/login-events`)
    ])
      .then(([sessionResult, riskResult, loginResult]) => {
        if (active) {
          setSessions(sessionResult.items);
          setRiskEvents(riskResult.items);
          setLoginEvents(loginResult.items);
        }
      })
      .catch((error) => {
        if (active) {
          message.error(apiMessage(error));
        }
      })
      .finally(() => {
        if (active) {
          setActivityLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, [id, message]);

  const patch = async (
    values: Partial<Pick<Player, "account_status" | "is_vip">> & {
      revoke_sessions?: boolean;
      reason: string;
      internal_note?: string;
    },
    success: string
  ) => {
    setWorking(true);
    try {
      await apiRequest(`/v1/admin/players/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: JSON.stringify(values)
      });
      message.success(success);
      await query.refetch();
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const openAction = (nextAction: PlayerAction) => {
    actionForm.resetFields();
    if (nextAction === "ban") {
      actionForm.setFieldValue("revoke_sessions", true);
    }
    setAction(nextAction);
  };

  const submitAction = async (values: ActionForm) => {
    if (!action || !player) {
      return;
    }
    if (action === "revoke") {
      setWorking(true);
      try {
        await apiRequest(
          `/v1/admin/players/${encodeURIComponent(id)}/revoke-sessions`,
          {
            method: "POST",
            body: JSON.stringify({reason: values.reason})
          }
        );
        message.success("玩家会话已撤销。");
        setAction(null);
      } catch (error) {
        message.error(apiMessage(error));
      } finally {
        setWorking(false);
      }
      return;
    }

    const common = {
      reason: values.reason,
      internal_note: values.internal_note
    };
    if (action === "ban") {
      await patch(
        {
          ...common,
          account_status: "BANNED",
          revoke_sessions: values.revoke_sessions
        },
        values.revoke_sessions ? "玩家账号已封禁并撤销全部会话。" : "玩家账号已封禁。"
      );
    } else if (action === "activate") {
      await patch({...common, account_status: "ACTIVE"}, "玩家账号已恢复。");
    } else {
      await patch(
        {...common, is_vip: !player.is_vip},
        player.is_vip ? "VIP 已取消。" : "VIP 已启用。"
      );
    }
    setAction(null);
  };

  if (query.isLoading) {
    return <Card><Skeleton active /></Card>;
  }
  if (!player || query.isError) {
    return (
      <Alert
        type="error"
        showIcon
        message="无法读取玩家详情"
        description="该玩家可能不存在，或当前管理员没有查看权限。"
        action={<Button onClick={() => navigate("/players")}>返回列表</Button>}
      />
    );
  }

  return (
    <div className="page-stack">
      <Button
        type="text"
        icon={<ArrowLeftOutlined />}
        onClick={() => navigate("/players")}
        className="back-button"
      >
        返回玩家列表
      </Button>
      <section className="player-profile-header">
        <div className="player-avatar">{(player.persona_name || "P").slice(0, 1).toUpperCase()}</div>
        <div>
          <Space wrap>
            <Typography.Title level={2}>{player.persona_name || "未命名玩家"}</Typography.Title>
            <Tag color={player.account_status === "ACTIVE" ? "green" : "red"}>
              {player.account_status}
            </Tag>
            {player.is_vip && <Tag color="gold">VIP</Tag>}
          </Space>
          <Typography.Text type="secondary" copyable>
            {player.player_id}
          </Typography.Text>
        </div>
      </section>

      <Card title="账号资料" className="dashboard-card">
        <Descriptions column={{xs: 1, md: 2, xl: 3}} bordered>
          <Descriptions.Item label="SteamID">{player.steam_id}</Descriptions.Item>
          <Descriptions.Item label="认证提供方">{player.auth_provider}</Descriptions.Item>
          <Descriptions.Item label="认证等级">{player.auth_level}</Descriptions.Item>
          <Descriptions.Item label="注册时间">{formatTime(player.created_at)}</Descriptions.Item>
          <Descriptions.Item label="最后登录">{formatTime(player.last_login_at)}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatTime(player.updated_at)}</Descriptions.Item>
        </Descriptions>
      </Card>

      <Card title="管理操作" className="dashboard-card">
        <Typography.Paragraph type="secondary">
          账号状态与会话操作会由控制面写入审计日志。前端权限仅用于改善体验，后端仍会再次校验。
        </Typography.Paragraph>
        <Space wrap>
          {permissions.includes("players.update_status") && (
            player.account_status === "BANNED" ? (
              <Button
                icon={<SafetyOutlined />}
                loading={working}
                onClick={() => openAction("activate")}
              >
                恢复为正常
              </Button>
            ) : (
              <Button
                danger
                icon={<StopOutlined />}
                loading={working}
                onClick={() => openAction("ban")}
              >
                封禁玩家
              </Button>
            )
          )}
          {permissions.includes("players.update_vip") && (
            <Button
              icon={<CrownOutlined />}
              loading={working}
              onClick={() => openAction("vip")}
            >
              {player.is_vip ? "取消 VIP" : "设为 VIP"}
            </Button>
          )}
          {permissions.includes("players.revoke_sessions") && (
            <Button
              danger
              icon={<DisconnectOutlined />}
              loading={working}
              onClick={() => openAction("revoke")}
            >
              撤销全部会话
            </Button>
          )}
        </Space>
      </Card>

      <Card title="认证活动与风险" className="dashboard-card">
        <Tabs
          items={[
            {
              key: "sessions",
              label: `Session (${sessions.length})`,
              children: (
                <Table<PlayerSession>
                  rowKey="session_id"
                  size="small"
                  loading={activityLoading}
                  pagination={false}
                  dataSource={sessions}
                  scroll={{x: 900}}
                  columns={[
                    {
                      title: "状态",
                      dataIndex: "active",
                      render: (value: boolean) => (
                        <Tag color={value ? "green" : "default"}>{value ? "有效" : "已失效"}</Tag>
                      )
                    },
                    {title: "设备摘要", dataIndex: "device_id_suffix", render: (value: string) => value ? `…${value}` : "—"},
                    {title: "IP 摘要", dataIndex: "ip_address", render: (value: string) => value || "—"},
                    {title: "创建时间", dataIndex: "created_at", render: formatTime},
                    {title: "最近活动", dataIndex: "last_used_at", render: (value: string | null) => value ? formatTime(value) : "—"},
                    {title: "撤销原因", dataIndex: "revoked_reason", render: (value: string) => value || "—"}
                  ]}
                  locale={{emptyText: "该玩家没有可显示的 Session。"}}
                />
              )
            },
            {
              key: "logins",
              label: `登录历史 (${loginEvents.length})`,
              children: (
                <Table<PlayerLoginEvent>
                  rowKey="id"
                  size="small"
                  loading={activityLoading}
                  pagination={false}
                  dataSource={loginEvents}
                  scroll={{x: 900}}
                  columns={[
                    {
                      title: "结果",
                      dataIndex: "result",
                      render: (value: PlayerLoginEvent["result"]) => (
                        <Tag color={value === "SUCCESS" ? "green" : "red"}>{value}</Tag>
                      )
                    },
                    {title: "失败原因", dataIndex: "failure_code", render: (value: string) => value || "—"},
                    {title: "IP 摘要", dataIndex: "ip_address", render: (value: string) => value || "—"},
                    {title: "User-Agent", dataIndex: "user_agent", ellipsis: true},
                    {title: "时间", dataIndex: "created_at", render: formatTime}
                  ]}
                  locale={{emptyText: "该玩家没有可显示的登录历史。"}}
                />
              )
            },
            {
              key: "risks",
              label: `风险事件 (${riskEvents.length})`,
              children: (
                <Table<RiskEvent>
                  rowKey="id"
                  size="small"
                  loading={activityLoading}
                  pagination={false}
                  dataSource={riskEvents}
                  scroll={{x: 800}}
                  columns={[
                    {title: "事件", dataIndex: "event_type"},
                    {title: "严重度", dataIndex: "severity", render: (value: string) => <Tag>{value}</Tag>},
                    {title: "IP 摘要", dataIndex: "ip_address", render: (value: string) => value || "—"},
                    {title: "时间", dataIndex: "created_at", render: formatTime},
                    {title: "状态", dataIndex: "resolved_at", render: (value: string | null) => value ? "已处理" : "待处理"}
                  ]}
                  locale={{emptyText: "该玩家没有关联风险事件。"}}
                />
              )
            }
          ]}
        />
      </Card>

      <Modal
        open={action !== null}
        title={actionTitle(action, player.is_vip)}
        okText={action === "revoke" ? "确认撤销" : "确认执行"}
        cancelText="取消"
        confirmLoading={working}
        okButtonProps={{danger: action === "ban" || action === "revoke"}}
        onCancel={() => !working && setAction(null)}
        onOk={() => actionForm.submit()}
        destroyOnHidden
      >
        <Alert
          type={action === "ban" || action === "revoke" ? "warning" : "info"}
          showIcon
          message={actionConsequence(action, player.is_vip)}
          className="action-alert"
        />
        <Form<ActionForm>
          form={actionForm}
          layout="vertical"
          requiredMark={false}
          onFinish={submitAction}
        >
          <Form.Item
            label="操作原因"
            name="reason"
            rules={[
              {required: true, whitespace: true, message: "请填写可供审计追溯的操作原因。"},
              {max: 500, message: "操作原因不能超过 500 个字符。"}
            ]}
          >
            <Input.TextArea
              rows={3}
              maxLength={500}
              showCount
              placeholder="例如：客服工单 CS-4812 已确认账号违规"
            />
          </Form.Item>
          {action !== "revoke" && (
            <Form.Item
              label="内部备注（可选）"
              name="internal_note"
              rules={[{max: 2000, message: "内部备注不能超过 2000 个字符。"}]}
            >
              <Input.TextArea
                rows={2}
                maxLength={2000}
                placeholder="记录复核人、工单或补充背景；请勿填写密码、Token 或隐私数据"
              />
            </Form.Item>
          )}
          {action === "ban" && (
            <Form.Item name="revoke_sessions" valuePropName="checked">
              <Checkbox>立即撤销该玩家的全部会话，使现有 Token 失效</Checkbox>
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  );
}

function actionTitle(action: PlayerAction | null, isVIP: boolean) {
  switch (action) {
    case "activate":
      return "恢复该玩家账号？";
    case "ban":
      return "封禁该玩家？";
    case "vip":
      return isVIP ? "取消该玩家的 VIP？" : "将该玩家设为 VIP？";
    case "revoke":
      return "撤销该玩家的全部会话？";
    default:
      return "确认管理操作";
  }
}

function actionConsequence(action: PlayerAction | null, isVIP: boolean) {
  switch (action) {
    case "activate":
      return "账号将恢复登录和使用权限。";
    case "ban":
      return "账号将无法继续使用；可同时撤销全部会话以立即下线。";
    case "vip":
      return isVIP ? "玩家将失去 VIP 权益。" : "玩家将立即获得当前配置的 VIP 权益。";
    case "revoke":
      return "所有现有 Access Token 和 Refresh Token 将立即失效，玩家需要重新登录。";
    default:
      return "";
  }
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}

function apiMessage(error: unknown) {
  if (error instanceof ApiError) {
    return error.requestId ? `${error.message}（请求编号：${error.requestId}）` : error.message;
  }
  return error instanceof Error ? error.message : "操作失败。";
}
