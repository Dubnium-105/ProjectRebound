CREATE TABLE admin_settings (
    setting_key VARCHAR(128) PRIMARY KEY,
    category VARCHAR(32) NOT NULL
        CHECK (category IN ('FEATURES', 'INTEGRATIONS')),
    display_name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    value JSONB NOT NULL,
    value_type VARCHAR(16) NOT NULL
        CHECK (value_type IN ('BOOLEAN', 'URL')),
    editable BOOLEAN NOT NULL DEFAULT TRUE,
    updated_by VARCHAR(64) REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
INSERT INTO admin_settings (
    setting_key, category, display_name, description, value,
    value_type, editable, updated_by, created_at, updated_at
) VALUES
    (
        'features.matchmaking', 'FEATURES', '匹配系统',
        '显示并启用后续匹配管理模块。', 'false'::jsonb,
        'BOOLEAN', TRUE, NULL, NOW(), NOW()
    ),
    (
        'features.parties', 'FEATURES', '组队系统',
        '显示并启用后续组队管理模块。', 'false'::jsonb,
        'BOOLEAN', TRUE, NULL, NOW(), NOW()
    ),
    (
        'features.match_history', 'FEATURES', '战局历史',
        '显示并启用后续战局历史模块。', 'false'::jsonb,
        'BOOLEAN', TRUE, NULL, NOW(), NOW()
    ),
    (
        'features.dual_approval', 'FEATURES', '双人审批',
        '能力预留；第一版仅展示状态，不会绕过单管理员安全确认。', 'false'::jsonb,
        'BOOLEAN', FALSE, NULL, NOW(), NOW()
    ),
    (
        'integrations.grafana_url', 'INTEGRATIONS', 'Grafana Dashboard',
        '只读 Grafana Dashboard 的 HTTPS 地址。', '""'::jsonb,
        'URL', TRUE, NULL, NOW(), NOW()
    ),
    (
        'integrations.runbook_base_url', 'INTEGRATIONS', 'Runbook 入口',
        '异常卡片和运维页面使用的 HTTPS Runbook 根地址。', '""'::jsonb,
        'URL', TRUE, NULL, NOW(), NOW()
    );
