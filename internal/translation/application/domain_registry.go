package application

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

// DomainRegistry is the composition boundary between the Translation
// application and each owning content domain. Implementations live under
// internal/adapters/translation; Translation core must not know domain SQL or
// import migrated domain packages.
type DomainRegistry interface {
	LoadSourceDocument(context.Context, *gorm.DB, *contentblock.Store, string, string) (*translation.SourceDocument, error)
	BuildExtractionPlan(*model.TranslationJob, *translation.SourceDocument) (*translation.ExtractionPlan, error)
	BuildCandidate(*translation.ExtractionPlan, *translation.SourceDocument, map[string]translation.UnitResult) (*translation.Candidate, error)
	ApplyCandidate(context.Context, *gorm.DB, *contentblock.Store, *model.TranslationJob, *translation.Candidate, translation.EntryWrite) (AppliedTranslationTarget, error)
	RequestLocaleOG(context.Context, *gorm.DB, *og.Planner, *og.Refresher, string, string, string, string) (bool, error)
	TranslationEntrySelectSQL(string, string) (string, error)
	LockRoot(context.Context, *gorm.DB, string, string) error
	RequireEditable(context.Context, *gorm.DB, string, string) error
	RequireTranslationInterchangeView(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error
	RequireTranslationInterchangeEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error
	RequireJobRead(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error
	RequireSourceLocaleEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error
	PrepareSourceLocale(context.Context, *gorm.DB, string, string, string, string, time.Time) error
	AppendSourceLocaleAudit(context.Context, *gorm.DB, string, string, string, string) error
	RequireLegalEditable(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error
	RequireDocumentContributors(context.Context, *gorm.DB, []string) error
	RequireTranslationSourceMutable(context.Context, *gorm.DB, string, string) error
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
