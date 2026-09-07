-- Bind a native reservation to one concrete handshake.  Reusing a JTI is
-- idempotent only when the authority supplies the same nonce; a second native
-- connection cannot borrow the first connection's reservation.
ALTER TABLE match_admission_grants
    ADD COLUMN reservation_nonce VARCHAR(128),
    ADD COLUMN consumed_connection_nonce VARCHAR(128);

-- Existing grants issued before this migration may have no nonce.  New
-- reservation/confirmation writes enforce both values in the service
-- transaction; no rewrite of historical grant rows is performed here.
