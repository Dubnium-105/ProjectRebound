package vnt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNodeDirectoryPaginationAgainstPostgreSQL(t *testing.T) {
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

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	playerID := "player_" + suffix
	nodeIDs := []string{"vnt_" + suffix[:30] + "01", "vnt_" + suffix[:30] + "02"}
	now := time.Now().UTC().Truncate(time.Second)
	steamID := fmt.Sprintf("%017d", now.UnixNano()%100_000_000_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, last_login_at, created_at, updated_at
		) VALUES ($1, $2, 'VNT Pagination', 'ACTIVE', FALSE,
		          'steam_ticket', 'verified', $3, $3, $3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
	})

	for index, nodeID := range nodeIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO vnt_nodes (
				id, owner_player_id, advertised_host, port, region, location, state,
				vnts_version, wrapper_version, server_key_fingerprint,
				supported_transports, max_rooms, reported_sessions,
				last_heartbeat_at, last_reachable_at, created_at, updated_at
			) VALUES ($1,$2,'203.0.113.10',$3,'pagination-test','Test','ONLINE',
			          '1.0.0','1.0.0',$4,ARRAY['tcp','udp'],10,0,$5,$5,$5,$5)
		`, nodeID, playerID, 31000+index,
			"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now); err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(NewRepository(pool), nil)
	first, err := service.List(ctx, ListFilter{Status: StateOnline, Region: "pagination-test", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].NodeID != nodeIDs[0] || first.NextCursor != nodeIDs[0] {
		t.Fatalf("first page = %#v", first)
	}
	second, err := service.List(ctx, ListFilter{
		Status: StateOnline, Region: "pagination-test", Cursor: first.NextCursor, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].NodeID != nodeIDs[1] || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestNodeRetirementRevokesCredentialsAgainstPostgreSQL(t *testing.T) {
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

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	playerID := "player_" + suffix
	nodeIDs := []string{"vnt_" + suffix[:30] + "11", "vnt_" + suffix[:30] + "12"}
	credentialIDs := []string{"vnc_" + suffix[:30] + "11", "vnc_" + suffix[:30] + "12"}
	now := time.Now().UTC().Truncate(time.Second)
	steamID := fmt.Sprintf("%017d", (now.UnixNano()+1)%100_000_000_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, last_login_at, created_at, updated_at
		) VALUES ($1, $2, 'VNT Retirement', 'ACTIVE', FALSE,
		          'steam_ticket', 'verified', $3, $3, $3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE node_id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_node_credentials WHERE node_id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
	})

	tokens := make([]string, len(nodeIDs))
	for index, nodeID := range nodeIDs {
		token, tokenHash, err := newSecret("vnn_")
		if err != nil {
			t.Fatal(err)
		}
		tokens[index] = token
		state := StateOnline
		if index == 1 {
			state = StateDraining
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO vnt_nodes (
				id, owner_player_id, advertised_host, port, region, location, state,
				vnts_version, wrapper_version, server_key_fingerprint,
				supported_transports, max_rooms, reported_sessions,
				last_heartbeat_at, last_reachable_at, created_at, updated_at
			) VALUES ($1,$2,'203.0.113.11',$3,'retirement-test','Test',$4,
			          '1.0.0','1.0.0',$5,ARRAY['tcp','udp'],10,0,$6,$6,$6,$6)
		`, nodeID, playerID, 32000+index, state,
			"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5)
		`, credentialIDs[index], nodeID, tokenHash, now.Add(90*24*time.Hour), now); err != nil {
			t.Fatal(err)
		}
	}

	repository := NewRepository(pool)
	service := NewService(repository, nil)
	service.now = func() time.Time { return now }
	state, err := service.Retire(ctx, nodeIDs[0], tokens[0])
	if err != nil || state != StateRetired {
		t.Fatalf("immediate retirement = %q, %v", state, err)
	}
	assertNodeRetiredAndCredentialRevoked(t, ctx, pool, nodeIDs[0])
	if _, err := service.RotateCredential(ctx, nodeIDs[0], tokens[0]); vntErrorCode(err) != "VNT_NODE_UNAUTHORIZED" {
		t.Fatalf("retired node credential rotation = %v", err)
	}

	if err := repository.MarkReachable(ctx, nodeIDs[1], now.Add(time.Second)); err != nil {
		t.Fatalf("mark draining node reachable: %v", err)
	}
	var drainingState string
	if err := pool.QueryRow(ctx, "SELECT state FROM vnt_nodes WHERE id = $1", nodeIDs[1]).Scan(&drainingState); err != nil {
		t.Fatal(err)
	}
	if drainingState != StateDraining {
		t.Fatalf("reachable draining node state = %q", drainingState)
	}
	service.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	assertNodeRetiredAndCredentialRevoked(t, ctx, pool, nodeIDs[1])
}

func TestCredentialRotationOverlapAgainstPostgreSQL(t *testing.T) {
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

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	playerID := "player_" + suffix
	nodeID := "vnt_" + suffix
	credentialID := "vnc_" + suffix
	now := time.Now().UTC().Truncate(time.Second)
	steamID := fmt.Sprintf("%017d", (now.UnixNano()+2)%100_000_000_000_000_000)
	oldToken, oldHash, err := newSecret("vnn_")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, last_login_at, created_at, updated_at
		) VALUES ($1, $2, 'VNT Rotation', 'ACTIVE', FALSE,
		          'steam_ticket', 'verified', $3, $3, $3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_nodes (
			id, owner_player_id, advertised_host, port, region, location, state,
			vnts_version, wrapper_version, server_key_fingerprint,
			supported_transports, max_rooms, reported_sessions,
			last_heartbeat_at, last_reachable_at, created_at, updated_at
		) VALUES ($1,$2,'203.0.113.12',32300,'rotation-test','Test','ONLINE',
		          '1.0.0','1.0.0',$3,ARRAY['tcp','udp'],10,0,$4,$4,$4,$4)
	`, nodeID, playerID,
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, credentialID, nodeID, oldHash, now.Add(90*24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE node_id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_node_credentials WHERE node_id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
	})

	service := NewService(NewRepository(pool), nil)
	service.now = func() time.Time { return now }
	service.SetCredentialRotationGrace(time.Minute)
	rotated, err := service.RotateCredential(ctx, nodeID, oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.PreviousValidUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("previous valid until = %s", rotated.PreviousValidUntil)
	}

	heartbeat := HeartbeatInput{
		WrapperVersion: "1.0.0", VNTSVersion: "1.0.0", UptimeSeconds: 1,
		ServerProcessHealthy: false,
	}
	service.now = func() time.Time { return now.Add(30 * time.Second) }
	if err := service.Heartbeat(ctx, nodeID, oldToken, heartbeat); err != nil {
		t.Fatalf("old credential heartbeat during overlap: %v", err)
	}
	if _, err := service.RotateCredential(ctx, nodeID, oldToken); vntErrorCode(err) != "VNT_NODE_UNAUTHORIZED" {
		t.Fatalf("old credential rotated again during overlap: %v", err)
	}
	if _, err := service.Retire(ctx, nodeID, oldToken); vntErrorCode(err) != "VNT_NODE_UNAUTHORIZED" {
		t.Fatalf("old credential retired node during overlap: %v", err)
	}

	service.now = func() time.Time { return now.Add(61 * time.Second) }
	if err := service.Heartbeat(ctx, nodeID, oldToken, heartbeat); vntErrorCode(err) != "VNT_NODE_UNAUTHORIZED" {
		t.Fatalf("old credential heartbeat after overlap: %v", err)
	}
	if err := service.Heartbeat(ctx, nodeID, rotated.NodeToken, heartbeat); err != nil {
		t.Fatalf("new credential heartbeat: %v", err)
	}
}

