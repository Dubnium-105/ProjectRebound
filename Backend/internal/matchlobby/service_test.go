package matchlobby

import "testing"

func TestCreateValidationLocksHostingKindAndTwoTeamCapacity(t *testing.T) {
	service := &Service{}
	valid := CreateInput{
		DisplayName: "Strict Lobby", HostingKind: HostingDedicated,
		Mode: "TDM", Region: "auto", ClientVersion: "1.0.0",
		ProtocolVersion: 1, TeamOneCapacity: 5, TeamTwoCapacity: 5, TeamID: 1,
	}
	if err := service.validateCreate(&valid); err != nil {
		t.Fatalf("valid dedicated lobby rejected: %v", err)
	}
	p2p := valid
	p2p.HostingKind = HostingP2P
	p2p.TransportKind = TransportVNT
	p2p.VNTNodeID = "node-1"
	if err := service.validateCreate(&p2p); err != nil {
		t.Fatalf("valid P2P lobby rejected: %v", err)
	}
	invalid := valid
	invalid.TransportKind = TransportLegacy
	if err := service.validateCreate(&invalid); err == nil {
		t.Fatal("dedicated lobby accepted a P2P transport")
	}
	invalid = valid
	invalid.TeamTwoCapacity = 0
	if err := service.validateCreate(&invalid); err == nil {
		t.Fatal("lobby accepted an empty second-team capacity")
	}
}

func TestOpenRevisionFailsClosedAfterFreezeOrConflict(t *testing.T) {
	lobby := Lobby{State: StateOpen, RosterRevision: 4}
	if err := requireOpenRevision(lobby, 4); err != nil {
		t.Fatalf("current open revision rejected: %v", err)
	}
	if err := requireOpenRevision(lobby, 3); errorCode(err) != "MATCH_LOBBY_REVISION_CONFLICT" {
		t.Fatalf("stale revision returned wrong error: %v", err)
	}
	lobby.State = StateFrozen
	if err := requireOpenRevision(lobby, 4); errorCode(err) != "MATCH_LOBBY_NOT_MUTABLE" {
		t.Fatalf("frozen roster was not immutable: %v", err)
	}
}

func errorCode(err error) string {
	if value, ok := err.(*serviceError); ok {
		return value.code
	}
	return ""
}
