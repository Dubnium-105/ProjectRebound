package relayregistry

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/connection"
)

var relayLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type RoomDirectory interface {
	RelayRegion(context.Context, string) (string, error)
}

type ControlPublisher interface {
	Publish(string, ControlMessage)
}

type ConnectionCoordinator interface {
	RelayBound(context.Context, string, string) error
}

type Service struct {
	repository            *Repository
	authority             *Authority
	tokenManager          *RelayTokenManager
	roomDirectory         RoomDirectory
	controlPublisher      ControlPublisher
	connectionCoordinator ConnectionCoordinator
	config                config.RelayRegistryConfig
	now                   func() time.Time
}

func NewService(
	repository *Repository,
	authority *Authority,
	tokenManager *RelayTokenManager,
	roomDirectory RoomDirectory,
	cfg config.RelayRegistryConfig,
) *Service {
	return &Service{
		repository: repository, authority: authority, tokenManager: tokenManager,
		roomDirectory: roomDirectory, config: cfg, now: time.Now,
	}
}

func (s *Service) SetControlPublisher(publisher ControlPublisher) { s.controlPublisher = publisher }

func (s *Service) SetConnectionCoordinator(coordinator ConnectionCoordinator) {
	s.connectionCoordinator = coordinator
}

func (s *Service) Keyset() Keyset { return s.tokenManager.Keyset() }

func (s *Service) Initialize(ctx context.Context, credentials []BootstrapCredential) error {
	return s.repository.SyncBootstrapCredentials(ctx, credentials, s.now().UTC())
}

func (s *Service) Enroll(ctx context.Context, bootstrapToken string, input EnrollInput) (EnrollResult, error) {
	if len(strings.TrimSpace(bootstrapToken)) < 32 {
		return EnrollResult{}, unauthorized("BOOTSTRAP_UNAUTHORIZED", "A valid one-time bootstrap token is required.")
	}
	if err := validateEnroll(input); err != nil {
		return EnrollResult{}, err
	}
	nodeID := newID("relay_")
	certificatePEM, fingerprint, certificateExpiresAt, err := s.authority.IssueClientCertificate(
		nodeID, input.CSRPEM, s.config.CertificateTTL(),
	)
	if err != nil {
		return EnrollResult{}, err
	}
	nodeToken, nodeTokenHash, err := newOpaqueToken("rnt_")
	if err != nil {
		return EnrollResult{}, internal(err)
	}
	now := s.now().UTC()
	protocols := normalizeProtocols(input.SupportedProtocols)
	node := Node{
		ID: nodeID, DisplayName: strings.TrimSpace(input.DisplayName),
		Region: strings.TrimSpace(input.Region), Zone: strings.TrimSpace(input.Zone), Provider: strings.TrimSpace(input.Provider),
		State: StateBootstrapping, SoftwareVersion: strings.TrimSpace(input.SoftwareVersion), ProtocolVersion: input.ProtocolVersion,
		PublicEndpoints: input.PublicEndpoints, SupportedProtocols: protocols,
		MaxAllocations: input.MaxAllocations, MaxEgressBPS: input.MaxEgressBPS,
		CertificateFingerprint: fingerprint, CertificateExpiresAt: certificateExpiresAt,
		NodeTokenHash: nodeTokenHash, ConfigVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.Enroll(ctx, hashToken(strings.TrimSpace(bootstrapToken)), node); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EnrollResult{}, unauthorized("BOOTSTRAP_UNAUTHORIZED", "Bootstrap token is invalid or already consumed.")
		}
		return EnrollResult{}, internal(err)
	}
	return EnrollResult{
		Node: node, NodeToken: nodeToken, CertificatePEM: certificatePEM,
		CACertificatePEM: s.authority.CACertificatePEM(), CertificateExpiresAt: certificateExpiresAt,
		Keyset: s.tokenManager.Keyset(),
	}, nil
}

