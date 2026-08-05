package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRewriteConnectEndpoint(t *testing.T) {
	for _, input := range []string{
		`{"gateToken":"redacted","endpoint":"logic.example:443"}`,
		`{"data":{"gate_ticket":"redacted","endpoint":"logic.example:443"},"request_id":"req_test"}`,
	} {
		output, err := rewriteConnectEndpoint([]byte(input), "127.0.0.1:49152")
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatal(err)
		}
		encoded := string(output)
		if !strings.Contains(encoded, "127.0.0.1:49152") || strings.Contains(encoded, "logic.example:443") {
			t.Fatalf("endpoint was not safely rewritten: %s", output)
		}
	}
}

func TestAccessTokenRequiresPipeSafeValue(t *testing.T) {
	if _, err := readAccessToken(bufio.NewReader(strings.NewReader("short\n"))); err == nil {
		t.Fatal("short token was accepted")
	}
	token := strings.Repeat("a", 64)
	if got, err := readAccessToken(bufio.NewReader(strings.NewReader(token + "\n"))); err != nil || got != token {
		t.Fatalf("token = %q, err = %v", got, err)
	}
}

func TestListenersMustBeLoopback(t *testing.T) {
	if !isLoopbackListen("127.0.0.1:0") || !isLoopbackListen("[::1]:0") {
		t.Fatal("loopback listener was rejected")
	}
	if isLoopbackListen("0.0.0.0:8000") {
		t.Fatal("public listener was accepted")
	}
}

func TestHTTPProxyForwardsCompleteRequestAndResponse(t *testing.T) {
	var upstreamRequest struct {
		method, requestURI, body, authorization, customHeader string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		upstreamRequest.method = r.Method
		upstreamRequest.requestURI = r.URL.RequestURI()
		upstreamRequest.body = string(body)
		upstreamRequest.authorization = r.Header.Get("Authorization")
		upstreamRequest.customHeader = r.Header.Get("X-Game-Header")
		w.Header().Set("X-Upstream", "preserved")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, "upstream-response")
	}))
	defer upstream.Close()

	instance := newTestTunnel(t, upstream.URL, upstream.Client().Transport)
	request := httptest.NewRequest(http.MethodPatch, "/v1/users/me/loadouts/PEACE?revision=7&mode=full", strings.NewReader(`{"snapshot":{}}`))
	request.Header.Set("Authorization", "Bearer client-controlled-token")
	request.Header.Set("X-Game-Header", "preserve-me")
	response := httptest.NewRecorder()
	instance.serveHTTP(response, request)

	if response.Code != http.StatusMultiStatus || response.Body.String() != "upstream-response" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Upstream") != "preserved" {
		t.Fatalf("upstream response header was lost: %v", response.Header())
	}
	if upstreamRequest.method != http.MethodPatch ||
		upstreamRequest.requestURI != "/v1/users/me/loadouts/PEACE?revision=7&mode=full" ||
		upstreamRequest.body != `{"snapshot":{}}` ||
		upstreamRequest.customHeader != "preserve-me" {
		t.Fatalf("request was not fully forwarded: %+v", upstreamRequest)
	}
	if upstreamRequest.authorization != "Bearer "+strings.Repeat("a", 64) {
		t.Fatalf("launcher authorization was not injected: %q", upstreamRequest.authorization)
	}
}

func TestConnectServerRewritesOnlySuccessfulEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "original-body-hash")
		_, _ = io.WriteString(w, `{"error":0,"gateToken":"redacted","endpoint":"logic.example:443"}`)
	}))
	defer upstream.Close()

	instance := newTestTunnel(t, upstream.URL, upstream.Client().Transport)
	request := httptest.NewRequest(http.MethodPost, "/connectServer", strings.NewReader(`{"legacy":true}`))
	response := httptest.NewRecorder()
	instance.serveHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, body = %q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), instance.localTCP.Addr().String()) || strings.Contains(response.Body.String(), "logic.example:443") {
		t.Fatalf("connect endpoint was not rewritten: %s", response.Body.String())
	}
	if response.Header().Get("ETag") != "" {
		t.Fatalf("stale response validator was retained: %q", response.Header().Get("ETag"))
	}
}

