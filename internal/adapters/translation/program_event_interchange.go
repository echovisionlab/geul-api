package translationadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/programevent"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// ProgramEventInterchange adapts XLIFF target projection and replacement to
// Program Event's typed locale-document, projection and Audit seam.
type ProgramEventInterchange struct {
	audit domainaudit.Appender
}

// NewProgramEventInterchange returns the Program Event interchange port; it
// appends locale-content records through audit.
func NewProgramEventInterchange(audit domainaudit.Appender) *ProgramEventInterchange {
	if audit == nil {
		panic("Program Event translation interchange audit is required")
	}
	return &ProgramEventInterchange{audit: audit}
}

type programEventInterchangeTarget struct {
	state     application.TranslationInterchangeTargetState
	localized *contentv1.LocalizedRichTextDocument
}

func (a *ProgramEventInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := a.loadProgramEventInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
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
	current, err := a.loadProgramEventInterchangeTarget(
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
	job := programevent.TranslationJob{
		EntityType: command.EntityType, EntityID: command.EntityID,
		TargetLocale: command.TargetLocale, RequestedByMemberID: memberID,
	}
	if err := programevent.ApplyTranslationInterchangeCandidate(
		ctx,
		programEventJoined(db, store, a.audit),
		job,
		candidate,
		core.EntryWrite{Now: command.Now.UTC()},
		command.ExpectedRevision,
	); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	after, err := a.loadProgramEventInterchangeTarget(
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

func (a *ProgramEventInterchange) loadProgramEventInterchangeTarget(
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
	loaded, err := programevent.LoadInterchangeLocale(ctx, programEventStore(db, store), entityID, plan.SourceLocale, locale)
	if err != nil {
		return programEventInterchangeTarget{}, programEventAnswer(err)
	}
	targets := make(map[string]core.UnitResult)
	if loaded.Exists {
		targets, err = projectBlockInterchangeTargets(plan, loaded.Localized)
		if err != nil {
			return programEventInterchangeTarget{}, err
		}
		addEntityInterchangeTarget(targets, plan, "summary", loaded.Summary)
	}
	state := application.TranslationInterchangeTargetState{Exists: loaded.Exists, Targets: targets}
	if loaded.Exists {
		state.Revision = loaded.DocumentRevision
	}
	return programEventInterchangeTarget{state: state, localized: loaded.Localized}, nil
}

var _ application.TranslationInterchangeDomains = (*ProgramEventInterchange)(nil)
