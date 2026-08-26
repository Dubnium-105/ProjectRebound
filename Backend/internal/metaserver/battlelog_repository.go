package metaserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (r *Repository) SubmitBattleLog(
	ctx context.Context,
	principal GameServerPrincipal,
	reportID string,
	result normalizedBattleLog,
) (BattleLogSubmission, error) {
	if !principal.HasScope("meta.battlelog.write") {
		return BattleLogSubmission{}, forbidden(
			"META_GAME_SERVER_SCOPE_REQUIRED",
			"Game Server token scope is required.",
		)
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `
		SELECT id FROM game_servers WHERE id = $1 FOR UPDATE
	`, principal.ServerID); err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	existing, found, err := loadExistingBattleLog(
		ctx, tx, principal.ServerID, reportID,
	)
	if err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	if found {
		if !bytes.Equal(existing.digest, result.Digest) {
			return BattleLogSubmission{}, conflict(
				"BATTLELOG_REPORT_CONFLICT",
				"The report ID has already been used with different content.",
			)
		}
		existing.submission.Duplicate = true
		return existing.submission, nil
	}

	metaMatchID, matchIDSource, err := resolveBattleLogMetaMatch(
		ctx, tx, principal.ServerID, strings.TrimSpace(result.Snapshot.MatchID),
	)
	if err != nil {
		return BattleLogSubmission{}, err
	}
	if err := resolveBattleLogParticipantIdentities(
		ctx, tx, metaMatchID, &result,
	); err != nil {
		return BattleLogSubmission{}, err
	}
	result.finishValidation()
	official := metaMatchID != "" && result.Status != BattleLogQuarantined
	for index := range result.Participants {
		participant := &result.Participants[index]
		participant.OfficialEligible = official &&
			participant.RosterVerified &&
			participant.SteamVerified
	}

	battleLogID := newMetaID("bl_")
	now := r.now().UTC()
	warningsJSON, err := json.Marshal(result.Warnings)
	if err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO battlelog_matches (
			id, report_id, game_server_id, meta_match_id, source_match_id,
			match_id_source, schema_name, schema_version, match_type,
			validation_status, risk_severity, is_official, mode_alias,
			map_alias, map_display_name, winner_team_id, duration_ms,
			captured_at, reported_at, raw_snapshot, raw_sha256,
			validation_warnings
		) VALUES (
			$1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9,
			$10, NULLIF($11, ''), $12, $13, $14, $15, $16, $17,
			$18, $19, $20::jsonb, $21, $22::jsonb
		)
	`,
		battleLogID, reportID, principal.ServerID, metaMatchID,
		strings.TrimSpace(result.Snapshot.MatchID), matchIDSource,
		result.Snapshot.Schema, result.Snapshot.SchemaVersion, result.MatchType,
		result.Status, result.RiskSeverity, official, result.ModeAlias,
		truncateBattleLog(result.Snapshot.GameState.MapAliasName, 128),
		truncateBattleLog(result.Snapshot.GameState.MapDisplayName, 256),
		result.Snapshot.GameState.MatchResult.WinnerTeamID,
		result.DurationMS, result.Snapshot.CapturedAtUTC, now,
		result.CanonicalRaw, result.Digest, warningsJSON,
	)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) &&
			postgresError.Code == "23505" &&
			postgresError.ConstraintName == "battlelog_matches_meta_match_unique" {
			return BattleLogSubmission{}, conflict(
				"BATTLELOG_MATCH_ALREADY_REPORTED",
				"The assigned match already has a BattleLog report.",
			)
		}
		return BattleLogSubmission{}, internalError(err)
	}

	if err := insertBattleLogTeams(ctx, tx, battleLogID, result.Teams); err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	participantIDs, err := insertBattleLogParticipants(
		ctx, tx, battleLogID, result,
	)
	if err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	if err := insertBattleLogRounds(
		ctx, tx, battleLogID, result.Rounds,
	); err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	if metaMatchID != "" {
		if err := completeBattleLogMetaMatch(
			ctx, tx, principal.ServerID, metaMatchID, battleLogID,
			result, participantIDs, now,
		); err != nil {
			return BattleLogSubmission{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return BattleLogSubmission{}, internalError(err)
	}
	return BattleLogSubmission{
		BattleLogID:      battleLogID,
		ReportID:         reportID,
		MetaMatchID:      metaMatchID,
		MatchType:        result.MatchType,
		ValidationStatus: result.Status,
		RiskSeverity:     result.RiskSeverity,
		Official:         official,
		Duplicate:        false,
		Warnings:         result.Warnings,
	}, nil
}

type existingBattleLog struct {
	submission BattleLogSubmission
	digest     []byte
}

func loadExistingBattleLog(
	ctx context.Context,
	tx pgx.Tx,
	serverID, reportID string,
) (existingBattleLog, bool, error) {
	var item existingBattleLog
	var metaMatchID sql.NullString
	var riskSeverity sql.NullString
	var warningsJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT id, meta_match_id, match_type, validation_status,
		       risk_severity, is_official, raw_sha256, validation_warnings
		FROM battlelog_matches
		WHERE game_server_id = $1 AND report_id = $2
		FOR UPDATE
	`, serverID, reportID).Scan(
		&item.submission.BattleLogID,
		&metaMatchID,
		&item.submission.MatchType,
		&item.submission.ValidationStatus,
		&riskSeverity,
		&item.submission.Official,
		&item.digest,
		&warningsJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return existingBattleLog{}, false, nil
	}
	if err != nil {
		return existingBattleLog{}, false, err
	}
	item.submission.ReportID = reportID
	item.submission.MetaMatchID = metaMatchID.String
	item.submission.RiskSeverity = riskSeverity.String
	if err := json.Unmarshal(warningsJSON, &item.submission.Warnings); err != nil {
		return existingBattleLog{}, false, err
	}
	return item, true, nil
}

