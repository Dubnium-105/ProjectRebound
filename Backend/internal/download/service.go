package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Service struct {
	pool       *pgxpool.Pool
	repository *Repository
	storage    ObjectStorage
	config     config.DownloadConfig
	now        func() time.Time
	probe      func(context.Context, string) error
}

func NewService(pool *pgxpool.Pool, repository *Repository, storage ObjectStorage, cfg config.DownloadConfig) *Service {
	return &Service{
		pool: pool, repository: repository, storage: storage, config: cfg, now: time.Now,
		probe: func(ctx context.Context, target string) error {
			request, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
			if err != nil {
				return err
			}
			response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 400 {
				return fmt.Errorf("public object returned HTTP %d", response.StatusCode)
			}
			return nil
		},
	}
}

func (s *Service) Enabled() bool { return s.config.Enabled && s.storage != nil }

func (s *Service) Capabilities() Capabilities {
	allowed := append([]string(nil), s.config.AllowedExtensions...)
	return Capabilities{
		Enabled: s.Enabled(), MaxFileBytes: s.config.MaxFileBytes, AllowedExtensions: allowed,
		MultipartThresholdBytes: s.config.MultipartThresholdBytes, PartSizeBytes: s.config.PartSizeBytes,
		UploadSessionTTLHours: s.config.UploadSessionTTLHours, PresignedRequestTTLMinutes: s.config.PresignTTLMinutes,
	}
}

func (s *Service) ListCategories(ctx context.Context) ([]Category, error) {
	items, err := s.repository.ListCategories(ctx, false)
	if err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *Service) CreateCategory(ctx context.Context, input CategoryInput, meta ActorMeta) (Category, error) {
	input, reason, err := validateCategoryInput(input, meta)
	if err != nil {
		return Category{}, err
	}
	now := s.now().UTC()
	item := Category{
		ID: newID("dcat_"), Slug: input.Slug,
		Title:       LocalizedText{EN: input.TitleEN, ZhCN: input.TitleZhCN},
		Description: LocalizedText{EN: input.DescriptionEN, ZhCN: input.DescriptionZhCN},
		SortOrder:   input.SortOrder, Enabled: input.Enabled, Status: CategoryStatusActive, CreatedBy: meta.AdminID,
		CreatedAt: now, UpdatedAt: now,
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if err := s.repository.InsertCategory(ctx, tx, item); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_CATEGORY_CREATED", "download_category", item.ID,
			map[string]any{}, categoryAudit(item), reason, meta, now)
	})
	if duplicate(err) {
		return Category{}, conflict("DOWNLOAD_CATEGORY_EXISTS", "A download category with this slug already exists.")
	}
	if err != nil {
		return Category{}, internal(err)
	}
	return item, nil
}

func (s *Service) UpdateCategory(ctx context.Context, id string, input CategoryInput, meta ActorMeta) (Category, error) {
	input, reason, err := validateCategoryInput(input, meta)
	if err != nil {
		return Category{}, err
	}
	var item Category
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		old, err := s.repository.GetCategoryForUpdate(ctx, tx, strings.TrimSpace(id))
		if err != nil {
			return err
		}
		if old.Status != CategoryStatusActive {
			return conflict("INVALID_DOWNLOAD_CATEGORY_STATE", "Only an active category can be updated.")
		}
		if input.Slug != old.Slug {
			return conflict("DOWNLOAD_CATEGORY_SLUG_IMMUTABLE", "A download category slug cannot be changed after creation.")
		}
		item = old
		item.Slug, item.Title.EN, item.Title.ZhCN = input.Slug, input.TitleEN, input.TitleZhCN
		item.Description.EN, item.Description.ZhCN = input.DescriptionEN, input.DescriptionZhCN
		item.SortOrder, item.Enabled, item.UpdatedAt = input.SortOrder, input.Enabled, s.now().UTC()
		if err := s.repository.UpdateCategory(ctx, tx, item); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_CATEGORY_UPDATED", "download_category", item.ID,
			categoryAudit(old), categoryAudit(item), reason, meta, item.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Category{}, notFound("Download category not found.")
	}
	if duplicate(err) {
		return Category{}, conflict("DOWNLOAD_CATEGORY_EXISTS", "A download category with this slug already exists.")
	}
	if err != nil {
		var serviceError *ServiceError
		if errors.As(err, &serviceError) {
			return Category{}, err
		}
		return Category{}, internal(err)
	}
	return item, nil
}

func (s *Service) ArchiveCategory(ctx context.Context, id, reasonInput string, meta ActorMeta) (Category, error) {
	reason, err := validateReason(meta, reasonInput)
	if err != nil {
		return Category{}, err
	}
	var item Category
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		old, err := s.repository.GetCategoryForUpdate(ctx, tx, strings.TrimSpace(id))
		if err != nil {
			return err
		}
		count, err := s.repository.CountActiveEntries(ctx, tx, old.ID)
		if err != nil {
			return err
		}
		if count > 0 {
			return conflict("DOWNLOAD_CATEGORY_NOT_EMPTY", "Archive every active item in this category first.")
		}
		now := s.now().UTC()
		if err := s.repository.ArchiveCategory(ctx, tx, old.ID, meta.AdminID, now); err != nil {
			return err
		}
		item = old
		item.Status, item.ArchivedBy, item.UpdatedAt, item.ArchivedAt = CategoryStatusArchived, meta.AdminID, now, &now
		return s.audit(ctx, tx, "DOWNLOAD_CATEGORY_ARCHIVED", "download_category", item.ID,
			categoryAudit(old), categoryAudit(item), reason, meta, now)
	})
	return item, mapTxError(err, "Download category not found.")
}

