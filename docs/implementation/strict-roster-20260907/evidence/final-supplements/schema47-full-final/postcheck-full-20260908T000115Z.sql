\pset pager off
\pset tuples_only off
SELECT 'schema=' || COALESCE(MAX(version)::text, '<none>') || '|migrations=' || COUNT(*)::text AS migration_summary FROM schema_migrations;
SELECT attempt_id, player_id, room_role, connection_state, connection_generation,
       live_connection_generation, live_route_generation,
       host_live_scope_preserved,
       COALESCE(live_native_connection_nonce, '<NULL>') AS live_nonce
FROM match_attempt_roster
WHERE attempt_id IN ('audit-attempt-valid-20260908','audit-attempt-missing-20260908')
ORDER BY attempt_id, player_id;
DO $$
DECLARE
  v_count integer;
  v_live integer;
  v_route integer;
  v_preserved boolean;
  v_nonce text;
BEGIN
  SELECT COUNT(*) INTO v_count FROM schema_migrations WHERE version = 48;
  IF v_count <> 1 THEN RAISE EXCEPTION 'expected schema version 48 exactly once, got %', v_count; END IF;
  SELECT live_connection_generation, live_route_generation, host_live_scope_preserved, live_native_connection_nonce
    INTO v_live, v_route, v_preserved, v_nonce
    FROM match_attempt_roster
   WHERE attempt_id='audit-attempt-valid-20260908' AND room_role='HOST';
  IF v_live <> 11 OR v_route <> 7 OR v_preserved IS DISTINCT FROM TRUE OR v_nonce IS DISTINCT FROM 'native-audit-valid-host-0001' THEN
    RAISE EXCEPTION 'valid connected HOST migration mismatch live=% route=% preserved=% nonce=%', v_live, v_route, v_preserved, v_nonce;
  END IF;
  SELECT live_connection_generation, live_route_generation, host_live_scope_preserved, live_native_connection_nonce
    INTO v_live, v_route, v_preserved, v_nonce
    FROM match_attempt_roster
   WHERE attempt_id='audit-attempt-missing-20260908' AND room_role='HOST';
  IF v_live <> 13 OR v_route <> 7 OR v_preserved IS DISTINCT FROM FALSE OR v_nonce IS NOT NULL THEN
    RAISE EXCEPTION 'missing-nonce connected HOST migration mismatch live=% route=% preserved=% nonce=%', v_live, v_route, v_preserved, v_nonce;
  END IF;
  SELECT live_connection_generation, live_route_generation, host_live_scope_preserved
    INTO v_live, v_route, v_preserved
    FROM match_attempt_roster
   WHERE attempt_id='audit-attempt-valid-20260908' AND room_role='MEMBER';
  IF v_live <> 12 OR v_route <> 7 OR v_preserved IS DISTINCT FROM FALSE THEN
    RAISE EXCEPTION 'connected MEMBER migration mismatch live=% route=% preserved=%', v_live, v_route, v_preserved;
  END IF;
END $$;
SELECT 'POSTCHECK_ASSERTIONS=PASS' AS result;