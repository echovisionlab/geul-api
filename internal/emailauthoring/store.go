package emailauthoring

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Sentinel outcomes of the Store.
var (
	// ErrTemplateNotFound answers a read or lock of a missing Email
	// Template.
	ErrTemplateNotFound = errors.New("email template not found")
	// ErrTemplateKeyTaken answers a write of a key another Email Template
	// holds.
	ErrTemplateKeyTaken = errors.New("email template key taken")
	// ErrLayoutNotFound answers a read or lock of a missing Email Layout.
	ErrLayoutNotFound = errors.New("email layout not found")
	// ErrLayoutKeyTaken answers a write of a key another Email Layout
	// holds.
	ErrLayoutKeyTaken = errors.New("email layout key taken")
)

// Template is an Email Template. Subject and ContentHTML are its source, or
// localized, content; DeliveryRunCount counts the delivery runs that render
// it.
type Template struct {
	ID                string
	ContentDocumentID *string
	SourceLocale      string
	Key               string
	Name              string
	Subject           string
	Description       *string
	ContentHTML       *string
	Variables         []TemplateVariable
	IsSystem          bool
	IsActive          bool
	EventKey          *string
	LayoutID          *string
	CreatedAt         time.Time
	UpdatedAt         *time.Time
	DeliveryRunCount  int32
}

// TemplateVariable is a variable an Email Template renders.
type TemplateVariable struct {
	Name         string
	Description  string
	DefaultValue *string
}

// TemplateChange is an update of an Email Template's metadata; a nil field
// keeps its value. SetLayout replaces the layout with LayoutID, nil clearing
// it.
type TemplateChange struct {
	Name        *string
	Description *string
	IsActive    *bool
	SetLayout   bool
	LayoutID    *string
	UpdatedAt   time.Time
}

// Layout is an Email Layout with its source HTML and the references that
// count it.
type Layout struct {
	ID                string
	ContentDocumentID string
	SourceLocale      string
	Name              string
	Key               string
	HTMLContent       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CampaignCount     int32
	TemplateCount     int32
	DeliveryRunCount  int32
}

// LayoutChange renames an Email Layout or changes its key; a nil field keeps
// its value.
type LayoutChange struct {
	Name      *string
	Key       *string
	UpdatedAt time.Time
}

// LayoutAuthority is an Email Layout root with its content document's
// current revision.
type LayoutAuthority struct {
	LayoutID         string
	SourceLocale     string
	DocumentID       uuid.UUID
	DocumentRevision uuid.UUID
}

// LayoutExternalReferenceCounts contains references owned outside Email
// Authoring. Template-to-layout references remain owned and counted locally.
type LayoutExternalReferenceCounts struct {
	Campaigns    int32
	DeliveryRuns int32
}

// ListQuery is one page of a filtered, sorted administrator list.
type ListQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Page    queryutil.Pagination
}

// Store is what the Email Template and Email Layout use cases read and
// write, bound to one transaction or read snapshot. Its embedded email
// content is the Email Templates'. emailauthoringpostgres implements it
// over the authoring tables and the delivery history, rendering, Member and
// collaboration runtimes an authoring transaction drives;
// emailauthoringtest is the in-memory fake.
type Store interface {
	emailcontent.Store
	Templates
	Layouts
	LayoutTranslations
	DeliveryReferences
	Collaboration
	Rendering
}

