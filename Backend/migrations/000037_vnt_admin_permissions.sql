INSERT INTO admin_permissions (
    id, permission_key, resource, action, description, risk_level
) VALUES
    ('aperm_vnt_nodes_read', 'vnt_nodes.read', 'vnt_nodes', 'read', '查看 VNT 节点。', 'LOW'),
    ('aperm_vnt_nodes_drain', 'vnt_nodes.drain', 'vnt_nodes', 'drain', '将 VNT 节点置为排空状态。', 'HIGH'),
    ('aperm_vnt_nodes_revoke', 'vnt_nodes.revoke', 'vnt_nodes', 'revoke', '立即吊销 VNT 节点及其凭据。', 'HIGH')
ON CONFLICT (permission_key) DO NOTHING;

-- statement-breakpoint
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT 'arol_super_admin', permission.id, NOW()
FROM admin_permissions AS permission
WHERE permission.resource = 'vnt_nodes'
ON CONFLICT DO NOTHING;

-- statement-breakpoint
WITH grants(role_name, permission_key) AS (
    VALUES
        ('OPERATIONS', 'vnt_nodes.read'),
        ('OPERATIONS', 'vnt_nodes.drain'),
        ('INFRA_OPERATOR', 'vnt_nodes.read'),
        ('INFRA_OPERATOR', 'vnt_nodes.drain'),
        ('INFRA_OPERATOR', 'vnt_nodes.revoke'),
        ('AUDITOR', 'vnt_nodes.read'),
        ('VIEWER', 'vnt_nodes.read')
)
INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
SELECT role.id, permission.id, NOW()
FROM grants
JOIN admin_roles AS role ON role.name = grants.role_name
JOIN admin_permissions AS permission ON permission.permission_key = grants.permission_key
ON CONFLICT DO NOTHING;
