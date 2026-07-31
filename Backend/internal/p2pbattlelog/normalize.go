package p2pbattlelog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maximumPlayers    = 256
	maximumRounds     = 512
	maximumCounter    = 10_000_000
	maximumScore      = 1_000_000_000_000
	maximumTimelineMS = 7 * 24 * 60 * 60 * 1000
)

var (
	reportIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	platformIDPattern = regexp.MustCompile(`^[0-9]{16,20}$`)
	hexDigestPattern  = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

type normalizedSnapshot struct {
	Snapshot         RawSnapshot
	CanonicalRaw     []byte
	RawDigest        []byte
	Result           NormalizedResult
	NormalizedJSON   []byte
	OutcomeDigest    []byte
	StatsDigest      []byte
	Warnings         []Warning
	ValidationStatus string
	RiskSeverity     string
}

func normalizeSnapshot(
	raw []byte,
	session MatchSession,
	capability Capability,
	roster []RosterMember,
	maximumEvents int,
) (normalizedSnapshot, error) {
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, raw); err != nil {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT",
			"The P2P BattleLog snapshot is not valid JSON.",
			map[string]any{"snapshot": err.Error()},
		)
	}
	var snapshot RawSnapshot
	if err := json.Unmarshal(canonical.Bytes(), &snapshot); err != nil {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT",
			"The P2P BattleLog snapshot cannot be decoded.",
			map[string]any{"snapshot": err.Error()},
		)
	}
	if snapshot.Schema != SchemaName || snapshot.SchemaVersion != SchemaVersion {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_SCHEMA_UNSUPPORTED",
			"The P2P BattleLog schema is not supported.",
			map[string]any{"schema": SchemaName, "schema_version": SchemaVersion},
		)
	}
	if snapshot.P2PMatchID != session.ID || snapshot.CapabilityID != capability.ID ||
		snapshot.ServerNonce != capability.ServerNonce {
		return normalizedSnapshot{}, forbidden(
			"P2P_BATTLELOG_CONTEXT_MISMATCH",
			"The report context does not match the authenticated P2P match capability.",
		)
	}
	if snapshot.CapturedAtUTC.IsZero() {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "captured_at_utc is required.", nil,
		)
	}
	if snapshot.ReportRevision < 1 {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "report_revision must be positive.", nil,
		)
	}
	if snapshot.ReportCompleteness != "PARTIAL" && snapshot.ReportCompleteness != "FINAL" {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "report_completeness must be PARTIAL or FINAL.", nil,
		)
	}
	if snapshot.AuthorityKind != "CLIENT_OBSERVER" && snapshot.AuthorityKind != "LISTEN_HOST_OBSERVER" {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_AUTHORITY",
			"P2P reports must identify a client or listen-host observer.", nil,
		)
	}
	if strings.TrimSpace(snapshot.ClientVersion) == "" || len(snapshot.ClientVersion) > 64 ||
		strings.TrimSpace(snapshot.TimelineSessionID) == "" || len(snapshot.TimelineSessionID) > 128 {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT",
			"client_version and timeline_session_id are required and must have valid lengths.", nil,
		)
	}
	if len(snapshot.Players) == 0 || len(snapshot.Players) > maximumPlayers {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT",
			"players must contain between 1 and 256 entries.", nil,
		)
	}
	if len(snapshot.GameState.MatchResult.Rounds) > maximumRounds {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "Too many match rounds were reported.", nil,
		)
	}
	if !finiteNonNegative(snapshot.GameState.ElapsedTime) ||
		snapshot.GameState.ElapsedTime > float64(maximumTimelineMS)/1000 {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "elapsed_time is outside the supported range.", nil,
		)
	}
	if err := validateTimeline(snapshot, capability, maximumEvents); err != nil {
		return normalizedSnapshot{}, err
	}

	result := normalizedSnapshot{Snapshot: snapshot, CanonicalRaw: canonical.Bytes()}
	rawDigest := sha256.Sum256(canonical.Bytes())
	result.RawDigest = rawDigest[:]
	if (!session.CreatedAt.IsZero() && snapshot.CapturedAtUTC.Before(session.CreatedAt.Add(-10*time.Minute))) ||
		(!session.HardExpiresAt.IsZero() && snapshot.CapturedAtUTC.After(session.HardExpiresAt.Add(10*time.Minute))) {
		result.addWarning(
			"P2P_BATTLELOG_CAPTURE_TIME_OUTSIDE_MATCH", "HIGH",
			"The capture timestamp is outside the server-created match window.", true,
		)
	}
	if snapshot.Timeline.TimelineTruncated {
		result.addWarning(
			"P2P_BATTLELOG_TIMELINE_TRUNCATED", "MEDIUM",
			"The local timeline reached its configured capacity.", false,
		)
	}
	if snapshot.ReportCompleteness == "PARTIAL" {
		result.addWarning(
			"P2P_BATTLELOG_PARTIAL_REPORT", "MEDIUM",
			"The player left or the game ended before a final snapshot was sealed.", false,
		)
	}

	platformRoster := make(map[string]RosterMember, len(roster))
	for _, member := range roster {
		platformRoster[member.PlatformID] = member
	}

	matchType := normalizeMatchType(snapshot.MatchClassification.Type)
	classificationContradicts := (matchType == "PVE" && (!snapshot.MatchClassification.IsPvE || snapshot.MatchClassification.IsPvP)) ||
		(matchType == "PVP" && (!snapshot.MatchClassification.IsPvP || snapshot.MatchClassification.IsPvE)) ||
		(matchType == "UNKNOWN" && (snapshot.MatchClassification.IsPvE || snapshot.MatchClassification.IsPvP))
	if classificationContradicts {
		result.addWarning(
			"P2P_BATTLELOG_CLASSIFICATION_CONFLICT", "HIGH",
			"The match type and PvE/PvP classification flags contradict each other.", true,
		)
	}
	if session.MatchType != "UNKNOWN" && matchType != session.MatchType {
		result.addWarning(
			"P2P_BATTLELOG_MATCH_TYPE_CONFLICT", "HIGH",
			"The report match type conflicts with the server-side room snapshot.", true,
		)
		matchType = session.MatchType
	}
	if matchType == "UNKNOWN" {
		result.addWarning(
			"P2P_BATTLELOG_MATCH_TYPE_UNKNOWN", "MEDIUM",
			"The report did not provide an unambiguous PvE or PvP classification.", false,
		)
	}

	modeAlias := strings.TrimSpace(snapshot.GameState.Mode.AliasName)
	mapAlias := strings.TrimSpace(snapshot.GameState.MapAliasName)
	if len(modeAlias) > 128 || len(mapAlias) > 128 {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "Mode or map identifiers are too long.", nil,
		)
	}
	if err := validateTeamScores(snapshot.GameState.MatchResult.TeamScores); err != nil {
		return normalizedSnapshot{}, err
	}
	if snapshot.GameState.MatchResult.WinnerTeamID < -1 || snapshot.GameState.MatchResult.WinnerTeamID > 255 {
		return normalizedSnapshot{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "winner_team_id is outside the supported range.", nil,
		)
	}

	result.Result.Outcome = NormalizedOutcome{
		MatchType:    matchType,
		ModeAlias:    modeAlias,
		MapAlias:     mapAlias,
		WinnerTeamID: snapshot.GameState.MatchResult.WinnerTeamID,
		TeamScores:   slices.Clone(snapshot.GameState.MatchResult.TeamScores),
	}
	for index, round := range snapshot.GameState.MatchResult.Rounds {
		if err := validateTeamScores(round.TeamScores); err != nil {
			return normalizedSnapshot{}, err
		}
		if round.WinnerTeamID < -1 || round.WinnerTeamID > 255 {
			return normalizedSnapshot{}, unprocessable(
				"P2P_BATTLELOG_INVALID_SNAPSHOT", "A round winner_team_id is outside the supported range.", nil,
			)
		}
		result.Result.Outcome.Rounds = append(result.Result.Outcome.Rounds, NormalizedRound{
			RoundIndex: index, WinnerTeamID: round.WinnerTeamID,
			TeamScores: slices.Clone(round.TeamScores), IsFinalRound: round.IsFinalRound,
		})
	}

	seenHumans := make(map[string]struct{}, len(roster))
	reporterPresent := false
	for index, rawPlayer := range snapshot.Players {
		if rawPlayer.RawFields.IsBot {
			continue
		}
		platformID := strings.TrimSpace(rawPlayer.Identity.PlatformID)
		if !platformIDPattern.MatchString(platformID) {
			result.addWarning(
				"P2P_BATTLELOG_HUMAN_IDENTITY_INVALID", "HIGH",
				fmt.Sprintf("Human participant %d does not contain a valid platform identity.", index), true,
			)
			continue
		}
		member, exists := platformRoster[platformID]
		if !exists {
			result.addWarning(
				"P2P_BATTLELOG_UNEXPECTED_HUMAN", "HIGH",
				fmt.Sprintf("Human participant %d is not in the server-frozen roster.", index), true,
			)
			continue
		}
		if _, duplicate := seenHumans[member.PlayerID]; duplicate {
			result.addWarning(
				"P2P_BATTLELOG_DUPLICATE_HUMAN", "HIGH",
				fmt.Sprintf("Player %s appears more than once in the report.", member.PlayerID), true,
			)
			continue
		}
		seenHumans[member.PlayerID] = struct{}{}
		if member.PlayerID == capability.PlayerID {
			reporterPresent = true
		}
		if err := validatePlayerCounters(rawPlayer); err != nil {
			return normalizedSnapshot{}, err
		}
		if rawPlayer.RawFields.NumKills != rawPlayer.ComputedStats.Kill ||
			rawPlayer.RawFields.NumDeaths != rawPlayer.ComputedStats.Death ||
			rawPlayer.RawFields.NumAssists != rawPlayer.ComputedStats.Assist {
			result.addWarning(
				"P2P_BATTLELOG_COUNTER_CONFLICT", "HIGH",
				fmt.Sprintf("Raw and computed counters disagree for player %s.", member.PlayerID), true,
			)
		}
		teamID := rawPlayer.Assignment.TeamID
		if teamID < -1 || teamID > 255 {
			return normalizedSnapshot{}, unprocessable(
				"P2P_BATTLELOG_INVALID_SNAPSHOT", "A participant team_id is outside the supported range.", nil,
			)
		}
		participant := NormalizedParticipant{
			PlayerID: member.PlayerID, TeamID: teamID,
			Outcome: normalizedOutcome(
				teamID, snapshot.GameState.MatchResult.WinnerTeamID,
				snapshot.GameState.MatchResult.TeamScores, rawPlayer.Outcome.IsMatchWinner,
			),
			Kills: rawPlayer.ComputedStats.Kill, Deaths: rawPlayer.ComputedStats.Death,
			Assists:   rawPlayer.ComputedStats.Assist,
			Score:     normalizedScore(rawPlayer.ComputedStats.PlayerScore),
			IsQuitter: rawPlayer.Outcome.IsQuitter, IsInactive: rawPlayer.RawFields.IsInactive,
		}
		result.Result.Participants = append(result.Result.Participants, participant)
		result.Result.Outcome.HumanTeams = append(
			result.Result.Outcome.HumanTeams,
			NormalizedTeamMember{PlayerID: member.PlayerID, TeamID: teamID},
		)
	}
	if !reporterPresent {
		result.addWarning(
			"P2P_BATTLELOG_REPORTER_MISSING", "CRITICAL",
			"The authenticated reporter is absent from its own player snapshot.", true,
		)
	}
	for _, member := range roster {
		if !member.EligibleReporter || member.IsSpectator {
			continue
		}
		if _, exists := seenHumans[member.PlayerID]; !exists {
			result.addWarning(
				"P2P_BATTLELOG_ROSTER_MEMBER_MISSING", "MEDIUM",
				fmt.Sprintf("Roster player %s is absent from this observer's final player array.", member.PlayerID), false,
			)
		}
	}
	sort.Slice(result.Result.Participants, func(i, j int) bool {
		return result.Result.Participants[i].PlayerID < result.Result.Participants[j].PlayerID
	})
	sort.Slice(result.Result.Outcome.HumanTeams, func(i, j int) bool {
		return result.Result.Outcome.HumanTeams[i].PlayerID < result.Result.Outcome.HumanTeams[j].PlayerID
	})

	outcomeJSON, err := json.Marshal(result.Result.Outcome)
	if err != nil {
		return normalizedSnapshot{}, internal(fmt.Errorf("marshal normalized P2P outcome: %w", err))
	}
	statsJSON, err := json.Marshal(result.Result.Participants)
	if err != nil {
		return normalizedSnapshot{}, internal(fmt.Errorf("marshal normalized P2P statistics: %w", err))
	}
	result.NormalizedJSON, err = json.Marshal(result.Result)
	if err != nil {
		return normalizedSnapshot{}, internal(fmt.Errorf("marshal normalized P2P report: %w", err))
	}
	outcomeDigest := sha256.Sum256(outcomeJSON)
	statsDigest := sha256.Sum256(statsJSON)
	result.OutcomeDigest = outcomeDigest[:]
	result.StatsDigest = statsDigest[:]
	result.finishValidation()
	return result, nil
}

