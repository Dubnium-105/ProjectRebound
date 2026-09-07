package metaserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetiredMetaHTTPPathsCannotUseDatabaseOrDecodeSuccess(t *testing.T) {
	// Nil services/repositories intentionally prove these paths reject before
	// any database access, including malformed old requests and old IDs.
	handler := &HTTPHandler{}
	adminHandler := &MetaAdminHandler{}
	for name, method := range map[string]http.HandlerFunc{
		"create":       handler.CreateMatchTicket,
		"poll":         handler.GetMatchTicket,
		"cancel":       handler.CancelMatchTicket,
		"connected":    handler.InternalConnected,
		"completed":    handler.InternalCompleted,
		"admin-cancel": adminHandler.CancelMatch,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/retired", strings.NewReader("invalid old payload"))
			method(response, request)
			if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "META_MATCHMAKING_RETIRED") {
				t.Fatalf("retired operation %s returned %d: %s", name, response.Code, response.Body.String())
			}
		})
	}
	if _, err := (&Service{}).CreateMatchTicket(context.Background(), "player", "", "", "", ""); metaErrorCode(err) != "META_MATCHMAKING_RETIRED" {
		t.Fatalf("service entry must also reject before accessing repository: %v", err)
	}
}

func TestRetiredNativeMatchmakingRPCsReturnNonzeroFailure(t *testing.T) {
	server := &TCPServer{metrics: NewMetaMetrics()}
	for _, path := range []string{
		"/matchmaking.Matchmaking/StartUnityMatchmaking",
		"/matchmaking.Matchmaking/QueryUnityMatchmaking",
		"/matchmaking.Matchmaking/StopUnityMatchmaking",
	} {
		request := RequestWrapper{MessageID: 42, RPCPath: path}
		response := server.dispatch(context.Background(), GateSession{PlayerID: "fixture"}, request)
		if response.MessageID != request.MessageID || response.RPCPath != path || response.ErrorCode == 0 || len(response.Message) == 0 {
			t.Fatalf("retired native RPC reported success or lost correlation: %#v", response)
		}
	}
}
