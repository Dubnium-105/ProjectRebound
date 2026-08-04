CREATE TABLE player_feature_grants (
    player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    capability VARCHAR(64) NOT NULL,
    source_invite_use_id VARCHAR(64) NOT NULL REFERENCES invite_code_uses(id) ON DELETE RESTRICT,
    granted_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    PRIMARY KEY (player_id, capability),
    CONSTRAINT player_feature_grants_capability CHECK (
        capability IN (
            'p2p_room_registration',
            'game_server_registration',
            'vnt_node_registration'
        )
    )
);

-- statement-breakpoint
CREATE INDEX player_feature_grants_source_idx
    ON player_feature_grants (source_invite_use_id);

-- statement-breakpoint
INSERT INTO player_feature_grants (player_id, capability, source_invite_use_id, granted_at, expires_at)
SELECT DISTINCT ON (invite_use.player_id)
       invite_use.player_id, 'p2p_room_registration', invite_use.id, invite_use.used_at, invite_code.expires_at
FROM invite_code_uses invite_use
JOIN invite_codes invite_code ON invite_code.id = invite_use.invite_code_id
WHERE invite_use.permission_snapshot @> '{"allow_p2p_room_registration": true}'::jsonb
   OR invite_use.permission_snapshot @> '{"allow_p2p": true}'::jsonb
ORDER BY invite_use.player_id, (invite_code.expires_at IS NULL) DESC,
         invite_code.expires_at DESC, invite_use.used_at DESC
ON CONFLICT DO NOTHING;

