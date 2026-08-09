package download

import (
	"io"
	"time"
)

const (
	CategoryStatusActive   = "ACTIVE"
	CategoryStatusArchived = "ARCHIVED"
	EntryStatusActive      = "ACTIVE"
	EntryStatusArchived    = "ARCHIVED"

	VersionStatusUploading = "UPLOADING"
	VersionStatusVerifying = "VERIFYING"
	VersionStatusDraft     = "DRAFT"
	VersionStatusPublished = "PUBLISHED"
	VersionStatusArchived  = "ARCHIVED"
	VersionStatusFailed    = "FAILED"

	UploadStrategySingle    = "SINGLE"
	UploadStrategyMultipart = "MULTIPART"
	UploadStatusActive      = "ACTIVE"
	UploadStatusCompleted   = "COMPLETED"
	UploadStatusAborted     = "ABORTED"
	UploadStatusExpired     = "EXPIRED"
)

type LocalizedText struct {
	EN   string `json:"en"`
	ZhCN string `json:"zh_cn"`
}

type Category struct {
	ID          string        `json:"id"`
	Slug        string        `json:"slug"`
	Title       LocalizedText `json:"title"`
	Description LocalizedText `json:"description"`
	SortOrder   int           `json:"sort_order"`
	Enabled     bool          `json:"enabled"`
	Status      string        `json:"status"`
	CreatedBy   string        `json:"created_by"`
	ArchivedBy  string        `json:"archived_by"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	ArchivedAt  *time.Time    `json:"archived_at"`
}

type Entry struct {
	ID              string        `json:"id"`
	CategoryID      string        `json:"category_id"`
	CategorySlug    string        `json:"category_slug"`
	Slug            string        `json:"slug"`
	Title           LocalizedText `json:"title"`
	Description     LocalizedText `json:"description"`
	SortOrder       int           `json:"sort_order"`
	Status          string        `json:"status"`
	CreatedBy       string        `json:"created_by"`
	ArchivedBy      string        `json:"archived_by"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	ArchivedAt      *time.Time    `json:"archived_at"`
	Versions        []Version     `json:"versions"`
	LatestVersionID string        `json:"latest_version_id"`
}

type Version struct {
	ID               string     `json:"id"`
	EntryID          string     `json:"entry_id"`
	VersionLabel     string     `json:"version_label"`
	OriginalFileName string     `json:"original_file_name"`
	ContentType      string     `json:"content_type"`
	SizeBytes        int64      `json:"size_bytes"`
	SHA256           string     `json:"sha256"`
	ObjectKey        string     `json:"-"`
	Status           string     `json:"status"`
	FailureReason    string     `json:"failure_reason,omitempty"`
	CreatedBy        string     `json:"created_by"`
	PublishedBy      string     `json:"published_by"`
	ArchivedBy       string     `json:"archived_by"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	VerifiedAt       *time.Time `json:"verified_at"`
	PublishedAt      *time.Time `json:"published_at"`
	ArchivedAt       *time.Time `json:"archived_at"`
	DownloadURL      string     `json:"download_url,omitempty"`
}

type UploadSession struct {
	ID               string         `json:"id"`
	VersionID        string         `json:"version_id"`
	Strategy         string         `json:"strategy"`
	ProviderUploadID string         `json:"-"`
	PartSizeBytes    int64          `json:"part_size_bytes"`
	Status           string         `json:"status"`
	ExpiresAt        time.Time      `json:"expires_at"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	Version          Version        `json:"version"`
	UploadedParts    []UploadedPart `json:"uploaded_parts"`
}

type UploadedPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
}

type SignedRequest struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type SignedPart struct {
	PartNumber int32         `json:"part_number"`
	Request    SignedRequest `json:"request"`
}

type UploadCreated struct {
	Session UploadSession  `json:"session"`
	Request *SignedRequest `json:"request,omitempty"`
}

type Catalog struct {
	Categories []PublicCategory `json:"categories"`
	Items      []PublicEntry    `json:"items"`
	UpdatedAt  time.Time        `json:"-"`
}

// Public catalog types intentionally omit administrator IDs, internal object
// keys, upload timestamps, and failure details.
type PublicCategory struct {
	ID          string        `json:"id"`
	Slug        string        `json:"slug"`
	Title       LocalizedText `json:"title"`
	Description LocalizedText `json:"description"`
	SortOrder   int           `json:"sort_order"`
	Enabled     bool          `json:"enabled"`
}

type PublicEntry struct {
	ID              string          `json:"id"`
	CategoryID      string          `json:"category_id"`
	CategorySlug    string          `json:"category_slug"`
	Slug            string          `json:"slug"`
	Title           LocalizedText   `json:"title"`
	Description     LocalizedText   `json:"description"`
	SortOrder       int             `json:"sort_order"`
	Versions        []PublicVersion `json:"versions"`
	LatestVersionID string          `json:"latest_version_id"`
}

type PublicVersion struct {
	ID               string    `json:"id"`
	VersionLabel     string    `json:"version_label"`
	OriginalFileName string    `json:"original_file_name"`
	ContentType      string    `json:"content_type"`
	SizeBytes        int64     `json:"size_bytes"`
	SHA256           string    `json:"sha256"`
	Status           string    `json:"status"`
	PublishedAt      time.Time `json:"published_at"`
	DownloadURL      string    `json:"download_url"`
}

type CategoryInput struct {
	Slug            string
	TitleEN         string
	TitleZhCN       string
	DescriptionEN   string
	DescriptionZhCN string
	SortOrder       int
	Enabled         bool
	Reason          string
}

type EntryInput struct {
	CategoryID      string
	Slug            string
	TitleEN         string
	TitleZhCN       string
	DescriptionEN   string
	DescriptionZhCN string
	SortOrder       int
	Reason          string
}

type UploadInput struct {
	VersionLabel string
	FileName     string
	ContentType  string
	SizeBytes    int64
	SHA256       string
	Reason       string
}

type ActorMeta struct {
	AdminID   string
	RequestID string
	IPAddress string
	UserAgent string
}

type ObjectMetadata struct {
	SizeBytes   int64
	ContentType string
}

// ReleaseFile is the minimal verified object-storage metadata exposed to
// administrators who can create managed client releases.
type ReleaseFile struct {
	ID               string    `json:"id"`
	VersionLabel     string    `json:"version_label"`
	OriginalFileName string    `json:"original_file_name"`
	SizeBytes        int64     `json:"size_bytes"`
	SHA256           string    `json:"sha256"`
	ObjectKey        string    `json:"object_key"`
	Status           string    `json:"status"`
	VerifiedAt       time.Time `json:"verified_at"`
}

type Capabilities struct {
	Enabled                    bool     `json:"enabled"`
	MaxFileBytes               int64    `json:"max_file_bytes"`
	AllowedExtensions          []string `json:"allowed_extensions"`
	MultipartThresholdBytes    int64    `json:"multipart_threshold_bytes"`
	PartSizeBytes              int64    `json:"part_size_bytes"`
	UploadSessionTTLHours      int      `json:"upload_session_ttl_hours"`
	PresignedRequestTTLMinutes int      `json:"presigned_request_ttl_minutes"`
}

type ObjectReader struct {
	Body io.ReadCloser
}
