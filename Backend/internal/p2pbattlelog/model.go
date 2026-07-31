package p2pbattlelog

import (
	"encoding/json"
	"time"
)

const (
	SchemaName    = "project-rebound.p2p-battlelog.raw"
	SchemaVersion = 3
)

type MatchState string

const (
	MatchStarting      MatchState = "STARTING"
	MatchRunning       MatchState = "RUNNING"
	MatchCollecting    MatchState = "COLLECTING"
	MatchPeerConfirmed MatchState = "PEER_CONFIRMED"
	MatchSelfReported  MatchState = "SELF_REPORTED"
	MatchDisputed      MatchState = "DISPUTED"
	MatchIncomplete    MatchState = "INCOMPLETE"
	MatchAborted       MatchState = "ABORTED"
	MatchExpired       MatchState = "EXPIRED"
)

type Actor struct {
	PlayerID      string
	SessionID     string
	AuthLevel     string
	SteamVerified bool
}

type MatchSession struct {
	ID                    string     `json:"match_id"`
	RoomID                string     `json:"room_id,omitempty"`
	RoomIDSnapshot        string     `json:"room_id_snapshot"`
	Sequence              int        `json:"sequence"`
	HostPlayerIDAtStart   string     `json:"host_player_id_at_start"`
	Mode                  string     `json:"mode"`
	MapAlias              string     `json:"map_alias"`
	MatchType             string     `json:"match_type"`
	State                 MatchState `json:"state"`
	RosterRevision        int        `json:"roster_revision"`
	ExpectedReporterCount int        `json:"expected_reporter_count"`
	PolicyVersion         string     `json:"policy_version"`
	CollectionStartedAt   *time.Time `json:"collection_started_at,omitempty"`
	CollectionDeadline    *time.Time `json:"collection_deadline,omitempty"`
	HardExpiresAt         time.Time  `json:"hard_expires_at"`
	FinalizedAt           *time.Time `json:"finalized_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type RosterMember struct {
	MatchID              string
	PlayerID             string
	PlatformID           string
	RoomRole             string
	SlotIndex            int
	TeamID               *int
	AuthLevelAtStart     string
	SteamVerifiedAtStart bool
	IsSpectator          bool
	IsInitialRoster      bool
	EligibleReporter     bool
	JoinedRoomAt         time.Time
	CreatedAt            time.Time
}

type Capability struct {
	ID            string
	MatchID       string
	PlayerID      string
	AuthSessionID string
	TokenHash     []byte
	ServerNonce   string
	ExpiresAt     time.Time
	FirstUsedAt   *time.Time
	LastUsedAt    *time.Time
	RevokedAt     *time.Time
	CreatedAt     time.Time
}

type CapabilityResult struct {
	MatchID      string    `json:"match_id"`
	CapabilityID string    `json:"capability_id"`
	Token        string    `json:"report_token"`
	ServerNonce  string    `json:"server_nonce"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type PresenceInput struct {
	PresenceSeq      uint64 `json:"presence_seq"`
	Status           string `json:"status"`
	TimelineSession  string `json:"timeline_session_id"`
	LastCheckpoint   uint64 `json:"last_checkpoint_seq"`
	GameProcessAlive bool   `json:"game_process_alive"`
	GameConnected    bool   `json:"game_connected"`
}

type PresenceResult struct {
	MatchID       string    `json:"match_id"`
	PlayerID      string    `json:"player_id"`
	SegmentNo     int       `json:"segment_no"`
	PresenceSeq   uint64    `json:"presence_seq"`
	Status        string    `json:"status"`
	LastPresence  time.Time `json:"last_presence_at"`
	WasDuplicate  bool      `json:"was_duplicate"`
	ReconnectOpen bool      `json:"reconnect_segment_opened"`
}

type ReportRecord struct {
	ID                 string
	ReportID           string
	MatchID            string
	ReporterPlayerID   string
	CapabilityID       string
	ReportRevision     int
	Completeness       string
	SchemaName         string
	SchemaVersion      int
	AuthorityKind      string
	ClientVersion      string
	TimelineSessionID  string
	CapturedAt         time.Time
	ReceivedAt         time.Time
	EventCount         int
	RawSizeBytes       int
	RawSHA256          []byte
	OutcomeSHA256      []byte
	StatsSHA256        []byte
	RawSnapshot        []byte
	NormalizedResult   []byte
	ValidationStatus   string
	RiskSeverity       string
	ValidationWarnings []Warning
}

type ReportResult struct {
	ReportID        string     `json:"report_id"`
	MatchID         string     `json:"match_id"`
	Status          string     `json:"validation_status"`
	RiskSeverity    string     `json:"risk_severity,omitempty"`
	Warnings        []Warning  `json:"validation_warnings"`
	Duplicate       bool       `json:"duplicate"`
	CollectionState MatchState `json:"collection_state"`
}

type Warning struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	Quarantine bool   `json:"quarantine"`
}