-- statement-breakpoint
CREATE TABLE vnt_node_enrollments (
    id VARCHAR(64) PRIMARY KEY,
    owner_player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    label VARCHAR(64) NOT NULL,
    secret_hash BYTEA UNIQUE NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX vnt_node_enrollments_owner_idx
    ON vnt_node_enrollments (owner_player_id, created_at DESC);

-- statement-breakpoint
CREATE TABLE vnt_nodes (
    id VARCHAR(64) PRIMARY KEY,
    owner_player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    advertised_host VARCHAR(253) NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    region VARCHAR(64) NOT NULL,
    location VARCHAR(128) NOT NULL,
    state VARCHAR(16) NOT NULL,
    vnts_version VARCHAR(32) NOT NULL,
    wrapper_version VARCHAR(32) NOT NULL,
    server_key_fingerprint VARCHAR(128) NOT NULL,
    supported_transports TEXT[] NOT NULL DEFAULT ARRAY['udp', 'tcp'],
    max_rooms INTEGER NOT NULL CHECK (max_rooms BETWEEN 1 AND 10000),
    reported_sessions INTEGER NOT NULL DEFAULT 0 CHECK (reported_sessions >= 0),
    last_heartbeat_at TIMESTAMPTZ,
    last_reachable_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    retired_at TIMESTAMPTZ,
    CONSTRAINT vnt_nodes_state CHECK (
        state IN ('REGISTERING','ONLINE','STALE','OFFLINE','DRAINING','REVOKED','RETIRED')
    ),
    UNIQUE (advertised_host, port)
);

-- statement-breakpoint
CREATE INDEX vnt_nodes_public_directory_idx
    ON vnt_nodes (state, region, id);

-- statement-breakpoint
CREATE TABLE vnt_node_credentials (
    id VARCHAR(64) PRIMARY KEY,
    node_id VARCHAR(64) NOT NULL REFERENCES vnt_nodes(id) ON DELETE RESTRICT,
    secret_hash BYTEA UNIQUE NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX vnt_node_credentials_node_idx
    ON vnt_node_credentials (node_id, expires_at DESC);

-- statement-breakpoint
ALTER TABLE p2p_rooms
    ADD COLUMN transport_kind VARCHAR(16) NOT NULL DEFAULT 'LEGACY_RELAY',
    ADD COLUMN expires_at TIMESTAMPTZ,
    ADD COLUMN idempotency_key VARCHAR(128),
    ADD COLUMN idempotency_request_hash BYTEA,
    ADD COLUMN host_token_ciphertext BYTEA,
    ADD COLUMN host_token_nonce BYTEA,
    ADD COLUMN host_token_key_id VARCHAR(64);

-- statement-breakpoint
UPDATE p2p_rooms
SET expires_at = created_at + INTERVAL '8 hours'
WHERE expires_at IS NULL;

-- statement-breakpoint
ALTER TABLE p2p_rooms
    ALTER COLUMN expires_at SET NOT NULL,
    ADD CONSTRAINT p2p_rooms_transport_kind
        CHECK (transport_kind IN ('LEGACY_RELAY', 'VNT'));

-- statement-breakpoint
CREATE UNIQUE INDEX p2p_rooms_idempotency_idx
    ON p2p_rooms (host_player_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- statement-breakpoint
CREATE TABLE p2p_vnt_sessions (
    room_id VARCHAR(64) PRIMARY KEY REFERENCES p2p_rooms(id) ON DELETE CASCADE,
    node_id VARCHAR(64) NOT NULL REFERENCES vnt_nodes(id) ON DELETE RESTRICT,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    state VARCHAR(24) NOT NULL,
    node_host_snapshot VARCHAR(253) NOT NULL,
    node_port_snapshot INTEGER NOT NULL,
    node_region_snapshot VARCHAR(64) NOT NULL,
    node_location_snapshot VARCHAR(128) NOT NULL,
    node_fingerprint_snapshot VARCHAR(128) NOT NULL,
    node_transports_snapshot TEXT[] NOT NULL,
    network_token_ciphertext BYTEA NOT NULL,
    e2e_password_ciphertext BYTEA NOT NULL,
    secret_key_id VARCHAR(64) NOT NULL,
    network_token_nonce BYTEA NOT NULL,
    e2e_password_nonce BYTEA NOT NULL,
    host_virtual_ip INET,
    failure_reason VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT p2p_vnt_state CHECK (
        state IN ('SELECTED','HOST_CONNECTING','HOST_READY','READY','ACTIVE',
                  'REBINDING','FAILED','CLOSED')
    )
);

-- statement-breakpoint
CREATE INDEX p2p_vnt_sessions_node_idx
    ON p2p_vnt_sessions (node_id, state);

-- statement-breakpoint
CREATE TABLE p2p_vnt_member_sessions (
    room_id VARCHAR(64) NOT NULL REFERENCES p2p_rooms(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    device_id VARCHAR(64) NOT NULL,
    virtual_ip INET NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'ISSUED',
    observed_path VARCHAR(16),
    failure_reason VARCHAR(64),
    last_report_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (room_id, generation, player_id),
    UNIQUE (room_id, generation, virtual_ip),
    UNIQUE (room_id, generation, device_id),
    CONSTRAINT p2p_vnt_member_state CHECK (
        state IN ('ISSUED','CONNECTING','CONNECTED','FAILED','STOPPED')
    )
);

-- statement-breakpoint
INSERT INTO player_feature_grants (player_id, capability, source_invite_use_id, granted_at, expires_at)
SELECT DISTINCT ON (invite_use.player_id)
       invite_use.player_id, 'game_server_registration', invite_use.id, invite_use.used_at, invite_code.expires_at
FROM invite_code_uses invite_use
JOIN invite_codes invite_code ON invite_code.id = invite_use.invite_code_id
WHERE invite_use.permission_snapshot @> '{"allow_game_server_registration": true}'::jsonb
ORDER BY invite_use.player_id, (invite_code.expires_at IS NULL) DESC,
         invite_code.expires_at DESC, invite_use.used_at DESC
ON CONFLICT DO NOTHING;

-- statement-breakpoint
INSERT INTO player_feature_grants (player_id, capability, source_invite_use_id, granted_at, expires_at)
SELECT DISTINCT ON (invite_use.player_id)
       invite_use.player_id, 'vnt_node_registration', invite_use.id, invite_use.used_at, invite_code.expires_at
FROM invite_code_uses invite_use
JOIN invite_codes invite_code ON invite_code.id = invite_use.invite_code_id
WHERE invite_use.permission_snapshot @> '{"allow_vnt_node_registration": true}'::jsonb
ORDER BY invite_use.player_id, (invite_code.expires_at IS NULL) DESC,
         invite_code.expires_at DESC, invite_use.used_at DESC
ON CONFLICT DO NOTHING;
