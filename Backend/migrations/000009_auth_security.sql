ALTER TABLE auth_sessions
    ADD COLUMN device_id_hash BYTEA,
    ADD COLUMN device_id_suffix VARCHAR(4);

-- statement-breakpoint
CREATE INDEX auth_sessions_device_hash_idx
    ON auth_sessions (device_id_hash)
    WHERE device_id_hash IS NOT NULL;

-- statement-breakpoint
CREATE TABLE auth_risk_events (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) REFERENCES players(id),
    steam_id VARCHAR(20),
    device_id_hash BYTEA,
    ip_address INET,
    event_type VARCHAR(64) NOT NULL,
    severity VARCHAR(16) NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    CONSTRAINT auth_risk_event_severity CHECK (severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    CONSTRAINT auth_risk_event_type CHECK (event_type IN (
        'BIND_RATE_LIMITED',
        'REFRESH_TOKEN_REUSE',
        'MULTI_DEVICE_LOGIN',
        'RAPID_IP_CHANGE',
        'MULTI_ACCOUNT_FROM_DEVICE',
        'MULTI_ACCOUNT_FROM_IP',
        'INVALID_STEAM_ID',
        'INVALID_INVITE_CODE',
        'REVOKED_SESSION_USAGE'
    ))
);

-- statement-breakpoint
CREATE INDEX auth_risk_events_player_idx
    ON auth_risk_events (player_id, created_at DESC);

-- statement-breakpoint
CREATE INDEX auth_risk_events_created_idx
    ON auth_risk_events (created_at DESC);

-- statement-breakpoint
CREATE TABLE auth_login_events (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) REFERENCES players(id),
    steam_id VARCHAR(20),
    session_id VARCHAR(64) REFERENCES auth_sessions(id),
    device_id_hash BYTEA,
    ip_address INET,
    user_agent TEXT,
    result VARCHAR(16) NOT NULL,
    failure_code VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT auth_login_event_result CHECK (result IN ('SUCCESS', 'FAILURE'))
);

-- statement-breakpoint
CREATE INDEX auth_login_events_player_idx
    ON auth_login_events (player_id, created_at DESC);

-- statement-breakpoint
CREATE INDEX auth_login_events_steam_idx
    ON auth_login_events (steam_id, created_at DESC);
