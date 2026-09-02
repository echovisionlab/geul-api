package campaign

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

// AIDocumentState is the Campaign-owned draft content projection consumed by
// the compact AI document adapter.
type AIDocumentState struct {
	CampaignID       string
	Status           string
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

type AIDocumentMutation struct {
	CampaignID               string
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

// AIDocumentMutationCompiler is invoked only with the current authorized
// Campaign state loaded under the Campaign root lock.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type campaignAIDocumentCompilerError struct{ cause error }

func (e *campaignAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *campaignAIDocumentCompilerError) Unwrap() error { return e.cause }

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
	return fmt.Sprintf("Campaign AI document revision conflict: current revision is %q", e.CurrentDocumentRevision)
}

// AIDocumentService retains Campaign authorization, draft lifecycle, CAS and
// locale persistence while the transport adapter owns only DCDP conversion.
type AIDocumentService struct{ internal *InternalCampaignService }

func NewAIDocumentService(internal *InternalCampaignService) (*AIDocumentService, error) {
	if internal == nil || internal.db == nil || internal.spiceDB == nil ||
		internal.contentBlocks == nil || internal.auditWriter == nil {
		return nil, errors.New("campaign AI document dependencies are required")
	}
	return &AIDocumentService{internal: internal}, nil
}

func (s *AIDocumentService) Load(ctx context.Context, campaignID, locale string) (AIDocumentState, error) {
	campaignID, err := canonicalCampaignAIDocumentID("campaign_id", campaignID)
	if err != nil {
		return AIDocumentState{}, err
	}
	locale, err = normalizeCampaignDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}

	var state AIDocumentState
	err = s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadCampaignAIDocumentRoot(ctx, tx, campaignID, "SHARE")
		if err != nil {
			return err
		}
		memberID, err := requireCampaignAIDocumentAuthority(ctx, s.internal.spiceDB, campaignID)
		if err != nil {
			return err
		}
		domain, err := loadCampaignEmailSourceContext(ctx, tx, campaignContentEntity, campaignID)
		if err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateInTransaction(
			ctx, tx, root, documentID, locale, domain, memberID, false,
		)
		return err
	})
	return state, err
}

var errRollbackCampaignAIDocumentValidation = errors.New("rollback Campaign AI document validation")

