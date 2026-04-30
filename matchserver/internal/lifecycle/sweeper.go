package lifecycle

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

type Sweeper struct {
	db      *sql.DB
	cfg     *config.MatchServerConfig
	natStore *store.NatTraversalStore
	relayStore *store.RelayStore
}

func New(db *sql.DB, cfg *config.MatchServerConfig, natStore *store.NatTraversalStore, relayStore *store.RelayStore) *Sweeper {
	return &Sweeper{db: db, cfg: cfg, natStore: natStore, relayStore: relayStore}
}

func (s *Sweeper) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.sweep(ctx); err != nil {
				slog.Error("lifecycle sweep failed", "error", err)
			}
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) error {
	now := time.Now()
	nowMs := now.UnixMilli()

	// Expire pending host probes
	_, _ = s.db.ExecContext(ctx,
		"UPDATE host_probes SET status = ? WHERE status = ? AND expires_at <= ?",
		models.HostProbeExpired, models.HostProbePending, nowMs,
	)

	// Expire stale join reservations
	_, _ = s.db.ExecContext(ctx,
		"UPDATE room_players SET status = ? WHERE status = ? AND expires_at <= ?",
		models.RoomPlayerExpired, models.RoomPlayerReserved, nowMs,
	)

	// Expire waiting match tickets
	_, _ = s.db.ExecContext(ctx,
		"UPDATE match_tickets SET state = ?, failure_reason = ? WHERE state = ? AND expires_at <= ?",
		models.MatchTicketExpired, "ticket_expired", models.MatchTicketWaiting, nowMs,
	)

	// Detect host_lost rooms
	hostLostAfter := time.Duration(s.cfg.HostLostAfterSeconds) * time.Second
	lostCutoff := now.Add(-hostLostAfter).UnixMilli()

	rows, err := s.db.QueryContext(ctx,
		`SELECT room_id FROM rooms
		 WHERE (state = ? OR state = ? OR state = ?) AND last_seen_at <= ?`,
		models.RoomStateOpen, models.RoomStateStarting, models.RoomStateInGame, lostCutoff,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var lostRoomIDs []string
	for rows.Next() {
		var roomID string
		if err := rows.Scan(&roomID); err != nil {
			continue
		}
		lostRoomIDs = append(lostRoomIDs, roomID)
	}

	for _, roomID := range lostRoomIDs {
		_, _ = s.db.ExecContext(ctx,
			"UPDATE rooms SET state = ?, ended_reason = ?, last_seen_at = ? WHERE room_id = ?",
			models.RoomStateEnded, "host_lost", nowMs, roomID,
		)
		_, _ = s.db.ExecContext(ctx,
			`UPDATE match_tickets SET state = ?, failure_reason = ?
			 WHERE assigned_room_id = ? AND state IN (?, ?, ?)`,
			models.MatchTicketFailed, "host_lost", roomID,
			models.MatchTicketWaiting, models.MatchTicketHostAssigned, models.MatchTicketMatched,
		)
	}

	// Purge old ended/expired rooms
	retention := time.Duration(s.cfg.EndedRoomRetentionMinutes) * time.Minute
	retentionCutoff := now.Add(-retention).UnixMilli()
	_, _ = s.db.ExecContext(ctx,
		"DELETE FROM rooms WHERE (state = ? OR state = ?) AND last_seen_at <= ?",
		models.RoomStateEnded, models.RoomStateExpired, retentionCutoff,
	)

	// Cleanup in-memory stores
	s.natStore.CleanupExpired()
	s.relayStore.CleanupExpired()

	return nil
}
