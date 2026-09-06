package label

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// ValidateAIDocumentDependencies lets the composition adapter fail startup
// closed instead of discovering a missing Content Block or runtime authority
// on the first AI request.
func (s *LabelService) ValidateAIDocumentDependencies() error {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil || s.translation == nil || s.runtime == nil || s.auditWriter == nil {
		return errors.New("label AI document requires db, SpiceDB, Content Block, Translation, Runtime, and Audit dependencies")
	}
	return nil
}

// LoadAIDocumentState applies the exact Label view authority before exposing
// draft or published content.
func (s *LabelService) LoadAIDocumentState(ctx context.Context, labelID, locale string) (AIDocumentState, error) {
	if err := s.ValidateAIDocumentDependencies(); err != nil {
		return AIDocumentState{}, errs.Internal(err)
	}
	canonicalID, err := canonicalLabelUUID(labelID)
	if err != nil {
		return AIDocumentState{}, errs.InvalidArgument("label_id", "must be a canonical UUID")
	}
	locale, err = normalizeLabelDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.MemberID == "" || principal.IdentityID == "" {
		return AIDocumentState{}, errs.AuthenticationRequired()
	}

	var state AIDocumentState
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadLabelAIDocumentRoot(ctx, tx, canonicalID.String(), "KEY SHARE")
		if err != nil {
			return err
		}
		if !labelAIDocumentLifecycleValid(root.Status) {
			return errs.InternalMsg("Label has an unsupported lifecycle status")
		}
		if err := requireLabelView(ctx, s.spiceDB, canonicalID.String()); err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, principal.MemberID.String(),
		)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return state, err
}

type labelAIDocumentRoot struct {
	ID         string     `gorm:"column:id"`
	Status     string     `gorm:"column:status"`
	DocumentID *uuid.UUID `gorm:"column:content_document_id"`
}

func loadLabelAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	labelID string,
	lock string,
) (labelAIDocumentRoot, error) {
	query := tx.WithContext(ctx).
		Table("label").
		Select("id::text AS id", "status", "content_document_id").
		Where("id = ?::uuid", labelID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root labelAIDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return labelAIDocumentRoot{}, errs.NotFound("label", labelID)
		}
		return labelAIDocumentRoot{}, errs.Internal(err)
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return labelAIDocumentRoot{}, errs.FailedPrecondition("label content document is not initialized")
	}
	return root, nil
}

func labelAIDocumentLifecycleValid(status string) bool {
	switch status {
	case managev1.LabelStatus_LABEL_STATUS_DRAFT.String(), managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String():
		return true
	default:
		return false
	}
}

