package matchlobby

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

type Sweeper struct {
	service  *Service
	interval time.Duration
	logger   *slog.Logger
}

func NewSweeper(service *Service, interval time.Duration, logger *slog.Logger) *Sweeper {
	return &Sweeper{service: service, interval: interval, logger: logger}
}

func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.service.Sweep(ctx); err != nil {
				s.logger.ErrorContext(ctx, "match lobby sweep failed", "error", err)
			}
		}
	}
}

func (s *Service) Sweep(ctx context.Context) error {
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, err = tx.Exec(ctx, `
		WITH expired_owner AS (
			SELECT lobby_id FROM match_lobby_members
			WHERE role = 'OWNER' AND membership_state = 'ACTIVE'
			  AND presence_expires_at <= $1
		), closed AS (
			UPDATE match_lobbies SET state = 'ABORTED', closed_at = $1, updated_at = $1
			WHERE id IN (SELECT lobby_id FROM expired_owner) AND state = 'OPEN'
			RETURNING id
		)
		UPDATE match_lobby_members SET membership_state = 'LEFT', presence_state = 'OFFLINE',
		       ready = FALSE, left_at = $1
		WHERE lobby_id IN (SELECT id FROM closed) AND membership_state = 'ACTIVE'
	`, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		WITH expired_member AS (
			UPDATE match_lobby_members AS member
			SET membership_state = 'LEFT', presence_state = 'OFFLINE', ready = FALSE, left_at = $1
			FROM match_lobbies AS lobby
			WHERE member.lobby_id = lobby.id AND lobby.state = 'OPEN'
			  AND member.role = 'MEMBER' AND member.membership_state = 'ACTIVE'
			  AND member.presence_expires_at <= $1
			RETURNING member.lobby_id
		), changed AS (
			UPDATE match_lobbies SET roster_revision = roster_revision + 1, updated_at = $1
			WHERE id IN (SELECT DISTINCT lobby_id FROM expired_member)
			RETURNING id
		)
		UPDATE match_lobby_members SET ready = FALSE
		WHERE lobby_id IN (SELECT id FROM changed) AND membership_state = 'ACTIVE'
	`, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE match_lobby_members SET presence_state = 'OFFLINE'
		WHERE membership_state = 'ACTIVE' AND presence_expires_at <= $1
	`, now)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_room_members AS transport_member
		SET status = 'LEFT', left_at = COALESCE(transport_member.left_at, $1)
		FROM p2p_rooms AS room, match_lobby_members AS lobby_member
		WHERE room.managed_lobby_id = lobby_member.lobby_id
		  AND transport_member.room_id = room.id
		  AND transport_member.player_id = lobby_member.player_id
		  AND lobby_member.membership_state = 'LEFT'
		  AND transport_member.status = 'ACTIVE'
	`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_rooms AS room
		SET player_count = (
		    SELECT COUNT(*) FROM p2p_room_members AS member
		    WHERE member.room_id = room.id AND member.status = 'ACTIVE'
		), updated_at = $1,
		state = CASE
		    WHEN EXISTS (
		        SELECT 1 FROM match_lobbies AS lobby
		        WHERE lobby.id = room.managed_lobby_id AND lobby.state = 'ABORTED'
		    ) THEN 'CLOSED'
		    ELSE room.state
		END,
		closed_at = CASE
		    WHEN EXISTS (
		        SELECT 1 FROM match_lobbies AS lobby
		        WHERE lobby.id = room.managed_lobby_id AND lobby.state = 'ABORTED'
		    ) THEN COALESCE(room.closed_at, $1)
		    ELSE room.closed_at
		END
		WHERE room.managed_lobby_id IS NOT NULL
	`, now); err != nil {
		return err
	}

	// Strict Dedicated projections are deliberately excluded from the legacy
	// Meta scheduler. Keep the attempt, lobby, projection, and server lease in
	// one transaction when provisioning stalls or the authority heartbeat is
	// lost. Before RUNNING the frozen roster returns to OPEN; a running world
	// cannot be reconstructed and therefore ends terminally.
	staleBefore := now.Add(-authorityHeartbeatStale)
	dedicatedRows, err := tx.Query(ctx, `
		SELECT id, lobby_id, COALESCE(authority_id, ''), state
		FROM match_attempts
		WHERE hosting_kind = 'DEDICATED'
		  AND (
		      (state = 'PROVISIONING' AND created_at <= $1)
		      OR (state IN ('CONNECTING', 'RUNNING')
		          AND (authority_last_seen_at IS NULL OR authority_last_seen_at <= $2))
		  )
		FOR UPDATE
	`, now.Add(-s.provisioningTimeout()), staleBefore)
	if err != nil {
		return err
	}
	type failedDedicated struct {
		attemptID, lobbyID, authorityID string
		state                           AttemptState
	}
	var failedDedicatedAttempts []failedDedicated
	for dedicatedRows.Next() {
		var item failedDedicated
		if err := dedicatedRows.Scan(&item.attemptID, &item.lobbyID, &item.authorityID, &item.state); err != nil {
			dedicatedRows.Close()
			return err
		}
		failedDedicatedAttempts = append(failedDedicatedAttempts, item)
	}
	if err := dedicatedRows.Err(); err != nil {
		dedicatedRows.Close()
		return err
	}
	dedicatedRows.Close()
	for _, item := range failedDedicatedAttempts {
		failureCode := "DEDICATED_AUTHORITY_HEARTBEAT_TIMEOUT"
		if item.state == AttemptProvisioning {
			failureCode = "DEDICATED_PROVISIONING_TIMEOUT"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempts
			SET state = 'ABORTED', failure_code = $2, completed_at = $3, updated_at = $3
			WHERE id = $1
		`, item.attemptID, failureCode, now); err != nil {
			return err
		}
		if item.state == AttemptRunning {
			if _, err := tx.Exec(ctx, `
				UPDATE match_lobbies SET state = 'ABORTED', closed_at = $2, updated_at = $2
				WHERE id = $1
			`, item.lobbyID, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE match_lobby_members SET ready = FALSE, presence_state = 'OFFLINE'
				WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
			`, item.lobbyID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE match_lobbies
				SET state = 'OPEN', current_attempt_id = NULL,
				    roster_revision = roster_revision + 1, updated_at = $2
				WHERE id = $1
			`, item.lobbyID, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE match_lobby_members SET ready = FALSE
				WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
			`, item.lobbyID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_admission_grants SET revoked_at = $2
			WHERE attempt_id = $1 AND revoked_at IS NULL
		`, item.attemptID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE meta_matches SET state = 'FAILED', completed_at = $2, updated_at = $2
			WHERE match_attempt_id = $1
		`, item.attemptID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE meta_match_tickets AS ticket
			SET state = 'FAILED', failure_code = $2, completed_at = $3, updated_at = $3
			FROM meta_matches AS match
			WHERE match.match_attempt_id = $1 AND ticket.id = match.ticket_id
			  AND ticket.state = 'MATCHED'
		`, item.attemptID, failureCode, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE game_servers
			SET state = CASE
			      WHEN last_heartbeat_at > $3
			       AND token_revoked_at IS NULL AND token_expires_at > $2
			      THEN 'READY' ELSE 'UNHEALTHY'
			    END,
			    player_count = 0, updated_at = $2
			WHERE id = $1 AND state IN ('RESERVED', 'RUNNING')
		`, item.authorityID, now, now.Add(-s.serverFreshness)); err != nil {
			return err
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT id, lobby_id, hosting_kind, COALESCE(authority_id, ''),
		       COALESCE(meta_match_id, '')
		FROM match_attempts
		WHERE state = 'CONNECTING' AND connection_deadline <= $1
		FOR UPDATE
	`, now)
	if err != nil {
		return err
	}
	type expiredAttempt struct{ id, lobbyID, hosting, authorityID, metaMatchID string }
	var expired []expiredAttempt
	for rows.Next() {
		var item expiredAttempt
		if err := rows.Scan(&item.id, &item.lobbyID, &item.hosting, &item.authorityID, &item.metaMatchID); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, attempt := range expired {
		var teamOne, teamTwo int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FILTER (WHERE team_id = 1 AND connection_state = 'CONNECTED'),
			       COUNT(*) FILTER (WHERE team_id = 2 AND connection_state = 'CONNECTED')
			FROM match_attempt_roster WHERE attempt_id = $1
		`, attempt.id).Scan(&teamOne, &teamTwo); err != nil {
			return err
		}
		if teamOne > 0 && teamTwo > 0 {
			if err := s.markAttemptRunning(ctx, tx, attempt.id, attempt.lobbyID, attempt.authorityID, now); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE match_attempts SET state = 'ABORTED', failure_code = 'INITIAL_TEAM_EMPTY', completed_at = $2, updated_at = $2 WHERE id = $1`, attempt.id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_lobbies SET state = 'OPEN', current_attempt_id = NULL,
			       roster_revision = roster_revision + 1, updated_at = $2 WHERE id = $1
		`, attempt.lobbyID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_lobby_members SET ready = FALSE
			WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
		`, attempt.lobbyID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE match_admission_grants SET revoked_at = $2 WHERE attempt_id = $1 AND revoked_at IS NULL`, attempt.id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE meta_matches SET state = 'FAILED', completed_at = $2, updated_at = $2 WHERE match_attempt_id = $1`, attempt.id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE meta_match_tickets AS ticket
			SET state = 'FAILED', failure_code = 'INITIAL_TEAM_EMPTY',
			    completed_at = $2, updated_at = $2
			FROM meta_matches AS match
			WHERE match.match_attempt_id = $1 AND ticket.id = match.ticket_id
			  AND ticket.state = 'MATCHED'
		`, attempt.id, now); err != nil {
			return err
		}
		if attempt.hosting == string(HostingDedicated) {
			if _, err := tx.Exec(ctx, `UPDATE game_servers SET state = 'READY', player_count = 0, updated_at = $2 WHERE id = $1`, attempt.authorityID, now); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE p2p_rooms SET state = 'LOBBY', updated_at = $2 WHERE managed_lobby_id = $1`, attempt.lobbyID, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE p2p_match_sessions SET state = 'ABORTED', finalized_at = $2, updated_at = $2
				WHERE match_attempt_id = $1 AND state IN ('STARTING', 'RUNNING', 'COLLECTING')
			`, attempt.id, now); err != nil {
				return err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE match_attempts
		SET host_reconnect_deadline = authority_last_seen_at + ($2 * interval '1 second'), updated_at = $1
		WHERE hosting_kind = 'P2P' AND state IN ('CONNECTING', 'RUNNING')
		  AND authority_last_seen_at IS NOT NULL AND authority_last_seen_at <= $3
		  AND host_reconnect_deadline IS NULL
	`, now, s.config.P2PHostReconnectSeconds, staleBefore); err != nil {
		return err
	}
	hostRows, err := tx.Query(ctx, `
		SELECT id, lobby_id FROM match_attempts
		WHERE hosting_kind = 'P2P' AND state IN ('PROVISIONING', 'CONNECTING', 'RUNNING')
		  AND host_reconnect_deadline <= $1
		FOR UPDATE
	`, now)
	if err != nil {
		return err
	}
	var expiredHosts []struct{ attemptID, lobbyID string }
	for hostRows.Next() {
		var item struct{ attemptID, lobbyID string }
		if err := hostRows.Scan(&item.attemptID, &item.lobbyID); err != nil {
			hostRows.Close()
			return err
		}
		expiredHosts = append(expiredHosts, item)
	}
	if err := hostRows.Err(); err != nil {
		hostRows.Close()
		return err
	}
	hostRows.Close()
	for _, item := range expiredHosts {
		if _, err := tx.Exec(ctx, `UPDATE match_attempts SET state = 'ABORTED', failure_code = 'P2P_HOST_RECONNECT_TIMEOUT', completed_at = $2, updated_at = $2 WHERE id = $1`, item.attemptID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET state = 'ABORTED', closed_at = $2, updated_at = $2 WHERE id = $1`, item.lobbyID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE match_admission_grants SET revoked_at = $2 WHERE attempt_id = $1 AND revoked_at IS NULL`, item.attemptID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE p2p_rooms SET state = 'CLOSED', closed_at = $2, updated_at = $2 WHERE managed_lobby_id = $1`, item.lobbyID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE p2p_match_sessions SET state = 'ABORTED', finalized_at = $2, updated_at = $2 WHERE match_attempt_id = $1 AND state IN ('STARTING', 'RUNNING', 'COLLECTING')`, item.attemptID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE match_lobby_members SET presence_state = 'OFFLINE', ready = FALSE WHERE lobby_id = $1 AND membership_state = 'ACTIVE'`, item.lobbyID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