// Templates reads and writes the email_template table. A missing row is
// ErrTemplateNotFound.
type Templates interface {
	// ListTemplates lists one page of Email Templates and counts every
	// match.
	ListTemplates(ctx context.Context, query ListQuery) ([]Template, int64, error)
	// FindTemplate reads a template.
	FindTemplate(ctx context.Context, templateID string) (Template, error)
	// LockTemplate locks a template for update and reads it.
	LockTemplate(ctx context.Context, templateID string) (Template, error)
	// TemplateLayoutID reads a template's layout assignment.
	TemplateLayoutID(ctx context.Context, templateID string) (*string, error)
	// TemplateDocument reads a template's content document reference,
	// locking the row when lock is set.
	TemplateDocument(ctx context.Context, templateID string, lock bool) (*string, error)
	// TemplateSourceLocale reads a template's translation source locale,
	// locking the row when lock is set.
	TemplateSourceLocale(ctx context.Context, templateID string, lock bool) (string, error)
	// InsertTemplate stores a new template and fills its ID and stored
	// times. A key another template holds is ErrTemplateKeyTaken.
	InsertTemplate(ctx context.Context, template *Template) error
	// UpdateTemplate writes change.
	UpdateTemplate(ctx context.Context, templateID string, change TemplateChange) error
	// SetCustomTemplateVariables stores the variables of a template that is
	// not a system template.
	SetCustomTemplateVariables(ctx context.Context, templateID string, variables []TemplateVariable) error
	// DeleteTemplate deletes the template row.
	DeleteTemplate(ctx context.Context, templateID string) error
	// MappedTemplates lists the templates mapped to an automatic email
	// event, ordered by event.
	MappedTemplates(ctx context.Context) ([]Template, error)
	// LockEventMappingTemplates locks, ordered by ID, the template mapped
	// to eventKey and the template targetID names.
	LockEventMappingTemplates(ctx context.Context, eventKey, targetID string) ([]Template, error)
	// ClearEventMapping unmaps eventKey, and SetEventMapping maps it to the
	// template.
	ClearEventMapping(ctx context.Context, eventKey string, now time.Time) error
	SetEventMapping(ctx context.Context, templateID, eventKey string, now time.Time) error
	// LockTranslationSource locks the root row of a translation source:
	// an email_template or email_layout.
	LockTranslationSource(ctx context.Context, entityType, entityID string) error
}

// Layouts reads and writes the email_layout table. A missing row is
// ErrLayoutNotFound.
type Layouts interface {
	// ListLayouts lists one page of Email Layouts and counts every match.
	ListLayouts(ctx context.Context, query ListQuery) ([]Layout, int64, error)
	// FindLayout reads a layout, and LockLayout locks it for update first.
	FindLayout(ctx context.Context, layoutID string) (Layout, error)
	LockLayout(ctx context.Context, layoutID string) (Layout, error)
	// LockLayoutRoot locks the layout row with lock and requires it.
	LockLayoutRoot(ctx context.Context, layoutID string, lock emailcontent.RowLock) error
	// LockLayouts locks the layouts of layoutIDs that exist for update, in
	// ID order, and returns their IDs.
	LockLayouts(ctx context.Context, layoutIDs []string) ([]string, error)
	// LayoutAuthority reads the layout with its content document revision,
	// locking the layout row with lock.
	LayoutAuthority(ctx context.Context, layoutID string, lock emailcontent.RowLock) (LayoutAuthority, error)
	// InsertLayout stores a new layout and fills its ID and stored times. A
	// key another layout holds is ErrLayoutKeyTaken.
	InsertLayout(ctx context.Context, layout *Layout) error
	// UpdateLayout writes change. A key another layout holds is
	// ErrLayoutKeyTaken.
	UpdateLayout(ctx context.Context, layoutID string, change LayoutChange) error
	// DeleteLayout deletes the layout row.
	DeleteLayout(ctx context.Context, layoutID string) error
	// TemplateCounts counts the templates assigned to each layout.
	TemplateCounts(ctx context.Context, layoutIDs []string) (map[string]int32, error)
}

