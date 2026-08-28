package p2proom

import (
	"context"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
)

type rejectVNTLimit struct{}

func (rejectVNTLimit) Check(context.Context, vnt.LimitOperation, string) vnt.LimitDecision {
	return vnt.LimitDecision{RetryAfter: 1500 * time.Millisecond}
}

type deniedEntitlementChecker struct{}

func (deniedEntitlementChecker) Has(context.Context, string, string) (bool, error) { return false, nil }

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

func TestMemberAttachStatePolicyKeepsOnlyLiveRoomsJoinable(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   State
		allowed bool
	}{
		{name: "lobby", state: StateLobby, allowed: true},
		{name: "connecting", state: StateConnecting, allowed: true},
		{name: "running", state: StateRunning, allowed: true},
		{name: "stale", state: StateStale, allowed: false},
		{name: "closed", state: StateClosed, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowsMemberAttach(Room{State: test.state}); got != test.allowed {
				t.Fatalf("allowsMemberAttach() = %v, want %v", got, test.allowed)
			}
		})
	}
}