func validateTimeline(snapshot RawSnapshot, capability Capability, maximumEvents int) error {
	events := snapshot.Timeline.Events
	if len(events) == 0 || len(events) > maximumEvents {
		return unprocessable(
			"P2P_BATTLELOG_INVALID_TIMELINE",
			"timeline.events must contain a bounded non-empty event sequence.",
			map[string]any{"maximum_events": maximumEvents},
		)
	}
	if events[0].Type != "MATCH_STARTED" {
		return unprocessable(
			"P2P_BATTLELOG_INVALID_TIMELINE", "The timeline must begin with MATCH_STARTED.", nil,
		)
	}
	root := sha256.Sum256([]byte(strings.Join([]string{
		snapshot.P2PMatchID, snapshot.CapabilityID, snapshot.ServerNonce, snapshot.TimelineSessionID,
	}, "|")))
	expectedPrevious := hex.EncodeToString(root[:])
	previousSeq := uint64(0)
	previousTime := uint64(0)
	matchEnded := false
	for index, event := range events {
		if event.Seq == 0 || (index > 0 && event.Seq != previousSeq+1) {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_TIMELINE", "Timeline event sequence numbers are not contiguous.", nil,
			)
		}
		if index == 0 && snapshot.Timeline.FirstSeq != event.Seq ||
			index == len(events)-1 && snapshot.Timeline.LastSeq != event.Seq {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_TIMELINE", "Timeline first_seq or last_seq does not match its events.", nil,
			)
		}
		if event.LocalMonotonicMS > maximumTimelineMS || event.LocalMonotonicMS < previousTime {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_TIMELINE", "Timeline monotonic time is invalid.", nil,
			)
		}
		if !allowedTimelineEvent(event.Type) || !hexDigestPattern.MatchString(event.EventHash) ||
			!strings.EqualFold(event.PreviousEventHash, expectedPrevious) {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_TIMELINE", "Timeline event type or hash linkage is invalid.", nil,
			)
		}
		if len(event.Payload) > 64*1024 || (len(event.Payload) > 0 && !json.Valid(event.Payload)) {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_TIMELINE", "A timeline event payload is invalid or too large.", nil,
			)
		}
		calculatedHash, err := timelineEventDigest(expectedPrevious, event)
		if err != nil || !strings.EqualFold(event.EventHash, calculatedHash) {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_TIMELINE", "A timeline event digest is invalid.", nil,
			)
		}
		if event.Type == "MATCH_ENDED" {
			if matchEnded || index != len(events)-1 {
				return unprocessable(
					"P2P_BATTLELOG_INVALID_TIMELINE", "MATCH_ENDED must occur exactly once at the timeline tail.", nil,
				)
			}
			matchEnded = true
		}
		expectedPrevious = strings.ToLower(event.EventHash)
		previousSeq = event.Seq
		previousTime = event.LocalMonotonicMS
	}
	if !strings.EqualFold(snapshot.Timeline.EventsDigest, expectedPrevious) {
		return unprocessable(
			"P2P_BATTLELOG_INVALID_TIMELINE", "events_digest does not match the timeline tail.", nil,
		)
	}
	if snapshot.ReportCompleteness == "FINAL" && !matchEnded {
		return unprocessable(
			"P2P_BATTLELOG_INVALID_TIMELINE", "A FINAL report must contain MATCH_ENDED.", nil,
		)
	}
	return nil
}

