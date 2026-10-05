package translationadapter

import (
	"context"

	"gorm.io/gorm"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ArtistInterchange adapts XLIFF target projection and exact sparse mutation
// to Artist's existing typed Content Block transaction seam.
type ArtistInterchange struct {
	auditWriter  domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewArtistInterchange(
	auditWriter domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *ArtistInterchange {
	if auditWriter == nil || auditBuilder == nil {
		panic("Artist translation interchange Audit dependencies are required")
	}
	return &ArtistInterchange{auditWriter: auditWriter, auditBuilder: auditBuilder}
}

func (*ArtistInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadArtistInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
	return loaded.state, err
}

func (a *ArtistInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindArtist)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadArtistInterchangeTarget(
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
	if err := artistdomain.ApplyTypedTranslationInterchangeCandidateWithDB(
		ctx, db, store, job, candidate, core.EntryWrite{Now: command.Now.UTC()}, command.ExpectedRevision,
	); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	after, err := loadArtistInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	changed := after.state.Revision != current.state.Revision
	if changed {
		if err := appendLocaleContentInterchangeAudit(
			ctx, db, a.auditWriter, a.auditBuilder, sharedtelemetry.AuditArtistUpdated,
			memberID, command.EntityID, command.TargetLocale, current.state.Exists,
		); err != nil {
			return application.TranslationInterchangeApplyResult{}, err
		}
	}
	return application.TranslationInterchangeApplyResult{
		Revision: after.state.Revision, Changed: changed,
		AffectedUnitHandles: append([]string(nil), command.UnitHandles...),
	}, nil
}

func loadArtistInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (creativeInterchangeTarget, error) {
	if err := validateCreativeInterchangeLoad(db, store, entityType, entityID, locale, plan, string(core.KindArtist)); err != nil {
		return creativeInterchangeTarget{}, err
	}
	target, err := artistdomain.LoadTypedTranslationInterchangeTarget(ctx, db, store, entityID, locale)
	if err != nil {
		return creativeInterchangeTarget{}, err
	}
	return projectCreativeInterchangeTarget(plan, target.Exists, target.Revision, target.Document)
}

var _ application.TranslationInterchangeDomains = (*ArtistInterchange)(nil)