func (s *Service) ListEntries(ctx context.Context) ([]Entry, error) {
	items, err := s.repository.ListEntries(ctx, false)
	if err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *Service) ListReleaseFiles(ctx context.Context) ([]ReleaseFile, error) {
	if !s.Enabled() {
		return nil, &ServiceError{
			Status: http.StatusServiceUnavailable, Code: "DOWNLOAD_STORAGE_DISABLED",
			Message: "Download storage is not configured.",
		}
	}
	entries, err := s.repository.ListEntries(ctx, false)
	if err != nil {
		return nil, internal(err)
	}
	return releaseFilesFromEntries(entries), nil
}

func releaseFilesFromEntries(entries []Entry) []ReleaseFile {
	files := make([]ReleaseFile, 0)
	for _, entry := range entries {
		for _, version := range entry.Versions {
			if version.VerifiedAt == nil ||
				(version.Status != VersionStatusDraft && version.Status != VersionStatusPublished) {
				continue
			}
			files = append(files, ReleaseFile{
				ID: version.ID, VersionLabel: version.VersionLabel,
				OriginalFileName: version.OriginalFileName, SizeBytes: version.SizeBytes,
				SHA256: version.SHA256, ObjectKey: version.ObjectKey,
				Status: version.Status, VerifiedAt: *version.VerifiedAt,
			})
		}
	}
	return files
}

func (s *Service) GetEntry(ctx context.Context, id string) (Entry, error) {
	item, err := s.repository.GetEntry(ctx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, notFound("Download item not found.")
	}
	if err != nil {
		return Entry{}, internal(err)
	}
	return item, nil
}

func (s *Service) CreateEntry(ctx context.Context, input EntryInput, meta ActorMeta) (Entry, error) {
	input, reason, err := validateEntryInput(input, meta)
	if err != nil {
		return Entry{}, err
	}
	now := s.now().UTC()
	item := Entry{
		ID: newID("dent_"), CategoryID: input.CategoryID, Slug: input.Slug,
		Title:       LocalizedText{EN: input.TitleEN, ZhCN: input.TitleZhCN},
		Description: LocalizedText{EN: input.DescriptionEN, ZhCN: input.DescriptionZhCN},
		SortOrder:   input.SortOrder, Status: EntryStatusActive, CreatedBy: meta.AdminID,
		CreatedAt: now, UpdatedAt: now, Versions: make([]Version, 0),
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		category, err := s.repository.GetCategoryForUpdate(ctx, tx, item.CategoryID)
		if err != nil {
			return err
		}
		if category.Status != CategoryStatusActive {
			return conflict("INVALID_DOWNLOAD_CATEGORY_STATE", "The selected download category is not active.")
		}
		item.CategorySlug = category.Slug
		if err := s.repository.InsertEntry(ctx, tx, item); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_ITEM_CREATED", "download_item", item.ID,
			map[string]any{}, entryAudit(item), reason, meta, now)
	})
	if duplicate(err) {
		return Entry{}, conflict("DOWNLOAD_ITEM_EXISTS", "A download item with this slug already exists.")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, notFound("Download category not found.")
	}
	if err != nil {
		var serviceError *ServiceError
		if errors.As(err, &serviceError) {
			return Entry{}, err
		}
		return Entry{}, internal(err)
	}
	return item, nil
}

func (s *Service) UpdateEntry(ctx context.Context, id string, input EntryInput, meta ActorMeta) (Entry, error) {
	input, reason, err := validateEntryInput(input, meta)
	if err != nil {
		return Entry{}, err
	}
	var item Entry
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		old, err := s.repository.GetEntryForUpdate(ctx, tx, strings.TrimSpace(id))
		if err != nil {
			return err
		}
		if old.Status != EntryStatusActive {
			return conflict("INVALID_DOWNLOAD_ITEM_STATE", "Only an active download item can be updated.")
		}
		if input.Slug != old.Slug {
			return conflict("DOWNLOAD_ITEM_SLUG_IMMUTABLE", "A download item slug cannot be changed after creation.")
		}
		category, err := s.repository.GetCategoryForUpdate(ctx, tx, input.CategoryID)
		if err != nil {
			return err
		}
		if category.Status != CategoryStatusActive {
			return conflict("INVALID_DOWNLOAD_CATEGORY_STATE", "The selected download category is not active.")
		}
		item = old
		item.CategoryID, item.CategorySlug, item.Slug = input.CategoryID, category.Slug, input.Slug
		item.Title.EN, item.Title.ZhCN = input.TitleEN, input.TitleZhCN
		item.Description.EN, item.Description.ZhCN = input.DescriptionEN, input.DescriptionZhCN
		item.SortOrder, item.UpdatedAt = input.SortOrder, s.now().UTC()
		if err := s.repository.UpdateEntry(ctx, tx, item); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_ITEM_UPDATED", "download_item", item.ID,
			entryAudit(old), entryAudit(item), reason, meta, item.UpdatedAt)
	})
	if duplicate(err) {
		return Entry{}, conflict("DOWNLOAD_ITEM_EXISTS", "A download item with this slug already exists.")
	}
	return item, mapTxError(err, "Download item or category not found.")
}

