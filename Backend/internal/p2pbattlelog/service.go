package p2pbattlelog

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	repository *Repository
	config     config.P2PBattleLogConfig
	now        func() time.Time
}

type normalizedReportView struct {
	reporter string
	report   ReportRecord
	result   NormalizedResult
}

func NewService(repository *Repository, cfg config.P2PBattleLogConfig) *Service {
	return &Service{repository: repository, config: cfg, now: time.Now}
}

func (s *Service) EnsureForRoomStart(
	ctx context.Context,
	tx pgx.Tx,
	room p2proom.MatchStartRoom,
	now time.Time,
) error {
	if !s.config.Enabled {
		return nil
	}
	_, _, err := s.repository.EnsureMatchForRoomStart(
		ctx, tx, newID("p2pm_"), room.ID, room.HostPlayerID, room.Mode,
		matchTypeFromMode(room.Mode), s.config.PolicyVersion, now,
		now.Add(s.config.HardExpiry()),
	)
	return err
}

// FreezeManagedAttempt creates the P2P match session and roster inside the
// authoritative match-lobby freeze transaction. Unlike legacy BattleLog
// intake, this projection is required even when report collection is disabled:
// transport startup must only reuse the already-frozen two-team roster.
func (s *Service) FreezeManagedAttempt(
	ctx context.Context,
	tx pgx.Tx,
	roomID, hostPlayerID, mode, attemptID string,
	now time.Time,
) error {
	match, _, err := s.repository.EnsureMatchForRoomStart(
		ctx, tx, newID("p2pm_"), roomID, hostPlayerID, mode,
		matchTypeFromMode(mode), s.config.PolicyVersion, now,
		now.Add(s.config.HardExpiry()),
	)
	if err != nil {
		return err
	}
	var linkedAttemptID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(match_attempt_id, '')
		FROM p2p_match_sessions WHERE id = $1
	`, match.ID).Scan(&linkedAttemptID); err != nil {
		return err
	}
	if linkedAttemptID != attemptID {
		return fmt.Errorf("active P2P match session is not bound to frozen attempt %s", attemptID)
	}
	return nil
}

// CompleteManagedAttempt keeps the transport projection out of an active
// state when the authoritative attempt ends. With BattleLog intake enabled a
// successful match enters its normal collection window; otherwise it is
// finalized as INCOMPLETE because no peer evidence can be collected.
func (s *Service) CompleteManagedAttempt(
	ctx context.Context,
	tx pgx.Tx,
	attemptID string,
	success bool,
	now time.Time,
) error {
	if !success {
		_, err := tx.Exec(ctx, `
			UPDATE p2p_match_sessions
			SET state = 'ABORTED', finalized_at = $2, updated_at = $2
			WHERE match_attempt_id = $1
			  AND state IN ('STARTING', 'RUNNING', 'COLLECTING')
		`, attemptID, now)
		return err
	}
	if s.config.Enabled {
		_, err := tx.Exec(ctx, `
			UPDATE p2p_match_sessions
			SET state = 'COLLECTING',
			    collection_started_at = COALESCE(collection_started_at, $2),
			    collection_deadline = COALESCE(collection_deadline, $3),
			    updated_at = $2
			WHERE match_attempt_id = $1
			  AND state IN ('STARTING', 'RUNNING', 'COLLECTING')
		`, attemptID, now, now.Add(s.config.CollectionDeadline()))
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE p2p_match_sessions
		SET state = 'INCOMPLETE', finalized_at = $2, updated_at = $2
		WHERE match_attempt_id = $1
		  AND state IN ('STARTING', 'RUNNING', 'COLLECTING')
	`, attemptID, now)
	return err
}

func (s *Service) MarkRoomRunning(ctx context.Context, roomID string, now time.Time) error {
	if !s.config.Enabled {
		return nil
	}
	return s.repository.MarkRoomMatchRunning(ctx, roomID, now)
}

func (s *Service) ActiveMatch(ctx context.Context, actor Actor, roomID string) (MatchSession, error) {
	if err := s.requireEnabledActor(actor); err != nil {
		return MatchSession{}, err
	}
	item, err := s.repository.GetActiveMatchForRoomPlayer(ctx, strings.TrimSpace(roomID), actor.PlayerID)
	if err != nil {
		return MatchSession{}, mapNotFound(err, "P2P_MATCH_NOT_FOUND", "No active P2P match was found for this room membership.")
	}
	return item, nil
}

