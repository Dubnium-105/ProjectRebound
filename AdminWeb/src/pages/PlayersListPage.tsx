import {EyeOutlined, ReloadOutlined, SearchOutlined} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  Button,
  Card,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {useMemo, useState} from "react";
import {useNavigate} from "react-router";
import type {Player} from "../types";

const columns: TableColumnsType<Player> = [
  {
    title: "玩家",
    key: "player",
    fixed: "left",
    render: (_, item) => (
      <div className="primary-cell">
        <strong>{item.persona_name || "未命名玩家"}</strong>
        <span>{item.player_id}</span>
      </div>
    )
  },
  {
    title: "SteamID",
    dataIndex: "steam_id",
    width: 190,
    render: (value: string) => <Typography.Text copyable>{value}</Typography.Text>
  },
  {
    title: "状态",
    dataIndex: "account_status",
    width: 120,
    render: (value: Player["account_status"]) => (
      <Tag color={value === "ACTIVE" ? "green" : value === "BANNED" ? "red" : "default"}>
        {value}
      </Tag>
    )
  },
  {
    title: "VIP",
    dataIndex: "is_vip",
    width: 90,
    render: (value: boolean) => (value ? <Tag color="gold">VIP</Tag> : "—")
  },
  {
    title: "认证",
    dataIndex: "auth_level",
    width: 130,
    render: (value: string) => <Tag bordered={false}>{value || "unknown"}</Tag>
  },
  {
    title: "最后登录",
    dataIndex: "last_login_at",
    width: 180,
    render: (value: string) => formatTime(value)
  }
];

export function PlayersListPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState("");
  const [search, setSearch] = useState("");
  const {query, result} = useList<Player>({
    resource: "players",
    pagination: {pageSize: 100},
    filters: status ? [{field: "account_status", operator: "eq", value: status}] : []
  });

  const items = useMemo(() => {
    const term = search.trim().toLowerCase();
    if (!term) {
      return result.data;
    }
    return result.data.filter((item) =>
      [item.player_id, item.steam_id, item.persona_name]
        .filter(Boolean)
        .some((value) => value.toLowerCase().includes(term))
    );
  }, [result.data, search]);

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">PLAYERS</Typography.Text>
          <Typography.Title level={2}>玩家管理</Typography.Title>
          <Typography.Paragraph type="secondary">
            查询玩家认证状态与账号属性。完整 IP 和凭据不会在列表中展示。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>
          刷新
        </Button>
      </section>
      <Card className="table-card">
        <div className="table-toolbar">
          <Input
            allowClear
            prefix={<SearchOutlined />}
            placeholder="在当前结果中搜索玩家 ID、SteamID 或名称"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            className="table-search"
          />
          <Select
            value={status}
            onChange={setStatus}
            options={[
              {value: "", label: "全部状态"},
              {value: "ACTIVE", label: "正常"},
              {value: "BANNED", label: "已封禁"},
              {value: "DELETED", label: "已删除"}
            ]}
            className="status-filter"
          />
          <Typography.Text type="secondary">
            当前结果 {items.length} 条
          </Typography.Text>
        </div>
        <Table<Player>
          rowKey="player_id"
          columns={[
            ...columns,
            {
              title: "操作",
              key: "actions",
              fixed: "right",
              width: 90,
              render: (_, item) => (
                <Button
                  type="link"
                  icon={<EyeOutlined />}
                  onClick={() => navigate(`/players/${item.player_id}`)}
                >
                  详情
                </Button>
              )
            }
          ]}
          dataSource={items}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1050}}
          locale={{emptyText: status ? "当前状态下没有玩家。" : "尚无玩家记录。"}}
        />
      </Card>
    </div>
  );
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}