func (s *Service) ArchiveEntry(ctx context.Context, id, reasonInput string, meta ActorMeta) (Entry, error) {
	reason, err := validateReason(meta, reasonInput)
	if err != nil {
		return Entry{}, err
	}
	var item Entry
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		old, err := s.repository.GetEntryForUpdate(ctx, tx, strings.TrimSpace(id))
		if err != nil {
			return err
		}
		count, err := s.repository.CountPublishedVersions(ctx, tx, old.ID)
		if err != nil {
			return err
		}
		if count > 0 {
			return conflict("DOWNLOAD_ITEM_HAS_PUBLISHED_VERSIONS", "Archive every published version first.")
		}
		now := s.now().UTC()
		if err := s.repository.ArchiveEntry(ctx, tx, old.ID, meta.AdminID, now); err != nil {
			return err
		}
		item = old
		item.Status, item.ArchivedBy, item.UpdatedAt, item.ArchivedAt = EntryStatusArchived, meta.AdminID, now, &now
		return s.audit(ctx, tx, "DOWNLOAD_ITEM_ARCHIVED", "download_item", item.ID,
			entryAudit(old), entryAudit(item), reason, meta, now)
	})
	return item, mapTxError(err, "Download item not found.")
}

func (s *Service) CreateUpload(ctx context.Context, entryID string, input UploadInput, meta ActorMeta) (UploadCreated, error) {
	if !s.Enabled() {
		return UploadCreated{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "DOWNLOAD_STORAGE_DISABLED", Message: "Download storage is not configured."}
	}
	input, reason, err := s.validateUploadInput(input, meta)
	if err != nil {
		return UploadCreated{}, err
	}
	entry, err := s.repository.GetEntry(ctx, strings.TrimSpace(entryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadCreated{}, notFound("Download item not found.")
	}
	if err != nil {
		return UploadCreated{}, internal(err)
	}
	if entry.Status != EntryStatusActive {
		return UploadCreated{}, conflict("INVALID_DOWNLOAD_ITEM_STATE", "Files can only be uploaded to an active download item.")
	}
	now := s.now().UTC()
	version := Version{
		ID: newID("dver_"), EntryID: entry.ID, VersionLabel: input.VersionLabel,
		OriginalFileName: input.FileName, ContentType: input.ContentType,
		SizeBytes: input.SizeBytes, SHA256: input.SHA256, Status: VersionStatusUploading,
		CreatedBy: meta.AdminID, CreatedAt: now, UpdatedAt: now,
	}
	version.ObjectKey = objectKey(entry, version)
	strategy := UploadStrategySingle
	partSize := input.SizeBytes
	providerID := ""
	if input.SizeBytes > s.config.MultipartThresholdBytes {
		strategy, partSize = UploadStrategyMultipart, s.config.PartSizeBytes
		providerID, err = s.storage.CreateMultipart(ctx, version)
		if err != nil {
			return UploadCreated{}, internal(err)
		}
	}
	session := UploadSession{
		ID: newID("dupl_"), VersionID: version.ID, Strategy: strategy,
		ProviderUploadID: providerID, PartSizeBytes: partSize, Status: UploadStatusActive,
		ExpiresAt: now.Add(s.config.UploadSessionTTL()), CreatedAt: now, UpdatedAt: now, Version: version,
		UploadedParts: make([]UploadedPart, 0),
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if err := s.repository.InsertVersionAndSession(ctx, tx, version, session); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_UPLOAD_CREATED", "download_version", version.ID,
			map[string]any{}, versionAudit(version), reason, meta, now)
	})
	if err != nil {
		if strategy == UploadStrategyMultipart {
			_ = s.storage.AbortMultipart(context.WithoutCancel(ctx), version, providerID)
		}
		if duplicate(err) {
			return UploadCreated{}, conflict("DOWNLOAD_VERSION_EXISTS", "This download item already has the same version label.")
		}
		return UploadCreated{}, internal(err)
	}
	created := UploadCreated{Session: session}
	if strategy == UploadStrategySingle {
		request, err := s.storage.PresignPut(ctx, version, s.config.PresignTTL())
		if err != nil {
			return UploadCreated{}, internal(err)
		}
		created.Request = &request
	}
	return created, nil
}

func (s *Service) GetUpload(ctx context.Context, id string) (UploadSession, error) {
	if !s.Enabled() {
		return UploadSession{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "DOWNLOAD_STORAGE_DISABLED", Message: "Download storage is not configured."}
	}
	item, err := s.repository.GetUploadSession(ctx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadSession{}, notFound("Download upload session not found.")
	}
	if err != nil {
		return UploadSession{}, internal(err)
	}
	item.UploadedParts = make([]UploadedPart, 0)
	if item.Status == UploadStatusActive && item.Strategy == UploadStrategyMultipart {
		item.UploadedParts, err = s.storage.ListParts(ctx, item.Version, item.ProviderUploadID)
		if err != nil {
			return UploadSession{}, internal(err)
		}
	}
	return item, nil
}

