package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

type memoryStorage struct {
	objects      map[string][]byte
	parts        map[string][]UploadedPart
	aborted      map[string]bool
	headFailures map[string]int
	completeErr  error
	multipartSeq int
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{objects: map[string][]byte{}, parts: map[string][]UploadedPart{}, aborted: map[string]bool{}, headFailures: map[string]int{}}
}

func (s *memoryStorage) PresignPut(context.Context, Version, time.Duration) (SignedRequest, error) {
	return SignedRequest{URL: "https://storage.invalid/signed-single", Method: "PUT"}, nil
}
func (s *memoryStorage) CreateMultipart(context.Context, Version) (string, error) {
	s.multipartSeq++
	return fmt.Sprintf("provider-upload-%d", s.multipartSeq), nil
}
func (s *memoryStorage) PresignPart(_ context.Context, _ Version, _ string, part int32, _ time.Duration) (SignedRequest, error) {
	return SignedRequest{URL: fmt.Sprintf("https://storage.invalid/signed-part-%d", part), Method: "PUT"}, nil
}
func (s *memoryStorage) ListParts(_ context.Context, version Version, _ string) ([]UploadedPart, error) {
	return append([]UploadedPart(nil), s.parts[version.ObjectKey]...), nil
}
func (s *memoryStorage) CompleteMultipart(context.Context, Version, string, []UploadedPart) error {
	return s.completeErr
}
func (s *memoryStorage) AbortMultipart(_ context.Context, version Version, _ string) error {
	s.aborted[version.ObjectKey] = true
	return nil
}
func (s *memoryStorage) Head(_ context.Context, key string) (ObjectMetadata, error) {
	if s.headFailures[key] > 0 {
		s.headFailures[key]--
		return ObjectMetadata{}, errors.New("temporary storage outage")
	}
	value, ok := s.objects[key]
	if !ok {
		return ObjectMetadata{}, errors.New("object not found")
	}
	return ObjectMetadata{SizeBytes: int64(len(value)), ContentType: "application/zip"}, nil
}
func (s *memoryStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	value, ok := s.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
func (s *memoryStorage) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}
func (s *memoryStorage) PublicURL(key string) string { return "https://cdn.invalid/" + key }
func (s *memoryStorage) PublicProbeURL(key string) string {
	return "http://minio/downloads/" + key
}

func TestDownloadLifecycleAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	assertDefaultDownloadPermissions(t, ctx, pool)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	adminID := "adm_download_" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users
		(id, username, display_name, password_hash, status, mfa_required, created_at, updated_at)
		VALUES ($1,$2,'Download integration','test-only','ACTIVE',TRUE,NOW(),NOW())`, adminID, "download-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM download_upload_sessions WHERE version_id IN (SELECT id FROM download_versions WHERE created_by=$1)`, adminID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM download_versions WHERE created_by=$1`, adminID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM download_entries WHERE created_by=$1`, adminID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM download_categories WHERE created_by=$1`, adminID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_audit_logs WHERE admin_id=$1`, adminID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id=$1`, adminID)
	})

	storage := newMemoryStorage()
	cfg := config.Defaults.Downloads
	cfg.Enabled = true
	cfg.AllowedExtensions = []string{"zip"}
	cfg.MaxFileBytes = 1024
	cfg.MultipartThresholdBytes = 8
	cfg.PartSizeBytes = 5
	repository := NewRepository(pool)
	service := NewService(pool, repository, storage, cfg)
	probedURL := ""
	service.probe = func(_ context.Context, target string) error {
		probedURL = target
		return nil
	}
	meta := ActorMeta{AdminID: adminID, RequestID: "req-download-integration", IPAddress: "192.0.2.10", UserAgent: "download-test"}

	category, err := service.CreateCategory(ctx, CategoryInput{
		Slug: "fixtures-" + suffix, TitleEN: "Fixtures", TitleZhCN: "测试夹具", Enabled: true, Reason: "integration category",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCategory(ctx, CategoryInput{
		Slug: category.Slug, TitleEN: "Duplicate", TitleZhCN: "重复", Enabled: true, Reason: "test uniqueness",
	}, meta); errorCode(err) != "DOWNLOAD_CATEGORY_EXISTS" {
		t.Fatalf("duplicate category error = %v", err)
	}
	if _, err := service.UpdateCategory(ctx, category.ID, CategoryInput{
		Slug: category.Slug + "-changed", TitleEN: category.Title.EN, TitleZhCN: category.Title.ZhCN, Enabled: true, Reason: "test stable slug",
	}, meta); errorCode(err) != "DOWNLOAD_CATEGORY_SLUG_IMMUTABLE" {
		t.Fatalf("mutable category slug error = %v", err)
	}
	entry, err := service.CreateEntry(ctx, EntryInput{
		CategoryID: category.ID, Slug: "fixture-" + suffix,
		TitleEN: "Fixture", TitleZhCN: "夹具", DescriptionEN: "Download fixture", DescriptionZhCN: "下载测试夹具",
		Reason: "integration item",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateEntry(ctx, EntryInput{
		CategoryID: category.ID, Slug: entry.Slug, TitleEN: "Duplicate", TitleZhCN: "重复", Reason: "test uniqueness",
	}, meta); errorCode(err) != "DOWNLOAD_ITEM_EXISTS" {
		t.Fatalf("duplicate entry error = %v", err)
	}
	if _, err := service.UpdateEntry(ctx, entry.ID, EntryInput{
		CategoryID: category.ID, Slug: entry.Slug + "-changed", TitleEN: entry.Title.EN, TitleZhCN: entry.Title.ZhCN, Reason: "test stable slug",
	}, meta); errorCode(err) != "DOWNLOAD_ITEM_SLUG_IMMUTABLE" {
		t.Fatalf("mutable entry slug error = %v", err)
	}

	smallContent := []byte("good")
	small, err := service.CreateUpload(ctx, entry.ID, UploadInput{
		VersionLabel: "small", FileName: "small.zip", ContentType: "application/zip",
		SizeBytes: int64(len(smallContent)), SHA256: checksum(smallContent), Reason: "small upload",
	}, meta)
	if err != nil || small.Session.Strategy != UploadStrategySingle || small.Request == nil {
		t.Fatalf("small upload = %#v, %v", small, err)
	}
	storage.objects[small.Session.Version.ObjectKey] = smallContent
	if _, err := service.CompleteUpload(ctx, small.Session.ID, "complete small upload", meta); err != nil {
		t.Fatal(err)
	}
	storage.headFailures[small.Session.Version.ObjectKey] = 1
	if err := service.VerifyPending(ctx, 10); err == nil {
		t.Fatal("transient storage failure was not reported")
	}
	retrying, err := repository.GetVersion(ctx, small.Session.Version.ID)
	if err != nil || retrying.Status != VersionStatusVerifying {
		t.Fatalf("transient verification changed state = %#v, %v", retrying, err)
	}
	if err := service.VerifyPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	verified, err := repository.GetVersion(ctx, small.Session.Version.ID)
	if err != nil || verified.Status != VersionStatusDraft || verified.VerifiedAt == nil {
		t.Fatalf("verified small version = %#v, %v", verified, err)
	}
	if _, err := service.CreateUpload(ctx, entry.ID, UploadInput{
		VersionLabel: "SMALL", FileName: "replacement.zip", ContentType: "application/zip",
		SizeBytes: 1, SHA256: checksum([]byte("x")), Reason: "immutable label",
	}, meta); errorCode(err) != "DOWNLOAD_VERSION_EXISTS" {
		t.Fatalf("duplicate version label error = %v", err)
	}
	published, err := service.Publish(ctx, verified.ID, "publish verified fixture", meta)
	if err != nil || published.Status != VersionStatusPublished {
		t.Fatalf("publish = %#v, %v", published, err)
	}
	if probedURL != storage.PublicProbeURL(verified.ObjectKey) {
		t.Fatalf("public availability probe = %q", probedURL)
	}
	catalog, err := service.Catalog(ctx)
	if err != nil || len(catalog.Items) != 1 || len(catalog.Items[0].Versions) != 1 || catalog.Items[0].LatestVersionID != verified.ID {
		t.Fatalf("public catalog = %#v, %v", catalog, err)
	}
	if catalog.Items[0].Versions[0].DownloadURL != "/v1/downloads/files/"+verified.ID {
		t.Fatalf("stable download URL = %q", catalog.Items[0].Versions[0].DownloadURL)
	}
	if target, err := service.DownloadURL(ctx, verified.ID); err != nil || target != "https://cdn.invalid/"+verified.ObjectKey {
		t.Fatalf("download redirect = %q, %v", target, err)
	}
	category, err = service.UpdateCategory(ctx, category.ID, CategoryInput{
		Slug: category.Slug, TitleEN: category.Title.EN, TitleZhCN: category.Title.ZhCN,
		DescriptionEN: category.Description.EN, DescriptionZhCN: category.Description.ZhCN,
		SortOrder: category.SortOrder, Enabled: false, Reason: "temporarily disable category",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err = service.Catalog(ctx)
	if err != nil || len(catalog.Categories) != 0 || len(catalog.Items) != 0 {
		t.Fatalf("disabled category catalog = %#v, %v", catalog, err)
	}
	if _, err := service.DownloadURL(ctx, verified.ID); errorCode(err) != "DOWNLOAD_NOT_FOUND" {
		t.Fatalf("disabled category redirect error = %v", err)
	}
	category, err = service.UpdateCategory(ctx, category.ID, CategoryInput{
		Slug: category.Slug, TitleEN: category.Title.EN, TitleZhCN: category.Title.ZhCN,
		DescriptionEN: category.Description.EN, DescriptionZhCN: category.Description.ZhCN,
		SortOrder: category.SortOrder, Enabled: true, Reason: "restore category",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveVersion(ctx, verified.ID, "archive fixture", meta); err != nil {
		t.Fatal(err)
	}
	catalog, err = service.Catalog(ctx)
	if err != nil || len(catalog.Items) != 0 {
		t.Fatalf("catalog after archive = %#v, %v", catalog, err)
	}
	if _, err := service.DownloadURL(ctx, verified.ID); errorCode(err) != "DOWNLOAD_NOT_FOUND" {
		t.Fatalf("archived redirect error = %v", err)
	}
	if _, ok := storage.objects[verified.ObjectKey]; !ok {
		t.Fatal("archiving deleted the immutable object")
	}

	multipartContent := []byte("hello world!")
	multipart, err := service.CreateUpload(ctx, entry.ID, UploadInput{
		VersionLabel: "multipart", FileName: "multipart.zip", ContentType: "application/zip",
		SizeBytes: int64(len(multipartContent)), SHA256: checksum(multipartContent), Reason: "multipart upload",
	}, meta)
	if err != nil || multipart.Session.Strategy != UploadStrategyMultipart {
		t.Fatalf("multipart upload = %#v, %v", multipart, err)
	}
	storage.parts[multipart.Session.Version.ObjectKey] = []UploadedPart{{PartNumber: 1, ETag: "one", SizeBytes: 5}, {PartNumber: 2, ETag: "two", SizeBytes: 5}}
	resumed, err := service.GetUpload(ctx, multipart.Session.ID)
	if err != nil || len(resumed.UploadedParts) != 2 {
		t.Fatalf("resumed upload = %#v, %v", resumed, err)
	}
	signed, err := service.SignParts(ctx, multipart.Session.ID, []int32{3})
	if err != nil || len(signed) != 1 || !strings.Contains(signed[0].Request.URL, "part-3") {
		t.Fatalf("signed missing part = %#v, %v", signed, err)
	}
	if _, err := service.CompleteUpload(ctx, multipart.Session.ID, "incomplete attempt", meta); errorCode(err) != "DOWNLOAD_UPLOAD_INCOMPLETE" {
		t.Fatalf("incomplete upload error = %v", err)
	}
	storage.parts[multipart.Session.Version.ObjectKey] = append(storage.parts[multipart.Session.Version.ObjectKey], UploadedPart{PartNumber: 3, ETag: "three", SizeBytes: 2})
	storage.objects[multipart.Session.Version.ObjectKey] = multipartContent
	storage.completeErr = errors.New("provider completed but response was lost")
	if _, err := service.CompleteUpload(ctx, multipart.Session.ID, "complete resumed upload", meta); err != nil {
		t.Fatalf("idempotent complete recovery: %v", err)
	}
	storage.completeErr = nil
	if err := service.VerifyPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	multipartVersion, err := repository.GetVersion(ctx, multipart.Session.Version.ID)
	if err != nil || multipartVersion.Status != VersionStatusDraft {
		t.Fatalf("multipart verification = %#v, %v", multipartVersion, err)
	}
	if _, err := service.Publish(ctx, multipartVersion.ID, "publish multipart history", meta); err != nil {
		t.Fatal(err)
	}
	latestContent := []byte("new!")
	latest, err := service.CreateUpload(ctx, entry.ID, UploadInput{
		VersionLabel: "latest", FileName: "latest.zip", ContentType: "application/zip",
		SizeBytes: int64(len(latestContent)), SHA256: checksum(latestContent), Reason: "latest fixture",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	storage.objects[latest.Session.Version.ObjectKey] = latestContent
	if _, err := service.CompleteUpload(ctx, latest.Session.ID, "complete latest fixture", meta); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, latest.Session.Version.ID, "publish latest fixture", meta); err != nil {
		t.Fatal(err)
	}
	history, err := service.Catalog(ctx)
	if err != nil || len(history.Items) != 1 || len(history.Items[0].Versions) != 2 ||
		history.Items[0].LatestVersionID != latest.Session.Version.ID ||
		history.Items[0].Versions[0].ID != latest.Session.Version.ID || history.Items[0].Versions[1].ID != multipartVersion.ID {
		t.Fatalf("published version history = %#v, %v", history, err)
	}

	bad, err := service.CreateUpload(ctx, entry.ID, UploadInput{
		VersionLabel: "bad-hash", FileName: "bad.zip", ContentType: "application/zip",
		SizeBytes: 4, SHA256: checksum([]byte("good")), Reason: "hash failure fixture",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	storage.objects[bad.Session.Version.ObjectKey] = []byte("evil")
	if _, err := service.CompleteUpload(ctx, bad.Session.ID, "complete bad fixture", meta); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	failed, err := repository.GetVersion(ctx, bad.Session.Version.ID)
	if err != nil || failed.Status != VersionStatusFailed || !strings.Contains(failed.FailureReason, "SHA-256") {
		t.Fatalf("failed verification = %#v, %v", failed, err)
	}
	if _, ok := storage.objects[failed.ObjectKey]; ok {
		t.Fatal("checksum-failed object was not removed")
	}

	expired, err := service.CreateUpload(ctx, entry.ID, UploadInput{
		VersionLabel: "expired", FileName: "expired.zip", ContentType: "application/zip",
		SizeBytes: 12, SHA256: checksum(multipartContent), Reason: "expiry fixture",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_upload_sessions SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, expired.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ExpireUploads(ctx, 10); err != nil {
		t.Fatal(err)
	}
	expiredSession, err := repository.GetUploadSession(ctx, expired.Session.ID)
	if err != nil || expiredSession.Status != UploadStatusExpired || expiredSession.Version.Status != VersionStatusFailed || !storage.aborted[expired.Session.Version.ObjectKey] {
		t.Fatalf("expired upload = %#v, aborted=%v, err=%v", expiredSession, storage.aborted, err)
	}

	archivedEntry, err := service.CreateEntry(ctx, EntryInput{
		CategoryID: category.ID, Slug: "archived-fixture-" + suffix,
		TitleEN: "Archived fixture", TitleZhCN: "归档夹具", Reason: "archive state fixture",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	archivedUpload, err := service.CreateUpload(ctx, archivedEntry.ID, UploadInput{
		VersionLabel: "draft", FileName: "draft.zip", ContentType: "application/zip",
		SizeBytes: int64(len(smallContent)), SHA256: checksum(smallContent), Reason: "archived item draft",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	storage.objects[archivedUpload.Session.Version.ObjectKey] = smallContent
	if _, err := service.CompleteUpload(ctx, archivedUpload.Session.ID, "complete archived item draft", meta); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveEntry(ctx, archivedEntry.ID, "archive item with draft", meta); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, archivedUpload.Session.Version.ID, "must not publish archived item", meta); errorCode(err) != "INVALID_DOWNLOAD_ITEM_STATE" {
		t.Fatalf("archived item publish error = %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE download_versions SET status='INVALID' WHERE id=$1`, multipartVersion.ID); err == nil {
		t.Fatal("database accepted an invalid download version state")
	}
	var auditText string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(string_agg(old_value::text || new_value::text, ''), '') FROM admin_audit_logs WHERE admin_id=$1`, adminID).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"storage.invalid", "cdn.invalid", "provider-upload", "signed-part", "secret_access_key"} {
		if strings.Contains(strings.ToLower(auditText), secret) {
			t.Fatalf("audit log leaked %q: %s", secret, auditText)
		}
	}
}

func assertDefaultDownloadPermissions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for role, expected := range map[string]int{"SUPER_ADMIN": 5, "RELEASE_MANAGER": 5, "VIEWER": 1, "OPERATIONS": 0} {
		var count int
		err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_role_permissions grant_row
			JOIN admin_roles role ON role.id=grant_row.role_id
			JOIN admin_permissions permission ON permission.id=grant_row.permission_id
			WHERE role.name=$1 AND permission.resource='downloads'`, role).Scan(&count)
		if err != nil || count != expected {
			t.Fatalf("%s download permissions = %d, want %d (err=%v)", role, count, expected, err)
		}
	}
}

func checksum(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := ErrorDetails(err)
	return code
}
