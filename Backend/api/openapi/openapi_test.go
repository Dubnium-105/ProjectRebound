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
		"/v1/diagnostic/report",
		"/v1/users/me",
		"/v1/users/me/sessions",
		"/v1/users/me/sessions/{session_id}",
		"/v1/users/me/sessions/revoke-others",
		"/v1/admin/players",
		"/v1/admin/players/{player_id}",
		"/v1/admin/players/{player_id}/revoke-sessions",
		"/v1/admin/players/{player_id}/sessions",
		"/v1/admin/players/{player_id}/risk-events",
		"/v1/admin/players/{player_id}/login-events",
		"/v1/admin/auth/config",
		"/v1/admin/auth/login",
		"/v1/admin/auth/mfa/verify",
		"/v1/admin/auth/refresh",
		"/v1/admin/auth/step-up",
		"/v1/admin/auth/logout",
		"/v1/admin/auth/me",
		"/v1/admin/auth/sessions",
		"/v1/admin/auth/sessions/{session_id}",
		"/v1/admin/invite-codes",
		"/v1/admin/invite-codes/{id}",
		"/v1/admin/invite-codes/{id}/revoke",
		"/v1/admin/invite-codes/{id}/uses",
		"/v1/admin/dashboard/summary",
		"/v1/admin/dashboard/timeseries",
		"/v1/admin/dashboard/alerts",
		"/v1/admin/risk-events",
		"/v1/admin/risk-events/{event_id}",
		"/v1/admin/risk-events/{event_id}/resolve",
		"/v1/admin/audit-logs",
		"/v1/admin/audit-logs/{audit_id}",
		"/v1/admin/login-audit",
		"/v1/admin/admins",
		"/v1/admin/admins/{admin_id}",
		"/v1/admin/admins/{admin_id}/reset-mfa",
		"/v1/admin/roles",
		"/v1/admin/roles/{role_id}",
		"/v1/admin/features",
		"/v1/admin/capabilities",
		"/v1/admin/settings",
		"/v1/admin/p2p-rooms",
		"/v1/admin/p2p-rooms/{room_id}",
		"/v1/admin/p2p-rooms/{room_id}/close",
		"/v1/admin/p2p-rooms/{room_id}/members",
		"/v1/admin/p2p-rooms/{room_id}/members/{player_id}/remove",
		"/v1/admin/p2p-battlelog/matches/{match_id}",
		"/v1/admin/p2p-battlelog/reports/{evidence_id}/raw",
		"/v1/admin/game-servers",
		"/v1/admin/game-servers/registration-tokens",
		"/v1/admin/game-servers/{server_id}",
		"/v1/admin/game-servers/{server_id}/drain",
		"/v1/admin/game-servers/{server_id}/resume",
		"/v1/admin/game-servers/{server_id}/disable",
		"/v1/admin/connections",
		"/v1/admin/connections/{connection_id}",
		"/v1/admin/connections/{connection_id}/close",
		"/v1/admin/connections/{connection_id}/migrate-relay",
		"/v1/admin/relay-nodes",
		"/v1/admin/relay-nodes/{node_id}",
		"/v1/admin/relay-nodes/{node_id}/drain",
		"/v1/admin/relay-nodes/{node_id}/resume",
		"/v1/admin/relay-nodes/{node_id}/revoke",
		"/v1/admin/releases",
		"/v1/admin/releases/{release_id}",
		"/v1/admin/releases/{release_id}/validate",
		"/v1/admin/releases/{release_id}/publish",
		"/v1/admin/releases/{release_id}/rollback",
		"/v1/admin/releases/{release_id}/archive",
		"/v1/game-server-registration-tokens",
		"/v1/game-servers",
		"/v1/game-servers/{server_id}",
		"/v1/game-servers/{server_id}/heartbeat",
		"/v1/game-servers/{server_id}/credential/rotate",
		"/v1/vnt/node-enrollments",
		"/v1/vnt/nodes",
		"/v1/vnt/nodes/{node_id}",
		"/v1/vnt/nodes/{node_id}/heartbeat",
		"/v1/vnt/nodes/{node_id}/credential/rotate",
		"/v1/p2p-rooms",
		"/v1/p2p-rooms/{room_id}",
		"/v1/p2p-rooms/{room_id}/join",
		"/v1/p2p-rooms/{room_id}/leave",
		"/v1/p2p-rooms/{room_id}/heartbeat",
		"/v1/p2p-rooms/{room_id}/start",
		"/v1/p2p-rooms/{room_id}/vnt/bootstrap",
		"/v1/p2p-rooms/{room_id}/vnt/presence/me",
		"/v1/p2p-rooms/{room_id}/vnt/host-ready",
		"/v1/p2p-rooms/{room_id}/vnt/rebind",
		"/v1/p2p-rooms/{room_id}/matches/active",
		"/v1/p2p-matches/{match_id}/report-capability",
		"/v1/p2p-matches/{match_id}/presence/me",
		"/v1/p2p-matches/{match_id}/reports/{report_id}",
		"/v1/p2p-matches/{match_id}/result",
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
		"/internal/v1/meta/battlelog/reports/{report_id}",
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
	for _, schema := range []string{"BindRequest", "BindResponse", "RefreshRequest", "DiagnosticReportRequest", "DiagnosticReportResponse", "MeResponse", "UserSession", "UserSessionListResponse", "AdminAuthConfigResponse", "AdminLoginRequest", "AdminLoginResponse", "AdminMFAVerifyRequest", "AdminStepUpRequest", "AdminStepUpResponse", "CurrentAdmin", "AdminAccessResponse", "AdminSession", "AdminSessionListResponse", "GovernedAdministrator", "GovernedAdministratorCreateRequest", "GovernedAdministratorUpdateRequest", "GovernedAdministratorProvisioningResponse", "AdminPermissionDefinition", "GovernedRole", "GovernedRoleUpdateRequest", "GovernedRoleListResponse", "AdminFeatureMap", "AdminCapabilities", "AdminSetting", "AdminSettingsUpdateRequest", "AdminPlayerPatch", "AdminPlayerListResponse", "AdminPlayerSession", "AdminPlayerLoginEvent", "InviteCode", "InviteCodeCreateRequest", "InviteCodePatchRequest", "InviteCodeListResponse", "InviteCodeUse", "InviteCodeUseListResponse", "AdminDashboardSummary", "AdminP2PRoom", "AdminP2PRoomMember", "AdminP2PRoomMemberListResponse", "AdminGameServer", "AdminGameServerListResponse", "AdminGameServerRegistrationTokenCreateRequest", "AdminGameServerRegistrationToken", "AdminGameServerRegistrationTokenCreateResponse", "AdminConnection", "AdminRelayMigration", "AdminConnectionListResponse", "AdminConnectionMigrationResponse", "AdminRelease", "AdminReleaseCreateRequest", "AdminReleaseValidation", "AdminReleaseListResponse", "AdminRelayDrainRequest", "AdminDashboardPoint", "AdminDashboardAlert", "AuthRiskEvent", "AuthRiskEventListResponse", "AdminAuditLog", "AdminLoginAudit", "GameServerRegistrationTokenRequest", "GameServerRegistrationTokenResponse", "GameServerRegistrationRequest", "GameServerCredentialRotationRequest", "GameServerCredentialRotationResponse", "GameServerListResponse", "P2PRoomCreateRequest", "PublicP2PRoom", "P2PRoomListResponse", "P2PActiveMatch", "P2PReportCapability", "P2PPresenceRequest", "P2PBattleLogRawV3", "P2PBattleLogSubmission", "P2PBattleLogResultResponse", "ConnectionCreateRequest", "ConnectionData", "ConnectionRealtimeEvent", "ConnectionRelayAllocatedEvent", "ConnectionRelayMigratingEvent", "ConnectionRelayMigratedEvent", "RelayEnrollRequest", "RelayNode", "RelayNodeListResponse", "RelayTokenClaims", "BattleLogSubmitRequest", "BattleLogSubmission", "BattleLogSubmitResponse", "UpdateCheckResponse", "SignedUpdateManifest", "UpdateFileResponse", "ClientConfigResponse", "HealthSuccess", "Error", "ErrorResponse"} {
		if _, ok := document.Components.Schemas[schema]; !ok {
			t.Errorf("required schema %s is missing", schema)
		}
	}
}
