ALTER TABLE auth_risk_events
    ADD COLUMN resolved_by VARCHAR(64) REFERENCES admin_users(id),
    ADD COLUMN resolution_note TEXT;

-- statement-breakpoint
CREATE INDEX auth_risk_events_unresolved_idx
    ON auth_risk_events (severity, created_at DESC)
    WHERE resolved_at IS NULL;

