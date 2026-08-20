package metaserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

func TestRepositoryIsolationAndConcurrentSchedulingAgainstPostgreSQL(t *testing.T) {
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
	playerIDs := []string{
		fmt.Sprintf("meta_it_leader_%d", suffix),
		fmt.Sprintf("meta_it_member_%d", suffix),
		fmt.Sprintf("meta_it_solo_%d", suffix),
		fmt.Sprintf("meta_it_outsider_%d", suffix),
	}
	serverIDs := []string{
		fmt.Sprintf("meta_it_server_a_%d", suffix),
		fmt.Sprintf("meta_it_server_b_%d", suffix),
	}
	serverTokens := map[string]string{
		serverIDs[0]: "gst_" + fmt.Sprintf("meta_it_a_%d", suffix),
		serverIDs[1]: "gst_" + fmt.Sprintf("meta_it_b_%d", suffix),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_matches WHERE game_server_id = ANY($1)`, serverIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_match_tickets WHERE player_id = ANY($1)`, playerIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM meta_parties WHERE leader_player_id = ANY($1)`, playerIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM game_servers WHERE id = ANY($1)`, serverIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM players WHERE id = ANY($1)`, playerIDs)
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	for index, playerID := range playerIDs {
		steamID := fmt.Sprintf("76%015d", (suffix+int64(index))%1_000_000_000_000_000)
		if _, err := pool.Exec(ctx, `
			INSERT INTO players (
				id, steam_id, persona_name, account_status, auth_provider,
				auth_level, created_at, updated_at
			) VALUES ($1, $2, $3, 'ACTIVE', 'steam_ticket', 'verified', $4, $4)
		`, playerID, steamID, "Meta integration player", now); err != nil {
			t.Fatalf("insert player %s: %v", playerID, err)
		}
	}
	for index, serverID := range serverIDs {
		tokenHash := sha256.Sum256([]byte(serverTokens[serverID]))
		if _, err := pool.Exec(ctx, `
			INSERT INTO game_servers (
				id, instance_id, display_name, region, mode, version,
				public_host, public_port, max_players, player_count, state,
				server_token_hash, registration_issuer, token_expires_at,
				last_heartbeat_at, created_at, updated_at
			) VALUES (
				$1, $2, 'Meta integration server', 'hgh', 'default', '1.1.0',
				'127.0.0.1', $3, 8, 0, 'READY', $4, 'integration',
				$5, $6, $6, $6
			)
		`, serverID, serverID, 28000+index, tokenHash[:], now.Add(time.Hour), now); err != nil {
			t.Fatalf("insert game server %s: %v", serverID, err)
		}
	}

	repository := NewRepository(pool, 90*time.Second)
	repository.now = func() time.Time { return now }
	repository.SetMetrics(NewMetaMetrics())

	initialSnapshot := json.RawMessage(`{"slot":"initial"}`)
	initialHash := sha256.Sum256(initialSnapshot)
	loadout, err := repository.PutLoadout(
		ctx, playerIDs[0], "role-integration", initialSnapshot, initialHash[:], 0,
	)
	if err != nil || loadout.Revision != 1 {
		t.Fatalf("create loadout = %#v, %v", loadout, err)
	}
	if _, err := repository.GetLoadout(ctx, playerIDs[3], "role-integration"); metaErrorCode(err) != "META_LOADOUT_NOT_FOUND" {
		t.Fatalf("cross-player loadout read error = %v", err)
	}

	var successfulUpdates atomic.Int32
	var revisionConflicts atomic.Int32
	var updateGroup sync.WaitGroup
	for index := range 2 {
		updateGroup.Add(1)
		go func(index int) {
			defer updateGroup.Done()
			snapshot := json.RawMessage(fmt.Sprintf(`{"slot":"update-%d"}`, index))
			digest := sha256.Sum256(snapshot)
			_, updateErr := repository.PutLoadout(
				ctx, playerIDs[0], "role-integration", snapshot, digest[:], 1,
			)
			switch metaErrorCode(updateErr) {
			case "":
				successfulUpdates.Add(1)
			case "META_LOADOUT_REVISION_CONFLICT":
				revisionConflicts.Add(1)
			default:
				t.Errorf("unexpected loadout update error: %v", updateErr)
			}
		}(index)
	}
	updateGroup.Wait()
	if successfulUpdates.Load() != 1 || revisionConflicts.Load() != 1 {
		t.Fatalf(
			"concurrent loadout updates: success=%d conflict=%d",
			successfulUpdates.Load(), revisionConflicts.Load(),
		)
	}

	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(
		repository, nil, 1, "127.0.0.1:18083",
		5*time.Minute, 45*time.Second, 2<<20, definitions,
	)
	nativeServer := &TCPServer{
		config:  config.MetaServerConfig{NativePlayerLevel: 1},
		service: service,
	}
	session := GateSession{PlayerID: playerIDs[0]}
	skinRaw, err := proto.Marshal(&metaprotocol.SkinPayload{
		TokenId: "PEACE_ORIGINAL", OrnamentId: "PEACE_ORIGINAL_PTOriginal",
	})
	if err != nil {
		t.Fatal(err)
	}
	roleUpdateRaw, err := proto.Marshal(&metaprotocol.UpdateRoleArchiveV2Request{
		Operation: 3, RoleId: "peace", ItemId: "PEACE_ATK-HE", SkinData: skinRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeServer.updateRoleArchive(ctx, session, roleUpdateRaw); err != nil {
		t.Fatalf("native role update failed: %v", err)
	}

	weaponArchive := validP2PAKMArchive()
	weaponArchive.Skin = &metaprotocol.WeaponSkin{
		SkinInfo: &metaprotocol.OrnamentInfo{
			Type: "SkinAKMTiger", Id: "SkinAKMTiger_PTTiger",
		},
		WeaponOrnament: "WO-SUN",
	}
	weaponUpdateRaw, err := proto.Marshal(&metaprotocol.UpdateWeaponArchiveV2Request{
		RoleId: "PEACE", WeaponArchive: weaponArchive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeServer.updateWeaponArchive(ctx, session, weaponUpdateRaw); err != nil {
		t.Fatalf("native weapon update failed: %v", err)
	}

	archiveRequest, err := proto.Marshal(&metaprotocol.GetPlayerArchiveV2Request{
		RoleIds: []string{"peace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	archiveRaw, err := nativeServer.getPlayerArchive(ctx, session, archiveRequest)
	if err != nil {
		t.Fatalf("native archive read-back failed: %v", err)
	}
	var archiveResponse metaprotocol.GetPlayerArchiveV2Response
	if err := proto.Unmarshal(archiveRaw, &archiveResponse); err != nil {
		t.Fatal(err)
	}
	if archiveResponse.GetPlayerLevel() != 1 || len(archiveResponse.GetPlayerRoleDatas()) != 1 {
		t.Fatalf("unexpected native archive envelope: %#v", &archiveResponse)
	}
	role := archiveResponse.GetPlayerRoleDatas()[0]
	if role.GetRoleId() != "peace" || role.GetRightPylon() != "PEACE_ATK-HE" {
		t.Fatalf("native role update did not round-trip: %#v", role)
	}
	if role.GetPrimaryWeapon() != "PEACE_RU-AKM" ||
		role.GetSecondWeapon() != "PEACE_RU-APS" {
		t.Fatalf("native default weapons did not round-trip: %#v", role)
	}
	if role.GetSkinToken() != "PEACE_ORIGINAL" ||
		role.GetOrnamentId() != "PEACE_ORIGINAL_PTOriginal" {
		t.Fatalf("native role cosmetics did not round-trip in fields 9/10: %#v", role)
	}
	allowedWeapons := make(map[string]struct{})
	for _, weaponID := range nativePlayerRoleWeaponIDs(role) {
		allowedWeapons[weaponID] = struct{}{}
	}
	projected := decodeNativeWeaponArchiveBundle(
		definitions,
		"PEACE",
		role.GetWeaponArchiveRaw(),
		allowedWeapons,
	)
	var projectedWeapon metaprotocol.WeaponArchiveV2
	if err := proto.Unmarshal(projected[weaponArchive.GetWeaponId()], &projectedWeapon); err != nil {
		t.Fatalf("field 8 weapon archive did not decode: %v", err)
	}
	expectedWeapon, ok := p2pCompleteWeaponArchive(
		definitions, weaponArchive.GetWeaponId(), weaponArchive,
	)
	if !ok || !proto.Equal(expectedWeapon, &projectedWeapon) {
		t.Fatalf("field 8 weapon archive did not round-trip: got %#v want %#v",
			&projectedWeapon, expectedWeapon)
	}
	loadout, err = repository.GetLoadout(ctx, playerIDs[0], "PEACE")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(loadout.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["skinModel"] != "PEACE_ORIGINAL" ||
		snapshot["skinPaint"] != "PEACE_ORIGINAL_PTOriginal" {
		t.Fatalf("native cosmetics did not persist: %#v", snapshot)
	}
	archives, err := repository.GetWeaponArchives(
		ctx, playerIDs[0], []string{"PEACE_RU-AKM"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var readBackWeapon metaprotocol.WeaponArchiveV2
	if err := proto.Unmarshal(archives["PEACE_RU-AKM"], &readBackWeapon); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(weaponArchive, &readBackWeapon) {
		t.Fatalf("native weapon archive changed after read-back: got %#v want %#v",
			&readBackWeapon, weaponArchive)
	}

	party, err := repository.CreateParty(
		ctx, playerIDs[0], "default", "hgh", "1.1.0", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_party_members (
			party_id, player_id, role, ready, presence, joined_at, updated_at
		) VALUES ($1, $2, 'MEMBER', TRUE, 'ONLINE', $3, $3)
	`, party.ID, playerIDs[1], now); err != nil {
		t.Fatal(err)
	}
	partyTicket, err := repository.CreateTicket(
		ctx, playerIDs[0], party.ID, "default", "hgh", "1.1.0", 1, 5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetTicket(ctx, partyTicket.ID, playerIDs[1]); err != nil {
		t.Fatalf("party member cannot poll ticket: %v", err)
	}
	if _, err := repository.GetTicket(ctx, partyTicket.ID, playerIDs[3]); metaErrorCode(err) != "META_MATCH_TICKET_NOT_FOUND" {
		t.Fatalf("ticket IDOR was not hidden: %v", err)
	}

	var ticketSuccesses atomic.Int32
	var ticketConflicts atomic.Int32
	var ticketGroup sync.WaitGroup
	for range 8 {
		ticketGroup.Add(1)
		go func() {
			defer ticketGroup.Done()
			_, ticketErr := repository.CreateTicket(
				ctx, playerIDs[2], "", "default", "hgh", "1.1.0", 1, 5*time.Minute,
			)
			switch metaErrorCode(ticketErr) {
			case "":
				ticketSuccesses.Add(1)
			case "META_MATCH_TICKET_EXISTS":
				ticketConflicts.Add(1)
			default:
				t.Errorf("unexpected ticket creation error: %v", ticketErr)
			}
		}()
	}
	ticketGroup.Wait()
	if ticketSuccesses.Load() != 1 || ticketConflicts.Load() != 7 {
		t.Fatalf(
			"concurrent tickets: success=%d conflict=%d",
			ticketSuccesses.Load(), ticketConflicts.Load(),
		)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewScheduler(
		pool, time.Second, 90*time.Second, 90*time.Second, NewMetaMetrics(), logger,
	)
	scheduler.now = func() time.Time { return now }
	var scheduleGroup sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		scheduleGroup.Add(1)
		go func() {
			defer scheduleGroup.Done()
			_, scheduleErr := scheduler.scheduleOne(ctx)
			if scheduleErr != nil {
				errs <- scheduleErr
			}
		}()
	}
	scheduleGroup.Wait()
	close(errs)
	for scheduleErr := range errs {
		t.Errorf("concurrent scheduler error: %v", scheduleErr)
	}
	for {
		scheduled, scheduleErr := scheduler.scheduleOne(ctx)
		if scheduleErr != nil {
			t.Fatal(scheduleErr)
		}
		if !scheduled {
			break
		}
	}

	var matchCount, distinctServerCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT game_server_id)
		FROM meta_matches
		WHERE game_server_id = ANY($1)
	`, serverIDs).Scan(&matchCount, &distinctServerCount); err != nil {
		t.Fatal(err)
	}
	if matchCount != 2 || distinctServerCount != 2 {
		t.Fatalf("matches=%d distinct_servers=%d, want 2/2", matchCount, distinctServerCount)
	}

	var partyMatchID, assignedServerID string
	if err := pool.QueryRow(ctx, `
		SELECT id, game_server_id FROM meta_matches WHERE ticket_id = $1
	`, partyTicket.ID).Scan(&partyMatchID, &assignedServerID); err != nil {
		t.Fatal(err)
	}
	assignedPrincipal, err := repository.AuthenticateGameServer(
		ctx, assignedServerID, serverTokens[assignedServerID],
	)
	if err != nil {
		t.Fatalf("authenticate assigned server: %v", err)
	}
	if _, err := repository.GetMatchPlayerLoadout(
		ctx, assignedPrincipal, partyMatchID, playerIDs[1],
	); err != nil {
		t.Fatalf("assigned server cannot read roster member: %v", err)
	}
	if _, err := repository.GetMatchPlayerLoadout(
		ctx, assignedPrincipal, partyMatchID, playerIDs[3],
	); metaErrorCode(err) != "META_MATCH_PLAYER_FORBIDDEN" {
		t.Fatalf("non-roster player was exposed: %v", err)
	}
	otherServerID := serverIDs[0]
	if otherServerID == assignedServerID {
		otherServerID = serverIDs[1]
	}
	otherPrincipal, err := repository.AuthenticateGameServer(
		ctx, otherServerID, serverTokens[otherServerID],
	)
	if err != nil {
		t.Fatalf("authenticate other server: %v", err)
	}
	if _, err := repository.GetMatchPlayerLoadout(
		ctx, otherPrincipal, partyMatchID, playerIDs[0],
	); metaErrorCode(err) != "META_MATCH_PLAYER_FORBIDDEN" {
		t.Fatalf("cross-server match access was accepted: %v", err)
	}

	cleanupNow := now.Add(2 * time.Minute)
	// Reservation expiry and heartbeat freshness are independent. Model live
	// servers continuing to heartbeat while their clients fail to connect.
	if _, err := pool.Exec(ctx, `
		UPDATE game_servers
		SET last_heartbeat_at = $2, updated_at = $2
		WHERE id = ANY($1)
	`, serverIDs, cleanupNow); err != nil {
		t.Fatal(err)
	}
	scheduler.now = func() time.Time { return cleanupNow }
	if err := scheduler.expireAndRelease(ctx); err != nil {
		t.Fatalf("expire stale reservations: %v", err)
	}
	var failedMatches, readyServers, connectionTimeouts int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM meta_matches
		   WHERE game_server_id = ANY($1) AND state = 'FAILED'),
		  (SELECT COUNT(*) FROM game_servers
		   WHERE id = ANY($1) AND state = 'READY'),
		  (SELECT COUNT(*) FROM meta_match_tickets
		   WHERE player_id = ANY($2)
		     AND state = 'FAILED'
		     AND failure_code = 'META_MATCH_CONNECTION_TIMEOUT')
	`, serverIDs, playerIDs).Scan(
		&failedMatches, &readyServers, &connectionTimeouts,
	); err != nil {
		t.Fatal(err)
	}
	if failedMatches != 2 || readyServers != 2 || connectionTimeouts != 2 {
		t.Fatalf(
			"reservation cleanup: failed_matches=%d ready_servers=%d connection_timeouts=%d",
			failedMatches, readyServers, connectionTimeouts,
		)
	}
	var partyState string
	if err := pool.QueryRow(ctx, `SELECT state FROM meta_parties WHERE id = $1`, party.ID).Scan(&partyState); err != nil {
		t.Fatal(err)
	}
	if partyState != "ACTIVE" {
		t.Fatalf("party state after reservation timeout = %s, want ACTIVE", partyState)
	}
}

func metaErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.Code
	}
	return "UNEXPECTED"
}
