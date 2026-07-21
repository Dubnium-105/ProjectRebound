ALTER TABLE relay_migrations
    ADD COLUMN reason VARCHAR(64) NOT NULL DEFAULT 'RELAY_UNHEALTHY',
    ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN bind_deadline TIMESTAMPTZ,
    ADD COLUMN failure_reason VARCHAR(128),
    ADD CONSTRAINT relay_migrations_attempt_check CHECK (attempt BETWEEN 1 AND 10);

-- statement-breakpoint
UPDATE relay_migrations
SET bind_deadline = COALESCE(dispatched_at, created_at) + INTERVAL '45 seconds'
WHERE bind_deadline IS NULL AND state = 'BINDING';

-- statement-breakpoint
CREATE INDEX relay_migrations_timeout_idx
    ON relay_migrations (bind_deadline, id)
    WHERE state = 'BINDING';
