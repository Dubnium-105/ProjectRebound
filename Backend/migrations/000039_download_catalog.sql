CREATE TABLE download_categories (
    id VARCHAR(64) PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    title_en VARCHAR(128) NOT NULL,
    title_zh_cn VARCHAR(128) NOT NULL,
    description_en TEXT NOT NULL DEFAULT '',
    description_zh_cn TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    created_by VARCHAR(64) NOT NULL REFERENCES admin_users(id),
    archived_by VARCHAR(64) REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    archived_at TIMESTAMPTZ,
    CONSTRAINT download_category_slug CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    CONSTRAINT download_category_archive_fields CHECK (
        (status = 'ACTIVE' AND archived_by IS NULL AND archived_at IS NULL) OR
        (status = 'ARCHIVED' AND archived_by IS NOT NULL AND archived_at IS NOT NULL)
    )
);

-- statement-breakpoint
CREATE INDEX download_categories_public_idx
    ON download_categories (sort_order, slug)
    WHERE status = 'ACTIVE' AND enabled;

-- statement-breakpoint
CREATE TABLE download_entries (
    id VARCHAR(64) PRIMARY KEY,
    category_id VARCHAR(64) NOT NULL REFERENCES download_categories(id),
    slug VARCHAR(64) NOT NULL UNIQUE,
    title_en VARCHAR(128) NOT NULL,
    title_zh_cn VARCHAR(128) NOT NULL,
    description_en TEXT NOT NULL DEFAULT '',
    description_zh_cn TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    created_by VARCHAR(64) NOT NULL REFERENCES admin_users(id),
    archived_by VARCHAR(64) REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    archived_at TIMESTAMPTZ,
    CONSTRAINT download_entry_slug CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    CONSTRAINT download_entry_archive_fields CHECK (
        (status = 'ACTIVE' AND archived_by IS NULL AND archived_at IS NULL) OR
        (status = 'ARCHIVED' AND archived_by IS NOT NULL AND archived_at IS NOT NULL)
    )
);

-- statement-breakpoint
CREATE INDEX download_entries_category_idx
    ON download_entries (category_id, sort_order, slug);

-- statement-breakpoint
CREATE TABLE download_versions (
    id VARCHAR(64) PRIMARY KEY,
    entry_id VARCHAR(64) NOT NULL REFERENCES download_entries(id),
    version_label VARCHAR(64) NOT NULL,
    original_file_name VARCHAR(255) NOT NULL,
    content_type VARCHAR(255) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256 CHAR(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    object_key TEXT NOT NULL UNIQUE,
    status VARCHAR(16) NOT NULL DEFAULT 'UPLOADING'
        CHECK (status IN ('UPLOADING', 'VERIFYING', 'DRAFT', 'PUBLISHED', 'ARCHIVED', 'FAILED')),
    failure_reason TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(64) NOT NULL REFERENCES admin_users(id),
    published_by VARCHAR(64) REFERENCES admin_users(id),
    archived_by VARCHAR(64) REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    verified_at TIMESTAMPTZ,
    verification_lease_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    archived_at TIMESTAMPTZ,
    CONSTRAINT download_version_draft_verified CHECK (
        status <> 'DRAFT' OR verified_at IS NOT NULL
    ),
    CONSTRAINT download_version_publish_fields CHECK (
        status <> 'PUBLISHED' OR
        (verified_at IS NOT NULL AND published_by IS NOT NULL AND published_at IS NOT NULL)
    ),
    CONSTRAINT download_version_archive_fields CHECK (
        status <> 'ARCHIVED' OR (archived_by IS NOT NULL AND archived_at IS NOT NULL)
    ),
    CONSTRAINT download_version_failure_fields CHECK (
        status <> 'FAILED' OR failure_reason <> ''
    )
);

-- statement-breakpoint
CREATE UNIQUE INDEX download_versions_entry_label_idx
    ON download_versions (entry_id, LOWER(version_label));

-- statement-breakpoint
CREATE INDEX download_versions_public_idx
    ON download_versions (entry_id, published_at DESC, id)
    WHERE status = 'PUBLISHED';

-- statement-breakpoint
CREATE INDEX download_versions_verification_idx
    ON download_versions (status, verification_lease_until, updated_at)
    WHERE status = 'VERIFYING';

-- statement-breakpoint
CREATE TABLE download_upload_sessions (
    id VARCHAR(64) PRIMARY KEY,
    version_id VARCHAR(64) NOT NULL UNIQUE REFERENCES download_versions(id) ON DELETE CASCADE,
    strategy VARCHAR(16) NOT NULL CHECK (strategy IN ('SINGLE', 'MULTIPART')),
    provider_upload_id TEXT,
    part_size_bytes BIGINT NOT NULL CHECK (part_size_bytes > 0),
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'COMPLETED', 'ABORTED', 'EXPIRED')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT download_upload_provider_id CHECK (
        (strategy = 'SINGLE' AND provider_upload_id IS NULL) OR
        (strategy = 'MULTIPART' AND provider_upload_id IS NOT NULL AND provider_upload_id <> '')
    )
);

-- statement-breakpoint
CREATE INDEX download_upload_sessions_expiry_idx
    ON download_upload_sessions (status, expires_at)
    WHERE status = 'ACTIVE';

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    ('aperm_downloads_read', 'downloads.read', 'downloads', 'read', '查看下载分类、项目、版本和上传状态。', 'LOW'),
    ('aperm_downloads_create', 'downloads.create', 'downloads', 'create', '创建下载项目、版本和上传会话。', 'MEDIUM'),
    ('aperm_downloads_update', 'downloads.update', 'downloads', 'update', '修改下载分类和项目元数据。', 'MEDIUM'),
    ('aperm_downloads_publish', 'downloads.publish', 'downloads', 'publish', '向公共下载目录发布已校验文件。', 'HIGH'),
    ('aperm_downloads_archive', 'downloads.archive', 'downloads', 'archive', '归档公共下载项目或文件版本。', 'HIGH')
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
WHERE permission.resource = 'downloads'
ON CONFLICT DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM admin_roles AS role
JOIN admin_permissions AS permission ON permission.resource = 'downloads'
WHERE role.name = 'RELEASE_MANAGER'
ON CONFLICT DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM admin_roles AS role
JOIN admin_permissions AS permission ON permission.permission_key = 'downloads.read'
WHERE role.name = 'VIEWER'
ON CONFLICT DO NOTHING;
