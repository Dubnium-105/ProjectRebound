package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	clientupdate "github.com/Dubnium-105/ProjectRebound/Backend/internal/update"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReleaseRepository struct {
	pool *pgxpool.Pool
}

func NewReleaseRepository(pool *pgxpool.Pool) *ReleaseRepository {
	return &ReleaseRepository{pool: pool}
}

func (r *ReleaseRepository) Insert(ctx context.Context, tx pgx.Tx, item Release) error {
	source, err := json.Marshal(item.Source)
	if err != nil {
		return err
	}
	validation, err := json.Marshal(item.Validation)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_releases (
			id, product, platform, architecture, channel, version,
			minimum_supported_version, force_update, status, source_release,
			signed_manifest, validation_result, created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb,
			NULL, $11::jsonb, $12, $13, $13
		)
	`, item.ID, item.Product, item.Platform, item.Architecture, item.Channel,
		item.Version, item.MinimumSupportedVersion, item.ForceUpdate, item.Status,
		source, validation, item.CreatedBy, item.CreatedAt)
	return err
}

func (r *ReleaseRepository) Get(ctx context.Context, id string) (Release, error) {
	return scanRelease(r.pool.QueryRow(ctx, releaseSelect+" WHERE id = $1", id))
}

func (r *ReleaseRepository) GetForUpdate(ctx context.Context, tx pgx.Tx, id string) (Release, error) {
	return scanRelease(tx.QueryRow(ctx, releaseSelect+" WHERE id = $1 FOR UPDATE", id))
}

func (r *ReleaseRepository) List(ctx context.Context, filter ReleaseListFilter) ([]Release, error) {
	rows, err := r.pool.Query(ctx, releaseSelect+`
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR platform = $3)
		  AND ($4 = '' OR architecture = $4)
		  AND ($5 = '' OR channel = $5)
		ORDER BY id
		LIMIT $6
	`, filter.Cursor, filter.Status, filter.Platform, filter.Architecture,
		filter.Channel, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Release, 0, filter.Limit)
	for rows.Next() {
		item, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *ReleaseRepository) MarkReady(
	ctx context.Context,
	tx pgx.Tx,
	id string,
	source clientupdate.SourceRelease,
	manifest clientupdate.Manifest,
	validation ReleaseValidation,
	now time.Time,
) (Release, error) {
	return r.updateState(ctx, tx, id, ReleaseStatusReady, source, &manifest, validation, "", now)
}

func (r *ReleaseRepository) MarkPublished(
	ctx context.Context,
	tx pgx.Tx,
	id, adminID string,
	source clientupdate.SourceRelease,
	manifest clientupdate.Manifest,
	validation ReleaseValidation,
	now time.Time,
) (Release, error) {
	return r.updateState(ctx, tx, id, ReleaseStatusPublished, source, &manifest, validation, adminID, now)
}

func (r *ReleaseRepository) MarkRolledBack(
	ctx context.Context,
	tx pgx.Tx,
	id, adminID string,
	now time.Time,
) (Release, error) {
	return scanRelease(tx.QueryRow(ctx, `
		UPDATE admin_releases
		SET status = 'ROLLED_BACK', rolled_back_by = $2,
		    rolled_back_at = $3, updated_at = $3
		WHERE id = $1
		RETURNING `+releaseColumns,
		id, adminID, now))
}

func (r *ReleaseRepository) MarkArchived(
	ctx context.Context,
	tx pgx.Tx,
	id, adminID string,
	now time.Time,
) (Release, error) {
	return scanRelease(tx.QueryRow(ctx, `
		UPDATE admin_releases
		SET status = 'ARCHIVED', archived_by = $2,
		    archived_at = $3, updated_at = $3
		WHERE id = $1
		RETURNING `+releaseColumns,
		id, adminID, now))
}

func (r *ReleaseRepository) updateState(
	ctx context.Context,
	tx pgx.Tx,
	id, status string,
	source clientupdate.SourceRelease,
	manifest *clientupdate.Manifest,
	validation ReleaseValidation,
	adminID string,
	now time.Time,
) (Release, error) {
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return Release{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return Release{}, err
	}
	validationJSON, err := json.Marshal(validation)
	if err != nil {
		return Release{}, err
	}
	return scanRelease(tx.QueryRow(ctx, `
		UPDATE admin_releases
		SET status = $2::varchar, source_release = $3::jsonb,
		    signed_manifest = $4::jsonb, validation_result = $5::jsonb,
		    published_by = CASE WHEN $2::varchar = 'PUBLISHED' THEN $6 ELSE published_by END,
		    published_at = CASE WHEN $2::varchar = 'PUBLISHED' THEN $7 ELSE published_at END,
		    minimum_supported_version = $8,
		    updated_at = $7
		WHERE id = $1
		RETURNING `+releaseColumns,
		id, status, sourceJSON, manifestJSON, validationJSON, adminID, now,
		source.MinimumSupportedVersion))
}

func (r *ReleaseRepository) PublishedManifests(ctx context.Context) ([]clientupdate.Manifest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT signed_manifest
		FROM admin_releases
		WHERE status = 'PUBLISHED' AND signed_manifest IS NOT NULL
		ORDER BY platform, architecture, channel, published_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]clientupdate.Manifest, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var manifest clientupdate.Manifest
		if err := json.Unmarshal(encoded, &manifest); err != nil {
			return nil, fmt.Errorf("decode published manifest: %w", err)
		}
		items = append(items, manifest)
	}
	return items, rows.Err()
}

const releaseColumns = `
	id, product, platform, architecture, channel, version,
	minimum_supported_version, force_update, status, source_release,
	signed_manifest, validation_result, created_by, published_by,
	rolled_back_by, archived_by, created_at, updated_at, published_at,
	rolled_back_at, archived_at
`

const releaseSelect = `SELECT ` + releaseColumns + ` FROM admin_releases`

func scanRelease(row pgx.Row) (Release, error) {
	var item Release
	var sourceJSON, manifestJSON, validationJSON []byte
	var publishedBy, rolledBackBy, archivedBy sql.NullString
	var publishedAt, rolledBackAt, archivedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.Product, &item.Platform, &item.Architecture,
		&item.Channel, &item.Version, &item.MinimumSupportedVersion,
		&item.ForceUpdate, &item.Status, &sourceJSON, &manifestJSON,
		&validationJSON, &item.CreatedBy, &publishedBy, &rolledBackBy,
		&archivedBy, &item.CreatedAt, &item.UpdatedAt, &publishedAt,
		&rolledBackAt, &archivedAt,
	)
	if err != nil {
		return Release{}, err
	}
	if err := json.Unmarshal(sourceJSON, &item.Source); err != nil {
		return Release{}, err
	}
	if len(manifestJSON) > 0 && string(manifestJSON) != "null" {
		var manifest clientupdate.Manifest
		if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
			return Release{}, err
		}
		item.Manifest = &manifest
	}
	if err := json.Unmarshal(validationJSON, &item.Validation); err != nil {
		return Release{}, err
	}
	if publishedBy.Valid {
		item.PublishedBy = publishedBy.String
	}
	if rolledBackBy.Valid {
		item.RolledBackBy = rolledBackBy.String
	}
	if archivedBy.Valid {
		item.ArchivedBy = archivedBy.String
	}
	if publishedAt.Valid {
		item.PublishedAt = &publishedAt.Time
	}
	if rolledBackAt.Valid {
		item.RolledBackAt = &rolledBackAt.Time
	}
	if archivedAt.Valid {
		item.ArchivedAt = &archivedAt.Time
	}
	return item, nil
}
