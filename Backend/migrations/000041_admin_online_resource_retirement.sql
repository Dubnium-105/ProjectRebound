ALTER TABLE p2p_rooms
    ADD COLUMN deleted_at TIMESTAMPTZ,
    ADD COLUMN deleted_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN delete_reason VARCHAR(500),
    ADD CONSTRAINT p2p_rooms_deleted_metadata
        CHECK (
            (deleted_at IS NULL AND deleted_by IS NULL AND delete_reason IS NULL) OR
            (deleted_at IS NOT NULL AND deleted_by IS NOT NULL AND delete_reason IS NOT NULL)
        );

-- statement-breakpoint
CREATE INDEX p2p_rooms_admin_visible_idx
    ON p2p_rooms (state, id)
    WHERE deleted_at IS NULL;

-- statement-breakpoint
ALTER TABLE game_servers
    ADD COLUMN banned_at TIMESTAMPTZ,
    ADD COLUMN banned_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN ban_reason VARCHAR(500),
    ADD COLUMN deleted_at TIMESTAMPTZ,
    ADD COLUMN deleted_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN delete_reason VARCHAR(500),
    ADD CONSTRAINT game_servers_banned_metadata
        CHECK (
            (banned_at IS NULL AND banned_by IS NULL AND ban_reason IS NULL) OR
            (banned_at IS NOT NULL AND banned_by IS NOT NULL AND ban_reason IS NOT NULL)
        ),
    ADD CONSTRAINT game_servers_deleted_metadata
        CHECK (
            (deleted_at IS NULL AND deleted_by IS NULL AND delete_reason IS NULL) OR
            (deleted_at IS NOT NULL AND deleted_by IS NOT NULL AND delete_reason IS NOT NULL)
        );

-- statement-breakpoint
CREATE INDEX game_servers_admin_visible_idx
    ON game_servers (state, id)
    WHERE deleted_at IS NULL;

-- statement-breakpoint
CREATE INDEX game_servers_banned_instance_idx
    ON game_servers (instance_id)
    WHERE banned_at IS NOT NULL;

-- statement-breakpoint
INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    ('aperm_rooms_delete', 'rooms.delete', 'rooms', 'delete', '删除已关闭的 P2P 房间目录记录。', 'HIGH'),
    ('aperm_game_servers_delete', 'game_servers.delete', 'game_servers', 'delete', '删除已下线的专服目录记录。', 'HIGH'),
    ('aperm_game_servers_ban', 'game_servers.ban', 'game_servers', 'ban', '封禁专服实例并阻止重新注册。', 'HIGH')
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
WHERE permission.permission_key IN ('rooms.delete', 'game_servers.delete', 'game_servers.ban')
ON CONFLICT DO NOTHING;

-- statement-breakpoint
WITH grants(role_name, permission_key) AS (
    VALUES
        ('OPERATIONS', 'rooms.delete'),
        ('INFRA_OPERATOR', 'game_servers.delete'),
        ('INFRA_OPERATOR', 'game_servers.ban')
)
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM grants
JOIN admin_roles AS role ON role.name = grants.role_name
JOIN admin_permissions AS permission ON permission.permission_key = grants.permission_key
ON CONFLICT DO NOTHING;
