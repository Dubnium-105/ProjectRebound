package diagnostic

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

type reportExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type Repository struct {
	database reportExecutor
}

func NewRepository(database reportExecutor) *Repository {
	return &Repository{database: database}
}

func (r *Repository) Store(ctx context.Context, playerID, report string) error {
	if _, err := r.database.Exec(ctx, `
		INSERT INTO diagnostic_reports (player_id, report)
		VALUES ($1, $2)
	`, playerID, report); err != nil {
		return fmt.Errorf("store diagnostic report: %w", err)
	}
	return nil
}
