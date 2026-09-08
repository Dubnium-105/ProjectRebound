package matchlobby

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/connection"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2pbattlelog"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestConnectionCreateRechecksManagedScopeAgainstPostgreSQL places the
// authoritative mutation immediately after the preflight read and before the
// connection write.  Each case proves that the write transaction, rather than
// only ResolveConnectionParticipants, rejects stale attempt/route/room state.
func TestConnectionCreateRechecksManagedScopeAgainstPostgreSQL(t *testing.T) {
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
	signer, err := NewAdmissionSigner("integration-connection-scope", testAdmissionPrivateKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	matchService := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	matchService.SetP2PTransport(p2pService)
	matchService.SetP2PMatchProjector(battleLogService)

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 91_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 92_000_000_000_000_000+suffix))
	playerIDs := []string{owner.PlayerID, member.PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE host_player_id = ANY($1) OR peer_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	tests := []struct {
		name   string
		mutate func(context.Context, string, string) error
	}{
		{
			name: "attempt abort after preflight",
			mutate: func(ctx context.Context, _, attemptID string) error {
				_, err := pool.Exec(ctx, `UPDATE match_attempts SET state = 'ABORTED' WHERE id = $1`, attemptID)
				return err
			},
		},
		{
			name: "route generation changes after preflight",
			mutate: func(ctx context.Context, _, attemptID string) error {
				_, err := pool.Exec(ctx, `UPDATE match_attempts SET route_generation = route_generation + 1 WHERE id = $1`, attemptID)
				return err
			},
		},
		{
			name: "room closes after preflight",
			mutate: func(ctx context.Context, roomID, _ string) error {
				_, err := pool.Exec(ctx, `UPDATE p2p_rooms SET state = 'CLOSED', closed_at = NOW() WHERE id = $1`, roomID)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, _ := createTwoPlayerReadyLobby(
				t, ctx, matchService, owner, member,
				"Connection scope race "+test.name, "integration-connection-scope-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if frozen.Attempt == nil {
				t.Fatal("frozen lobby omitted current attempt")
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cleanupCancel()
				_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE room_id = $1", frozen.P2PRoomID)
				_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE id = $1", frozen.LobbyID)
				_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE id = $1", frozen.P2PRoomID)
			})

			authorizer := &connectionScopeRaceAuthorizer{
				rooms:  p2pService,
				mutate: func(ctx context.Context) error { return test.mutate(ctx, frozen.P2PRoomID, frozen.Attempt.AttemptID) },
			}
			service := connection.NewService(
				connection.NewRepository(pool), authorizer, connection.NewHub(8), config.Defaults.Connection,
			)
			_, err := service.Create(ctx, connection.Actor{
				PlayerID: member.PlayerID, AccountStatus: member.AccountStatus,
			}, connection.CreateInput{RoomID: frozen.P2PRoomID})
			var serviceErr *connection.ServiceError
			if !errors.As(err, &serviceErr) || serviceErr.Code != "CONNECTION_ATTEMPT_SCOPE_REQUIRED" {
				t.Fatalf("stale managed connection result = %v, want CONNECTION_ATTEMPT_SCOPE_REQUIRED", err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM connections WHERE room_id = $1`, frozen.P2PRoomID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("stale managed connection inserted %d rows", count)
			}
		})
	}
}

type connectionScopeRaceAuthorizer struct {
	rooms  *p2proom.Service
	mutate func(context.Context) error
}

func (a *connectionScopeRaceAuthorizer) ResolveConnectionParticipants(ctx context.Context, roomID, actorPlayerID, requestedPeerPlayerID string) (string, string, error) {
	hostPlayerID, peerPlayerID, _, _, _, _, err := a.ResolveConnectionParticipantsWithScope(ctx, roomID, actorPlayerID, requestedPeerPlayerID)
	return hostPlayerID, peerPlayerID, err
}

func (a *connectionScopeRaceAuthorizer) ResolveConnectionParticipantsWithScope(ctx context.Context, roomID, actorPlayerID, requestedPeerPlayerID string) (string, string, string, string, int64, int32, error) {
	hostPlayerID, peerPlayerID, lobbyID, attemptID, rosterRevision, routeGeneration, err := a.rooms.ResolveConnectionParticipantsWithScope(ctx, roomID, actorPlayerID, requestedPeerPlayerID)
	if err != nil {
		return "", "", "", "", 0, 0, err
	}
	if err := a.mutate(ctx); err != nil {
		return "", "", "", "", 0, 0, err
	}
	return hostPlayerID, peerPlayerID, lobbyID, attemptID, rosterRevision, routeGeneration, nil
}

func (*connectionScopeRaceAuthorizer) MarkConnectionEstablished(context.Context, string) error {
	return nil
}