func (s *Service) RenewCertificate(ctx context.Context, nodeID, nodeToken, csrPEM string) (EnrollResult, error) {
	if !strings.HasPrefix(nodeToken, "rnt_") || len(nodeToken) < 64 {
		return EnrollResult{}, unauthorized("RELAY_NODE_UNAUTHORIZED", "Valid relay node credentials are required.")
	}
	if _, err := s.repository.AuthenticateNodeToken(ctx, nodeID, hashToken(nodeToken)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EnrollResult{}, unauthorized("RELAY_NODE_UNAUTHORIZED", "Valid relay node credentials are required.")
		}
		return EnrollResult{}, internal(err)
	}
	certificatePEM, fingerprint, expiresAt, err := s.authority.IssueClientCertificate(nodeID, csrPEM, s.config.CertificateTTL())
	if err != nil {
		return EnrollResult{}, err
	}
	node, err := s.repository.RenewCertificate(ctx, nodeID, hashToken(nodeToken), fingerprint, expiresAt, s.now().UTC())
	if err != nil {
		return EnrollResult{}, internal(err)
	}
	if s.controlPublisher != nil {
		s.controlPublisher.Publish(nodeID, ControlMessage{Type: "CertificateRotation", Payload: map[string]any{"certificate_expires_at": expiresAt}})
	}
	return EnrollResult{
		Node: node, CertificatePEM: certificatePEM, CACertificatePEM: s.authority.CACertificatePEM(),
		CertificateExpiresAt: expiresAt, Keyset: s.tokenManager.Keyset(),
	}, nil
}

func (s *Service) MarkConnecting(ctx context.Context, nodeID, fingerprint, softwareVersion string, protocolVersion int) (Node, error) {
	if nodeID == "" || fingerprint == "" || protocolVersion < 1 {
		return Node{}, unauthorized("RELAY_NODE_UNAUTHORIZED", "Relay node identity is invalid.")
	}
	now := s.now().UTC()
	node, err := s.repository.MarkConnecting(
		ctx, nodeID, fingerprint, strings.TrimSpace(softwareVersion), protocolVersion,
		now, now.Add(time.Duration(s.config.UnhealthyAfterSeconds)*time.Second),
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Node{}, unauthorized("RELAY_NODE_UNAUTHORIZED", "Relay node certificate is invalid, expired, or revoked.")
		}
		return Node{}, internal(err)
	}
	return node, nil
}

func (s *Service) Heartbeat(ctx context.Context, nodeID string, input HeartbeatInput) (Node, error) {
	if input.ActiveAllocations < 0 || input.CurrentEgressBPS < 0 || input.CurrentIngressBPS < 0 {
		return Node{}, invalid("Invalid relay heartbeat.", nil)
	}
	now := s.now().UTC()
	node, err := s.repository.Heartbeat(
		ctx, nodeID, input, now,
		now.Add(time.Duration(s.config.UnhealthyAfterSeconds)*time.Second),
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Node{}, conflict("INVALID_RELAY_STATE", "Relay node cannot accept this heartbeat.")
		}
		return Node{}, internal(err)
	}
	return node, nil
}

func (s *Service) AllocationOpened(ctx context.Context, nodeID, allocationID string) error {
	if strings.TrimSpace(allocationID) == "" {
		return invalid("allocation_id is required.", nil)
	}
	allocation, err := s.repository.AllocationOpened(ctx, nodeID, allocationID, s.now().UTC())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("ALLOCATION_NOT_FOUND", "Relay allocation was not assigned to this node.")
		}
		return internal(err)
	}
	if s.connectionCoordinator != nil {
		if err := s.connectionCoordinator.RelayBound(ctx, allocation.ConnectionID, allocation.ID); err != nil {
			return internal(err)
		}
	}
	return nil
}

func (s *Service) AllocationClosed(ctx context.Context, nodeID, allocationID string) error {
	if strings.TrimSpace(allocationID) == "" {
		return invalid("allocation_id is required.", nil)
	}
	if err := s.repository.AllocationClosed(ctx, nodeID, allocationID, s.now().UTC()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("ALLOCATION_NOT_FOUND", "Relay allocation was not assigned to this node.")
		}
		return internal(err)
	}
	return nil
}

