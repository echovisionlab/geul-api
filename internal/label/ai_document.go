package label

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// AIDocumentState is the Label-owned, authorized compact document projection.
// The adapter converts the generated Rich Text value into DCDP handles; Label
// retains lifecycle, translation-presence and revision authority.
type AIDocumentState struct {
	LabelID           string
	Status            string
	ContentDocumentID uuid.UUID
	Revision          string
	SourceLocale      string
	Locale            string
	LocaleExists      bool
	TargetRevision    *string
	Document          *contentv1.LocalizedRichTextDocument
	ViewerMemberID    string
}

// AIDocumentMutation is a compiler-validated mutation handed back by the DCDP
// adapter. Exactly one of Batch, CreateTranslation or DeleteTranslation is
// present. The Label service repeats all mutable authority and context checks
// under the same transaction as the content-document CAS.
type AIDocumentMutation struct {
	LabelID                string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	ExpectedSource         string
	ExpectedPresence       bool
	ContributorMemberID    uuid.UUID
	Batch                  *contentblock.Batch
	CreateTranslation      bool
	DeleteTranslation      bool
}

type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type labelAIDocumentCompilerError struct{ cause error }

func (e *labelAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *labelAIDocumentCompilerError) Unwrap() error { return e.cause }

type AIDocumentMutationResult struct {
	Content        contentblock.Result
	TargetRevision *string
}

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target"
)

type AIDocumentRevisionConflictError struct {
	Kind                    AIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return fmt.Sprintf("label AI document revision conflict: current document revision is %q", e.CurrentDocumentRevision)
}

// ValidateAIDocumentDependencies lets the AI document registration fail
// startup closed. NewService already required every collaborator, so only a
// missing service fails.
func (s *Service) ValidateAIDocumentDependencies() error {
	if s == nil {
		return errors.New("label AI document service is required")
	}
	return nil
}

