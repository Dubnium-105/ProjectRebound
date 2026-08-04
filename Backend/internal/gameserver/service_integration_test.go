package gameserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGameServerRegistryAgainstPostgreSQL(t *testing.T) {
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
		t.Fatal(err)
	}
	repository := NewRepository(pool)
	registrationRepository := gameserverregistration.NewRepository()
	authority, err := NewAuthority(config.Defaults.GameServer, "development")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, registrationRepository, config.Defaults.GameServer, authority)
	fixedNow := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return fixedNow }
	service.proofVerifier.now = service.now
	_, playerCSR, err := NewNodeIdentity("player-server")
	if err != nil {
		t.Fatal(err)
	}
	firstPrivateKey, firstCSR, err := NewNodeIdentity("first-server")
	if err != nil {
		t.Fatal(err)
	}
	_, secondCSR, err := NewNodeIdentity("second-server")
	if err != nil {
		t.Fatal(err)
	}
	_, concurrentCSR, err := NewNodeIdentity("concurrent-server")
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	firstInstance := fmt.Sprintf("integration-primary-%d", suffix)
	secondInstance := fmt.Sprintf("integration-secondary-%d", suffix)
	concurrentInstance := fmt.Sprintf("integration-concurrent-%d", suffix)
	playerInstance := fmt.Sprintf("integration-player-%d", suffix)
	redeemInstance := fmt.Sprintf("integration-redeem-%d", suffix)
	adminID := fmt.Sprintf("adm_game_server_%d", suffix)
	playerID := fmt.Sprintf("p_game_server_%d", suffix)
	inviteID := fmt.Sprintf("inv_game_server_%d", suffix)
	inviteUseID := fmt.Sprintf("icu_game_server_%d", suffix)
	inviteHash := sha256.Sum256([]byte(inviteID))
	redeemPlayerID := fmt.Sprintf("p_game_redeem_%d", suffix)
	redeemInviteID := fmt.Sprintf("inv_game_redeem_%d", suffix)
	redeemCode := fmt.Sprintf("INV-DEDICATED-%d", suffix)
	redeemHash := sha256.Sum256([]byte(redeemCode))
	redeemSteamID := fmt.Sprintf("76562%012d", suffix%1_000_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_users (
			id, username, display_name, password_hash, status, mfa_required,
			created_at, updated_at
		) VALUES ($1, $2, 'Game Server Integration', 'test-only', 'ACTIVE', TRUE, $3, $3)
	`, adminID, adminID, fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider, auth_level,
			created_at, updated_at
		) VALUES ($1, $2, 'Game Server Registrant', 'ACTIVE', 'steam_ticket', 'verified', $3, $3)
	`, playerID, fmt.Sprintf("76561%012d", suffix%1_000_000_000_000), fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invite_codes (
			id, code_hash, batch_name, max_uses, used_count, enabled, permissions,
			created_by, created_at, updated_at
		) VALUES ($1, $2, 'Dedicated Server Integration', 1, 1, TRUE,
		          '{"allow_game_server_registration": true}'::jsonb, $3, $4, $4)
	`, inviteID, inviteHash[:], adminID, fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invite_code_uses (
			id, invite_code_id, player_id, steam_id, used_at, result, permission_snapshot
		) VALUES ($1, $2, $3, $4, $5, 'SUCCESS',
		          '{"allow_game_server_registration": true}'::jsonb)
	`, inviteUseID, inviteID, playerID,
		fmt.Sprintf("76561%012d", suffix%1_000_000_000_000), fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO player_feature_grants (player_id, capability, source_invite_use_id, granted_at)
		VALUES ($1, 'game_server_registration', $2, $3)
	`, playerID, inviteUseID, fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider, auth_level,
			created_at, updated_at
		) VALUES ($1, $2, 'Invite Redeemer', 'ACTIVE', 'steam_ticket', 'verified', $3, $3)
	`, redeemPlayerID, redeemSteamID, fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invite_codes (
			id, code_hash, batch_name, max_uses, used_count, enabled, permissions,
			created_by, created_at, updated_at
		) VALUES ($1, $2, 'Dedicated Server Redemption', 1, 0, TRUE,
		          '{"allow_game_server_registration": true}'::jsonb, $3, $4, $4)
	`, redeemInviteID, redeemHash[:], adminID, fixedNow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		instances := []string{firstInstance, secondInstance, concurrentInstance, playerInstance, redeemInstance}
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_server_registration_tokens WHERE instance_id = ANY($1)", instances)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_servers WHERE instance_id = ANY($1)", instances)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM player_feature_grants WHERE player_id = ANY($1)", []string{playerID, redeemPlayerID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM invite_code_uses WHERE invite_code_id = $1", inviteID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM invite_codes WHERE id = $1", inviteID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM invite_code_uses WHERE invite_code_id = $1", redeemInviteID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM invite_codes WHERE id = $1", redeemInviteID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", redeemPlayerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_users WHERE id = $1", adminID)
	})
	playerCredential, err := service.IssueRegistrationCredential(ctx, RegistrationCredentialInput{
		InstanceID: playerInstance, PlayerID: playerID,
	})
	if err != nil || !gameserverregistration.HasValidShape(playerCredential.Plaintext) ||
		playerCredential.Credential.SourceInviteUseID != inviteUseID {
		t.Fatalf("player registration credential = %#v, %v", playerCredential, err)
	}
	redeemedCredential, err := service.IssueRegistrationCredential(ctx, RegistrationCredentialInput{
		InstanceID: redeemInstance, PlayerID: redeemPlayerID, SteamID: redeemSteamID,
		InviteCode: redeemCode,
	})
	if err != nil || redeemedCredential.Credential.SourceInviteUseID == "" {
		t.Fatalf("redeemed registration credential = %#v, %v", redeemedCredential, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE invite_codes SET permissions = '{}'::jsonb WHERE id = $1", redeemInviteID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueRegistrationCredential(ctx, RegistrationCredentialInput{
		InstanceID: redeemInstance, PlayerID: redeemPlayerID,
	}); err != nil {
		t.Fatalf("immutable redeemed invitation grant was not retained: %v", err)
	}
	playerServer, err := service.Register(ctx, RegistrationInput{
		InstanceID: playerInstance, DisplayName: "Player Owned", Region: "asia-hk",
		Mode: "tdm", Version: "1.0.0", PublicHost: "9.9.9.9", PublicPort: 7780,
		MaxPlayers: 8, CSRPEM: playerCSR,
	}, playerCredential.Plaintext)
	if err != nil || playerServer.Server.OwnerPlayerID != playerID {
		t.Fatalf("player-owned registration = %#v, %v", playerServer, err)
	}
	firstRegistrationToken := issueRegistrationToken(
		t, ctx, pool, registrationRepository, adminID, firstInstance, fixedNow,
	)
	input := RegistrationInput{
		InstanceID: firstInstance, DisplayName: "Integration Server", Region: "us-west",
		Mode: "tdm", Version: "1.0.0", PublicHost: "8.8.8.8", PublicPort: 7777, MaxPlayers: 12,
		CSRPEM: firstCSR,
	}
	first, err := service.Register(ctx, input, firstRegistrationToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(ctx, input, firstRegistrationToken); errorStatus(err) != 401 {
		t.Fatalf("consumed registration token reuse error = %v", err)
	}
	replacementRegistrationToken := issueRegistrationToken(
		t, ctx, pool, registrationRepository, adminID, firstInstance, fixedNow,
	)
	secondRegistration, err := service.Register(ctx, input, replacementRegistrationToken)
	if err != nil {
		t.Fatal(err)
	}
	if secondRegistration.Server.ID != first.Server.ID || secondRegistration.ServerToken == first.ServerToken {
		t.Fatalf("authorized re-registration did not preserve ID and rotate token: %#v %#v", first, secondRegistration)
	}
	proof := SignedRequestInput{
		ServerID: secondRegistration.Server.ID, ServerToken: secondRegistration.ServerToken,
		CertificateFingerprint: secondRegistration.Server.CertificateFingerprint,
		Timestamp:              fixedNow.Unix(),
		Nonce:                  base64.RawURLEncoding.EncodeToString([]byte("signed-request-nonce-1")),
		CredentialGeneration:   secondRegistration.Server.CredentialGeneration,
		Method:                 "POST", RequestTarget: "/v1/game-servers/" + secondRegistration.Server.ID + "/heartbeat",
		Body: []byte(`{"state":"READY","player_count":2}`),
	}
	proof.Signature, err = SignRequest(proof, firstPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if principal, err := service.VerifySignedRequest(ctx, proof); err != nil || principal.Legacy {
		t.Fatalf("signed request proof = %#v, %v", principal, err)
	}
	if _, err := service.VerifySignedRequest(ctx, proof); errorStatus(err) != 401 {
		t.Fatalf("signed request replay error = %v", err)
	}
	if _, err := service.Heartbeat(ctx, first.Server.ID, first.ServerToken, HeartbeatInput{State: StateReady}); errorStatus(err) != 401 {
		t.Fatalf("old rotated token heartbeat error = %v", err)
	}
	ready, err := service.Heartbeat(ctx, first.Server.ID, secondRegistration.ServerToken, HeartbeatInput{State: StateReady, PlayerCount: 2})
	if err != nil || ready.State != StateReady || ready.PlayerCount != 2 {
		t.Fatalf("heartbeat = %#v, %v", ready, err)
	}
	rotationPrivateKey, rotationCSR, err := NewNodeIdentity("rotated-server")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.RotateCredential(ctx, first.Server.ID, secondRegistration.ServerToken, rotationCSR)
	if err != nil || rotated.ServerToken == secondRegistration.ServerToken || rotated.CredentialGeneration < 2 {
		t.Fatalf("credential rotation = %#v, %v", rotated, err)
	}
	previousProof := proof
	previousProof.Nonce = base64.RawURLEncoding.EncodeToString([]byte("signed-request-nonce-2"))
	previousProof.Signature, err = SignRequest(previousProof, firstPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifySignedRequest(ctx, previousProof); err != nil {
		t.Fatalf("previous signed credential was not accepted during overlap: %v", err)
	}
	currentProof := SignedRequestInput{
		ServerID: rotated.ServerID, ServerToken: rotated.ServerToken,
		CertificateFingerprint: rotated.CertificateFingerprint,
		Timestamp:              fixedNow.Unix(),
		Nonce:                  base64.RawURLEncoding.EncodeToString([]byte("signed-request-nonce-3")),
		CredentialGeneration:   rotated.CredentialGeneration,
		Method:                 "POST", RequestTarget: "/v1/game-servers/" + rotated.ServerID + "/heartbeat",
		Body: []byte(`{"state":"READY","player_count":2}`),
	}
	currentProof.Signature, err = SignRequest(currentProof, rotationPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifySignedRequest(ctx, currentProof); err != nil {
		t.Fatalf("rotated signed credential was rejected: %v", err)
	}
	if _, err := service.RotateCredential(ctx, first.Server.ID, secondRegistration.ServerToken, rotationCSR); errorStatus(err) != 401 {
		t.Fatalf("previous credential was allowed to rotate identity: %v", err)
	}
	if err := service.Deregister(ctx, first.Server.ID, secondRegistration.ServerToken); errorStatus(err) != 401 {
		t.Fatalf("previous credential was allowed to deregister identity: %v", err)
	}
	if _, err := service.Heartbeat(ctx, first.Server.ID, secondRegistration.ServerToken, HeartbeatInput{State: StateReady, PlayerCount: 2}); err != nil {
		t.Fatalf("previous token was not accepted during overlap: %v", err)
	}
	service.now = func() time.Time {
		return fixedNow.Add(config.Defaults.GameServer.ServerTokenRotationGrace() + time.Second)
	}
	service.proofVerifier.now = service.now
	previousProof.Timestamp = service.now().Unix()
	previousProof.Nonce = base64.RawURLEncoding.EncodeToString([]byte("signed-request-nonce-4"))
	previousProof.Signature, err = SignRequest(previousProof, firstPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifySignedRequest(ctx, previousProof); errorStatus(err) != 401 {
		t.Fatalf("expired previous signed credential error = %v", err)
	}
	if _, err := service.Heartbeat(ctx, first.Server.ID, secondRegistration.ServerToken, HeartbeatInput{State: StateReady, PlayerCount: 2}); errorStatus(err) != 401 {
		t.Fatalf("expired overlap token heartbeat error = %v", err)
	}
	if _, err := service.Heartbeat(ctx, first.Server.ID, rotated.ServerToken, HeartbeatInput{State: StateReady, PlayerCount: 2}); err != nil {
		t.Fatalf("rotated token heartbeat error = %v", err)
	}
	service.now = func() time.Time { return fixedNow }
	service.proofVerifier.now = service.now

	secondRegistrationToken := issueRegistrationToken(
		t, ctx, pool, registrationRepository, adminID, secondInstance, fixedNow,
	)
	if _, err := service.Register(ctx, input, secondRegistrationToken); errorStatus(err) != 401 {
		t.Fatalf("instance-bound registration token was accepted for another instance: %v", err)
	}
	secondServer, err := service.Register(ctx, RegistrationInput{
		InstanceID: secondInstance, DisplayName: "Other", Region: "eu-west", Mode: "tdm",
		Version: "1.0.0", PublicHost: "1.1.1.1", PublicPort: 7778, MaxPlayers: 8, CSRPEM: secondCSR,
	}, secondRegistrationToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(ctx, secondServer.Server.ID, secondRegistration.ServerToken, HeartbeatInput{State: StateReady}); errorStatus(err) != 401 {
		t.Fatalf("cross-server token was accepted: %v", err)
	}

	concurrentRegistrationToken := issueRegistrationToken(
		t, ctx, pool, registrationRepository, adminID, concurrentInstance, fixedNow,
	)
	concurrentInput := RegistrationInput{
		InstanceID: concurrentInstance, DisplayName: "Concurrent", Region: "asia-hk", Mode: "tdm",
		Version: "1.0.0", PublicHost: "9.9.9.9", PublicPort: 7779, MaxPlayers: 8, CSRPEM: concurrentCSR,
	}
	var wait sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Register(ctx, concurrentInput, concurrentRegistrationToken)
			statuses <- errorStatus(err)
		}()
	}
	wait.Wait()
	close(statuses)
	successes, rejected := 0, 0
	for status := range statuses {
		if status == 0 {
			successes++
		}
		if status == 401 {
			rejected++
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent registration results: successes=%d rejected=%d", successes, rejected)
	}

	if _, err := pool.Exec(ctx, "UPDATE game_servers SET last_heartbeat_at = $2, state = 'READY' WHERE id = $1", first.Server.ID, fixedNow.Add(-46*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	unhealthy, err := service.Get(ctx, first.Server.ID)
	if err != nil || unhealthy.State != StateUnhealthy {
		t.Fatalf("45-second sweep = %#v, %v", unhealthy, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE game_servers SET last_heartbeat_at = $2 WHERE id = $1", first.Server.ID, fixedNow.Add(-91*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	offline, err := service.Get(ctx, first.Server.ID)
	if err != nil || offline.State != StateOffline {
		t.Fatalf("90-second sweep = %#v, %v", offline, err)
	}

	if err := service.Deregister(ctx, secondServer.Server.ID, secondServer.ServerToken); err != nil {
		t.Fatal(err)
	}
	deregistered, err := service.Get(ctx, secondServer.Server.ID)
	if err != nil || deregistered.State != StateOffline || deregistered.TokenRevokedAt == nil {
		t.Fatalf("deregistered server = %#v, %v", deregistered, err)
	}
}

func issueRegistrationToken(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *gameserverregistration.Repository,
	adminID, instanceID string,
	now time.Time,
) string {
	t.Helper()
	plaintext, tokenHash, err := gameserverregistration.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := repository.RevokeActiveForInstance(ctx, tx, instanceID, now); err != nil {
		t.Fatal(err)
	}
	credential := gameserverregistration.Credential{
		ID:         fmt.Sprintf("gsrt_test_%d", time.Now().UnixNano()),
		InstanceID: instanceID, CreatedBy: adminID,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := repository.Insert(ctx, tx, credential, tokenHash); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func errorStatus(err error) int {
	if err == nil {
		return 0
	}
	status, _, _, _ := errorDetails(err)
	return status
}
