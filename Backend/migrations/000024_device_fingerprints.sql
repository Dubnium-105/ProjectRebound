CREATE TABLE auth_device_fingerprints (
    id VARCHAR(64) PRIMARY KEY,
    format_version SMALLINT NOT NULL,
    digest_key_id VARCHAR(32) NOT NULL,
    composite_digest BYTEA NOT NULL,
    smbios_uuid_digest BYTEA,
    disk_serial_digest BYTEA,
    cpu_id_digest BYTEA,
    factor_mask SMALLINT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT auth_device_fingerprints_format_version CHECK (format_version = 1),
    CONSTRAINT auth_device_fingerprints_factor_mask CHECK (factor_mask BETWEEN 1 AND 7),
    CONSTRAINT auth_device_fingerprints_composite_length CHECK (octet_length(composite_digest) = 32),
    CONSTRAINT auth_device_fingerprints_smbios_length CHECK (
        smbios_uuid_digest IS NULL OR octet_length(smbios_uuid_digest) = 32
    ),
    CONSTRAINT auth_device_fingerprints_disk_length CHECK (
        disk_serial_digest IS NULL OR octet_length(disk_serial_digest) = 32
    ),
    CONSTRAINT auth_device_fingerprints_cpu_length CHECK (
        cpu_id_digest IS NULL OR octet_length(cpu_id_digest) = 32
    ),
    CONSTRAINT auth_device_fingerprints_key_composite_unique UNIQUE (digest_key_id, composite_digest)
);

-- statement-breakpoint
CREATE INDEX auth_device_fingerprints_smbios_idx
    ON auth_device_fingerprints (digest_key_id, smbios_uuid_digest)
    WHERE smbios_uuid_digest IS NOT NULL;

-- statement-breakpoint
CREATE INDEX auth_device_fingerprints_disk_idx
    ON auth_device_fingerprints (digest_key_id, disk_serial_digest)
    WHERE disk_serial_digest IS NOT NULL;

-- statement-breakpoint
CREATE INDEX auth_device_fingerprints_cpu_idx
    ON auth_device_fingerprints (digest_key_id, cpu_id_digest)
    WHERE cpu_id_digest IS NOT NULL;

-- statement-breakpoint
ALTER TABLE auth_sessions
    ADD COLUMN device_fingerprint_id VARCHAR(64) REFERENCES auth_device_fingerprints(id);

-- statement-breakpoint
ALTER TABLE auth_login_events
    ADD COLUMN device_fingerprint_id VARCHAR(64) REFERENCES auth_device_fingerprints(id);

-- statement-breakpoint
ALTER TABLE auth_risk_events
    ADD COLUMN device_fingerprint_id VARCHAR(64) REFERENCES auth_device_fingerprints(id);

-- statement-breakpoint
CREATE INDEX auth_sessions_device_fingerprint_idx
    ON auth_sessions (device_fingerprint_id)
    WHERE device_fingerprint_id IS NOT NULL;

-- statement-breakpoint
CREATE INDEX auth_login_events_device_fingerprint_idx
    ON auth_login_events (device_fingerprint_id, created_at DESC)
    WHERE device_fingerprint_id IS NOT NULL;

-- statement-breakpoint
CREATE INDEX auth_risk_events_device_fingerprint_idx
    ON auth_risk_events (device_fingerprint_id, created_at DESC)
    WHERE device_fingerprint_id IS NOT NULL;
