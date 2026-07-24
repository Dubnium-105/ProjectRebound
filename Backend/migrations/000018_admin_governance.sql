ALTER TABLE admin_audit_logs
    ADD COLUMN reason TEXT NOT NULL DEFAULT 'legacy operation',
    ADD COLUMN user_agent TEXT,
    ADD COLUMN result VARCHAR(16) NOT NULL DEFAULT 'SUCCEEDED';

-- statement-breakpoint
ALTER TABLE admin_audit_logs
    ADD CONSTRAINT admin_audit_result_check
    CHECK (result IN ('SUCCEEDED', 'FAILED', 'DENIED'));

-- statement-breakpoint
CREATE TABLE admin_action_requests (
    id VARCHAR(64) PRIMARY KEY,
    action VARCHAR(128) NOT NULL,
    target_type VARCHAR(64) NOT NULL,
    target_id VARCHAR(128) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    reason TEXT NOT NULL,
    requested_by VARCHAR(64) NOT NULL REFERENCES admin_users(id),
    status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    approved_by VARCHAR(64) REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    executed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT admin_action_request_status_check CHECK (
        status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXECUTED', 'FAILED', 'EXPIRED', 'CANCELLED')
    )
);

-- statement-breakpoint
CREATE INDEX admin_action_request_status_idx
    ON admin_action_requests (status, expires_at, created_at DESC);

-- statement-breakpoint
CREATE INDEX admin_action_request_requester_idx
    ON admin_action_requests (requested_by, created_at DESC);

-- statement-breakpoint
CREATE TABLE admin_saved_filters (
    id VARCHAR(64) PRIMARY KEY,
    admin_id VARCHAR(64) NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    resource VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    filter JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (admin_id, resource, name)
);

-- statement-breakpoint
CREATE INDEX admin_saved_filter_admin_resource_idx
    ON admin_saved_filters (admin_id, resource, updated_at DESC);

