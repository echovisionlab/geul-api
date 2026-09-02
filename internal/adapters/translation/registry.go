package translationadapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DomainRegistry composes Translation core with each aggregate-owned adapter.
type DomainRegistry struct {
	ports map[core.Kind]domainPortFunctions
}

func NewDomainRegistry(
	emailReferences emailauthoring.CampaignDeliveryReferences,
	auditWriter domainaudit.Appender,
) *DomainRegistry {
	if emailReferences == nil || auditWriter == nil {
		panic("translation domain registry dependencies are required")
	}
	ports, err := buildDomainPorts(defaultDomainRegistrations(emailReferences, auditWriter))
	if err != nil {
		panic(err)
	}
	return &DomainRegistry{ports: ports}
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

func (r *DomainRegistry) BuildExtractionPlan(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
	if job == nil {
		return nil, fmt.Errorf("translation job is required")
	}
	port, err := r.domainPort(job.EntityType)
	if err != nil {
		return nil, err
	}
	return port.buildExtractionPlan(job, source)
}

func (r *DomainRegistry) BuildCandidate(
	plan *core.ExtractionPlan,
	source *core.SourceDocument,
	results map[string]core.UnitResult,
) (*core.Candidate, error) {
	if plan == nil {
		return nil, fmt.Errorf("translation extraction plan is required")
	}
	port, err := r.domainPort(plan.EntityType)
	if err != nil {
		return nil, err
	}
	return port.buildCandidate(plan, source, results)
}

func (r *DomainRegistry) ApplyCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *core.Candidate,
	input core.EntryWrite,
) (application.AppliedTranslationTarget, error) {
	before, err := loadProviderLocaleFence(ctx, tx, job)
	if err != nil {
		return application.AppliedTranslationTarget{}, err
	}
	port, err := r.domainPort(job.EntityType)
	if err != nil {
		return application.AppliedTranslationTarget{}, err
	}
	if err := port.applyCandidate(ctx, tx, store, job, candidate, input); err != nil {
		return application.AppliedTranslationTarget{}, err
	}
	after, err := loadProviderLocaleFence(ctx, tx, job)
	if err != nil {
		return application.AppliedTranslationTarget{}, err
	}
	return classifyAppliedProviderTranslation(job.TargetLocale, before, after)
}

type providerLocaleFence struct {
	sourceLocale     string
	documentRevision string
	localeUpdatedAt  *time.Time
	targetRevision   string
	exists           bool
}

func loadProviderLocaleFence(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
) (providerLocaleFence, error) {
	if tx == nil || job == nil {
		return providerLocaleFence{}, fmt.Errorf("provider locale fence dependencies are required")
	}
	definition, ok := core.DefinitionForKind(job.EntityType)
	if !ok {
		return providerLocaleFence{}, fmt.Errorf(
			"unsupported translation entity type %q",
			job.EntityType,
		)
	}
	var row struct {
		SourceLocale     string     `gorm:"column:source_locale"`
		DocumentRevision string     `gorm:"column:document_revision"`
		LocaleUpdatedAt  *time.Time `gorm:"column:locale_updated_at"`
	}
	result := tx.WithContext(ctx).Raw(
		"SELECT root.source_locale AS source_locale, "+
			"CAST(document.revision AS text) AS document_revision, locale_entry.updated_at AS locale_updated_at "+
			"FROM "+definition.RootTable+" AS root "+
			"JOIN content_document AS document ON document.id = root.content_document_id "+
			"LEFT JOIN "+definition.EntryTable+" AS locale_entry "+
			"ON locale_entry.entity_id = root.id AND locale_entry.locale = ? "+
			"WHERE root.id = ?",
		job.TargetLocale,
		job.EntityID,
	).Scan(&row)
	if result.Error != nil {
		return providerLocaleFence{}, errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return providerLocaleFence{}, errs.NotFound(job.EntityType, job.EntityID)
	}
	sourceLocale := strings.TrimSpace(row.SourceLocale)
	if sourceLocale == "" || sourceLocale != row.SourceLocale {
		return providerLocaleFence{}, errs.FailedPrecondition(
			"Translation source locale is not canonical",
		)
	}
	documentRevision := strings.TrimSpace(row.DocumentRevision)
	parsedRevision, err := uuid.Parse(documentRevision)
	if err != nil || parsedRevision == uuid.Nil || parsedRevision.String() != documentRevision {
		return providerLocaleFence{}, errs.FailedPrecondition(
			"Content Document revision is not a canonical UUID",
		)
	}
	fence := providerLocaleFence{
		sourceLocale: sourceLocale, documentRevision: documentRevision,
		localeUpdatedAt: row.LocaleUpdatedAt, exists: row.LocaleUpdatedAt != nil,
	}
	if job.TargetLocale == sourceLocale {
		if !fence.exists {
			return providerLocaleFence{}, errs.FailedPrecondition(
				"Translation source locale values are missing",
			)
		}
		return fence, nil
	}
	if !fence.exists {
		return fence, nil
	}
	targetRevision, err := core.DeriveTargetRevision(core.TargetRevisionFacts{
		LocaleExists:     true,
		DocumentRevision: documentRevision,
		LocaleUpdatedAt:  row.LocaleUpdatedAt,
	})
	if err != nil {
		return providerLocaleFence{}, errs.Internal(err)
	}
	fence.targetRevision = targetRevision
	return fence, nil
}