func TestOwnerRecoveryAndRetirementAgainstPostgreSQL(t *testing.T) {
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

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	ownerID := "player_owner_" + suffix
	otherID := "player_other_" + suffix
	nodeID := "vnt_" + suffix
	now := time.Now().UTC().Truncate(time.Second)
	for index, playerID := range []string{ownerID, otherID} {
		steamID := fmt.Sprintf("%017d", (now.UnixNano()+int64(10+index))%100_000_000_000_000_000)
		if _, err := pool.Exec(ctx, `
			INSERT INTO players (
				id, steam_id, persona_name, account_status, is_vip,
				auth_provider, auth_level, last_login_at, created_at, updated_at
			) VALUES ($1, $2, 'VNT Recovery', 'ACTIVE', FALSE,
			          'steam_ticket', 'trusted', $3, $3, $3)
		`, playerID, steamID, now); err != nil {
			t.Fatal(err)
		}
	}
	oldToken, oldHash, err := newSecret("vnn_")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_nodes (
			id, owner_player_id, advertised_host, port, region, location, state,
			vnts_version, wrapper_version, server_key_fingerprint,
			supported_transports, max_rooms, reported_sessions,
			last_heartbeat_at, last_reachable_at, created_at, updated_at
		) VALUES ($1,$2,'203.0.113.13',32400,'recovery-test','Test','OFFLINE',
		          '1.0.0','1.0.0',$3,ARRAY['tcp','udp'],10,0,$4,$4,$4,$4)
	`, nodeID, ownerID,
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, "vnc_"+suffix, nodeID, oldHash, now.Add(90*24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE node_id = $1 OR player_id = ANY($2)", nodeID, []string{ownerID, otherID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_node_enrollments WHERE owner_player_id = ANY($1)", []string{ownerID, otherID})
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_node_credentials WHERE node_id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", []string{ownerID, otherID})
	})

	service := NewService(NewRepository(pool), nil)
	service.now = func() time.Time { return now.Add(time.Minute) }
	trustedActor := func(playerID string) Actor {
		return Actor{
			PlayerID: playerID, AccountStatus: "ACTIVE", SteamVerified: true, IntegrityTrusted: true,
		}
	}
	if _, err := service.RetireOwned(ctx, trustedActor(otherID), nodeID); vntErrorCode(err) != "VNT_NODE_NOT_FOUND" {
		t.Fatalf("non-owner retirement = %v", err)
	}
	wrongOwnerCode, wrongOwnerHash, err := newSecret("vne_")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_node_enrollments (id, owner_player_id, label, secret_hash, expires_at, created_at)
		VALUES ($1,$2,'wrong-owner-recovery',$3,$4,$5)
	`, "vne_wrong_"+suffix, otherID, wrongOwnerHash, now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Recover(ctx, nodeID, wrongOwnerCode, RegisterInput{
		AdvertisedHost: "203.0.113.13", Port: 32400, Region: "recovery-test", Location: "Test",
		VNTSVersion: "1.0.0", WrapperVersion: "1.0.0",
		ServerKeyFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		SupportedTransports:  []string{"tcp", "udp"}, MaxRooms: 10,
	}); vntErrorCode(err) != "VNT_NODE_NOT_FOUND" {
		t.Fatalf("non-owner recovery = %v", err)
	}

	recoveryCode, recoveryHash, err := newSecret("vne_")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_node_enrollments (id, owner_player_id, label, secret_hash, expires_at, created_at)
		VALUES ($1,$2,'recovery',$3,$4,$5)
	`, "vne_"+suffix, ownerID, recoveryHash, now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Recover(ctx, nodeID, recoveryCode, RegisterInput{
		AdvertisedHost: "203.0.113.13", Port: 32400, Region: "recovery-test", Location: "Test",
		VNTSVersion: "1.0.0", WrapperVersion: "1.0.0",
		ServerKeyFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		SupportedTransports:  []string{"tcp", "udp"}, MaxRooms: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.NodeID != nodeID || recovered.State != StateRegistering || recovered.NodeToken == "" {
		t.Fatalf("recovered node = %#v", recovered)
	}
	owned, err := service.ListOwned(ctx, trustedActor(ownerID), OwnedListFilter{Limit: 10})
	if err != nil || len(owned.Items) != 1 || owned.Items[0].NodeID != nodeID || owned.Items[0].CredentialExpiresAt == nil {
		t.Fatalf("owned VNT nodes = %#v, %v", owned, err)
	}
	heartbeat := HeartbeatInput{
		WrapperVersion: "1.0.0", VNTSVersion: "1.0.0", UptimeSeconds: 1,
		ServerProcessHealthy: false,
	}
	if err := service.Heartbeat(ctx, nodeID, oldToken, heartbeat); vntErrorCode(err) != "VNT_NODE_UNAUTHORIZED" {
		t.Fatalf("old credential after recovery = %v", err)
	}
	if err := service.Heartbeat(ctx, nodeID, recovered.NodeToken, heartbeat); err != nil {
		t.Fatalf("recovered credential heartbeat: %v", err)
	}
	service.now = func() time.Time { return now.Add(2 * time.Minute) }
	state, err := service.RetireOwned(ctx, trustedActor(ownerID), nodeID)
	if err != nil || state != StateRetired {
		t.Fatalf("owner retirement = %q, %v", state, err)
	}
	assertNodeRetiredAndCredentialRevoked(t, ctx, pool, nodeID)
	var recoveryAudits, retirementAudits int
	var auditDetails string
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE event_type = 'VNT_NODE_RECOVERED' AND result = 'SUCCEEDED'),
			COUNT(*) FILTER (WHERE event_type = 'VNT_NODE_RETIREMENT_REQUESTED' AND result = 'SUCCEEDED'),
			COALESCE(string_agg(details::text, ' '), '')
		FROM vnt_security_audit_logs WHERE node_id = $1
	`, nodeID).Scan(&recoveryAudits, &retirementAudits, &auditDetails); err != nil {
		t.Fatal(err)
	}
	if recoveryAudits != 1 || retirementAudits != 1 {
		t.Fatalf("owner recovery audit counts = recovery %d retirement %d", recoveryAudits, retirementAudits)
	}
	for _, secret := range []string{oldToken, recoveryCode, recovered.NodeToken} {
		if strings.Contains(auditDetails, secret) {
			t.Fatal("VNT owner lifecycle audit persisted a plaintext credential")
		}
	}
}

