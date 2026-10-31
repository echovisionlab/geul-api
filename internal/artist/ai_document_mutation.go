package artist

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errRollbackArtistAIDocumentValidation = errors.New("rollback Artist AI document validation")

// ExecuteAIDocumentMutation owns Artist's exact mutation transaction. The
// locked lifecycle and one Edit decision are established before any document
// projection or adapter compiler executes. Validation runs the whole
// mutation and rolls it back.
func (s *Service) ExecuteAIDocumentMutation(
	ctx context.Context,
	artistID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Artist AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	canonicalArtistID, err := canonicalArtistUUID(artistID)
	if err != nil {
		return AIDocumentMutationResult{}, domainerr.InvalidField("artist_id", "must be a canonical UUID")
	}
	locale, err = normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root artistAIDocumentRoot
	var outcome artistAIDocumentOutcome
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[artistAIDocumentOutcome]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			if root, err = loadArtistAIDocumentRoot(ctx, tx, canonicalArtistID.String(), clause.LockingStrengthUpdate); err != nil {
				return err
			}
			if _, valid := artistAIDocumentLifecycle(root.Status); !valid {
				return errors.New("Artist has an unsupported lifecycle status")
			}
			return nil
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			allowed, err := requireArtistAIDocumentMutationAuthority(ctx, tx, s.spiceDB, root.ID)
			if err != nil {
				return err
			}
			if !allowed {
				return domainerr.ResourceNotFound("artist", root.ID)
			}
			return nil
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (artistAIDocumentOutcome, error) {
			var err error
			outcome, err = s.applyAIDocumentMutation(ctx, tx, root, locale, compiler)
			return outcome, err
		},
		Audit: func(outcome artistAIDocumentOutcome) mutation.Audit { return outcome.audit() },
		Outbox: func(ctx context.Context, tx *gorm.DB, outcome artistAIDocumentOutcome) error {
			if mode == AIDocumentExecutionValidate {
				return errRollbackArtistAIDocumentValidation
			}
			if !outcome.result.Changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher,
				artistAIDocumentContentUpdatedEvent(outcome.input, outcome.result, outcome.input.ContributorMemberID.String()))
		},
	})
	if errors.Is(err, errRollbackArtistAIDocumentValidation) {
		return outcome.result, nil
	}
	if err != nil {
		var compilerErr *artistAIDocumentCompilerError
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		if errors.As(err, &targetConflict) {
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentTargetRevisionConflict, CurrentRevision: outcome.input.ExpectedRevision,
				CurrentTargetRevision: artistTargetConflictRevision(targetConflict),
			}
		}
		return AIDocumentMutationResult{}, err
	}
	return outcome.result, nil
}

// artistAIDocumentOutcome is an applied AI-document mutation and the Member
// it is attributed to.
type artistAIDocumentOutcome struct {
	input    AIDocumentMutation
	memberID string
	result   AIDocumentMutationResult
}

// applyAIDocumentMutation compiles the mutation against the locked state and
// applies it as the principal's Member.
func (s *Service) applyAIDocumentMutation(
	ctx context.Context,
	tx *gorm.DB,
	root artistAIDocumentRoot,
	locale string,
	compiler AIDocumentMutationCompiler,
) (artistAIDocumentOutcome, error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return artistAIDocumentOutcome{}, domainerr.ResourceNotFound("artist", root.ID)
	}
	memberID, err := canonicalArtistUUID(principal.MemberID.String())
	if err != nil {
		return artistAIDocumentOutcome{}, domainerr.ResourceNotFound("artist", root.ID).WithCause(err)
	}
	state, err := s.loadAIDocumentStateAfterAuthorization(ctx, tx, root, locale, memberID.String())
	if err != nil {
		return artistAIDocumentOutcome{}, err
	}
	outcome := artistAIDocumentOutcome{memberID: memberID.String()}
	if outcome.input, err = compiler(state); err != nil {
		return outcome, &artistAIDocumentCompilerError{cause: err}
	}
	input := outcome.input
	expected, err := validateCompiledArtistAIDocumentMutation(state, input, memberID)
	if err != nil {
		return outcome, err
	}
	if err := s.translation.RequireDocumentContributors(ctx, tx, []string{memberID.String()}); err != nil {
		return outcome, err
	}
	fence := artistAuthorizedAIDocumentFence(root, input.Locale, input.ExpectedSource, input.ExpectedPresence)
	result, targetRevision, err := s.applyAIDocumentMode(ctx, tx, input, expected, memberID, *root.DocumentID, fence)
	if err != nil {
		return outcome, mapArtistAIDocumentMutationError(err)
	}
	outcome.result = AIDocumentMutationResult{Revision: result.DocumentRevision.String(), TargetRevision: targetRevision, Changed: result.Changed}
	if result.Changed {
		if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, root.ID, input.Locale, result.TranslationSourceChanged, "artist_ai_document_saved"); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// audit attributes a changed locale to the Member the mutation ran as: a