func classifyAppliedProviderTranslation(
	targetLocale string,
	before providerLocaleFence,
	after providerLocaleFence,
) (application.AppliedTranslationTarget, error) {
	if before.sourceLocale == "" || before.sourceLocale != after.sourceLocale {
		return application.AppliedTranslationTarget{}, fmt.Errorf(
			"provider translation source locale changed during apply",
		)
	}
	documentChanged := before.documentRevision != after.documentRevision
	if targetLocale == before.sourceLocale {
		if !before.exists || !after.exists {
			return application.AppliedTranslationTarget{}, fmt.Errorf(
				"provider source translation requires existing locale values",
			)
		}
		localeChanged := !providerLocaleUpdatedAtEqual(before.localeUpdatedAt, after.localeUpdatedAt)
		if localeChanged && !documentChanged {
			return application.AppliedTranslationTarget{}, fmt.Errorf(
				"provider source translation changed locale values without advancing the document revision",
			)
		}
		return application.AppliedTranslationTarget{
			Changed:              documentChanged,
			DocumentRevision:     after.documentRevision,
			DocumentStateChanged: documentChanged,
		}, nil
	}
	if documentChanged {
		return application.AppliedTranslationTarget{}, fmt.Errorf(
			"provider target translation advanced the shared document revision",
		)
	}
	changed := before.exists != after.exists || before.targetRevision != after.targetRevision
	if changed && !after.exists {
		return application.AppliedTranslationTarget{}, fmt.Errorf(
			"provider target translation removed its target locale",
		)
	}
	return application.AppliedTranslationTarget{
		Changed:          changed,
		DocumentRevision: after.documentRevision,
		TargetRevision:   after.targetRevision,
	}, nil
}

func providerLocaleUpdatedAtEqual(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func (r *DomainRegistry) RequestLocaleOG(
	ctx context.Context,
	tx *gorm.DB,
	planner *og.Planner,
	refresher *og.Refresher,
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
		Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", entityID).Take(&row)
	if result.Error == gorm.ErrRecordNotFound {
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

func (r *DomainRegistry) RequireJobRead(
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
	return port.requireJobRead(ctx, db, spiceDB, entityID)
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

func (r *DomainRegistry) AppendSourceLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	previousLocale string,
	newLocale string,
) error {
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	return port.appendSourceLocaleAudit(ctx, tx, entityID, previousLocale, newLocale)
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
	if connect.CodeOf(err) == connect.CodePermissionDenied {
		return errs.NotFound(entityType, entityID)
	}
	return err
}

type translationCanSet struct {
	view         auth.ResourceAction
	edit         auth.ResourceAction
	viewArchived auth.ResourceAction
	editArchived auth.ResourceAction
}

func (r *DomainRegistry) RequireLegalEditable(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
) error {
	if entityType != "privacy" && entityType != "terms" {
		return errs.InvalidArgument("target.entity_type", "Legal translation target is required")
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	port, err := r.domainPort(entityType)
	if err != nil {
		return errs.InvalidArgument("target.entity_type", err.Error())
	}
	err = port.requireRegeneration(ctx, db, spiceDB, entityID)
	return maskTranslationAuthorizationDenial(err, entityType, entityID)
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
	var locked []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", contributors).
		Find(&locked).Error; err != nil {
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

var _ application.DomainRegistry = (*DomainRegistry)(nil)
