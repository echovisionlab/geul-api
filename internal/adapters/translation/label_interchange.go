package translationadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	labeldomain "github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// LabelInterchange adapts XLIFF target projection and exact sparse mutation
// to Label's existing typed Content Block transaction seam.
type LabelInterchange struct {
	auditWriter domainaudit.Appender
}

func NewLabelInterchange(auditWriter domainaudit.Appender) *LabelInterchange {
	if auditWriter == nil {
		panic("Label translation interchange Audit writer is required")
	}
	return &LabelInterchange{auditWriter: auditWriter}
}

func (*LabelInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadLabelInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
	return loaded.state, err
}

func (a *LabelInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindLabel)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadLabelInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if err := requireTranslationInterchangeRevision(current.state, command.ExpectedRevision); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	candidate, err := buildCreativeInterchangeCandidate(command, current.localized)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	job := &model.TranslationJob{
		EntityType: command.EntityType, EntityID: command.EntityID,
		SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		RequestedByMemberID: memberID,
	}
	var expectedTargetRevision *string
	if current.state.Exists {
		expectedTargetRevision = command.ExpectedRevision
	}
	if err := labeldomain.ApplyTypedTranslationCandidateWithTargetRevision(
		ctx, db, store, job, candidate, core.EntryWrite{Now: command.Now.UTC()}, a.auditWriter,
		expectedTargetRevision,
	); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	after, err := loadLabelInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	changed := after.state.Revision != current.state.Revision
	return application.TranslationInterchangeApplyResult{
		Revision: after.state.Revision, Changed: changed,
		AffectedUnitHandles: append([]string(nil), command.UnitHandles...),
	}, nil
}

func loadLabelInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (creativeInterchangeTarget, error) {
	if err := validateCreativeInterchangeLoad(db, store, entityType, entityID, locale, plan, string(core.KindLabel)); err != nil {
		return creativeInterchangeTarget{}, err
	}
	target, err := labeldomain.LoadTypedTranslationInterchangeTarget(ctx, db, store, entityID, locale)
	if err != nil {
		return creativeInterchangeTarget{}, err
	}
	return projectCreativeInterchangeTarget(plan, target.Exists, target.Revision, target.Document)
}

var _ application.TranslationInterchangeDomains = (*LabelInterchange)(nil)
