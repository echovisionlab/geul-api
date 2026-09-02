package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// AIDocumentState is the Email Template-owned coherent subject and Email
// profile document exposed to the compact AI document adapter.
type AIDocumentState struct {
	TemplateID       string
	DocumentID       uuid.UUID
	DocumentRevision string
	TargetRevision   *string
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	ViewerMemberID   string
	Subject          *string
	Document         *contentv1.LocalizedRichTextDocument
}

// AIDocumentMutation is an adapter-compiled Email Template mutation. Subject
// omission and an explicit empty target subject remain distinct.
type AIDocumentMutation struct {
	TemplateID               string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedSource           string
	ExpectedPresence         bool
	ContributorMember        uuid.UUID
	Batch                    *contentblock.Batch
	SetSubject               bool
	Subject                  string
	CreateTranslation        bool
	DeleteTranslation        bool
}

type AIDocumentMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type emailTemplateAIDocumentCompilerError struct{ cause error }

func (e *emailTemplateAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *emailTemplateAIDocumentCompilerError) Unwrap() error { return e.cause }

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document_revision"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target_revision"
)

type AIDocumentRevisionConflictError struct {
	Kind                    AIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return fmt.Sprintf("Email Template AI document revision conflict: current revision is %q", e.CurrentDocumentRevision)
}

// AIDocumentService keeps DCDP transport conversion outside the Email
// Template domain while retaining authorization, lifecycle, transaction and
// locale persistence here.
type AIDocumentService struct{ internal *InternalEmailTemplateService }

func NewAIDocumentService(internal *InternalEmailTemplateService) (*AIDocumentService, error) {
	if internal == nil || internal.db == nil || internal.spiceDB == nil ||
		internal.contentBlocks == nil || internal.auditWriter == nil {
		return nil, errors.New("email template AI document dependencies are required")
	}
	return &AIDocumentService{internal: internal}, nil
}

func (s *AIDocumentService) Load(ctx context.Context, templateID, locale string) (AIDocumentState, error) {
	templateID, err := canonicalEmailAIDocumentID("email_template_id", templateID)
	if err != nil {
		return AIDocumentState{}, err
	}
	locale, err = normalizeEmailTemplateDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}

	var state AIDocumentState
	err = s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, emailTemplateContentEntity, templateID, false)
		if err != nil {
			return err
		}
		memberID, err := requireEmailTemplateAIDocumentAuthority(ctx, s.internal.spiceDB, templateID)
		if err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateInTransaction(
			ctx, tx, templateID, locale, documentID, memberID, false,
		)
		return err
	})
	return state, err
}

var errRollbackEmailTemplateAIDocumentValidation = errors.New("rollback Email Template AI document validation")

// ExecuteAIDocumentMutation is Email Template's exact DCDP mutation boundary.
// It locks the aggregate root and delivery lifecycle, performs one Edit
// decision, locks the current source/locale/contributor facts, and only then
// invokes the adapter compiler. Validate rolls the same transaction back.
func (s *AIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	templateID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.internal == nil || s.internal.db == nil ||
		s.internal.spiceDB == nil || s.internal.contentBlocks == nil ||
		s.internal.auditWriter == nil ||
		s.internal.references == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Email Template AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Email Template AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	templateID, err := canonicalEmailAIDocumentID("email_template_id", templateID)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	locale, err = normalizeEmailTemplateDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}

	var output AIDocumentMutationResult
	var currentDocumentRevision string
	err = s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, err := loadCampaignEmailContentDocumentRoot(
			ctx, tx, emailTemplateContentEntity, templateID, true,
		)
		if err != nil {
			return err
		}
		if err := ensureEmailTemplateMutableForActiveDelivery(ctx, tx, s.internal.references, templateID); err != nil {
			return err
		}
		memberID, err := requireEmailTemplateAIDocumentAuthority(ctx, s.internal.spiceDB, templateID)
		if err != nil {
			return err
		}
		if err := lockEmailAIDocumentContributor(ctx, tx, memberID); err != nil {
			return err
		}
		state, err := s.loadAIDocumentStateInTransaction(
			ctx, tx, templateID, locale, documentID, memberID, true,
		)
		if err != nil {
			return err
		}
		currentDocumentRevision = state.DocumentRevision
		mutation, err := compiler(state)
		if err != nil {
			return &emailTemplateAIDocumentCompilerError{cause: err}
		}
		validated, expected, err := validateEmailTemplateAIDocumentMutation(mutation)
		if err != nil {
			return err
		}
		mutation = validated
		if err := validateCompiledEmailTemplateAIDocumentMutation(state, mutation, expected); err != nil {
			return err
		}
		domain := contentblock.DomainContext{
			SourceLocale: state.SourceLocale,
		}
		fence := emailTemplateAuthorizedAIDocumentFence(documentID, domain)
		output, err = s.applyAIDocumentMutationInTransaction(
			ctx, tx, mutation, expected, documentID, fence,
		)
		if err != nil {
			return err
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackEmailTemplateAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackEmailTemplateAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *emailTemplateAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		return AIDocumentMutationResult{}, mapEmailTemplateAIDocumentError(err, currentDocumentRevision)
	}
	return output, nil
}

