package p2pbattlelog

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

func TestBuildDecisionPeerConfirmsPVPWithBothTeamObservers(t *testing.T) {
	result := normalizedPVPResult()
	reports := []ReportRecord{
		reportFor(t, "player_a", result, "outcome-a"),
		reportFor(t, "player_b", result, "outcome-a"),
	}
	decision := buildDecision(MatchSession{
		ID: "match", MatchType: "PVP", ExpectedReporterCount: 2, PolicyVersion: "p2p-v1",
	}, reports, time.Now().UTC())

	if decision.State != MatchPeerConfirmed || !decision.TeamCoverage || decision.TrustTier != "PEER_ATTESTED" {
		t.Fatalf("decision = %#v", decision)
	}
	if len(decision.Participants) != 2 || decision.Participants[0].StatsStatus != "CONSENSUS" {
		t.Fatalf("participants = %#v", decision.Participants)
	}
}

func TestBuildDecisionSingleHumanPVESelfReported(t *testing.T) {
	result := normalizedPVPResult()
	result.Outcome.MatchType = "PVE"
	result.Outcome.HumanTeams = result.Outcome.HumanTeams[:1]
	result.Participants = result.Participants[:1]
	reports := []ReportRecord{reportFor(t, "player_a", result, "outcome-pve")}

	decision := buildDecision(MatchSession{
		ID: "match", MatchType: "PVE", ExpectedReporterCount: 1, PolicyVersion: "p2p-v1",
	}, reports, time.Now().UTC())
	if decision.State != MatchSelfReported || decision.Participants[0].StatsStatus != "SELF_ONLY" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestBuildDecisionPVEPeerDepartureFallsBackToHighRiskSelfReport(t *testing.T) {
	result := normalizedPVPResult()
	result.Outcome.MatchType = "PVE"
	reports := []ReportRecord{reportFor(t, "player_a", result, "outcome-pve")}

	decision := buildDecision(MatchSession{
		ID: "match", MatchType: "PVE", ExpectedReporterCount: 2, PolicyVersion: "p2p-v1",
	}, reports, time.Now().UTC())
	if decision.State != MatchSelfReported || decision.RiskSeverity != "HIGH" ||
		!containsReason(decision.Reasons, "PVE_PEERS_MISSING_SELF_REPORT") {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestBuildDecisionKeepsEarlyLeaverMatchIncompleteUntilQuorum(t *testing.T) {
	result := normalizedPVPResult()
	decision := buildDecision(MatchSession{
		ID: "match", MatchType: "PVP", ExpectedReporterCount: 3, PolicyVersion: "p2p-v1",
	}, []ReportRecord{reportFor(t, "player_a", result, "outcome-a")}, time.Now().UTC())

	if decision.State != MatchIncomplete || !containsReason(decision.Reasons, "REPORT_QUORUM_NOT_REACHED") {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestBuildDecisionDisputesConflictingOutcomes(t *testing.T) {
	result := normalizedPVPResult()
	reports := []ReportRecord{
		reportFor(t, "player_a", result, "outcome-a"),
		reportFor(t, "player_b", result, "outcome-b"),
	}
	decision := buildDecision(MatchSession{
		ID: "match", MatchType: "PVP", ExpectedReporterCount: 2, PolicyVersion: "p2p-v1",
	}, reports, time.Now().UTC())

	if decision.State != MatchDisputed || decision.RiskSeverity != "HIGH" {
		t.Fatalf("decision = %#v", decision)
	}
}

func normalizedPVPResult() NormalizedResult {
	return NormalizedResult{
		Outcome: NormalizedOutcome{
			MatchType: "PVP", ModeAlias: "pvp", MapAlias: "map", WinnerTeamID: 1,
			TeamScores: []int{5, 3},
			HumanTeams: []NormalizedTeamMember{{PlayerID: "player_a", TeamID: 1}, {PlayerID: "player_b", TeamID: 2}},
			Rounds:     []NormalizedRound{{RoundIndex: 0, WinnerTeamID: 1, TeamScores: []int{5, 3}, IsFinalRound: true}},
		},
		Participants: []NormalizedParticipant{
			{PlayerID: "player_a", TeamID: 1, Outcome: "WON", Kills: 5, Deaths: 2, Assists: 1, Score: 100},
			{PlayerID: "player_b", TeamID: 2, Outcome: "LOST", Kills: 2, Deaths: 5, Score: 50},
		},
	}
}

func reportFor(t *testing.T, reporter string, result NormalizedResult, outcomeSeed string) ReportRecord {
	t.Helper()
	normalized, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	outcome := sha256.Sum256([]byte(outcomeSeed))
	stats := sha256.Sum256(normalized)
	return ReportRecord{
		ReporterPlayerID: reporter, NormalizedResult: normalized,
		OutcomeSHA256: outcome[:], StatsSHA256: stats[:], ValidationStatus: "ACCEPTED",
	}
}

func containsReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if reason == expected {
			return true
		}
	}
	return false
}
