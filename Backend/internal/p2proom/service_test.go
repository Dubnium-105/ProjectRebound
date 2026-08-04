package p2proom

import (
	"context"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

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
