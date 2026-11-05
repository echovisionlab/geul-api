// Package translationpostgres is the PostgreSQL Translation Store: the
// settings, the locale catalog, and the owning content domains' translation
// adapters, bound to one gorm transaction.
package translationpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mutation"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// Registry routes each translation step to the owning content domain's
// adapter in the caller's transaction.
type Registry interface {
	LoadSourceDocument(ctx context.Context, tx *gorm.DB, contents *contentblock.Store, entityType, entityID string) (*translation.SourceDocument, error)
	RequestLocaleOG(ctx context.Context, tx *gorm.DB, planner *ogpostgres.Planner, refresher *ogpostgres.Refresher, entityType, entityID, locale, reason string) (bool, error)
	// TranslationEntrySelectSQL is the domain's projection of its entry
	// table into the columns Entries scans.
	TranslationEntrySelectSQL(entityType, table string) (string, error)
	LockRoot(ctx context.Context, tx *gorm.DB, entityType, entityID string) error
	RequireEditable(ctx context.Context, tx *gorm.DB, entityType, entityID string) error
	RequireTranslationInterchangeView(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityType, entityID string) error
	RequireTranslationInterchangeEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityType, entityID string) error
	RequireTranslationRead(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityType, entityID string) error
	RequireSourceLocaleEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityType, entityID string) error
	RequireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error
	RequireTranslationSourceMutable(ctx context.Context, tx *gorm.DB, entityType, entityID string) error
	// PrepareSourceLocale readies the requested locale's entry before a
	// source-locale switch moves the root to it.
	PrepareSourceLocale(ctx context.Context, tx *gorm.DB, entityType, entityID, currentSourceLocale, requestedLocale string, now time.Time) error
	SourceLocaleAudit(ctx context.Context, tx *gorm.DB, entityType, entityID, previousLocale, newLocale string) (application.SourceLocaleAudit, error)
}

// InterchangeRegistry routes XLIFF target reads and applies to the owning
// content domain's adapter in the caller's transaction.
type InterchangeRegistry interface {
	LoadTranslationInterchangeTarget(ctx context.Context, tx *gorm.DB, contents *contentblock.Store, entityType, entityID, locale string, plan *translation.ExtractionPlan) (application.TranslationInterchangeTargetState, error)
	ApplyTranslationInterchange(ctx context.Context, tx *gorm.DB, contents *contentblock.Store, apply application.TranslationInterchangeApply) (application.TranslationInterchangeApplyResult, error)
}

// Ports are the domain adapters and shared runtimes the Store drives in the
// Translation transaction.
type Ports struct {
	Domains     Registry
	Interchange InterchangeRegistry
	// ContentBlocks, OGPlanner and OGRefresher are handed to the domain
	// adapters.
	ContentBlocks *contentblock.Store
	OGPlanner     *ogpostgres.Planner
	OGRefresher   *ogpostgres.Refresher
	// SpiceDB answers the domain adapters' authorization checks.
	SpiceDB *auth.SpiceDBClient
	// Signals schedules content.updated in the mutation's transaction.
	Signals contentsignal.Publisher
	// CDNDomain prefixes OG asset URLs; empty keeps them relative.
	CDNDomain string
}

func (p Ports) validate() error {
	absent := map[string]bool{
		"Domains": p.Domains == nil, "Interchange": p.Interchange == nil, "ContentBlocks": p.ContentBlocks == nil,
		"OGPlanner": p.OGPlanner == nil, "OGRefresher": p.OGRefresher == nil, "SpiceDB": p.SpiceDB == nil,
		"Signals": p.Signals == nil,
	}
	var missing []string
	for name, isAbsent := range absent {
		if isAbsent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("translation store dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Translation use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[application.Store], error) {
	if db == nil {
		return nil, errors.New("translation store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) application.Store { return NewStore(tx, ports) }), nil
}

// NewStore returns the Store over tx.
func NewStore(tx *gorm.DB, ports Ports) application.Store {
	return &store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ application.Store = (*store)(nil)

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers a domain sentinel.
type notFoundError struct {
	error
	sentinel error
}

func (err notFoundError) Is(target error) bool { return target == err.sentinel }
func (err notFoundError) Unwrap() error        { return err.error }

// notFound answers gorm's not-found error as sentinel.
func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}
