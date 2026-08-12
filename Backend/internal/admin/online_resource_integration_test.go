package admin

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdministrativeResourceRetirementAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	now := time.Now().UTC().Truncate(time.Second)
	adminID := fmt.Sprintf("adm_retire_%d", suffix)
	playerID := fmt.Sprintf("p_retire_%d", suffix)
	closedRoomID := fmt.Sprintf("room_closed_%d", suffix)
	activeRoomID := fmt.Sprintf("room_active_%d", suffix)
	deletableServerID := fmt.Sprintf("gs_delete_%d", suffix)
	bannedServerID := fmt.Sprintf("gs_ban_%d", suffix)
	deletableInstance := fmt.Sprintf("delete-instance-%d", suffix)
	bannedInstance := fmt.Sprintf("ban-instance-%d", suffix)
	serverTokenHash := sha256.Sum256([]byte(deletableServerID))
	bannedTokenHash := sha256.Sum256([]byte(bannedServerID))
	roomTokenHash := sha256.Sum256([]byte(closedRoomID))
	activeRoomTokenHash := sha256.Sum256([]byte(activeRoomID))

	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_users (
			id, username, display_name, password_hash, status, mfa_required,
			created_at, updated_at
		) VALUES ($1, $2, 'Resource Retirement', 'test-only', 'ACTIVE', TRUE, $3, $3)
	`, adminID, adminID+"@example.test", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider, auth_level,
			created_at, updated_at
		) VALUES ($1, $2, 'Retirement Host', 'ACTIVE', 'steam_ticket', 'verified', $3, $3)
	`, playerID, fmt.Sprintf("76561%012d", suffix%1_000_000_000_000), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode, version,
			max_players, player_count, state, last_heartbeat_at, created_at, updated_at,
			closed_at, transport_kind, expires_at
		) VALUES
			($1, $3, $4, 'Closed placeholder', 'asia-hk', 'tdm', '1.0.0', 8, 0,
			 'CLOSED', $6, $6, $6, $6, 'LEGACY_RELAY', $7),
			($2, $3, $5, 'Active room', 'asia-hk', 'tdm', '1.0.0', 8, 1,
			 'LOBBY', $6, $6, $6, NULL, 'LEGACY_RELAY', $7)
	`, closedRoomID, activeRoomID, playerID, roomTokenHash[:], activeRoomTokenHash[:], now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO game_servers (
			id, instance_id, display_name, region, mode, version, public_host, public_port,
			max_players, player_count, state, server_token_hash, registration_issuer,
			token_expires_at, token_revoked_at, last_heartbeat_at, created_at, updated_at
		) VALUES
			($1, $3, 'Offline server', 'asia-hk', 'tdm', '1.0.0', '203.0.113.10', 7777,
			 8, 0, 'OFFLINE', $5, 'integration', $7, $6, $6, $6, $6),
			($2, $4, 'Server to ban', 'asia-hk', 'tdm', '1.0.0', '203.0.113.11', 7778,
			 8, 1, 'READY', $8, 'integration', $7, NULL, $6, $6, $6)
	`, deletableServerID, bannedServerID, deletableInstance, bannedInstance,
		serverTokenHash[:], now, now.Add(time.Hour), bannedTokenHash[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_audit_logs WHERE admin_id = $1", adminID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_server_registration_tokens WHERE instance_id = ANY($1)", []string{deletableInstance, bannedInstance})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_servers WHERE id = ANY($1)", []string{deletableServerID, bannedServerID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE id = ANY($1)", []string{closedRoomID, activeRoomID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_users WHERE id = $1", adminID)
	})

	registrations := gameserverregistration.NewRepository()
	service := NewOnlineService(
		pool, NewRepository(), nil, registrations,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	service.now = func() time.Time { return now.Add(time.Minute) }
	meta := RequestMeta{AdminID: adminID, RequestID: "req_retirement", IPAddress: "192.0.2.10", UserAgent: "integration-test"}

	if err := service.DeleteRoom(ctx, activeRoomID, "Active rooms must not be deleted", meta); adminServiceCode(err) != "ROOM_MUST_BE_CLOSED" {
		t.Fatalf("active room deletion error = %v", err)
	}
	if err := service.DeleteRoom(ctx, closedRoomID, "Remove legacy placeholder room", meta); err != nil {
		t.Fatal(err)
	}
	if _, err := p2proom.NewRepository(pool).Get(ctx, closedRoomID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted room remained visible: %v", err)
	}

	if err := service.DeleteGameServer(ctx, deletableServerID, "Remove offline placeholder server", meta); err != nil {
		t.Fatal(err)
	}
	if _, err := gameserver.NewRepository(pool).Get(ctx, deletableServerID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted game server remained visible: %v", err)
	}

	banned, err := service.ChangeGameServerState(ctx, bannedServerID, "ban", "Block abusive server instance", meta)
	if err != nil {
		t.Fatal(err)
	}
	if banned.State != gameserver.StateOffline || banned.BannedAt == nil || banned.BanReason == "" {
		t.Fatalf("banned game server = %#v", banned)
	}
	if _, err := service.CreateGameServerRegistration(ctx, GameServerRegistrationInput{
		InstanceID: bannedInstance, ExpiresInHours: 24, Reason: "Must reject banned instance",
	}, meta); adminServiceCode(err) != "GAME_SERVER_BANNED" {
		t.Fatalf("administrator credential issuance error = %v", err)
	}

	authority, err := gameserver.NewAuthority(config.Defaults.GameServer, "development")
	if err != nil {
		t.Fatal(err)
	}
	gameServerService := gameserver.NewService(gameserver.NewRepository(pool), registrations, config.Defaults.GameServer, authority)
	if _, err := gameServerService.IssueRegistrationCredential(ctx, gameserver.RegistrationCredentialInput{
		InstanceID: bannedInstance, PlayerID: playerID,
	}); gameServerServiceCode(err) != "GAME_SERVER_BANNED" {
		t.Fatalf("player credential issuance error = %v", err)
	}
	_, csrPEM, err := gameserver.NewNodeIdentity("banned-integration-server")
	if err != nil {
		t.Fatal(err)
	}
	registrationToken, _, err := gameserverregistration.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gameServerService.Register(ctx, gameserver.RegistrationInput{
		InstanceID: bannedInstance, DisplayName: "Banned server", Region: "asia-hk",
		Mode: "tdm", Version: "1.0.0", PublicHost: "203.0.113.11",
		PublicPort: 7778, MaxPlayers: 8, CSRPEM: csrPEM,
	}, registrationToken); gameServerServiceCode(err) != "GAME_SERVER_BANNED" {
		t.Fatalf("banned registration error = %v", err)
	}
	if err := service.DeleteGameServer(ctx, bannedServerID, "Bans must remain visible", meta); adminServiceCode(err) != "GAME_SERVER_BANNED" {
		t.Fatalf("banned game server deletion error = %v", err)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM admin_audit_logs
		WHERE admin_id = $1
		  AND action IN ('P2P_ROOM_DELETED', 'GAME_SERVER_DELETED', 'GAME_SERVER_BANNED')
	`, adminID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("retirement audit count = %d", auditCount)
	}
}

func adminServiceCode(err error) string {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Code
	}
	return ""
}

func gameServerServiceCode(err error) string {
	var serviceError *gameserver.ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Code
	}
	return ""
}