// LoadAIDocumentState applies the exact Label view authority before exposing
// draft or published content.
func (s *Service) LoadAIDocumentState(ctx context.Context, labelID, locale string) (AIDocumentState, error) {
	canonicalID, err := canonicalLabelUUID(labelID)
	if err != nil {
		return AIDocumentState{}, domainerr.InvalidField("label_id", "must be a canonical UUID")
	}
	locale, err = normalizeLabelDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.MemberID == "" || principal.IdentityID == "" {
		return AIDocumentState{}, errAuthenticationRequired
	}

	var state AIDocumentState
	err = s.units.ReadLocked(ctx, func(store Store) error {
		root, err := loadLabelAIDocumentRoot(ctx, store, canonicalID.String(), KeyShareLock)
		if err != nil {
			return err
		}
		if !labelAIDocumentLifecycleValid(root.Status) {
			return errors.New("Label has an unsupported lifecycle status")
		}
		if err := requireLabelView(ctx, s.spiceDB, canonicalID.String()); err != nil {
			return err
		}
		state, err = loadAIDocumentStateAfterAuthorization(
			ctx, store, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

// loadLabelAIDocumentRoot reads the Label root with lock; a missing Label
// answers not found and one without a document fails its precondition.
func loadLabelAIDocumentRoot(ctx context.Context, store Store, labelID string, lock RowLock) (DocumentRoot, error) {
	root, err := store.DocumentRoot(ctx, labelID, lock)
	if err != nil {
		if errors.Is(err, ErrLabelNotFound) {
			return DocumentRoot{}, domainerr.ResourceNotFound("label", labelID)
		}
		return DocumentRoot{}, err
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return DocumentRoot{}, domainerr.Precondition("label content document is not initialized")
	}
	return root, nil
}

func labelAIDocumentLifecycleValid(status Status) bool {
	switch status {
	case StatusDraft, StatusPublished:
		return true
	default:
		return false
	}
}

func loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	sourceLocale, err := loadLabelSourceLocale(ctx, store, root.ID, ShareLock)
	if err != nil {
		return AIDocumentState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, *root.DocumentID, sourceLocale)
	if err != nil {
		return AIDocumentState{}, normalizeLabelContentBlockError(err)
	}
	if snapshot.Document.Profile != creativeContentProfile {
		return AIDocumentState{}, errors.New("Label AI document requires the compact content profile")
	}
	localeMetadata, localeExists, err := store.LocaleTitle(ctx, root.ID, locale, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	if locale == sourceLocale && !localeExists {
		return AIDocumentState{}, errors.New("Label source locale metadata is missing")
	}
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeLabelContentBlockError(err)
	}
	if !localeExists && localized.GetLocaleOverlay() != nil && len(localized.GetLocaleOverlay().GetBlocks()) != 0 {
		return AIDocumentState{}, errors.New("Label locale overlay exists without translation metadata")
	}
	state := AIDocumentState{
		LabelID: root.ID, Status: string(root.Status), ContentDocumentID: *root.DocumentID,
		Revision: snapshot.Document.Revision.String(), SourceLocale: sourceLocale,
		Locale: locale, LocaleExists: localeExists, Document: localized,
		ViewerMemberID: viewerMemberID,
	}
	if locale != sourceLocale && localeExists {
		targetRevision, revisionErr := deriveLabelTargetRevision(state.Revision, localeMetadata.UpdatedAt)
		if revisionErr != nil {
			return AIDocumentState{}, revisionErr
		}
		state.TargetRevision = &targetRevision
	}
	return state, nil
}

var errRollbackLabelAIDocumentValidation = errors.New("rollback Label AI document validation")

// ExecuteAIDocumentMutation is Label's exact AI-document mutation boundary.
// The root and active principal are locked, then one Edit decision is made,
// before the adapter can observe state or compile a mutation. Validation
// runs the whole mutation and rolls it back.
func (s *Service) ExecuteAIDocumentMutation(
	ctx context.Context,
	labelID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Label AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	canonicalID, err := canonicalLabelUUID(labelID)
	if err != nil {
		return AIDocumentMutationResult{}, domainerr.InvalidField("label_id", "must be a canonical UUID")
	}
	locale, err = normalizeLabelDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root DocumentRoot
	var output labelAIDocumentOutcome
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[labelAIDocumentOutcome, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			if root, err = loadLabelAIDocumentRoot(ctx, store, canonicalID.String(), UpdateLock); err != nil {
				return err
			}
			if !labelAIDocumentLifecycleValid(root.Status) {
				return errors.New("Label has an unsupported lifecycle status")
			}
			return nil
		},
		Access: labelEntity.LockedAccess(s.spiceDB, canonicalID.String(), policyv1.Label.Edit),
		Change: func(ctx context.Context, store Store) (labelAIDocumentOutcome, error) {
			var err error
			output, err = applyAIDocumentMutation(ctx, store, root, locale, compiler)
			return output, err
		},
		Audit: func(outcome labelAIDocumentOutcome) mutation.Audit { return outcome.audit() },
		Outbox: func(ctx context.Context, store Store, outcome labelAIDocumentOutcome) error {
			if mode == AIDocumentExecutionValidate {
				return errRollbackLabelAIDocumentValidation
			}
			if !outcome.result.Content.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, outcome.contentUpdate())
		},
	})
	if errors.Is(err, errRollbackLabelAIDocumentValidation) {
		return output.result, nil
	}
	if err != nil {
		var compilerErr *labelAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		return AIDocumentMutationResult{}, err
	}
	return output.result, nil
}

// labelAIDocumentOutcome is an applied AI-document mutation and the Member
// it is attributed to.
type labelAIDocumentOutcome struct {
	input    AIDocumentMutation
	memberID string
	result   AIDocumentMutationResult
}