type RawSnapshot struct {
	Schema                string                 `json:"schema"`
	SchemaVersion         int                    `json:"schema_version"`
	P2PMatchID            string                 `json:"p2p_match_id"`
	CapabilityID          string                 `json:"capability_id"`
	ServerNonce           string                 `json:"server_nonce"`
	AuthorityKind         string                 `json:"authority_kind"`
	ClientVersion         string                 `json:"client_version"`
	TimelineSessionID     string                 `json:"timeline_session_id"`
	ReportCompleteness    string                 `json:"report_completeness"`
	ReportRevision        int                    `json:"report_revision"`
	CapturedAtUTC         time.Time              `json:"captured_at_utc"`
	MatchClassification   RawMatchClassification `json:"match_classification"`
	GameState             RawGameState           `json:"game_state"`
	Players               []RawPlayer            `json:"players"`
	Timeline              RawTimeline            `json:"timeline"`
	ParticipantSummary    json.RawMessage        `json:"participant_summary,omitempty"`
	PvERecord             json.RawMessage        `json:"pve_record,omitempty"`
	PvPRecord             json.RawMessage        `json:"pvp_record,omitempty"`
	ExtractionWarnings    []string               `json:"warnings,omitempty"`
	ExtractionSDKLayout   json.RawMessage        `json:"sdk_layout,omitempty"`
	DiagnosticWorldObject json.RawMessage        `json:"world,omitempty"`
}

type RawMatchClassification struct {
	Type       string   `json:"type"`
	IsPvE      bool     `json:"is_pve"`
	IsPvP      bool     `json:"is_pvp"`
	Confidence string   `json:"confidence"`
	Source     string   `json:"source"`
	Evidence   []string `json:"evidence"`
}

type RawNamedValue struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type RawGameState struct {
	ElapsedTime  float64        `json:"elapsed_time"`
	MapAliasName string         `json:"map_alias_name"`
	Mode         RawMode        `json:"mode"`
	MatchResult  RawMatchResult `json:"match_result"`
}

type RawMode struct {
	AliasName string `json:"alias_name"`
}

type RawMatchResult struct {
	WinnerTeamID int              `json:"winner_team_id"`
	TeamScores   []int            `json:"team_scores"`
	Rounds       []RawRoundResult `json:"rounds"`
}

type RawRoundResult struct {
	WinnerTeamID int   `json:"winner_team_id"`
	TeamScores   []int `json:"team_scores"`
	IsFinalRound bool  `json:"is_final_round"`
}

type RawPlayer struct {
	Assignment    RawAssignment    `json:"assignment"`
	ComputedStats RawComputedStats `json:"computed_stats"`
	Identity      RawIdentity      `json:"identity"`
	Outcome       RawOutcome       `json:"outcome"`
	RawFields     RawPlayerFields  `json:"raw_fields"`
}

