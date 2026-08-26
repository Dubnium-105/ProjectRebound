package p2pbattlelog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

var errPresenceSegmentLimit = errors.New("P2P presence segment limit reached")

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) EnsureMatchForRoomStart(
	ctx context.Context,
	tx pgx.Tx,
	matchID, roomID, hostPlayerID, mode, matchType, policyVersion string,
	now, hardExpiresAt time.Time,
) (MatchSession, bool, error) {
	existing, err := scanMatchSession(tx.QueryRow(ctx, `
		SELECT `+matchSessionColumns+`
		FROM p2p_match_sessions
		WHERE room_id_snapshot = $1
		  AND state IN ('STARTING', 'RUNNING', 'COLLECTING')
		FOR UPDATE
	`, roomID))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return MatchSession{}, false, fmt.Errorf("load active P2P match: %w", err)
	}

	var sequence int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM p2p_match_sessions
		WHERE room_id_snapshot = $1
	`, roomID).Scan(&sequence); err != nil {
		return MatchSession{}, false, fmt.Errorf("allocate P2P match sequence: %w", err)
	}
	var managedAttemptID string
	rosterRevision := 1
	managedErr := tx.QueryRow(ctx, `
		SELECT attempt.id, attempt.roster_revision
		FROM p2p_rooms AS room
		JOIN match_lobbies AS lobby ON lobby.id = room.managed_lobby_id
		JOIN match_attempts AS attempt ON attempt.id = lobby.current_attempt_id
		WHERE room.id = $1 AND attempt.state IN ('PROVISIONING', 'CONNECTING', 'RUNNING')
	`, roomID).Scan(&managedAttemptID, &rosterRevision)
	if managedErr != nil && !errors.Is(managedErr, pgx.ErrNoRows) {
		return MatchSession{}, false, fmt.Errorf("load authoritative P2P roster: %w", managedErr)
	}
	item := MatchSession{
		ID: matchID, RoomID: roomID, RoomIDSnapshot: roomID, Sequence: sequence,
		HostPlayerIDAtStart: hostPlayerID, Mode: mode, MatchType: matchType,
		State: MatchStarting, RosterRevision: rosterRevision, PolicyVersion: policyVersion,
		HardExpiresAt: hardExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO p2p_match_sessions (
			id, room_id, room_id_snapshot, sequence, host_player_id_at_start,
			mode, match_type, state, roster_revision, expected_reporter_count,
			policy_version, hard_expires_at, created_at, updated_at, match_attempt_id
		) VALUES ($1, $2, $2, $3, $4, $5, $6, 'STARTING', $7, 0, $8, $9, $10, $10, NULLIF($11, ''))
	`, item.ID, roomID, sequence, hostPlayerID, mode, matchType, rosterRevision, policyVersion, hardExpiresAt, now, managedAttemptID)
	if err != nil {
		return MatchSession{}, false, fmt.Errorf("insert P2P match session: %w", err)
	}
	if managedAttemptID != "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO p2p_match_roster (
				match_id, player_id, platform_id, room_role, slot_index, team_id,
				team_slot, connection_generation,
				auth_level_at_start, steam_verified_at_start, is_spectator,
				is_initial_roster, eligible_reporter, joined_room_at, created_at
			)
			SELECT $1, roster.player_id, roster.platform_id, roster.room_role,
			       roster.logical_slot, roster.team_id, roster.team_slot,
			       roster.connection_generation, roster.auth_level_at_freeze,
			       roster.steam_verified_at_freeze, FALSE, TRUE,
			       roster.steam_verified_at_freeze, roster.joined_lobby_at, $2
			FROM match_attempt_roster AS roster
			WHERE roster.attempt_id = $3
			ORDER BY roster.logical_slot
		`, item.ID, now, managedAttemptID)
	} else {
		_, err = tx.Exec(ctx, `
		INSERT INTO p2p_match_roster (
			match_id, player_id, platform_id, room_role, slot_index, team_id,
			auth_level_at_start, steam_verified_at_start, is_spectator,
			is_initial_roster, eligible_reporter, joined_room_at, created_at
		)
		SELECT $1, member.player_id, player.steam_id, member.role,
		       ROW_NUMBER() OVER (
		           ORDER BY CASE WHEN member.role = 'HOST' THEN 0 ELSE 1 END,
		                    member.joined_at, member.player_id
		       ) - 1,
		       NULL, player.auth_level,
		       player.auth_level IN ('verified', 'trusted'), FALSE, TRUE,
		       player.auth_level IN ('verified', 'trusted'), member.joined_at, $2
		FROM p2p_room_members AS member
		JOIN players AS player ON player.id = member.player_id
		WHERE member.room_id = $3 AND member.status = 'ACTIVE'
	`, item.ID, now, roomID)
	}
	if err != nil {
		return MatchSession{}, false, fmt.Errorf("snapshot P2P match roster: %w", err)
	}
	if managedAttemptID != "" {
		if _, err := tx.Exec(ctx, `UPDATE match_attempts SET p2p_match_id = $2, updated_at = $3 WHERE id = $1`, managedAttemptID, item.ID, now); err != nil {
			return MatchSession{}, false, fmt.Errorf("link authoritative P2P match: %w", err)
		}
	}
	if err := tx.QueryRow(ctx, `
		UPDATE p2p_match_sessions AS match
		SET expected_reporter_count = roster.reporter_count,
		    updated_at = $2
		FROM (
			SELECT COUNT(*)::integer AS reporter_count
			FROM p2p_match_roster
			WHERE match_id = $1 AND eligible_reporter AND NOT is_spectator
		) AS roster
		WHERE match.id = $1
		RETURNING match.expected_reporter_count
	`, item.ID, now).Scan(&item.ExpectedReporterCount); err != nil {
		return MatchSession{}, false, fmt.Errorf("count P2P match reporters: %w", err)
	}
	return item, true, nil
}

func (r *Repository) MarkRoomMatchRunning(ctx context.Context, roomID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE p2p_match_sessions
		SET state = 'RUNNING', updated_at = $2
		WHERE room_id_snapshot = $1 AND state = 'STARTING'
		  AND match_attempt_id IS NULL
	`, roomID, now)
	if err != nil {
		return fmt.Errorf("mark P2P match running: %w", err)
	}
	return nil
}

