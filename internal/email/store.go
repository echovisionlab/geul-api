package email

import (
	"context"
	"errors"
	"time"
)

// ErrTemplateNotFound answers a template lookup that matched no row.
var ErrTemplateNotFound = errors.New("email template not found")

// Store combines the owner-provided rendering and delivery views.
type Store interface {
	AuthoringStore
	DeliveryStore
}

// AuthoringStore reads and writes template and layout content owned by Email Authoring.
type AuthoringStore interface {
	// ActiveTemplateByEventKey returns the active template of eventKey, or
	// ErrTemplateNotFound.
	ActiveTemplateByEventKey(ctx context.Context, eventKey string) (Template, error)
	// TemplateByID returns the template, active or not, or
	// ErrTemplateNotFound.
	TemplateByID(ctx context.Context, templateID string) (Template, error)
	// TemplateSourceLocale returns the template's stored source locale;
	// found is false when there is no such template.
	TemplateSourceLocale(ctx context.Context, templateID string) (locale string, found bool, err error)
	// TemplateContent returns the template's locale row when it has HTML
	// content, or nil.
	TemplateContent(ctx context.Context, templateID, locale string) (*TemplateContent, error)
	// TemplateTranslations returns the template's rows in locales.
	TemplateTranslations(ctx context.Context, templateID string, locales []string) ([]TemplateContent, error)

	// LayoutSourceLocale returns the layout's stored source locale; found is
	// false when there is no such layout.
	LayoutSourceLocale(ctx context.Context, layoutID string) (locale string, found bool, err error)
	// LayoutContent returns the HTML of the layout's locale row when it has
	// any, or nil.
	LayoutContent(ctx context.Context, layoutID, locale string) (*string, error)
	// LayoutTranslations returns the layout's rows in locales.
	LayoutTranslations(ctx context.Context, layoutID string, locales []string) ([]LayoutContent, error)
	// SourceLayouts returns the source-locale row of each layout that has
	// one.
	SourceLayouts(ctx context.Context, layoutIDs []string) ([]LayoutContent, error)
	// LayoutLocales returns every locale row of the layout, the source
	// locale first and the others by locale.
	LayoutLocales(ctx context.Context, layoutID string) ([]LayoutContent, error)
	// LayoutEntry returns the layout's locale row, or nil.
	LayoutEntry(ctx context.Context, layoutID, locale string) (*LayoutEntry, error)
	// LayoutTargets returns the layout's rows other than sourceLocale, by
	// locale.
	LayoutTargets(ctx context.Context, layoutID, sourceLocale string) ([]LayoutEntry, error)
	// UpsertLayoutEntry inserts the layout's locale row or replaces its
	// content and updated_at.
	UpsertLayoutEntry(ctx context.Context, layoutID, locale string, html, text *string, now time.Time) error
	// UpdateLayoutEntry replaces the content of an existing row and keeps
	// its updated_at.
	UpdateLayoutEntry(ctx context.Context, layoutID, locale string, html, text *string) error
	// InsertLayoutEntry inserts the row unless the locale already has one.
	InsertLayoutEntry(ctx context.Context, layoutID, locale string, html, text *string, now time.Time) error
}

// DeliveryStore reads provider and suppression state owned by Email Delivery.
type DeliveryStore interface {
	ReadinessStore
	AdapterStore
}

// ReadinessStore checks whether a test recipient can be sent mail.
type ReadinessStore interface {
	// ActiveSuppressionCount counts the unreleased suppressions of the
	// lower-cased address.
	ActiveSuppressionCount(ctx context.Context, address string) (int64, error)
	// ActiveMailAdapterCount counts the active mail adapters.
	ActiveMailAdapterCount(ctx context.Context) (int64, error)
}

// TemplateContent is one locale row of a template.
type TemplateContent struct {
	Locale      string
	Subject     *string
	ContentHTML *string
}

// LayoutContent is one locale row of a layout: its layout, locale, HTML and
// whether the locale is the layout's source locale.
type LayoutContent struct {
	LayoutID       string
	Locale         string
	HTMLContent    *string
	IsSourceLocale bool
}

// LayoutEntry is one stored locale row of a layout with its plain text.
type LayoutEntry struct {
	Locale      string
	HTMLContent *string
	ContentText *string
	UpdatedAt   time.Time
}
