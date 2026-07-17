package p2proom

import (
	"testing"

	"github.com/projectrebound/matchserver/internal/player"
)

func TestRequireActiveRejectsBannedActor(t *testing.T) {
	err := requireActive(Actor{PlayerID: "p_test", AccountStatus: player.AccountStatusBanned})
	status, code, _, _ := errorDetails(err)
	if status != 403 || code != "ACCOUNT_NOT_ACTIVE" {
		t.Fatalf("error = %d %s %v", status, code, err)
	}
}