func resolveBattleLogMetaMatch(
	ctx context.Context,
	tx pgx.Tx,
	serverID, sourceMatchID string,
) (string, string, error) {
	if sourceMatchID != "" {
		var matchID string
		err := tx.QueryRow(ctx, `
			SELECT id
			FROM meta_matches
			WHERE id = $1 AND game_server_id = $2
			  AND state IN ('RESERVED', 'RUNNING')
			  AND match_attempt_id IS NULL
			FOR UPDATE
		`, sourceMatchID, serverID).Scan(&matchID)
		if err == nil {
			return matchID, "SNAPSHOT", nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", internalError(err)
		}
		if strings.HasPrefix(sourceMatchID, "mm_") || strings.HasPrefix(sourceMatchID, "mlm_") {
			return "", "", forbidden(
				"BATTLELOG_MATCH_FORBIDDEN",
				"The match is not assigned to this Game Server.",
			)
		}
		return "", "STANDALONE", nil
	}

	var matchID string
	err := tx.QueryRow(ctx, `
		SELECT id
		FROM meta_matches
		WHERE game_server_id = $1 AND state IN ('RESERVED', 'RUNNING')
		  AND match_attempt_id IS NULL
		FOR UPDATE
	`, serverID).Scan(&matchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "STANDALONE", nil
	}
	if err != nil {
		return "", "", internalError(err)
	}
	return matchID, "ACTIVE_ASSIGNMENT", nil
}

type battleLogRosterIdentity struct {
	PlayerID      string
	SteamID       string
	AuthLevel     string
	SteamVerified bool
}