func (s *Service) SignParts(ctx context.Context, id string, partNumbers []int32) ([]SignedPart, error) {
	item, err := s.GetUpload(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != UploadStatusActive || !s.now().UTC().Before(item.ExpiresAt) {
		return nil, conflict("INVALID_DOWNLOAD_UPLOAD_STATE", "The upload is not active.")
	}
	if len(partNumbers) < 1 || len(partNumbers) > 20 {
		return nil, invalid("INVALID_UPLOAD_PARTS", "Request between one and twenty upload parts.", nil)
	}
	// A single PUT is resumable by issuing a fresh short-lived signature for the
	// same immutable object key. Treat it as logical part 1 so the browser can use
	// the same recovery endpoint after a reload or an expired presigned URL.
	if item.Strategy == UploadStrategySingle {
		if len(partNumbers) != 1 || partNumbers[0] != 1 {
			return nil, invalid("INVALID_UPLOAD_PARTS", "A single upload only accepts part number one.", nil)
		}
		request, err := s.storage.PresignPut(ctx, item.Version, s.config.PresignTTL())
		if err != nil {
			return nil, internal(err)
		}
		return []SignedPart{{PartNumber: 1, Request: request}}, nil
	}
	if item.Strategy != UploadStrategyMultipart {
		return nil, conflict("INVALID_DOWNLOAD_UPLOAD_STATE", "The upload strategy is not supported.")
	}
	maximum := int32((item.Version.SizeBytes + item.PartSizeBytes - 1) / item.PartSizeBytes)
	seen := make(map[int32]struct{}, len(partNumbers))
	parts := make([]SignedPart, 0, len(partNumbers))
	for _, number := range partNumbers {
		if number < 1 || number > maximum {
			return nil, invalid("INVALID_UPLOAD_PARTS", "An upload part number is outside the file range.", nil)
		}
		if _, exists := seen[number]; exists {
			return nil, invalid("INVALID_UPLOAD_PARTS", "Upload part numbers must be unique.", nil)
		}
		seen[number] = struct{}{}
		request, err := s.storage.PresignPart(ctx, item.Version, item.ProviderUploadID, number, s.config.PresignTTL())
		if err != nil {
			return nil, internal(err)
		}
		parts = append(parts, SignedPart{PartNumber: number, Request: request})
	}
	return parts, nil
}

func (s *Service) CompleteUpload(ctx context.Context, id, reasonInput string, meta ActorMeta) (UploadSession, error) {
	reason, err := validateReason(meta, reasonInput)
	if err != nil {
		return UploadSession{}, err
	}
	item, err := s.repository.GetUploadSession(ctx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadSession{}, notFound("Download upload session not found.")
	}
	if err != nil {
		return UploadSession{}, internal(err)
	}
	if item.Status != UploadStatusActive || item.Version.Status != VersionStatusUploading || !s.now().UTC().Before(item.ExpiresAt) {
		return UploadSession{}, conflict("INVALID_DOWNLOAD_UPLOAD_STATE", "The upload session is not active.")
	}
	if item.Strategy == UploadStrategyMultipart {
		parts, err := s.storage.ListParts(ctx, item.Version, item.ProviderUploadID)
		if err != nil {
			return UploadSession{}, internal(err)
		}
		if err := validateUploadedParts(item, parts); err != nil {
			return UploadSession{}, err
		}
		if err := s.storage.CompleteMultipart(ctx, item.Version, item.ProviderUploadID, parts); err != nil {
			metadata, headErr := s.storage.Head(ctx, item.Version.ObjectKey)
			if headErr != nil || metadata.SizeBytes != item.Version.SizeBytes {
				return UploadSession{}, internal(err)
			}
		}
	} else {
		metadata, err := s.storage.Head(ctx, item.Version.ObjectKey)
		if err != nil {
			return UploadSession{}, invalid("DOWNLOAD_OBJECT_MISSING", "The uploaded object is not available yet.", nil)
		}
		if metadata.SizeBytes != item.Version.SizeBytes {
			return UploadSession{}, invalid("DOWNLOAD_SIZE_MISMATCH", "The uploaded object size does not match the declared file.", nil)
		}
	}
	now := s.now().UTC()
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		locked, err := s.repository.GetUploadSessionForUpdate(ctx, tx, item.ID)
		if err != nil {
			return err
		}
		if locked.Status != UploadStatusActive || locked.Version.Status != VersionStatusUploading {
			return conflict("INVALID_DOWNLOAD_UPLOAD_STATE", "The upload session is no longer active.")
		}
		if err := s.repository.MarkUploadComplete(ctx, tx, item.ID, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_UPLOAD_COMPLETED", "download_version", item.Version.ID,
			versionAudit(item.Version), map[string]any{"status": VersionStatusVerifying}, reason, meta, now)
	})
	if err != nil {
		var serviceError *ServiceError
		if errors.As(err, &serviceError) {
			return UploadSession{}, err
		}
		return UploadSession{}, internal(err)
	}
	item.Status, item.UpdatedAt = UploadStatusCompleted, now
	item.Version.Status, item.Version.UpdatedAt = VersionStatusVerifying, now
	return item, nil
}

