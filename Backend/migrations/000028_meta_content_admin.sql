CREATE TABLE meta_notifications (
    id VARCHAR(64) PRIMARY KEY,
    title VARCHAR(256) NOT NULL,
    body TEXT NOT NULL,
    locale VARCHAR(16) NOT NULL DEFAULT 'en',
    priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    starts_at TIMESTAMPTZ,
    ends_at TIMESTAMPTZ,
    created_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT meta_notifications_window CHECK (
        starts_at IS NULL OR ends_at IS NULL OR starts_at < ends_at
    )
);

-- statement-breakpoint
CREATE INDEX meta_notifications_public_idx
    ON meta_notifications (enabled, locale, priority DESC, starts_at, ends_at);

-- statement-breakpoint
CREATE TABLE meta_playlists (
    id VARCHAR(64) PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    display_name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    mode VARCHAR(64) NOT NULL,
    definition JSONB NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT meta_playlists_slug_format CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT meta_playlists_definition_object CHECK (jsonb_typeof(definition) = 'object')
);

-- statement-breakpoint
CREATE INDEX meta_playlists_public_idx
    ON meta_playlists (enabled, sort_order, slug);

-- statement-breakpoint
CREATE TABLE meta_settings (
    setting_key VARCHAR(128) PRIMARY KEY,
    value JSONB NOT NULL,
    updated_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
ALTER TABLE game_servers
    ADD COLUMN token_scopes TEXT[] NOT NULL DEFAULT ARRAY[
        'meta.loadouts.read',
        'meta.matches.connect',
        'meta.matches.complete'
    ]::TEXT[];

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    ('aperm_meta_read', 'meta.read', 'meta', 'read', 'Read MetaServer operational data.', 'LOW'),
    ('aperm_meta_content_manage', 'meta.content.manage', 'meta_content', 'manage', 'Manage MetaServer playlists and notifications.', 'HIGH'),
    ('aperm_meta_matches_manage', 'meta.matches.manage', 'meta_matches', 'manage', 'Cancel or repair MetaServer matches.', 'HIGH'),
    ('aperm_meta_loadouts_read', 'meta.loadouts.read', 'meta_loadouts', 'read', 'Read player MetaServer loadouts.', 'MEDIUM'),
    ('aperm_meta_loadouts_update', 'meta.loadouts.update', 'meta_loadouts', 'update', 'Update player MetaServer loadouts.', 'HIGH')
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
WHERE permission.permission_key LIKE 'meta.%'
ON CONFLICT DO NOTHING;

-- statement-breakpoint
WITH grants(role_name, permission_key) AS (
    VALUES
        ('OPERATIONS', 'meta.read'),
        ('OPERATIONS', 'meta.matches.manage'),
        ('PLAYER_SUPPORT', 'meta.read'),
        ('PLAYER_SUPPORT', 'meta.loadouts.read'),
        ('INFRA_OPERATOR', 'meta.read'),
        ('INFRA_OPERATOR', 'meta.matches.manage'),
        ('AUDITOR', 'meta.read'),
        ('AUDITOR', 'meta.loadouts.read'),
        ('VIEWER', 'meta.read')
)
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM grants
JOIN admin_roles AS role ON role.name = grants.role_name
JOIN admin_permissions AS permission ON permission.permission_key = grants.permission_key
ON CONFLICT DO NOTHING;
