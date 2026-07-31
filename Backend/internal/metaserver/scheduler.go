package metaserver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schedulerAdvisoryKey int64 = 0x50524d455441

type Scheduler struct {
	pool            *pgxpool.Pool
	interval        time.Duration
	serverFreshness time.Duration
	reservationTTL  time.Duration
	metrics         *MetaMetrics
	logger          *slog.Logger
	now             func() time.Time
}

func NewScheduler(
	pool *pgxpool.Pool,
	interval, serverFreshness, reservationTTL time.Duration,
	metrics *MetaMetrics,
	logger *slog.Logger,
) *Scheduler {
	return &Scheduler{
		pool: pool, interval: interval, serverFreshness: serverFreshness,
		reservationTTL: reservationTTL,
		metrics:        metrics, logger: logger, now: time.Now,
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.logger.ErrorContext(ctx, "MetaServer scheduler tick failed", "error", err)
			}
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) error {
	if err := s.expireAndRelease(ctx); err != nil {
		return err
	}
	for range 32 {
		scheduled, err := s.scheduleOne(ctx)
		if err != nil {
			return err
		}
		if !scheduled {
			return nil
		}
	}
	return nil
}

func (s *Scheduler) scheduleOne(ctx context.Context) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var leader bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, schedulerAdvisoryKey).Scan(&leader); err != nil {
		return false, err
	}
	if !leader {
		return false, nil
	}

	var ticket struct {
		id, playerID, partyID, mode, region, version string
		protocolVersion                              int
		createdAt                                    time.Time
	}
	err = tx.QueryRow(ctx, `
		SELECT id, player_id, COALESCE(party_id, ''), mode, region,
		       client_version, protocol_version, created_at
		FROM meta_match_tickets
		WHERE state = 'QUEUED' AND expires_at > $1
		ORDER BY created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, s.now().UTC()).Scan(
		&ticket.id, &ticket.playerID, &ticket.partyID, &ticket.mode,
		&ticket.region, &ticket.version, &ticket.protocolVersion,
		&ticket.createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	memberCount := 1
	if ticket.partyID != "" {
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM meta_party_members
			WHERE party_id = $1 AND left_at IS NULL
		`, ticket.partyID).Scan(&memberCount); err != nil {
			return false, err
		}
	}

	var server struct {
		id, host string
		port     int
	}
	err = tx.QueryRow(ctx, `
		SELECT id, public_host, public_port
		FROM game_servers
		WHERE state = 'READY'
		  AND mode = $1
		  AND version = $2
		  AND ($3 = 'auto' OR region = $3)
		  AND max_players - player_count >= $4
		  AND last_heartbeat_at > $5
		  AND token_revoked_at IS NULL
		  AND token_expires_at > $6
		ORDER BY CASE WHEN region = $3 THEN 0 ELSE 1 END,
		         player_count::float / max_players, last_heartbeat_at DESC, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, ticket.mode, ticket.version, ticket.region, memberCount,
		s.now().UTC().Add(-s.serverFreshness), s.now().UTC()).Scan(
		&server.id, &server.host, &server.port,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	now := s.now().UTC()
	matchID := newMetaID("mm_")
	if _, err := tx.Exec(ctx, `
		UPDATE game_servers
		SET state = 'RESERVED', updated_at = $2
		WHERE id = $1 AND state = 'READY'
	`, server.id, now); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO meta_matches (
			id, game_server_id, ticket_id, mode, region, client_version,
			protocol_version, state, endpoint_host, endpoint_port,
			reserved_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'RESERVED', $8, $9, $10, $10)
	`, matchID, server.id, ticket.id, ticket.mode, ticket.region,
		ticket.version, ticket.protocolVersion, server.host, server.port, now,
	); err != nil {
		return false, err
	}
	if ticket.partyID == "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO meta_match_players (
				match_id, player_id, auth_level_at_reservation,
				steam_verified_at_reservation
			)
			SELECT $1, player.id, player.auth_level,
			       player.auth_level IN ('verified', 'trusted')
			FROM players AS player
			WHERE player.id = $2
		`, matchID, ticket.playerID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO meta_match_players (
				match_id, player_id, auth_level_at_reservation,
				steam_verified_at_reservation
			)
			SELECT $1, member.player_id, player.auth_level,
			       player.auth_level IN ('verified', 'trusted')
			FROM meta_party_members AS member
			JOIN players AS player ON player.id = member.player_id
			WHERE member.party_id = $2 AND member.left_at IS NULL
		`, matchID, ticket.partyID)
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE meta_match_tickets
		SET state = 'MATCHED', matched_id = $2, completed_at = $3, updated_at = $3
		WHERE id = $1 AND state = 'QUEUED'
	`, ticket.id, matchID, now); err != nil {
		return false, err
	}
	if ticket.partyID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE meta_parties
			SET state = 'IN_MATCH', revision = revision + 1, updated_at = $2
			WHERE id = $1 AND state IN ('ACTIVE', 'MATCHMAKING')
		`, ticket.partyID, now); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if s.metrics != nil {
		s.metrics.MatchOutcome("matched", 1, now.Sub(ticket.createdAt))
	}
	s.logger.InfoContext(ctx, "MetaServer match reserved",
		"match_id", matchID, "ticket_id", ticket.id, "game_server_id", server.id,
	)
	return true, nil
}

