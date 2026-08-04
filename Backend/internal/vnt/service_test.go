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
	if available := node.Public().CapacityAvailable; available != 5 {
		t.Fatalf("capacity available = %d", available)
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
