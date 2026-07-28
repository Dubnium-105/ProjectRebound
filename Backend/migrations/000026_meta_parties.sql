CREATE TABLE meta_parties (
    id VARCHAR(64) PRIMARY KEY,
    leader_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    state VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
        CHECK (state IN ('ACTIVE', 'MATCHMAKING', 'IN_MATCH', 'CLOSED')),
    mode VARCHAR(64) NOT NULL DEFAULT 'default',
    region VARCHAR(64) NOT NULL DEFAULT 'auto',
    client_version VARCHAR(64) NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL DEFAULT 1 CHECK (protocol_version > 0),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    CONSTRAINT meta_parties_closed_state CHECK (
        (state = 'CLOSED' AND closed_at IS NOT NULL) OR
        (state <> 'CLOSED' AND closed_at IS NULL)
    )
);

-- statement-breakpoint
CREATE TABLE meta_party_members (
    party_id VARCHAR(64) NOT NULL REFERENCES meta_parties(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    role VARCHAR(16) NOT NULL DEFAULT 'MEMBER' CHECK (role IN ('LEADER', 'MEMBER')),
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    presence VARCHAR(16) NOT NULL DEFAULT 'ONLINE'
        CHECK (presence IN ('ONLINE', 'AWAY', 'IN_GAME', 'OFFLINE')),
    joined_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    left_at TIMESTAMPTZ,
    PRIMARY KEY (party_id, player_id)
);

-- statement-breakpoint
CREATE UNIQUE INDEX meta_party_members_one_active_party_idx
    ON meta_party_members (player_id)
    WHERE left_at IS NULL;

-- statement-breakpoint
CREATE UNIQUE INDEX meta_party_members_one_leader_idx
    ON meta_party_members (party_id)
    WHERE role = 'LEADER' AND left_at IS NULL;

-- statement-breakpoint
CREATE INDEX meta_party_members_party_active_idx
    ON meta_party_members (party_id, joined_at)
    WHERE left_at IS NULL;
