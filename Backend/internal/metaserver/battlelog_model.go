package metaserver

import (
	"encoding/json"
	"time"
)

const battleLogSchemaName = "project-rebound.battlelog.raw"
const battleLogSchemaVersion = 2

type BattleLogValidationStatus string

const (
	BattleLogAccepted             BattleLogValidationStatus = "ACCEPTED"
	BattleLogAcceptedWithWarnings BattleLogValidationStatus = "ACCEPTED_WITH_WARNINGS"
	BattleLogQuarantined          BattleLogValidationStatus = "QUARANTINED"
)

type BattleLogMatchType string

const (
	BattleLogPvE     BattleLogMatchType = "PVE"
	BattleLogPvP     BattleLogMatchType = "PVP"
	BattleLogUnknown BattleLogMatchType = "UNKNOWN"
)

type BattleLogWarning struct {
	Code             string `json:"code"`
	Severity         string `json:"severity"`
	Message          string `json:"message"`
	ParticipantIndex *int   `json:"participant_index,omitempty"`
}

type BattleLogSubmission struct {
	BattleLogID      string                    `json:"battlelog_id"`
	ReportID         string                    `json:"report_id"`
	MetaMatchID      string                    `json:"meta_match_id,omitempty"`
	MatchType        BattleLogMatchType        `json:"match_type"`
	ValidationStatus BattleLogValidationStatus `json:"validation_status"`
	RiskSeverity     string                    `json:"risk_severity,omitempty"`
	Official         bool                      `json:"official"`
	Duplicate        bool                      `json:"duplicate"`
	Warnings         []BattleLogWarning        `json:"warnings"`
}

type battleLogSnapshot struct {
	Schema              string                       `json:"schema"`
	SchemaVersion       int                          `json:"schema_version"`
	Source              string                       `json:"source"`
	CapturedAtUTC       time.Time                    `json:"captured_at_utc"`
	MatchID             string                       `json:"match_id"`
	MatchClassification battleLogMatchClassification `json:"match_classification"`
	GameState           battleLogGameState           `json:"game_state"`
	ParticipantSummary  battleLogParticipantSummary  `json:"participant_summary"`
	Players             []json.RawMessage            `json:"players"`
	PvERecord           json.RawMessage              `json:"pve_record"`
	PvPRecord           json.RawMessage              `json:"pvp_record"`
}

type battleLogMatchClassification struct {
	Type       string          `json:"type"`
	IsPvE      bool            `json:"is_pve"`
	IsPvP      bool            `json:"is_pvp"`
	Source     string          `json:"source"`
	Confidence string          `json:"confidence"`
	Evidence   json.RawMessage `json:"evidence"`
}

type battleLogGameState struct {
	ElapsedTime    float64              `json:"elapsed_time"`
	MapAliasName   string               `json:"map_alias_name"`
	MapDisplayName string               `json:"map_display_name"`
	MatchResult    battleLogMatchResult `json:"match_result"`
}

type battleLogMatchResult struct {
	WinnerTeamID int                    `json:"winner_team_id"`
	TeamScores   []int                  `json:"team_scores"`
	Rounds       []battleLogRoundResult `json:"rounds"`
}

type battleLogRoundResult struct {
	WinnerTeamID int   `json:"winner_team_id"`
	TeamScores   []int `json:"team_scores"`
	IsFinalRound bool  `json:"is_final_round"`
}

type battleLogParticipantSummary struct {
	All    battleLogSummaryBucket `json:"all"`
	Humans battleLogSummaryBucket `json:"humans"`
	AI     battleLogSummaryBucket `json:"ai"`
	Teams  []battleLogTeamSummary `json:"teams"`
}

type battleLogSummaryBucket struct {
	Count   int     `json:"count"`
	Kills   int     `json:"kills"`
	Deaths  int     `json:"deaths"`
	Assists int     `json:"assists"`
	Score   float64 `json:"score"`
}

type battleLogTeamSummary struct {
	TeamID int                    `json:"team_id"`
	All    battleLogSummaryBucket `json:"all"`
	Humans battleLogSummaryBucket `json:"humans"`
	AI     battleLogSummaryBucket `json:"ai"`
}

type battleLogRawParticipant struct {
	Assignment             battleLogAssignment    `json:"assignment"`
	ComputedStats          battleLogComputedStats `json:"computed_stats"`
	Identity               battleLogIdentity      `json:"identity"`
	MatchCharacterScoreMap []battleLogScoreEntry  `json:"match_character_score_map"`
	Outcome                battleLogOutcome       `json:"outcome"`
	RawFields              battleLogRawFields     `json:"raw_fields"`
	RoleScoreMap           []battleLogScoreEntry  `json:"role_score_map"`
	ServerMatchResultInfo  battleLogServerResult  `json:"server_match_result_info"`
}

