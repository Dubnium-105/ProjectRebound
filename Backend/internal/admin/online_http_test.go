package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
)

func TestAdministrativeGameServerResponseDoesNotExposeTokenHash(t *testing.T) {
	encoded, err := json.Marshal(administrativeGameServer(gameserver.Server{
		ID: "gs_test", ServerTokenHash: []byte("credential-hash"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if strings.Contains(body, "credential-hash") ||
		strings.Contains(body, "token_hash") ||
		strings.Contains(body, "registration_issuer") {
		t.Fatalf("administrator game-server response exposed registration credentials: %s", body)
	}
}
