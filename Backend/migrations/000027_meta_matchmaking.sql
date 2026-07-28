CREATE TABLE meta_match_tickets (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    party_id VARCHAR(64) REFERENCES meta_parties(id) ON DELETE SET NULL,
    mode VARCHAR(64) NOT NULL,
    region VARCHAR(64) NOT NULL,
    client_version VARCHAR(64) NOT NULL,
    protocol_version INTEGER NOT NULL CHECK (protocol_version > 0),
    state VARCHAR(16) NOT NULL DEFAULT 'QUEUED'
        CHECK (state IN ('QUEUED', 'MATCHED', 'CANCELLED', 'TIMED_OUT', 'FAILED')),
    failure_code VARCHAR(64),
    matched_id VARCHAR(64),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

-- statement-breakpoint
CREATE UNIQUE INDEX meta_match_tickets_one_active_player_idx
    ON meta_match_tickets (player_id)
    WHERE state = 'QUEUED';

-- statement-breakpoint
CREATE UNIQUE INDEX meta_match_tickets_one_active_party_idx
    ON meta_match_tickets (party_id)
    WHERE party_id IS NOT NULL AND state = 'QUEUED';

-- statement-breakpoint
CREATE INDEX meta_match_tickets_scheduler_idx
    ON meta_match_tickets (state, mode, region, client_version, created_at)
    WHERE state = 'QUEUED';

-- statement-breakpoint
CREATE INDEX meta_match_tickets_expiry_idx
    ON meta_match_tickets (expires_at)
    WHERE state = 'QUEUED';

-- statement-breakpoint
CREATE TABLE meta_matches (
    id VARCHAR(64) PRIMARY KEY,
    game_server_id VARCHAR(64) NOT NULL REFERENCES game_servers(id),
    ticket_id VARCHAR(64) NOT NULL REFERENCES meta_match_tickets(id),
    mode VARCHAR(64) NOT NULL,
    region VARCHAR(64) NOT NULL,
    client_version VARCHAR(64) NOT NULL,
    protocol_version INTEGER NOT NULL CHECK (protocol_version > 0),
    state VARCHAR(16) NOT NULL DEFAULT 'RESERVED'
        CHECK (state IN ('RESERVED', 'RUNNING', 'COMPLETED', 'CANCELLED', 'FAILED')),
    endpoint_host VARCHAR(255) NOT NULL,
    endpoint_port INTEGER NOT NULL CHECK (endpoint_port BETWEEN 1 AND 65535),
    reserved_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE UNIQUE INDEX meta_matches_one_active_server_idx
    ON meta_matches (game_server_id)
    WHERE state IN ('RESERVED', 'RUNNING');

-- statement-breakpoint
CREATE TABLE meta_match_players (
    match_id VARCHAR(64) NOT NULL REFERENCES meta_matches(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    connected_at TIMESTAMPTZ,
    disconnected_at TIMESTAMPTZ,
    result JSONB,
    PRIMARY KEY (match_id, player_id),
    CONSTRAINT meta_match_players_result_object CHECK (
        result IS NULL OR jsonb_typeof(result) = 'object'
    )
);

-- statement-breakpoint
CREATE INDEX meta_match_players_player_idx
    ON meta_match_players (player_id, match_id);
