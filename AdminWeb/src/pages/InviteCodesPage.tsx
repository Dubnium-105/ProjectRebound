import {
  CopyOutlined,
  DownloadOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
  StopOutlined
} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Drawer,
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
import type {InviteCode, InviteCodeUse} from "../types";

type CreateValues = {
  batch_name: string;
  quantity: number;
  max_uses: number;
  expires_at?: string;
  create_account: boolean;
  p2p: boolean;
  note?: string;
  reason: string;
};

type EditValues = {
  batch_name: string;
  max_uses: number;
  expires_at?: string;
  enabled: boolean;
  reason: string;
};

type CreatedItem = {
  invite_code: InviteCode;
  code: string;
};

type SimpleOperation = {
  item: InviteCode;
  operation: "toggle" | "revoke";
};

export function InviteCodesPage() {
  const {message} = App.useApp();
  const [createForm] = Form.useForm<CreateValues>();
  const [editForm] = Form.useForm<EditValues>();
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<InviteCode | null>(null);
  const [operation, setOperation] = useState<SimpleOperation | null>(null);
  const [created, setCreated] = useState<CreatedItem[] | null>(null);
  const [usesTarget, setUsesTarget] = useState<InviteCode | null>(null);
  const [uses, setUses] = useState<InviteCodeUse[]>([]);
  const [usesLoading, setUsesLoading] = useState(false);
  const [working, setWorking] = useState(false);
  const {query, result} = useList<InviteCode>({
    resource: "invite-codes",
    pagination: {pageSize: 100}
  });
  const permissions = authClient.permissions();

  const openCreate = () => {
    createForm.setFieldsValue({
      batch_name: "",
      quantity: 1,
      max_uses: 1,
      expires_at: "",
      create_account: true,
      p2p: true,
      note: "",
      reason: ""
    });
    setCreateOpen(true);
  };

  const submitCreate = async (values: CreateValues) => {
    setWorking(true);
    try {
      const response = await apiRequest<{
        invite_codes: CreatedItem[];
        invite_code: InviteCode;
        code: string;
      }>("/v1/admin/invite-codes", {
        method: "POST",
        body: JSON.stringify({
          batch_name: values.batch_name,
          quantity: values.quantity,
          max_uses: values.max_uses,
          expires_at: toISOString(values.expires_at),
          permissions: {
            allow_create_account: values.create_account,
            allow_p2p: values.p2p,
            note: values.note?.trim() || undefined
          },
          reason: values.reason
        })
      });
      const oneTimeItems = response.invite_codes?.length
        ? response.invite_codes
        : [{invite_code: response.invite_code, code: response.code}];
      setCreateOpen(false);
      setCreated(oneTimeItems);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const openEdit = (item: InviteCode) => {
    editForm.setFieldsValue({
      batch_name: item.batch_name,
      max_uses: item.max_uses,
      expires_at: toLocalDateTime(item.expires_at),
      enabled: item.enabled,
      reason: ""
    });
    setEditTarget(item);
  };

  const submitEdit = async (values: EditValues) => {
    if (!editTarget) return;
    setWorking(true);
    try {
      await apiRequest(`/v1/admin/invite-codes/${encodeURIComponent(editTarget.id)}`, {
        method: "PATCH",
        body: JSON.stringify({
          batch_name: values.batch_name,
          max_uses: values.max_uses,
          expires_at: values.expires_at ? toISOString(values.expires_at) : null,
          enabled: values.enabled,
          reason: values.reason
        })
      });
      message.success("邀请码配置已更新。");
      setEditTarget(null);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const executeSimple = async ({reason}: {reason: string}) => {
    if (!operation) return;
    setWorking(true);
    try {
      if (operation.operation === "revoke") {
        await apiRequest(`/v1/admin/invite-codes/${encodeURIComponent(operation.item.id)}/revoke`, {
          method: "POST",
          body: JSON.stringify({reason})
        });
        message.success("邀请码已撤销，不能再次启用。");
      } else {
        await apiRequest(`/v1/admin/invite-codes/${encodeURIComponent(operation.item.id)}`, {
          method: "PATCH",
          body: JSON.stringify({enabled: !operation.item.enabled, reason})
        });
        message.success(operation.item.enabled ? "邀请码已停用。" : "邀请码已重新启用。");
      }
      setOperation(null);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const openUses = async (item: InviteCode) => {
    setUsesTarget(item);
    setUses([]);
    setUsesLoading(true);
    try {
      const response = await apiRequest<{items: InviteCodeUse[]}>(
        `/v1/admin/invite-codes/${encodeURIComponent(item.id)}/uses?limit=100`
      );
      setUses(response.items);
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setUsesLoading(false);
    }
  };

  const columns: TableColumnsType<InviteCode> = [
    {
      title: "批次",
      dataIndex: "batch_name",
      render: (value: string, item) => (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.id}</span>
        </div>
      )
    },
    {
      title: "状态",
      key: "status",
      width: 120,
      render: (_, item) => {
        const status = inviteStatus(item);
        return <Tag color={status.color}>{status.label}</Tag>;
      }
    },
    {
      title: "使用量",
      key: "usage",
      width: 210,
      render: (_, item) => {
        const percent = Math.min(100, Math.round((item.used_count / item.max_uses) * 100));
        return (
          <div className="usage-cell">
            <span>{item.used_count} / {item.max_uses}</span>
            <Progress percent={percent} showInfo={false} size="small" />
          </div>
        );
      }
    },
    {
      title: "权限范围",
      dataIndex: "permissions",
      width: 220,
      render: (value: Record<string, unknown>) => (
        <Space size={[4, 4]} wrap>
          {value.allow_create_account === true && <Tag>创建账号</Tag>}
          {value.allow_p2p === true && <Tag>P2P</Tag>}
          {value.allow_create_account !== true && value.allow_p2p !== true && <span>默认</span>}
        </Space>
      )
    },
    {
      title: "有效期至",
      dataIndex: "expires_at",
      width: 180,
      render: (value: string | null) => (value ? formatTime(value) : "长期有效")
    },
    {title: "创建者", dataIndex: "created_by", width: 150},
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 300,
      render: (_, item) => (
        <Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => openUses(item)}>
            使用记录
          </Button>
          {permissions.includes("invite_codes.update") && !item.revoked_at && (
            <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(item)}>
              编辑
            </Button>
          )}
          {permissions.includes("invite_codes.update") && !item.revoked_at && (
            <Button type="link" onClick={() => setOperation({item, operation: "toggle"})}>
              {item.enabled ? "停用" : "启用"}
            </Button>
          )}
          {permissions.includes("invite_codes.revoke") && !item.revoked_at && (
            <Button
              danger
              type="link"
              icon={<StopOutlined />}
              onClick={() => setOperation({item, operation: "revoke"})}
            >
              撤销
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
          <Typography.Text className="eyebrow">ACCESS / INVITATIONS</Typography.Text>
          <Typography.Title level={2}>邀请码</Typography.Title>
          <Typography.Paragraph type="secondary">
            明文邀请码只在创建成功时显示一次；后端仅保存不可逆哈希。
          </Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>
            刷新
          </Button>
          {permissions.includes("invite_codes.create") && (
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              创建邀请码
            </Button>
          )}
        </Space>
      </section>
      <Card className="table-card">
        <Table<InviteCode>
          rowKey="id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1400}}
          locale={{emptyText: "尚无邀请码。"}}
        />
      </Card>

      <Modal
        open={createOpen}
        title="创建邀请码批次"
        okText="生成邀请码"
        cancelText="取消"
        confirmLoading={working}
        onCancel={() => !working && setCreateOpen(false)}
        onOk={() => createForm.submit()}
        destroyOnHidden
      >
        <Form form={createForm} layout="vertical" requiredMark={false} onFinish={submitCreate}>
          <Form.Item label="批次名称" name="batch_name" rules={[{required: true, whitespace: true}, {max: 128}]}>
            <Input placeholder="例如：2026 夏季测试资格" maxLength={128} />
          </Form.Item>
          <Space align="start" style={{width: "100%"}}>
            <Form.Item label="生成数量" name="quantity" rules={[{required: true}]}>
              <InputNumber min={1} max={100} />
            </Form.Item>
            <Form.Item label="每码最大使用次数" name="max_uses" rules={[{required: true}]}>
              <InputNumber min={1} max={1_000_000} />
            </Form.Item>
          </Space>
          <Form.Item label="有效期（留空为长期）" name="expires_at">
            <Input type="datetime-local" />
          </Form.Item>
          <Form.Item name="create_account" valuePropName="checked">
            <Checkbox>允许创建账号</Checkbox>
          </Form.Item>
          <Form.Item name="p2p" valuePropName="checked">
            <Checkbox>允许 P2P 联机</Checkbox>
          </Form.Item>
          <Form.Item label="运营备注" name="note" rules={[{max: 300}]}>
            <Input.TextArea rows={2} maxLength={300} showCount />
          </Form.Item>
          <Form.Item label="创建原因" name="reason" rules={reasonRules}>
            <Input.TextArea rows={3} maxLength={500} showCount />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={editTarget !== null}
        title={`编辑 ${editTarget?.batch_name ?? ""}`}
        okText="保存修改"
        cancelText="取消"
        confirmLoading={working}
        onCancel={() => !working && setEditTarget(null)}
        onOk={() => editForm.submit()}
        destroyOnHidden
      >
        <Form form={editForm} layout="vertical" requiredMark={false} onFinish={submitEdit}>
          <Form.Item label="批次名称" name="batch_name" rules={[{required: true, whitespace: true}, {max: 128}]}>
            <Input maxLength={128} />
          </Form.Item>
          <Form.Item
            label="最大使用次数"
            name="max_uses"
            rules={[
              {required: true},
              {
                validator: (_, value) =>
                  value >= (editTarget?.used_count ?? 0)
                    ? Promise.resolve()
                    : Promise.reject(new Error("不能低于已使用次数。"))
              }
            ]}
          >
            <InputNumber min={Math.max(1, editTarget?.used_count ?? 1)} max={1_000_000} />
          </Form.Item>
          <Form.Item label="有效期（留空清除）" name="expires_at">
            <Input type="datetime-local" />
          </Form.Item>
          <Form.Item name="enabled" valuePropName="checked">
            <Checkbox>启用</Checkbox>
          </Form.Item>
          <Form.Item label="修改原因" name="reason" rules={reasonRules}>
            <Input.TextArea rows={3} maxLength={500} showCount />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={created !== null}
        title="邀请码已生成"
        footer={[
          <Button key="copy" icon={<CopyOutlined />} onClick={() => copyCodes(created ?? [], message)}>
            复制全部
          </Button>,
          <Button key="download" icon={<DownloadOutlined />} onClick={() => downloadCodes(created ?? [])}>
            下载 CSV
          </Button>,
          <Button key="close" type="primary" onClick={() => setCreated(null)}>
            我已安全保存
          </Button>
        ]}
        closable={false}
        maskClosable={false}
        width={720}
      >
        <Alert
          type="warning"
          showIcon
          message="这些明文邀请码只显示本次"
          description="关闭后无法从服务器恢复。请立即复制或下载，并将文件存放在受控位置。"
          style={{marginBottom: 16}}
        />
        <Table<CreatedItem>
          rowKey={(item) => item.invite_code.id}
          dataSource={created ?? []}
          pagination={false}
          size="small"
          columns={[
            {title: "批次", render: (_, item) => item.invite_code.batch_name},
            {
              title: "邀请码",
              dataIndex: "code",
              render: (value: string) => <Typography.Text code copyable>{value}</Typography.Text>
            },
            {title: "最大次数", render: (_, item) => item.invite_code.max_uses, width: 100}
          ]}
        />
      </Modal>

      <Drawer
        open={usesTarget !== null}
        title={`使用记录 · ${usesTarget?.batch_name ?? ""}`}
        width={760}
        onClose={() => setUsesTarget(null)}
      >
        <Table<InviteCodeUse>
          rowKey="id"
          dataSource={uses}
          loading={usesLoading}
          pagination={false}
          columns={[
            {
              title: "玩家",
              render: (_, item) => (
                <div className="primary-cell">
                  <strong>{item.player_id}</strong>
                  <span>Steam {item.steam_id}</span>
                </div>
              )
            },
            {title: "IP 摘要", dataIndex: "ip_address", width: 160, render: (value: string) => value || "—"},
            {title: "使用时间", dataIndex: "used_at", width: 190, render: formatTime},
            {title: "结果", dataIndex: "result", width: 100, render: () => <Tag color="green">成功</Tag>}
          ]}
          locale={{emptyText: "暂无使用记录。"}}
        />
      </Drawer>

      <OperationReasonModal
        open={operation !== null}
        title={operation?.operation === "revoke"
          ? `撤销 ${operation?.item.batch_name ?? ""}？`
          : `${operation?.item.enabled ? "停用" : "启用"} ${operation?.item.batch_name ?? ""}？`}
        consequence={operation?.operation === "revoke"
          ? "撤销是永久操作；该邀请码之后不能再次启用。"
          : operation?.item.enabled
            ? "停用后新注册无法使用，但可由有权限的管理员重新启用。"
            : "启用后，该邀请码将在配额和有效期允许时立即可用。"}
        confirmLabel={operation?.operation === "revoke" ? "确认永久撤销" : "确认执行"}
        danger={operation?.operation === "revoke"}
        loading={working}
        onCancel={() => setOperation(null)}
        onConfirm={executeSimple}
      />
    </div>
  );
}

const reasonRules = [
  {required: true, whitespace: true, message: "请填写可供审计追溯的操作原因。"},
  {max: 500, message: "不能超过 500 个字符。"}
];

function inviteStatus(item: InviteCode) {
  if (item.revoked_at) return {label: "已撤销", color: "red"};
  if (!item.enabled) return {label: "已停用", color: "default"};
  if (item.used_count >= item.max_uses) return {label: "已用尽", color: "default"};
  if (item.expires_at && new Date(item.expires_at) <= new Date()) return {label: "已过期", color: "default"};
  return {label: "有效", color: "green"};
}

function toISOString(value?: string) {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}

function toLocalDateTime(value: string | null) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
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

async function copyCodes(items: CreatedItem[], message: ReturnType<typeof App.useApp>["message"]) {
  try {
    await navigator.clipboard.writeText(items.map((item) => item.code).join("\n"));
    message.success("已复制全部邀请码。");
  } catch {
    message.error("浏览器未允许访问剪贴板，请逐条复制或下载 CSV。");
  }
}

function downloadCodes(items: CreatedItem[]) {
  const rows = [
    ["invite_code_id", "batch_name", "code", "max_uses", "expires_at"],
    ...items.map((item) => [
      item.invite_code.id,
      item.invite_code.batch_name,
      item.code,
      String(item.invite_code.max_uses),
      item.invite_code.expires_at ?? ""
    ])
  ];
  const csv = rows.map((row) => row.map(csvCell).join(",")).join("\r\n");
  const blob = new Blob([`\uFEFF${csv}`], {type: "text/csv;charset=utf-8"});
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `invite-codes-${new Date().toISOString().slice(0, 10)}.csv`;
  link.click();
  URL.revokeObjectURL(url);
}

function csvCell(value: string) {
  return `"${value.replaceAll('"', '""')}"`;
}
