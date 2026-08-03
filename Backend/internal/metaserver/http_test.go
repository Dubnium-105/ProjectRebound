package metaserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionRequestNormalize(t *testing.T) {
	tests := []struct {
		name                  string
		input                 sessionRequest
		clientVersionFallback string
		wantClientVersion     string
		wantProtocolVersion   int
	}{
		{
			name: "explicit client version",
			input: sessionRequest{
				ClientVersion: " 1.1.0 ", ProtocolVersion: 1,
			},
			clientVersionFallback: boundaryLegacyClientVersion,
			wantClientVersion:     "1.1.0",
			wantProtocolVersion:   1,
		},
		{
			name:                  "compatibility alias",
			input:                 sessionRequest{Version: " release-42 "},
			clientVersionFallback: boundaryLegacyClientVersion,
			wantClientVersion:     "release-42",
			wantProtocolVersion:   7,
		},
		{
			name:                  "shipped Boundary client without version",
			input:                 sessionRequest{},
			clientVersionFallback: boundaryLegacyClientVersion,
			wantClientVersion:     boundaryLegacyClientVersion,
			wantProtocolVersion:   7,
		},
		{
			name:                "modern session remains strict",
			input:               sessionRequest{},
			wantClientVersion:   "",
			wantProtocolVersion: 7,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.input.normalize(7, test.clientVersionFallback)
			if test.input.ClientVersion != test.wantClientVersion {
				t.Fatalf("client version = %q, want %q", test.input.ClientVersion, test.wantClientVersion)
			}
			if test.input.ProtocolVersion != test.wantProtocolVersion {
				t.Fatalf("protocol version = %d, want %d", test.input.ProtocolVersion, test.wantProtocolVersion)
			}
		})
	}
}

func TestCompatibilityJSONAllowsUnknownLegacyFields(t *testing.T) {
	request := httptest.NewRequest(
		"POST",
		"/connectServer",
		strings.NewReader(`{
			"loginToken":"legacy-token",
			"playerId":"legacy-player",
			"version":"",
			"legacyBuild":42,
			"legacyMetadata":{"channel":"steam"}
		}`),
	)
	var input sessionRequest
	if err := decodeCompatibilityJSON(request, &input); err != nil {
		t.Fatalf("decode compatibility request: %v", err)
	}
	input.normalize(7, boundaryLegacyClientVersion)
	if input.ClientVersion != boundaryLegacyClientVersion || input.ProtocolVersion != 7 {
		t.Fatalf("normalized compatibility request = %#v", input)
	}
}

func TestStrictJSONRejectsUnknownFields(t *testing.T) {
	request := httptest.NewRequest(
		"POST",
		"/v1/meta/sessions",
		strings.NewReader(`{"client_version":"1.1.0","legacyBuild":42}`),
	)
	var input sessionRequest
	if err := decodeJSON(request, &input); err == nil {
		t.Fatal("strict decoder accepted an unknown field")
	}
}