func (s *Service) AbortUpload(ctx context.Context, id, reasonInput string, meta ActorMeta) (UploadSession, error) {
	reason, err := validateReason(meta, reasonInput)
	if err != nil {
		return UploadSession{}, err
	}
	item, err := s.repository.GetUploadSession(ctx, strings.TrimSpace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadSession{}, notFound("Download upload session not found.")
	}
	if err != nil {
		return UploadSession{}, internal(err)
	}
	if item.Status != UploadStatusActive {
		return UploadSession{}, conflict("INVALID_DOWNLOAD_UPLOAD_STATE", "Only an active upload can be aborted.")
	}
	if item.Strategy == UploadStrategyMultipart {
		if err := s.storage.AbortMultipart(ctx, item.Version, item.ProviderUploadID); err != nil {
			return UploadSession{}, internal(err)
		}
	} else {
		_ = s.storage.Delete(ctx, item.Version.ObjectKey)
	}
	now := s.now().UTC()
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if err := s.repository.MarkUploadAborted(ctx, tx, item.ID, UploadStatusAborted, "Upload aborted by administrator.", now); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_UPLOAD_ABORTED", "download_version", item.Version.ID,
			versionAudit(item.Version), map[string]any{"status": VersionStatusFailed}, reason, meta, now)
	})
	if err != nil {
		return UploadSession{}, internal(err)
	}
	item.Status, item.UpdatedAt = UploadStatusAborted, now
	item.Version.Status, item.Version.FailureReason = VersionStatusFailed, "Upload aborted by administrator."
	return item, nil
}

func (s *Service) Publish(ctx context.Context, versionID, reasonInput string, meta ActorMeta) (Version, error) {
	reason, err := validateReason(meta, reasonInput)
	if err != nil {
		return Version{}, err
	}
	version, err := s.repository.GetVersion(ctx, strings.TrimSpace(versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, notFound("Download version not found.")
	}
	if err != nil {
		return Version{}, internal(err)
	}
	if version.Status != VersionStatusDraft {
		return Version{}, conflict("INVALID_DOWNLOAD_VERSION_STATE", "Only a verified DRAFT version can be published.")
	}
	entryStatus, categoryStatus, err := s.repository.EntryLifecycle(ctx, version.EntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, notFound("Download item not found.")
	}
	if err != nil {
		return Version{}, internal(err)
	}
	if entryStatus != EntryStatusActive || categoryStatus != CategoryStatusActive {
		return Version{}, conflict("INVALID_DOWNLOAD_ITEM_STATE", "A version can only be published from an active download item and category.")
	}
	metadata, err := s.storage.Head(ctx, version.ObjectKey)
	if err != nil || metadata.SizeBytes != version.SizeBytes {
		return Version{}, invalid("DOWNLOAD_OBJECT_UNAVAILABLE", "The verified object is not available in storage.", nil)
	}
	if err := s.probe(ctx, s.storage.PublicProbeURL(version.ObjectKey)); err != nil {
		return Version{}, &ServiceError{Status: http.StatusBadRequest, Code: "DOWNLOAD_PUBLIC_URL_UNAVAILABLE", Message: "The public CDN object is not available.", Cause: err}
	}
	var item Version
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		old, err := s.repository.GetVersionForUpdate(ctx, tx, version.ID)
		if err != nil {
			return err
		}
		if old.Status != VersionStatusDraft {
			return conflict("INVALID_DOWNLOAD_VERSION_STATE", "Only a verified DRAFT version can be published.")
		}
		entryStatus, categoryStatus, err := s.repository.EntryLifecycleForUpdate(ctx, tx, old.EntryID)
		if err != nil {
			return err
		}
		if entryStatus != EntryStatusActive || categoryStatus != CategoryStatusActive {
			return conflict("INVALID_DOWNLOAD_ITEM_STATE", "A version can only be published from an active download item and category.")
		}
		now := s.now().UTC()
		if err := s.repository.MarkPublished(ctx, tx, old.ID, meta.AdminID, now); err != nil {
			return err
		}
		item = old
		item.Status, item.PublishedBy, item.PublishedAt, item.UpdatedAt = VersionStatusPublished, meta.AdminID, &now, now
		return s.audit(ctx, tx, "DOWNLOAD_VERSION_PUBLISHED", "download_version", item.ID,
			versionAudit(old), versionAudit(item), reason, meta, now)
	})
	return item, mapTxError(err, "Download version not found.")
}

