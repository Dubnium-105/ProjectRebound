package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/player"
)

var foundationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type RoomAuthorizer interface {
	ResolveConnectionParticipants(context.Context, string, string, string) (string, string, error)
	MarkConnectionEstablished(context.Context, string) error
}

type EventPublisher interface {
	Publish([]string, Event)
}

type RelayAllocator interface {
	AllocateRelay(context.Context, RelayAllocationRequest) (RelayAllocation, error)
}

type Service struct {
	repository     *Repository
	roomAuthorizer RoomAuthorizer
	publisher      EventPublisher
	config         config.ConnectionConfig
	relayAllocator RelayAllocator
	now            func() time.Time
}

func (s *Service) SetRelayAllocator(allocator RelayAllocator) {
	s.relayAllocator = allocator
}

func NewService(repository *Repository, roomAuthorizer RoomAuthorizer, publisher EventPublisher, cfg config.ConnectionConfig) *Service {
	return &Service{
		repository: repository, roomAuthorizer: roomAuthorizer, publisher: publisher,
		config: cfg, now: time.Now,
	}
}

func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (Connection, error) {
	if err := requireActive(actor); err != nil {
		return Connection{}, err
	}
	if strings.TrimSpace(input.RoomID) == "" {
		return Connection{}, invalid("Invalid connection request.", map[string]any{"room_id": "is required"})
	}
	hostPlayerID, peerPlayerID, err := s.roomAuthorizer.ResolveConnectionParticipants(
		ctx, strings.TrimSpace(input.RoomID), actor.PlayerID, strings.TrimSpace(input.PeerPlayerID),
	)
	if err != nil {
		return Connection{}, mapDependencyError(err)
	}
	return s.ensure(ctx, input.RoomID, hostPlayerID, peerPlayerID)
}

// EnsureForRoomPeer lets the P2P room service create a connection immediately
// after a successful join without reaching into this module's tables.
func (s *Service) EnsureForRoomPeer(ctx context.Context, roomID, hostPlayerID, peerPlayerID string) error {
	_, err := s.ensure(ctx, roomID, hostPlayerID, peerPlayerID)
	return err
}

func (s *Service) CloseForRoom(ctx context.Context, roomID, reason string) error {
	items, err := s.repository.CloseForRoom(ctx, roomID, truncate(strings.TrimSpace(reason), 128), s.now().UTC())
	if err != nil {
		return err
	}
	for _, item := range items {
		s.publish(item, Event{Type: "connection.closed", Payload: connectionEventPayload(item), CreatedAt: s.now().UTC()})
	}
	return nil
}

func (s *Service) RelayBound(ctx context.Context, connectionID, allocationID string) error {
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := s.repository.GetForUpdate(ctx, tx, connectionID)
	if err != nil {
		return err
	}
	if item.State == StateConnected && item.SelectedPath == PathUDPRelay {
		return tx.Commit(ctx)
	}
	if item.State != StateRelayBinding {
		return conflict("INVALID_CONNECTION_STATE", "Connection is not binding a relay path.")
	}
	item, err = s.repository.UpdateState(ctx, tx, item.ID, StateConnected, PathUDPRelay, "", s.now().UTC())
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if err := s.roomAuthorizer.MarkConnectionEstablished(ctx, item.RoomID); err != nil {
		return err
	}
	s.publish(item, Event{Type: "connection.path_selected", Payload: map[string]any{
		"connection_id": item.ID, "allocation_id": allocationID,
		"state": item.State, "selected_path": item.SelectedPath,
	}, CreatedAt: s.now().UTC()})
	return nil
}