type battleLogAssignment struct {
	CampID               int                `json:"camp_id"`
	PlayerIndex          int                `json:"player_index"`
	PlayingGameTime      float64            `json:"playing_game_time"`
	PossessedCharacterID string             `json:"possessed_character_id"`
	Role                 battleLogEnumValue `json:"role"`
	SelectedCharacterID  string             `json:"selected_character_id"`
	TeamID               int                `json:"team_id"`
}

type battleLogEnumValue struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type battleLogComputedStats struct {
	Assist             int     `json:"assist"`
	AvgKillDistance    float64 `json:"avg_kill_distance"`
	Death              int     `json:"death"`
	HeadshotCount      int     `json:"headshot_count"`
	KDRatio            float64 `json:"kd_ratio"`
	Kill               int     `json:"kill"`
	KillingStreakCount int     `json:"killing_streak_count"`
	MaxKillDistance    float64 `json:"max_kill_distance"`
	MaxKillStreakCount int     `json:"max_kill_streak_count"`
	NumBulletsFired    int     `json:"num_bullets_fired"`
	NumRocketsFired    int     `json:"num_rockets_fired"`
	PingMS             float64 `json:"ping_ms"`
	PlayerScore        float64 `json:"player_score"`
	SPM                float64 `json:"spm"`
	TeamScore          float64 `json:"team_score"`
}

type battleLogIdentity struct {
	PlatformID string `json:"platform_id"`
	PlayerName string `json:"player_name"`
	UserID     string `json:"user_id"`
}

type battleLogOutcome struct {
	IsMatchMVP    bool               `json:"is_match_mvp"`
	IsMatchWinner bool               `json:"is_match_winner"`
	IsQuitter     bool               `json:"is_quitter"`
	MatchResult   battleLogEnumValue `json:"match_result"`
}

type battleLogRawFields struct {
	IsBot           bool    `json:"is_bot"`
	IsInactive      bool    `json:"is_inactive"`
	IsSpectator     bool    `json:"is_spectator"`
	NumAssists      int     `json:"num_assists"`
	NumBulletsFired int     `json:"num_bullets_fired"`
	NumDeaths       int     `json:"num_deaths"`
	NumKills        int     `json:"num_kills"`
	NumRocketsFired int     `json:"num_rockets_fired"`
	Score           float64 `json:"score"`
}

type battleLogServerResult struct {
	Personal battleLogPersonalResult `json:"personal"`
}

type battleLogPersonalResult struct {
	Accuracy *float64 `json:"accuracy"`
}

type battleLogScoreEntry struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
}

type normalizedBattleLog struct {
	Snapshot     battleLogSnapshot
	CanonicalRaw []byte
	Digest       []byte
	MatchType    BattleLogMatchType
	Status       BattleLogValidationStatus
	RiskSeverity string
	ModeAlias    string
	DurationMS   int64
	Warnings     []BattleLogWarning
	Teams        []normalizedBattleLogTeam
	Participants []normalizedBattleLogParticipant
	Rounds       []battleLogRoundResult
}

type normalizedBattleLogTeam struct {
	TeamID     int
	Outcome    string
	MatchScore *int
	Kills      int
	Deaths     int
	Assists    int
	Score      float64
	HumanCount int
	AICount    int
}

type normalizedBattleLogParticipant struct {
	SlotIndex          int
	PlayerID           string
	PlatformID         string
	PlayerName         string
	IsAI               bool
	RosterVerified     bool
	OfficialEligible   bool
	AuthLevelAtMatch   string
	SteamVerified      bool
	TeamID             int
	CampID             int
	RoleName           string
	RoleValue          int
	SelectedCharacter  string
	PossessedCharacter string
	IsSpectator        bool
	IsInactive         bool
	IsQuitter          bool
	Outcome            string
	IsMatchMVP         bool
	RawPlayer          []byte
	Kills              int
	Deaths             int
	Assists            int
	Score              float64
	TeamScore          float64
	Headshots          int
	BulletsFired       int
	RocketsFired       int
	MaxKillDistance    float64
	AvgKillDistance    float64
	MaxKillStreak      int
	KillingStreakCount int
	PingMS             float64
	ReportedKDA        float64
	CalculatedKDA      float64
	ReportedSPM        float64
	CalculatedSPM      float64
	ReportedAccuracy   *float64
	PlayingTimeMS      int64
	CharacterScores    []battleLogScoreEntry
	RoleScores         []battleLogScoreEntry
}
