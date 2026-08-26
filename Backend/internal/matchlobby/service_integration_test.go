package matchlobby

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2pbattlelog"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStrictRosterP2PLifecycleAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	p2pService := p2proom.NewService(p2proom.NewRepository(pool), config.Defaults.P2PRoom)
	battleLogConfig := config.Defaults.P2PBattleLog
	battleLogService := p2pbattlelog.NewService(p2pbattlelog.NewRepository(pool), battleLogConfig)
	p2pService.SetMatchLifecycle(battleLogService)
	matchConfig := config.Defaults.MatchLobby
	matchConfig.StrictRosterV1Enabled = true
	signer, err := NewAdmissionSigner("integration-admission", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	service.SetP2PTransport(p2pService)
	service.SetP2PMatchProjector(battleLogService)
	currentTime := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return currentTime }

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	actors := make([]Actor, 4)
	playerIDs := make([]string, 4)
	for index := range actors {
		steamID := fmt.Sprintf("%017d", uint64(index+5)*10_000_000_000_000_000+suffix)
		actors[index] = insertStrictRosterPlayer(t, ctx, pool, steamID)
		playerIDs[index] = actors[index].PlayerID
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	created, err := service.Create(ctx, actors[0], strictP2PCreateInput("Concurrent Roster", "integration-roster-1"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Snapshot.RosterRevision != 1 || created.TransportHostToken == "" {
		t.Fatalf("unexpected create result: %+v", created.Snapshot)
	}

	type joinResult struct {
		actor Actor
		item  Snapshot
		err   error
	}
	results := make(chan joinResult, 2)
	var wait sync.WaitGroup
	for _, actor := range actors[1:3] {
		wait.Add(1)
		go func(actor Actor) {
			defer wait.Done()
			item, joinErr := service.Join(ctx, actor, created.Snapshot.LobbyID, 2, 1)
			results <- joinResult{actor: actor, item: item, err: joinErr}
		}(actor)
	}
	wait.Wait()
	close(results)
	var winner, loser Actor
	for result := range results {
		if result.err == nil {
			winner = result.actor
			continue
		}
		if errorCode(result.err) != "MATCH_LOBBY_REVISION_CONFLICT" {
			t.Fatalf("concurrent join returned unexpected error: %v", result.err)
		}
		loser = result.actor
	}
	if winner.PlayerID == "" || loser.PlayerID == "" {
		t.Fatalf("expected one serialized winner and one revision conflict: winner=%+v loser=%+v", winner, loser)
	}
	afterFirst, err := service.Get(ctx, created.Snapshot.LobbyID, actors[0].PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	afterSecond, err := service.Join(ctx, loser, created.Snapshot.LobbyID, 2, afterFirst.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Join(ctx, actors[3], created.Snapshot.LobbyID, 2, afterSecond.RosterRevision); errorCode(err) != "MATCH_LOBBY_TEAM_FULL" {
		t.Fatalf("full team accepted another member: %v", err)
	}
	assertUniqueLowestSeats(t, afterSecond)

	for _, actor := range actors[:3] {
		if _, err := service.SetReady(ctx, actor, created.Snapshot.LobbyID, true, afterSecond.RosterRevision); err != nil {
			t.Fatal(err)
		}
	}
	switched, err := service.SelectTeam(ctx, loser, created.Snapshot.LobbyID, 1, afterSecond.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range switched.Teams {
		for _, member := range team.Members {
			if member.Ready {
				t.Fatalf("team change did not clear ready for %s", member.PlayerID)
			}
		}
	}
	for _, actor := range actors[:3] {
		if _, err := service.SetReady(ctx, actor, created.Snapshot.LobbyID, true, switched.RosterRevision); err != nil {
			t.Fatal(err)
		}
	}
	frozen, err := service.Start(ctx, actors[0], created.Snapshot.LobbyID, switched.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.Start(ctx, actors[0], created.Snapshot.LobbyID, switched.RosterRevision)
	if err != nil || repeated.Attempt == nil || frozen.Attempt == nil ||
		repeated.Attempt.AttemptID != frozen.Attempt.AttemptID {
		t.Fatalf("start was not idempotent: first=%+v repeated=%+v err=%v", frozen.Attempt, repeated.Attempt, err)
	}

	attemptID := frozen.Attempt.AttemptID
	assertP2PProjectionMatchesAttempt(t, ctx, pool, attemptID)
	allocation, err := service.P2PHostAllocation(ctx, actors[0], attemptID)
	if err != nil {
		t.Fatal(err)
	}
	claims := decodeAllocationClaims(t, allocation.Allocation)
	if claims.Audience != allocationAudience || claims.ConnectionWindow != 120 || len(claims.Roster) != 3 {
		t.Fatalf("allocation lost frozen roster/window: %+v", claims)
	}
	var authoritySession string
	if err := pool.QueryRow(ctx, "SELECT authority_session_id FROM match_attempts WHERE id = $1", attemptID).Scan(&authoritySession); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PPayloadInstalled(
		ctx, actors[0], attemptID, authoritySession, "strict-roster-v1",
		matchConfig.LockedGameSHA256, frozen.Attempt.RouteGeneration,
	); err != nil {
		t.Fatal(err)
	}
	connecting, err := service.P2PAuthorityReady(
		ctx, actors[0], attemptID, authoritySession, created.TransportHostToken,
		"10.88.0.1", 7777, frozen.Attempt.RouteGeneration,
	)
	if err != nil || connecting.State != StateConnecting {
		t.Fatalf("authority ready = %+v, %v", connecting, err)
	}
	if _, err := service.P2PAuthorityReady(
		ctx, actors[0], attemptID, authoritySession, created.TransportHostToken,
		"10.88.0.1", 7777, frozen.Attempt.RouteGeneration,
	); err != nil {
		t.Fatalf("authority ready retry was not idempotent: %v", err)
	}
	if _, err := service.JoinGrant(ctx, actors[0], attemptID); errorCode(err) != "MATCH_P2P_HOST_USES_ALLOCATION" {
		t.Fatalf("P2P host received a remote grant: %v", err)
	}
	assertP2PProjectionMatchesAttempt(t, ctx, pool, attemptID)

	if _, err := pool.Exec(ctx, `UPDATE match_attempts SET authority_last_seen_at = $2 WHERE id = $1`, attemptID, currentTime.Add(-31*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.JoinGrant(ctx, actors[1], attemptID); errorCode(err) != "MATCH_AUTHORITY_RECONNECTING" {
		t.Fatalf("join grant escaped while P2P authority was offline: %v", err)
	}
	if err := service.P2PAuthorityHeartbeat(ctx, actors[0], authoritySession, attemptID); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Get(ctx, created.Snapshot.LobbyID, actors[0].PlayerID)
	if err != nil || recovered.Attempt == nil || recovered.Attempt.RouteGeneration != 2 || recovered.Attempt.PayloadInstalled {
		t.Fatalf("P2P route recovery did not require a refreshed allocation: %+v, %v", recovered.Attempt, err)
	}
	if _, err := service.JoinGrant(ctx, winner, attemptID); errorCode(err) != "MATCH_AUTHORITY_ROUTE_REFRESHING" {
		t.Fatalf("join grant escaped while authority allocation was stale: %v", err)
	}
	refreshedAllocation, err := service.P2PHostAllocation(ctx, actors[0], attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed := decodeAllocationClaims(t, refreshedAllocation.Allocation); refreshed.RouteGeneration != 2 {
		t.Fatalf("refreshed allocation route generation = %d", refreshed.RouteGeneration)
	}
	if _, err := service.P2PPayloadInstalled(
		ctx, actors[0], attemptID, authoritySession, "strict-roster-v1",
		matchConfig.LockedGameSHA256, 2,
	); err != nil {
		t.Fatal(err)
	}
	assertP2PProjectionMatchesAttempt(t, ctx, pool, attemptID)

	for _, actor := range []Actor{winner, loser} {
		grant, err := service.JoinGrant(ctx, actor, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		jti := decodeJoinGrantJTI(t, grant.Grant)
		connected, err := service.P2PMarkConnected(
			ctx, actors[0], authoritySession, attemptID, actor.PlayerID, jti,
			grant.ConnectionGeneration,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.P2PMarkConnected(
			ctx, actors[0], authoritySession, attemptID, actor.PlayerID, jti,
			grant.ConnectionGeneration,
		); err != nil {
			t.Fatalf("connected report retry was not idempotent: %v", err)
		}
		connecting = connected
	}
	if connecting.State != StateRunning || connecting.Attempt == nil || connecting.Attempt.State != AttemptRunning {
		t.Fatalf("all connected players did not start early: %+v", connecting)
	}
	terminal, err := service.P2PComplete(ctx, actors[0], authoritySession, attemptID, true, "")
	if err != nil || terminal.State != StateCompleted {
		t.Fatalf("complete = %+v, %v", terminal, err)
	}
	if repeatedTerminal, err := service.P2PComplete(ctx, actors[0], authoritySession, attemptID, true, ""); err != nil || repeatedTerminal.State != StateCompleted {
		t.Fatalf("completion retry was not idempotent: %+v, %v", repeatedTerminal, err)
	}
	var projectedTerminalState string
	if err := pool.QueryRow(ctx, `SELECT state FROM p2p_match_sessions WHERE match_attempt_id = $1`, attemptID).Scan(&projectedTerminalState); err != nil {
		t.Fatal(err)
	}
	if projectedTerminalState != "INCOMPLETE" {
		t.Fatalf("disabled BattleLog projection remained active after completion: %s", projectedTerminalState)
	}
	if _, err := service.JoinGrant(ctx, winner, attemptID); errorCode(err) != "MATCH_ATTEMPT_NOT_CONNECTABLE" {
		t.Fatalf("terminal attempt still issued a grant: %v", err)
	}

	abortLobby, _ := createTwoPlayerReadyLobby(
		t, ctx, service, actors[0], actors[1], "Provisioning Abort", "integration-roster-abort",
	)
	if abortLobby.Attempt == nil {
		t.Fatal("provisioning-abort lobby omitted attempt")
	}
	var abortSession string
	if err := pool.QueryRow(ctx, "SELECT authority_session_id FROM match_attempts WHERE id = $1", abortLobby.Attempt.AttemptID).Scan(&abortSession); err != nil {
		t.Fatal(err)
	}
	aborted, err := service.P2PComplete(ctx, actors[0], abortSession, abortLobby.Attempt.AttemptID, false, "PAYLOAD_INSTALL_FAILED")
	if err != nil || aborted.State != StateAborted {
		t.Fatalf("provisioning abort = %+v, %v", aborted, err)
	}
	if repeatedAbort, err := service.P2PComplete(ctx, actors[0], abortSession, abortLobby.Attempt.AttemptID, false, "PAYLOAD_INSTALL_FAILED"); err != nil || repeatedAbort.State != StateAborted {
		t.Fatalf("provisioning abort retry was not idempotent: %+v, %v", repeatedAbort, err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM p2p_match_sessions WHERE match_attempt_id = $1`, abortLobby.Attempt.AttemptID).Scan(&projectedTerminalState); err != nil {
		t.Fatal(err)
	}
	if projectedTerminalState != "ABORTED" {
		t.Fatalf("aborted attempt projection state = %s", projectedTerminalState)
	}

	timeoutLobby, timeoutHostToken := createTwoPlayerReadyLobby(
		t, ctx, service, actors[0], actors[1], "Timeout Roster", "integration-roster-2",
	)
	timeoutAttempt := timeoutLobby.Attempt
	if timeoutAttempt == nil {
		t.Fatal("timeout lobby omitted attempt")
	}
	timeoutAllocation, err := service.P2PHostAllocation(ctx, actors[0], timeoutAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	_ = timeoutAllocation
	var timeoutSession string
	if err := pool.QueryRow(ctx, "SELECT authority_session_id FROM match_attempts WHERE id = $1", timeoutAttempt.AttemptID).Scan(&timeoutSession); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PPayloadInstalled(ctx, actors[0], timeoutAttempt.AttemptID, timeoutSession, "strict-roster-v1", matchConfig.LockedGameSHA256, timeoutAttempt.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PAuthorityReady(
		ctx, actors[0], timeoutAttempt.AttemptID, timeoutSession,
		timeoutHostToken, "10.88.0.2", 7777,
		timeoutAttempt.RouteGeneration,
	); err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(121 * time.Second)
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	returned, err := service.Get(ctx, timeoutLobby.LobbyID, actors[0].PlayerID)
	if err != nil || returned.State != StateOpen || returned.Attempt != nil ||
		returned.RosterRevision != timeoutLobby.RosterRevision+1 {
		t.Fatalf("empty-team timeout did not return the roster to OPEN: %+v, %v", returned, err)
	}
	for _, team := range returned.Teams {
		for _, member := range team.Members {
			if member.Ready || member.PresenceState != "OFFLINE" {
				t.Fatalf("returned member was not reset: %+v", member)
			}
		}
	}
}

func strictP2PCreateInput(name, idempotencyKey string) CreateInput {
	return CreateInput{
		DisplayName: name, HostingKind: HostingP2P, TransportKind: TransportLegacy,
		Mode: "TDM", Region: "hk", ClientVersion: "1.0.0", ProtocolVersion: 1,
		TeamOneCapacity: 2, TeamTwoCapacity: 2, TeamID: 1,
		IdempotencyKey: idempotencyKey,
	}
}

func createTwoPlayerReadyLobby(
	t *testing.T,
	ctx context.Context,
	service *Service,
	owner, member Actor,
	name, idempotencyKey string,
) (Snapshot, string) {
	t.Helper()
	created, err := service.Create(ctx, owner, strictP2PCreateInput(name, idempotencyKey))
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(ctx, member, created.Snapshot.LobbyID, 2, created.Snapshot.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetReady(ctx, owner, joined.LobbyID, true, joined.RosterRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetReady(ctx, member, joined.LobbyID, true, joined.RosterRevision); err != nil {
		t.Fatal(err)
	}
	frozen, err := service.Start(ctx, owner, joined.LobbyID, joined.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	return frozen, created.TransportHostToken
}

func insertStrictRosterPlayer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, steamID string) Actor {
	t.Helper()
	id := newAdmissionID("integration_player_")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider,
			auth_level, created_at, updated_at
		) VALUES ($1, $2, 'Strict Roster Integration', 'ACTIVE',
		          'steam_ticket', 'verified', $3, $3)
	`, id, steamID, now); err != nil {
		t.Fatal(err)
	}
	return Actor{
		PlayerID: id, AccountStatus: player.AccountStatusActive,
		AuthLevel: player.AuthLevelVerified, SteamVerified: true,
	}
}

func assertUniqueLowestSeats(t *testing.T, snapshot Snapshot) {
	t.Helper()
	for _, team := range snapshot.Teams {
		seen := make(map[int]bool)
		for _, member := range team.Members {
			if seen[member.TeamSlot] {
				t.Fatalf("duplicate team %d slot %d", team.TeamID, member.TeamSlot)
			}
			seen[member.TeamSlot] = true
		}
		for slot := 0; slot < len(team.Members); slot++ {
			if !seen[slot] {
				t.Fatalf("team %d did not allocate its lowest slots: %+v", team.TeamID, team.Members)
			}
		}
	}
}

func decodeAllocationClaims(t *testing.T, token string) AllocationClaims {
	t.Helper()
	parts := splitAdmissionToken(t, token)
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims AllocationClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func decodeJoinGrantJTI(t *testing.T, token string) string {
	t.Helper()
	parts := splitAdmissionToken(t, token)
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims JoinGrantClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	return claims.TokenID
}

func splitAdmissionToken(t *testing.T, token string) []string {
	t.Helper()
	parts := make([]string, 0, 3)
	start := 0
	for index := 0; index <= len(token); index++ {
		if index == len(token) || token[index] == '.' {
			parts = append(parts, token[start:index])
			start = index + 1
		}
	}
	if len(parts) != 3 {
		t.Fatalf("admission token has %d parts", len(parts))
	}
	return parts
}

func assertP2PProjectionMatchesAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID string) {
	t.Helper()
	var mismatch int
	if err := pool.QueryRow(ctx, `
		WITH projected AS (
			SELECT roster.player_id, roster.team_id, roster.team_slot,
			       roster.slot_index AS logical_slot, roster.connection_generation
			FROM p2p_match_sessions AS match
			JOIN p2p_match_roster AS roster ON roster.match_id = match.id
			WHERE match.match_attempt_id = $1
		), frozen AS (
			SELECT player_id, team_id, team_slot, logical_slot, connection_generation
			FROM match_attempt_roster WHERE attempt_id = $1
		), differences AS (
			(SELECT * FROM projected EXCEPT SELECT * FROM frozen)
			UNION ALL
			(SELECT * FROM frozen EXCEPT SELECT * FROM projected)
		)
		SELECT COUNT(*) FROM differences
	`, attemptID).Scan(&mismatch); err != nil {
		t.Fatal(err)
	}
	if mismatch != 0 {
		t.Fatalf("P2P projection differs from frozen roster by %d rows", mismatch)
	}
}
