CREATE TABLE relay_node_credentials (
    relay_node_id VARCHAR(64) NOT NULL REFERENCES relay_nodes(id) ON DELETE CASCADE,
    certificate_serial VARCHAR(128) NOT NULL,
    certificate_fingerprint VARCHAR(64) UNIQUE NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL,
    not_before TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revocation_reason VARCHAR(128),
    last_rotated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (relay_node_id, certificate_serial)
);

-- statement-breakpoint
CREATE INDEX relay_node_credentials_active_idx ON relay_node_credentials (relay_node_id, expires_at) WHERE revoked_at IS NULL;
