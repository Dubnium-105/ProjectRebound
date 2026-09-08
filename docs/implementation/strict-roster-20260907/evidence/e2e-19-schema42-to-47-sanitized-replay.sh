#!/usr/bin/env bash
set -Eeuo pipefail

# SANITIZED_REPLAY_SOURCE=1
# EXECUTION_STATUS=NOT_RUN
#
# This is a reviewable, credential-free replay source for the schema portion of
# E2E-19.  The actual run is recorded in
# e2e-19-schema-upgrade-rebound_e2e19_live_1788791710.log.  This copy deliberately
# takes every identity, database name, and output path from the environment so
# no private fixture or Steam identity is embedded in the evidence tree.

: "${E2E19_REPO_ROOT:?set E2E19_REPO_ROOT to the checkout root}"
: "${E2E19_DATABASE_NAME:?set an isolated database name}"
: "${E2E19_PGPORT:?set the isolated PostgreSQL port}"
: "${E2E19_PGUSER:?set the isolated PostgreSQL role}"
: "${E2E19_FIXTURE_PLAYER_ID:?set a disposable fixture player id}"
: "${E2E19_FIXTURE_PLAYER_STEAM_ID:?set a disposable fixture platform id}"
: "${E2E19_LOG:?set an output log path outside the public evidence tree or use a redacted log}"

MIGRATIONS="$E2E19_REPO_ROOT/Backend/migrations"
export PGHOST="${E2E19_PGHOST:-127.0.0.1}"
export PGPORT="$E2E19_PGPORT"
export PGUSER="$E2E19_PGUSER"
export PGDATABASE="$E2E19_DATABASE_NAME"

exec > >(tee "$E2E19_LOG") 2>&1

psql_q() {
  psql -X -v ON_ERROR_STOP=1 -At "$@"
}

migration_for_version() {
  local version="$1"
  find "$MIGRATIONS" -maxdepth 1 -type f -name "$(printf '%06d' "$version")_*.sql" -print -quit
}

apply_migration() {
  local file="$1"
  local name version checksum
  name="$(basename "$file")"
  version="$((10#${name%%_*}))"
  checksum="$(sha256sum "$file" | awk '{print $1}')"
  {
    printf 'BEGIN;\n'
    cat "$file"
    printf '\nINSERT INTO schema_migrations(version,name,checksum) VALUES (%d, :'"'"'migration_name'"'"', :'"'"'migration_checksum'"'"');\nCOMMIT;\n' "$version"
  } | psql -X -v ON_ERROR_STOP=1 \
      -v migration_name="$name" -v migration_checksum="$checksum"
}

apply_range() {
  local first="$1" last="$2" version file
  for ((version = first; version <= last; version++)); do
    file="$(migration_for_version "$version")"
    test -n "$file" || { echo "missing migration version=$version" >&2; return 1; }
    apply_migration "$file"
  done
}

echo 'SANITIZED_E2E19_SCHEMA_REPLAY_START'
psql -X -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT PRIMARY KEY,
  name TEXT NOT NULL,
  checksum CHAR(64) NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
SQL

apply_range 1 42
count42="$(psql_q -c 'SELECT COUNT(*) FROM schema_migrations WHERE version <= 42;')"
max42="$(psql_q -c 'SELECT COALESCE(MAX(version), 0) FROM schema_migrations;')"
echo "PRE_UPGRADE applied_count=$count42 max_version=$max42"

psql -X -v ON_ERROR_STOP=1 \
  -v fixture_player_id="$E2E19_FIXTURE_PLAYER_ID" \
  -v fixture_steam_id="$E2E19_FIXTURE_PLAYER_STEAM_ID" <<'SQL'
INSERT INTO players(id, steam_id, persona_name, account_status, auth_provider,
                    auth_level, created_at, updated_at)
VALUES (:'fixture_player_id', :'fixture_steam_id', 'sanitized E2E-19 fixture',
        'ACTIVE', 'steam_ticket', 'verified', NOW(), NOW())
ON CONFLICT (id) DO NOTHING;
SQL
pre_count="$(psql_q -v fixture_player_id="$E2E19_FIXTURE_PLAYER_ID" \
  -c 'SELECT COUNT(*) FROM players WHERE id = :'"'"'fixture_player_id'"'"';')"
echo "PRE_UPGRADE_DATA player_count=$pre_count"

apply_range 43 47
max47="$(psql_q -c 'SELECT COALESCE(MAX(version), 0) FROM schema_migrations;')"
post_count="$(psql_q -v fixture_player_id="$E2E19_FIXTURE_PLAYER_ID" \
  -c 'SELECT COUNT(*) FROM players WHERE id = :'"'"'fixture_player_id'"'"';')"
echo "POST_UPGRADE max_version=$max47 player_count=$post_count"

test "$count42" = 42
test "$max42" = 42
test "$max47" = 47
test "$pre_count" = 1
test "$post_count" = 1
echo 'SANITIZED_E2E19_SCHEMA_REPLAY_RESULT=PASS_IF_EXPLICITLY_EXECUTED'
