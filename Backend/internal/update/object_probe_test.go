package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyReleaseObjectsProbesEveryObject(t *testing.T) {
	requested := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		requested <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := &Service{managedReleaseProbeBaseURL: server.URL + "/project-rebound-downloads"}
	err := service.VerifyReleaseObjects(context.Background(), SourceRelease{Files: []SourceFile{
		{Path: "bin/game.exe", ObjectKey: "downloads/game.exe"},
		{Path: "bin/data.pack", ObjectKey: "downloads/data.pack"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	close(requested)
	seen := map[string]bool{}
	for path := range requested {
		seen[path] = true
	}
	if !seen["/project-rebound-downloads/downloads/game.exe"] ||
		!seen["/project-rebound-downloads/downloads/data.pack"] {
		t.Fatalf("probed paths = %#v", seen)
	}
}

func TestVerifyReleaseObjectsRejectsUnavailableObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	service := &Service{managedReleaseProbeBaseURL: server.URL}
	err := service.VerifyReleaseObjects(context.Background(), SourceRelease{Files: []SourceFile{
		{Path: "missing.pack", ObjectKey: "missing.pack"},
	}})
	if err == nil || !strings.Contains(err.Error(), "HTTP status 404") {
		t.Fatalf("error = %v, want HTTP status 404", err)
	}
}

func TestVerifyReleaseObjectsRejectsCrossOriginRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cross-origin redirect target must not be requested")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusFound)
	}))
	defer source.Close()

	service := &Service{managedReleaseProbeBaseURL: source.URL}
	err := service.VerifyReleaseObjects(context.Background(), SourceRelease{Files: []SourceFile{
		{Path: "redirect.pack", ObjectKey: "redirect.pack"},
	}})
	if err == nil || !strings.Contains(err.Error(), "cross-origin redirect rejected") {
		t.Fatalf("error = %v, want cross-origin rejection", err)
	}
}
