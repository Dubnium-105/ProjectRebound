package p2pbattlelog

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type AdminService struct {
	repository *Repository
	shadowMode bool
}

func NewAdminService(repository *Repository, shadowMode bool) *AdminService {
	return &AdminService{repository: repository, shadowMode: shadowMode}
}

func (s *AdminService) MatchEvidence(ctx context.Context, matchID string) (AdminMatchEvidence, error) {
	match, err := s.repository.GetMatch(ctx, matchID)
	if err != nil {
		return AdminMatchEvidence{}, err
	}
	roster, err := s.repository.ListRoster(ctx, s.repository.pool, match.ID)
	if err != nil {
		return AdminMatchEvidence{}, err
	}
	presence, err := s.repository.ListAdminPresence(ctx, match.ID)
	if err != nil {
		return AdminMatchEvidence{}, err
	}
	reports, err := s.repository.ListAdminReports(ctx, match.ID)
	if err != nil {
		return AdminMatchEvidence{}, err
	}
	result, err := s.repository.GetFinalizedResult(ctx, match)
	if err != nil {
		return AdminMatchEvidence{}, err
	}
	adminRoster := make([]AdminRosterMember, 0, len(roster))
	for _, member := range roster {
		adminRoster = append(adminRoster, AdminRosterMember{
			PlayerID: member.PlayerID, PlatformID: member.PlatformID,
			RoomRole: member.RoomRole, SlotIndex: member.SlotIndex,
			AuthLevelAtStart:     member.AuthLevelAtStart,
			SteamVerifiedAtStart: member.SteamVerifiedAtStart,
			EligibleReporter:     member.EligibleReporter, IsSpectator: member.IsSpectator,
		})
	}
	return AdminMatchEvidence{
		Match: match, Roster: adminRoster, Presence: presence, Reports: reports,
		Result: result, ShadowMode: s.shadowMode, StorageClass: "P2P_PEER_EVIDENCE",
	}, nil
}

func (s *AdminService) RawEvidence(ctx context.Context, evidenceID string) (AdminRawEvidence, error) {
	return s.repository.GetAdminRawEvidence(ctx, evidenceID)
}

func (r *Repository) ListAdminPresence(ctx context.Context, matchID string) ([]AdminPresence, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT player_id, segment_no, join_kind, status,
		       COALESCE(timeline_session_id, ''), presence_seq, last_checkpoint_seq,
		       joined_at, last_presence_at, left_at, COALESCE(leave_kind, '')
		FROM p2p_match_presence_segments
		WHERE match_id = $1
		ORDER BY player_id, segment_no
	`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list administrative P2P presence: %w", err)
	}
	defer rows.Close()
	items := make([]AdminPresence, 0)
	for rows.Next() {
		var item AdminPresence
		var leftAt sql.NullTime
		if err := rows.Scan(
			&item.PlayerID, &item.SegmentNo, &item.JoinKind, &item.Status,
			&item.TimelineSessionID, &item.PresenceSeq, &item.LastCheckpointSeq,
			&item.JoinedAt, &item.LastPresenceAt, &leftAt, &item.LeaveKind,
		); err != nil {
			return nil, fmt.Errorf("scan administrative P2P presence: %w", err)
		}
		if leftAt.Valid {
			item.LeftAt = &leftAt.Time
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ListAdminReports(ctx context.Context, matchID string) ([]AdminReportSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, report_id, reporter_player_id, completeness, authority_kind,
		       captured_at, received_at, event_count, raw_size_bytes,
		       raw_sha256, outcome_sha256, stats_sha256, validation_status,
		       COALESCE(risk_severity, ''), jsonb_array_length(validation_warnings)
		FROM p2p_battlelog_reports
		WHERE match_id = $1
		ORDER BY received_at, id
	`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list administrative P2P reports: %w", err)
	}
	defer rows.Close()
	items := make([]AdminReportSummary, 0)
	for rows.Next() {
		var item AdminReportSummary
		var rawDigest, outcomeDigest, statsDigest []byte
		if err := rows.Scan(
			&item.EvidenceID, &item.ReportID, &item.ReporterPlayerID,
			&item.Completeness, &item.AuthorityKind, &item.CapturedAt,
			&item.ReceivedAt, &item.EventCount, &item.RawSizeBytes,
			&rawDigest, &outcomeDigest, &statsDigest, &item.ValidationStatus,
			&item.RiskSeverity, &item.WarningCount,
		); err != nil {
			return nil, fmt.Errorf("scan administrative P2P report: %w", err)
		}
		item.RawSHA256 = hex.EncodeToString(rawDigest)
		item.OutcomeSHA256 = hex.EncodeToString(outcomeDigest)
		item.StatsSHA256 = hex.EncodeToString(statsDigest)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) GetAdminRawEvidence(ctx context.Context, evidenceID string) (AdminRawEvidence, error) {
	var item AdminRawEvidence
	var digest []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, match_id, reporter_player_id, raw_sha256,
		       validation_status, COALESCE(risk_severity, ''), raw_payload
		FROM p2p_battlelog_reports
		WHERE id = $1
	`, evidenceID).Scan(
		&item.EvidenceID, &item.MatchID, &item.ReporterPlayerID, &digest,
		&item.ValidationStatus, &item.RiskSeverity, &item.Snapshot,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return AdminRawEvidence{}, err
		}
		return AdminRawEvidence{}, fmt.Errorf("load administrative raw P2P evidence: %w", err)
	}
	item.RawSHA256 = hex.EncodeToString(digest)
	return item, nil
}
