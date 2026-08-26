CREATE TABLE match_lobbies (
    id VARCHAR(64) PRIMARY KEY,
    owner_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    p2p_room_id VARCHAR(64) UNIQUE REFERENCES p2p_rooms(id) ON DELETE SET NULL,
    display_name VARCHAR(128) NOT NULL,
    hosting_kind VARCHAR(16) NOT NULL,
    transport_kind VARCHAR(16),
    mode VARCHAR(64) NOT NULL,
    region VARCHAR(64) NOT NULL,
    client_version VARCHAR(64) NOT NULL,
    protocol_version INTEGER NOT NULL CHECK (protocol_version > 0),
    team_one_capacity INTEGER NOT NULL CHECK (team_one_capacity BETWEEN 1 AND 32),
    team_two_capacity INTEGER NOT NULL CHECK (team_two_capacity BETWEEN 1 AND 32),
    state VARCHAR(16) NOT NULL DEFAULT 'OPEN',
    roster_revision BIGINT NOT NULL DEFAULT 1 CHECK (roster_revision > 0),
    current_attempt_id VARCHAR(64),
    idempotency_key VARCHAR(128),
    idempotency_request_hash BYTEA,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    CONSTRAINT match_lobbies_hosting_kind CHECK (hosting_kind IN ('DEDICATED', 'P2P')),
    CONSTRAINT match_lobbies_transport_kind CHECK (
        (hosting_kind = 'DEDICATED' AND transport_kind IS NULL)
        OR (hosting_kind = 'P2P' AND transport_kind IN ('LEGACY_RELAY', 'VNT'))
    ),
    CONSTRAINT match_lobbies_state CHECK (
        state IN ('OPEN', 'FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING',
                  'COMPLETED', 'ABORTED')
    ),
    CONSTRAINT match_lobbies_idempotency_hash CHECK (
        (idempotency_key IS NULL AND idempotency_request_hash IS NULL)
        OR (idempotency_key IS NOT NULL
            AND idempotency_request_hash IS NOT NULL
            AND octet_length(idempotency_request_hash) = 32)
    )
);