func (s *AIDocumentService) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	expected uuid.UUID,
	documentID uuid.UUID,
	fence contentblock.DomainFence,
) (AIDocumentMutationResult, error) {
	now := time.Now().UTC()

	var result contentblock.Result
	var err error
	var targetRevision *string
	var targetSubject *string
	if mutation.SetSubject {
		targetSubject = &mutation.Subject
	}
	switch {
	case mutation.CreateTranslation:
		output, applyErr := applyEmailTemplateTargetMutation(
			ctx, tx, s.internal.contentBlocks,
			emailTemplateTargetMutationInput{
				TemplateID: mutation.TemplateID, DocumentID: documentID, Locale: mutation.Locale,
				Batch: contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expected,
					ContributorMemberIDs: []uuid.UUID{mutation.ContributorMember},
				},
				ExpectedDocumentRevision: expected, ExpectedTargetRevision: mutation.ExpectedTargetRevision,
				AllowCreate: true, SeedSourceOnCreate: true, Now: now, Fence: fence,
			},
		)
		if applyErr != nil {
			return AIDocumentMutationResult{}, applyErr
		}
		result = output.Result
		targetRevision = &output.TargetRevision
	case mutation.DeleteTranslation:
		result, err = deleteEmailTemplateTargetLocale(
			ctx, tx, s.internal.contentBlocks, mutation.TemplateID, documentID, mutation.Locale,
			expected, mutation.ExpectedTargetRevision, []uuid.UUID{mutation.ContributorMember}, now, fence,
		)
		if err != nil {
			return AIDocumentMutationResult{}, err
		}
	default:
		batch := *mutation.Batch
		batch.DocumentID = documentID
		batch.ExpectedRevision = expected
		batch.ContributorMemberIDs = []uuid.UUID{mutation.ContributorMember}
		if mutation.Locale == mutation.ExpectedSource {
			if mutation.ExpectedTargetRevision != nil {
				return AIDocumentMutationResult{}, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
			}
			result, err = s.internal.contentBlocks.ApplyBatchWithMetadata(
				ctx, tx, batch, fence,
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					return applyEmailTemplateAIDocumentSubject(ctx, tx, mutation, now)
				},
			)
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
		} else {
			output, applyErr := applyEmailTemplateTargetMutation(
				ctx, tx, s.internal.contentBlocks,
				emailTemplateTargetMutationInput{
					TemplateID: mutation.TemplateID, DocumentID: documentID, Locale: mutation.Locale,
					Batch: batch, ExpectedDocumentRevision: expected,
					ExpectedTargetRevision: mutation.ExpectedTargetRevision,
					AllowCreate:            false,
					SetSubject:             mutation.SetSubject, Subject: targetSubject,
					Now: now, Fence: fence,
				},
			)
			if applyErr != nil {
				return AIDocumentMutationResult{}, applyErr
			}
			result = output.Result
			targetRevision = &output.TargetRevision
		}
	}

	if result.Changed {
		if mutation.Locale == mutation.ExpectedSource {
			if err := tx.WithContext(ctx).Table("email_template").Where("id = ?", mutation.TemplateID).
				Update("updated_at", now).Error; err != nil {
				return AIDocumentMutationResult{}, errs.Internal(err)
			}
			snapshot, err := s.internal.contentBlocks.LoadSnapshotInTransaction(
				ctx, tx, documentID, mutation.ExpectedSource,
			)
			if err != nil {
				return AIDocumentMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
			}
			projectionLocales := []string{mutation.Locale}
			if result.TranslationSourceChanged {
				// Source edits can alter fallback output for every existing target
				// row. Keep their sparse values, but refresh each materialized
				// delivery projection from the new shared snapshot.
				projectionLocales = nil
			}
			if err := projectEmailTemplateMaterializedContent(
				ctx, tx, mutation.TemplateID, snapshot, projectionLocales, now,
			); err != nil {
				return AIDocumentMutationResult{}, err
			}
			if result.TranslationSourceChanged {
				localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, mutation.ExpectedSource)
				if err != nil {
					return AIDocumentMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
				}
				projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localized, nil)
				if err != nil {
					return AIDocumentMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
				}
				if err := syncCustomEmailTemplateVariables(ctx, tx, mutation.TemplateID, projection.HTML); err != nil {
					return AIDocumentMutationResult{}, err
				}
			}
		}
		if err := appendEmailTemplateLocaleContentAudit(
			ctx,
			tx,
			s.internal.auditWriter,
			mutation.ContributorMember.String(),
			mutation.TemplateID,
			mutation.Locale,
			emailAuthoringLocaleContentOperation(
				mutation.Locale == mutation.ExpectedSource,
				mutation.CreateTranslation,
				mutation.DeleteTranslation,
				mutation.ExpectedPresence,
			),
		); err != nil {
			return AIDocumentMutationResult{}, err
		}
	}

	output := AIDocumentMutationResult{
		DocumentRevision: result.DocumentRevision.String(), TargetRevision: targetRevision, Changed: result.Changed,
	}
	return output, nil
}

