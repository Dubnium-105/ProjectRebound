import type {AccessControlProvider, AuthProvider} from "@refinedev/core";
import {ApiError, authClient} from "../api/client";

export const authProvider: AuthProvider = {
  async login() {
    return {
      success: false,
      error: new Error("请使用管理员登录页面完成 Turnstile、密码和 MFA 校验。")
    };
  },

  async logout() {
    await authClient.logout();
    return {success: true, redirectTo: "/login"};
  },

  async check() {
    try {
      await authClient.ensureSession();
      return {authenticated: true};
    } catch {
      authClient.clear();
      return {authenticated: false, redirectTo: "/login"};
    }
  },

  async onError(error) {
    if (error instanceof ApiError && error.status === 401) {
      authClient.clear();
      return {logout: true, redirectTo: "/login"};
    }
    return {};
  },

  async getPermissions() {
    await authClient.ensureSession();
    return authClient.permissions();
  },

  async getIdentity() {
    await authClient.ensureSession();
    const admin = authClient.identity();
    if (!admin) {
      return null;
    }
    return {
      id: admin.admin_id,
      name: admin.display_name,
      username: admin.username,
      roles: admin.roles,
      permissions: admin.permissions
    };
  }
};

const permissionMap: Record<string, Partial<Record<string, string>>> = {
  players: {
    list: "players.read",
    show: "players.read",
    edit: "players.update_status"
  },
  "invite-codes": {
    list: "invite_codes.read",
    show: "invite_codes.read",
    create: "invite_codes.create",
    edit: "invite_codes.update",
    delete: "invite_codes.revoke"
  },
  "risk-events": {
    list: "risk_events.read",
    show: "risk_events.read"
  },
  "audit-logs": {
    list: "audit_logs.read",
    show: "audit_logs.read"
  },
  "login-audit": {
    list: "audit_logs.read",
    show: "audit_logs.read"
  },
  "p2p-rooms": {
    list: "rooms.read",
    show: "rooms.read"
  },
  "game-servers": {
    list: "game_servers.read",
    show: "game_servers.read"
  },
  "relay-nodes": {
    list: "relay_nodes.read",
    show: "relay_nodes.read"
  },
  connections: {
    list: "connections.read",
    show: "connections.read"
  },
  releases: {
    list: "updates.read",
    show: "updates.read",
    create: "updates.create"
  },
  administrators: {
    list: "admins.read",
    create: "admins.create",
    edit: "admins.update"
  },
  roles: {
    list: "admins.read",
    edit: "roles.manage"
  },
  settings: {
    list: "settings.read",
    edit: "settings.update"
  }
};

export const accessControlProvider: AccessControlProvider = {
  async can({resource, action, params}) {
    await authClient.ensureSession();
    const explicit = params?.resource?.meta?.permission;
    const required =
      typeof explicit === "string"
        ? explicit
        : resource
          ? permissionMap[resource]?.[action]
          : undefined;
    if (!required) {
      return {can: true};
    }
    const can = authClient.permissions().includes(required);
    return {
      can,
      reason: can ? undefined : "当前管理员角色缺少此操作权限。"
    };
  },
  options: {
    buttons: {
      enableAccessControl: true,
      hideIfUnauthorized: true
    }
  }
};
