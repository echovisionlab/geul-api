package translationadapter

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DomainRegistry composes Translation core with each aggregate-owned adapter.
type DomainRegistry struct {
	ports map[core.Kind]domainPortFunctions
}

// NewDomainRegistry routes every catalog domain to its owning adapter; the
// owners append their locale-content records to audit.
func NewDomainRegistry(
	emailReferences emailauthoringpostgres.DeliveryReferences,
	audit domainaudit.Appender,
) (*DomainRegistry, error) {
	if emailReferences == nil || audit == nil {
		return nil, errors.New("translation domain registry dependencies are required: EmailReferences, Audit")
	}
	ports, err := buildDomainPorts(defaultDomainRegistrations(emailReferences, audit))
	if err != nil {
		return nil, err
	}
	return &DomainRegistry{ports: ports}, nil
}

func (r *DomainRegistry) LoadSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
) (*core.SourceDocument, error) {
	port, err := r.domainPort(entityType)
	if err != nil {
		return nil, err
	}
	return port.loadSourceDocument(ctx, db, store, entityID)
}

func (r *DomainRegistry) BuildExtractionPlan(target application.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
	port, err := r.domainPort(target.EntityType)
	if err != nil {
		return nil, err
	}
	return port.buildExtractionPlan(target, source)
}

func (r *DomainRegistry) RequestLocaleOG(
	ctx context.Context,
	tx *gorm.DB,
	planner *ogpostgres.Planner,
	refresher *ogpostgres.Refresher,
	entityType string,
	entityID string,
	locale string,
	reason string,
) (bool, error) {
	port, err := r.domainPort(entityType)
	if err != nil {
		return false, err
	}
	return port.requestLocaleOG(ctx, tx, planner, refresher, entityID, locale, reason)
}

func (r *DomainRegistry) TranslationEntrySelectSQL(entityType string, table string) (string, error) {
	port, err := r.domainPort(entityType)
	if err != nil {
		return "", errs.InvalidArgument("target.entity_type", err.Error())
	}
	return port.translationEntrySelectSQL(table), nil
}

func (*DomainRegistry) LockRoot(ctx context.Context, tx *gorm.DB, entityType string, entityID string) error {
	definition, ok := core.DefinitionForKind(entityType)
	if !ok {
		return errs.InvalidArgument("target.entity_type", "unsupported translation entity type")
	}
	var row struct{ ID string }
	result := tx.WithContext(ctx).Table(definition.RootTable).
		Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", entityID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound(entityType, entityID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	return nil
}

func (r *DomainRegistry) RequireEditable(ctx context.Context, db *gorm.DB, entityType string, entityID string) error {
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	return port.requireEditable(ctx, db, entityID)
}

// RequireTranslationInterchangeView is the single XLIFF export authorization
// boundary. Each owning lifecycle chooses exactly view or view_archived while
// the caller-owned transaction keeps the root stable for the following source
// and target projection.
func (r *DomainRegistry) RequireTranslationInterchangeView(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	err = port.requireInterchangeView(ctx, tx, spiceDB, entityID)
	return maskTranslationAuthorizationDenial(err, entityType, entityID)
}

// RequireTranslationInterchangeEdit is the single XLIFF import authorization
// boundary. It intentionally uses the same exact lifecycle-aware Edit seam as
// an interactive target mutation, not the export/View seam.
func (r *DomainRegistry) RequireTranslationInterchangeEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	err = port.requireInterchangeEdit(ctx, tx, spiceDB, entityID)
	return maskTranslationAuthorizationDenial(err, entityType, entityID)
}

func (r *DomainRegistry) RequireTranslationRead(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("filters.entity_type", err.Error())
	}
	return port.requireTranslationRead(ctx, db, spiceDB, entityID)
}

func (r *DomainRegistry) PrepareSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	currentSourceLocale string,
	requestedLocale string,
	now time.Time,
) error {
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	return port.prepareSourceLocale(ctx, db, entityID, currentSourceLocale, requestedLocale, now)
}

// SourceLocaleAudit returns the owning domain's record of a source-locale
// switch from previousLocale to newLocale.
func (r *DomainRegistry) SourceLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	previousLocale string,
	newLocale string,
) (application.SourceLocaleAudit, error) {
	port, err := r.domainPort(entityType)
	if err != nil {
		return application.SourceLocaleAudit{}, errs.InvalidArgument("target.entity_type", err.Error())
	}
	return port.sourceLocaleAudit(ctx, tx, entityID, previousLocale, newLocale)
}

func (r *DomainRegistry) RequireSourceLocaleEdit(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	err = port.requireSourceLocaleEdit(ctx, db, spiceDB, entityID)
	return maskTranslationAuthorizationDenial(err, entityType, entityID)
}

func maskTranslationAuthorizationDenial(err error, entityType string, entityID string) error {
	if errs.CodeOf(err) == connect.CodePermissionDenied {
		return errs.WithCause(errs.NotFound(entityType, entityID), err)
	}
	return err
}

type translationCanSet struct {
	view         auth.ResourceAction
	edit         auth.ResourceAction
	viewArchived auth.ResourceAction
	editArchived auth.ResourceAction
}

func (*DomainRegistry) RequireDocumentContributors(
	ctx context.Context,
	tx *gorm.DB,
	contributors []string,
) error {
	if len(contributors) == 0 {
		return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires contributors")
	}
	for index, contributor := range contributors {
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		if index > 0 && contributors[index-1] >= contributor {
			return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
	}
	locked, err := memberadapter.ContributorMembers{}.LockMembers(ctx, tx, contributors)
	if err != nil {
		return errs.Internal(err)
	}
	if len(locked) != len(contributors) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

func (r *DomainRegistry) RequireTranslationSourceMutable(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) error {
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	return port.requireSourceMutable(ctx, tx, entityID)
}

var (
	_ translationpostgres.Registry = (*DomainRegistry)(nil)
	_ application.DomainPlans      = (*DomainRegistry)(nil)
)