type RawAssignment struct {
	CampID               int           `json:"camp_id"`
	GenericTeamID        int           `json:"generic_team_id"`
	TeamID               int           `json:"team_id"`
	TeamNum              int           `json:"team_num"`
	SelectedCharacterID  string        `json:"selected_character_id"`
	PossessedCharacterID string        `json:"possessed_character_id"`
	Role                 RawNamedValue `json:"role"`
}

type RawComputedStats struct {
	Kill        int     `json:"kill"`
	Death       int     `json:"death"`
	Assist      int     `json:"assist"`
	PlayerScore float64 `json:"player_score"`
	TeamScore   float64 `json:"team_score"`
}

type RawIdentity struct {
	PlatformID string `json:"platform_id"`
	UserID     string `json:"user_id"`
	PlayerName string `json:"player_name"`
	PlayerID   int    `json:"player_id"`
}

type RawOutcome struct {
	IsMatchWinner bool          `json:"is_match_winner"`
	IsQuitter     bool          `json:"is_quitter"`
	MatchResult   RawNamedValue `json:"match_result"`
}

type RawPlayerFields struct {
	IsBot       bool    `json:"is_bot"`
	IsInactive  bool    `json:"is_inactive"`
	IsSpectator bool    `json:"is_spectator"`
	NumKills    int     `json:"num_kills"`
	NumDeaths   int     `json:"num_deaths"`
	NumAssists  int     `json:"num_assists"`
	Score       float64 `json:"score"`
	StartTime   int     `json:"start_time"`
}

type RawTimeline struct {
	FirstSeq          uint64             `json:"first_seq"`
	LastSeq           uint64             `json:"last_seq"`
	EventsDigest      string             `json:"events_digest"`
	TimelineTruncated bool               `json:"timeline_truncated"`
	Events            []RawTimelineEvent `json:"events"`
}

type RawTimelineEvent struct {
	Seq               uint64          `json:"seq"`
	Type              string          `json:"type"`
	LocalMonotonicMS  uint64          `json:"local_monotonic_ms"`
	PreviousEventHash string          `json:"previous_event_hash"`
	EventHash         string          `json:"event_hash"`
	Payload           json.RawMessage `json:"payload"`
}

type NormalizedResult struct {
	Outcome      NormalizedOutcome       `json:"outcome"`
	Participants []NormalizedParticipant `json:"participants"`
}

type NormalizedOutcome struct {
	MatchType    string                 `json:"match_type"`
	ModeAlias    string                 `json:"mode_alias"`
	MapAlias     string                 `json:"map_alias"`
	WinnerTeamID int                    `json:"winner_team_id"`
	TeamScores   []int                  `json:"team_scores"`
	Rounds       []NormalizedRound      `json:"rounds"`
	HumanTeams   []NormalizedTeamMember `json:"human_teams"`
}

type NormalizedRound struct {
	RoundIndex   int   `json:"round_index"`
	WinnerTeamID int   `json:"winner_team_id"`
	TeamScores   []int `json:"team_scores"`
	IsFinalRound bool  `json:"is_final_round"`
}

type NormalizedTeamMember struct {
	PlayerID string `json:"player_id"`
	TeamID   int    `json:"team_id"`
}

type NormalizedParticipant struct {
	PlayerID   string  `json:"player_id"`
	TeamID     int     `json:"team_id"`
	Outcome    string  `json:"outcome"`
	Kills      int     `json:"kills"`
	Deaths     int     `json:"deaths"`
	Assists    int     `json:"assists"`
	Score      float64 `json:"score"`
	IsQuitter  bool    `json:"is_quitter"`
	IsInactive bool    `json:"is_inactive"`
}

