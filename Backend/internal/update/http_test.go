package update

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/go-chi/chi/v5"
)

type stubHTTPService struct{}

func (stubHTTPService) Check(_ context.Context, input CheckInput) (CheckResult, error) {
	return CheckResult{Platform: input.Platform, Architecture: input.Architecture, Channel: input.Channel, CurrentVersion: input.Version, LatestVersion: "1.2.0"}, nil
}

func (stubHTTPService) Manifest(_ context.Context, platform, architecture, channel, version string) (Manifest, error) {
	return Manifest{SchemaVersion: 1, Platform: platform, Architecture: architecture, Channel: channel, Version: version, PublishedAt: time.Unix(1, 0).UTC()}, nil
}

func (stubHTTPService) File(_ context.Context, fileID string) (FileDownload, error) {
	return FileDownload{FileID: fileID, DownloadURL: "https://cdn.example.test/file"}, nil
}

func (stubHTTPService) ClientConfig(context.Context) (ClientConfig, error) {
	return ClientConfig{APIVersion: "v1", ProtocolVersion: 1}, nil
}

func TestHTTPRoutesReturnEnvelopedMetadata(t *testing.T) {
	handler := NewHTTPHandler(stubHTTPService{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	router.Get("/v1/updates/check", handler.Check)
	router.Get("/v1/updates/{platform}/{version}/manifest", handler.Manifest)
	router.Get("/v1/updates/files/{file_id}", handler.File)
	router.Get("/v1/client/config", handler.ClientConfig)
	for _, target := range []string{
		"/v1/updates/check?platform=windows&architecture=amd64&channel=stable&current_version=1.0.0",
		"/v1/updates/windows/1.2.0/manifest?architecture=amd64&channel=stable",
		"/v1/updates/files/file_a", "/v1/client/config",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", target, response.Code)
		}
		var envelope api.SuccessEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data == nil {
			t.Fatalf("GET %s response = %s, %v", target, response.Body.String(), err)
		}
	}
}
