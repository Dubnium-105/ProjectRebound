CREATE TABLE relay_signing_keys (
    key_id VARCHAR(128) PRIMARY KEY,
    public_key TEXT NOT NULL,
    encrypted_private_key_reference TEXT NOT NULL,
    status VARCHAR(16) NOT NULL,
    not_before TIMESTAMPTZ NOT NULL,
    sign_until TIMESTAMPTZ,
    verify_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    retired_at TIMESTAMPTZ,
    CONSTRAINT relay_signing_keys_status CHECK (status IN ('PENDING', 'ACTIVE', 'VERIFY_ONLY', 'RETIRED', 'REVOKED'))
);

-- statement-breakpoint
CREATE UNIQUE INDEX relay_signing_keys_one_active_idx ON relay_signing_keys (status) WHERE status = 'ACTIVE';

-- statement-breakpoint
CREATE TABLE relay_keyset_acks (
    relay_node_id VARCHAR(64) NOT NULL REFERENCES relay_nodes(id) ON DELETE CASCADE,
    keyset_version BIGINT NOT NULL,
    acknowledged_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (relay_node_id, keyset_version)
);