type integrationEntitlements struct{}

func (integrationEntitlements) Has(context.Context, string, string) (bool, error) { return true, nil }

func TestEnrollmentEnforcesOwnerQuotaAgainstPostgreSQL(t *testing.T) {
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

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	playerID := "player_quota_" + suffix
	nodeIDs := []string{"vnt_" + suffix + "1", "vnt_" + suffix + "2"}
	now := time.Now().UTC().Truncate(time.Second)
	steamID := fmt.Sprintf("%017d", (now.UnixNano()+20)%100_000_000_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, last_login_at, created_at, updated_at
		) VALUES ($1, $2, 'VNT Quota', 'ACTIVE', FALSE,
		          'steam_ticket', 'trusted', $3, $3, $3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range nodeIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO vnt_nodes (
				id, owner_player_id, advertised_host, port, region, location, state,
				vnts_version, wrapper_version, server_key_fingerprint,
				supported_transports, max_rooms, created_at, updated_at
			) VALUES ($1,$2,$3,$4,'quota-test','Test','OFFLINE','1','1',$5,ARRAY['tcp','udp'],1,$6,$6)
		`, nodeID, playerID, fmt.Sprintf("203.0.113.%d", 20+index), 32500+index,
			"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE player_id = $1", playerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_node_enrollments WHERE owner_player_id = $1", playerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
	})

	service := NewService(NewRepository(pool), integrationEntitlements{})
	service.SetMaxNodesPerPlayer(2)
	actor := Actor{PlayerID: playerID, AccountStatus: "ACTIVE", SteamVerified: true, IntegrityTrusted: true}
	if _, err := service.CreateEnrollment(ctx, actor, "quota-node"); vntErrorCode(err) != "VNT_NODE_QUOTA_EXCEEDED" {
		t.Fatalf("enrollment above quota = %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE vnt_nodes SET state = 'RETIRED', retired_at = $2 WHERE id = $1", nodeIDs[0], now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateEnrollment(ctx, actor, "quota-node"); err != nil {
		t.Fatalf("enrollment after retirement: %v", err)
	}
}

func assertNodeRetiredAndCredentialRevoked(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	nodeID string,
) {
	t.Helper()
	var state string
	var revoked bool
	if err := pool.QueryRow(ctx, `
		SELECT node.state, credential.revoked_at IS NOT NULL
		FROM vnt_nodes node
		JOIN vnt_node_credentials credential ON credential.node_id = node.id
		WHERE node.id = $1
	`, nodeID).Scan(&state, &revoked); err != nil {
		t.Fatal(err)
	}
	if state != StateRetired || !revoked {
		t.Fatalf("node lifecycle = state %q, credential revoked %v", state, revoked)
	}
}

func vntErrorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := errorDetails(err)
	return code
}
