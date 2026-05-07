package db

import (
	"database/sql"
	"fmt"
)

func Migrate(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS players (
			player_id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			device_token_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'Active'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_players_device_token_hash ON players(device_token_hash)`,
		`CREATE TABLE IF NOT EXISTS host_probes (
			probe_id TEXT PRIMARY KEY,
			player_id TEXT NOT NULL,
			public_ip TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL,
			nonce TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'Pending',
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rooms (
			room_id TEXT PRIMARY KEY,
			host_player_id TEXT,
			host_probe_id TEXT,
			host_token_hash TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			region TEXT NOT NULL DEFAULT '',
			map TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			version TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			max_players INTEGER NOT NULL DEFAULT 12,
			player_count INTEGER NOT NULL DEFAULT 0,
			server_state TEXT,
			host_loadout_snapshot_json TEXT,
			state TEXT NOT NULL DEFAULT 'Open',
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL,
			ended_reason TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rooms_state ON rooms(state)`,
		`CREATE INDEX IF NOT EXISTS idx_rooms_region_version ON rooms(region, version)`,
		`CREATE TABLE IF NOT EXISTS room_players (
			room_player_id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL,
			player_id TEXT NOT NULL,
			join_ticket_hash TEXT NOT NULL DEFAULT '',
			loadout_snapshot_json TEXT,
			status TEXT NOT NULL DEFAULT 'Reserved',
			joined_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS match_tickets (
			ticket_id TEXT PRIMARY KEY,
			player_id TEXT NOT NULL,
			region TEXT NOT NULL DEFAULT '',
			map TEXT,
			mode TEXT,
			version TEXT NOT NULL DEFAULT '',
			can_host INTEGER NOT NULL DEFAULT 0,
			probe_id TEXT,
			room_name TEXT,
			max_players INTEGER NOT NULL DEFAULT 12,
			state TEXT NOT NULL DEFAULT 'Waiting',
			assigned_room_id TEXT,
			host_token_plain TEXT,
			join_ticket_plain TEXT,
			failure_reason TEXT,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			metaserver_mode INTEGER NOT NULL DEFAULT 0,
			qos_data_json TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_match_tickets_state ON match_tickets(state)`,
		`CREATE TABLE IF NOT EXISTS legacy_servers (
			server_id TEXT PRIMARY KEY,
			source_key TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			region TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			map TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			player_count INTEGER NOT NULL DEFAULT 0,
			server_state TEXT NOT NULL DEFAULT '',
			version TEXT,
			last_seen_at INTEGER NOT NULL
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}
