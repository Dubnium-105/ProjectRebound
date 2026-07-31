package metaserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBattleLogSubmissionAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	suffix := time.Now().UnixNano()
	playerID := fmt.Sprintf("battlelog_player_%d", suffix)
	serverID := fmt.Sprintf("battlelog_server_%d", suffix)
	ticketID := fmt.Sprintf("battlelog_ticket_%d", suffix)
	matchID := fmt.Sprintf("mm_battlelog_%d", suffix)
	steamID := fmt.Sprintf("76%015d", suffix%1_000_000_000_000_000)
	now := time.Now().UTC().Truncate(time.Millisecond)
	tokenHash := sha256.Sum256([]byte("gst_battlelog_integration_token"))

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM battlelog_matches WHERE game_server_id = $1`, serverID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_matches WHERE id = $1`, matchID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_match_tickets WHERE id = $1`, ticketID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM game_servers WHERE id = $1`, serverID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id = $1`, playerID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider,
			auth_level, created_at, updated_at
		) VALUES ($1, $2, 'BattleLog player', 'ACTIVE',
		          'steam_ticket', 'verified', $3, $3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO game_servers (
			id, instance_id, display_name, region, mode, version,
			public_host, public_port, max_players, player_count, state,
			server_token_hash, registration_issuer, token_expires_at,
			last_heartbeat_at, created_at, updated_at
		) VALUES (
			$1, $1, 'BattleLog server', 'hgh', 'Rush_PVE_Normal', '1.1.0',
			'127.0.0.1', 28080, 8, 1, 'RUNNING', $2, 'integration',
			$3, $4, $4, $4
		)
	`, serverID, tokenHash[:], now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_match_tickets (
			id, player_id, mode, region, client_version,
			protocol_version, state, matched_id, expires_at,
			created_at, updated_at, completed_at
		) VALUES (
			$1, $2, 'Rush_PVE_Normal', 'hgh', '1.1.0',
			1, 'MATCHED', $3, $4, $5, $5, $5
		)
	`, ticketID, playerID, matchID, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_matches (
			id, game_server_id, ticket_id, mode, region, client_version,
			protocol_version, state, endpoint_host, endpoint_port,
			reserved_at, started_at, updated_at
		) VALUES (
			$1, $2, $3, 'Rush_PVE_Normal', 'hgh', '1.1.0',
			1, 'RUNNING', '127.0.0.1', 28080, $4, $4, $4
		)
	`, matchID, serverID, ticketID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_match_players (
			match_id, player_id, auth_level_at_reservation,
			steam_verified_at_reservation
		) VALUES ($1, $2, 'verified', TRUE)
	`, matchID, playerID); err != nil {
		t.Fatal(err)
	}

	raw := testBattleLogSnapshot(t, "pve")
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["match_id"] = matchID
	players := document["players"].([]any)
	players[0].(map[string]any)["identity"].(map[string]any)["platform_id"] = steamID
	players[0].(map[string]any)["identity"].(map[string]any)["user_id"] = steamID
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeBattleLogSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(pool, 90*time.Second)
	repository.now = func() time.Time { return now }
	principal := GameServerPrincipal{
		ServerID: serverID,
		Scopes:   []string{"meta.battlelog.write"},
	}
	first, err := repository.SubmitBattleLog(
		ctx, principal, "report-integration-1", normalized,
	)
	if err != nil {
		t.Fatalf("submit BattleLog: %v", err)
	}
	if !first.Official || first.Duplicate ||
		first.ValidationStatus != BattleLogAccepted ||
		first.MetaMatchID != matchID {
		t.Fatalf("first submission = %#v", first)
	}

	replay, err := repository.SubmitBattleLog(
		ctx, principal, "report-integration-1", normalized,
	)
	if err != nil {
		t.Fatalf("replay BattleLog: %v", err)
	}
	if !replay.Duplicate || replay.BattleLogID != first.BattleLogID {
		t.Fatalf("replay = %#v", replay)
	}

	var participantAuth string
	var eligible bool
	if err := pool.QueryRow(ctx, `
		SELECT auth_level_at_match, official_eligible
		FROM battlelog_participants
		WHERE match_id = $1
	`, first.BattleLogID).Scan(&participantAuth, &eligible); err != nil {
		t.Fatal(err)
	}
	if participantAuth != "verified" || !eligible {
		t.Fatalf("participant security snapshot = %q/%v", participantAuth, eligible)
	}
	var matchState, serverState string
	if err := pool.QueryRow(ctx, `
		SELECT match.state, server.state
		FROM meta_matches AS match
		JOIN game_servers AS server ON server.id = match.game_server_id
		WHERE match.id = $1
	`, matchID).Scan(&matchState, &serverState); err != nil {
		t.Fatal(err)
	}
	if matchState != "COMPLETED" || serverState != "READY" {
		t.Fatalf("states = %s/%s", matchState, serverState)
	}

	document["captured_at_utc"] = "2026-07-31T07:02:37.805Z"
	changedRaw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := normalizeBattleLogSnapshot(changedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SubmitBattleLog(
		ctx, principal, "report-integration-1", changed,
	); metaErrorCode(err) != "BATTLELOG_REPORT_CONFLICT" {
		t.Fatalf("conflicting replay error = %v", err)
	}
}
