import { localeTag, tr } from "../i18n";
import { CopyOutlined, EditOutlined, PlusOutlined, ReloadOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Alert, App, Button, Card, Checkbox, Form, Input, Modal, QRCode, Select, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useEffect, useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { GovernedAdministrator, GovernedRole } from "../types";
type CreateValues = {
    username: string;
    display_name: string;
    password: string;
    roles: string[];
    reason: string;
    mfa_code: string;
};
type EditValues = {
    display_name: string;
    status: "ACTIVE" | "DISABLED";
    roles: string[];
    revoke_sessions: boolean;
    reason: string;
    mfa_code: string;
};
type ProvisioningResult = {
    admin: GovernedAdministrator;
    totp_provisioning_uri: string;
    recovery_codes: string[];
};
export function AdministratorsPage() {
    const { message } = App.useApp();
    const permissions = authClient.permissions();
    const [createForm] = Form.useForm<CreateValues>();
    const [editForm] = Form.useForm<EditValues>();
    const [roles, setRoles] = useState<GovernedRole[]>([]);
    const [createOpen, setCreateOpen] = useState(false);
    const [editing, setEditing] = useState<GovernedAdministrator | null>(null);
    const [resetting, setResetting] = useState<GovernedAdministrator | null>(null);
    const [provisioning, setProvisioning] = useState<ProvisioningResult | null>(null);
    const [working, setWorking] = useState(false);
    const { query, result } = useList<GovernedAdministrator>({
        resource: "administrators",
        pagination: { pageSize: 100 }
    });
    useEffect(() => {
        apiRequest<{
            items: GovernedRole[];
            permissions: unknown[];
        }>("/v1/admin/roles")
            .then((result) => setRoles(result.items))
            .catch((error) => message.error(errorMessage(error)));
    }, [message]);
    const roleOptions = roles.map((role) => ({
        label: `${role.display_name} (${role.name})`,
        value: role.name
    }));
    const openCreate = () => {
        createForm.resetFields();
        createForm.setFieldsValue({ roles: [], reason: "" });
        setCreateOpen(true);
    };
    const createAdministrator = async (values: CreateValues) => {
        setWorking(true);
        try {
            await authClient.stepUp(values.mfa_code);
            const result = await apiRequest<ProvisioningResult>("/v1/admin/admins", {
                method: "POST",
                body: JSON.stringify({
                    username: values.username,
                    display_name: values.display_name,
                    password: values.password,
                    roles: values.roles,
                    reason: values.reason
                })
            });
            createForm.resetFields();
            setCreateOpen(false);
            setProvisioning(result);
            await query.refetch();
            message.success(tr("\u7BA1\u7406\u5458\u5DF2\u521B\u5EFA\u3002\u8BF7\u7ACB\u5373\u4EA4\u4ED8\u4E00\u6B21\u6027 MFA \u914D\u7F6E\u3002"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const openEdit = (administrator: GovernedAdministrator) => {
        editForm.setFieldsValue({
            display_name: administrator.display_name,
            status: administrator.status,
            roles: administrator.roles,
            revoke_sessions: false,
            reason: "",
            mfa_code: ""
        });
        setEditing(administrator);
    };
    const updateAdministrator = async (values: EditValues) => {
        if (!editing)
            return;
        setWorking(true);
        try {
            await authClient.stepUp(values.mfa_code);
            await apiRequest<GovernedAdministrator>(`/v1/admin/admins/${encodeURIComponent(editing.id)}`, {
                method: "PATCH",
                body: JSON.stringify({
                    display_name: values.display_name,
                    status: values.status,
                    roles: values.roles,
                    revoke_sessions: values.revoke_sessions,
                    reason: values.reason
                })
            });
            editForm.resetFields();
            setEditing(null);
            await query.refetch();
            message.success(tr("\u7BA1\u7406\u5458\u8D44\u6599\u548C\u6743\u9650\u5206\u914D\u5DF2\u66F4\u65B0\u3002"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const resetMFA = async ({ reason }: {
        reason: string;
    }) => {
        if (!resetting)
            return;
        setWorking(true);
        try {
            const result = await apiRequest<ProvisioningResult>(`/v1/admin/admins/${encodeURIComponent(resetting.id)}/reset-mfa`, { method: "POST", body: JSON.stringify({ reason }) });
            setResetting(null);
            setProvisioning(result);
            await query.refetch();
            message.success(tr("MFA \u5DF2\u91CD\u7F6E\uFF0C\u539F\u6709\u7BA1\u7406\u5458\u4F1A\u8BDD\u5747\u5DF2\u64A4\u9500\u3002"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const copyProvisioning = async () => {
        if (!provisioning)
            return;
        const content = [
            `TOTP: ${provisioning.totp_provisioning_uri}`,
            "",
            tr("\u6062\u590D\u7801\uFF08\u6BCF\u4E2A\u4EC5\u53EF\u4F7F\u7528\u4E00\u6B21\uFF09\uFF1A"),
            ...provisioning.recovery_codes
        ].join("\n");
        await navigator.clipboard.writeText(content);
        message.success(tr("\u4E00\u6B21\u6027 MFA \u914D\u7F6E\u5DF2\u590D\u5236\u5230\u526A\u8D34\u677F\u3002"));
    };
    const columns: TableColumnsType<GovernedAdministrator> = [
        {
            title: tr("\u7BA1\u7406\u5458"),
            dataIndex: "username",
            render: (username: string, item) => (<div className="primary-cell">
          <strong>{item.display_name}</strong>
          <span>{username}</span>
        </div>)
        },
        {
            title: tr("\u72B6\u6001"),
            dataIndex: "status",
            width: 110,
            render: (status: GovernedAdministrator["status"]) => (<Tag color={status === "ACTIVE" ? "green" : "default"}>{status}</Tag>)
        },
        {
            title: tr("\u89D2\u8272"),
            dataIndex: "roles",
            render: (assignedRoles: string[]) => (<Space size={[4, 4]} wrap>
          {assignedRoles.map((role) => (<Tag color={role === "SUPER_ADMIN" ? "purple" : "blue"} key={role}>{role}</Tag>))}
        </Space>)
        },
        {
            title: "MFA",
            dataIndex: "mfa_enabled",
            width: 90,
            render: (enabled: boolean) => (<Tag color={enabled ? "green" : "red"}>{enabled ? tr("\u5DF2\u542F\u7528") : tr("\u5F02\u5E38")}</Tag>)
        },
        {
            title: tr("\u6700\u540E\u767B\u5F55"),
            dataIndex: "last_login_at",
            width: 180,
            render: (value?: string) => value ? formatTime(value) : tr("\u5C1A\u672A\u767B\u5F55")
        },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            width: 230,
            fixed: "right",
            render: (_, item) => permissions.includes("admins.update") ? (<Space size={2}>
          <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(item)}>{tr("\u7F16\u8F91")}</Button>
          <Button type="link" icon={<SafetyCertificateOutlined />} onClick={() => setResetting(item)}>{tr("\u91CD\u7F6E MFA")}</Button>
        </Space>) : null
        }
    ];
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / ADMINISTRATORS</Typography.Text>
          <Typography.Title level={2}>{tr("\u7BA1\u7406\u5458\u8D26\u53F7")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u7BA1\u7406\u5458\u8EAB\u4EFD\u4E0E\u73A9\u5BB6\u8D26\u53F7\u9694\u79BB\uFF1B\u9AD8\u98CE\u9669\u53D8\u66F4\u8981\u6C42\u6743\u9650\u3001\u64CD\u4F5C\u539F\u56E0\u548C\u77ED\u65F6 MFA \u63D0\u6743\u3002")}</Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
          {permissions.includes("admins.create") && (<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{tr("\u65B0\u5EFA\u7BA1\u7406\u5458")}</Button>)}
        </Space>
      </section>

      <Card className="table-card">
        <Table<GovernedAdministrator> rowKey="id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1050 }} locale={{ emptyText: tr("\u6682\u65E0\u7BA1\u7406\u5458\u8D26\u53F7\u3002") }}/>
      </Card>

      <Modal open={createOpen} title={tr("\u65B0\u5EFA\u7BA1\u7406\u5458")} width={680} okText={tr("\u521B\u5EFA\u5E76\u751F\u6210 MFA")} confirmLoading={working} onCancel={() => !working && setCreateOpen(false)} onOk={() => createForm.submit()} destroyOnHidden>
        <Alert showIcon type="info" message={tr("\u5BC6\u7801\u53EA\u7528\u4E8E\u672C\u6B21\u521B\u5EFA\uFF1BTOTP URI \u548C\u6062\u590D\u7801\u5C06\u5728\u6210\u529F\u540E\u663E\u793A\u4E00\u6B21\u3002")} className="action-alert"/>
        <Form form={createForm} layout="vertical" requiredMark={false} onFinish={createAdministrator}>
          <Space align="start" style={{ width: "100%" }} wrap>
            <Form.Item label={tr("\u767B\u5F55\u540D")} name="username" rules={[{ required: true, whitespace: true }, { max: 128 }]}>
              <Input autoComplete="off" maxLength={128}/>
            </Form.Item>
            <Form.Item label={tr("\u663E\u793A\u540D")} name="display_name" rules={[{ required: true, whitespace: true }, { max: 128 }]}>
              <Input maxLength={128}/>
            </Form.Item>
          </Space>
          <Form.Item label={tr("\u521D\u59CB\u5BC6\u7801")} name="password" rules={[
            { required: true, message: tr("\u8BF7\u8F93\u5165\u81F3\u5C11 12 \u4E2A\u5B57\u7B26\u7684\u521D\u59CB\u5BC6\u7801\u3002") },
            { min: 12, max: 256 }
        ]}>
            <Input.Password autoComplete="new-password"/>
          </Form.Item>
          <Form.Item label={tr("\u89D2\u8272")} name="roles" rules={[{ required: true, type: "array", min: 1 }]}>
            <Select mode="multiple" options={roleOptions} optionFilterProp="label"/>
          </Form.Item>
          <Form.Item label={tr("\u64CD\u4F5C\u539F\u56E0")} name="reason" rules={[{ required: true, whitespace: true }, { max: 500 }]}>
            <Input.TextArea rows={3} maxLength={500} showCount/>
          </Form.Item>
          <Form.Item label={tr("\u5F53\u524D\u7BA1\u7406\u5458 TOTP \u6216\u6062\u590D\u7801")} name="mfa_code" rules={[{ required: true, whitespace: true }, { min: 6, max: 32 }]}>
            <Input.Password autoComplete="one-time-code" maxLength={32}/>
          </Form.Item>
        </Form>
      </Modal>

      <Modal open={Boolean(editing)} title={tr(`编辑管理员 · ${editing?.username ?? ""}`)} width={680} okText={tr("\u786E\u8BA4\u66F4\u65B0")} confirmLoading={working} okButtonProps={{ danger: editing?.status === "ACTIVE" }} onCancel={() => !working && setEditing(null)} onOk={() => editForm.submit()} destroyOnHidden>
        <Form form={editForm} layout="vertical" requiredMark={false} onFinish={updateAdministrator}>
          <Form.Item label={tr("\u663E\u793A\u540D")} name="display_name" rules={[{ required: true, whitespace: true }, { max: 128 }]}>
            <Input maxLength={128}/>
          </Form.Item>
          <Form.Item label={tr("\u72B6\u6001")} name="status" rules={[{ required: true }]}>
            <Select options={[
            { label: tr("ACTIVE \u00B7 \u53EF\u767B\u5F55"), value: "ACTIVE" },
            { label: tr("DISABLED \u00B7 \u7981\u6B62\u767B\u5F55\u5E76\u64A4\u9500\u4F1A\u8BDD"), value: "DISABLED" }
        ]}/>
          </Form.Item>
          <Form.Item label={tr("\u89D2\u8272")} name="roles" rules={[{ required: true, type: "array", min: 1 }]}>
            <Select mode="multiple" options={roleOptions} optionFilterProp="label"/>
          </Form.Item>
          <Form.Item name="revoke_sessions" valuePropName="checked">
            <Checkbox>{tr("\u7ACB\u5373\u64A4\u9500\u8BE5\u7BA1\u7406\u5458\u7684\u5168\u90E8\u73B0\u6709\u4F1A\u8BDD")}</Checkbox>
          </Form.Item>
          <Form.Item label={tr("\u64CD\u4F5C\u539F\u56E0")} name="reason" rules={[{ required: true, whitespace: true }, { max: 500 }]}>
            <Input.TextArea rows={3} maxLength={500} showCount/>
          </Form.Item>
          <Form.Item label={tr("\u5F53\u524D\u7BA1\u7406\u5458 TOTP \u6216\u6062\u590D\u7801")} name="mfa_code" rules={[{ required: true, whitespace: true }, { min: 6, max: 32 }]}>
            <Input.Password autoComplete="one-time-code" maxLength={32}/>
          </Form.Item>
        </Form>
      </Modal>

      <OperationReasonModal open={Boolean(resetting)} title={tr(`重置 MFA · ${resetting?.username ?? ""}`)} consequence={tr("\u539F TOTP\u3001\u6062\u590D\u7801\u548C\u5168\u90E8\u767B\u5F55\u4F1A\u8BDD\u5C06\u7ACB\u5373\u5931\u6548\uFF1B\u65B0\u914D\u7F6E\u53EA\u663E\u793A\u4E00\u6B21\u3002")} confirmLabel={tr("\u91CD\u7F6E\u5E76\u64A4\u9500\u4F1A\u8BDD")} danger requireMFA loading={working} onCancel={() => setResetting(null)} onConfirm={resetMFA}/>

      <Modal open={Boolean(provisioning)} title={tr("\u4E00\u6B21\u6027 MFA \u914D\u7F6E")} width={720} okText={tr("\u6211\u5DF2\u5B89\u5168\u4FDD\u5B58")} cancelButtonProps={{ style: { display: "none" } }} onOk={() => setProvisioning(null)} onCancel={() => setProvisioning(null)} destroyOnHidden>
        {provisioning && (<div className="page-stack">
            <Alert showIcon type="warning" message={tr(`请立即交付给 ${provisioning.admin.display_name}；关闭后无法再次查看。`)}/>
            <div className="provisioning-grid">
              <QRCode value={provisioning.totp_provisioning_uri} size={190} bordered={false}/>
              <div>
                <Typography.Text strong>TOTP Provisioning URI</Typography.Text>
                <Typography.Paragraph copyable code className="secret-uri">
                  {provisioning.totp_provisioning_uri}
                </Typography.Paragraph>
              </div>
            </div>
            <div>
              <Typography.Text strong>{tr("\u4E00\u6B21\u6027\u6062\u590D\u7801")}</Typography.Text>
              <div className="recovery-code-grid">
                {provisioning.recovery_codes.map((code) => <code key={code}>{code}</code>)}
              </div>
            </div>
            <Button icon={<CopyOutlined />} onClick={copyProvisioning}>{tr("\u590D\u5236\u5B8C\u6574\u914D\u7F6E")}</Button>
          </div>)}
      </Modal>
    </div>);
}
function formatTime(value: string) {
    return new Date(value).toLocaleString(localeTag(), { hour12: false });
}
function errorMessage(error: unknown) {
    return error instanceof ApiError ? error.message : tr("\u7BA1\u7406\u5458\u6CBB\u7406\u8BF7\u6C42\u5931\u8D25\u3002");
}
