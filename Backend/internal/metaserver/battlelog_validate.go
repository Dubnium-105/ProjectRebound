package metaserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const maximumBattleLogPlayers = 256
const maximumBattleLogRounds = 512
const maximumBattleLogDurationSeconds = 7 * 24 * 60 * 60

func normalizeBattleLogSnapshot(raw json.RawMessage) (normalizedBattleLog, error) {
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, raw); err != nil {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"snapshot": "must be valid JSON"},
		)
	}
	var snapshot battleLogSnapshot
	if err := json.Unmarshal(canonical.Bytes(), &snapshot); err != nil {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"snapshot": err.Error()},
		)
	}
	if snapshot.Schema != battleLogSchemaName || snapshot.SchemaVersion != battleLogSchemaVersion {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_SCHEMA_UNSUPPORTED",
			map[string]any{
				"schema":         battleLogSchemaName,
				"schema_version": battleLogSchemaVersion,
			},
		)
	}
	if !strings.EqualFold(strings.TrimSpace(snapshot.Source), "server") {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_SERVER_SNAPSHOT_REQUIRED",
			map[string]any{"source": "must be server"},
		)
	}
	if snapshot.CapturedAtUTC.IsZero() {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"captured_at_utc": "is required"},
		)
	}
	if len(snapshot.Players) > maximumBattleLogPlayers {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"players": "must contain at most 256 entries"},
		)
	}
	if !finiteNonNegative(snapshot.GameState.ElapsedTime) ||
		snapshot.GameState.ElapsedTime > maximumBattleLogDurationSeconds {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{
				"game_state.elapsed_time": "must be between zero and seven days",
			},
		)
	}
	if len(snapshot.GameState.MatchResult.Rounds) > maximumBattleLogRounds {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"game_state.match_result.rounds": "must contain at most 512 entries"},
		)
	}
	if len(strings.TrimSpace(snapshot.MatchID)) > 128 {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"match_id": "must contain at most 128 characters"},
		)
	}
	if err := validateBattleLogSummary(snapshot); err != nil {
		return normalizedBattleLog{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"participant_summary": err.Error()},
		)
	}

	digest := sha256.Sum256(canonical.Bytes())
	result := normalizedBattleLog{
		Snapshot:     snapshot,
		CanonicalRaw: canonical.Bytes(),
		Digest:       digest[:],
		DurationMS:   int64(math.Round(snapshot.GameState.ElapsedTime * 1000)),
		Rounds:       snapshot.GameState.MatchResult.Rounds,
	}
	result.MatchType = normalizedMatchType(snapshot.MatchClassification.Type)
	result.ModeAlias = truncateBattleLog(
		classificationEvidenceString(snapshot.MatchClassification.Evidence, "mode_alias"),
		128,
	)
	result.validateClassification()

	seenPlatformIDs := make(map[string]int)
	for index, encoded := range snapshot.Players {
		participant, err := normalizeBattleLogParticipant(encoded, index, result.DurationMS)
		if err != nil {
			return normalizedBattleLog{}, err
		}
		if !participant.IsAI && participant.PlatformID != "" {
			if len(participant.PlatformID) > 20 {
				return normalizedBattleLog{}, unprocessable(
					"BATTLELOG_INVALID_SNAPSHOT",
					map[string]any{
						"players": fmt.Sprintf(
							"participant %d platform identity is too long",
							index,
						),
					},
				)
			}
			if firstIndex, exists := seenPlatformIDs[participant.PlatformID]; exists {
				result.addWarning(
					"BATTLELOG_DUPLICATE_PLATFORM_ID", "HIGH",
					fmt.Sprintf("participants %d and %d use the same platform identity", firstIndex, index),
					&index, true,
				)
			} else {
				seenPlatformIDs[participant.PlatformID] = index
			}
		}
		if !participant.IsAI && participant.PlatformID == "" {
			result.addWarning(
				"BATTLELOG_HUMAN_IDENTITY_MISSING", "MEDIUM",
				"human participant does not contain a platform identity",
				&index, false,
			)
		}
		result.Participants = append(result.Participants, participant)
	}
	result.buildTeams()
	result.validateSummary()
	result.finishValidation()
	return result, nil
}

