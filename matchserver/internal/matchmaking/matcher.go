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

type P2PMatcher struct {
	db         *sql.DB
	cfg        *config.MatchServerConfig
	natStore   *store.NatTraversalStore
	relayStore *store.RelayStore
}

func NewP2PMatcher(db *sql.DB, cfg *config.MatchServerConfig, natStore *store.NatTraversalStore, relayStore *store.RelayStore) *P2PMatcher {
	return &P2PMatcher{db: db, cfg: cfg, natStore: natStore, relayStore: relayStore}
}

func (m *P2PMatcher) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.matchOnce(ctx); err != nil {
				slog.Error("p2p matchmaking tick failed", "error", err)
			}
		}
	}
}

func (m *P2PMatcher) matchOnce(ctx context.Context) error {
	now := time.Now()
	nowMs := now.UnixMilli()

	// 1. Fetch waiting P2P tickets (non-metaserver)
	rows, err := m.db.QueryContext(ctx,
		`SELECT ticket_id, player_id, region, map, mode, version, can_host, probe_id, max_players
		 FROM match_tickets WHERE state = ? AND expires_at > ? AND metaserver_mode = 0
		 ORDER BY created_at ASC`,
		models.MatchTicketWaiting, nowMs,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type waitingTicket struct {
		ticketID   string
		playerID   string
		region     string
		mapName    string
		mode       string
		version    string
		canHost    bool
		probeID    sql.NullString
		maxPlayers int
	}
	var tickets []waitingTicket
	for rows.Next() {
		var t waitingTicket
		if err := rows.Scan(&t.ticketID, &t.playerID, &t.region, &t.mapName, &t.mode, &t.version, &t.canHost, &t.probeID, &t.maxPlayers); err != nil {
			continue
		}
		tickets = append(tickets, t)
	}

	for _, ticket := range tickets {
		// Try to find an existing open room
		roomQuery := `SELECT room_id, host_player_id, max_players, player_count, endpoint, port
			FROM rooms WHERE (state = ? OR state = ?) AND region = ? AND version = ? AND host_player_id != ?`
		roomArgs := []any{models.RoomStateOpen, models.RoomStateStarting, ticket.region, ticket.version, ticket.playerID}

		if ticket.mapName != "" {
			roomQuery += " AND map = ?"
			roomArgs = append(roomArgs, ticket.mapName)
		}
		if ticket.mode != "" {
			roomQuery += " AND mode = ?"
			roomArgs = append(roomArgs, ticket.mode)
		}
		roomQuery += " ORDER BY player_count DESC, last_seen_at DESC LIMIT 1"

		var roomID, hostPlayerID string
		var maxPlayers, playerCount int
		var endpoint string
		var port int
		err := m.db.QueryRowContext(ctx, roomQuery, roomArgs...).Scan(&roomID, &hostPlayerID, &maxPlayers, &playerCount, &endpoint, &port)

		if err == nil && playerCount < maxPlayers {
			// Match to existing room
			joinTicket := store.NewToken(32)

			_, _ = m.db.ExecContext(ctx,
				`UPDATE match_tickets SET state = ?, assigned_room_id = ?, join_ticket_plain = ?, failure_reason = NULL
				 WHERE ticket_id = ?`,
				models.MatchTicketMatched, roomID, joinTicket, ticket.ticketID,
			)
			continue
		}

		// For remaining canHost tickets, create a new room
		if ticket.canHost && ticket.probeID.Valid {
			var probePublicIP string
			var probePort int
			var probeStatus string
			err := m.db.QueryRowContext(ctx,
				"SELECT public_ip, port, status FROM host_probes WHERE probe_id = ? AND player_id = ? AND expires_at > ?",
				ticket.probeID.String, ticket.playerID, nowMs,
			).Scan(&probePublicIP, &probePort, &probeStatus)

			if err == nil && probeStatus == string(models.HostProbeSucceeded) {
				hostToken := store.NewToken(32)
				hostTokenHash := store.SHA256Hash(hostToken)
				roomID := newUUID()

				_, _ = m.db.ExecContext(ctx,
					`INSERT INTO rooms (room_id, host_player_id, host_probe_id, host_token_hash, name, region, map, mode, version,
					 endpoint, port, max_players, player_count, state, created_at, last_seen_at)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
					roomID, ticket.playerID, ticket.probeID.String, hostTokenHash,
					"Quick Match", ticket.region, ticket.mapName, ticket.mode, ticket.version,
					probePublicIP, probePort, ticket.maxPlayers, models.RoomStateStarting, nowMs, nowMs,
				)

				m.relayStore.CreateHostAllocation(roomID, ticket.playerID, time.Duration(m.cfg.RelayAllocationSeconds)*time.Second)

				_, _ = m.db.ExecContext(ctx,
					`UPDATE match_tickets SET state = ?, assigned_room_id = ?, host_token_plain = ?, failure_reason = NULL
					 WHERE ticket_id = ?`,
					models.MatchTicketHostAssigned, roomID, hostToken, ticket.ticketID,
				)
				break // Only create one room per tick
			}
		}
	}

	return nil
}

func newUUID() string {
	return store.NewToken(16)
}
