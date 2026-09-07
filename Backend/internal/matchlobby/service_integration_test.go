package matchlobby

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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

func TestStrictRosterP2PHostOwnedProcessExitCleanupAgainstPostgreSQL(t *testing.T) {
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
	matchConfig.StrictRosterV1Enabled = true
	signer, err := NewAdmissionSigner("integration-p2p-owned-exit", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	service.SetP2PTransport(p2pService)
	service.SetP2PMatchProjector(battleLogService)
	currentTime := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return currentTime }
	signer.now = func() time.Time { return currentTime }

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 71_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 72_000_000_000_000_000+suffix))
	playerIDs := []string{owner.PlayerID, member.PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	frozen, transportHostToken := createTwoPlayerReadyLobby(t, ctx, service, owner, member, "P2P Owned Exit", "integration-p2p-owned-exit")
	if frozen.Attempt == nil {
		t.Fatal("P2P owned-process lobby omitted its attempt")
	}
	attemptID := frozen.Attempt.AttemptID
	allocation, err := service.P2PHostAllocation(ctx, owner, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	claims := decodeAllocationClaims(t, allocation.Allocation)
	var authoritySession string
	if err := pool.QueryRow(ctx, "SELECT authority_session_id FROM match_attempts WHERE id = $1", attemptID).Scan(&authoritySession); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PPayloadInstalled(ctx, owner, attemptID, authoritySession, strictNativeAdmissionVersion, matchConfig.LockedGameSHA256, frozen.Attempt.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	connecting, err := service.P2PAuthorityReady(
		ctx, owner, attemptID, authoritySession, transportHostToken,
		"10.88.0.9", 7788, frozen.Attempt.RouteGeneration,
		"world-p2p-owned-exit", "native-host-owned-exit",
	)
	if err != nil || connecting.Attempt == nil || connecting.Attempt.State != AttemptConnecting {
		t.Fatalf("P2P authority ready = %+v, %v", connecting, err)
	}

	grant, err := service.JoinGrant(ctx, member, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	grantJTI := decodeJoinGrantJTI(t, grant.Grant)
	nativeNonce := "native-owned-exit-member"
	if _, err := service.P2PMarkAdmissionDelivered(ctx, owner, authoritySession, attemptID, grantJTI); err != nil {
		t.Fatal(err)
	}
	reservation, err := service.P2PReserveAdmission(
		ctx, owner, authoritySession, attemptID, "world-p2p-owned-exit", member.PlayerID,
		grantJTI, nativeNonce, grant.ConnectionGeneration,
	)
	if err != nil || reservation.GrantJTI != grantJTI {
		t.Fatalf("P2P reservation = %+v, %v", reservation, err)
	}
	if _, err := service.P2PConfirmConnected(
		ctx, owner, authoritySession, attemptID, "world-p2p-owned-exit", member.PlayerID,
		grantJTI, nativeNonce, grant.ConnectionGeneration,
	); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.P2PComplete(ctx, owner, authoritySession, attemptID, true, "")
	if err != nil || terminal.State != StateCompleted {
		t.Fatalf("complete P2P attempt = %+v, %v", terminal, err)
	}

	evidence := OwnedProcessExitEvidence{
		EvidenceKind:            "owned_process_exited",
		OwnedProcessID:          61234,
		ProcessStartFingerprint: "win-filetime:0123456789abcdef",
	}
	if _, err := service.P2PNativeCleared(
		ctx, member, authoritySession, attemptID, "world-p2p-owned-exit",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration, evidence,
	); errorCode(err) != "MATCH_AUTHORITY_SCOPE_REQUIRED" {
		t.Fatalf("non-HOST owned-process cleanup was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "world-p2p-owned-exit",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration,
		OwnedProcessExitEvidence{EvidenceKind: "pid_string", OwnedProcessID: evidence.OwnedProcessID, ProcessStartFingerprint: evidence.ProcessStartFingerprint},
	); errorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("malformed owned-process evidence was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "world-p2p-old",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration, evidence,
	); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("old-world owned-process cleanup was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "world-p2p-owned-exit",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration+1, evidence,
	); errorCode(err) != "MATCH_ROUTE_GENERATION_STALE" {
		t.Fatalf("stale-route owned-process cleanup was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "world-p2p-owned-exit",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration, evidence,
	); err != nil {
		t.Fatal(err)
	}
	var cleanupState string
	if err := pool.QueryRow(ctx, "SELECT cleanup_state FROM match_attempts WHERE id = $1", attemptID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "CLEARED" {
		t.Fatalf("P2P owned-process cleanup state = %s, want CLEARED", cleanupState)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "world-p2p-owned-exit",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration, evidence,
	); err != nil {
		t.Fatalf("same-scope cleanup retry failed: %v", err)
	}
	var auditEvent string
	var auditDetails []byte
	if err := pool.QueryRow(ctx, `
		SELECT event_type, details
		FROM vnt_security_audit_logs
		WHERE event_type = 'MATCH_NATIVE_CLEANUP_CLEARED'
		  AND details->>'lobby_id' = $1 AND details->>'attempt_id' = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, frozen.LobbyID, attemptID).Scan(&auditEvent, &auditDetails); err != nil {
		t.Fatalf("native cleanup audit missing: %v", err)
	}
	if auditEvent != "MATCH_NATIVE_CLEANUP_CLEARED" {
		t.Fatalf("native cleanup audit event = %q", auditEvent)
	}
	var audited struct {
		AttemptID               string `json:"attempt_id"`
		AuthorityID             string `json:"authority_id"`
		AuthoritySessionSHA256  string `json:"authority_session_sha256"`
		LobbyID                 string `json:"lobby_id"`
		WorldInstanceID         string `json:"world_instance_id"`
		RosterRevision          int64  `json:"roster_revision"`
		RouteGeneration         int    `json:"route_generation"`
		EvidenceKind            string `json:"evidence_kind"`
		OwnedProcessID          uint32 `json:"owned_process_id"`
		ProcessStartFingerprint string `json:"process_start_fingerprint"`
	}
	if strings.Contains(string(auditDetails), authoritySession) {
		t.Fatalf("native cleanup audit retained the authority session bearer")
	}
	if err := json.Unmarshal(auditDetails, &audited); err != nil {
		t.Fatalf("decode native cleanup audit: %v", err)
	}
	expectedAuthoritySessionSHA256 := fmt.Sprintf("%x", sha256.Sum256([]byte(authoritySession)))
	if audited.AttemptID != attemptID || audited.AuthorityID != owner.PlayerID ||
		audited.AuthoritySessionSHA256 != expectedAuthoritySessionSHA256 || audited.LobbyID != frozen.LobbyID ||
		audited.WorldInstanceID != "world-p2p-owned-exit" || audited.RosterRevision != connecting.Attempt.RosterRevision ||
		audited.RouteGeneration != connecting.Attempt.RouteGeneration || audited.EvidenceKind != evidence.EvidenceKind ||
		audited.OwnedProcessID != evidence.OwnedProcessID || audited.ProcessStartFingerprint != evidence.ProcessStartFingerprint {
		t.Fatalf("native cleanup audit scope/evidence = %+v", audited)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM vnt_security_audit_logs
		WHERE event_type = 'MATCH_NATIVE_CLEANUP_CLEARED'
		  AND details->>'lobby_id' = $1 AND details->>'attempt_id' = $2
	`, frozen.LobbyID, attemptID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("native cleanup audit count = %d, want one receipt after duplicate ACK", auditCount)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "world-p2p-old",
		connecting.Attempt.RosterRevision, connecting.Attempt.RouteGeneration, evidence,
	); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("idempotent cleanup accepted a different world: %v", err)
	}
	_ = claims
}

func TestStrictRosterP2PPreflightNativeProcessNotStartedCleanupAgainstPostgreSQL(t *testing.T) {
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
	service, _, owner, member, frozen, authoritySession := setupStrictRosterP2PPreflightFixture(t, ctx, pool, "P2P Preflight No Process", "integration-p2p-preflight-no-process")
	if frozen.Attempt == nil {
		t.Fatal("P2P preflight lobby omitted its attempt")
	}
	attemptID := frozen.Attempt.AttemptID
	if _, err := service.P2PHostAllocation(ctx, owner, attemptID); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.P2PComplete(ctx, owner, authoritySession, attemptID, false, "PRE_READY_PROCESS_NOT_STARTED")
	if err != nil || terminal.State != StateAborted {
		t.Fatalf("preflight P2P abort = %+v, %v", terminal, err)
	}
	evidence := OwnedProcessExitEvidence{EvidenceKind: "native_process_not_started"}
	if _, err := service.P2PNativeCleared(
		ctx, member, authoritySession, attemptID, "", frozen.Attempt.RosterRevision, frozen.Attempt.RouteGeneration, evidence,
	); errorCode(err) != "MATCH_AUTHORITY_SCOPE_REQUIRED" {
		t.Fatalf("non-HOST preflight cleanup was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "preflight-world", frozen.Attempt.RosterRevision, frozen.Attempt.RouteGeneration, evidence,
	); errorCode(err) != "MATCH_WORLD_INSTANCE_REQUIRED" {
		t.Fatalf("non-empty preflight world was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "", frozen.Attempt.RosterRevision, frozen.Attempt.RouteGeneration,
		OwnedProcessExitEvidence{EvidenceKind: "native_process_not_started", OwnedProcessID: 7},
	); errorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("preflight receipt with a process id was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "", frozen.Attempt.RosterRevision, frozen.Attempt.RouteGeneration, evidence,
	); err != nil {
		t.Fatalf("scoped preflight cleanup failed: %v", err)
	}
	var cleanupState string
	if err := pool.QueryRow(ctx, "SELECT cleanup_state FROM match_attempts WHERE id = $1", attemptID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "CLEARED" {
		t.Fatalf("preflight cleanup state = %s, want CLEARED", cleanupState)
	}
	var details []byte
	if err := pool.QueryRow(ctx, `
		SELECT details
		FROM vnt_security_audit_logs
		WHERE event_type = 'MATCH_NATIVE_CLEANUP_CLEARED'
		  AND details->>'lobby_id' = $1 AND details->>'attempt_id' = $2
		LIMIT 1
	`, frozen.LobbyID, attemptID).Scan(&details); err != nil {
		t.Fatalf("preflight cleanup audit missing: %v", err)
	}
	var audited struct {
		WorldInstanceID         string `json:"world_instance_id"`
		EvidenceKind            string `json:"evidence_kind"`
		OwnedProcessID          uint32 `json:"owned_process_id"`
		ProcessStartFingerprint string `json:"process_start_fingerprint"`
	}
	if err := json.Unmarshal(details, &audited); err != nil {
		t.Fatalf("decode preflight cleanup audit: %v", err)
	}
	if audited.WorldInstanceID != "" || audited.EvidenceKind != evidence.EvidenceKind || audited.OwnedProcessID != 0 || audited.ProcessStartFingerprint != "" {
		t.Fatalf("preflight cleanup audit = %+v", audited)
	}
}

func TestStrictRosterP2PPreflightNotStartedRejectsInstalledPayloadAgainstPostgreSQL(t *testing.T) {
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
	service, matchConfig, owner, _, frozen, authoritySession := setupStrictRosterP2PPreflightFixture(t, ctx, pool, "P2P Preflight Installed Payload", "integration-p2p-preflight-installed-payload")
	if frozen.Attempt == nil {
		t.Fatal("P2P installed-payload lobby omitted its attempt")
	}
	attemptID := frozen.Attempt.AttemptID
	if _, err := service.P2PHostAllocation(ctx, owner, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PPayloadInstalled(ctx, owner, attemptID, authoritySession, strictNativeAdmissionVersion, matchConfig.LockedGameSHA256, frozen.Attempt.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PComplete(ctx, owner, authoritySession, attemptID, false, "PRE_READY_PAYLOAD_INSTALLED"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, attemptID, "", frozen.Attempt.RosterRevision, frozen.Attempt.RouteGeneration,
		OwnedProcessExitEvidence{EvidenceKind: "native_process_not_started"},
	); errorCode(err) != "MATCH_NATIVE_PROCESS_NOT_STARTED_INVALID" {
		t.Fatalf("not-started receipt after Payload installation was accepted: %v", err)
	}
}

func setupStrictRosterP2PPreflightFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	label, idempotencyKey string,
) (*Service, config.MatchLobbyConfig, Actor, Actor, Snapshot, string) {
	t.Helper()
	p2pService := p2proom.NewService(p2proom.NewRepository(pool), config.Defaults.P2PRoom)
	secretBox, _, err := p2proom.NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	p2pService.SetVNT(nil, secretBox)
	battleLogService := p2pbattlelog.NewService(p2pbattlelog.NewRepository(pool), config.Defaults.P2PBattleLog)
	p2pService.SetMatchLifecycle(battleLogService)
	matchConfig := config.Defaults.MatchLobby
	matchConfig.StrictRosterV1Enabled = true
	signer, err := NewAdmissionSigner(idempotencyKey, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	service.SetP2PTransport(p2pService)
	service.SetP2PMatchProjector(battleLogService)
	currentTime := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return currentTime }
	signer.now = func() time.Time { return currentTime }
	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 73_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 74_000_000_000_000_000+suffix))
	playerIDs := []string{owner.PlayerID, member.PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})
	frozen, _ := createTwoPlayerReadyLobby(t, ctx, service, owner, member, label, idempotencyKey)
	var authoritySession string
	if frozen.Attempt == nil {
		t.Fatal("preflight fixture omitted its attempt")
	}
	if err := pool.QueryRow(ctx, "SELECT authority_session_id FROM match_attempts WHERE id = $1", frozen.Attempt.AttemptID).Scan(&authoritySession); err != nil {
		t.Fatal(err)
	}
	return service, matchConfig, owner, member, frozen, authoritySession
}

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
	secretBox, _, err := p2proom.NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	p2pService.SetVNT(nil, secretBox)
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
	signer.now = func() time.Time { return currentTime }

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
	restoredOwner, err := service.Active(ctx, actors[0])
	if err != nil || restoredOwner.Snapshot.LobbyID != created.Snapshot.LobbyID ||
		restoredOwner.TransportHostToken != created.TransportHostToken {
		t.Fatalf("active owner recovery lost lobby or transport credential: %+v, %v", restoredOwner.Snapshot, err)
	}
	if _, err := service.Active(ctx, actors[1]); errorCode(err) != "MATCH_LOBBY_NOT_ACTIVE" {
		t.Fatalf("player without a lobby recovered an active snapshot: %v", err)
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
	restoredMember, err := service.Active(ctx, winner)
	if err != nil || restoredMember.Snapshot.LobbyID != created.Snapshot.LobbyID || restoredMember.TransportHostToken != "" {
		t.Fatalf("active member recovery leaked or lost state: %+v, %v", restoredMember, err)
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
		ctx, actors[0], attemptID, authoritySession, "strict-roster-v2",
		matchConfig.LockedGameSHA256, frozen.Attempt.RouteGeneration,
	); err != nil {
		t.Fatal(err)
	}
	connecting, err := service.P2PAuthorityReady(
		ctx, actors[0], attemptID, authoritySession, created.TransportHostToken,
		"10.88.0.1", 7777, frozen.Attempt.RouteGeneration, "world-p2p-primary", "native-host-p2p-primary",
	)
	if err != nil || connecting.State != StateConnecting {
		t.Fatalf("authority ready = %+v, %v", connecting, err)
	}
	if _, err := service.P2PAuthorityReady(
		ctx, actors[0], attemptID, authoritySession, created.TransportHostToken,
		"10.88.0.1", 7777, frozen.Attempt.RouteGeneration, "world-p2p-primary", "native-host-p2p-primary",
	); err != nil {
		t.Fatalf("authority ready retry was not idempotent: %v", err)
	}
	var storedHostNonce string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(live_native_connection_nonce, '')
		FROM match_attempt_roster WHERE attempt_id = $1 AND player_id = $2
	`, attemptID, actors[0].PlayerID).Scan(&storedHostNonce); err != nil {
		t.Fatal(err)
	}
	if storedHostNonce != "native-host-p2p-primary" {
		t.Fatalf("P2P host native nonce was not persisted: %q", storedHostNonce)
	}
	if _, err := service.P2PMarkDisconnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", actors[0].PlayerID,
		"native-host-p2p-stale-xxxxxxxx", 1,
	); errorCode(err) != "MATCH_CONNECTION_GENERATION_STALE" {
		t.Fatalf("stale P2P host disconnect nonce was accepted: %v", err)
	}
	// Toolbox deliberately detaches and re-attaches the transport projection
	// when it launches (and later reconnects) a frozen member. CONNECTING must
	// permit that operation only for a member of the authoritative lobby.
	if _, err := p2pService.LeaveManaged(ctx, toP2PActor(winner), connecting.P2PRoomID); err != nil {
		t.Fatalf("detach frozen member transport while connecting: %v", err)
	}
	if _, err := p2pService.Join(ctx, toP2PActor(winner), connecting.P2PRoomID, connecting.ClientVersion); err != nil {
		t.Fatalf("re-attach frozen member transport while connecting: %v", err)
	}
	if _, err := p2pService.Join(ctx, toP2PActor(actors[3]), connecting.P2PRoomID, connecting.ClientVersion); err == nil {
		t.Fatal("non-roster player attached to a connecting managed transport")
	} else {
		var roomError *p2proom.ServiceError
		if !errors.As(err, &roomError) || roomError.Code != "MANAGED_LOBBY_MEMBERSHIP_REQUIRED" {
			t.Fatalf("non-roster connecting attach error = %v", err)
		}
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
		ctx, actors[0], attemptID, authoritySession, "strict-roster-v2",
		matchConfig.LockedGameSHA256, 2,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PAuthorityReady(
		ctx, actors[0], attemptID, authoritySession, created.TransportHostToken,
		"10.88.0.1", 7777, 2, "world-p2p-replaced", "native-host-p2p-recovered",
	); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("P2P recovery accepted a replaced native world: %v", err)
	}
	recoveredReady, err := service.P2PAuthorityReady(
		ctx, actors[0], attemptID, authoritySession, created.TransportHostToken,
		"10.88.0.1", 7777, 2, "world-p2p-primary", "native-host-p2p-recovered",
	)
	if err != nil || recoveredReady.State != StateConnecting {
		t.Fatalf("recovered P2P authority ready = %+v, %v", recoveredReady, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(live_native_connection_nonce, '')
		FROM match_attempt_roster WHERE attempt_id = $1 AND player_id = $2
	`, attemptID, actors[0].PlayerID).Scan(&storedHostNonce); err != nil {
		t.Fatal(err)
	}
	if storedHostNonce != "native-host-p2p-recovered" {
		t.Fatalf("recovered P2P host nonce was not replaced: %q", storedHostNonce)
	}
	assertP2PProjectionMatchesAttempt(t, ctx, pool, attemptID)

	connectedGrants := make(map[string]GrantResult)
	for _, actor := range []Actor{winner, loser} {
		grant, err := service.JoinGrant(ctx, actor, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		jti := decodeJoinGrantJTI(t, grant.Grant)
		nativeNonce := "native-handshake-" + actor.PlayerID
		if grant.GrantJTI != jti {
			t.Fatalf("grant response JTI = %q, token JTI = %q", grant.GrantJTI, jti)
		}
		status, err := service.GrantDelivery(ctx, actor, attemptID, jti)
		if err != nil || status.Delivered {
			t.Fatalf("new grant delivery status = %+v, %v", status, err)
		}
		pending, err := service.P2PAuthorityAdmissions(
			ctx, actors[0], authoritySession, attemptID,
		)
		if err != nil {
			t.Fatal(err)
		}
		var staged *AuthorityAdmission
		for index := range pending.Items {
			if pending.Items[index].GrantJTI == jti {
				staged = &pending.Items[index]
				break
			}
		}
		if staged == nil || staged.PlayerID != actor.PlayerID ||
			staged.JoinGrant != grant.Grant ||
			staged.ConnectionGeneration != grant.ConnectionGeneration {
			t.Fatalf("authority did not reconstruct issued grant: pending=%+v grant=%+v", pending, grant)
		}
		delivered, err := service.P2PMarkAdmissionDelivered(
			ctx, actors[0], authoritySession, attemptID, jti,
		)
		if err != nil || !delivered.Delivered || delivered.DeliveredAt == nil {
			t.Fatalf("authority delivery acknowledgement = %+v, %v", delivered, err)
		}
		if _, err := service.P2PMarkAdmissionDelivered(
			ctx, actors[0], authoritySession, attemptID, jti,
		); err != nil {
			t.Fatalf("delivery acknowledgement retry was not idempotent: %v", err)
		}
		status, err = service.GrantDelivery(ctx, actor, attemptID, jti)
		if err != nil || !status.Delivered {
			t.Fatalf("delivered grant remained hidden from member: %+v, %v", status, err)
		}
		reservation, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		)
		if err != nil || reservation.GrantJTI != jti {
			t.Fatalf("admission reservation = %+v, %v", reservation, err)
		}
		repeatedReservation, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		)
		if err != nil || !repeatedReservation.ReservedUntil.Equal(reservation.ReservedUntil) {
			t.Fatalf("reservation retry was not idempotent: first=%+v retry=%+v err=%v", reservation, repeatedReservation, err)
		}
		if _, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, "native-competing-handshake-"+actor.PlayerID, grant.ConnectionGeneration,
		); errorCode(err) != "MATCH_ADMISSION_RESERVED" {
			t.Fatalf("a second native handshake borrowed the active reservation: %v", err)
		}
		if _, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration-1,
		); errorCode(err) != "MATCH_CONNECTION_GENERATION_STALE" {
			t.Fatalf("old connection generation was accepted: %v", err)
		}
		var currentRouteGeneration int
		if err := pool.QueryRow(ctx, `SELECT route_generation FROM match_attempts WHERE id = $1`, attemptID).Scan(&currentRouteGeneration); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE match_admission_grants SET route_generation = route_generation - 1 WHERE jti = $1`, jti); err != nil {
			t.Fatal(err)
		}
		if _, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		); errorCode(err) != "MATCH_ROUTE_GENERATION_STALE" {
			t.Fatalf("old route generation was accepted: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE match_admission_grants SET route_generation = $2 WHERE jti = $1`, jti, currentRouteGeneration); err != nil {
			t.Fatal(err)
		}
		var currentRosterGeneration int
		if err := pool.QueryRow(ctx, `SELECT connection_generation FROM match_attempt_roster WHERE attempt_id = $1 AND player_id = $2`, attemptID, actor.PlayerID).Scan(&currentRosterGeneration); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE match_attempt_roster SET connection_generation = connection_generation + 1 WHERE attempt_id = $1 AND player_id = $2`, attemptID, actor.PlayerID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		); errorCode(err) != "MATCH_CONNECTION_GENERATION_STALE" {
			t.Fatalf("roster generation drift was accepted: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE match_attempt_roster SET connection_generation = $3 WHERE attempt_id = $1 AND player_id = $2`, attemptID, actor.PlayerID, currentRosterGeneration); err != nil {
			t.Fatal(err)
		}
		if _, err := service.P2PReserveAdmission(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-old",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
			t.Fatalf("old-world reservation was accepted: %v", err)
		}
		if actor.PlayerID == loser.PlayerID {
			if err := service.P2PReleaseAdmission(
				ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
				actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
			); err != nil {
				t.Fatal(err)
			}
			replacement, replacementErr := service.JoinGrant(ctx, actor, attemptID)
			if replacementErr != nil {
				t.Fatal(replacementErr)
			}
			grant = replacement
			jti = decodeJoinGrantJTI(t, grant.Grant)
			nativeNonce = "native-replacement-" + actor.PlayerID
			if _, err := service.P2PMarkAdmissionDelivered(ctx, actors[0], authoritySession, attemptID, jti); err != nil {
				t.Fatal(err)
			}
			reservation, err = service.P2PReserveAdmission(
				ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
				actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
			)
			if err != nil {
				t.Fatal(err)
			}
		}
		connectedGrants[actor.PlayerID] = grant
		connected, err := service.P2PConfirmConnected(
			ctx, actors[0], authoritySession, attemptID, reservation.WorldInstanceID,
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.P2PConfirmConnected(
			ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
			actor.PlayerID, jti, nativeNonce, grant.ConnectionGeneration,
		); err != nil {
			t.Fatalf("connected report retry was not idempotent: %v", err)
		}
		connecting = connected
	}
	if connecting.State != StateRunning || connecting.Attempt == nil || connecting.Attempt.State != AttemptRunning {
		t.Fatalf("all connected players did not start early: %+v", connecting)
	}
	if _, err := p2pService.LeaveManaged(ctx, toP2PActor(winner), connecting.P2PRoomID); err != nil {
		t.Fatalf("detach frozen member transport while running: %v", err)
	}
	if _, err := p2pService.Join(ctx, toP2PActor(winner), connecting.P2PRoomID, connecting.ClientVersion); err != nil {
		t.Fatalf("re-attach frozen member transport while running: %v", err)
	}
	prior := connectedGrants[winner.PlayerID]
	liveView, err := service.Get(ctx, created.Snapshot.LobbyID, winner.PlayerID)
	if err != nil || liveView.Local.CanRetry {
		t.Fatalf("live P2P member advertised reconnect capability: %+v, %v", liveView.Local, err)
	}
	if _, err := service.JoinGrant(ctx, winner, attemptID); errorCode(err) != "MATCH_CONNECTION_STILL_ACTIVE" {
		t.Fatalf("live P2P connection received a reconnect grant: %v", err)
	}
	if _, err := service.P2PMarkDisconnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-old", winner.PlayerID,
		"native-handshake-"+winner.PlayerID, prior.ConnectionGeneration,
	); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("old P2P world was accepted for disconnect: %v", err)
	}
	if _, err := service.P2PMarkDisconnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", winner.PlayerID,
		"native-handshake-stale-xxxxxxxx", prior.ConnectionGeneration,
	); errorCode(err) != "MATCH_CONNECTION_GENERATION_STALE" {
		t.Fatalf("old P2P disconnect nonce was accepted: %v", err)
	}
	disconnected, err := service.P2PMarkDisconnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", winner.PlayerID,
		"native-handshake-"+winner.PlayerID,
		prior.ConnectionGeneration,
	)
	if err != nil {
		t.Fatal(err)
	}
	disconnectedView, err := service.Get(ctx, disconnected.LobbyID, winner.PlayerID)
	if err != nil || !disconnectedView.Local.CanRetry {
		t.Fatalf("disconnected P2P member lacked reconnect capability: %+v, %v", disconnectedView.Local, err)
	}
	if _, err := service.P2PMarkDisconnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", winner.PlayerID,
		"native-handshake-"+winner.PlayerID,
		prior.ConnectionGeneration,
	); err != nil {
		t.Fatalf("repeated P2P disconnect was not idempotent: %v", err)
	}
	reconnect, err := service.JoinGrant(ctx, winner, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if reconnect.ConnectionGeneration != prior.ConnectionGeneration+1 {
		t.Fatalf("P2P reconnect generation = %d", reconnect.ConnectionGeneration)
	}
	reconnectClaims := decodeJoinGrantClaims(t, reconnect.Grant)
	priorClaims := decodeJoinGrantClaims(t, prior.Grant)
	if reconnectClaims.TeamID != priorClaims.TeamID ||
		reconnectClaims.TeamSlot != priorClaims.TeamSlot ||
		reconnectClaims.LogicalSlot != priorClaims.LogicalSlot {
		t.Fatalf("P2P reconnect moved the frozen seat: prior=%+v reconnect=%+v", priorClaims, reconnectClaims)
	}
	reconnectJTI := decodeJoinGrantJTI(t, reconnect.Grant)
	reconnectNonce := "native-reconnect-" + winner.PlayerID
	if _, err := service.P2PMarkAdmissionDelivered(ctx, actors[0], authoritySession, attemptID, reconnectJTI); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PReserveAdmission(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", winner.PlayerID,
		reconnectJTI, reconnectNonce, reconnect.ConnectionGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PConfirmConnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
		winner.PlayerID, decodeJoinGrantJTI(t, prior.Grant), "native-handshake-"+winner.PlayerID, prior.ConnectionGeneration,
	); errorCode(err) != "MATCH_JOIN_GRANT_NOT_CONSUMABLE" {
		t.Fatalf("old P2P grant remained consumable after reconnect: %v", err)
	}
	reconnected, err := service.P2PConfirmConnected(
		ctx, actors[0], authoritySession, attemptID, "world-p2p-primary",
		winner.PlayerID, reconnectJTI, reconnectNonce, reconnect.ConnectionGeneration,
	)
	if err != nil {
		t.Fatal(err)
	}
	reconnectedView, err := service.Get(ctx, reconnected.LobbyID, winner.PlayerID)
	if err != nil || reconnectedView.Local.CanRetry {
		t.Fatalf("reconnected P2P member advertised reconnect capability: %+v, %v", reconnectedView.Local, err)
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
	var cleanupState string
	if err := pool.QueryRow(ctx, `SELECT cleanup_state FROM match_attempts WHERE id = $1`, attemptID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "PENDING" {
		t.Fatalf("completed P2P attempt cleanup state = %s, want PENDING", cleanupState)
	}
	if _, err := service.P2PNativeCleared(ctx, actors[0], authoritySession, attemptID, "world-p2p-old", reconnected.Attempt.RosterRevision, reconnected.Attempt.RouteGeneration); errorCode(err) != "MATCH_WORLD_INSTANCE_CONFLICT" {
		t.Fatalf("old-world cleanup acknowledgement was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", reconnected.Attempt.RosterRevision, reconnected.Attempt.RouteGeneration-1); errorCode(err) != "MATCH_ROUTE_GENERATION_STALE" {
		t.Fatalf("stale P2P cleanup route was accepted: %v", err)
	}
	if _, err := service.P2PNativeCleared(ctx, actors[0], authoritySession, attemptID, "world-p2p-primary", reconnected.Attempt.RosterRevision, reconnected.Attempt.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT cleanup_state FROM match_attempts WHERE id = $1`, attemptID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "CLEARED" {
		t.Fatalf("cleared P2P attempt cleanup state = %s", cleanupState)
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
	if _, err := service.P2PPayloadInstalled(ctx, actors[0], timeoutAttempt.AttemptID, timeoutSession, "strict-roster-v2", matchConfig.LockedGameSHA256, timeoutAttempt.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PAuthorityReady(
		ctx, actors[0], timeoutAttempt.AttemptID, timeoutSession,
		timeoutHostToken, "10.88.0.2", 7777,
		timeoutAttempt.RouteGeneration, "world-p2p-timeout", "native-host-p2p-timeout",
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
	return decodeJoinGrantClaims(t, token).TokenID
}

func decodeJoinGrantClaims(t *testing.T, token string) JoinGrantClaims {
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
	return claims
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
