package release

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var errRollbackReleaseAIDocumentValidation = errors.New("rollback Release AI document validation")

func (s *InternalReleaseService) ExecuteAIDocumentMutation(ctx context.Context, releaseID, locale string, mode AIDocumentExecutionMode, compiler AIDocumentMutationCompiler) (AIDocumentMutationResult, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil || s.ogRefresher == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Release AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Release AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	canonicalReleaseID, err := uuidFromCanonicalString(releaseID)
	if err != nil {
		return AIDocumentMutationResult{}, errs.InvalidArgument("release_id", "must be a canonical UUID")
	}
	locale, err = normalizeReleaseDocumentLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var output AIDocumentMutationResult
	var input AIDocumentMutation
	var sourceResult contentblock.Result
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadReleaseAIDocumentRoot(ctx, tx, canonicalReleaseID.String(), "UPDATE")
		if err != nil {
			return err
		}
		if _, valid := releaseAIDocumentLifecycle(root.Status); !valid {
			return errs.InternalMsg("Release has an unsupported lifecycle status")
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, root.ID, releaseActionEdit); err != nil {
			return err
		}
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return errs.NotFound("release", root.ID)
		}
		memberID, err := uuidFromCanonicalString(principal.MemberID.String())
		if err != nil {
			return errs.NotFound("release", root.ID)
		}
		state, err := s.loadAIDocumentStateAfterAuthorization(ctx, tx, root, locale, memberID.String())
		if err != nil {
			return err
		}
		input, err = compiler(state)
		if err != nil {
			return &releaseAIDocumentCompilerError{cause: err}
		}
		expected, err := validateCompiledReleaseAIDocumentMutation(state, input, memberID)
		if err != nil {
			return err
		}
		if err := requireDocumentContributors(ctx, tx, []string{memberID.String()}); err != nil {
			return err
		}
		fence := releaseAuthorizedAIDocumentFence(root, state.SourceLocale)
		output, sourceResult, err = s.applyAIDocumentMode(ctx, tx, input, expected, memberID, *root.DocumentID, fence)
		if err != nil {
			return err
		}
		if sourceResult.Changed && sourceResult.TranslationSourceChanged {
			if err := s.ogRefresher.RequestCurrent(ctx, tx, root.ID, "release_ai_document_saved"); err != nil {
				return err
			}
			if err := s.appendAIDocumentAudit(ctx, tx, root.ID, state.Locale, memberID.String()); err != nil {
				return err
			}
		} else if output.Changed && input.Locale != state.SourceLocale {
			if err := appendReleaseMemberTargetLocaleAudit(
				ctx,
				tx,
				s.auditWriter,
				memberID.String(),
				root.ID,
				input.Locale,
				releaseTargetLocaleAuditOperation(input.CreateTranslation, input.DeleteTranslation, input.ExpectedPresence),
			); err != nil {
				return err
			}
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackReleaseAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackReleaseAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *releaseAIDocumentCompilerError
		var conflict *AIDocumentRevisionConflictError
		var stale *contentblock.StaleRevisionError
		var targetConflict *translation.TargetRevisionConflict
		switch {
		case errors.As(err, &compilerErr):
			return AIDocumentMutationResult{}, compilerErr.cause
		case errors.As(err, &conflict):
			return AIDocumentMutationResult{}, conflict
		case errors.As(err, &stale):
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String()}
		case errors.As(err, &targetConflict):
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: input.ExpectedDocumentRevision, CurrentTargetRevision: releaseTargetConflictRevision(targetConflict)}
		default:
			return AIDocumentMutationResult{}, mapReleaseAIDocumentMutationError(err)
		}
	}
	if mode == AIDocumentExecutionApply && output.Changed {
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, releaseAIDocumentContentUpdatedEvent(input, output, input.ContributorMemberID.String()))
	}
	return output, nil
}

func validateCompiledReleaseAIDocumentMutation(state AIDocumentState, input AIDocumentMutation, memberID uuid.UUID) (uuid.UUID, error) {
	_, expected, err := validateReleaseAIDocumentMutation(input)
	if err != nil {
		return uuid.Nil, err
	}
	if input.ReleaseID != state.ReleaseID || input.Locale != state.Locale || input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists || input.ContributorMemberID != memberID || state.ViewerMemberID != memberID.String() {
		return uuid.Nil, errs.InvalidArgument("mutation", "compiled Release identity, locale, contributor, source observation, and locale presence must match the locked state")
	}
	if input.ExpectedDocumentRevision != state.DocumentRevision {
		return uuid.Nil, &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision}
	}
	if !releaseOptionalRevisionEqual(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: state.DocumentRevision, CurrentTargetRevision: cloneReleaseRevision(state.TargetRevision)}
	}
	if input.Batch != nil && (input.Batch.DocumentID != state.DocumentID || input.Batch.ExpectedRevision != expected || len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID) {
		return uuid.Nil, errs.InvalidArgument("mutation", "compiled Release document, revision, and attribution must match the locked state")
	}
	return expected, nil
}

