ALTER TABLE game_servers
    ALTER COLUMN token_scopes SET DEFAULT ARRAY[
        'meta.loadouts.read',
        'meta.matches.connect',
        'meta.matches.complete',
        'meta.battlelog.write'
    ]::TEXT[];

-- statement-breakpoint
UPDATE game_servers
SET token_scopes = array_append(token_scopes, 'meta.battlelog.write')
WHERE NOT token_scopes @> ARRAY['meta.battlelog.write']::TEXT[];

-- statement-breakpoint
ALTER TABLE meta_match_players
    ADD COLUMN auth_level_at_reservation VARCHAR(16),
    ADD COLUMN steam_verified_at_reservation BOOLEAN,
    ADD CONSTRAINT meta_match_players_auth_level_at_reservation CHECK (
        auth_level_at_reservation IS NULL OR auth_level_at_reservation IN (
            'unverified', 'verified', 'trusted'
        )
    ),
    ADD CONSTRAINT meta_match_players_steam_verified_at_reservation CHECK (
        (auth_level_at_reservation IS NULL AND steam_verified_at_reservation IS NULL)
        OR (
            auth_level_at_reservation = 'unverified'
            AND steam_verified_at_reservation = FALSE
        )
        OR (
            auth_level_at_reservation IN ('verified', 'trusted')
            AND steam_verified_at_reservation = TRUE
        )
    );

-- statement-breakpoint
UPDATE meta_match_players AS member
SET auth_level_at_reservation = player.auth_level,
    steam_verified_at_reservation = player.auth_level IN ('verified', 'trusted')
FROM players AS player
WHERE player.id = member.player_id
  AND member.auth_level_at_reservation IS NULL;

-- statement-breakpoint
CREATE TABLE battlelog_matches (
    id VARCHAR(64) PRIMARY KEY,
    report_id VARCHAR(128) NOT NULL,
    game_server_id VARCHAR(64) NOT NULL REFERENCES game_servers(id),
    meta_match_id VARCHAR(64) REFERENCES meta_matches(id),
    source_match_id VARCHAR(128),
    match_id_source VARCHAR(32) NOT NULL,
    schema_name VARCHAR(128) NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    match_type VARCHAR(16) NOT NULL,
    validation_status VARCHAR(32) NOT NULL,
    risk_severity VARCHAR(16),
    is_official BOOLEAN NOT NULL DEFAULT FALSE,
    mode_alias VARCHAR(128) NOT NULL DEFAULT '',
    map_alias VARCHAR(128) NOT NULL DEFAULT '',
    map_display_name VARCHAR(256) NOT NULL DEFAULT '',
    winner_team_id INTEGER,
    duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    captured_at TIMESTAMPTZ NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL,
    raw_snapshot JSONB NOT NULL,
    raw_sha256 BYTEA NOT NULL,
    validation_warnings JSONB NOT NULL DEFAULT '[]'::jsonb,
    CONSTRAINT battlelog_matches_report_unique UNIQUE (game_server_id, report_id),
    CONSTRAINT battlelog_matches_meta_match_unique UNIQUE (meta_match_id),
    CONSTRAINT battlelog_matches_report_id_format CHECK (
        report_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
    ),
    CONSTRAINT battlelog_matches_match_id_source CHECK (
        match_id_source IN ('SNAPSHOT', 'ACTIVE_ASSIGNMENT', 'STANDALONE')
    ),
    CONSTRAINT battlelog_matches_type CHECK (
        match_type IN ('PVE', 'PVP', 'UNKNOWN')
    ),
    CONSTRAINT battlelog_matches_validation_status CHECK (
        validation_status IN (
            'ACCEPTED', 'ACCEPTED_WITH_WARNINGS', 'QUARANTINED'
        )
    ),
    CONSTRAINT battlelog_matches_risk_severity CHECK (
        risk_severity IS NULL OR risk_severity IN (
            'LOW', 'MEDIUM', 'HIGH', 'CRITICAL'
        )
    ),
    CONSTRAINT battlelog_matches_raw_object CHECK (
        jsonb_typeof(raw_snapshot) = 'object'
    ),
    CONSTRAINT battlelog_matches_raw_sha256_length CHECK (
        octet_length(raw_sha256) = 32
    ),
    CONSTRAINT battlelog_matches_warnings_array CHECK (
        jsonb_typeof(validation_warnings) = 'array'
    )
);