func (s *Service) ArchiveVersion(ctx context.Context, versionID, reasonInput string, meta ActorMeta) (Version, error) {
	reason, err := validateReason(meta, reasonInput)
	if err != nil {
		return Version{}, err
	}
	var item Version
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		old, err := s.repository.GetVersionForUpdate(ctx, tx, strings.TrimSpace(versionID))
		if err != nil {
			return err
		}
		if old.Status != VersionStatusDraft && old.Status != VersionStatusPublished && old.Status != VersionStatusFailed {
			return conflict("INVALID_DOWNLOAD_VERSION_STATE", "Only DRAFT, PUBLISHED, or FAILED versions can be archived.")
		}
		now := s.now().UTC()
		if err := s.repository.MarkArchived(ctx, tx, old.ID, meta.AdminID, now); err != nil {
			return err
		}
		item = old
		item.Status, item.ArchivedBy, item.ArchivedAt, item.UpdatedAt = VersionStatusArchived, meta.AdminID, &now, now
		return s.audit(ctx, tx, "DOWNLOAD_VERSION_ARCHIVED", "download_version", item.ID,
			versionAudit(old), versionAudit(item), reason, meta, now)
	})
	return item, mapTxError(err, "Download version not found.")
}

func (s *Service) Catalog(ctx context.Context) (Catalog, error) {
	if !s.Enabled() {
		return Catalog{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "DOWNLOAD_STORAGE_DISABLED", Message: "Download storage is not configured."}
	}
	categories, err := s.repository.ListCategories(ctx, true)
	if err != nil {
		return Catalog{}, internal(err)
	}
	items, err := s.repository.ListEntries(ctx, true)
	if err != nil {
		return Catalog{}, internal(err)
	}
	updatedAt, err := s.repository.PublicUpdatedAt(ctx)
	if err != nil {
		return Catalog{}, internal(err)
	}
	publicCategories := make([]PublicCategory, 0, len(categories))
	for _, category := range categories {
		publicCategories = append(publicCategories, PublicCategory{
			ID: category.ID, Slug: category.Slug, Title: category.Title,
			Description: category.Description, SortOrder: category.SortOrder, Enabled: category.Enabled,
		})
	}
	publicItems := make([]PublicEntry, 0, len(items))
	for _, item := range items {
		versions := make([]PublicVersion, 0, len(item.Versions))
		for _, version := range item.Versions {
			publishedAt := time.Time{}
			if version.PublishedAt != nil {
				publishedAt = *version.PublishedAt
			}
			versions = append(versions, PublicVersion{
				ID: version.ID, VersionLabel: version.VersionLabel,
				OriginalFileName: version.OriginalFileName, ContentType: version.ContentType,
				SizeBytes: version.SizeBytes, SHA256: version.SHA256, Status: version.Status,
				PublishedAt: publishedAt, DownloadURL: "/v1/downloads/files/" + version.ID,
			})
		}
		publicItems = append(publicItems, PublicEntry{
			ID: item.ID, CategoryID: item.CategoryID, CategorySlug: item.CategorySlug,
			Slug: item.Slug, Title: item.Title, Description: item.Description,
			SortOrder: item.SortOrder, Versions: versions, LatestVersionID: item.LatestVersionID,
		})
	}
	return Catalog{Categories: publicCategories, Items: publicItems, UpdatedAt: updatedAt}, nil
}

func (s *Service) DownloadURL(ctx context.Context, versionID string) (string, error) {
	if !s.Enabled() {
		return "", &ServiceError{Status: http.StatusServiceUnavailable, Code: "DOWNLOAD_STORAGE_DISABLED", Message: "Download storage is not configured."}
	}
	version, err := s.repository.GetPublishedVersion(ctx, strings.TrimSpace(versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound("Published download not found.")
	}
	if err != nil {
		return "", internal(err)
	}
	return s.storage.PublicURL(version.ObjectKey), nil
}

func (s *Service) VerifyPending(ctx context.Context, limit int) error {
	if !s.Enabled() {
		return nil
	}
	now := s.now().UTC()
	versions, err := s.repository.ClaimVerifying(ctx, limit, now, now.Add(time.Hour))
	if err != nil {
		return err
	}
	var verificationErrors []error
	for _, version := range versions {
		if err := s.verifyOne(ctx, version); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_ = s.repository.ReleaseVerificationLease(context.WithoutCancel(ctx), version.ID)
			verificationErrors = append(verificationErrors, fmt.Errorf("verify download version %s: %w", version.ID, err))
		}
	}
	return errors.Join(verificationErrors...)
}

func (s *Service) verifyOne(ctx context.Context, version Version) error {
	metadata, err := s.storage.Head(ctx, version.ObjectKey)
	if err != nil {
		return fmt.Errorf("head stored object: %w", err)
	}
	if metadata.SizeBytes != version.SizeBytes {
		return s.failVerification(ctx, version, "Stored object size could not be verified.")
	}
	body, err := s.storage.Open(ctx, version.ObjectKey)
	if err != nil {
		return fmt.Errorf("open stored object: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, body)
	closeErr := body.Close()
	if copyErr != nil {
		return fmt.Errorf("read stored object: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close stored object: %w", closeErr)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != version.SHA256 {
		_ = s.storage.Delete(context.WithoutCancel(ctx), version.ObjectKey)
		return s.failVerification(ctx, version, "Stored object SHA-256 does not match the declared checksum.")
	}
	now := s.now().UTC()
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := s.repository.MarkVerified(ctx, tx, version.ID, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_VERSION_VERIFIED", "download_version", version.ID,
			versionAudit(version), map[string]any{"status": VersionStatusDraft, "sha256": version.SHA256},
			"Automatic object integrity verification.", ActorMeta{AdminID: version.CreatedBy}, now)
	})
}

func (s *Service) failVerification(ctx context.Context, version Version, reason string) error {
	now := s.now().UTC()
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := s.repository.MarkVerificationFailed(ctx, tx, version.ID, reason, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, "DOWNLOAD_VERSION_VERIFICATION_FAILED", "download_version", version.ID,
			versionAudit(version), map[string]any{"status": VersionStatusFailed}, reason,
			ActorMeta{AdminID: version.CreatedBy}, now)
	})
	return err
}

