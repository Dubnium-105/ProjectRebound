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
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStrictRosterDedicatedSelectionRejectsUnverifiedAndPreventsReadyReentryAgainstPostgreSQL(t *testing.T) {
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
	matchConfig.AcceptNewLobbies = true
	signer, err := NewAdmissionSigner("integration-dedicated-selection", testAdmissionPrivateKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	now := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return now }

	var serverIDs []string
	var playerIDs []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for _, serverID := range serverIDs {
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM meta_matches WHERE game_server_id = $1", serverID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_servers WHERE id = $1", serverID)
		}
		for _, playerID := range playerIDs {
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = $1", playerID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM meta_match_tickets WHERE player_id = $1", playerID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
		}
	})

	candidates := []struct {
		name     string
		verified bool
		version  string
	}{
		{name: "legacy", verified: true, version: "strict-roster-v1"},
		{name: "unverified", verified: false, version: strictNativeAdmissionVersion},
	}
	for index, candidate := range candidates {
		suffix := uint64(time.Now().UnixNano())%10_000_000_000_000 + uint64(index)
		owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 63_000_000_000_000_000+suffix))
		member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 64_000_000_000_000_000+suffix))
		playerIDs = append(playerIDs, owner.PlayerID, member.PlayerID)
		serverID := newAdmissionID("ac045_" + candidate.name + "_")
		serverIDs = append(serverIDs, serverID)
		insertStrictDedicatedCandidate(t, ctx, pool, serverID, now, candidate.verified, candidate.version, matchConfig.LockedGameSHA256)

		prepared := prepareDedicatedTwoPlayerReadyLobby(t, ctx, service, owner, member, "AC045 "+candidate.name, "ac045-"+candidate.name+"-"+serverID)
		if _, err := service.Start(ctx, owner, prepared.LobbyID, prepared.RosterRevision); errorCode(err) != "MATCH_LOBBY_NO_DEDICATED_SERVER" {
			t.Fatalf("%s candidate was selected despite missing strict capability: %v", candidate.name, err)
		}
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM game_servers WHERE id = $1`, serverID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "READY" {
			t.Fatalf("%s rejected candidate changed state to %s", candidate.name, state)
		}
	}

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 65_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 66_000_000_000_000_000+suffix))
	playerIDs = append(playerIDs, owner.PlayerID, member.PlayerID)
	serverID := newAdmissionID("ac045_valid_")
	serverIDs = append(serverIDs, serverID)
	token := fmt.Sprintf("gst_%064x", suffix)
	insertStrictDedicatedCandidateWithToken(t, ctx, pool, serverID, token, now, true, strictNativeAdmissionVersion, matchConfig.LockedGameSHA256)

	first := prepareDedicatedTwoPlayerReadyLobby(t, ctx, service, owner, member, "AC045 valid", "ac045-valid-"+serverID)
	frozen, err := service.Start(ctx, owner, first.LobbyID, first.RosterRevision)
	if err != nil || frozen.Attempt == nil {
		t.Fatalf("strict candidate was not allocated: %+v, %v", frozen, err)
	}
	if frozen.Attempt.State != AttemptProvisioning {
		t.Fatalf("strict candidate attempt state = %s, want PROVISIONING", frozen.Attempt.State)
	}

	gameServerService := gameserver.NewService(
		gameserver.NewRepository(pool), gameserverregistration.NewRepository(), config.Defaults.GameServer,
	)
	heartbeat, err := gameServerService.Heartbeat(ctx, serverID, token, gameserver.HeartbeatInput{
		State: gameserver.StateReady, PlayerCount: 0,
		NativeAdmissionVerified: true, NativeAdmissionVersion: strictNativeAdmissionVersion,
		NativeAdmissionGameSHA256: matchConfig.LockedGameSHA256,
	})
	if err != nil {
		t.Fatalf("formal READY heartbeat failed: %v", err)
	}
	if heartbeat.State != gameserver.StateReserved {
		t.Fatalf("formal READY heartbeat re-entered allocation pool: state=%s", heartbeat.State)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM game_servers WHERE id = $1`, serverID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(gameserver.StateReserved) {
		t.Fatalf("persisted server state after READY heartbeat = %s, want RESERVED", state)
	}

	secondOwner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 67_000_000_000_000_000+suffix))
	secondMember := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 68_000_000_000_000_000+suffix))
	playerIDs = append(playerIDs, secondOwner.PlayerID, secondMember.PlayerID)
	second := prepareDedicatedTwoPlayerReadyLobby(t, ctx, service, secondOwner, secondMember, "AC045 second", "ac045-second-"+serverID)
	if _, err := service.Start(ctx, secondOwner, second.LobbyID, second.RosterRevision); errorCode(err) != "MATCH_LOBBY_NO_DEDICATED_SERVER" {
		t.Fatalf("server with active strict assignment was allocated a second time: %v", err)
	}
}