// ExecuteAIDocumentMutation is Campaign's exact DCDP mutation boundary. It
// locks the Campaign root, proves the draft lifecycle, performs one Edit
// decision, locks current source/locale/contributor facts, then invokes the
// compiler and persists under the same transaction. Validate rolls back the
// identical CAS, projection, and Audit path.
func (s *AIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	campaignID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.internal == nil || s.internal.db == nil ||
		s.internal.spiceDB == nil || s.internal.contentBlocks == nil ||
		s.internal.auditWriter == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Campaign AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Campaign AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	campaignID, err := canonicalCampaignAIDocumentID("campaign_id", campaignID)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	locale, err = normalizeCampaignDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}

	var output AIDocumentMutationResult
	var currentDocumentRevision string
	err = s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadCampaignAIDocumentRoot(ctx, tx, campaignID, "UPDATE")
		if err != nil {
			return err
		}
		if !campaignStatusAllowsEdit(root.Status) {
			return errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
		}
		memberID, err := requireCampaignAIDocumentAuthority(ctx, s.internal.spiceDB, campaignID)
		if err != nil {
			return err
		}
		if err := requireCampaignContributors(
			ctx, tx, []string{memberID.String()},
		); err != nil {
			return err
		}
		domain, err := lockCampaignEmailTranslationSource(ctx, tx, campaignContentEntity, campaignID)
		if err != nil {
			return err
		}
		state, err := s.loadAIDocumentStateInTransaction(
			ctx, tx, root, documentID, locale, domain, memberID, true,
		)
		if err != nil {
			return err
		}
		currentDocumentRevision = state.DocumentRevision
		mutation, err := compiler(state)
		if err != nil {
			return &campaignAIDocumentCompilerError{cause: err}
		}
		validated, expected, err := validateCampaignAIDocumentMutation(mutation)
		if err != nil {
			return err
		}
		mutation = validated
		if err := validateCompiledCampaignAIDocumentMutation(state, mutation, expected); err != nil {
			return err
		}
		fence := campaignAuthorizedAIDocumentFence(documentID, domain)
		output, err = s.applyAIDocumentMutationInTransaction(
			ctx, tx, mutation, expected, documentID, fence,
		)
		if err != nil {
			return err
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackCampaignAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackCampaignAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *campaignAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		return AIDocumentMutationResult{}, mapCampaignAIDocumentError(err, currentDocumentRevision)
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
		output, applyErr := applyCampaignTargetMutation(
			ctx, tx, s.internal.contentBlocks,
			campaignTargetMutationInput{
				CampaignID: mutation.CampaignID, DocumentID: documentID, Locale: mutation.Locale,
				Batch: contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expected,
					ContributorMemberIDs: []uuid.UUID{mutation.ContributorMember},
				},
				ExpectedDocumentRevision: expected, ExpectedTargetRevision: mutation.ExpectedTargetRevision,
				AllowCreate: true, SeedSourceOnCreate: true,
				ContributorMember: mutation.ContributorMember, Now: now, Fence: fence,
			},
		)
		if applyErr != nil {
			return AIDocumentMutationResult{}, applyErr
		}
		result = output.Result
		targetRevision = &output.TargetRevision
	case mutation.DeleteTranslation:
		result, err = deleteCampaignTargetLocale(
			ctx, tx, s.internal.contentBlocks, mutation.CampaignID, documentID, mutation.Locale,
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
					return applyCampaignAIDocumentSubject(ctx, tx, mutation, now)
				},
			)
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
		} else {
			output, applyErr := applyCampaignTargetMutation(
				ctx, tx, s.internal.contentBlocks,
				campaignTargetMutationInput{
					CampaignID: mutation.CampaignID, DocumentID: documentID, Locale: mutation.Locale,
					Batch: batch, ExpectedDocumentRevision: expected,
					ExpectedTargetRevision: mutation.ExpectedTargetRevision,
					AllowCreate:            true, SeedSourceOnCreate: true,
					SetSubject: mutation.SetSubject, Subject: targetSubject,
					ContributorMember: mutation.ContributorMember, Now: now, Fence: fence,
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
			if err := tx.WithContext(ctx).Table("campaign").Where("id = ?", mutation.CampaignID).
				Update("updated_at", now).Error; err != nil {
				return AIDocumentMutationResult{}, errs.Internal(err)
			}
			snapshot, err := s.internal.contentBlocks.LoadSnapshotInTransaction(
				ctx, tx, documentID, mutation.ExpectedSource,
			)
			if err != nil {
				return AIDocumentMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
			}
			if err := projectCampaignEmailMaterializedContent(
				ctx, tx, campaignContentEntity, mutation.CampaignID,
				snapshot, []string{mutation.Locale}, now,
			); err != nil {
				return AIDocumentMutationResult{}, err
			}
		}
		if err := appendCampaignLocaleContentAudit(
			ctx,
			tx,
			s.internal.auditWriter,
			mutation.ContributorMember.String(),
			mutation.CampaignID,
			mutation.Locale,
			campaignLocaleContentOperation(
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
	root campaignAIDocumentRoot,
	documentID uuid.UUID,
	locale string,
	domain contentblock.DomainContext,
	memberID uuid.UUID,
	lockLocale bool,
) (AIDocumentState, error) {
	state, err := loadCampaignExactLocaleState(
		ctx, tx, s.internal.contentBlocks, root.ID, documentID, locale, lockLocale,
	)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	var subject *string
	if state.TargetMetadata != nil && state.TargetMetadata.Subject.Valid {
		value := state.TargetMetadata.Subject.String
		subject = &value
	}
	return AIDocumentState{
		CampaignID: root.ID, Status: root.Status, DocumentID: documentID,
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		TargetRevision:   optionalCampaignTargetRevision(state, locale), SourceLocale: domain.SourceLocale,
		Locale: locale, LocaleExists: state.TargetMetadata != nil, ViewerMemberID: memberID.String(),
		Subject: cloneCampaignAIDocumentString(subject), Document: document,
	}, nil
}

func optionalCampaignTargetRevision(state campaignExactLocaleState, locale string) *string {
	if locale == state.SourceLocale || state.TargetMetadata == nil {
		return nil
	}
	revision := state.TargetRevision
	return &revision
}

func validateCompiledCampaignAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
	expected uuid.UUID,
) error {
	if mutation.CampaignID != state.CampaignID || mutation.Locale != state.Locale ||
		mutation.ContributorMember.String() != state.ViewerMemberID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Campaign identity, locale, and contributor must match the locked state",
		)
	}
	if mutation.ExpectedDocumentRevision != state.DocumentRevision || mutation.ExpectedSource != state.SourceLocale ||
		mutation.ExpectedPresence != state.LocaleExists {
		return &AIDocumentRevisionConflictError{
			Kind:                    AIDocumentDocumentRevisionConflict,
			CurrentDocumentRevision: state.DocumentRevision, CurrentTargetRevision: state.TargetRevision,
		}
	}
	if !campaignAIDocumentStringEqual(mutation.ExpectedTargetRevision, state.TargetRevision) {
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
			"compiled Campaign document, revision, and attribution must match the locked state",
		)
	}
	return nil
}

func validateCampaignAIDocumentMutation(
	input AIDocumentMutation,
) (AIDocumentMutation, uuid.UUID, error) {
	id, err := canonicalCampaignAIDocumentID("campaign_id", input.CampaignID)
	if err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	input.CampaignID = id
	input.Locale, err = normalizeCampaignDocumentLocale(input.Locale)
	if err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	input.ExpectedSource, err = normalizeCampaignDocumentLocale(input.ExpectedSource)
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
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("operations", "exactly one Campaign mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("locale", "only a missing non-source Campaign locale can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("locale", "only an existing non-source Campaign locale can be deleted")
	}
	return input, expected, nil
}

func mapCampaignAIDocumentError(err error, currentDocumentRevision string) error {
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
	return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
}

func cloneCampaignAIDocumentString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
