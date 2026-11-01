// Package form owns the Form domain: management, submissions, the
// editor-collab document boundary and the AI document boundary.
package form

import (
	"errors"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Status is the stored lifecycle state of a Form. The values are the
// strings the form table holds.
type Status string

const (
	StatusDraft     Status = "FORM_STATUS_DRAFT"
	StatusPublished Status = "FORM_STATUS_PUBLISHED"
)

// Known reports whether this build knows the stored status.
func (s Status) Known() bool {
	return s == StatusDraft || s == StatusPublished
}

// Form is one form row. AllowedRoles keeps nil and empty as the row holds
// them.
type Form struct {
	ID                       string
	ContentDocumentID        string
	SourceLocale             string
	Slug                     *string
	Status                   Status
	IsPublic                 bool
	RequireAuth              *bool
	AllowedRoles             []string
	AllowDuplicateSubmission *bool
	AccessPassword           *string
	MaxSubmissions           *int32
	OpensAt                  *time.Time
	ClosesAt                 *time.Time
	FeaturedImageFileID      *string
	OgAssetID                *string
	CreatedAt                time.Time
	UpdatedAt                *time.Time
}

// HasPassword reports whether the Form asks for an access password.
func (f Form) HasPassword() bool {
	return f.AccessPassword != nil && *f.AccessPassword != ""
}

// Locator names the form column a lookup matches.
type Locator uint8

const (
	ByID Locator = iota + 1
	BySlug
)

// LocatorOf is the lookup of a value that is either a Form ID or a slug.
func LocatorOf(idOrSlug string) Locator {
	if IsValidUUID(idOrSlug) {
		return ByID
	}
	return BySlug
}

// RowLock is the row lock a Store read of the form row takes.
type RowLock uint8

const (
	// Unlocked takes no lock.
	Unlocked RowLock = iota
	// KeyShareLock keeps the row from being deleted or re-keyed.
	KeyShareLock
	// UpdateLock serializes with a concurrent writer.
	UpdateLock
)

// DocumentRoot is the Form's content document reference and source locale,
// as the document paths lock them.
type DocumentRoot struct {
	Status       Status
	SourceLocale string
	DocumentID   string
}

// Locale is one stored locale document as the document paths read it:
// Revision is the row version a target revision derives from.
type Locale struct {
	Revision    string
	Title       *string
	Schema      []byte
	ContentText *string
	UpdatedAt   time.Time
}

// LocaleChange is one locale document write. Only the parts it sets are
// written, with UpdatedAt.
type LocaleChange struct {
	SetTitle  bool
	Title     *string
	SetSchema bool
	Schema    []byte
	// ContentText is the plain text of Schema, written with it.
	ContentText *string
	UpdatedAt   time.Time
}

// LocalizationRow is one stored locale as a public read selects it.
type LocalizationRow struct {
	Locale      string
	Title       *string
	ContentJSON []byte
	ContentText *string
	OgAssetID   *string
}

// Setting names one Form setting an update writes.
type Setting uint8

const (
	SettingSlug Setting = iota + 1
	SettingIsPublic
	SettingRequireAuth
	SettingAllowDuplicateSubmission
	SettingAccessPassword
	SettingMaxSubmissions
	SettingOpensAt
	SettingClosesAt
	SettingStatus
	SettingAllowedRoles
)

// ListForms selects a page of Forms.
type ListForms struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// Submission is one form_submission row.
type Submission struct {
	ID          string
	FormID      string
	MemberID    *string
	Data        []byte
	IPAddress   *string
	CountryCode *string
	UserAgent   *string
	CreatedAt   time.Time
}

// SubmissionList selects a page of a Form's submissions. Search matches the
// submitted data, CountryCode is upper case, the creation bounds are
// inclusive from and exclusive before. Order sorts by creation time, then
// newest ID first; an empty Order is newest first.
type SubmissionList struct {
	FormID          string
	Search          string
	CountryCode     string
	CreatedAtFrom   *time.Time
	CreatedAtBefore *time.Time
	Order           []SubmissionOrder
	Limit           int32
	Offset          int32
}

// SubmissionOrder sorts submissions by creation time.
type SubmissionOrder struct {
	Descending bool
}

// SubmissionData is the creation time and answers of one submission, as
// the statistics read them.
type SubmissionData struct {
	CreatedAt time.Time
	Data      []byte
}

// AssetDisposition is the content disposition of a public asset, numbered as
// the API's AssetDisposition.
type AssetDisposition int32

// Asset is a ready public asset as the media runtime projects it.
type Asset struct {
	ID               string
	URL              string
	Extension        string
	MIMEType         string
	FileSize         int64
	SHA256           []byte
	Disposition      AssetDisposition
	DownloadFilename *string
}

// LocalizedOGDisposition is the Form-owned public-read view of a localized OG generation.
type LocalizedOGDisposition uint8

const (
	LocalizedOGUnavailable LocalizedOGDisposition = iota
	LocalizedOGPending
	LocalizedOGReady
)

// ContentUpdate is the content.updated signal of a changed locale document
// an AI document edit schedules.
type ContentUpdate struct {
	FormID string
	Locale string
	// Fields names what changed: "title", "document.content".
	Fields           []string
	DocumentRevision string
	Contributor      string
	// SourceLocale is set for a change of the source locale, which
	// advances the shared document revision.
	SourceLocale  bool
	LocaleDeleted bool
	// TargetRevision is the new revision of a changed target locale.
	TargetRevision *string
}

// TranslationJob is the translation job a provider delivery applies to a
// Form.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	TargetLocale        string
	RequestedByMemberID string
}

// Store errors the use cases branch on.
var (
	ErrFormNotFound       = errors.New("form not found")
	ErrLocaleNotFound     = errors.New("form locale not found")
	ErrSubmissionNotFound = errors.New("form submission not found")
	ErrFileNotFound       = errors.New("file not found")
	// ErrContentDocumentNotFound reports a Form whose content document is
	// gone.
	ErrContentDocumentNotFound = errors.New("form content document not found")
)

// TranslationDocumentState is one stored locale document with its OG image.
type TranslationDocumentState struct {
	Title       *string
	ContentJSON []byte
	ContentText *string
	OgAssetID   *string
}
