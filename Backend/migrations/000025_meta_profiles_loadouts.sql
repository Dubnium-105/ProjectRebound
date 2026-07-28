CREATE TABLE meta_player_profiles (
    player_id VARCHAR(64) PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    level INTEGER NOT NULL DEFAULT 1 CHECK (level > 0),
    experience BIGINT NOT NULL DEFAULT 0 CHECK (experience >= 0),
    currencies JSONB NOT NULL DEFAULT '{}'::jsonb,
    statistics JSONB NOT NULL DEFAULT '{}'::jsonb,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT meta_player_profiles_currencies_object CHECK (jsonb_typeof(currencies) = 'object'),
    CONSTRAINT meta_player_profiles_statistics_object CHECK (jsonb_typeof(statistics) = 'object')
);

-- statement-breakpoint
CREATE TABLE meta_role_loadouts (
    player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    role_id VARCHAR(128) NOT NULL,
    snapshot JSONB NOT NULL,
    snapshot_sha256 BYTEA NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (player_id, role_id),
    CONSTRAINT meta_role_loadouts_role_id_format CHECK (role_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    CONSTRAINT meta_role_loadouts_snapshot_object CHECK (jsonb_typeof(snapshot) = 'object'),
    CONSTRAINT meta_role_loadouts_sha256_length CHECK (octet_length(snapshot_sha256) = 32)
);

-- statement-breakpoint
CREATE INDEX meta_role_loadouts_updated_idx
    ON meta_role_loadouts (player_id, updated_at DESC);

-- statement-breakpoint
CREATE TABLE meta_weapon_archives (
    id VARCHAR(64) PRIMARY KEY,
    player_id VARCHAR(64) NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    weapon_id VARCHAR(128) NOT NULL,
    raw_protobuf BYTEA NOT NULL,
    decoded JSONB NOT NULL,
    protobuf_sha256 BYTEA NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT meta_weapon_archives_weapon_id_format CHECK (weapon_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    CONSTRAINT meta_weapon_archives_decoded_object CHECK (jsonb_typeof(decoded) = 'object'),
    CONSTRAINT meta_weapon_archives_sha256_length CHECK (octet_length(protobuf_sha256) = 32),
    CONSTRAINT meta_weapon_archives_size CHECK (octet_length(raw_protobuf) <= 2097152),
    CONSTRAINT meta_weapon_archives_player_weapon_unique UNIQUE (player_id, weapon_id)
);

-- statement-breakpoint
CREATE INDEX meta_weapon_archives_player_idx
    ON meta_weapon_archives (player_id, updated_at DESC);
