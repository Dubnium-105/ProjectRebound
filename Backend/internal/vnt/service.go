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

var (
	labelPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	nodeCursorPattern = regexp.MustCompile(`^vnt_[A-Za-z0-9]+$`)
)

type Service struct {
	repository   *Repository
	entitlements interface {
		Has(context.Context, string, string) (bool, error)
	}
	now               func() time.Time
	enrollmentTTL     time.Duration
	credentialTTL     time.Duration
	rotationGrace     time.Duration
	heartbeatInterval int
	probeTimeout      time.Duration
	versionPolicy     *VersionPolicy
	limiter           interface {
		Check(context.Context, LimitOperation, string) LimitDecision
	}
	maxNodesPerPlayer int
}

func (s *Service) SetVersionPolicy(policy *VersionPolicy) {
	s.versionPolicy = policy
}

func (s *Service) SetCredentialRotationGrace(grace time.Duration) {
	if grace > 0 {
		s.rotationGrace = grace
	}
}

func (s *Service) SetLimiter(limiter interface {
	Check(context.Context, LimitOperation, string) LimitDecision
}) {
	s.limiter = limiter
}

func (s *Service) SetMaxNodesPerPlayer(maximum int) {
	if maximum > 0 {
		s.maxNodesPerPlayer = maximum
	}
}

func NewService(repository *Repository, entitlements interface {
	Has(context.Context, string, string) (bool, error)
}) *Service {
	return &Service{
		repository: repository, entitlements: entitlements, now: time.Now,
		enrollmentTTL: 10 * time.Minute, credentialTTL: 90 * 24 * time.Hour,
		rotationGrace:     60 * time.Second,
		maxNodesPerPlayer: 3,
		heartbeatInterval: 30,
		probeTimeout:      2 * time.Second,
	}
}

