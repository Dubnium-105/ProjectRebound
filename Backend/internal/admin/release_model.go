package admin

import (
	"time"

	clientupdate "github.com/projectrebound/matchserver/internal/update"
)

const (
	ReleaseStatusDraft      = "DRAFT"
	ReleaseStatusReady      = "READY"
	ReleaseStatusPublished  = "PUBLISHED"
	ReleaseStatusRolledBack = "ROLLED_BACK"
	ReleaseStatusArchived   = "ARCHIVED"
)

type ReleaseValidation struct {
	Valid  bool                     `json:"valid"`
	Checks []ReleaseValidationCheck `json:"checks"`
}

type ReleaseValidationCheck struct {
	Key     string `json:"key"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

type Release struct {
	ID                      string
	Product                 string
	Platform                string
	Architecture            string
	Channel                 string
	Version                 string
	MinimumSupportedVersion string
	ForceUpdate             bool
	Status                  string
	Source                  clientupdate.SourceRelease
	Manifest                *clientupdate.Manifest
	Validation              ReleaseValidation
	CreatedBy               string
	PublishedBy             string
	RolledBackBy            string
	ArchivedBy              string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	PublishedAt             *time.Time
	RolledBackAt            *time.Time
	ArchivedAt              *time.Time
}

type ReleaseCreateInput struct {
	Platform                string
	Architecture            string
	Channel                 string
	Version                 string
	MinimumSupportedVersion string
	ForceUpdate             bool
	Files                   []clientupdate.SourceFile
	Reason                  string
}

type ReleaseListFilter struct {
	Cursor       string
	Status       string
	Platform     string
	Architecture string
	Channel      string
	Limit        int
}

type ReleaseListResult struct {
	Items      []Release
	NextCursor string
}