func (r *Repository) GetActiveMatchForRoomPlayer(ctx context.Context, roomID, playerID string) (MatchSession, error) {
	return scanMatchSession(r.pool.QueryRow(ctx, `
		SELECT `+prefixedMatchSessionColumns("match")+`
		FROM p2p_match_sessions AS match
		JOIN p2p_match_roster AS roster
		  ON roster.match_id = match.id AND roster.player_id = $2
		WHERE match.room_id_snapshot = $1
		  AND match.state IN ('STARTING', 'RUNNING', 'COLLECTING')
		ORDER BY match.sequence DESC
		LIMIT 1
	`, roomID, playerID))
}

func (r *Repository) GetMatchForPlayer(ctx context.Context, matchID, playerID string) (MatchSession, error) {
	return scanMatchSession(r.pool.QueryRow(ctx, `
		SELECT `+prefixedMatchSessionColumns("match")+`
		FROM p2p_match_sessions AS match
		JOIN p2p_match_roster AS roster
		  ON roster.match_id = match.id AND roster.player_id = $2
		WHERE match.id = $1
	`, matchID, playerID))
}

func (r *Repository) GetMatchForUpdate(ctx context.Context, tx pgx.Tx, matchID string) (MatchSession, error) {
	return scanMatchSession(tx.QueryRow(ctx, `
		SELECT `+matchSessionColumns+`
		FROM p2p_match_sessions
		WHERE id = $1
		FOR UPDATE
	`, matchID))
}

func (r *Repository) GetMatch(ctx context.Context, matchID string) (MatchSession, error) {
	return scanMatchSession(r.pool.QueryRow(ctx, `
		SELECT `+matchSessionColumns+`
		FROM p2p_match_sessions
		WHERE id = $1
	`, matchID))
}

