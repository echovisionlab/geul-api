package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// JobStore is what the job runner reads and writes, bound to one transaction
// or read snapshot. translationpostgres implements it over translation_job
// and the owning domains' adapters; translationtest is the in-memory fake.
type JobStore interface {
	// Job reads the job's state without its request artifact; found is
	// false when the row is gone.
	Job(ctx context.Context, jobID string) (job Job, found bool, err error)
	// RequestArtifact reads the job's stored request artifact, or
	// ErrJobNotFound.
	RequestArtifact(ctx context.Context, jobID string) (StoredRequestArtifact, error)
	// ClaimJob moves a queued job to running for provider and model, unless
	// a stored provider document pins another provider. It answers whether
	// it moved the row.
	ClaimJob(ctx context.Context, jobID, provider, model string, now time.Time) (bool, error)
	// ResumeJob records provider and model on a running job under the same
	// pin. It answers whether it updated the row.
	ResumeJob(ctx context.Context, jobID, provider, model string, now time.Time) (bool, error)
	// LockJobStatus locks the job's row FOR UPDATE and reads its status, or
	// ErrJobNotFound.
	LockJobStatus(ctx context.Context, jobID string) (JobStatus, error)
	// LockJobProviderDocument locks the job's row FOR UPDATE and reads its
	// status, provider, model and provider document, or ErrJobNotFound.
	LockJobProviderDocument(ctx context.Context, jobID string) (Job, error)
	// SetJobProvider records provider and model on a running job.
	SetJobProvider(ctx context.Context, jobID, provider, model string, now time.Time) error
	// SetJobProviderDocument stores document on the running job of its
	// provider and model; it answers whether it updated the row.
	SetJobProviderDocument(ctx context.Context, jobID string, document ProviderDocument, now time.Time) (bool, error)
	// ClearJobProviderDocument clears document from the running job that
	// still holds exactly it; it answers whether it updated the row.
	ClearJobProviderDocument(ctx context.Context, jobID string, document ProviderDocument, now time.Time) (bool, error)
	// DeleteJob deletes the job while its status is one of statuses; it
	// answers how many rows it deleted.
	DeleteJob(ctx context.Context, jobID string, statuses ...JobStatus) (int64, error)

	// RuntimeSettings reads the settings singleton, inserting the defaults
	// first when it is missing; never call it under a read-only unit.
	RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// SiteNames reads the site title and company name, which a request
	// protects as literal spellings.
	SiteNames(ctx context.Context) (siteTitle, companyName string, err error)
	// ActiveProviders lists the active providers by priority.
	ActiveProviders(ctx context.Context) ([]Provider, error)

	// Domains is the owning content domains, bound to this transaction.
	Domains() Domains
	// PublishTranslationApplied schedules content.updated for a provider
	// result the owning domain applied as target.
	PublishTranslationApplied(ctx context.Context, job Job, target AppliedTranslationTarget, now time.Time) error
}

// Store is what the Translation use cases read and write, bound to one
// transaction or read snapshot.
type Store interface {
	JobStore

	// JobForCommand reads the whole job, FOR UPDATE when locked, or
	// ErrJobNotFound.
	JobForCommand(ctx context.Context, jobID string, locked bool) (Job, error)
	// ListJobs reads one page of jobs and the count of every match.
	ListJobs(ctx context.Context, query JobListQuery) ([]Job, int64, error)
	// LockActiveJobs locks FOR UPDATE the queued and running jobs of key,
	// oldest first.
	LockActiveJobs(ctx context.Context, key ActiveJobKey) ([]Job, error)
	// InsertJob inserts job unless an active job of the same request holds
	// the active-job unique index; it answers whether it inserted.
	InsertJob(ctx context.Context, job Job) (bool, error)
	// EnqueueGenerate enqueues the generate command of a job in this
	// transaction.
	EnqueueGenerate(ctx context.Context, jobID string) error

	// Locales lists the runtime locales, only the enabled ones when
	// enabledOnly.
	Locales(ctx context.Context, enabledOnly bool) ([]Locale, error)
	// Locale reads one runtime locale, or ErrLocaleNotFound.
	Locale(ctx context.Context, code string) (Locale, error)
	// MachineTranslationTargets lists the locales machine translation may
	// target.
	MachineTranslationTargets(ctx context.Context) ([]Locale, error)

	// LockRuntimeSettings locks the settings singleton FOR UPDATE and reads
	// it as stored.
	LockRuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// UpdateRuntimeSettings writes the set fields of change and returns the
	// stored updated_at.
	UpdateRuntimeSettings(ctx context.Context, change SettingsChange) (time.Time, error)

	// ListProviders reads one page of providers by priority and the count
	// of every match.
	ListProviders(ctx context.Context, query ProviderListQuery) ([]Provider, int64, error)
	// LockProvider locks a provider FOR UPDATE, or ErrProviderNotFound.
	LockProvider(ctx context.Context, providerID string) (Provider, error)
	// InsertProvider inserts provider and returns it as stored.
	InsertProvider(ctx context.Context, provider Provider) (Provider, error)
	// UpdateProvider writes change and returns the stored updated_at.
	UpdateProvider(ctx context.Context, providerID string, change ProviderChange) (time.Time, error)
	// DeleteProvider deletes a provider.
	DeleteProvider(ctx context.Context, providerID string) error

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
	ErrJobNotFound      = errors.New("translation job not found")
	ErrProviderNotFound = errors.New("translation provider not found")
	ErrLocaleNotFound   = errors.New("translation locale not found")
	ErrEntryNotFound    = errors.New("translation entry not found")
	ErrEntityNotFound   = errors.New("translation entity not found")
)

