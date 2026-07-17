CREATE TABLE game_servers (
    id VARCHAR(64) PRIMARY KEY,
    instance_id VARCHAR(128) UNIQUE NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    mode VARCHAR(64) NOT NULL,
    version VARCHAR(64) NOT NULL,
    public_host VARCHAR(255) NOT NULL,
    public_port INTEGER NOT NULL CHECK (public_port BETWEEN 1 AND 65535),
    max_players INTEGER NOT NULL CHECK (max_players BETWEEN 1 AND 256),
    player_count INTEGER NOT NULL DEFAULT 0 CHECK (player_count >= 0),
    state VARCHAR(16) NOT NULL DEFAULT 'STARTING',
    server_token_hash BYTEA UNIQUE NOT NULL,
    registration_issuer VARCHAR(128) NOT NULL,
    token_expires_at TIMESTAMPTZ NOT NULL,
    token_revoked_at TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT game_servers_state CHECK (state IN (
        'STARTING', 'READY', 'RESERVED', 'RUNNING', 'DRAINING', 'UNHEALTHY', 'OFFLINE'
    )),
    CONSTRAINT game_servers_player_capacity CHECK (player_count <= max_players)
);

-- statement-breakpoint
CREATE INDEX game_servers_directory_idx ON game_servers (state, region, mode, version, id);

-- statement-breakpoint
CREATE INDEX game_servers_heartbeat_idx ON game_servers (last_heartbeat_at) WHERE state <> 'OFFLINE';

-- statement-breakpoint
CREATE INDEX game_servers_token_hash_idx ON game_servers (server_token_hash);