func (s *Service) ensure(ctx context.Context, roomID, hostPlayerID, peerPlayerID string) (Connection, error) {
	if roomID == "" || hostPlayerID == "" || peerPlayerID == "" || hostPlayerID == peerPlayerID {
		return Connection{}, invalid("Invalid connection participants.", nil)
	}
	now := s.now().UTC()
	item, created, err := s.repository.CreateOrGet(ctx, Connection{
		ID: newID("conn_"), RoomID: roomID, HostPlayerID: hostPlayerID, PeerPlayerID: peerPlayerID,
		State: StateCreated, ExpiresAt: now.Add(s.config.SessionTTL()), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Connection{}, internal(err)
	}
	if created {
		s.publish(item, Event{Type: "connection.created", Payload: connectionEventPayload(item), CreatedAt: now})
	}
	return item, nil
}

func (s *Service) Get(ctx context.Context, actor Actor, connectionID string) (Connection, error) {
	if actor.PlayerID == "" {
		return Connection{}, forbidden("CONNECTION_FORBIDDEN", "Connection access is restricted to its participants.")
	}
	item, err := s.repository.Get(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, notFound()
		}
		return Connection{}, internal(err)
	}
	if !isParticipant(item, actor.PlayerID) {
		return Connection{}, forbidden("CONNECTION_FORBIDDEN", "Connection access is restricted to its participants.")
	}
	return item, nil
}

