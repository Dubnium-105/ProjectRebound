package vnt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/entitlement"
	"github.com/jackc/pgx/v5"
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Service struct {
	repository   *Repository
	entitlements interface {
		Has(context.Context, string, string) (bool, error)
	}
	now               func() time.Time
	enrollmentTTL     time.Duration
	credentialTTL     time.Duration
	heartbeatInterval int
	probeTimeout      time.Duration
}

func NewService(repository *Repository, entitlements interface {
	Has(context.Context, string, string) (bool, error)
}) *Service {
	return &Service{
		repository: repository, entitlements: entitlements, now: time.Now,
		enrollmentTTL: 10 * time.Minute, credentialTTL: 90 * 24 * time.Hour,
		heartbeatInterval: 30,
		probeTimeout:      2 * time.Second,
	}
}

func (s *Service) CreateEnrollment(ctx context.Context, actor Actor, label string) (EnrollmentResult, error) {
	if actor.PlayerID == "" || actor.AccountStatus != "ACTIVE" || !actor.SteamVerified {
		return EnrollmentResult{}, serviceError(http.StatusForbidden, "VNT_NODE_ENROLLMENT_FORBIDDEN", "A verified active player is required.")
	}
	if s.entitlements == nil {
		return EnrollmentResult{}, internal(errors.New("entitlement repository is not configured"))
	}
	allowed, err := s.entitlements.Has(ctx, actor.PlayerID, entitlement.VNTNodeRegistration)
	if err != nil {
		return EnrollmentResult{}, internal(err)
	}
	if !allowed {
		return EnrollmentResult{}, serviceError(http.StatusForbidden, "VNT_NODE_REGISTRATION_NOT_ALLOWED", "This player is not allowed to register VNT nodes.")
	}
	label = strings.TrimSpace(label)
	if !labelPattern.MatchString(label) {
		return EnrollmentResult{}, serviceError(http.StatusBadRequest, "INVALID_REQUEST", "Invalid VNT node label.")
	}
	secret, hash, err := newSecret("vne_")
	if err != nil {
		return EnrollmentResult{}, internal(err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.enrollmentTTL)
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return EnrollmentResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.InsertEnrollment(ctx, tx, newID("vne_"), actor.PlayerID, label, hash, expiresAt, now); err != nil {
		return EnrollmentResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return EnrollmentResult{}, internal(err)
	}
	return EnrollmentResult{Code: secret, ExpiresAt: expiresAt}, nil
}

func (s *Service) Register(ctx context.Context, enrollmentCode string, input RegisterInput) (RegisterResult, error) {
	if err := validateRegisterInput(&input); err != nil {
		return RegisterResult{}, err
	}
	if !strings.HasPrefix(enrollmentCode, "vne_") {
		return RegisterResult{}, serviceError(401, "VNT_ENROLLMENT_INVALID", "Invalid or expired VNT enrollment code.")
	}
	nodeToken, nodeHash, err := newSecret("vnn_")
	if err != nil {
		return RegisterResult{}, internal(err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.credentialTTL)
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return RegisterResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	ownerID, err := s.repository.ConsumeEnrollment(ctx, tx, hashSecret(enrollmentCode), now)
	if err != nil {
		return RegisterResult{}, mapRepositoryError(err)
	}
	node := Node{
		ID: newID("vnt_"), OwnerPlayerID: ownerID, AdvertisedHost: input.AdvertisedHost,
		Port: input.Port, Region: input.Region, Location: input.Location,
		State: StateRegistering, VNTSVersion: input.VNTSVersion,
		WrapperVersion: input.WrapperVersion, ServerKeyFingerprint: input.ServerKeyFingerprint,
		SupportedTransports: input.SupportedTransports, MaxRooms: input.MaxRooms,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.InsertNode(ctx, tx, node, newID("vnc_"), nodeHash, expiresAt); err != nil {
		return RegisterResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RegisterResult{}, internal(err)
	}
	return RegisterResult{
		NodeID: node.ID, NodeToken: nodeToken, State: node.State,
		HeartbeatIntervalSeconds: s.heartbeatInterval, CredentialExpiresAt: expiresAt,
	}, nil
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]PublicNode, error) {
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if filter.Status == "" {
		filter.Status = StateOnline
	}
	filter.Region = strings.TrimSpace(filter.Region)
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	if filter.Limit < 1 || filter.Limit > 200 || !validState(filter.Status) {
		return nil, serviceError(400, "INVALID_REQUEST", "Invalid VNT node filter.")
	}
	nodes, err := s.repository.List(ctx, filter)
	if err != nil {
		return nil, internal(err)
	}
	result := make([]PublicNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.Public())
	}
	return result, nil
}

func (s *Service) Heartbeat(ctx context.Context, nodeID, nodeToken string, input HeartbeatInput) error {
	input.WrapperVersion = strings.TrimSpace(input.WrapperVersion)
	input.VNTSVersion = strings.TrimSpace(input.VNTSVersion)
	if input.UptimeSeconds < 0 || input.ReportedSessions < 0 || input.WrapperVersion == "" ||
		input.VNTSVersion == "" || len(input.WrapperVersion) > 32 || len(input.VNTSVersion) > 32 {
		return serviceError(400, "INVALID_REQUEST", "Invalid VNT heartbeat.")
	}
	now := s.now().UTC()
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.AuthenticateCredential(ctx, tx, nodeID, hashSecret(nodeToken), now); err != nil {
		return mapRepositoryError(err)
	}
	state := StateRegistering
	if !input.ServerProcessHealthy {
		state = StateOffline
	}
	if err := s.repository.Heartbeat(ctx, tx, nodeID, input, state, now); err != nil {
		return mapRepositoryError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return internal(err)
	}
	if input.ServerProcessHealthy {
		host, port, err := s.repository.Endpoint(ctx, nodeID)
		if err != nil {
			return mapRepositoryError(err)
		}
		connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), s.probeTimeout)
		if err == nil {
			_ = connection.Close()
			if err := s.repository.MarkReachable(ctx, nodeID, now); err != nil {
				return mapRepositoryError(err)
			}
		}
	}
	return nil
}

