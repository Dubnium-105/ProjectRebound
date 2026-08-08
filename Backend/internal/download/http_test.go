package download

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type publicServiceStub struct {
	catalog Catalog
	target  string
	err     error
}

func (s publicServiceStub) Catalog(context.Context) (Catalog, error) { return s.catalog, s.err }
func (s publicServiceStub) DownloadURL(context.Context, string) (string, error) {
	return s.target, s.err
}

func TestPublicCatalogETagAndNotModified(t *testing.T) {
	updated := time.Date(2026, time.August, 8, 12, 0, 0, 123, time.UTC)
	handler := NewHTTPHandler(publicServiceStub{catalog: Catalog{
		UpdatedAt:  updated,
		Categories: []PublicCategory{{ID: "dcat_test", Slug: "documents", Enabled: true}},
		Items: []PublicEntry{{ID: "dent_test", Slug: "manual",
			Versions: []PublicVersion{{ID: "dver_test", Status: VersionStatusPublished}}}},
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	recorder := httptest.NewRecorder()
	handler.Catalog(recorder, httptest.NewRequest(http.MethodGet, "/v1/downloads", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=60, stale-while-revalidate=300" {
		t.Fatalf("Cache-Control = %q", got)
	}
	etag := recorder.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is empty")
	}
	var envelope struct {
		Data struct {
			Categories []PublicCategory `json:"categories"`
			Items      []PublicEntry    `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Categories) != 1 || len(envelope.Data.Items) != 1 {
		t.Fatalf("unexpected catalog: %#v", envelope.Data)
	}
	for _, forbidden := range []string{"created_by", "archived_by", "object_key", "failure_reason"} {
		if bytes.Contains(recorder.Body.Bytes(), []byte(forbidden)) {
			t.Fatalf("public catalog contains administrative field %q: %s", forbidden, recorder.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/downloads", nil)
	request.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	handler.Catalog(notModified, request)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional response = %d, %q", notModified.Code, notModified.Body.String())
	}
}

func TestPublicFileRedirectAndHiddenErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHTTPHandler(publicServiceStub{target: "https://cdn.example/downloads/file.zip"}, logger)
	request := httptest.NewRequest(http.MethodGet, "/v1/downloads/files/dver_test", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("version_id", "dver_test")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.File(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "https://cdn.example/downloads/file.zip" {
		t.Fatalf("redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("redirect cache policy = %q", recorder.Header().Get("Cache-Control"))
	}

	handler = NewHTTPHandler(publicServiceStub{err: notFound("Published download not found.")}, logger)
	recorder = httptest.NewRecorder()
	handler.File(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("hidden version status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
