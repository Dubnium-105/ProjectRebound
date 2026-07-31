package p2pbattlelog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizeSnapshotAcceptsBoundRosterAndHashChainedTimeline(t *testing.T) {
	session, capability, roster, snapshot := validSnapshotFixture("PVP")
	raw := marshalSnapshot(t, snapshot)

	result, err := normalizeSnapshot(raw, session, capability, roster, 32)
	if err != nil {
		t.Fatalf("normalizeSnapshot() error = %v", err)
	}
	if result.ValidationStatus != "ACCEPTED" || result.RiskSeverity != "" {
		t.Fatalf("validation = %s/%s, warnings=%#v", result.ValidationStatus, result.RiskSeverity, result.Warnings)
	}
	if len(result.Result.Participants) != 2 || result.Result.Participants[0].PlayerID != "player_a" {
		t.Fatalf("participants = %#v", result.Result.Participants)
	}
	if len(result.OutcomeDigest) != sha256.Size || len(result.StatsDigest) != sha256.Size {
		t.Fatal("normalized digests are not SHA-256 values")
	}
}

func TestNormalizeSnapshotRejectsTimelinePayloadTampering(t *testing.T) {
	session, capability, roster, snapshot := validSnapshotFixture("PVP")
	snapshot.Timeline.Events[1].Payload = json.RawMessage(`{"winner_team_id":2}`)

	_, err := normalizeSnapshot(marshalSnapshot(t, snapshot), session, capability, roster, 32)
	if errorCode(err) != "P2P_BATTLELOG_INVALID_TIMELINE" {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeSnapshotQuarantinesUnexpectedHuman(t *testing.T) {
	session, capability, roster, snapshot := validSnapshotFixture("PVP")
	snapshot.Players = append(snapshot.Players, rawHuman("76561198000000999", 1, 0, 0, 0, 0))

	result, err := normalizeSnapshot(marshalSnapshot(t, snapshot), session, capability, roster, 32)
	if err != nil {
		t.Fatalf("normalizeSnapshot() error = %v", err)
	}
	if result.ValidationStatus != "QUARANTINED" || !hasWarning(result.Warnings, "P2P_BATTLELOG_UNEXPECTED_HUMAN") {
		t.Fatalf("validation = %s, warnings=%#v", result.ValidationStatus, result.Warnings)
	}
}

func TestNormalizeSnapshotRejectsInvalidElapsedTime(t *testing.T) {
	session, capability, roster, snapshot := validSnapshotFixture("PVP")
	snapshot.GameState.ElapsedTime = -1

	_, err := normalizeSnapshot(marshalSnapshot(t, snapshot), session, capability, roster, 32)
	if errorCode(err) != "P2P_BATTLELOG_INVALID_SNAPSHOT" {
		t.Fatalf("error = %v", err)
	}
}

func validSnapshotFixture(matchType string) (MatchSession, Capability, []RosterMember, RawSnapshot) {
	session := MatchSession{ID: "p2pm_test", MatchType: matchType}
	capability := Capability{ID: "p2rc_test", PlayerID: "player_a", ServerNonce: "nonce_test"}
	roster := []RosterMember{
		{PlayerID: "player_a", PlatformID: "76561198000000001", EligibleReporter: true},
		{PlayerID: "player_b", PlatformID: "76561198000000002", EligibleReporter: true},
	}
	snapshot := RawSnapshot{
		Schema: SchemaName, SchemaVersion: SchemaVersion,
		P2PMatchID: session.ID, CapabilityID: capability.ID, ServerNonce: capability.ServerNonce,
		AuthorityKind: "CLIENT_OBSERVER", ClientVersion: "test-1", TimelineSessionID: "timeline-test",
		ReportCompleteness: "FINAL", ReportRevision: 1, CapturedAtUTC: time.Unix(1_700_000_000, 0).UTC(),
		MatchClassification: RawMatchClassification{Type: matchType, IsPvP: matchType == "PVP", IsPvE: matchType == "PVE"},
		GameState: RawGameState{
			ElapsedTime: 120, MapAliasName: "map_test", Mode: RawMode{AliasName: strings.ToLower(matchType)},
			MatchResult: RawMatchResult{WinnerTeamID: 1, TeamScores: []int{5, 3}, Rounds: []RawRoundResult{{WinnerTeamID: 1, TeamScores: []int{5, 3}, IsFinalRound: true}}},
		},
		Players: []RawPlayer{
			rawHuman("76561198000000001", 1, 5, 2, 1, 100),
			rawHuman("76561198000000002", 2, 2, 5, 0, 50),
		},
	}
	snapshot.Timeline = signedTimeline(snapshot, capability)
	return session, capability, roster, snapshot
}

func rawHuman(platformID string, teamID, kills, deaths, assists int, score float64) RawPlayer {
	return RawPlayer{
		Assignment: RawAssignment{TeamID: teamID}, Identity: RawIdentity{PlatformID: platformID},
		ComputedStats: RawComputedStats{Kill: kills, Death: deaths, Assist: assists, PlayerScore: score},
		RawFields:     RawPlayerFields{NumKills: kills, NumDeaths: deaths, NumAssists: assists, Score: score},
		Outcome:       RawOutcome{IsMatchWinner: teamID == 1},
	}
}

func signedTimeline(snapshot RawSnapshot, capability Capability) RawTimeline {
	root := sha256.Sum256([]byte(strings.Join([]string{
		snapshot.P2PMatchID, capability.ID, capability.ServerNonce, snapshot.TimelineSessionID,
	}, "|")))
	previous := hex.EncodeToString(root[:])
	events := []RawTimelineEvent{
		{Seq: 1, Type: "MATCH_STARTED", LocalMonotonicMS: 0, Payload: json.RawMessage(`{}`)},
		{Seq: 2, Type: "MATCH_ENDED", LocalMonotonicMS: 120000, Payload: json.RawMessage(`{"winner_team_id":1}`)},
	}
	for index := range events {
		events[index].PreviousEventHash = previous
		events[index].EventHash, _ = timelineEventDigest(previous, events[index])
		previous = events[index].EventHash
	}
	return RawTimeline{FirstSeq: 1, LastSeq: 2, EventsDigest: previous, Events: events}
}

func marshalSnapshot(t *testing.T, snapshot RawSnapshot) []byte {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := errorDetails(err)
	return code
}

func hasWarning(warnings []Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