-- statement-breakpoint
CREATE INDEX battlelog_matches_player_history_idx
    ON battlelog_matches (match_type, captured_at DESC, id)
    WHERE validation_status <> 'QUARANTINED';

-- statement-breakpoint
CREATE INDEX battlelog_matches_pve_idx
    ON battlelog_matches (captured_at DESC, id)
    WHERE match_type = 'PVE';

-- statement-breakpoint
CREATE INDEX battlelog_matches_pvp_idx
    ON battlelog_matches (captured_at DESC, id)
    WHERE match_type = 'PVP';

-- statement-breakpoint
CREATE INDEX battlelog_matches_quarantine_idx
    ON battlelog_matches (risk_severity, reported_at DESC, id)
    WHERE validation_status = 'QUARANTINED';

-- statement-breakpoint
CREATE TABLE battlelog_teams (
    match_id VARCHAR(64) NOT NULL REFERENCES battlelog_matches(id) ON DELETE CASCADE,
    team_id INTEGER NOT NULL,
    outcome VARCHAR(16) NOT NULL,
    match_score INTEGER,
    kills INTEGER NOT NULL DEFAULT 0 CHECK (kills >= 0),
    deaths INTEGER NOT NULL DEFAULT 0 CHECK (deaths >= 0),
    assists INTEGER NOT NULL DEFAULT 0 CHECK (assists >= 0),
    score DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (score >= 0),
    human_count INTEGER NOT NULL DEFAULT 0 CHECK (human_count >= 0),
    ai_count INTEGER NOT NULL DEFAULT 0 CHECK (ai_count >= 0),
    PRIMARY KEY (match_id, team_id),
    CONSTRAINT battlelog_teams_outcome CHECK (
        outcome IN ('WON', 'LOST', 'DRAW', 'UNKNOWN')
    )
);

-- statement-breakpoint
CREATE TABLE battlelog_participants (
    id VARCHAR(64) PRIMARY KEY,
    match_id VARCHAR(64) NOT NULL REFERENCES battlelog_matches(id) ON DELETE CASCADE,
    slot_index INTEGER NOT NULL CHECK (slot_index >= 0),
    player_id VARCHAR(64) REFERENCES players(id),
    platform_id VARCHAR(20),
    player_name VARCHAR(256) NOT NULL DEFAULT '',
    is_ai BOOLEAN NOT NULL,
    roster_verified BOOLEAN NOT NULL DEFAULT FALSE,
    official_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    auth_level_at_match VARCHAR(16),
    steam_verified_at_match BOOLEAN NOT NULL DEFAULT FALSE,
    team_id INTEGER,
    camp_id INTEGER,
    role_name VARCHAR(128) NOT NULL DEFAULT '',
    role_value INTEGER,
    selected_character_id VARCHAR(128) NOT NULL DEFAULT '',
    possessed_character_id VARCHAR(128) NOT NULL DEFAULT '',
    is_spectator BOOLEAN NOT NULL DEFAULT FALSE,
    is_inactive BOOLEAN NOT NULL DEFAULT FALSE,
    is_quitter BOOLEAN NOT NULL DEFAULT FALSE,
    outcome VARCHAR(16) NOT NULL,
    is_match_mvp BOOLEAN NOT NULL DEFAULT FALSE,
    raw_player JSONB NOT NULL,
    CONSTRAINT battlelog_participants_slot_unique UNIQUE (match_id, slot_index),
    CONSTRAINT battlelog_participants_player_unique UNIQUE (match_id, player_id),
    CONSTRAINT battlelog_participants_auth_level CHECK (
        auth_level_at_match IS NULL OR auth_level_at_match IN (
            'unverified', 'verified', 'trusted'
        )
    ),
    CONSTRAINT battlelog_participants_steam_verified CHECK (
        (auth_level_at_match IS NULL AND steam_verified_at_match = FALSE)
        OR (auth_level_at_match = 'unverified' AND steam_verified_at_match = FALSE)
        OR (auth_level_at_match IN ('verified', 'trusted') AND steam_verified_at_match = TRUE)
    ),
    CONSTRAINT battlelog_participants_outcome CHECK (
        outcome IN ('WON', 'LOST', 'DRAW', 'UNKNOWN')
    ),
    CONSTRAINT battlelog_participants_raw_object CHECK (
        jsonb_typeof(raw_player) = 'object'
    )
);

-- statement-breakpoint
CREATE INDEX battlelog_participants_player_history_idx
    ON battlelog_participants (player_id, match_id)
    WHERE player_id IS NOT NULL;

