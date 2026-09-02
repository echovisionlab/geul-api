package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/legal"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type legalTranslationInterchangeDomain interface {
	LoadTranslationInterchangeAIDocumentWithDB(
		context.Context,
		*gorm.DB,
		string,
		string,
		string,
	) (legal.AIDocument, error)
	ExecuteTranslationInterchangeMutationWithDB(
		context.Context,
		*gorm.DB,
		string,
		string,
		string,
		legal.AIDocumentMutationCompiler,
	) (legal.AIDocumentMutationResult, error)
}

// LegalInterchange maps PrivacyHistory and TermsHistory XLIFF values to the
// Legal-owned exact mutation seam. The shared Rich Text interchange codec is
// used here so owning-domain code never depends on this adapter package.
type LegalInterchange struct {
	domain legalTranslationInterchangeDomain
}

func NewLegalInterchange(domain legalTranslationInterchangeDomain) *LegalInterchange {
	if domain == nil {
		panic("Legal translation interchange domain is required")
	}
	return &LegalInterchange{domain: domain}
}

type legalInterchangeTarget struct {
	state           application.TranslationInterchangeTargetState
	document        *contentv1.LocalizedRichTextDocument
	memberID        string
	documentID      uuid.UUID
	contentRevision string
}

func (a *LegalInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if a == nil || a.domain == nil || db == nil || store == nil || plan == nil {
		return application.TranslationInterchangeTargetState{}, errs.DependencyUnavailable(
			"Legal translation interchange",
		)
	}
	if err := validateLegalInterchangeIdentity(entityType, entityID, locale, plan); err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	raw, err := a.domain.LoadTranslationInterchangeAIDocumentWithDB(
		ctx, db, entityType, entityID, locale,
	)
	if err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	target, err := projectLegalInterchangeTarget(raw, plan)
	return target.state, err
}

func (a *LegalInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if a == nil || a.domain == nil || db == nil || store == nil {
		return application.TranslationInterchangeApplyResult{}, errs.DependencyUnavailable(
			"Legal translation interchange",
		)
	}
	if err := validateLegalInterchangeApply(command); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}

	var before application.TranslationInterchangeTargetState
	domainResult, err := a.domain.ExecuteTranslationInterchangeMutationWithDB(
		ctx,
		db,
		command.EntityType,
		command.EntityID,
		command.TargetLocale,
		func(raw legal.AIDocument) (legal.AIDocumentMutation, error) {
			current, projectErr := projectLegalInterchangeTarget(raw, command.Plan)
			if projectErr != nil {
				return legal.AIDocumentMutation{}, projectErr
			}
			before = current.state
			if command.Source.ContentDocumentRevision != raw.Revision {
				return legal.AIDocumentMutation{}, connect.NewError(
					connect.CodeAborted,
					errors.New("legal source document changed; rebuild the XLIFF manifest"),
				)
			}
			if err := core.ValidateExpectedTargetRevision(
				command.ExpectedRevision, current.state.Revision, current.state.Exists,
			); err != nil {
				var conflict *core.TargetRevisionConflict
				if errors.As(err, &conflict) {
					return legal.AIDocumentMutation{}, connect.NewError(connect.CodeAborted, err)
				}
				return legal.AIDocumentMutation{}, errs.Internal(err)
			}
			return buildLegalInterchangeMutation(command, current)
		},
	)
	if err != nil {
		var revisionConflict *legal.AIDocumentRevisionConflict
		if errors.As(err, &revisionConflict) {
			return application.TranslationInterchangeApplyResult{}, connect.NewError(
				connect.CodeAborted,
				errors.New("legal content changed; reload before importing"),
			)
		}
		return application.TranslationInterchangeApplyResult{}, err
	}

	accepted, err := a.domain.LoadTranslationInterchangeAIDocumentWithDB(
		ctx, db, command.EntityType, command.EntityID, command.TargetLocale,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if domainResult.Revision != accepted.Revision {
		return application.TranslationInterchangeApplyResult{}, errs.InternalMsg(
			"Legal translation interchange returned an incoherent content revision",
		)
	}
	after, err := projectLegalInterchangeTarget(accepted, command.Plan)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if !after.state.Exists || strings.TrimSpace(after.state.Revision) == "" {
		return application.TranslationInterchangeApplyResult{}, errs.InternalMsg(
			"Legal translation interchange did not create its target locale",
		)
	}
	if domainResult.TargetRevision == nil || *domainResult.TargetRevision != after.state.Revision {
		return application.TranslationInterchangeApplyResult{}, errs.InternalMsg(
			"Legal translation interchange returned an incoherent target revision",
		)
	}
	affected := changedLegalInterchangeHandles(before.Targets, command.Targets, command.UnitHandles)
	if !domainResult.Changed {
		affected = nil
	}
	return application.TranslationInterchangeApplyResult{
		Revision:            after.state.Revision,
		Changed:             domainResult.Changed,
		AffectedUnitHandles: affected,
	}, nil
}