func timelineEventDigest(previousHash string, event RawTimelineEvent) (string, error) {
	payload := []byte("null")
	if len(event.Payload) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, event.Payload); err != nil {
			return "", err
		}
		payload = compact.Bytes()
	}
	payloadDigest := sha256.Sum256(payload)
	preimage := strings.Join([]string{
		strings.ToLower(previousHash), strconv.FormatUint(event.Seq, 10), event.Type,
		strconv.FormatUint(event.LocalMonotonicMS, 10), hex.EncodeToString(payloadDigest[:]),
	}, "|")
	digest := sha256.Sum256([]byte(preimage))
	return hex.EncodeToString(digest[:]), nil
}

func compactJSONDigest(raw []byte) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(compact.Bytes())
	return digest[:], nil
}

func allowedTimelineEvent(eventType string) bool {
	switch eventType {
	case "MATCH_STARTED", "ROSTER_CHECKPOINT", "PLAYER_CONNECTED", "PLAYER_BECAME_ACTIVE",
		"PLAYER_DISCONNECTED", "PLAYER_RECONNECTED", "PLAYER_LEFT", "ROUND_STARTED",
		"ROUND_ENDED", "STAT_CHECKPOINT", "MATCH_ENDED", "PLAYER_KILLED",
		"ASSIST_GRANTED", "OBJECTIVE_SCORE_CHANGED":
		return true
	default:
		return false
	}
}

