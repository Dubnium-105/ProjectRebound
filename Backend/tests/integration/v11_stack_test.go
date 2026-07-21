//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/compose"
	"github.com/testcontainers/testcontainers-go/wait"
)

const integrationAdminToken = "v11-integration-admin-token-0123456789-abcdef"

type nodeListResponse struct {
	Data struct {
		Items []struct {
			DisplayName       string     `json:"display_name"`
			State             string     `json:"state"`
			ActiveAllocations int        `json:"active_allocations"`
			LastHeartbeatAt   *time.Time `json:"last_heartbeat_at"`
		} `json:"items"`
	} `json:"data"`
}

type loadReport struct {
	SuccessfulRequests     uint64            `json:"successful_requests"`
	FailedRequests         uint64            `json:"failed_requests"`
	SuccessRatePercent     float64           `json:"success_rate_percent"`
	RoomsCreated           uint64            `json:"rooms_created"`
	RelayAllocations       uint64            `json:"relay_allocations"`
	RelayAllocationsClosed uint64            `json:"relay_allocations_closed"`
	RelayBindSuccess       uint64            `json:"relay_bind_success"`
	RelayBindFailures      uint64            `json:"relay_bind_failures"`
	RelayMigrationSuccess  uint64            `json:"relay_migration_success"`
	RelayMigrationAttempts uint64            `json:"relay_migration_attempts"`
	WebSocketReconnects    uint64            `json:"websocket_reconnects"`
	PacketsSent            uint64            `json:"packets_sent"`
	PacketsReceived        uint64            `json:"packets_received"`
	PacketLossPercent      float64           `json:"packet_loss_percent"`
	TokenRefreshFailures   uint64            `json:"token_refresh_failures"`
	Failures               map[string]uint64 `json:"failures"`
}

func TestV11ControlPlaneAndTwoRelays(t *testing.T) {
	if os.Getenv("V11_INTEGRATION") != "1" {
		t.Skip("set V11_INTEGRATION=1 to run the disposable Docker integration gate")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the load bot must reach the isolated Docker bridge addresses; run this gate on Linux")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	integrationDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	backendDir, err := filepath.Abs(filepath.Join(integrationDir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := integrationSecrets()
	if err != nil {
		t.Fatal(err)
	}
	projectID := makeProjectID(t)
	dockerCompose, err := compose.NewDockerComposeWith(
		compose.StackIdentifier(projectID),
		compose.WithStackFiles(filepath.Join(integrationDir, "docker-compose.yaml")),
	)
	if err != nil {
		t.Fatal(err)
	}
	stack := dockerCompose.WithEnv(secrets).WaitForService(
		"control-plane",
		wait.ForHTTP("/health/live").WithPort("8080/tcp").WithStartupTimeout(4*time.Minute),
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := stack.Down(cleanupCtx, compose.RemoveOrphans(true), compose.RemoveVolumes(true)); err != nil {
			t.Logf("integration stack cleanup: %v", err)
		}
	})
	if err := buildComposeImages(ctx, filepath.Join(integrationDir, "docker-compose.yaml"), projectID, secrets); err != nil {
		t.Fatalf("build current integration images: %v", err)
	}
	if err := stack.Up(ctx, compose.Wait(true)); err != nil {
		t.Fatalf("start integration stack: %v", err)
	}

	nodes := waitForReadyRelays(t, ctx, 2)
	for _, expected := range []string{"integration-relay-a", "integration-relay-b"} {
		if !nodes[expected] {
			t.Fatalf("ready relay %q not found: %#v", expected, nodes)
		}
	}

	report, contents, runErr := runLoadBot(t, ctx, backendDir, integrationDir)
	assertLoadReport(t, report, contents, runErr)
	relayA, err := stack.ServiceContainer(ctx, "edge-relay-a")
	if err != nil {
		t.Fatalf("get edge-relay-a container: %v", err)
	}
	if os.Getenv("V11_SKIP_NETEM") != "1" {
		for _, profile := range []string{"mild", "moderate", "severe"} {
			t.Run("netem-"+profile, func(t *testing.T) {
				runImpairedLoadBot(t, ctx, backendDir, integrationDir, relayA.GetContainerID(), profile)
			})
		}
	}
	runReconnectStorm(t, ctx, backendDir, integrationDir)
	runRelayFailureMigration(t, ctx, backendDir, integrationDir, relayA.GetContainerID())
	if err := relayA.Start(ctx); err != nil {
		t.Fatalf("restart killed edge-relay-a: %v", err)
	}
	waitForReadyRelays(t, ctx, 2)

	for _, service := range []string{"redis", "postgres", "control-plane"} {
		started := time.Now()
		restartedAfter := time.Now().UTC()
		restartService(t, ctx, stack, service)
		waitForHTTPStatus(t, ctx, "http://127.0.0.1:28080/health/ready", http.StatusOK)
		if service == "control-plane" {
			waitForReadyRelaysAfter(t, ctx, 2, restartedAfter)
		}
		t.Logf("fault-recovery service=%s recovery_seconds=%.3f", service, time.Since(started).Seconds())
	}

	assertRecoveredLoadReport(t, ctx, backendDir, integrationDir)
}

