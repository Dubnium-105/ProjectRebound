package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/projectrebound/matchserver/internal/requestctx"
)

func TestWriteDataUsesEnvelope(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(requestctx.WithRequestID(req.Context(), "req_test"))
	recorder := httptest.NewRecorder()

	WriteData(recorder, req, 200, map[string]bool{"ok": true})

	var body SuccessEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != "req_test" {
		t.Fatalf("request_id = %q", body.RequestID)
	}
}

func TestWriteErrorAlwaysIncludesDetails(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(requestctx.WithRequestID(req.Context(), "req_error"))
	recorder := httptest.NewRecorder()

	WriteError(recorder, req, 400, "INVALID_REQUEST", "Invalid request.", nil)

	var body ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Details == nil || body.RequestID != "req_error" {
		t.Fatalf("unexpected envelope: %#v", body)
	}
}
