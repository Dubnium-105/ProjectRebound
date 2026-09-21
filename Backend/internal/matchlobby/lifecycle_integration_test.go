package matchlobby

import (
	"context"
	"errors"
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

// lifecycleFaultTransport lets the PostgreSQL lifecycle test model a room
// close that reached the database but whose relay/connection side effect was
// not yet confirmed. The next retry uses the real p2proom close path.
type lifecycleFaultTransport struct {
	*p2proom.Service
	failNextClose bool
}

func (t *lifecycleFaultTransport) CloseManagedByLobby(ctx context.Context, lobbyID, reason string) error {
	if err := t.Service.CloseManagedByLobby(ctx, lobbyID, reason); err != nil {
		return err
	}
	if t.failNextClose {
		t.failNextClose = false
		return errors.New("relay revoke acknowledgement pending")
	}
	return nil
}

func TestStrictRosterP2PLifecycleTerminalRecoveryAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	roomService := p2proom.NewService(p2proom.NewRepository(pool), config.Defaults.P2PRoom)
	secretBox, _, err := p2proom.NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	roomService.SetVNT(nil, secretBox)
	battleLogService := p2pbattlelog.NewService(p2pbattlelog.NewRepository(pool), config.Defaults.P2PBattleLog)
	roomService.SetMatchLifecycle(battleLogService)
	transport := &lifecycleFaultTransport{Service: roomService}

	matchConfig := config.Defaults.MatchLobby
	matchConfig.AcceptNewLobbies = true
	matchConfig.EndingSeconds = 30
	signer, err := NewAdmissionSigner("integration-lifecycle-terminal", testAdmissionPrivateKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	service.SetP2PTransport(transport)
	service.SetP2PMatchProjector(battleLogService)
	currentTime := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return currentTime }
	signer.now = func() time.Time { return currentTime }

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 91_000_000_000_000_000+suffix))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 92_000_000_000_000_000+suffix))
	playerIDs := []string{owner.PlayerID, member.PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	// A persisted RETURN_READY event must be recoverable when the first
	// Complete transaction fails. The duplicate event retries Complete rather
	// than leaving the attempt in ENDING forever.
	first, firstSession := startLifecycleRunningAttempt(t, ctx, service, pool, owner, member, "lifecycle-duplicate", "world-lifecycle-duplicate", matchConfig)
	firstResult := lifecycleInputFromAttempt(first, LifecycleResultConfirmed, 1)
	ending, err := service.P2PLifecycle(ctx, owner, firstSession, first.Attempt.AttemptID, firstResult)
	if err != nil || ending.Attempt == nil || ending.Attempt.State != AttemptEnding {
		t.Fatalf("result confirmation = %+v, %v", ending, err)
	}
	if _, err := service.JoinGrant(ctx, member, first.Attempt.AttemptID); errorCode(err) != "MATCH_ATTEMPT_NOT_CONNECTABLE" {
		t.Fatalf("ENDING attempt issued a new admission grant: %v", err)
	}
	if _, err := service.P2PComplete(ctx, owner, firstSession, first.Attempt.AttemptID, true, ""); errorCode(err) != "MATCH_RETURN_NOT_READY" {
		t.Fatalf("early successful Complete was accepted: %v", err)
	}
	service.SetP2PMatchProjector(nil)
	firstReturn := lifecycleInputFromAttempt(ending, LifecycleReturnReady, 2)
	if _, err := service.P2PLifecycle(ctx, owner, firstSession, first.Attempt.AttemptID, firstReturn); err == nil {
		t.Fatal("RETURN_READY unexpectedly completed while the projector was unavailable")
	}
	var firstState, firstPhase string
	if err := pool.QueryRow(ctx, `
		SELECT state, COALESCE(lifecycle_phase, '')
		FROM match_attempts WHERE id = $1
	`, first.Attempt.AttemptID).Scan(&firstState, &firstPhase); err != nil {
		t.Fatal(err)
	}
	if firstState != string(AttemptEnding) || firstPhase != string(LifecycleReturnReady) {
		t.Fatalf("failed Complete did not preserve RETURN_READY receipt: state=%s phase=%s", firstState, firstPhase)
	}
	service.SetP2PMatchProjector(battleLogService)
	firstTerminal, err := service.P2PLifecycle(ctx, owner, firstSession, first.Attempt.AttemptID, firstReturn)
	if err != nil || firstTerminal.State != StateCompleted || !firstTerminal.Local.IsOwner {
		t.Fatalf("duplicate RETURN_READY did not finish terminal transition: %+v, %v", firstTerminal, err)
	}
	clearLifecycleAttempt(t, ctx, service, owner, firstSession, firstTerminal)

	// A trusted result remains successful after process loss, and a delayed
	// RETURN_READY receipt updates the phase without changing that result.
	second, secondSession := startLifecycleRunningAttempt(t, ctx, service, pool, owner, member, "lifecycle-process-loss", "world-lifecycle-process-loss", matchConfig)
	secondResult := lifecycleInputFromAttempt(second, LifecycleResultConfirmed, 1)
	secondEnding, err := service.P2PLifecycle(ctx, owner, secondSession, second.Attempt.AttemptID, secondResult)
	if err != nil || secondEnding.Attempt == nil || secondEnding.Attempt.State != AttemptEnding {
		t.Fatalf("process-loss result confirmation = %+v, %v", secondEnding, err)
	}
	secondTerminal, err := service.P2PComplete(ctx, owner, secondSession, second.Attempt.AttemptID, false, "PROCESS_LOST")
	if err != nil || secondTerminal.State != StateCompleted || secondTerminal.Attempt == nil || secondTerminal.Attempt.CompletionWarning != "PROCESS_LOST" {
		t.Fatalf("process-loss did not preserve completed result: %+v, %v", secondTerminal, err)
	}
	secondReturn := lifecycleInputFromAttempt(secondEnding, LifecycleReturnReady, 2)
	secondLate, err := service.P2PLifecycle(ctx, owner, secondSession, second.Attempt.AttemptID, secondReturn)
	if err != nil || secondLate.State != StateCompleted || secondLate.Attempt == nil || secondLate.Attempt.CompletionWarning != "PROCESS_LOST" || secondLate.Attempt.LifecyclePhase != string(LifecycleReturnReady) {
		t.Fatalf("late RETURN_READY changed process-loss result: %+v, %v", secondLate, err)
	}
	clearLifecycleAttempt(t, ctx, service, owner, secondSession, secondLate)

	// The ending watchdog also completes a confirmed result successfully and
	// preserves a warning for the missing normal return acknowledgement.
	third, thirdSession := startLifecycleRunningAttempt(t, ctx, service, pool, owner, member, "lifecycle-timeout", "world-lifecycle-timeout", matchConfig)
	thirdResult := lifecycleInputFromAttempt(third, LifecycleResultConfirmed, 1)
	thirdEnding, err := service.P2PLifecycle(ctx, owner, thirdSession, third.Attempt.AttemptID, thirdResult)
	if err != nil || thirdEnding.Attempt == nil || thirdEnding.Attempt.State != AttemptEnding {
		t.Fatalf("timeout result confirmation = %+v, %v", thirdEnding, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE match_attempts SET ending_deadline = $2 WHERE id = $1`, third.Attempt.AttemptID, currentTime.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	thirdTimeout, err := service.Get(ctx, third.LobbyID, owner.PlayerID)
	if err != nil || thirdTimeout.State != StateCompleted || thirdTimeout.Attempt == nil || thirdTimeout.Attempt.CompletionWarning != "MATCH_ENDING_TIMEOUT" {
		t.Fatalf("ending timeout did not preserve confirmed result: %+v, %v", thirdTimeout, err)
	}
	thirdReturn := lifecycleInputFromAttempt(thirdEnding, LifecycleReturnReady, 2)
	thirdLate, err := service.P2PLifecycle(ctx, owner, thirdSession, third.Attempt.AttemptID, thirdReturn)
	if err != nil || thirdLate.State != StateCompleted || thirdLate.Attempt == nil || thirdLate.Attempt.CompletionWarning != "MATCH_ENDING_TIMEOUT" {
		t.Fatalf("late return after ending timeout changed result: %+v, %v", thirdLate, err)
	}
	clearLifecycleAttempt(t, ctx, service, owner, thirdSession, thirdLate)

	// If transport/relay revoke is temporarily unavailable after the native
	// receipt, retain native_cleared_at and retry the external close before
	// allowing the player to start another lobby.
	fourth, fourthSession := startLifecycleRunningAttempt(t, ctx, service, pool, owner, member, "lifecycle-transport-retry", "world-lifecycle-transport-retry", matchConfig)
	fourthResult := lifecycleInputFromAttempt(fourth, LifecycleResultConfirmed, 1)
	fourthEnding, err := service.P2PLifecycle(ctx, owner, fourthSession, fourth.Attempt.AttemptID, fourthResult)
	if err != nil {
		t.Fatalf("transport retry result confirmation: %v", err)
	}
	fourthTerminal, err := service.P2PComplete(ctx, owner, fourthSession, fourth.Attempt.AttemptID, false, "PROCESS_LOST")
	if err != nil || fourthTerminal.State != StateCompleted {
		t.Fatalf("transport retry process-loss completion = %+v, %v", fourthTerminal, err)
	}
	transport.failNextClose = true
	fourthPending, err := service.P2PNativeCleared(ctx, owner, fourthSession, fourth.Attempt.AttemptID, fourthEnding.Attempt.WorldInstanceID, fourthEnding.Attempt.RosterRevision, fourthEnding.Attempt.RouteGeneration)
	if err != nil || fourthPending.Attempt == nil || fourthPending.Attempt.CleanupState != "PENDING" || fourthPending.Attempt.NativeClearedAt == nil {
		t.Fatalf("transport failure did not retain native receipt: %+v, %v", fourthPending, err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	fourthCleared, err := service.Get(ctx, fourth.LobbyID, owner.PlayerID)
	if err != nil || fourthCleared.Attempt == nil || fourthCleared.Attempt.CleanupState != "CLEARED" || fourthCleared.Attempt.NativeClearedAt == nil {
		t.Fatalf("transport cleanup retry did not reach CLEARED: %+v, %v", fourthCleared, err)
	}
}

func startLifecycleRunningAttempt(
	t *testing.T,
	ctx context.Context,
	service *Service,
	pool *pgxpool.Pool,
	owner, member Actor,
	idempotencyKey, world string,
	matchConfig config.MatchLobbyConfig,
) (Snapshot, string) {
	t.Helper()
	frozen, hostToken := createTwoPlayerReadyLobby(t, ctx, service, owner, member, idempotencyKey, idempotencyKey)
	if frozen.Attempt == nil {
		t.Fatal("lifecycle fixture omitted its attempt")
	}
	attemptID := frozen.Attempt.AttemptID
	allocation, err := service.P2PHostAllocation(ctx, owner, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	_ = decodeAllocationClaims(t, allocation.Allocation)
	var authoritySession string
	if err := pool.QueryRow(ctx, `SELECT authority_session_id FROM match_attempts WHERE id = $1`, attemptID).Scan(&authoritySession); err != nil {
		t.Fatal(err)
	}
	if _, err := service.P2PPayloadInstalled(ctx, owner, attemptID, authoritySession, strictNativeAdmissionVersion, matchConfig.LockedGameSHA256, frozen.Attempt.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	connecting, err := service.P2PAuthorityReady(ctx, owner, attemptID, authoritySession, hostToken, "10.89.0.1", 7788, frozen.Attempt.RouteGeneration, world, "native-lifecycle-host-0001")
	if err != nil || connecting.Attempt == nil {
		t.Fatalf("lifecycle authority ready = %+v, %v", connecting, err)
	}
	grant, err := service.JoinGrant(ctx, member, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	grantJTI := decodeJoinGrantJTI(t, grant.Grant)
	if _, err := service.P2PMarkAdmissionDelivered(ctx, owner, authoritySession, attemptID, grantJTI); err != nil {
		t.Fatal(err)
	}
	nonce := "native-lifecycle-member-0001"
	reservation, err := service.P2PReserveAdmission(ctx, owner, authoritySession, attemptID, world, member.PlayerID, grantJTI, nonce, grant.ConnectionGeneration)
	if err != nil {
		t.Fatal(err)
	}
	running, err := service.P2PConfirmConnected(ctx, owner, authoritySession, attemptID, reservation.WorldInstanceID, member.PlayerID, grantJTI, nonce, grant.ConnectionGeneration)
	if err != nil || running.Attempt == nil || running.Attempt.State != AttemptRunning {
		t.Fatalf("lifecycle fixture did not reach RUNNING: %+v, %v", running, err)
	}
	return running, authoritySession
}

func lifecycleInputFromAttempt(snapshot Snapshot, phase LifecyclePhase, eventSeq int64) LifecycleInput {
	return LifecycleInput{
		Phase:           phase,
		WorldInstanceID: snapshot.Attempt.WorldInstanceID,
		RosterRevision:  snapshot.Attempt.RosterRevision,
		RouteGeneration: snapshot.Attempt.RouteGeneration,
		MatchGeneration: snapshot.Attempt.MatchGeneration,
		EventSeq:        eventSeq,
	}
}

func clearLifecycleAttempt(
	t *testing.T,
	ctx context.Context,
	service *Service,
	owner Actor,
	authoritySession string,
	snapshot Snapshot,
) {
	t.Helper()
	if snapshot.Attempt == nil {
		t.Fatal("lifecycle terminal snapshot omitted attempt")
	}
	cleared, err := service.P2PNativeCleared(
		ctx, owner, authoritySession, snapshot.Attempt.AttemptID,
		snapshot.Attempt.WorldInstanceID, snapshot.Attempt.RosterRevision,
		snapshot.Attempt.RouteGeneration,
	)
	if err != nil || cleared.Attempt == nil || cleared.Attempt.CleanupState != "CLEARED" {
		t.Fatalf("lifecycle native cleanup = %+v, %v", cleared, err)
	}
}
