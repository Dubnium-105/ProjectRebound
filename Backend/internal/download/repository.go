package download

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

type rowScanner interface {
	Scan(...any) error
}

const categoryColumns = `id, slug, title_en, title_zh_cn, description_en, description_zh_cn,
sort_order, enabled, status, created_by, COALESCE(archived_by, ''), created_at, updated_at, archived_at`

func scanCategory(row rowScanner) (Category, error) {
	var item Category
	err := row.Scan(
		&item.ID, &item.Slug, &item.Title.EN, &item.Title.ZhCN,
		&item.Description.EN, &item.Description.ZhCN, &item.SortOrder, &item.Enabled, &item.Status,
		&item.CreatedBy, &item.ArchivedBy, &item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt,
	)
	return item, err
}

func (r *Repository) ListCategories(ctx context.Context, publicOnly bool) ([]Category, error) {
	query := `SELECT ` + categoryColumns + ` FROM download_categories`
	if publicOnly {
		query += ` WHERE status = 'ACTIVE' AND enabled`
	}
	query += ` ORDER BY sort_order, slug`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Category, 0)
	for rows.Next() {
		item, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) GetCategory(ctx context.Context, id string) (Category, error) {
	return scanCategory(r.pool.QueryRow(ctx, `SELECT `+categoryColumns+` FROM download_categories WHERE id = $1`, id))
}

func (r *Repository) GetCategoryForUpdate(ctx context.Context, tx pgx.Tx, id string) (Category, error) {
	return scanCategory(tx.QueryRow(ctx, `SELECT `+categoryColumns+` FROM download_categories WHERE id = $1 FOR UPDATE`, id))
}