func (s *Service) ExpireUploads(ctx context.Context, limit int) error {
	if !s.Enabled() {
		return nil
	}
	now := s.now().UTC()
	items, err := s.repository.ListExpiredUploads(ctx, now, limit)
	if err != nil {
		return err
	}
	for _, listed := range items {
		item, err := s.repository.GetUploadSession(ctx, listed.ID)
		if err != nil {
			continue
		}
		if item.Strategy == UploadStrategyMultipart {
			_ = s.storage.AbortMultipart(ctx, item.Version, item.ProviderUploadID)
		} else {
			_ = s.storage.Delete(ctx, item.Version.ObjectKey)
		}
		_ = s.withTx(ctx, func(tx pgx.Tx) error {
			if err := s.repository.MarkUploadAborted(ctx, tx, item.ID, UploadStatusExpired, "Upload session expired.", now); err != nil {
				return err
			}
			return s.audit(ctx, tx, "DOWNLOAD_UPLOAD_EXPIRED", "download_version", item.Version.ID,
				versionAudit(item.Version), map[string]any{"status": VersionStatusFailed}, "Upload session expired.",
				ActorMeta{AdminID: item.Version.CreatedBy}, now)
		})
	}
	return nil
}

func (s *Service) validateUploadInput(input UploadInput, meta ActorMeta) (UploadInput, string, error) {
	reason, err := validateReason(meta, input.Reason)
	if err != nil {
		return UploadInput{}, "", err
	}
	input.VersionLabel = strings.TrimSpace(input.VersionLabel)
	input.FileName = safeFileName(input.FileName)
	input.ContentType = strings.TrimSpace(input.ContentType)
	input.SHA256 = strings.ToLower(strings.TrimSpace(input.SHA256))
	if input.SizeBytes < 1 {
		return UploadInput{}, "", invalid("DOWNLOAD_FILE_EMPTY", "The selected file is empty.", nil)
	}
	if input.SizeBytes > s.config.MaxFileBytes {
		return UploadInput{}, "", invalid("DOWNLOAD_FILE_TOO_LARGE", "The selected file exceeds the configured size limit.", map[string]any{"max_file_bytes": s.config.MaxFileBytes})
	}
	if input.VersionLabel == "" || len(input.VersionLabel) > 64 || input.FileName == "" || len(input.FileName) > 255 ||
		input.ContentType == "" || len(input.ContentType) > 255 ||
		len(input.SHA256) != 64 {
		return UploadInput{}, "", invalid("INVALID_DOWNLOAD_UPLOAD", "Download version or file metadata is invalid.", nil)
	}
	if _, err := hex.DecodeString(input.SHA256); err != nil {
		return UploadInput{}, "", invalid("INVALID_DOWNLOAD_CHECKSUM", "SHA-256 must contain 64 lowercase hexadecimal characters.", nil)
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(input.FileName)), ".")
	allowed := false
	for _, candidate := range s.config.AllowedExtensions {
		if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(candidate)), ".") == extension {
			allowed = true
			break
		}
	}
	if !allowed {
		return UploadInput{}, "", invalid("DOWNLOAD_FILE_TYPE_NOT_ALLOWED", "This file extension is not allowed.", map[string]any{"extension": extension})
	}
	input.ContentType = contentTypeForExtension(extension)
	return input, reason, nil
}

