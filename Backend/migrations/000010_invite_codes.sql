CREATE TABLE invite_codes (
    id VARCHAR(64) PRIMARY KEY,
    code_hash BYTEA UNIQUE NOT NULL,
    batch_name VARCHAR(128) NOT NULL,
    max_uses INTEGER NOT NULL CHECK (max_uses > 0),
    used_count INTEGER NOT NULL DEFAULT 0 CHECK (used_count >= 0 AND used_count <= max_uses),
    expires_at TIMESTAMPTZ,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    permissions JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

-- statement-breakpoint
CREATE INDEX invite_codes_batch_idx ON invite_codes (batch_name, created_at DESC);

-- statement-breakpoint
CREATE INDEX invite_codes_active_idx ON invite_codes (enabled, expires_at);

-- statement-breakpoint
CREATE TABLE invite_code_uses (
    id VARCHAR(64) PRIMARY KEY,
    invite_code_id VARCHAR(64) NOT NULL REFERENCES invite_codes(id),
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    steam_id VARCHAR(20) NOT NULL,
    ip_address INET,
    used_at TIMESTAMPTZ NOT NULL,
    result VARCHAR(16) NOT NULL,
    CONSTRAINT invite_code_use_result CHECK (result IN ('SUCCESS'))
);

-- statement-breakpoint
CREATE INDEX invite_code_uses_code_idx ON invite_code_uses (invite_code_id, used_at DESC);

-- statement-breakpoint
CREATE INDEX invite_code_uses_player_idx ON invite_code_uses (player_id, used_at DESC);