func (r *Repository) InsertCategory(ctx context.Context, tx pgx.Tx, item Category) error {
	_, err := tx.Exec(ctx, `INSERT INTO download_categories (
		id, slug, title_en, title_zh_cn, description_en, description_zh_cn,
		sort_order, enabled, status, created_by, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		item.ID, item.Slug, item.Title.EN, item.Title.ZhCN, item.Description.EN,
		item.Description.ZhCN, item.SortOrder, item.Enabled, item.Status, item.CreatedBy, item.CreatedAt, item.UpdatedAt)
	return err
}

func (r *Repository) UpdateCategory(ctx context.Context, tx pgx.Tx, item Category) error {
	_, err := tx.Exec(ctx, `UPDATE download_categories SET slug=$2, title_en=$3, title_zh_cn=$4,
		description_en=$5, description_zh_cn=$6, sort_order=$7, enabled=$8, updated_at=$9 WHERE id=$1`,
		item.ID, item.Slug, item.Title.EN, item.Title.ZhCN, item.Description.EN,
		item.Description.ZhCN, item.SortOrder, item.Enabled, item.UpdatedAt)
	return err
}

func (r *Repository) ArchiveCategory(ctx context.Context, tx pgx.Tx, id, adminID string, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE download_categories SET status='ARCHIVED', archived_by=$2,
		archived_at=$3, updated_at=$3 WHERE id=$1 AND status='ACTIVE'`, id, adminID, now)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

const entryColumns = `entry.id, entry.category_id, category.slug, entry.slug,
entry.title_en, entry.title_zh_cn, entry.description_en, entry.description_zh_cn,
entry.sort_order, entry.status, entry.created_by, COALESCE(entry.archived_by, ''),
entry.created_at, entry.updated_at, entry.archived_at`

func scanEntry(row rowScanner) (Entry, error) {
	var item Entry
	err := row.Scan(
		&item.ID, &item.CategoryID, &item.CategorySlug, &item.Slug,
		&item.Title.EN, &item.Title.ZhCN, &item.Description.EN, &item.Description.ZhCN,
		&item.SortOrder, &item.Status, &item.CreatedBy, &item.ArchivedBy,
		&item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt,
	)
	item.Versions = make([]Version, 0)
	return item, err
}

func (r *Repository) ListEntries(ctx context.Context, publicOnly bool) ([]Entry, error) {
	query := `SELECT ` + entryColumns + ` FROM download_entries entry
		JOIN download_categories category ON category.id = entry.category_id`
	if publicOnly {
		query += ` WHERE entry.status='ACTIVE' AND category.status='ACTIVE' AND category.enabled
		AND EXISTS (SELECT 1 FROM download_versions version WHERE version.entry_id=entry.id AND version.status='PUBLISHED')`
	}
	query += ` ORDER BY category.sort_order, entry.sort_order, entry.slug`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Entry, 0)
	for rows.Next() {
		item, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range items {
		versions, err := r.ListVersions(ctx, items[index].ID, publicOnly)
		if err != nil {
			return nil, err
		}
		items[index].Versions = versions
		if len(versions) > 0 {
			items[index].LatestVersionID = versions[0].ID
		}
	}
	return items, nil
}

func (r *Repository) GetEntry(ctx context.Context, id string) (Entry, error) {
	item, err := scanEntry(r.pool.QueryRow(ctx, `SELECT `+entryColumns+` FROM download_entries entry
		JOIN download_categories category ON category.id=entry.category_id WHERE entry.id=$1`, id))
	if err != nil {
		return Entry{}, err
	}
	item.Versions, err = r.ListVersions(ctx, item.ID, false)
	if len(item.Versions) > 0 {
		item.LatestVersionID = item.Versions[0].ID
	}
	return item, err
}

func (r *Repository) EntryLifecycle(ctx context.Context, id string) (entryStatus, categoryStatus string, err error) {
	err = r.pool.QueryRow(ctx, `SELECT entry.status, category.status FROM download_entries entry
		JOIN download_categories category ON category.id=entry.category_id WHERE entry.id=$1`, id).Scan(&entryStatus, &categoryStatus)
	return
}

func (r *Repository) EntryLifecycleForUpdate(ctx context.Context, tx pgx.Tx, id string) (entryStatus, categoryStatus string, err error) {
	err = tx.QueryRow(ctx, `SELECT entry.status, category.status FROM download_entries entry
		JOIN download_categories category ON category.id=entry.category_id WHERE entry.id=$1
		FOR UPDATE OF entry, category`, id).Scan(&entryStatus, &categoryStatus)
	return
}

func (r *Repository) GetEntryForUpdate(ctx context.Context, tx pgx.Tx, id string) (Entry, error) {
	return scanEntry(tx.QueryRow(ctx, `SELECT `+entryColumns+` FROM download_entries entry
		JOIN download_categories category ON category.id=entry.category_id WHERE entry.id=$1 FOR UPDATE OF entry`, id))
}

func (r *Repository) InsertEntry(ctx context.Context, tx pgx.Tx, item Entry) error {
	_, err := tx.Exec(ctx, `INSERT INTO download_entries (
		id, category_id, slug, title_en, title_zh_cn, description_en, description_zh_cn,
		sort_order, status, created_by, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		item.ID, item.CategoryID, item.Slug, item.Title.EN, item.Title.ZhCN,
		item.Description.EN, item.Description.ZhCN, item.SortOrder, item.Status,
		item.CreatedBy, item.CreatedAt, item.UpdatedAt)
	return err
}

func (r *Repository) UpdateEntry(ctx context.Context, tx pgx.Tx, item Entry) error {
	_, err := tx.Exec(ctx, `UPDATE download_entries SET category_id=$2, slug=$3, title_en=$4,
		title_zh_cn=$5, description_en=$6, description_zh_cn=$7, sort_order=$8, updated_at=$9 WHERE id=$1`,
		item.ID, item.CategoryID, item.Slug, item.Title.EN, item.Title.ZhCN,
		item.Description.EN, item.Description.ZhCN, item.SortOrder, item.UpdatedAt)
	return err
}

func (r *Repository) ArchiveEntry(ctx context.Context, tx pgx.Tx, id, adminID string, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE download_entries SET status='ARCHIVED', archived_by=$2,
		archived_at=$3, updated_at=$3 WHERE id=$1 AND status='ACTIVE'`, id, adminID, now)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *Repository) CountActiveEntries(ctx context.Context, tx pgx.Tx, categoryID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM download_entries WHERE category_id=$1 AND status='ACTIVE'`, categoryID).Scan(&count)
	return count, err
}

func (r *Repository) CountPublishedVersions(ctx context.Context, tx pgx.Tx, entryID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM download_versions WHERE entry_id=$1 AND status='PUBLISHED'`, entryID).Scan(&count)
	return count, err
}

const versionColumns = `id, entry_id, version_label, original_file_name, content_type, size_bytes,
sha256, object_key, status, failure_reason, created_by, COALESCE(published_by, ''),
COALESCE(archived_by, ''), created_at, updated_at, verified_at, published_at, archived_at`

