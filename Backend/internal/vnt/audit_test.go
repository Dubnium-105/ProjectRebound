package vnt

import (
	"fmt"
	"strings"
	"testing"
)

func TestRequestMetaSanitizesCredentialShapedValuesAndInvalidIP(t *testing.T) {
	ctx := WithRequestMeta(t.Context(), RequestMeta{
		RequestID: "req_vnn_super_secret_credential",
		IPAddress: "unknown",
		UserAgent: "Supervisor/1 Authorization: Bearer vnn_super_secret_credential",
	})
	meta := RequestMetaFromContext(ctx)
	if meta.IPAddress != "" {
		t.Fatalf("invalid audit IP = %q", meta.IPAddress)
	}
	if strings.Contains(meta.RequestID, "vnn_super_secret_credential") ||
		strings.Contains(meta.UserAgent, "vnn_super_secret_credential") {
		t.Fatalf("request metadata retained a credential: %#v", meta)
	}
}

func TestSecurityAuditDetailsAreRecursivelySanitized(t *testing.T) {
	details := sanitizeAuditDetails(map[string]any{
		"node_token": "vnn_super_secret_credential",
		"reason":     "operator pasted vne_super_secret_enrollment",
		"nested": map[string]any{
			"safe":     "visible",
			"password": "vntw_super_secret_password",
		},
		"items": []any{"Bearer vnn_another_secret", map[string]any{"cookie": "session-secret"}},
	})
	encoded := strings.ToLower(fmt.Sprintf("%v", details))
	for _, forbidden := range []string{"vnn_", "vne_", "vntw_", "session-secret"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("sanitized audit details retained %q: %#v", forbidden, details)
		}
	}
	if details["nested"].(map[string]any)["safe"] != "visible" {
		t.Fatalf("safe audit detail was removed: %#v", details)
	}
}
