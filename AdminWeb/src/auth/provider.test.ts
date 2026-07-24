import {beforeEach, describe, expect, it, vi} from "vitest";
import {authClient} from "../api/client";
import type {AdminAccess} from "../types";
import {accessControlProvider} from "./provider";

const access: AdminAccess = {
  access_token: "access-token",
  access_token_expires_at: "2099-01-01T00:00:00Z",
  admin: {
    admin_id: "adm_support",
    username: "support@example.com",
    display_name: "客服",
    roles: ["PLAYER_SUPPORT"],
    permissions: ["players.read"]
  }
};

describe("Refine access control provider", () => {
  beforeEach(() => {
    authClient.clear();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({data: access, request_id: "req_access"}), {
          status: 200,
          headers: {"Content-Type": "application/json"}
        })
      )
    );
  });

  it("allows mapped permissions and rejects unauthorized mutations", async () => {
    await authClient.ensureSession();

    await expect(
      accessControlProvider.can({resource: "players", action: "list"})
    ).resolves.toMatchObject({can: true});
    await expect(
      accessControlProvider.can({resource: "players", action: "edit"})
    ).resolves.toMatchObject({can: false});
  });

  it("honors an explicit permission declared by a resource", async () => {
    await authClient.ensureSession();

    await expect(
      accessControlProvider.can({
        resource: "custom",
        action: "list",
        params: {resource: {name: "custom", meta: {permission: "players.read"}}}
      })
    ).resolves.toMatchObject({can: true});
  });
});

