import { localizeSystemText, tr } from "../i18n";
import type { AdminAccess, AdminIdentity, TurnstileConfig } from "../types";
type SuccessEnvelope<T> = {
    data: T;
    request_id: string;
};
type ErrorEnvelope = {
    error?: {
        code?: string;
        message?: string;
        details?: Record<string, unknown>;
    };
    request_id?: string;
};
type AccessState = {
    token: string;
    expiresAt: number;
    admin: AdminIdentity;
} | null;
export class ApiError extends Error {
    readonly status: number;
    readonly code: string;
    readonly details: Record<string, unknown>;
    readonly requestId: string;
    constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}, requestId = "") {
        super(message);
        this.name = "ApiError";
        this.status = status;
        this.code = code;
        this.details = details;
        this.requestId = requestId;
    }
}
let accessState: AccessState = null;
let refreshPromise: Promise<AdminAccess> | null = null;
let stepUpState: {
    token: string;
    expiresAt: number;
} | null = null;
function updateAccess(access: AdminAccess | null) {
    stepUpState = null;
    if (!access) {
        accessState = null;
        return;
    }
    accessState = {
        token: access.access_token,
        expiresAt: Date.parse(access.access_token_expires_at),
        admin: access.admin
    };
}
async function decodeResponse<T>(response: Response): Promise<T> {
    let body: SuccessEnvelope<T> | ErrorEnvelope | null = null;
    try {
        body = (await response.json()) as SuccessEnvelope<T> | ErrorEnvelope;
    }
    catch {
        body = null;
    }
    if (!response.ok) {
        const errorBody = body as ErrorEnvelope | null;
        const errorMessage = errorBody?.error?.message ?? tr("\u8BF7\u6C42\u5931\u8D25\uFF0C\u8BF7\u7A0D\u540E\u91CD\u8BD5\u3002");
        throw new ApiError(response.status, errorBody?.error?.code ?? "HTTP_ERROR", localizeSystemText(errorMessage, "The request failed. Please try again later."), errorBody?.error?.details ?? {}, errorBody?.request_id ?? response.headers.get("X-Request-Id") ?? "");
    }
    if (!body || !("data" in body)) {
        throw new ApiError(response.status, "INVALID_RESPONSE", tr("\u670D\u52A1\u5668\u8FD4\u56DE\u4E86\u65E0\u6CD5\u8BC6\u522B\u7684\u54CD\u5E94\u3002"));
    }
    return body.data;
}
async function rawRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    if (init.body && !headers.has("Content-Type")) {
        headers.set("Content-Type", "application/json");
    }
    const response = await fetch(path, {
        ...init,
        headers,
        credentials: "include"
    });
    return decodeResponse<T>(response);
}
async function refreshAccess(): Promise<AdminAccess> {
    if (!refreshPromise) {
        refreshPromise = rawRequest<AdminAccess>("/v1/admin/auth/refresh", {
            method: "POST"
        })
            .then((access) => {
            updateAccess(access);
            return access;
        })
            .finally(() => {
            refreshPromise = null;
        });
    }
    return refreshPromise;
}
export const authClient = {
    async config(): Promise<TurnstileConfig> {
        const response = await rawRequest<{
            turnstile: TurnstileConfig;
        }>("/v1/admin/auth/config");
        return response.turnstile;
    },
    async beginLogin(input: {
        username: string;
        password: string;
        turnstile_token: string;
    }): Promise<{
        mfa_required: true;
        challenge_token: string;
        expires_at: string;
    }> {
        return rawRequest("/v1/admin/auth/login", {
            method: "POST",
            body: JSON.stringify(input)
        });
    },
    async verifyMFA(input: {
        challenge_token: string;
        code: string;
    }): Promise<AdminAccess> {
        const access = await rawRequest<AdminAccess>("/v1/admin/auth/mfa/verify", {
            method: "POST",
            body: JSON.stringify(input)
        });
        updateAccess(access);
        return access;
    },
    async stepUp(code: string): Promise<void> {
        const result = await apiRequest<{
            step_up_token: string;
            expires_at: string;
        }>("/v1/admin/auth/step-up", { method: "POST", body: JSON.stringify({ code }) });
        stepUpState = {
            token: result.step_up_token,
            expiresAt: Date.parse(result.expires_at)
        };
    },
    async ensureSession(): Promise<AdminAccess> {
        if (accessState && accessState.expiresAt > Date.now() + 20000) {
            return {
                access_token: accessState.token,
                access_token_expires_at: new Date(accessState.expiresAt).toISOString(),
                admin: accessState.admin
            };
        }
        return refreshAccess();
    },
    async logout(): Promise<void> {
        try {
            if (accessState?.token) {
                await rawRequest("/v1/admin/auth/logout", {
                    method: "POST",
                    headers: { Authorization: `Bearer ${accessState.token}` }
                });
            }
        }
        finally {
            updateAccess(null);
        }
    },
    clear() {
        updateAccess(null);
    },
    identity(): AdminIdentity | null {
        return accessState?.admin ?? null;
    },
    permissions(): string[] {
        return accessState?.admin.permissions ?? [];
    },
    token(): string {
        return accessState?.token ?? "";
    }
};
export async function apiRequest<T>(path: string, init: RequestInit = {}, allowRefresh = true): Promise<T> {
    if (!accessState || accessState.expiresAt <= Date.now() + 20000) {
        await refreshAccess();
    }
    const headers = new Headers(init.headers);
    headers.set("Authorization", `Bearer ${accessState?.token ?? ""}`);
    if (stepUpState && stepUpState.expiresAt > Date.now() + 5000) {
        headers.set("X-Admin-Step-Up", stepUpState.token);
    }
    try {
        return await rawRequest<T>(path, { ...init, headers });
    }
    catch (error) {
        if (error instanceof ApiError && error.code === "ADMIN_STEP_UP_REQUIRED") {
            stepUpState = null;
        }
        if (allowRefresh && error instanceof ApiError && error.status === 401) {
            await refreshAccess();
            headers.set("Authorization", `Bearer ${accessState?.token ?? ""}`);
            return rawRequest<T>(path, { ...init, headers });
        }
        throw error;
    }
}
