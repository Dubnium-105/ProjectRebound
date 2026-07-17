CREATE TABLE players (
    id VARCHAR(64) PRIMARY KEY,
    steam_id VARCHAR(20) UNIQUE NOT NULL,
    persona_name VARCHAR(256) NOT NULL,
    account_status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    is_vip BOOLEAN NOT NULL DEFAULT FALSE,
    auth_provider VARCHAR(32) NOT NULL DEFAULT 'steam_client_asserted',
    auth_level VARCHAR(16) NOT NULL DEFAULT 'unverified',
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT players_steam_id_format CHECK (steam_id ~ '^[0-9]{16,20}$'),
    CONSTRAINT players_account_status CHECK (account_status IN ('ACTIVE', 'BANNED', 'DELETED')),
    CONSTRAINT players_auth_provider CHECK (auth_provider IN ('steam_client_asserted', 'steam_ticket', 'steam_openid')),
    CONSTRAINT players_auth_level CHECK (auth_level IN ('unverified', 'verified'))
);

-- statement-breakpoint
CREATE INDEX players_last_login_idx ON players (last_login_at DESC);

-- statement-breakpoint
CREATE TABLE auth_sessions (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    refresh_token_hash BYTEA UNIQUE NOT NULL,
    token_family_id VARCHAR(64) NOT NULL,
    token_version INTEGER NOT NULL DEFAULT 1 CHECK (token_version > 0),
    device_id VARCHAR(128),
    ip_address INET,
    user_agent TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoked_reason VARCHAR(32),
    replaced_by_session_id VARCHAR(64),
    reuse_detected_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ
);

-- statement-breakpoint
CREATE INDEX auth_sessions_player_idx ON auth_sessions (player_id);

-- statement-breakpoint
CREATE INDEX auth_sessions_family_idx ON auth_sessions (token_family_id);

-- statement-breakpoint
CREATE INDEX auth_sessions_expires_idx ON auth_sessions (expires_at);

-- statement-breakpoint
CREATE INDEX auth_sessions_refresh_hash_idx ON auth_sessions (refresh_token_hash);

-- statement-breakpoint
CREATE TABLE auth_login_audit_logs (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) REFERENCES players(id),
    steam_id VARCHAR(20),
    event VARCHAR(48) NOT NULL,
    success BOOLEAN NOT NULL,
    failure_code VARCHAR(64),
    request_id VARCHAR(128),
    ip_address INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX auth_login_audit_player_idx ON auth_login_audit_logs (player_id, created_at DESC);

-- statement-breakpoint
CREATE INDEX auth_login_audit_steam_idx ON auth_login_audit_logs (steam_id, created_at DESC);