func (s *Scheduler) expireAndRelease(ctx context.Context) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	var timedOut int64
	if err := tx.QueryRow(ctx, `
		WITH expired AS (
			UPDATE meta_match_tickets
			SET state = 'TIMED_OUT', failure_code = 'META_MATCH_TIMEOUT',
			    completed_at = $1, updated_at = $1
			WHERE state = 'QUEUED' AND expires_at <= $1
			RETURNING party_id
		), reset_parties AS (
			UPDATE meta_parties
			SET state = 'ACTIVE', revision = revision + 1, updated_at = $1
			WHERE id IN (
				SELECT party_id FROM expired WHERE party_id IS NOT NULL
			) AND state = 'MATCHMAKING'
			RETURNING id
		)
		SELECT COUNT(*) FROM expired
	`, now).Scan(&timedOut); err != nil {
		return err
	}

	type released struct{ serverID, ticketID string }
	reservationRows, err := tx.Query(ctx, `
		UPDATE meta_matches
		SET state = 'FAILED', completed_at = $1, updated_at = $1
		WHERE state = 'RESERVED' AND reserved_at <= $2
		RETURNING game_server_id, ticket_id
	`, now, now.Add(-s.reservationTTL))
	if err != nil {
		return err
	}
	var reservationTimeouts []released
	for reservationRows.Next() {
		var item released
		if err := reservationRows.Scan(&item.serverID, &item.ticketID); err != nil {
			reservationRows.Close()
			return err
		}
		reservationTimeouts = append(reservationTimeouts, item)
	}
	if err := reservationRows.Err(); err != nil {
		reservationRows.Close()
		return err
	}
	reservationRows.Close()
	for _, item := range reservationTimeouts {
		var partyID string
		if err := tx.QueryRow(ctx, `
			UPDATE meta_match_tickets
			SET state = 'FAILED', failure_code = 'META_MATCH_CONNECTION_TIMEOUT',
			    completed_at = $2, updated_at = $2
			WHERE id = $1 AND state = 'MATCHED'
			RETURNING COALESCE(party_id, '')
		`, item.ticketID, now).Scan(&partyID); err != nil {
			return err
		}
		if partyID != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE meta_parties
				SET state = 'ACTIVE', revision = revision + 1, updated_at = $2
				WHERE id = $1 AND state = 'IN_MATCH'
			`, partyID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE game_servers
			SET state = CASE
			      WHEN last_heartbeat_at > $3
			       AND token_revoked_at IS NULL AND token_expires_at > $2
			      THEN 'READY' ELSE 'UNHEALTHY'
			    END,
			    updated_at = $2
			WHERE id = $1 AND state = 'RESERVED'
		`, item.serverID, now, now.Add(-s.serverFreshness)); err != nil {
			return err
		}
	}

	rows, err := tx.Query(ctx, `
		UPDATE meta_matches AS match
		SET state = 'FAILED', completed_at = $1, updated_at = $1
		FROM game_servers AS server
		WHERE match.game_server_id = server.id
		  AND match.state IN ('RESERVED', 'RUNNING')
		  AND server.state IN ('DRAINING', 'UNHEALTHY', 'OFFLINE')
		RETURNING match.game_server_id, match.ticket_id
	`, now)
	if err != nil {
		return err
	}
	var items []released
	for rows.Next() {
		var item released
		if err := rows.Scan(&item.serverID, &item.ticketID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		var partyID string
		if err := tx.QueryRow(ctx, `
			UPDATE meta_match_tickets
			SET state = 'FAILED', failure_code = 'META_GAME_SERVER_UNAVAILABLE',
			    completed_at = $2, updated_at = $2
			WHERE id = $1
			RETURNING COALESCE(party_id, '')
		`, item.ticketID, now).Scan(&partyID); err != nil {
			return err
		}
		if partyID != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE meta_parties
				SET state = 'ACTIVE', revision = revision + 1, updated_at = $2
				WHERE id = $1 AND state = 'IN_MATCH'
			`, partyID, now); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if s.metrics != nil {
		if timedOut > 0 {
			s.metrics.MatchOutcome("timed_out", uint64(timedOut), 0)
		}
		if len(items) > 0 {
			s.metrics.MatchOutcome("server_unavailable", uint64(len(items)), 0)
		}
		if len(reservationTimeouts) > 0 {
			s.metrics.MatchOutcome("connection_timeout", uint64(len(reservationTimeouts)), 0)
		}
	}
	return nil
}
