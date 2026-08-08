import { localeTag, tr } from "../i18n";
import { CloudUploadOutlined, EditOutlined, FolderAddOutlined, InboxOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import {
    Alert, App, Button, Card, Form, Input, InputNumber, Modal, Progress,
    Select, Space, Switch, Table, Tag, Typography, Upload, type FormInstance, type TableColumnsType, type UploadFile
} from "antd";
import { useEffect, useMemo, useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import { putObject, retry, runConcurrent, type SignedRequest } from "./download-upload";

type LocalizedText = { en: string; zh_cn: string };
type DownloadCategory = {
    id: string;
    slug: string;
    title: LocalizedText;
    description: LocalizedText;
    sort_order: number;
    enabled: boolean;
    status: "ACTIVE" | "ARCHIVED";
};
type DownloadVersion = {
    id: string;
    entry_id: string;
    version_label: string;
    original_file_name: string;
    content_type: string;
    size_bytes: number;
    sha256: string;
    status: "UPLOADING" | "VERIFYING" | "DRAFT" | "PUBLISHED" | "ARCHIVED" | "FAILED";
    failure_reason?: string;
    created_at: string;
    published_at?: string;
};
type DownloadEntry = {
    id: string;
    category_id: string;
    category_slug: string;
    slug: string;
    title: LocalizedText;
    description: LocalizedText;
    sort_order: number;
    status: "ACTIVE" | "ARCHIVED";
    versions: DownloadVersion[];
};
type UploadSession = {
    id: string;
    version_id: string;
    strategy: "SINGLE" | "MULTIPART";
    part_size_bytes: number;
    status: "ACTIVE" | "COMPLETED" | "ABORTED" | "EXPIRED";
    expires_at: string;
    version: DownloadVersion;
    uploaded_parts: { part_number: number; etag: string; size_bytes?: number }[];
};
type UploadCreated = { session: UploadSession; request?: SignedRequest };
type DownloadCapabilities = {
    enabled: boolean;
    max_file_bytes: number;
    allowed_extensions: string[];
    multipart_threshold_bytes: number;
    part_size_bytes: number;
    upload_session_ttl_hours: number;
    presigned_request_ttl_minutes: number;
};

type CategoryValues = {
    slug: string;
    title_en: string;
    title_zh_cn: string;
    description_en: string;
    description_zh_cn: string;
    sort_order: number;
    enabled?: boolean;
    reason: string;
};
type EntryValues = CategoryValues & { category_id: string };
type UploadValues = { version_label: string; file_list: UploadFile[]; reason: string };
type Operation =
    | { kind: "publish-version"; target: DownloadVersion }
    | { kind: "archive-version"; target: DownloadVersion }
    | { kind: "archive-entry"; target: DownloadEntry }
    | { kind: "archive-category"; target: DownloadCategory };

const resumeStorageKey = "project-rebound-download-upload";

export function DownloadsPage() {
    const { message } = App.useApp();
    const permissions = authClient.permissions();
    const [categoryForm] = Form.useForm<CategoryValues>();
    const [entryForm] = Form.useForm<EntryValues>();
    const [uploadForm] = Form.useForm<UploadValues>();
    const [categories, setCategories] = useState<DownloadCategory[]>([]);
    const [capabilities, setCapabilities] = useState<DownloadCapabilities>({
        enabled: false, max_file_bytes: 2 * 1024 ** 3, allowed_extensions: [],
        multipart_threshold_bytes: 64 * 1024 ** 2, part_size_bytes: 16 * 1024 ** 2,
        upload_session_ttl_hours: 24, presigned_request_ttl_minutes: 15
    });
    const [categoryOpen, setCategoryOpen] = useState(false);
    const [editingCategory, setEditingCategory] = useState<DownloadCategory | null>(null);
    const [entryOpen, setEntryOpen] = useState(false);
    const [editingEntry, setEditingEntry] = useState<DownloadEntry | null>(null);
    const [uploadEntry, setUploadEntry] = useState<DownloadEntry | null>(null);
    const [operation, setOperation] = useState<Operation | null>(null);
    const [working, setWorking] = useState(false);
    const [uploadPhase, setUploadPhase] = useState("");
    const [progress, setProgress] = useState(0);
    const [categoryFilter, setCategoryFilter] = useState<string>();
    const [statusFilter, setStatusFilter] = useState<string>();
    const { query, result } = useList<DownloadEntry>({ resource: "downloads", pagination: { pageSize: 100 } });

    const loadSupportingData = async () => {
        const [categoryResult, capabilityResult] = await Promise.all([
            apiRequest<{ items: DownloadCategory[] }>("/v1/admin/download-categories"),
            apiRequest<DownloadCapabilities>("/v1/admin/downloads/capabilities")
        ]);
        setCategories(categoryResult.items);
        setCapabilities(capabilityResult);
    };
    useEffect(() => {
        loadSupportingData().catch((error) => message.error(errorMessage(error)));
    }, [message]);

    const categoryOptions = useMemo(() => categories
        .filter((item) => item.status === "ACTIVE")
        .map((item) => ({ label: `${localized(item.title)} (${item.slug})`, value: item.id })), [categories]);
    const filteredEntries = useMemo(() => (result.data ?? []).filter((item) => {
        if (categoryFilter && item.category_id !== categoryFilter) return false;
        if (!statusFilter) return true;
        if (item.status === statusFilter) return true;
        return item.versions.some((version) => version.status === statusFilter);
    }), [result.data, categoryFilter, statusFilter]);

    const openCategory = (item?: DownloadCategory) => {
        setEditingCategory(item ?? null);
        categoryForm.setFieldsValue(item ? {
            slug: item.slug,
            title_en: item.title.en,
            title_zh_cn: item.title.zh_cn,
            description_en: item.description.en,
            description_zh_cn: item.description.zh_cn,
            sort_order: item.sort_order,
            enabled: item.enabled,
            reason: ""
        } : { slug: "", title_en: "", title_zh_cn: "", description_en: "", description_zh_cn: "", sort_order: 0, enabled: true, reason: "" });
        setCategoryOpen(true);
    };

    const saveCategory = async (values: CategoryValues) => {
        setWorking(true);
        try {
            await apiRequest(editingCategory
                ? `/v1/admin/download-categories/${encodeURIComponent(editingCategory.id)}`
                : "/v1/admin/download-categories", {
                method: editingCategory ? "PATCH" : "POST",
                body: JSON.stringify(values)
            });
            setCategoryOpen(false);
            await loadSupportingData();
            message.success(tr("下载分类已保存。"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };

    const openEntry = (item?: DownloadEntry) => {
        setEditingEntry(item ?? null);
        entryForm.setFieldsValue(item ? {
            category_id: item.category_id,
            slug: item.slug,
            title_en: item.title.en,
            title_zh_cn: item.title.zh_cn,
            description_en: item.description.en,
            description_zh_cn: item.description.zh_cn,
            sort_order: item.sort_order,
            reason: ""
        } : {
            category_id: categoryOptions[0]?.value,
            slug: "", title_en: "", title_zh_cn: "", description_en: "", description_zh_cn: "", sort_order: 0, reason: ""
        });
        setEntryOpen(true);
    };

    const saveEntry = async (values: EntryValues) => {
        setWorking(true);
        try {
            await apiRequest(editingEntry
                ? `/v1/admin/downloads/${encodeURIComponent(editingEntry.id)}`
                : "/v1/admin/downloads", {
                method: editingEntry ? "PATCH" : "POST",
                body: JSON.stringify(values)
            });
            setEntryOpen(false);
            await query.refetch();
            message.success(tr("下载项目已保存。"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };

    const openUpload = (entry: DownloadEntry) => {
        uploadForm.resetFields();
        setUploadPhase("");
        setProgress(0);
        setUploadEntry(entry);
    };

    const startUpload = async (values: UploadValues) => {
        const file = values.file_list?.[0]?.originFileObj;
        if (!uploadEntry || !file) {
            message.error(tr("请选择要上传的文件。"));
            return;
        }
        if (file.size > capabilities.max_file_bytes) {
            message.error(tr(`文件超过 ${formatBytes(capabilities.max_file_bytes)} 上限。`));
            return;
        }
        setWorking(true);
        try {
            setUploadPhase(tr("正在计算 SHA-256；此过程不会将文件发送到服务器。"));
            const sha256 = await hashFile(file, (loaded) => setProgress(Math.round(loaded / file.size * 25)));
            let created = await resumeOrCreateUpload(uploadEntry, values, file, sha256);
            let session = created.session;
            if (session.status === "ACTIVE") {
                setUploadPhase(tr("正在上传到对象存储。"));
                await transferFile(created, file, (loaded) => setProgress(25 + Math.round(loaded / file.size * 65)));
                setUploadPhase(tr("上传已完成，正在等待服务端复核大小和 SHA-256。"));
                session = await apiRequest<UploadSession>(`/v1/admin/download-uploads/${encodeURIComponent(created.session.id)}/complete`, {
                    method: "POST", body: JSON.stringify({ reason: values.reason })
                });
            }
            else {
                setUploadPhase(tr("已恢复上传会话，正在等待服务端复核。"));
            }
            const verified = await pollVerification(session.id, (phase) => setUploadPhase(phase));
            localStorage.removeItem(resumeStorageKey);
            if (verified.version.status === "FAILED") {
                throw new Error(verified.version.failure_reason || tr("服务端文件校验失败。"));
            }
            setProgress(100);
            setUploadPhase(tr("文件校验通过，版本已保存为 DRAFT。"));
            await query.refetch();
            message.success(tr("文件上传并校验成功。"));
            setTimeout(() => setUploadEntry(null), 500);
        }
        catch (error) {
            message.error(errorMessage(error));
            setUploadPhase(tr("上传尚未完成；重新选择同一文件后可继续已完成的分片。"));
        }
        finally {
            setWorking(false);
        }
    };

    const resumeOrCreateUpload = async (entry: DownloadEntry, values: UploadValues, file: File, sha256: string) => {
        const saved = readResumeState();
        if (saved && saved.entryId === entry.id && saved.fileName === file.name && saved.size === file.size &&
            saved.sha256 === sha256 && saved.versionLabel === values.version_label) {
            try {
                const session = await apiRequest<UploadSession>(`/v1/admin/download-uploads/${encodeURIComponent(saved.uploadId)}`);
                if (session.status === "ACTIVE" || (session.status === "COMPLETED" &&
                    ["VERIFYING", "DRAFT", "FAILED"].includes(session.version.status))) {
                    return { session } as UploadCreated;
                }
            }
            catch {
                localStorage.removeItem(resumeStorageKey);
            }
        }
        const created = await apiRequest<UploadCreated>(`/v1/admin/downloads/${encodeURIComponent(entry.id)}/uploads`, {
            method: "POST",
            body: JSON.stringify({
                version_label: values.version_label,
                file_name: file.name,
                content_type: file.type || "application/octet-stream",
                size_bytes: file.size,
                sha256,
                reason: values.reason
            })
        });
        localStorage.setItem(resumeStorageKey, JSON.stringify({
            uploadId: created.session.id, entryId: entry.id, fileName: file.name,
            size: file.size, sha256, versionLabel: values.version_label
        }));
        return created;
    };

    const transferFile = async (created: UploadCreated, file: File, report: (loaded: number) => void) => {
        if (created.session.strategy === "SINGLE") {
            const resumed = !created.request;
            let request = created.request;
            if (!request) {
                const signed = await apiRequest<{ items: { part_number: number; request: SignedRequest }[] }>(
                    `/v1/admin/download-uploads/${encodeURIComponent(created.session.id)}/parts`, {
                        method: "POST", body: JSON.stringify({ part_numbers: [1] })
                    });
                request = signed.items[0]?.request;
            }
            if (!request) {
                throw new Error(tr("无法续签单文件上传请求。"));
            }
            await retry(() => putObject(request!, file, resumed), 3);
            report(file.size);
            return;
        }
        const current = await apiRequest<UploadSession>(`/v1/admin/download-uploads/${encodeURIComponent(created.session.id)}`);
        const completed = new Set(current.uploaded_parts.map((item) => item.part_number));
        let loaded = current.uploaded_parts.reduce((sum, item) => sum + (item.size_bytes ?? 0), 0);
        report(loaded);
        const totalParts = Math.ceil(file.size / current.part_size_bytes);
        const pending = Array.from({ length: totalParts }, (_, index) => index + 1).filter((number) => !completed.has(number));
        for (let offset = 0; offset < pending.length; offset += 20) {
            const numbers = pending.slice(offset, offset + 20);
            const signed = await apiRequest<{ items: { part_number: number; request: SignedRequest }[] }>(
                `/v1/admin/download-uploads/${encodeURIComponent(current.id)}/parts`, {
                    method: "POST", body: JSON.stringify({ part_numbers: numbers })
                });
            await runConcurrent(signed.items, 4, async (part) => {
                const start = (part.part_number - 1) * current.part_size_bytes;
                const body = file.slice(start, Math.min(file.size, start + current.part_size_bytes));
                await retry(() => putObject(part.request, body), 3);
                loaded += body.size;
                report(loaded);
            });
        }
    };

    const confirmOperation = async ({ reason }: { reason: string }) => {
        if (!operation) return;
        setWorking(true);
        try {
            let path = "";
            if (operation.kind === "publish-version") path = `/v1/admin/download-versions/${encodeURIComponent(operation.target.id)}/publish`;
            if (operation.kind === "archive-version") path = `/v1/admin/download-versions/${encodeURIComponent(operation.target.id)}/archive`;
            if (operation.kind === "archive-entry") path = `/v1/admin/downloads/${encodeURIComponent(operation.target.id)}/archive`;
            if (operation.kind === "archive-category") path = `/v1/admin/download-categories/${encodeURIComponent(operation.target.id)}/archive`;
            await apiRequest(path, { method: "POST", body: JSON.stringify({ reason }) });
            setOperation(null);
            await Promise.all([query.refetch(), loadSupportingData()]);
            message.success(tr("下载目录状态已更新。"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };

    const abortSavedUpload = async () => {
        const saved = readResumeState();
        const reason = String(uploadForm.getFieldValue("reason") ?? "").trim();
        if (!saved || saved.entryId !== uploadEntry?.id) return;
        if (!reason) {
            message.error(tr("请先填写操作原因，再终止上传会话。"));
            return;
        }
        setWorking(true);
        try {
            await apiRequest(`/v1/admin/download-uploads/${encodeURIComponent(saved.uploadId)}/abort`, {
                method: "POST", body: JSON.stringify({ reason })
            });
            localStorage.removeItem(resumeStorageKey);
            setUploadPhase(tr("已终止并清理保存的上传会话。"));
            setProgress(0);
            await query.refetch();
            message.success(tr("上传会话已终止。"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };

    const versionColumns: TableColumnsType<DownloadVersion> = [
        { title: tr("版本"), dataIndex: "version_label", render: (value: string) => <Typography.Text code>{value}</Typography.Text> },
        { title: tr("文件"), dataIndex: "original_file_name" },
        { title: tr("大小"), dataIndex: "size_bytes", width: 110, render: formatBytes },
        { title: "SHA-256", dataIndex: "sha256", width: 150, render: (value: string) => <Typography.Text copyable={{ text: value }} code>{value.slice(0, 12)}…</Typography.Text> },
        { title: tr("状态"), dataIndex: "status", width: 120, render: (value: DownloadVersion["status"]) => <Tag color={statusColor(value)}>{value}</Tag> },
        {
            title: tr("操作"), key: "actions", width: 180, render: (_, version) => <Space size={2}>
                {version.status === "DRAFT" && permissions.includes("downloads.publish") && <Button type="link" onClick={() => setOperation({ kind: "publish-version", target: version })}>{tr("发布")}</Button>}
                {["DRAFT", "PUBLISHED", "FAILED"].includes(version.status) && permissions.includes("downloads.archive") && <Button type="link" danger onClick={() => setOperation({ kind: "archive-version", target: version })}>{tr("归档")}</Button>}
            </Space>
        }
    ];
    const columns: TableColumnsType<DownloadEntry> = [
        {
            title: tr("下载项目"), dataIndex: "title", render: (_: LocalizedText, item) => <div className="primary-cell">
                <strong>{localized(item.title)}</strong><span>{item.slug}</span>
            </div>
        },
        { title: tr("分类"), dataIndex: "category_slug", width: 150, render: (value: string) => <Tag>{value}</Tag> },
        { title: tr("版本数"), dataIndex: "versions", width: 100, render: (items: DownloadVersion[]) => items.length },
        { title: tr("状态"), dataIndex: "status", width: 110, render: (value: string) => <Tag color={value === "ACTIVE" ? "green" : "default"}>{value}</Tag> },
        {
            title: tr("操作"), key: "actions", width: 300, render: (_, item) => <Space size={2}>
                {permissions.includes("downloads.update") && item.status === "ACTIVE" && <Button type="link" icon={<EditOutlined />} onClick={() => openEntry(item)}>{tr("编辑")}</Button>}
                {permissions.includes("downloads.create") && item.status === "ACTIVE" && <Button type="link" icon={<CloudUploadOutlined />} disabled={!capabilities.enabled} onClick={() => openUpload(item)}>{tr("上传版本")}</Button>}
                {permissions.includes("downloads.archive") && item.status === "ACTIVE" && <Button type="link" danger onClick={() => setOperation({ kind: "archive-entry", target: item })}>{tr("归档项目")}</Button>}
            </Space>
        }
    ];

    return <div className="page-stack">
        <section className="page-heading">
            <div>
                <Typography.Text className="eyebrow">CONTENT / DOWNLOADS</Typography.Text>
                <Typography.Title level={2}>{tr("下载管理")}</Typography.Title>
                <Typography.Paragraph type="secondary">{tr("维护官网公开下载分类、双语说明和经过完整性校验的历史版本。")}</Typography.Paragraph>
            </div>
            <Space wrap>
                <Button icon={<ReloadOutlined />} loading={query.isFetching} onClick={() => Promise.all([query.refetch(), loadSupportingData()])}>{tr("刷新")}</Button>
                {permissions.includes("downloads.create") && <Button icon={<FolderAddOutlined />} onClick={() => openCategory()}>{tr("新建分类")}</Button>}
                {permissions.includes("downloads.create") && <Button type="primary" icon={<PlusOutlined />} disabled={!categoryOptions.length} onClick={() => openEntry()}>{tr("新建下载项目")}</Button>}
            </Space>
        </section>

        {!capabilities.enabled && <Alert type="warning" showIcon message={tr("对象存储尚未启用；可以维护目录元数据，但不能创建上传会话。")} />}
        {capabilities.enabled && <Alert type="info" showIcon message={tr(`允许 ${capabilities.allowed_extensions.map((value) => `.${value}`).join("、")}；单文件最大 ${formatBytes(capabilities.max_file_bytes)}，超过 ${formatBytes(capabilities.multipart_threshold_bytes)} 自动分片。`)} />}

        <Card title={tr("自定义分类")} className="table-card">
            <Table<DownloadCategory> rowKey="id" size="small" pagination={false} dataSource={categories} columns={[
                { title: tr("分类"), render: (_, item) => <div className="primary-cell"><strong>{localized(item.title)}</strong><span>{item.slug}</span></div> },
                { title: tr("排序"), dataIndex: "sort_order", width: 90 },
                { title: tr("启用"), dataIndex: "enabled", width: 90, render: (value: boolean) => <Tag color={value ? "green" : "default"}>{value ? tr("是") : tr("否")}</Tag> },
                { title: tr("状态"), dataIndex: "status", width: 110, render: (value) => <Tag color={value === "ACTIVE" ? "green" : "default"}>{value}</Tag> },
                { title: tr("操作"), width: 180, render: (_, item) => <Space size={2}>
                    {permissions.includes("downloads.update") && item.status === "ACTIVE" && <Button type="link" onClick={() => openCategory(item)}>{tr("编辑")}</Button>}
                    {permissions.includes("downloads.archive") && item.status === "ACTIVE" && <Button type="link" danger onClick={() => setOperation({ kind: "archive-category", target: item })}>{tr("归档")}</Button>}
                </Space> }
            ]} />
        </Card>

        <Card title={tr("下载项目与版本")} className="table-card">
            <Space wrap style={{ marginBottom: 16 }}>
                <Select allowClear style={{ minWidth: 220 }} placeholder={tr("按分类筛选")} value={categoryFilter}
                    onChange={setCategoryFilter} options={categories.map((item) => ({ label: localized(item.title), value: item.id }))} />
                <Select allowClear style={{ minWidth: 180 }} placeholder={tr("按状态筛选")} value={statusFilter}
                    onChange={setStatusFilter} options={["ACTIVE", "ARCHIVED", "UPLOADING", "VERIFYING", "DRAFT", "PUBLISHED", "FAILED"].map((value) => ({ label: value, value }))} />
            </Space>
            <Table<DownloadEntry> rowKey="id" columns={columns} dataSource={filteredEntries} loading={query.isLoading} pagination={false}
                expandable={{ expandedRowRender: (item) => <Table<DownloadVersion> rowKey="id" size="small" pagination={false} columns={versionColumns} dataSource={item.versions} /> }} />
        </Card>

        <Modal open={categoryOpen} title={editingCategory ? tr("编辑下载分类") : tr("新建下载分类")} width={760} confirmLoading={working}
            onCancel={() => !working && setCategoryOpen(false)} onOk={() => categoryForm.submit()} destroyOnHidden>
            <MetadataForm form={categoryForm} onFinish={saveCategory} includeCategory={false} immutableSlug={Boolean(editingCategory)} categoryOptions={categoryOptions} />
        </Modal>

        <Modal open={entryOpen} title={editingEntry ? tr("编辑下载项目") : tr("新建下载项目")} width={760} confirmLoading={working}
            onCancel={() => !working && setEntryOpen(false)} onOk={() => entryForm.submit()} destroyOnHidden>
            <MetadataForm form={entryForm} onFinish={saveEntry} includeCategory immutableSlug={Boolean(editingEntry)} categoryOptions={categoryOptions} />
        </Modal>

        <Modal open={Boolean(uploadEntry)} title={tr(`上传版本 · ${uploadEntry ? localized(uploadEntry.title) : ""}`)} width={720}
            okText={tr("计算校验值并上传")} confirmLoading={working} closable={!working} maskClosable={!working}
            onCancel={() => !working && setUploadEntry(null)} onOk={() => uploadForm.submit()} destroyOnHidden>
            <Form form={uploadForm} layout="vertical" requiredMark={false} onFinish={startUpload}>
                <Form.Item label={tr("版本标签")} name="version_label" rules={[{ required: true, whitespace: true }, { max: 64 }]}>
                    <Input maxLength={64} placeholder="1.0.0 / 2026-08-08" />
                </Form.Item>
                <Form.Item label={tr("文件")} name="file_list" valuePropName="fileList" getValueFromEvent={(event) => event?.fileList}
                    rules={[{ required: true, type: "array", min: 1 }]}>
                    <Upload.Dragger maxCount={1} beforeUpload={() => false} disabled={working}
                        accept={capabilities.allowed_extensions.map((value) => `.${value.replace(/^\./, "")}`).join(",")}>
                        <p className="ant-upload-drag-icon"><InboxOutlined /></p>
                        <p>{tr(`点击或拖拽文件到此区域；最大 ${formatBytes(capabilities.max_file_bytes)}。`)}</p>
                    </Upload.Dragger>
                </Form.Item>
                <Form.Item label={tr("操作原因")} name="reason" rules={[{ required: true, whitespace: true }, { max: 500 }]}>
                    <Input.TextArea rows={3} maxLength={500} showCount />
                </Form.Item>
                {permissions.includes("downloads.create") && readResumeState()?.entryId === uploadEntry?.id && <Button danger disabled={working} onClick={abortSavedUpload}>{tr("终止已保存的上传会话")}</Button>}
                {uploadPhase && <Card size="small"><Space direction="vertical" style={{ width: "100%" }}><Typography.Text>{uploadPhase}</Typography.Text><Progress percent={progress} status={progress === 100 ? "success" : "active"} /></Space></Card>}
            </Form>
        </Modal>

        <OperationReasonModal open={Boolean(operation)} title={operationTitle(operation)} consequence={tr("操作会写入审计日志；发布会立即进入公共目录，归档会立即停止公开下载。")}
            confirmLabel={operation?.kind === "publish-version" ? tr("确认发布") : tr("确认归档")} danger={operation?.kind !== "publish-version"}
            requireMFA loading={working} onCancel={() => setOperation(null)} onConfirm={confirmOperation} />
    </div>;
}

function MetadataForm<T extends CategoryValues | EntryValues>({ form, onFinish, includeCategory, immutableSlug, categoryOptions }: {
    form: FormInstance<T>;
    onFinish: (values: T) => void;
    includeCategory: boolean;
    immutableSlug: boolean;
    categoryOptions: { label: string; value: string }[];
}) {
    return <Form form={form} layout="vertical" requiredMark={false} onFinish={onFinish}>
        {includeCategory && <Form.Item label={tr("分类")} name="category_id" rules={[{ required: true }]}><Select options={categoryOptions} /></Form.Item>}
        <Space align="start" wrap style={{ width: "100%" }}>
            <Form.Item label="Slug" name="slug" rules={[{ required: true, pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/ }, { max: 64 }]}><Input maxLength={64} disabled={immutableSlug} /></Form.Item>
            <Form.Item label={tr("排序")} name="sort_order" rules={[{ required: true }]}><InputNumber min={-100000} max={100000} /></Form.Item>
        </Space>
        <Form.Item label={tr("中文标题")} name="title_zh_cn" rules={[{ required: true, whitespace: true }, { max: 128 }]}><Input maxLength={128} /></Form.Item>
        <Form.Item label={tr("英文标题")} name="title_en" rules={[{ required: true, whitespace: true }, { max: 128 }]}><Input maxLength={128} /></Form.Item>
        <Form.Item label={tr("中文说明")} name="description_zh_cn" rules={[{ max: includeCategory ? 4000 : 2000 }]}><Input.TextArea rows={3} maxLength={includeCategory ? 4000 : 2000} showCount /></Form.Item>
        <Form.Item label={tr("英文说明")} name="description_en" rules={[{ max: includeCategory ? 4000 : 2000 }]}><Input.TextArea rows={3} maxLength={includeCategory ? 4000 : 2000} showCount /></Form.Item>
        {!includeCategory && <Form.Item label={tr("启用分类")} name="enabled" valuePropName="checked"><Switch /></Form.Item>}
        <Form.Item label={tr("操作原因")} name="reason" rules={[{ required: true, whitespace: true }, { max: 500 }]}><Input.TextArea rows={2} maxLength={500} showCount /></Form.Item>
    </Form>;
}

function hashFile(file: File, onProgress: (loaded: number) => void): Promise<string> {
    return new Promise((resolve, reject) => {
        const worker = new Worker(new URL("../workers/sha256.worker.ts", import.meta.url), { type: "module" });
        worker.onmessage = (event: MessageEvent<{ type: string; loaded?: number; sha256?: string; message?: string }>) => {
            if (event.data.type === "progress") onProgress(event.data.loaded ?? 0);
            if (event.data.type === "complete") { worker.terminate(); resolve(event.data.sha256 ?? ""); }
            if (event.data.type === "error") { worker.terminate(); reject(new Error(event.data.message)); }
        };
        worker.onerror = (event) => { worker.terminate(); reject(new Error(event.message)); };
        worker.postMessage({ file });
    });
}

async function pollVerification(uploadId: string, report: (phase: string) => void): Promise<UploadSession> {
    for (;;) {
        await new Promise((resolve) => setTimeout(resolve, 2000));
        const session = await apiRequest<UploadSession>(`/v1/admin/download-uploads/${encodeURIComponent(uploadId)}`);
        if (["DRAFT", "FAILED"].includes(session.version.status)) return session;
        report(tr("服务端正在流式复核文件完整性，请勿关闭页面。"));
    }
}

function readResumeState(): { uploadId: string; entryId: string; fileName: string; size: number; sha256: string; versionLabel: string } | null {
    try { return JSON.parse(localStorage.getItem(resumeStorageKey) ?? "null"); }
    catch { return null; }
}

function localized(value: LocalizedText) {
    return localeTag().toLowerCase().startsWith("zh") ? value.zh_cn : value.en;
}

function formatBytes(value: number) {
    if (value < 1024) return `${value} B`;
    if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`;
    if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB`;
    return `${(value / 1024 ** 3).toFixed(2)} GiB`;
}

function statusColor(status: DownloadVersion["status"]) {
    return ({ UPLOADING: "blue", VERIFYING: "processing", DRAFT: "gold", PUBLISHED: "green", ARCHIVED: "default", FAILED: "red" } as const)[status];
}

function operationTitle(operation: Operation | null) {
    if (!operation) return "";
    if (operation.kind === "publish-version") return tr(`发布版本 · ${operation.target.version_label}`);
    if (operation.kind === "archive-version") return tr(`归档版本 · ${operation.target.version_label}`);
    if (operation.kind === "archive-entry") return tr(`归档下载项目 · ${localized(operation.target.title)}`);
    return tr(`归档分类 · ${localized(operation.target.title)}`);
}

function errorMessage(error: unknown) {
    return error instanceof ApiError || error instanceof Error ? error.message : tr("下载管理请求失败。");
}
