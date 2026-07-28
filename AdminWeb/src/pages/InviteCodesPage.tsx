import { localeTag, tr } from "../i18n";
import { CopyOutlined, DownloadOutlined, EditOutlined, EyeOutlined, PlusOutlined, ReloadOutlined, StopOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Alert, App, Button, Card, Checkbox, Drawer, Form, Input, InputNumber, Modal, Progress, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { InviteCode, InviteCodeUse } from "../types";
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
    const { message } = App.useApp();
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
    const { query, result } = useList<InviteCode>({
        resource: "invite-codes",
        pagination: { pageSize: 100 }
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
                : [{ invite_code: response.invite_code, code: response.code }];
            setCreateOpen(false);
            setCreated(oneTimeItems);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
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
        if (!editTarget)
            return;
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
            message.success(tr("\u9080\u8BF7\u7801\u914D\u7F6E\u5DF2\u66F4\u65B0\u3002"));
            setEditTarget(null);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const executeSimple = async ({ reason }: {
        reason: string;
    }) => {
        if (!operation)
            return;
        setWorking(true);
        try {
            if (operation.operation === "revoke") {
                await apiRequest(`/v1/admin/invite-codes/${encodeURIComponent(operation.item.id)}/revoke`, {
                    method: "POST",
                    body: JSON.stringify({ reason })
                });
                message.success(tr("\u9080\u8BF7\u7801\u5DF2\u64A4\u9500\uFF0C\u4E0D\u80FD\u518D\u6B21\u542F\u7528\u3002"));
            }
            else {
                await apiRequest(`/v1/admin/invite-codes/${encodeURIComponent(operation.item.id)}`, {
                    method: "PATCH",
                    body: JSON.stringify({ enabled: !operation.item.enabled, reason })
                });
                message.success(operation.item.enabled ? tr("\u9080\u8BF7\u7801\u5DF2\u505C\u7528\u3002") : tr("\u9080\u8BF7\u7801\u5DF2\u91CD\u65B0\u542F\u7528\u3002"));
            }
            setOperation(null);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const openUses = async (item: InviteCode) => {
        setUsesTarget(item);
        setUses([]);
        setUsesLoading(true);
        try {
            const response = await apiRequest<{
                items: InviteCodeUse[];
            }>(`/v1/admin/invite-codes/${encodeURIComponent(item.id)}/uses?limit=100`);
            setUses(response.items);
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setUsesLoading(false);
        }
    };
    const columns: TableColumnsType<InviteCode> = [
        {
            title: tr("\u6279\u6B21"),
            dataIndex: "batch_name",
            render: (value: string, item) => (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.id}</span>
        </div>)
        },
        {
            title: tr("\u72B6\u6001"),
            key: "status",
            width: 120,
            render: (_, item) => {
                const status = inviteStatus(item);
                return <Tag color={status.color}>{status.label}</Tag>;
            }
        },
        {
            title: tr("\u4F7F\u7528\u91CF"),
            key: "usage",
            width: 210,
            render: (_, item) => {
                const percent = Math.min(100, Math.round((item.used_count / item.max_uses) * 100));
                return (<div className="usage-cell">
            <span>{item.used_count} / {item.max_uses}</span>
            <Progress percent={percent} showInfo={false} size="small"/>
          </div>);
            }
        },
        {
            title: tr("\u6743\u9650\u8303\u56F4"),
            dataIndex: "permissions",
            width: 220,
            render: (value: Record<string, unknown>) => (<Space size={[4, 4]} wrap>
          {value.allow_create_account === true && <Tag>{tr("\u521B\u5EFA\u8D26\u53F7")}</Tag>}
          {value.allow_p2p === true && <Tag>P2P</Tag>}
          {value.allow_create_account !== true && value.allow_p2p !== true && <span>{tr("\u9ED8\u8BA4")}</span>}
        </Space>)
        },
        {
            title: tr("\u6709\u6548\u671F\u81F3"),
            dataIndex: "expires_at",
            width: 180,
            render: (value: string | null) => (value ? formatTime(value) : tr("\u957F\u671F\u6709\u6548"))
        },
        { title: tr("\u521B\u5EFA\u8005"), dataIndex: "created_by", width: 150 },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            fixed: "right",
            width: 300,
            render: (_, item) => (<Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => openUses(item)}>{tr("\u4F7F\u7528\u8BB0\u5F55")}</Button>
          {permissions.includes("invite_codes.update") && !item.revoked_at && (<Button type="link" icon={<EditOutlined />} onClick={() => openEdit(item)}>{tr("\u7F16\u8F91")}</Button>)}
          {permissions.includes("invite_codes.update") && !item.revoked_at && (<Button type="link" onClick={() => setOperation({ item, operation: "toggle" })}>
              {item.enabled ? tr("\u505C\u7528") : tr("\u542F\u7528")}
            </Button>)}
          {permissions.includes("invite_codes.revoke") && !item.revoked_at && (<Button danger type="link" icon={<StopOutlined />} onClick={() => setOperation({ item, operation: "revoke" })}>{tr("\u64A4\u9500")}</Button>)}
        </Space>)
        }
    ];
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">ACCESS / INVITATIONS</Typography.Text>
          <Typography.Title level={2}>{tr("\u9080\u8BF7\u7801")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u660E\u6587\u9080\u8BF7\u7801\u53EA\u5728\u521B\u5EFA\u6210\u529F\u65F6\u663E\u793A\u4E00\u6B21\uFF1B\u540E\u7AEF\u4EC5\u4FDD\u5B58\u4E0D\u53EF\u9006\u54C8\u5E0C\u3002")}</Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
          {permissions.includes("invite_codes.create") && (<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{tr("\u521B\u5EFA\u9080\u8BF7\u7801")}</Button>)}
        </Space>
      </section>
      <Card className="table-card">
        <Table<InviteCode> rowKey="id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1400 }} locale={{ emptyText: tr("\u5C1A\u65E0\u9080\u8BF7\u7801\u3002") }}/>
      </Card>

      <Modal open={createOpen} title={tr("\u521B\u5EFA\u9080\u8BF7\u7801\u6279\u6B21")} okText={tr("\u751F\u6210\u9080\u8BF7\u7801")} cancelText={tr("\u53D6\u6D88")} confirmLoading={working} onCancel={() => !working && setCreateOpen(false)} onOk={() => createForm.submit()} destroyOnHidden>
        <Form form={createForm} layout="vertical" requiredMark={false} onFinish={submitCreate}>
          <Form.Item label={tr("\u6279\u6B21\u540D\u79F0")} name="batch_name" rules={[{ required: true, whitespace: true }, { max: 128 }]}>
            <Input placeholder={tr("\u4F8B\u5982\uFF1A2026 \u590F\u5B63\u6D4B\u8BD5\u8D44\u683C")} maxLength={128}/>
          </Form.Item>
          <Space align="start" style={{ width: "100%" }}>
            <Form.Item label={tr("\u751F\u6210\u6570\u91CF")} name="quantity" rules={[{ required: true }]}>
              <InputNumber min={1} max={100}/>
            </Form.Item>
            <Form.Item label={tr("\u6BCF\u7801\u6700\u5927\u4F7F\u7528\u6B21\u6570")} name="max_uses" rules={[{ required: true }]}>
              <InputNumber min={1} max={1000000}/>
            </Form.Item>
          </Space>
          <Form.Item label={tr("\u6709\u6548\u671F\uFF08\u7559\u7A7A\u4E3A\u957F\u671F\uFF09")} name="expires_at">
            <Input type="datetime-local"/>
          </Form.Item>
          <Form.Item name="create_account" valuePropName="checked">
            <Checkbox>{tr("\u5141\u8BB8\u521B\u5EFA\u8D26\u53F7")}</Checkbox>
          </Form.Item>
          <Form.Item name="p2p" valuePropName="checked">
            <Checkbox>{tr("\u5141\u8BB8 P2P \u8054\u673A")}</Checkbox>
          </Form.Item>
          <Form.Item label={tr("\u8FD0\u8425\u5907\u6CE8")} name="note" rules={[{ max: 300 }]}>
            <Input.TextArea rows={2} maxLength={300} showCount/>
          </Form.Item>
          <Form.Item label={tr("\u521B\u5EFA\u539F\u56E0")} name="reason" rules={reasonRules()}>
            <Input.TextArea rows={3} maxLength={500} showCount/>
          </Form.Item>
        </Form>
      </Modal>

      <Modal open={editTarget !== null} title={tr(`编辑 ${editTarget?.batch_name ?? ""}`)} okText={tr("\u4FDD\u5B58\u4FEE\u6539")} cancelText={tr("\u53D6\u6D88")} confirmLoading={working} onCancel={() => !working && setEditTarget(null)} onOk={() => editForm.submit()} destroyOnHidden>
        <Form form={editForm} layout="vertical" requiredMark={false} onFinish={submitEdit}>
          <Form.Item label={tr("\u6279\u6B21\u540D\u79F0")} name="batch_name" rules={[{ required: true, whitespace: true }, { max: 128 }]}>
            <Input maxLength={128}/>
          </Form.Item>
          <Form.Item label={tr("\u6700\u5927\u4F7F\u7528\u6B21\u6570")} name="max_uses" rules={[
            { required: true },
            {
                validator: (_, value) => value >= (editTarget?.used_count ?? 0)
                    ? Promise.resolve()
                    : Promise.reject(new Error(tr("\u4E0D\u80FD\u4F4E\u4E8E\u5DF2\u4F7F\u7528\u6B21\u6570\u3002")))
            }
        ]}>
            <InputNumber min={Math.max(1, editTarget?.used_count ?? 1)} max={1000000}/>
          </Form.Item>
          <Form.Item label={tr("\u6709\u6548\u671F\uFF08\u7559\u7A7A\u6E05\u9664\uFF09")} name="expires_at">
            <Input type="datetime-local"/>
          </Form.Item>
          <Form.Item name="enabled" valuePropName="checked">
            <Checkbox>{tr("\u542F\u7528")}</Checkbox>
          </Form.Item>
          <Form.Item label={tr("\u4FEE\u6539\u539F\u56E0")} name="reason" rules={reasonRules()}>
            <Input.TextArea rows={3} maxLength={500} showCount/>
          </Form.Item>
        </Form>
      </Modal>

      <Modal open={created !== null} title={tr("\u9080\u8BF7\u7801\u5DF2\u751F\u6210")} footer={[
            <Button key="copy" icon={<CopyOutlined />} onClick={() => copyCodes(created ?? [], message)}>{tr("\u590D\u5236\u5168\u90E8")}</Button>,
            <Button key="download" icon={<DownloadOutlined />} onClick={() => downloadCodes(created ?? [])}>{tr("\u4E0B\u8F7D CSV")}</Button>,
            <Button key="close" type="primary" onClick={() => setCreated(null)}>{tr("\u6211\u5DF2\u5B89\u5168\u4FDD\u5B58")}</Button>
        ]} closable={false} maskClosable={false} width={720}>
        <Alert type="warning" showIcon message={tr("\u8FD9\u4E9B\u660E\u6587\u9080\u8BF7\u7801\u53EA\u663E\u793A\u672C\u6B21")} description={tr("\u5173\u95ED\u540E\u65E0\u6CD5\u4ECE\u670D\u52A1\u5668\u6062\u590D\u3002\u8BF7\u7ACB\u5373\u590D\u5236\u6216\u4E0B\u8F7D\uFF0C\u5E76\u5C06\u6587\u4EF6\u5B58\u653E\u5728\u53D7\u63A7\u4F4D\u7F6E\u3002")} style={{ marginBottom: 16 }}/>
        <Table<CreatedItem> rowKey={(item) => item.invite_code.id} dataSource={created ?? []} pagination={false} size="small" columns={[
            { title: tr("\u6279\u6B21"), render: (_, item) => item.invite_code.batch_name },
            {
                title: tr("\u9080\u8BF7\u7801"),
                dataIndex: "code",
                render: (value: string) => <Typography.Text code copyable>{value}</Typography.Text>
            },
            { title: tr("\u6700\u5927\u6B21\u6570"), render: (_, item) => item.invite_code.max_uses, width: 100 }
        ]}/>
      </Modal>

      <Drawer open={usesTarget !== null} title={tr(`使用记录 · ${usesTarget?.batch_name ?? ""}`)} width={760} onClose={() => setUsesTarget(null)}>
        <Table<InviteCodeUse> rowKey="id" dataSource={uses} loading={usesLoading} pagination={false} columns={[
            {
                title: tr("\u73A9\u5BB6"),
                render: (_, item) => (<div className="primary-cell">
                  <strong>{item.player_id}</strong>
                  <span>Steam {item.steam_id}</span>
                </div>)
            },
            { title: tr("IP \u6458\u8981"), dataIndex: "ip_address", width: 160, render: (value: string) => value || "—" },
            { title: tr("\u4F7F\u7528\u65F6\u95F4"), dataIndex: "used_at", width: 190, render: formatTime },
            { title: tr("\u7ED3\u679C"), dataIndex: "result", width: 100, render: () => <Tag color="green">{tr("\u6210\u529F")}</Tag> }
        ]} locale={{ emptyText: tr("\u6682\u65E0\u4F7F\u7528\u8BB0\u5F55\u3002") }}/>
      </Drawer>

      <OperationReasonModal open={operation !== null} title={operation?.operation === "revoke"
            ? tr(`撤销 ${operation?.item.batch_name ?? ""}？`) : `${operation?.item.enabled ? tr("\u505C\u7528") : tr("\u542F\u7528")} ${operation?.item.batch_name ?? ""}？`} consequence={operation?.operation === "revoke"
            ? tr("\u64A4\u9500\u662F\u6C38\u4E45\u64CD\u4F5C\uFF1B\u8BE5\u9080\u8BF7\u7801\u4E4B\u540E\u4E0D\u80FD\u518D\u6B21\u542F\u7528\u3002") : operation?.item.enabled
            ? tr("\u505C\u7528\u540E\u65B0\u6CE8\u518C\u65E0\u6CD5\u4F7F\u7528\uFF0C\u4F46\u53EF\u7531\u6709\u6743\u9650\u7684\u7BA1\u7406\u5458\u91CD\u65B0\u542F\u7528\u3002") : tr("\u542F\u7528\u540E\uFF0C\u8BE5\u9080\u8BF7\u7801\u5C06\u5728\u914D\u989D\u548C\u6709\u6548\u671F\u5141\u8BB8\u65F6\u7ACB\u5373\u53EF\u7528\u3002")} confirmLabel={operation?.operation === "revoke" ? tr("\u786E\u8BA4\u6C38\u4E45\u64A4\u9500") : tr("\u786E\u8BA4\u6267\u884C")} danger={operation?.operation === "revoke"} loading={working} onCancel={() => setOperation(null)} onConfirm={executeSimple}/>
    </div>);
}
function reasonRules() {
    return [
        { required: true, whitespace: true, message: tr("\u8BF7\u586B\u5199\u53EF\u4F9B\u5BA1\u8BA1\u8FFD\u6EAF\u7684\u64CD\u4F5C\u539F\u56E0\u3002") },
        { max: 500, message: tr("\u4E0D\u80FD\u8D85\u8FC7 500 \u4E2A\u5B57\u7B26\u3002") }
    ];
}
function inviteStatus(item: InviteCode) {
    if (item.revoked_at)
        return { label: tr("\u5DF2\u64A4\u9500"), color: "red" };
    if (!item.enabled)
        return { label: tr("\u5DF2\u505C\u7528"), color: "default" };
    if (item.used_count >= item.max_uses)
        return { label: tr("\u5DF2\u7528\u5C3D"), color: "default" };
    if (item.expires_at && new Date(item.expires_at) <= new Date())
        return { label: tr("\u5DF2\u8FC7\u671F"), color: "default" };
    return { label: tr("\u6709\u6548"), color: "green" };
}
function toISOString(value?: string) {
    if (!value)
        return undefined;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}
function toLocalDateTime(value: string | null) {
    if (!value)
        return "";
    const date = new Date(value);
    if (Number.isNaN(date.getTime()))
        return "";
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
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
async function copyCodes(items: CreatedItem[], message: ReturnType<typeof App.useApp>["message"]) {
    try {
        await navigator.clipboard.writeText(items.map((item) => item.code).join("\n"));
        message.success(tr("\u5DF2\u590D\u5236\u5168\u90E8\u9080\u8BF7\u7801\u3002"));
    }
    catch {
        message.error(tr("\u6D4F\u89C8\u5668\u672A\u5141\u8BB8\u8BBF\u95EE\u526A\u8D34\u677F\uFF0C\u8BF7\u9010\u6761\u590D\u5236\u6216\u4E0B\u8F7D CSV\u3002"));
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
    const blob = new Blob([`\uFEFF${csv}`], { type: "text/csv;charset=utf-8" });
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
