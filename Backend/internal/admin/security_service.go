package admin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecurityService struct {
	pool       *pgxpool.Pool
	repository *SecurityRepository
	audits     *Repository
	now        func() time.Time
}

func NewSecurityService(
	pool *pgxpool.Pool,
	repository *SecurityRepository,
	audits *Repository,
) *SecurityService {
	return &SecurityService{
		pool:       pool,
		repository: repository,
		audits:     audits,
		now:        time.Now,
	}
}

func (s *SecurityService) Summary(ctx context.Context) (DashboardSummary, error) {
	result, err := s.repository.DashboardSummary(ctx, s.now().UTC())
	if err != nil {
		return DashboardSummary{}, internal(err)
	}
	return result, nil
}

func (s *SecurityService) Timeseries(ctx context.Context, period string) ([]DashboardPoint, error) {
	var window, step time.Duration
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "", "24h":
		window, step = 24*time.Hour, time.Hour
	case "1h":
		window, step = time.Hour, 5*time.Minute
	case "7d":
		window, step = 7*24*time.Hour, 6*time.Hour
	case "30d":
		window, step = 30*24*time.Hour, 24*time.Hour
	default:
		return nil, &ServiceError{
			Status:  http.StatusBadRequest,
			Code:    "INVALID_REQUEST",
			Message: "Invalid dashboard period.",
			Details: map[string]any{"period": "must be 1h, 24h, 7d, or 30d"},
		}
	}
	now := s.now().UTC()
	items, err := s.repository.DashboardTimeseries(ctx, now.Add(-window), now, step)
	if err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *SecurityService) Alerts(ctx context.Context) ([]DashboardAlert, error) {
	counts, err := s.repository.AlertCounts(ctx)
	if err != nil {
		return nil, internal(err)
	}
	definitions := []DashboardAlert{
		{
			ID: "relay-unhealthy", Severity: "HIGH", ResourceType: "relay_node",
			Title: "中继节点异常", Summary: "存在离线或不健康的中继节点。",
			Count: counts["relay_unhealthy"], ResourcePath: "/online/relay-nodes?health=unhealthy",
		},
		{
			ID: "game-server-unhealthy", Severity: "HIGH", ResourceType: "game_server",
			Title: "专服心跳异常", Summary: "存在离线或不健康的 Dedicated Server。",
			Count: counts["game_server_unhealthy"], ResourcePath: "/online/game-servers?health=unhealthy",
		},
		{
			ID: "critical-risk-events", Severity: "CRITICAL", ResourceType: "risk_event",
			Title: "高优先级风险事件", Summary: "存在尚未处理的高危或严重登录风险事件。",
			Count: counts["critical_risk"], ResourcePath: "/risk-events?unresolved_only=true",
		},
	}
	items := make([]DashboardAlert, 0, len(definitions))
	for _, item := range definitions {
		if item.Count > 0 {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *SecurityService) ListRiskEvents(ctx context.Context, filter RiskEventFilter) ([]AdminRiskEvent, string, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return nil, "", &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid limit."}
	}
	filter.Limit++
	items, err := s.repository.ListRiskEvents(ctx, filter)
	if err != nil {
		return nil, "", internal(err)
	}
	nextCursor := ""
	if len(items) == filter.Limit {
		items = items[:len(items)-1]
		nextCursor = items[len(items)-1].ID
	}
	for index := range items {
		items[index] = sanitizeRiskEvent(items[index])
	}
	return items, nextCursor, nil
}

func (s *SecurityService) GetRiskEvent(ctx context.Context, id string) (AdminRiskEvent, error) {
	item, err := s.repository.GetRiskEvent(ctx, s.pool, strings.TrimSpace(id), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminRiskEvent{}, &ServiceError{Status: 404, Code: "RISK_EVENT_NOT_FOUND", Message: "Risk event not found."}
	}
	if err != nil {
		return AdminRiskEvent{}, internal(err)
	}
	return sanitizeRiskEvent(item), nil
}

func (s *SecurityService) ResolveRiskEvent(ctx context.Context, id, reasonInput string, meta RequestMeta) (AdminRiskEvent, error) {
	meta = sanitizeMeta(meta)
	if meta.AdminID == "" {
		return AdminRiskEvent{}, &ServiceError{Status: 401, Code: "ADMIN_UNAUTHORIZED", Message: "Administrator authentication is required."}
	}
	reason, err := validateAuditReason(reasonInput)
	if err != nil {
		return AdminRiskEvent{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminRiskEvent{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := s.repository.GetRiskEvent(ctx, tx, strings.TrimSpace(id), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminRiskEvent{}, &ServiceError{Status: 404, Code: "RISK_EVENT_NOT_FOUND", Message: "Risk event not found."}
	}
	if err != nil {
		return AdminRiskEvent{}, internal(err)
	}
	if item.ResolvedAt != nil {
		return AdminRiskEvent{}, &ServiceError{
			Status: http.StatusConflict, Code: "RISK_EVENT_ALREADY_RESOLVED",
			Message: "Risk event has already been resolved.",
		}
	}
	now := s.now().UTC()
	if err := s.repository.ResolveRiskEvent(ctx, tx, item.ID, meta.AdminID, reason, now); err != nil {
		return AdminRiskEvent{}, internal(err)
	}
	resolved := item
	resolved.ResolvedAt = &now
	resolved.ResolvedBy = meta.AdminID
	resolved.ResolutionNote = reason
	if err := s.audits.InsertAudit(ctx, tx, AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID, Action: "RISK_EVENT_RESOLVED",
		TargetType: "risk_event", TargetID: item.ID,
		OldValue: map[string]any{"resolved_at": nil, "resolved_by": ""},
		NewValue: map[string]any{"resolved_at": now, "resolved_by": meta.AdminID},
		Reason:   reason, RequestID: meta.RequestID, IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: "SUCCEEDED", CreatedAt: now,
	}); err != nil {
		return AdminRiskEvent{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminRiskEvent{}, internal(fmt.Errorf("commit risk event resolution: %w", err))
	}
	return sanitizeRiskEvent(resolved), nil
}

func (s *SecurityService) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditEntry, string, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return nil, "", &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid limit."}
	}
	filter.Limit++
	items, err := s.repository.ListAudit(ctx, filter)
	if err != nil {
		return nil, "", internal(err)
	}
	nextCursor := ""
	if len(items) == filter.Limit {
		items = items[:len(items)-1]
		nextCursor = items[len(items)-1].ID
	}
	for index := range items {
		items[index].OldValue = redactSensitiveMap(items[index].OldValue)
		items[index].NewValue = redactSensitiveMap(items[index].NewValue)
	}
	return items, nextCursor, nil
}

func (s *SecurityService) GetAudit(ctx context.Context, id string) (AuditEntry, error) {
	item, err := s.repository.GetAudit(ctx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditEntry{}, &ServiceError{Status: 404, Code: "AUDIT_LOG_NOT_FOUND", Message: "Audit log not found."}
	}
	if err != nil {
		return AuditEntry{}, internal(err)
	}
	item.OldValue = redactSensitiveMap(item.OldValue)
	item.NewValue = redactSensitiveMap(item.NewValue)
	return item, nil
}

func (s *SecurityService) ListVNTSecurityAudit(
	ctx context.Context,
	filter VNTSecurityAuditFilter,
) ([]VNTSecurityAuditEntry, string, error) {
	filter.Cursor = strings.TrimSpace(filter.Cursor)
	filter.EventType = strings.ToUpper(strings.TrimSpace(filter.EventType))
	filter.Result = strings.ToUpper(strings.TrimSpace(filter.Result))
	filter.ActorType = strings.ToUpper(strings.TrimSpace(filter.ActorType))
	filter.PlayerID = strings.TrimSpace(filter.PlayerID)
	filter.AdminID = strings.TrimSpace(filter.AdminID)
	filter.NodeID = strings.TrimSpace(filter.NodeID)
	filter.RoomID = strings.TrimSpace(filter.RoomID)
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 ||
		len(filter.Cursor) > 64 || (filter.Cursor != "" && !strings.HasPrefix(filter.Cursor, "vsa_")) ||
		len(filter.EventType) > 64 || len(filter.PlayerID) > 64 || len(filter.AdminID) > 128 || len(filter.NodeID) > 64 || len(filter.RoomID) > 64 ||
		(filter.Result != "" && filter.Result != "SUCCEEDED" && filter.Result != "FAILED" && filter.Result != "DENIED") ||
		(filter.ActorType != "" && filter.ActorType != "PLAYER" && filter.ActorType != "NODE" &&
			filter.ActorType != "ADMIN" && filter.ActorType != "SYSTEM" && filter.ActorType != "UNKNOWN") {
		return nil, "", &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid VNT security audit filter."}
	}
	filter.Limit++
	items, err := s.repository.ListVNTSecurityAudit(ctx, filter)
	if err != nil {
		return nil, "", internal(err)
	}
	nextCursor := ""
	if len(items) == filter.Limit {
		items = items[:len(items)-1]
		nextCursor = items[len(items)-1].ID
	}
	for index := range items {
		items[index].Details = redactSensitiveMap(items[index].Details)
	}
	return items, nextCursor, nil
}

func (s *SecurityService) ListLoginAudit(ctx context.Context, filter LoginAuditFilter) ([]LoginAuditEntry, string, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return nil, "", &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid limit."}
	}
	if filter.Result != "" && filter.Result != "SUCCESS" && filter.Result != "FAILURE" {
		return nil, "", &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid result filter."}
	}
	filter.Limit++
	items, err := s.repository.ListLoginAudit(ctx, filter)
	if err != nil {
		return nil, "", internal(err)
	}
	nextCursor := ""
	if len(items) == filter.Limit {
		items = items[:len(items)-1]
		nextCursor = items[len(items)-1].ID
	}
	return items, nextCursor, nil
}

func (s *SecurityService) PlayerSessions(ctx context.Context, playerID string) ([]PlayerSessionEntry, error) {
	if err := s.requirePlayer(ctx, playerID); err != nil {
		return nil, err
	}
	items, err := s.repository.ListPlayerSessions(ctx, strings.TrimSpace(playerID), s.now().UTC())
	if err != nil {
		return nil, internal(err)
	}
	for index := range items {
		items[index].IPAddress = maskIPAddress(items[index].IPAddress)
	}
	return items, nil
}

func (s *SecurityService) PlayerRiskEvents(ctx context.Context, playerID string) ([]AdminRiskEvent, error) {
	if err := s.requirePlayer(ctx, playerID); err != nil {
		return nil, err
	}
	items, _, err := s.ListRiskEvents(ctx, RiskEventFilter{
		PlayerID: strings.TrimSpace(playerID),
		Limit:    100,
	})
	return items, err
}

func (s *SecurityService) PlayerLoginEvents(ctx context.Context, playerID string) ([]PlayerLoginEventEntry, error) {
	if err := s.requirePlayer(ctx, playerID); err != nil {
		return nil, err
	}
	items, err := s.repository.ListPlayerLoginEvents(ctx, strings.TrimSpace(playerID))
	if err != nil {
		return nil, internal(err)
	}
	for index := range items {
		items[index].IPAddress = maskIPAddress(items[index].IPAddress)
	}
	return items, nil
}

func (s *SecurityService) requirePlayer(ctx context.Context, playerID string) error {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid player ID."}
	}
	exists, err := s.repository.PlayerExists(ctx, playerID)
	if err != nil {
		return internal(err)
	}
	if !exists {
		return &ServiceError{Status: 404, Code: "PLAYER_NOT_FOUND", Message: "Player not found."}
	}
	return nil
}

