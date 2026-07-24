INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    ('aperm_rooms_read', 'rooms.read', 'rooms', 'read', '查看 P2P 房间。', 'LOW'),
    ('aperm_rooms_close', 'rooms.close', 'rooms', 'close', '关闭 P2P 房间。', 'HIGH'),
    ('aperm_rooms_remove_member', 'rooms.remove_member', 'rooms', 'remove_member', '移出 P2P 房间成员。', 'HIGH'),
    ('aperm_relay_nodes_rotate_certificate', 'relay_nodes.rotate_certificate', 'relay_nodes', 'rotate_certificate', '续期或撤销中继节点证书。', 'HIGH')
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
ON CONFLICT DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_roles (
    id, name, display_name, description, system_role, created_at, updated_at
) VALUES
    ('arol_operations', 'OPERATIONS', '运营', '房间、服务器、Relay 与日常运营。', TRUE, NOW(), NOW()),
    ('arol_player_support', 'PLAYER_SUPPORT', '玩家客服', '玩家、Session、风险与邀请码运营。', TRUE, NOW(), NOW()),
    ('arol_release_manager', 'RELEASE_MANAGER', '发布管理员', '客户端版本校验、发布与回滚。', TRUE, NOW(), NOW()),
    ('arol_infra_operator', 'INFRA_OPERATOR', '基础设施运维', '专服、Relay、Connection 与运行状态。', TRUE, NOW(), NOW()),
    ('arol_auditor', 'AUDITOR', '审计员', '只读风险、登录与操作审计。', TRUE, NOW(), NOW()),
    ('arol_viewer', 'VIEWER', '只读查看', '只读查看基本运营状态。', TRUE, NOW(), NOW())
ON CONFLICT (name) DO NOTHING;

-- statement-breakpoint
WITH grants(role_name, permission_key) AS (
    VALUES
        ('OPERATIONS', 'dashboard.read'),
        ('OPERATIONS', 'invite_codes.read'),
        ('OPERATIONS', 'invite_codes.create'),
        ('OPERATIONS', 'invite_codes.update'),
        ('OPERATIONS', 'rooms.read'),
        ('OPERATIONS', 'rooms.close'),
        ('OPERATIONS', 'rooms.remove_member'),
        ('OPERATIONS', 'game_servers.read'),
        ('OPERATIONS', 'game_servers.drain'),
        ('OPERATIONS', 'relay_nodes.read'),
        ('OPERATIONS', 'relay_nodes.drain'),
        ('OPERATIONS', 'relay_nodes.resume'),
        ('OPERATIONS', 'connections.read'),
        ('OPERATIONS', 'connections.migrate'),
        ('OPERATIONS', 'connections.close'),
        ('OPERATIONS', 'settings.read'),

        ('PLAYER_SUPPORT', 'dashboard.read'),
        ('PLAYER_SUPPORT', 'players.read'),
        ('PLAYER_SUPPORT', 'players.update_status'),
        ('PLAYER_SUPPORT', 'players.update_vip'),
        ('PLAYER_SUPPORT', 'players.revoke_sessions'),
        ('PLAYER_SUPPORT', 'risk_events.read'),
        ('PLAYER_SUPPORT', 'risk_events.resolve'),
        ('PLAYER_SUPPORT', 'invite_codes.read'),

        ('RELEASE_MANAGER', 'dashboard.read'),
        ('RELEASE_MANAGER', 'updates.read'),
        ('RELEASE_MANAGER', 'updates.create'),
        ('RELEASE_MANAGER', 'updates.publish'),
        ('RELEASE_MANAGER', 'updates.rollback'),

        ('INFRA_OPERATOR', 'dashboard.read'),
        ('INFRA_OPERATOR', 'game_servers.read'),
        ('INFRA_OPERATOR', 'game_servers.drain'),
        ('INFRA_OPERATOR', 'game_servers.disable'),
        ('INFRA_OPERATOR', 'relay_nodes.read'),
        ('INFRA_OPERATOR', 'relay_nodes.drain'),
        ('INFRA_OPERATOR', 'relay_nodes.resume'),
        ('INFRA_OPERATOR', 'relay_nodes.revoke'),
        ('INFRA_OPERATOR', 'relay_nodes.rotate_certificate'),
        ('INFRA_OPERATOR', 'connections.read'),
        ('INFRA_OPERATOR', 'connections.migrate'),
        ('INFRA_OPERATOR', 'connections.close'),
        ('INFRA_OPERATOR', 'settings.read'),

        ('AUDITOR', 'dashboard.read'),
        ('AUDITOR', 'risk_events.read'),
        ('AUDITOR', 'admins.read'),
        ('AUDITOR', 'audit_logs.read'),
        ('AUDITOR', 'settings.read'),

        ('VIEWER', 'dashboard.read'),
        ('VIEWER', 'players.read'),
        ('VIEWER', 'invite_codes.read'),
        ('VIEWER', 'rooms.read'),
        ('VIEWER', 'game_servers.read'),
        ('VIEWER', 'relay_nodes.read'),
        ('VIEWER', 'connections.read'),
        ('VIEWER', 'updates.read'),
        ('VIEWER', 'settings.read')
)
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM grants
JOIN admin_roles AS role ON role.name = grants.role_name
JOIN admin_permissions AS permission ON permission.permission_key = grants.permission_key
ON CONFLICT DO NOTHING;