func resolveBattleLogParticipantIdentities(
	ctx context.Context,
	tx pgx.Tx,
	metaMatchID string,
	result *normalizedBattleLog,
) error {
	rosterBySteam := make(map[string]battleLogRosterIdentity)
	rosterSeen := make(map[string]bool)
	if metaMatchID != "" {
		rows, err := tx.Query(ctx, `
			SELECT player.id, player.steam_id,
			       COALESCE(member.auth_level_at_reservation, player.auth_level),
			       COALESCE(
			           member.steam_verified_at_reservation,
			           player.auth_level IN ('verified', 'trusted')
			       )
			FROM meta_match_players AS member
			JOIN players AS player ON player.id = member.player_id
			WHERE member.match_id = $1
		`, metaMatchID)
		if err != nil {
			return internalError(err)
		}
		for rows.Next() {
			var identity battleLogRosterIdentity
			if err := rows.Scan(
				&identity.PlayerID, &identity.SteamID, &identity.AuthLevel,
				&identity.SteamVerified,
			); err != nil {
				rows.Close()
				return internalError(err)
			}
			rosterBySteam[identity.SteamID] = identity
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return internalError(err)
		}
		rows.Close()
	}

	resolvedPlayers := make(map[string]bool)
	for index := range result.Participants {
		participant := &result.Participants[index]
		if participant.IsAI || participant.PlatformID == "" {
			continue
		}
		identity, rosterVerified := rosterBySteam[participant.PlatformID]
		if !rosterVerified {
			err := tx.QueryRow(ctx, `
				SELECT id, steam_id, auth_level,
				       auth_level IN ('verified', 'trusted')
				FROM players
				WHERE steam_id = $1
			`, participant.PlatformID).Scan(
				&identity.PlayerID, &identity.SteamID, &identity.AuthLevel,
				&identity.SteamVerified,
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return internalError(err)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if metaMatchID != "" {
					result.addWarning(
						"BATTLELOG_PLAYER_IDENTITY_UNKNOWN", "MEDIUM",
						"reported human identity is not known to the backend",
						&index, false,
					)
				}
				continue
			}
		}
		if resolvedPlayers[identity.PlayerID] {
			continue
		}
		resolvedPlayers[identity.PlayerID] = true
		participant.PlayerID = identity.PlayerID
		participant.AuthLevelAtMatch = identity.AuthLevel
		participant.SteamVerified = identity.SteamVerified
		participant.RosterVerified = rosterVerified
		if rosterVerified {
			rosterSeen[identity.PlayerID] = true
		} else if metaMatchID != "" {
			result.addWarning(
				"BATTLELOG_PLAYER_NOT_IN_ROSTER", "MEDIUM",
				"reported human identity is not in the assigned match roster",
				&index, false,
			)
		}
		if rosterVerified && !participant.SteamVerified {
			result.addWarning(
				"BATTLELOG_UNVERIFIED_ROSTER_IDENTITY", "MEDIUM",
				"assigned roster identity is not verified at report time",
				&index, false,
			)
		}
	}
	if metaMatchID != "" {
		for _, identity := range rosterBySteam {
			if !rosterSeen[identity.PlayerID] {
				result.addWarning(
					"BATTLELOG_ROSTER_MEMBER_MISSING", "MEDIUM",
					"an assigned roster member is missing from the report",
					nil, false,
				)
			}
		}
	}
	return nil
}

func insertBattleLogTeams(
	ctx context.Context,
	tx pgx.Tx,
	matchID string,
	teams []normalizedBattleLogTeam,
) error {
	for _, team := range teams {
		_, err := tx.Exec(ctx, `
			INSERT INTO battlelog_teams (
				match_id, team_id, outcome, match_score, kills, deaths,
				assists, score, human_count, ai_count
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, matchID, team.TeamID, team.Outcome, team.MatchScore,
			team.Kills, team.Deaths, team.Assists, team.Score,
			team.HumanCount, team.AICount,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func insertBattleLogParticipants(
	ctx context.Context,
	tx pgx.Tx,
	matchID string,
	result normalizedBattleLog,
) (map[string]string, error) {
	participantIDs := make(map[string]string)
	for _, participant := range result.Participants {
		participantID := newMetaID("blp_")
		_, err := tx.Exec(ctx, `
			INSERT INTO battlelog_participants (
				id, match_id, slot_index, player_id, platform_id,
				player_name, is_ai, roster_verified, official_eligible,
				auth_level_at_match, steam_verified_at_match, team_id,
				camp_id, role_name, role_value, selected_character_id,
				possessed_character_id, is_spectator, is_inactive,
				is_quitter, outcome, is_match_mvp, raw_player
			) VALUES (
				$1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8,
				$9, NULLIF($10, ''), $11, $12, $13, $14, $15, $16, $17,
				$18, $19, $20, $21, $22, $23::jsonb
			)
		`, participantID, matchID, participant.SlotIndex,
			participant.PlayerID, participant.PlatformID,
			participant.PlayerName, participant.IsAI,
			participant.RosterVerified, participant.OfficialEligible,
			participant.AuthLevelAtMatch, participant.SteamVerified,
			participant.TeamID, participant.CampID, participant.RoleName,
			participant.RoleValue, participant.SelectedCharacter,
			participant.PossessedCharacter, participant.IsSpectator,
			participant.IsInactive, participant.IsQuitter,
			participant.Outcome, participant.IsMatchMVP,
			participant.RawPlayer,
		)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO battlelog_participant_stats (
				participant_id, kills, deaths, assists, score, team_score,
				headshot_count, bullets_fired, rockets_fired,
				max_kill_distance, avg_kill_distance, max_kill_streak,
				killing_streak_count, ping_ms, reported_kda,
				calculated_kda, reported_spm, calculated_spm,
				reported_accuracy, playing_time_ms
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				$13, $14, $15, $16, $17, $18, $19, $20
			)
		`, participantID, participant.Kills, participant.Deaths,
			participant.Assists, participant.Score, participant.TeamScore,
			participant.Headshots, participant.BulletsFired,
			participant.RocketsFired, participant.MaxKillDistance,
			participant.AvgKillDistance, participant.MaxKillStreak,
			participant.KillingStreakCount, participant.PingMS,
			participant.ReportedKDA, participant.CalculatedKDA,
			participant.ReportedSPM, participant.CalculatedSPM,
			participant.ReportedAccuracy, participant.PlayingTimeMS,
		)
		if err != nil {
			return nil, err
		}
		if err := insertBattleLogScoreBreakdowns(
			ctx, tx, participantID, "CHARACTER", participant.CharacterScores,
		); err != nil {
			return nil, err
		}
		if err := insertBattleLogScoreBreakdowns(
			ctx, tx, participantID, "ROLE", participant.RoleScores,
		); err != nil {
			return nil, err
		}
		if participant.PlayerID != "" {
			participantIDs[participant.PlayerID] = participantID
		}
	}
	return participantIDs, nil
}

