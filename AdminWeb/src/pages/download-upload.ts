import { tr } from "../i18n";

export type SignedRequest = {
    url: string;
    method: string;
    headers: Record<string, string>;
    expires_at: string;
};

export async function putObject(request: SignedRequest, body: Blob, existingObjectIsSuccess = false) {
    const headers = new Headers();
    for (const [key, value] of Object.entries(request.headers ?? {})) {
        if (!["host", "content-length"].includes(key.toLowerCase())) headers.set(key, value);
    }
    const response = await fetch(request.url, { method: request.method || "PUT", headers, body });
    // A resumed single-file upload can reach this branch after the object was
    // stored but before the control-plane completion request was sent. The
    // immutable PUT correctly returns 412; server-side verification is still
    // authoritative before the version can become a draft.
    if (existingObjectIsSuccess && response.status === 412) return;
    if (!response.ok) throw new Error(tr(`对象存储上传失败（HTTP ${response.status}）。`));
}

export async function retry(operation: () => Promise<void>, attempts: number, wait = defaultWait) {
    let lastError: unknown;
    for (let attempt = 0; attempt < attempts; attempt++) {
        try { await operation(); return; }
        catch (error) {
            lastError = error;
            if (attempt + 1 < attempts) await wait(500 * 2 ** attempt);
        }
    }
    throw lastError;
}

export async function runConcurrent<T>(items: T[], concurrency: number, operation: (item: T) => Promise<void>) {
    let cursor = 0;
    const workers = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
        while (cursor < items.length) {
            const item = items[cursor++];
            await operation(item);
        }
    });
    await Promise.all(workers);
}

function defaultWait(milliseconds: number) {
    return new Promise<void>((resolve) => setTimeout(resolve, milliseconds));
}
