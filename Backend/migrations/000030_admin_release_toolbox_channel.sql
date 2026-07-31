ALTER TABLE admin_releases
    DROP CONSTRAINT admin_release_channel;

-- statement-breakpoint
ALTER TABLE admin_releases
    ADD CONSTRAINT admin_release_channel
    CHECK (channel IN ('stable', 'beta', 'toolbox'));