-- statement-breakpoint
CREATE UNIQUE INDEX match_lobbies_owner_idempotency_idx
    ON match_lobbies (owner_player_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- statement-breakpoint
CREATE INDEX match_lobbies_directory_idx
    ON match_lobbies (state, hosting_kind, region, mode, client_version, id);

-- statement-breakpoint
CREATE TABLE match_lobby_members (
    lobby_id VARCHAR(64) NOT NULL REFERENCES match_lobbies(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    role VARCHAR(16) NOT NULL,
    team_id INTEGER NOT NULL,
    team_slot INTEGER NOT NULL CHECK (team_slot >= 0),
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    presence_state VARCHAR(16) NOT NULL DEFAULT 'ONLINE',
    presence_expires_at TIMESTAMPTZ NOT NULL,
    membership_state VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    joined_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    left_at TIMESTAMPTZ,
    PRIMARY KEY (lobby_id, player_id),
    CONSTRAINT match_lobby_members_role CHECK (role IN ('OWNER', 'MEMBER')),
    CONSTRAINT match_lobby_members_team CHECK (team_id IN (1, 2)),
    CONSTRAINT match_lobby_members_presence CHECK (presence_state IN ('ONLINE', 'OFFLINE')),
    CONSTRAINT match_lobby_members_state CHECK (membership_state IN ('ACTIVE', 'LEFT'))
);

-- statement-breakpoint
CREATE UNIQUE INDEX match_lobby_members_active_seat_idx
    ON match_lobby_members (lobby_id, team_id, team_slot)
    WHERE membership_state = 'ACTIVE';

-- statement-breakpoint
CREATE INDEX match_lobby_members_player_idx
    ON match_lobby_members (player_id, membership_state, lobby_id);

-- statement-breakpoint
CREATE TABLE match_attempts (
    id VARCHAR(64) PRIMARY KEY,
    lobby_id VARCHAR(64) NOT NULL REFERENCES match_lobbies(id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    hosting_kind VARCHAR(16) NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'FROZEN',
    roster_revision BIGINT NOT NULL CHECK (roster_revision > 0),
    authority_id VARCHAR(128),
    authority_session_id VARCHAR(128) NOT NULL,
    authority_last_seen_at TIMESTAMPTZ,
    payload_installed_at TIMESTAMPTZ,
    payload_version VARCHAR(64),
    game_binary_sha256 VARCHAR(64),
    payload_route_generation INTEGER CHECK (payload_route_generation > 0),
    route_generation INTEGER NOT NULL DEFAULT 1 CHECK (route_generation > 0),
    endpoint_host VARCHAR(255),
    endpoint_port INTEGER CHECK (endpoint_port BETWEEN 1 AND 65535),
    connection_deadline TIMESTAMPTZ,
    host_reconnect_deadline TIMESTAMPTZ,
    meta_match_id VARCHAR(64),
    p2p_match_id VARCHAR(64),
    failure_code VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CONSTRAINT match_attempts_hosting_kind CHECK (hosting_kind IN ('DEDICATED', 'P2P')),
    CONSTRAINT match_attempts_state CHECK (
        state IN ('FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING',
                  'COMPLETED', 'ABORTED')
    ),
    CONSTRAINT match_attempts_payload_confirmation CHECK (
        (payload_installed_at IS NULL AND payload_version IS NULL
            AND game_binary_sha256 IS NULL AND payload_route_generation IS NULL)
        OR (
            payload_installed_at IS NOT NULL
            AND payload_version IS NOT NULL
            AND game_binary_sha256 IS NOT NULL
            AND game_binary_sha256 ~ '^[0-9a-f]{64}$'
        )
    ),
    CONSTRAINT match_attempts_sequence_unique UNIQUE (lobby_id, attempt_number)
);

-- statement-breakpoint
CREATE UNIQUE INDEX match_attempts_one_active_lobby_idx
    ON match_attempts (lobby_id)
    WHERE state IN ('FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING');

-- statement-breakpoint
ALTER TABLE match_lobbies
    ADD CONSTRAINT match_lobbies_current_attempt_fk
        FOREIGN KEY (current_attempt_id) REFERENCES match_attempts(id) ON DELETE SET NULL;

-- statement-breakpoint
CREATE TABLE match_attempt_roster (
    attempt_id VARCHAR(64) NOT NULL REFERENCES match_attempts(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    platform_id VARCHAR(20) NOT NULL,
    display_name VARCHAR(256) NOT NULL,
    room_role VARCHAR(16) NOT NULL,
    team_id INTEGER NOT NULL,
    team_slot INTEGER NOT NULL CHECK (team_slot >= 0),
    logical_slot INTEGER NOT NULL CHECK (logical_slot >= 0),
    connection_generation INTEGER NOT NULL DEFAULT 1 CHECK (connection_generation > 0),
    connection_state VARCHAR(16) NOT NULL DEFAULT 'RESERVED',
    auth_level_at_freeze VARCHAR(16) NOT NULL,
    steam_verified_at_freeze BOOLEAN NOT NULL,
    joined_lobby_at TIMESTAMPTZ NOT NULL,
    connected_at TIMESTAMPTZ,
    disconnected_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (attempt_id, player_id),
    CONSTRAINT match_attempt_roster_role CHECK (room_role IN ('HOST', 'MEMBER')),
    CONSTRAINT match_attempt_roster_team CHECK (team_id IN (1, 2)),
    CONSTRAINT match_attempt_roster_connection_state CHECK (
        connection_state IN ('RESERVED', 'CONNECTING', 'CONNECTED', 'DISCONNECTED')
    ),
    CONSTRAINT match_attempt_roster_auth_level CHECK (
        auth_level_at_freeze IN ('unverified', 'verified', 'trusted')
    ),
    CONSTRAINT match_attempt_roster_steam_verified CHECK (
        steam_verified_at_freeze = (auth_level_at_freeze IN ('verified', 'trusted'))
    ),
    CONSTRAINT match_attempt_roster_team_seat_unique UNIQUE (attempt_id, team_id, team_slot),
    CONSTRAINT match_attempt_roster_logical_seat_unique UNIQUE (attempt_id, logical_slot)
);

-- statement-breakpoint
CREATE TABLE match_admission_grants (
    jti VARCHAR(96) PRIMARY KEY,
    attempt_id VARCHAR(64) NOT NULL,
    player_id VARCHAR(64) NOT NULL,
    connection_generation INTEGER NOT NULL CHECK (connection_generation > 0),
    route_generation INTEGER NOT NULL CHECK (route_generation > 0),
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    FOREIGN KEY (attempt_id, player_id)
        REFERENCES match_attempt_roster(attempt_id, player_id) ON DELETE CASCADE
);

-- statement-breakpoint
CREATE INDEX match_admission_grants_active_idx
    ON match_admission_grants (attempt_id, player_id, expires_at)
    WHERE revoked_at IS NULL;

-- statement-breakpoint
ALTER TABLE p2p_rooms
    ADD COLUMN managed_lobby_id VARCHAR(64) UNIQUE
        REFERENCES match_lobbies(id) ON DELETE SET NULL;

-- statement-breakpoint
ALTER TABLE p2p_match_sessions
    ADD COLUMN match_attempt_id VARCHAR(64) UNIQUE
        REFERENCES match_attempts(id) ON DELETE SET NULL;

-- statement-breakpoint
ALTER TABLE p2p_match_roster
    ADD COLUMN team_slot INTEGER,
    ADD COLUMN connection_generation INTEGER NOT NULL DEFAULT 1,
    ADD CONSTRAINT p2p_match_roster_team_slot CHECK (team_slot IS NULL OR team_slot >= 0),
    ADD CONSTRAINT p2p_match_roster_connection_generation CHECK (connection_generation > 0);

-- statement-breakpoint
ALTER TABLE meta_matches
    ADD COLUMN match_attempt_id VARCHAR(64) UNIQUE
        REFERENCES match_attempts(id) ON DELETE SET NULL;

-- statement-breakpoint
ALTER TABLE meta_match_players
    ADD COLUMN team_id INTEGER,
    ADD COLUMN team_slot INTEGER,
    ADD COLUMN logical_slot INTEGER,
    ADD COLUMN connection_generation INTEGER NOT NULL DEFAULT 1,
    ADD CONSTRAINT meta_match_players_team CHECK (team_id IS NULL OR team_id IN (1, 2)),
    ADD CONSTRAINT meta_match_players_team_slot CHECK (team_slot IS NULL OR team_slot >= 0),
    ADD CONSTRAINT meta_match_players_logical_slot CHECK (logical_slot IS NULL OR logical_slot >= 0),
    ADD CONSTRAINT meta_match_players_connection_generation CHECK (connection_generation > 0);
