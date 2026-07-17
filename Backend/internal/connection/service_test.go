package connection

import "testing"

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

func candidateInput(candidateType CandidateType, address string) CandidateInput {
	return CandidateInput{
		ConnectionID: "conn_test", Foundation: "candidate-1", CandidateType: candidateType,
		Protocol: "UDP", Address: address, Port: 7777, Priority: 100,
	}
}