func insertStrictDedicatedCandidate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverID string, now time.Time, verified bool, version, gameSHA256 string) {
	t.Helper()
	token := fmt.Sprintf("gst_%064x", uint64(time.Now().UnixNano()))
	insertStrictDedicatedCandidateWithToken(t, ctx, pool, serverID, token, now, verified, version, gameSHA256)
}

func insertStrictDedicatedCandidateWithToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverID, token string, now time.Time, verified bool, version, gameSHA256 string) {
	t.Helper()
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(ctx, `
		INSERT INTO game_servers (
			id, instance_id, display_name, region, mode, version,
			public_host, public_port, max_players, player_count, state,
			server_token_hash, registration_issuer, token_expires_at,
			last_heartbeat_at, created_at, updated_at,
			native_admission_verified, native_admission_version,
			native_admission_game_sha256, native_admission_verified_at
		) VALUES ($1, $2, 'AC045 candidate', 'hk', 'TDM', '1.0.0',
		          '127.0.0.1', 7777, 8, 0, 'READY', $3, 'integration',
		          $4::timestamptz, $5::timestamptz, $5::timestamptz, $5::timestamptz,
		          $6::boolean, $7::varchar, NULLIF($8::varchar, ''),
		          CASE WHEN $6::boolean THEN $5::timestamptz ELSE NULL::timestamptz END)
	`, serverID, serverID, tokenHash[:], now.Add(time.Hour), now, verified, version, gameSHA256); err != nil {
		t.Fatal(err)
	}
}

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
	matchConfig.AcceptNewLobbies = true
	signer, err := NewAdmissionSigner("integration-dedicated-admission", testAdmissionPrivateKey(), "test")
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
			last_heartbeat_at, created_at, updated_at,
			native_admission_verified, native_admission_version,
			native_admission_game_sha256, native_admission_verified_at
		) VALUES ($1, $2, 'Strict Dedicated Integration', 'hk', 'TDM', '1.0.0',
		          '127.0.0.1', 7777, 8, 0, 'READY', $3, 'integration',
		          $4, $5, $5, $5, TRUE, 'strict-roster-v2', $6, $5)
	`, serverID, serverID, tokenHash[:], currentTime.Add(time.Hour), currentTime, matchConfig.LockedGameSHA256); err != nil {
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
	if _, err := service.CurrentMemberConnection(ctx, member, first.Attempt.AttemptID); errorCode(err) != "MATCH_CONNECTION_NOT_CONNECTED" {
		t.Fatalf("superseded Dedicated attempt exposed connection evidence: %v", err)
	}
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
		"strict-roster-v2", matchConfig.LockedGameSHA256, active.Attempt.RouteGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DedicatedAuthorityReady(ctx, serverID, attemptID, allocationClaims.AuthoritySessionID, "world-dedicated-primary", "native-host-dedicated-primary"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CurrentMemberConnection(ctx, member, attemptID); errorCode(err) != "MATCH_CONNECTION_NOT_CONNECTED" {
		t.Fatalf("unconfirmed Dedicated member exposed connection evidence: %v", err)
	}

	connectedGrants := make(map[string]GrantResult)
	for _, actor := range []Actor{owner, member} {
		var grant GrantResult
		var err error
		if actor.PlayerID == member.PlayerID {
			grant, err = service.JoinGrantWithIdempotency(ctx, actor, attemptID, "member-scope-proof")
		} else {
			grant, err = service.JoinGrant(ctx, actor, attemptID)
		}
		if err != nil {
			t.Fatal(err)
		}
		if grant.AuthoritySessionID != allocationClaims.AuthoritySessionID ||
			grant.WorldInstanceID != "world-dedicated-primary" ||
			grant.RosterRevision != active.Attempt.RosterRevision ||
			grant.RouteGeneration != active.Attempt.RouteGeneration ||
			grant.PlayerID != actor.PlayerID || grant.GrantJTI == "" {
			t.Fatalf("join grant omitted exact signed scope: %+v", grant)
		}
		grantClaims := decodeJoinGrantClaims(t, grant.Grant)
		if grantClaims.AuthoritySessionID != grant.AuthoritySessionID ||
			grantClaims.WorldInstanceID != grant.WorldInstanceID ||
			grantClaims.RosterRevision != grant.RosterRevision ||
			grantClaims.RouteGeneration != grant.RouteGeneration ||
			grantClaims.PlayerID != grant.PlayerID || grantClaims.TokenID != grant.GrantJTI {
			t.Fatalf("join grant JWT scope differs from response: claims=%+v response=%+v", grantClaims, grant)
		}
		if actor.PlayerID == member.PlayerID {
			replayed, replayErr := service.JoinGrantWithIdempotency(ctx, actor, attemptID, "member-scope-proof")
			if replayErr != nil {
				t.Fatalf("idempotent join grant replay: %v", replayErr)
			}
			if replayed.GrantJTI != grant.GrantJTI || replayed.Grant != grant.Grant ||
				replayed.AuthoritySessionID != grant.AuthoritySessionID ||
				replayed.WorldInstanceID != grant.WorldInstanceID ||
				replayed.RosterRevision != grant.RosterRevision ||
				replayed.RouteGeneration != grant.RouteGeneration ||
				replayed.PlayerID != grant.PlayerID ||
				replayed.ConnectionGeneration != grant.ConnectionGeneration {
				t.Fatalf("idempotent replay changed signed scope: first=%+v replay=%+v", grant, replayed)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE match_admission_grants SET expires_at = $2 WHERE jti = $1
			`, grant.GrantJTI, currentTime.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := service.JoinGrantWithIdempotency(ctx, actor, attemptID, "member-scope-proof"); errorCode(err) != "MATCH_JOIN_INTENT_COMPLETED" {
				t.Fatalf("expired join intent was replayed: %v", err)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE match_admission_grants SET expires_at = $2 WHERE jti = $1
			`, grant.GrantJTI, grant.ExpiresAt); err != nil {
				t.Fatal(err)
			}
		}
		connectedGrants[actor.PlayerID] = grant
		grantJTI := decodeJoinGrantJTI(t, grant.Grant)
		nativeNonce := "native-dedicated-" + actor.PlayerID
		if _, err := service.MarkAdmissionDelivered(
			ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, grantJTI,
		); err != nil {
			t.Fatal(err)
		}
		reservation, err := service.ReserveAdmission(
			ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
			"world-dedicated-primary", actor.PlayerID, grantJTI, nativeNonce, grant.ConnectionGeneration,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ConfirmConnected(
			ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
			reservation.WorldInstanceID, actor.PlayerID, grantJTI, nativeNonce, grant.ConnectionGeneration,
		); err != nil {
			t.Fatal(err)
		}
		assertMemberConnectionEvidence(t, ctx, service, actor, attemptID, MemberConnectionEvidence{
			AttemptID: attemptID, AuthoritySessionID: allocationClaims.AuthoritySessionID,
			WorldInstanceID: "world-dedicated-primary", RosterRevision: active.Attempt.RosterRevision,
			RouteGeneration: active.Attempt.RouteGeneration, PlayerID: actor.PlayerID,
			Role: "MEMBER", GrantJTI: grantJTI, ConnectionGeneration: grant.ConnectionGeneration,
			NativeConnectionNonce: nativeNonce, ConnectionState: "CONNECTED",
		})
		if actor.PlayerID == owner.PlayerID {
			if _, err := service.CurrentMemberConnection(ctx, member, attemptID); errorCode(err) != "MATCH_CONNECTION_NOT_CONNECTED" {
				t.Fatalf("wrong Dedicated player received connection evidence: %v", err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE match_attempt_roster
		SET connection_generation = connection_generation + 1
		WHERE attempt_id = $1 AND player_id = $2
	`, attemptID, member.PlayerID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CurrentMemberConnection(ctx, member, attemptID); errorCode(err) != "MATCH_CONNECTION_NOT_CONNECTED" {
		t.Fatalf("stale Dedicated generation exposed connection evidence: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE match_attempt_roster
		SET connection_generation = connection_generation - 1
		WHERE attempt_id = $1 AND player_id = $2
	`, attemptID, member.PlayerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE match_attempts SET route_generation = route_generation + 1 WHERE id = $1", attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CurrentMemberConnection(ctx, member, attemptID); errorCode(err) != "MATCH_CONNECTION_NOT_CONNECTED" {
		t.Fatalf("stale Dedicated route exposed connection evidence: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE match_attempts SET route_generation = route_generation - 1 WHERE id = $1", attemptID); err != nil {
		t.Fatal(err)
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
		"world-dedicated-old", owner.PlayerID, "native-dedicated-"+owner.PlayerID,
		connectedGrants[owner.PlayerID].ConnectionGeneration, connectedGrants[owner.PlayerID].RouteGeneration,
	); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("old Dedicated world was accepted for disconnect: %v", err)
	}
	if _, err := service.MarkDisconnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		"world-dedicated-primary", owner.PlayerID, "native-dedicated-stale-xxxxxxxx",
		connectedGrants[owner.PlayerID].ConnectionGeneration, connectedGrants[owner.PlayerID].RouteGeneration,
	); errorCode(err) != "MATCH_CONNECTION_GENERATION_STALE" {
		t.Fatalf("old Dedicated disconnect nonce was accepted: %v", err)
	}
	if _, err := service.MarkDisconnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		"world-dedicated-primary", owner.PlayerID, "native-dedicated-"+owner.PlayerID,
		connectedGrants[owner.PlayerID].ConnectionGeneration, connectedGrants[owner.PlayerID].RouteGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CurrentMemberConnection(ctx, owner, attemptID); errorCode(err) != "MATCH_CONNECTION_NOT_CONNECTED" {
		t.Fatalf("disconnected Dedicated member exposed connection evidence: %v", err)
	}
	disconnectedView, err := service.Get(ctx, active.LobbyID, owner.PlayerID)
	if err != nil || !disconnectedView.Local.CanRetry {
		t.Fatalf("disconnected Dedicated member lacked reconnect capability: %+v, %v", disconnectedView.Local, err)
	}
	if _, err := service.MarkDisconnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		"world-dedicated-primary", owner.PlayerID, "native-dedicated-"+owner.PlayerID,
		connectedGrants[owner.PlayerID].ConnectionGeneration, connectedGrants[owner.PlayerID].RouteGeneration,
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
	reconnectJTI := decodeJoinGrantJTI(t, reconnect.Grant)
	reconnectNonce := "native-dedicated-reconnect-" + owner.PlayerID
	if _, err := service.MarkAdmissionDelivered(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, reconnectJTI,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReserveAdmission(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		"world-dedicated-primary", owner.PlayerID, reconnectJTI, reconnectNonce, reconnect.ConnectionGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmConnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		"world-dedicated-primary", owner.PlayerID, decodeJoinGrantJTI(t, connectedGrants[owner.PlayerID].Grant),
		"native-dedicated-"+owner.PlayerID, connectedGrants[owner.PlayerID].ConnectionGeneration,
	); errorCode(err) != "MATCH_JOIN_GRANT_NOT_CONSUMABLE" {
		t.Fatalf("old Dedicated grant remained consumable: %v", err)
	}
	if _, err := service.ConfirmConnected(
		ctx, serverID, allocationClaims.AuthoritySessionID, attemptID,
		"world-dedicated-primary", owner.PlayerID, reconnectJTI, reconnectNonce, reconnect.ConnectionGeneration,
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
	// Closing an attempt without a BattleLog report must not delete or
	// regenerate the authoritative frozen roster projection.
	assertDedicatedProjectionMatchesAttempt(t, ctx, pool, attemptID)
	var frozenRosterCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM match_attempt_roster WHERE attempt_id = $1`, attemptID).Scan(&frozenRosterCount); err != nil {
		t.Fatal(err)
	}
	if frozenRosterCount != 2 {
		t.Fatalf("closed Dedicated attempt lost frozen roster: count=%d", frozenRosterCount)
	}
	var cleanupState string
	if err := pool.QueryRow(ctx, `SELECT cleanup_state FROM match_attempts WHERE id = $1`, attemptID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "PENDING" {
		t.Fatalf("completed Dedicated attempt cleanup state = %s, want PENDING", cleanupState)
	}
	if _, err := service.NativeCleared(ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, "world-dedicated-old", active.Attempt.RosterRevision, active.Attempt.RouteGeneration); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("old-world Dedicated cleanup acknowledgement was accepted: %v", err)
	}
	if _, err := service.NativeCleared(ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, "world-dedicated-primary", active.Attempt.RosterRevision, active.Attempt.RouteGeneration+1); errorCode(err) != "MATCH_ROUTE_GENERATION_STALE" {
		t.Fatalf("stale Dedicated cleanup route was accepted: %v", err)
	}
	evidence := OwnedProcessExitEvidence{
		EvidenceKind:            "owned_process_exited",
		OwnedProcessID:          45123,
		ProcessStartFingerprint: "win-filetime:0123456789abcdef",
	}
	if _, err := service.DedicatedNativeCleared(ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, "world-dedicated-old", active.Attempt.RosterRevision, active.Attempt.RouteGeneration, evidence); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("Dedicated owned-process cleanup accepted a stale world: %v", err)
	}
	if _, err := service.DedicatedNativeCleared(ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, "world-dedicated-primary", active.Attempt.RosterRevision, active.Attempt.RouteGeneration+1, evidence); errorCode(err) != "MATCH_ROUTE_GENERATION_STALE" {
		t.Fatalf("Dedicated owned-process cleanup accepted a stale route: %v", err)
	}
	if _, err := service.DedicatedNativeCleared(ctx, serverID, allocationClaims.AuthoritySessionID, attemptID, "world-dedicated-primary", active.Attempt.RosterRevision, active.Attempt.RouteGeneration, evidence); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT cleanup_state FROM match_attempts WHERE id = $1`, attemptID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "CLEARED" {
		t.Fatalf("cleared Dedicated attempt cleanup state = %s", cleanupState)
	}
}

func prepareDedicatedTwoPlayerReadyLobby(
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
	return joined
}

func createDedicatedTwoPlayerReadyLobby(
	t *testing.T,
	ctx context.Context,
	service *Service,
	owner, member Actor,
	name, idempotencyKey string,
) Snapshot {
	t.Helper()
	joined := prepareDedicatedTwoPlayerReadyLobby(t, ctx, service, owner, member, name, idempotencyKey)
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