// LayoutTranslations reads and writes the email_layout_translation rows of
// a layout (internal/email holds their HTML rules).
type LayoutTranslations interface {
	// LayoutSourceLocale reads the source locale of the layout's
	// translations and reports whether it is initialized.
	LayoutSourceLocale(ctx context.Context, layoutID string) (string, bool, error)
	// CanonicalLayoutSource reads the source locale's document.
	CanonicalLayoutSource(ctx context.Context, layoutID, sourceLocale string) (email.LayoutTranslationDocument, error)
	// CanonicalLayoutSources reads the source HTML of each layout of
	// layoutIDs that has one.
	CanonicalLayoutSources(ctx context.Context, layoutIDs []string) (map[string]string, error)
	// LayoutTranslationEntry reads the row of locale, nil when absent.
	LayoutTranslationEntry(ctx context.Context, layoutID, locale string) (*email.LayoutTranslationEntry, error)
	// LockLayoutTranslationEntry reads the row of locale with lock, nil
	// when absent.
	LockLayoutTranslationEntry(ctx context.Context, layoutID, locale string, lock emailcontent.RowLock) (*email.LayoutTranslationEntry, error)
	// SaveLayoutSource stores the source locale's document.
	SaveLayoutSource(ctx context.Context, layoutID, locale string, document email.LayoutTranslationDocument, now time.Time) error
	// UpsertLayoutTranslation stores the row of locale.
	UpsertLayoutTranslation(ctx context.Context, layoutID, locale string, input translation.EntryWrite) error
	// SeedLayoutTranslation creates the row of locale from the source.
	SeedLayoutTranslation(ctx context.Context, layoutID, locale, sourceLocale string, now time.Time) error
	// DeleteLayoutTranslation deletes the row of locale and reports whether
	// it existed.
	DeleteLayoutTranslation(ctx context.Context, layoutID, locale string) (bool, error)
	// LocalizedLayoutHTML resolves the layout's HTML in locale, falling
	// back to the source.
	LocalizedLayoutHTML(ctx context.Context, layoutID, locale string) (string, error)
}

// DeliveryReferences is Campaign's delivery history that freezes or retains
// an authoring resource; Campaign owns it.
type DeliveryReferences interface {
	TemplateDeliveryRunCounts(ctx context.Context, templateIDs []string) (map[string]int32, error)
	LayoutExternalReferenceCounts(ctx context.Context, layoutIDs []string) (map[string]LayoutExternalReferenceCounts, error)
	// RequireTemplateMutable and RequireLayoutMutable reject a resource an
	// active delivery run still renders.
	RequireTemplateMutable(ctx context.Context, templateID string) error
	RequireLayoutMutable(ctx context.Context, layoutID string) error
	// DetachTemplateHistory and DetachLayoutHistory release the resource
	// from terminal delivery history before it is deleted.
	DetachTemplateHistory(ctx context.Context, templateID string) error
	DetachLayoutHistory(ctx context.Context, layoutID string) error
}

// Collaboration is the session, Member and contributor state an authoring
// collaboration write checks in its transaction.
type Collaboration interface {
	// ResolveSessionPrincipal resolves a browser session's principal; an
	// invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// LockMembers takes a KEY SHARE lock on each existing Member of
	// memberIDs and returns the IDs it locked.
	LockMembers(ctx context.Context, memberIDs []string) ([]string, error)
	// RequireCurrentTemplateContributors and
	// RequireCurrentLayoutContributors require the room's contributors to
	// still be current.
	RequireCurrentTemplateContributors(ctx context.Context, templateID string, contributors []string) error
	RequireCurrentLayoutContributors(ctx context.Context, layoutID string, contributors []string) error
}

// Rendering is the delivery runtime a preview and a test send use.
type Rendering interface {
	// LocalizedTemplate resolves the template's subject, content and
	// layout in locale, falling back to the source.
	LocalizedTemplate(ctx context.Context, template Template, locale string) (Template, error)
	// EmailRenderData adds the site defaults to a preview's render data;
	// it logs and ignores a failed read.
	EmailRenderData(ctx context.Context, cdnDomain, siteOrigin, locale string, input map[string]string) map[string]string
	// CheckTestSendReadiness rejects a test send the mail adapter cannot
	// deliver to recipient.
	CheckTestSendReadiness(ctx context.Context, recipient string) error
}

// Joined is a transaction a pipeline outside Email Authoring opened, which
// an authoring step joins instead of opening its own: a translation
// delivery or interchange. Store and Audit are both bound to that
// transaction, so the step passes a nil transaction to domainaudit.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
