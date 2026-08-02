package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/connection"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RoomConnectionCloser interface {
	CloseForRoom(context.Context, string, string) error
	CloseForRoomMember(context.Context, string, string, string) error
	FinalizeAdministrativeClose(context.Context, connection.Connection, string) error
}

type RelayConnectionOperator interface {
	MigrateConnection(context.Context, string) (migrationID, previousNodeID, newNodeID string, err error)
}

type OnlineService struct {
	pool          *pgxpool.Pool
	audits        *Repository
	connections   RoomConnectionCloser
	registrations *gameserverregistration.Repository
	relays        RelayConnectionOperator
	logger        *slog.Logger
	now           func() time.Time
}

func NewOnlineService(
	pool *pgxpool.Pool,
	audits *Repository,
	connections RoomConnectionCloser,
	registrations *gameserverregistration.Repository,
	logger *slog.Logger,
) *OnlineService {
	return &OnlineService{
		pool: pool, audits: audits, connections: connections,
		registrations: registrations, logger: logger, now: time.Now,
	}
}

func (s *OnlineService) SetRelayConnectionOperator(operator RelayConnectionOperator) {
	s.relays = operator
}

type OnlineOperationResult[T any] struct {
	Resource                  T
	ConnectionCleanupComplete bool
}

type GameServerRegistrationInput struct {
	InstanceID     string
	ExpiresInHours int
	Reason         string
}

type GameServerRegistrationResult struct {
	Credential gameserverregistration.Credential
	Plaintext  string
}

type AdministrativeConnection struct {
	Connection       connection.Connection
	AllocationID     string
	RelayNodeID      string
	AllocationState  string
	MigrationHistory []AdministrativeRelayMigration
}

type AdministrativeRoomMember struct {
	RoomID        string
	PlayerID      string
	SteamID       string
	PersonaName   string
	AccountStatus string
	Role          string
	Status        string
	JoinedAt      time.Time
	LeftAt        *time.Time
}