func contentTypeForExtension(extension string) string {
	switch extension {
	case "exe":
		return "application/vnd.microsoft.portable-executable"
	case "msi":
		return "application/x-msi"
	case "zip":
		return "application/zip"
	case "7z":
		return "application/x-7z-compressed"
	case "pdf":
		return "application/pdf"
	case "md":
		return "text/markdown; charset=utf-8"
	case "txt":
		return "text/plain; charset=utf-8"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}

func validateUploadedParts(session UploadSession, parts []UploadedPart) error {
	expected := int((session.Version.SizeBytes + session.PartSizeBytes - 1) / session.PartSizeBytes)
	if len(parts) != expected {
		return invalid("DOWNLOAD_UPLOAD_INCOMPLETE", "Not every upload part has completed.", map[string]any{"expected_parts": expected, "uploaded_parts": len(parts)})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	var total int64
	for index, part := range parts {
		if part.PartNumber != int32(index+1) || strings.TrimSpace(part.ETag) == "" {
			return invalid("DOWNLOAD_UPLOAD_INCOMPLETE", "Upload parts are not contiguous.", nil)
		}
		expectedSize := session.PartSizeBytes
		if index == len(parts)-1 {
			expectedSize = session.Version.SizeBytes - int64(index)*session.PartSizeBytes
		}
		if part.SizeBytes != expectedSize {
			return invalid("DOWNLOAD_UPLOAD_INCOMPLETE", "An upload part has an unexpected size.", map[string]any{"part_number": part.PartNumber})
		}
		total += part.SizeBytes
	}
	if total != session.Version.SizeBytes {
		return invalid("DOWNLOAD_SIZE_MISMATCH", "Uploaded parts do not match the declared file size.", nil)
	}
	return nil
}

func validateCategoryInput(input CategoryInput, meta ActorMeta) (CategoryInput, string, error) {
	reason, err := validateReason(meta, input.Reason)
	if err != nil {
		return CategoryInput{}, "", err
	}
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	input.TitleEN, input.TitleZhCN = strings.TrimSpace(input.TitleEN), strings.TrimSpace(input.TitleZhCN)
	input.DescriptionEN, input.DescriptionZhCN = strings.TrimSpace(input.DescriptionEN), strings.TrimSpace(input.DescriptionZhCN)
	if !validSlug(input.Slug) || input.TitleEN == "" || input.TitleZhCN == "" || len(input.TitleEN) > 128 ||
		len(input.TitleZhCN) > 128 || len(input.DescriptionEN) > 2000 || len(input.DescriptionZhCN) > 2000 ||
		input.SortOrder < -100000 || input.SortOrder > 100000 {
		return CategoryInput{}, "", invalid("INVALID_DOWNLOAD_CATEGORY", "Download category metadata is invalid.", nil)
	}
	return input, reason, nil
}

func validateEntryInput(input EntryInput, meta ActorMeta) (EntryInput, string, error) {
	reason, err := validateReason(meta, input.Reason)
	if err != nil {
		return EntryInput{}, "", err
	}
	input.CategoryID, input.Slug = strings.TrimSpace(input.CategoryID), strings.ToLower(strings.TrimSpace(input.Slug))
	input.TitleEN, input.TitleZhCN = strings.TrimSpace(input.TitleEN), strings.TrimSpace(input.TitleZhCN)
	input.DescriptionEN, input.DescriptionZhCN = strings.TrimSpace(input.DescriptionEN), strings.TrimSpace(input.DescriptionZhCN)
	if input.CategoryID == "" || !validSlug(input.Slug) || input.TitleEN == "" || input.TitleZhCN == "" ||
		len(input.TitleEN) > 128 || len(input.TitleZhCN) > 128 || len(input.DescriptionEN) > 4000 ||
		len(input.DescriptionZhCN) > 4000 || input.SortOrder < -100000 || input.SortOrder > 100000 {
		return EntryInput{}, "", invalid("INVALID_DOWNLOAD_ITEM", "Download item metadata is invalid.", nil)
	}
	return input, reason, nil
}

func validateReason(meta ActorMeta, raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if strings.TrimSpace(meta.AdminID) == "" || reason == "" || len(reason) > 500 {
		return "", invalid("INVALID_OPERATION_REASON", "An authenticated administrator and a reason of at most 500 characters are required.", nil)
	}
	return reason, nil
}

func validSlug(value string) bool { return len(value) <= 64 && slugPattern.MatchString(value) }

func safeFileName(raw string) string {
	name := filepath.Base(strings.TrimSpace(raw))
	var builder strings.Builder
	for _, value := range name {
		if value > unicode.MaxASCII || !(unicode.IsLetter(value) || unicode.IsDigit(value) || strings.ContainsRune("._-() ", value)) {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(value)
	}
	return strings.Trim(builder.String(), ". ")
}

func objectKey(entry Entry, version Version) string {
	return "downloads/" + entry.Slug + "/" + version.ID + "/" + strings.ReplaceAll(version.OriginalFileName, " ", "_")
}

func newID(prefix string) string { return prefix + strings.ReplaceAll(uuid.NewString(), "-", "") }

func (s *Service) withTx(ctx context.Context, operation func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := operation(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, action, targetType, targetID string,
	oldValue, newValue map[string]any, reason string, meta ActorMeta, now time.Time) error {
	return s.repository.InsertAudit(ctx, tx, newID("ada_"), action, targetType, targetID,
		oldValue, newValue, reason, meta, now)
}

func duplicate(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

func mapTxError(err error, notFoundMessage string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(notFoundMessage)
	}
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return err
	}
	return internal(err)
}

func categoryAudit(item Category) map[string]any {
	return map[string]any{"slug": item.Slug, "title": item.Title, "sort_order": item.SortOrder, "enabled": item.Enabled, "status": item.Status}
}

func entryAudit(item Entry) map[string]any {
	return map[string]any{"category_id": item.CategoryID, "slug": item.Slug, "title": item.Title, "sort_order": item.SortOrder, "status": item.Status}
}

func versionAudit(item Version) map[string]any {
	return map[string]any{
		"entry_id": item.EntryID, "version_label": item.VersionLabel, "file_name": item.OriginalFileName,
		"content_type": item.ContentType, "size_bytes": item.SizeBytes, "sha256": item.SHA256, "status": item.Status,
	}
}
