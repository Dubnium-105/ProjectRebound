import { tr } from "../i18n";
import { EditOutlined, LockOutlined, ReloadOutlined, SafetyOutlined } from "@ant-design/icons";
import { Alert, App, Button, Card, Checkbox, Collapse, Form, Input, Modal, Space, Spin, Tag, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import type { AdminPermissionDefinition, GovernedRole } from "../types";
type RoleCatalog = {
    items: GovernedRole[];
    permissions: AdminPermissionDefinition[];
};
type RoleEditValues = {
    permissions: string[];
    reason: string;
    mfa_code: string;
};
export function RolesPage() {
    const { message } = App.useApp();
    const [form] = Form.useForm<RoleEditValues>();
    const [catalog, setCatalog] = useState<RoleCatalog>({ items: [], permissions: [] });
    const [editing, setEditing] = useState<GovernedRole | null>(null);
    const [loading, setLoading] = useState(true);
    const [working, setWorking] = useState(false);
    const canManage = authClient.permissions().includes("roles.manage");
    const load = async () => {
        setLoading(true);
        try {
            setCatalog(await apiRequest<RoleCatalog>("/v1/admin/roles"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setLoading(false);
        }
    };
    useEffect(() => {
        void load();
    }, []);
    const groupedPermissions = useMemo(() => {
        const groups = new Map<string, AdminPermissionDefinition[]>();
        for (const permission of catalog.permissions) {
            const items = groups.get(permission.resource) ?? [];
            items.push(permission);
            groups.set(permission.resource, items);
        }
        return [...groups.entries()].sort(([left], [right]) => left.localeCompare(right));
    }, [catalog.permissions]);
    const openEdit = (role: GovernedRole) => {
        form.setFieldsValue({
            permissions: role.permissions,
            reason: "",
            mfa_code: ""
        });
        setEditing(role);
    };
    const updateRole = async (values: RoleEditValues) => {
        if (!editing)
            return;
        setWorking(true);
        try {
            await authClient.stepUp(values.mfa_code);
            const updated = await apiRequest<GovernedRole>(`/v1/admin/roles/${encodeURIComponent(editing.id)}`, {
                method: "PATCH",
                body: JSON.stringify({
                    permissions: values.permissions,
                    reason: values.reason
                })
            });
            setCatalog((current) => ({
                ...current,
                items: current.items.map((item) => item.id === updated.id ? updated : item)
            }));
            form.resetFields();
            setEditing(null);
            message.success(tr("\u89D2\u8272\u6743\u9650\u96C6\u5DF2\u539F\u5B50\u66F4\u65B0\u3002"));
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
          <Typography.Text className="eyebrow">SECURITY / RBAC</Typography.Text>
          <Typography.Title level={2}>{tr("\u89D2\u8272\u4E0E\u6743\u9650")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u6743\u9650\u7531\u540E\u7AEF\u76EE\u5F55\u7EDF\u4E00\u5B9A\u4E49\uFF1B\u89D2\u8272\u53D8\u66F4\u4F1A\u88AB\u8BB0\u5F55\u5230\u5BA1\u8BA1\u65E5\u5FD7\uFF0C\u5E76\u8981\u6C42\u5F53\u524D\u7BA1\u7406\u5458\u518D\u6B21\u5B8C\u6210 MFA\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>{tr("\u5237\u65B0")}</Button>
      </section>

      <Alert showIcon type="info" message={tr("SUPER_ADMIN \u59CB\u7EC8\u62E5\u6709\u5168\u90E8\u6743\u9650\u4E14\u4E0D\u53EF\u4FEE\u6539\uFF0C\u907F\u514D\u51FA\u73B0\u6743\u9650\u76EE\u5F55\u4E0E\u5B9E\u9645\u6388\u6743\u6F02\u79FB\u3002")}/>

      {loading ? (<Card><Spin /></Card>) : (<div className="role-card-grid">
          {catalog.items.map((role) => (<Card key={role.id} className="metric-card" title={<Space>
                  {role.name === "SUPER_ADMIN" ? <LockOutlined /> : <SafetyOutlined />}
                  <span>{role.display_name}</span>
                </Space>} extra={canManage && role.name !== "SUPER_ADMIN" ? (<Button type="link" icon={<EditOutlined />} onClick={() => openEdit(role)}>{tr("\u7F16\u8F91\u6743\u9650")}</Button>) : null}>
              <Typography.Paragraph type="secondary">{role.description}</Typography.Paragraph>
              <Space size={[4, 4]} wrap>
                <Tag color={role.system_role ? "purple" : "blue"}>{role.name}</Tag>
                <Tag>{role.permissions.length}{tr("\u9879\u6743\u9650")}</Tag>
              </Space>
              <Collapse ghost size="small" items={[{
                        key: "permissions",
                        label: tr("\u67E5\u770B\u6743\u9650\u660E\u7EC6"),
                        children: (<Space size={[4, 6]} wrap>
                      {role.permissions.map((permission) => (<Tag key={permission}>{permission}</Tag>))}
                    </Space>)
                    }]}/>
            </Card>))}
        </div>)}

      <Modal open={Boolean(editing)} title={tr(`编辑角色 · ${editing?.display_name ?? ""}`)} width={900} okText={tr("\u786E\u8BA4\u66FF\u6362\u6743\u9650\u96C6")} confirmLoading={working} onCancel={() => !working && setEditing(null)} onOk={() => form.submit()} destroyOnHidden>
        <Alert showIcon type="warning" message={tr("\u4FDD\u5B58\u540E\uFF0C\u6240\u6709\u62E5\u6709\u8BE5\u89D2\u8272\u7684\u7BA1\u7406\u5458\u4F1A\u5728\u4E0B\u4E00\u6B21\u4F1A\u8BDD\u6821\u9A8C\u65F6\u4F7F\u7528\u65B0\u6743\u9650\u3002")} className="action-alert"/>
        <Form form={form} layout="vertical" requiredMark={false} onFinish={updateRole}>
          <Form.Item label={tr("\u6743\u9650")} name="permissions">
            <Checkbox.Group className="permission-catalog">
              {groupedPermissions.map(([resource, permissions]) => (<Card key={resource} size="small" title={resource}>
                  <div className="permission-list">
                    {permissions.map((permission) => (<Checkbox key={permission.key} value={permission.key}>
                        <span className="permission-copy">
                          <span>
                            <code>{permission.key}</code>
                            <Tag color={riskColor[permission.risk_level]}>{permission.risk_level}</Tag>
                          </span>
                          <small>{permission.description}</small>
                        </span>
                      </Checkbox>))}
                  </div>
                </Card>))}
            </Checkbox.Group>
          </Form.Item>
          <Form.Item label={tr("\u64CD\u4F5C\u539F\u56E0")} name="reason" rules={[{ required: true, whitespace: true }, { max: 500 }]}>
            <Input.TextArea rows={3} maxLength={500} showCount/>
          </Form.Item>
          <Form.Item label={tr("TOTP \u6216\u6062\u590D\u7801")} name="mfa_code" extra={tr("MFA \u63D0\u6743\u51ED\u8BC1\u53EA\u4FDD\u5B58\u5728\u5F53\u524D\u6D4F\u89C8\u5668\u5185\u5B58\u4E2D\uFF0C\u5E76\u7ED1\u5B9A\u5F53\u524D Session\u3002")} rules={[{ required: true, whitespace: true }, { min: 6, max: 32 }]}>
            <Input.Password autoComplete="one-time-code" maxLength={32}/>
          </Form.Item>
        </Form>
      </Modal>
    </div>);
}
const riskColor: Record<AdminPermissionDefinition["risk_level"], string> = {
    LOW: "default",
    MEDIUM: "blue",
    HIGH: "orange",
    CRITICAL: "red"
};
function errorMessage(error: unknown) {
    return error instanceof ApiError ? error.message : tr("\u89D2\u8272\u6743\u9650\u8BF7\u6C42\u5931\u8D25\u3002");
}
