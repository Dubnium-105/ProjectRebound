import {ReloadOutlined} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  App,
  Button,
  Card,
  Collapse,
  Form,
  Input,
  Modal,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import type {RiskEvent} from "../types";

const severityColor: Record<string, string> = {
  LOW: "blue",
  MEDIUM: "gold",
  HIGH: "orange",
  CRITICAL: "red"
};

const columns: TableColumnsType<RiskEvent> = [
  {
    title: "事件",
    dataIndex: "event_type",
    render: (value: string, item) => (
      <div className="primary-cell">
        <strong>{value}</strong>
        <span>{item.id}</span>
      </div>
    )
  },
  {
    title: "严重度",
    dataIndex: "severity",
    width: 120,
    render: (value: string) => <Tag color={severityColor[value] ?? "default"}>{value}</Tag>
  },
  {
    title: "玩家",
    key: "player",
    width: 190,
    render: (_, item) => item.player_id || item.steam_id || "未关联"
  },
  {
    title: "IP 摘要",
    dataIndex: "ip_address",
    width: 150,
    render: (value?: string) => value || "—"
  },
  {
    title: "时间",
    dataIndex: "created_at",
    width: 180,
    render: (value: string) => formatTime(value)
  },
  {
    title: "处理状态",
    dataIndex: "resolved_at",
    width: 120,
    render: (value: string | null) => (
      <Tag color={value ? "green" : "red"}>{value ? "已处理" : "待处理"}</Tag>
    )
  }
];

export function RiskEventsPage() {
  const {message} = App.useApp();
  const [resolutionForm] = Form.useForm<{reason: string}>();
  const [selected, setSelected] = useState<RiskEvent | null>(null);
  const [resolving, setResolving] = useState(false);
  const {query, result} = useList<RiskEvent>({
    resource: "risk-events",
    pagination: {pageSize: 100}
  });
  const canResolve = authClient.permissions().includes("risk_events.resolve");
  const tableColumns: TableColumnsType<RiskEvent> = [
    ...columns,
    {
      title: "操作",
      key: "actions",
      width: 110,
      fixed: "right",
      render: (_, item) =>
        canResolve && !item.resolved_at ? (
          <Button
            type="link"
            onClick={() => {
              resolutionForm.resetFields();
              setSelected(item);
            }}
          >
            标记已处理
          </Button>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        )
    }
  ];

  const resolve = async ({reason}: {reason: string}) => {
    if (!selected) {
      return;
    }
    setResolving(true);
    try {
      await apiRequest(`/v1/admin/risk-events/${encodeURIComponent(selected.id)}/resolve`, {
        method: "POST",
        body: JSON.stringify({reason})
      });
      message.success("风险事件已标记为处理完成。");
      setSelected(null);
      await query.refetch();
    } catch (error) {
      message.error(
        error instanceof ApiError && error.requestId
          ? `${error.message}（请求编号：${error.requestId}）`
          : error instanceof Error
            ? error.message
            : "处理失败。"
      );
    } finally {
      setResolving(false);
    }
  };

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / RISK EVENTS</Typography.Text>
          <Typography.Title level={2}>登录风险事件</Typography.Title>
          <Typography.Paragraph type="secondary">
            设备哈希不返回前端，IP 地址由控制面脱敏。技术详情按需展开。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>
          刷新
        </Button>
      </section>
      <Card className="table-card">
        <Table<RiskEvent>
          rowKey="id"
          columns={tableColumns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1000}}
          expandable={{
            expandedRowRender: (item) => (
              <Collapse
                size="small"
                ghost
                items={[
                  {
                    key: "details",
                    label: "技术详情",
                    children: (
                      <pre className="json-block">
                        {JSON.stringify(item.details, null, 2)}
                      </pre>
                    )
                  }
                ]}
              />
            )
          }}
          locale={{emptyText: "当前没有登录风险事件。"}}
        />
      </Card>
      <Modal
        open={selected !== null}
        title="标记风险事件已处理"
        okText="确认处理"
        cancelText="取消"
        confirmLoading={resolving}
        onCancel={() => !resolving && setSelected(null)}
        onOk={() => resolutionForm.submit()}
        destroyOnHidden
      >
        <Typography.Paragraph type="secondary">
          这不会自动封禁玩家或撤销会话。处理人、时间和原因会写入操作审计。
        </Typography.Paragraph>
        <Form form={resolutionForm} layout="vertical" requiredMark={false} onFinish={resolve}>
          <Form.Item
            label="处理结论与原因"
            name="reason"
            rules={[
              {required: true, whitespace: true, message: "请填写处理结论。"},
              {max: 500, message: "处理结论不能超过 500 个字符。"}
            ]}
          >
            <Input.TextArea rows={4} maxLength={500} showCount />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}
