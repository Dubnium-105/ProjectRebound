CREATE TABLE p2p_rooms (
    id VARCHAR(64) PRIMARY KEY,
    host_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    host_token_hash BYTEA UNIQUE NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    mode VARCHAR(64) NOT NULL,
    version VARCHAR(64) NOT NULL,
    max_players INTEGER NOT NULL CHECK (max_players BETWEEN 2 AND 64),
    player_count INTEGER NOT NULL DEFAULT 1 CHECK (player_count >= 0),
    state VARCHAR(16) NOT NULL DEFAULT 'LOBBY',
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    CONSTRAINT p2p_rooms_state CHECK (state IN ('LOBBY', 'CONNECTING', 'RUNNING', 'STALE', 'CLOSED')),
    CONSTRAINT p2p_rooms_capacity CHECK (player_count <= max_players)
);

-- statement-breakpoint
CREATE INDEX p2p_rooms_directory_idx ON p2p_rooms (state, region, mode, version, id);

-- statement-breakpoint
CREATE INDEX p2p_rooms_heartbeat_idx ON p2p_rooms (last_heartbeat_at) WHERE state <> 'CLOSED';

-- statement-breakpoint
CREATE TABLE p2p_room_members (
    room_id VARCHAR(64) NOT NULL REFERENCES p2p_rooms(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    role VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    joined_at TIMESTAMPTZ NOT NULL,
    left_at TIMESTAMPTZ,
    PRIMARY KEY (room_id, player_id),
    CONSTRAINT p2p_room_members_role CHECK (role IN ('HOST', 'MEMBER')),
    CONSTRAINT p2p_room_members_status CHECK (status IN ('ACTIVE', 'LEFT'))
);

-- statement-breakpoint
CREATE INDEX p2p_room_members_player_idx ON p2p_room_members (player_id, status);
