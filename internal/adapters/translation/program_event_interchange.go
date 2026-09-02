package translationadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// ProgramEventInterchange adapts XLIFF target projection and replacement to
// Program Event's typed locale-document, projection and Audit seam.
type ProgramEventInterchange struct {
	domain programEventTranslationInterchangeDomain
}

type programEventTranslationInterchangeDomain interface {
	ApplyTranslationInterchangeCandidateWithDB(
		context.Context,
		*gorm.DB,
		*contentblock.Store,
		*model.TranslationJob,
		*core.Candidate,
		core.EntryWrite,
		*string,
	) error
}

func NewProgramEventInterchange(domain programEventTranslationInterchangeDomain) *ProgramEventInterchange {
	if domain == nil {
		panic("Program Event translation interchange domain is required")
	}
	return &ProgramEventInterchange{domain: domain}
}

type programEventInterchangeTarget struct {
	state     application.TranslationInterchangeTargetState
	localized *contentv1.LocalizedRichTextDocument
}

func (*ProgramEventInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadProgramEventInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
	return loaded.state, err
}

func (a *ProgramEventInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindProgramEvent)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadProgramEventInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if err := requireTranslationInterchangeRevision(current.state, command.ExpectedRevision); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	targets := interchangeCandidateTargets(command.Mode, current.state.Targets, command.Targets)
	candidate, err := buildBlockInterchangeCandidate(command, current.localized, targets)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	candidate.Summary = entityInterchangeTarget(targets, command.Plan, "summary")
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	job := &model.TranslationJob{
		EntityType: command.EntityType, EntityID: command.EntityID,
		SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		RequestedByMemberID: memberID,
	}
	if err := a.domain.ApplyTranslationInterchangeCandidateWithDB(
		ctx,
		db,
		store,
		job,
		candidate,
		core.EntryWrite{Now: command.Now.UTC()},
		command.ExpectedRevision,
	); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	after, err := loadProgramEventInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	return application.TranslationInterchangeApplyResult{
		Revision:            after.state.Revision,
		Changed:             after.state.Revision != current.state.Revision,
		AffectedUnitHandles: append([]string(nil), command.UnitHandles...),
	}, nil
}

func loadProgramEventInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (programEventInterchangeTarget, error) {
	if db == nil || store == nil || plan == nil {
		return programEventInterchangeTarget{}, errors.New("program event translation interchange load dependencies are required")
	}
	if entityType != string(core.KindProgramEvent) || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return programEventInterchangeTarget{}, errs.InvalidArgument("target", "Program Event translation interchange identity does not match the extraction plan")
	}
	var root struct {
		ContentDocumentID *string `gorm:"column:content_document_id"`
	}
	if err := db.WithContext(ctx).Table("program_event").Select("content_document_id").
		Where("id = ?", entityID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programEventInterchangeTarget{}, errs.NotFound("program event", entityID)
		}
		return programEventInterchangeTarget{}, errs.Internal(err)
	}
	documentID, err := canonicalInterchangeDocumentID(root.ContentDocumentID)
	if err != nil {
		return programEventInterchangeTarget{}, errs.FailedPrecondition("Program Event content document identity is invalid")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, db, documentID, plan.SourceLocale)
	if err != nil {
		return programEventInterchangeTarget{}, err
	}
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return programEventInterchangeTarget{}, err
	}
	if localized.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT {
		return programEventInterchangeTarget{}, errs.FailedPrecondition("Program Event translation interchange requires the Program Event content profile")
	}

	var row struct {
		Summary *string `gorm:"column:summary"`
	}
	query := db.WithContext(ctx).Table("program_event_translation").Select("summary").
		Where("entity_id = ? AND locale = ?", entityID, locale).Take(&row)
	if query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return programEventInterchangeTarget{}, errs.Internal(query.Error)
	}
	exists := query.Error == nil
	if !exists && len(localized.GetLocaleOverlay().GetBlocks()) != 0 {
		return programEventInterchangeTarget{}, errs.InternalMsg("Program Event target Blocks exist without owning locale metadata")
	}
	targets := make(map[string]core.UnitResult)
	if exists {
		targets, err = projectBlockInterchangeTargets(plan, localized)
		if err != nil {
			return programEventInterchangeTarget{}, err
		}
		addEntityInterchangeTarget(targets, plan, "summary", row.Summary)
	}
	state := application.TranslationInterchangeTargetState{Exists: exists, Targets: targets}
	if exists {
		state.Revision = snapshot.Document.Revision.String()
	}
	return programEventInterchangeTarget{state: state, localized: localized}, nil
}

var _ application.TranslationInterchangeDomains = (*ProgramEventInterchange)(nil)
