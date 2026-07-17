package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsUnknownFieldsAndTrailingObjects(t *testing.T) {
	for _, body := range []string{
		`{"known":"ok","admin":true}`,
		`{"known":"ok"}{"known":"again"}`,
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var value struct {
			Known string `json:"known"`
		}
		if err := DecodeJSON(req, &value); err == nil {
			t.Fatalf("DecodeJSON(%q) returned nil", body)
		}
	}
}
