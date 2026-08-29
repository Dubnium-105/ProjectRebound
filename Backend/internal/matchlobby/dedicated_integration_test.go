package matchlobby

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStrictRosterDedicatedLifecycleAgainstPostgreSQL(t *testing.T) {
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

	matchConfig := config.Defaults.MatchLobby
	matchConfig.StrictRosterV1Enabled = true
	signer, err := NewAdmissionSigner("integration-dedicated-admission", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	currentTime := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return currentTime }
	signer.now = func() time.Time { return currentTime }

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 61_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 62_000_000_000_000_000+suffix))
	serverID := newAdmissionID("integration_server_")
	tokenHash := sha256.Sum256([]byte(serverID))
	if _, err := pool.Exec(ctx, `
		INSERT INTO game_servers (
			id, instance_id, display_name, region, mode, version,
			public_host, public_port, max_players, player_count, state,
			server_token_hash, registration_issuer, token_expires_at,
			last_heartbeat_at, created_at, updated_at
		) VALUES ($1, $2, 'Strict Dedicated Integration', 'hk', 'TDM', '1.0.0',
		          '127.0.0.1', 7777, 8, 0, 'READY', $3, 'integration',
		          $4, $5, $5, $5)
	`, serverID, serverID, tokenHash[:], currentTime.Add(time.Hour), currentTime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM meta_matches WHERE game_server_id = $1", serverID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id IN ($1, $2)", owner.PlayerID, member.PlayerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM meta_match_tickets WHERE player_id IN ($1, $2)", owner.PlayerID, member.PlayerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_servers WHERE id = $1", serverID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id IN ($1, $2)", owner.PlayerID, member.PlayerID)
	})

	first := createDedicatedTwoPlayerReadyLobby(t, ctx, service, owner, member, "Dedicated Timeout", "integration-dedicated-timeout")
	if first.Attempt == nil {
		t.Fatal("Dedicated timeout lobby omitted its attempt")
	}
	assertDedicatedProjectionMatchesAttempt(t, ctx, pool, first.Attempt.AttemptID)
	currentTime = currentTime.Add(time.Duration(matchConfig.ProvisioningSeconds+1) * time.Second)
	if _, err := pool.Exec(ctx, "UPDATE game_servers SET last_heartbeat_at = $2 WHERE id = $1", serverID, currentTime); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	returned, err := service.Get(ctx, first.LobbyID, owner.PlayerID)
	if err != nil || returned.State != StateOpen || returned.Attempt != nil {
		t.Fatalf("Dedicated provisioning timeout did not return OPEN: %+v, %v", returned, err)
	}
	var attemptState, attemptFailure, matchState, ticketState, serverState string
	if err := pool.QueryRow(ctx, `
		SELECT attempt.state, COALESCE(attempt.failure_code, ''), match.state,
		       ticket.state, server.state
		FROM match_attempts AS attempt
		JOIN meta_matches AS match ON match.match_attempt_id = attempt.id
		JOIN meta_match_tickets AS ticket ON ticket.id = match.ticket_id
		JOIN game_servers AS server ON server.id = match.game_server_id
		WHERE attempt.id = $1
	`, first.Attempt.AttemptID).Scan(&attemptState, &attemptFailure, &matchState, &ticketState, &serverState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "ABORTED" || attemptFailure != "DEDICATED_PROVISIONING_TIMEOUT" ||
		matchState != "FAILED" || ticketState != "FAILED" || serverState != "READY" {
		t.Fatalf("inconsistent Dedicated timeout projection: %s %s %s %s %s", attemptState, attemptFailure, matchState, ticketState, serverState)
	}

	for _, actor := range []Actor{owner, member} {
		if _, err := service.SetReady(ctx, actor, returned.LobbyID, true, returned.RosterRevision); err != nil {
			t.Fatal(err)
		}
	}
	active, err := service.Start(ctx, owner, returned.LobbyID, returned.RosterRevision)
	if err != nil || active.Attempt == nil {
		t.Fatalf("restart Dedicated attempt: %+v, %v", active, err)
	}
	attemptID := active.Attempt.AttemptID
	allocation, err := service.DedicatedAllocation(ctx, serverID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	allocationClaims := decodeAllocationClaims(t, allocation.Allocation)
	if allocationClaims.HostingKind != HostingDedicated || len(allocationClaims.Roster) != 2 {
		t.Fatalf("Dedicated allocation lost its frozen roster: %+v", allocationClaims)
	}
	if _, err := service.DedicatedPayloadInstalled(
		ctx, serverID, attemptID, allocationClaims.AuthoritySessionID,
		"strict-roster-v1", matchConfig.LockedGameSHA256, active.Attempt.RouteGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DedicatedAuthorityReady(ctx, serverID, attemptID, allocationClaims.AuthoritySessionID); err != nil {
		t.Fatal(err)
	}

	connectedGrants := make(map[string]GrantResult)
	for _, actor := range []Actor{owner, member} {
		grant, err := service.JoinGrant(ctx, actor, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		connectedGrants[actor.PlayerID] = grant
		if _, err := service.MarkConnected(
			ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
			actor.PlayerID, decodeJoinGrantJTI(t, grant.Grant), grant.ConnectionGeneration,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.JoinGrant(ctx, owner, attemptID); errorCode(err) != "MATCH_CONNECTION_STILL_ACTIVE" {
		t.Fatalf("live Dedicated connection received a reconnect grant: %v", err)
	}
	liveView, err := service.Get(ctx, active.LobbyID, owner.PlayerID)
	if err != nil || liveView.Local.CanRetry {
		t.Fatalf("live Dedicated member advertised reconnect capability: %+v, %v", liveView.Local, err)
	}
	if _, err := service.MarkDisconnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		owner.PlayerID, connectedGrants[owner.PlayerID].ConnectionGeneration,
	); err != nil {
		t.Fatal(err)
	}
	disconnectedView, err := service.Get(ctx, active.LobbyID, owner.PlayerID)
	if err != nil || !disconnectedView.Local.CanRetry {
		t.Fatalf("disconnected Dedicated member lacked reconnect capability: %+v, %v", disconnectedView.Local, err)
	}
	if _, err := service.MarkDisconnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		owner.PlayerID, connectedGrants[owner.PlayerID].ConnectionGeneration,
	); err != nil {
		t.Fatalf("repeated Dedicated disconnect was not idempotent: %v", err)
	}
	reconnect, err := service.JoinGrant(ctx, owner, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if reconnect.ConnectionGeneration != connectedGrants[owner.PlayerID].ConnectionGeneration+1 {
		t.Fatalf("reconnect generation = %d", reconnect.ConnectionGeneration)
	}
	if _, err := service.MarkConnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		owner.PlayerID, decodeJoinGrantJTI(t, connectedGrants[owner.PlayerID].Grant),
		connectedGrants[owner.PlayerID].ConnectionGeneration,
	); errorCode(err) != "MATCH_JOIN_GRANT_NOT_CONSUMABLE" {
		t.Fatalf("old Dedicated grant remained consumable: %v", err)
	}
	if _, err := service.MarkConnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		owner.PlayerID, decodeJoinGrantJTI(t, reconnect.Grant), reconnect.ConnectionGeneration,
	); err != nil {
		t.Fatal(err)
	}
	reconnectedView, err := service.Get(ctx, active.LobbyID, owner.PlayerID)
	if err != nil || reconnectedView.Local.CanRetry {
		t.Fatalf("reconnected Dedicated member advertised reconnect capability: %+v, %v", reconnectedView.Local, err)
	}
	assertDedicatedProjectionMatchesAttempt(t, ctx, pool, attemptID)
	terminal, err := service.Complete(ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, true, "")
	if err != nil || terminal.State != StateCompleted {
		t.Fatalf("complete Dedicated attempt: %+v, %v", terminal, err)
	}
}

func createDedicatedTwoPlayerReadyLobby(
	t *testing.T,
	ctx context.Context,
	service *Service,
	owner, member Actor,
	name, idempotencyKey string,
) Snapshot {
	t.Helper()
	created, err := service.Create(ctx, owner, CreateInput{
		DisplayName: name, HostingKind: HostingDedicated,
		Mode: "TDM", Region: "hk", ClientVersion: "1.0.0", ProtocolVersion: 1,
		TeamOneCapacity: 2, TeamTwoCapacity: 2, TeamID: 1,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(ctx, member, created.Snapshot.LobbyID, 2, created.Snapshot.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []Actor{owner, member} {
		if _, err := service.SetReady(ctx, actor, joined.LobbyID, true, joined.RosterRevision); err != nil {
			t.Fatal(err)
		}
	}
	frozen, err := service.Start(ctx, owner, joined.LobbyID, joined.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func assertDedicatedProjectionMatchesAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID string) {
	t.Helper()
	var mismatch int
	if err := pool.QueryRow(ctx, `
		WITH projected AS (
			SELECT roster.player_id, roster.team_id, roster.team_slot,
			       roster.logical_slot, roster.connection_generation
			FROM meta_matches AS match
			JOIN meta_match_players AS roster ON roster.match_id = match.id
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
		t.Fatalf("Dedicated projection differs from frozen roster by %d rows", mismatch)
	}
}