func (s *InternalReleaseService) applyAIDocumentMode(ctx context.Context, tx *gorm.DB, input AIDocumentMutation, expected, memberID, documentID uuid.UUID, fence contentblock.DomainFence) (AIDocumentMutationResult, contentblock.Result, error) {
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{memberID}}
	if input.Batch != nil {
		batch = contentblock.CloneBatch(*input.Batch)
		batch.DocumentID, batch.ExpectedRevision = documentID, expected
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
	}
	if input.Locale != input.ExpectedSource {
		if input.DeleteTranslation {
			result, err := deleteReleaseTargetLocale(ctx, tx, s.contentBlocks, input.ReleaseID, documentID, input.Locale, expected, input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence)
			if err != nil {
				return AIDocumentMutationResult{}, contentblock.Result{}, err
			}
			return AIDocumentMutationResult{DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed}, contentblock.Result{}, nil
		}
		target, err := applyReleaseTargetLocaleMutation(ctx, tx, s.contentBlocks, releaseTargetLocaleMutationInput{
			ReleaseID: input.ReleaseID, DocumentID: documentID, Locale: input.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: input.ExpectedTargetRevision,
			// A missing target is an explicit lifecycle boundary.  Editing a
			// sparse target must not silently create its metadata/overlay; callers
			// create it with the dedicated CreateTranslation operation first.
			AllowCreate: input.CreateTranslation,
			Batch:       batch, SetTitle: input.SetTitle, Title: input.Title,
			CreditNotePatch: input.CreditNotePatch, Now: time.Now().UTC(), Fence: fence,
		})
		if err != nil {
			return AIDocumentMutationResult{}, contentblock.Result{}, err
		}
		targetRevision := target.TargetRevision
		return AIDocumentMutationResult{DocumentRevision: target.Content.DocumentRevision.String(), TargetRevision: &targetRevision, Changed: target.Content.Changed}, contentblock.Result{}, nil
	}
	if input.ExpectedTargetRevision != nil {
		return AIDocumentMutationResult{}, contentblock.Result{}, errs.InvalidArgument("expected_target_revision", "source Release mutation cannot carry a target revision")
	}
	if input.CreateTranslation || input.DeleteTranslation {
		return AIDocumentMutationResult{}, contentblock.Result{}, errs.InvalidArgument("locale", "Release source translation lifecycle cannot be changed")
	}
	result, err := s.contentBlocks.ApplyBatchWithMetadata(ctx, tx, batch, fence, func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
		changed, err := applyReleaseSourceAIDocumentMetadata(ctx, tx, input, time.Now().UTC())
		return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: changed, SourceLocale: input.ExpectedSource, ChangedLocales: []string{input.ExpectedSource}}, err
	})
	if err != nil {
		return AIDocumentMutationResult{}, contentblock.Result{}, err
	}
	return AIDocumentMutationResult{DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed}, result, nil
}

func applyReleaseSourceAIDocumentMetadata(ctx context.Context, tx *gorm.DB, input AIDocumentMutation, now time.Time) (bool, error) {
	row, exists, err := loadOptionalReleaseLocaleMetadataRow(ctx, tx, input.ReleaseID, input.ExpectedSource, true)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errs.FailedPrecondition("Release source locale metadata is missing")
	}
	currentNotes, err := loadReleaseCreditLocaleNotes(ctx, tx, input.ReleaseID, input.ExpectedSource)
	if err != nil {
		return false, errs.Internal(err)
	}
	nextTitle := row.Title
	if input.SetTitle {
		if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
			return false, errs.InvalidArgument("title", "must not be empty")
		}
		normalized := strings.TrimSpace(*input.Title)
		nextTitle = &normalized
	}
	nextNotes := maps.Clone(currentNotes)
	for creditID, note := range input.CreditNotePatch {
		nextNotes[creditID] = note
	}
	if err := validateReleaseCreditNoteIdentityMap(ctx, tx, input.ReleaseID, nextNotes); err != nil {
		return false, err
	}
	titleChanged := !releaseOptionalStringsEqual(row.Title, nextTitle)
	notesChanged := !maps.Equal(currentNotes, nextNotes)
	if !titleChanged && !notesChanged {
		return false, nil
	}
	if titleChanged {
		if err := tx.WithContext(ctx).Table("release_translation").Where("entity_id = ?::uuid AND locale = ?", input.ReleaseID, input.ExpectedSource).Updates(map[string]any{"title": nextTitle, "updated_at": now}).Error; err != nil {
			return false, errs.Internal(err)
		}
	}
	if notesChanged {
		if err := replaceReleaseCreditLocaleNotes(ctx, tx, input.ReleaseID, input.ExpectedSource, nextNotes, now); err != nil {
			return false, errs.Internal(err)
		}
	}
	if err := touchReleaseSourceLocaleState(ctx, tx, input.ReleaseID, now); err != nil {
		return false, err
	}
	return true, nil
}

