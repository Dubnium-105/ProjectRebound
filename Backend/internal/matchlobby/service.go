package matchlobby

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var lobbyLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var lobbyIdempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var worldInstancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var nativeConnectionNoncePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$`)
var processStartFingerprintPattern = regexp.MustCompile(`^win-filetime:[0-9a-f]{16}$`)

// The admission mode remains strict_roster_v1. This is the native receipt
// protocol version carried by the Payload pipe and backend capability proof.
const strictNativeAdmissionVersion = "strict-roster-v2"

const authorityHeartbeatStale = 30 * time.Second

type P2PTransport interface {
	CreateManaged(context.Context, p2proom.Actor, p2proom.CreateInput) (p2proom.CreateResult, error)
	RecoverManagedHostToken(context.Context, p2proom.Actor, string, string) (string, error)
	Join(context.Context, p2proom.Actor, string, string) (p2proom.Room, error)
	LeaveManaged(context.Context, p2proom.Actor, string) (p2proom.Room, error)
	Heartbeat(context.Context, p2proom.Actor, string, string) (p2proom.Room, error)
	StartManaged(context.Context, p2proom.Actor, string, string) (p2proom.Room, error)
	DeleteManaged(context.Context, p2proom.Actor, string, string) (p2proom.Room, error)
}

type managedTransportReader interface {
	Get(context.Context, string) (p2proom.Room, error)
}

type managedVNTTransport interface {
	VNTBootstrap(context.Context, p2proom.Actor, string) (p2proom.VNTBootstrap, error)
	UpdateVNTPresence(context.Context, p2proom.Actor, string, p2proom.VNTPresenceInput) (p2proom.Room, error)
	VNTHostReady(context.Context, p2proom.Actor, string, string, int, string) (p2proom.Room, error)
}

type P2PMatchProjector interface {
	FreezeManagedAttempt(context.Context, pgx.Tx, string, string, string, string, time.Time) error
	CompleteManagedAttempt(context.Context, pgx.Tx, string, bool, time.Time) error
}

type Service struct {
	repository      *Repository
	config          config.MatchLobbyConfig
	signer          *AdmissionSigner
	p2p             P2PTransport
	p2pProjector    P2PMatchProjector
	serverFreshness time.Duration
	now             func() time.Time
}

// OwnedProcessExitEvidence is a scoped cleanup receipt from the supervisor.
// The owned-process-exited kind requires a non-zero process id and Windows
// start fingerprint; the native-process-not-started kind requires both fields
// to be absent. Neither kind is a remote proof by itself: the exact
// attempt/session/world/roster/route scope is checked in PostgreSQL, while the
// supervisor owns the local child-handle evidence.
type OwnedProcessExitEvidence struct {
	EvidenceKind            string
	OwnedProcessID          uint32
	ProcessStartFingerprint string
}

func validateOwnedProcessExitEvidence(evidence OwnedProcessExitEvidence) error {
	if evidence.EvidenceKind != "owned_process_exited" || evidence.OwnedProcessID == 0 ||
		!processStartFingerprintPattern.MatchString(evidence.ProcessStartFingerprint) {
		return invalid("Invalid owned process exit evidence.", nil)
	}
	return nil
}

func validateP2PNativeCleanupEvidence(evidence OwnedProcessExitEvidence) error {
	switch evidence.EvidenceKind {
	case "owned_process_exited":
		return validateOwnedProcessExitEvidence(evidence)
	case "native_process_not_started":
		if evidence.OwnedProcessID != 0 || evidence.ProcessStartFingerprint != "" {
			return invalid("Invalid native process not-started evidence.", nil)
		}
		return nil
	default:
		return invalid("Invalid native cleanup evidence.", nil)
	}
}

func NewService(repository *Repository, cfg config.MatchLobbyConfig, signer *AdmissionSigner, serverFreshness time.Duration) *Service {
	return &Service{repository: repository, config: cfg, signer: signer, serverFreshness: serverFreshness, now: time.Now}
}

func (s *Service) SetP2PTransport(transport P2PTransport) { s.p2p = transport }

func (s *Service) SetP2PMatchProjector(projector P2PMatchProjector) {
	s.p2pProjector = projector
}

func (s *Service) FailClosedDisabledAttempts(ctx context.Context) error {
	// Configuration validation is fail-closed for new work.  It must never
	// mutate shared match attempts: a second instance starting with a different
	// configuration cannot abort lobbies owned by the first instance.  Explicit
	// operator cancellation and the normal lease sweeper are the only paths
	// allowed to terminate an active attempt.
	return nil
}

func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (CreateResult, error) {
	if !s.config.AcceptNewLobbies {
		return CreateResult{}, conflict("MATCH_LOBBY_CREATION_DISABLED", "New match lobbies are disabled while existing authoritative attempts drain.", nil)
	}
	if err := requireActive(actor); err != nil {
		return CreateResult{}, err
	}
	if err := s.validateCreate(&input); err != nil {
		return CreateResult{}, err
	}
	now := s.now().UTC()
	requestHash := createRequestHash(input)
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.LockPlayerLobby(ctx, tx, actor.PlayerID); err != nil {
		return CreateResult{}, internal(err)
	}
	if input.IdempotencyKey != "" {
		if err := s.repository.LockIdempotency(ctx, tx, actor.PlayerID, input.IdempotencyKey); err != nil {
			return CreateResult{}, internal(err)
		}
		existing, findErr := s.repository.FindIdempotent(ctx, tx, actor.PlayerID, input.IdempotencyKey)
		if findErr == nil {
			if subtle.ConstantTimeCompare(existing.IdempotencyHash, requestHash) != 1 {
				return CreateResult{}, conflict("IDEMPOTENCY_KEY_CONFLICT", "The idempotency key was already used for a different match lobby request.", nil)
			}
			if err := tx.Commit(ctx); err != nil {
				return CreateResult{}, internal(err)
			}
			return s.recoverCreateResult(ctx, actor, existing, input)
		}
		if !errors.Is(findErr, pgx.ErrNoRows) {
			return CreateResult{}, internal(findErr)
		}
	}
	if activeLobbyID, activeErr := s.repository.ActiveLobbyForPlayer(ctx, tx, actor.PlayerID, ""); activeErr == nil {
		return CreateResult{}, conflict("MATCH_LOBBY_ALREADY_ACTIVE", "Leave the current match lobby before creating another one.", map[string]any{"lobby_id": activeLobbyID})
	} else if !errors.Is(activeErr, pgx.ErrNoRows) {
		return CreateResult{}, internal(activeErr)
	}
	lobby := Lobby{
		ID: newAdmissionID("lby_"), OwnerPlayerID: actor.PlayerID,
		DisplayName: input.DisplayName, HostingKind: input.HostingKind,
		TransportKind: input.TransportKind, Mode: input.Mode, Region: input.Region,
		ClientVersion: input.ClientVersion, ProtocolVersion: input.ProtocolVersion,
		TeamOneCapacity: input.TeamOneCapacity, TeamTwoCapacity: input.TeamTwoCapacity,
		State: StateOpen, RosterRevision: 1, IdempotencyKey: input.IdempotencyKey,
		IdempotencyHash: requestHash, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.InsertLobby(ctx, tx, lobby, input.TeamID, 0, now.Add(s.presenceGrace())); err != nil {
		return CreateResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, internal(err)
	}
	result := CreateResult{}
	if input.HostingKind == HostingP2P {
		if s.p2p == nil {
			s.repository.DeleteUnlinkedLobby(context.WithoutCancel(ctx), lobby.ID)
			return CreateResult{}, conflict("P2P_TRANSPORT_UNAVAILABLE", "P2P transport is not available.", nil)
		}
		created, err := s.p2p.CreateManaged(ctx, toP2PActor(actor), s.p2pCreateInput(lobby.ID, input))
		if err != nil {
			s.repository.DeleteUnlinkedLobby(context.WithoutCancel(ctx), lobby.ID)
			return CreateResult{}, internal(fmt.Errorf("create managed P2P transport: %w", err))
		}
		if err := s.repository.LinkP2PRoom(ctx, lobby.ID, created.Room.ID, s.now().UTC()); err != nil {
			_, _ = s.p2p.DeleteManaged(context.WithoutCancel(ctx), toP2PActor(actor), created.Room.ID, created.HostToken)
			s.repository.DeleteUnlinkedLobby(context.WithoutCancel(ctx), lobby.ID)
			return CreateResult{}, internal(err)
		}
		result.TransportHostToken = created.HostToken
	}
	snapshot, err := s.repository.Snapshot(ctx, lobby.ID, actor.PlayerID, s.now().UTC())
	if err != nil {
		return CreateResult{}, internal(err)
	}
	result.Snapshot = snapshot
	return result, nil
}

func (s *Service) recoverCreateResult(ctx context.Context, actor Actor, lobby Lobby, input CreateInput) (CreateResult, error) {
	result := CreateResult{}
	if lobby.HostingKind == HostingP2P {
		if s.p2p == nil {
			return CreateResult{}, conflict("P2P_TRANSPORT_UNAVAILABLE", "P2P transport is not available.", nil)
		}
		created, err := s.p2p.CreateManaged(ctx, toP2PActor(actor), s.p2pCreateInput(lobby.ID, input))
		if err != nil {
			return CreateResult{}, internal(fmt.Errorf("recover managed P2P transport: %w", err))
		}
		result.TransportHostToken = created.HostToken
		if lobby.P2PRoomID == "" {
			if err := s.repository.LinkP2PRoom(ctx, lobby.ID, created.Room.ID, s.now().UTC()); err != nil {
				return CreateResult{}, internal(err)
			}
		}
	}
	snapshot, err := s.repository.Snapshot(ctx, lobby.ID, actor.PlayerID, s.now().UTC())
	if err != nil {
		return CreateResult{}, internal(err)
	}
	result.Snapshot = snapshot
	return result, nil
}

func (s *Service) Get(ctx context.Context, lobbyID, viewerPlayerID string) (Snapshot, error) {
	snapshot, err := s.repository.Snapshot(ctx, strings.TrimSpace(lobbyID), viewerPlayerID, s.now().UTC())
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, notFound("MATCH_LOBBY_NOT_FOUND", "Match lobby not found.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	return snapshot, nil
}

func (s *Service) Transport(ctx context.Context, actor Actor, scopeRequest TransportScopeRequest) (TransportProjection, error) {
	scope, room, err := s.authorizeTransport(ctx, actor, scopeRequest)
	if err != nil {
		return TransportProjection{}, err
	}
	return TransportProjection{
		AttemptID: scope.AttemptID, LobbyID: scope.LobbyID,
		RosterRevision: scope.RosterRevision, RouteGeneration: scope.RouteGeneration,
		Room: projectTransportRoom(room),
	}, nil
}

func (s *Service) TransportVNTBootstrap(ctx context.Context, actor Actor, scopeRequest TransportScopeRequest) (p2proom.VNTBootstrap, error) {
	scope, _, err := s.authorizeTransport(ctx, actor, scopeRequest)
	if err != nil {
		return p2proom.VNTBootstrap{}, err
	}
	if scope.TransportKind != TransportVNT {
		return p2proom.VNTBootstrap{}, conflict("MATCH_VNT_TRANSPORT_REQUIRED", "This authoritative attempt does not use VNT transport.", nil)
	}
	transport, ok := s.p2p.(managedVNTTransport)
	if !ok {
		return p2proom.VNTBootstrap{}, conflict("MATCH_TRANSPORT_UNAVAILABLE", "The authoritative transport is unavailable.", nil)
	}
	ctx = p2proom.WithManagedAttemptScope(ctx, p2proom.ManagedAttemptScope{
		AttemptID: scope.AttemptID, RosterRevision: scope.RosterRevision, RouteGeneration: scope.RouteGeneration,
	})
	result, err := transport.VNTBootstrap(ctx, toP2PActor(actor), scope.RoomID)
	if err != nil {
		return p2proom.VNTBootstrap{}, mapTransportDependencyError(err)
	}
	return result, nil
}

func (s *Service) TransportVNTPresence(ctx context.Context, actor Actor, scopeRequest TransportScopeRequest, input p2proom.VNTPresenceInput) (TransportRoomProjection, error) {
	scope, _, err := s.authorizeTransport(ctx, actor, scopeRequest)
	if err != nil {
		return TransportRoomProjection{}, err
	}
	if scope.TransportKind != TransportVNT {
		return TransportRoomProjection{}, conflict("MATCH_VNT_TRANSPORT_REQUIRED", "This authoritative attempt does not use VNT transport.", nil)
	}
	transport, ok := s.p2p.(managedVNTTransport)
	if !ok {
		return TransportRoomProjection{}, conflict("MATCH_TRANSPORT_UNAVAILABLE", "The authoritative transport is unavailable.", nil)
	}
	ctx = p2proom.WithManagedAttemptScope(ctx, p2proom.ManagedAttemptScope{
		AttemptID: scope.AttemptID, RosterRevision: scope.RosterRevision, RouteGeneration: scope.RouteGeneration,
	})
	room, err := transport.UpdateVNTPresence(ctx, toP2PActor(actor), scope.RoomID, input)
	if err != nil {
		return TransportRoomProjection{}, mapTransportDependencyError(err)
	}
	return projectTransportRoom(room), nil
}

func (s *Service) TransportVNTHostReady(ctx context.Context, actor Actor, scopeRequest TransportScopeRequest, hostToken string, generation int, virtualIP string) (TransportRoomProjection, error) {
	scope, _, err := s.authorizeTransport(ctx, actor, scopeRequest)
	if err != nil {
		return TransportRoomProjection{}, err
	}
	if scope.TransportKind != TransportVNT {
		return TransportRoomProjection{}, conflict("MATCH_VNT_TRANSPORT_REQUIRED", "This authoritative attempt does not use VNT transport.", nil)
	}
	if scope.RoomRole != "HOST" {
		return TransportRoomProjection{}, forbidden("MATCH_TRANSPORT_HOST_REQUIRED", "Only the frozen host may mark the VNT transport ready.")
	}
	if strings.TrimSpace(hostToken) == "" {
		return TransportRoomProjection{}, forbidden("MATCH_TRANSPORT_HOST_TOKEN_REQUIRED", "The transport host token is required.")
	}
	transport, ok := s.p2p.(managedVNTTransport)
	if !ok {
		return TransportRoomProjection{}, conflict("MATCH_TRANSPORT_UNAVAILABLE", "The authoritative transport is unavailable.", nil)
	}
	ctx = p2proom.WithManagedAttemptScope(ctx, p2proom.ManagedAttemptScope{
		AttemptID: scope.AttemptID, RosterRevision: scope.RosterRevision, RouteGeneration: scope.RouteGeneration,
		RequiredRole: "HOST",
	})
	room, err := transport.VNTHostReady(ctx, toP2PActor(actor), scope.RoomID, hostToken, generation, virtualIP)
	if err != nil {
		return TransportRoomProjection{}, mapTransportDependencyError(err)
	}
	return projectTransportRoom(room), nil
}

func (s *Service) authorizeTransport(ctx context.Context, actor Actor, request TransportScopeRequest) (transportScope, p2proom.Room, error) {
	if err := requireActive(actor); err != nil {
		return transportScope{}, p2proom.Room{}, err
	}
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if request.AttemptID == "" || request.RosterRevision < 1 || request.RouteGeneration < 1 {
		return transportScope{}, p2proom.Room{}, invalid("A positive attempt transport scope is required.", nil)
	}
	scope, err := s.repository.TransportScope(ctx, request.AttemptID, actor.PlayerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return transportScope{}, p2proom.Room{}, notFound("MATCH_ATTEMPT_NOT_FOUND", "The authoritative match attempt was not found.")
		}
		return transportScope{}, p2proom.Room{}, internal(err)
	}
	if scope.RoomRole == "" {
		return transportScope{}, p2proom.Room{}, forbidden("MATCH_TRANSPORT_MEMBERSHIP_REQUIRED", "The authenticated player is not in the frozen attempt roster.")
	}
	if scope.HostingKind != HostingP2P || scope.RoomID == "" {
		return transportScope{}, p2proom.Room{}, conflict("MATCH_P2P_TRANSPORT_REQUIRED", "The authoritative attempt has no managed P2P transport.", nil)
	}
	if scope.RosterRevision != request.RosterRevision || scope.RouteGeneration != request.RouteGeneration {
		return transportScope{}, p2proom.Room{}, conflict("MATCH_TRANSPORT_SCOPE_MISMATCH", "The transport scope no longer matches the frozen attempt.", map[string]any{
			"roster_revision": scope.RosterRevision, "route_generation": scope.RouteGeneration,
		})
	}
	reader, ok := s.p2p.(managedTransportReader)
	if !ok {
		return transportScope{}, p2proom.Room{}, conflict("MATCH_TRANSPORT_UNAVAILABLE", "The authoritative transport is unavailable.", nil)
	}
	room, err := reader.Get(ctx, scope.RoomID)
	if err != nil {
		return transportScope{}, p2proom.Room{}, mapTransportDependencyError(err)
	}
	if room.ID != scope.RoomID || room.ManagedLobbyID != scope.LobbyID || room.TransportKind != p2proom.TransportKind(scope.TransportKind) {
		return transportScope{}, p2proom.Room{}, conflict("MATCH_TRANSPORT_SCOPE_MISMATCH", "The managed transport does not match the authoritative lobby.", nil)
	}
	if room.State == p2proom.StateClosed || room.State == p2proom.StateStale {
		return transportScope{}, p2proom.Room{}, conflict("MATCH_TRANSPORT_NOT_ACTIVE", "The managed transport is no longer active.", nil)
	}
	return scope, room, nil
}

func projectTransportRoom(room p2proom.Room) TransportRoomProjection {
	return TransportRoomProjection{
		RoomID: room.ID, HostPlayerID: room.HostPlayerID, DisplayName: room.DisplayName,
		Region: room.Region, Mode: room.Mode, Version: room.Version,
		MaxPlayers: room.MaxPlayers, PlayerCount: room.PlayerCount, State: string(room.State),
		LastHeartbeatAt: room.LastHeartbeatAt, CreatedAt: room.CreatedAt,
		TransportKind: string(room.TransportKind), VNTNodeID: room.VNTNodeID,
		VNTHost: room.VNTHost, VNTPort: room.VNTPort, VNTRegion: room.VNTRegion,
		VNTLocation: room.VNTLocation, VNTState: room.VNTState,
		VNTGeneration: room.VNTGeneration, ExpiresAt: room.ExpiresAt,
	}
}

type transportDependencyError interface {
	ErrorDetails() (int, string, string, map[string]any)
}

func mapTransportDependencyError(err error) error {
	if err == nil {
		return nil
	}
	var dependency transportDependencyError
	if errors.As(err, &dependency) {
		status, code, message, details := dependency.ErrorDetails()
		return &serviceError{status: status, code: code, message: message, details: details}
	}
	return internal(err)
}

// CurrentMemberConnection returns a server-validated connection receipt for
// the authenticated player only.  A normal MEMBER must have a consumed grant
// whose route, generation, and nonce match the frozen roster projection.  A
// P2P HOST has no JoinGrant; that branch is accepted only from its persisted
// CONNECTED live native nonce after authority readiness.
func (s *Service) CurrentMemberConnection(ctx context.Context, actor Actor, attemptID string) (MemberConnectionEvidence, error) {
	if err := requireActive(actor); err != nil {
		return MemberConnectionEvidence{}, err
	}
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return MemberConnectionEvidence{}, invalid("Invalid match attempt.", nil)
	}
	evidence, err := s.repository.MemberConnectionEvidence(ctx, attemptID, actor.PlayerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberConnectionEvidence{}, conflict(
			"MATCH_CONNECTION_NOT_CONNECTED",
			"The authenticated player has no current server-validated connection for this match attempt.",
			nil,
		)
	}
	if err != nil {
		return MemberConnectionEvidence{}, internal(err)
	}
	return evidence, nil
}

func (s *Service) Active(ctx context.Context, actor Actor) (CreateResult, error) {
	if err := requireActive(actor); err != nil {
		return CreateResult{}, err
	}
	lobbyID, err := s.repository.ActiveLobbyID(ctx, actor.PlayerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, notFound("MATCH_LOBBY_NOT_ACTIVE", "The player does not have an active match lobby.")
	}
	if err != nil {
		return CreateResult{}, internal(err)
	}
	snapshot, err := s.repository.Snapshot(ctx, lobbyID, actor.PlayerID, s.now().UTC())
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, notFound("MATCH_LOBBY_NOT_ACTIVE", "The player does not have an active match lobby.")
	}
	if err != nil {
		return CreateResult{}, internal(err)
	}
	result := CreateResult{Snapshot: snapshot}
	if snapshot.HostingKind == HostingP2P && snapshot.OwnerPlayerID == actor.PlayerID {
		if s.p2p == nil {
			return CreateResult{}, conflict("P2P_TRANSPORT_UNAVAILABLE", "P2P transport is not available.", nil)
		}
		if snapshot.P2PRoomID == "" {
			return CreateResult{}, internal(errors.New("active P2P lobby omitted its managed transport room"))
		}
		result.TransportHostToken, err = s.p2p.RecoverManagedHostToken(
			ctx, toP2PActor(actor), snapshot.P2PRoomID, snapshot.LobbyID,
		)
		if err != nil {
			return CreateResult{}, internal(fmt.Errorf("recover managed P2P transport credential: %w", err))
		}
	}
	return result, nil
}

func (s *Service) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return ListResult{}, invalid("Invalid limit.", map[string]any{"limit": "must be between 1 and 100"})
	}
	if filter.HostingKind != "" && filter.HostingKind != HostingDedicated && filter.HostingKind != HostingP2P {
		return ListResult{}, invalid("Invalid hosting_kind filter.", nil)
	}
	for name, value := range map[string]string{"region": filter.Region, "mode": filter.Mode, "client_version": filter.ClientVersion} {
		if value != "" && !lobbyLabelPattern.MatchString(value) {
			return ListResult{}, invalid("Invalid filter.", map[string]any{name: "contains unsupported characters"})
		}
	}
	result, err := s.repository.List(ctx, filter)
	if err != nil {
		return ListResult{}, internal(err)
	}
	return result, nil
}

func (s *Service) Join(ctx context.Context, actor Actor, lobbyID string, teamID int, expectedRevision int64) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	if teamID != 1 && teamID != 2 {
		return Snapshot{}, invalid("Invalid team.", map[string]any{"team_id": "must be 1 or 2"})
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.LockPlayerLobby(ctx, tx, actor.PlayerID); err != nil {
		return Snapshot{}, internal(err)
	}
	lobby, err := s.repository.GetLobbyForUpdate(ctx, tx, lobbyID)
	if err != nil {
		return Snapshot{}, s.mapLobbyError(err)
	}
	if lobby.HostingKind == HostingP2P && lobby.P2PRoomID == "" {
		return Snapshot{}, conflict("MATCH_LOBBY_P2P_NOT_READY", "The managed P2P transport is not ready for members.", nil)
	}
	existing, membership, memberErr := s.repository.GetMemberForUpdate(ctx, tx, lobby.ID, actor.PlayerID)
	if memberErr == nil && membership == "ACTIVE" {
		if existing.TeamID != teamID {
			return Snapshot{}, conflict("MATCH_LOBBY_ALREADY_JOINED", "Use the team selection endpoint to switch teams.", nil)
		}
		_, err = tx.Exec(ctx, `
			UPDATE match_lobby_members SET presence_state = 'ONLINE', presence_expires_at = $3,
			       last_seen_at = $2 WHERE lobby_id = $1 AND player_id = $4
		`, lobby.ID, now, now.Add(s.presenceGrace()), actor.PlayerID)
		if err != nil {
			return Snapshot{}, internal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobby.ID, actor.PlayerID)
	}
	if err := requireOpenRevision(lobby, expectedRevision); err != nil {
		return Snapshot{}, err
	}
	if memberErr != nil && !errors.Is(memberErr, pgx.ErrNoRows) {
		return Snapshot{}, internal(memberErr)
	}
	if activeLobbyID, activeErr := s.repository.ActiveLobbyForPlayer(ctx, tx, actor.PlayerID, lobby.ID); activeErr == nil {
		return Snapshot{}, conflict("MATCH_LOBBY_ALREADY_ACTIVE", "Leave the current match lobby before joining another one.", map[string]any{"lobby_id": activeLobbyID})
	} else if !errors.Is(activeErr, pgx.ErrNoRows) {
		return Snapshot{}, internal(activeErr)
	}
	slot, err := s.repository.NextTeamSlot(ctx, tx, lobby, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, conflict("MATCH_LOBBY_TEAM_FULL", "The selected team is full.", nil)
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if err := s.repository.UpsertMember(ctx, tx, lobby.ID, actor.PlayerID, teamID, slot, now, now.Add(s.presenceGrace())); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET roster_revision = roster_revision + 1, updated_at = $2 WHERE id = $1`, lobby.ID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobby_members SET ready = FALSE WHERE lobby_id = $1 AND membership_state = 'ACTIVE'`, lobby.ID); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	if lobby.HostingKind == HostingP2P && lobby.P2PRoomID != "" {
		if s.p2p == nil {
			_ = s.compensateJoin(context.WithoutCancel(ctx), lobby.ID, actor.PlayerID)
			return Snapshot{}, conflict("P2P_TRANSPORT_UNAVAILABLE", "P2P transport is not available.", nil)
		}
		if _, err := s.p2p.Join(ctx, toP2PActor(actor), lobby.P2PRoomID, lobby.ClientVersion); err != nil {
			_ = s.compensateJoin(context.WithoutCancel(ctx), lobby.ID, actor.PlayerID)
			return Snapshot{}, internal(fmt.Errorf("project match lobby member to P2P room: %w", err))
		}
	}
	return s.Get(ctx, lobby.ID, actor.PlayerID)
}

func (s *Service) SelectTeam(ctx context.Context, actor Actor, lobbyID string, teamID int, expectedRevision int64) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	if teamID != 1 && teamID != 2 {
		return Snapshot{}, invalid("Invalid team.", map[string]any{"team_id": "must be 1 or 2"})
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	lobby, err := s.repository.GetLobbyForUpdate(ctx, tx, lobbyID)
	if err != nil {
		return Snapshot{}, s.mapLobbyError(err)
	}
	if err := requireOpenRevision(lobby, expectedRevision); err != nil {
		return Snapshot{}, err
	}
	member, membership, err := s.repository.GetMemberForUpdate(ctx, tx, lobby.ID, actor.PlayerID)
	if errors.Is(err, pgx.ErrNoRows) || membership != "ACTIVE" {
		return Snapshot{}, forbidden("MATCH_LOBBY_MEMBERSHIP_REQUIRED", "Join the lobby before selecting a team.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if member.TeamID == teamID {
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobby.ID, actor.PlayerID)
	}
	slot, err := s.repository.NextTeamSlot(ctx, tx, lobby, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, conflict("MATCH_LOBBY_TEAM_FULL", "The selected team is full.", nil)
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobby_members SET team_id = $3, team_slot = $4, ready = FALSE,
		       last_seen_at = $5, presence_state = 'ONLINE', presence_expires_at = $6
		WHERE lobby_id = $1 AND player_id = $2 AND membership_state = 'ACTIVE'
	`, lobby.ID, actor.PlayerID, teamID, slot, now, now.Add(s.presenceGrace())); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobby_members SET ready = FALSE WHERE lobby_id = $1 AND membership_state = 'ACTIVE'`, lobby.ID); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET roster_revision = roster_revision + 1, updated_at = $2 WHERE id = $1`, lobby.ID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobby.ID, actor.PlayerID)
}