func (r *Repository) ListRoster(ctx context.Context, executor interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, matchID string) ([]RosterMember, error) {
	rows, err := executor.Query(ctx, `
		SELECT match_id, player_id, platform_id, room_role, slot_index, team_id,
		       auth_level_at_start, steam_verified_at_start, is_spectator,
		       is_initial_roster, eligible_reporter, joined_room_at, created_at
		FROM p2p_match_roster
		WHERE match_id = $1
		ORDER BY slot_index
	`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list P2P match roster: %w", err)
	}
	defer rows.Close()
	items := make([]RosterMember, 0)
	for rows.Next() {
		var item RosterMember
		var teamID sql.NullInt32
		if err := rows.Scan(
			&item.MatchID, &item.PlayerID, &item.PlatformID, &item.RoomRole,
			&item.SlotIndex, &teamID, &item.AuthLevelAtStart,
			&item.SteamVerifiedAtStart, &item.IsSpectator, &item.IsInitialRoster,
			&item.EligibleReporter, &item.JoinedRoomAt, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan P2P match roster: %w", err)
		}
		if teamID.Valid {
			value := int(teamID.Int32)
			item.TeamID = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate P2P match roster: %w", err)
	}
	return items, nil
}

func (r *Repository) GetRosterMember(ctx context.Context, executor interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, matchID, playerID string) (RosterMember, error) {
	var item RosterMember
	var teamID sql.NullInt32
	err := executor.QueryRow(ctx, `
		SELECT match_id, player_id, platform_id, room_role, slot_index, team_id,
		       auth_level_at_start, steam_verified_at_start, is_spectator,
		       is_initial_roster, eligible_reporter, joined_room_at, created_at
		FROM p2p_match_roster
		WHERE match_id = $1 AND player_id = $2
	`, matchID, playerID).Scan(
		&item.MatchID, &item.PlayerID, &item.PlatformID, &item.RoomRole,
		&item.SlotIndex, &teamID, &item.AuthLevelAtStart,
		&item.SteamVerifiedAtStart, &item.IsSpectator, &item.IsInitialRoster,
		&item.EligibleReporter, &item.JoinedRoomAt, &item.CreatedAt,
	)
	if teamID.Valid {
		value := int(teamID.Int32)
		item.TeamID = &value
	}
	return item, err
}

func (r *Repository) RotateCapability(
	ctx context.Context,
	tx pgx.Tx,
	capability Capability,
	now time.Time,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_match_report_capabilities
		SET revoked_at = $3
		WHERE match_id = $1 AND player_id = $2 AND revoked_at IS NULL
	`, capability.MatchID, capability.PlayerID, now); err != nil {
		return fmt.Errorf("revoke previous P2P report capability: %w", err)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO p2p_match_report_capabilities (
			id, match_id, player_id, auth_session_id, token_hash, server_nonce,
			expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, capability.ID, capability.MatchID, capability.PlayerID,
		capability.AuthSessionID, capability.TokenHash, capability.ServerNonce,
		capability.ExpiresAt, capability.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert P2P report capability: %w", err)
	}
	return nil
}

func (r *Repository) GetCapabilityForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	capabilityID, matchID, playerID string,
) (Capability, error) {
	return scanCapability(tx.QueryRow(ctx, `
		SELECT id, match_id, player_id, auth_session_id, token_hash, server_nonce,
		       expires_at, first_used_at, last_used_at, revoked_at, created_at
		FROM p2p_match_report_capabilities
		WHERE id = $1 AND match_id = $2 AND player_id = $3
		FOR UPDATE
	`, capabilityID, matchID, playerID))
}

func (r *Repository) CapabilitySessionMatches(
	ctx context.Context,
	tx pgx.Tx,
	issuedSessionID, currentSessionID, playerID string,
) (bool, error) {
	var matches bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM auth_sessions AS issued
			JOIN auth_sessions AS current
			  ON current.token_family_id = issued.token_family_id
			 AND current.player_id = issued.player_id
			WHERE issued.id = $1
			  AND current.id = $2
			  AND issued.player_id = $3
		)
	`, issuedSessionID, currentSessionID, playerID).Scan(&matches)
	if err != nil {
		return false, fmt.Errorf("validate P2P capability session family: %w", err)
	}
	return matches, nil
}

