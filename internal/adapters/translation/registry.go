package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/google/uuid"
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

func (r *DomainRegistry) BuildExtractionPlan(job application.Job, source *core.SourceDocument) (*core.ExtractionPlan, error) {
	port, err := r.domainPort(job.EntityType)
	if err != nil {
		return nil, err
	}
	return port.buildExtractionPlan(modelJob(job), source)
}

// modelJob is the job as the owning domains' translation adapters take it.
func modelJob(job application.Job) *model.TranslationJob {
	return &model.TranslationJob{
		ID: job.ID, EntityType: job.EntityType, EntityID: job.EntityID,
		TargetLocale: job.TargetLocale, SourceLocale: job.SourceLocale,
		RequestArtifactDigest: job.RequestArtifactDigest, OperationID: job.OperationID,
		Status: string(job.Status), RequestedByMemberID: job.RequestedByMemberID,
		Provider: job.Provider, Model: job.Model,
		ProviderDocumentID: job.ProviderDocumentID, ProviderDocumentKey: job.ProviderDocumentKey,
		ProviderDocumentSubmittedAt: job.ProviderDocumentSubmittedAt,
		RequestXLIFF:                job.RequestXLIFF, RequestManifest: job.RequestManifest,
		FailureReason: job.FailureReason, RequestedAt: job.RequestedAt, StartedAt: job.StartedAt,
		CompletedAt: job.CompletedAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
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
	appliedJob application.Job,
	candidate *core.Candidate,
	input core.EntryWrite,
) (application.AppliedTranslationTarget, error) {
	job := modelJob(appliedJob)
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
		SourceLocale    string     `gorm:"column:source_locale"`
		DocumentID      uuid.UUID  `gorm:"column:content_document_id"`
		LocaleUpdatedAt *time.Time `gorm:"column:locale_updated_at"`
	}
	result := tx.WithContext(ctx).Raw(
		"SELECT root.source_locale AS source_locale, root.content_document_id, "+
			"locale_entry.updated_at AS locale_updated_at "+
			"FROM "+definition.RootTable+" AS root "+
			"LEFT JOIN "+definition.EntryTable+" AS locale_entry "+
			"ON locale_entry.entity_id = root.id AND locale_entry.locale = ? "+
			"WHERE root.id = ?",
		job.TargetLocale,
		job.EntityID,
	).Scan(&row)
	if result.Error != nil {
		return providerLocaleFence{}, errs.Internal(result.Error)
	}
	// A root without its Content Document is as absent as a missing root.
	if result.RowsAffected != 1 || row.DocumentID == uuid.Nil {
		return providerLocaleFence{}, errs.NotFound(job.EntityType, job.EntityID)
	}
	revision, err := contentblock.CurrentRevision(ctx, tx, row.DocumentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return providerLocaleFence{}, errs.NotFound(job.EntityType, job.EntityID)
	}
	if err != nil {
		return providerLocaleFence{}, errs.Internal(err)
	}
	sourceLocale := strings.TrimSpace(row.SourceLocale)
	if sourceLocale == "" || sourceLocale != row.SourceLocale {
		return providerLocaleFence{}, errs.FailedPrecondition(
			"Translation source locale is not canonical",
		)
	}
	documentRevision := revision.String()
	if revision == uuid.Nil {
		return providerLocaleFence{}, errs.FailedPrecondition("Content Document revision is not a canonical UUID")
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