func runReconnectStorm(t *testing.T, ctx context.Context, backendDir, integrationDir string) {
	t.Helper()
	waitForNoRelayAllocations(t, ctx)
	for attempt := 1; attempt <= 2; attempt++ {
		report, contents, runErr := runReconnectStormAttempt(t, ctx, backendDir, integrationDir)
		if validationErr := validateReconnectStormReport(report, contents, runErr); validationErr == nil {
			return
		} else if attempt == 2 {
			t.Fatalf("reconnect storm failed after retry: %v", validationErr)
		} else {
			t.Logf("reconnect storm was transiently unhealthy; retrying once: %v", validationErr)
		}
		waitForNoRelayAllocations(t, ctx)
		waitForReadyRelays(t, ctx, 2)
		if !waitForDuration(ctx, 2*time.Second) {
			t.Fatal(ctx.Err())
		}
	}
}

func runReconnectStormAttempt(t *testing.T, ctx context.Context, backendDir, integrationDir string) (loadReport, []byte, error) {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "load-report.json")
	metricsPath := filepath.Join(t.TempDir(), "load-report.prom")
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/load-bot",
		"-config", filepath.Join(integrationDir, "scenario-reconnect-storm.yaml"),
		"-report", reportPath,
		"-prometheus-report", metricsPath,
	)
	cmd.Dir = backendDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, runErr := cmd.CombinedOutput()
	t.Logf("100-client reconnect-storm load-bot output:\n%s", output)
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		return loadReport{}, nil, fmt.Errorf("read reconnect-storm report after command error %v: %w", runErr, err)
	}
	var report loadReport
	if err := json.Unmarshal(contents, &report); err != nil {
		return loadReport{}, contents, fmt.Errorf("decode reconnect-storm report after command error %v: %w", runErr, err)
	}
	return report, contents, runErr
}

func validateReconnectStormReport(report loadReport, contents []byte, runErr error) error {
	if report.FailedRequests != 0 || len(report.Failures) != 0 || report.TokenRefreshFailures != 0 ||
		report.RelayBindFailures != 0 {
		return fmt.Errorf("reconnect-storm command error %v reported failures: %s", runErr, contents)
	}
	if report.SuccessRatePercent != 100 ||
		report.RoomsCreated != 50 || report.RelayAllocations != 50 || report.RelayAllocationsClosed != 50 ||
		report.RelayBindSuccess != 100 || report.WebSocketReconnects < 100 ||
		report.PacketsSent == 0 || report.PacketsReceived == 0 || report.PacketLossPercent != 0 {
		return fmt.Errorf("reconnect-storm command error %v did not meet acceptance thresholds: %s", runErr, contents)
	}
	if runErr != nil {
		return fmt.Errorf("reconnect-storm command failed despite a passing report: %w", runErr)
	}
	return nil
}