func (s *AIDocumentService) loadAIDocumentStateInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
	locale string,
	documentID uuid.UUID,
	memberID uuid.UUID,
	lockLocale bool,
) (AIDocumentState, error) {
	state, err := loadEmailTemplateExactLocaleState(
		ctx, tx, s.internal.contentBlocks, templateID, documentID, locale, lockLocale,
	)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	var subject *string
	if state.TargetMetadata != nil && state.TargetMetadata.Subject.Valid {
		value := state.TargetMetadata.Subject.String
		subject = &value
	}
	return AIDocumentState{
		TemplateID: templateID, DocumentID: documentID,
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		TargetRevision:   optionalEmailTemplateTargetRevision(state, locale),
		SourceLocale:     state.SourceLocale, Locale: locale,
		LocaleExists: state.TargetMetadata != nil, ViewerMemberID: memberID.String(),
		Subject: cloneEmailAIDocumentString(subject), Document: document,
	}, nil
}

func optionalEmailTemplateTargetRevision(state emailTemplateExactLocaleState, locale string) *string {
	if locale == state.SourceLocale || state.TargetMetadata == nil {
		return nil
	}
	revision := state.TargetRevision
	return &revision
}

func validateCompiledEmailTemplateAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
	expected uuid.UUID,
) error {
	if mutation.TemplateID != state.TemplateID || mutation.Locale != state.Locale ||
		mutation.ContributorMember.String() != state.ViewerMemberID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Email Template identity, locale, and contributor must match the locked state",
		)
	}
	if mutation.ExpectedDocumentRevision != state.DocumentRevision || mutation.ExpectedSource != state.SourceLocale ||
		mutation.ExpectedPresence != state.LocaleExists {
		return &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentDocumentRevisionConflict,
			CurrentDocumentRevision: state.DocumentRevision, CurrentTargetRevision: state.TargetRevision,
		}
	}
	if !emailTemplateAIDocumentStringEqual(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: state.DocumentRevision, CurrentTargetRevision: state.TargetRevision,
		}
	}
	if mutation.Batch != nil && (mutation.Batch.DocumentID != state.DocumentID ||
		mutation.Batch.ExpectedRevision != expected || len(mutation.Batch.ContributorMemberIDs) != 1 ||
		mutation.Batch.ContributorMemberIDs[0] != mutation.ContributorMember) {
		return errs.InvalidArgument(
			"mutation",
			"compiled Email Template document, revision, and attribution must match the locked state",
		)
	}
	return nil
}

func validateEmailTemplateAIDocumentMutation(
	input AIDocumentMutation,
) (AIDocumentMutation, uuid.UUID, error) {
	id, err := canonicalEmailAIDocumentID("email_template_id", input.TemplateID)
	if err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	input.TemplateID = id
	input.Locale, err = normalizeEmailTemplateDocumentLocale(input.Locale)
	if err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	input.ExpectedSource, err = normalizeEmailTemplateDocumentLocale(input.ExpectedSource)
	if err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	expected, err := uuid.Parse(strings.TrimSpace(input.ExpectedDocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != strings.TrimSpace(input.ExpectedDocumentRevision) {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	if input.ContributorMember == uuid.Nil {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("contributor_member_id", "is required")
	}
	modes := 0
	if input.Batch != nil {
		modes++
	}
	if input.CreateTranslation {
		modes++
	}
	if input.DeleteTranslation {
		modes++
	}
	if modes != 1 {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("operations", "exactly one Email Template mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("locale", "only a missing non-source Email Template locale can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("locale", "only an existing non-source Email Template locale can be deleted")
	}
	return input, expected, nil
}

func mapEmailTemplateAIDocumentError(err error, currentDocumentRevision string) error {
	var conflict *AIDocumentRevisionConflictError
	if errors.As(err, &conflict) {
		return conflict
	}
	var stale *contentblock.StaleRevisionError
	if errors.As(err, &stale) {
		return &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentDocumentRevisionConflict,
			CurrentDocumentRevision: stale.CurrentRevision.String(),
		}
	}
	var target *translation.TargetRevisionConflict
	if errors.As(err, &target) {
		var current *string
		if target.CurrentExists {
			value := target.CurrentRevision
			current = &value
		}
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentTargetRevision: current,
			CurrentDocumentRevision: currentDocumentRevision,
		}
	}
	return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
}

func emailTemplateAIDocumentStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneEmailAIDocumentString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