func (s *InternalReleaseService) appendAIDocumentAudit(ctx context.Context, tx *gorm.DB, releaseID, locale, contributor string) error {
	if s.auditWriter == nil {
		return errs.InternalMsg("Release AI document audit writer is not configured")
	}
	return domainaudit.AppendMember(ctx, tx, s.auditWriter, contributor, sharedtelemetry.AuditReleaseUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseLocaleContentAuditRecord(
			metadata, releaseID, locale, sharedtelemetry.AuditItemOperationUpdated,
		)
	})
}

func releaseAuthorizedAIDocumentFence(root releaseAIDocumentRoot, expectedSource string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Release content document changed; reload before saving")
		}
		domain, err := lockReleaseTranslationSourceContext(ctx, tx, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != expectedSource {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Release source locale changed; reload before saving")
		}
		return domain, nil
	}
}

func deleteReleaseAIDocumentTranslation(ctx context.Context, tx *gorm.DB, releaseID, locale string) (bool, error) {
	if err := tx.WithContext(ctx).Exec("DELETE FROM release_credit_locale AS localized USING release_credit AS credit WHERE localized.credit_id = credit.id AND credit.release_id = ?::uuid AND localized.locale = ?", releaseID, locale).Error; err != nil {
		return false, err
	}
	result := tx.WithContext(ctx).Exec("DELETE FROM release_translation WHERE entity_id = ?::uuid AND locale = ?", releaseID, locale)
	return result.RowsAffected != 0, result.Error
}

func releaseAIDocumentContentUpdatedEvent(input AIDocumentMutation, result AIDocumentMutationResult, contributor string) *managev1.ContentUpdatedEvent {
	if !result.Changed {
		return nil
	}
	revision := result.DocumentRevision
	fields := make([]*managev1.ContentUpdatedField, 0, 3)
	if input.SetTitle {
		fields = append(fields, &managev1.ContentUpdatedField{
			Path: "title", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		})
	}
	if input.Batch != nil || input.CreateTranslation || input.DeleteTranslation {
		fields = append(fields, &managev1.ContentUpdatedField{
			Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		})
	}
	if len(input.CreditNotePatch) != 0 {
		fields = append(fields, &managev1.ContentUpdatedField{
			Path: "document.credits", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		})
	}
	if len(fields) == 0 {
		// A translation lifecycle operation can be represented without a
		// content field, but the subscriber still needs a meaningful invalidation
		// signal. Keep the document content path as the stable Release surface.
		fields = append(fields, &managev1.ContentUpdatedField{
			Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		})
	}
	sourcePath := input.Locale == input.ExpectedSource
	exists := !input.DeleteTranslation
	if sourcePath && result.TargetRevision != nil {
		return nil
	}
	if !sourcePath && exists && result.TargetRevision == nil {
		return nil
	}
	event := &managev1.ContentUpdatedEvent{
		EntityType:    managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE,
		EntityId:      input.ReleaseID,
		Source:        managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		ChangedFields: fields, ContributorMemberIds: []string{contributor},
		DocumentStateChanged: sourcePath, DocumentRevision: &revision,
		Locale: &input.Locale, LocaleExists: &exists, TimestampMs: time.Now().UnixMilli(),
	}
	if !sourcePath && exists && result.TargetRevision != nil {
		targetRevision := *result.TargetRevision
		event.TargetRevision = &targetRevision
	}
	return event
}

func releaseOptionalRevisionEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func cloneReleaseRevision(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func releaseTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists {
		return nil
	}
	revision := conflict.CurrentRevision
	return &revision
}
