CREATE TABLE p2p_match_sessions (
    id VARCHAR(64) PRIMARY KEY,
    room_id VARCHAR(64) REFERENCES p2p_rooms(id) ON DELETE SET NULL,
    room_id_snapshot VARCHAR(64) NOT NULL,
    sequence INTEGER NOT NULL DEFAULT 1 CHECK (sequence > 0),
    host_player_id_at_start VARCHAR(64) NOT NULL REFERENCES players(id),
    mode VARCHAR(64) NOT NULL,
    map_alias VARCHAR(128) NOT NULL DEFAULT '',
    match_type VARCHAR(16) NOT NULL,
    state VARCHAR(32) NOT NULL,
    roster_revision INTEGER NOT NULL DEFAULT 1 CHECK (roster_revision > 0),
    expected_reporter_count INTEGER NOT NULL DEFAULT 0 CHECK (expected_reporter_count >= 0),
    policy_version VARCHAR(32) NOT NULL,
    collection_started_at TIMESTAMPTZ,
    collection_deadline TIMESTAMPTZ,
    hard_expires_at TIMESTAMPTZ NOT NULL,
    finalized_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT p2p_match_sessions_room_sequence_unique
        UNIQUE (room_id_snapshot, sequence),
    CONSTRAINT p2p_match_sessions_type CHECK (
        match_type IN ('PVE', 'PVP', 'UNKNOWN')
    ),
    CONSTRAINT p2p_match_sessions_state CHECK (
        state IN (
            'STARTING', 'RUNNING', 'COLLECTING', 'PEER_CONFIRMED',
            'SELF_REPORTED', 'DISPUTED', 'INCOMPLETE', 'ABORTED', 'EXPIRED'
        )
    ),
    CONSTRAINT p2p_match_sessions_collection_window CHECK (
        (collection_started_at IS NULL AND collection_deadline IS NULL)
        OR (
            collection_started_at IS NOT NULL
            AND collection_deadline IS NOT NULL
            AND collection_deadline >= collection_started_at
        )
    ),
    CONSTRAINT p2p_match_sessions_finalized_state CHECK (
        (finalized_at IS NULL AND state IN ('STARTING', 'RUNNING', 'COLLECTING'))
        OR (
            finalized_at IS NOT NULL
            AND state IN (
                'PEER_CONFIRMED', 'SELF_REPORTED', 'DISPUTED',
                'INCOMPLETE', 'ABORTED', 'EXPIRED'
            )
        )
    )
);

-- statement-breakpoint
CREATE UNIQUE INDEX p2p_match_sessions_active_room_unique
    ON p2p_match_sessions (room_id_snapshot)
    WHERE state IN ('STARTING', 'RUNNING', 'COLLECTING');

-- statement-breakpoint
CREATE INDEX p2p_match_sessions_finalizer_idx
    ON p2p_match_sessions (collection_deadline, hard_expires_at, id)
    WHERE state IN ('STARTING', 'RUNNING', 'COLLECTING');

