package download

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestUploadValidationAndServerOwnedObjectKey(t *testing.T) {
	service := &Service{config: config.DownloadConfig{
		MaxFileBytes:      10,
		AllowedExtensions: []string{"zip", ".PDF"},
	}}
	meta := ActorMeta{AdminID: "adm_test"}
	validHash := strings.Repeat("a", 64)
	input, reason, err := service.validateUploadInput(UploadInput{
		VersionLabel: " build 2026-08-08 ", FileName: "../../Release Notes.PDF",
		ContentType: "application/pdf", SizeBytes: 10, SHA256: strings.ToUpper(validHash), Reason: " publish docs ",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if input.FileName != "Release Notes.PDF" || input.SHA256 != validHash || input.ContentType != "application/pdf" || reason != "publish docs" {
		t.Fatalf("normalized upload = %#v, reason %q", input, reason)
	}
	key := objectKey(Entry{Slug: "manual"}, Version{ID: "dver_server", OriginalFileName: input.FileName})
	if key != "downloads/manual/dver_server/Release_Notes.PDF" {
		t.Fatalf("object key = %q", key)
	}

	cases := []struct {
		name  string
		input UploadInput
		code  string
	}{
		{"empty", UploadInput{VersionLabel: "1", FileName: "a.zip", ContentType: "application/zip", SizeBytes: 0, SHA256: validHash, Reason: "test"}, "DOWNLOAD_FILE_EMPTY"},
		{"too large", UploadInput{VersionLabel: "1", FileName: "a.zip", ContentType: "application/zip", SizeBytes: 11, SHA256: validHash, Reason: "test"}, "DOWNLOAD_FILE_TOO_LARGE"},
		{"extension", UploadInput{VersionLabel: "1", FileName: "a.sh", ContentType: "text/plain", SizeBytes: 1, SHA256: validHash, Reason: "test"}, "DOWNLOAD_FILE_TYPE_NOT_ALLOWED"},
		{"checksum", UploadInput{VersionLabel: "1", FileName: "a.zip", ContentType: "application/zip", SizeBytes: 1, SHA256: strings.Repeat("g", 64), Reason: "test"}, "INVALID_DOWNLOAD_CHECKSUM"},
		{"reason", UploadInput{VersionLabel: "1", FileName: "a.zip", ContentType: "application/zip", SizeBytes: 1, SHA256: validHash}, "INVALID_OPERATION_REASON"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := service.validateUploadInput(test.input, meta)
			_, code, _, _ := ErrorDetails(err)
			if code != test.code {
				t.Fatalf("error code = %q, want %q (%v)", code, test.code, err)
			}
		})
	}
}

func TestMultipartPartValidation(t *testing.T) {
	session := UploadSession{PartSizeBytes: 5, Version: Version{SizeBytes: 12}}
	valid := []UploadedPart{{PartNumber: 3, ETag: "c", SizeBytes: 2}, {PartNumber: 1, ETag: "a", SizeBytes: 5}, {PartNumber: 2, ETag: "b", SizeBytes: 5}}
	if err := validateUploadedParts(session, valid); err != nil {
		t.Fatalf("valid parts rejected: %v", err)
	}
	for name, parts := range map[string][]UploadedPart{
		"missing":    valid[:2],
		"noncontig":  {{PartNumber: 1, ETag: "a", SizeBytes: 5}, {PartNumber: 3, ETag: "c", SizeBytes: 5}, {PartNumber: 4, ETag: "d", SizeBytes: 2}},
		"wrong size": {{PartNumber: 1, ETag: "a", SizeBytes: 4}, {PartNumber: 2, ETag: "b", SizeBytes: 5}, {PartNumber: 3, ETag: "c", SizeBytes: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateUploadedParts(session, parts); err == nil {
				t.Fatal("invalid parts accepted")
			}
		})
	}
}

func TestDownloadAuditSnapshotIsSecretFree(t *testing.T) {
	version := Version{
		ID: "dver_test", EntryID: "dent_test", VersionLabel: "1", OriginalFileName: "file.zip",
		ContentType: "application/zip", SizeBytes: 10, SHA256: strings.Repeat("a", 64),
		ObjectKey: "downloads/private/provider-key", Status: VersionStatusDraft,
	}
	encoded, err := json.Marshal(versionAudit(version))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"object_key", "provider-key", "presign", "credential", "https://"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("audit snapshot leaked %q: %s", forbidden, text)
		}
	}
}

func TestS3ObjectMetadataIsImmutableAttachment(t *testing.T) {
	storage := &S3Storage{publicBase: "https://cdn.example/base"}
	disposition, cache, metadata := storage.objectInput(Version{ID: "dver_1", OriginalFileName: "Manual.pdf", SHA256: strings.Repeat("b", 64)})
	if disposition != `attachment; filename="Manual.pdf"` || cache != "public, max-age=31536000, immutable" {
		t.Fatalf("metadata = %q, %q", disposition, cache)
	}
	if metadata["download-version-id"] != "dver_1" || storage.PublicURL("downloads/a.pdf") != "https://cdn.example/base/downloads/a.pdf" {
		t.Fatalf("metadata/public URL = %#v / %q", metadata, storage.PublicURL("downloads/a.pdf"))
	}
}

func TestCapabilitiesExposeLimitsWithoutCredentials(t *testing.T) {
	service := &Service{config: config.DownloadConfig{
		Enabled: true, MaxFileBytes: 100, AllowedExtensions: []string{"zip"},
		MultipartThresholdBytes: 80, PartSizeBytes: 20, UploadSessionTTLHours: 24, PresignTTLMinutes: 15,
	}, storage: newMemoryStorage()}
	capabilities := service.Capabilities()
	if !capabilities.Enabled || capabilities.MaxFileBytes != 100 || len(capabilities.AllowedExtensions) != 1 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	capabilities.AllowedExtensions[0] = "exe"
	if service.config.AllowedExtensions[0] != "zip" {
		t.Fatal("capabilities exposed mutable service configuration")
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_key", "secret", "endpoint", "bucket", "public_base_url"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("capabilities leaked %q: %s", forbidden, encoded)
		}
	}
}
