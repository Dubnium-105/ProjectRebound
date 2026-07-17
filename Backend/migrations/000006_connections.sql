CREATE TABLE connections (
    id VARCHAR(64) PRIMARY KEY,
    room_id VARCHAR(64) NOT NULL REFERENCES p2p_rooms(id) ON DELETE CASCADE,
    host_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    peer_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    state VARCHAR(32) NOT NULL DEFAULT 'CREATED',
    selected_path VARCHAR(32),
    failure_reason VARCHAR(128),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    CONSTRAINT connections_distinct_participants CHECK (host_player_id <> peer_player_id),
    CONSTRAINT connections_state CHECK (state IN (
        'CREATED', 'GATHERING_CANDIDATES', 'CHECKING_DIRECT', 'ALLOCATING_RELAY',
        'RELAY_BINDING', 'CONNECTED', 'FAILED', 'EXPIRED', 'CLOSED'
    )),
    CONSTRAINT connections_selected_path CHECK (selected_path IS NULL OR selected_path IN (
        'LAN', 'IPV6', 'UDP_PUNCH', 'UDP_RELAY', 'TCP_TLS_RELAY'
    ))
);

-- statement-breakpoint
CREATE UNIQUE INDEX connections_active_peer_idx
    ON connections (room_id, peer_player_id)
    WHERE state NOT IN ('FAILED', 'EXPIRED', 'CLOSED');

-- statement-breakpoint
CREATE INDEX connections_participant_idx
    ON connections (host_player_id, peer_player_id, state);

-- statement-breakpoint
CREATE INDEX connections_expiry_idx
    ON connections (expires_at)
    WHERE state NOT IN ('FAILED', 'EXPIRED', 'CLOSED');

-- statement-breakpoint
CREATE TABLE connection_candidates (
    id VARCHAR(64) PRIMARY KEY,
    connection_id VARCHAR(64) NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    foundation VARCHAR(64) NOT NULL,
    candidate_type VARCHAR(16) NOT NULL,
    protocol VARCHAR(8) NOT NULL,
    address INET NOT NULL,
    port INTEGER NOT NULL,
    priority INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT connection_candidates_type CHECK (candidate_type IN ('LAN', 'IPV6', 'SRFLX')),
    CONSTRAINT connection_candidates_protocol CHECK (protocol IN ('UDP', 'TCP')),
    CONSTRAINT connection_candidates_port CHECK (port BETWEEN 1 AND 65535),
    CONSTRAINT connection_candidates_priority CHECK (priority BETWEEN 1 AND 2147483647),
    UNIQUE (connection_id, player_id, foundation)
);

-- statement-breakpoint
CREATE INDEX connection_candidates_connection_idx
    ON connection_candidates (connection_id, priority DESC, id);

-- statement-breakpoint
CREATE TABLE connection_path_checks (
    connection_id VARCHAR(64) NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    path VARCHAR(32) NOT NULL,
    reporter_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    success BOOLEAN NOT NULL,
    latency_ms INTEGER NOT NULL,
    reason VARCHAR(128),
    checked_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (connection_id, path),
    CONSTRAINT connection_path_checks_path CHECK (path IN ('LAN', 'IPV6', 'UDP_PUNCH')),
    CONSTRAINT connection_path_checks_latency CHECK (latency_ms BETWEEN 0 AND 60000)
);
