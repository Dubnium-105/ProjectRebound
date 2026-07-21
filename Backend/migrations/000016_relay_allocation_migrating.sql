ALTER TABLE relay_allocations DROP CONSTRAINT relay_allocations_state;

-- statement-breakpoint
ALTER TABLE relay_allocations ADD CONSTRAINT relay_allocations_state CHECK (
    state IN ('ALLOCATED', 'BINDING', 'ACTIVE', 'MIGRATING', 'CLOSED', 'FAILED')
);
