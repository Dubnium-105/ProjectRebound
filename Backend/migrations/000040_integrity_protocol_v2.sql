ALTER TABLE auth_sessions
    ADD COLUMN pem_fingerprint BYTEA,
    ADD COLUMN integrity_trusted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT auth_sessions_pem_fingerprint_length
        CHECK (pem_fingerprint IS NULL OR octet_length(pem_fingerprint) = 32),
    ADD CONSTRAINT auth_sessions_integrity_trusted_requires_verified
        CHECK (
            NOT integrity_trusted OR
            (steam_verified = TRUE AND pem_fingerprint IS NOT NULL)
        );
