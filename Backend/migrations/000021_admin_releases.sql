CREATE TABLE admin_releases (
    id VARCHAR(64) PRIMARY KEY,
    product VARCHAR(64) NOT NULL,
    platform VARCHAR(64) NOT NULL,
    architecture VARCHAR(64) NOT NULL,
    channel VARCHAR(32) NOT NULL,
    version VARCHAR(64) NOT NULL,
    minimum_supported_version VARCHAR(64) NOT NULL,
    force_update BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(32) NOT NULL,
    source_release JSONB NOT NULL,
    signed_manifest JSONB,
    validation_result JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by VARCHAR(64) NOT NULL REFERENCES admin_users(id),
    published_by VARCHAR(64) REFERENCES admin_users(id),
    rolled_back_by VARCHAR(64) REFERENCES admin_users(id),
    archived_by VARCHAR(64) REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    rolled_back_at TIMESTAMPTZ,
    archived_at TIMESTAMPTZ,
    CONSTRAINT admin_release_status
        CHECK (status IN ('DRAFT', 'READY', 'PUBLISHED', 'ROLLED_BACK', 'ARCHIVED')),
    CONSTRAINT admin_release_channel CHECK (channel IN ('stable', 'beta')),
    CONSTRAINT admin_release_identity
        UNIQUE (platform, architecture, channel, version)
);

-- statement-breakpoint
CREATE INDEX admin_releases_status_idx
    ON admin_releases (status, platform, architecture, channel, created_at DESC);

-- statement-breakpoint
CREATE INDEX admin_releases_published_idx
    ON admin_releases (platform, architecture, channel, published_at DESC)
    WHERE status = 'PUBLISHED';
