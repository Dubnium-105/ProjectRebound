ALTER TABLE connections DROP CONSTRAINT connections_state;

-- statement-breakpoint
ALTER TABLE connections ADD CONSTRAINT connections_state CHECK (state IN (
    'CREATED', 'GATHERING_CANDIDATES', 'CHECKING_DIRECT', 'ALLOCATING_RELAY',
    'RELAY_BINDING', 'MIGRATING_RELAY', 'CONNECTED', 'FAILED', 'EXPIRED', 'CLOSED'
));

-- statement-breakpoint
ALTER TABLE relay_allocations ADD COLUMN failure_reason VARCHAR(128);

-- statement-breakpoint
CREATE TABLE relay_migrations (
    id VARCHAR(64) PRIMARY KEY,
    connection_id VARCHAR(64) NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    old_allocation_id VARCHAR(64) NOT NULL REFERENCES relay_allocations(id),
    new_allocation_id VARCHAR(64) NOT NULL REFERENCES relay_allocations(id),
    old_relay_node_id VARCHAR(64) NOT NULL REFERENCES relay_nodes(id),
    new_relay_node_id VARCHAR(64) NOT NULL REFERENCES relay_nodes(id),
    state VARCHAR(16) NOT NULL DEFAULT 'BINDING',
    dispatched_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT relay_migrations_distinct_nodes CHECK (old_relay_node_id <> new_relay_node_id),
    CONSTRAINT relay_migrations_state CHECK (state IN ('BINDING', 'COMPLETED', 'FAILED')),
    UNIQUE (new_allocation_id)
);

-- statement-breakpoint
CREATE UNIQUE INDEX relay_migrations_active_connection_idx
    ON relay_migrations (connection_id)
    WHERE state = 'BINDING';

-- statement-breakpoint
CREATE INDEX relay_migrations_dispatch_idx
    ON relay_migrations (state, dispatched_at, created_at);
