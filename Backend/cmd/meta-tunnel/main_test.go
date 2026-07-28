package main

import (
	"encoding/json"
	"strings"
	"testing"
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
	if _, err := readAccessToken(strings.NewReader("short\n")); err == nil {
		t.Fatal("short token was accepted")
	}
	token := strings.Repeat("a", 64)
	if got, err := readAccessToken(strings.NewReader(token + "\n")); err != nil || got != token {
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