func (result *normalizedBattleLog) validateClassification() {
	pvePresent := jsonValuePresent(result.Snapshot.PvERecord)
	pvpPresent := jsonValuePresent(result.Snapshot.PvPRecord)
	classification := result.Snapshot.MatchClassification
	switch result.MatchType {
	case BattleLogPvE:
		if !classification.IsPvE || classification.IsPvP || !pvePresent || pvpPresent {
			result.addWarning(
				"BATTLELOG_PVE_RECORD_CONFLICT", "HIGH",
				"PvE classification and PvE/PvP record presence disagree",
				nil, true,
			)
		}
	case BattleLogPvP:
		if classification.IsPvE || !classification.IsPvP || pvePresent || !pvpPresent {
			result.addWarning(
				"BATTLELOG_PVP_RECORD_CONFLICT", "HIGH",
				"PvP classification and PvE/PvP record presence disagree",
				nil, true,
			)
		}
	default:
		result.addWarning(
			"BATTLELOG_MATCH_TYPE_UNKNOWN", "MEDIUM",
			"server snapshot did not produce an authoritative PvE or PvP classification",
			nil, true,
		)
	}
	modeEvidence := strings.ToUpper(
		result.ModeAlias + " " +
			classificationEvidenceString(classification.Evidence, "game_mode_full_name") + " " +
			classificationEvidenceString(classification.Evidence, "configured_mode_path"),
	)
	if strings.Contains(modeEvidence, "PVE") && result.MatchType == BattleLogPvP {
		result.addWarning(
			"BATTLELOG_MODE_CLASSIFICATION_CONFLICT", "HIGH",
			"runtime mode evidence identifies PvE but the report is classified as PvP",
			nil, true,
		)
	}
}

func normalizeBattleLogParticipant(
	raw json.RawMessage,
	index int,
	durationMS int64,
) (normalizedBattleLogParticipant, error) {
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, raw); err != nil {
		return normalizedBattleLogParticipant{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"players": fmt.Sprintf("participant %d is not valid JSON", index)},
		)
	}
	var source battleLogRawParticipant
	if err := json.Unmarshal(canonical.Bytes(), &source); err != nil {
		return normalizedBattleLogParticipant{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"players": fmt.Sprintf("participant %d: %v", index, err)},
		)
	}
	if err := validateParticipantNumbers(source); err != nil {
		return normalizedBattleLogParticipant{}, unprocessable(
			"BATTLELOG_INVALID_SNAPSHOT",
			map[string]any{"players": fmt.Sprintf("participant %d: %v", index, err)},
		)
	}
	kdaDenominator := source.RawFields.NumDeaths
	if kdaDenominator < 1 {
		kdaDenominator = 1
	}
	calculatedKDA := float64(source.RawFields.NumKills+source.RawFields.NumAssists) /
		float64(kdaDenominator)
	calculatedSPM := 0.0
	if durationMS > 0 {
		calculatedSPM = source.RawFields.Score * 60000 / float64(durationMS)
	}
	platformID := strings.TrimSpace(source.Identity.PlatformID)
	if platformID == "" {
		platformID = strings.TrimSpace(source.Identity.UserID)
	}
	return normalizedBattleLogParticipant{
		SlotIndex:          index,
		PlatformID:         platformID,
		PlayerName:         truncateBattleLog(strings.TrimSpace(source.Identity.PlayerName), 256),
		IsAI:               source.RawFields.IsBot,
		TeamID:             source.Assignment.TeamID,
		CampID:             source.Assignment.CampID,
		RoleName:           truncateBattleLog(strings.TrimSpace(source.Assignment.Role.Name), 128),
		RoleValue:          source.Assignment.Role.Value,
		SelectedCharacter:  truncateBattleLog(strings.TrimSpace(source.Assignment.SelectedCharacterID), 128),
		PossessedCharacter: truncateBattleLog(strings.TrimSpace(source.Assignment.PossessedCharacterID), 128),
		IsSpectator:        source.RawFields.IsSpectator,
		IsInactive:         source.RawFields.IsInactive,
		IsQuitter:          source.Outcome.IsQuitter,
		Outcome:            normalizedOutcome(source.Outcome.MatchResult.Name, source.Outcome.IsMatchWinner),
		IsMatchMVP:         source.Outcome.IsMatchMVP,
		RawPlayer:          canonical.Bytes(),
		Kills:              source.RawFields.NumKills,
		Deaths:             source.RawFields.NumDeaths,
		Assists:            source.RawFields.NumAssists,
		Score:              source.RawFields.Score,
		TeamScore:          source.ComputedStats.TeamScore,
		Headshots:          source.ComputedStats.HeadshotCount,
		BulletsFired:       source.RawFields.NumBulletsFired,
		RocketsFired:       source.RawFields.NumRocketsFired,
		MaxKillDistance:    source.ComputedStats.MaxKillDistance,
		AvgKillDistance:    source.ComputedStats.AvgKillDistance,
		MaxKillStreak:      source.ComputedStats.MaxKillStreakCount,
		KillingStreakCount: source.ComputedStats.KillingStreakCount,
		PingMS:             source.ComputedStats.PingMS,
		ReportedKDA:        source.ComputedStats.KDRatio,
		CalculatedKDA:      calculatedKDA,
		ReportedSPM:        source.ComputedStats.SPM,
		CalculatedSPM:      calculatedSPM,
		ReportedAccuracy:   source.ServerMatchResultInfo.Personal.Accuracy,
		PlayingTimeMS:      int64(math.Round(source.Assignment.PlayingGameTime * 1000)),
		CharacterScores:    source.MatchCharacterScoreMap,
		RoleScores:         source.RoleScoreMap,
	}, nil
}

