package vnt

import (
	"context"
	"testing"
)

func TestValidateRegisterInputNormalizesTransports(t *testing.T) {
	input := RegisterInput{
		AdvertisedHost: "203.0.113.10", Port: 29878, Region: "cn-east",
		Location: "Shanghai", VNTSVersion: "1.2.3", WrapperVersion: "0.1.0",
		ServerKeyFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SupportedTransports: []string{"UDP", "tcp", "udp"},
		MaxRooms: 64,
	}
	if err := validateRegisterInput(&input); err != nil {
		t.Fatal(err)
	}
	if len(input.SupportedTransports) != 2 || input.SupportedTransports[0] != "tcp" || input.SupportedTransports[1] != "udp" {
		t.Fatalf("transports = %#v", input.SupportedTransports)
	}
}

func TestValidateRegisterInputRejectsPrivateEndpoint(t *testing.T) {
	input := RegisterInput{
		AdvertisedHost: "192.168.1.2", Port: 29878, Region: "cn-east",
		Location: "Home", VNTSVersion: "1.2.3", WrapperVersion: "0.1.0",
		ServerKeyFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SupportedTransports: []string{"udp", "tcp"},
		MaxRooms: 1,
	}
	if err := validateRegisterInput(&input); err == nil {
		t.Fatal("private endpoint was accepted")
	}
}

func TestValidateRegisterInputRejectsLinkLocalEndpoint(t *testing.T) {
	input := RegisterInput{
		AdvertisedHost: "169.254.1.2", Port: 29878, Region: "cn-east",
		Location: "Local", VNTSVersion: "1.2.3", WrapperVersion: "0.1.0",
		ServerKeyFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SupportedTransports: []string{"udp", "tcp"},
		MaxRooms: 1,
	}
	if err := validateRegisterInput(&input); err == nil {
		t.Fatal("link-local endpoint was accepted")
	}
}

func TestValidateRegisterInputRejectsNodeWithoutProbeTransport(t *testing.T) {
	input := RegisterInput{
		AdvertisedHost: "203.0.113.10", Port: 29878, Region: "cn-east",
		Location: "Shanghai", VNTSVersion: "1.2.3", WrapperVersion: "0.1.0",
		ServerKeyFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		SupportedTransports:  []string{"udp"}, MaxRooms: 1,
	}
	if err := validateRegisterInput(&input); err == nil {
		t.Fatal("node without TCP probe transport was accepted")
	}
}

func TestPublicNodeCapacityUsesDatabaseAllocationCount(t *testing.T) {
	node := Node{ID: "vnt_one", MaxRooms: 8, ActiveRooms: 3}
	if available := node.Public(true).CapacityAvailable; available != 5 {
		t.Fatalf("capacity available = %d", available)
	}
}

func TestVersionPolicyRequiresBothExactVersions(t *testing.T) {
	policy := NewVersionPolicy([]string{"1.2.3"}, []string{"0.4.0"})
	compatible := Node{VNTSVersion: "1.2.3", WrapperVersion: "0.4.0"}
	if !policy.Compatible(compatible) {
		t.Fatal("matching versions were rejected")
	}
	compatible.WrapperVersion = "0.4.1"
	if policy.Compatible(compatible) {
		t.Fatal("unlisted wrapper version was accepted")
	}
	if (VersionPolicy{}).Compatible(Node{VNTSVersion: "1", WrapperVersion: "1"}) {
		t.Fatal("empty policy must fail closed")
	}
}

func TestHeartbeatRejectsNegativeUptimeBeforeRepositoryAccess(t *testing.T) {
	service := &Service{}
	err := service.Heartbeat(context.Background(), "vnt_test", "vnn_test", HeartbeatInput{
		WrapperVersion: "1", VNTSVersion: "1", UptimeSeconds: -1,
	})
	if err == nil {
		t.Fatal("negative uptime was accepted")
	}
}

func TestEnrollmentRequiresIntegrityTrustedSessionBeforeRepositoryAccess(t *testing.T) {
	service := &Service{}
	_, err := service.CreateEnrollment(t.Context(), Actor{
		PlayerID: "player_one", AccountStatus: "ACTIVE", SteamVerified: true,
	}, "node-one")
	_, code, _, _ := errorDetails(err)
	if code != "VNT_NODE_ENROLLMENT_FORBIDDEN" {
		t.Fatalf("enrollment error = %v", err)
	}
}

func TestListRejectsInvalidFiltersBeforeRepositoryAccess(t *testing.T) {
	service := &Service{}
	tests := []ListFilter{
		{Status: "UNKNOWN", Limit: 10},
		{Status: StateOnline, Region: "not a region", Limit: 10},
		{Status: StateOnline, Cursor: "room_wrong", Limit: 10},
		{Status: StateOnline, Cursor: "vnt_not-valid", Limit: 10},
		{Status: StateOnline, Limit: 201},
	}
	for _, filter := range tests {
		if _, err := service.List(context.Background(), filter); err == nil {
			t.Fatalf("invalid filter was accepted: %#v", filter)
		}
	}
}

func TestListOwnedRequiresVerifiedActivePlayerBeforeRepositoryAccess(t *testing.T) {
	service := &Service{}
	if _, err := service.ListOwned(t.Context(), Actor{PlayerID: "player_one", AccountStatus: "ACTIVE"}, OwnedListFilter{}); err == nil {
		t.Fatal("unverified player listed owned VNT nodes")
	}
}

func TestListOwnedRejectsInvalidFiltersBeforeRepositoryAccess(t *testing.T) {
	service := &Service{}
	actor := Actor{PlayerID: "player_one", AccountStatus: "ACTIVE", SteamVerified: true}
	for _, filter := range []OwnedListFilter{
		{Status: "UNKNOWN", Limit: 10},
		{Cursor: "room_wrong", Limit: 10},
		{Limit: 101},
	} {
		if _, err := service.ListOwned(t.Context(), actor, filter); err == nil {
			t.Fatalf("invalid owned-node filter was accepted: %#v", filter)
		}
	}
}
