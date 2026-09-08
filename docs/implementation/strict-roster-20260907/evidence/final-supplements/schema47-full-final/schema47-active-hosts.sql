BEGIN;
INSERT INTO players (id, steam_id, persona_name, account_status, is_vip, auth_provider, auth_level, created_at, updated_at)
VALUES
  ('audit-host-valid-20260908', '76561198000000911', 'Audit Valid Host', 'ACTIVE', FALSE, 'steam_ticket', 'verified', NOW(), NOW()),
  ('audit-member-valid-20260908', '76561198000000912', 'Audit Valid Member', 'ACTIVE', FALSE, 'steam_ticket', 'verified', NOW(), NOW()),
  ('audit-host-missing-20260908', '76561198000000913', 'Audit Missing Host', 'ACTIVE', FALSE, 'steam_ticket', 'verified', NOW(), NOW());
INSERT INTO match_lobbies (
  id, owner_player_id, display_name, hosting_kind, transport_kind, mode, region,
  client_version, protocol_version, team_one_capacity, team_two_capacity,
  state, roster_revision, created_at, updated_at
) VALUES
  ('audit-lobby-valid-20260908', 'audit-host-valid-20260908', 'schema47 valid host', 'P2P', 'LEGACY_RELAY', 'TDM', 'test', 'history-fixture', 1, 1, 1, 'CONNECTING', 3, NOW(), NOW()),
  ('audit-lobby-missing-20260908', 'audit-host-missing-20260908', 'schema47 missing host nonce', 'P2P', 'LEGACY_RELAY', 'TDM', 'test', 'history-fixture', 1, 1, 1, 'CONNECTING', 3, NOW(), NOW());
INSERT INTO match_lobby_members (
  lobby_id, player_id, role, team_id, team_slot, ready, presence_state,
  presence_expires_at, membership_state, joined_at, last_seen_at
) VALUES
  ('audit-lobby-valid-20260908', 'audit-host-valid-20260908', 'OWNER', 1, 0, TRUE, 'ONLINE', NOW() + INTERVAL '1 hour', 'ACTIVE', NOW(), NOW()),
  ('audit-lobby-valid-20260908', 'audit-member-valid-20260908', 'MEMBER', 2, 0, TRUE, 'ONLINE', NOW() + INTERVAL '1 hour', 'ACTIVE', NOW(), NOW()),
  ('audit-lobby-missing-20260908', 'audit-host-missing-20260908', 'OWNER', 1, 0, TRUE, 'ONLINE', NOW() + INTERVAL '1 hour', 'ACTIVE', NOW(), NOW());
INSERT INTO match_attempts (
  id, lobby_id, attempt_number, hosting_kind, state, roster_revision,
  authority_id, authority_session_id, route_generation, created_at, updated_at
) VALUES
  ('audit-attempt-valid-20260908', 'audit-lobby-valid-20260908', 1, 'P2P', 'CONNECTING', 3, 'audit-host-valid-20260908', 'audit-authority-valid-20260908', 7, NOW(), NOW()),
  ('audit-attempt-missing-20260908', 'audit-lobby-missing-20260908', 1, 'P2P', 'CONNECTING', 3, 'audit-host-missing-20260908', 'audit-authority-missing-20260908', 7, NOW(), NOW());
UPDATE match_lobbies SET current_attempt_id = 'audit-attempt-valid-20260908' WHERE id = 'audit-lobby-valid-20260908';
UPDATE match_lobbies SET current_attempt_id = 'audit-attempt-missing-20260908' WHERE id = 'audit-lobby-missing-20260908';
INSERT INTO match_attempt_roster (
  attempt_id, player_id, platform_id, display_name, room_role, team_id,
  team_slot, logical_slot, connection_generation, connection_state,
  auth_level_at_freeze, steam_verified_at_freeze, joined_lobby_at,
  connected_at, created_at, updated_at, live_native_connection_nonce
) VALUES
  ('audit-attempt-valid-20260908', 'audit-host-valid-20260908', '76561198000000911', 'Audit Valid Host', 'HOST', 1, 0, 0, 11, 'CONNECTED', 'verified', TRUE, NOW() - INTERVAL '5 minutes', NOW() - INTERVAL '4 minutes', NOW(), NOW(), 'native-audit-valid-host-0001'),
  ('audit-attempt-valid-20260908', 'audit-member-valid-20260908', '76561198000000912', 'Audit Valid Member', 'MEMBER', 2, 0, 1, 12, 'CONNECTED', 'verified', TRUE, NOW() - INTERVAL '5 minutes', NOW() - INTERVAL '4 minutes', NOW(), NOW(), 'native-audit-valid-member-0001'),
  ('audit-attempt-missing-20260908', 'audit-host-missing-20260908', '76561198000000913', 'Audit Missing Host', 'HOST', 1, 0, 0, 13, 'CONNECTED', 'verified', TRUE, NOW() - INTERVAL '5 minutes', NOW() - INTERVAL '4 minutes', NOW(), NOW(), NULL);
COMMIT;