type AdministrativeRelayMigration struct {
	ID             string
	PreviousNodeID string
	NewNodeID      string
	State          string
	Reason         string
	Attempt        int
	FailureReason  string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

type AdministrativeConnectionFilter struct {
	Cursor      string
	State       string
	RoomID      string
	PlayerID    string
	RelayNodeID string
	Limit       int
}

type AdministrativeConnectionList struct {
	Items      []AdministrativeConnection
	NextCursor string
}

type ConnectionMigrationResult struct {
	ConnectionID   string
	MigrationID    string
	PreviousNodeID string
	NewNodeID      string
}

func (s *OnlineService) ListRoomMembers(
	ctx context.Context,
	roomID string,
) ([]AdministrativeRoomMember, error) {
	if _, err := queryAdministrativeRoom(ctx, s.pool, strings.TrimSpace(roomID), false); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &ServiceError{Status: 404, Code: "ROOM_NOT_FOUND", Message: "P2P room not found."}
		}
		return nil, internal(err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT member.room_id, member.player_id, player.steam_id,
		       player.persona_name, player.account_status,
		       member.role, member.status, member.joined_at, member.left_at
		FROM p2p_room_members AS member
		JOIN players AS player ON player.id = member.player_id
		WHERE member.room_id = $1
		ORDER BY (member.status = 'ACTIVE') DESC, (member.role = 'HOST') DESC, member.joined_at
	`, strings.TrimSpace(roomID))
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	items := make([]AdministrativeRoomMember, 0)
	for rows.Next() {
		var item AdministrativeRoomMember
		var leftAt sql.NullTime
		if err := rows.Scan(
			&item.RoomID, &item.PlayerID, &item.SteamID, &item.PersonaName,
			&item.AccountStatus, &item.Role, &item.Status, &item.JoinedAt, &leftAt,
		); err != nil {
			return nil, internal(err)
		}
		if leftAt.Valid {
			item.LeftAt = &leftAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *OnlineService) ListConnections(
	ctx context.Context,
	filter AdministrativeConnectionFilter,
) (AdministrativeConnectionList, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return AdministrativeConnectionList{}, &ServiceError{
			Status: 400, Code: "INVALID_REQUEST", Message: "Invalid limit.",
			Details: map[string]any{"limit": "must be between 1 and 100"},
		}
	}
	if filter.State != "" && !validAdministrativeConnectionState(filter.State) {
		return AdministrativeConnectionList{}, &ServiceError{
			Status: 400, Code: "INVALID_REQUEST", Message: "Invalid connection state.",
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.room_id, c.host_player_id, c.peer_player_id, c.state,
		       COALESCE(c.selected_path, ''), COALESCE(c.failure_reason, ''),
		       c.expires_at, c.created_at, c.updated_at, c.closed_at,
		       COALESCE(allocation.id, ''), COALESCE(allocation.relay_node_id, ''),
		       COALESCE(allocation.state, '')
		FROM connections AS c
		LEFT JOIN LATERAL (
			SELECT id, relay_node_id, state
			FROM relay_allocations
			WHERE connection_id = c.id
			  AND state NOT IN ('CLOSED', 'FAILED')
			ORDER BY created_at DESC
			LIMIT 1
		) AS allocation ON TRUE
		WHERE ($1 = '' OR c.id > $1)
		  AND ($2 = '' OR c.state = $2)
		  AND ($3 = '' OR c.room_id = $3)
		  AND ($4 = '' OR c.host_player_id = $4 OR c.peer_player_id = $4)
		  AND ($5 = '' OR allocation.relay_node_id = $5)
		ORDER BY c.id
		LIMIT $6
	`, strings.TrimSpace(filter.Cursor), filter.State, strings.TrimSpace(filter.RoomID),
		strings.TrimSpace(filter.PlayerID), strings.TrimSpace(filter.RelayNodeID), filter.Limit+1)
	if err != nil {
		return AdministrativeConnectionList{}, internal(err)
	}
	defer rows.Close()
	items := make([]AdministrativeConnection, 0, filter.Limit+1)
	for rows.Next() {
		item, err := scanAdministrativeConnection(rows)
		if err != nil {
			return AdministrativeConnectionList{}, internal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AdministrativeConnectionList{}, internal(err)
	}
	nextCursor := ""
	if len(items) > filter.Limit {
		nextCursor = items[filter.Limit-1].Connection.ID
		items = items[:filter.Limit]
	}
	return AdministrativeConnectionList{Items: items, NextCursor: nextCursor}, nil
}

func (s *OnlineService) GetConnection(ctx context.Context, connectionID string) (AdministrativeConnection, error) {
	item, err := queryAdministrativeConnection(ctx, s.pool, strings.TrimSpace(connectionID), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdministrativeConnection{}, &ServiceError{
			Status: 404, Code: "CONNECTION_NOT_FOUND", Message: "Connection not found.",
		}
	}
	if err != nil {
		return AdministrativeConnection{}, internal(err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, old_relay_node_id, new_relay_node_id, state, reason,
		       attempt, COALESCE(failure_reason, ''), created_at, completed_at
		FROM relay_migrations
		WHERE connection_id = $1
		ORDER BY created_at DESC
		LIMIT 100
	`, item.Connection.ID)
	if err != nil {
		return AdministrativeConnection{}, internal(err)
	}
	defer rows.Close()
	item.MigrationHistory = make([]AdministrativeRelayMigration, 0)
	for rows.Next() {
		var migration AdministrativeRelayMigration
		var completedAt sql.NullTime
		if err := rows.Scan(
			&migration.ID, &migration.PreviousNodeID, &migration.NewNodeID,
			&migration.State, &migration.Reason, &migration.Attempt,
			&migration.FailureReason, &migration.CreatedAt, &completedAt,
		); err != nil {
			return AdministrativeConnection{}, internal(err)
		}
		if completedAt.Valid {
			migration.CompletedAt = &completedAt.Time
		}
		item.MigrationHistory = append(item.MigrationHistory, migration)
	}
	if err := rows.Err(); err != nil {
		return AdministrativeConnection{}, internal(err)
	}
	return item, nil
}

func (s *OnlineService) CloseConnection(
	ctx context.Context,
	connectionID, reasonInput string,
	meta RequestMeta,
) (OnlineOperationResult[AdministrativeConnection], error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return OnlineOperationResult[AdministrativeConnection]{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OnlineOperationResult[AdministrativeConnection]{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldItem, err := queryAdministrativeConnection(ctx, tx, strings.TrimSpace(connectionID), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return OnlineOperationResult[AdministrativeConnection]{}, &ServiceError{
			Status: 404, Code: "CONNECTION_NOT_FOUND", Message: "Connection not found.",
		}
	}
	if err != nil {
		return OnlineOperationResult[AdministrativeConnection]{}, internal(err)
	}
	if isTerminalConnectionState(oldItem.Connection.State) {
		return OnlineOperationResult[AdministrativeConnection]{}, &ServiceError{
			Status: 409, Code: "CONNECTION_ALREADY_TERMINAL",
			Message: "Connection is already closed or terminal.",
		}
	}
	now := s.now().UTC()
	var item AdministrativeConnection
	item, err = scanAdministrativeConnection(tx.QueryRow(ctx, `
		WITH updated AS (
			UPDATE connections
			SET state = 'CLOSED', selected_path = NULL,
			    failure_reason = 'ADMIN_CLOSED',
			    closed_at = COALESCE(closed_at, $2), updated_at = $2
			WHERE id = $1
			RETURNING *
		)
		SELECT c.id, c.room_id, c.host_player_id, c.peer_player_id, c.state,
		       COALESCE(c.selected_path, ''), COALESCE(c.failure_reason, ''),
		       c.expires_at, c.created_at, c.updated_at, c.closed_at,
		       COALESCE(allocation.id, ''), COALESCE(allocation.relay_node_id, ''),
		       COALESCE(allocation.state, '')
		FROM updated AS c
		LEFT JOIN LATERAL (
			SELECT id, relay_node_id, state
			FROM relay_allocations
			WHERE connection_id = c.id AND state NOT IN ('CLOSED', 'FAILED')
			ORDER BY created_at DESC LIMIT 1
		) AS allocation ON TRUE
	`, oldItem.Connection.ID, now))
	if err != nil {
		return OnlineOperationResult[AdministrativeConnection]{}, internal(err)
	}
	if err := s.insertOnlineAudit(
		ctx, tx, meta, "CONNECTION_CLOSED", "connection", item.Connection.ID,
		connectionAuditValue(oldItem), connectionAuditValue(item), reason, now,
	); err != nil {
		return OnlineOperationResult[AdministrativeConnection]{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OnlineOperationResult[AdministrativeConnection]{}, internal(fmt.Errorf("commit administrator connection closure: %w", err))
	}
	cleanupComplete := true
	if s.connections != nil {
		if err := s.connections.FinalizeAdministrativeClose(ctx, item.Connection, "ADMIN_CLOSED"); err != nil {
			cleanupComplete = false
			s.logger.ErrorContext(ctx, "finalize administrator connection closure",
				"connection_id", item.Connection.ID, "error", err)
		}
	}
	return OnlineOperationResult[AdministrativeConnection]{
		Resource: item, ConnectionCleanupComplete: cleanupComplete,
	}, nil
}

func (s *OnlineService) MigrateConnectionRelay(
	ctx context.Context,
	connectionID, reasonInput string,
	meta RequestMeta,
) (ConnectionMigrationResult, error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return ConnectionMigrationResult{}, err
	}
	item, err := queryAdministrativeConnection(ctx, s.pool, strings.TrimSpace(connectionID), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectionMigrationResult{}, &ServiceError{
			Status: 404, Code: "CONNECTION_NOT_FOUND", Message: "Connection not found.",
		}
	}
	if err != nil {
		return ConnectionMigrationResult{}, internal(err)
	}
	if item.Connection.State != connection.StateConnected ||
		(item.Connection.SelectedPath != connection.PathUDPRelay &&
			item.Connection.SelectedPath != connection.PathTCPTLSRelay) {
		return ConnectionMigrationResult{}, &ServiceError{
			Status: 409, Code: "CONNECTION_NOT_RELAYED",
			Message: "Only a connected relay path can be migrated.",
		}
	}
	if s.relays == nil {
		return ConnectionMigrationResult{}, &ServiceError{
			Status: 503, Code: "RELAY_COORDINATOR_UNAVAILABLE",
			Message: "Relay migration is temporarily unavailable.",
		}
	}
	requestedAt := s.now().UTC()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConnectionMigrationResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.insertOnlineAudit(
		ctx, tx, meta, "CONNECTION_RELAY_MIGRATION_REQUESTED", "connection", item.Connection.ID,
		connectionAuditValue(item),
		map[string]any{"relay_node_id": item.RelayNodeID, "state": "REQUESTED"},
		reason, requestedAt,
	); err != nil {
		return ConnectionMigrationResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectionMigrationResult{}, internal(fmt.Errorf("commit relay migration request audit: %w", err))
	}
	migrationID, previousNodeID, newNodeID, err := s.relays.MigrateConnection(ctx, item.Connection.ID)
	if err != nil {
		s.recordConnectionMigrationOutcome(ctx, meta, item.Connection.ID, reason, "FAILED", map[string]any{
			"previous_relay_node_id": item.RelayNodeID,
		})
		var publicError interface {
			HTTPStatus() int
			ErrorCode() string
			PublicMessage() string
		}
		if errors.As(err, &publicError) {
			return ConnectionMigrationResult{}, &ServiceError{
				Status: publicError.HTTPStatus(), Code: publicError.ErrorCode(),
				Message: publicError.PublicMessage(),
			}
		}
		return ConnectionMigrationResult{}, internal(err)
	}
	result := ConnectionMigrationResult{
		ConnectionID: item.Connection.ID, MigrationID: migrationID,
		PreviousNodeID: previousNodeID, NewNodeID: newNodeID,
	}
	s.recordConnectionMigrationOutcome(ctx, meta, item.Connection.ID, reason, "SUCCEEDED", map[string]any{
		"migration_id": migrationID, "previous_relay_node_id": previousNodeID,
		"new_relay_node_id": newNodeID, "state": "BINDING",
	})
	return result, nil
}

func (s *OnlineService) CloseRoom(
	ctx context.Context,
	roomID, reasonInput string,
	meta RequestMeta,
) (OnlineOperationResult[p2proom.Room], error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldRoom, err := queryAdministrativeRoom(ctx, tx, roomID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{Status: 404, Code: "ROOM_NOT_FOUND", Message: "P2P room not found."}
	}
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if oldRoom.State == p2proom.StateClosed {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{
			Status: http.StatusConflict, Code: "ROOM_ALREADY_CLOSED", Message: "P2P room is already closed.",
		}
	}
	now := s.now().UTC()
	room, err := updateAdministrativeRoomClosed(ctx, tx, roomID, now)
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_room_members
		SET status = 'LEFT', left_at = COALESCE(left_at, $2)
		WHERE room_id = $1 AND status = 'ACTIVE'
	`, roomID, now); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if err := s.insertOnlineAudit(ctx, tx, meta, "P2P_ROOM_CLOSED", "p2p_room", roomID,
		roomAuditValue(oldRoom), roomAuditValue(room), reason, now); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(fmt.Errorf("commit administrator room closure: %w", err))
	}
	cleanupComplete := true
	if s.connections != nil {
		if err := s.connections.CloseForRoom(ctx, roomID, "ADMIN_ROOM_CLOSED"); err != nil {
			cleanupComplete = false
			s.logger.ErrorContext(ctx, "close connections after administrator room closure", "room_id", roomID, "error", err)
		}
	}
	return OnlineOperationResult[p2proom.Room]{
		Resource: room, ConnectionCleanupComplete: cleanupComplete,
	}, nil
}

func (s *OnlineService) RemoveRoomMember(
	ctx context.Context,
	roomID, playerID, reasonInput string,
	meta RequestMeta,
) (OnlineOperationResult[p2proom.Room], error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	room, err := queryAdministrativeRoom(ctx, tx, roomID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{Status: 404, Code: "ROOM_NOT_FOUND", Message: "P2P room not found."}
	}
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if room.State == p2proom.StateClosed {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{Status: 409, Code: "ROOM_CLOSED", Message: "P2P room is closed."}
	}
	var role, status string
	err = tx.QueryRow(ctx, `
		SELECT role, status
		FROM p2p_room_members
		WHERE room_id = $1 AND player_id = $2
		FOR UPDATE
	`, roomID, playerID).Scan(&role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{Status: 404, Code: "ROOM_MEMBER_NOT_FOUND", Message: "Room member not found."}
	}
	if err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if role == "HOST" {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{
			Status: 409, Code: "ROOM_HOST_CANNOT_BE_REMOVED",
			Message: "The room host cannot be removed. Close the room instead.",
		}
	}
	if status != "ACTIVE" {
		return OnlineOperationResult[p2proom.Room]{}, &ServiceError{
			Status: 409, Code: "ROOM_MEMBER_ALREADY_LEFT", Message: "Room member has already left.",
		}
	}
	now := s.now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_room_members
		SET status = 'LEFT', left_at = $3
		WHERE room_id = $1 AND player_id = $2
	`, roomID, playerID, now); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	room.PlayerCount = max(0, room.PlayerCount-1)
	room.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_rooms
		SET player_count = GREATEST(0, player_count - 1), updated_at = $2
		WHERE id = $1
	`, roomID, now); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if err := s.insertOnlineAudit(ctx, tx, meta, "P2P_ROOM_MEMBER_REMOVED", "p2p_room", roomID,
		map[string]any{"player_id": playerID, "status": "ACTIVE"},
		map[string]any{"player_id": playerID, "status": "LEFT"}, reason, now); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OnlineOperationResult[p2proom.Room]{}, internal(fmt.Errorf("commit administrator room member removal: %w", err))
	}
	cleanupComplete := true
	if s.connections != nil {
		if err := s.connections.CloseForRoomMember(ctx, roomID, playerID, "ADMIN_MEMBER_REMOVED"); err != nil {
			cleanupComplete = false
			s.logger.ErrorContext(ctx, "close connection after administrator room member removal",
				"room_id", roomID, "player_id", playerID, "error", err)
		}
	}
	return OnlineOperationResult[p2proom.Room]{
		Resource: room, ConnectionCleanupComplete: cleanupComplete,
	}, nil
}

