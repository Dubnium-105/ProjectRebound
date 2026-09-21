-- Match result/return lifecycle is deliberately separate from terminal
-- completion.  RESULT_CONFIRMED freezes new admission while the native
-- result screen and normal player exit are still allowed to drain.

ALTER TABLE match_lobbies
    DROP CONSTRAINT match_lobbies_state;

ALTER TABLE match_lobbies
    ADD CONSTRAINT match_lobbies_state CHECK (
        state IN ('OPEN', 'FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING',
                  'ENDING', 'COMPLETED', 'ABORTED')
    );

ALTER TABLE match_attempts
    DROP CONSTRAINT match_attempts_state;

ALTER TABLE match_attempts
    ADD CONSTRAINT match_attempts_state CHECK (
        state IN ('FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING',
                  'ENDING', 'COMPLETED', 'ABORTED')
    );

DROP INDEX match_attempts_one_active_lobby_idx;

CREATE UNIQUE INDEX match_attempts_one_active_lobby_idx
    ON match_attempts (lobby_id)
    WHERE state IN ('FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING', 'ENDING');

ALTER TABLE match_attempts
    ADD COLUMN match_generation INTEGER NOT NULL DEFAULT 1
        CHECK (match_generation > 0),
    ADD COLUMN lifecycle_phase VARCHAR(32) NOT NULL DEFAULT ''
        CHECK (lifecycle_phase IN ('', 'RESULT_CONFIRMED', 'RETURN_READY')),
    ADD COLUMN lifecycle_event_seq BIGINT NOT NULL DEFAULT 0
        CHECK (lifecycle_event_seq >= 0),
    ADD COLUMN result_confirmed_at TIMESTAMPTZ,
    ADD COLUMN return_ready_at TIMESTAMPTZ,
    ADD COLUMN ending_deadline TIMESTAMPTZ,
    ADD COLUMN completion_warning VARCHAR(64);

ALTER TABLE relay_allocations
    ADD COLUMN revoke_requested_at TIMESTAMPTZ;

CREATE INDEX relay_allocations_revoke_pending_idx
    ON relay_allocations (connection_id, revoke_requested_at)
    WHERE revoke_requested_at IS NOT NULL;

CREATE INDEX match_attempts_ending_deadline_idx
    ON match_attempts (state, ending_deadline, updated_at)
    WHERE state = 'ENDING';