-- statement-breakpoint
CREATE TABLE p2p_match_roster (
    match_id VARCHAR(64) NOT NULL REFERENCES p2p_match_sessions(id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    platform_id VARCHAR(20) NOT NULL,
    room_role VARCHAR(16) NOT NULL,
    slot_index INTEGER NOT NULL CHECK (slot_index >= 0),
    team_id INTEGER,
    auth_level_at_start VARCHAR(16) NOT NULL,
    steam_verified_at_start BOOLEAN NOT NULL,
    is_spectator BOOLEAN NOT NULL DEFAULT FALSE,
    is_initial_roster BOOLEAN NOT NULL DEFAULT TRUE,
    eligible_reporter BOOLEAN NOT NULL DEFAULT TRUE,
    joined_room_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (match_id, player_id),
    CONSTRAINT p2p_match_roster_slot_unique UNIQUE (match_id, slot_index),
    CONSTRAINT p2p_match_roster_role CHECK (room_role IN ('HOST', 'MEMBER')),
    CONSTRAINT p2p_match_roster_auth_level CHECK (
        auth_level_at_start IN ('unverified', 'verified', 'trusted')
    ),
    CONSTRAINT p2p_match_roster_steam_verified CHECK (
        steam_verified_at_start = (auth_level_at_start IN ('verified', 'trusted'))
    )
);

-- statement-breakpoint
CREATE INDEX p2p_match_roster_player_history_idx
    ON p2p_match_roster (player_id, match_id);

-- statement-breakpoint
CREATE TABLE p2p_match_presence_segments (
    id VARCHAR(64) PRIMARY KEY,
    match_id VARCHAR(64) NOT NULL,
    player_id VARCHAR(64) NOT NULL,
    segment_no INTEGER NOT NULL CHECK (segment_no > 0),
    join_kind VARCHAR(16) NOT NULL,
    status VARCHAR(24) NOT NULL,
    timeline_session_id VARCHAR(128),
    presence_seq BIGINT NOT NULL DEFAULT 0 CHECK (presence_seq >= 0),
    last_checkpoint_seq BIGINT NOT NULL DEFAULT 0 CHECK (last_checkpoint_seq >= 0),
    game_process_alive BOOLEAN NOT NULL DEFAULT FALSE,
    game_connected BOOLEAN NOT NULL DEFAULT FALSE,
    joined_at TIMESTAMPTZ NOT NULL,
    became_active_at TIMESTAMPTZ,
    last_presence_at TIMESTAMPTZ NOT NULL,
    disconnected_at TIMESTAMPTZ,
    left_at TIMESTAMPTZ,
    leave_kind VARCHAR(32),
    FOREIGN KEY (match_id, player_id)
        REFERENCES p2p_match_roster(match_id, player_id) ON DELETE CASCADE,
    CONSTRAINT p2p_match_presence_segment_unique
        UNIQUE (match_id, player_id, segment_no),
    CONSTRAINT p2p_match_presence_join_kind CHECK (
        join_kind IN ('INITIAL', 'LATE_JOIN', 'RECONNECT')
    ),
    CONSTRAINT p2p_match_presence_status CHECK (
        status IN (
            'CONNECTING', 'ACTIVE', 'DISCONNECTED', 'RESULT_SCREEN',
            'EXIT_INTENT', 'LEFT'
        )
    ),
    CONSTRAINT p2p_match_presence_leave_kind CHECK (
        leave_kind IS NULL OR leave_kind IN (
            'VOLUNTARY', 'NETWORK_LOST', 'KICKED', 'HOST_LOST', 'UNKNOWN'
        )
    )
);

-- statement-breakpoint
CREATE INDEX p2p_match_presence_active_idx
    ON p2p_match_presence_segments (match_id, player_id, segment_no DESC)
    WHERE left_at IS NULL;

-- statement-breakpoint
CREATE TABLE p2p_match_report_capabilities (
    id VARCHAR(64) PRIMARY KEY,
    match_id VARCHAR(64) NOT NULL,
    player_id VARCHAR(64) NOT NULL,
    auth_session_id VARCHAR(64) NOT NULL REFERENCES auth_sessions(id),
    token_hash BYTEA NOT NULL UNIQUE,
    server_nonce VARCHAR(128) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    first_used_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (match_id, player_id)
        REFERENCES p2p_match_roster(match_id, player_id) ON DELETE CASCADE,
    CONSTRAINT p2p_match_report_capability_hash_length CHECK (
        octet_length(token_hash) = 32
    )
);

-- statement-breakpoint
CREATE UNIQUE INDEX p2p_match_report_capability_active_unique
    ON p2p_match_report_capabilities (match_id, player_id)
    WHERE revoked_at IS NULL;

-- statement-breakpoint
CREATE INDEX p2p_match_report_capability_expiry_idx
    ON p2p_match_report_capabilities (expires_at)
    WHERE revoked_at IS NULL;

-- statement-breakpoint
CREATE TABLE p2p_battlelog_reports (
    id VARCHAR(64) PRIMARY KEY,
    report_id VARCHAR(128) NOT NULL,
    match_id VARCHAR(64) NOT NULL REFERENCES p2p_match_sessions(id) ON DELETE CASCADE,
    reporter_player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    capability_id VARCHAR(64) NOT NULL REFERENCES p2p_match_report_capabilities(id),
    report_revision INTEGER NOT NULL CHECK (report_revision > 0),
    completeness VARCHAR(16) NOT NULL,
    schema_name VARCHAR(128) NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    authority_kind VARCHAR(32) NOT NULL,
    client_version VARCHAR(64) NOT NULL,
    timeline_session_id VARCHAR(128) NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
    raw_size_bytes INTEGER NOT NULL CHECK (raw_size_bytes > 0),
    raw_sha256 BYTEA NOT NULL,
    outcome_sha256 BYTEA NOT NULL,
    stats_sha256 BYTEA NOT NULL,
    raw_payload BYTEA NOT NULL,
    raw_snapshot JSONB NOT NULL,
    normalized_result JSONB NOT NULL,
    validation_status VARCHAR(32) NOT NULL,
    risk_severity VARCHAR(16),
    validation_warnings JSONB NOT NULL DEFAULT '[]'::jsonb,
    CONSTRAINT p2p_battlelog_reports_report_unique
        UNIQUE (match_id, reporter_player_id, report_id),
    CONSTRAINT p2p_battlelog_reports_report_id_format CHECK (
        report_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
    ),
    CONSTRAINT p2p_battlelog_reports_completeness CHECK (
        completeness IN ('PARTIAL', 'FINAL')
    ),
    CONSTRAINT p2p_battlelog_reports_authority CHECK (
        authority_kind IN ('CLIENT_OBSERVER', 'LISTEN_HOST_OBSERVER')
    ),
    CONSTRAINT p2p_battlelog_reports_validation_status CHECK (
        validation_status IN ('ACCEPTED', 'ACCEPTED_WITH_WARNINGS', 'QUARANTINED')
    ),
    CONSTRAINT p2p_battlelog_reports_risk CHECK (
        risk_severity IS NULL OR risk_severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')
    ),
    CONSTRAINT p2p_battlelog_reports_raw_object CHECK (
        jsonb_typeof(raw_snapshot) = 'object'
    ),
    CONSTRAINT p2p_battlelog_reports_normalized_object CHECK (
        jsonb_typeof(normalized_result) = 'object'
    ),
    CONSTRAINT p2p_battlelog_reports_warnings_array CHECK (
        jsonb_typeof(validation_warnings) = 'array'
    ),
    CONSTRAINT p2p_battlelog_reports_digest_lengths CHECK (
        octet_length(raw_sha256) = 32
        AND octet_length(outcome_sha256) = 32
        AND octet_length(stats_sha256) = 32
    ),
    CONSTRAINT p2p_battlelog_reports_raw_payload_size CHECK (
        octet_length(raw_payload) = raw_size_bytes
    )
);

-- statement-breakpoint
CREATE UNIQUE INDEX p2p_battlelog_reports_final_reporter_unique
    ON p2p_battlelog_reports (match_id, reporter_player_id)
    WHERE completeness = 'FINAL';

-- statement-breakpoint
CREATE INDEX p2p_battlelog_reports_match_received_idx
    ON p2p_battlelog_reports (match_id, received_at, id);

-- statement-breakpoint
CREATE TABLE p2p_battlelog_matches (
    match_id VARCHAR(64) PRIMARY KEY REFERENCES p2p_match_sessions(id) ON DELETE CASCADE,
    trust_tier VARCHAR(24) NOT NULL,
    validation_status VARCHAR(32) NOT NULL,
    risk_severity VARCHAR(16),
    mode_alias VARCHAR(128) NOT NULL DEFAULT '',
    map_alias VARCHAR(128) NOT NULL DEFAULT '',
    match_type VARCHAR(16) NOT NULL,
    winner_team_id INTEGER,
    team_scores JSONB NOT NULL DEFAULT '[]'::jsonb,
    outcome_sha256 BYTEA NOT NULL,
    policy_version VARCHAR(32) NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT p2p_battlelog_matches_trust CHECK (
        trust_tier IN ('PEER_ATTESTED', 'SELF_REPORTED')
    ),
    CONSTRAINT p2p_battlelog_matches_validation CHECK (
        validation_status IN ('PEER_CONFIRMED', 'SELF_REPORTED')
    ),
    CONSTRAINT p2p_battlelog_matches_risk CHECK (
        risk_severity IS NULL OR risk_severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')
    ),
    CONSTRAINT p2p_battlelog_matches_type CHECK (
        match_type IN ('PVE', 'PVP', 'UNKNOWN')
    ),
    CONSTRAINT p2p_battlelog_matches_scores_array CHECK (
        jsonb_typeof(team_scores) = 'array'
    ),
    CONSTRAINT p2p_battlelog_matches_outcome_hash_length CHECK (
        octet_length(outcome_sha256) = 32
    )
);

-- statement-breakpoint
CREATE INDEX p2p_battlelog_matches_history_idx
    ON p2p_battlelog_matches (decided_at DESC, match_id);

-- statement-breakpoint
CREATE TABLE p2p_battlelog_participants (
    id VARCHAR(64) PRIMARY KEY,
    match_id VARCHAR(64) NOT NULL REFERENCES p2p_battlelog_matches(match_id) ON DELETE CASCADE,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id),
    team_id INTEGER,
    outcome VARCHAR(16) NOT NULL,
    stats_status VARCHAR(16) NOT NULL,
    kills INTEGER NOT NULL DEFAULT 0 CHECK (kills >= 0),
    deaths INTEGER NOT NULL DEFAULT 0 CHECK (deaths >= 0),
    assists INTEGER NOT NULL DEFAULT 0 CHECK (assists >= 0),
    score DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (score >= 0),
    is_quitter BOOLEAN NOT NULL DEFAULT FALSE,
    is_inactive BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT p2p_battlelog_participants_player_unique UNIQUE (match_id, player_id),
    CONSTRAINT p2p_battlelog_participants_outcome CHECK (
        outcome IN ('WON', 'LOST', 'DRAW', 'UNKNOWN')
    ),
    CONSTRAINT p2p_battlelog_participants_stats_status CHECK (
        stats_status IN ('CONSENSUS', 'SELF_ONLY', 'UNVERIFIED', 'CONFLICTED')
    )
);