func (s *Service) Sweep(ctx context.Context) (int64, error) {
	count, err := s.repository.Sweep(ctx, s.now().UTC(), 90*time.Second, 5*time.Minute)
	if err != nil {
		return 0, internal(err)
	}
	return count, nil
}

func (s *Service) Retire(ctx context.Context, nodeID, nodeToken string) (string, error) {
	now := s.now().UTC()
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return "", internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.AuthenticateCredential(ctx, tx, nodeID, hashSecret(nodeToken), now); err != nil {
		return "", mapRepositoryError(err)
	}
	state, err := s.repository.Retire(ctx, tx, nodeID, now)
	if err != nil {
		return "", mapRepositoryError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", internal(err)
	}
	return state, nil
}

func (s *Service) RotateCredential(ctx context.Context, nodeID, nodeToken string) (CredentialRotationResult, error) {
	newToken, newHash, err := newSecret("vnn_")
	if err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.credentialTTL)
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.RotateCredential(ctx, tx, nodeID, hashSecret(nodeToken), newID("vnc_"), newHash, expiresAt, now); err != nil {
		return CredentialRotationResult{}, mapRepositoryError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	return CredentialRotationResult{NodeToken: newToken, CredentialExpiresAt: expiresAt}, nil
}

func validateRegisterInput(input *RegisterInput) error {
	input.AdvertisedHost = strings.TrimSpace(input.AdvertisedHost)
	input.Region = strings.TrimSpace(input.Region)
	input.Location = strings.TrimSpace(input.Location)
	input.VNTSVersion = strings.TrimSpace(input.VNTSVersion)
	input.WrapperVersion = strings.TrimSpace(input.WrapperVersion)
	input.ServerKeyFingerprint = strings.TrimSpace(input.ServerKeyFingerprint)
	if input.Port < 1024 || input.Port > 65535 || input.MaxRooms < 1 || input.MaxRooms > 10000 ||
		input.AdvertisedHost == "" || len(input.AdvertisedHost) > 253 || !labelPattern.MatchString(input.Region) ||
		input.Location == "" || len(input.Location) > 128 || input.VNTSVersion == "" || len(input.VNTSVersion) > 32 ||
		input.WrapperVersion == "" || len(input.WrapperVersion) > 32 || input.ServerKeyFingerprint == "" ||
		len(input.ServerKeyFingerprint) > 128 {
		return serviceError(400, "INVALID_REQUEST", "Invalid VNT node registration.")
	}
	ip := net.ParseIP(input.AdvertisedHost)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return serviceError(400, "INVALID_REQUEST", "VNT advertised_host must be publicly reachable.")
	}
	if len(input.SupportedTransports) == 0 {
		input.SupportedTransports = []string{"udp", "tcp"}
	}
	for index, transport := range input.SupportedTransports {
		transport = strings.ToLower(strings.TrimSpace(transport))
		if transport != "udp" && transport != "tcp" {
			return serviceError(400, "INVALID_REQUEST", "Unsupported VNT node transport.")
		}
		input.SupportedTransports[index] = transport
	}
	slices.Sort(input.SupportedTransports)
	input.SupportedTransports = slices.Compact(input.SupportedTransports)
	if !slices.Contains(input.SupportedTransports, "udp") || !slices.Contains(input.SupportedTransports, "tcp") {
		return serviceError(400, "INVALID_REQUEST", "VNT nodes must support UDP data traffic and TCP reachability probes.")
	}
	fingerprint := strings.ToLower(input.ServerKeyFingerprint)
	fingerprint = strings.TrimPrefix(fingerprint, "sha256:")
	fingerprint = strings.TrimPrefix(fingerprint, "sha256-")
	fingerprint = strings.NewReplacer(":", "", "-", "", " ", "").Replace(fingerprint)
	if len(fingerprint) != 64 || strings.IndexFunc(fingerprint, func(value rune) bool {
		return !(value >= '0' && value <= '9') && !(value >= 'a' && value <= 'f')
	}) >= 0 {
		return serviceError(400, "INVALID_REQUEST", "VNT server key fingerprint must be a complete SHA-256 digest.")
	}
	input.ServerKeyFingerprint = "sha256:" + fingerprint
	return nil
}

func validState(value string) bool {
	return slices.Contains([]string{StateRegistering, StateOnline, StateStale, StateOffline, StateDraining, StateRevoked, StateRetired}, value)
}

func mapRepositoryError(err error) error {
	var target *ServiceError
	if errors.As(err, &target) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return serviceError(404, "VNT_NODE_NOT_FOUND", "VNT node not found.")
	}
	return internal(err)
}
