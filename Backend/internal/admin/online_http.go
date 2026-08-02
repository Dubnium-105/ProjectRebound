package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type OnlineHTTPService interface {
	CloseRoom(context.Context, string, string, RequestMeta) (OnlineOperationResult[p2proom.Room], error)
	RemoveRoomMember(context.Context, string, string, string, RequestMeta) (OnlineOperationResult[p2proom.Room], error)
	ChangeGameServerState(context.Context, string, string, string, RequestMeta) (gameserver.Server, error)
	CreateGameServerRegistration(context.Context, GameServerRegistrationInput, RequestMeta) (GameServerRegistrationResult, error)
	ListRoomMembers(context.Context, string) ([]AdministrativeRoomMember, error)
	ListConnections(context.Context, AdministrativeConnectionFilter) (AdministrativeConnectionList, error)
	GetConnection(context.Context, string) (AdministrativeConnection, error)
	CloseConnection(context.Context, string, string, RequestMeta) (OnlineOperationResult[AdministrativeConnection], error)
	MigrateConnectionRelay(context.Context, string, string, RequestMeta) (ConnectionMigrationResult, error)
}

type OnlineHTTPHandler struct {
	service    OnlineHTTPService
	logger     *slog.Logger
	trustProxy bool
}

type administrativeRoomResponse struct {
	RoomID          string        `json:"room_id"`
	HostPlayerID    string        `json:"host_player_id"`
	DisplayName     string        `json:"display_name"`
	Region          string        `json:"region"`
	Mode            string        `json:"mode"`
	Version         string        `json:"version"`
	MaxPlayers      int           `json:"max_players"`
	PlayerCount     int           `json:"player_count"`
	State           p2proom.State `json:"state"`
	LastHeartbeatAt time.Time     `json:"last_heartbeat_at"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	ClosedAt        *time.Time    `json:"closed_at"`
}

type administrativeGameServerResponse struct {
	ServerID        string           `json:"server_id"`
	InstanceID      string           `json:"instance_id"`
	DisplayName     string           `json:"display_name"`
	Region          string           `json:"region"`
	Mode            string           `json:"mode"`
	Version         string           `json:"version"`
	PublicHost      string           `json:"public_host"`
	PublicPort      int              `json:"public_port"`
	MaxPlayers      int              `json:"max_players"`
	PlayerCount     int              `json:"player_count"`
	State           gameserver.State `json:"state"`
	TokenExpiresAt  time.Time        `json:"token_expires_at"`
	TokenRevokedAt  *time.Time       `json:"token_revoked_at"`
	LastHeartbeatAt time.Time        `json:"last_heartbeat_at"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type gameServerRegistrationRequest struct {
	InstanceID     string `json:"instance_id"`
	ExpiresInHours int    `json:"expires_in_hours"`
	Reason         string `json:"reason"`
}

type gameServerRegistrationResponse struct {
	RegistrationID    string    `json:"registration_id"`
	InstanceID        string    `json:"instance_id"`
	RegistrationToken string    `json:"registration_token"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type administrativeConnectionResponse struct {
	ConnectionID     string                            `json:"connection_id"`
	RoomID           string                            `json:"room_id"`
	HostPlayerID     string                            `json:"host_player_id"`
	PeerPlayerID     string                            `json:"peer_player_id"`
	State            string                            `json:"state"`
	SelectedPath     string                            `json:"selected_path"`
	FailureReason    string                            `json:"failure_reason"`
	AllocationID     string                            `json:"allocation_id"`
	RelayNodeID      string                            `json:"relay_node_id"`
	AllocationState  string                            `json:"allocation_state"`
	ExpiresAt        time.Time                         `json:"expires_at"`
	CreatedAt        time.Time                         `json:"created_at"`
	UpdatedAt        time.Time                         `json:"updated_at"`
	ClosedAt         *time.Time                        `json:"closed_at"`
	MigrationHistory []administrativeMigrationResponse `json:"migration_history,omitempty"`
}

type administrativeMigrationResponse struct {
	MigrationID    string     `json:"migration_id"`
	PreviousNodeID string     `json:"previous_node_id"`
	NewNodeID      string     `json:"new_node_id"`
	State          string     `json:"state"`
	Reason         string     `json:"reason"`
	Attempt        int        `json:"attempt"`
	FailureReason  string     `json:"failure_reason"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

type administrativeRoomMemberResponse struct {
	RoomID        string     `json:"room_id"`
	PlayerID      string     `json:"player_id"`
	SteamID       string     `json:"steam_id"`
	PersonaName   string     `json:"persona_name"`
	AccountStatus string     `json:"account_status"`
	Role          string     `json:"role"`
	Status        string     `json:"status"`
	JoinedAt      time.Time  `json:"joined_at"`
	LeftAt        *time.Time `json:"left_at"`
}

func NewOnlineHTTPHandler(service OnlineHTTPService, logger *slog.Logger, trustProxy bool) *OnlineHTTPHandler {
	return &OnlineHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

func (h *OnlineHTTPHandler) CloseRoom(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.CloseRoom(r.Context(), chi.URLParam(r, "room_id"), request.Reason, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"room": administrativeRoom(result.Resource), "connections_cleanup_complete": result.ConnectionCleanupComplete,
	})
}

func (h *OnlineHTTPHandler) RemoveRoomMember(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.RemoveRoomMember(
		r.Context(), chi.URLParam(r, "room_id"), chi.URLParam(r, "player_id"),
		request.Reason, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"room": administrativeRoom(result.Resource), "connections_cleanup_complete": result.ConnectionCleanupComplete,
	})
}

func (h *OnlineHTTPHandler) ListRoomMembers(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.ListRoomMembers(r.Context(), chi.URLParam(r, "room_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]administrativeRoomMemberResponse, 0, len(result))
	for _, item := range result {
		items = append(items, administrativeRoomMemberResponse{
			RoomID: item.RoomID, PlayerID: item.PlayerID, SteamID: item.SteamID,
			PersonaName: item.PersonaName, AccountStatus: item.AccountStatus,
			Role: item.Role, Status: item.Status, JoinedAt: item.JoinedAt, LeftAt: item.LeftAt,
		})
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *OnlineHTTPHandler) DrainGameServer(w http.ResponseWriter, r *http.Request) {
	h.changeGameServerState(w, r, "drain")
}

func (h *OnlineHTTPHandler) ResumeGameServer(w http.ResponseWriter, r *http.Request) {
	h.changeGameServerState(w, r, "resume")
}

func (h *OnlineHTTPHandler) DisableGameServer(w http.ResponseWriter, r *http.Request) {
	h.changeGameServerState(w, r, "disable")
}

func (h *OnlineHTTPHandler) CreateGameServerRegistration(w http.ResponseWriter, r *http.Request) {
	var request gameServerRegistrationRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.CreateGameServerRegistration(
		r.Context(),
		GameServerRegistrationInput{
			InstanceID: request.InstanceID, ExpiresInHours: request.ExpiresInHours,
			Reason: request.Reason,
		},
		h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	api.WriteData(w, r, http.StatusCreated, gameServerRegistrationResponse{
		RegistrationID:    result.Credential.ID,
		InstanceID:        result.Credential.InstanceID,
		RegistrationToken: result.Plaintext,
		ExpiresAt:         result.Credential.ExpiresAt,
	})
}

func (h *OnlineHTTPHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultOnlineString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.ListConnections(r.Context(), AdministrativeConnectionFilter{
		Cursor: r.URL.Query().Get("cursor"), State: r.URL.Query().Get("state"),
		RoomID: r.URL.Query().Get("room_id"), PlayerID: r.URL.Query().Get("player_id"),
		RelayNodeID: r.URL.Query().Get("relay_node_id"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]administrativeConnectionResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, administrativeConnection(item))
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *OnlineHTTPHandler) GetConnection(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.GetConnection(r.Context(), chi.URLParam(r, "connection_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, administrativeConnection(item))
}

func (h *OnlineHTTPHandler) CloseConnection(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.CloseConnection(
		r.Context(), chi.URLParam(r, "connection_id"), request.Reason, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"connection":             administrativeConnection(result.Resource),
		"relay_cleanup_complete": result.ConnectionCleanupComplete,
	})
}

func (h *OnlineHTTPHandler) MigrateConnectionRelay(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.MigrateConnectionRelay(
		r.Context(), chi.URLParam(r, "connection_id"), request.Reason, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 202, map[string]any{
		"connection_id": result.ConnectionID, "migration_id": result.MigrationID,
		"previous_relay_node_id": result.PreviousNodeID, "new_relay_node_id": result.NewNodeID,
		"state": "BINDING",
	})
}

func (h *OnlineHTTPHandler) changeGameServerState(w http.ResponseWriter, r *http.Request, operation string) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	item, err := h.service.ChangeGameServerState(
		r.Context(), chi.URLParam(r, "server_id"), operation, request.Reason, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, administrativeGameServer(item))
}

func administrativeRoom(item p2proom.Room) administrativeRoomResponse {
	return administrativeRoomResponse{
		RoomID: item.ID, HostPlayerID: item.HostPlayerID, DisplayName: item.DisplayName,
		Region: item.Region, Mode: item.Mode, Version: item.Version,
		MaxPlayers: item.MaxPlayers, PlayerCount: item.PlayerCount, State: item.State,
		LastHeartbeatAt: item.LastHeartbeatAt, CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt, ClosedAt: item.ClosedAt,
	}
}

func administrativeGameServer(item gameserver.Server) administrativeGameServerResponse {
	return administrativeGameServerResponse{
		ServerID: item.ID, InstanceID: item.InstanceID, DisplayName: item.DisplayName,
		Region: item.Region, Mode: item.Mode, Version: item.Version,
		PublicHost: item.PublicHost, PublicPort: item.PublicPort,
		MaxPlayers: item.MaxPlayers, PlayerCount: item.PlayerCount, State: item.State,
		TokenExpiresAt: item.TokenExpiresAt, TokenRevokedAt: item.TokenRevokedAt,
		LastHeartbeatAt: item.LastHeartbeatAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func administrativeConnection(item AdministrativeConnection) administrativeConnectionResponse {
	migrations := make([]administrativeMigrationResponse, 0, len(item.MigrationHistory))
	for _, migration := range item.MigrationHistory {
		migrations = append(migrations, administrativeMigrationResponse{
			MigrationID: migration.ID, PreviousNodeID: migration.PreviousNodeID,
			NewNodeID: migration.NewNodeID, State: migration.State, Reason: migration.Reason,
			Attempt: migration.Attempt, FailureReason: migration.FailureReason,
			CreatedAt: migration.CreatedAt, CompletedAt: migration.CompletedAt,
		})
	}
	return administrativeConnectionResponse{
		ConnectionID: item.Connection.ID, RoomID: item.Connection.RoomID,
		HostPlayerID: item.Connection.HostPlayerID, PeerPlayerID: item.Connection.PeerPlayerID,
		State: string(item.Connection.State), SelectedPath: string(item.Connection.SelectedPath),
		FailureReason: item.Connection.FailureReason, AllocationID: item.AllocationID,
		RelayNodeID: item.RelayNodeID, AllocationState: item.AllocationState,
		ExpiresAt: item.Connection.ExpiresAt, CreatedAt: item.Connection.CreatedAt,
		UpdatedAt: item.Connection.UpdatedAt, ClosedAt: item.Connection.ClosedAt,
		MigrationHistory: migrations,
	}
}

func (h *OnlineHTTPHandler) requestMeta(r *http.Request) RequestMeta {
	principal := PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return RequestMeta{
		AdminID: adminID, RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy), UserAgent: r.UserAgent(),
	}
}

func (h *OnlineHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "administrator online operation failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func defaultOnlineString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