func validateLegalInterchangeIdentity(
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) error {
	if entityType != string(core.KindPrivacy) && entityType != string(core.KindTerms) {
		return errs.InvalidArgument("target", "Legal translation interchange requires privacy or terms")
	}
	if plan == nil || plan.EntityType != entityType || plan.EntityID != entityID ||
		plan.TargetLocale != locale || plan.SourceLocale == locale {
		return errs.InvalidArgument("target", "Legal translation interchange identity does not match the route")
	}
	return nil
}

func validateLegalInterchangeApply(command application.TranslationInterchangeApply) error {
	if err := validateBlockInterchangeApply(command, command.EntityType); err != nil {
		return err
	}
	if err := validateLegalInterchangeIdentity(
		command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	); err != nil {
		return err
	}
	if command.Source.ContentBlockDocument.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY ||
		command.Source.ContentBlockDocument.GetLocale() != command.SourceLocale ||
		strings.TrimSpace(command.Source.ContentDocumentRevision) == "" {
		return errs.InvalidArgument("file_id", "Legal XLIFF source document is invalid")
	}
	known := make(map[string]struct{}, len(command.Plan.Units))
	for _, unit := range command.Plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	switch command.Mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		if len(command.UnitHandles) == 0 {
			return errs.InvalidArgument("file_id", "Legal XLIFF PATCH requires an explicit stable unit selection")
		}
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		if len(command.UnitHandles) != len(known) {
			return errs.InvalidArgument("file_id", "Legal XLIFF REPLACE requires the complete current stable unit manifest")
		}
	}
	if len(command.Targets) != len(command.UnitHandles) {
		return errs.InvalidArgument("file_id", "Legal XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(command.UnitHandles))
	for _, handle := range command.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return errs.InvalidArgument("file_id", "Legal XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return errs.InvalidArgument("file_id", "Legal XLIFF contains an unknown stable unit")
		}
		target, ok := command.Targets[handle]
		if !ok || target.UnitID != handle {
			return errs.InvalidArgument("file_id", "Legal XLIFF target identity does not match its stable unit")
		}
	}
	for handle := range command.Targets {
		if _, ok := seen[handle]; !ok {
			return errs.InvalidArgument("file_id", "Legal XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func projectLegalInterchangeTarget(
	raw legal.AIDocument,
	plan *core.ExtractionPlan,
) (legalInterchangeTarget, error) {
	if plan == nil || raw.EntityType != plan.EntityType || raw.EntityID != plan.EntityID ||
		raw.SourceLocale != plan.SourceLocale || raw.Locale != plan.TargetLocale ||
		raw.SourceLocale == raw.Locale || raw.DocumentID == uuid.Nil || strings.TrimSpace(raw.Revision) == "" {
		return legalInterchangeTarget{}, errs.InvalidArgument(
			"target", "Legal translation interchange state does not match the current plan",
		)
	}
	document, err := localizedLegalInterchangeDocument(raw)
	if err != nil {
		return legalInterchangeTarget{}, err
	}
	target := legalInterchangeTarget{
		state: application.TranslationInterchangeTargetState{
			Exists:  raw.LocaleExists,
			Targets: make(map[string]core.UnitResult),
		},
		document: document, memberID: raw.ViewerMemberID,
		documentID: raw.DocumentID, contentRevision: raw.Revision,
	}
	if !raw.LocaleExists {
		if raw.LocaleUpdatedAt != nil || raw.Title != nil || len(document.GetLocaleOverlay().GetBlocks()) != 0 {
			return legalInterchangeTarget{}, errs.InternalMsg(
				"Legal target values exist without an owning translation locale",
			)
		}
		return target, nil
	}
	target.state.Targets, err = ProjectRichTextInterchangeTargets(plan, document)
	if err != nil {
		return legalInterchangeTarget{}, errs.InvalidArgument("target", err.Error())
	}
	if raw.Title != nil && interchangePlanHasUnit(plan, "entity:title") {
		target.state.Targets["entity:title"] = core.UnitResult{
			UnitID: "entity:title", TranslatedText: *raw.Title,
		}
	}
	target.state.Revision, err = core.DeriveTargetRevision(core.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: raw.Revision, LocaleUpdatedAt: raw.LocaleUpdatedAt,
	})
	if err != nil {
		return legalInterchangeTarget{}, errs.Internal(err)
	}
	return target, nil
}

func localizedLegalInterchangeDocument(raw legal.AIDocument) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentv1.MaterializeRichTextDocumentStorage(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		raw.SourceLocale,
		raw.Rows,
	)
	if err != nil {
		return nil, fmt.Errorf("materialize Legal interchange Rich Text document: %w", err)
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY {
		return nil, errors.New("legal interchange content document must use the policy profile")
	}
	overlay := &contentv1.RichTextLocaleOverlay{Locale: raw.Locale}
	for _, candidate := range document.GetLocaleOverlays() {
		if candidate.GetLocale() == raw.Locale {
			overlay = proto.Clone(candidate).(*contentv1.RichTextLocaleOverlay)
			break
		}
	}
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		Locale:                  raw.Locale,
		Base:                    proto.Clone(document.GetBase()).(*contentv1.RichTextBlockGraph),
		LocaleOverlay:           overlay,
	}, nil
}

func buildLegalInterchangeMutation(
	command application.TranslationInterchangeApply,
	current legalInterchangeTarget,
) (legal.AIDocumentMutation, error) {
	candidate, err := buildCreativeInterchangeCandidate(command, current.document)
	if err != nil {
		return legal.AIDocumentMutation{}, errs.InvalidArgument("file_id", err.Error())
	}
	mutation := legal.AIDocumentMutation{
		EntityType: command.EntityType, EntityID: command.EntityID, Locale: command.TargetLocale,
		ExpectedRevision: current.contentRevision, ContributorMemberID: current.memberID,
		ExpectedTargetRevision:         command.ExpectedRevision,
		AuthoritativeTargetReplacement: command.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
	}
	if !current.state.Exists {
		mutation.Translation = legal.AITranslationCreate
	}
	if title, ok := command.Targets["entity:title"]; ok {
		value := title.TranslatedText
		mutation.SetTitle = true
		mutation.Title = &value
	}
	localeMutations := candidate.RichTextLocaleMutations()
	if len(localeMutations) == 0 {
		return mutation, nil
	}
	batch, err := contentblock.BatchFromRichTextProto(
		current.documentID,
		&contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: command.Source.ContentBlockDocument.GetBlockCatalogFingerprint(),
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
			ExpectedRevision:        current.contentRevision,
			ContributorMemberIds:    []string{current.memberID},
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: command.TargetLocale, Mutations: localeMutations,
			}},
		},
	)
	if err != nil {
		return legal.AIDocumentMutation{}, errs.InvalidArgument("file_id", err.Error())
	}
	if err := validateLegalInterchangeTargetOnlyBatch(batch, command.TargetLocale); err != nil {
		return legal.AIDocumentMutation{}, err
	}
	mutation.Content = &batch
	return mutation, nil
}

func validateLegalInterchangeTargetOnlyBatch(batch contentblock.Batch, targetLocale string) error {
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 ||
		len(batch.LocaleGroups) != 1 || batch.LocaleGroups[0].Locale != targetLocale {
		return errs.InternalMsg("Legal XLIFF mutation must change only the requested target locale")
	}
	return nil
}

func changedLegalInterchangeHandles(
	current map[string]core.UnitResult,
	incoming map[string]core.UnitResult,
	handles []string,
) []string {
	affected := make([]string, 0, len(handles))
	for _, handle := range handles {
		if !reflect.DeepEqual(current[handle], incoming[handle]) {
			affected = append(affected, handle)
		}
	}
	sort.Strings(affected)
	return affected
}

var _ application.TranslationInterchangeDomains = (*LegalInterchange)(nil)