// Domains is what Translation asks of the owning content domains in one
// transaction. Each call routes by entity type to the domain's adapter; the
// authorization calls answer the request principal.
type Domains interface {
	LoadSourceDocument(ctx context.Context, entityType, entityID string) (*translation.SourceDocument, error)
	ApplyCandidate(ctx context.Context, job Job, candidate *translation.Candidate, input translation.EntryWrite) (AppliedTranslationTarget, error)
	RequestLocaleOG(ctx context.Context, entityType, entityID, locale, reason string) (bool, error)
	// LockRoot locks the entity's root row FOR UPDATE.
	LockRoot(ctx context.Context, entityType, entityID string) error
	RequireEditable(ctx context.Context, entityType, entityID string) error
	RequireInterchangeView(ctx context.Context, entityType, entityID string) error
	RequireInterchangeEdit(ctx context.Context, entityType, entityID string) error
	RequireJobRead(ctx context.Context, entityType, entityID string) error
	RequireSourceLocaleEdit(ctx context.Context, entityType, entityID string) error
	RequireLegalEditable(ctx context.Context, entityType, entityID string) error
	RequireDocumentContributors(ctx context.Context, contributors []string) error
	RequireSourceMutable(ctx context.Context, entityType, entityID string) error
	SourceLocaleAudit(ctx context.Context, entityType, entityID, previousLocale, newLocale string) (SourceLocaleAudit, error)
	LoadInterchangeTarget(ctx context.Context, entityType, entityID, locale string, plan *translation.ExtractionPlan) (TranslationInterchangeTargetState, error)
	ApplyInterchange(ctx context.Context, apply TranslationInterchangeApply) (TranslationInterchangeApplyResult, error)
}

// DomainPlans builds a job's extraction plan and a provider result's
// candidate by the owning domain's rules, without a transaction.
type DomainPlans interface {
	BuildExtractionPlan(job Job, source *translation.SourceDocument) (*translation.ExtractionPlan, error)
	BuildCandidate(plan *translation.ExtractionPlan, source *translation.SourceDocument, results map[string]translation.UnitResult) (*translation.Candidate, error)
}

// AppliedTranslationTarget is the exact locale-role fence observed by the
// owning adapter in the same transaction that accepted a provider result.
// DocumentStateChanged distinguishes a result applied to the current source
// locale from a target-only result. Changed=false represents a semantic no-op
// and carries no invalidation.
type AppliedTranslationTarget struct {
	Changed              bool
	DocumentRevision     string
	DocumentStateChanged bool
	TargetRevision       string
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
	DefaultLocale  *string
	ProtectedTerms []string
	// TermsChanged tells an emptied ProtectedTerms from an unchanged one.
	TermsChanged bool
}

// JobListQuery selects and orders one page of jobs. Filters and Sorts are
// the shared query language; the Store rejects an unknown field or operation.
type JobListQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// ProviderListQuery selects one page of providers.
type ProviderListQuery struct {
	ActiveOnly bool
	Limit      int32
	Offset     int32
}

// Locale is a runtime locale of the translation catalog.
type Locale struct {
	Code                      string
	DisplayName               string
	Enabled                   bool
	IsPublic                  bool
	Dir                       string
	MachineTranslationAllowed bool
	FontProfile               *string
	SortOrder                 int32
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

// OverviewStats counts locales, translatable entities, active jobs and
// target entries.
type OverviewStats struct {
	TotalLocales    int64
	EnabledLocales  int64
	PublicLocales   int64
	SourceEntities  int64
	ActiveJobs      int64
	ExistingEntries int64
}

// LocaleHealth is the coverage of one runtime locale.
type LocaleHealth struct {
	Locale             Locale
	ExistingEntries    int64
	ActiveJobs         int64
	LastTargetUpdateAt *time.Time
}

// EntityHealth is the coverage of one entity type.
type EntityHealth struct {
	EntityType         string
	SourceEntities     int64
	ExistingEntries    int64
	ActiveJobs         int64
	LastSourceUpdateAt *time.Time
}