func scanVersion(row rowScanner) (Version, error) {
	var item Version
	err := row.Scan(
		&item.ID, &item.EntryID, &item.VersionLabel, &item.OriginalFileName,
		&item.ContentType, &item.SizeBytes, &item.SHA256, &item.ObjectKey,
		&item.Status, &item.FailureReason, &item.CreatedBy, &item.PublishedBy,
		&item.ArchivedBy, &item.CreatedAt, &item.UpdatedAt, &item.VerifiedAt,
		&item.PublishedAt, &item.ArchivedAt,
	)
	return item, err
}

func (r *Repository) ListVersions(ctx context.Context, entryID string, publicOnly bool) ([]Version, error) {
	query := `SELECT ` + versionColumns + ` FROM download_versions WHERE entry_id=$1`
	if publicOnly {
		query += ` AND status='PUBLISHED'`
	}
	query += ` ORDER BY published_at DESC NULLS LAST, created_at DESC, id`
	rows, err := r.pool.Query(ctx, query, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Version, 0)
	for rows.Next() {
		item, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) GetVersion(ctx context.Context, id string) (Version, error) {
	return scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM download_versions WHERE id=$1`, id))
}

func (r *Repository) GetVersionForUpdate(ctx context.Context, tx pgx.Tx, id string) (Version, error) {
	return scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+` FROM download_versions WHERE id=$1 FOR UPDATE`, id))
}

func (r *Repository) GetPublishedVersion(ctx context.Context, id string) (Version, error) {
	return scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM download_versions
		WHERE id=$1 AND status='PUBLISHED' AND EXISTS (
			SELECT 1 FROM download_entries entry JOIN download_categories category ON category.id=entry.category_id
			WHERE entry.id=download_versions.entry_id AND entry.status='ACTIVE' AND category.status='ACTIVE' AND category.enabled
		)`, id))
}

func (r *Repository) InsertVersionAndSession(ctx context.Context, tx pgx.Tx, version Version, session UploadSession) error {
	if _, err := tx.Exec(ctx, `INSERT INTO download_versions (
		id, entry_id, version_label, original_file_name, content_type, size_bytes, sha256,
		object_key, status, created_by, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, version.ID, version.EntryID,
		version.VersionLabel, version.OriginalFileName, version.ContentType, version.SizeBytes,
		version.SHA256, version.ObjectKey, version.Status, version.CreatedBy, version.CreatedAt, version.UpdatedAt); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO download_upload_sessions (
		id, version_id, strategy, provider_upload_id, part_size_bytes, status, expires_at, created_at, updated_at
	) VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9)`, session.ID, session.VersionID,
		session.Strategy, session.ProviderUploadID, session.PartSizeBytes, session.Status,
		session.ExpiresAt, session.CreatedAt, session.UpdatedAt)
	return err
}

const sessionColumns = `id, version_id, strategy, COALESCE(provider_upload_id, ''), part_size_bytes,
status, expires_at, created_at, updated_at`

