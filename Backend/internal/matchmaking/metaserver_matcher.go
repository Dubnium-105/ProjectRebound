package matchmaking

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

type MetaServerMatcher struct {
	db         *sql.DB
	cfg        *config.MatchServerConfig
	natStore   *store.NatTraversalStore
	relayStore *store.RelayStore
}

func NewMetaServerMatcher(db *sql.DB, cfg *config.MatchServerConfig, natStore *store.NatTraversalStore, relayStore *store.RelayStore) *MetaServerMatcher {
	return &MetaServerMatcher{db: db, cfg: cfg, natStore: natStore, relayStore: relayStore}
}

func (m *MetaServerMatcher) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.matchOnce(ctx); err != nil {
				slog.Error("metaserver matchmaking tick failed", "error", err)
			}
		}
	}
}

func (m *MetaServerMatcher) matchOnce(ctx context.Context) error {
	now := time.Now()
	nowMs := now.UnixMilli()

	// Fetch all waiting metaserver-mode tickets
	rows, err := m.db.QueryContext(ctx,
		`SELECT ticket_id, player_id, region, mode
		 FROM match_tickets WHERE state = ? AND metaserver_mode = 1 AND expires_at > ?
		 ORDER BY created_at ASC`,
		models.MatchTicketWaiting, nowMs,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type msTicket struct {
		ticketID string
		playerID string
		region   string
		mode     string
	}
	var allTickets []msTicket
	for rows.Next() {
		var t msTicket
		if err := rows.Scan(&t.ticketID, &t.playerID, &t.region, &t.mode); err != nil {
			continue
		}
		allTickets = append(allTickets, t)
	}

	// Group by (region, mode)
	groups := make(map[string][]msTicket)
	for _, t := range allTickets {
		key := t.region + "/" + t.mode
		groups[key] = append(groups[key], t)
	}

	for _, group := range groups {
		if len(group) < m.cfg.MetaserverTargetPlayerCount {
			continue
		}

		// Select first player as host
		host := group[0]

		// Create room for host
		hostToken := store.NewToken(32)
		hostTokenHash := store.SHA256Hash(hostToken)
		roomID := newUUID()

		_, err := m.db.ExecContext(ctx,
			`INSERT INTO rooms (room_id, host_player_id, host_token_hash, name, region, map, mode, version,
			 endpoint, port, max_players, player_count, state, created_at, last_seen_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			roomID, host.playerID, hostTokenHash,
			"MetaServer Match", host.region, "Warehouse", host.mode, "dev",
			"pending", 0, m.cfg.MetaserverTargetPlayerCount,
			models.RoomStateStarting, nowMs, nowMs,
		)
		if err != nil {
			continue
		}

		relayTTL := time.Duration(m.cfg.RelayAllocationSeconds) * time.Second
		m.relayStore.CreateHostAllocation(roomID, host.playerID, relayTTL)
		for _, ticket := range group {
			if ticket.playerID != host.playerID {
				m.relayStore.CreateClientAllocation(roomID, host.playerID, ticket.playerID, relayTTL)
			}
		}

		// Update all tickets in group
		for _, ticket := range group {
			if ticket.playerID == host.playerID {
				_, _ = m.db.ExecContext(ctx,
					`UPDATE match_tickets SET state = ?, assigned_room_id = ?, host_token_plain = ?, failure_reason = NULL
					 WHERE ticket_id = ?`,
					models.MatchTicketHostAssigned, roomID, hostToken, ticket.ticketID,
				)
			} else {
				joinTicket := store.NewToken(32)
				_, _ = m.db.ExecContext(ctx,
					`UPDATE match_tickets SET state = ?, assigned_room_id = ?, join_ticket_plain = ?, failure_reason = NULL
					 WHERE ticket_id = ?`,
					models.MatchTicketMatched, roomID, joinTicket, ticket.ticketID,
				)
			}
		}

		slog.Info("metaserver match formed",
			"region", host.region,
			"mode", host.mode,
			"playerCount", len(group),
			"roomId", roomID,
		)
	}

	// Expire old meta tickets
	timeout := time.Duration(m.cfg.MetaserverMatchTimeoutSeconds) * time.Second
	timeoutCutoff := now.Add(-timeout).UnixMilli()
	_, _ = m.db.ExecContext(ctx,
		"UPDATE match_tickets SET state = ? WHERE state = ? AND metaserver_mode = 1 AND expires_at <= ?",
		models.MatchTicketExpired, models.MatchTicketWaiting, timeoutCutoff,
	)

	return nil
}
