ALTER TABLE relay_node_audit_logs
    ADD COLUMN reason TEXT NOT NULL DEFAULT 'legacy operation',
    ADD COLUMN user_agent TEXT,
    ADD COLUMN result VARCHAR(16) NOT NULL DEFAULT 'SUCCEEDED',
    ADD CONSTRAINT relay_node_audit_result_check
        CHECK (result IN ('SUCCEEDED', 'FAILED', 'DENIED'));