func buildComposeImages(ctx context.Context, composeFile, projectID string, environment map[string]string) error {
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-name", projectID, "--file", composeFile, "build")
	cmd.Env = append(os.Environ(), environmentEntries(environment)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func environmentEntries(values map[string]string) []string {
	entries := make([]string, 0, len(values))
	for name, value := range values {
		entries = append(entries, name+"="+value)
	}
	return entries
}

func runRelayFailureMigration(t *testing.T, ctx context.Context, backendDir, integrationDir, relayContainerID string) {
	t.Helper()
	waitForNoRelayAllocations(t, ctx)
	reportPath := filepath.Join(t.TempDir(), "load-report.json")
	metricsPath := filepath.Join(t.TempDir(), "load-report.prom")
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/load-bot",
		"-config", filepath.Join(integrationDir, "scenario-relay-fault.yaml"),
		"-report", reportPath,
		"-prometheus-report", metricsPath,
	)
	cmd.Dir = backendDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	}()
	waitForRelayAllocations(t, ctx, "integration-relay-a", "integration-relay-b")
	kill := exec.CommandContext(ctx, "docker", "kill", "--signal", "KILL", relayContainerID)
	if killOutput, err := kill.CombinedOutput(); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("SIGKILL edge-relay-a: %v: %s", err, killOutput)
	}
	runErr := cmd.Wait()
	t.Logf("relay SIGKILL load-bot output:\n%s", output.String())
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read Relay failure report after command error %v: %v", runErr, err)
	}
	var report loadReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	for category := range report.Failures {
		if category != "relay_traffic" {
			t.Fatalf("unexpected Relay failure category %q: %s", category, contents)
		}
	}
	if report.FailedRequests != 0 || report.RelayBindFailures != 0 || report.TokenRefreshFailures != 0 ||
		report.RelayMigrationAttempts < 1 || report.RelayMigrationSuccess != report.RelayMigrationAttempts ||
		report.PacketsReceived == 0 {
		t.Fatalf("Relay migration acceptance thresholds were not met: %s", contents)
	}
}

func waitForNoRelayAllocations(t *testing.T, ctx context.Context) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	lastObserved := "no successful node inventory response"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:28080/internal/v1/relay-nodes?limit=10", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+integrationAdminToken)
		resp, err := client.Do(req)
		if err == nil {
			var payload nodeListResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil {
				active := 0
				observed := make([]string, 0, len(payload.Data.Items))
				for _, node := range payload.Data.Items {
					active += node.ActiveAllocations
					observed = append(observed, fmt.Sprintf("%s=%d", node.DisplayName, node.ActiveAllocations))
				}
				lastObserved = strings.Join(observed, ", ")
				if active == 0 {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait for previous Relay allocations to close timed out; last observed: %s", lastObserved)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func waitForRelayAllocations(t *testing.T, ctx context.Context, displayNames ...string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:28080/internal/v1/relay-nodes?state=READY&limit=10", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+integrationAdminToken)
		resp, err := client.Do(req)
		if err == nil {
			var payload nodeListResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil {
				active := make(map[string]bool, len(displayNames))
				for _, node := range payload.Data.Items {
					if node.ActiveAllocations > 0 {
						active[node.DisplayName] = true
					}
				}
				allActive := true
				for _, displayName := range displayNames {
					allActive = allActive && active[displayName]
				}
				if allActive {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for allocations on %v: %v", displayNames, ctx.Err())
		case <-time.After(time.Second):
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait for allocations on %v timed out", displayNames)
		}
	}
}

func runImpairedLoadBot(t *testing.T, ctx context.Context, backendDir, integrationDir, relayContainerID, profile string) {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "load-report.json")
	metricsPath := filepath.Join(t.TempDir(), "load-report.prom")
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/load-bot",
		"-config", filepath.Join(integrationDir, "scenario-smoke.yaml"),
		"-report", reportPath,
		"-prometheus-report", metricsPath,
	)
	cmd.Dir = backendDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitForDuration(ctx, 6*time.Second) {
		t.Fatal(ctx.Err())
	}
	if err := runNetem(ctx, integrationDir, relayContainerID, profile); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("apply %s netem: %v", profile, err)
	}
	if !waitForDuration(ctx, 8*time.Second) {
		_ = runNetem(context.Background(), integrationDir, relayContainerID, "reset")
		_ = cmd.Process.Kill()
		t.Fatal(ctx.Err())
	}
	if err := runNetem(ctx, integrationDir, relayContainerID, "reset"); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("reset %s netem: %v", profile, err)
	}
	runErr := cmd.Wait()
	t.Logf("netem=%s load-bot output:\n%s", profile, output.String())
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read %s report after command error %v: %v", profile, runErr, err)
	}
	var report loadReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	for category := range report.Failures {
		if category != "relay_traffic" {
			t.Fatalf("unexpected %s failure category %q: %s", profile, category, contents)
		}
	}
	if report.FailedRequests != 0 || report.TokenRefreshFailures != 0 || report.RelayBindFailures != 0 ||
		report.SuccessRatePercent != 100 || report.PacketsSent == 0 || report.PacketsReceived == 0 ||
		report.PacketLossPercent > 60 {
		t.Fatalf("%s impairment acceptance thresholds were not met: %s", profile, contents)
	}
}

