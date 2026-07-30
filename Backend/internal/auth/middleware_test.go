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

func TestRequireVerifiedUsesSessionAuthenticationLevel(t *testing.T) {
	handler := RequireVerified(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name      string
		principal *Principal
		status    int
	}{
		{
			name: "verified",
			principal: &Principal{
				AuthLevel: player.AuthLevelVerified, SteamVerified: true,
			},
			status: http.StatusNoContent,
		},
		{
			name: "unverified player with globally verified profile",
			principal: &Principal{
				Player:    player.Player{AuthLevel: player.AuthLevelVerified},
				AuthLevel: player.AuthLevelUnverified, SteamVerified: false,
			},
			status: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/p2p-rooms", nil)
			request = request.WithContext(context.WithValue(request.Context(), principalKey, test.principal))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}
