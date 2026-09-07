-- Strict roster live native nonce.  HOST seats do not consume a JoinGrant,
-- so their authority-start nonce must be persisted on the frozen roster row
-- for world- and handshake-scoped disconnect validation.
ALTER TABLE match_attempt_roster
    ADD COLUMN live_native_connection_nonce VARCHAR(128);

ALTER TABLE match_attempt_roster
    ADD CONSTRAINT match_attempt_roster_live_nonce_format CHECK (
        live_native_connection_nonce IS NULL
        OR live_native_connection_nonce ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$'
    );
