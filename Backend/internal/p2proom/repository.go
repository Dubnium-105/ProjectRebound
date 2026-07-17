package p2proom

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, tx pgx.Tx, room Room) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode,
			version, max_players, player_count, state, last_heartbeat_at,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 'LOBBY', $9, $9, $9)
	`, room.ID, room.HostPlayerID, room.HostTokenHash, room.DisplayName,
		room.Region, room.Mode, room.Version, room.MaxPlayers, room.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert P2P room: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
		VALUES ($1, $2, 'HOST', 'ACTIVE', $3)
	`, room.ID, room.HostPlayerID, room.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert P2P host member: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, roomID string) (Room, error) {
	return scanRoom(r.pool.QueryRow(ctx, `SELECT `+roomColumns+` FROM p2p_rooms WHERE id = $1`, roomID))
}

func (r *Repository) GetForUpdate(ctx context.Context, tx pgx.Tx, roomID string) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM p2p_rooms WHERE id = $1 FOR UPDATE`, roomID))
}

func (r *Repository) List(ctx context.Context, filter ListFilter, hasSlots int) ([]Room, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+roomColumns+`
		FROM p2p_rooms
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR region = $2)
		  AND ($3 = '' OR mode = $3)
		  AND ($4 = '' OR version = $4)
		  AND ($5 = '' OR state = $5)
		  AND ($6 = -1 OR ($6 = 1 AND player_count < max_players) OR ($6 = 0 AND player_count >= max_players))
		ORDER BY id
		LIMIT $7
	`, filter.Cursor, filter.Region, filter.Mode, filter.Version, filter.State, hasSlots, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list P2P rooms: %w", err)
	}
	defer rows.Close()
	items := make([]Room, 0, filter.Limit)
	for rows.Next() {
		item, err := scanRoom(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed P2P room: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate P2P rooms: %w", err)
	}
	return items, nil
}

func (r *Repository) GetMemberForUpdate(ctx context.Context, tx pgx.Tx, roomID, playerID string) (Member, error) {
	return scanMember(tx.QueryRow(ctx, `
		SELECT room_id, player_id, role, status, joined_at, left_at
		FROM p2p_room_members
		WHERE room_id = $1 AND player_id = $2
		FOR UPDATE
	`, roomID, playerID))
}

func (r *Repository) GetMember(ctx context.Context, roomID, playerID string) (Member, error) {
	return scanMember(r.pool.QueryRow(ctx, `
		SELECT room_id, player_id, role, status, joined_at, left_at
		FROM p2p_room_members
		WHERE room_id = $1 AND player_id = $2
	`, roomID, playerID))
}

func (r *Repository) ActivateMember(ctx context.Context, tx pgx.Tx, roomID, playerID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
		VALUES ($1, $2, 'MEMBER', 'ACTIVE', $3)
		ON CONFLICT (room_id, player_id) DO UPDATE SET
			status = 'ACTIVE', joined_at = $3, left_at = NULL
	`, roomID, playerID, now)
	if err != nil {
		return fmt.Errorf("activate P2P room member: %w", err)
	}
	return nil
}

func (r *Repository) MarkMemberLeft(ctx context.Context, tx pgx.Tx, roomID, playerID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE p2p_room_members
		SET status = 'LEFT', left_at = $3
		WHERE room_id = $1 AND player_id = $2 AND status = 'ACTIVE'
	`, roomID, playerID, now)
	if err != nil {
		return fmt.Errorf("leave P2P room: %w", err)
	}
	return nil
}

func (r *Repository) UpdatePlayerCount(ctx context.Context, tx pgx.Tx, roomID string, delta int, now time.Time) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET player_count = player_count + $2, updated_at = $3
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, delta, now,
	))
}

func (r *Repository) Heartbeat(ctx context.Context, tx pgx.Tx, roomID string, now time.Time) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = CASE WHEN state = 'STALE' THEN 'LOBBY' ELSE state END,
		    last_heartbeat_at = $2, updated_at = $2
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, now,
	))
}

func (r *Repository) Start(ctx context.Context, tx pgx.Tx, roomID string, now time.Time) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = 'CONNECTING', updated_at = $2
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, now,
	))
}

func (r *Repository) MarkRunning(ctx context.Context, roomID string, now time.Time) (Room, error) {
	return scanRoom(r.pool.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = 'RUNNING', updated_at = $2
		WHERE id = $1 AND state IN ('LOBBY', 'CONNECTING')
		RETURNING `+roomColumns,
		roomID, now,
	))
}

func (r *Repository) Close(ctx context.Context, tx pgx.Tx, roomID string, now time.Time) (Room, error) {
	item, err := scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = 'CLOSED', closed_at = COALESCE(closed_at, $2), updated_at = $2
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, now,
	))
	if err != nil {
		return Room{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_room_members
		SET status = 'LEFT', left_at = COALESCE(left_at, $2)
		WHERE room_id = $1 AND status = 'ACTIVE'
	`, roomID, now); err != nil {
		return Room{}, fmt.Errorf("close P2P room members: %w", err)
	}
	return item, nil
}

func (r *Repository) SweepStale(ctx context.Context, now time.Time, staleAfter, closedAfter time.Duration) (int64, []string, error) {
	var updated int64
	var closedRoomIDs []string
	err := r.pool.QueryRow(ctx, `
		WITH changed AS (
			UPDATE p2p_rooms
			SET state = CASE
			        WHEN last_heartbeat_at <= $2 THEN 'CLOSED'
			        WHEN last_heartbeat_at <= $3 THEN 'STALE'
			        ELSE state
			    END,
			    closed_at = CASE WHEN last_heartbeat_at <= $2 THEN COALESCE(closed_at, $1) ELSE closed_at END,
			    updated_at = $1
			WHERE state <> 'CLOSED' AND last_heartbeat_at <= $3
			RETURNING id, state
		), closed_members AS (
			UPDATE p2p_room_members AS member
			SET status = 'LEFT', left_at = COALESCE(member.left_at, $1)
			FROM changed
			WHERE changed.id = member.room_id AND changed.state = 'CLOSED' AND member.status = 'ACTIVE'
			RETURNING member.room_id
		)
		SELECT COUNT(*), COALESCE(array_agg(id) FILTER (WHERE state = 'CLOSED'), ARRAY[]::varchar[])
		FROM changed
	`, now, now.Add(-closedAfter), now.Add(-staleAfter)).Scan(&updated, &closedRoomIDs)
	if err != nil {
		return 0, nil, fmt.Errorf("sweep P2P rooms: %w", err)
	}
	return updated, closedRoomIDs, nil
}

const roomColumns = `
	id, host_player_id, host_token_hash, display_name, region, mode, version,
	max_players, player_count, state, last_heartbeat_at, created_at, updated_at, closed_at
`

func scanRoom(row pgx.Row) (Room, error) {
	var item Room
	var closedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.HostPlayerID, &item.HostTokenHash, &item.DisplayName,
		&item.Region, &item.Mode, &item.Version, &item.MaxPlayers,
		&item.PlayerCount, &item.State, &item.LastHeartbeatAt,
		&item.CreatedAt, &item.UpdatedAt, &closedAt,
	)
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}

func scanMember(row pgx.Row) (Member, error) {
	var item Member
	var leftAt sql.NullTime
	err := row.Scan(&item.RoomID, &item.PlayerID, &item.Role, &item.Status, &item.JoinedAt, &leftAt)
	if leftAt.Valid {
		item.LeftAt = &leftAt.Time
	}
	return item, err
}
