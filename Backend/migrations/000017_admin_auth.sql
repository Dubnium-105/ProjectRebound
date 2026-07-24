CREATE TABLE admin_users (
    id VARCHAR(64) PRIMARY KEY,
    username VARCHAR(128) NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    password_hash TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED')),
    mfa_required BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    disabled_at TIMESTAMPTZ
);

-- statement-breakpoint
CREATE UNIQUE INDEX admin_users_username_idx ON admin_users (LOWER(username));

-- statement-breakpoint
CREATE TABLE admin_roles (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(64) NOT NULL UNIQUE,
    display_name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    system_role BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE TABLE admin_permissions (
    id VARCHAR(64) PRIMARY KEY,
    permission_key VARCHAR(128) NOT NULL UNIQUE,
    resource VARCHAR(64) NOT NULL,
    action VARCHAR(64) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    risk_level VARCHAR(16) NOT NULL DEFAULT 'LOW'
        CHECK (risk_level IN ('LOW', 'MEDIUM', 'HIGH'))
);

-- statement-breakpoint
CREATE TABLE admin_user_roles (
    admin_id VARCHAR(64) NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    role_id VARCHAR(64) NOT NULL REFERENCES admin_roles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (admin_id, role_id)
);

-- statement-breakpoint
CREATE TABLE admin_role_permissions (
    role_id VARCHAR(64) NOT NULL REFERENCES admin_roles(id) ON DELETE CASCADE,
    permission_id VARCHAR(64) NOT NULL REFERENCES admin_permissions(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (role_id, permission_id)
);

-- statement-breakpoint
CREATE TABLE admin_mfa_credentials (
    admin_id VARCHAR(64) PRIMARY KEY REFERENCES admin_users(id) ON DELETE CASCADE,
    secret_ciphertext BYTEA NOT NULL,
    key_version INTEGER NOT NULL DEFAULT 1,
    verified_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE TABLE admin_recovery_codes (
    id VARCHAR(64) PRIMARY KEY,
    admin_id VARCHAR(64) NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ
);

-- statement-breakpoint
CREATE UNIQUE INDEX admin_recovery_codes_hash_idx
    ON admin_recovery_codes (admin_id, code_hash);

-- statement-breakpoint
CREATE TABLE admin_login_challenges (
    id VARCHAR(64) PRIMARY KEY,
    admin_id VARCHAR(64) NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    attempts INTEGER NOT NULL DEFAULT 0,
    request_id VARCHAR(128),
    ip_address INET,
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX admin_login_challenges_expiry_idx
    ON admin_login_challenges (expires_at);

-- statement-breakpoint
CREATE TABLE admin_sessions (
    id VARCHAR(64) PRIMARY KEY,
    admin_id VARCHAR(64) NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    refresh_token_hash BYTEA NOT NULL UNIQUE,
    previous_refresh_token_hash BYTEA UNIQUE,
    token_version INTEGER NOT NULL DEFAULT 1,
    ip_address INET,
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoke_reason VARCHAR(64)
);

-- statement-breakpoint
CREATE INDEX admin_sessions_admin_idx
    ON admin_sessions (admin_id, created_at DESC);

-- statement-breakpoint
CREATE INDEX admin_sessions_expiry_idx
    ON admin_sessions (expires_at)
    WHERE revoked_at IS NULL;

-- statement-breakpoint
CREATE TABLE admin_login_audit_logs (
    id VARCHAR(64) PRIMARY KEY,
    admin_id VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    username_hash CHAR(64),
    event_type VARCHAR(64) NOT NULL,
    result VARCHAR(16) NOT NULL CHECK (result IN ('SUCCESS', 'FAILURE')),
    reason_code VARCHAR(64),
    request_id VARCHAR(128),
    ip_address INET,
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    turnstile_success BOOLEAN,
    turnstile_error_codes TEXT[] NOT NULL DEFAULT '{}',
    turnstile_hostname VARCHAR(253),
    turnstile_action VARCHAR(32),
    turnstile_verify_latency_ms INTEGER,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX admin_login_audit_created_idx
    ON admin_login_audit_logs (created_at DESC);

-- statement-breakpoint
CREATE INDEX admin_login_audit_admin_idx
    ON admin_login_audit_logs (admin_id, created_at DESC);

-- statement-breakpoint
INSERT INTO admin_roles (
    id, name, display_name, description, system_role, created_at, updated_at
) VALUES (
    'arol_super_admin',
    'SUPER_ADMIN',
    '超级管理员',
    '拥有全部管理权限的系统角色。',
    TRUE,
    NOW(),
    NOW()
);

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    ('aperm_dashboard_read', 'dashboard.read', 'dashboard', 'read', '查看运营总览。', 'LOW'),
    ('aperm_players_read', 'players.read', 'players', 'read', '查看玩家资料。', 'LOW'),
    ('aperm_players_update_status', 'players.update_status', 'players', 'update_status', '修改玩家账号状态。', 'HIGH'),
    ('aperm_players_update_vip', 'players.update_vip', 'players', 'update_vip', '修改玩家 VIP 状态。', 'MEDIUM'),
    ('aperm_players_revoke_sessions', 'players.revoke_sessions', 'players', 'revoke_sessions', '撤销玩家会话。', 'MEDIUM'),
    ('aperm_risk_events_read', 'risk_events.read', 'risk_events', 'read', '查看风险事件。', 'LOW'),
    ('aperm_risk_events_resolve', 'risk_events.resolve', 'risk_events', 'resolve', '处理风险事件。', 'MEDIUM'),
    ('aperm_invite_codes_read', 'invite_codes.read', 'invite_codes', 'read', '查看邀请码。', 'LOW'),
    ('aperm_invite_codes_create', 'invite_codes.create', 'invite_codes', 'create', '创建邀请码。', 'MEDIUM'),
    ('aperm_invite_codes_update', 'invite_codes.update', 'invite_codes', 'update', '修改邀请码。', 'MEDIUM'),
    ('aperm_invite_codes_revoke', 'invite_codes.revoke', 'invite_codes', 'revoke', '撤销邀请码。', 'HIGH'),
    ('aperm_game_servers_read', 'game_servers.read', 'game_servers', 'read', '查看专服。', 'LOW'),
    ('aperm_game_servers_drain', 'game_servers.drain', 'game_servers', 'drain', '将专服切换为维护模式。', 'MEDIUM'),
    ('aperm_game_servers_disable', 'game_servers.disable', 'game_servers', 'disable', '停用专服。', 'HIGH'),
    ('aperm_relay_nodes_read', 'relay_nodes.read', 'relay_nodes', 'read', '查看中继节点。', 'LOW'),
    ('aperm_relay_nodes_drain', 'relay_nodes.drain', 'relay_nodes', 'drain', '将中继节点切换为维护模式。', 'MEDIUM'),
    ('aperm_relay_nodes_resume', 'relay_nodes.resume', 'relay_nodes', 'resume', '恢复中继节点接收连接。', 'MEDIUM'),
    ('aperm_relay_nodes_revoke', 'relay_nodes.revoke', 'relay_nodes', 'revoke', '撤销中继节点。', 'HIGH'),
    ('aperm_connections_read', 'connections.read', 'connections', 'read', '查看连接。', 'LOW'),
    ('aperm_connections_migrate', 'connections.migrate', 'connections', 'migrate', '迁移连接。', 'HIGH'),
    ('aperm_connections_close', 'connections.close', 'connections', 'close', '关闭连接。', 'HIGH'),
    ('aperm_updates_read', 'updates.read', 'updates', 'read', '查看发布版本。', 'LOW'),
    ('aperm_updates_create', 'updates.create', 'updates', 'create', '创建发布版本。', 'MEDIUM'),
    ('aperm_updates_publish', 'updates.publish', 'updates', 'publish', '发布客户端版本。', 'HIGH'),
    ('aperm_updates_rollback', 'updates.rollback', 'updates', 'rollback', '回滚客户端版本。', 'HIGH'),
    ('aperm_admins_read', 'admins.read', 'admins', 'read', '查看管理员。', 'LOW'),
    ('aperm_admins_create', 'admins.create', 'admins', 'create', '创建管理员。', 'HIGH'),
    ('aperm_admins_update', 'admins.update', 'admins', 'update', '修改或停用管理员。', 'HIGH'),
    ('aperm_roles_manage', 'roles.manage', 'roles', 'manage', '管理角色和权限。', 'HIGH'),
    ('aperm_audit_logs_read', 'audit_logs.read', 'audit_logs', 'read', '查看审计日志。', 'LOW'),
    ('aperm_settings_read', 'settings.read', 'settings', 'read', '查看系统设置。', 'LOW'),
    ('aperm_settings_update', 'settings.update', 'settings', 'update', '修改系统设置。', 'HIGH');

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', id, NOW()
FROM admin_permissions;