func (r *Repository) MarkCapabilityUsed(ctx context.Context, tx pgx.Tx, capabilityID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE p2p_match_report_capabilities
		SET first_used_at = COALESCE(first_used_at, $2), last_used_at = $2
		WHERE id = $1
	`, capabilityID, now)
	if err != nil {
		return fmt.Errorf("mark P2P report capability used: %w", err)
	}
	return nil
}

func (r *Repository) OpenCollection(
	ctx context.Context,
	tx pgx.Tx,
	matchID string,
	now, deadline time.Time,
) (MatchSession, error) {
	return scanMatchSession(tx.QueryRow(ctx, `
		UPDATE p2p_match_sessions
		SET state = CASE WHEN state IN ('STARTING', 'RUNNING') THEN 'COLLECTING' ELSE state END,
		    collection_started_at = COALESCE(collection_started_at, $2),
		    collection_deadline = COALESCE(collection_deadline, $3),
		    updated_at = $2
		WHERE id = $1
		RETURNING `+matchSessionColumns+`
	`, matchID, now, deadline))
}

func (r *Repository) AllEligibleReportersAtResultOrLeft(
	ctx context.Context,
	tx pgx.Tx,
	matchID string,
) (bool, error) {
	var allTerminal bool
	err := tx.QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1
			FROM p2p_match_roster AS roster
			WHERE roster.match_id = $1
			  AND roster.eligible_reporter
			  AND NOT roster.is_spectator
			  AND NOT EXISTS (
				SELECT 1
				FROM p2p_match_presence_segments AS presence
				WHERE presence.match_id = roster.match_id
				  AND presence.player_id = roster.player_id
				  AND presence.segment_no = (
					SELECT MAX(latest.segment_no)
					FROM p2p_match_presence_segments AS latest
					WHERE latest.match_id = roster.match_id
					  AND latest.player_id = roster.player_id
				  )
				  AND presence.status IN ('RESULT_SCREEN', 'EXIT_INTENT', 'LEFT')
			  )
		)
	`, matchID).Scan(&allTerminal)
	if err != nil {
		return false, fmt.Errorf("check terminal P2P reporter presence: %w", err)
	}
	return allTerminal, nil
}

func (r *Repository) OpenCollectionForClosedRooms(
	ctx context.Context,
	now, deadline time.Time,
) (int64, error) {
	command, err := r.pool.Exec(ctx, `
		UPDATE p2p_match_sessions AS match
		SET state = 'COLLECTING', collection_started_at = $1,
		    collection_deadline = $2, updated_at = $1
		FROM p2p_rooms AS room
		WHERE match.room_id = room.id
		  AND room.state = 'CLOSED'
		  AND match.state IN ('STARTING', 'RUNNING')
		  AND match.collection_deadline IS NULL
	`, now, deadline)
	if err != nil {
		return 0, fmt.Errorf("open P2P collection for closed rooms: %w", err)
	}
	return command.RowsAffected(), nil
}

func (r *Repository) FindReport(
	ctx context.Context,
	executor interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	matchID, playerID, reportID string,
) (ReportRecord, error) {
	return scanReport(executor.QueryRow(ctx, `
		SELECT `+reportColumns+`
		FROM p2p_battlelog_reports
		WHERE match_id = $1 AND reporter_player_id = $2 AND report_id = $3
	`, matchID, playerID, reportID))
}

func (r *Repository) FindFinalReportForPlayer(
	ctx context.Context,
	executor interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	matchID, playerID string,
) (ReportRecord, error) {
	return scanReport(executor.QueryRow(ctx, `
		SELECT `+reportColumns+`
		FROM p2p_battlelog_reports
		WHERE match_id = $1 AND reporter_player_id = $2 AND completeness = 'FINAL'
	`, matchID, playerID))
}

