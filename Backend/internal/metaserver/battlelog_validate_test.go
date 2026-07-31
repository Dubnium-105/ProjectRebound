package metaserver

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestNormalizeBattleLogSnapshotPvE(t *testing.T) {
	raw := testBattleLogSnapshot(t, "pve")
	result, err := normalizeBattleLogSnapshot(raw)
	if err != nil {
		t.Fatalf("normalize PvE snapshot: %v", err)
	}
	if result.MatchType != BattleLogPvE ||
		result.Status != BattleLogAccepted ||
		result.ModeAlias != "Rush_PVE_Normal" {
		t.Fatalf("classification = %#v", result)
	}
	if result.DurationMS != 120_000 || len(result.Participants) != 1 {
		t.Fatalf("duration/participants = %d/%d", result.DurationMS, len(result.Participants))
	}
	if result.Warnings == nil {
		t.Fatal("warning-free snapshot must normalize warnings to an empty array")
	}
	warningsJSON, err := json.Marshal(result.Warnings)
	if err != nil {
		t.Fatalf("marshal warnings: %v", err)
	}
	if string(warningsJSON) != "[]" {
		t.Fatalf("warning-free snapshot warnings JSON = %s, want []", warningsJSON)
	}
	participant := result.Participants[0]
	if participant.IsAI || participant.Kills != 5 || participant.Deaths != 2 ||
		participant.Assists != 1 || participant.Score != 100 {
		t.Fatalf("participant = %#v", participant)
	}
	if math.Abs(participant.CalculatedKDA-3) > 0.0001 ||
		math.Abs(participant.CalculatedSPM-50) > 0.0001 {
		t.Fatalf(
			"calculated KDA/SPM = %f/%f",
			participant.CalculatedKDA,
			participant.CalculatedSPM,
		)
	}
}

func TestNormalizeBattleLogSnapshotKeepsPvPSeparate(t *testing.T) {
	raw := testBattleLogSnapshot(t, "pvp")
	result, err := normalizeBattleLogSnapshot(raw)
	if err != nil {
		t.Fatalf("normalize PvP snapshot: %v", err)
	}
	if result.MatchType != BattleLogPvP || result.Status != BattleLogAccepted {
		t.Fatalf("classification = %s/%s", result.MatchType, result.Status)
	}
}

