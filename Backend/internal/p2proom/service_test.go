package p2proom

import (
	"context"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/jackc/pgx/v5"
)

type rejectVNTLimit struct{}

func (rejectVNTLimit) Check(context.Context, vnt.LimitOperation, string) vnt.LimitDecision {
	return vnt.LimitDecision{RetryAfter: 1500 * time.Millisecond}
}

type deniedEntitlementChecker struct{}

func (deniedEntitlementChecker) Has(context.Context, string, string) (bool, error) { return false, nil }

type recordingConnectionCreator struct {
	created int
}

func (r *recordingConnectionCreator) EnsureForRoomPeer(context.Context, string, string, string) error {
	r.created++
	return nil
}

func (*recordingConnectionCreator) CloseForRoom(context.Context, string, string) error { return nil }

func (*recordingConnectionCreator) RenewForRoom(context.Context, pgx.Tx, string, time.Time) error {
	return nil
}

func TestRequireActiveRejectsBannedActor(t *testing.T) {
	err := requireActive(Actor{PlayerID: "p_test", AccountStatus: player.AccountStatusBanned})
	status, code, _, _ := errorDetails(err)
	if status != 403 || code != "ACCOUNT_NOT_ACTIVE" {
		t.Fatalf("error = %d %s %v", status, code, err)
	}
}

func TestCreateRequiresP2PRoomRegistrationCapability(t *testing.T) {
	service := NewService(nil, config.Defaults.P2PRoom)
	service.SetEntitlementChecker(deniedEntitlementChecker{})
	_, err := service.Create(context.Background(), Actor{
		PlayerID: "p_test", AccountStatus: player.AccountStatusActive,
	}, CreateInput{})
	status, code, _, _ := errorDetails(err)
	if status != 403 || code != "P2P_ROOM_REGISTRATION_NOT_ALLOWED" {
		t.Fatalf("error = %d %s %v", status, code, err)
	}
}

func TestCreateManagedRequiresAnAuthoritativeLobbyID(t *testing.T) {
	service := NewService(nil, config.Defaults.P2PRoom)
	service.SetEntitlementChecker(deniedEntitlementChecker{})
	_, err := service.CreateManaged(context.Background(), Actor{
		PlayerID: "p_test", AccountStatus: player.AccountStatusActive,
	}, CreateInput{})
	status, code, _, _ := errorDetails(err)
	if status != 400 || code != "INVALID_REQUEST" {
		t.Fatalf("error = %d %s %v", status, code, err)
	}
}

func TestCreateManagedBypassesStandaloneRegistrationEntitlement(t *testing.T) {
	service := NewService(nil, config.Defaults.P2PRoom)
	service.SetEntitlementChecker(deniedEntitlementChecker{})
	_, err := service.CreateManaged(context.Background(), Actor{
		PlayerID: "p_test", AccountStatus: player.AccountStatusActive,
	}, CreateInput{
		DisplayName: "Managed room", Region: "hk", Mode: "pvp", Version: "1.0.0",
		MaxPlayers: 4, TransportKind: TransportVNT, VNTNodeID: "vnt_node",
		ManagedLobbyID: "lby_test",
	})
	status, code, _, _ := errorDetails(err)
	if status != 409 || code != "VNT_FEATURE_DISABLED" {
		t.Fatalf("managed create did not bypass standalone entitlement: %d %s %v", status, code, err)
	}
}

func TestPublicCreateCannotRequestAManagedRoom(t *testing.T) {
	service := NewService(nil, config.Defaults.P2PRoom)
	_, err := service.Create(context.Background(), Actor{
		PlayerID: "p_test", AccountStatus: player.AccountStatusActive,
	}, CreateInput{ManagedLobbyID: "lby_test"})
	status, code, _, _ := errorDetails(err)
	if status != 400 || code != "INVALID_REQUEST" {
		t.Fatalf("error = %d %s %v", status, code, err)
	}
}

func TestCreateVNTRequiresServerFeatureGate(t *testing.T) {
	service := NewService(nil, config.Defaults.P2PRoom)
	_, err := service.Create(context.Background(), Actor{
		PlayerID: "player_host", AccountStatus: player.AccountStatusActive,
	}, CreateInput{
		DisplayName: "VNT Room", Region: "hk", Mode: "coop", Version: "1.0.0",
		MaxPlayers: 4, TransportKind: TransportVNT, VNTNodeID: "vnt_node",
	})
	status, code, _, _ := errorDetails(err)
	if status != 409 || code != "VNT_FEATURE_DISABLED" {
		t.Fatalf("disabled VNT create error = %d %s %v", status, code, err)
	}
}

func TestVNTBootstrapRateLimitRunsBeforeRepositoryAccess(t *testing.T) {
	service := NewService(nil, config.Defaults.P2PRoom)
	service.SetVNTLimiter(rejectVNTLimit{})
	_, err := service.VNTBootstrap(t.Context(), Actor{
		PlayerID: "player_host", AccountStatus: player.AccountStatusActive,
	}, "room_test")
	status, code, _, details := errorDetails(err)
	if status != 429 || code != "VNT_RATE_LIMITED" || details["operation"] != vnt.LimitBootstrap || details["retry_after_seconds"] != 2 {
		t.Fatalf("bootstrap rate-limit error = status %d code %q details %#v", status, code, details)
	}
}

func TestMemberAttachStatePolicyKeepsStrictRosterReconnectNarrow(t *testing.T) {
	for _, test := range []struct {
		name    string
		room    Room
		allowed bool
	}{
		{name: "standalone lobby", room: Room{State: StateLobby}, allowed: true},
		{name: "standalone connecting", room: Room{State: StateConnecting}, allowed: true},
		{name: "standalone running", room: Room{State: StateRunning}, allowed: true},
		{name: "managed lobby", room: Room{State: StateLobby, ManagedLobbyID: "lby_test"}, allowed: true},
		{name: "managed connecting", room: Room{State: StateConnecting, ManagedLobbyID: "lby_test"}, allowed: true},
		{name: "managed running", room: Room{State: StateRunning, ManagedLobbyID: "lby_test"}, allowed: true},
		{name: "managed stale", room: Room{State: StateStale, ManagedLobbyID: "lby_test"}, allowed: false},
		{name: "managed closed", room: Room{State: StateClosed, ManagedLobbyID: "lby_test"}, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowsMemberAttach(test.room); got != test.allowed {
				t.Fatalf("allowsMemberAttach() = %v, want %v", got, test.allowed)
			}
		})
	}
}

func TestManagedLegacyConnectionWaitsForFrozenTransportAttach(t *testing.T) {
	recorder := &recordingConnectionCreator{}
	service := NewService(nil, config.Defaults.P2PRoom)
	service.SetConnectionCreator(recorder)
	room := Room{
		ID: "room_test", HostPlayerID: "player_host", TransportKind: TransportLegacy,
		ManagedLobbyID: "lby_test", State: StateLobby,
	}

	if err := service.ensureConnection(t.Context(), room, "player_member"); err != nil {
		t.Fatal(err)
	}
	if recorder.created != 0 {
		t.Fatalf("OPEN managed lobby created %d premature transport connections", recorder.created)
	}

	room.State = StateConnecting
	if err := service.ensureConnection(t.Context(), room, "player_member"); err != nil {
		t.Fatal(err)
	}
	if recorder.created != 1 {
		t.Fatalf("CONNECTING managed lobby created %d transport connections, want 1", recorder.created)
	}
}