func validateParticipantNumbers(source battleLogRawParticipant) error {
	integers := []int{
		source.RawFields.NumKills,
		source.RawFields.NumDeaths,
		source.RawFields.NumAssists,
		source.RawFields.NumBulletsFired,
		source.RawFields.NumRocketsFired,
		source.ComputedStats.HeadshotCount,
		source.ComputedStats.MaxKillStreakCount,
		source.ComputedStats.KillingStreakCount,
	}
	for _, value := range integers {
		if value < 0 {
			return errors.New("counters must be non-negative")
		}
	}
	numbers := []float64{
		source.RawFields.Score,
		source.ComputedStats.TeamScore,
		source.ComputedStats.MaxKillDistance,
		source.ComputedStats.AvgKillDistance,
		source.ComputedStats.PingMS,
		source.ComputedStats.KDRatio,
		source.ComputedStats.SPM,
		source.Assignment.PlayingGameTime,
	}
	for _, value := range numbers {
		if !finiteNonNegative(value) {
			return errors.New("statistics must be non-negative finite numbers")
		}
	}
	if source.ServerMatchResultInfo.Personal.Accuracy != nil &&
		!finiteNonNegative(*source.ServerMatchResultInfo.Personal.Accuracy) {
		return errors.New("accuracy must be a non-negative finite number")
	}
	for _, values := range [][]battleLogScoreEntry{
		source.MatchCharacterScoreMap,
		source.RoleScoreMap,
	} {
		for _, entry := range values {
			if strings.TrimSpace(entry.Key) == "" || !finiteNonNegative(entry.Value) {
				return errors.New("score breakdown entries must have a key and non-negative finite score")
			}
		}
	}
	return nil
}

func validateBattleLogSummary(snapshot battleLogSnapshot) error {
	buckets := []battleLogSummaryBucket{
		snapshot.ParticipantSummary.All,
		snapshot.ParticipantSummary.Humans,
		snapshot.ParticipantSummary.AI,
	}
	seenTeams := make(map[int]bool)
	for _, team := range snapshot.ParticipantSummary.Teams {
		if team.TeamID < 0 {
			return errors.New("team IDs must be non-negative")
		}
		if seenTeams[team.TeamID] {
			return errors.New("team IDs must be unique")
		}
		seenTeams[team.TeamID] = true
		buckets = append(buckets, team.All, team.Humans, team.AI)
	}
	for _, bucket := range buckets {
		if bucket.Count < 0 || bucket.Kills < 0 || bucket.Deaths < 0 ||
			bucket.Assists < 0 || !finiteNonNegative(bucket.Score) {
			return errors.New("summary counters must be non-negative finite numbers")
		}
	}
	for _, score := range snapshot.GameState.MatchResult.TeamScores {
		if score < 0 {
			return errors.New("match team scores must be non-negative")
		}
	}
	for _, round := range snapshot.GameState.MatchResult.Rounds {
		for _, score := range round.TeamScores {
			if score < 0 {
				return errors.New("round team scores must be non-negative")
			}
		}
	}
	return nil
}