func (s *Service) Drain(ctx context.Context, nodeID string, meta AdminMeta) (Node, error) {
	deadline := s.now().UTC().Add(time.Duration(s.config.DrainDeadlineSeconds) * time.Second)
	node, err := s.changeState(ctx, nodeID, StateDraining, &deadline, meta)
	if err == nil && s.controlPublisher != nil {
		s.controlPublisher.Publish(nodeID, ControlMessage{Type: "EnterDrain", Payload: map[string]any{"deadline": deadline}})
	}
	return node, err
}

func (s *Service) Resume(ctx context.Context, nodeID string, meta AdminMeta) (Node, error) {
	node, err := s.changeState(ctx, nodeID, StateReady, nil, meta)
	if err == nil && s.controlPublisher != nil {
		s.controlPublisher.Publish(nodeID, ControlMessage{Type: "ExitDrain", Payload: map[string]any{}})
	}
	return node, err
}

func (s *Service) Revoke(ctx context.Context, nodeID string, meta AdminMeta) (Node, error) {
	node, err := s.changeState(ctx, nodeID, StateRevoked, nil, meta)
	if err == nil && s.controlPublisher != nil {
		s.controlPublisher.Publish(nodeID, ControlMessage{Type: "Shutdown", Payload: map[string]any{"reason": "REVOKED"}})
	}
	return node, err
}

func (s *Service) changeState(ctx context.Context, nodeID string, next State, deadline *time.Time, meta AdminMeta) (Node, error) {
	if strings.TrimSpace(meta.ActorID) == "" {
		return Node{}, unauthorized("ADMIN_UNAUTHORIZED", "Administrator authentication is required.")
	}
	node, err := s.repository.Get(ctx, nodeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Node{}, notFound()
		}
		return Node{}, internal(err)
	}
	if node.State == StateRevoked {
		return Node{}, conflict("RELAY_NODE_REVOKED", "Revoked relay nodes cannot change state.")
	}
	if next == StateReady && node.State != StateDraining && node.State != StateUnhealthy && node.State != StateOffline {
		return Node{}, conflict("INVALID_RELAY_STATE", "Relay node cannot resume from its current state.")
	}
	if next == StateDraining && node.State != StateReady {
		return Node{}, conflict("INVALID_RELAY_STATE", "Only READY relay nodes can enter drain.")
	}
	updated, err := s.repository.ChangeState(ctx, nodeID, next, deadline, meta, s.now().UTC())
	if err != nil {
		return Node{}, internal(err)
	}
	return updated, nil
}

func (s *Service) Get(ctx context.Context, nodeID string) (Node, error) {
	node, err := s.repository.Get(ctx, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, notFound()
	}
	if err != nil {
		return Node{}, internal(err)
	}
	return node, nil
}

func (s *Service) SweepNodes(ctx context.Context) (int64, error) {
	return s.repository.SweepNodes(
		ctx, s.now().UTC(),
		time.Duration(s.config.UnhealthyAfterSeconds)*time.Second,
		time.Duration(s.config.OfflineAfterSeconds)*time.Second,
	)
}