func TestMetaHealthIsForwardedAndTunnelHealthIsLocal(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = io.WriteString(w, "meta-health")
	}))
	defer upstream.Close()

	instance := newTestTunnel(t, upstream.URL, upstream.Client().Transport)
	metaResponse := httptest.NewRecorder()
	instance.serveHTTP(metaResponse, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	localResponse := httptest.NewRecorder()
	instance.serveHTTP(localResponse, httptest.NewRequest(http.MethodGet, "/_meta-tunnel/health/live", nil))

	if upstreamCalls != 1 || metaResponse.Body.String() != "meta-health" {
		t.Fatalf("MetaServer health path was not forwarded: calls=%d body=%q", upstreamCalls, metaResponse.Body.String())
	}
	if localResponse.Body.String() != "{\"status\":\"live\"}\n" {
		t.Fatalf("local tunnel health response = %q", localResponse.Body.String())
	}
}

func TestAccessTokenCanRotateOnPersistentStdin(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	reader := bufio.NewReader(strings.NewReader(first + "\nmalformed\n" + second + "\n"))
	initial, err := readAccessToken(reader)
	if err != nil {
		t.Fatal(err)
	}
	instance := &tunnel{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	instance.setAccessToken(initial)
	instance.watchAccessTokens(context.Background(), reader)
	if instance.currentAccessToken() != second {
		t.Fatalf("rotated token was not installed")
	}
}

func TestHTTPProxyUsesRotatedAccessToken(t *testing.T) {
	seen := make([]string, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	instance := newTestTunnel(t, upstream.URL, upstream.Client().Transport)

	instance.serveHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/first", nil))
	instance.setAccessToken(strings.Repeat("b", 64))
	instance.serveHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/second", nil))
	if len(seen) != 2 || seen[0] != "Bearer "+strings.Repeat("a", 64) || seen[1] != "Bearer "+strings.Repeat("b", 64) {
		t.Fatalf("authorization tokens = %q", seen)
	}
}

func TestHTTPProxyFlushesStreamingResponses(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second\n")
	}))
	defer upstream.Close()
	instance := newTestTunnel(t, upstream.URL, upstream.Client().Transport)
	local := httptest.NewServer(http.HandlerFunc(instance.serveHTTP))
	defer local.Close()

	response, err := local.Client().Get(local.URL + "/events")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || first != "first\n" {
		close(release)
		t.Fatalf("first streamed chunk = %q, err = %v", first, err)
	}
	close(release)
	second, err := reader.ReadString('\n')
	if err != nil || second != "second\n" {
		t.Fatalf("second streamed chunk = %q, err = %v", second, err)
	}
}

func TestHTTPProxySupportsUpgradeConnections(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") || r.Header.Get("Upgrade") != "rebound-test" {
			http.Error(w, "missing upgrade", http.StatusBadRequest)
			return
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprint(connection, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: rebound-test\r\n\r\n")
		_, _ = io.Copy(connection, connection)
	}))
	defer upstream.Close()

	instance := newTestTunnel(t, upstream.URL, http.DefaultTransport)
	local := httptest.NewServer(http.HandlerFunc(instance.serveHTTP))
	defer local.Close()
	localURL, err := url.Parse(local.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", localURL.Host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(connection, "GET /stream HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: rebound-test\r\n\r\n", localURL.Host)
	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, "101 Switching Protocols") {
		t.Fatalf("upgrade status = %q, err = %v", status, err)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = io.WriteString(connection, "ping")
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("upgrade echo = %q, err = %v", echo, err)
	}
}

func newTestTunnel(t *testing.T, upstreamURL string, transport http.RoundTripper) *tunnel {
	t.Helper()
	baseURL, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	instance := &tunnel{
		metaBaseURL: baseURL,
		localTCP:    listener,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	instance.setAccessToken(strings.Repeat("a", 64))
	instance.httpProxy = instance.newHTTPProxy(transport)
	return instance
}
