-- Strict-roster runtime capabilities, admission intent idempotency, and
-- cleanup leases.  This migration is additive; it never rewrites or drops
-- existing roster data.

ALTER TABLE game_servers
    DROP CONSTRAINT game_servers_state;

-- A server remains isolated while its native process/transport cleanup is
-- pending.  READY is granted only by the NativeCleared acknowledgement.
ALTER TABLE game_servers
    ADD CONSTRAINT game_servers_state CHECK (state IN (
        'STARTING', 'READY', 'RESERVED', 'RUNNING', 'CLEANUP_PENDING',
        'DRAINING', 'UNHEALTHY', 'OFFLINE'
    ));

ALTER TABLE game_servers
    ADD COLUMN native_admission_verified BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN native_admission_version VARCHAR(64),
    ADD COLUMN native_admission_game_sha256 VARCHAR(64),
    ADD COLUMN native_admission_verified_at TIMESTAMPTZ;

ALTER TABLE game_servers
    ADD CONSTRAINT game_servers_native_admission_attestation CHECK (
        NOT native_admission_verified
        OR (
            native_admission_version IS NOT NULL
            AND native_admission_game_sha256 IS NOT NULL
            AND native_admission_game_sha256 ~ '^[0-9a-f]{64}$'
            AND native_admission_verified_at IS NOT NULL
        )
    );

CREATE INDEX game_servers_strict_admission_idx
    ON game_servers (state, mode, version, region, native_admission_verified,
                     native_admission_game_sha256, last_heartbeat_at, id);

ALTER TABLE match_attempts
    ADD COLUMN cleanup_state VARCHAR(16) NOT NULL DEFAULT 'CLEARED',
    ADD COLUMN cleanup_requested_at TIMESTAMPTZ,
    ADD COLUMN native_cleared_at TIMESTAMPTZ,
    ADD COLUMN cleanup_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN cleanup_error VARCHAR(256),
    ADD COLUMN transport_last_seen_at TIMESTAMPTZ,
    ADD COLUMN transport_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN world_instance_id VARCHAR(128);

ALTER TABLE match_attempts
    ADD CONSTRAINT match_attempts_cleanup_state CHECK (
        cleanup_state IN ('CLEARED', 'PENDING')
    );

CREATE INDEX match_attempts_cleanup_pending_idx
    ON match_attempts (cleanup_state, cleanup_lease_expires_at, updated_at)
    WHERE cleanup_state = 'PENDING';

ALTER TABLE match_admission_grants
    ADD COLUMN idempotency_key VARCHAR(128),
    ADD COLUMN idempotency_request_hash BYTEA;

ALTER TABLE match_admission_grants
    ADD CONSTRAINT match_admission_grants_idempotency_hash CHECK (
        (idempotency_key IS NULL AND idempotency_request_hash IS NULL)
        OR (
            idempotency_key IS NOT NULL
            AND idempotency_request_hash IS NOT NULL
            AND octet_length(idempotency_request_hash) = 32
        )
    );

CREATE UNIQUE INDEX match_admission_grants_intent_idx
    ON match_admission_grants (attempt_id, player_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX match_admission_grants_cleanup_idx
    ON match_admission_grants (attempt_id, revoked_at, consumed_at, expires_at);
