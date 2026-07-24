import {Alert, Form, Input, Modal} from "antd";
import {useEffect, useState} from "react";
import {ApiError, authClient} from "../api/client";

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

export function OperationReasonModal({
  open,
  title,
  consequence,
  confirmLabel,
  danger = false,
  requireMFA = false,
  loading = false,
  onCancel,
  onConfirm
}: Props) {
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
      } catch (error) {
        const message = error instanceof ApiError ? error.message : "二次 MFA 校验失败。";
        form.setFields([{name: "mfa_code", errors: [message]}]);
        return;
      } finally {
        setVerifying(false);
      }
    }
    await onConfirm({reason: values.reason});
  };

  return (
    <Modal
      open={open}
      title={title}
      okText={confirmLabel}
      cancelText="取消"
      confirmLoading={loading || verifying}
      okButtonProps={{danger}}
      onCancel={() => !loading && onCancel()}
      onOk={() => form.submit()}
      destroyOnHidden
    >
      <Alert
        type={danger ? "warning" : "info"}
        showIcon
        message={consequence}
        className="action-alert"
      />
      <Form form={form} layout="vertical" requiredMark={false} onFinish={submit}>
        <Form.Item
          label="操作原因"
          name="reason"
          rules={[
            {required: true, whitespace: true, message: "请填写可供审计追溯的操作原因。"},
            {max: 500, message: "操作原因不能超过 500 个字符。"}
          ]}
        >
          <Input.TextArea
            rows={4}
            maxLength={500}
            showCount
            placeholder="例如：值班工单 OPS-4812，节点心跳异常需要进入维护"
          />
        </Form.Item>
        {requireMFA && (
          <Form.Item
            label="TOTP 或恢复码"
            name="mfa_code"
            extra="二次校验结果仅在内存中保存数分钟，并绑定当前管理员 Session。"
            rules={[
              {required: true, whitespace: true, message: "请输入当前 TOTP 或一次性恢复码。"},
              {min: 6, max: 32}
            ]}
          >
            <Input.Password
              autoComplete="one-time-code"
              maxLength={32}
              placeholder="6 位动态验证码"
            />
          </Form.Item>
        )}
      </Form>
    </Modal>
  );
}
