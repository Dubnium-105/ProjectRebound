CREATE TABLE admin_audit_logs (
    id VARCHAR(64) PRIMARY KEY,
    admin_id VARCHAR(128) NOT NULL,
    action VARCHAR(64) NOT NULL,
    target_type VARCHAR(64) NOT NULL,
    target_id VARCHAR(64) NOT NULL,
    old_value JSONB NOT NULL,
    new_value JSONB NOT NULL,
    request_id VARCHAR(128),
    ip_address INET,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX admin_audit_target_idx ON admin_audit_logs (target_type, target_id, created_at DESC);

-- statement-breakpoint
CREATE INDEX admin_audit_admin_idx ON admin_audit_logs (admin_id, created_at DESC);