// target locale is created, updated or deleted, the source is updated.
func (o artistAIDocumentOutcome) audit() mutation.Audit {
	if !o.result.Changed {
		return mutation.Unchanged()
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if o.input.Locale != o.input.ExpectedSource && o.input.CreateTranslation {
		operation = sharedtelemetry.AuditItemOperationCreated
	} else if o.input.Locale != o.input.ExpectedSource && o.input.DeleteTranslation {
		operation = sharedtelemetry.AuditItemOperationDeleted
	}
	return artistMemberLocaleAudit(o.memberID, o.input.ArtistID, o.input.Locale, operation)
}

func validateCompiledArtistAIDocumentMutation(
	state AIDocumentState,
	input AIDocumentMutation,
	memberID uuid.UUID,
) (uuid.UUID, error) {
	_, expected, err := validateArtistAIDocumentMutation(input)
	if err != nil {
		return uuid.Nil, err
	}
	if input.ArtistID != state.ArtistID || input.Locale != state.Locale ||
		input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists ||
		input.ContributorMemberID != memberID || state.ViewerMemberID != memberID.String() {
		return uuid.Nil, domainerr.InvalidField(
			"mutation",
			"compiled Artist identity, locale, contributor, source observation, and locale presence must match the locked state",
		)
	}
	if state.Locale == state.SourceLocale {
		if state.TargetRevision != nil {
			return uuid.Nil, domainerr.Precondition("Artist source locale cannot carry a target revision")
		}
	} else if state.LocaleExists != (state.TargetRevision != nil) {
		return uuid.Nil, domainerr.Precondition("Artist target locale presence and revision are inconsistent")
	}
	if input.ExpectedRevision != state.Revision {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: state.Revision,
		}
	}
	if !optional.Equal(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: state.Revision,
			CurrentTargetRevision: optional.Clone(state.TargetRevision),
		}
	}
	if input.Batch != nil {
		if input.Batch.DocumentID != state.DocumentID || input.Batch.ExpectedRevision != expected ||
			len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID {
			return uuid.Nil, domainerr.InvalidField(
				"mutation",
				"compiled Artist document, revision, and attribution must match the locked state",
			)
		}
	}
	return expected, nil
}

