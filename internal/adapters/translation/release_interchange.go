package translationadapter

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
)

// ReleaseInterchange keeps Release-owned credit notes and typed Content Blocks
// in one XLIFF target mutation and one caller-owned transaction.
type ReleaseInterchange struct {
	auditWriter  domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewReleaseInterchange(
	auditWriter domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *ReleaseInterchange {
	if auditWriter == nil || auditBuilder == nil {
		panic("Release translation interchange Audit dependencies are required")
	}
	return &ReleaseInterchange{auditWriter: auditWriter, auditBuilder: auditBuilder}
}

type releaseInterchangeTarget struct {
	creativeInterchangeTarget
	creditNotes map[string]string
}

func (*ReleaseInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadReleaseInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
	return loaded.state, err
}

func (a *ReleaseInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindRelease)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadReleaseInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if err := requireTranslationInterchangeRevision(current.state, command.ExpectedRevision); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	candidate, err := buildReleaseInterchangeCandidate(command, current)
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
	if err := releasedomain.ApplyTypedTranslationInterchangeCandidateWithDB(
		ctx,
		db,
		store,
		job,
		candidate,
		core.EntryWrite{Now: command.Now.UTC()},
		command.ExpectedRevision,
		command.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
	); err != nil {
		var conflict *core.TargetRevisionConflict
		if errors.As(err, &conflict) {
			return application.TranslationInterchangeApplyResult{}, connect.NewError(connect.CodeAborted, errors.New("translation target revision changed; reload before importing"))
		}
		return application.TranslationInterchangeApplyResult{}, err
	}
	after, err := loadReleaseInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	changed := after.state.Revision != current.state.Revision
	if changed {
		if err := appendLocaleContentInterchangeAudit(
			ctx, db, a.auditWriter, a.auditBuilder, sharedtelemetry.AuditReleaseUpdated,
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

func loadReleaseInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (releaseInterchangeTarget, error) {
	if err := validateCreativeInterchangeLoad(db, store, entityType, entityID, locale, plan, string(core.KindRelease)); err != nil {
		return releaseInterchangeTarget{}, err
	}
	target, err := releasedomain.LoadTypedTranslationInterchangeTarget(ctx, db, store, entityID, locale)
	if err != nil {
		return releaseInterchangeTarget{}, err
	}
	creative, err := projectCreativeInterchangeTarget(plan, target.Exists, target.Revision, target.Document)
	if err != nil {
		return releaseInterchangeTarget{}, err
	}
	if target.Exists {
		addReleaseCreditNoteInterchangeTargets(creative.state.Targets, plan, target.CreditNotes)
	}
	return releaseInterchangeTarget{creativeInterchangeTarget: creative, creditNotes: target.CreditNotes}, nil
}

func addReleaseCreditNoteInterchangeTargets(
	targets map[string]core.UnitResult,
	plan *core.ExtractionPlan,
	notes map[string]string,
) {
	for _, unit := range plan.Units {
		if !strings.HasPrefix(unit.UnitID, "credit-note:") {
			continue
		}
		creditID := strings.TrimPrefix(unit.UnitID, "credit-note:")
		value, exists := notes[creditID]
		if !exists {
			continue
		}
		targets[unit.UnitID] = core.UnitResult{
			UnitID: unit.UnitID, TranslatedText: value,
			OriginalData: cloneOriginalData(unit.OriginalData),
			TargetInline: cloneXLIFFInline(unit.SourceInline),
		}
	}
}

func buildReleaseInterchangeCandidate(
	command application.TranslationInterchangeApply,
	current releaseInterchangeTarget,
) (*core.Candidate, error) {
	candidate, err := buildCreativeInterchangeCandidate(command, current.localized)
	if err != nil {
		return nil, err
	}
	if command.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
		ensureReleasePatchBlocks(candidate.ContentBlockLocaleOverlay, current.localized, command.Source.ContentBlockDocument)
	}
	candidate.ReleaseCreditNotes = make(map[string]string)
	if command.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
		for creditID, value := range current.creditNotes {
			candidate.ReleaseCreditNotes[creditID] = value
		}
	}
	for handle, result := range command.Targets {
		if !strings.HasPrefix(handle, "credit-note:") {
			continue
		}
		if _, known := releaseInterchangePlanUnits(command.Plan)[handle]; !known {
			continue
		}
		candidate.ReleaseCreditNotes[strings.TrimPrefix(handle, "credit-note:")] = strings.TrimSpace(result.TranslatedText)
	}
	return candidate, nil
}

func releaseInterchangePlanUnits(plan *core.ExtractionPlan) map[string]struct{} {
	units := make(map[string]struct{}, len(plan.Units))
	for _, unit := range plan.Units {
		units[unit.UnitID] = struct{}{}
	}
	return units
}

func ensureReleasePatchBlocks(
	overlay *contentv1.RichTextLocaleOverlay,
	current *contentv1.LocalizedRichTextDocument,
	source *contentv1.LocalizedRichTextDocument,
) {
	present := richTextLocaleBlocks(overlay)
	sourceBlocks := richTextBaseBlocks(source.GetBase())
	for _, block := range current.GetLocaleOverlay().GetBlocks() {
		blockID := strings.TrimSpace(block.GetBlockId())
		if _, currentSource := sourceBlocks[blockID]; !currentSource {
			continue
		}
		if _, patched := present[blockID]; patched {
			continue
		}
		overlay.Blocks = append(overlay.Blocks, proto.Clone(block).(*contentv1.RichTextBlockLocale))
	}
}

var _ application.TranslationInterchangeDomains = (*ReleaseInterchange)(nil)
