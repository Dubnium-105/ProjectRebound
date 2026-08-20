package metaserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type p2pLoadoutTestAuthenticator struct{ principal auth.Principal }

func (a p2pLoadoutTestAuthenticator) AuthenticateAccess(context.Context, string) (auth.Principal, error) {
	return a.principal, nil
}

func TestP2PRoomMemberLoadoutsAuthorizationAndHTTPAgainstPostgreSQL(t *testing.T) {
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
	roomID := fmt.Sprintf("p2pr_meta_loadout_%d", suffix)
	playerIDs := []string{
		fmt.Sprintf("p_meta_loadout_host_%d", suffix),
		fmt.Sprintf("p_meta_loadout_member_%d", suffix),
		fmt.Sprintf("p_meta_loadout_outsider_%d", suffix),
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for index, playerID := range playerIDs {
		steamID := fmt.Sprintf("76%015d", (suffix+int64(index))%1_000_000_000_000_000)
		if _, err := pool.Exec(ctx, `
			INSERT INTO players (
				id, steam_id, persona_name, account_status, auth_provider,
				auth_level, created_at, updated_at
			) VALUES ($1, $2, 'P2P loadout integration player', 'ACTIVE',
			          'steam_ticket', 'verified', $3, $3)
		`, playerID, steamID, now); err != nil {
			t.Fatalf("insert player %s: %v", playerID, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM p2p_rooms WHERE id = $1`, roomID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id = ANY($1)`, playerIDs)
	})

	hostTokenHash := sha256.Sum256([]byte(roomID))
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode,
			version, max_players, player_count, state, last_heartbeat_at,
			created_at, updated_at, transport_kind, expires_at
		) VALUES (
			$1, $2, $3, 'P2P loadout room', 'hgh', 'default',
			'1.1.0', 4, 2, 'CONNECTING', $4, $4, $4, 'LEGACY_RELAY', $5
		)
	`, roomID, playerIDs[0], hostTokenHash[:], now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
		VALUES ($1, $2, 'HOST', 'ACTIVE', $4), ($1, $3, 'MEMBER', 'ACTIVE', $4)
	`, roomID, playerIDs[0], playerIDs[1], now); err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(pool, 90*time.Second)
	repository.now = func() time.Time { return now }
	snapshot := json.RawMessage(`{
		"roleId":"PEACE",
		"primaryWeapon":"PEACE_RU-AKM",
		"secondaryWeapon":"PEACE_RU-APS",
		"meleeWeapon":"MELEE-KNIFE"
	}`)
	digest := sha256.Sum256(snapshot)
	if _, err := repository.PutLoadout(ctx, playerIDs[1], "PEACE", snapshot, digest[:], 0); err != nil {
		t.Fatal(err)
	}

	if err := repository.AuthorizeP2PRoomLoadoutRead(ctx, playerIDs[0], roomID, playerIDs[1]); err != nil {
		t.Fatalf("host authorization failed: %v", err)
	}
	if err := repository.AuthorizeP2PRoomLoadoutRead(ctx, playerIDs[1], roomID, playerIDs[1]); metaErrorCode(err) != "META_P2P_ROOM_HOST_REQUIRED" {
		t.Fatalf("member authorization error=%v", err)
	}
	if err := repository.AuthorizeP2PRoomLoadoutRead(ctx, playerIDs[0], roomID, playerIDs[2]); metaErrorCode(err) != "META_P2P_ROOM_MEMBER_INACTIVE" {
		t.Fatalf("outsider authorization error=%v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE p2p_rooms SET state = 'LOBBY' WHERE id = $1`, roomID); err != nil {
		t.Fatal(err)
	}
	if err := repository.AuthorizeP2PRoomLoadoutRead(ctx, playerIDs[0], roomID, playerIDs[1]); metaErrorCode(err) != "META_P2P_ROOM_NOT_RUNNING" {
		t.Fatalf("lobby authorization error=%v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_rooms SET state = 'RUNNING', expires_at = $2 WHERE id = $1
	`, roomID, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.AuthorizeP2PRoomLoadoutRead(ctx, playerIDs[0], roomID, playerIDs[1]); metaErrorCode(err) != "META_P2P_ROOM_NOT_RUNNING" {
		t.Fatalf("expired authorization error=%v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_rooms SET expires_at = $2 WHERE id = $1
	`, roomID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_room_members SET status = 'LEFT', left_at = $3
		WHERE room_id = $1 AND player_id = $2
	`, roomID, playerIDs[1], now); err != nil {
		t.Fatal(err)
	}
	if err := repository.AuthorizeP2PRoomLoadoutRead(ctx, playerIDs[0], roomID, playerIDs[1]); metaErrorCode(err) != "META_P2P_ROOM_MEMBER_INACTIVE" {
		t.Fatalf("inactive member authorization error=%v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_room_members SET status = 'ACTIVE', left_at = NULL
		WHERE room_id = $1 AND player_id = $2
	`, roomID, playerIDs[1]); err != nil {
		t.Fatal(err)
	}

	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(
		repository, nil, 1, "", time.Minute, time.Minute, 1024*1024, definitions,
	)
	handler := NewHTTPHandler(
		service, repository, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	serveAs := func(playerID, token string) *httptest.ResponseRecorder {
		router := chi.NewRouter()
		router.With(
			auth.RequireAccess(p2pLoadoutTestAuthenticator{principal: auth.Principal{
				Player:    player.Player{ID: playerID, AccountStatus: player.AccountStatusActive},
				AuthLevel: player.AuthLevelVerified, SteamVerified: true,
			}}, slog.New(slog.NewTextHandler(io.Discard, nil))),
			auth.RequireActive,
			auth.RequireVerified,
		).Get(
			"/v1/meta/p2p-rooms/{room_id}/members/{player_id}/loadouts",
			handler.P2PRoomMemberLoadouts,
		)
		request := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/v1/meta/p2p-rooms/%s/members/%s/loadouts", roomID, playerIDs[1]),
			nil,
		)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	recorder := serveAs(playerIDs[0], "host-access-token")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Body.Len() > p2pRoomLoadoutResponseMaxBytes {
		t.Fatalf("host response=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var envelope struct {
		Data P2PRoomMemberLoadouts `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != 1 || envelope.Data.RoomID != roomID ||
		envelope.Data.PlayerID != playerIDs[1] || len(envelope.Data.Loadouts) != 1 ||
		len(envelope.Data.Loadouts[0].WeaponConfigs) != 2 {
		t.Fatalf("host response data=%#v", envelope.Data)
	}
	if recorder := serveAs(playerIDs[1], "member-access-token"); recorder.Code != http.StatusForbidden {
		t.Fatalf("member HTTP response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := serveAs(playerIDs[0], ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated HTTP response=%d body=%s", recorder.Code, recorder.Body.String())
	}

	largeSnapshot := json.RawMessage(`{
		"roleId":"PEACE",
		"primaryWeapon":"PEACE_RU-AKM",
		"padding":"` + strings.Repeat("x", p2pRoomLoadoutResponseMaxBytes) + `"
	}`)
	largeDigest := sha256.Sum256(largeSnapshot)
	if _, err := repository.PutLoadout(
		ctx, playerIDs[1], "PEACE", largeSnapshot, largeDigest[:], 1,
	); err != nil {
		t.Fatal(err)
	}
	recorder = serveAs(playerIDs[0], "host-access-token")
	if recorder.Code != http.StatusRequestEntityTooLarge ||
		recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("oversize response=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}
