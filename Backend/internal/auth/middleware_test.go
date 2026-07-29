package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

func TestRequireActiveRejectsBannedPlayer(t *testing.T) {
	principal := &Principal{Player: player.Player{ID: "p_test", AccountStatus: player.AccountStatusBanned}}
	req := httptest.NewRequest("POST", "/v1/p2p-rooms", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalKey, principal))
	recorder := httptest.NewRecorder()
	RequireActive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
}
