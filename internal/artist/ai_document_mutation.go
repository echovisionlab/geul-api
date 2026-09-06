package artist

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var errRollbackArtistAIDocumentValidation = errors.New("rollback Artist AI document validation")

// ExecuteAIDocumentMutation owns Artist's exact mutation transaction. The
// locked lifecycle and one Edit decision are established before any document
// projection or adapter compiler executes.
func (s *InternalArtistService) ExecuteAIDocumentMutation(
	ctx context.Context,
	artistID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil || s.translation == nil || s.runtime == nil {
		return AIDocumentMutationResult{}, errs.Internal(errors.New("artist AI document dependencies are not configured"))
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Artist AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	canonicalArtistID, err := canonicalArtistUUID(artistID)
	if err != nil {
		return AIDocumentMutationResult{}, errs.InvalidArgument("artist_id", "must be a canonical UUID")
	}
	locale, err = normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}

	var output AIDocumentMutationResult
	var input AIDocumentMutation
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadArtistAIDocumentRoot(ctx, tx, canonicalArtistID.String(), "UPDATE")
		if err != nil {
			return err
		}
		if _, valid := artistAIDocumentLifecycle(root.Status); !valid {
			return errs.InternalMsg("Artist has an unsupported lifecycle status")
		}
		allowed, err := requireArtistAIDocumentMutationAuthority(ctx, tx, s.spiceDB, root.ID)
		if err != nil {
			return err
		}
		if !allowed {
			return errs.NotFound("artist", root.ID)
		}
		principal := auth.GetUser(ctx)
		if principal == nil {
			return errs.NotFound("artist", root.ID)
		}
		memberID, err := canonicalArtistUUID(principal.MemberID.String())
		if err != nil {
			return errs.NotFound("artist", root.ID)
		}
		state, err := s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, memberID.String(),
		)
		if err != nil {
			return err
		}
		input, err = compiler(state)
		if err != nil {
			return &artistAIDocumentCompilerError{cause: err}
		}
		expected, err := validateCompiledArtistAIDocumentMutation(state, input, memberID)
		if err != nil {
			return err
		}
		if err := s.translation.RequireDocumentContributors(ctx, tx, []string{memberID.String()}); err != nil {
			return err
		}
		fence := artistAuthorizedAIDocumentFence(
			root, input.Locale, input.ExpectedSource, input.ExpectedPresence,
		)
		result, targetRevision, err := s.applyAIDocumentMode(ctx, tx, input, expected, memberID, *root.DocumentID, fence)
		if err != nil {
			return mapArtistAIDocumentMutationError(err)
		}
		output = AIDocumentMutationResult{
			Revision: result.DocumentRevision.String(), TargetRevision: targetRevision,
			Changed: result.Changed,
		}
		if result.Changed {
			if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, root.ID, input.Locale, result.TranslationSourceChanged, "artist_ai_document_saved"); err != nil {
				return err
			}
			if input.Locale == input.ExpectedSource {
				if err := s.appendAIDocumentAudit(ctx, tx, root.ID, input.Locale, memberID.String()); err != nil {
					return err
				}
			} else if err := s.appendAIDocumentTargetLocaleAudit(ctx, tx, input, memberID.String()); err != nil {
				return err
			}
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackArtistAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackArtistAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *artistAIDocumentCompilerError
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		if errors.As(err, &targetConflict) {
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentTargetRevisionConflict, CurrentRevision: input.ExpectedRevision,
				CurrentTargetRevision: artistTargetConflictRevision(targetConflict),
			}
		}
		return AIDocumentMutationResult{}, err
	}
	if mode == AIDocumentExecutionApply && output.Changed {
		_ = publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			artistAIDocumentContentUpdatedEvent(input, output, input.ContributorMemberID.String()),
		)
	}
	return output, nil
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
		return uuid.Nil, errs.InvalidArgument(
			"mutation",
			"compiled Artist identity, locale, contributor, source observation, and locale presence must match the locked state",
		)
	}
	if state.Locale == state.SourceLocale {
		if state.TargetRevision != nil {
			return uuid.Nil, errs.FailedPrecondition("Artist source locale cannot carry a target revision")
		}
	} else if state.LocaleExists != (state.TargetRevision != nil) {
		return uuid.Nil, errs.FailedPrecondition("Artist target locale presence and revision are inconsistent")
	}
	if input.ExpectedRevision != state.Revision {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: state.Revision,
		}
	}
	if !sameArtistTargetRevision(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: state.Revision,
			CurrentTargetRevision: cloneArtistTargetRevision(state.TargetRevision),
		}
	}
	if input.Batch != nil {
		if input.Batch.DocumentID != state.DocumentID || input.Batch.ExpectedRevision != expected ||
			len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID {
			return uuid.Nil, errs.InvalidArgument(
				"mutation",
				"compiled Artist document, revision, and attribution must match the locked state",
			)
		}
	}
	return expected, nil
}

