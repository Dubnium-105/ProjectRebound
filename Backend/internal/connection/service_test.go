package connection

import "testing"

type detailedDependencyError struct{}

func (detailedDependencyError) Error() string { return "relay pending" }

func (detailedDependencyError) ErrorDetails() (int, string, string, map[string]any) {
	return 409, "RELAY_ALLOCATION_REVOKE_PENDING", "Relay allocation revocation is still pending.", map[string]any{
		"pending_allocations": 1,
	}
}

func TestMapDependencyErrorPreservesTypedRelayPending(t *testing.T) {
	err := mapDependencyError(detailedDependencyError{})
	serviceErr, ok := err.(*ServiceError)
	if !ok {
		t.Fatalf("mapped error type = %T, want *ServiceError", err)
	}
	if serviceErr.Status != 409 || serviceErr.Code != "RELAY_ALLOCATION_REVOKE_PENDING" || serviceErr.Details["pending_allocations"] != 1 {
		t.Fatalf("mapped relay error = %#v", serviceErr)
	}
}

func TestValidateCandidateEnforcesAddressClass(t *testing.T) {
	tests := []struct {
		name      string
		candidate CandidateInput
		valid     bool
	}{
		{name: "private LAN", candidate: candidateInput(CandidateLAN, "192.168.1.20"), valid: true},
		{name: "public LAN", candidate: candidateInput(CandidateLAN, "8.8.8.8")},
		{name: "public reflexive", candidate: candidateInput(CandidateSRFLX, "8.8.8.8"), valid: true},
		{name: "private reflexive", candidate: candidateInput(CandidateSRFLX, "10.0.0.1")},
		{name: "routable IPv6", candidate: candidateInput(CandidateIPv6, "2001:4860:4860::8888"), valid: true},
		{name: "loopback", candidate: candidateInput(CandidateIPv6, "::1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateCandidate(test.candidate)
			if (err == nil) != test.valid {
				t.Fatalf("validateCandidate() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestStableCandidateReplayPolicy(t *testing.T) {
	for _, state := range []State{
		StateAllocatingRelay,
		StateRelayBinding,
		StateMigratingRelay,
		StateConnected,
	} {
		if !acceptsStableCandidateReplay(state) {
			t.Fatalf("state %s should accept an exact stable candidate replay", state)
		}
	}
	for _, state := range []State{
		StateCreated,
		StateGatheringCandidates,
		StateCheckingDirect,
		StateFailed,
		StateExpired,
		StateClosed,
	} {
		if acceptsStableCandidateReplay(state) {
			t.Fatalf("state %s should not use the advanced-state replay path", state)
		}
	}
	input := candidateInput(CandidateLAN, "192.168.1.20")
	candidate := Candidate{
		ConnectionID:  input.ConnectionID,
		Foundation:    input.Foundation,
		CandidateType: input.CandidateType,
		Protocol:      input.Protocol,
		Address:       input.Address,
		Port:          input.Port,
		Priority:      input.Priority,
	}
	if !candidateMatchesInput(candidate, input) {
		t.Fatal("exact candidate replay should match")
	}
	input.Port++
	if candidateMatchesInput(candidate, input) {
		t.Fatal("changed candidate must not match an existing stable candidate")
	}
}

func candidateInput(candidateType CandidateType, address string) CandidateInput {
	return CandidateInput{
		ConnectionID: "conn_test", Foundation: "candidate-1", CandidateType: candidateType,
		Protocol: "UDP", Address: address, Port: 7777, Priority: 100,
	}
}
