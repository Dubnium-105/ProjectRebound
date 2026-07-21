ALTER TABLE relay_nodes
    ADD COLUMN load_state VARCHAR(16) NOT NULL DEFAULT 'NORMAL',
    ADD CONSTRAINT relay_nodes_load_state_check
        CHECK (load_state IN ('NORMAL', 'DEGRADED', 'REJECT_NEW', 'DRAINING'));

CREATE INDEX relay_nodes_schedulable_load_idx
    ON relay_nodes (state, load_state, region)
    WHERE state = 'READY' AND load_state IN ('NORMAL', 'DEGRADED');
