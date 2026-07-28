import { localeTag, tr } from "../i18n";
import { EyeOutlined, ReloadOutlined, SearchOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Button, Card, Input, Select, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import type { Player } from "../types";
function playerColumns(): TableColumnsType<Player> {
    return [
    {
        title: tr("\u73A9\u5BB6"),
        key: "player",
        fixed: "left",
        render: (_, item) => (<div className="primary-cell">
        <strong>{item.persona_name || tr("\u672A\u547D\u540D\u73A9\u5BB6")}</strong>
        <span>{item.player_id}</span>
      </div>)
    },
    {
        title: "SteamID",
        dataIndex: "steam_id",
        width: 190,
        render: (value: string) => <Typography.Text copyable>{value}</Typography.Text>
    },
    {
        title: tr("\u72B6\u6001"),
        dataIndex: "account_status",
        width: 120,
        render: (value: Player["account_status"]) => (<Tag color={value === "ACTIVE" ? "green" : value === "BANNED" ? "red" : "default"}>
        {value}
      </Tag>)
    },
    {
        title: "VIP",
        dataIndex: "is_vip",
        width: 90,
        render: (value: boolean) => (value ? <Tag color="gold">VIP</Tag> : "—")
    },
    {
        title: tr("\u8BA4\u8BC1"),
        dataIndex: "auth_level",
        width: 130,
        render: (value: string) => <Tag bordered={false}>{value || "unknown"}</Tag>
    },
    {
        title: tr("\u6700\u540E\u767B\u5F55"),
        dataIndex: "last_login_at",
        width: 180,
        render: (value: string) => formatTime(value)
    }
    ];
}
export function PlayersListPage() {
    const columns = playerColumns();
    const navigate = useNavigate();
    const [status, setStatus] = useState("");
    const [search, setSearch] = useState("");
    const { query, result } = useList<Player>({
        resource: "players",
        pagination: { pageSize: 100 },
        filters: status ? [{ field: "account_status", operator: "eq", value: status }] : []
    });
    const items = useMemo(() => {
        const term = search.trim().toLowerCase();
        if (!term) {
            return result.data;
        }
        return result.data.filter((item) => [item.player_id, item.steam_id, item.persona_name]
            .filter(Boolean)
            .some((value) => value.toLowerCase().includes(term)));
    }, [result.data, search]);
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">PLAYERS</Typography.Text>
          <Typography.Title level={2}>{tr("\u73A9\u5BB6\u7BA1\u7406")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u67E5\u8BE2\u73A9\u5BB6\u8BA4\u8BC1\u72B6\u6001\u4E0E\u8D26\u53F7\u5C5E\u6027\u3002\u5B8C\u6574 IP \u548C\u51ED\u636E\u4E0D\u4F1A\u5728\u5217\u8868\u4E2D\u5C55\u793A\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
      </section>
      <Card className="table-card">
        <div className="table-toolbar">
          <Input allowClear prefix={<SearchOutlined />} placeholder={tr("\u5728\u5F53\u524D\u7ED3\u679C\u4E2D\u641C\u7D22\u73A9\u5BB6 ID\u3001SteamID \u6216\u540D\u79F0")} value={search} onChange={(event) => setSearch(event.target.value)} className="table-search"/>
          <Select value={status} onChange={setStatus} options={[
            { value: "", label: tr("\u5168\u90E8\u72B6\u6001") },
            { value: "ACTIVE", label: tr("\u6B63\u5E38") },
            { value: "BANNED", label: tr("\u5DF2\u5C01\u7981") },
            { value: "DELETED", label: tr("\u5DF2\u5220\u9664") }
        ]} className="status-filter"/>
          <Typography.Text type="secondary">{tr("\u5F53\u524D\u7ED3\u679C")}{items.length}{tr("\u6761")}</Typography.Text>
        </div>
        <Table<Player> rowKey="player_id" columns={[
            ...columns,
            {
                title: tr("\u64CD\u4F5C"),
                key: "actions",
                fixed: "right",
                width: 90,
                render: (_, item) => (<Button type="link" icon={<EyeOutlined />} onClick={() => navigate(`/players/${item.player_id}`)}>{tr("\u8BE6\u60C5")}</Button>)
            }
        ]} dataSource={items} loading={query.isLoading} pagination={false} scroll={{ x: 1050 }} locale={{ emptyText: status ? tr("\u5F53\u524D\u72B6\u6001\u4E0B\u6CA1\u6709\u73A9\u5BB6\u3002") : tr("\u5C1A\u65E0\u73A9\u5BB6\u8BB0\u5F55\u3002") }}/>
      </Card>
    </div>);
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