// applyAIDocumentMutation compiles the mutation against the locked state and
// applies it as the principal's Member.
func applyAIDocumentMutation(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	compiler AIDocumentMutationCompiler,
) (labelAIDocumentOutcome, error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return labelAIDocumentOutcome{}, domainerr.ResourceNotFound("label", root.ID)
	}
	memberID, err := canonicalLabelUUID(principal.MemberID.String())
	if err != nil {
		return labelAIDocumentOutcome{}, domainerr.ResourceNotFound("label", root.ID).WithCause(err)
	}
	state, err := loadAIDocumentStateAfterAuthorization(ctx, store, root, locale, memberID.String())
	if err != nil {
		return labelAIDocumentOutcome{}, err
	}
	input, err := compiler(state)
	if err != nil {
		return labelAIDocumentOutcome{}, &labelAIDocumentCompilerError{cause: err}
	}
	expected, err := validateCompiledLabelAIDocumentMutation(state, input, memberID)
	if err != nil {
		return labelAIDocumentOutcome{}, err
	}
	if err := store.RequireDocumentContributors(ctx, []string{memberID.String()}); err != nil {
		return labelAIDocumentOutcome{}, err
	}
	fence := labelAuthorizedAIDocumentFence(root, input.ExpectedSource)
	result, err := applyAIDocumentMode(ctx, store, input, expected, memberID, *root.DocumentID, fence)
	if err != nil {
		return labelAIDocumentOutcome{}, mapLabelAIDocumentMutationError(err, input.ExpectedRevision)
	}
	if result.Content.TranslationSourceChanged {
		if _, err := store.RequestOG(ctx, root.ID, "label_ai_document_saved"); err != nil {
			return labelAIDocumentOutcome{}, err
		}
	}
	return labelAIDocumentOutcome{input: input, memberID: memberID.String(), result: result}, nil
}

