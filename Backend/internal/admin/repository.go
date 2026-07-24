package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

func (r *Repository) InsertAudit(ctx context.Context, tx pgx.Tx, audit AuditLog) error {
	oldValue, err := json.Marshal(audit.OldValue)
	if err != nil {
		return fmt.Errorf("marshal old audit value: %w", err)
	}
	newValue, err := json.Marshal(audit.NewValue)
	if err != nil {
		return fmt.Errorf("marshal new audit value: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_audit_logs (
			id, admin_id, action, target_type, target_id,
			old_value, new_value, reason, request_id, ip_address,
			user_agent, result, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6::jsonb, $7::jsonb,
			$8, NULLIF($9, ''), NULLIF($10, '')::inet,
			NULLIF($11, ''), $12, $13
		)
	`, audit.ID, audit.AdminID, audit.Action, audit.TargetType, audit.TargetID,
		oldValue, newValue, audit.Reason, audit.RequestID, audit.IPAddress,
		audit.UserAgent, defaultString(audit.Result, "SUCCEEDED"), audit.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert admin audit log: %w", err)
	}
	return nil
}
