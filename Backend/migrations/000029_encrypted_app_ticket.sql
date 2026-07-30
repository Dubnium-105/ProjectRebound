ALTER TABLE players
    DROP CONSTRAINT players_auth_level;

-- statement-breakpoint
ALTER TABLE players
    ADD CONSTRAINT players_auth_level
    CHECK (auth_level IN ('unverified', 'verified', 'trusted'));

-- statement-breakpoint
ALTER TABLE auth_sessions
    ADD COLUMN auth_provider VARCHAR(32) NOT NULL DEFAULT 'steam_client_asserted',
    ADD COLUMN auth_level VARCHAR(16) NOT NULL DEFAULT 'unverified',
    ADD COLUMN steam_verified BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT auth_sessions_auth_provider
        CHECK (auth_provider IN ('steam_client_asserted', 'steam_ticket', 'steam_openid')),
    ADD CONSTRAINT auth_sessions_auth_level
        CHECK (auth_level IN ('unverified', 'verified', 'trusted')),
    ADD CONSTRAINT auth_sessions_steam_verified
        CHECK (steam_verified = (auth_level IN ('verified', 'trusted')));

-- statement-breakpoint
ALTER TABLE auth_login_audit_logs
    ADD COLUMN device_id_hash BYTEA,
    ADD COLUMN device_fingerprint_id VARCHAR(64) REFERENCES auth_device_fingerprints(id);

-- statement-breakpoint
CREATE TABLE auth_steam_ticket_verifications (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    steam_id VARCHAR(20) NOT NULL,
    app_id BIGINT NOT NULL CHECK (app_id BETWEEN 1 AND 4294967295),
    ticket_hash BYTEA NOT NULL UNIQUE,
    issue_time TIMESTAMPTZ NOT NULL,
    verified_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT auth_steam_ticket_hash_length CHECK (octet_length(ticket_hash) = 32)
);

-- statement-breakpoint
CREATE INDEX auth_steam_ticket_player_idx
    ON auth_steam_ticket_verifications (player_id, verified_at DESC);

-- statement-breakpoint
CREATE TABLE ban_device_fingerprint (
    id VARCHAR(64) PRIMARY KEY,
    digest_key_id VARCHAR(32) NOT NULL,
    uuid_hash BYTEA,
    disk_hash BYTEA,
    cpu_hash BYTEA,
    user_id VARCHAR(64) NOT NULL REFERENCES players(id),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ban_device_fingerprint_two_factors CHECK (
        (CASE WHEN uuid_hash IS NULL THEN 0 ELSE 1 END) +
        (CASE WHEN disk_hash IS NULL THEN 0 ELSE 1 END) +
        (CASE WHEN cpu_hash IS NULL THEN 0 ELSE 1 END) >= 2
    ),
    CONSTRAINT ban_device_fingerprint_uuid_length CHECK (
        uuid_hash IS NULL OR octet_length(uuid_hash) = 32
    ),
    CONSTRAINT ban_device_fingerprint_disk_length CHECK (
        disk_hash IS NULL OR octet_length(disk_hash) = 32
    ),
    CONSTRAINT ban_device_fingerprint_cpu_length CHECK (
        cpu_hash IS NULL OR octet_length(cpu_hash) = 32
    )
);

-- statement-breakpoint
CREATE INDEX ban_device_fingerprint_uuid_idx
    ON ban_device_fingerprint (digest_key_id, uuid_hash)
    WHERE uuid_hash IS NOT NULL;

-- statement-breakpoint
CREATE INDEX ban_device_fingerprint_disk_idx
    ON ban_device_fingerprint (digest_key_id, disk_hash)
    WHERE disk_hash IS NOT NULL;

-- statement-breakpoint
CREATE INDEX ban_device_fingerprint_cpu_idx
    ON ban_device_fingerprint (digest_key_id, cpu_hash)
    WHERE cpu_hash IS NOT NULL;

-- statement-breakpoint
ALTER TABLE auth_risk_events
    DROP CONSTRAINT auth_risk_event_type;

-- statement-breakpoint
ALTER TABLE auth_risk_events
    ADD CONSTRAINT auth_risk_event_type CHECK (event_type IN (
        'BIND_RATE_LIMITED',
        'REFRESH_TOKEN_REUSE',
        'MULTI_DEVICE_LOGIN',
        'RAPID_IP_CHANGE',
        'MULTI_ACCOUNT_FROM_DEVICE',
        'MULTI_ACCOUNT_FROM_IP',
        'INVALID_STEAM_ID',
        'INVALID_INVITE_CODE',
        'REVOKED_SESSION_USAGE',
        'STEAM_TICKET_VERIFY_FAILED',
        'DEVICE_MISMATCH',
        'INTEGRITY_FAILED'
    ));