// audit attributes a changed locale to the Member the mutation ran as: a
// target locale is created, updated or deleted, the source is updated.
func (o labelAIDocumentOutcome) audit() mutation.Audit {
	if !o.result.Content.Changed {
		return mutation.Unchanged()
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if o.input.Locale != o.input.ExpectedSource && !o.input.ExpectedPresence {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if o.input.Locale != o.input.ExpectedSource && o.input.DeleteTranslation {
		operation = sharedtelemetry.AuditItemOperationDeleted
	}
	return labelMemberLocaleAudit(o.memberID, o.input.LabelID, o.input.Locale, operation)
}

func (o labelAIDocumentOutcome) contentUpdate() ContentUpdate {
	localeExists := o.input.Locale == o.input.ExpectedSource || o.result.TargetRevision != nil
	return ContentUpdate{LabelID: o.input.LabelID, Kind: LocaleUpdate, Locale: LocaleChange{
		Fields:               []string{"content"},
		DocumentRevision:     o.result.Content.DocumentRevision.String(),
		Contributors:         []string{o.input.ContributorMemberID.String()},
		Source:               SourceAI,
		Locale:               o.input.Locale,
		LocaleDeleted:        !localeExists,
		TargetRevision:       o.result.TargetRevision,
		DocumentStateChanged: o.input.Locale == o.input.ExpectedSource,
	}}
}

func validateLabelAIDocumentMutation(input AIDocumentMutation) (uuid.UUID, uuid.UUID, error) {
	labelID, err := canonicalLabelUUID(input.LabelID)
	if err != nil {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("label_id", "must be a canonical UUID")
	}
	expected, err := canonicalLabelUUID(input.ExpectedRevision)
	if err != nil {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("expected_revision", "must be a canonical UUID")
	}
	locale, localeErr := normalizeLabelDocumentLocale(input.Locale)
	if localeErr != nil || locale != input.Locale {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	source, sourceErr := normalizeLabelDocumentLocale(input.ExpectedSource)
	if sourceErr != nil || source != input.ExpectedSource {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("expected_source", "must be an exact canonical locale")
	}
	modeCount := 0
	if input.Batch != nil {
		modeCount++
	}
	if input.CreateTranslation {
		modeCount++
	}
	if input.DeleteTranslation {
		modeCount++
	}
	if modeCount != 1 {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("operation", "exactly one Label AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("locale", "only a missing non-source Label translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("locale", "only an existing non-source Label translation can be deleted")
	}
	return labelID, expected, nil
}

func validateCompiledLabelAIDocumentMutation(
	state AIDocumentState,
	input AIDocumentMutation,
	memberID uuid.UUID,
) (uuid.UUID, error) {
	_, expected, err := validateLabelAIDocumentMutation(input)
	if err != nil {
		return uuid.Nil, err
	}
	if input.LabelID != state.LabelID || input.Locale != state.Locale ||
		input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists ||
		input.ContributorMemberID != memberID || state.ViewerMemberID != memberID.String() {
		return uuid.Nil, domainerr.InvalidField(
			"mutation",
			"compiled Label identity, locale, contributor, source observation, and locale presence must match the locked state",
		)
	}
	if input.ExpectedRevision != state.Revision {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.Revision,
		}
	}
	if !optional.Equal(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: state.Revision,
			CurrentTargetRevision:   optional.Clone(state.TargetRevision),
		}
	}
	if input.Batch != nil {
		if input.Batch.DocumentID != state.ContentDocumentID || input.Batch.ExpectedRevision != expected ||
			len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID {
			return uuid.Nil, domainerr.InvalidField(
				"mutation",
				"compiled Label document, revision, and attribution must match the locked state",
			)
		}
	}
	return expected, nil
}

func applyAIDocumentMode(
	ctx context.Context,
	store Store,
	input AIDocumentMutation,
	expected uuid.UUID,
	memberID uuid.UUID,
	documentID uuid.UUID,
	fence ContentFence,
) (AIDocumentMutationResult, error) {
	if input.Batch != nil {
		batch := *input.Batch
		if batch.DocumentID != documentID {
			return AIDocumentMutationResult{}, domainerr.InvalidField("document", "Label content document does not match")
		}
		batch.ExpectedRevision = expected
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
		if input.Locale != input.ExpectedSource {
			createTarget := !input.ExpectedPresence
			target, err := applyLabelTargetLocaleMutation(ctx, store, labelTargetLocaleMutationInput{
				LabelID: input.LabelID, Locale: input.Locale,
				ExpectedDocumentRevision: expected, ExpectedTargetRevision: input.ExpectedTargetRevision,
				Batch: batch, Policy: targetlocale.Policy{AllowCreate: createTarget, SeedSource: createTarget}, Fence: fence,
			})
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
			return AIDocumentMutationResult{Content: target.Content, TargetRevision: &target.TargetRevision}, nil
		}
		if input.ExpectedTargetRevision != nil {
			return AIDocumentMutationResult{}, domainerr.InvalidField("expected_target_revision", "source Label mutation cannot carry a target revision")
		}
		result, err := store.ApplyContentBatch(ctx, batch, fence)
		return AIDocumentMutationResult{Content: result}, err
	}
	if input.CreateTranslation {
		target, err := applyLabelTargetLocaleMutation(ctx, store, labelTargetLocaleMutationInput{
			LabelID: input.LabelID, Locale: input.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: input.ExpectedTargetRevision,
			Batch:  contentblock.Batch{DocumentID: documentID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{memberID}},
			Policy: targetlocale.Policy{AllowCreate: true, SeedSource: true}, Fence: fence,
		})
		if err != nil {
			return AIDocumentMutationResult{}, err
		}
		return AIDocumentMutationResult{Content: target.Content, TargetRevision: &target.TargetRevision}, nil
	}
	result, err := deleteLabelTargetLocale(ctx, store, input.LabelID, input.Locale, expected, input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence)
	return AIDocumentMutationResult{Content: result}, err
}

func labelAuthorizedAIDocumentFence(
	root DocumentRoot,
	expectedSource string,
) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, domainerr.Precondition("label content document changed; reload before saving")
		}
		source, err := lockLabelTranslationSourceContext(ctx, store, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if source.SourceLocale != expectedSource {
			current, err := store.CurrentContentRevision(ctx, documentID)
			if err != nil {
				return contentblock.DomainContext{}, err
			}
			return contentblock.DomainContext{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: current.String(),
			}
		}
		return source, nil
	}
}

func mapLabelAIDocumentMutationError(err error, currentDocumentRevision string) error {
	var conflict *AIDocumentRevisionConflictError
	if errors.As(err, &conflict) {
		return conflict
	}
	var stale *contentblock.StaleRevisionError
	if errors.As(err, &stale) {
		return &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String()}
	}
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		var currentTargetRevision *string
		if targetConflict.CurrentExists {
			currentTargetRevision = optional.Clone(&targetConflict.CurrentRevision)
		}
		return &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: currentDocumentRevision,
			CurrentTargetRevision:   currentTargetRevision,
		}
	}
	return normalizeLabelContentBlockError(err)
}