func (r *Repository) InsertReport(ctx context.Context, tx pgx.Tx, report ReportRecord) error {
	warnings, err := json.Marshal(report.ValidationWarnings)
	if err != nil {
		return fmt.Errorf("marshal P2P validation warnings: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO p2p_battlelog_reports (
			id, report_id, match_id, reporter_player_id, capability_id,
			report_revision, completeness, schema_name, schema_version,
			authority_kind, client_version, timeline_session_id,
			captured_at, received_at, event_count, raw_size_bytes,
			raw_sha256, outcome_sha256, stats_sha256, raw_payload, raw_snapshot,
			normalized_result, validation_status, risk_severity, validation_warnings
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, NULLIF($24, ''), $25
		)
	`, report.ID, report.ReportID, report.MatchID, report.ReporterPlayerID,
		report.CapabilityID, report.ReportRevision, report.Completeness,
		report.SchemaName, report.SchemaVersion, report.AuthorityKind,
		report.ClientVersion, report.TimelineSessionID, report.CapturedAt,
		report.ReceivedAt, report.EventCount, report.RawSizeBytes,
		report.RawSHA256, report.OutcomeSHA256, report.StatsSHA256,
		report.RawSnapshot, report.RawSnapshot, report.NormalizedResult, report.ValidationStatus,
		report.RiskSeverity, warnings)
	if err != nil {
		return fmt.Errorf("insert P2P BattleLog report: %w", err)
	}
	return nil
}

func (r *Repository) UpsertPresence(
	ctx context.Context,
	tx pgx.Tx,
	matchID, playerID, timelineSessionID, status string,
	presenceSeq, lastCheckpoint uint64,
	processAlive, gameConnected bool,
	now time.Time,
) (PresenceResult, error) {
	var latestID string
	var latestSegment int
	var latestSeq uint64
	var latestStatus string
	var latestTimeline sql.NullString
	var leftAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT id, segment_no, presence_seq, status, timeline_session_id, left_at
		FROM p2p_match_presence_segments
		WHERE match_id = $1 AND player_id = $2
		ORDER BY segment_no DESC
		LIMIT 1
		FOR UPDATE
	`, matchID, playerID).Scan(
		&latestID, &latestSegment, &latestSeq, &latestStatus, &latestTimeline, &leftAt,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PresenceResult{}, fmt.Errorf("load P2P presence segment: %w", err)
	}
	if err == nil && presenceSeq <= latestSeq && latestTimeline.String == timelineSessionID {
		return PresenceResult{
			MatchID: matchID, PlayerID: playerID, SegmentNo: latestSegment,
			PresenceSeq: latestSeq, Status: latestStatus, LastPresence: now,
			WasDuplicate: true,
		}, nil
	}

	openReconnect := errors.Is(err, pgx.ErrNoRows) || leftAt.Valid ||
		(latestTimeline.Valid && latestTimeline.String != timelineSessionID)
	if openReconnect {
		if latestSegment >= 32 {
			return PresenceResult{}, errPresenceSegmentLimit
		}
		segmentNo := latestSegment + 1
		joinKind := "INITIAL"
		if segmentNo > 1 {
			joinKind = "RECONNECT"
		}
		segmentID := newID("p2ps_")
		_, err = tx.Exec(ctx, `
			INSERT INTO p2p_match_presence_segments (
				id, match_id, player_id, segment_no, join_kind, status,
				timeline_session_id, presence_seq, last_checkpoint_seq,
				game_process_alive, game_connected, joined_at, became_active_at,
				last_presence_at, disconnected_at, left_at, leave_kind
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				CASE WHEN $6 = 'ACTIVE' THEN $12 ELSE NULL END, $12,
				CASE WHEN $6 = 'DISCONNECTED' THEN $12 ELSE NULL END,
				CASE WHEN $6 IN ('EXIT_INTENT', 'LEFT') THEN $12 ELSE NULL END,
				CASE WHEN $6 = 'EXIT_INTENT' THEN 'VOLUNTARY'
				     WHEN $6 = 'LEFT' THEN 'UNKNOWN' ELSE NULL END
			)
		`, segmentID, matchID, playerID, segmentNo, joinKind, status,
			timelineSessionID, presenceSeq, lastCheckpoint,
			processAlive, gameConnected, now)
		if err != nil {
			return PresenceResult{}, fmt.Errorf("insert P2P presence segment: %w", err)
		}
		return PresenceResult{
			MatchID: matchID, PlayerID: playerID, SegmentNo: segmentNo,
			PresenceSeq: presenceSeq, Status: status, LastPresence: now,
			ReconnectOpen: segmentNo > 1,
		}, nil
	}

	_, err = tx.Exec(ctx, `
		UPDATE p2p_match_presence_segments
		SET status = $2, timeline_session_id = $3, presence_seq = $4,
		    last_checkpoint_seq = $5, game_process_alive = $6,
		    game_connected = $7, last_presence_at = $8,
		    became_active_at = CASE
		        WHEN $2 = 'ACTIVE' THEN COALESCE(became_active_at, $8)
		        ELSE became_active_at END,
		    disconnected_at = CASE WHEN $2 = 'DISCONNECTED' THEN $8 ELSE disconnected_at END,
		    left_at = CASE WHEN $2 IN ('EXIT_INTENT', 'LEFT') THEN $8 ELSE left_at END,
		    leave_kind = CASE WHEN $2 = 'EXIT_INTENT' THEN 'VOLUNTARY'
		                      WHEN $2 = 'LEFT' THEN COALESCE(leave_kind, 'UNKNOWN')
		                      ELSE leave_kind END
		WHERE id = $1
	`, latestID, status, timelineSessionID, presenceSeq, lastCheckpoint,
		processAlive, gameConnected, now)
	if err != nil {
		return PresenceResult{}, fmt.Errorf("update P2P presence segment: %w", err)
	}
	return PresenceResult{
		MatchID: matchID, PlayerID: playerID, SegmentNo: latestSegment,
		PresenceSeq: presenceSeq, Status: status, LastPresence: now,
	}, nil
}