func (s *InternalArtistService) appendAIDocumentAudit(ctx context.Context, tx *gorm.DB, artistID, locale, contributor string) error {
	if s.auditWriter == nil {
		return errs.InternalMsg("Artist AI document audit writer is not configured")
	}
	return appendArtistMemberTargetLocaleAudit(
		ctx, tx, s.auditWriter, contributor, artistID, locale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

func (s *InternalArtistService) appendAIDocumentTargetLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	input AIDocumentMutation,
	memberID string,
) error {
	if s.auditWriter == nil {
		return errs.InternalMsg("Artist AI document audit writer is not configured")
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if input.CreateTranslation {
		operation = sharedtelemetry.AuditItemOperationCreated
	} else if input.DeleteTranslation {
		operation = sharedtelemetry.AuditItemOperationDeleted
	}
	return appendArtistMemberTargetLocaleAudit(ctx, tx, s.auditWriter, memberID, input.ArtistID, input.Locale, operation)
}

func (s *InternalArtistService) applyAIDocumentMode(ctx context.Context, tx *gorm.DB, input AIDocumentMutation, expected, memberID, documentID uuid.UUID, fence contentblock.DomainFence) (contentblock.Result, *string, error) {
	if input.Locale != input.ExpectedSource {
		if input.Batch != nil {
			batch := *input.Batch
			batch.ExpectedRevision, batch.ContributorMemberIDs = expected, []uuid.UUID{memberID}
			result, revision, err := applyArtistTargetLocaleBatch(
				ctx, tx, s.contentBlocks, input.ArtistID, documentID, input.Locale, batch,
				input.ExpectedTargetRevision, false, time.Now().UTC(), fence,
			)
			return result, &revision, err
		}
		if input.CreateTranslation {
			batch := contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: expected,
				ContributorMemberIDs: []uuid.UUID{memberID},
			}
			result, revision, err := applyArtistTargetLocaleBatch(
				ctx, tx, s.contentBlocks, input.ArtistID, documentID, input.Locale, batch,
				input.ExpectedTargetRevision, true, time.Now().UTC(), fence,
			)
			return result, &revision, err
		}
		result, err := deleteArtistTargetLocale(
			ctx, tx, s.contentBlocks, input.ArtistID, documentID, input.Locale, expected,
			input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence,
		)
		return result, nil, err
	}
	if input.ExpectedTargetRevision != nil {
		return contentblock.Result{}, nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	if input.Batch != nil {
		batch := *input.Batch
		if batch.DocumentID != documentID {
			return contentblock.Result{}, nil, errs.InvalidArgument("document", "Artist content document does not match")
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
	return contentblock.Result{}, nil, errs.InvalidArgument("locale", "source translation cannot be deleted")
}

func artistAuthorizedAIDocumentFence(
	root artistAIDocumentRoot,
	locale string,
	expectedSource string,
	expectedPresence bool,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Artist content document changed; reload before saving")
		}
		source, err := lockCreativeTranslationSourceContext(ctx, tx, artistContentEntity, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		var count int64
		if err := tx.WithContext(ctx).Table("artist_translation").Where("entity_id = ?::uuid AND locale = ?", root.ID, locale).Count(&count).Error; err != nil {
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		if source.SourceLocale != expectedSource || (count != 0) != expectedPresence {
			var current uuid.UUID
			if err := tx.WithContext(ctx).Table("content_document").Select("revision").Where("id = ?", documentID).Scan(&current).Error; err != nil {
				return contentblock.DomainContext{}, errs.Internal(err)
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
		return false, errs.Internal(err)
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
		return false, errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return false, errs.DependencyUnavailable("SpiceDB")
	}
	return allowed, nil
}

func artistAIDocumentContentUpdatedEvent(input AIDocumentMutation, result AIDocumentMutationResult, contributor string) *managev1.ContentUpdatedEvent {
	if !result.Changed {
		return nil
	}
	exists := input.Locale == input.ExpectedSource || !input.DeleteTranslation
	documentChanged := input.Locale == input.ExpectedSource
	return buildArtistContentUpdatedEvent(
		input.ArtistID,
		[]string{"content"},
		result.Revision,
		[]string{contributor},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		input.Locale,
		exists,
		result.TargetRevision,
		documentChanged,
	)
}

func createArtistAIDocumentTranslation(ctx context.Context, tx *gorm.DB, artistID, locale string) (bool, error) {
	result := tx.WithContext(ctx).Exec("INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, NULL, now(), now()) ON CONFLICT (entity_id, locale) DO NOTHING", artistID, locale)
	return result.RowsAffected != 0, result.Error
}

func sameArtistTargetRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneArtistTargetRevision(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func artistTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists || conflict.CurrentRevision == "" {
		return nil
	}
	current := conflict.CurrentRevision
	return &current
}
