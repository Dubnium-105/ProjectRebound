ALTER TABLE match_admission_grants
    ADD COLUMN delivered_at TIMESTAMPTZ;

-- statement-breakpoint
CREATE INDEX match_admission_grants_pending_delivery_idx
    ON match_admission_grants (attempt_id, issued_at)
    WHERE delivered_at IS NULL AND consumed_at IS NULL AND revoked_at IS NULL;
