package openapi_test

import (
	"context"
	"os"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

func TestDocumentValidatesAgainstOpenAPISchema(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
}

func TestDocumentHasRequiredOpenAPIShape(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI    string                    `yaml:"openapi"`
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if document.OpenAPI == "" {
		t.Fatal("openapi version is missing")
	}
	for _, path := range []string{
		"/v1/auth/bind",
		"/v1/auth/refresh",
		"/v1/auth/logout",
		"/v1/users/me",
		"/v1/admin/players",
		"/v1/admin/players/{player_id}",
		"/v1/admin/players/{player_id}/revoke-sessions",
		"/v1/game-servers",
		"/v1/game-servers/{server_id}",
		"/v1/game-servers/{server_id}/heartbeat",
		"/health/live",
		"/health/ready",
	} {
		if _, ok := document.Paths[path]; !ok {
			t.Errorf("required path %s is missing", path)
		}
	}
	for _, schema := range []string{"BindRequest", "BindResponse", "RefreshRequest", "MeResponse", "AdminPlayerPatch", "AdminPlayerListResponse", "GameServerRegistrationRequest", "GameServerListResponse", "HealthSuccess", "Error", "ErrorResponse"} {
		if _, ok := document.Components.Schemas[schema]; !ok {
			t.Errorf("required schema %s is missing", schema)
		}
	}
}