func (s *LabelService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root labelAIDocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	source, err := loadLabelSourceAuthority(ctx, tx, root.ID, "SHARE")
	if err != nil {
		return AIDocumentState{}, err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, *root.DocumentID, source.SourceLocale)
	if err != nil {
		return AIDocumentState{}, normalizeLabelContentBlockError(err)
	}
	if snapshot.Document.Profile != creativeContentProfile {
		return AIDocumentState{}, errs.InternalMsg("Label AI document requires the compact content profile")
	}
	localeMetadata, localeExists, err := loadOptionalLabelLocaleMetadata(ctx, tx, root.ID, locale, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	if locale == source.SourceLocale && !localeExists {
		return AIDocumentState{}, errs.InternalMsg("Label source locale metadata is missing")
	}
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeLabelContentBlockError(err)
	}
	if !localeExists && localized.GetLocaleOverlay() != nil && len(localized.GetLocaleOverlay().GetBlocks()) != 0 {
		return AIDocumentState{}, errs.InternalMsg("Label locale overlay exists without translation metadata")
	}
	state := AIDocumentState{
		LabelID: root.ID, Status: root.Status, ContentDocumentID: *root.DocumentID,
		Revision: snapshot.Document.Revision.String(), SourceLocale: source.SourceLocale,
		Locale: locale, LocaleExists: localeExists, Document: localized,
		ViewerMemberID: viewerMemberID,
	}
	if locale != source.SourceLocale && localeExists {
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
// before the adapter can observe state or compile a mutation.
func (s *LabelService) ExecuteAIDocumentMutation(
	ctx context.Context,
	labelID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if err := s.ValidateAIDocumentDependencies(); err != nil {
		return AIDocumentMutationResult{}, errs.Internal(err)
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Label AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	canonicalID, err := canonicalLabelUUID(labelID)
	if err != nil {
		return AIDocumentMutationResult{}, errs.InvalidArgument("label_id", "must be a canonical UUID")
	}
	locale, err = normalizeLabelDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var output AIDocumentMutationResult
	var input AIDocumentMutation
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadLabelAIDocumentRoot(ctx, tx, canonicalID.String(), "UPDATE")
		if err != nil {
			return err
		}
		if !labelAIDocumentLifecycleValid(root.Status) {
			return errs.InternalMsg("Label has an unsupported lifecycle status")
		}
		if err := requireLockedLabelPermission(
			ctx, tx, s.spiceDB, root.ID, policyv1.Label.Edit,
		); err != nil {
			return err
		}
		principal := auth.GetUser(ctx)
		if principal == nil {
			return errs.NotFound("label", root.ID)
		}
		memberID, err := canonicalLabelUUID(principal.MemberID.String())
		if err != nil {
			return errs.NotFound("label", root.ID)
		}
		state, err := s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, memberID.String(),
		)
		if err != nil {
			return err
		}
		input, err = compiler(state)
		if err != nil {
			return &labelAIDocumentCompilerError{cause: err}
		}
		expected, err := validateCompiledLabelAIDocumentMutation(state, input, memberID)
		if err != nil {
			return err
		}
		if err := s.translation.RequireDocumentContributors(
			ctx, tx, []string{memberID.String()},
		); err != nil {
			return err
		}
		fence := labelAuthorizedAIDocumentFence(root, input.ExpectedSource)
		result, err := s.applyAIDocumentMode(
			ctx, tx, input, expected, memberID, *root.DocumentID, fence,
		)
		if err != nil {
			return mapLabelAIDocumentMutationError(err, input.ExpectedRevision)
		}
		if result.Content.TranslationSourceChanged {
			if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, root.ID, "label_ai_document_saved"); err != nil {
				return err
			}
		}
		if result.Content.Changed && input.Locale != input.ExpectedSource {
			operation := sharedtelemetry.AuditItemOperationUpdated
			if !input.ExpectedPresence {
				operation = sharedtelemetry.AuditItemOperationCreated
			}
			if input.DeleteTranslation {
				operation = sharedtelemetry.AuditItemOperationDeleted
			}
			if err := appendLabelMemberTargetLocaleAudit(
				ctx, tx, s.auditWriter, memberID.String(), root.ID, input.Locale, operation,
			); err != nil {
				return err
			}
		} else if result.Content.Changed {
			if err := appendLabelMemberLocaleAudit(
				ctx, tx, s.auditWriter, memberID.String(), root.ID, input.Locale,
				sharedtelemetry.AuditItemOperationUpdated,
			); err != nil {
				return err
			}
		}
		output = result
		if mode == AIDocumentExecutionValidate {
			return errRollbackLabelAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackLabelAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *labelAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		return AIDocumentMutationResult{}, err
	}
	if mode == AIDocumentExecutionApply && output.Content.Changed {
		localeExists := input.Locale == input.ExpectedSource || output.TargetRevision != nil
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelContentUpdatedEvent(
			input.LabelID,
			[]string{"content"},
			output.Content.DocumentRevision.String(),
			[]string{input.ContributorMemberID.String()},
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
			input.Locale,
			localeExists,
			output.TargetRevision,
			input.Locale == input.ExpectedSource,
		))
	}
	return output, nil
}

