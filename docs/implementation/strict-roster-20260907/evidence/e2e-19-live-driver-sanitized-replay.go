// SANITIZED_REPLAY_SOURCE=1
// EXECUTION_STATUS=NOT_RUN
//
// This is a credential-free, reviewable replay harness for the live portions
// of E2E-19.  It is intentionally not the deleted temporary driver and it is
// not an acceptance result.  The real run and its observations are preserved
// in the e2e-19-live-*.log files named by the accompanying manifest.  Running
// this source requires explicit operator supplied environment variables and
// -execute; it never contains or prints bearer, step-up, Steam, allocation, or
// native nonce values.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type scope struct {
	AttemptID        string `json:"attempt_id"`
	AuthoritySession string `json:"authority_session_ref"`
	WorldInstance    string `json:"world_instance_id"`
	RosterRevision   int64  `json:"roster_revision"`
	RouteGeneration  int64  `json:"route_generation"`
}

type observation struct {
	Step       string `json:"step"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type config struct {
	BaseURL           string
	DatabaseName      string
	EvidenceRoot      string
	ActiveLobbyPath   string
	ActiveAttemptPath string
	ForceAbortPath    string
	Schema48Log       string
	MetaSchema48Log   string
}

func main() {
	execute := flag.Bool("execute", false, "run against explicitly supplied isolated services")
	mode := flag.String("mode", "describe", "describe, schema, drain, or all")
	flag.Parse()
	if !*execute {
		fmt.Println(`{"artifact":"e2e-19-live-driver-sanitized-replay","execution_status":"NOT_RUN"}`)
		return
	}

	cfg := configFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var observations []observation
	var err error
	switch *mode {
	case "schema":
		err = runSchemaChecks(ctx, cfg, &observations)
	case "drain":
		err = runDrainChecks(ctx, cfg, &observations)
	case "all":
		err = runSchemaChecks(ctx, cfg, &observations)
		if err == nil {
			err = runDrainChecks(ctx, cfg, &observations)
		}
	default:
		fmt.Fprintln(os.Stderr, "mode must be describe, schema, drain, or all")
		os.Exit(2)
	}

	for _, item := range observations {
		fmt.Printf("step=%s status=%s http_status=%d detail=%s\n", item.Step, item.Status, item.HTTPStatus, item.Detail)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sanitized replay failed:", err)
		os.Exit(1)
	}
	if out := os.Getenv("E2E19_REPLAY_OUTPUT"); out != "" {
		data, marshalErr := json.MarshalIndent(observations, "", "  ")
		if marshalErr == nil {
			_ = os.WriteFile(out, append(data, '\n'), 0600)
		}
	}
}

func configFromEnv() config {
	return config{
		BaseURL:           requiredEnv("E2E19_CONTROL_PLANE_URL"),
		DatabaseName:      requiredEnv("E2E19_DATABASE_NAME"),
		EvidenceRoot:      requiredEnv("E2E19_EVIDENCE_ROOT"),
		ActiveLobbyPath:   requiredEnv("E2E19_ACTIVE_LOBBY_PATH"),
		ActiveAttemptPath: requiredEnv("E2E19_ACTIVE_ATTEMPT_PATH"),
		ForceAbortPath:    requiredEnv("E2E19_FORCE_ABORT_PATH"),
		Schema48Log:       requiredEnv("E2E19_SCHEMA48_CONTROL_PLANE_LOG"),
		MetaSchema48Log:   requiredEnv("E2E19_SCHEMA48_META_SERVER_LOG"),
	}
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic("missing required replay environment: " + name)
	}
	return value
}

func runSchemaChecks(ctx context.Context, cfg config, observations *[]observation) error {
	if err := expectLog(ctx, "control-plane-schema-48-rejection", cfg.Schema48Log, "schema version mismatch"); err != nil {
		return err
	}
	*observations = append(*observations, observation{Step: "control-plane-schema-48-rejection", Status: "observed"})
	if err := expectLog(ctx, "meta-server-schema-48-rejection", cfg.MetaSchema48Log, "schema version mismatch"); err != nil {
		return err
	}
	*observations = append(*observations, observation{Step: "meta-server-schema-48-rejection", Status: "observed"})
	actual, err := psqlScalar(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations;")
	if err != nil {
		return err
	}
	if strings.TrimSpace(actual) != "47" {
		return fmt.Errorf("expected isolated schema max 47 after rejected start, got %q", strings.TrimSpace(actual))
	}
	*observations = append(*observations, observation{Step: "schema-48-no-mutation", Status: "observed", Detail: "schema_max=47"})
	return nil
}

func runDrainChecks(ctx context.Context, cfg config, observations *[]observation) error {
	client := &http.Client{Timeout: 10 * time.Second}
	status, err := request(ctx, client, cfg, http.MethodGet, "/health/ready", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("readiness returned %d", status)
	}
	*observations = append(*observations, observation{Step: "draining-instance-readiness", Status: "observed", HTTPStatus: status})

	for _, check := range []struct {
		step   string
		method string
		path   string
		want   int
	}{
		{"existing-context-read", http.MethodGet, cfg.ActiveLobbyPath, http.StatusOK},
		{"existing-context-heartbeat", http.MethodPost, cfg.ActiveAttemptPath + "/host/heartbeat", http.StatusOK},
		{"accept-new-disabled", http.MethodPost, "/v1/match-lobbies", http.StatusConflict},
		{"admin-step-up-force-abort", http.MethodPost, cfg.ForceAbortPath, http.StatusOK},
	} {
		status, err = request(ctx, client, cfg, check.method, check.path, map[string]string{"reason": "sanitized-e2e19-explicit-abort"})
		if err != nil {
			return err
		}
		if status != check.want {
			return fmt.Errorf("%s returned %d, wanted %d", check.step, status, check.want)
		}
		*observations = append(*observations, observation{Step: check.step, Status: "observed", HTTPStatus: status})
	}
	return nil
}

func request(ctx context.Context, client *http.Client, cfg config, method, path string, body any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer := os.Getenv("E2E19_OPERATOR_BEARER"); bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if stepUp := os.Getenv("E2E19_STEP_UP"); stepUp != "" {
		req.Header.Set("X-Admin-Step-Up", stepUp)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

func expectLog(ctx context.Context, step, path, marker string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("%s: read log: %w", step, err)
	}
	if !strings.Contains(string(data), marker) {
		return fmt.Errorf("%s: expected marker absent", step)
	}
	return nil
}

func psqlScalar(ctx context.Context, query string) (string, error) {
	cmd := exec.CommandContext(ctx, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-At", "-c", query)
	cmd.Env = append(os.Environ(),
		"PGHOST="+requiredEnv("E2E19_PGHOST"),
		"PGPORT="+requiredEnv("E2E19_PGPORT"),
		"PGUSER="+requiredEnv("E2E19_PGUSER"),
		"PGDATABASE="+requiredEnv("E2E19_DATABASE_NAME"),
	)
	data, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// sha256ForEvidence is intentionally available to a local operator who wants
// to bind a replay output to a source file. It prints only a digest, never the
// file contents or any credential material.
func sha256ForEvidence(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