type FinalizedResult struct {
	MatchID        string                 `json:"match_id"`
	State          MatchState             `json:"state"`
	TrustTier      string                 `json:"trust_tier,omitempty"`
	MatchType      string                 `json:"match_type"`
	ModeAlias      string                 `json:"mode_alias,omitempty"`
	MapAlias       string                 `json:"map_alias,omitempty"`
	WinnerTeamID   *int                   `json:"winner_team_id,omitempty"`
	TeamScores     []int                  `json:"team_scores"`
	Rounds         []NormalizedRound      `json:"rounds"`
	Participants   []FinalizedParticipant `json:"participants"`
	EligibleCount  int                    `json:"eligible_reporter_count"`
	ReceivedCount  int                    `json:"received_final_count"`
	RequiredQuorum int                    `json:"required_quorum"`
	TeamCoverage   bool                   `json:"team_coverage"`
	RiskSeverity   string                 `json:"risk_severity,omitempty"`
	Reasons        []string               `json:"reasons"`
	PolicyVersion  string                 `json:"policy_version"`
	FinalizedAt    *time.Time             `json:"finalized_at,omitempty"`
}

type FinalizedParticipant struct {
	NormalizedParticipant
	StatsStatus string `json:"stats_status"`
}

type AdminMatchEvidence struct {
	Match        MatchSession         `json:"match"`
	Roster       []AdminRosterMember  `json:"roster"`
	Presence     []AdminPresence      `json:"presence"`
	Reports      []AdminReportSummary `json:"reports"`
	Result       FinalizedResult      `json:"result"`
	ShadowMode   bool                 `json:"shadow_mode"`
	StorageClass string               `json:"storage_class"`
}

type AdminRosterMember struct {
	PlayerID             string `json:"player_id"`
	PlatformID           string `json:"platform_id"`
	RoomRole             string `json:"room_role"`
	SlotIndex            int    `json:"slot_index"`
	AuthLevelAtStart     string `json:"auth_level_at_start"`
	SteamVerifiedAtStart bool   `json:"steam_verified_at_start"`
	EligibleReporter     bool   `json:"eligible_reporter"`
	IsSpectator          bool   `json:"is_spectator"`
}

type AdminPresence struct {
	PlayerID          string     `json:"player_id"`
	SegmentNo         int        `json:"segment_no"`
	JoinKind          string     `json:"join_kind"`
	Status            string     `json:"status"`
	TimelineSessionID string     `json:"timeline_session_id,omitempty"`
	PresenceSeq       uint64     `json:"presence_seq"`
	LastCheckpointSeq uint64     `json:"last_checkpoint_seq"`
	JoinedAt          time.Time  `json:"joined_at"`
	LastPresenceAt    time.Time  `json:"last_presence_at"`
	LeftAt            *time.Time `json:"left_at,omitempty"`
	LeaveKind         string     `json:"leave_kind,omitempty"`
}

type AdminReportSummary struct {
	EvidenceID       string    `json:"evidence_id"`
	ReportID         string    `json:"report_id"`
	ReporterPlayerID string    `json:"reporter_player_id"`
	Completeness     string    `json:"completeness"`
	AuthorityKind    string    `json:"authority_kind"`
	CapturedAt       time.Time `json:"captured_at"`
	ReceivedAt       time.Time `json:"received_at"`
	EventCount       int       `json:"event_count"`
	RawSizeBytes     int       `json:"raw_size_bytes"`
	RawSHA256        string    `json:"raw_sha256"`
	OutcomeSHA256    string    `json:"outcome_sha256"`
	StatsSHA256      string    `json:"stats_sha256"`
	ValidationStatus string    `json:"validation_status"`
	RiskSeverity     string    `json:"risk_severity,omitempty"`
	WarningCount     int       `json:"warning_count"`
}

type AdminRawEvidence struct {
	EvidenceID       string          `json:"evidence_id"`
	MatchID          string          `json:"match_id"`
	ReporterPlayerID string          `json:"reporter_player_id"`
	RawSHA256        string          `json:"raw_sha256"`
	ValidationStatus string          `json:"validation_status"`
	RiskSeverity     string          `json:"risk_severity,omitempty"`
	Snapshot         json.RawMessage `json:"snapshot"`
}
