package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Store is what the Translation use cases read and write, bound to one
// transaction or read snapshot.
type Store interface {
	// RuntimeSettings reads the settings singleton, inserting the defaults
	// first when it is missing; never call it under a read-only unit.
	RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// Domains is the owning content domains, bound to this transaction.
	Domains() Domains

	// Locales lists the runtime locales, only the enabled ones when
	// enabledOnly.
	Locales(ctx context.Context, enabledOnly bool) ([]Locale, error)
	// Locale reads one runtime locale, or ErrLocaleNotFound.
	Locale(ctx context.Context, code string) (Locale, error)

	// LockRuntimeSettings locks the settings singleton FOR UPDATE and reads
	// it as stored.
	LockRuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// UpdateRuntimeSettings writes the set fields of change and returns the
	// stored updated_at.
	UpdateRuntimeSettings(ctx context.Context, change SettingsChange) (time.Time, error)

	// OverviewStats, LocaleHealth and EntityHealth summarize translation
	// coverage across every catalog entity.
	OverviewStats(ctx context.Context) (OverviewStats, error)
	LocaleHealth(ctx context.Context) ([]LocaleHealth, error)
	EntityHealth(ctx context.Context) ([]EntityHealth, error)

	// DocumentAuthority reads the root's source locale and Content Document
	// revision; a missing root or document answers ErrEntityNotFound.
	DocumentAuthority(ctx context.Context, entityType, entityID string) (DocumentAuthority, error)
	// Entries reads every stored locale entry of an entity by locale.
	Entries(ctx context.Context, entityType, entityID string) ([]Entry, error)
	// Entry reads one stored locale entry, or ErrEntryNotFound.
	Entry(ctx context.Context, entityType, entityID, locale string) (Entry, error)
	// SourceSwitchLocales lists the locales a source-locale switch touches:
	// both locales, every stored entry and every Content Block locale.
	SourceSwitchLocales(ctx context.Context, entityType, entityID string, documentID uuid.UUID, previous, next string) ([]string, error)
	// SwitchSourceLocale moves the Content Document and the root to
	// switch.Requested under the expected document revision, preparing the
	// requested locale's entry through the owning domain.
	SwitchSourceLocale(ctx context.Context, change SourceLocaleSwitch) (SwitchedSourceLocale, error)
	// PublishSourceLocaleChanged schedules content.updated for a switched
	// source locale.
	PublishSourceLocaleChanged(ctx context.Context, entityType, entityID, documentRevision string) error
}

// The answers the Store gives for a missing row.
var (
	ErrLocaleNotFound = errors.New("translation locale not found")
	ErrEntryNotFound  = errors.New("translation entry not found")
	ErrEntityNotFound = errors.New("translation entity not found")
)

// Domains is what Translation asks of the owning content domains in one
// transaction. Each call routes by entity type to the domain's adapter; the
// authorization calls answer the request principal.
type Domains interface {
	LoadSourceDocument(ctx context.Context, entityType, entityID string) (*translation.SourceDocument, error)
	RequestLocaleOG(ctx context.Context, entityType, entityID, locale, reason string) (bool, error)
	// LockRoot locks the entity's root row FOR UPDATE.
	LockRoot(ctx context.Context, entityType, entityID string) error
	RequireEditable(ctx context.Context, entityType, entityID string) error
	RequireInterchangeView(ctx context.Context, entityType, entityID string) error
	RequireTranslationRead(ctx context.Context, entityType, entityID string) error
	RequireInterchangeEdit(ctx context.Context, entityType, entityID string) error
	RequireSourceLocaleEdit(ctx context.Context, entityType, entityID string) error
	RequireDocumentContributors(ctx context.Context, contributors []string) error
	RequireSourceMutable(ctx context.Context, entityType, entityID string) error
	SourceLocaleAudit(ctx context.Context, entityType, entityID, previousLocale, newLocale string) (SourceLocaleAudit, error)
	LoadInterchangeTarget(ctx context.Context, entityType, entityID, locale string, plan *translation.ExtractionPlan) (TranslationInterchangeTargetState, error)
	ApplyInterchange(ctx context.Context, apply TranslationInterchangeApply) (TranslationInterchangeApplyResult, error)
}

// DomainPlans builds an extraction plan by the owning domain's rules, without
// a transaction.
type DomainPlans interface {
	BuildExtractionPlan(target PlanTarget, source *translation.SourceDocument) (*translation.ExtractionPlan, error)
}

// SourceLocaleAudit is the owning domain's record of a source-locale switch,
// appended by the switch's mutation.
type SourceLocaleAudit struct {
	Action sharedtelemetry.AuditAction
	Build  domainaudit.Builder
}

// DocumentAuthority is a root's source locale and Content Document revision.
type DocumentAuthority struct {
	SourceLocale     string
	DocumentID       uuid.UUID
	DocumentRevision uuid.UUID
}

// SourceLocaleSwitch moves an entity's source locale from Authority's to
// Requested.
type SourceLocaleSwitch struct {
	EntityType       string
	EntityID         string
	Authority        DocumentAuthority
	Requested        string
	ExpectedRevision uuid.UUID
	Contributor      uuid.UUID
	Now              time.Time
}

// SwitchedSourceLocale is the Content Document after a switch.
type SwitchedSourceLocale struct {
	DocumentRevision uuid.UUID
	Changed          bool
}

// SettingsChange is an update of the settings singleton; a nil field is
// unchanged.
type SettingsChange struct {
	DefaultLocale *string
}

// Locale is a runtime locale of the translation catalog.
type Locale struct {
	Code        string
	DisplayName string
	Enabled     bool
	IsPublic    bool
	Dir         string
	FontProfile *string
	SortOrder   int32
}

// Entry is one stored locale entry of an entity.
type Entry struct {
	EntityType  string
	EntityID    string
	Locale      string
	Title       *string
	Summary     *string
	ContentHTML *string
	ContentText *string
	ContentJSON []byte
	UpdatedAt   time.Time
	// OgAsset is the entry's ready OG image, resolved by the Store.
	OgAsset *mediaasset.Reference
}

// OverviewStats counts locales, translatable entities and
// target entries.
type OverviewStats struct {
	TotalLocales    int64
	EnabledLocales  int64
	PublicLocales   int64
	SourceEntities  int64
	ExistingEntries int64
}

// LocaleHealth is the coverage of one runtime locale.
type LocaleHealth struct {
	Locale             Locale
	ExistingEntries    int64
	LastTargetUpdateAt *time.Time
}

// EntityHealth is the coverage of one entity type.
type EntityHealth struct {
	EntityType         string
	SourceEntities     int64
	ExistingEntries    int64
	LastSourceUpdateAt *time.Time
}

// PlanTarget names the entity and locale pair an extraction plan covers.
type PlanTarget = translation.PlanTarget
