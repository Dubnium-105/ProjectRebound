ALTER TABLE invite_code_uses
    ADD COLUMN permission_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;

-- statement-breakpoint
UPDATE invite_code_uses AS invite_use
SET permission_snapshot = invite.permissions
FROM invite_codes AS invite
WHERE invite.id = invite_use.invite_code_id;

-- statement-breakpoint
CREATE TABLE game_server_registration_tokens (
    id VARCHAR(64) PRIMARY KEY,
    instance_id VARCHAR(128) NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE,
    created_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE RESTRICT,
    issued_to_player_id VARCHAR(64) REFERENCES players(id) ON DELETE RESTRICT,
    source_invite_use_id VARCHAR(64) REFERENCES invite_code_uses(id) ON DELETE RESTRICT,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    consumed_server_id VARCHAR(64) REFERENCES game_servers(id) ON DELETE RESTRICT,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT game_server_registration_token_hash_length
        CHECK (octet_length(token_hash) = 32),
    CONSTRAINT game_server_registration_token_expiry
        CHECK (expires_at > created_at),
    CONSTRAINT game_server_registration_token_consumption
        CHECK ((consumed_at IS NULL) = (consumed_server_id IS NULL)),
    CONSTRAINT game_server_registration_token_issuer
        CHECK (
            created_by IS NOT NULL OR
            (issued_to_player_id IS NOT NULL AND source_invite_use_id IS NOT NULL)
        )
);

-- statement-breakpoint
CREATE UNIQUE INDEX game_server_registration_tokens_active_instance_idx
    ON game_server_registration_tokens (instance_id)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

-- statement-breakpoint
CREATE INDEX game_server_registration_tokens_expiry_idx
    ON game_server_registration_tokens (expires_at)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

-- statement-breakpoint
CREATE INDEX game_server_registration_tokens_player_idx
    ON game_server_registration_tokens (issued_to_player_id, created_at DESC)
    WHERE issued_to_player_id IS NOT NULL;

-- statement-breakpoint
ALTER TABLE game_servers
    ADD COLUMN owner_player_id VARCHAR(64) REFERENCES players(id) ON DELETE RESTRICT,
    ADD COLUMN previous_server_token_hash BYTEA,
    ADD COLUMN previous_token_expires_at TIMESTAMPTZ,
    ADD COLUMN credential_generation BIGINT NOT NULL DEFAULT 1,
    ADD CONSTRAINT game_servers_previous_token_hash_length
        CHECK (previous_server_token_hash IS NULL OR octet_length(previous_server_token_hash) = 32),
    ADD CONSTRAINT game_servers_previous_token_pair
        CHECK ((previous_server_token_hash IS NULL) = (previous_token_expires_at IS NULL)),
    ADD CONSTRAINT game_servers_credential_generation_positive
        CHECK (credential_generation > 0);

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES (
    'aperm_game_servers_register',
    'game_servers.register',
    'game_servers',
    'register',
    '为单台专用服务器签发一次性注册凭据。',
    'HIGH'
)
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM admin_roles AS role
JOIN admin_permissions AS permission
    ON permission.permission_key = 'game_servers.register'
WHERE role.name IN ('SUPER_ADMIN', 'INFRA_OPERATOR')
ON CONFLICT DO NOTHING;