func (s *Service) CreateEnrollment(ctx context.Context, actor Actor, label string) (result EnrollmentResult, resultErr error) {
	defer func() {
		if resultErr != nil {
			s.recordSecurityFailure(ctx, "VNT_ENROLLMENT_REJECTED", AuditActorPlayer, actor.PlayerID, "", "", resultErr, nil)
		}
	}()
	if actor.PlayerID == "" || actor.AccountStatus != "ACTIVE" || !actor.SteamVerified || !actor.IntegrityTrusted {
		return EnrollmentResult{}, serviceError(http.StatusForbidden, "VNT_NODE_ENROLLMENT_FORBIDDEN", "An active, Steam-verified, integrity-trusted player is required.")
	}
	if err := s.checkLimit(ctx, LimitEnrollment, actor.PlayerID); err != nil {
		return EnrollmentResult{}, err
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
	enrollmentID := newID("vne_")
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return EnrollmentResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.EnsureOwnerQuota(ctx, tx, actor.PlayerID, s.maxNodesPerPlayer); err != nil {
		return EnrollmentResult{}, mapRepositoryError(err)
	}
	if err := s.repository.InsertEnrollment(ctx, tx, enrollmentID, actor.PlayerID, label, hash, expiresAt, now); err != nil {
		return EnrollmentResult{}, internal(err)
	}
	if err := s.insertSecurityAudit(
		ctx, tx, "VNT_ENROLLMENT_CREATED", AuditSucceeded, AuditActorPlayer,
		actor.PlayerID, "", "", "", map[string]any{
			"enrollment_id": enrollmentID, "label": label, "expires_at": expiresAt,
		}, now,
	); err != nil {
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
		err := serviceError(401, "VNT_ENROLLMENT_INVALID", "Invalid or expired VNT enrollment code.")
		s.recordSecurityFailure(ctx, "VNT_ENROLLMENT_REJECTED", AuditActorUnknown, "", "", "", err, map[string]any{"operation": "register"})
		return RegisterResult{}, err
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
	enrollmentID, ownerID, err := s.repository.ConsumeEnrollment(ctx, tx, hashSecret(enrollmentCode), now)
	if err != nil {
		s.recordSecurityFailure(ctx, "VNT_ENROLLMENT_REJECTED", AuditActorUnknown, "", "", "", err, nil)
		return RegisterResult{}, mapRepositoryError(err)
	}
	if err := s.repository.EnsureOwnerQuota(ctx, tx, ownerID, s.maxNodesPerPlayer); err != nil {
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
	if err := s.insertSecurityAudit(
		ctx, tx, "VNT_NODE_REGISTERED", AuditSucceeded, AuditActorPlayer,
		ownerID, node.ID, "", "", map[string]any{
			"enrollment_id": enrollmentID,
			"endpoint":      net.JoinHostPort(node.AdvertisedHost, fmt.Sprintf("%d", node.Port)),
			"region":        node.Region,
			"fingerprint":   node.ServerKeyFingerprint,
		}, now,
	); err != nil {
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

func (s *Service) Recover(ctx context.Context, nodeID, enrollmentCode string, input RegisterInput) (RegisterResult, error) {
	if err := validateRegisterInput(&input); err != nil {
		return RegisterResult{}, err
	}
	if !strings.HasPrefix(enrollmentCode, "vne_") {
		err := serviceError(401, "VNT_ENROLLMENT_INVALID", "Invalid or expired VNT enrollment code.")
		s.recordSecurityFailure(ctx, "VNT_ENROLLMENT_REJECTED", AuditActorUnknown, "", nodeID, "", err, map[string]any{"operation": "recover"})
		return RegisterResult{}, err
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
	enrollmentID, ownerID, err := s.repository.ConsumeEnrollment(ctx, tx, hashSecret(enrollmentCode), now)
	if err != nil {
		s.recordSecurityFailure(ctx, "VNT_ENROLLMENT_REJECTED", AuditActorUnknown, "", nodeID, "", err, map[string]any{"operation": "recover"})
		return RegisterResult{}, mapRepositoryError(err)
	}
	state, identityChanged, err := s.repository.RecoverNode(ctx, tx, ownerID, Node{
		ID: nodeID, AdvertisedHost: input.AdvertisedHost, Port: input.Port,
		Region: input.Region, Location: input.Location, VNTSVersion: input.VNTSVersion,
		WrapperVersion: input.WrapperVersion, ServerKeyFingerprint: input.ServerKeyFingerprint,
		SupportedTransports: input.SupportedTransports, MaxRooms: input.MaxRooms,
	}, newID("vnc_"), nodeHash, expiresAt, now)
	if err != nil {
		s.recordSecurityFailure(ctx, "VNT_NODE_RECOVERY_REJECTED", AuditActorPlayer, ownerID, nodeID, "", err, nil)
		return RegisterResult{}, mapRepositoryError(err)
	}
	if err := s.insertSecurityAudit(
		ctx, tx, "VNT_NODE_RECOVERED", AuditSucceeded, AuditActorPlayer,
		ownerID, nodeID, "", "", map[string]any{
			"enrollment_id": enrollmentID, "identity_changed": identityChanged,
			"endpoint":    net.JoinHostPort(input.AdvertisedHost, fmt.Sprintf("%d", input.Port)),
			"fingerprint": input.ServerKeyFingerprint,
		}, now,
	); err != nil {
		return RegisterResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RegisterResult{}, internal(err)
	}
	return RegisterResult{
		NodeID: nodeID, NodeToken: nodeToken, State: state,
		HeartbeatIntervalSeconds: s.heartbeatInterval, CredentialExpiresAt: expiresAt,
	}, nil
}

func (s *Service) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	if err := s.checkLimit(ctx, LimitDirectory, RequestMetaFromContext(ctx).IPAddress); err != nil {
		return ListResult{}, err
	}
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if filter.Status == "" {
		filter.Status = StateOnline
	}
	filter.Region = strings.TrimSpace(filter.Region)
	filter.Cursor = strings.TrimSpace(filter.Cursor)
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	if filter.Limit < 1 || filter.Limit > 200 || !validState(filter.Status) {
		return ListResult{}, serviceError(400, "INVALID_REQUEST", "Invalid VNT node filter.")
	}
	if filter.Region != "" && !labelPattern.MatchString(filter.Region) {
		return ListResult{}, serviceError(400, "INVALID_REQUEST", "Invalid VNT node region filter.")
	}
	if filter.Cursor != "" && (len(filter.Cursor) > 64 || !nodeCursorPattern.MatchString(filter.Cursor)) {
		return ListResult{}, serviceError(400, "INVALID_REQUEST", "Invalid VNT node cursor.")
	}
	limit := filter.Limit
	filter.Limit++
	nodes, err := s.repository.List(ctx, filter)
	if err != nil {
		return ListResult{}, internal(err)
	}
	versions, err := s.versionPolicy.Resolve(ctx)
	if err != nil {
		return ListResult{}, internal(err)
	}
	nextCursor := ""
	if len(nodes) > limit {
		nextCursor = nodes[limit-1].ID
		nodes = nodes[:limit]
	}
	result := make([]PublicNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.Public(versions.Compatible(node)))
	}
	return ListResult{Items: result, NextCursor: nextCursor}, nil
}

func (s *Service) ListOwned(ctx context.Context, actor Actor, filter OwnedListFilter) (OwnedListResult, error) {
	if actor.PlayerID == "" || actor.AccountStatus != "ACTIVE" || !actor.SteamVerified {
		return OwnedListResult{}, serviceError(http.StatusForbidden, "VNT_NODE_OWNER_LIST_FORBIDDEN", "An active, Steam-verified player is required.")
	}
	if err := s.checkLimit(ctx, LimitDirectory, "owner\x00"+actor.PlayerID); err != nil {
		return OwnedListResult{}, err
	}
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	filter.Cursor = strings.TrimSpace(filter.Cursor)
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 ||
		(filter.Status != "" && !validState(filter.Status)) ||
		(filter.Cursor != "" && (len(filter.Cursor) > 64 || !nodeCursorPattern.MatchString(filter.Cursor))) {
		return OwnedListResult{}, serviceError(http.StatusBadRequest, "INVALID_REQUEST", "Invalid owned VNT node filter.")
	}
	limit := filter.Limit
	items, err := s.repository.AdminList(ctx, AdminListFilter{
		State: filter.Status, OwnerPlayerID: actor.PlayerID, Cursor: filter.Cursor, Limit: limit + 1,
	})
	if err != nil {
		return OwnedListResult{}, internal(err)
	}
	versions, err := s.versionPolicy.Resolve(ctx)
	if err != nil {
		return OwnedListResult{}, internal(err)
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	result := make([]OwnedNode, 0, len(items))
	for index := range items {
		items[index].VersionCompatible = versions.Compatible(items[index].Node)
		result = append(result, items[index].Owned())
	}
	return OwnedListResult{Items: result, NextCursor: nextCursor}, nil
}

func (s *Service) Heartbeat(ctx context.Context, nodeID, nodeToken string, input HeartbeatInput) error {
	input.WrapperVersion = strings.TrimSpace(input.WrapperVersion)
	input.VNTSVersion = strings.TrimSpace(input.VNTSVersion)
	if input.UptimeSeconds < 0 || input.ReportedSessions < 0 || input.WrapperVersion == "" ||
		input.VNTSVersion == "" || len(input.WrapperVersion) > 32 || len(input.VNTSVersion) > 32 {
		return serviceError(400, "INVALID_REQUEST", "Invalid VNT heartbeat.")
	}
	if err := s.checkLimit(ctx, LimitHeartbeat, nodeID+"\x00"+nodeToken); err != nil {
		return err
	}
	now := s.now().UTC()
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.AuthenticateCredential(ctx, tx, nodeID, hashSecret(nodeToken), now); err != nil {
		s.recordSecurityFailure(ctx, "VNT_NODE_CREDENTIAL_REJECTED", AuditActorNode, "", nodeID, "", err, map[string]any{"operation": "heartbeat"})
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
	if err := s.checkLimit(ctx, LimitManagement, nodeID+"\x00"+nodeToken); err != nil {
		return "", err
	}
	now := s.now().UTC()
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return "", internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.AuthenticateCurrentCredential(ctx, tx, nodeID, hashSecret(nodeToken), now); err != nil {
		s.recordSecurityFailure(ctx, "VNT_NODE_CREDENTIAL_REJECTED", AuditActorNode, "", nodeID, "", err, map[string]any{"operation": "retire"})
		return "", mapRepositoryError(err)
	}
	state, err := s.repository.Retire(ctx, tx, nodeID, now)
	if err != nil {
		return "", mapRepositoryError(err)
	}
	if state == StateRetired {
		if err := s.repository.RevokeCredentials(ctx, tx, nodeID, now); err != nil {
			return "", internal(err)
		}
	}
	if err := s.insertSecurityAudit(
		ctx, tx, "VNT_NODE_RETIREMENT_REQUESTED", AuditSucceeded, AuditActorNode,
		"", nodeID, "", "", map[string]any{"resulting_state": state}, now,
	); err != nil {
		return "", internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", internal(err)
	}
	return state, nil
}

func (s *Service) RetireOwned(ctx context.Context, actor Actor, nodeID string) (string, error) {
	if actor.PlayerID == "" || actor.AccountStatus != "ACTIVE" || !actor.SteamVerified || !actor.IntegrityTrusted {
		err := serviceError(403, "VNT_NODE_OWNER_STEP_UP_REQUIRED", "An integrity-trusted owner session is required.")
		s.recordSecurityFailure(ctx, "VNT_NODE_OWNER_RETIREMENT_REJECTED", AuditActorPlayer, actor.PlayerID, nodeID, "", err, nil)
		return "", err
	}
	if err := s.checkLimit(ctx, LimitManagement, "owner\x00"+actor.PlayerID); err != nil {
		return "", err
	}
	now := s.now().UTC()
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return "", internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := s.repository.GetForAllocation(ctx, tx, nodeID, now)
	if err != nil {
		return "", mapRepositoryError(err)
	}
	if item.OwnerPlayerID != actor.PlayerID {
		err := serviceError(404, "VNT_NODE_NOT_FOUND", "VNT node not found.")
		s.recordSecurityFailure(ctx, "VNT_NODE_OWNER_RETIREMENT_REJECTED", AuditActorPlayer, actor.PlayerID, nodeID, "", err, nil)
		return "", err
	}
	if item.State == StateRevoked || item.State == StateRetired {
		err := serviceError(409, "VNT_NODE_TERMINAL", "A revoked or retired VNT node cannot be retired again.")
		s.recordSecurityFailure(ctx, "VNT_NODE_OWNER_RETIREMENT_REJECTED", AuditActorPlayer, actor.PlayerID, nodeID, "", err, nil)
		return "", err
	}
	state, err := s.repository.Retire(ctx, tx, nodeID, now)
	if err != nil {
		return "", mapRepositoryError(err)
	}
	if state == StateRetired {
		if err := s.repository.RevokeCredentials(ctx, tx, nodeID, now); err != nil {
			return "", internal(err)
		}
	}
	if err := s.insertSecurityAudit(
		ctx, tx, "VNT_NODE_RETIREMENT_REQUESTED", AuditSucceeded, AuditActorPlayer,
		actor.PlayerID, nodeID, "", "", map[string]any{"resulting_state": state}, now,
	); err != nil {
		return "", internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", internal(err)
	}
	return state, nil
}

func (s *Service) RotateCredential(ctx context.Context, nodeID, nodeToken string) (CredentialRotationResult, error) {
	if err := s.checkLimit(ctx, LimitManagement, nodeID+"\x00"+nodeToken); err != nil {
		return CredentialRotationResult{}, err
	}
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
	previousValidUntil, err := s.repository.RotateCredential(
		ctx, tx, nodeID, hashSecret(nodeToken), newID("vnc_"), newHash,
		expiresAt, now.Add(s.rotationGrace), now,
	)
	if err != nil {
		s.recordSecurityFailure(ctx, "VNT_NODE_CREDENTIAL_REJECTED", AuditActorNode, "", nodeID, "", err, map[string]any{"operation": "rotate"})
		return CredentialRotationResult{}, mapRepositoryError(err)
	}
	if err := s.insertSecurityAudit(
		ctx, tx, "VNT_NODE_CREDENTIAL_ROTATED", AuditSucceeded, AuditActorNode,
		"", nodeID, "", "", map[string]any{
			"credential_expires_at": expiresAt, "previous_valid_until": previousValidUntil,
		}, now,
	); err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	return CredentialRotationResult{
		NodeToken: newToken, CredentialExpiresAt: expiresAt, PreviousValidUntil: previousValidUntil,
	}, nil
}

func (s *Service) checkLimit(ctx context.Context, operation LimitOperation, identity string) error {
	if s.limiter == nil {
		return nil
	}
	decision := s.limiter.Check(ctx, operation, identity)
	if decision.Allowed {
		return nil
	}
	retryAfterSeconds := max(1, int((decision.RetryAfter+time.Second-1)/time.Second))
	return &ServiceError{
		Status: 429, Code: "VNT_RATE_LIMITED", Message: "Too many VNT requests.",
		Details: map[string]any{
			"operation": operation, "retry_after_seconds": retryAfterSeconds,
		},
	}
}

func (s *Service) insertSecurityAudit(
	ctx context.Context,
	executor auditExecutor,
	eventType, result, actorType, playerID, nodeID, roomID, reasonCode string,
	details map[string]any,
	now time.Time,
) error {
	meta := RequestMetaFromContext(ctx)
	return s.repository.InsertSecurityAudit(ctx, executor, SecurityAudit{
		ID: NewSecurityAuditID(), EventType: eventType, Result: result, ActorType: actorType,
		PlayerID: playerID, NodeID: nodeID, RoomID: roomID, RequestID: meta.RequestID,
		IPAddress: meta.IPAddress, UserAgent: meta.UserAgent, ReasonCode: reasonCode,
		Details: details, CreatedAt: now,
	})
}

func (s *Service) recordSecurityFailure(
	ctx context.Context,
	eventType, actorType, playerID, nodeID, roomID string,
	err error,
	details map[string]any,
) {
	if s.repository == nil {
		return
	}
	status, reasonCode, _, _ := errorDetails(err)
	result := AuditDenied
	if status >= http.StatusInternalServerError {
		result = AuditFailed
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = s.insertSecurityAudit(
		auditCtx, s.repository.pool, eventType, result, actorType,
		playerID, nodeID, roomID, reasonCode, details, s.now().UTC(),
	)
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
