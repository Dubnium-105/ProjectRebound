-- Keep the authorization generation independent from the generation of the
-- native connection that is actually live.  A heartbeat/route refresh may
-- invalidate grants without proving that the native connection disappeared.
ALTER TABLE match_attempt_roster
    ADD COLUMN live_connection_generation INTEGER;

ALTER TABLE match_attempt_roster
    ADD COLUMN live_route_generation INTEGER;

-- This is a durable acknowledgement for the preserved HOST scope.  A route
-- refresh sets it false until the exact old live scope is acknowledged by
-- P2PAuthorityReady; otherwise a repeated heartbeat could advance the same
-- recovery window more than once after Payload installation.
ALTER TABLE match_attempt_roster
    ADD COLUMN host_live_scope_preserved BOOLEAN NOT NULL DEFAULT FALSE;

-- Keep the exact native nonce that produced a DISCONNECTED transition so a
-- retry is idempotent only for that same connection.  Clearing the live
-- nonce alone would let any well-formed nonce replay the old disconnect
-- scope before a replacement connection is installed.
ALTER TABLE match_attempt_roster
    ADD COLUMN last_disconnected_native_connection_nonce VARCHAR(128);

-- Existing schema-47 rows were written before this distinction existed.  A
-- connected row is the only row for which a live generation can be recovered;
-- disconnected/connecting rows remain without a live native connection.
UPDATE match_attempt_roster
SET live_connection_generation = connection_generation,
    live_route_generation = attempts.route_generation,
    -- A schema-47 HOST row may be CONNECTED without the nonce that binds the
    -- native connection.  Preserve its historical generation for diagnosis,
    -- but never mark that scope as proven.  The authority heartbeat rejects
    -- the row closed until an operator/native cleanup path terminates it.
    host_live_scope_preserved = (
        match_attempt_roster.room_role = 'HOST'
        AND NULLIF(match_attempt_roster.live_native_connection_nonce, '') IS NOT NULL
    )
FROM match_attempts AS attempts
WHERE match_attempt_roster.attempt_id = attempts.id
  AND connection_state = 'CONNECTED';

ALTER TABLE match_attempt_roster
    ADD CONSTRAINT match_attempt_roster_live_generation_check CHECK (
        live_connection_generation IS NULL OR live_connection_generation > 0
    );

ALTER TABLE match_attempt_roster
    ADD CONSTRAINT match_attempt_roster_live_route_check CHECK (
        live_route_generation IS NULL OR live_route_generation > 0
    );

ALTER TABLE match_attempt_roster
    ADD CONSTRAINT match_attempt_roster_last_disconnected_nonce_format CHECK (
        last_disconnected_native_connection_nonce IS NULL
        OR last_disconnected_native_connection_nonce ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$'
    );

-- The preserved HOST marker is only an acknowledgement of a still-live
-- native connection.  A missing nonce therefore cannot be promoted into a
-- replacement route or a reusable live scope during/after migration.
ALTER TABLE match_attempt_roster
    ADD CONSTRAINT match_attempt_roster_host_live_scope_nonce CHECK (
        NOT host_live_scope_preserved
        OR (
            room_role = 'HOST'
            AND connection_state = 'CONNECTED'
            AND NULLIF(live_native_connection_nonce, '') IS NOT NULL
            AND live_connection_generation = connection_generation
            AND live_route_generation IS NOT NULL
        )
    );
