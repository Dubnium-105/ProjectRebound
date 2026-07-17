package relayregistry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicNodeResponseDoesNotExposeCredentials(t *testing.T) {
	node := Node{ID: "relay_a", NodeTokenHash: []byte("credential-hash"), CertificateFingerprint: "fingerprint"}
	encoded, err := json.Marshal(resultNode(node))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if strings.Contains(body, "credential-hash") || strings.Contains(body, "node_token") {
		t.Fatalf("node response exposed a credential: %s", body)
	}
}