func (s *Service) SetReady(ctx context.Context, actor Actor, lobbyID string, ready bool, expectedRevision int64) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	lobby, err := s.repository.GetLobbyForUpdate(ctx, tx, lobbyID)
	if err != nil {
		return Snapshot{}, s.mapLobbyError(err)
	}
	if err := requireOpenRevision(lobby, expectedRevision); err != nil {
		return Snapshot{}, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE match_lobby_members SET ready = $3, presence_state = 'ONLINE',
		       presence_expires_at = $4, last_seen_at = $5
		WHERE lobby_id = $1 AND player_id = $2 AND membership_state = 'ACTIVE'
	`, lobby.ID, actor.PlayerID, ready, now.Add(s.presenceGrace()), now)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		return Snapshot{}, forbidden("MATCH_LOBBY_MEMBERSHIP_REQUIRED", "Join the lobby before setting ready state.")
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET updated_at = $2 WHERE id = $1`, lobby.ID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobby.ID, actor.PlayerID)
}

func (s *Service) Presence(ctx context.Context, actor Actor, lobbyID, transportHostToken string, online bool) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC()
	presenceState := "OFFLINE"
	if online {
		presenceState = "ONLINE"
	}
	command, err := s.repository.pool.Exec(ctx, `
		UPDATE match_lobby_members SET presence_state = $5, presence_expires_at = $3,
		       last_seen_at = $2
		WHERE lobby_id = $1 AND player_id = $4 AND membership_state = 'ACTIVE'
	`, lobbyID, now, now.Add(s.presenceGrace()), actor.PlayerID, presenceState)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		return Snapshot{}, forbidden("MATCH_LOBBY_MEMBERSHIP_REQUIRED", "Active lobby membership is required.")
	}
	lobby, err := s.repository.GetLobby(ctx, lobbyID)
	if online && err == nil && lobby.HostingKind == HostingP2P && lobby.P2PRoomID != "" && actor.PlayerID == lobby.OwnerPlayerID && s.p2p != nil {
		_, _ = s.p2p.Heartbeat(ctx, toP2PActor(actor), lobby.P2PRoomID, transportHostToken)
	}
	return s.Get(ctx, lobbyID, actor.PlayerID)
}

