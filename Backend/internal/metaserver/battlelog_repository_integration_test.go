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
	t.Run("running", func(t *testing.T) { testStrictBattleLogSubmission(t, false, "pvp") })
	t.Run("completed_late_report", func(t *testing.T) { testStrictBattleLogSubmission(t, true, "pvp") })
	t.Run("pve_claim_non_official", func(t *testing.T) { testStrictBattleLogSubmission(t, false, "pve") })
}

func testStrictBattleLogSubmission(t *testing.T, completed bool, reportKind string) {
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
	legacyTicketID := fmt.Sprintf("legacy_ticket_%d", suffix)
	legacyMatchID := fmt.Sprintf("mm_legacy_%d", suffix)
	lobbyID := fmt.Sprintf("lby_battlelog_%d", suffix)
	attemptID := fmt.Sprintf("mat_battlelog_%d", suffix)
	steamID := fmt.Sprintf("76%015d", suffix%1_000_000_000_000_000)
	now := time.Now().UTC().Truncate(time.Millisecond)
	tokenHash := sha256.Sum256([]byte("gst_battlelog_integration_token"))

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM battlelog_matches WHERE game_server_id = $1`, serverID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_matches WHERE id = $1`, matchID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_matches WHERE id = $1`, legacyMatchID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_match_tickets WHERE id = $1`, ticketID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_match_tickets WHERE id = $1`, legacyTicketID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM match_lobbies WHERE id = $1`, lobbyID)
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
		INSERT INTO match_lobbies (
			id, owner_player_id, display_name, hosting_kind, mode, region,
			client_version, protocol_version, team_one_capacity, team_two_capacity,
			state, roster_revision, created_at, updated_at
		) VALUES ($1, $2, 'BattleLog strict lobby', 'DEDICATED', 'Rush_PVE_Normal',
		          'hgh', '1.1.0', 1, 1, 1, 'RUNNING', 1, $3, $3)
	`, lobbyID, playerID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO match_attempts (
			id, lobby_id, attempt_number, hosting_kind, state, roster_revision,
			authority_id, authority_session_id, route_generation,
			endpoint_host, endpoint_port, connection_deadline, created_at, updated_at,
			started_at
		) VALUES ($1, $2, 1, 'DEDICATED', 'RUNNING', 1, $3, $4, 1,
		          '127.0.0.1', 28080, $5, $6, $6, $6)
	`, attemptID, lobbyID, serverID, "battlelog-authority-session", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE match_lobbies SET current_attempt_id = $2 WHERE id = $1
	`, lobbyID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO match_attempt_roster (
			attempt_id, player_id, platform_id, display_name, room_role,
			team_id, team_slot, logical_slot, connection_generation,
			connection_state, auth_level_at_freeze, steam_verified_at_freeze,
			joined_lobby_at, connected_at, created_at, updated_at
		) VALUES ($1, $2, $3, 'BattleLog player', 'MEMBER', 1, 0, 0, 1,
		          'CONNECTED', 'verified', TRUE, $4, $4, $4, $4)
	`, attemptID, playerID, steamID, now); err != nil {
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
			reserved_at, started_at, updated_at, match_attempt_id
		) VALUES (
			$1, $2, $3, 'Rush_PVE_Normal', 'hgh', '1.1.0',
			1, 'RUNNING', '127.0.0.1', 28080, $4, $4, $4, $5
		)
	`, matchID, serverID, ticketID, now, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE match_attempts SET meta_match_id = $2 WHERE id = $1`, attemptID, matchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_match_players (
			match_id, player_id, auth_level_at_reservation,
			steam_verified_at_reservation, team_id, team_slot,
			logical_slot, connection_generation
		) VALUES ($1, $2, 'verified', TRUE, 1, 0, 0, 1)
	`, matchID, playerID); err != nil {
		t.Fatal(err)
	}
	raw := testBattleLogSnapshot(t, reportKind)
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
	repositoryPool := pool
	if restrictedURL := os.Getenv("TEST_META_DATABASE_URL"); restrictedURL != "" {
		repositoryPool, err = pgxpool.New(ctx, restrictedURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(repositoryPool.Close)
		t.Log("repository operations use the restricted Meta role; fixture setup uses the separate admin pool")
	}
	repository := NewRepository(repositoryPool, 90*time.Second)
	repository.now = func() time.Time { return now }
	principal := GameServerPrincipal{
		ServerID: serverID,
		Scopes:   []string{"meta.battlelog.write", "meta.loadouts.read"},
	}
	if _, err := repository.GetMatchPlayerLoadout(ctx, principal, matchID, playerID); err != nil {
		t.Fatalf("strict frozen member loadout rejected: %v", err)
	}
	// A mutable account row cannot replace the identity or trust frozen for
	// the match. The uploaded Steam ID remains the original roster platform ID.
	if _, err := pool.Exec(ctx, `UPDATE players SET steam_id = $2, auth_level = 'unverified' WHERE id = $1`, playerID, steamID+"9"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE match_attempts SET state = 'CONNECTING' WHERE id = $1`, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE meta_matches SET state = 'RESERVED' WHERE id = $1`, matchID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SubmitBattleLog(ctx, principal, "premature-report", normalized); metaErrorCode(err) != "BATTLELOG_MATCH_FORBIDDEN" {
		t.Fatalf("pre-running report must not promote authority: %v", err)
	}
	var earlyAttempt, earlyProjection string
	if err := pool.QueryRow(ctx, `SELECT a.state, m.state FROM match_attempts a JOIN meta_matches m ON m.match_attempt_id = a.id WHERE a.id = $1`, attemptID).Scan(&earlyAttempt, &earlyProjection); err != nil {
		t.Fatal(err)
	}
	if earlyAttempt != "CONNECTING" || earlyProjection != "RESERVED" {
		t.Fatalf("premature report changed lifecycle: %s/%s", earlyAttempt, earlyProjection)
	}
	expectedState := "RUNNING"
	if completed {
		expectedState = "COMPLETED"
	}
	if _, err := pool.Exec(ctx, `UPDATE match_attempts SET state = $2 WHERE id = $1`, attemptID, expectedState); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE meta_matches SET state = $2 WHERE id = $1`, matchID, expectedState); err != nil {
		t.Fatal(err)
	}
	type submissionResult struct {
		value BattleLogSubmission
		err   error
	}
	const submissions = 8
	responses := make(chan submissionResult, submissions)
	start := make(chan struct{})
	for range submissions {
		go func() {
			<-start
			input, inputErr := normalizeBattleLogSnapshot(raw)
			if inputErr != nil {
				responses <- submissionResult{err: inputErr}
				return
			}
			value, submitErr := repository.SubmitBattleLog(ctx, principal, "report-integration-1", input)
			responses <- submissionResult{value, submitErr}
		}()
	}
	close(start)
	var first BattleLogSubmission
	var submitError error
	created := 0
	reportIDs := make(map[string]bool)
	for range submissions {
		response := <-responses
		if response.err != nil {
			submitError = response.err
			continue
		}
		reportIDs[response.value.BattleLogID] = true
		if !response.value.Duplicate {
			first = response.value
			created++
		}
	}
	if submitError != nil {
		t.Fatalf("concurrent BattleLog submission: %v", submitError)
	}
	if created != 1 || len(reportIDs) != 1 {
		t.Fatalf("concurrent reports created=%d distinct_ids=%d", created, len(reportIDs))
	}
	t.Log("eight concurrent reports produced one stored report and seven duplicate receipts")
	expectedOfficial := reportKind == "pvp"
	if first.Official != expectedOfficial || first.Duplicate ||
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
	if participantAuth != "verified" || eligible != expectedOfficial {
		t.Fatalf("participant security snapshot = %q/%v", participantAuth, eligible)
	}
	var matchState, serverState, attemptState string
	if err := pool.QueryRow(ctx, `
		SELECT match.state, server.state, attempt.state
		FROM meta_matches AS match
		JOIN game_servers AS server ON server.id = match.game_server_id
		JOIN match_attempts AS attempt ON attempt.id = match.match_attempt_id
		WHERE match.id = $1
	`, matchID).Scan(&matchState, &serverState, &attemptState); err != nil {
		t.Fatal(err)
	}
	if matchState != expectedState || serverState != "RUNNING" || attemptState != expectedState {
		t.Fatalf("strict lifecycle was mutated by BattleLog upload: match=%s server=%s attempt=%s", matchState, serverState, attemptState)
	}
	// Make room for a retired independent Meta record on the same server. A
	// report without an explicit strict match identity must not discover or
	// complete that record implicitly, and must remain non-official PvE evidence.
	if _, err := pool.Exec(ctx, `UPDATE meta_matches SET state = 'COMPLETED' WHERE id = $1`, matchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_match_tickets (
			id, player_id, mode, region, client_version,
			protocol_version, state, matched_id, expires_at,
			created_at, updated_at, completed_at
		) VALUES ($1, $2, 'Rush_PVE_Normal', 'hgh', '1.1.0',
		          1, 'MATCHED', $3, $4, $5, $5, $5)
	`, legacyTicketID, playerID, legacyMatchID, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_matches (
			id, game_server_id, ticket_id, mode, region, client_version,
			protocol_version, state, endpoint_host, endpoint_port,
			reserved_at, started_at, updated_at
		) VALUES ($1, $2, $3, 'Rush_PVE_Normal', 'hgh', '1.1.0',
		          1, 'RUNNING', '127.0.0.1', 28081, $4, $4, $4)
	`, legacyMatchID, serverID, legacyTicketID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO meta_match_players (match_id, player_id, auth_level_at_reservation, steam_verified_at_reservation) VALUES ($1, $2, 'verified', TRUE)`, legacyMatchID, playerID); err != nil {
		t.Fatal(err)
	}

	standaloneDocument := make(map[string]any)
	if err := json.Unmarshal(testBattleLogSnapshot(t, "pve"), &standaloneDocument); err != nil {
		t.Fatal(err)
	}
	standaloneDocument["match_id"] = ""
	standaloneRaw, err := json.Marshal(standaloneDocument)
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := normalizeBattleLogSnapshot(standaloneRaw)
	if err != nil {
		t.Fatal(err)
	}
	standaloneSubmission, err := repository.SubmitBattleLog(
		ctx, principal, "report-standalone-pve", standalone,
	)
	if err != nil {
		t.Fatalf("submit standalone BattleLog: %v", err)
	}
	if standaloneSubmission.Official || standaloneSubmission.MetaMatchID != "" || standaloneSubmission.MatchType != "PVE" {
		t.Fatalf("standalone PvE was treated as official assignment: %#v", standaloneSubmission)
	}
	var legacyState, unchangedServerState string
	if err := pool.QueryRow(ctx, `
		SELECT match.state, server.state
		FROM meta_matches AS match
		JOIN game_servers AS server ON server.id = match.game_server_id
		WHERE match.id = $1
	`, legacyMatchID).Scan(&legacyState, &unchangedServerState); err != nil {
		t.Fatal(err)
	}
	if legacyState != "RUNNING" || unchangedServerState != "RUNNING" {
		t.Fatalf("standalone upload changed retired Meta state: match=%s server=%s", legacyState, unchangedServerState)
	}
	standalone.Snapshot.MatchID = legacyMatchID
	if _, err := repository.SubmitBattleLog(ctx, principal, "retired-explicit-report", standalone); metaErrorCode(err) != "BATTLELOG_MATCH_FORBIDDEN" {
		t.Fatalf("explicit retired match was accepted: %v", err)
	}
	if _, err := repository.GetMatchPlayerLoadout(ctx, principal, legacyMatchID, playerID); metaErrorCode(err) != "META_MATCH_PLAYER_FORBIDDEN" {
		t.Fatalf("retired match loadout accepted: %v", err)
	}
	document["match_id"] = matchID

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