func (s *Service) IssueCapability(ctx context.Context, actor Actor, matchID string) (CapabilityResult, error) {
	if err := s.requireEnabledActor(actor); err != nil {
		return CapabilityResult{}, err
	}
	if strings.TrimSpace(matchID) == "" || strings.TrimSpace(actor.SessionID) == "" {
		return CapabilityResult{}, unauthorized("P2P_BATTLELOG_AUTH_REQUIRED", "A current verified player session is required.")
	}
	token, tokenHash, err := newReportToken()
	if err != nil {
		return CapabilityResult{}, internal(err)
	}
	nonce, err := newOpaque("p2n_", 24)
	if err != nil {
		return CapabilityResult{}, internal(err)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CapabilityResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	match, err := s.repository.GetMatchForUpdate(ctx, tx, matchID)
	if err != nil {
		return CapabilityResult{}, mapNotFound(err, "P2P_MATCH_NOT_FOUND", "P2P match not found.")
	}
	if !activeMatchState(match.State) || !now.Before(match.HardExpiresAt) {
		return CapabilityResult{}, conflict("P2P_MATCH_NOT_ACCEPTING_REPORTS", "The P2P match is no longer accepting report capabilities.")
	}
	member, err := s.repository.GetRosterMember(ctx, tx, match.ID, actor.PlayerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CapabilityResult{}, forbidden("P2P_MATCH_MEMBERSHIP_REQUIRED", "The authenticated player is not in the frozen P2P match roster.")
		}
		return CapabilityResult{}, internal(err)
	}
	if !member.EligibleReporter || member.IsSpectator {
		return CapabilityResult{}, forbidden("P2P_REPORTER_NOT_ELIGIBLE", "This roster member is not eligible to submit a result report.")
	}
	expiresAt := now.Add(s.config.CapabilityTTL())
	capability := Capability{
		ID: newID("p2rc_"), MatchID: match.ID, PlayerID: actor.PlayerID,
		AuthSessionID: actor.SessionID, TokenHash: tokenHash, ServerNonce: nonce,
		ExpiresAt: expiresAt, CreatedAt: now,
	}
	if err := s.repository.RotateCapability(ctx, tx, capability, now); err != nil {
		return CapabilityResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CapabilityResult{}, internal(err)
	}
	return CapabilityResult{
		MatchID: match.ID, CapabilityID: capability.ID, Token: token,
		ServerNonce: nonce, ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) UpdatePresence(
	ctx context.Context,
	actor Actor,
	matchID string,
	input PresenceInput,
) (PresenceResult, error) {
	if err := s.requireEnabledActor(actor); err != nil {
		return PresenceResult{}, err
	}
	if input.PresenceSeq == 0 || strings.TrimSpace(input.TimelineSession) == "" ||
		input.PresenceSeq > math.MaxInt64 || input.LastCheckpoint > math.MaxInt64 ||
		len(input.TimelineSession) > 128 || !allowedPresenceStatus(input.Status) {
		return PresenceResult{}, invalid(
			"P2P_PRESENCE_INVALID", "The P2P presence update is invalid.", nil,
		)
	}
	input.TimelineSession = strings.TrimSpace(input.TimelineSession)
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PresenceResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	match, err := s.repository.GetMatchForUpdate(ctx, tx, matchID)
	if err != nil {
		return PresenceResult{}, mapNotFound(err, "P2P_MATCH_NOT_FOUND", "P2P match not found.")
	}
	if !activeMatchState(match.State) {
		return PresenceResult{}, conflict("P2P_MATCH_FINALIZED", "Presence cannot be changed after the P2P match is finalized.")
	}
	if _, err := s.repository.GetRosterMember(ctx, tx, match.ID, actor.PlayerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PresenceResult{}, forbidden("P2P_MATCH_MEMBERSHIP_REQUIRED", "The authenticated player is not in the frozen P2P match roster.")
		}
		return PresenceResult{}, internal(err)
	}
	result, err := s.repository.UpsertPresence(
		ctx, tx, match.ID, actor.PlayerID, input.TimelineSession,
		strings.TrimSpace(input.Status), input.PresenceSeq, input.LastCheckpoint,
		input.GameProcessAlive, input.GameConnected, now,
	)
	if err != nil {
		if errors.Is(err, errPresenceSegmentLimit) {
			return PresenceResult{}, conflict(
				"P2P_PRESENCE_SEGMENT_LIMIT", "Too many reconnect segments were recorded for this match.",
			)
		}
		return PresenceResult{}, internal(err)
	}
	if input.Status == "ACTIVE" && match.State == MatchStarting {
		if _, err := tx.Exec(ctx, `
			UPDATE p2p_match_sessions SET state = 'RUNNING', updated_at = $2
			WHERE id = $1 AND state = 'STARTING'
		`, match.ID, now); err != nil {
			return PresenceResult{}, internal(err)
		}
	}
	if input.Status == "RESULT_SCREEN" || input.Status == "EXIT_INTENT" || input.Status == "LEFT" {
		allTerminal, terminalErr := s.repository.AllEligibleReportersAtResultOrLeft(ctx, tx, match.ID)
		if terminalErr != nil {
			return PresenceResult{}, internal(terminalErr)
		}
		if allTerminal && match.CollectionDeadline == nil {
			if _, openErr := s.repository.OpenCollection(
				ctx, tx, match.ID, now, now.Add(s.config.CollectionDeadline()),
			); openErr != nil {
				return PresenceResult{}, internal(openErr)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PresenceResult{}, internal(err)
	}
	return result, nil
}

func (s *Service) SubmitReport(
	ctx context.Context,
	actor Actor,
	matchID, reportID, suppliedToken string,
	raw []byte,
) (ReportResult, error) {
	if err := s.requireEnabledActor(actor); err != nil {
		return ReportResult{}, err
	}
	matchID = strings.TrimSpace(matchID)
	reportID = strings.TrimSpace(reportID)
	if !reportIDPattern.MatchString(reportID) {
		return ReportResult{}, invalid("P2P_REPORT_ID_INVALID", "The report_id format is invalid.", nil)
	}
	if len(raw) == 0 || len(raw) > s.config.MaxReportBytes {
		return ReportResult{}, unprocessable(
			"P2P_REPORT_TOO_LARGE", "The P2P BattleLog report size is invalid.",
			map[string]any{"maximum_bytes": s.config.MaxReportBytes},
		)
	}
	if !strings.HasPrefix(suppliedToken, "p2r_") || len(suppliedToken) < 40 {
		return ReportResult{}, unauthorized("P2P_REPORT_TOKEN_REQUIRED", "A valid P2P report token is required.")
	}
	var contextFields struct {
		CapabilityID string `json:"capability_id"`
	}
	if err := json.Unmarshal(raw, &contextFields); err != nil || strings.TrimSpace(contextFields.CapabilityID) == "" {
		return ReportResult{}, unprocessable(
			"P2P_BATTLELOG_INVALID_SNAPSHOT", "The report does not contain a valid capability_id.", nil,
		)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReportResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	match, err := s.repository.GetMatchForUpdate(ctx, tx, matchID)
	if err != nil {
		return ReportResult{}, mapNotFound(err, "P2P_MATCH_NOT_FOUND", "P2P match not found.")
	}
	capability, err := s.repository.GetCapabilityForUpdate(
		ctx, tx, contextFields.CapabilityID, match.ID, actor.PlayerID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReportResult{}, unauthorized("P2P_REPORT_TOKEN_INVALID", "The P2P report capability is invalid.")
		}
		return ReportResult{}, internal(err)
	}
	sessionMatches, err := s.repository.CapabilitySessionMatches(
		ctx, tx, capability.AuthSessionID, actor.SessionID, actor.PlayerID,
	)
	if err != nil {
		return ReportResult{}, internal(err)
	}
	if !sessionMatches || capability.RevokedAt != nil || !now.Before(capability.ExpiresAt) ||
		subtle.ConstantTimeCompare(capability.TokenHash, hashReportToken(suppliedToken)) != 1 {
		return ReportResult{}, unauthorized("P2P_REPORT_TOKEN_INVALID", "The P2P report capability is invalid or expired.")
	}
	if !now.Before(match.HardExpiresAt) {
		existing, existingErr := s.repository.FindReport(ctx, tx, match.ID, actor.PlayerID, reportID)
		if existingErr == nil {
			rawDigest, digestErr := compactJSONDigest(raw)
			if digestErr == nil && slices.Equal(existing.RawSHA256, rawDigest) {
				return ReportResult{
					ReportID: reportID, MatchID: match.ID, Status: existing.ValidationStatus,
					RiskSeverity: existing.RiskSeverity, Warnings: existing.ValidationWarnings,
					Duplicate: true, CollectionState: match.State,
				}, nil
			}
		}
		return ReportResult{}, conflict("P2P_MATCH_EXPIRED", "The P2P match report window has expired.")
	}
	if !activeMatchState(match.State) {
		existing, existingErr := s.repository.FindReport(ctx, tx, match.ID, actor.PlayerID, reportID)
		if existingErr == nil {
			rawDigest, digestErr := compactJSONDigest(raw)
			if digestErr == nil && slices.Equal(existing.RawSHA256, rawDigest) {
				return ReportResult{
					ReportID: reportID, MatchID: match.ID, Status: existing.ValidationStatus,
					RiskSeverity: existing.RiskSeverity, Warnings: existing.ValidationWarnings,
					Duplicate: true, CollectionState: match.State,
				}, nil
			}
		}
		return ReportResult{}, conflict("P2P_MATCH_FINALIZED", "The P2P match has already been finalized.")
	}
	roster, err := s.repository.ListRoster(ctx, tx, match.ID)
	if err != nil {
		return ReportResult{}, internal(err)
	}
	normalized, err := normalizeSnapshot(raw, match, capability, roster, s.config.MaxEvents)
	if err != nil {
		return ReportResult{}, err
	}
	existing, err := s.repository.FindReport(ctx, tx, match.ID, actor.PlayerID, reportID)
	if err == nil {
		if slices.Equal(existing.RawSHA256, normalized.RawDigest) {
			return ReportResult{
				ReportID: reportID, MatchID: match.ID, Status: existing.ValidationStatus,
				RiskSeverity: existing.RiskSeverity, Warnings: existing.ValidationWarnings,
				Duplicate: true, CollectionState: match.State,
			}, nil
		}
		return ReportResult{}, conflict("P2P_REPORT_ID_CONFLICT", "The report_id is already bound to different content.")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ReportResult{}, internal(err)
	}
	if normalized.Snapshot.ReportCompleteness == "FINAL" {
		finalReport, finalErr := s.repository.FindFinalReportForPlayer(ctx, tx, match.ID, actor.PlayerID)
		if finalErr == nil {
			if slices.Equal(finalReport.RawSHA256, normalized.RawDigest) {
				return ReportResult{
					ReportID: finalReport.ReportID, MatchID: match.ID,
					Status: finalReport.ValidationStatus, RiskSeverity: finalReport.RiskSeverity,
					Warnings: finalReport.ValidationWarnings, Duplicate: true,
					CollectionState: match.State,
				}, nil
			}
			return ReportResult{}, conflict("P2P_FINAL_REPORT_CONFLICT", "A different final report already exists for this player and match.")
		}
		if !errors.Is(finalErr, pgx.ErrNoRows) {
			return ReportResult{}, internal(finalErr)
		}
	}
	record := ReportRecord{
		ID: newID("p2pr_"), ReportID: reportID, MatchID: match.ID,
		ReporterPlayerID: actor.PlayerID, CapabilityID: capability.ID,
		ReportRevision: normalized.Snapshot.ReportRevision,
		Completeness:   normalized.Snapshot.ReportCompleteness,
		SchemaName:     normalized.Snapshot.Schema, SchemaVersion: normalized.Snapshot.SchemaVersion,
		AuthorityKind:     normalized.Snapshot.AuthorityKind,
		ClientVersion:     normalized.Snapshot.ClientVersion,
		TimelineSessionID: normalized.Snapshot.TimelineSessionID,
		CapturedAt:        normalized.Snapshot.CapturedAtUTC, ReceivedAt: now,
		EventCount: len(normalized.Snapshot.Timeline.Events), RawSizeBytes: len(normalized.CanonicalRaw),
		RawSHA256: normalized.RawDigest, OutcomeSHA256: normalized.OutcomeDigest,
		StatsSHA256: normalized.StatsDigest, RawSnapshot: normalized.CanonicalRaw,
		NormalizedResult: normalized.NormalizedJSON,
		ValidationStatus: normalized.ValidationStatus, RiskSeverity: normalized.RiskSeverity,
		ValidationWarnings: normalized.Warnings,
	}
	if err := s.repository.InsertReport(ctx, tx, record); err != nil {
		return ReportResult{}, internal(err)
	}
	if err := s.repository.MarkCapabilityUsed(ctx, tx, capability.ID, now); err != nil {
		return ReportResult{}, internal(err)
	}
	if record.Completeness == "FINAL" {
		match, err = s.repository.OpenCollection(
			ctx, tx, match.ID, now, now.Add(s.config.CollectionDeadline()),
		)
		if err != nil {
			return ReportResult{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ReportResult{}, internal(err)
	}
	if record.Completeness == "FINAL" {
		_, _ = s.finalizeMatch(ctx, match.ID, false)
		if current, getErr := s.repository.GetMatchForPlayer(ctx, match.ID, actor.PlayerID); getErr == nil {
			match.State = current.State
		}
	}
	return ReportResult{
		ReportID: reportID, MatchID: match.ID, Status: record.ValidationStatus,
		RiskSeverity: record.RiskSeverity, Warnings: record.ValidationWarnings,
		CollectionState: match.State,
	}, nil
}

func (s *Service) Result(ctx context.Context, actor Actor, matchID string) (FinalizedResult, error) {
	if err := s.requireEnabledActor(actor); err != nil {
		return FinalizedResult{}, err
	}
	match, err := s.repository.GetMatchForPlayer(ctx, matchID, actor.PlayerID)
	if err != nil {
		return FinalizedResult{}, mapNotFound(err, "P2P_MATCH_NOT_FOUND", "P2P match not found.")
	}
	result, err := s.repository.GetFinalizedResult(ctx, match)
	if err != nil {
		return FinalizedResult{}, internal(err)
	}
	return result, nil
}

func (s *Service) FinalizeDue(ctx context.Context) (int, error) {
	if !s.config.Enabled {
		return 0, nil
	}
	now := s.now().UTC()
	if _, err := s.repository.OpenCollectionForClosedRooms(
		ctx, now, now.Add(s.config.CollectionDeadline()),
	); err != nil {
		return 0, err
	}
	ids, err := s.repository.ListFinalizableMatchIDs(ctx, now, 100)
	if err != nil {
		return 0, err
	}
	finalized := 0
	for _, id := range ids {
		changed, err := s.finalizeMatch(ctx, id, true)
		if err != nil {
			return finalized, err
		}
		if changed {
			finalized++
		}
	}
	return finalized, nil
}

func (s *Service) finalizeMatch(ctx context.Context, matchID string, deadlineReached bool) (bool, error) {
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	session, err := s.repository.GetMatchForUpdate(ctx, tx, matchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !activeMatchState(session.State) {
		return false, nil
	}
	reports, err := s.repository.ListFinalReports(ctx, tx, matchID)
	if err != nil {
		return false, err
	}
	allArrived := len(reports) >= session.ExpectedReporterCount && session.ExpectedReporterCount > 0
	hardExpired := !now.Before(session.HardExpiresAt)
	collectionExpired := session.CollectionDeadline != nil && !now.Before(*session.CollectionDeadline)
	if !allArrived && !deadlineReached && !hardExpired && !collectionExpired {
		return false, nil
	}
	if !allArrived && !hardExpired && !collectionExpired {
		return false, nil
	}

	decision := buildDecision(session, reports, now)
	if hardExpired && len(reports) == 0 {
		decision.State = MatchExpired
		decision.RiskSeverity = "MEDIUM"
		decision.Reasons = []string{"P2P_MATCH_HARD_EXPIRY_WITHOUT_REPORTS"}
	}
	var outcomeDigest []byte
	matchingCount := 0
	outcomeCounts := make(map[string]int)
	outcomeDigests := make(map[string][]byte)
	for _, report := range reports {
		if report.ValidationStatus == "QUARANTINED" {
			continue
		}
		key := hex.EncodeToString(report.OutcomeSHA256)
		outcomeCounts[key]++
		outcomeDigests[key] = report.OutcomeSHA256
		if outcomeCounts[key] > matchingCount {
			matchingCount = outcomeCounts[key]
			outcomeDigest = slices.Clone(outcomeDigests[key])
		}
	}
	if len(outcomeDigest) == 0 {
		outcomeDigest = make([]byte, 32)
	}
	if err := s.repository.SaveDecision(
		ctx, tx, session, decision, outcomeDigest, 1, matchingCount, now,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func buildDecision(session MatchSession, reports []ReportRecord, now time.Time) FinalizedResult {
	result := FinalizedResult{
		MatchID: session.ID, State: MatchIncomplete, MatchType: session.MatchType,
		EligibleCount: session.ExpectedReporterCount, ReceivedCount: len(reports),
		RequiredQuorum: requiredQuorum(session.MatchType, session.ExpectedReporterCount),
		PolicyVersion:  session.PolicyVersion, TeamScores: []int{}, Reasons: []string{},
		FinalizedAt: &now,
	}
	views := make([]normalizedReportView, 0, len(reports))
	outcomeGroups := make(map[string][]normalizedReportView)
	for _, report := range reports {
		if report.ValidationStatus == "QUARANTINED" {
			continue
		}
		var normalized NormalizedResult
		if err := json.Unmarshal(report.NormalizedResult, &normalized); err != nil {
			continue
		}
		view := normalizedReportView{reporter: report.ReporterPlayerID, report: report, result: normalized}
		views = append(views, view)
		key := hex.EncodeToString(report.OutcomeSHA256)
		outcomeGroups[key] = append(outcomeGroups[key], view)
	}
	if len(views) == 0 {
		result.Reasons = append(result.Reasons, "NO_ACCEPTED_FINAL_REPORTS")
		result.RiskSeverity = "HIGH"
		return result
	}
	if len(outcomeGroups) > 1 {
		result.State = MatchDisputed
		result.RiskSeverity = "HIGH"
		result.Reasons = append(result.Reasons, "CONFLICTING_FINAL_OUTCOMES")
		return result
	}
	var agreeing []normalizedReportView
	for _, group := range outcomeGroups {
		agreeing = group
	}
	outcome := agreeing[0].result.Outcome
	result.MatchType = outcome.MatchType
	result.ModeAlias = outcome.ModeAlias
	result.MapAlias = outcome.MapAlias
	winner := outcome.WinnerTeamID
	result.WinnerTeamID = &winner
	result.TeamScores = slices.Clone(outcome.TeamScores)
	result.Rounds = slices.Clone(outcome.Rounds)
	result.TeamCoverage = hasTeamCoverage(outcome, agreeing)

	if outcome.MatchType == "PVE" && len(agreeing) == 1 {
		result.State = MatchSelfReported
		result.TrustTier = "SELF_REPORTED"
		result.RiskSeverity = "MEDIUM"
		result.TeamCoverage = true
		if session.ExpectedReporterCount == 1 {
			result.Reasons = append(result.Reasons, "SINGLE_HUMAN_PVE_SELF_REPORT")
		} else {
			result.RiskSeverity = "HIGH"
			result.Reasons = append(result.Reasons, "PVE_PEERS_MISSING_SELF_REPORT")
		}
	} else if len(agreeing) < result.RequiredQuorum {
		result.Reasons = append(result.Reasons, "REPORT_QUORUM_NOT_REACHED")
		result.RiskSeverity = "MEDIUM"
		return result
	} else if outcome.MatchType == "PVP" && !result.TeamCoverage {
		result.Reasons = append(result.Reasons, "PVP_TEAM_COVERAGE_NOT_REACHED")
		result.RiskSeverity = "HIGH"
		return result
	} else {
		result.State = MatchPeerConfirmed
		result.TrustTier = "PEER_ATTESTED"
	}

	participantByID := make(map[string]NormalizedParticipant)
	for _, participant := range agreeing[0].result.Participants {
		participantByID[participant.PlayerID] = participant
	}
	playerIDs := make([]string, 0, len(outcome.HumanTeams))
	for _, member := range outcome.HumanTeams {
		playerIDs = append(playerIDs, member.PlayerID)
	}
	sort.Strings(playerIDs)
	for _, playerID := range playerIDs {
		groups := make(map[string][]NormalizedParticipant)
		for _, view := range agreeing {
			for _, participant := range view.result.Participants {
				if participant.PlayerID != playerID {
					continue
				}
				encoded, _ := json.Marshal(participant)
				groups[string(encoded)] = append(groups[string(encoded)], participant)
			}
		}
		base := participantByID[playerID]
		status := "UNVERIFIED"
		if result.State == MatchSelfReported && len(groups) == 1 {
			status = "SELF_ONLY"
		} else if len(groups) > 1 {
			status = "CONFLICTED"
			base.Kills, base.Deaths, base.Assists, base.Score = 0, 0, 0, 0
		} else {
			for _, group := range groups {
				if len(group) >= minInt(2, result.RequiredQuorum) {
					status = "CONSENSUS"
					base = group[0]
				}
			}
		}
		result.Participants = append(result.Participants, FinalizedParticipant{
			NormalizedParticipant: base, StatsStatus: status,
		})
	}
	return result
}

func hasTeamCoverage(outcome NormalizedOutcome, reports []normalizedReportView) bool {
	if outcome.MatchType != "PVP" {
		return true
	}
	playerTeams := make(map[string]int, len(outcome.HumanTeams))
	requiredTeams := make(map[int]struct{})
	for _, member := range outcome.HumanTeams {
		playerTeams[member.PlayerID] = member.TeamID
		requiredTeams[member.TeamID] = struct{}{}
	}
	if len(requiredTeams) < 2 {
		return false
	}
	reportedTeams := make(map[int]struct{})
	for _, report := range reports {
		if teamID, exists := playerTeams[report.reporter]; exists {
			reportedTeams[teamID] = struct{}{}
		}
	}
	for teamID := range requiredTeams {
		if _, exists := reportedTeams[teamID]; !exists {
			return false
		}
	}
	return true
}

func requiredQuorum(matchType string, eligible int) int {
	if eligible <= 0 {
		return 0
	}
	if eligible == 1 {
		return 1
	}
	if matchType == "PVP" && eligible == 2 {
		return 2
	}
	value := int(math.Ceil(float64(eligible) * 2.0 / 3.0))
	if value < 2 {
		return 2
	}
	return value
}

func activeMatchState(state MatchState) bool {
	return state == MatchStarting || state == MatchRunning || state == MatchCollecting
}

func allowedPresenceStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "CONNECTING", "ACTIVE", "DISCONNECTED", "RESULT_SCREEN", "EXIT_INTENT", "LEFT":
		return true
	default:
		return false
	}
}

func matchTypeFromMode(mode string) string {
	upper := strings.ToUpper(strings.TrimSpace(mode))
	if strings.Contains(upper, "PVE") {
		return "PVE"
	}
	if strings.Contains(upper, "PVP") {
		return "PVP"
	}
	return "UNKNOWN"
}

func (s *Service) requireEnabledActor(actor Actor) error {
	if !s.config.Enabled {
		return notFound("P2P_BATTLELOG_DISABLED", "P2P BattleLog intake is not enabled.")
	}
	if strings.TrimSpace(actor.PlayerID) == "" || strings.TrimSpace(actor.SessionID) == "" {
		return unauthorized("P2P_BATTLELOG_AUTH_REQUIRED", "A current verified player session is required.")
	}
	if !actor.SteamVerified || (actor.AuthLevel != "verified" && actor.AuthLevel != "trusted") {
		return forbidden("VERIFIED_SESSION_REQUIRED", "A verified Steam session is required.")
	}
	return nil
}

func mapNotFound(err error, code, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(code, message)
	}
	return internal(err)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