func scanSession(row rowScanner) (UploadSession, error) {
	var item UploadSession
	err := row.Scan(&item.ID, &item.VersionID, &item.Strategy, &item.ProviderUploadID,
		&item.PartSizeBytes, &item.Status, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (r *Repository) GetUploadSession(ctx context.Context, id string) (UploadSession, error) {
	item, err := scanSession(r.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM download_upload_sessions WHERE id=$1`, id))
	if err != nil {
		return UploadSession{}, err
	}
	item.Version, err = r.GetVersion(ctx, item.VersionID)
	return item, err
}

func (r *Repository) GetUploadSessionForUpdate(ctx context.Context, tx pgx.Tx, id string) (UploadSession, error) {
	item, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM download_upload_sessions WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return UploadSession{}, err
	}
	item.Version, err = r.GetVersionForUpdate(ctx, tx, item.VersionID)
	return item, err
}

func (r *Repository) MarkUploadComplete(ctx context.Context, tx pgx.Tx, sessionID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE download_upload_sessions SET status='COMPLETED', updated_at=$2 WHERE id=$1`, sessionID, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE download_versions SET status='VERIFYING', updated_at=$2 WHERE id=(
		SELECT version_id FROM download_upload_sessions WHERE id=$1) AND status='UPLOADING'`, sessionID, now)
	return err
}

func (r *Repository) MarkUploadAborted(ctx context.Context, tx pgx.Tx, sessionID, sessionStatus, failure string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE download_upload_sessions SET status=$2, updated_at=$3 WHERE id=$1`, sessionID, sessionStatus, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE download_versions SET status='FAILED', failure_reason=$2, updated_at=$3 WHERE id=(
		SELECT version_id FROM download_upload_sessions WHERE id=$1) AND status IN ('UPLOADING','VERIFYING')`, sessionID, failure, now)
	return err
}

func (r *Repository) ListExpiredUploads(ctx context.Context, now time.Time, limit int) ([]UploadSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionColumns+` FROM download_upload_sessions
		WHERE status='ACTIVE' AND expires_at < $1 ORDER BY expires_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UploadSession, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ClaimVerifying(ctx context.Context, limit int, now, leaseUntil time.Time) ([]Version, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `SELECT `+versionColumns+` FROM download_versions
		WHERE status='VERIFYING' AND (verification_lease_until IS NULL OR verification_lease_until < $1)
		ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Version, 0)
	for rows.Next() {
		item, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, item := range items {
		if _, err := tx.Exec(ctx, `UPDATE download_versions SET verification_lease_until=$2
			WHERE id=$1 AND status='VERIFYING'`, item.ID, leaseUntil); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *Repository) ReleaseVerificationLease(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE download_versions SET verification_lease_until=NULL
		WHERE id=$1 AND status='VERIFYING'`, id)
	return err
}

func (r *Repository) MarkVerified(ctx context.Context, tx pgx.Tx, id string, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE download_versions SET status='DRAFT', verified_at=$2,
		verification_lease_until=NULL, failure_reason='', updated_at=$2 WHERE id=$1 AND status='VERIFYING'`, id, now)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *Repository) MarkVerificationFailed(ctx context.Context, tx pgx.Tx, id, reason string, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE download_versions SET status='FAILED', failure_reason=$2,
		verification_lease_until=NULL, updated_at=$3 WHERE id=$1 AND status='VERIFYING'`, id, reason, now)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *Repository) MarkPublished(ctx context.Context, tx pgx.Tx, id, adminID string, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE download_versions SET status='PUBLISHED', published_by=$2,
		published_at=$3, updated_at=$3 WHERE id=$1 AND status='DRAFT'`, id, adminID, now)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *Repository) MarkArchived(ctx context.Context, tx pgx.Tx, id, adminID string, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE download_versions SET status='ARCHIVED', archived_by=$2,
		archived_at=$3, updated_at=$3 WHERE id=$1 AND status IN ('DRAFT','PUBLISHED','FAILED')`, id, adminID, now)
	if err == nil && result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *Repository) PublicUpdatedAt(ctx context.Context) (time.Time, error) {
	var updated time.Time
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(MAX(updated_at), '1970-01-01'::timestamptz) FROM (
		SELECT category.updated_at FROM download_categories category WHERE category.status='ACTIVE' AND category.enabled
		UNION ALL SELECT entry.updated_at FROM download_entries entry
			JOIN download_categories category ON category.id=entry.category_id
			WHERE entry.status='ACTIVE' AND category.status='ACTIVE' AND category.enabled
		UNION ALL SELECT version.updated_at FROM download_versions version
			JOIN download_entries entry ON entry.id=version.entry_id
			JOIN download_categories category ON category.id=entry.category_id
			WHERE version.status='PUBLISHED' AND entry.status='ACTIVE' AND category.status='ACTIVE' AND category.enabled
	) changed`).Scan(&updated)
	return updated, err
}

func (r *Repository) InsertAudit(ctx context.Context, tx pgx.Tx, id, action, targetType, targetID string,
	oldValue, newValue map[string]any, reason string, meta ActorMeta, now time.Time) error {
	oldJSON, err := json.Marshal(oldValue)
	if err != nil {
		return err
	}
	newJSON, err := json.Marshal(newValue)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_audit_logs (
		id, admin_id, action, target_type, target_id, old_value, new_value, reason,
		request_id, ip_address, user_agent, result, created_at
	) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,NULLIF($9,''),NULLIF($10,'')::inet,$11,'SUCCEEDED',$12)`,
		id, meta.AdminID, action, targetType, targetID, oldJSON, newJSON, reason,
		meta.RequestID, meta.IPAddress, meta.UserAgent, now)
	if err != nil {
		return fmt.Errorf("insert download audit: %w", err)
	}
	return nil
}
