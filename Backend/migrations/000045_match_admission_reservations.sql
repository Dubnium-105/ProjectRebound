-- Short-lived native admission reservations.  A reservation is distinct from
-- delivery and connection: it may never mark a roster member CONNECTED.
ALTER TABLE match_admission_grants
    ADD COLUMN reserved_at TIMESTAMPTZ,
    ADD COLUMN reservation_expires_at TIMESTAMPTZ,
    ADD COLUMN reservation_authority_id VARCHAR(128),
    ADD COLUMN reservation_authority_session_id VARCHAR(128),
    ADD COLUMN reservation_world_instance_id VARCHAR(128);

ALTER TABLE match_admission_grants
    ADD CONSTRAINT match_admission_grants_reservation_fields CHECK (
        (reserved_at IS NULL
            AND reservation_expires_at IS NULL
            AND reservation_authority_id IS NULL
            AND reservation_authority_session_id IS NULL
            AND reservation_world_instance_id IS NULL)
        OR (
            reserved_at IS NOT NULL
            AND reservation_expires_at IS NOT NULL
            AND reservation_expires_at > reserved_at
            AND reservation_authority_id IS NOT NULL
            AND reservation_authority_session_id IS NOT NULL
            AND reservation_world_instance_id IS NOT NULL
        )
    );

CREATE INDEX match_admission_grants_reservation_idx
    ON match_admission_grants (attempt_id, player_id, reservation_expires_at)
    WHERE reserved_at IS NOT NULL AND consumed_at IS NULL AND revoked_at IS NULL;