func validateLabelAIDocumentMutation(input AIDocumentMutation) (uuid.UUID, uuid.UUID, error) {
	labelID, err := canonicalLabelUUID(input.LabelID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("label_id", "must be a canonical UUID")
	}
	expected, err := canonicalLabelUUID(input.ExpectedRevision)
	if err != nil {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	locale, localeErr := normalizeLabelDocumentLocale(input.Locale)
	if localeErr != nil || locale != input.Locale {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	source, sourceErr := normalizeLabelDocumentLocale(input.ExpectedSource)
	if sourceErr != nil || source != input.ExpectedSource {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("expected_source", "must be an exact canonical locale")
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
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("operation", "exactly one Label AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("locale", "only a missing non-source Label translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("locale", "only an existing non-source Label translation can be deleted")
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
		return uuid.Nil, errs.InvalidArgument(
			"mutation",
			"compiled Label identity, locale, contributor, source observation, and locale presence must match the locked state",
		)
	}
	if input.ExpectedRevision != state.Revision {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.Revision,
		}
	}
	if !equalLabelTargetRevision(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: state.Revision,
			CurrentTargetRevision:   cloneLabelTargetRevision(state.TargetRevision),
		}
	}
	if input.Batch != nil {
		if input.Batch.DocumentID != state.ContentDocumentID || input.Batch.ExpectedRevision != expected ||
			len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID {
			return uuid.Nil, errs.InvalidArgument(
				"mutation",
				"compiled Label document, revision, and attribution must match the locked state",
			)
		}
	}
	return expected, nil
}

func (s *LabelService) applyAIDocumentMode(
	ctx context.Context,
	tx *gorm.DB,
	input AIDocumentMutation,
	expected uuid.UUID,
	memberID uuid.UUID,
	documentID uuid.UUID,
	fence contentblock.DomainFence,
) (AIDocumentMutationResult, error) {
	if input.Batch != nil {
		batch := *input.Batch
		if batch.DocumentID != documentID {
			return AIDocumentMutationResult{}, errs.InvalidArgument("document", "Label content document does not match")
		}
		batch.ExpectedRevision = expected
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
		if input.Locale != input.ExpectedSource {
			createTarget := !input.ExpectedPresence
			target, err := applyLabelTargetLocaleMutation(ctx, tx, s.contentBlocks, labelTargetLocaleMutationInput{
				LabelID: input.LabelID, Locale: input.Locale,
				ExpectedDocumentRevision: expected, ExpectedTargetRevision: input.ExpectedTargetRevision,
				Batch: batch, AllowCreate: createTarget, SeedSource: createTarget, Fence: fence,
			})
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
			return AIDocumentMutationResult{Content: target.Content, TargetRevision: &target.TargetRevision}, nil
		}
		if input.ExpectedTargetRevision != nil {
			return AIDocumentMutationResult{}, errs.InvalidArgument("expected_target_revision", "source Label mutation cannot carry a target revision")
		}
		result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, fence)
		return AIDocumentMutationResult{Content: result}, err
	}
	if input.CreateTranslation {
		target, err := applyLabelTargetLocaleMutation(ctx, tx, s.contentBlocks, labelTargetLocaleMutationInput{
			LabelID: input.LabelID, Locale: input.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: input.ExpectedTargetRevision,
			Batch:       contentblock.Batch{DocumentID: documentID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{memberID}},
			AllowCreate: true, SeedSource: true, Fence: fence,
		})
		if err != nil {
			return AIDocumentMutationResult{}, err
		}
		return AIDocumentMutationResult{Content: target.Content, TargetRevision: &target.TargetRevision}, nil
	}
	result, err := deleteLabelTargetLocale(ctx, tx, s.contentBlocks, input.LabelID, input.Locale, expected, input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence)
	return AIDocumentMutationResult{Content: result}, err
}

func labelAuthorizedAIDocumentFence(
	root labelAIDocumentRoot,
	expectedSource string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("label content document changed; reload before saving")
		}
		source, err := lockLabelTranslationSourceContext(ctx, tx, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if source.SourceLocale != expectedSource {
			var current struct {
				Revision uuid.UUID `gorm:"column:revision"`
			}
			if err := tx.WithContext(ctx).Table("content_document").Select("revision").Where("id = ?", documentID).Take(&current).Error; err != nil {
				return contentblock.DomainContext{}, errs.Internal(err)
			}
			return contentblock.DomainContext{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: current.Revision.String(),
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
			currentTargetRevision = cloneLabelTargetRevision(&targetConflict.CurrentRevision)
		}
		return &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: currentDocumentRevision,
			CurrentTargetRevision:   currentTargetRevision,
		}
	}
	return normalizeLabelContentBlockError(err)
}

func equalLabelTargetRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneLabelTargetRevision(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