func sanitizeRiskEvent(item AdminRiskEvent) AdminRiskEvent {
	item.IPAddress = maskIPAddress(item.IPAddress)
	item.Details = redactSensitiveMap(item.Details)
	return item
}

func maskIPAddress(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return fmt.Sprintf("%d.%d.%d.x", ipv4[0], ipv4[1], ipv4[2])
	}
	ipv6 := ip.To16()
	return fmt.Sprintf("%x:%x:%x:%x::/64",
		uint16(ipv6[0])<<8|uint16(ipv6[1]),
		uint16(ipv6[2])<<8|uint16(ipv6[3]),
		uint16(ipv6[4])<<8|uint16(ipv6[5]),
		uint16(ipv6[6])<<8|uint16(ipv6[7]),
	)
}

func redactSensitiveMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		lower := strings.ToLower(key)
		sensitive := false
		for _, marker := range []string{"token", "password", "secret", "private_key", "cookie", "authorization", "certificate_pem"} {
			if strings.Contains(lower, marker) {
				sensitive = true
				break
			}
		}
		if sensitive {
			output[key] = "[REDACTED]"
			continue
		}
		switch nested := value.(type) {
		case map[string]any:
			output[key] = redactSensitiveMap(nested)
		case []any:
			items := make([]any, len(nested))
			for index, element := range nested {
				if nestedMap, ok := element.(map[string]any); ok {
					items[index] = redactSensitiveMap(nestedMap)
				} else {
					items[index] = element
				}
			}
			output[key] = items
		default:
			output[key] = value
		}
	}
	return output
}
