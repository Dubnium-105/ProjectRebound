#!/usr/bin/env sh
set -eu

: "${POSTGRES_HOST:?POSTGRES_HOST is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${META_POSTGRES_USER:?META_POSTGRES_USER is required}"
: "${META_POSTGRES_PASSWORD:?META_POSTGRES_PASSWORD is required}"

export PGPASSWORD="$POSTGRES_PASSWORD"
attempt=0
until psql \
  --host "$POSTGRES_HOST" \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --tuples-only --no-align \
  --command "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 31)" 2>/dev/null |
  grep -qx t; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    printf 'MetaServer migration 31 was not applied within 120 seconds.\n' >&2
    exit 1
  fi
  sleep 2
done

psql \
  --host "$POSTGRES_HOST" \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set ON_ERROR_STOP=1 \
  --set meta_user="$META_POSTGRES_USER" \
  --set meta_password="$META_POSTGRES_PASSWORD" \
  --set database_name="$POSTGRES_DB" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'meta_user', :'meta_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'meta_user')
\gexec

SELECT format('ALTER ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT', :'meta_user', :'meta_password')
\gexec

SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'database_name', :'meta_user')
\gexec
SELECT format('GRANT USAGE ON SCHEMA public TO %I', :'meta_user')
\gexec
SELECT format('REVOKE CREATE ON SCHEMA public FROM %I', :'meta_user')
\gexec

-- Reset previous grants so rerunning provisioning also removes capabilities
-- that a past release no longer needs.
SELECT format(
  'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM %I',
  :'meta_user'
)
\gexec
SELECT format(
  'REVOKE %s (%I) ON TABLE %I FROM %I',
  privilege_type, column_name, table_name, :'meta_user'
)
FROM information_schema.column_privileges
WHERE table_schema = 'public' AND grantee = :'meta_user'
\gexec

SELECT format(
  'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I TO %I',
  table_name, :'meta_user'
)
FROM (VALUES
  ('meta_player_profiles'),
  ('meta_role_loadouts'),
  ('meta_weapon_archives'),
  ('meta_parties'),
  ('meta_party_members'),
  ('meta_match_tickets'),
  ('meta_matches'),
  ('meta_match_players'),
  ('meta_notifications'),
  ('meta_playlists'),
  ('meta_settings'),
  ('battlelog_matches'),
  ('battlelog_teams'),
  ('battlelog_participants'),
  ('battlelog_participant_stats'),
  ('battlelog_rounds'),
  ('battlelog_score_breakdowns')
) AS allowed(table_name)
\gexec

SELECT format(
  'GRANT SELECT (%s) ON TABLE %I TO %I',
  columns, table_name, :'meta_user'
)
FROM (VALUES
  ('players', 'id, steam_id, auth_level, account_status'),
  ('relay_nodes', 'id, region, state, load_state, public_endpoints, last_heartbeat_at, lease_expires_at'),
  ('schema_migrations', 'version'),
  ('auth_sessions', 'id, player_id, expires_at, revoked_at'),
  ('admin_users', 'id, username, display_name, status, last_login_at, created_at, updated_at, disabled_at'),
  ('admin_sessions', 'id, admin_id, token_version, expires_at, revoked_at'),
  ('admin_roles', 'id, name'),
  ('admin_permissions', 'id, permission_key'),
  ('admin_user_roles', 'admin_id, role_id'),
  ('admin_role_permissions', 'role_id, permission_id'),
  ('game_servers', 'id, region, mode, version, public_host, public_port, max_players, player_count, state, server_token_hash, token_expires_at, token_revoked_at, last_heartbeat_at, updated_at, token_scopes')
) AS allowed(table_name, columns)
\gexec

SELECT format(
  'GRANT UPDATE (state, updated_at) ON TABLE game_servers TO %I',
  :'meta_user'
)
\gexec

SELECT format('GRANT INSERT ON TABLE admin_audit_logs TO %I', :'meta_user')
\gexec
SQL