func (s *OnlineService) ChangeGameServerState(
	ctx context.Context,
	serverID, operation, reasonInput string,
	meta RequestMeta,
) (gameserver.Server, error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return gameserver.Server{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return gameserver.Server{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldItem, err := queryAdministrativeGameServer(ctx, tx, serverID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return gameserver.Server{}, &ServiceError{Status: 404, Code: "GAME_SERVER_NOT_FOUND", Message: "Game server not found."}
	}
	if err != nil {
		return gameserver.Server{}, internal(err)
	}
	var next gameserver.State
	var action string
	revokeToken := false
	switch operation {
	case "drain":
		if oldItem.State == gameserver.StateOffline {
			return gameserver.Server{}, &ServiceError{Status: 409, Code: "GAME_SERVER_OFFLINE", Message: "Offline game servers cannot enter maintenance mode."}
		}
		next, action = gameserver.StateDraining, "GAME_SERVER_DRAINED"
	case "resume":
		if oldItem.State != gameserver.StateDraining && oldItem.State != gameserver.StateUnhealthy {
			return gameserver.Server{}, &ServiceError{Status: 409, Code: "INVALID_GAME_SERVER_STATE", Message: "Game server cannot resume from its current state."}
		}
		next, action = gameserver.StateReady, "GAME_SERVER_RESUMED"
	case "disable":
		next, action, revokeToken = gameserver.StateOffline, "GAME_SERVER_DISABLED", true
	default:
		return gameserver.Server{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid game server operation."}
	}
	now := s.now().UTC()
	item, err := updateAdministrativeGameServer(ctx, tx, serverID, next, revokeToken, now)
	if err != nil {
		return gameserver.Server{}, internal(err)
	}
	if revokeToken {
		if _, err := s.registrations.RevokeActiveForInstance(ctx, tx, item.InstanceID, now); err != nil {
			return gameserver.Server{}, internal(err)
		}
	}
	if err := s.insertOnlineAudit(ctx, tx, meta, action, "game_server", serverID,
		gameServerAuditValue(oldItem), gameServerAuditValue(item), reason, now); err != nil {
		return gameserver.Server{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return gameserver.Server{}, internal(fmt.Errorf("commit administrator game server operation: %w", err))
	}
	return item, nil
}

func (s *OnlineService) CreateGameServerRegistration(
	ctx context.Context,
	input GameServerRegistrationInput,
	meta RequestMeta,
) (GameServerRegistrationResult, error) {
	meta, reason, err := validateOnlineOperation(meta, input.Reason)
	if err != nil {
		return GameServerRegistrationResult{}, err
	}
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	if !gameserver.ValidInstanceID(input.InstanceID) {
		return GameServerRegistrationResult{}, &ServiceError{
			Status: http.StatusBadRequest, Code: "INVALID_REQUEST",
			Message: "Invalid game server instance ID.",
			Details: map[string]any{
				"instance_id": "must contain 1 to 128 supported characters",
			},
		}
	}
	if input.ExpiresInHours == 0 {
		input.ExpiresInHours = 24
	}
	if input.ExpiresInHours < 1 || input.ExpiresInHours > 168 {
		return GameServerRegistrationResult{}, &ServiceError{
			Status: http.StatusBadRequest, Code: "INVALID_REQUEST",
			Message: "Invalid registration token expiry.",
			Details: map[string]any{
				"expires_in_hours": "must be between 1 and 168",
			},
		}
	}
	plaintext, tokenHash, err := gameserverregistration.GenerateToken()
	if err != nil {
		return GameServerRegistrationResult{}, internal(err)
	}
	now := s.now().UTC()
	credential := gameserverregistration.Credential{
		ID: newID("gsrt_"), InstanceID: input.InstanceID, CreatedBy: meta.AdminID,
		ExpiresAt: now.Add(time.Duration(input.ExpiresInHours) * time.Hour), CreatedAt: now,
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return GameServerRegistrationResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	replaced, err := s.registrations.RevokeActiveForInstance(ctx, tx, credential.InstanceID, now)
	if err != nil {
		return GameServerRegistrationResult{}, internal(err)
	}
	if err := s.registrations.Insert(ctx, tx, credential, tokenHash); err != nil {
		return GameServerRegistrationResult{}, internal(err)
	}
	if err := s.insertOnlineAudit(
		ctx, tx, meta, "GAME_SERVER_REGISTRATION_TOKEN_CREATED",
		"game_server_registration", credential.ID,
		map[string]any{}, map[string]any{
			"instance_id":            credential.InstanceID,
			"expires_at":             credential.ExpiresAt,
			"replaced_active_tokens": replaced,
		}, reason, now,
	); err != nil {
		return GameServerRegistrationResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GameServerRegistrationResult{}, internal(fmt.Errorf("commit game server registration token creation: %w", err))
	}
	return GameServerRegistrationResult{Credential: credential, Plaintext: plaintext}, nil
}

func validateOnlineOperation(meta RequestMeta, reasonInput string) (RequestMeta, string, error) {
	meta = sanitizeMeta(meta)
	if meta.AdminID == "" {
		return meta, "", &ServiceError{Status: 401, Code: "ADMIN_UNAUTHORIZED", Message: "Administrator authentication is required."}
	}
	reason, err := validateAuditReason(reasonInput)
	return meta, reason, err
}

func (s *OnlineService) insertOnlineAudit(
	ctx context.Context,
	tx pgx.Tx,
	meta RequestMeta,
	action, targetType, targetID string,
	oldValue, newValue map[string]any,
	reason string,
	now time.Time,
) error {
	return s.audits.InsertAudit(ctx, tx, AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID, Action: action,
		TargetType: targetType, TargetID: targetID, OldValue: oldValue, NewValue: newValue,
		Reason: reason, RequestID: meta.RequestID, IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: "SUCCEEDED", CreatedAt: now,
	})
}

func queryAdministrativeRoom(ctx context.Context, queryer adminAuthExecutor, id string, forUpdate bool) (p2proom.Room, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var item p2proom.Room
	var closedAt sql.NullTime
	err := queryer.QueryRow(ctx, `
		SELECT id, host_player_id, display_name, region, mode, version,
		       max_players, player_count, state, last_heartbeat_at,
		       created_at, updated_at, closed_at
		FROM p2p_rooms
		WHERE id = $1
	`+suffix, strings.TrimSpace(id)).Scan(
		&item.ID, &item.HostPlayerID, &item.DisplayName, &item.Region, &item.Mode, &item.Version,
		&item.MaxPlayers, &item.PlayerCount, &item.State, &item.LastHeartbeatAt,
		&item.CreatedAt, &item.UpdatedAt, &closedAt,
	)
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}

func updateAdministrativeRoomClosed(ctx context.Context, tx pgx.Tx, id string, now time.Time) (p2proom.Room, error) {
	var item p2proom.Room
	var closedAt sql.NullTime
	err := tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = 'CLOSED', closed_at = COALESCE(closed_at, $2), updated_at = $2
		WHERE id = $1
		RETURNING id, host_player_id, display_name, region, mode, version,
		          max_players, player_count, state, last_heartbeat_at,
		          created_at, updated_at, closed_at
	`, id, now).Scan(
		&item.ID, &item.HostPlayerID, &item.DisplayName, &item.Region, &item.Mode, &item.Version,
		&item.MaxPlayers, &item.PlayerCount, &item.State, &item.LastHeartbeatAt,
		&item.CreatedAt, &item.UpdatedAt, &closedAt,
	)
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}

func queryAdministrativeGameServer(ctx context.Context, queryer adminAuthExecutor, id string, forUpdate bool) (gameserver.Server, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	return scanAdministrativeGameServer(queryer.QueryRow(ctx, `
		SELECT id, instance_id, display_name, region, mode, version,
		       public_host, public_port, max_players, player_count, state,
		       registration_issuer, token_expires_at, token_revoked_at,
		       credential_generation, COALESCE(certificate_fingerprint, ''),
		       certificate_expires_at, legacy_auth_expires_at,
		       last_heartbeat_at, created_at, updated_at
		FROM game_servers
		WHERE id = $1
	`+suffix, strings.TrimSpace(id)))
}

func updateAdministrativeGameServer(
	ctx context.Context,
	tx pgx.Tx,
	id string,
	state gameserver.State,
	revokeToken bool,
	now time.Time,
) (gameserver.Server, error) {
	return scanAdministrativeGameServer(tx.QueryRow(ctx, `
		UPDATE game_servers
		SET state = $2,
		    token_revoked_at = CASE WHEN $3 THEN COALESCE(token_revoked_at, $4) ELSE token_revoked_at END,
		    updated_at = $4
		WHERE id = $1
		RETURNING id, instance_id, display_name, region, mode, version,
		          public_host, public_port, max_players, player_count, state,
		          registration_issuer, token_expires_at, token_revoked_at,
		          credential_generation, COALESCE(certificate_fingerprint, ''),
		          certificate_expires_at, legacy_auth_expires_at,
		          last_heartbeat_at, created_at, updated_at
	`, id, state, revokeToken, now))
}

func scanAdministrativeGameServer(row pgx.Row) (gameserver.Server, error) {
	var item gameserver.Server
	var revokedAt, certificateExpiresAt, legacyAuthExpiresAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.InstanceID, &item.DisplayName, &item.Region, &item.Mode, &item.Version,
		&item.PublicHost, &item.PublicPort, &item.MaxPlayers, &item.PlayerCount, &item.State,
		&item.RegistrationIssuer, &item.TokenExpiresAt, &revokedAt,
		&item.CredentialGeneration, &item.CertificateFingerprint,
		&certificateExpiresAt, &legacyAuthExpiresAt,
		&item.LastHeartbeatAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if revokedAt.Valid {
		item.TokenRevokedAt = &revokedAt.Time
	}
	if certificateExpiresAt.Valid {
		item.CertificateExpiresAt = &certificateExpiresAt.Time
	}
	if legacyAuthExpiresAt.Valid {
		item.LegacyAuthExpiresAt = &legacyAuthExpiresAt.Time
	}
	return item, err
}

func roomAuditValue(item p2proom.Room) map[string]any {
	return map[string]any{"state": item.State, "player_count": item.PlayerCount, "closed_at": item.ClosedAt}
}

func gameServerAuditValue(item gameserver.Server) map[string]any {
	return map[string]any{"state": item.State, "token_revoked_at": item.TokenRevokedAt}
}

func queryAdministrativeConnection(
	ctx context.Context,
	queryer adminAuthExecutor,
	connectionID string,
	forUpdate bool,
) (AdministrativeConnection, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE OF c"
	}
	return scanAdministrativeConnection(queryer.QueryRow(ctx, `
		SELECT c.id, c.room_id, c.host_player_id, c.peer_player_id, c.state,
		       COALESCE(c.selected_path, ''), COALESCE(c.failure_reason, ''),
		       c.expires_at, c.created_at, c.updated_at, c.closed_at,
		       COALESCE(allocation.id, ''), COALESCE(allocation.relay_node_id, ''),
		       COALESCE(allocation.state, '')
		FROM connections AS c
		LEFT JOIN LATERAL (
			SELECT id, relay_node_id, state
			FROM relay_allocations
			WHERE connection_id = c.id AND state NOT IN ('CLOSED', 'FAILED')
			ORDER BY created_at DESC LIMIT 1
		) AS allocation ON TRUE
		WHERE c.id = $1
	`+suffix, connectionID))
}

func scanAdministrativeConnection(row pgx.Row) (AdministrativeConnection, error) {
	var item AdministrativeConnection
	var selectedPath, failureReason string
	var closedAt sql.NullTime
	err := row.Scan(
		&item.Connection.ID, &item.Connection.RoomID,
		&item.Connection.HostPlayerID, &item.Connection.PeerPlayerID,
		&item.Connection.State, &selectedPath, &failureReason,
		&item.Connection.ExpiresAt, &item.Connection.CreatedAt,
		&item.Connection.UpdatedAt, &closedAt,
		&item.AllocationID, &item.RelayNodeID, &item.AllocationState,
	)
	item.Connection.SelectedPath = connection.Path(selectedPath)
	item.Connection.FailureReason = failureReason
	if closedAt.Valid {
		item.Connection.ClosedAt = &closedAt.Time
	}
	return item, err
}

func validAdministrativeConnectionState(value string) bool {
	switch connection.State(value) {
	case connection.StateCreated,
		connection.StateGatheringCandidates,
		connection.StateCheckingDirect,
		connection.StateAllocatingRelay,
		connection.StateRelayBinding,
		connection.StateMigratingRelay,
		connection.StateConnected,
		connection.StateFailed,
		connection.StateExpired,
		connection.StateClosed:
		return true
	default:
		return false
	}
}

func isTerminalConnectionState(state connection.State) bool {
	return state == connection.StateFailed || state == connection.StateExpired || state == connection.StateClosed
}

func connectionAuditValue(item AdministrativeConnection) map[string]any {
	return map[string]any{
		"state": item.Connection.State, "selected_path": item.Connection.SelectedPath,
		"failure_reason": item.Connection.FailureReason, "closed_at": item.Connection.ClosedAt,
		"relay_node_id": item.RelayNodeID, "allocation_id": item.AllocationID,
		"allocation_state": item.AllocationState,
	}
}

func (s *OnlineService) recordConnectionMigrationOutcome(
	ctx context.Context,
	meta RequestMeta,
	connectionID, reason, result string,
	value map[string]any,
) {
	auditCtx := context.WithoutCancel(ctx)
	tx, err := s.pool.BeginTx(auditCtx, pgx.TxOptions{})
	if err != nil {
		s.logger.ErrorContext(auditCtx, "begin relay migration outcome audit",
			"connection_id", connectionID, "result", result, "error", err)
		return
	}
	defer func() { _ = tx.Rollback(auditCtx) }()
	action := "CONNECTION_RELAY_MIGRATION_STARTED"
	if result == "FAILED" {
		action = "CONNECTION_RELAY_MIGRATION_FAILED"
	}
	err = s.audits.InsertAudit(auditCtx, tx, AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID, Action: action,
		TargetType: "connection", TargetID: connectionID,
		OldValue: map[string]any{}, NewValue: value,
		Reason: reason, RequestID: meta.RequestID, IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: result, CreatedAt: s.now().UTC(),
	})
	if err == nil {
		err = tx.Commit(auditCtx)
	}
	if err != nil {
		s.logger.ErrorContext(auditCtx, "write relay migration outcome audit",
			"connection_id", connectionID, "result", result, "error", err)
	}
}
