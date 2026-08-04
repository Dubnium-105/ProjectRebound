package vnt

import (
	"context"
	"net"
	"regexp"
	"strings"
	"time"
)

var (
	auditCredentialPattern    = regexp.MustCompile(`(?i)(?:vne|vnn|vntk|vntw|p2h|gsr|gst)_[A-Za-z0-9._~+/=-]{6,}`)
	auditAuthorizationPattern = regexp.MustCompile(`(?i)\b(?:bearer|vntenrollment)\s+[A-Za-z0-9._~+/=-]+`)
	auditJWTPattern           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	auditAssignmentPattern    = regexp.MustCompile(`(?i)\b(?:token|password|secret|authorization|cookie)\s*[:=]\s*[^\s,;]+`)
)

const (
	AuditSucceeded = "SUCCEEDED"
	AuditFailed    = "FAILED"
	AuditDenied    = "DENIED"

	AuditActorPlayer  = "PLAYER"
	AuditActorNode    = "NODE"
	AuditActorAdmin   = "ADMIN"
	AuditActorSystem  = "SYSTEM"
	AuditActorUnknown = "UNKNOWN"
)

type RequestMeta struct {
	RequestID string
	IPAddress string
	UserAgent string
}

type SecurityAudit struct {
	ID         string
	EventType  string
	Result     string
	ActorType  string
	PlayerID   string
	AdminID    string
	NodeID     string
	RoomID     string
	RequestID  string
	IPAddress  string
	UserAgent  string
	ReasonCode string
	Details    map[string]any
	CreatedAt  time.Time
}

func NewSecurityAuditID() string { return newID("vsa_") }

type requestMetaContextKey uint8

const requestMetaKey requestMetaContextKey = iota

func WithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	meta.RequestID = sanitizeAuditText(strings.TrimSpace(meta.RequestID))
	meta.IPAddress = strings.TrimSpace(meta.IPAddress)
	if net.ParseIP(meta.IPAddress) == nil {
		meta.IPAddress = ""
	}
	meta.UserAgent = sanitizeAuditText(strings.TrimSpace(meta.UserAgent))
	if len(meta.UserAgent) > 512 {
		meta.UserAgent = meta.UserAgent[:512]
	}
	return context.WithValue(ctx, requestMetaKey, meta)
}

func sanitizeAuditText(value string) string {
	value = auditAuthorizationPattern.ReplaceAllString(value, "[REDACTED]")
	value = auditCredentialPattern.ReplaceAllString(value, "[REDACTED]")
	value = auditJWTPattern.ReplaceAllString(value, "[REDACTED]")
	return auditAssignmentPattern.ReplaceAllString(value, "[REDACTED]")
}

func sanitizeAuditDetails(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "private_key") ||
			strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") {
			output[key] = "[REDACTED]"
			continue
		}
		output[key] = sanitizeAuditValue(value)
	}
	return output
}

func sanitizeAuditValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeAuditText(typed)
	case map[string]any:
		return sanitizeAuditDetails(typed)
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = sanitizeAuditValue(typed[index])
		}
		return result
	case []string:
		result := make([]string, len(typed))
		for index := range typed {
			result[index] = sanitizeAuditText(typed[index])
		}
		return result
	default:
		return value
	}
}

func RequestMetaFromContext(ctx context.Context) RequestMeta {
	meta, _ := ctx.Value(requestMetaKey).(RequestMeta)
	return meta
}