func runNetem(ctx context.Context, integrationDir, relayContainerID, profile string) error {
	cmd := exec.CommandContext(ctx, "sh", filepath.Join(integrationDir, "container-netem.sh"), relayContainerID, profile)
	cmd.Env = append(os.Environ(), "NETEM_I_UNDERSTAND=disposable-container")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func waitForDuration(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runLoadBot(t *testing.T, ctx context.Context, backendDir, integrationDir string) (loadReport, []byte, error) {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "load-report.json")
	metricsPath := filepath.Join(t.TempDir(), "load-report.prom")
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/load-bot",
		"-config", filepath.Join(integrationDir, "scenario-smoke.yaml"),
		"-report", reportPath,
		"-prometheus-report", metricsPath,
	)
	cmd.Dir = backendDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, runErr := cmd.CombinedOutput()
	t.Logf("load-bot output:\n%s", output)
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read load-bot report after command error %v: %v", runErr, err)
	}
	var report loadReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	return report, contents, runErr
}

func assertRecoveredLoadReport(t *testing.T, ctx context.Context, backendDir, integrationDir string) {
	t.Helper()
	for attempt := 1; attempt <= 2; attempt++ {
		report, contents, runErr := runLoadBot(t, ctx, backendDir, integrationDir)
		if validationErr := validateLoadReport(report, contents, runErr); validationErr == nil {
			return
		} else if attempt == 2 {
			t.Fatalf("post-recovery smoke failed after retry: %v", validationErr)
		} else {
			t.Logf("post-recovery smoke was transiently unhealthy; retrying once: %v", validationErr)
		}
		waitForNoRelayAllocations(t, ctx)
		waitForReadyRelays(t, ctx, 2)
		if !waitForDuration(ctx, 2*time.Second) {
			t.Fatal(ctx.Err())
		}
	}
}

func assertLoadReport(t *testing.T, report loadReport, contents []byte, runErr error) {
	t.Helper()
	if err := validateLoadReport(report, contents, runErr); err != nil {
		t.Fatal(err)
	}
}

