CREATE TABLE vnt_security_audit_logs (
    id VARCHAR(64) PRIMARY KEY,
    event_type VARCHAR(64) NOT NULL,
    result VARCHAR(16) NOT NULL,
    actor_type VARCHAR(16) NOT NULL,
    player_id VARCHAR(64) REFERENCES players(id) ON DELETE SET NULL,
    admin_id VARCHAR(128),
    node_id VARCHAR(64),
    room_id VARCHAR(64),
    request_id VARCHAR(128),
    ip_address INET,
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    reason_code VARCHAR(64),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT vnt_security_audit_result CHECK (result IN ('SUCCEEDED','FAILED','DENIED')),
    CONSTRAINT vnt_security_audit_actor CHECK (actor_type IN ('PLAYER','NODE','ADMIN','SYSTEM','UNKNOWN'))
);

-- statement-breakpoint
CREATE INDEX vnt_security_audit_created_idx
    ON vnt_security_audit_logs (created_at DESC, id DESC);

-- statement-breakpoint
CREATE INDEX vnt_security_audit_node_idx
    ON vnt_security_audit_logs (node_id, created_at DESC)
    WHERE node_id IS NOT NULL;

-- statement-breakpoint
CREATE INDEX vnt_security_audit_player_idx
    ON vnt_security_audit_logs (player_id, created_at DESC)
    WHERE player_id IS NOT NULL;

-- statement-breakpoint
CREATE INDEX vnt_security_audit_admin_idx
    ON vnt_security_audit_logs (admin_id, created_at DESC)
    WHERE admin_id IS NOT NULL;

-- statement-breakpoint
CREATE INDEX vnt_security_audit_room_idx
    ON vnt_security_audit_logs (room_id, created_at DESC)
    WHERE room_id IS NOT NULL;
