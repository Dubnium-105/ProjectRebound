import type {
  BaseRecord,
  CrudFilter,
  DataProvider,
  HttpError
} from "@refinedev/core";
import {ApiError, apiRequest} from "./client";

const paths: Record<string, string> = {
  players: "/v1/admin/players",
  "invite-codes": "/v1/admin/invite-codes",
  "risk-events": "/v1/admin/risk-events",
  "audit-logs": "/v1/admin/audit-logs",
  "login-audit": "/v1/admin/login-audit",
  "p2p-rooms": "/v1/admin/p2p-rooms",
  "game-servers": "/v1/admin/game-servers",
  "relay-nodes": "/v1/admin/relay-nodes",
  connections: "/v1/admin/connections",
  releases: "/v1/admin/releases",
  administrators: "/v1/admin/admins"
};

function pathFor(resource: string): string {
  const path = paths[resource];
  if (!path) {
    throw new Error(`Unsupported administrator resource: ${resource}`);
  }
  return path;
}

function recordID(resource: string, item: Record<string, unknown>) {
  if (resource === "players") {
    return item.player_id;
  }
  if (resource === "p2p-rooms") {
    return item.room_id;
  }
  if (resource === "game-servers") {
    return item.server_id;
  }
  if (resource === "relay-nodes") {
    return item.node_id;
  }
  if (resource === "connections") {
    return item.connection_id;
  }
  return item.id;
}

function normalizeRecord(resource: string, item: Record<string, unknown>): BaseRecord {
  return {...item, id: recordID(resource, item) as string};
}

function addFilters(search: URLSearchParams, filters: CrudFilter[] | undefined) {
  for (const filter of filters ?? []) {
    if ("field" in filter && filter.value !== undefined && filter.value !== "") {
      search.set(filter.field, String(filter.value));
    }
  }
}

function toHttpError(error: unknown): HttpError {
  if (error instanceof ApiError) {
    return {
      message: error.message,
      statusCode: error.status
    };
  }
  return {
    message: error instanceof Error ? error.message : "请求失败。",
    statusCode: 500
  };
}

export const dataProvider = {
  async getList({resource, pagination, filters}) {
    try {
      const search = new URLSearchParams();
      search.set("limit", String(pagination?.pageSize ?? 50));
      addFilters(search, filters);
      const result = await apiRequest<{
        items: Record<string, unknown>[];
        next_cursor?: string;
      }>(`${pathFor(resource)}?${search.toString()}`);
      const data = result.items.map((item) => normalizeRecord(resource, item));
      return {
        data,
        total: data.length,
        next_cursor: result.next_cursor ?? ""
      };
    } catch (error) {
      throw toHttpError(error);
    }
  },

  async getOne({resource, id}) {
    try {
      const item = await apiRequest<Record<string, unknown>>(
        `${pathFor(resource)}/${encodeURIComponent(String(id))}`
      );
      return {data: normalizeRecord(resource, item)};
    } catch (error) {
      throw toHttpError(error);
    }
  },

  async create({resource, variables}) {
    try {
      const result = await apiRequest<Record<string, unknown>>(pathFor(resource), {
        method: "POST",
        body: JSON.stringify(variables)
      });
      const item =
        resource === "invite-codes" &&
        typeof result.invite_code === "object" &&
        result.invite_code
          ? (result.invite_code as Record<string, unknown>)
          : result;
      return {data: normalizeRecord(resource, item)};
    } catch (error) {
      throw toHttpError(error);
    }
  },

  async update({resource, id, variables}) {
    try {
      const result = await apiRequest<Record<string, unknown>>(
        `${pathFor(resource)}/${encodeURIComponent(String(id))}`,
        {
          method: "PATCH",
          body: JSON.stringify(variables)
        }
      );
      const item =
        resource === "players" &&
        typeof result.player === "object" &&
        result.player
          ? (result.player as Record<string, unknown>)
          : result;
      return {data: normalizeRecord(resource, item)};
    } catch (error) {
      throw toHttpError(error);
    }
  },

  async deleteOne() {
    throw toHttpError(new ApiError(405, "METHOD_NOT_ALLOWED", "该资源不支持直接删除。"));
  },

  getApiUrl() {
    return "/v1/admin";
  }
} as DataProvider;