func (s *Service) AllocateRelay(ctx context.Context, request connection.RelayAllocationRequest) (connection.RelayAllocation, error) {
	region, err := s.roomDirectory.RelayRegion(ctx, request.RoomID)
	if err != nil {
		return connection.RelayAllocation{}, internal(err)
	}
	now := s.now().UTC()
	allocation := Allocation{
		ID: newID("alloc_"), ConnectionID: request.ConnectionID, RoomID: request.RoomID,
		State: "ALLOCATED", Protocol: "UDP", MaxBPS: 256000, MaxPPS: 200,
		MaxTotalBytes: 268435456, ExpiresAt: now.Add(s.config.AllocationTTL()), CreatedAt: now, UpdatedAt: now,
	}
	allocation, node, _, err := s.repository.Schedule(ctx, allocation, region, s.config.CapacityThresholdPercent)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connection.RelayAllocation{}, unavailable("RELAY_UNAVAILABLE", "No healthy relay capacity is available.")
		}
		return connection.RelayAllocation{}, internal(err)
	}
	endpoint, ok := chooseEndpoint(node.PublicEndpoints, allocation.Protocol)
	if !ok {
		return connection.RelayAllocation{}, unavailable("RELAY_PROTOCOL_UNAVAILABLE", "Scheduled relay has no compatible endpoint.")
	}
	baseClaims := RelayClaims{
		RelayNodeID: node.ID, AllocationID: allocation.ID, ConnectionID: request.ConnectionID,
		RoomID: request.RoomID, Protocol: allocation.Protocol,
		MaxBPS: allocation.MaxBPS, MaxPPS: allocation.MaxPPS, MaxTotalBytes: allocation.MaxTotalBytes,
		AllocationExpiresAt: allocation.ExpiresAt.Unix(),
	}
	hostClaims := baseClaims
	hostClaims.EndpointRole = "HOST"
	hostToken, _, err := s.tokenManager.Sign(hostClaims, s.config.RelayTokenTTL())
	if err != nil {
		return connection.RelayAllocation{}, internal(err)
	}
	peerClaims := baseClaims
	peerClaims.EndpointRole = "PEER"
	peerToken, _, err := s.tokenManager.Sign(peerClaims, s.config.RelayTokenTTL())
	if err != nil {
		return connection.RelayAllocation{}, internal(err)
	}
	return connection.RelayAllocation{
		AllocationID: allocation.ID,
		Endpoint:     connection.RelayEndpoint{NodeID: node.ID, Protocol: strings.ToLower(endpoint.Protocol), Host: endpoint.Host, Port: endpoint.Port},
		HostToken:    hostToken, PeerToken: peerToken, ExpiresAt: allocation.ExpiresAt,
	}, nil
}

func validateEnroll(input EnrollInput) error {
	details := make(map[string]any)
	for name, value := range map[string]string{
		"display_name": input.DisplayName, "region": input.Region, "zone": input.Zone,
		"provider": input.Provider, "software_version": input.SoftwareVersion,
	} {
		if !relayLabelPattern.MatchString(strings.TrimSpace(value)) {
			details[name] = "contains unsupported characters or has invalid length"
		}
	}
	if input.ProtocolVersion < 1 {
		details["protocol_version"] = "must be positive"
	}
	if input.MaxAllocations < 1 || input.MaxAllocations > 1_000_000 {
		details["max_allocations"] = "must be between 1 and 1000000"
	}
	if input.MaxEgressBPS < 1 {
		details["max_egress_bps"] = "must be positive"
	}
	if len(input.PublicEndpoints) == 0 {
		details["public_endpoints"] = "at least one endpoint is required"
	}
	for index, endpoint := range input.PublicEndpoints {
		protocol := strings.ToUpper(strings.TrimSpace(endpoint.Protocol))
		address, err := netip.ParseAddr(strings.TrimSpace(endpoint.Host))
		if (protocol != "UDP" && protocol != "TCP_TLS") || err != nil || !address.IsGlobalUnicast() || address.IsPrivate() || endpoint.Port < 1 || endpoint.Port > 65535 {
			details["public_endpoints"] = "contains an invalid public endpoint at index " + strconv.Itoa(index)
		}
		input.PublicEndpoints[index].Protocol = protocol
		if err == nil {
			input.PublicEndpoints[index].Host = address.Unmap().String()
		}
	}
	protocols := normalizeProtocols(input.SupportedProtocols)
	if len(protocols) == 0 {
		details["supported_protocols"] = "at least one supported protocol is required"
	} else if len(protocols) != len(input.SupportedProtocols) {
		details["supported_protocols"] = "contains duplicates or unsupported protocols"
	}
	if strings.TrimSpace(input.CSRPEM) == "" {
		details["csr_pem"] = "is required"
	}
	if len(details) > 0 {
		return invalid("Invalid relay enrollment.", details)
	}
	return nil
}

func normalizeProtocols(protocols []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		protocol = strings.ToUpper(strings.TrimSpace(protocol))
		if (protocol == "UDP" || protocol == "TCP_TLS") && !seen[protocol] {
			result = append(result, protocol)
			seen[protocol] = true
		}
	}
	sort.Strings(result)
	return result
}

func chooseEndpoint(endpoints []Endpoint, protocol string) (Endpoint, bool) {
	for _, endpoint := range endpoints {
		if strings.EqualFold(endpoint.Protocol, protocol) {
			return endpoint, true
		}
	}
	return Endpoint{}, false
}

func newID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}