-- statement-breakpoint
CREATE INDEX battlelog_participants_match_team_idx
    ON battlelog_participants (match_id, team_id, slot_index);

-- statement-breakpoint
CREATE TABLE battlelog_participant_stats (
    participant_id VARCHAR(64) PRIMARY KEY
        REFERENCES battlelog_participants(id) ON DELETE CASCADE,
    kills INTEGER NOT NULL DEFAULT 0 CHECK (kills >= 0),
    deaths INTEGER NOT NULL DEFAULT 0 CHECK (deaths >= 0),
    assists INTEGER NOT NULL DEFAULT 0 CHECK (assists >= 0),
    score DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (score >= 0),
    team_score DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (team_score >= 0),
    headshot_count INTEGER NOT NULL DEFAULT 0 CHECK (headshot_count >= 0),
    bullets_fired INTEGER NOT NULL DEFAULT 0 CHECK (bullets_fired >= 0),
    rockets_fired INTEGER NOT NULL DEFAULT 0 CHECK (rockets_fired >= 0),
    max_kill_distance DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (max_kill_distance >= 0),
    avg_kill_distance DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (avg_kill_distance >= 0),
    max_kill_streak INTEGER NOT NULL DEFAULT 0 CHECK (max_kill_streak >= 0),
    killing_streak_count INTEGER NOT NULL DEFAULT 0 CHECK (killing_streak_count >= 0),
    ping_ms DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (ping_ms >= 0),
    reported_kda DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (reported_kda >= 0),
    calculated_kda DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (calculated_kda >= 0),
    reported_spm DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (reported_spm >= 0),
    calculated_spm DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (calculated_spm >= 0),
    reported_accuracy DOUBLE PRECISION CHECK (
        reported_accuracy IS NULL OR reported_accuracy >= 0
    ),
    playing_time_ms BIGINT NOT NULL DEFAULT 0 CHECK (playing_time_ms >= 0)
);

-- statement-breakpoint
CREATE TABLE battlelog_rounds (
    match_id VARCHAR(64) NOT NULL REFERENCES battlelog_matches(id) ON DELETE CASCADE,
    round_index INTEGER NOT NULL CHECK (round_index >= 0),
    winner_team_id INTEGER,
    is_final_round BOOLEAN NOT NULL DEFAULT FALSE,
    team_scores JSONB NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (match_id, round_index),
    CONSTRAINT battlelog_rounds_scores_array CHECK (
        jsonb_typeof(team_scores) = 'array'
    )
);

-- statement-breakpoint
CREATE TABLE battlelog_score_breakdowns (
    participant_id VARCHAR(64) NOT NULL
        REFERENCES battlelog_participants(id) ON DELETE CASCADE,
    category VARCHAR(16) NOT NULL,
    score_key VARCHAR(128) NOT NULL,
    score DOUBLE PRECISION NOT NULL CHECK (score >= 0),
    PRIMARY KEY (participant_id, category, score_key),
    CONSTRAINT battlelog_score_breakdowns_category CHECK (
        category IN ('CHARACTER', 'ROLE')
    )
);

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    (
        'aperm_meta_battlelog_read',
        'meta.battlelog.read',
        'meta_battlelog',
        'read',
        'Read normalized BattleLog match and participant records.',
        'LOW'
    ),
    (
        'aperm_meta_battlelog_raw_read',
        'meta.battlelog.raw.read',
        'meta_battlelog',
        'raw_read',
        'Read raw BattleLog SDK snapshots containing diagnostic identity data.',
        'MEDIUM'
    ),
    (
        'aperm_meta_battlelog_manage',
        'meta.battlelog.manage',
        'meta_battlelog',
        'manage',
        'Quarantine, reprocess, or invalidate BattleLog records.',
        'HIGH'
    )
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
WHERE permission.permission_key LIKE 'meta.battlelog.%'
ON CONFLICT DO NOTHING;

-- statement-breakpoint
WITH grants(role_name, permission_key) AS (
    VALUES
        ('OPERATIONS', 'meta.battlelog.read'),
        ('PLAYER_SUPPORT', 'meta.battlelog.read'),
        ('AUDITOR', 'meta.battlelog.read'),
        ('AUDITOR', 'meta.battlelog.raw.read'),
        ('VIEWER', 'meta.battlelog.read')
)
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM grants
JOIN admin_roles AS role ON role.name = grants.role_name
JOIN admin_permissions AS permission ON permission.permission_key = grants.permission_key
ON CONFLICT DO NOTHING;