func validateTeamScores(scores []int) error {
	if len(scores) > 64 {
		return unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "Too many team scores were reported.", nil,
		)
	}
	for _, score := range scores {
		if score < 0 || score > maximumCounter {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_SNAPSHOT", "A team score is outside the supported range.", nil,
			)
		}
	}
	return nil
}

func validatePlayerCounters(player RawPlayer) error {
	for name, value := range map[string]int{
		"kill": player.ComputedStats.Kill, "death": player.ComputedStats.Death,
		"assist": player.ComputedStats.Assist, "raw_kill": player.RawFields.NumKills,
		"raw_death": player.RawFields.NumDeaths, "raw_assist": player.RawFields.NumAssists,
	} {
		if value < 0 || value > maximumCounter {
			return unprocessable(
				"P2P_BATTLELOG_INVALID_SNAPSHOT",
				fmt.Sprintf("Participant counter %s is outside the supported range.", name), nil,
			)
		}
	}
	if !finiteNonNegative(player.ComputedStats.PlayerScore) ||
		player.ComputedStats.PlayerScore > maximumScore ||
		!finiteNonNegative(player.RawFields.Score) || player.RawFields.Score > maximumScore {
		return unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "A participant score is outside the supported range.", nil,
		)
	}
	return nil
}

func normalizedOutcome(teamID, winnerTeamID int, scores []int, winnerFlag bool) string {
	if len(scores) >= 2 {
		allEqual := true
		for _, score := range scores[1:] {
			if score != scores[0] {
				allEqual = false
				break
			}
		}
		if allEqual {
			return "DRAW"
		}
	}
	if winnerFlag || teamID == winnerTeamID {
		return "WON"
	}
	if winnerTeamID >= 0 {
		return "LOST"
	}
	return "UNKNOWN"
}

func normalizeMatchType(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PVE":
		return "PVE"
	case "PVP":
		return "PVP"
	default:
		return "UNKNOWN"
	}
}

func normalizedScore(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func (result *normalizedSnapshot) addWarning(code, severity, message string, quarantine bool) {
	result.Warnings = append(result.Warnings, Warning{
		Code: code, Severity: severity, Message: message, Quarantine: quarantine,
	})
}

func (result *normalizedSnapshot) finishValidation() {
	result.ValidationStatus = "ACCEPTED"
	for _, warning := range result.Warnings {
		if severityRank(warning.Severity) > severityRank(result.RiskSeverity) {
			result.RiskSeverity = warning.Severity
		}
		if warning.Quarantine {
			result.ValidationStatus = "QUARANTINED"
		}
	}
	if result.ValidationStatus != "QUARANTINED" && len(result.Warnings) > 0 {
		result.ValidationStatus = "ACCEPTED_WITH_WARNINGS"
	}
}

func severityRank(value string) int {
	switch value {
	case "LOW":
		return 1
	case "MEDIUM":
		return 2
	case "HIGH":
		return 3
	case "CRITICAL":
		return 4
	default:
		return 0
	}
}
