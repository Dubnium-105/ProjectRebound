package matchlobby

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2pbattlelog"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthoritativeTransportAndConnectionScopeAgainstPostgreSQL(t *testing.T) {
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
	secretBox, _, err := p2proom.NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	p2pService.SetVNT(nil, secretBox)
	battleLogService := p2pbattlelog.NewService(p2pbattlelog.NewRepository(pool), config.Defaults.P2PBattleLog)
	p2pService.SetMatchLifecycle(battleLogService)
	matchConfig := config.Defaults.MatchLobby
	matchConfig.AcceptNewLobbies = true
	signer, err := NewAdmissionSigner("integration-transport-scope", testAdmissionPrivateKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	service.SetP2PTransport(p2pService)
	service.SetP2PMatchProjector(battleLogService)

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 81_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 82_000_000_000_000_000+suffix))
	playerIDs := []string{owner.PlayerID, member.PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	frozen, _ := createTwoPlayerReadyLobby(t, ctx, service, owner, member, "Transport scope", "integration-transport-scope")
	if frozen.Attempt == nil {
		t.Fatal("frozen lobby omitted current attempt")
	}
	var authoritySession string
	if err := pool.QueryRow(ctx, `
		SELECT authority_session_id FROM match_attempts WHERE id = $1
	`, frozen.Attempt.AttemptID).Scan(&authoritySession); err != nil {
		t.Fatal(err)
	}
	// The host authority is allowed to keep a still-provisioning managed room
	// alive. This renews only the authenticated attempt/room lease; it does not
	// mark Payload installed or claim a native connection.
	if err := service.P2PAuthorityHeartbeat(ctx, owner, authoritySession, frozen.Attempt.AttemptID); err != nil {
		t.Fatalf("provisioning authority heartbeat: %v", err)
	}
	var attemptState, payloadInstalled string
	if err := pool.QueryRow(ctx, `
		SELECT state, CASE WHEN payload_installed_at IS NULL THEN 'false' ELSE 'true' END
		FROM match_attempts WHERE id = $1
	`, frozen.Attempt.AttemptID).Scan(&attemptState, &payloadInstalled); err != nil {
		t.Fatal(err)
	}
	if attemptState != string(AttemptProvisioning) || payloadInstalled != "false" {
		t.Fatalf("provisioning heartbeat changed native readiness state: attempt=%s payload_installed=%s", attemptState, payloadInstalled)
	}
	scope := TransportScopeRequest{
		AttemptID: frozen.Attempt.AttemptID, RosterRevision: frozen.Attempt.RosterRevision,
		RouteGeneration: frozen.Attempt.RouteGeneration,
	}
	projection, err := service.Transport(ctx, member, scope)
	if err != nil {
		t.Fatalf("member transport projection: %v", err)
	}
	if projection.AttemptID != scope.AttemptID || projection.LobbyID != frozen.LobbyID || projection.Room.RoomID != frozen.P2PRoomID {
		t.Fatalf("transport projection = %#v", projection)
	}
	if _, err := service.Transport(ctx, member, TransportScopeRequest{
		AttemptID: scope.AttemptID, RosterRevision: scope.RosterRevision, RouteGeneration: scope.RouteGeneration + 1,
	}); errorCode(err) != "MATCH_TRANSPORT_SCOPE_MISMATCH" {
		t.Fatalf("stale route generation error = %v", err)
	}

	if _, _, err := p2pService.ResolveConnectionParticipants(ctx, frozen.P2PRoomID, owner.PlayerID, member.PlayerID); err != nil {
		t.Fatalf("current frozen roster connection rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE match_attempts SET state = 'ABORTED' WHERE id = $1", scope.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p2pService.ResolveConnectionParticipants(ctx, frozen.P2PRoomID, owner.PlayerID, member.PlayerID); errorCodeP2P(err) != "CONNECTION_ATTEMPT_SCOPE_REQUIRED" {
		t.Fatalf("aborted attempt connection error = %v", err)
	}
}

func errorCodeP2P(err error) string {
	if serviceError, ok := err.(*p2proom.ServiceError); ok {
		return serviceError.Code
	}
	return ""
}
