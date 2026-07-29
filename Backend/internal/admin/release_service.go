package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	clientupdate "github.com/Dubnium-105/ProjectRebound/Backend/internal/update"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReleaseManifestService interface {
	BuildAndSign(clientupdate.SourceRelease) (clientupdate.Manifest, error)
	VerifySignedManifest(clientupdate.Manifest) error
	VerifyReleaseObjects(context.Context, clientupdate.Manifest) error
}

type ReleaseService struct {
	pool       *pgxpool.Pool
	repository *ReleaseRepository
	audits     *Repository
	manifests  ReleaseManifestService
	product    string
	now        func() time.Time
}

func NewReleaseService(
	pool *pgxpool.Pool,
	repository *ReleaseRepository,
	audits *Repository,
	manifests ReleaseManifestService,
	product string,
) *ReleaseService {
	return &ReleaseService{
		pool: pool, repository: repository, audits: audits,
		manifests: manifests, product: product, now: time.Now,
	}
}

func (s *ReleaseService) Create(
	ctx context.Context,
	input ReleaseCreateInput,
	meta RequestMeta,
) (Release, error) {
	meta, reason, err := validateOnlineOperation(meta, input.Reason)
	if err != nil {
		return Release{}, err
	}
	source := clientupdate.SourceRelease{
		SchemaVersion: 1, Product: s.product,
		Platform:                strings.ToLower(strings.TrimSpace(input.Platform)),
		Architecture:            strings.ToLower(strings.TrimSpace(input.Architecture)),
		Channel:                 strings.ToLower(strings.TrimSpace(input.Channel)),
		Version:                 strings.TrimSpace(input.Version),
		MinimumSupportedVersion: strings.TrimSpace(input.MinimumSupportedVersion),
		PublishedAt:             s.now().UTC(), Files: append([]clientupdate.SourceFile(nil), input.Files...),
	}
	if _, err := s.manifests.BuildAndSign(source); err != nil {
		return Release{}, &ServiceError{
			Status: 400, Code: "RELEASE_INVALID", Message: "Release metadata or files are invalid.",
			Details: map[string]any{"validation": err.Error()},
		}
	}
	now := s.now().UTC()
	item := Release{
		ID: newID("rel_"), Product: source.Product, Platform: source.Platform,
		Architecture: source.Architecture, Channel: source.Channel, Version: source.Version,
		MinimumSupportedVersion: source.MinimumSupportedVersion, ForceUpdate: input.ForceUpdate,
		Status: ReleaseStatusDraft, Source: source,
		Validation: ReleaseValidation{Valid: false, Checks: []ReleaseValidationCheck{}},
		CreatedBy:  meta.AdminID, CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Release{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.Insert(ctx, tx, item); err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return Release{}, &ServiceError{
				Status: 409, Code: "RELEASE_ALREADY_EXISTS",
				Message: "A release with the same platform, architecture, channel, and version already exists.",
			}
		}
		return Release{}, internal(err)
	}
	if err := s.insertReleaseAudit(
		ctx, tx, meta, "RELEASE_CREATED", item, map[string]any{},
		releaseAuditValue(item), reason, now,
	); err != nil {
		return Release{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, internal(fmt.Errorf("commit release creation: %w", err))
	}
	return item, nil
}

func (s *ReleaseService) List(ctx context.Context, filter ReleaseListFilter) (ReleaseListResult, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return ReleaseListResult{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid limit."}
	}
	if filter.Status != "" && !validReleaseStatus(filter.Status) {
		return ReleaseListResult{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid release status."}
	}
	filter.Limit++
	items, err := s.repository.List(ctx, filter)
	if err != nil {
		return ReleaseListResult{}, internal(err)
	}
	limit := filter.Limit - 1
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return ReleaseListResult{Items: items, NextCursor: nextCursor}, nil
}

func (s *ReleaseService) Get(ctx context.Context, id string) (Release, error) {
	item, err := s.repository.Get(ctx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, &ServiceError{Status: 404, Code: "RELEASE_NOT_FOUND", Message: "Release not found."}
	}
	if err != nil {
		return Release{}, internal(err)
	}
	return item, nil
}

func (s *ReleaseService) Validate(
	ctx context.Context,
	id, reasonInput string,
	meta RequestMeta,
) (Release, error) {
	return s.transition(ctx, id, reasonInput, meta, "validate")
}

func (s *ReleaseService) Publish(
	ctx context.Context,
	id, reasonInput string,
	meta RequestMeta,
) (Release, error) {
	return s.transition(ctx, id, reasonInput, meta, "publish")
}

func (s *ReleaseService) Rollback(
	ctx context.Context,
	id, reasonInput string,
	meta RequestMeta,
) (Release, error) {
	return s.transition(ctx, id, reasonInput, meta, "rollback")
}

func (s *ReleaseService) Archive(
	ctx context.Context,
	id, reasonInput string,
	meta RequestMeta,
) (Release, error) {
	return s.transition(ctx, id, reasonInput, meta, "archive")
}

func (s *ReleaseService) transition(
	ctx context.Context,
	id, reasonInput string,
	meta RequestMeta,
	operation string,
) (Release, error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return Release{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Release{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldItem, err := s.repository.GetForUpdate(ctx, tx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, &ServiceError{Status: 404, Code: "RELEASE_NOT_FOUND", Message: "Release not found."}
	}
	if err != nil {
		return Release{}, internal(err)
	}
	now := s.now().UTC()
	item := oldItem
	action := ""
	switch operation {
	case "validate":
		if oldItem.Status != ReleaseStatusDraft && oldItem.Status != ReleaseStatusReady {
			return Release{}, releaseStateConflict("Only DRAFT or READY releases can be validated.")
		}
		source := oldItem.Source
		source.PublishedAt = now
		manifest, validation, err := s.validateManifest(ctx, source)
		if err != nil {
			return Release{}, err
		}
		item, err = s.repository.MarkReady(ctx, tx, oldItem.ID, source, manifest, validation, now)
		action = "RELEASE_VALIDATED"
		if err != nil {
			return Release{}, internal(err)
		}
	case "publish":
		if oldItem.Status != ReleaseStatusReady {
			return Release{}, releaseStateConflict("Only a validated READY release can be published.")
		}
		source := oldItem.Source
		source.PublishedAt = now
		if oldItem.ForceUpdate {
			source.MinimumSupportedVersion = source.Version
		}
		manifest, validation, err := s.validateManifest(ctx, source)
		if err != nil {
			return Release{}, err
		}
		item, err = s.repository.MarkPublished(
			ctx, tx, oldItem.ID, meta.AdminID, source, manifest, validation, now,
		)
		action = "RELEASE_PUBLISHED"
		if err != nil {
			return Release{}, internal(err)
		}
	case "rollback":
		if oldItem.Status != ReleaseStatusPublished {
			return Release{}, releaseStateConflict("Only a PUBLISHED release can be rolled back.")
		}
		item, err = s.repository.MarkRolledBack(ctx, tx, oldItem.ID, meta.AdminID, now)
		action = "RELEASE_ROLLED_BACK"
		if err != nil {
			return Release{}, internal(err)
		}
	case "archive":
		if oldItem.Status == ReleaseStatusPublished {
			return Release{}, releaseStateConflict("A PUBLISHED release must be rolled back before it can be archived.")
		}
		if oldItem.Status != ReleaseStatusDraft &&
			oldItem.Status != ReleaseStatusReady &&
			oldItem.Status != ReleaseStatusRolledBack {
			return Release{}, releaseStateConflict("Only DRAFT, READY, or ROLLED_BACK releases can be archived.")
		}
		item, err = s.repository.MarkArchived(ctx, tx, oldItem.ID, meta.AdminID, now)
		action = "RELEASE_ARCHIVED"
		if err != nil {
			return Release{}, internal(err)
		}
	default:
		return Release{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid release operation."}
	}
	if err := s.insertReleaseAudit(
		ctx, tx, meta, action, item, releaseAuditValue(oldItem),
		releaseAuditValue(item), reason, now,
	); err != nil {
		return Release{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, internal(fmt.Errorf("commit release transition: %w", err))
	}
	return item, nil
}

func (s *ReleaseService) validateManifest(
	ctx context.Context,
	source clientupdate.SourceRelease,
) (clientupdate.Manifest, ReleaseValidation, error) {
	manifest, err := s.manifests.BuildAndSign(source)
	if err != nil {
		return clientupdate.Manifest{}, ReleaseValidation{}, &ServiceError{
			Status: 400, Code: "RELEASE_VALIDATION_FAILED",
			Message: "Release validation failed.", Details: map[string]any{"validation": err.Error()},
		}
	}
	if err := s.manifests.VerifySignedManifest(manifest); err != nil {
		return clientupdate.Manifest{}, ReleaseValidation{}, internal(err)
	}
	if err := s.manifests.VerifyReleaseObjects(ctx, manifest); err != nil {
		return clientupdate.Manifest{}, ReleaseValidation{}, &ServiceError{
			Status: 400, Code: "RELEASE_OBJECT_UNAVAILABLE",
			Message: "One or more release objects are unavailable.",
			Details: map[string]any{"validation": err.Error()},
		}
	}
	validation := ReleaseValidation{
		Valid: true,
		Checks: []ReleaseValidationCheck{
			{Key: "manifest_schema", Passed: true, Message: "Manifest schema and release identity are valid."},
			{Key: "file_hashes", Passed: true, Message: "Every file has a valid SHA-256, size, path, and compression mode."},
			{Key: "object_urls", Passed: true, Message: "Every object URL resolved under the configured CDN base URL and passed an HTTP HEAD availability probe."},
			{Key: "manifest_signature", Passed: true, Message: "Ed25519 manifest signature verified."},
			{Key: "version_compatibility", Passed: true, Message: "Version and minimum-supported-version ordering is valid."},
		},
	}
	return manifest, validation, nil
}

func (s *ReleaseService) PublishedManifests(ctx context.Context) ([]clientupdate.Manifest, error) {
	manifests, err := s.repository.PublishedManifests(ctx)
	if err != nil {
		return nil, err
	}
	for _, manifest := range manifests {
		if err := s.manifests.VerifySignedManifest(manifest); err != nil {
			return nil, err
		}
	}
	return manifests, nil
}

func (s *ReleaseService) insertReleaseAudit(
	ctx context.Context,
	tx pgx.Tx,
	meta RequestMeta,
	action string,
	item Release,
	oldValue, newValue map[string]any,
	reason string,
	now time.Time,
) error {
	return s.audits.InsertAudit(ctx, tx, AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID, Action: action,
		TargetType: "release", TargetID: item.ID,
		OldValue: oldValue, NewValue: newValue, Reason: reason,
		RequestID: meta.RequestID, IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: "SUCCEEDED", CreatedAt: now,
	})
}

func releaseAuditValue(item Release) map[string]any {
	value := map[string]any{
		"platform": item.Platform, "architecture": item.Architecture,
		"channel": item.Channel, "version": item.Version,
		"minimum_supported_version": item.MinimumSupportedVersion,
		"force_update":              item.ForceUpdate, "status": item.Status,
	}
	if item.Manifest != nil {
		value["manifest_hash"] = item.Manifest.ManifestHash
		value["key_id"] = item.Manifest.KeyID
	}
	return value
}

func validReleaseStatus(value string) bool {
	switch value {
	case ReleaseStatusDraft, ReleaseStatusReady, ReleaseStatusPublished,
		ReleaseStatusRolledBack, ReleaseStatusArchived:
		return true
	default:
		return false
	}
}

func releaseStateConflict(message string) error {
	return &ServiceError{Status: http.StatusConflict, Code: "INVALID_RELEASE_STATE", Message: message}
}