func insertBattleLogScoreBreakdowns(
	ctx context.Context,
	tx pgx.Tx,
	participantID, category string,
	entries []battleLogScoreEntry,
) error {
	for _, entry := range entries {
		_, err := tx.Exec(ctx, `
			INSERT INTO battlelog_score_breakdowns (
				participant_id, category, score_key, score
			) VALUES ($1, $2, $3, $4)
			ON CONFLICT (participant_id, category, score_key)
			DO UPDATE SET score = EXCLUDED.score
		`, participantID, category,
			truncateBattleLog(strings.TrimSpace(entry.Key), 128), entry.Value,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func insertBattleLogRounds(
	ctx context.Context,
	tx pgx.Tx,
	matchID string,
	rounds []battleLogRoundResult,
) error {
	for index, round := range rounds {
		scores, err := json.Marshal(round.TeamScores)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO battlelog_rounds (
				match_id, round_index, winner_team_id,
				is_final_round, team_scores
			) VALUES ($1, $2, $3, $4, $5::jsonb)
		`, matchID, index, round.WinnerTeamID,
			round.IsFinalRound, scores,
		); err != nil {
			return err
		}
	}
	return nil
}

func completeBattleLogMetaMatch(
	ctx context.Context,
	tx pgx.Tx,
	serverID, metaMatchID, battleLogID string,
	result normalizedBattleLog,
	participantIDs map[string]string,
	now time.Time,
) error {
	for _, participant := range result.Participants {
		if participant.PlayerID == "" || !participant.RosterVerified {
			continue
		}
		playerResult, err := json.Marshal(map[string]any{
			"battlelog_id":      battleLogID,
			"participant_id":    participantIDs[participant.PlayerID],
			"match_type":        result.MatchType,
			"validation_status": result.Status,
			"outcome":           participant.Outcome,
			"kills":             participant.Kills,
			"deaths":            participant.Deaths,
			"assists":           participant.Assists,
			"score":             participant.Score,
		})
		if err != nil {
			return internalError(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE meta_match_players
			SET result = $3::jsonb
			WHERE match_id = $1 AND player_id = $2
		`, metaMatchID, participant.PlayerID, playerResult); err != nil {
			return internalError(err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE meta_matches
		SET state = 'COMPLETED', completed_at = $3, updated_at = $3
		WHERE id = $1 AND game_server_id = $2
		  AND state IN ('RESERVED', 'RUNNING')
		  AND match_attempt_id IS NULL
	`, metaMatchID, serverID, now)
	if err != nil {
		return internalError(err)
	}
	if tag.RowsAffected() == 0 {
		return forbidden(
			"BATTLELOG_MATCH_FORBIDDEN",
			"The match is not assigned to this Game Server.",
		)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE game_servers
		SET state = 'READY', updated_at = $2
		WHERE id = $1 AND state IN ('RESERVED', 'RUNNING')
	`, serverID, now); err != nil {
		return internalError(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE meta_parties
		SET state = 'ACTIVE', revision = revision + 1, updated_at = $2
		WHERE id IN (
			SELECT ticket.party_id
			FROM meta_match_tickets AS ticket
			JOIN meta_matches AS match ON match.ticket_id = ticket.id
			WHERE match.id = $1 AND ticket.party_id IS NOT NULL
		)
	`, metaMatchID, now); err != nil {
		return internalError(err)
	}
	return nil
}
