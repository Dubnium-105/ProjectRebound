import {afterEach, beforeEach, describe, expect, it, vi} from "vitest";
import {apiRequest, authClient} from "./client";
import type {AdminAccess} from "../types";

const firstAccess: AdminAccess = {
  access_token: "access-token-one",
  access_token_expires_at: "2099-01-01T00:00:00Z",
  admin: {
    admin_id: "adm_test",
    username: "operator@example.com",
    display_name: "值班管理员",
    roles: ["PLAYER_SUPPORT"],
    permissions: ["players.read", "players.revoke_sessions"]
  }
};

function jsonResponse(data: unknown, status = 200, requestID = "req_test") {
  return new Response(
    JSON.stringify(
      status >= 400
        ? {error: data, request_id: requestID}
        : {data, request_id: requestID}
    ),
    {
      status,
      headers: {"Content-Type": "application/json", "X-Request-Id": requestID}
    }
  );
}

describe("administrator API client", () => {
  beforeEach(() => {
    authClient.clear();
    vi.stubGlobal("fetch", vi.fn());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches public Turnstile configuration with same-origin credentials", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        turnstile: {
          configured: true,
          site_key: "1x00000000000000000000AA",
          action: "admin_login"
        }
      })
    );

    await expect(authClient.config()).resolves.toMatchObject({
      configured: true,
      action: "admin_login"
    });
    expect(fetch).toHaveBeenCalledWith(
      "/v1/admin/auth/config",
      expect.objectContaining({credentials: "include"})
    );
  });

  it("deduplicates simultaneous refreshes and keeps access tokens only in memory", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(firstAccess));

    const [left, right] = await Promise.all([
      authClient.ensureSession(),
      authClient.ensureSession()
    ]);

    expect(left.access_token).toBe("access-token-one");
    expect(right.access_token).toBe("access-token-one");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(authClient.token()).toBe("access-token-one");
    expect(authClient.permissions()).toContain("players.read");
  });

  it("refreshes once and retries a protected request after a 401", async () => {
    const secondAccess = {
      ...firstAccess,
      access_token: "access-token-two"
    };
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(firstAccess))
      .mockResolvedValueOnce(
        jsonResponse(
          {code: "ADMIN_UNAUTHORIZED", message: "Administrator authentication is required."},
          401
        )
      )
      .mockResolvedValueOnce(jsonResponse(secondAccess))
      .mockResolvedValueOnce(jsonResponse({player_id: "ply_test"}));

    await authClient.ensureSession();
    await expect(apiRequest<{player_id: string}>("/v1/admin/players/ply_test")).resolves.toEqual({
      player_id: "ply_test"
    });

    const calls = vi.mocked(fetch).mock.calls;
    expect(new Headers(calls[1][1]?.headers).get("Authorization")).toBe(
      "Bearer access-token-one"
    );
    expect(new Headers(calls[3][1]?.headers).get("Authorization")).toBe(
      "Bearer access-token-two"
    );
  });

  it("preserves backend error details and request ID for actionable UI errors", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse(
        {
          code: "TURNSTILE_FAILED",
          message: "安全验证失败，请重新验证后重试。",
          details: {retryable: true}
        },
        400,
        "req_turnstile"
      )
    );

    await expect(
      authClient.beginLogin({
        username: "operator@example.com",
        password: "not-a-real-password",
        turnstile_token: "test-token"
      })
    ).rejects.toMatchObject({
      status: 400,
      code: "TURNSTILE_FAILED",
      requestId: "req_turnstile",
      details: {retryable: true}
    });
  });

  it("keeps step-up proof in memory and sends it only as an authenticated header", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(firstAccess))
      .mockResolvedValueOnce(jsonResponse({
        step_up_token: "step-up-proof",
        expires_at: "2099-01-01T00:00:00Z"
      }))
      .mockResolvedValueOnce(jsonResponse({state: "REVOKED"}));

    await authClient.ensureSession();
    await authClient.stepUp("123456");
    await apiRequest("/v1/admin/relay-nodes/relay_test/revoke", {
      method: "POST",
      body: JSON.stringify({reason: "Security incident"})
    });

    const calls = vi.mocked(fetch).mock.calls;
    expect(new Headers(calls[1][1]?.headers).get("X-Admin-Step-Up")).toBeNull();
    expect(new Headers(calls[2][1]?.headers).get("X-Admin-Step-Up")).toBe("step-up-proof");
  });
});