-- statement-breakpoint
CREATE INDEX p2p_battlelog_participants_player_history_idx
    ON p2p_battlelog_participants (player_id, match_id);

-- statement-breakpoint
CREATE TABLE p2p_battlelog_rounds (
    match_id VARCHAR(64) NOT NULL REFERENCES p2p_battlelog_matches(match_id) ON DELETE CASCADE,
    round_index INTEGER NOT NULL CHECK (round_index >= 0),
    winner_team_id INTEGER,
    is_final_round BOOLEAN NOT NULL DEFAULT FALSE,
    team_scores JSONB NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (match_id, round_index),
    CONSTRAINT p2p_battlelog_rounds_scores_array CHECK (
        jsonb_typeof(team_scores) = 'array'
    )
);

-- statement-breakpoint
CREATE TABLE p2p_battlelog_decisions (
    id VARCHAR(64) PRIMARY KEY,
    match_id VARCHAR(64) NOT NULL REFERENCES p2p_match_sessions(id) ON DELETE CASCADE,
    decision_revision INTEGER NOT NULL CHECK (decision_revision > 0),
    policy_version VARCHAR(32) NOT NULL,
    eligible_reporter_count INTEGER NOT NULL CHECK (eligible_reporter_count >= 0),
    received_final_count INTEGER NOT NULL CHECK (received_final_count >= 0),
    matching_outcome_count INTEGER NOT NULL CHECK (matching_outcome_count >= 0),
    required_quorum INTEGER NOT NULL CHECK (required_quorum >= 0),
    team_coverage BOOLEAN NOT NULL,
    decision VARCHAR(32) NOT NULL,
    risk_severity VARCHAR(16),
    reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    decided_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT p2p_battlelog_decisions_revision_unique
        UNIQUE (match_id, decision_revision),
    CONSTRAINT p2p_battlelog_decisions_decision CHECK (
        decision IN (
            'PEER_CONFIRMED', 'SELF_REPORTED', 'DISPUTED',
            'INCOMPLETE', 'ABORTED', 'EXPIRED'
        )
    ),
    CONSTRAINT p2p_battlelog_decisions_risk CHECK (
        risk_severity IS NULL OR risk_severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')
    ),
    CONSTRAINT p2p_battlelog_decisions_reasons_array CHECK (
        jsonb_typeof(reasons) = 'array'
    )
);

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    (
        'aperm_p2p_battlelog_read',
        'p2p.battlelog.read',
        'p2p_battlelog',
        'read',
        'Read normalized peer-attested P2P BattleLog records.',
        'LOW'
    ),
    (
        'aperm_p2p_battlelog_raw_read',
        'p2p.battlelog.raw.read',
        'p2p_battlelog',
        'raw_read',
        'Read raw player-submitted P2P BattleLog evidence.',
        'MEDIUM'
    ),
    (
        'aperm_p2p_battlelog_manage',
        'p2p.battlelog.manage',
        'p2p_battlelog',
        'manage',
        'Resolve or quarantine peer-attested P2P BattleLog evidence.',
        'HIGH'
    )
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
WHERE permission.permission_key LIKE 'p2p.battlelog.%'
ON CONFLICT DO NOTHING;

-- statement-breakpoint
WITH grants(role_name, permission_key) AS (
    VALUES
        ('OPERATIONS', 'p2p.battlelog.read'),
        ('PLAYER_SUPPORT', 'p2p.battlelog.read'),
        ('AUDITOR', 'p2p.battlelog.read'),
        ('AUDITOR', 'p2p.battlelog.raw.read'),
        ('VIEWER', 'p2p.battlelog.read')
)
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM grants
JOIN admin_roles AS role ON role.name = grants.role_name
JOIN admin_permissions AS permission ON permission.permission_key = grants.permission_key
ON CONFLICT DO NOTHING;
