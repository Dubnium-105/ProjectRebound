CREATE TABLE relay_bootstrap_tokens (
    id VARCHAR(128) PRIMARY KEY,
    token_hash BYTEA UNIQUE NOT NULL,
    consumed_at TIMESTAMPTZ,
    consumed_by_node_id VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE TABLE relay_nodes (
    id VARCHAR(64) PRIMARY KEY,
    display_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    zone VARCHAR(64) NOT NULL,
    provider VARCHAR(64) NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'BOOTSTRAPPING',
    software_version VARCHAR(64) NOT NULL,
    protocol_version INTEGER NOT NULL,
    public_endpoints JSONB NOT NULL,
    supported_protocols TEXT[] NOT NULL,
    max_allocations INTEGER NOT NULL,
    max_egress_bps BIGINT NOT NULL,
    active_allocations INTEGER NOT NULL DEFAULT 0,
    current_egress_bps BIGINT NOT NULL DEFAULT 0,
    current_ingress_bps BIGINT NOT NULL DEFAULT 0,
    certificate_fingerprint VARCHAR(64) UNIQUE NOT NULL,
    certificate_expires_at TIMESTAMPTZ NOT NULL,
    node_token_hash BYTEA UNIQUE NOT NULL,
    config_version BIGINT NOT NULL DEFAULT 1,
    last_heartbeat_at TIMESTAMPTZ,
    lease_expires_at TIMESTAMPTZ,
    drain_deadline TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT relay_nodes_state CHECK (state IN ('BOOTSTRAPPING', 'CONNECTING', 'READY', 'DRAINING', 'UNHEALTHY', 'OFFLINE', 'REVOKED')),
    CONSTRAINT relay_nodes_protocol_version CHECK (protocol_version > 0),
    CONSTRAINT relay_nodes_capacity CHECK (max_allocations > 0 AND max_egress_bps > 0),
    CONSTRAINT relay_nodes_active_allocations CHECK (active_allocations >= 0 AND active_allocations <= max_allocations),
    CONSTRAINT relay_nodes_bandwidth CHECK (current_egress_bps >= 0 AND current_ingress_bps >= 0)
);

-- statement-breakpoint
ALTER TABLE relay_bootstrap_tokens
    ADD CONSTRAINT relay_bootstrap_node_fk FOREIGN KEY (consumed_by_node_id) REFERENCES relay_nodes(id);

-- statement-breakpoint
CREATE INDEX relay_nodes_scheduler_idx ON relay_nodes (state, region, active_allocations, current_egress_bps);

-- statement-breakpoint
CREATE INDEX relay_nodes_heartbeat_idx ON relay_nodes (last_heartbeat_at) WHERE state NOT IN ('OFFLINE', 'REVOKED');

-- statement-breakpoint
CREATE TABLE relay_allocations (
    id VARCHAR(64) PRIMARY KEY,
    connection_id VARCHAR(64) NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    room_id VARCHAR(64) NOT NULL REFERENCES p2p_rooms(id),
    relay_node_id VARCHAR(64) NOT NULL REFERENCES relay_nodes(id),
    state VARCHAR(16) NOT NULL DEFAULT 'ALLOCATED',
    protocol VARCHAR(16) NOT NULL,
    max_bps BIGINT NOT NULL,
    max_pps INTEGER NOT NULL,
    max_total_bytes BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    CONSTRAINT relay_allocations_state CHECK (state IN ('ALLOCATED', 'BINDING', 'ACTIVE', 'CLOSED', 'FAILED')),
    CONSTRAINT relay_allocations_protocol CHECK (protocol IN ('UDP', 'TCP_TLS')),
    CONSTRAINT relay_allocations_limits CHECK (max_bps > 0 AND max_pps > 0 AND max_total_bytes > 0)
);

-- statement-breakpoint
CREATE UNIQUE INDEX relay_allocations_active_connection_idx
    ON relay_allocations (connection_id)
    WHERE state IN ('ALLOCATED', 'BINDING', 'ACTIVE');

-- statement-breakpoint
CREATE INDEX relay_allocations_node_idx ON relay_allocations (relay_node_id, state, expires_at);

-- statement-breakpoint
CREATE TABLE relay_node_audit_logs (
    id VARCHAR(64) PRIMARY KEY,
    node_id VARCHAR(64) NOT NULL REFERENCES relay_nodes(id),
    actor_id VARCHAR(128) NOT NULL,
    action VARCHAR(32) NOT NULL,
    old_state VARCHAR(16),
    new_state VARCHAR(16),
    request_id VARCHAR(128),
    ip_address INET,
    created_at TIMESTAMPTZ NOT NULL
);

-- statement-breakpoint
CREATE INDEX relay_node_audit_node_idx ON relay_node_audit_logs (node_id, created_at DESC);
