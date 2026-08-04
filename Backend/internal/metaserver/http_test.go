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

func TestCompatibilityBodyIsOpaque(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty"},
		{name: "legacy field names and types", body: []byte(`{
			"loginToken":{"release":"legacy"},
			"playerId":76561198000000000,
			"version":42,
			"protocol_version":"1",
			"platform":false
		}`)},
		{name: "UTF-8 BOM", body: append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"legacy":true}`)...)},
		{name: "UTF-16 little endian", body: []byte{0xff, 0xfe, 0x7b, 0x00, 0x7d, 0x00}},
		{name: "UTF-16 big endian", body: []byte{0xfe, 0xff, 0x00, 0x7b, 0x00, 0x7d}},
		{name: "non JSON legacy payload", body: []byte{0x00, 0x01, 0x02, 0xff}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/connectServer", strings.NewReader(string(test.body)))
			if err := discardCompatibilityBody(request); err != nil {
				t.Fatalf("discard compatibility body: %v", err)
			}
		})
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
