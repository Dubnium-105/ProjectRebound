package matchlobby

import (
	"context"
	"testing"
)

func assertMemberConnectionEvidence(t *testing.T, ctx context.Context, service *Service, actor Actor, attemptID string, want MemberConnectionEvidence) {
	t.Helper()
	got, err := service.CurrentMemberConnection(ctx, actor, attemptID)
	if err != nil {
		t.Fatalf("current member connection for %s: %v", actor.PlayerID, err)
	}
	if got != want {
		t.Fatalf("current member connection = %+v, want %+v", got, want)
	}
}