func validateLoadReport(report loadReport, contents []byte, runErr error) error {
	if report.FailedRequests != 0 || len(report.Failures) != 0 || report.TokenRefreshFailures != 0 || report.RelayBindFailures != 0 {
		return fmt.Errorf("load-bot command error %v reported failures: %s", runErr, contents)
	}
	if report.SuccessRatePercent != 100 || report.RoomsCreated != 2 || report.RelayAllocations != 2 ||
		report.RelayAllocationsClosed != 2 || report.RelayBindSuccess < 4 ||
		report.PacketsSent == 0 || report.PacketsReceived == 0 || report.PacketLossPercent != 0 {
		return fmt.Errorf("load-bot command error %v did not meet acceptance thresholds: %s", runErr, contents)
	}
	if runErr != nil {
		return fmt.Errorf("load-bot command failed despite a passing report: %w", runErr)
	}
	return nil
}

func restartService(t *testing.T, ctx context.Context, stack compose.ComposeStack, service string) {
	t.Helper()
	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get %s container: %v", service, err)
	}
	stopTimeout := 5 * time.Second
	if err := container.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stop %s: %v", service, err)
	}
	time.Sleep(2 * time.Second)
	if err := container.Start(ctx); err != nil {
		t.Fatalf("start %s: %v", service, err)
	}
}

func waitForHTTPStatus(t *testing.T, ctx context.Context, target string, wanted int) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == wanted {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s status %d: %v", target, wanted, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func waitForReadyRelays(t *testing.T, ctx context.Context, wanted int) map[string]bool {
	return waitForReadyRelaysAfter(t, ctx, wanted, time.Time{})
}

func waitForReadyRelaysAfter(t *testing.T, ctx context.Context, wanted int, minimumHeartbeat time.Time) map[string]bool {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:28080/internal/v1/relay-nodes?state=READY&limit=10", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+integrationAdminToken)
		resp, err := client.Do(req)
		if err == nil {
			var payload nodeListResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil && len(payload.Data.Items) >= wanted {
				ready := make(map[string]bool, len(payload.Data.Items))
				for _, node := range payload.Data.Items {
					fresh := minimumHeartbeat.IsZero() || node.LastHeartbeatAt != nil && node.LastHeartbeatAt.After(minimumHeartbeat)
					if node.State == "READY" && fresh {
						ready[node.DisplayName] = true
					}
				}
				if len(ready) >= wanted {
					return ready
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %d ready relays: %v", wanted, ctx.Err())
		case <-ticker.C:
		}
	}
}

func integrationSecrets() (map[string]string, error) {
	caCert, caKey, err := makeCA()
	if err != nil {
		return nil, err
	}
	seed := func() (string, error) {
		value := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(value); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(value), nil
	}
	access, err := seed()
	if err != nil {
		return nil, err
	}
	relay, err := seed()
	if err != nil {
		return nil, err
	}
	update, err := seed()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"V11_CONTROL_HTTP_PORT":                 "28080",
		"V11_ADMIN_TOKEN":                       integrationAdminToken,
		"V11_RELAY_A_BOOTSTRAP_TOKEN":           "integration-relay-a-bootstrap-token-0123456789",
		"V11_RELAY_B_BOOTSTRAP_TOKEN":           "integration-relay-b-bootstrap-token-0123456789",
		"V11_ACCESS_TOKEN_PRIVATE_KEY_BASE64":   access,
		"V11_RELAY_TOKEN_PRIVATE_KEY_BASE64":    relay,
		"V11_UPDATE_SIGNING_PRIVATE_KEY_BASE64": update,
		"V11_RELAY_CA_CERT_PEM_BASE64":          base64.StdEncoding.EncodeToString(caCert),
		"V11_RELAY_CA_KEY_PEM_BASE64":           base64.StdEncoding.EncodeToString(caKey),
		"V11_GOPROXY":                           envOrDefault("GOPROXY", "https://proxy.golang.org,direct"),
		"V11_GOSUMDB":                           envOrDefault("GOSUMDB", "sum.golang.org"),
	}, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func makeCA() ([]byte, []byte, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Project Rebound V1.1 Integration CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), nil
}

func makeProjectID(t *testing.T) string {
	t.Helper()
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("project-rebound-v11-%s", strings.ToLower(hex.EncodeToString(random)))
}
