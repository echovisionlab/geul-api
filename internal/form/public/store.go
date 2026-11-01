package public

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Store is what the public Form use cases read and write, bound to one
// transaction or read snapshot. formpublicpostgres implements it; a missing
// Form is form.ErrFormNotFound.
type Store interface {
	// SourceReader reads the Form's source locale document.
	form.SourceReader
	// FindForm reads the Form whose locator column equals value.
	FindForm(ctx context.Context, locator form.Locator, value string) (form.Form, error)
	// LockForm locks the Form row for update and reads it.
	LockForm(ctx context.Context, formID string) (form.Form, error)
	// LocalizationRows reads the Form's locales of locales, every locale
	// when it is empty.
	LocalizationRows(ctx context.Context, formID string, locales []string) ([]form.LocalizationRow, error)
	// RuntimeSettings reads the localization settings, storing the
	// defaults when none exist.
	RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// CountSubmissions counts the Form's submissions, and
	// HasMemberSubmission reports whether the Member submitted it.
	CountSubmissions(ctx context.Context, formID string) (int64, error)
	HasMemberSubmission(ctx context.Context, formID, memberID string) (bool, error)
	// InsertSubmission stores a new submission and fills it with the
	// stored row.
	InsertSubmission(ctx context.Context, submission *form.Submission) error
	// Submissions reads every submission of the Form, newest first.
	Submissions(ctx context.Context, formID string) ([]form.Submission, error)
	// ShareLink reads the share link of token. A missing one is
	// ErrShareLinkNotFound.
	ShareLink(ctx context.Context, token string) (ShareLink, error)
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, memberID string) (*string, error)
	// LocalizedOGDisposition reads whether the locale's own OG image is
	// pending or ready.
	LocalizedOGDisposition(ctx context.Context, formID, locale string) (form.LocalizedOGDisposition, error)
	// ReadyOGAsset resolves the first ready public asset of localizedID
	// and sourceID.
	ReadyOGAsset(ctx context.Context, localizedID, sourceID *string) (*form.Asset, error)
	// FeaturedImage resolves the Form's ready featured image; a failure is
	// logged and answers none.
	FeaturedImage(ctx context.Context, formID string) *form.Asset
}

// ShareLink is a share link that may open a Form or its dashboard.
// EntityType is the stored share link entity type.
type ShareLink struct {
	EntityID     string
	EntityType   string
	ExpiresAt    *time.Time
	PasswordHash *string
}

// The stored share link entity types that open a Form and its dashboard.
const (
	ShareLinkForm          = "SHARE_LINK_ENTITY_TYPE_FORM"
	ShareLinkFormDashboard = "SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD"
)

// ErrShareLinkNotFound answers a token no share link holds.
var ErrShareLinkNotFound = errors.New("share link not found")
