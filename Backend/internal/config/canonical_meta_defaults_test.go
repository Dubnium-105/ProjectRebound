package config

import "testing"

func TestCanonicalMetaServerPublicDefaults(t *testing.T) {
	if got, want := Defaults.MetaServer.PublicHTTPBaseURL, "https://meta.project-rebound.space"; got != want {
		t.Fatalf("MetaServer PublicHTTPBaseURL = %q, want %q", got, want)
	}
	if got, want := Defaults.MetaServer.PublicLogicEndpoint, "logic.project-rebound.space:443"; got != want {
		t.Fatalf("MetaServer PublicLogicEndpoint = %q, want %q", got, want)
	}
}
