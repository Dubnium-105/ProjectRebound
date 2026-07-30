package integrity

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
)

type stubAuthenticator struct {
	principal auth.Principal
}

func (s stubAuthenticator) AuthenticateAccess(context.Context, string) (auth.Principal, error) {
	return s.principal, nil
}

func TestChallengeAndProofHTTP(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	recorder := &recordingRecorder{}
	service := newTestService(t, recorder, now)
	principal := testPrincipal("ses_test")
	ticket := []byte{0xde, 0xad}
	initial, err := service.RegisterSession(principal.SessionID, ticket, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHTTPHandler(service, logger, false)
	middleware := auth.RequireAccess(stubAuthenticator{principal: principal}, logger)

	challengeRecorder := httptest.NewRecorder()
	challengeRequest := httptest.NewRequest(http.MethodPost, "/v1/integrity/challenge", nil)
	challengeRequest.Header.Set("Authorization", "Bearer test")
	middleware(http.HandlerFunc(handler.Challenge)).ServeHTTP(challengeRecorder, challengeRequest)
	if challengeRecorder.Code != http.StatusOK ||
		!strings.Contains(challengeRecorder.Body.String(), `"nonce":"`) ||
		strings.Contains(challengeRecorder.Body.String(), "challenge_id") {
		t.Fatalf("challenge response: status=%d body=%s", challengeRecorder.Code, challengeRecorder.Body.String())
	}

	proof := expectedProof([]byte(testPublicKey), ticket, initial.Nonce)
	proofRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/integrity/proof",
		strings.NewReader(`{"nonce":"`+initial.Nonce+`","proof":"`+proof+`","component":"toolbox"}`),
	)
	proofRequest.Header.Set("Authorization", "Bearer test")
	proofRecorder := httptest.NewRecorder()
	middleware(http.HandlerFunc(handler.Proof)).ServeHTTP(proofRecorder, proofRequest)
	if proofRecorder.Code != http.StatusOK ||
		!strings.Contains(proofRecorder.Body.String(), `"ok":false`) {
		t.Fatalf("replaced nonce response: status=%d body=%s", proofRecorder.Code, proofRecorder.Body.String())
	}

	fresh, err := service.Challenge(principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	proof = expectedProof([]byte(testPublicKey), ticket, fresh.Nonce)
	proofRequest = httptest.NewRequest(
		http.MethodPost,
		"/v1/integrity/proof",
		strings.NewReader(`{"nonce":"`+fresh.Nonce+`","proof":"`+proof+`","component":"toolbox"}`),
	)
	proofRequest.Header.Set("Authorization", "Bearer test")
	proofRecorder = httptest.NewRecorder()
	middleware(http.HandlerFunc(handler.Proof)).ServeHTTP(proofRecorder, proofRequest)
	if proofRecorder.Code != http.StatusOK ||
		!strings.Contains(proofRecorder.Body.String(), `"ok":true`) {
		t.Fatalf("proof response: status=%d body=%s", proofRecorder.Code, proofRecorder.Body.String())
	}
}
