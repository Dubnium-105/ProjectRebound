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
	"github.com/jackc/pgx/v5"
)

var lobbyLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var lobbyIdempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

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

func NewService(repository *Repository, cfg config.MatchLobbyConfig, signer *AdmissionSigner, serverFreshness time.Duration) *Service {
	return &Service{repository: repository, config: cfg, signer: signer, serverFreshness: serverFreshness, now: time.Now}
}

func (s *Service) SetP2PTransport(transport P2PTransport) { s.p2p = transport }

func (s *Service) SetP2PMatchProjector(projector P2PMatchProjector) {
	s.p2pProjector = projector
}

func (s *Service) FailClosedDisabledAttempts(ctx context.Context) error {
	if s.config.StrictRosterV1Enabled {
		return nil
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `
		SELECT id, lobby_id, COALESCE(authority_id, ''), hosting_kind
		FROM match_attempts
		WHERE state IN ('FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING')
		FOR UPDATE
	`)
	if err != nil {
		return err
	}
	type activeAttempt struct{ id, lobbyID, authorityID, hosting string }
	var attempts []activeAttempt
	for rows.Next() {
		var item activeAttempt
		if err := rows.Scan(&item.id, &item.lobbyID, &item.authorityID, &item.hosting); err != nil {
			rows.Close()
			return err
		}
		attempts = append(attempts, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, attempt := range attempts {
		if _, err := tx.Exec(ctx, `UPDATE match_attempts SET state = 'ABORTED', failure_code = 'STRICT_ROSTER_V1_DISABLED', completed_at = $2, updated_at = $2 WHERE id = $1`, attempt.id, now); err != nil {
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
			SET state = 'FAILED', failure_code = 'STRICT_ROSTER_V1_DISABLED',
			    completed_at = $2, updated_at = $2
			FROM meta_matches AS match
			WHERE match.match_attempt_id = $1 AND ticket.id = match.ticket_id
			  AND ticket.state = 'MATCHED'
		`, attempt.id, now); err != nil {
			return err
		}
		if attempt.hosting == string(HostingDedicated) && attempt.authorityID != "" {
			if _, err := tx.Exec(ctx, `UPDATE game_servers SET state = 'READY', player_count = 0, updated_at = $2 WHERE id = $1`, attempt.authorityID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE p2p_match_sessions SET state = 'ABORTED', finalized_at = $2, updated_at = $2 WHERE match_attempt_id = $1 AND state IN ('STARTING', 'RUNNING', 'COLLECTING')`, attempt.id, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_rooms AS room SET state = 'CLOSED', closed_at = $1, updated_at = $1
		FROM match_lobbies AS lobby
		WHERE room.managed_lobby_id = lobby.id
		  AND lobby.state IN ('OPEN', 'FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING')
	`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobby_members AS member
		SET presence_state = 'OFFLINE', ready = FALSE
		FROM match_lobbies AS lobby
		WHERE member.lobby_id = lobby.id
		  AND lobby.state IN ('OPEN', 'FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING')
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE match_lobbies SET state = 'ABORTED', closed_at = $1, updated_at = $1
		WHERE state IN ('OPEN', 'FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING')
	`, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (CreateResult, error) {
	if err := s.requireEnabled(); err != nil {
		return CreateResult{}, err
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
	if !s.config.StrictRosterV1Enabled {
		return Snapshot{}, notFound("MATCH_LOBBY_NOT_FOUND", "Match lobby not found.")
	}
	snapshot, err := s.repository.Snapshot(ctx, strings.TrimSpace(lobbyID), viewerPlayerID, s.now().UTC())
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, notFound("MATCH_LOBBY_NOT_FOUND", "Match lobby not found.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	return snapshot, nil
}

func (s *Service) Active(ctx context.Context, actor Actor) (CreateResult, error) {
	if err := s.requireEnabled(); err != nil {
		return CreateResult{}, err
	}
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
	if !s.config.StrictRosterV1Enabled {
		return ListResult{Items: []Summary{}}, nil
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
			ORDER BY CASE WHEN region = $3 THEN 0 ELSE 1 END,
			         player_count::float / max_players, last_heartbeat_at DESC, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		`, lobby.Mode, lobby.ClientVersion, lobby.Region, teamOne+teamTwo,
			now.Add(-s.serverFreshness), now).Scan(&authorityID, &endpointHost, &endpointPort)
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

func (s *Service) P2PAuthorityReady(ctx context.Context, actor Actor, attemptID, authoritySession, hostToken, endpointHost string, endpointPort, routeGeneration int) (Snapshot, error) {
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	endpointIP := net.ParseIP(strings.TrimSpace(endpointHost))
	if endpointIP == nil || endpointPort < 1 || endpointPort > 65535 || routeGeneration < 1 {
		return Snapshot{}, invalid("Invalid P2P authority endpoint.", nil)
	}
	endpointHost = endpointIP.String()
	queryNow := s.now().UTC()
	var lobbyID, roomID, ownerID, storedEndpointHost string
	var expectedRouteGeneration int
	var storedEndpointPort int
	var state AttemptState
	var hostDeadline sql.NullTime
	var payloadInstalled bool
	err := s.repository.pool.QueryRow(ctx, `
		SELECT lobby.id, COALESCE(lobby.p2p_room_id, ''), lobby.owner_player_id,
		       attempt.route_generation, attempt.state,
		       COALESCE(attempt.endpoint_host, ''), COALESCE(attempt.endpoint_port, 0),
		       attempt.host_reconnect_deadline,
		       attempt.payload_installed_at IS NOT NULL
		         AND attempt.payload_route_generation = attempt.route_generation
		FROM match_attempts AS attempt
		JOIN match_lobbies AS lobby ON lobby.id = attempt.lobby_id
		WHERE attempt.id = $1 AND attempt.hosting_kind = 'P2P'
		  AND attempt.authority_session_id = $2
	`, attemptID, authoritySession).Scan(
		&lobbyID, &roomID, &ownerID, &expectedRouteGeneration, &state,
		&storedEndpointHost, &storedEndpointPort, &hostDeadline, &payloadInstalled,
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
	if state == AttemptConnecting || state == AttemptRunning {
		if storedEndpointHost != endpointHost || storedEndpointPort != endpointPort {
			return Snapshot{}, conflict("MATCH_AUTHORITY_ENDPOINT_CONFLICT", "The P2P authority is already ready at a different endpoint.", nil)
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
		       connection_deadline = $5, authority_last_seen_at = $6,
		       host_reconnect_deadline = NULL, updated_at = $6
		WHERE id = $1 AND state = 'PROVISIONING' AND authority_id = $7
		  AND authority_session_id = $8 AND route_generation = $4
		  AND host_reconnect_deadline > $6
	`, attemptID, endpointHost, endpointPort, routeGeneration, deadline, now, actor.PlayerID, authoritySession)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		latest, latestErr := s.Get(ctx, lobbyID, actor.PlayerID)
		if latestErr == nil && latest.Attempt != nil &&
			(latest.Attempt.State == AttemptConnecting || latest.Attempt.State == AttemptRunning) &&
			latest.Attempt.RouteGeneration == routeGeneration &&
			latest.Attempt.EndpointHost == endpointHost && latest.Attempt.EndpointPort == endpointPort {
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
		    disconnected_at = NULL, updated_at = $3
		WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
	`, attemptID, actor.PlayerID, now); err != nil {
		return Snapshot{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, actor.PlayerID)
}

func (s *Service) DedicatedAllocation(ctx context.Context, serverID, attemptID string) (AllocationResult, error) {
	if err := s.requireEnabled(); err != nil {
		return AllocationResult{}, err
	}
	return s.allocation(ctx, attemptID, serverID, false)
}

func (s *Service) P2PHostAllocation(ctx context.Context, actor Actor, attemptID string) (AllocationResult, error) {
	if err := s.requireEnabled(); err != nil {
		return AllocationResult{}, err
	}
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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

func (s *Service) DedicatedAuthorityReady(ctx context.Context, serverID, attemptID, authoritySession string) (Snapshot, error) {
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
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
		       authority_last_seen_at = $2, updated_at = $2
		WHERE id = $1 AND authority_id = $4 AND hosting_kind = 'DEDICATED'
		  AND state = 'PROVISIONING' AND authority_session_id = $5
		  AND payload_installed_at IS NOT NULL
		  AND payload_route_generation = route_generation
		RETURNING lobby_id
	`, attemptID, now, deadline, serverID, authoritySession).Scan(&lobbyID)
	if errors.Is(err, pgx.ErrNoRows) {
		var state AttemptState
		lookupErr := tx.QueryRow(ctx, `
			SELECT lobby_id, state FROM match_attempts
			WHERE id = $1 AND authority_id = $2 AND hosting_kind = 'DEDICATED'
			  AND authority_session_id = $3
		`, attemptID, serverID, authoritySession).Scan(&lobbyID, &state)
		if lookupErr == nil && (state == AttemptConnecting || state == AttemptRunning) {
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
	if err := s.requireEnabled(); err != nil {
		return GrantResult{}, err
	}
	if err := requireActive(actor); err != nil {
		return GrantResult{}, err
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
	var payloadRouteReady bool
	var hostReconnecting bool
	var authorityFresh bool
	err = tx.QueryRow(ctx, `
		SELECT attempt.lobby_id, attempt.hosting_kind, attempt.state,
		       COALESCE(attempt.authority_id, ''), attempt.authority_session_id,
		       attempt.roster_revision, attempt.route_generation,
		       COALESCE(attempt.endpoint_host, ''), COALESCE(attempt.endpoint_port, 0),
		       roster.platform_id, roster.room_role, roster.team_id, roster.team_slot,
		       roster.logical_slot, roster.connection_generation,
		       roster.connection_state,
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
		&claims.AuthoritySessionID, &claims.RosterRevision, &claims.RouteGeneration,
		&endpointHost, &endpointPort, &claims.PlatformID, &roomRole, &claims.TeamID,
		&claims.TeamSlot, &claims.LogicalSlot, &claims.ConnectionGeneration,
		&connectionState,
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
	if claims.HostingKind == HostingP2P && roomRole == "HOST" {
		return GrantResult{}, forbidden("MATCH_P2P_HOST_USES_ALLOCATION", "The local P2P host is admitted only through its signed allocation.")
	}
	if endpointHost == "" || endpointPort == 0 {
		return GrantResult{}, conflict("MATCH_AUTHORITY_ENDPOINT_UNAVAILABLE", "The match authority endpoint is unavailable.", nil)
	}
	if priorGrantCount > 0 && connectionState == "CONNECTED" {
		return GrantResult{}, conflict("MATCH_CONNECTION_STILL_ACTIVE", "The previous connection must be released by the authority before a reconnect grant is issued.", nil)
	}
	if priorGrantCount > 0 {
		claims.ConnectionGeneration++
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
			route_generation, issued_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, claims.TokenID, attemptID, actor.PlayerID, claims.ConnectionGeneration,
		claims.RouteGeneration, now, expires); err != nil {
		return GrantResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GrantResult{}, internal(err)
	}
	return GrantResult{
		AttemptID: attemptID, GrantJTI: claims.TokenID,
		EndpointHost: endpointHost, EndpointPort: endpointPort,
		Grant: token, ExpiresAt: expires, ConnectionGeneration: claims.ConnectionGeneration,
	}, nil
}

// AuthorityAdmissions returns grants which Meta has issued to frozen roster
// members but which the scoped authority Payload has not acknowledged staging.
// The bearer token is reconstructed from persisted claims and is never stored.
func (s *Service) AuthorityAdmissions(ctx context.Context, authorityID, authoritySession, attemptID string) (AuthorityAdmissionList, error) {
	if err := s.requireEnabled(); err != nil {
		return AuthorityAdmissionList{}, err
	}
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
			&claims.AuthoritySessionID, &claims.RosterRevision,
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
	if err := s.requireEnabled(); err != nil {
		return GrantDeliveryStatus{}, err
	}
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
		FROM match_attempts AS attempt
		WHERE admission.jti = $4 AND admission.attempt_id = $1
		  AND attempt.id = admission.attempt_id
		  AND attempt.authority_id = $2
		  AND attempt.authority_session_id = $3
		  AND attempt.state IN ('CONNECTING', 'RUNNING')
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
	if err := s.requireEnabled(); err != nil {
		return GrantDeliveryStatus{}, err
	}
	if err := requireActive(actor); err != nil {
		return GrantDeliveryStatus{}, err
	}
	var status GrantDeliveryStatus
	var deliveredAt, revokedAt, consumedAt sql.NullTime
	err := s.repository.pool.QueryRow(ctx, `
		SELECT admission.attempt_id, admission.jti, admission.delivered_at,
		       admission.expires_at, admission.revoked_at, admission.consumed_at
		FROM match_admission_grants AS admission
		WHERE admission.attempt_id = $1 AND admission.jti = $2
		  AND admission.player_id = $3
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

func (s *Service) MarkConnected(ctx context.Context, authorityID, authoritySession, attemptID, playerID, grantJTI string, generation int) (Snapshot, error) {
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
	grantJTI = strings.TrimSpace(grantJTI)
	if generation < 1 || grantJTI == "" {
		return Snapshot{}, invalid("Invalid connection generation.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID string
	var routeGeneration int
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, route_generation FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(&lobbyID, &routeGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the active match attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	grantCommand, err := tx.Exec(ctx, `
		UPDATE match_admission_grants
		SET consumed_at = $6
		WHERE jti = $1 AND attempt_id = $2 AND player_id = $3
		  AND connection_generation = $4 AND route_generation = $5
		  AND revoked_at IS NULL AND consumed_at IS NULL AND expires_at > $6
	`, grantJTI, attemptID, playerID, generation, routeGeneration, now)
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
				  AND roster.connection_generation = $4 AND roster.connection_state = 'CONNECTED'
			)
		`, grantJTI, attemptID, playerID, generation, routeGeneration).Scan(&repeated); err != nil {
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
		       updated_at = $4
		WHERE attempt_id = $1 AND player_id = $2 AND connection_generation = $3
	`, attemptID, playerID, generation, now)
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
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM match_attempt_roster WHERE attempt_id = $1 AND connection_state <> 'CONNECTED'`, attemptID).Scan(&missing); err != nil {
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

func (s *Service) P2PMarkConnected(ctx context.Context, actor Actor, authoritySession, attemptID, playerID, grantJTI string, generation int) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	snapshot, err := s.MarkConnected(ctx, actor.PlayerID, authoritySession, attemptID, playerID, grantJTI, generation)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx, snapshot.LobbyID, actor.PlayerID)
}

func (s *Service) MarkDisconnected(ctx context.Context, authorityID, authoritySession, attemptID, playerID string, generation int) (Snapshot, error) {
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
	if generation < 1 {
		return Snapshot{}, invalid("Invalid connection generation.", nil)
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lobbyID string
	err = tx.QueryRow(ctx, `
		SELECT lobby_id FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(&lobbyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SESSION_REQUIRED", "The authority session does not match the active attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE match_attempt_roster
		SET connection_state = 'DISCONNECTED', disconnected_at = $4, updated_at = $4
		WHERE attempt_id = $1 AND player_id = $2 AND connection_generation = $3
		  AND connection_state = 'CONNECTED'
	`, attemptID, playerID, generation, now)
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if command.RowsAffected() != 1 {
		var repeated bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM match_attempt_roster
				WHERE attempt_id = $1 AND player_id = $2
				  AND connection_generation = $3 AND connection_state = 'DISCONNECTED'
			)
		`, attemptID, playerID, generation).Scan(&repeated); err != nil {
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
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	return s.Get(ctx, lobbyID, "")
}

func (s *Service) P2PMarkDisconnected(ctx context.Context, actor Actor, authoritySession, attemptID, playerID string, generation int) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	snapshot, err := s.MarkDisconnected(ctx, actor.PlayerID, authoritySession, attemptID, playerID, generation)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx, snapshot.LobbyID, actor.PlayerID)
}

func (s *Service) AuthorityHeartbeat(ctx context.Context, authorityID, authoritySession, attemptID string) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var reconnecting bool
	err = tx.QueryRow(ctx, `
		SELECT host_reconnect_deadline IS NOT NULL
		FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND state IN ('CONNECTING', 'RUNNING')
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(&reconnecting)
	if errors.Is(err, pgx.ErrNoRows) {
		return forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "This authority is not assigned to the active match attempt.")
	}
	if err != nil {
		return internal(err)
	}
	if reconnecting {
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempts SET authority_last_seen_at = $3,
			       host_reconnect_deadline = NULL, route_generation = route_generation + 1,
			       payload_route_generation = NULL,
			       updated_at = $3 WHERE id = $1 AND authority_id = $2
		`, attemptID, authorityID, now); err != nil {
			return internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempt_roster SET connection_generation = connection_generation + 1,
			       connection_state = CASE WHEN connection_state = 'CONNECTED' THEN 'DISCONNECTED' ELSE connection_state END,
			       disconnected_at = CASE WHEN connection_state = 'CONNECTED' THEN $2 ELSE disconnected_at END,
			       updated_at = $2 WHERE attempt_id = $1
		`, attemptID, now); err != nil {
			return internal(err)
		}
		if err := s.syncProjectionGenerations(ctx, tx, attemptID); err != nil {
			return internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempt_roster SET connection_state = 'CONNECTED',
			       connected_at = COALESCE(connected_at, $3), disconnected_at = NULL,
			       updated_at = $3
			WHERE attempt_id = $1 AND player_id = $2 AND room_role = 'HOST'
		`, attemptID, authorityID, now); err != nil {
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
	if err := s.requireEnabled(); err != nil {
		return Snapshot{}, err
	}
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
	var metaMatchID, storedFailureCode string
	var currentState AttemptState
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, hosting_kind, COALESCE(meta_match_id, ''), state,
		       COALESCE(failure_code, '')
		FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		FOR UPDATE
	`, attemptID, authorityID, authoritySession).Scan(
		&lobbyID, &hosting, &metaMatchID, &currentState, &storedFailureCode,
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
		       completed_at = $5, updated_at = $5 WHERE id = $1 AND authority_id = $2
	`, attemptID, authorityID, attemptState, failureCode, now); err != nil {
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
		if _, err := tx.Exec(ctx, `UPDATE game_servers SET state = 'READY', player_count = 0, updated_at = $2 WHERE id = $1`, authorityID, now); err != nil {
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

func (s *Service) P2PComplete(ctx context.Context, actor Actor, authoritySession, attemptID string, success bool, failureCode string) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	return s.Complete(ctx, actor.PlayerID, authoritySession, attemptID, success, failureCode)
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

func (s *Service) requireEnabled() error {
	if !s.config.StrictRosterV1Enabled {
		return conflict("STRICT_ROSTER_V1_DISABLED", "Strict roster lobbies are disabled for the configured game binary.", nil)
	}
	return nil
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
