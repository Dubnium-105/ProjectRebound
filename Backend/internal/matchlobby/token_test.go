package matchlobby

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdmissionSignerBindsFrozenRosterAndJoinSeat(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	signer, err := NewAdmissionSigner("match-key-1", base64.StdEncoding.EncodeToString(seed), "test")
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return fixedNow }
	allocation, expires, err := signer.SignAllocation(AllocationClaims{
		AttemptID: "mat_1", LobbyID: "lby_1", HostingKind: HostingP2P,
		AuthorityID: "player_host", AuthoritySessionID: "mas_1",
		RosterRevision: 7, RouteGeneration: 2,
		Roster: []FrozenRosterMember{{
			PlayerID: "player_a", PlatformID: "76561198000000001", TeamID: 2,
			TeamSlot: 1, LogicalSlot: 5, ConnectionGeneration: 1,
		}},
	}, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(fixedNow.Add(8 * time.Hour)) {
		t.Fatalf("unexpected allocation expiry: %s", expires)
	}
	var allocationClaims AllocationClaims
	verifyAdmissionToken(t, signer, allocation, "match-allocation+jwt", &allocationClaims)
	if allocationClaims.Audience != allocationAudience ||
		allocationClaims.RosterRevision != 7 || len(allocationClaims.Roster) != 1 ||
		allocationClaims.Roster[0].TeamID != 2 || allocationClaims.Roster[0].LogicalSlot != 5 {
		t.Fatalf("allocation lost frozen roster identity: %+v", allocationClaims)
	}

	grant, _, err := signer.SignJoinGrant(JoinGrantClaims{
		AttemptID: "mat_1", LobbyID: "lby_1", HostingKind: HostingP2P,
		AuthorityID: "player_host", AuthoritySessionID: "mas_1",
		PlayerID: "player_a", PlatformID: "76561198000000001", RosterRevision: 7,
		TeamID: 2, TeamSlot: 1, LogicalSlot: 5, ConnectionGeneration: 3, RouteGeneration: 2,
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var grantClaims JoinGrantClaims
	verifyAdmissionToken(t, signer, grant, "match-join+jwt", &grantClaims)
	if grantClaims.Audience != joinGrantAudience ||
		grantClaims.PlayerID != "player_a" || grantClaims.TeamID != 2 ||
		grantClaims.ConnectionGeneration != 3 || grantClaims.ExpiresAt-fixedNow.Unix() != 60 {
		t.Fatalf("join grant lost reserved seat claims: %+v", grantClaims)
	}
}

func TestAdmissionSignerUsesSeparateTokenTypes(t *testing.T) {
	signer, err := NewAdmissionSigner("dev", "", "development")
	if err != nil {
		t.Fatal(err)
	}
	allocation, _, _ := signer.SignAllocation(AllocationClaims{}, time.Minute)
	grant, _, _ := signer.SignJoinGrant(JoinGrantClaims{}, time.Minute)
	if strings.Split(allocation, ".")[0] == strings.Split(grant, ".")[0] {
		t.Fatal("allocation and join grant must use distinct protected token types")
	}
}

func verifyAdmissionToken(t *testing.T, signer *AdmissionSigner, token, expectedType string, claims any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments", len(parts))
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var header admissionHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	if header.Algorithm != "EdDSA" || header.Type != expectedType || header.KeyID != signer.KeyID() {
		t.Fatalf("unexpected header: %+v", header)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(signer.publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		t.Fatal("signature did not verify")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, claims); err != nil {
		t.Fatal(err)
	}
}
