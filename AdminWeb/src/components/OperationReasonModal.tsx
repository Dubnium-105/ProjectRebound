import { tr } from "../i18n";
import { Alert, Form, Input, Modal } from "antd";
import { useEffect, useState } from "react";
import { ApiError, authClient } from "../api/client";
export type OperationReasonValues = {
    reason: string;
    mfa_code?: string;
};
type Props = {
    open: boolean;
    title: string;
    consequence: string;
    confirmLabel: string;
    danger?: boolean;
    requireMFA?: boolean;
    loading?: boolean;
    onCancel: () => void;
    onConfirm: (values: OperationReasonValues) => void | Promise<void>;
};
export function OperationReasonModal({ open, title, consequence, confirmLabel, danger = false, requireMFA = false, loading = false, onCancel, onConfirm }: Props) {
    const [form] = Form.useForm<OperationReasonValues>();
    const [verifying, setVerifying] = useState(false);
    useEffect(() => {
        if (open) {
            form.resetFields();
        }
    }, [form, open]);
    const submit = async (values: OperationReasonValues) => {
        if (requireMFA) {
            setVerifying(true);
            try {
                await authClient.stepUp(values.mfa_code ?? "");
            }
            catch (error) {
                const message = error instanceof ApiError ? error.message : tr("\u4E8C\u6B21 MFA \u6821\u9A8C\u5931\u8D25\u3002");
                form.setFields([{ name: "mfa_code", errors: [message] }]);
                return;
            }
            finally {
                setVerifying(false);
            }
        }
        await onConfirm({ reason: values.reason });
    };
    return (<Modal open={open} title={title} okText={confirmLabel} cancelText={tr("\u53D6\u6D88")} confirmLoading={loading || verifying} okButtonProps={{ danger }} onCancel={() => !loading && onCancel()} onOk={() => form.submit()} destroyOnHidden>
      <Alert type={danger ? "warning" : "info"} showIcon message={consequence} className="action-alert"/>
      <Form form={form} layout="vertical" requiredMark={false} onFinish={submit}>
        <Form.Item label={tr("\u64CD\u4F5C\u539F\u56E0")} name="reason" rules={[
            { required: true, whitespace: true, message: tr("\u8BF7\u586B\u5199\u53EF\u4F9B\u5BA1\u8BA1\u8FFD\u6EAF\u7684\u64CD\u4F5C\u539F\u56E0\u3002") },
            { max: 500, message: tr("\u64CD\u4F5C\u539F\u56E0\u4E0D\u80FD\u8D85\u8FC7 500 \u4E2A\u5B57\u7B26\u3002") }
        ]}>
          <Input.TextArea rows={4} maxLength={500} showCount placeholder={tr("\u4F8B\u5982\uFF1A\u503C\u73ED\u5DE5\u5355 OPS-4812\uFF0C\u8282\u70B9\u5FC3\u8DF3\u5F02\u5E38\u9700\u8981\u8FDB\u5165\u7EF4\u62A4")}/>
        </Form.Item>
        {requireMFA && (<Form.Item label={tr("TOTP \u6216\u6062\u590D\u7801")} name="mfa_code" extra={tr("\u4E8C\u6B21\u6821\u9A8C\u7ED3\u679C\u4EC5\u5728\u5185\u5B58\u4E2D\u4FDD\u5B58\u6570\u5206\u949F\uFF0C\u5E76\u7ED1\u5B9A\u5F53\u524D\u7BA1\u7406\u5458 Session\u3002")} rules={[
                { required: true, whitespace: true, message: tr("\u8BF7\u8F93\u5165\u5F53\u524D TOTP \u6216\u4E00\u6B21\u6027\u6062\u590D\u7801\u3002") },
                { min: 6, max: 32 }
            ]}>
            <Input.Password autoComplete="one-time-code" maxLength={32} placeholder={tr("6 \u4F4D\u52A8\u6001\u9A8C\u8BC1\u7801")}/>
          </Form.Item>)}
      </Form>
    </Modal>);
}