func (r *Repository) ListFinalReports(
	ctx context.Context,
	executor interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	},
	matchID string,
) ([]ReportRecord, error) {
	rows, err := executor.Query(ctx, `
		SELECT `+reportColumns+`
		FROM p2p_battlelog_reports
		WHERE match_id = $1 AND completeness = 'FINAL'
		ORDER BY reporter_player_id
	`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list final P2P reports: %w", err)
	}
	defer rows.Close()
	items := make([]ReportRecord, 0)
	for rows.Next() {
		item, err := scanReport(rows)
		if err != nil {
			return nil, fmt.Errorf("scan final P2P report: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate final P2P reports: %w", err)
	}
	return items, nil
}

func (r *Repository) ListFinalizableMatchIDs(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id
		FROM p2p_match_sessions
		WHERE state IN ('STARTING', 'RUNNING', 'COLLECTING')
		  AND (
		      (collection_deadline IS NOT NULL AND collection_deadline <= $1)
		      OR hard_expires_at <= $1
		  )
		ORDER BY COALESCE(collection_deadline, hard_expires_at), id
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list finalizable P2P matches: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan finalizable P2P match: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) SaveDecision(
	ctx context.Context,
	tx pgx.Tx,
	session MatchSession,
	result FinalizedResult,
	outcomeDigest []byte,
	decisionRevision, matchingCount int,
	now time.Time,
) error {
	reasons, err := json.Marshal(result.Reasons)
	if err != nil {
		return fmt.Errorf("marshal P2P decision reasons: %w", err)
	}
	decisionID := newID("p2pd_")
	_, err = tx.Exec(ctx, `
		INSERT INTO p2p_battlelog_decisions (
			id, match_id, decision_revision, policy_version,
			eligible_reporter_count, received_final_count, matching_outcome_count,
			required_quorum, team_coverage, decision, risk_severity, reasons, decided_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), $12, $13)
	`, decisionID, session.ID, decisionRevision, result.PolicyVersion,
		result.EligibleCount, result.ReceivedCount, matchingCount, result.RequiredQuorum,
		result.TeamCoverage, result.State, result.RiskSeverity, reasons, now)
	if err != nil {
		return fmt.Errorf("insert P2P BattleLog decision: %w", err)
	}

	if result.State == MatchPeerConfirmed || result.State == MatchSelfReported {
		teamScores, err := json.Marshal(result.TeamScores)
		if err != nil {
			return fmt.Errorf("marshal P2P team scores: %w", err)
		}
		winner := any(nil)
		if result.WinnerTeamID != nil {
			winner = *result.WinnerTeamID
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO p2p_battlelog_matches (
				match_id, trust_tier, validation_status, risk_severity,
				mode_alias, map_alias, match_type, winner_team_id, team_scores,
				outcome_sha256, policy_version, decided_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9,
				$10, $11, $12, $12, $12
			)
		`, session.ID, result.TrustTier, result.State, result.RiskSeverity,
			result.ModeAlias, result.MapAlias, result.MatchType, winner, teamScores,
			outcomeDigest, result.PolicyVersion, now)
		if err != nil {
			return fmt.Errorf("insert normalized P2P BattleLog match: %w", err)
		}
		for _, participant := range result.Participants {
			_, err = tx.Exec(ctx, `
				INSERT INTO p2p_battlelog_participants (
					id, match_id, player_id, team_id, outcome, stats_status,
					kills, deaths, assists, score, is_quitter, is_inactive, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			`, newID("p2pp_"), session.ID, participant.PlayerID, participant.TeamID,
				participant.Outcome, participant.StatsStatus, participant.Kills,
				participant.Deaths, participant.Assists, participant.Score,
				participant.IsQuitter, participant.IsInactive, now)
			if err != nil {
				return fmt.Errorf("insert normalized P2P participant: %w", err)
			}
		}
		for _, round := range result.Rounds {
			teamScores, err := json.Marshal(round.TeamScores)
			if err != nil {
				return fmt.Errorf("marshal normalized P2P round scores: %w", err)
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO p2p_battlelog_rounds (
					match_id, round_index, winner_team_id, is_final_round, team_scores
				) VALUES ($1, $2, $3, $4, $5)
			`, session.ID, round.RoundIndex, round.WinnerTeamID, round.IsFinalRound, teamScores)
			if err != nil {
				return fmt.Errorf("insert normalized P2P round: %w", err)
			}
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE p2p_match_sessions
		SET state = $2, map_alias = $3, finalized_at = $4, updated_at = $4
		WHERE id = $1
	`, session.ID, result.State, result.MapAlias, now)
	if err != nil {
		return fmt.Errorf("finalize P2P match session: %w", err)
	}
	return nil
}

func (r *Repository) GetFinalizedResult(ctx context.Context, match MatchSession) (FinalizedResult, error) {
	result := FinalizedResult{
		MatchID: match.ID, State: match.State, MatchType: match.MatchType,
		EligibleCount: match.ExpectedReporterCount, PolicyVersion: match.PolicyVersion,
		FinalizedAt: match.FinalizedAt, Reasons: []string{}, TeamScores: []int{},
	}
	if match.State == MatchStarting || match.State == MatchRunning || match.State == MatchCollecting {
		var received int
		if err := r.pool.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM p2p_battlelog_reports
			WHERE match_id = $1 AND completeness = 'FINAL'
		`, match.ID).Scan(&received); err != nil {
			return FinalizedResult{}, fmt.Errorf("count collected P2P reports: %w", err)
		}
		result.ReceivedCount = received
		result.RequiredQuorum = requiredQuorum(match.MatchType, match.ExpectedReporterCount)
		return result, nil
	}

	var reasonsJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT eligible_reporter_count, received_final_count, required_quorum,
		       team_coverage, COALESCE(risk_severity, ''), reasons
		FROM p2p_battlelog_decisions
		WHERE match_id = $1
		ORDER BY decision_revision DESC
		LIMIT 1
	`, match.ID).Scan(
		&result.EligibleCount, &result.ReceivedCount, &result.RequiredQuorum,
		&result.TeamCoverage, &result.RiskSeverity, &reasonsJSON,
	)
	if err != nil {
		return FinalizedResult{}, fmt.Errorf("load P2P decision: %w", err)
	}
	_ = json.Unmarshal(reasonsJSON, &result.Reasons)
	if match.State != MatchPeerConfirmed && match.State != MatchSelfReported {
		return result, nil
	}
	var winner sql.NullInt32
	var scoresJSON []byte
	err = r.pool.QueryRow(ctx, `
		SELECT trust_tier, match_type, mode_alias, map_alias, winner_team_id,
		       team_scores, COALESCE(risk_severity, '')
		FROM p2p_battlelog_matches
		WHERE match_id = $1
	`, match.ID).Scan(
		&result.TrustTier, &result.MatchType, &result.ModeAlias, &result.MapAlias,
		&winner, &scoresJSON, &result.RiskSeverity,
	)
	if err != nil {
		return FinalizedResult{}, fmt.Errorf("load normalized P2P match: %w", err)
	}
	if winner.Valid {
		value := int(winner.Int32)
		result.WinnerTeamID = &value
	}
	_ = json.Unmarshal(scoresJSON, &result.TeamScores)
	roundRows, err := r.pool.Query(ctx, `
		SELECT round_index, winner_team_id, team_scores, is_final_round
		FROM p2p_battlelog_rounds
		WHERE match_id = $1
		ORDER BY round_index
	`, match.ID)
	if err != nil {
		return FinalizedResult{}, fmt.Errorf("list normalized P2P rounds: %w", err)
	}
	for roundRows.Next() {
		var item NormalizedRound
		var roundScoresJSON []byte
		if err := roundRows.Scan(
			&item.RoundIndex, &item.WinnerTeamID, &roundScoresJSON, &item.IsFinalRound,
		); err != nil {
			roundRows.Close()
			return FinalizedResult{}, fmt.Errorf("scan normalized P2P round: %w", err)
		}
		_ = json.Unmarshal(roundScoresJSON, &item.TeamScores)
		result.Rounds = append(result.Rounds, item)
	}
	if err := roundRows.Err(); err != nil {
		roundRows.Close()
		return FinalizedResult{}, fmt.Errorf("iterate normalized P2P rounds: %w", err)
	}
	roundRows.Close()
	rows, err := r.pool.Query(ctx, `
		SELECT player_id, team_id, outcome, stats_status, kills, deaths,
		       assists, score, is_quitter, is_inactive
		FROM p2p_battlelog_participants
		WHERE match_id = $1
		ORDER BY player_id
	`, match.ID)
	if err != nil {
		return FinalizedResult{}, fmt.Errorf("list normalized P2P participants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item FinalizedParticipant
		if err := rows.Scan(
			&item.PlayerID, &item.TeamID, &item.Outcome, &item.StatsStatus,
			&item.Kills, &item.Deaths, &item.Assists, &item.Score,
			&item.IsQuitter, &item.IsInactive,
		); err != nil {
			return FinalizedResult{}, fmt.Errorf("scan normalized P2P participant: %w", err)
		}
		result.Participants = append(result.Participants, item)
	}
	return result, rows.Err()
}

const matchSessionColumns = `
	id, room_id, room_id_snapshot, sequence, host_player_id_at_start,
	mode, map_alias, match_type, state, roster_revision,
	expected_reporter_count, policy_version, collection_started_at,
	collection_deadline, hard_expires_at, finalized_at, created_at, updated_at
`

func prefixedMatchSessionColumns(alias string) string {
	return alias + `.id, ` + alias + `.room_id, ` + alias + `.room_id_snapshot, ` +
		alias + `.sequence, ` + alias + `.host_player_id_at_start, ` + alias + `.mode, ` +
		alias + `.map_alias, ` + alias + `.match_type, ` + alias + `.state, ` +
		alias + `.roster_revision, ` + alias + `.expected_reporter_count, ` +
		alias + `.policy_version, ` + alias + `.collection_started_at, ` +
		alias + `.collection_deadline, ` + alias + `.hard_expires_at, ` +
		alias + `.finalized_at, ` + alias + `.created_at, ` + alias + `.updated_at`
}

func scanMatchSession(row pgx.Row) (MatchSession, error) {
	var item MatchSession
	var roomID sql.NullString
	var collectionStarted, collectionDeadline, finalized sql.NullTime
	err := row.Scan(
		&item.ID, &roomID, &item.RoomIDSnapshot, &item.Sequence,
		&item.HostPlayerIDAtStart, &item.Mode, &item.MapAlias, &item.MatchType,
		&item.State, &item.RosterRevision, &item.ExpectedReporterCount,
		&item.PolicyVersion, &collectionStarted, &collectionDeadline,
		&item.HardExpiresAt, &finalized, &item.CreatedAt, &item.UpdatedAt,
	)
	if roomID.Valid {
		item.RoomID = roomID.String
	}
	if collectionStarted.Valid {
		item.CollectionStartedAt = &collectionStarted.Time
	}
	if collectionDeadline.Valid {
		item.CollectionDeadline = &collectionDeadline.Time
	}
	if finalized.Valid {
		item.FinalizedAt = &finalized.Time
	}
	return item, err
}

func scanCapability(row pgx.Row) (Capability, error) {
	var item Capability
	var firstUsed, lastUsed, revoked sql.NullTime
	err := row.Scan(
		&item.ID, &item.MatchID, &item.PlayerID, &item.AuthSessionID,
		&item.TokenHash, &item.ServerNonce, &item.ExpiresAt,
		&firstUsed, &lastUsed, &revoked, &item.CreatedAt,
	)
	if firstUsed.Valid {
		item.FirstUsedAt = &firstUsed.Time
	}
	if lastUsed.Valid {
		item.LastUsedAt = &lastUsed.Time
	}
	if revoked.Valid {
		item.RevokedAt = &revoked.Time
	}
	return item, err
}

const reportColumns = `
	id, report_id, match_id, reporter_player_id, capability_id,
	report_revision, completeness, schema_name, schema_version, authority_kind,
	client_version, timeline_session_id, captured_at, received_at, event_count,
	raw_size_bytes, raw_sha256, outcome_sha256, stats_sha256, raw_snapshot,
	normalized_result, validation_status, COALESCE(risk_severity, ''), validation_warnings
`

func scanReport(row pgx.Row) (ReportRecord, error) {
	var item ReportRecord
	var warningsJSON []byte
	err := row.Scan(
		&item.ID, &item.ReportID, &item.MatchID, &item.ReporterPlayerID,
		&item.CapabilityID, &item.ReportRevision, &item.Completeness,
		&item.SchemaName, &item.SchemaVersion, &item.AuthorityKind,
		&item.ClientVersion, &item.TimelineSessionID, &item.CapturedAt,
		&item.ReceivedAt, &item.EventCount, &item.RawSizeBytes, &item.RawSHA256,
		&item.OutcomeSHA256, &item.StatsSHA256, &item.RawSnapshot,
		&item.NormalizedResult, &item.ValidationStatus, &item.RiskSeverity,
		&warningsJSON,
	)
	if err == nil {
		_ = json.Unmarshal(warningsJSON, &item.ValidationWarnings)
	}
	return item, err
}
