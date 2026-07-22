package openapi_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

func TestDocumentValidatesAgainstOpenAPISchema(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
}

func TestReferenceDocsCoverEveryHTTPPath(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	external, err := os.ReadFile("../../../docs/api/external.md")
	if err != nil {
		t.Fatal(err)
	}
	internal, err := os.ReadFile("../../../docs/api/internal.md")
	if err != nil {
		t.Fatal(err)
	}
	for path := range document.Paths {
		target := external
		if strings.HasPrefix(path, "/internal/") || strings.HasPrefix(path, "/v1/admin/") {
			target = internal
		}
		if !strings.Contains(string(target), path) {
			t.Errorf("reference documentation does not cover %s", path)
		}
	}
}

func TestDocumentHasRequiredOpenAPIShape(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI    string                    `yaml:"openapi"`
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if document.OpenAPI == "" {
		t.Fatal("openapi version is missing")
	}
	for _, path := range []string{
		"/v1/auth/bind",
		"/v1/auth/refresh",
		"/v1/auth/logout",
		"/v1/users/me",
		"/v1/users/me/sessions",
		"/v1/users/me/sessions/{session_id}",
		"/v1/users/me/sessions/revoke-others",
		"/v1/admin/players",
		"/v1/admin/players/{player_id}",
		"/v1/admin/players/{player_id}/revoke-sessions",
		"/v1/admin/invite-codes",
		"/v1/admin/invite-codes/{id}",
		"/v1/admin/invite-codes/{id}/revoke",
		"/v1/admin/auth/risk-events",
		"/v1/game-servers",
		"/v1/game-servers/{server_id}",
		"/v1/game-servers/{server_id}/heartbeat",
		"/v1/p2p-rooms",
		"/v1/p2p-rooms/{room_id}",
		"/v1/p2p-rooms/{room_id}/join",
		"/v1/p2p-rooms/{room_id}/leave",
		"/v1/p2p-rooms/{room_id}/heartbeat",
		"/v1/p2p-rooms/{room_id}/start",
		"/v1/connections",
		"/v1/connections/{connection_id}",
		"/v1/realtime/connect",
		"/internal/v1/relay-nodes/enroll",
		"/internal/v1/relay-nodes",
		"/internal/v1/relay-nodes/{node_id}/certificate/renew",
		"/internal/v1/relay-nodes/{node_id}",
		"/internal/v1/relay-nodes/{node_id}/drain",
		"/internal/v1/relay-nodes/{node_id}/resume",
		"/internal/v1/relay-nodes/{node_id}/revoke",
		"/v1/updates/check",
		"/v1/updates/{platform}/{version}/manifest",
		"/v1/updates/files/{file_id}",
		"/v1/client/config",
		"/internal/metrics",
		"/health/live",
		"/health/ready",
	} {
		if _, ok := document.Paths[path]; !ok {
			t.Errorf("required path %s is missing", path)
		}
	}
	for _, schema := range []string{"BindRequest", "BindResponse", "RefreshRequest", "MeResponse", "UserSession", "UserSessionListResponse", "AdminPlayerPatch", "AdminPlayerListResponse", "InviteCode", "InviteCodeCreateRequest", "InviteCodePatchRequest", "InviteCodeListResponse", "AuthRiskEvent", "AuthRiskEventListResponse", "GameServerRegistrationRequest", "GameServerListResponse", "P2PRoomCreateRequest", "PublicP2PRoom", "P2PRoomListResponse", "ConnectionCreateRequest", "ConnectionData", "ConnectionRealtimeEvent", "ConnectionRelayAllocatedEvent", "ConnectionRelayMigratingEvent", "ConnectionRelayMigratedEvent", "RelayEnrollRequest", "RelayNode", "RelayNodeListResponse", "RelayTokenClaims", "UpdateCheckResponse", "SignedUpdateManifest", "UpdateFileResponse", "ClientConfigResponse", "HealthSuccess", "Error", "ErrorResponse"} {
		if _, ok := document.Components.Schemas[schema]; !ok {
			t.Errorf("required schema %s is missing", schema)
		}
	}
}