func TestNormalizeBattleLogSnapshotQuarantinesClassificationConflict(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(testBattleLogSnapshot(t, "pve"), &document); err != nil {
		t.Fatal(err)
	}
	document["pve_record"] = nil
	document["pvp_record"] = map[string]any{"result": map[string]any{}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	result, err := normalizeBattleLogSnapshot(raw)
	if err != nil {
		t.Fatalf("normalize contradictory snapshot: %v", err)
	}
	if result.Status != BattleLogQuarantined || result.RiskSeverity != "HIGH" {
		t.Fatalf("status/risk = %s/%s", result.Status, result.RiskSeverity)
	}
}

func TestNormalizeBattleLogSnapshotRejectsClientSource(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(testBattleLogSnapshot(t, "pve"), &document); err != nil {
		t.Fatal(err)
	}
	document["source"] = "client"
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	_, err = normalizeBattleLogSnapshot(raw)
	if metaErrorCode(err) != "BATTLELOG_SERVER_SNAPSHOT_REQUIRED" {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeBattleLogFixtureWhenConfigured(t *testing.T) {
	path := os.Getenv("BATTLELOG_FIXTURE")
	if path == "" {
		t.Skip("BATTLELOG_FIXTURE is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := normalizeBattleLogSnapshot(raw)
	if err != nil {
		t.Fatalf("normalize real BattleLog fixture: %#v", err)
	}
	if len(result.Participants) == 0 {
		t.Fatal("real BattleLog fixture has no normalized participants")
	}
	t.Logf(
		"type=%s status=%s participants=%d warnings=%d",
		result.MatchType, result.Status, len(result.Participants), len(result.Warnings),
	)
}

func testBattleLogSnapshot(t *testing.T, matchType string) json.RawMessage {
	t.Helper()
	isPvE := matchType == "pve"
	modeAlias := "TeamDeathMatch"
	var pveRecord any
	var pvpRecord any = map[string]any{"result": map[string]any{}}
	if isPvE {
		modeAlias = "Rush_PVE_Normal"
		pveRecord = map[string]any{"result": map[string]any{}}
		pvpRecord = nil
	}
	document := map[string]any{
		"schema":          battleLogSchemaName,
		"schema_version":  battleLogSchemaVersion,
		"source":          "server",
		"captured_at_utc": "2026-07-31T07:01:37.805Z",
		"match_id":        "",
		"pve_record":      pveRecord,
		"pvp_record":      pvpRecord,
		"match_classification": map[string]any{
			"type":       matchType,
			"is_pve":     isPvE,
			"is_pvp":     !isPvE,
			"source":     "server_config",
			"confidence": "authoritative",
			"evidence": map[string]any{
				"mode_alias": modeAlias,
			},
		},
		"game_state": map[string]any{
			"elapsed_time":     120.0,
			"map_alias_name":   "Warehouse",
			"map_display_name": "Warehouse",
			"match_result": map[string]any{
				"winner_team_id": 0,
				"team_scores":    []int{5},
				"rounds": []any{
					map[string]any{
						"winner_team_id": 0,
						"team_scores":    []int{5},
						"is_final_round": true,
					},
				},
			},
		},
		"participant_summary": map[string]any{
			"all":    testBattleLogSummaryBucket(1, 5, 2, 1, 100),
			"humans": testBattleLogSummaryBucket(1, 5, 2, 1, 100),
			"ai":     testBattleLogSummaryBucket(0, 0, 0, 0, 0),
			"teams": []any{
				map[string]any{
					"team_id": 0,
					"all":     testBattleLogSummaryBucket(1, 5, 2, 1, 100),
					"humans":  testBattleLogSummaryBucket(1, 5, 2, 1, 100),
					"ai":      testBattleLogSummaryBucket(0, 0, 0, 0, 0),
				},
			},
		},
		"players": []any{
			map[string]any{
				"assignment": map[string]any{
					"camp_id":                0,
					"player_index":           0,
					"playing_game_time":      120,
					"possessed_character_id": "FORT",
					"selected_character_id":  "FORT",
					"team_id":                0,
					"role":                   map[string]any{"name": "Tank", "value": 4},
				},
				"computed_stats": map[string]any{
					"avg_kill_distance":     10,
					"headshot_count":        1,
					"kd_ratio":              2.5,
					"killing_streak_count":  1,
					"max_kill_distance":     20,
					"max_kill_streak_count": 3,
					"ping_ms":               20,
					"spm":                   5000,
					"team_score":            5,
				},
				"identity": map[string]any{
					"platform_id": "76561198000000001",
					"player_name": "Test Player",
					"user_id":     "76561198000000001",
				},
				"match_character_score_map": []any{
					map[string]any{"key": "FORT", "value": 100},
				},
				"outcome": map[string]any{
					"is_match_mvp":    true,
					"is_match_winner": true,
					"is_quitter":      false,
					"match_result": map[string]any{
						"name":  "Won",
						"value": 0,
					},
				},
				"raw_fields": map[string]any{
					"is_bot":            false,
					"is_inactive":       false,
					"is_spectator":      false,
					"num_assists":       1,
					"num_bullets_fired": 50,
					"num_deaths":        2,
					"num_kills":         5,
					"num_rockets_fired": 0,
					"score":             100,
				},
				"role_score_map": []any{},
				"server_match_result_info": map[string]any{
					"personal": map[string]any{"accuracy": 0.5},
				},
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testBattleLogSummaryBucket(
	count, kills, deaths, assists int,
	score float64,
) map[string]any {
	return map[string]any{
		"count": count, "kills": kills, "deaths": deaths,
		"assists": assists, "score": score,
	}
}
