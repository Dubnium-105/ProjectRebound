ALTER TABLE game_servers
    ADD COLUMN certificate_fingerprint VARCHAR(64),
    ADD COLUMN certificate_public_key BYTEA,
    ADD COLUMN certificate_serial VARCHAR(64),
    ADD COLUMN certificate_expires_at TIMESTAMPTZ,
    ADD COLUMN previous_certificate_fingerprint VARCHAR(64),
    ADD COLUMN previous_certificate_public_key BYTEA,
    ADD COLUMN previous_certificate_expires_at TIMESTAMPTZ,
    ADD COLUMN legacy_auth_expires_at TIMESTAMPTZ,
    ADD CONSTRAINT game_servers_certificate_fingerprint_shape
        CHECK (certificate_fingerprint IS NULL OR certificate_fingerprint ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT game_servers_certificate_public_key_length
        CHECK (certificate_public_key IS NULL OR octet_length(certificate_public_key) = 32),
    ADD CONSTRAINT game_servers_certificate_pair
        CHECK (
            (certificate_fingerprint IS NULL) = (certificate_public_key IS NULL) AND
            (certificate_public_key IS NULL) = (certificate_expires_at IS NULL)
        ),
    ADD CONSTRAINT game_servers_previous_certificate_fingerprint_shape
        CHECK (previous_certificate_fingerprint IS NULL OR previous_certificate_fingerprint ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT game_servers_previous_certificate_public_key_length
        CHECK (previous_certificate_public_key IS NULL OR octet_length(previous_certificate_public_key) = 32),
    ADD CONSTRAINT game_servers_previous_certificate_pair
        CHECK (
            (previous_certificate_fingerprint IS NULL) = (previous_certificate_public_key IS NULL) AND
            (previous_certificate_public_key IS NULL) = (previous_certificate_expires_at IS NULL)
        );

-- statement-breakpoint
UPDATE game_servers
SET legacy_auth_expires_at = NOW() + INTERVAL '24 hours'
WHERE certificate_public_key IS NULL;

-- statement-breakpoint
CREATE TABLE game_server_request_nonces (
    server_id VARCHAR(64) NOT NULL REFERENCES game_servers(id) ON DELETE CASCADE,
    nonce_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (server_id, nonce_hash),
    CONSTRAINT game_server_request_nonce_hash_length
        CHECK (octet_length(nonce_hash) = 32),
    CONSTRAINT game_server_request_nonce_expiry
        CHECK (expires_at > created_at)
);

-- statement-breakpoint
CREATE INDEX game_server_request_nonces_expiry_idx
    ON game_server_request_nonces (expires_at);
