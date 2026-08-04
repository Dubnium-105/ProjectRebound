package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdministratorDrainsAndRevokesVNTNodeAgainstPostgreSQL(t *testing.T) {
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

	now := time.Now().UTC().Truncate(time.Second)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	playerID := "ply_vnt_admin_" + suffix[:24]
	nodeID := "vnt_" + suffix[:32]
	roomID := "room_vnt_admin_" + suffix[:24]
	credentialID := "vnc_" + suffix[:32]
	steamID := fmt.Sprintf("%017d", uint64(now.UnixNano())%100_000_000_000_000_000)

	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, created_at, updated_at
		) VALUES ($1,$2,'VNT Admin Owner','ACTIVE',FALSE,'steam_ticket','verified',$3,$3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_nodes (
			id, owner_player_id, advertised_host, port, region, location, state,
			vnts_version, wrapper_version, server_key_fingerprint,
			supported_transports, max_rooms, reported_sessions,
			last_heartbeat_at, last_reachable_at, created_at, updated_at
		) VALUES ($1,$2,'203.0.113.40',34000,'admin-test','Test','ONLINE',
		          '1.0.0','0.1.0',$3,ARRAY['tcp','udp'],10,1,$4,$4,$4,$4)
	`, nodeID, playerID, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5)
	`, credentialID, nodeID, []byte("vnt-admin-secret-hash-"+suffix), now.Add(24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode, version,
			max_players, player_count, state, last_heartbeat_at, created_at, updated_at,
			transport_kind, expires_at
		) VALUES ($1,$2,$3,'VNT Admin Room','hk','coop','1.0.0',4,1,'LOBBY',$4,$4,$4,'VNT',$5)
	`, roomID, playerID, []byte("vnt-admin-host-hash-"+suffix), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
		VALUES ($1,$2,'HOST','ACTIVE',$3)
	`, roomID, playerID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_vnt_sessions (
			room_id, node_id, generation, state, node_host_snapshot, node_port_snapshot,
			node_region_snapshot, node_location_snapshot, node_fingerprint_snapshot,
			node_transports_snapshot, network_token_ciphertext, e2e_password_ciphertext,
			secret_key_id, network_token_nonce, e2e_password_nonce, created_at, updated_at
		) VALUES ($1,$2,1,'HOST_READY','203.0.113.40',34000,'admin-test','Test',$3,
		          ARRAY['tcp','udp'],$4,$5,'test-key',$6,$7,$8,$8)
	`, roomID, nodeID, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		[]byte("network"), []byte("password"), []byte("nonce-one"), []byte("nonce-two"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_vnt_member_sessions (
			room_id, generation, player_id, device_id, virtual_ip, state, created_at
		) VALUES ($1,1,$2,$3,'10.26.0.2','CONNECTED',$4)
	`, roomID, playerID, "vnd_"+suffix[:32], now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE node_id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_audit_logs WHERE target_id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE id = $1", roomID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_node_credentials WHERE node_id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", playerID)
	})

	service := NewOnlineService(
		pool, NewRepository(), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	service.now = func() time.Time { return now }
	service.SetVNT(vnt.NewRepository(pool), vnt.NewVersionPolicy([]string{"1.0.0"}, []string{"0.1.0"}))
	meta := RequestMeta{AdminID: "integration-operator", RequestID: "req_vnt_admin", IPAddress: "192.0.2.60"}

	drained, err := service.ChangeVNTNodeState(ctx, nodeID, "drain", "Drain for maintenance", meta)
	if err != nil {
		t.Fatal(err)
	}
	if drained.Node.State != vnt.StateDraining || drained.Node.ActiveRooms != 1 || drained.ClosedRooms != 0 {
		t.Fatalf("drain result = %#v", drained)
	}
	if _, err := pool.Exec(ctx, "UPDATE vnt_nodes SET state = 'ONLINE' WHERE id = $1", nodeID); err != nil {
		t.Fatal(err)
	}

	revoked, err := service.ChangeVNTNodeState(ctx, nodeID, "revoke", "Revoke compromised node", meta)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Node.State != vnt.StateRevoked || revoked.ClosedRooms != 1 || !revoked.Node.VersionCompatible {
		t.Fatalf("revoke result = %#v", revoked)
	}
	var roomState, roomMemberStatus, sessionState, sessionFailure, vntMemberState string
	var credentialRevoked bool
	if err := pool.QueryRow(ctx, `
		SELECT room.state, member.status, session.state, session.failure_reason,
		       vnt_member.state, credential.revoked_at IS NOT NULL
		FROM p2p_rooms room
		JOIN p2p_room_members member ON member.room_id = room.id
		JOIN p2p_vnt_sessions session ON session.room_id = room.id
		JOIN p2p_vnt_member_sessions vnt_member ON vnt_member.room_id = room.id
		JOIN vnt_node_credentials credential ON credential.node_id = session.node_id
		WHERE room.id = $1
	`, roomID).Scan(&roomState, &roomMemberStatus, &sessionState, &sessionFailure, &vntMemberState, &credentialRevoked); err != nil {
		t.Fatal(err)
	}
	if roomState != "CLOSED" || roomMemberStatus != "LEFT" || sessionState != "FAILED" ||
		sessionFailure != "NODE_REVOKED" || vntMemberState != "FAILED" || !credentialRevoked {
		t.Fatalf("revoked state = %s %s %s %s %s %v", roomState, roomMemberStatus, sessionState, sessionFailure, vntMemberState, credentialRevoked)
	}
	if len(revoked.Node.ReferencedRooms) != 1 || revoked.Node.ReferencedRooms[0].RoomID != roomID {
		t.Fatalf("referenced rooms = %#v", revoked.Node.ReferencedRooms)
	}
	listed, err := service.ListVNTNodes(ctx, vnt.AdminListFilter{State: vnt.StateRevoked, OwnerPlayerID: playerID, Limit: 10})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != nodeID {
		t.Fatalf("listed revoked nodes = %#v, %v", listed, err)
	}
	var audits int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM admin_audit_logs
		WHERE target_id = $1 AND action IN ('VNT_NODE_DRAINED','VNT_NODE_REVOKED')
	`, nodeID).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count = %d, %v", audits, err)
	}
	var securityAudits int
	var recordedAdminID, details string
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*), MIN(admin_id), COALESCE(string_agg(details::text, ' '), '')
		FROM vnt_security_audit_logs
		WHERE node_id = $1 AND event_type IN ('VNT_NODE_DRAINED','VNT_NODE_REVOKED')
	`, nodeID).Scan(&securityAudits, &recordedAdminID, &details); err != nil {
		t.Fatal(err)
	}
	if securityAudits != 2 || recordedAdminID != meta.AdminID ||
		!strings.Contains(details, "Drain for maintenance") || !strings.Contains(details, "Revoke compromised node") {
		t.Fatalf("VNT security audit = count %d admin %q details %q", securityAudits, recordedAdminID, details)
	}
}
