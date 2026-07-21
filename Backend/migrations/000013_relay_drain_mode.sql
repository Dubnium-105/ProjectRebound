ALTER TABLE relay_nodes
    ADD COLUMN drain_migrate_existing BOOLEAN NOT NULL DEFAULT FALSE;

-- statement-breakpoint
CREATE INDEX relay_nodes_drain_migration_idx
    ON relay_nodes (drain_migrate_existing, id)
    WHERE state = 'DRAINING' AND drain_migrate_existing;