func (result *normalizedBattleLog) buildTeams() {
	for _, team := range result.Snapshot.ParticipantSummary.Teams {
		var matchScore *int
		if team.TeamID >= 0 && team.TeamID < len(result.Snapshot.GameState.MatchResult.TeamScores) {
			value := result.Snapshot.GameState.MatchResult.TeamScores[team.TeamID]
			matchScore = &value
		}
		outcome := "LOST"
		if team.TeamID == result.Snapshot.GameState.MatchResult.WinnerTeamID {
			outcome = "WON"
		}
		result.Teams = append(result.Teams, normalizedBattleLogTeam{
			TeamID:     team.TeamID,
			Outcome:    outcome,
			MatchScore: matchScore,
			Kills:      team.All.Kills,
			Deaths:     team.All.Deaths,
			Assists:    team.All.Assists,
			Score:      team.All.Score,
			HumanCount: team.Humans.Count,
			AICount:    team.AI.Count,
		})
	}
}

func (result *normalizedBattleLog) validateSummary() {
	summary := result.Snapshot.ParticipantSummary
	if summary.All.Count != len(result.Participants) {
		result.addWarning(
			"BATTLELOG_PARTICIPANT_COUNT_MISMATCH", "LOW",
			"participant summary count does not match the players array",
			nil, false,
		)
	}
	humans, ai := 0, 0
	for _, participant := range result.Participants {
		if participant.IsAI {
			ai++
		} else {
			humans++
		}
	}
	if summary.Humans.Count != humans || summary.AI.Count != ai {
		result.addWarning(
			"BATTLELOG_PARTICIPANT_KIND_MISMATCH", "LOW",
			"human or AI summary count does not match the players array",
			nil, false,
		)
	}
}

func (result *normalizedBattleLog) addWarning(
	code, severity, message string,
	participantIndex *int,
	quarantine bool,
) {
	result.Warnings = append(result.Warnings, BattleLogWarning{
		Code: code, Severity: severity, Message: message,
		ParticipantIndex: participantIndex,
	})
	if riskRank(severity) > riskRank(result.RiskSeverity) {
		result.RiskSeverity = severity
	}
	if quarantine {
		result.Status = BattleLogQuarantined
	}
}

func (result *normalizedBattleLog) finishValidation() {
	if result.Status == BattleLogQuarantined {
		return
	}
	if len(result.Warnings) > 0 {
		result.Status = BattleLogAcceptedWithWarnings
		return
	}
	result.Status = BattleLogAccepted
}

func normalizedMatchType(value string) BattleLogMatchType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pve":
		return BattleLogPvE
	case "pvp":
		return BattleLogPvP
	default:
		return BattleLogUnknown
	}
}

func normalizedOutcome(name string, winner bool) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "won", "win", "winner":
		return "WON"
	case "lost", "loss", "lose":
		return "LOST"
	case "draw", "tie":
		return "DRAW"
	}
	if winner {
		return "WON"
	}
	return "UNKNOWN"
}

func classificationEvidenceString(evidence json.RawMessage, key string) string {
	if len(evidence) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(evidence, &object); err == nil {
		value, ok := object[key]
		if !ok || value == nil {
			return ""
		}
		text, ok := value.(string)
		if !ok {
			return ""
		}
		return strings.TrimSpace(text)
	}
	var entries []string
	if err := json.Unmarshal(evidence, &entries); err != nil {
		return ""
	}
	prefix := key + "="
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(entry, prefix))
		}
	}
	return ""
}

func jsonValuePresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func riskRank(value string) int {
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

func truncateBattleLog(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