func (s *Service) applyAIDocumentMode(ctx context.Context, tx *gorm.DB, input AIDocumentMutation, expected, memberID, documentID uuid.UUID, fence contentblock.DomainFence) (contentblock.Result, *string, error) {
	if input.Locale != input.ExpectedSource {
		if input.Batch != nil {
			batch := *input.Batch
			batch.ExpectedRevision, batch.ContributorMemberIDs = expected, []uuid.UUID{memberID}
			result, revision, err := applyArtistTargetLocaleMutation(ctx, tx, s.contentBlocks, artistTargetLocaleMutationInput{
				ArtistID: input.ArtistID, DocumentID: documentID, Locale: input.Locale, Batch: batch,
				ExpectedTargetRevision: input.ExpectedTargetRevision, Now: time.Now().UTC(), Fence: fence,
			})
			return result, &revision, err
		}
		if input.CreateTranslation {
			batch := contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: expected,
				ContributorMemberIDs: []uuid.UUID{memberID},
			}
			result, revision, err := applyArtistTargetLocaleMutation(ctx, tx, s.contentBlocks, artistTargetLocaleMutationInput{
				ArtistID: input.ArtistID, DocumentID: documentID, Locale: input.Locale, Batch: batch,
				ExpectedTargetRevision: input.ExpectedTargetRevision, Policy: targetlocale.Policy{AllowCreate: true},
				Now: time.Now().UTC(), Fence: fence,
			})
			return result, &revision, err
		}
		result, err := deleteArtistTargetLocale(
			ctx, tx, s.contentBlocks, input.ArtistID, documentID, input.Locale, expected,
			input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence,
		)
		return result, nil, err
	}
	if input.ExpectedTargetRevision != nil {
		return contentblock.Result{}, nil, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	if input.Batch != nil {
		batch := *input.Batch
		if batch.DocumentID != documentID {
			return contentblock.Result{}, nil, domainerr.InvalidField("document", "Artist content document does not match")
		}
		batch.ExpectedRevision, batch.ContributorMemberIDs = expected, []uuid.UUID{memberID}
		result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, fence)
		return result, nil, err
	}
	if input.CreateTranslation {
		advanced, err := s.contentBlocks.AdvanceRevision(ctx, tx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expected}, fence, func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			created, err := createArtistAIDocumentTranslation(ctx, tx, input.ArtistID, input.Locale)
			return contentblock.MetadataEffect{Changed: created, ChangedLocales: []string{input.Locale}}, err
		})
		return contentblock.Result{DocumentRevision: advanced.DocumentRevision, Changed: advanced.Changed, TranslationSourceChanged: advanced.TranslationSourceChanged}, nil, err
	}
	return contentblock.Result{}, nil, domainerr.InvalidField("locale", "source translation cannot be deleted")
}

func artistAuthorizedAIDocumentFence(
	root artistAIDocumentRoot,
	locale string,
	expectedSource string,
	expectedPresence bool,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Artist content document changed; reload before saving")
		}
		source, err := lockCreativeTranslationSourceContext(ctx, tx, artistContentEntity, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		var count int64
		if err := tx.WithContext(ctx).Table("artist_translation").Where("entity_id = ?::uuid AND locale = ?", root.ID, locale).Count(&count).Error; err != nil {
			return contentblock.DomainContext{}, err
		}
		if source.SourceLocale != expectedSource || (count != 0) != expectedPresence {
			current, err := contentblock.CurrentRevision(ctx, tx, documentID)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return contentblock.DomainContext{}, err
			}
			return contentblock.DomainContext{}, &AIDocumentRevisionConflictError{CurrentRevision: current.String()}
		}
		return source, nil
	}
}

func requireArtistAIDocumentMutationAuthority(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, artistID string) (bool, error) {
	principal := auth.GetUser(ctx)
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	can, err := policyv1.Artist.Edit(artistID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, errAuthenticationRequired.WithCause(err)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}

func artistAIDocumentContentUpdatedEvent(input AIDocumentMutation, result AIDocumentMutationResult, contributor string) *managev1.ContentUpdatedEvent {
	if !result.Changed {
		return nil
	}
	exists := input.Locale == input.ExpectedSource || !input.DeleteTranslation
	documentChanged := input.Locale == input.ExpectedSource
	return buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             input.ArtistID,
		Fields:               []string{"content"},
		DocumentRevision:     result.Revision,
		Contributors:         []string{contributor},
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:               input.Locale,
		LocaleDeleted:        !exists,
		TargetRevision:       result.TargetRevision,
		DocumentStateChanged: documentChanged,
	})
}

func createArtistAIDocumentTranslation(ctx context.Context, tx *gorm.DB, artistID, locale string) (bool, error) {
	result := tx.WithContext(ctx).Exec("INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, NULL, now(), now()) ON CONFLICT (entity_id, locale) DO NOTHING", artistID, locale)
	return result.RowsAffected != 0, result.Error
}

func artistTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists || conflict.CurrentRevision == "" {
		return nil
	}
	current := conflict.CurrentRevision
	return &current
}