func (s *Service) Close(ctx context.Context, actor Actor, connectionID string) (Connection, error) {
	if err := requireActive(actor); err != nil {
		return Connection{}, err
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Connection{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := s.repository.GetForUpdate(ctx, tx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, notFound()
		}
		return Connection{}, internal(err)
	}
	if !isParticipant(item, actor.PlayerID) {
		return Connection{}, forbidden("CONNECTION_FORBIDDEN", "Connection access is restricted to its participants.")
	}
	changed := item.State != StateClosed
	if item.State != StateClosed {
		item, err = s.repository.UpdateState(ctx, tx, item.ID, StateClosed, "", "CLIENT_CLOSED", s.now().UTC())
		if err != nil {
			return Connection{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, internal(err)
	}
	if changed {
		s.publish(item, Event{Type: "connection.closed", Payload: connectionEventPayload(item), CreatedAt: s.now().UTC()})
	}
	return item, nil
}

func (s *Service) AddCandidate(ctx context.Context, actor Actor, input CandidateInput) (Candidate, error) {
	if err := requireActive(actor); err != nil {
		return Candidate{}, err
	}
	validated, err := validateCandidate(input)
	if err != nil {
		return Candidate{}, err
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Candidate{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := s.repository.GetForUpdate(ctx, tx, validated.ConnectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Candidate{}, notFound()
		}
		return Candidate{}, internal(err)
	}
	if !isParticipant(item, actor.PlayerID) {
		return Candidate{}, forbidden("CONNECTION_FORBIDDEN", "Connection access is restricted to its participants.")
	}
	if item.State != StateCreated && item.State != StateGatheringCandidates && item.State != StateCheckingDirect {
		return Candidate{}, conflict("INVALID_CONNECTION_STATE", "Connection is not gathering direct candidates.")
	}
	now := s.now().UTC()
	candidate, err := s.repository.UpsertCandidate(ctx, tx, Candidate{
		ID: newID("cand_"), ConnectionID: item.ID, PlayerID: actor.PlayerID,
		Foundation: validated.Foundation, CandidateType: validated.CandidateType,
		Protocol: validated.Protocol, Address: validated.Address, Port: validated.Port,
		Priority: validated.Priority, CreatedAt: now,
	})
	if err != nil {
		return Candidate{}, internal(err)
	}
	participantCount, err := s.repository.CandidateParticipantCount(ctx, tx, item.ID)
	if err != nil {
		return Candidate{}, internal(err)
	}
	nextState := StateGatheringCandidates
	if participantCount == 2 {
		nextState = StateCheckingDirect
	}
	item, err = s.repository.UpdateState(ctx, tx, item.ID, nextState, "", "", now)
	if err != nil {
		return Candidate{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Candidate{}, internal(err)
	}
	s.publish(item, Event{Type: "connection.candidate", Payload: map[string]any{
		"connection_id": item.ID, "candidate": candidate,
	}, CreatedAt: now})
	return candidate, nil
}

func (s *Service) ReportCheck(ctx context.Context, actor Actor, input CheckResultInput) (Connection, error) {
	if err := requireActive(actor); err != nil {
		return Connection{}, err
	}
	if strings.TrimSpace(input.ConnectionID) == "" || input.LatencyMS < 0 || input.LatencyMS > 60_000 {
		return Connection{}, invalid("Invalid direct-check result.", nil)
	}
	if !isDirectPath(input.Path) {
		return Connection{}, invalid("Direct checks require a LAN, IPv6, or UDP punch path.", nil)
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Connection{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := s.repository.GetForUpdate(ctx, tx, input.ConnectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, notFound()
		}
		return Connection{}, internal(err)
	}
	if !isParticipant(item, actor.PlayerID) {
		return Connection{}, forbidden("CONNECTION_FORBIDDEN", "Connection access is restricted to its participants.")
	}
	if item.State != StateCheckingDirect && item.State != StateGatheringCandidates {
		if input.Success && item.State == StateConnected && item.SelectedPath == input.Path {
			if err := tx.Commit(ctx); err != nil {
				return Connection{}, internal(err)
			}
			if err := s.roomAuthorizer.MarkConnectionEstablished(ctx, item.RoomID); err != nil {
				return Connection{}, mapDependencyError(err)
			}
			return item, nil
		}
		return Connection{}, conflict("INVALID_CONNECTION_STATE", "Connection is not checking a direct path.")
	}
	expectedPath, err := s.nextDirectPath(ctx, tx, item.ID)
	if err != nil {
		return Connection{}, internal(err)
	}
	if expectedPath == "" {
		return Connection{}, conflict("NO_DIRECT_PATH", "No mutually usable direct candidate path is available.")
	}
	if input.Path != expectedPath {
		return Connection{}, &ServiceError{
			Status: 409, Code: "PATH_PRIORITY_VIOLATION", Message: "Direct paths must be checked in priority order.",
			Details: map[string]any{"expected_path": expectedPath},
		}
	}
	now := s.now().UTC()
	nextState := StateCheckingDirect
	selectedPath := Path("")
	failureReason := truncate(strings.TrimSpace(input.Reason), 128)
	if failureReason == "" && !input.Success {
		failureReason = "DIRECT_CHECK_FAILED"
	}
	if input.Success {
		nextState = StateConnected
		selectedPath = input.Path
		failureReason = ""
	}
	if err := s.repository.RecordPathCheck(ctx, tx, item.ID, actor.PlayerID, input, failureReason, now); err != nil {
		return Connection{}, internal(err)
	}
	if !input.Success {
		nextPath, err := s.nextDirectPath(ctx, tx, item.ID)
		if err != nil {
			return Connection{}, internal(err)
		}
		if nextPath == "" {
			nextState = StateAllocatingRelay
		}
	}
	item, err = s.repository.UpdateState(ctx, tx, item.ID, nextState, selectedPath, failureReason, now)
	if err != nil {
		return Connection{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, internal(err)
	}
	if input.Success {
		if err := s.roomAuthorizer.MarkConnectionEstablished(ctx, item.RoomID); err != nil {
			return Connection{}, mapDependencyError(err)
		}
	}
	s.publish(item, Event{Type: "connection.check_result", Payload: map[string]any{
		"connection_id": item.ID, "reported_by": actor.PlayerID, "success": input.Success,
		"path": input.Path, "latency_ms": input.LatencyMS, "reason": failureReason,
	}, CreatedAt: now})
	eventType := "connection.path_failed"
	if input.Success {
		eventType = "connection.path_selected"
	}
	s.publish(item, Event{Type: eventType, Payload: connectionEventPayload(item), CreatedAt: now})
	if item.State == StateAllocatingRelay && s.relayAllocator != nil {
		item = s.allocateRelay(ctx, item)
	}
	return item, nil
}

func (s *Service) allocateRelay(ctx context.Context, item Connection) Connection {
	allocation, err := s.relayAllocator.AllocateRelay(ctx, RelayAllocationRequest{
		ConnectionID: item.ID, RoomID: item.RoomID,
		HostPlayerID: item.HostPlayerID, PeerPlayerID: item.PeerPlayerID,
	})
	if err != nil {
		event := Event{Type: "connection.relay_failed", Payload: map[string]any{
			"connection_id": item.ID, "code": "RELAY_UNAVAILABLE",
		}, CreatedAt: s.now().UTC()}
		s.publish(item, event)
		return item
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return item
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	locked, err := s.repository.GetForUpdate(ctx, tx, item.ID)
	if err != nil || locked.State != StateAllocatingRelay {
		return item
	}
	updated, err := s.repository.UpdateState(ctx, tx, item.ID, StateRelayBinding, PathUDPRelay, "", s.now().UTC())
	if err != nil || tx.Commit(ctx) != nil {
		return item
	}
	basePayload := map[string]any{
		"connection_id": item.ID, "allocation_id": allocation.AllocationID,
		"relay": allocation.Endpoint, "expires_at": allocation.ExpiresAt,
	}
	hostPayload := cloneMap(basePayload)
	hostPayload["relay_token"] = allocation.HostToken
	peerPayload := cloneMap(basePayload)
	peerPayload["relay_token"] = allocation.PeerToken
	if s.publisher != nil {
		s.publisher.Publish([]string{item.HostPlayerID}, Event{Type: "connection.relay_allocated", Payload: hostPayload, CreatedAt: s.now().UTC()})
		s.publisher.Publish([]string{item.PeerPlayerID}, Event{Type: "connection.relay_allocated", Payload: peerPayload, CreatedAt: s.now().UTC()})
	}
	return updated
}

func (s *Service) HandleRealtime(ctx context.Context, actor Actor, incoming IncomingEvent) error {
	switch incoming.Type {
	case "connection.candidate":
		var input CandidateInput
		if err := decodeStrict(incoming.Payload, &input); err != nil {
			return invalid("Invalid candidate event.", map[string]any{"payload": err.Error()})
		}
		_, err := s.AddCandidate(ctx, actor, input)
		return err
	case "connection.check_result":
		var input CheckResultInput
		if err := decodeStrict(incoming.Payload, &input); err != nil {
			return invalid("Invalid check-result event.", map[string]any{"payload": err.Error()})
		}
		_, err := s.ReportCheck(ctx, actor, input)
		return err
	default:
		return invalid("Unsupported realtime event type.", map[string]any{"type": incoming.Type})
	}
}

func (s *Service) SweepExpired(ctx context.Context) (int, error) {
	items, err := s.repository.SweepExpired(ctx, s.now().UTC())
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		s.publish(item, Event{Type: "connection.closed", Payload: connectionEventPayload(item), CreatedAt: s.now().UTC()})
	}
	return len(items), nil
}

func (s *Service) publish(item Connection, event Event) {
	if s.publisher != nil {
		s.publisher.Publish([]string{item.HostPlayerID, item.PeerPlayerID}, event)
	}
}

func requireActive(actor Actor) error {
	if actor.PlayerID == "" {
		return &ServiceError{Status: 401, Code: "UNAUTHORIZED", Message: "Authentication is required."}
	}
	if actor.AccountStatus != player.AccountStatusActive {
		return forbidden("ACCOUNT_NOT_ACTIVE", "An active account is required.")
	}
	return nil
}

func validateCandidate(input CandidateInput) (CandidateInput, error) {
	details := make(map[string]any)
	input.ConnectionID = strings.TrimSpace(input.ConnectionID)
	input.Foundation = strings.TrimSpace(input.Foundation)
	input.CandidateType = CandidateType(strings.ToUpper(strings.TrimSpace(string(input.CandidateType))))
	input.Protocol = strings.ToUpper(strings.TrimSpace(input.Protocol))
	if input.ConnectionID == "" {
		details["connection_id"] = "is required"
	}
	if !foundationPattern.MatchString(input.Foundation) {
		details["foundation"] = "contains unsupported characters or has invalid length"
	}
	if input.CandidateType != CandidateLAN && input.CandidateType != CandidateIPv6 && input.CandidateType != CandidateSRFLX {
		details["candidate_type"] = "must be LAN, IPV6, or SRFLX"
	}
	if input.Protocol != "UDP" && input.Protocol != "TCP" {
		details["protocol"] = "must be UDP or TCP"
	}
	if input.Port < 1 || input.Port > 65535 {
		details["port"] = "must be between 1 and 65535"
	}
	if input.Priority < 1 || input.Priority > 2_147_483_647 {
		details["priority"] = "must be between 1 and 2147483647"
	}
	address, err := netip.ParseAddr(strings.TrimSpace(input.Address))
	if err != nil || !address.IsValid() || address.IsUnspecified() || address.IsMulticast() || address.IsLoopback() {
		details["address"] = "must be a valid unicast IP address"
	} else {
		address = address.Unmap()
		input.Address = address.String()
		switch input.CandidateType {
		case CandidateLAN:
			if !address.Is4() || !address.IsPrivate() {
				details["address"] = "LAN candidates require a private IPv4 address"
			}
		case CandidateIPv6:
			if !address.Is6() || address.IsLinkLocalUnicast() {
				details["address"] = "IPV6 candidates require a routable IPv6 address"
			}
		case CandidateSRFLX:
			if address.IsPrivate() || address.IsLinkLocalUnicast() {
				details["address"] = "SRFLX candidates require a public unicast address"
			}
		}
	}
	if len(details) > 0 {
		return CandidateInput{}, invalid("Invalid connection candidate.", details)
	}
	return input, nil
}

func isParticipant(item Connection, playerID string) bool {
	return item.HostPlayerID == playerID || item.PeerPlayerID == playerID
}

func isDirectPath(path Path) bool {
	return path == PathLAN || path == PathIPv6 || path == PathUDPPunch
}

func (s *Service) nextDirectPath(ctx context.Context, tx pgx.Tx, connectionID string) (Path, error) {
	eligible, err := s.repository.EligibleCandidateTypes(ctx, tx, connectionID)
	if err != nil {
		return "", err
	}
	attempted, err := s.repository.AttemptedPaths(ctx, tx, connectionID)
	if err != nil {
		return "", err
	}
	ordered := []struct {
		path          Path
		candidateType CandidateType
	}{
		{PathLAN, CandidateLAN},
		{PathIPv6, CandidateIPv6},
		{PathUDPPunch, CandidateSRFLX},
	}
	for _, candidate := range ordered {
		if eligible[candidate.candidateType] && !attempted[candidate.path] {
			return candidate.path, nil
		}
	}
	return "", nil
}

func connectionEventPayload(item Connection) map[string]any {
	return map[string]any{
		"connection_id": item.ID, "room_id": item.RoomID,
		"host_player_id": item.HostPlayerID, "peer_player_id": item.PeerPlayerID,
		"state": item.State, "selected_path": item.SelectedPath,
		"failure_reason": item.FailureReason, "expires_at": item.ExpiresAt,
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func mapDependencyError(err error) error {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return err
	}
	type detailedError interface {
		ErrorDetails() (int, string, string, map[string]any)
	}
	var detailed detailedError
	if errors.As(err, &detailed) {
		status, code, message, details := detailed.ErrorDetails()
		return &ServiceError{Status: status, Code: code, Message: message, Details: details}
	}
	return internal(err)
}

func newID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func cloneMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source)+1)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
