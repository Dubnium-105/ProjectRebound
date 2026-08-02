import type {components} from "./api/openapi.generated";

export type AdminIdentity = {
  admin_id: string;
  username: string;
  display_name: string;
  roles: string[];
  permissions: string[];
};

export type AdminAccess = {
  access_token: string;
  access_token_expires_at: string;
  admin: AdminIdentity;
};

export type TurnstileConfig = {
  configured: boolean;
  site_key: string;
  action: string;
};

export type Player = {
  id: string;
  player_id: string;
  steam_id: string;
  persona_name: string;
  account_status: "ACTIVE" | "BANNED" | "DELETED";
  is_vip: boolean;
  auth_provider: string;
  auth_level: string;
  last_login_at: string;
  created_at: string;
  updated_at: string;
};

export type InviteCode = {
  id: string;
  batch_name: string;
  max_uses: number;
  used_count: number;
  expires_at: string | null;
  enabled: boolean;
  permissions: Record<string, unknown>;
  created_by: string;
  created_at: string;
  updated_at: string;
  revoked_at: string | null;
};

export type InviteCodeUse = {
  id: string;
  invite_code_id: string;
  player_id: string;
  steam_id: string;
  ip_address: string;
  used_at: string;
  result: "SUCCESS";
};

export type RiskEvent = {
  id: string;
  player_id?: string;
  steam_id?: string;
  ip_address?: string;
  event_type: string;
  severity: "LOW" | "MEDIUM" | "HIGH" | "CRITICAL";
  details: Record<string, unknown>;
  created_at: string;
  resolved_at: string | null;
  resolved_by: string;
  resolution_note: string;
};

export type AdminSession = {
  session_id: string;
  ip_address: string;
  user_agent: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string;
  is_current: boolean;
};

export type GovernedAdministrator = components["schemas"]["GovernedAdministrator"];
export type AdminPermissionDefinition = components["schemas"]["AdminPermissionDefinition"];
export type GovernedRole = components["schemas"]["GovernedRole"];
export type AdminSetting = components["schemas"]["AdminSetting"];
export type AdminCapabilities = components["schemas"]["AdminCapabilities"];
export type DashboardSummary = components["schemas"]["AdminDashboardSummary"];
export type DashboardAlert = components["schemas"]["AdminDashboardAlert"];
export type DashboardPoint = components["schemas"]["AdminDashboardPoint"];

export type AuditLog = {
  id: string;
  admin_id: string;
  action: string;
  target_type: string;
  target_id: string;
  old_value: Record<string, unknown>;
  new_value: Record<string, unknown>;
  reason: string;
  request_id: string;
  ip_address: string;
  user_agent: string;
  result: "SUCCEEDED" | "FAILED" | "DENIED";
  created_at: string;
};

export type LoginAudit = {
  id: string;
  admin_id: string;
  event_type: string;
  result: "SUCCESS" | "FAILURE";
  reason_code: string;
  request_id: string;
  ip_address: string;
  user_agent: string;
  turnstile_success: boolean | null;
  turnstile_error_codes: string[];
  turnstile_hostname: string;
  turnstile_action: string;
  turnstile_verify_latency_ms: number | null;
  created_at: string;
};

export type PlayerSession = {
  session_id: string;
  device_id_suffix: string;
  ip_address: string;
  token_family_id: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string;
  revoked_at: string | null;
  revoked_reason: string;
  reuse_detected_at: string | null;
  active: boolean;
};

export type PlayerLoginEvent = {
  id: string;
  session_id: string;
  ip_address: string;
  user_agent: string;
  result: "SUCCESS" | "FAILURE";
  failure_code: string;
  created_at: string;
};

export type P2PRoom = {
  id: string;
  room_id: string;
  host_player_id: string;
  display_name: string;
  region: string;
  mode: string;
  version: string;
  max_players: number;
  player_count: number;
  state: "LOBBY" | "CONNECTING" | "RUNNING" | "STALE" | "CLOSED";
  last_heartbeat_at: string;
  created_at: string;
};

export type P2PRoomMember = {
  room_id: string;
  player_id: string;
  steam_id: string;
  persona_name: string;
  account_status: "ACTIVE" | "BANNED" | "DELETED";
  role: "HOST" | "MEMBER";
  status: "ACTIVE" | "LEFT";
  joined_at: string;
  left_at: string | null;
};

export type GameServer = {
  id: string;
  server_id: string;
  instance_id?: string;
  display_name: string;
  region: string;
  mode: string;
  version: string;
  endpoint: {host: string; port: number};
  max_players: number;
  player_count: number;
  state: "STARTING" | "READY" | "RESERVED" | "RUNNING" | "DRAINING" | "UNHEALTHY" | "OFFLINE";
  last_heartbeat_at: string;
  token_expires_at?: string;
  token_revoked_at?: string | null;
  credential_generation?: number;
  certificate_fingerprint?: string;
  certificate_expires_at?: string | null;
  legacy_auth_expires_at?: string | null;
};

export type RelayNode = {
  id: string;
  node_id: string;
  display_name: string;
  region: string;
  zone: string;
  provider: string;
  state: "BOOTSTRAPPING" | "CONNECTING" | "READY" | "DRAINING" | "UNHEALTHY" | "OFFLINE" | "REVOKED";
  load_state: string;
  software_version: string;
  protocol_version: number;
  public_endpoints: Array<{protocol: string; host: string; port: number}>;
  supported_protocols: string[];
  max_allocations: number;
  active_allocations: number;
  max_egress_bps: number;
  current_egress_bps: number;
  current_ingress_bps: number;
  certificate_fingerprint: string;
  certificate_expires_at: string;
  last_heartbeat_at?: string;
  drain_deadline?: string;
};

export type RelayMigration = {
  migration_id: string;
  previous_node_id: string;
  new_node_id: string;
  state: "BINDING" | "COMPLETED" | "FAILED";
  reason: string;
  attempt: number;
  failure_reason: string;
  created_at: string;
  completed_at: string | null;
};

export type Connection = {
  id: string;
  connection_id: string;
  room_id: string;
  host_player_id: string;
  peer_player_id: string;
  state:
    | "CREATED"
    | "GATHERING_CANDIDATES"
    | "CHECKING_DIRECT"
    | "ALLOCATING_RELAY"
    | "RELAY_BINDING"
    | "MIGRATING_RELAY"
    | "CONNECTED"
    | "FAILED"
    | "EXPIRED"
    | "CLOSED";
  selected_path: "" | "LAN" | "IPV6" | "UDP_PUNCH" | "UDP_RELAY" | "TCP_TLS_RELAY";
  failure_reason: string;
  allocation_id: string;
  relay_node_id: string;
  allocation_state: string;
  expires_at: string;
  created_at: string;
  updated_at: string;
  closed_at: string | null;
  migration_history?: RelayMigration[];
};

export type ReleaseSourceFile = components["schemas"]["AdminReleaseSourceFile"];
export type ReleaseValidation = components["schemas"]["AdminReleaseValidation"];
export type Release = components["schemas"]["AdminRelease"];