func (s *Service) Leave(ctx context.Context, actor Actor, lobbyID, transportHostToken string, expectedRevision int64) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	lobby, err := s.repository.GetLobbyForUpdate(ctx, tx, lobbyID)
	if err != nil {
		return Snapshot{}, s.mapLobbyError(err)
	}
	if err := requireOpenRevision(lobby, expectedRevision); err != nil {
		return Snapshot{}, err
	}
	member, membership, err := s.repository.GetMemberForUpdate(ctx, tx, lobby.ID, actor.PlayerID)
	if errors.Is(err, pgx.ErrNoRows) || membership != "ACTIVE" {
		return Snapshot{}, forbidden("MATCH_LOBBY_MEMBERSHIP_REQUIRED", "Active lobby membership is required.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if member.Role == "OWNER" {
		if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET state = 'ABORTED', closed_at = $2, updated_at = $2 WHERE id = $1`, lobby.ID, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_lobby_members SET membership_state = 'LEFT', presence_state = 'OFFLINE', ready = FALSE, left_at = $2
			WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
		`, lobby.ID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE match_lobby_members SET membership_state = 'LEFT', ready = FALSE, left_at = $3
			WHERE lobby_id = $1 AND player_id = $2
		`, lobby.ID, actor.PlayerID, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE match_lobby_members SET ready = FALSE WHERE lobby_id = $1 AND membership_state = 'ACTIVE'`, lobby.ID); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET roster_revision = roster_revision + 1, updated_at = $2 WHERE id = $1`, lobby.ID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	if lobby.HostingKind == HostingP2P && lobby.P2PRoomID != "" && s.p2p != nil {
		if member.Role == "OWNER" {
			_, _ = s.p2p.DeleteManaged(ctx, toP2PActor(actor), lobby.P2PRoomID, transportHostToken)
		} else {
			_, _ = s.p2p.LeaveManaged(ctx, toP2PActor(actor), lobby.P2PRoomID)
		}
	}
	return s.Get(ctx, lobby.ID, actor.PlayerID)
}

func (s *Service) Start(ctx context.Context, actor Actor, lobbyID string, expectedRevision int64) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	lobby, err := s.repository.GetLobbyForUpdate(ctx, tx, lobbyID)
	if err != nil {
		return Snapshot{}, s.mapLobbyError(err)
	}
	if lobby.OwnerPlayerID != actor.PlayerID {
		return Snapshot{}, forbidden("MATCH_LOBBY_OWNER_REQUIRED", "Only the lobby owner can start the match.")
	}
	if lobby.CurrentAttemptID != "" && (lobby.State == StateFrozen || lobby.State == StateProvisioning || lobby.State == StateConnecting || lobby.State == StateRunning) {
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobby.ID, actor.PlayerID)
	}
	var cleanupPending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM match_attempts
			WHERE lobby_id = $1 AND state IN ('COMPLETED', 'ABORTED')
			  AND cleanup_state = 'PENDING'
		)
	`, lobby.ID).Scan(&cleanupPending); err != nil {
		return Snapshot{}, internal(err)
	}
	if cleanupPending {
		return Snapshot{}, conflict("MATCH_ATTEMPT_CLEANUP_PENDING", "The previous match attempt is still releasing its native world and transport resources.", nil)
	}
	if err := requireOpenRevision(lobby, expectedRevision); err != nil {
		return Snapshot{}, err
	}
	var teamOne, teamTwo, notReadyOrOffline, unverified int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE team_id = 1),
		       COUNT(*) FILTER (WHERE team_id = 2),
		       COUNT(*) FILTER (WHERE NOT member.ready OR member.presence_state <> 'ONLINE' OR member.presence_expires_at <= $2),
		       COUNT(*) FILTER (WHERE player.auth_level NOT IN ('verified', 'trusted'))
		FROM match_lobby_members AS member
		JOIN players AS player ON player.id = member.player_id
		WHERE member.lobby_id = $1 AND member.membership_state = 'ACTIVE'
	`, lobby.ID, now).Scan(&teamOne, &teamTwo, &notReadyOrOffline, &unverified); err != nil {
		return Snapshot{}, internal(err)
	}
	if teamOne == 0 || teamTwo == 0 {
		return Snapshot{}, conflict("MATCH_LOBBY_BOTH_TEAMS_REQUIRED", "Both teams must contain at least one player.", nil)
	}
	if notReadyOrOffline != 0 {
		return Snapshot{}, conflict("MATCH_LOBBY_NOT_READY", "Every seated player must be online and ready.", nil)
	}
	if unverified != 0 {
		return Snapshot{}, conflict("MATCH_LOBBY_IDENTITY_NOT_VERIFIED", "Every frozen seat requires a currently verified Steam identity.", nil)
	}
	var authorityID, endpointHost string
	var endpointPort int
	if lobby.HostingKind == HostingDedicated {
		err := tx.QueryRow(ctx, `
			SELECT id, public_host, public_port
			FROM game_servers
			WHERE state = 'READY' AND mode = $1 AND version = $2
			  AND ($3 = 'auto' OR region = $3)
			  AND max_players - player_count >= $4
			  AND last_heartbeat_at > $5 AND token_revoked_at IS NULL
			  AND token_expires_at > $6
			  AND native_admission_verified = TRUE
			  AND native_admission_game_sha256 = $7
			  AND native_admission_version = $8
			ORDER BY CASE WHEN region = $3 THEN 0 ELSE 1 END,
			         player_count::float / max_players, last_heartbeat_at DESC, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		`, lobby.Mode, lobby.ClientVersion, lobby.Region, teamOne+teamTwo,
			now.Add(-s.serverFreshness), now, strings.ToLower(strings.TrimSpace(s.config.LockedGameSHA256)), strictNativeAdmissionVersion).Scan(&authorityID, &endpointHost, &endpointPort)
		if errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, conflict("MATCH_LOBBY_NO_DEDICATED_SERVER", "No compatible dedicated server is currently available.", nil)
		}
		if err != nil {
			return Snapshot{}, internal(err)
		}
	} else if lobby.P2PRoomID == "" {
		return Snapshot{}, conflict("MATCH_LOBBY_P2P_NOT_READY", "The managed P2P transport has not been created.", nil)
	}
	if lobby.HostingKind == HostingP2P {
		var projectionMismatch bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				(SELECT player_id FROM match_lobby_members
				 WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
				 EXCEPT
				 SELECT player_id FROM p2p_room_members
				 WHERE room_id = $2 AND status = 'ACTIVE')
				UNION ALL
				(SELECT player_id FROM p2p_room_members
				 WHERE room_id = $2 AND status = 'ACTIVE'
				 EXCEPT
				 SELECT player_id FROM match_lobby_members
				 WHERE lobby_id = $1 AND membership_state = 'ACTIVE')
			)
		`, lobby.ID, lobby.P2PRoomID).Scan(&projectionMismatch); err != nil {
			return Snapshot{}, internal(err)
		}
		if projectionMismatch {
			return Snapshot{}, conflict("MATCH_P2P_TRANSPORT_SYNCING", "The P2P transport is still synchronizing the authoritative roster.", nil)
		}
	}
	var attemptNumber int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM match_attempts WHERE lobby_id = $1`, lobby.ID).Scan(&attemptNumber); err != nil {
		return Snapshot{}, internal(err)
	}
	attemptID := newAdmissionID("mat_")
	authoritySessionID := newAdmissionID("mas_")
	var hostReconnectDeadline any
	if lobby.HostingKind == HostingP2P {
		authorityID = actor.PlayerID
		hostReconnectDeadline = now.Add(s.provisioningTimeout())
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO match_attempts (
			id, lobby_id, attempt_number, hosting_kind, state, roster_revision,
			authority_id, authority_session_id, route_generation,
			endpoint_host, endpoint_port, host_reconnect_deadline, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'FROZEN', $5, $6, $7, 1,
		          NULLIF($8, ''), NULLIF($9, 0),
		          $10, $11, $11)
	`, attemptID, lobby.ID, attemptNumber, lobby.HostingKind, lobby.RosterRevision,
		authorityID, authoritySessionID, endpointHost, endpointPort, hostReconnectDeadline, now)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO match_attempt_roster (
			attempt_id, player_id, platform_id, display_name, room_role,
			team_id, team_slot, logical_slot, connection_generation,
			connection_state, auth_level_at_freeze, steam_verified_at_freeze,
			joined_lobby_at, created_at, updated_at
		)
		SELECT $1, member.player_id, player.steam_id, player.persona_name,
		       CASE WHEN $5 = 'P2P' AND member.role = 'OWNER'
		            THEN 'HOST' ELSE 'MEMBER' END,
		       member.team_id, member.team_slot,
		       CASE WHEN member.team_id = 1 THEN member.team_slot
		            ELSE $3 + member.team_slot END,
		       1, 'RESERVED', player.auth_level,
		       player.auth_level IN ('verified', 'trusted'), member.joined_at, $2, $2
		FROM match_lobby_members AS member
		JOIN players AS player ON player.id = member.player_id
		WHERE member.lobby_id = $4 AND member.membership_state = 'ACTIVE'
		ORDER BY member.team_id, member.team_slot
	`, attemptID, now, lobby.TeamOneCapacity, lobby.ID, lobby.HostingKind)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if lobby.HostingKind == HostingDedicated {
		if err := s.projectDedicated(ctx, tx, lobby, attemptID, authorityID, endpointHost, endpointPort, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE match_attempts SET state = 'PROVISIONING', updated_at = $2 WHERE id = $1 AND state = 'FROZEN'`, attemptID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobbies SET state = 'PROVISIONING', current_attempt_id = $2, updated_at = $3
		WHERE id = $1
	`, lobby.ID, attemptID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if lobby.HostingKind == HostingP2P {
		if s.p2pProjector == nil {
			return Snapshot{}, conflict("MATCH_P2P_PROJECTION_UNAVAILABLE", "The authoritative P2P match projection is unavailable.", nil)
		}
		if err := s.p2pProjector.FreezeManagedAttempt(
			ctx, tx, lobby.P2PRoomID, lobby.OwnerPlayerID, lobby.Mode, attemptID, now,
		); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobby.ID, actor.PlayerID)
}

func (s *Service) projectDedicated(ctx context.Context, tx pgx.Tx, lobby Lobby, attemptID, serverID, host string, port int, now time.Time) error {
	ticketID := newAdmissionID("mlt_")
	matchID := newAdmissionID("mlm_")
	if _, err := tx.Exec(ctx, `
		INSERT INTO meta_match_tickets (
			id, player_id, mode, region, client_version, protocol_version,
			state, matched_id, expires_at, created_at, updated_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'MATCHED', $7, $8, $9, $9, $9)
	`, ticketID, lobby.OwnerPlayerID, lobby.Mode, lobby.Region, lobby.ClientVersion,
		lobby.ProtocolVersion, matchID, now.Add(10*time.Minute), now); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE game_servers SET state = 'RESERVED', updated_at = $2 WHERE id = $1 AND state = 'READY'`, serverID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("dedicated server reservation was lost")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO meta_matches (
			id, game_server_id, ticket_id, mode, region, client_version,
			protocol_version, state, endpoint_host, endpoint_port,
			reserved_at, updated_at, match_attempt_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'RESERVED', $8, $9, $10, $10, $11)
	`, matchID, serverID, ticketID, lobby.Mode, lobby.Region, lobby.ClientVersion,
		lobby.ProtocolVersion, host, port, now, attemptID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO meta_match_players (
			match_id, player_id, auth_level_at_reservation,
			steam_verified_at_reservation, team_id, team_slot, logical_slot,
			connection_generation
		)
		SELECT $1, player_id, auth_level_at_freeze, steam_verified_at_freeze,
		       team_id, team_slot, logical_slot, connection_generation
		FROM match_attempt_roster WHERE attempt_id = $2
	`, matchID, attemptID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE match_attempts SET meta_match_id = $2 WHERE id = $1`, attemptID, matchID)
	return err
}

func (s *Service) P2PAuthorityReady(ctx context.Context, actor Actor, attemptID, authoritySession, hostToken, endpointHost string, endpointPort, routeGeneration int, worldInstanceID, nativeConnectionNonce string, preserve ...P2PHostLiveConnectionScope) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	nativeConnectionNonce = strings.TrimSpace(nativeConnectionNonce)
	if !worldInstancePattern.MatchString(worldInstanceID) {
		return Snapshot{}, invalid("Invalid P2P world instance identity.", nil)
	}
	if !nativeConnectionNoncePattern.MatchString(nativeConnectionNonce) {
		return Snapshot{}, invalid("Invalid P2P native connection nonce.", nil)
	}
	endpointIP := net.ParseIP(strings.TrimSpace(endpointHost))
	if endpointIP == nil || endpointPort < 1 || endpointPort > 65535 || routeGeneration < 1 {
		return Snapshot{}, invalid("Invalid P2P authority endpoint.", nil)
	}
	endpointHost = endpointIP.String()
	queryNow := s.now().UTC()
	var lobbyID, roomID, ownerID, storedEndpointHost, storedWorldInstanceID, storedHostNonce, storedHostState string
	var expectedRouteGeneration int
	var storedHostAuthorizationGeneration, storedHostLiveGeneration, storedHostLiveRouteGeneration int
	var rosterRevision int64
	var storedEndpointPort int
	var state AttemptState
	var hostDeadline sql.NullTime
	var payloadInstalled bool
	err := s.repository.pool.QueryRow(ctx, `
		SELECT lobby.id, COALESCE(lobby.p2p_room_id, ''), lobby.owner_player_id,
		       attempt.route_generation, attempt.roster_revision, attempt.state,
		       COALESCE(attempt.endpoint_host, ''), COALESCE(attempt.endpoint_port, 0),
		       COALESCE(attempt.world_instance_id, ''),
		       COALESCE((SELECT connection_state FROM match_attempt_roster
		                  WHERE attempt_id = attempt.id AND room_role = 'HOST'
		                  LIMIT 1), ''),
		       COALESCE((SELECT live_native_connection_nonce FROM match_attempt_roster
		                  WHERE attempt_id = attempt.id AND room_role = 'HOST'
		                  LIMIT 1), ''),
		       COALESCE((SELECT connection_generation FROM match_attempt_roster
		                  WHERE attempt_id = attempt.id AND room_role = 'HOST'
		                  LIMIT 1), 0),
		       COALESCE((SELECT live_connection_generation FROM match_attempt_roster
		                  WHERE attempt_id = attempt.id AND room_role = 'HOST'
		                  LIMIT 1), 0),
		       COALESCE((SELECT live_route_generation FROM match_attempt_roster
		                  WHERE attempt_id = attempt.id AND room_role = 'HOST'
		                  LIMIT 1), 0),
		       attempt.host_reconnect_deadline,
		       COALESCE(attempt.payload_installed_at IS NOT NULL
		         AND attempt.payload_route_generation = attempt.route_generation, FALSE)
		FROM match_attempts AS attempt
		JOIN match_lobbies AS lobby ON lobby.id = attempt.lobby_id
		WHERE attempt.id = $1 AND attempt.hosting_kind = 'P2P'
		  AND attempt.authority_session_id = $2
	`, attemptID, authoritySession).Scan(
		&lobbyID, &roomID, &ownerID, &expectedRouteGeneration, &rosterRevision, &state,
		&storedEndpointHost, &storedEndpointPort, &storedWorldInstanceID, &storedHostState, &storedHostNonce,
		&storedHostAuthorizationGeneration, &storedHostLiveGeneration, &storedHostLiveRouteGeneration,
		&hostDeadline, &payloadInstalled,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_PROVISIONING", "The P2P attempt is not waiting for its authority.", nil)
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if actor.PlayerID != ownerID {
		return Snapshot{}, forbidden("MATCH_LOBBY_OWNER_REQUIRED", "Only the frozen P2P host can publish authority readiness.")
	}
	if routeGeneration != expectedRouteGeneration {
		return Snapshot{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The P2P route generation changed; refresh the allocation before publishing readiness.", map[string]any{"route_generation": expectedRouteGeneration})
	}
	if len(preserve) > 1 {
		return Snapshot{}, invalid("Only one preserved P2P HOST connection scope may be supplied.", nil)
	}
	if state == AttemptConnecting || state == AttemptRunning {
		if len(preserve) == 1 {
			scope := preserve[0]
			if scope.AttemptID != attemptID || scope.AuthoritySessionID != authoritySession ||
				scope.WorldInstanceID != storedWorldInstanceID || scope.RosterRevision != rosterRevision ||
				scope.PlayerID != ownerID || scope.GrantJTI != "" ||
				scope.RouteGeneration != storedHostLiveRouteGeneration ||
				scope.ConnectionGeneration != storedHostLiveGeneration ||
				scope.NativeConnectionNonce != storedHostNonce || nativeConnectionNonce != storedHostNonce {
				return Snapshot{}, conflict("MATCH_HOST_LIVE_SCOPE_CONFLICT", "The preserved P2P HOST connection scope is not the persisted live scope.", nil)
			}
			if storedHostState != "CONNECTED" || storedHostLiveGeneration != storedHostAuthorizationGeneration || storedHostNonce == "" {
				return Snapshot{}, conflict("MATCH_HOST_LIVE_SCOPE_CONFLICT", "The preserved P2P HOST connection is no longer live.", nil)
			}
			if !payloadInstalled {
				return Snapshot{}, conflict("MATCH_AUTHORITY_ROUTE_REFRESHING", "The current route Payload projection is not installed.", nil)
			}
			now := s.now().UTC()
			deadline := now.Add(s.initialConnectionWindow())
			tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				return Snapshot{}, internal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var lockedAttemptID string
			if err := tx.QueryRow(ctx, `
				SELECT attempt.id
				FROM match_attempts AS attempt
				JOIN match_attempt_roster AS host
				  ON host.attempt_id = attempt.id AND host.room_role = 'HOST'
				WHERE attempt.id = $1 AND attempt.authority_id = $2
				  AND attempt.authority_session_id = $3
				  AND attempt.state IN ('CONNECTING', 'RUNNING')
				  AND attempt.route_generation = $4
				  AND attempt.payload_route_generation = $4
				  AND attempt.world_instance_id = $5
				  AND attempt.roster_revision = $6
				  AND host.player_id = $2
				  AND host.connection_state = 'CONNECTED'
				  AND host.connection_generation = $7
				  AND host.live_connection_generation = $7
				  AND host.live_route_generation = $8
				  AND host.live_native_connection_nonce = $9
				FOR UPDATE OF attempt, host
			`, attemptID, actor.PlayerID, authoritySession, routeGeneration, worldInstanceID,
				rosterRevision, scope.ConnectionGeneration, scope.RouteGeneration, scope.NativeConnectionNonce).Scan(&lockedAttemptID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return Snapshot{}, conflict("MATCH_HOST_LIVE_SCOPE_CONFLICT", "The preserved P2P HOST connection scope changed before authority readiness was committed.", nil)
				}
				return Snapshot{}, internal(err)
			}
			command, err := tx.Exec(ctx, `
				UPDATE match_attempts
				SET endpoint_host = $2, endpoint_port = $3, world_instance_id = $4,
				    connection_deadline = $5, authority_last_seen_at = $6,
				    host_reconnect_deadline = NULL, updated_at = $6
				WHERE id = $1 AND authority_id = $7 AND authority_session_id = $8
				  AND state IN ('CONNECTING', 'RUNNING')
				  AND route_generation = $9 AND payload_route_generation = $9
				  AND world_instance_id = $4
			`, attemptID, endpointHost, endpointPort, worldInstanceID, deadline, now,
				actor.PlayerID, authoritySession, routeGeneration)
			if err != nil {
				return Snapshot{}, internal(err)
			}
			if command.RowsAffected() != 1 {
				return Snapshot{}, conflict("MATCH_ATTEMPT_STATE_CONFLICT", "The preserved P2P HOST scope changed while publishing authority readiness.", nil)
			}
			command, err = tx.Exec(ctx, `
				UPDATE match_attempt_roster
				SET host_live_scope_preserved = TRUE, updated_at = $3
				WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
				  AND connection_state = 'CONNECTED'
				  AND connection_generation = $4
				  AND live_connection_generation = $4
				  AND live_route_generation = $5
				  AND live_native_connection_nonce = $6
			`, attemptID, actor.PlayerID, now, scope.ConnectionGeneration, scope.RouteGeneration, scope.NativeConnectionNonce)
			if err != nil {
				return Snapshot{}, internal(err)
			}
			if command.RowsAffected() != 1 {
				return Snapshot{}, conflict("MATCH_HOST_LIVE_SCOPE_CONFLICT", "The preserved P2P HOST connection changed while recording its acknowledgement.", nil)
			}
			if err := tx.Commit(ctx); err != nil {
				return Snapshot{}, internal(err)
			}
			return s.Get(ctx, lobbyID, actor.PlayerID)
		}
		if storedEndpointHost == endpointHost && storedEndpointPort == endpointPort &&
			storedWorldInstanceID == worldInstanceID && storedHostNonce == nativeConnectionNonce &&
			storedHostState == "CONNECTED" && storedHostLiveGeneration == storedHostAuthorizationGeneration &&
			storedHostLiveRouteGeneration == expectedRouteGeneration {
			return s.Get(ctx, lobbyID, actor.PlayerID)
		}
		// A route refresh may advance the authority route while the old native
		// HOST remains live. It must be acknowledged with the exact preserved
		// scope above; a normal Ready call can only install a fresh nonce after
		// the native DISCONNECTED event has been accepted.
		if storedHostState == "CONNECTED" &&
			(storedHostLiveGeneration != storedHostAuthorizationGeneration || storedHostLiveRouteGeneration != expectedRouteGeneration) {
			return Snapshot{}, conflict("MATCH_HOST_LIVE_CONNECTION_PENDING_DISCONNECT", "The previous native host connection must report DISCONNECTED before a replacement authority can be ready.", nil)
		}
		if !payloadInstalled {
			return Snapshot{}, conflict("MATCH_AUTHORITY_ROUTE_REFRESHING", "The current route Payload projection is not installed.", nil)
		}
		if storedHostState != "DISCONNECTED" {
			return Snapshot{}, conflict("MATCH_AUTHORITY_ENDPOINT_CONFLICT", "The P2P authority is already ready at a different endpoint.", nil)
		}
		if storedWorldInstanceID == "" || storedWorldInstanceID != worldInstanceID {
			return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_CONFLICT", "A recovering P2P authority must retain the persisted native world instance.", nil)
		}
		if s.p2p == nil {
			return Snapshot{}, conflict("P2P_TRANSPORT_UNAVAILABLE", "P2P transport is not available.", nil)
		}
		if _, err := s.p2p.StartManaged(ctx, toP2PActor(actor), roomID, hostToken); err != nil {
			return Snapshot{}, conflict("MATCH_P2P_TRANSPORT_NOT_READY", "The P2P transport is not ready to start.", nil)
		}
		now := s.now().UTC()
		deadline := now.Add(s.initialConnectionWindow())
		tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return Snapshot{}, internal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		command, err := tx.Exec(ctx, `
			UPDATE match_attempts
			SET endpoint_host = $2, endpoint_port = $3, world_instance_id = $4,
			    connection_deadline = $5, authority_last_seen_at = $6,
			    host_reconnect_deadline = NULL, updated_at = $6
			WHERE id = $1 AND authority_id = $7 AND authority_session_id = $8
			  AND state IN ('CONNECTING', 'RUNNING') AND route_generation = $9
			  AND payload_route_generation = $9
			  AND world_instance_id = $4
		`, attemptID, endpointHost, endpointPort, worldInstanceID, deadline, now,
			actor.PlayerID, authoritySession, routeGeneration)
		if err != nil {
			return Snapshot{}, internal(err)
		}
		if command.RowsAffected() != 1 {
			return Snapshot{}, conflict("MATCH_ATTEMPT_STATE_CONFLICT", "The P2P attempt changed while publishing authority readiness.", nil)
		}
		command, err = tx.Exec(ctx, `
			UPDATE match_attempt_roster
			SET connection_state = 'CONNECTED', connected_at = COALESCE(connected_at, $3),
			    disconnected_at = NULL, live_native_connection_nonce = $4,
			    live_connection_generation = connection_generation,
			    last_disconnected_native_connection_nonce = NULL,
			    live_route_generation = $5, host_live_scope_preserved = TRUE, updated_at = $3
			WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
			  AND connection_state = 'DISCONNECTED'
		`, attemptID, actor.PlayerID, now, nativeConnectionNonce, routeGeneration)
		if err != nil {
			return Snapshot{}, internal(err)
		}
		if command.RowsAffected() != 1 {
			return Snapshot{}, conflict("MATCH_HOST_ROSTER_REQUIRED", "The P2P authority has no disconnected frozen host seat.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, actor.PlayerID)
	}
	if state != AttemptProvisioning || !payloadInstalled || !hostDeadline.Valid || !hostDeadline.Time.After(queryNow) {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_PROVISIONING", "The P2P attempt is not waiting for its authority.", nil)
	}
	if s.p2p == nil {
		return Snapshot{}, conflict("P2P_TRANSPORT_UNAVAILABLE", "P2P transport is not available.", nil)
	}
	if _, err := s.p2p.StartManaged(ctx, toP2PActor(actor), roomID, hostToken); err != nil {
		return Snapshot{}, conflict("MATCH_P2P_TRANSPORT_NOT_READY", "The P2P transport is not ready to start.", nil)
	}
	now := s.now().UTC()
	deadline := now.Add(s.initialConnectionWindow())
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	command, err := tx.Exec(ctx, `
		UPDATE match_attempts SET state = 'CONNECTING', endpoint_host = $2,
		       endpoint_port = $3, route_generation = $4,
		       world_instance_id = $5, connection_deadline = $6,
		       authority_last_seen_at = $7, host_reconnect_deadline = NULL,
		       updated_at = $7
		WHERE id = $1 AND state = 'PROVISIONING' AND authority_id = $8
		  AND authority_session_id = $9 AND route_generation = $4
		  AND host_reconnect_deadline > $7
	`, attemptID, endpointHost, endpointPort, routeGeneration, worldInstanceID, deadline, now, actor.PlayerID, authoritySession)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		var latestHostNonce string
		_ = s.repository.pool.QueryRow(ctx, `
			SELECT COALESCE(live_native_connection_nonce, '')
			FROM match_attempt_roster
			WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
		`, attemptID, actor.PlayerID).Scan(&latestHostNonce)
		latest, latestErr := s.Get(ctx, lobbyID, actor.PlayerID)
		if latestErr == nil && latest.Attempt != nil &&
			(latest.Attempt.State == AttemptConnecting || latest.Attempt.State == AttemptRunning) &&
			latest.Attempt.RouteGeneration == routeGeneration &&
			latest.Attempt.EndpointHost == endpointHost && latest.Attempt.EndpointPort == endpointPort &&
			latest.Attempt.WorldInstanceID == worldInstanceID &&
			latestHostNonce == nativeConnectionNonce {
			return latest, nil
		}
		return Snapshot{}, conflict("MATCH_ATTEMPT_STATE_CONFLICT", "The match attempt changed while publishing authority readiness.", nil)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET state = 'CONNECTING', updated_at = $2 WHERE id = $1 AND current_attempt_id = $3`, lobbyID, now, attemptID); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_attempt_roster
		SET connection_state = 'CONNECTED', connected_at = COALESCE(connected_at, $3),
		    disconnected_at = NULL, live_native_connection_nonce = $4,
		    live_connection_generation = connection_generation,
		    last_disconnected_native_connection_nonce = NULL,
		    live_route_generation = $5, host_live_scope_preserved = TRUE, updated_at = $3
		WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
	`, attemptID, actor.PlayerID, now, nativeConnectionNonce, routeGeneration); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, actor.PlayerID)
}

func (s *Service) DedicatedAllocation(ctx context.Context, serverID, attemptID string) (AllocationResult, error) {
	return s.allocation(ctx, attemptID, serverID, false)
}

func (s *Service) P2PHostAllocation(ctx context.Context, actor Actor, attemptID string) (AllocationResult, error) {
	if err := requireActive(actor); err != nil {
		return AllocationResult{}, err
	}
	return s.allocation(ctx, attemptID, actor.PlayerID, true)
}

func (s *Service) allocation(ctx context.Context, attemptID, authorityID string, requireP2P bool) (AllocationResult, error) {
	var claims AllocationClaims
	var state AttemptState
	var deadline sql.NullTime
	var hosting HostingKind
	err := s.repository.pool.QueryRow(ctx, `
		SELECT attempt.lobby_id, attempt.hosting_kind, attempt.state,
		       attempt.authority_id, attempt.authority_session_id,
		       attempt.roster_revision, attempt.route_generation,
		       attempt.connection_deadline
		FROM match_attempts AS attempt WHERE attempt.id = $1
	`, attemptID).Scan(&claims.LobbyID, &hosting, &state, &claims.AuthorityID,
		&claims.AuthoritySessionID, &claims.RosterRevision, &claims.RouteGeneration, &deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return AllocationResult{}, notFound("MATCH_ATTEMPT_NOT_FOUND", "Match attempt not found.")
	}
	if err != nil {
		return AllocationResult{}, internal(err)
	}
	if claims.AuthorityID != authorityID || (requireP2P && hosting != HostingP2P) || (!requireP2P && hosting != HostingDedicated) {
		return AllocationResult{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the match attempt.")
	}
	if state != AttemptProvisioning && state != AttemptConnecting && state != AttemptRunning {
		return AllocationResult{}, conflict("MATCH_ATTEMPT_NOT_ACTIVE", "The match attempt is not active.", nil)
	}
	roster, err := s.repository.FrozenRoster(ctx, s.repository.pool, attemptID)
	if err != nil {
		return AllocationResult{}, internal(err)
	}
	claims.AttemptID = attemptID
	claims.HostingKind = hosting
	claims.Roster = roster
	claims.ConnectionWindow = int(s.initialConnectionWindow() / time.Second)
	if deadline.Valid {
		claims.ConnectionDeadline = deadline.Time.Unix()
	}
	token, expires, err := s.signer.SignAllocation(claims, 8*time.Hour)
	if err != nil {
		return AllocationResult{}, internal(err)
	}
	return AllocationResult{
		AttemptID: attemptID, Allocation: token, AdmissionKeyID: s.signer.KeyID(),
		AdmissionPublicKey: s.signer.PublicKeyBase64(), ExpiresAt: expires,
	}, nil
}

func (s *Service) DedicatedPayloadInstalled(ctx context.Context, serverID, attemptID, authoritySession, payloadVersion, gameBinarySHA256 string, routeGeneration int) (Snapshot, error) {
	return s.payloadInstalled(ctx, attemptID, serverID, authoritySession, HostingDedicated, payloadVersion, gameBinarySHA256, routeGeneration)
}

func (s *Service) P2PPayloadInstalled(ctx context.Context, actor Actor, attemptID, authoritySession, payloadVersion, gameBinarySHA256 string, routeGeneration int) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	return s.payloadInstalled(ctx, attemptID, actor.PlayerID, authoritySession, HostingP2P, payloadVersion, gameBinarySHA256, routeGeneration)
}

func (s *Service) payloadInstalled(ctx context.Context, attemptID, authorityID, authoritySession string, hosting HostingKind, payloadVersion, gameBinarySHA256 string, routeGeneration int) (Snapshot, error) {
	payloadVersion = strings.TrimSpace(payloadVersion)
	gameBinarySHA256 = strings.ToLower(strings.TrimSpace(gameBinarySHA256))
	if !lobbyLabelPattern.MatchString(payloadVersion) || !sha256HexPattern.MatchString(gameBinarySHA256) || routeGeneration < 1 {
		return Snapshot{}, invalid("Invalid Payload installation confirmation.", nil)
	}
	if gameBinarySHA256 != strings.ToLower(strings.TrimSpace(s.config.LockedGameSHA256)) {
		return Snapshot{}, conflict("STRICT_ROSTER_GAME_BINARY_MISMATCH", "The authority game binary is not the locked strict-roster build.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID string
	var state AttemptState
	var installedAt sql.NullTime
	var storedVersion, storedHash string
	var currentRoute, storedRoute int
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, state, payload_installed_at,
		       COALESCE(payload_version, ''), COALESCE(game_binary_sha256, ''),
		       route_generation, COALESCE(payload_route_generation, 0)
		FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND hosting_kind = $4
		FOR UPDATE
	`, attemptID, authorityID, authoritySession, hosting).Scan(
		&lobbyID, &state, &installedAt, &storedVersion, &storedHash,
		&currentRoute, &storedRoute,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SESSION_REQUIRED", "The authority session does not match this attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if state != AttemptProvisioning && state != AttemptConnecting && state != AttemptRunning {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_ACTIVE", "Payload installation can only be confirmed for an active attempt.", nil)
	}
	if installedAt.Valid {
		if storedVersion != payloadVersion || storedHash != gameBinarySHA256 {
			return Snapshot{}, conflict("MATCH_PAYLOAD_CONFIRMATION_CONFLICT", "The attempt was confirmed by a different Payload or game binary.", nil)
		}
		if routeGeneration != currentRoute {
			return Snapshot{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The Payload confirmation does not match the active route generation.", map[string]any{"route_generation": currentRoute})
		}
		if storedRoute != currentRoute {
			if _, err := tx.Exec(ctx, `UPDATE match_attempts SET payload_route_generation = $2, updated_at = $3 WHERE id = $1`, attemptID, currentRoute, now); err != nil {
				return Snapshot{}, internal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, "")
	}
	if state != AttemptProvisioning {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_PROVISIONING", "Payload installation can only be confirmed while provisioning.", nil)
	}
	if routeGeneration != currentRoute {
		return Snapshot{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The Payload confirmation does not match the active route generation.", map[string]any{"route_generation": currentRoute})
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_attempts
		SET payload_installed_at = $2, payload_version = $3,
		    game_binary_sha256 = $4, payload_route_generation = $5, updated_at = $2
		WHERE id = $1
	`, attemptID, now, payloadVersion, gameBinarySHA256, routeGeneration); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

func (s *Service) DedicatedAuthorityReady(ctx context.Context, serverID, attemptID, authoritySession, worldInstanceID, nativeConnectionNonce string) (Snapshot, error) {
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	nativeConnectionNonce = strings.TrimSpace(nativeConnectionNonce)
	if !worldInstancePattern.MatchString(worldInstanceID) {
		return Snapshot{}, invalid("Invalid dedicated world instance identity.", nil)
	}
	if !nativeConnectionNoncePattern.MatchString(nativeConnectionNonce) {
		return Snapshot{}, invalid("Invalid dedicated native connection nonce.", nil)
	}
	now := s.now().UTC()
	deadline := now.Add(s.initialConnectionWindow())
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID string
	err = tx.QueryRow(ctx, `
		UPDATE match_attempts SET state = 'CONNECTING', connection_deadline = $3,
		       authority_last_seen_at = $2, world_instance_id = $6, updated_at = $2
		WHERE id = $1 AND authority_id = $4 AND hosting_kind = 'DEDICATED'
		  AND state = 'PROVISIONING' AND authority_session_id = $5
		  AND payload_installed_at IS NOT NULL
		  AND payload_route_generation = route_generation
		RETURNING lobby_id
	`, attemptID, now, deadline, serverID, authoritySession, worldInstanceID).Scan(&lobbyID)
	if errors.Is(err, pgx.ErrNoRows) {
		var state AttemptState
		var storedWorldInstanceID string
		lookupErr := tx.QueryRow(ctx, `
			SELECT lobby_id, state, COALESCE(world_instance_id, '') FROM match_attempts
			WHERE id = $1 AND authority_id = $2 AND hosting_kind = 'DEDICATED'
			  AND authority_session_id = $3
		`, attemptID, serverID, authoritySession).Scan(&lobbyID, &state, &storedWorldInstanceID)
		// Dedicated authority readiness identifies the native server process, while
		// its player seats remain MEMBER rows and bind disconnects to each consumed
		// admission nonce. Only a P2P player HOST has a live roster nonce.
		if lookupErr == nil && (state == AttemptConnecting || state == AttemptRunning) && storedWorldInstanceID == worldInstanceID {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Snapshot{}, internal(commitErr)
			}
			return s.Get(ctx, lobbyID, "")
		}
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return Snapshot{}, internal(lookupErr)
		}
		return Snapshot{}, conflict("MATCH_ATTEMPT_STATE_CONFLICT", "The dedicated attempt is not waiting for this authority.", nil)
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET state = 'CONNECTING', updated_at = $2 WHERE id = $1`, lobbyID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

func (s *Service) JoinGrant(ctx context.Context, actor Actor, attemptID string) (GrantResult, error) {
	return s.joinGrant(ctx, actor, attemptID, "")
}

// JoinGrantWithIdempotency binds a network retry to one join intent. A new
// key is required for an intentional replacement connection; retrying the
// same key returns the original unconsumed grant without advancing the seat
// generation.
func (s *Service) JoinGrantWithIdempotency(ctx context.Context, actor Actor, attemptID, idempotencyKey string) (GrantResult, error) {
	return s.joinGrant(ctx, actor, attemptID, idempotencyKey)
}

func (s *Service) joinGrant(ctx context.Context, actor Actor, attemptID, idempotencyKey string) (GrantResult, error) {
	if err := requireActive(actor); err != nil {
		return GrantResult{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey != "" && !lobbyIdempotencyPattern.MatchString(idempotencyKey) {
		return GrantResult{}, invalid("Invalid join idempotency key.", nil)
	}
	var intentHash []byte
	if idempotencyKey != "" {
		hash := sha256.Sum256([]byte(attemptID + "\x00" + actor.PlayerID + "\x00" + idempotencyKey))
		intentHash = hash[:]
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return GrantResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var claims JoinGrantClaims
	var state AttemptState
	var endpointHost string
	var endpointPort int
	var priorGrantCount int
	var roomRole string
	var connectionState string
	var liveConnectionGeneration int
	var liveRouteGeneration int
	var worldInstanceID string
	var payloadRouteReady bool
	var hostReconnecting bool
	var authorityFresh bool
	err = tx.QueryRow(ctx, `
		SELECT attempt.lobby_id, attempt.hosting_kind, attempt.state,
		       COALESCE(attempt.authority_id, ''), attempt.authority_session_id,
		       COALESCE(attempt.world_instance_id, ''), attempt.roster_revision, attempt.route_generation,
		       COALESCE(attempt.endpoint_host, ''), COALESCE(attempt.endpoint_port, 0),
		       roster.platform_id, roster.room_role, roster.team_id, roster.team_slot,
		       roster.logical_slot, roster.connection_generation,
		       roster.connection_state, COALESCE(roster.live_connection_generation, 0),
		       COALESCE(roster.live_route_generation, 0),
		       COALESCE(attempt.payload_route_generation = attempt.route_generation, FALSE),
		       attempt.host_reconnect_deadline IS NOT NULL,
		       COALESCE(attempt.authority_last_seen_at > $3, FALSE),
		       (SELECT COUNT(*) FROM match_admission_grants AS admission
		        WHERE admission.attempt_id = roster.attempt_id AND admission.player_id = roster.player_id)
		FROM match_attempts AS attempt
		JOIN match_attempt_roster AS roster ON roster.attempt_id = attempt.id
		WHERE attempt.id = $1 AND roster.player_id = $2
		FOR UPDATE OF attempt, roster
	`, attemptID, actor.PlayerID, now.Add(-authorityHeartbeatStale)).Scan(
		&claims.LobbyID, &claims.HostingKind, &state, &claims.AuthorityID,
		&claims.AuthoritySessionID, &worldInstanceID, &claims.RosterRevision, &claims.RouteGeneration,
		&endpointHost, &endpointPort, &claims.PlatformID, &roomRole, &claims.TeamID,
		&claims.TeamSlot, &claims.LogicalSlot, &claims.ConnectionGeneration,
		&connectionState, &liveConnectionGeneration, &liveRouteGeneration,
		&payloadRouteReady, &hostReconnecting, &authorityFresh,
		&priorGrantCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GrantResult{}, forbidden("MATCH_ROSTER_MEMBERSHIP_REQUIRED", "The authenticated player is not in the frozen match roster.")
	}
	if err != nil {
		return GrantResult{}, internal(err)
	}
	if state != AttemptConnecting && state != AttemptRunning {
		return GrantResult{}, conflict("MATCH_ATTEMPT_NOT_CONNECTABLE", "The match authority is not ready for connections.", nil)
	}
	if hostReconnecting {
		return GrantResult{}, conflict("MATCH_AUTHORITY_RECONNECTING", "The P2P authority is inside its continuity recovery window.", nil)
	}
	if !authorityFresh {
		return GrantResult{}, conflict("MATCH_AUTHORITY_UNAVAILABLE", "The match authority heartbeat is stale.", nil)
	}
	if !payloadRouteReady {
		return GrantResult{}, conflict("MATCH_AUTHORITY_ROUTE_REFRESHING", "The match authority is refreshing admission for the current route generation.", nil)
	}
	if worldInstanceID == "" {
		return GrantResult{}, conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The match authority has not published a native world instance.", nil)
	}
	claims.WorldInstanceID = worldInstanceID
	if claims.HostingKind == HostingP2P && roomRole == "HOST" {
		return GrantResult{}, forbidden("MATCH_P2P_HOST_USES_ALLOCATION", "The local P2P host is admitted only through its signed allocation.")
	}
	if endpointHost == "" || endpointPort == 0 {
		return GrantResult{}, conflict("MATCH_AUTHORITY_ENDPOINT_UNAVAILABLE", "The match authority endpoint is unavailable.", nil)
	}
	if idempotencyKey != "" {
		var priorJTI string
		var priorGeneration, priorRoute int
		var issuedAt, expiresAt time.Time
		var consumedAt, revokedAt sql.NullTime
		lookupErr := tx.QueryRow(ctx, `
			SELECT jti, connection_generation, route_generation,
			       issued_at, expires_at, consumed_at, revoked_at
			FROM match_admission_grants
			WHERE attempt_id = $1 AND player_id = $2 AND idempotency_key = $3
			FOR UPDATE
		`, attemptID, actor.PlayerID, idempotencyKey).Scan(
			&priorJTI, &priorGeneration, &priorRoute, &issuedAt, &expiresAt,
			&consumedAt, &revokedAt,
		)
		if lookupErr == nil {
			if priorRoute != claims.RouteGeneration || priorGeneration != claims.ConnectionGeneration {
				return GrantResult{}, conflict("MATCH_JOIN_INTENT_STALE", "The join intent belongs to an older route or seat generation; use a new idempotency key.", nil)
			}
			if intentHash != nil {
				var storedHash []byte
				if err := tx.QueryRow(ctx, `SELECT COALESCE(idempotency_request_hash, ''::bytea) FROM match_admission_grants WHERE jti = $1`, priorJTI).Scan(&storedHash); err != nil {
					return GrantResult{}, internal(err)
				}
				if subtle.ConstantTimeCompare(storedHash, intentHash) != 1 {
					return GrantResult{}, conflict("MATCH_JOIN_IDEMPOTENCY_CONFLICT", "The idempotency key was already used for a different join intent.", nil)
				}
			}
			if consumedAt.Valid || revokedAt.Valid || !expiresAt.After(now) {
				return GrantResult{}, conflict("MATCH_JOIN_INTENT_COMPLETED", "The join intent already reached a terminal result; use a new idempotency key to reconnect.", nil)
			}
			claims.AttemptID = attemptID
			claims.PlayerID = actor.PlayerID
			claims.TokenID = priorJTI
			claims.ConnectionGeneration = priorGeneration
			claims.RouteGeneration = priorRoute
			token, signErr := s.signer.SignJoinGrantWindow(claims, issuedAt, expiresAt)
			if signErr != nil {
				return GrantResult{}, internal(signErr)
			}
			if err := tx.Commit(ctx); err != nil {
				return GrantResult{}, internal(err)
			}
			return GrantResult{
				AttemptID: attemptID, AuthoritySessionID: claims.AuthoritySessionID,
				WorldInstanceID: claims.WorldInstanceID, RosterRevision: claims.RosterRevision,
				RouteGeneration: claims.RouteGeneration, PlayerID: claims.PlayerID,
				GrantJTI:     priorJTI,
				EndpointHost: endpointHost, EndpointPort: endpointPort,
				Grant: token, ExpiresAt: expiresAt, ConnectionGeneration: priorGeneration,
			}, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return GrantResult{}, internal(lookupErr)
		}
	}
	if priorGrantCount > 0 && connectionState == "CONNECTED" &&
		liveConnectionGeneration == claims.ConnectionGeneration &&
		liveRouteGeneration == claims.RouteGeneration {
		return GrantResult{}, conflict("MATCH_CONNECTION_STILL_ACTIVE", "The previous connection must be released by the authority before a reconnect grant is issued.", nil)
	}
	if priorGrantCount > 0 {
		if liveConnectionGeneration == claims.ConnectionGeneration && liveRouteGeneration == claims.RouteGeneration {
			claims.ConnectionGeneration++
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempt_roster SET connection_generation = $3,
			       connection_state = 'CONNECTING', updated_at = $4
			WHERE attempt_id = $1 AND player_id = $2
		`, attemptID, actor.PlayerID, claims.ConnectionGeneration, now); err != nil {
			return GrantResult{}, internal(err)
		}
		if err := s.syncProjectionGenerations(ctx, tx, attemptID); err != nil {
			return GrantResult{}, internal(err)
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE match_attempt_roster SET connection_state = 'CONNECTING', updated_at = $3 WHERE attempt_id = $1 AND player_id = $2`, attemptID, actor.PlayerID, now); err != nil {
			return GrantResult{}, internal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE match_admission_grants SET revoked_at = $3 WHERE attempt_id = $1 AND player_id = $2 AND revoked_at IS NULL`, attemptID, actor.PlayerID, now); err != nil {
		return GrantResult{}, internal(err)
	}
	claims.AttemptID = attemptID
	claims.PlayerID = actor.PlayerID
	claims.TokenID = newAdmissionID("mj_")
	expires := now.Add(s.grantTTL())
	token, err := s.signer.SignJoinGrantWindow(claims, now, expires)
	if err != nil {
		return GrantResult{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO match_admission_grants (
			jti, attempt_id, player_id, connection_generation,
			route_generation, issued_at, expires_at, idempotency_key,
			idempotency_request_hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)
	`, claims.TokenID, attemptID, actor.PlayerID, claims.ConnectionGeneration,
		claims.RouteGeneration, now, expires, idempotencyKey, intentHash); err != nil {
		return GrantResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GrantResult{}, internal(err)
	}
	return GrantResult{
		AttemptID: attemptID, AuthoritySessionID: claims.AuthoritySessionID,
		WorldInstanceID: claims.WorldInstanceID, RosterRevision: claims.RosterRevision,
		RouteGeneration: claims.RouteGeneration, PlayerID: claims.PlayerID,
		GrantJTI:     claims.TokenID,
		EndpointHost: endpointHost, EndpointPort: endpointPort,
		Grant: token, ExpiresAt: expires, ConnectionGeneration: claims.ConnectionGeneration,
	}, nil
}

// AuthorityAdmissions returns grants which Meta has issued to frozen roster
// members but which the scoped authority Payload has not acknowledged staging.
// The bearer token is reconstructed from persisted claims and is never stored.
func (s *Service) AuthorityAdmissions(ctx context.Context, authorityID, authoritySession, attemptID string) (AuthorityAdmissionList, error) {
	authorityID = strings.TrimSpace(authorityID)
	authoritySession = strings.TrimSpace(authoritySession)
	if authorityID == "" || authoritySession == "" {
		return AuthorityAdmissionList{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "A live authority session is required.")
	}
	now := s.now().UTC()
	var scoped bool
	if err := s.repository.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM match_attempts
			WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
			  AND state IN ('CONNECTING', 'RUNNING')
		)
	`, attemptID, authorityID, authoritySession).Scan(&scoped); err != nil {
		return AuthorityAdmissionList{}, internal(err)
	}
	if !scoped {
		return AuthorityAdmissionList{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the active match attempt.")
	}
	rows, err := s.repository.pool.Query(ctx, `
		SELECT attempt.lobby_id, attempt.hosting_kind, attempt.authority_id,
		       attempt.authority_session_id, attempt.roster_revision,
		       COALESCE(attempt.world_instance_id, ''),
		       admission.jti, admission.player_id, roster.platform_id,
		       roster.team_id, roster.team_slot, roster.logical_slot,
		       admission.connection_generation, admission.route_generation,
		       admission.issued_at, admission.expires_at
		FROM match_admission_grants AS admission
		JOIN match_attempts AS attempt ON attempt.id = admission.attempt_id
		JOIN match_attempt_roster AS roster
		  ON roster.attempt_id = admission.attempt_id
		 AND roster.player_id = admission.player_id
		WHERE admission.attempt_id = $1
		  AND attempt.authority_id = $2
		  AND attempt.authority_session_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING')
		  AND COALESCE(attempt.world_instance_id, '') <> ''
		  AND attempt.payload_route_generation = attempt.route_generation
		  AND admission.route_generation = attempt.route_generation
		  AND admission.connection_generation = roster.connection_generation
		  AND admission.delivered_at IS NULL
		  AND admission.consumed_at IS NULL
		  AND admission.revoked_at IS NULL
		  AND admission.expires_at > $4
		ORDER BY admission.issued_at, admission.jti
	`, attemptID, authorityID, authoritySession, now)
	if err != nil {
		return AuthorityAdmissionList{}, internal(err)
	}
	defer rows.Close()
	result := AuthorityAdmissionList{Items: make([]AuthorityAdmission, 0)}
	for rows.Next() {
		var claims JoinGrantClaims
		var issuedAt, expiresAt time.Time
		if err := rows.Scan(
			&claims.LobbyID, &claims.HostingKind, &claims.AuthorityID,
			&claims.AuthoritySessionID, &claims.RosterRevision, &claims.WorldInstanceID,
			&claims.TokenID, &claims.PlayerID, &claims.PlatformID,
			&claims.TeamID, &claims.TeamSlot, &claims.LogicalSlot,
			&claims.ConnectionGeneration, &claims.RouteGeneration,
			&issuedAt, &expiresAt,
		); err != nil {
			return AuthorityAdmissionList{}, internal(err)
		}
		claims.AttemptID = attemptID
		token, err := s.signer.SignJoinGrantWindow(claims, issuedAt, expiresAt)
		if err != nil {
			return AuthorityAdmissionList{}, internal(err)
		}
		result.Items = append(result.Items, AuthorityAdmission{
			AttemptID: attemptID, PlayerID: claims.PlayerID,
			PlatformID: claims.PlatformID, GrantJTI: claims.TokenID,
			JoinGrant: token, ConnectionGeneration: claims.ConnectionGeneration,
			RouteGeneration: claims.RouteGeneration, ExpiresAt: expiresAt,
		})
	}
	if err := rows.Err(); err != nil {
		return AuthorityAdmissionList{}, internal(err)
	}
	return result, nil
}

func (s *Service) P2PAuthorityAdmissions(ctx context.Context, actor Actor, authoritySession, attemptID string) (AuthorityAdmissionList, error) {
	if err := requireActive(actor); err != nil {
		return AuthorityAdmissionList{}, err
	}
	return s.AuthorityAdmissions(ctx, actor.PlayerID, authoritySession, attemptID)
}

// MarkAdmissionDelivered is called only after the authority Payload verifies
// and stages the grant. Retrying the acknowledgement is idempotent.
func (s *Service) MarkAdmissionDelivered(ctx context.Context, authorityID, authoritySession, attemptID, grantJTI string) (GrantDeliveryStatus, error) {
	grantJTI = strings.TrimSpace(grantJTI)
	if grantJTI == "" {
		return GrantDeliveryStatus{}, invalid("Invalid join grant identity.", nil)
	}
	now := s.now().UTC()
	var status GrantDeliveryStatus
	var deliveredAt sql.NullTime
	err := s.repository.pool.QueryRow(ctx, `
		UPDATE match_admission_grants AS admission
		SET delivered_at = COALESCE(admission.delivered_at, $5)
		FROM match_attempts AS attempt, match_attempt_roster AS roster
		WHERE admission.jti = $4 AND admission.attempt_id = $1
		  AND attempt.id = admission.attempt_id
		  AND roster.attempt_id = admission.attempt_id
		  AND roster.player_id = admission.player_id
		  AND attempt.authority_id = $2
		  AND attempt.authority_session_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING')
		  AND COALESCE(attempt.world_instance_id, '') <> ''
		  AND attempt.payload_route_generation = attempt.route_generation
		  AND admission.route_generation = attempt.route_generation
		  AND admission.connection_generation = roster.connection_generation
		  AND admission.consumed_at IS NULL
		  AND admission.revoked_at IS NULL
		  AND admission.expires_at > $5
		RETURNING admission.attempt_id, admission.jti,
		          admission.delivered_at, admission.expires_at
	`, attemptID, authorityID, authoritySession, grantJTI, now).Scan(
		&status.AttemptID, &status.GrantJTI, &deliveredAt, &status.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GrantDeliveryStatus{}, conflict("MATCH_JOIN_GRANT_NOT_DELIVERABLE", "The join grant is expired, revoked, consumed, or outside this authority session.", nil)
	}
	if err != nil {
		return GrantDeliveryStatus{}, internal(err)
	}
	status.Delivered = deliveredAt.Valid
	if deliveredAt.Valid {
		value := deliveredAt.Time
		status.DeliveredAt = &value
	}
	return status, nil
}

func (s *Service) P2PMarkAdmissionDelivered(ctx context.Context, actor Actor, authoritySession, attemptID, grantJTI string) (GrantDeliveryStatus, error) {
	if err := requireActive(actor); err != nil {
		return GrantDeliveryStatus{}, err
	}
	return s.MarkAdmissionDelivered(ctx, actor.PlayerID, authoritySession, attemptID, grantJTI)
}

func (s *Service) GrantDelivery(ctx context.Context, actor Actor, attemptID, grantJTI string) (GrantDeliveryStatus, error) {
	if err := requireActive(actor); err != nil {
		return GrantDeliveryStatus{}, err
	}
	var status GrantDeliveryStatus
	var deliveredAt, revokedAt, consumedAt sql.NullTime
	err := s.repository.pool.QueryRow(ctx, `
		SELECT admission.attempt_id, admission.jti, admission.delivered_at,
		       admission.expires_at, admission.revoked_at, admission.consumed_at
		FROM match_admission_grants AS admission
		JOIN match_attempts AS attempt ON attempt.id = admission.attempt_id
		JOIN match_attempt_roster AS roster
		  ON roster.attempt_id = admission.attempt_id
		 AND roster.player_id = admission.player_id
		WHERE admission.attempt_id = $1 AND admission.jti = $2
		  AND admission.player_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING')
		  AND COALESCE(attempt.world_instance_id, '') <> ''
		  AND attempt.payload_route_generation = attempt.route_generation
		  AND admission.route_generation = attempt.route_generation
		  AND admission.connection_generation = roster.connection_generation
	`, attemptID, strings.TrimSpace(grantJTI), actor.PlayerID).Scan(
		&status.AttemptID, &status.GrantJTI, &deliveredAt,
		&status.ExpiresAt, &revokedAt, &consumedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GrantDeliveryStatus{}, notFound("MATCH_JOIN_GRANT_NOT_FOUND", "Join grant not found.")
	}
	if err != nil {
		return GrantDeliveryStatus{}, internal(err)
	}
	if revokedAt.Valid || consumedAt.Valid || !status.ExpiresAt.After(s.now().UTC()) {
		return GrantDeliveryStatus{}, conflict("MATCH_JOIN_GRANT_INACTIVE", "The join grant is no longer active.", nil)
	}
	status.Delivered = deliveredAt.Valid
	if deliveredAt.Valid {
		value := deliveredAt.Time
		status.DeliveredAt = &value
	}
	return status, nil
}

// admissionReservationTTL is intentionally shorter than the grant lifetime:
// native authentication gets a bounded handoff window, while a lost native
// connection cannot leave a roster seat reserved until the grant expires.
func (s *Service) admissionReservationTTL() time.Duration {
	ttl := s.grantTTL() / 3
	if ttl > 10*time.Second {
		ttl = 10 * time.Second
	}
	if ttl < 3*time.Second {
		ttl = 3 * time.Second
	}
	return ttl
}

// ReserveAdmission is the backend linearization point after the native side
// has authenticated a platform identity.  It locks the attempt, roster seat,
// and grant together, but deliberately leaves the roster CONNECTING.
func (s *Service) ReserveAdmission(
	ctx context.Context,
	authorityID, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce string,
	generation int,
) (AdmissionReservation, error) {
	authorityID = strings.TrimSpace(authorityID)
	authoritySession = strings.TrimSpace(authoritySession)
	attemptID = strings.TrimSpace(attemptID)
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	playerID = strings.TrimSpace(playerID)
	grantJTI = strings.TrimSpace(grantJTI)
	nativeConnectionNonce = strings.TrimSpace(nativeConnectionNonce)
	if authorityID == "" || authoritySession == "" || attemptID == "" || playerID == "" || grantJTI == "" ||
		!worldInstancePattern.MatchString(worldInstanceID) || !nativeConnectionNoncePattern.MatchString(nativeConnectionNonce) || generation < 1 {
		return AdmissionReservation{}, invalid("Invalid admission reservation identity.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdmissionReservation{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var routeGeneration, rosterGeneration, grantRouteGeneration, storedGeneration int
	var storedWorld, connectionState string
	var grantExpires time.Time
	var deliveredAt, consumedAt, revokedAt sql.NullTime
	var reservedAt, reservationExpires sql.NullTime
	var reservedAuthority, reservedSession, reservedWorld, reservationNonce string
	err = tx.QueryRow(ctx, `
		SELECT attempt.route_generation, COALESCE(attempt.world_instance_id, ''),
		       roster.connection_state, roster.connection_generation,
		       admission.connection_generation, admission.route_generation,
		       admission.expires_at, admission.delivered_at,
		       admission.consumed_at, admission.revoked_at, admission.reserved_at,
		       admission.reservation_expires_at,
		       COALESCE(admission.reservation_authority_id, ''),
		       COALESCE(admission.reservation_authority_session_id, ''),
		       COALESCE(admission.reservation_world_instance_id, ''),
		       COALESCE(admission.reservation_nonce, '')
		FROM match_attempts AS attempt
		JOIN match_attempt_roster AS roster ON roster.attempt_id = attempt.id AND roster.player_id = $4
		JOIN match_admission_grants AS admission
		  ON admission.attempt_id = attempt.id AND admission.player_id = roster.player_id
		 AND admission.jti = $5
		WHERE attempt.id = $1 AND attempt.authority_id = $2
		  AND attempt.authority_session_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE OF attempt, roster, admission
	`, attemptID, authorityID, authoritySession, playerID, grantJTI).Scan(
		&routeGeneration, &storedWorld, &connectionState, &rosterGeneration,
		&storedGeneration, &grantRouteGeneration,
		&grantExpires, &deliveredAt, &consumedAt, &revokedAt,
		&reservedAt, &reservationExpires, &reservedAuthority, &reservedSession, &reservedWorld,
		&reservationNonce,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdmissionReservation{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "The authority session or admission grant is not active for this attempt.")
	}
	if err != nil {
		return AdmissionReservation{}, internal(err)
	}
	if storedWorld == "" {
		return AdmissionReservation{}, conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The attempt has no persisted native world identity.", nil)
	}
	if storedWorld != worldInstanceID {
		return AdmissionReservation{}, conflict("MATCH_WORLD_INSTANCE_CONFLICT", "The admission belongs to a different world instance.", nil)
	}
	if consumedAt.Valid || revokedAt.Valid || !grantExpires.After(now) || !deliveredAt.Valid {
		return AdmissionReservation{}, conflict("MATCH_JOIN_GRANT_NOT_RESERVABLE", "The join grant is not delivered, active, or current.", nil)
	}
	if rosterGeneration != generation || storedGeneration != generation {
		return AdmissionReservation{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The admission generation is stale.", nil)
	}
	if grantRouteGeneration != routeGeneration {
		return AdmissionReservation{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The admission route generation is stale.", nil)
	}
	if connectionState == "CONNECTED" {
		return AdmissionReservation{}, conflict("MATCH_CONNECTION_ALREADY_ACTIVE", "The roster seat already has an active connection.", nil)
	}
	if reservedAt.Valid && reservationExpires.Valid && reservationExpires.Time.After(now) {
		if reservedAuthority != authorityID || reservedSession != authoritySession || reservedWorld != worldInstanceID || reservationNonce != nativeConnectionNonce {
			return AdmissionReservation{}, conflict("MATCH_ADMISSION_RESERVED", "The admission is reserved by another authority session.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return AdmissionReservation{}, internal(err)
		}
		return AdmissionReservation{
			AttemptID: attemptID, GrantJTI: grantJTI, PlayerID: playerID,
			WorldInstanceID: worldInstanceID, NativeConnectionNonce: nativeConnectionNonce,
			ConnectionGeneration: generation, RouteGeneration: routeGeneration,
			ReservedUntil: reservationExpires.Time,
		}, nil
	}
	reservedUntil := now.Add(s.admissionReservationTTL())
	if grantExpires.Before(reservedUntil) {
		reservedUntil = grantExpires
	}
	if !reservedUntil.After(now) {
		return AdmissionReservation{}, conflict("MATCH_JOIN_GRANT_NOT_RESERVABLE", "The join grant expires before a native admission reservation can be established.", nil)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_admission_grants
		SET reserved_at = $2, reservation_expires_at = $3,
		    reservation_authority_id = $4, reservation_authority_session_id = $5,
		    reservation_world_instance_id = $6, reservation_nonce = $7
		WHERE jti = $1 AND consumed_at IS NULL AND revoked_at IS NULL
	`, grantJTI, now, reservedUntil, authorityID, authoritySession, worldInstanceID, nativeConnectionNonce); err != nil {
		return AdmissionReservation{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AdmissionReservation{}, internal(err)
	}
	return AdmissionReservation{
		AttemptID: attemptID, GrantJTI: grantJTI, PlayerID: playerID,
		WorldInstanceID: worldInstanceID, NativeConnectionNonce: nativeConnectionNonce,
		ConnectionGeneration: generation, RouteGeneration: routeGeneration,
		ReservedUntil: reservedUntil,
	}, nil
}

func (s *Service) P2PReserveAdmission(ctx context.Context, actor Actor, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce string, generation int) (AdmissionReservation, error) {
	if err := requireActive(actor); err != nil {
		return AdmissionReservation{}, err
	}
	return s.ReserveAdmission(ctx, actor.PlayerID, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce, generation)
}

// ReleaseAdmission revokes a reserved grant and returns a still-connecting
// seat to DISCONNECTED.  It is idempotent after the grant has already been
// revoked or consumed, and never changes a connected roster member.
func (s *Service) ReleaseAdmission(
	ctx context.Context,
	authorityID, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce string,
	generation int,
) error {
	authorityID = strings.TrimSpace(authorityID)
	authoritySession = strings.TrimSpace(authoritySession)
	attemptID = strings.TrimSpace(attemptID)
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	playerID = strings.TrimSpace(playerID)
	grantJTI = strings.TrimSpace(grantJTI)
	nativeConnectionNonce = strings.TrimSpace(nativeConnectionNonce)
	if authorityID == "" || authoritySession == "" || attemptID == "" || playerID == "" || grantJTI == "" ||
		!worldInstancePattern.MatchString(worldInstanceID) || !nativeConnectionNoncePattern.MatchString(nativeConnectionNonce) || generation < 1 {
		return invalid("Invalid admission release identity.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var storedWorld, state, reservationAuthority, reservationSession, reservationWorld, connectionState string
	var routeGeneration, storedGeneration int
	var consumedAt, revokedAt, reservedAt, reservationExpires sql.NullTime
	var reservationNonce, consumedNonce string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(attempt.world_instance_id, ''), attempt.state,
		       attempt.route_generation, roster.connection_state,
		       admission.connection_generation, admission.consumed_at, admission.revoked_at,
		       admission.reserved_at, admission.reservation_expires_at,
		       COALESCE(admission.reservation_authority_id, ''),
		       COALESCE(admission.reservation_authority_session_id, ''),
		       COALESCE(admission.reservation_world_instance_id, ''),
		       COALESCE(admission.reservation_nonce, ''),
		       COALESCE(admission.consumed_connection_nonce, '')
		FROM match_attempts AS attempt
		JOIN match_attempt_roster AS roster ON roster.attempt_id = attempt.id AND roster.player_id = $4
		JOIN match_admission_grants AS admission
		  ON admission.attempt_id = attempt.id AND admission.player_id = roster.player_id
		 AND admission.jti = $5
		WHERE attempt.id = $1 AND attempt.authority_id = $2
		  AND attempt.authority_session_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING', 'COMPLETED', 'ABORTED')
		FOR UPDATE OF attempt, roster, admission
	`, attemptID, authorityID, authoritySession, playerID, grantJTI).Scan(
		&storedWorld, &state, &routeGeneration, &connectionState, &storedGeneration,
		&consumedAt, &revokedAt, &reservedAt, &reservationExpires,
		&reservationAuthority, &reservationSession, &reservationWorld,
		&reservationNonce, &consumedNonce,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "The authority session or admission grant is not scoped to this attempt.")
	}
	if err != nil {
		return internal(err)
	}
	if storedWorld == "" {
		return conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The attempt has no persisted native world identity.", nil)
	}
	if storedWorld != worldInstanceID {
		return conflict("MATCH_WORLD_INSTANCE_CONFLICT", "The admission belongs to a different world instance.", nil)
	}
	if consumedAt.Valid {
		if consumedNonce != nativeConnectionNonce {
			return conflict("MATCH_JOIN_GRANT_NOT_CONSUMABLE", "The admission was consumed by a different native connection.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return internal(err)
		}
		return nil
	}
	if revokedAt.Valid && !reservedAt.Valid {
		if err := tx.Commit(ctx); err != nil {
			return internal(err)
		}
		return nil
	}
	if reservedAt.Valid && reservationExpires.Valid && reservationExpires.Time.After(now) &&
		(reservationAuthority != authorityID || reservationSession != authoritySession || reservationWorld != worldInstanceID || reservationNonce != nativeConnectionNonce) {
		return conflict("MATCH_ADMISSION_RESERVED", "The admission is reserved by another authority session.", nil)
	}
	if storedGeneration != generation || routeGeneration < 1 {
		return conflict("MATCH_CONNECTION_GENERATION_STALE", "The admission generation is stale.", nil)
	}
	if connectionState == "CONNECTED" {
		return conflict("MATCH_CONNECTION_ALREADY_ACTIVE", "A connected roster member cannot be released through an admission failure.", nil)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_admission_grants
		SET revoked_at = COALESCE(revoked_at, $2), reserved_at = NULL,
		    reservation_expires_at = NULL, reservation_authority_id = NULL,
		    reservation_authority_session_id = NULL, reservation_world_instance_id = NULL,
		    reservation_nonce = NULL
		WHERE jti = $1
	`, grantJTI, now); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_attempt_roster
		SET connection_state = 'DISCONNECTED', disconnected_at = COALESCE(disconnected_at, $3), updated_at = $3
		WHERE attempt_id = $1 AND player_id = $2 AND connection_generation = $4
		  AND connection_state IN ('CONNECTING', 'DISCONNECTED')
	`, attemptID, playerID, now, generation); err != nil {
		return internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return internal(err)
	}
	_ = state
	return nil
}

func (s *Service) P2PReleaseAdmission(ctx context.Context, actor Actor, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce string, generation int) error {
	if err := requireActive(actor); err != nil {
		return err
	}
	return s.ReleaseAdmission(ctx, actor.PlayerID, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce, generation)
}

// ConfirmConnected is the only backend operation that consumes an admission
// grant and may promote a roster seat to CONNECTED/RUNNING.
func (s *Service) ConfirmConnected(ctx context.Context, authorityID, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce string, generation int) (Snapshot, error) {
	authorityID = strings.TrimSpace(authorityID)
	authoritySession = strings.TrimSpace(authoritySession)
	attemptID = strings.TrimSpace(attemptID)
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	playerID = strings.TrimSpace(playerID)
	grantJTI = strings.TrimSpace(grantJTI)
	nativeConnectionNonce = strings.TrimSpace(nativeConnectionNonce)
	if generation < 1 || grantJTI == "" || authorityID == "" || authoritySession == "" || attemptID == "" || playerID == "" ||
		!worldInstancePattern.MatchString(worldInstanceID) || !nativeConnectionNoncePattern.MatchString(nativeConnectionNonce) {
		return Snapshot{}, invalid("Invalid connection generation.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID, storedWorldInstanceID string
	var routeGeneration int
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, route_generation, COALESCE(world_instance_id, '') FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(&lobbyID, &routeGeneration, &storedWorldInstanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the active match attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if storedWorldInstanceID == "" {
		return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The attempt has no persisted native world identity.", nil)
	}
	if storedWorldInstanceID != worldInstanceID {
		return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_CONFLICT", "The connection belongs to a different world instance.", nil)
	}
	grantCommand, err := tx.Exec(ctx, `
		UPDATE match_admission_grants
		SET consumed_at = $6, consumed_connection_nonce = $10, reservation_nonce = NULL, reserved_at = NULL,
		    reservation_expires_at = NULL, reservation_authority_id = NULL,
		    reservation_authority_session_id = NULL, reservation_world_instance_id = NULL
		WHERE jti = $1 AND attempt_id = $2 AND player_id = $3
		  AND connection_generation = $4 AND route_generation = $5
		  AND delivered_at IS NOT NULL
		  AND revoked_at IS NULL AND consumed_at IS NULL AND expires_at > $6
		  AND reserved_at IS NOT NULL AND reservation_expires_at > $6
		  AND reservation_authority_id = $7
		  AND reservation_authority_session_id = $8
		  AND reservation_world_instance_id = $9
		  AND reservation_nonce = $10
	`, grantJTI, attemptID, playerID, generation, routeGeneration, now, authorityID, authoritySession, worldInstanceID, nativeConnectionNonce)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if grantCommand.RowsAffected() != 1 {
		var repeated bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM match_admission_grants AS admission
				JOIN match_attempt_roster AS roster
				  ON roster.attempt_id = admission.attempt_id AND roster.player_id = admission.player_id
				WHERE admission.jti = $1 AND admission.attempt_id = $2 AND admission.player_id = $3
				  AND admission.connection_generation = $4 AND admission.route_generation = $5
			  AND admission.consumed_at IS NOT NULL
			  AND admission.consumed_connection_nonce = $6
			  AND roster.connection_generation = $4
			  AND COALESCE(roster.live_connection_generation, 0) = $4
			  AND COALESCE(roster.live_route_generation, 0) = $5
			  AND roster.connection_state = 'CONNECTED'
			)
		`, grantJTI, attemptID, playerID, generation, routeGeneration, nativeConnectionNonce).Scan(&repeated); err != nil {
			return Snapshot{}, internal(err)
		}
		if !repeated {
			return Snapshot{}, conflict("MATCH_JOIN_GRANT_NOT_CONSUMABLE", "The join grant is expired, revoked, replayed, or does not match the current route generation.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, "")
	}
	command, err := tx.Exec(ctx, `
		UPDATE match_attempt_roster SET connection_state = 'CONNECTED',
		       connected_at = COALESCE(connected_at, $4), disconnected_at = NULL,
		       live_native_connection_nonce = $5,
		       live_connection_generation = connection_generation,
		       last_disconnected_native_connection_nonce = NULL,
		       live_route_generation = $6, updated_at = $4
		WHERE attempt_id = $1 AND player_id = $2 AND connection_generation = $3
	`, attemptID, playerID, generation, now, nativeConnectionNonce, routeGeneration)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The connection grant generation is stale or the player is not reserved.", nil)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE meta_match_players AS projected
		SET connected_at = COALESCE(projected.connected_at, $3), disconnected_at = NULL,
		    connection_generation = $4
		FROM meta_matches AS match
		WHERE match.match_attempt_id = $1 AND projected.match_id = match.id
		  AND projected.player_id = $2
	`, attemptID, playerID, now, generation); err != nil {
		return Snapshot{}, internal(err)
	}
	var missing int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM match_attempt_roster
		WHERE attempt_id = $1
		  AND (connection_state <> 'CONNECTED'
		       OR COALESCE(live_connection_generation, 0) <> connection_generation
		       OR COALESCE(live_native_connection_nonce, '') = ''
		       OR (room_role = 'MEMBER' AND COALESCE(live_route_generation, 0) <> (
				SELECT route_generation FROM match_attempts WHERE id = $1
			)))
	`, attemptID).Scan(&missing); err != nil {
		return Snapshot{}, internal(err)
	}
	if missing == 0 {
		if err := s.markAttemptRunning(ctx, tx, attemptID, lobbyID, authorityID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

func (s *Service) markAttemptRunning(ctx context.Context, tx pgx.Tx, attemptID, lobbyID, authorityID string, now time.Time) error {
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE match_attempts SET state = 'RUNNING', started_at = COALESCE(started_at, $2), updated_at = $2 WHERE id = $1`, []any{attemptID, now}},
		{`UPDATE match_lobbies SET state = 'RUNNING', updated_at = $2 WHERE id = $1`, []any{lobbyID, now}},
		{`UPDATE meta_matches SET state = 'RUNNING', started_at = COALESCE(started_at, $2), updated_at = $2 WHERE match_attempt_id = $1`, []any{attemptID, now}},
		{`UPDATE game_servers SET state = 'RUNNING', updated_at = $2 WHERE id = $1`, []any{authorityID, now}},
		{`UPDATE p2p_match_sessions SET state = 'RUNNING', updated_at = $2 WHERE match_attempt_id = $1 AND state = 'STARTING'`, []any{attemptID, now}},
		{`UPDATE p2p_rooms SET state = 'RUNNING', updated_at = $2 WHERE managed_lobby_id = $1 AND state IN ('CONNECTING', 'RUNNING')`, []any{lobbyID, now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncProjectionGenerations(ctx context.Context, tx pgx.Tx, attemptID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE meta_match_players AS projected
		SET connection_generation = roster.connection_generation
		FROM meta_matches AS match, match_attempt_roster AS roster
		WHERE match.match_attempt_id = $1
		  AND projected.match_id = match.id
		  AND roster.attempt_id = $1
		  AND projected.player_id = roster.player_id
	`, attemptID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_match_roster AS projected
		SET connection_generation = roster.connection_generation
		FROM p2p_match_sessions AS match, match_attempt_roster AS roster
		WHERE match.match_attempt_id = $1
		  AND projected.match_id = match.id
		  AND roster.attempt_id = $1
		  AND projected.player_id = roster.player_id
	`, attemptID); err != nil {
		return err
	}
	return nil
}

func (s *Service) P2PConfirmConnected(ctx context.Context, actor Actor, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce string, generation int) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	snapshot, err := s.ConfirmConnected(ctx, actor.PlayerID, authoritySession, attemptID, worldInstanceID, playerID, grantJTI, nativeConnectionNonce, generation)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx, snapshot.LobbyID, actor.PlayerID)
}

func (s *Service) MarkDisconnected(ctx context.Context, authorityID, authoritySession, attemptID, worldInstanceID, playerID, nativeConnectionNonce string, generation, routeGeneration int) (Snapshot, error) {
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	nativeConnectionNonce = strings.TrimSpace(nativeConnectionNonce)
	if generation < 1 || routeGeneration < 1 || !worldInstancePattern.MatchString(worldInstanceID) || !nativeConnectionNoncePattern.MatchString(nativeConnectionNonce) {
		return Snapshot{}, invalid("Invalid connection generation.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID, storedWorldInstanceID, roomRole, connectionState, liveNonce, lastDisconnectedNonce string
	var currentRouteGeneration, authorizationGeneration, liveGeneration, liveRouteGeneration int
	err = tx.QueryRow(ctx, `
		SELECT attempt.lobby_id, attempt.route_generation,
		       COALESCE(attempt.world_instance_id, ''),
		       roster.room_role, roster.connection_state,
		       roster.connection_generation,
		       COALESCE(roster.live_connection_generation, 0),
		       COALESCE(roster.live_route_generation, 0),
		       COALESCE(roster.live_native_connection_nonce, ''),
	       COALESCE(roster.last_disconnected_native_connection_nonce, '')
		FROM match_attempts AS attempt
		JOIN match_attempt_roster AS roster
		  ON roster.attempt_id = attempt.id AND roster.player_id = $4
		WHERE attempt.id = $1 AND attempt.authority_id = $2 AND attempt.authority_session_id = $3
		  AND state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE OF attempt, roster
	`, attemptID, authorityID, authoritySession, playerID).Scan(
		&lobbyID, &currentRouteGeneration, &storedWorldInstanceID, &roomRole,
		&connectionState, &authorizationGeneration, &liveGeneration, &liveRouteGeneration, &liveNonce, &lastDisconnectedNonce,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SESSION_REQUIRED", "The authority session does not match the active attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if storedWorldInstanceID == "" {
		return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The attempt has no persisted native world identity.", nil)
	}
	if storedWorldInstanceID != worldInstanceID {
		return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_CONFLICT", "The disconnect belongs to a different world instance.", nil)
	}
	if liveGeneration != generation {
		return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The reported connection is stale or has already been replaced.", nil)
	}
	if liveRouteGeneration != routeGeneration {
		return Snapshot{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The reported native connection route is stale or has already been replaced.", nil)
	}
	if liveNonce != "" && liveNonce != nativeConnectionNonce {
		return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The reported native connection nonce is stale.", nil)
	}
	if liveNonce == "" && lastDisconnectedNonce != "" && lastDisconnectedNonce != nativeConnectionNonce {
		return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The reported native connection nonce is stale.", nil)
	}
	if liveNonce == "" && connectionState != "DISCONNECTED" &&
		!(connectionState == "CONNECTING" && liveGeneration != authorizationGeneration) {
		return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The reported connection is not currently live.", nil)
	}
	if roomRole == "MEMBER" {
		var consumed bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM match_admission_grants AS admission
				WHERE admission.attempt_id = $1 AND admission.player_id = $2
				  AND admission.connection_generation = $3
				  AND admission.route_generation = $4
				  AND admission.consumed_at IS NOT NULL
				  AND admission.consumed_connection_nonce = $5
			)
		`, attemptID, playerID, generation, routeGeneration, nativeConnectionNonce).Scan(&consumed); err != nil {
			return Snapshot{}, internal(err)
		}
		if !consumed {
			return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The reported native connection nonce is not bound to this member seat.", nil)
		}
	} else if roomRole != "HOST" {
		return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The roster seat has no disconnectable native connection.", nil)
	}
	firstDisconnect := liveNonce != ""
	command, err := tx.Exec(ctx, `
		UPDATE match_attempt_roster
		SET connection_state = CASE
				WHEN connection_generation = live_connection_generation THEN 'DISCONNECTED'
				WHEN connection_state = 'CONNECTED' THEN 'DISCONNECTED'
				ELSE connection_state
			END,
			disconnected_at = CASE
				WHEN connection_generation = live_connection_generation OR connection_state = 'CONNECTED' THEN $4
				ELSE disconnected_at
			END,
			last_disconnected_native_connection_nonce = CASE
				WHEN live_native_connection_nonce IS NOT NULL THEN live_native_connection_nonce
				ELSE last_disconnected_native_connection_nonce
			END,
			live_native_connection_nonce = NULL,
			host_live_scope_preserved = CASE WHEN room_role = 'HOST' THEN FALSE ELSE host_live_scope_preserved END,
			updated_at = $4
		WHERE attempt_id = $1 AND player_id = $2
		  AND live_connection_generation = $3
		  AND live_route_generation = $5
		  AND (
			live_native_connection_nonce = $6
			OR (live_native_connection_nonce IS NULL
			    AND last_disconnected_native_connection_nonce = $6
			    AND connection_state IN ('DISCONNECTED', 'CONNECTING'))
		  )
	`, attemptID, playerID, generation, now, routeGeneration, nativeConnectionNonce)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		var repeated bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM match_attempt_roster AS roster
				WHERE roster.attempt_id = $1 AND roster.player_id = $2
				  AND COALESCE(roster.live_connection_generation, 0) = $3
				  AND COALESCE(roster.live_route_generation, 0) = $4
				  AND roster.connection_state IN ('DISCONNECTED', 'CONNECTING')
				  AND roster.live_native_connection_nonce IS NULL
				  AND roster.last_disconnected_native_connection_nonce = $5
			)
		`, attemptID, playerID, generation, routeGeneration, nativeConnectionNonce).Scan(&repeated); err != nil {
			return Snapshot{}, internal(err)
		}
		if !repeated {
			return Snapshot{}, conflict("MATCH_CONNECTION_GENERATION_STALE", "The reported connection is stale or is not currently connected.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, "")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE meta_match_players AS projected
		SET disconnected_at = $3
		FROM meta_matches AS match
		WHERE match.match_attempt_id = $1 AND projected.match_id = match.id
		  AND projected.player_id = $2 AND projected.connection_generation = $4
	`, attemptID, playerID, now, generation); err != nil {
		return Snapshot{}, internal(err)
	}
	// A real P2P HOST disconnect is the only event that invalidates the live
	// authority connection and opens a route refresh.  Heartbeat timeout alone
	// never relabels the old live connection.  If heartbeat already advanced
	// the authorization generation, this branch is intentionally skipped: the
	// old native event only clears its own live generation.
	if roomRole == "HOST" && firstDisconnect && authorizationGeneration == generation && currentRouteGeneration == liveRouteGeneration {
		command, err := tx.Exec(ctx, `
			UPDATE match_attempts
			SET route_generation = route_generation + 1,
			    payload_route_generation = NULL,
			    host_reconnect_deadline = CASE WHEN host_reconnect_deadline IS NULL
			                                  THEN $2::timestamptz + ($3::double precision * interval '1 second')
			                                  ELSE host_reconnect_deadline END,
			    authority_last_seen_at = $2, updated_at = $2
			WHERE id = $1 AND authority_id = $4 AND authority_session_id = $5
			  AND route_generation = $6
		`, attemptID, now, s.config.P2PHostReconnectSeconds, authorityID, authoritySession, currentRouteGeneration)
		if err != nil {
			return Snapshot{}, internal(err)
		}
		if command.RowsAffected() != 1 {
			return Snapshot{}, conflict("MATCH_ATTEMPT_STATE_CONFLICT", "The P2P attempt changed while recording native disconnect.", nil)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempt_roster
			SET connection_generation = connection_generation + 1, updated_at = $2
			WHERE attempt_id = $1
		`, attemptID, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if err := s.syncProjectionGenerations(ctx, tx, attemptID); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_admission_grants SET revoked_at = $2
			WHERE attempt_id = $1 AND revoked_at IS NULL
		`, attemptID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	} else if roomRole == "HOST" && firstDisconnect {
		// Heartbeat may already have moved the authority route while the old
		// HOST was still live.  The native event clears only that old scope but
		// must still open/refresh the bounded same-world recovery window.
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempts
			SET host_reconnect_deadline = CASE WHEN host_reconnect_deadline IS NULL
			                                  THEN $2::timestamptz + ($3::double precision * interval '1 second')
			                                  ELSE host_reconnect_deadline END,
			    payload_route_generation = NULL,
			    authority_last_seen_at = $2, updated_at = $2
			WHERE id = $1 AND authority_id = $4 AND authority_session_id = $5
		`, attemptID, now, s.config.P2PHostReconnectSeconds, authorityID, authoritySession); err != nil {
			return Snapshot{}, internal(err)
		}
		// The old HOST connection was live under an earlier authorization
		// route.  Once its native DISCONNECTED event is accepted, advance the
		// HOST seat generation before a fresh same-world Ready can install its
		// new nonce.  This keeps the old live scope and the replacement scope
		// distinct even though the world is retained.
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempt_roster
			SET connection_generation = connection_generation + 1, updated_at = $3
			WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
			  AND connection_generation = live_connection_generation
		`, attemptID, playerID, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if err := s.syncProjectionGenerations(ctx, tx, attemptID); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

func (s *Service) P2PMarkDisconnected(ctx context.Context, actor Actor, authoritySession, attemptID, worldInstanceID, playerID, nativeConnectionNonce string, generation, routeGeneration int) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	snapshot, err := s.MarkDisconnected(ctx, actor.PlayerID, authoritySession, attemptID, worldInstanceID, playerID, nativeConnectionNonce, generation, routeGeneration)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx, snapshot.LobbyID, actor.PlayerID)
}

func (s *Service) AuthorityHeartbeat(ctx context.Context, authorityID, authoritySession, attemptID string) error {
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var reconnecting, hostLiveCurrent, hostLiveUnverified bool
	err = tx.QueryRow(ctx, `
		SELECT attempt.host_reconnect_deadline IS NOT NULL,
		       COALESCE(host.connection_state = 'CONNECTED'
		                AND host.live_connection_generation = host.connection_generation
		                AND host.live_route_generation <= attempt.route_generation
		                AND host.host_live_scope_preserved
		                AND NULLIF(host.live_native_connection_nonce, '') IS NOT NULL
		                AND attempt.payload_route_generation = attempt.route_generation,
		                FALSE),
		       COALESCE(host.connection_state = 'CONNECTED'
		                AND NULLIF(host.live_native_connection_nonce, '') IS NULL,
		                FALSE)
		FROM match_attempts AS attempt
		LEFT JOIN match_attempt_roster AS host
		  ON host.attempt_id = attempt.id AND host.room_role = 'HOST'
		WHERE attempt.id = $1 AND attempt.authority_id = $2 AND attempt.authority_session_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE OF attempt
	`, attemptID, authorityID, authoritySession).Scan(&reconnecting, &hostLiveCurrent, &hostLiveUnverified)
	if errors.Is(err, pgx.ErrNoRows) {
		return forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the active match attempt.")
	}
	if err != nil {
		return internal(err)
	}
	// A connected HOST without its persisted native nonce cannot be treated as
	// live or refreshed.  Keep the attempt fail-closed and require the owner to
	// terminate/reconcile it through the normal cleanup lease; never synthesize
	// a disconnect or replace the unknown connection with a new scope.
	if hostLiveUnverified {
		return conflict("MATCH_HOST_LIVE_SCOPE_UNVERIFIED", "The connected P2P HOST has no persisted native connection nonce; terminate or reconcile the attempt before refreshing its authority route.", nil)
	}
	// A recovery window is opened by the sweeper or by a real native HOST
	// DISCONNECTED event.  Only a still-live HOST causes this heartbeat to
	// advance the authorization route.  The old live generation/nonce remains
	// untouched until its native DISCONNECTED event arrives; reconnecting after
	// a prior route refresh merely keeps the attempt alive.
	if reconnecting && hostLiveCurrent {
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempts SET authority_last_seen_at = $3,
			       route_generation = route_generation + 1,
			       payload_route_generation = NULL,
			       updated_at = $3 WHERE id = $1 AND authority_id = $2
		`, attemptID, authorityID, now); err != nil {
			return internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempt_roster SET connection_generation = CASE WHEN room_role = 'MEMBER' THEN connection_generation + 1 ELSE connection_generation END,
			       host_live_scope_preserved = CASE WHEN room_role = 'HOST' THEN FALSE ELSE host_live_scope_preserved END,
			       updated_at = $2 WHERE attempt_id = $1 AND room_role IN ('HOST', 'MEMBER')
		`, attemptID, now); err != nil {
			return internal(err)
		}
		if err := s.syncProjectionGenerations(ctx, tx, attemptID); err != nil {
			return internal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE match_admission_grants SET revoked_at = $2 WHERE attempt_id = $1 AND revoked_at IS NULL`, attemptID, now); err != nil {
			return internal(err)
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE match_attempts SET authority_last_seen_at = $3, updated_at = $3
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $4
	`, attemptID, authorityID, now, authoritySession); err != nil {
		return internal(err)
	}
	return tx.Commit(ctx)
}

func (s *Service) P2PAuthorityHeartbeat(ctx context.Context, actor Actor, authoritySession, attemptID string) error {
	if err := requireActive(actor); err != nil {
		return err
	}
	return s.AuthorityHeartbeat(ctx, actor.PlayerID, authoritySession, attemptID)
}

func (s *Service) Complete(ctx context.Context, authorityID, authoritySession, attemptID string, success bool, failureCode string) (Snapshot, error) {
	failureCode = strings.TrimSpace(failureCode)
	if success && failureCode != "" {
		return Snapshot{}, invalid("Successful completion cannot include a failure code.", nil)
	}
	if !success && !lobbyLabelPattern.MatchString(failureCode) {
		return Snapshot{}, invalid("A failed attempt requires a valid failure code.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID, hosting string
	var metaMatchID, storedFailureCode, cleanupState string
	var currentState AttemptState
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, hosting_kind, COALESCE(meta_match_id, ''), state,
		       COALESCE(failure_code, ''), cleanup_state
		FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(
		&lobbyID, &hosting, &metaMatchID, &currentState, &storedFailureCode,
		&cleanupState,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the active match attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if currentState == AttemptCompleted || currentState == AttemptAborted {
		if (success && currentState != AttemptCompleted) ||
			(!success && (currentState != AttemptAborted || storedFailureCode != failureCode)) {
			return Snapshot{}, conflict("MATCH_ATTEMPT_COMPLETION_CONFLICT", "The attempt already has a different terminal result.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, "")
	}
	if success && currentState != AttemptRunning {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_RUNNING", "A successful match can complete only after it is running.", nil)
	}
	if !success && currentState != AttemptProvisioning && currentState != AttemptConnecting && currentState != AttemptRunning {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_ACTIVE", "The match attempt cannot be aborted from its current state.", nil)
	}
	attemptState := AttemptCompleted
	lobbyState := StateCompleted
	metaState := "COMPLETED"
	if !success {
		attemptState, lobbyState, metaState = AttemptAborted, StateAborted, "FAILED"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_attempts SET state = $3, failure_code = NULLIF($4, ''),
		       completed_at = $5, cleanup_state = 'PENDING',
		       cleanup_requested_at = COALESCE(cleanup_requested_at, $5),
		       cleanup_lease_expires_at = $6, cleanup_error = NULL,
		       updated_at = $5 WHERE id = $1 AND authority_id = $2
	`, attemptID, authorityID, attemptState, failureCode, now, now.Add(s.provisioningTimeout())); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET state = $2, closed_at = $3, updated_at = $3 WHERE id = $1`, lobbyID, lobbyState, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_admission_grants SET revoked_at = $2 WHERE attempt_id = $1 AND revoked_at IS NULL`, attemptID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if metaMatchID != "" {
		if _, err := tx.Exec(ctx, `UPDATE meta_matches SET state = $2, completed_at = $3, updated_at = $3 WHERE id = $1`, metaMatchID, metaState, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if !success {
			if _, err := tx.Exec(ctx, `
				UPDATE meta_match_tickets AS ticket
				SET state = 'FAILED', failure_code = $2, completed_at = $3, updated_at = $3
				FROM meta_matches AS match
				WHERE match.id = $1 AND ticket.id = match.ticket_id
				  AND ticket.state = 'MATCHED'
			`, metaMatchID, failureCode, now); err != nil {
				return Snapshot{}, internal(err)
			}
		}
	}
	if hosting == string(HostingDedicated) {
		// Keep the instance isolated until NativeCleared confirms that the
		// process/world and transport resources have actually been released.
		if _, err := tx.Exec(ctx, `UPDATE game_servers SET state = 'CLEANUP_PENDING', updated_at = $2 WHERE id = $1`, authorityID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	} else {
		if s.p2pProjector == nil {
			return Snapshot{}, internal(errors.New("authoritative P2P match projector is unavailable"))
		}
		if err := s.p2pProjector.CompleteManagedAttempt(ctx, tx, attemptID, success, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if hosting == string(HostingP2P) {
		if _, err := tx.Exec(ctx, `UPDATE p2p_rooms SET state = 'CLOSED', closed_at = $2, updated_at = $2 WHERE managed_lobby_id = $1`, lobbyID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobby_members SET presence_state = 'OFFLINE', ready = FALSE
		WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
	`, lobbyID); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

// AdministrativeAbortMeta is the non-secret request context recorded for an
// operator initiated abort. Authentication, rooms.close authorization, and
// step-up verification are enforced by the admin HTTP route; the service
// still requires an administrator identity so direct callers cannot perform
// an unattributed state transition.
type AdministrativeAbortMeta struct {
	AdminID   string
	RequestID string
	IPAddress string
	UserAgent string
}

// AdminForceAbort aborts one active attempt while retaining the normal
// cleanup lease. It deliberately shares the same terminal transition shape
// as an authority failure and never clears a game-server/P2P lease directly.
func (s *Service) AdminForceAbort(
	ctx context.Context,
	attemptID, failureCode, reason string,
	meta AdministrativeAbortMeta,
) (Snapshot, error) {
	attemptID = strings.TrimSpace(attemptID)
	failureCode = strings.TrimSpace(failureCode)
	reason = strings.TrimSpace(reason)
	meta.AdminID = strings.TrimSpace(meta.AdminID)
	if attemptID == "" || failureCode == "" || !lobbyLabelPattern.MatchString(failureCode) {
		return Snapshot{}, invalid("A valid match attempt and failure code are required.", nil)
	}
	if meta.AdminID == "" {
		return Snapshot{}, forbidden("ADMIN_UNAUTHORIZED", "Administrator authentication is required.")
	}
	if reason == "" || len([]rune(reason)) > 512 {
		return Snapshot{}, invalid("A non-empty operator reason of at most 512 characters is required.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID, hosting, authorityID, metaMatchID, storedFailureCode, cleanupState, lobbyState string
	var currentState AttemptState
	err = tx.QueryRow(ctx, `
		SELECT attempt.lobby_id, attempt.hosting_kind, COALESCE(attempt.authority_id, ''),
		       COALESCE(attempt.meta_match_id, ''), attempt.state,
		       COALESCE(attempt.failure_code, ''), attempt.cleanup_state, lobby.state
		FROM match_attempts AS attempt
		JOIN match_lobbies AS lobby ON lobby.id = attempt.lobby_id
		WHERE attempt.id = $1
		FOR UPDATE OF attempt, lobby
	`, attemptID).Scan(
		&lobbyID, &hosting, &authorityID, &metaMatchID, &currentState,
		&storedFailureCode, &cleanupState, &lobbyState,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, notFound("MATCH_ATTEMPT_NOT_FOUND", "The match attempt was not found.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if currentState == AttemptAborted {
		if storedFailureCode != failureCode {
			return Snapshot{}, conflict("MATCH_ATTEMPT_COMPLETION_CONFLICT", "The attempt already has a different terminal result.", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, "")
	}
	if currentState == AttemptCompleted {
		return Snapshot{}, conflict("MATCH_ATTEMPT_COMPLETION_CONFLICT", "The attempt already has a different terminal result.", nil)
	}
	if currentState != AttemptFrozen && currentState != AttemptProvisioning &&
		currentState != AttemptConnecting && currentState != AttemptRunning {
		return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_ACTIVE", "The match attempt cannot be aborted from its current state.", nil)
	}
	oldValue := map[string]any{
		"attempt_state": currentState, "lobby_state": lobbyState,
		"cleanup_state": cleanupState, "failure_code": storedFailureCode,
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_attempts SET state = 'ABORTED', failure_code = $2,
		       completed_at = $3, cleanup_state = 'PENDING',
		       cleanup_requested_at = COALESCE(cleanup_requested_at, $3),
		       cleanup_lease_expires_at = $4, cleanup_error = NULL,
		       updated_at = $3
		WHERE id = $1
	`, attemptID, failureCode, now, now.Add(s.provisioningTimeout())); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobbies SET state = 'ABORTED', closed_at = $2, updated_at = $2
		WHERE id = $1
	`, lobbyID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_admission_grants SET revoked_at = $2
		WHERE attempt_id = $1 AND revoked_at IS NULL
	`, attemptID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if metaMatchID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE meta_matches SET state = 'FAILED', completed_at = $2, updated_at = $2
			WHERE id = $1
		`, metaMatchID, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE meta_match_tickets AS ticket
			SET state = 'FAILED', failure_code = $2, completed_at = $3, updated_at = $3
			FROM meta_matches AS match
			WHERE match.id = $1 AND ticket.id = match.ticket_id AND ticket.state = 'MATCHED'
		`, metaMatchID, failureCode, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if hosting == string(HostingDedicated) {
		if authorityID == "" {
			return Snapshot{}, internal(errors.New("dedicated administrative abort omitted its authority server"))
		}
		if _, err := tx.Exec(ctx, `
			UPDATE game_servers SET state = 'CLEANUP_PENDING', updated_at = $2 WHERE id = $1
		`, authorityID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	} else {
		if s.p2pProjector == nil {
			return Snapshot{}, internal(errors.New("authoritative P2P match projector is unavailable"))
		}
		if err := s.p2pProjector.CompleteManagedAttempt(ctx, tx, attemptID, false, now); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE p2p_rooms SET state = 'CLOSED', closed_at = $2, updated_at = $2
			WHERE managed_lobby_id = $1
		`, lobbyID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobby_members SET presence_state = 'OFFLINE', ready = FALSE
		WHERE lobby_id = $1 AND membership_state = 'ACTIVE'
	`, lobbyID); err != nil {
		return Snapshot{}, internal(err)
	}
	newValue := map[string]any{
		"attempt_state": "ABORTED", "lobby_state": "ABORTED",
		"cleanup_state": "PENDING", "failure_code": failureCode,
	}
	oldJSON, err := json.Marshal(oldValue)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	newJSON, err := json.Marshal(newValue)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_logs (
			id, admin_id, action, target_type, target_id, old_value, new_value,
			reason, request_id, ip_address, user_agent, result, created_at
		) VALUES ($1, $2, 'MATCH_ATTEMPT_FORCE_ABORT', 'match_attempt', $3,
		          $4::jsonb, $5::jsonb, $6, NULLIF($7, ''), NULLIF($8, '')::inet,
		          NULLIF($9, ''), 'SUCCEEDED', $10)
	`, newAdmissionID("ada_"), meta.AdminID, attemptID, oldJSON, newJSON,
		reason, meta.RequestID, meta.IPAddress, meta.UserAgent, now); err != nil {
		return Snapshot{}, internal(fmt.Errorf("audit administrative match abort: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(fmt.Errorf("commit administrative match abort: %w", err))
	}
	return s.Get(ctx, lobbyID, "")
}

func (s *Service) P2PComplete(ctx context.Context, actor Actor, authoritySession, attemptID string, success bool, failureCode string) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	return s.Complete(ctx, actor.PlayerID, authoritySession, attemptID, success, failureCode)
}

// NativeCleared is the native-world acknowledgement branch. It always
// requires a persisted world identity; the P2P HOST owned-process receipt is
// accepted only through P2PNativeCleared's explicit evidence argument.
func (s *Service) NativeCleared(
	ctx context.Context,
	authorityID, authoritySession, attemptID, worldInstanceID string,
	rosterRevision int64, routeGeneration int,
) (Snapshot, error) {
	return s.nativeCleared(ctx, authorityID, authoritySession, attemptID, worldInstanceID, rosterRevision, routeGeneration, nil, "")
}

// DedicatedNativeCleared is the Dedicated cleanup transition carrying an
// owned-process exit claim. The caller is the signed Dedicated node, and the
// claim is still validated against the terminal attempt scope in PostgreSQL.
func (s *Service) DedicatedNativeCleared(
	ctx context.Context,
	authorityID, authoritySession, attemptID, worldInstanceID string,
	rosterRevision int64, routeGeneration int,
	evidence OwnedProcessExitEvidence,
) (Snapshot, error) {
	if err := validateOwnedProcessExitEvidence(evidence); err != nil {
		return Snapshot{}, err
	}
	return s.nativeCleared(ctx, authorityID, authoritySession, attemptID, worldInstanceID, rosterRevision, routeGeneration, &evidence, HostingDedicated)
}

// nativeCleared is shared by the Dedicated and P2P cleanup routes. An empty
// world is accepted only for a validated owned-process claim when that
// authority never published a world; once a world is persisted, every route
// must echo it. P2P cleanup also requires the frozen HOST row.
func (s *Service) nativeCleared(
	ctx context.Context,
	authorityID, authoritySession, attemptID, worldInstanceID string,
	rosterRevision int64, routeGeneration int,
	evidence *OwnedProcessExitEvidence,
	expectedHosting HostingKind,
) (Snapshot, error) {
	authorityID = strings.TrimSpace(authorityID)
	authoritySession = strings.TrimSpace(authoritySession)
	attemptID = strings.TrimSpace(attemptID)
	worldInstanceID = strings.TrimSpace(worldInstanceID)
	if authorityID == "" || authoritySession == "" || attemptID == "" || len(worldInstanceID) > 128 || rosterRevision < 1 || routeGeneration < 1 || (worldInstanceID == "" && evidence == nil) {
		return Snapshot{}, invalid("Invalid native cleanup acknowledgement.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID, hosting, storedWorld string
	var cleanupState string
	var currentState AttemptState
	var storedRosterRevision int64
	var storedRouteGeneration int
	var payloadInstalled bool
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, hosting_kind, state, cleanup_state,
		       COALESCE(world_instance_id, ''), roster_revision, route_generation,
		       payload_installed_at IS NOT NULL
		FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND state IN ('COMPLETED', 'ABORTED')
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(
		&lobbyID, &hosting, &currentState, &cleanupState, &storedWorld,
		&storedRosterRevision, &storedRouteGeneration, &payloadInstalled,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "The authority session does not own a terminal match attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if expectedHosting != "" && hosting != string(expectedHosting) {
		return Snapshot{}, forbidden("MATCH_HOSTING_KIND_REQUIRED", "This cleanup evidence is not valid for the authority hosting mode.")
	}
	if expectedHosting == HostingP2P {
		var hostAuthority bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM match_attempt_roster
				WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
			)
		`, attemptID, authorityID).Scan(&hostAuthority); err != nil {
			return Snapshot{}, internal(err)
		}
		if !hostAuthority {
			return Snapshot{}, forbidden("MATCH_HOST_AUTHORITY_REQUIRED", "Only the frozen P2P HOST may acknowledge native cleanup.")
		}
	}
	if evidence != nil && evidence.EvidenceKind == "native_process_not_started" {
		if expectedHosting != HostingP2P || hosting != string(HostingP2P) {
			return Snapshot{}, forbidden("MATCH_HOSTING_KIND_REQUIRED", "The native process not-started receipt is valid only for a P2P HOST.")
		}
		if payloadInstalled || storedWorld != "" {
			return Snapshot{}, conflict("MATCH_NATIVE_PROCESS_NOT_STARTED_INVALID", "The attempt already published a native world or installed Payload.", nil)
		}
	}
	if cleanupState == "CLEARED" {
		if storedWorld == "" {
			if evidence == nil || (hosting != string(HostingDedicated) && hosting != string(HostingP2P)) || worldInstanceID != "" {
				return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The attempt has no persisted native world identity.", nil)
			}
		} else if storedWorld != worldInstanceID {
			return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_CONFLICT", "The cleanup acknowledgement belongs to a different world instance.", nil)
		}
		if storedRosterRevision != rosterRevision {
			return Snapshot{}, conflict("MATCH_ROSTER_REVISION_CONFLICT", "The cleanup acknowledgement belongs to a different frozen roster.", nil)
		}
		if storedRouteGeneration != routeGeneration {
			return Snapshot{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The cleanup acknowledgement belongs to a stale authority route.", nil)
		}
		if err := verifyNativeCleanupAudit(ctx, tx, lobbyID, attemptID, evidence); err != nil {
			return Snapshot{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, "")
	}
	if storedWorld == "" {
		if evidence == nil || (hosting != string(HostingDedicated) && hosting != string(HostingP2P)) || worldInstanceID != "" {
			return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_REQUIRED", "The attempt has no persisted native world identity.", nil)
		}
	} else if storedWorld != worldInstanceID {
		return Snapshot{}, conflict("MATCH_WORLD_INSTANCE_CONFLICT", "The cleanup acknowledgement belongs to a different world instance.", nil)
	}
	if storedRosterRevision != rosterRevision {
		return Snapshot{}, conflict("MATCH_ROSTER_REVISION_CONFLICT", "The cleanup acknowledgement belongs to a different frozen roster.", nil)
	}
	if storedRouteGeneration != routeGeneration {
		return Snapshot{}, conflict("MATCH_ROUTE_GENERATION_STALE", "The cleanup acknowledgement belongs to a stale authority route.", nil)
	}
	var updateTag pgconn.CommandTag
	var updateErr error
	if storedWorld == "" {
		updateTag, updateErr = tx.Exec(ctx, `
			UPDATE match_attempts
			SET cleanup_state = 'CLEARED', native_cleared_at = $2,
			    cleanup_error = NULL, cleanup_lease_expires_at = NULL,
			    updated_at = $2
			WHERE id = $1 AND cleanup_state = 'PENDING' AND COALESCE(world_instance_id, '') = ''
			  AND roster_revision = $3 AND route_generation = $4
		`, attemptID, now, rosterRevision, routeGeneration)
	} else {
		updateTag, updateErr = tx.Exec(ctx, `
			UPDATE match_attempts
			SET cleanup_state = 'CLEARED', native_cleared_at = $2,
			    cleanup_error = NULL, cleanup_lease_expires_at = NULL,
			    updated_at = $2
			WHERE id = $1 AND cleanup_state = 'PENDING' AND world_instance_id = $3
			  AND roster_revision = $4 AND route_generation = $5
		`, attemptID, now, worldInstanceID, rosterRevision, routeGeneration)
	}
	if updateErr != nil {
		return Snapshot{}, internal(updateErr)
	}
	if updateTag.RowsAffected() != 1 {
		return Snapshot{}, conflict("MATCH_CLEANUP_STATE_CHANGED", "The native cleanup lease changed before this acknowledgement committed.", nil)
	}
	if err := insertNativeCleanupAudit(ctx, tx, lobbyID, attemptID, authorityID, hosting, authoritySession, worldInstanceID, rosterRevision, routeGeneration, evidence, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if hosting == string(HostingDedicated) {
		if _, err := tx.Exec(ctx, `
			UPDATE game_servers AS server
			SET state = CASE
			      WHEN server.last_heartbeat_at > $2
			       AND server.token_revoked_at IS NULL
			       AND server.token_expires_at > $3
			      THEN 'READY' ELSE 'UNHEALTHY' END,
			    player_count = 0, updated_at = $3
			WHERE server.id = $1 AND server.state = 'CLEANUP_PENDING'
		`, authorityID, now.Add(-s.serverFreshness), now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

// verifyNativeCleanupAudit binds a repeated CLEARED acknowledgement to the
// first receipt that performed the transition.  The terminal state is
// idempotent only for the exact same evidence shape; a changed evidence kind,
// process id, or start fingerprint is a different assertion and must not
// overwrite the original audit record.
func verifyNativeCleanupAudit(
	ctx context.Context,
	tx pgx.Tx,
	lobbyID, attemptID string,
	evidence *OwnedProcessExitEvidence,
) error {
	var details []byte
	err := tx.QueryRow(ctx, `
		SELECT details
		FROM vnt_security_audit_logs
		WHERE event_type = 'MATCH_NATIVE_CLEANUP_CLEARED'
		  AND request_id = $1
		  AND details->>'attempt_id' = $1
		  AND details->>'lobby_id' = $2
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`, attemptID, lobbyID).Scan(&details)
	if errors.Is(err, pgx.ErrNoRows) {
		return conflict("MATCH_CLEANUP_RECEIPT_UNAVAILABLE", "The original native cleanup receipt is not available for idempotent verification.", nil)
	}
	if err != nil {
		return internal(err)
	}
	var recorded struct {
		EvidenceKind            string `json:"evidence_kind"`
		OwnedProcessID          uint32 `json:"owned_process_id"`
		ProcessStartFingerprint string `json:"process_start_fingerprint"`
	}
	if err := json.Unmarshal(details, &recorded); err != nil {
		return internal(fmt.Errorf("decode native cleanup audit evidence: %w", err))
	}
	expectedKind := ""
	expectedProcessID := uint32(0)
	expectedFingerprint := ""
	if evidence != nil {
		expectedKind = evidence.EvidenceKind
		expectedProcessID = evidence.OwnedProcessID
		expectedFingerprint = evidence.ProcessStartFingerprint
	}
	if recorded.EvidenceKind != expectedKind ||
		recorded.OwnedProcessID != expectedProcessID ||
		recorded.ProcessStartFingerprint != expectedFingerprint {
		return conflict("MATCH_CLEANUP_RECEIPT_CONFLICT", "The native cleanup acknowledgement does not match the receipt that cleared this attempt.", nil)
	}
	return nil
}

// insertNativeCleanupAudit persists the first successful cleanup receipt in
// the existing security-audit stream. It runs in the same transaction as the
// CLEARED transition; duplicate acknowledgements return above without adding
// or replacing the original receipt. The fields are scoped operational
// metadata, not a substitute for the supervisor's local child-handle proof.
func insertNativeCleanupAudit(
	ctx context.Context,
	tx pgx.Tx,
	lobbyID, attemptID, authorityID, hosting, authoritySession, worldInstanceID string,
	rosterRevision int64,
	routeGeneration int,
	evidence *OwnedProcessExitEvidence,
	createdAt time.Time,
) error {
	evidenceKind := ""
	ownedProcessID := uint32(0)
	processStartFingerprint := ""
	if evidence != nil {
		evidenceKind = evidence.EvidenceKind
		ownedProcessID = evidence.OwnedProcessID
		processStartFingerprint = evidence.ProcessStartFingerprint
	}
	authoritySessionSHA256 := fmt.Sprintf("%x", sha256.Sum256([]byte(authoritySession)))
	details, err := json.Marshal(map[string]any{
		"attempt_id":                attemptID,
		"authority_id":              authorityID,
		"authority_session_sha256":  authoritySessionSHA256,
		"lobby_id":                  lobbyID,
		"world_instance_id":         worldInstanceID,
		"roster_revision":           rosterRevision,
		"route_generation":          routeGeneration,
		"hosting_kind":              hosting,
		"evidence_kind":             evidenceKind,
		"owned_process_id":          ownedProcessID,
		"process_start_fingerprint": processStartFingerprint,
	})
	if err != nil {
		return fmt.Errorf("marshal native cleanup audit details: %w", err)
	}
	playerID := ""
	if hosting == string(HostingP2P) {
		playerID = authorityID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO vnt_security_audit_logs (
			id, event_type, result, actor_type, player_id, node_id, room_id,
			request_id, reason_code, details, created_at
		) VALUES (
			$1, 'MATCH_NATIVE_CLEANUP_CLEARED', 'SUCCEEDED', 'SYSTEM',
			NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''),
			NULLIF($5, ''), 'MATCH_NATIVE_CLEARED', $6, $7
		)
	`, vnt.NewSecurityAuditID(), playerID, "", "", attemptID, details, createdAt)
	if err != nil {
		return fmt.Errorf("insert native cleanup audit: %w", err)
	}
	return nil
}

func (s *Service) P2PNativeCleared(ctx context.Context, actor Actor, authoritySession, attemptID, worldInstanceID string, rosterRevision int64, routeGeneration int, evidence ...OwnedProcessExitEvidence) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	if len(evidence) > 1 {
		return Snapshot{}, invalid("At most one owned process exit evidence receipt is allowed.", nil)
	}
	if len(evidence) == 1 {
		if err := validateP2PNativeCleanupEvidence(evidence[0]); err != nil {
			return Snapshot{}, err
		}
		return s.nativeCleared(ctx, actor.PlayerID, authoritySession, attemptID, worldInstanceID, rosterRevision, routeGeneration, &evidence[0], HostingP2P)
	}
	return s.nativeCleared(ctx, actor.PlayerID, authoritySession, attemptID, worldInstanceID, rosterRevision, routeGeneration, nil, HostingP2P)
}

func (s *Service) compensateJoin(ctx context.Context, lobbyID, playerID string) error {
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	command, err := tx.Exec(ctx, `
		UPDATE match_lobby_members AS member
		SET membership_state = 'LEFT', ready = FALSE, left_at = $3
		FROM match_lobbies AS lobby
		WHERE member.lobby_id = $1 AND member.player_id = $2 AND member.role = 'MEMBER'
		  AND lobby.id = member.lobby_id AND lobby.state = 'OPEN'
	`, lobbyID, playerID, now)
	if err != nil {
		return err
	}
	// Join commits the lobby seat before projecting it into the managed room.
	// If the transport call fails after its own commit, compensate both sides
	// in this transaction so a retry cannot observe a ghost transport member.
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_room_members AS transport_member
		SET status = 'LEFT', left_at = COALESCE(transport_member.left_at, $2)
		FROM p2p_rooms AS room
		WHERE room.managed_lobby_id = $1
		  AND transport_member.room_id = room.id
		  AND transport_member.player_id = $3
		  AND transport_member.status = 'ACTIVE'
	`, lobbyID, now, playerID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_rooms AS room
		SET player_count = (
			SELECT COUNT(*) FROM p2p_room_members AS member
			WHERE member.room_id = room.id AND member.status = 'ACTIVE'
		), updated_at = $2
		WHERE room.managed_lobby_id = $1
	`, lobbyID, now); err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobby_members SET ready = FALSE WHERE lobby_id = $1 AND membership_state = 'ACTIVE'`, lobbyID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE match_lobbies SET roster_revision = roster_revision + 1, updated_at = $2 WHERE id = $1 AND state = 'OPEN'`, lobbyID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) validateCreate(input *CreateInput) error {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Mode = strings.TrimSpace(input.Mode)
	input.Region = strings.TrimSpace(input.Region)
	input.ClientVersion = strings.TrimSpace(input.ClientVersion)
	input.VNTNodeID = strings.TrimSpace(input.VNTNodeID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.DisplayName == "" || len(input.DisplayName) > 128 {
		return invalid("Invalid match lobby.", map[string]any{"display_name": "must contain between 1 and 128 characters"})
	}
	for name, value := range map[string]string{"mode": input.Mode, "region": input.Region, "client_version": input.ClientVersion} {
		if !lobbyLabelPattern.MatchString(value) {
			return invalid("Invalid match lobby.", map[string]any{name: "contains unsupported characters"})
		}
	}
	if input.HostingKind != HostingDedicated && input.HostingKind != HostingP2P {
		return invalid("Invalid match lobby.", map[string]any{"hosting_kind": "must be DEDICATED or P2P"})
	}
	if input.HostingKind == HostingDedicated {
		if input.TransportKind != "" || input.VNTNodeID != "" {
			return invalid("Invalid match lobby.", map[string]any{"transport_kind": "dedicated lobbies do not select a P2P transport"})
		}
	} else {
		if input.TransportKind == "" {
			input.TransportKind = TransportLegacy
		}
		if input.TransportKind != TransportLegacy && input.TransportKind != TransportVNT {
			return invalid("Invalid match lobby.", map[string]any{"transport_kind": "must be LEGACY_RELAY or VNT"})
		}
	}
	if input.TeamOneCapacity < 1 || input.TeamOneCapacity > 32 || input.TeamTwoCapacity < 1 || input.TeamTwoCapacity > 32 || input.TeamOneCapacity+input.TeamTwoCapacity > 64 {
		return invalid("Invalid team capacity.", nil)
	}
	if input.TeamID != 1 && input.TeamID != 2 {
		return invalid("Invalid team.", map[string]any{"team_id": "must be 1 or 2"})
	}
	if input.ProtocolVersion < 1 {
		return invalid("Invalid protocol version.", nil)
	}
	if input.IdempotencyKey != "" && !lobbyIdempotencyPattern.MatchString(input.IdempotencyKey) {
		return invalid("Invalid idempotency key.", nil)
	}
	return nil
}

func (s *Service) p2pCreateInput(lobbyID string, input CreateInput) p2proom.CreateInput {
	return p2proom.CreateInput{
		DisplayName: input.DisplayName, Region: input.Region, Mode: input.Mode,
		Version: input.ClientVersion, MaxPlayers: input.TeamOneCapacity + input.TeamTwoCapacity,
		TransportKind: p2proom.TransportKind(input.TransportKind), VNTNodeID: input.VNTNodeID,
		IdempotencyKey: "match-lobby:" + lobbyID, ManagedLobbyID: lobbyID,
	}
}

func (s *Service) mapLobbyError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("MATCH_LOBBY_NOT_FOUND", "Match lobby not found.")
	}
	return internal(err)
}

func (s *Service) presenceGrace() time.Duration {
	return time.Duration(s.config.PresenceGraceSeconds) * time.Second
}
func (s *Service) initialConnectionWindow() time.Duration {
	return time.Duration(s.config.InitialConnectionSeconds) * time.Second
}
func (s *Service) provisioningTimeout() time.Duration {
	return time.Duration(s.config.ProvisioningSeconds) * time.Second
}
func (s *Service) grantTTL() time.Duration {
	return time.Duration(s.config.AdmissionGrantTTLSeconds) * time.Second
}

func requireActive(actor Actor) error {
	if strings.TrimSpace(actor.PlayerID) == "" {
		return unauthorized("AUTH_REQUIRED", "Authentication is required.")
	}
	if actor.AccountStatus != player.AccountStatusActive {
		return forbidden("ACCOUNT_NOT_ACTIVE", "The player account is not active.")
	}
	if !actor.SteamVerified || (actor.AuthLevel != player.AuthLevelVerified && actor.AuthLevel != player.AuthLevelTrusted) {
		return forbidden("STEAM_VERIFICATION_REQUIRED", "Strict roster lobbies require a verified Steam identity.")
	}
	return nil
}

func requireOpenRevision(lobby Lobby, expected int64) error {
	if lobby.State != StateOpen {
		return conflict("MATCH_LOBBY_NOT_MUTABLE", "The match lobby roster is frozen or closed.", map[string]any{"state": lobby.State, "roster_revision": lobby.RosterRevision})
	}
	if expected < 1 || expected != lobby.RosterRevision {
		return conflict("MATCH_LOBBY_REVISION_CONFLICT", "The match lobby changed. Refresh the snapshot and retry.", map[string]any{"roster_revision": lobby.RosterRevision})
	}
	return nil
}

func toP2PActor(actor Actor) p2proom.Actor {
	return p2proom.Actor{PlayerID: actor.PlayerID, AccountStatus: actor.AccountStatus}
}

func createRequestHash(input CreateInput) []byte {
	encoded, _ := json.Marshal(struct {
		DisplayName     string
		HostingKind     HostingKind
		TransportKind   TransportKind
		Mode            string
		Region          string
		ClientVersion   string
		ProtocolVersion int
		TeamOneCapacity int
		TeamTwoCapacity int
		TeamID          int
		VNTNodeID       string
	}{input.DisplayName, input.HostingKind, input.TransportKind, input.Mode, input.Region,
		input.ClientVersion, input.ProtocolVersion, input.TeamOneCapacity,
		input.TeamTwoCapacity, input.TeamID, input.VNTNodeID})
	digest := sha256.Sum256(encoded)
	return digest[:]
}
