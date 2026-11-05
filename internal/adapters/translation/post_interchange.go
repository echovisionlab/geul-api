package translationadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// PostInterchange adapts XLIFF target projection and replacement to Post's
// typed locale-document mutation and Audit seam.
type PostInterchange struct {
	domain postTranslationInterchangeDomain
}

type postTranslationInterchangeDomain interface {
	ApplyCandidateWithDB(
		context.Context,
		*gorm.DB,
		*contentblock.Store,
		postdomain.TranslationJob,
		*core.Candidate,
		core.EntryWrite,
		*string,
	) (postdomain.TranslationInterchangeMutationResult, error)
}

func NewPostInterchange(domain postTranslationInterchangeDomain) *PostInterchange {
	if domain == nil {
		panic("Post translation interchange domain is required")
	}
	return &PostInterchange{domain: domain}
}

type postInterchangeTarget struct {
	state     application.TranslationInterchangeTargetState
	localized *contentv1.LocalizedRichTextDocument
}

func (*PostInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadPostInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
	return loaded.state, err
}

func (a *PostInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindPost)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadPostInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if err := requireTranslationInterchangeRevision(current.state, command.ExpectedRevision); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	candidate, err := buildPostInterchangeCandidate(command, current)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	job := postdomain.TranslationJob{
		EntityType: command.EntityType, EntityID: command.EntityID,
		SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		RequestedByMemberID: memberID,
	}
	result, err := a.domain.ApplyCandidateWithDB(
		ctx,
		db,
		store,
		job,
		candidate,
		core.EntryWrite{Now: command.Now.UTC()},
		command.ExpectedRevision,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	return application.TranslationInterchangeApplyResult{
		Revision:            result.Revision,
		Changed:             result.Changed,
		AffectedUnitHandles: append([]string(nil), command.UnitHandles...),
	}, nil
}

func loadPostInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (postInterchangeTarget, error) {
	if db == nil || store == nil || plan == nil {
		return postInterchangeTarget{}, errors.New("post translation interchange load dependencies are required")
	}
	if entityType != string(core.KindPost) || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return postInterchangeTarget{}, errs.InvalidArgument("target", "Post translation interchange identity does not match the extraction plan")
	}
	contentDocumentID, err := postadapter.TranslationInterchangeDocumentID(ctx, db, entityID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return postInterchangeTarget{}, errs.NotFound("post", entityID)
		}
		return postInterchangeTarget{}, errs.Internal(err)
	}
	documentID, err := canonicalInterchangeDocumentID(contentDocumentID)
	if err != nil {
		return postInterchangeTarget{}, errs.WithCause(errs.FailedPrecondition("Post content document identity is invalid"), failurecause.ServerFault(err))
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, db, documentID, plan.SourceLocale)
	if err != nil {
		return postInterchangeTarget{}, err
	}
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return postInterchangeTarget{}, err
	}
	if localized.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST {
		return postInterchangeTarget{}, errs.FailedPrecondition("Post translation interchange requires the Post content profile")
	}

	row, exists, err := postadapter.TranslationInterchangeLocaleRow(ctx, db, entityID, locale)
	if err != nil {
		return postInterchangeTarget{}, errs.Internal(err)
	}
	if !exists && len(localized.GetLocaleOverlay().GetBlocks()) != 0 {
		return postInterchangeTarget{}, errs.InternalMsg("Post target Blocks exist without owning locale metadata")
	}
	targets := make(map[string]core.UnitResult)
	if exists {
		targets, err = projectBlockInterchangeTargets(plan, localized)
		if err != nil {
			return postInterchangeTarget{}, err
		}
		addEntityInterchangeTarget(targets, plan, "title", row.Title)
		addEntityInterchangeTarget(targets, plan, "summary", row.Summary)
	}
	state := application.TranslationInterchangeTargetState{Exists: exists, Targets: targets}
	if exists {
		state.Revision, err = core.DeriveTargetRevision(core.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: snapshot.Document.Revision.String(), LocaleUpdatedAt: &row.UpdatedAt,
		})
		if err != nil {
			return postInterchangeTarget{}, errs.Internal(err)
		}
	}
	return postInterchangeTarget{state: state, localized: localized}, nil
}

func buildPostInterchangeCandidate(
	command application.TranslationInterchangeApply,
	current postInterchangeTarget,
) (*core.Candidate, error) {
	targets := interchangeCandidateTargets(command.Mode, current.state.Targets, command.Targets)
	candidate, err := buildBlockInterchangeCandidate(command, current.localized, targets)
	if err != nil {
		return nil, err
	}
	candidate.Title = entityInterchangeTarget(targets, command.Plan, "title")
	candidate.Summary = entityInterchangeTarget(targets, command.Plan, "summary")
	return candidate, nil
}

var _ translationpostgres.InterchangeRegistry = (*PostInterchange)(nil)
