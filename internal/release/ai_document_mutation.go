package release

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

var errRollbackReleaseAIDocumentValidation = errors.New("rollback Release AI document validation")

// ExecuteAIDocumentMutationCommand compiles and applies one AI document
// mutation of a Release locale.
type ExecuteAIDocumentMutationCommand struct {
	ReleaseID string
	Locale    string
	Mode      AIDocumentExecutionMode
	Compiler  AIDocumentMutationCompiler
}

// aiDocumentApplied is an applied AI document mutation and the facts its
// audit record and signals need.
type aiDocumentApplied struct {
	output        AIDocumentMutationResult
	input         AIDocumentMutation
	sourceChanged bool
	locale        string
	sourceLocale  string
	memberID      string
}

// ExecuteAIDocumentMutation runs the compiled mutation behind the Release
// root lock and the exact Edit decision. Validation runs the same path,
// audit and signals included, and rolls it back.
func (s *Service) ExecuteAIDocumentMutation(ctx context.Context, command ExecuteAIDocumentMutationCommand) (AIDocumentMutationResult, error) {
	if command.Compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Release AI document compiler")
	}
	if command.Mode != AIDocumentExecutionValidate && command.Mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	canonicalReleaseID, err := uuidFromCanonicalString(command.ReleaseID)
	if err != nil {
		return AIDocumentMutationResult{}, domainerr.InvalidField("release_id", "must be a canonical UUID")
	}
	locale, err := normalizeReleaseDocumentLocale(command.Locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root DocumentRoot
	var applied aiDocumentApplied
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[aiDocumentApplied, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			root, err = loadReleaseAIDocumentRoot(ctx, store, canonicalReleaseID.String(), UpdateLock)
			return err
		},
		Access: mutation.Fenced(func(ctx context.Context, _ Store, lock lockActive) error {
			return requireActiveReleaseAction(ctx, lock, s.spiceDB, root.ID, releaseActionEdit)
		}),
		Change: func(ctx context.Context, store Store) (aiDocumentApplied, error) {
			var err error
			applied, err = applyAIDocumentMutation(ctx, store, root, locale, command.Compiler)
			return applied, err
		},
		Audit: func(applied aiDocumentApplied) mutation.Audit {
			switch {
			case applied.sourceChanged:
				return releaseMemberLocaleAudit(applied.memberID, root.ID, applied.locale, sharedtelemetry.AuditItemOperationUpdated)
			case applied.output.Changed && applied.input.Locale != applied.sourceLocale:
				return releaseMemberLocaleAudit(applied.memberID, root.ID, applied.input.Locale, releaseTargetLocaleAuditOperation(
					applied.input.CreateTranslation, applied.input.DeleteTranslation, applied.input.ExpectedPresence,
				))
			default:
				return mutation.Unchanged()
			}
		},
		Outbox: func(ctx context.Context, store Store, applied aiDocumentApplied) error {
			if applied.sourceChanged {
				if err := store.RequestOG(ctx, root.ID, "release_ai_document_saved"); err != nil {
					return err
				}
			}
			if command.Mode == AIDocumentExecutionValidate {
				return errRollbackReleaseAIDocumentValidation
			}
			if !applied.output.Changed {
				return nil
			}
			return publishContentUpdated(ctx, store, releaseAIDocumentContentUpdatedEvent(applied.input, applied.output, applied.input.ContributorMemberID.String()))
		},
	})
	if errors.Is(err, errRollbackReleaseAIDocumentValidation) {
		return applied.output, nil
	}
	if err != nil {
		return AIDocumentMutationResult{}, mapReleaseAIDocumentExecutionError(err, applied.input)
	}
	return applied.output, nil
}

// applyAIDocumentMutation compiles the mutation against the locked state of
// the caller's Member and applies it.
func applyAIDocumentMutation(
	ctx context.Context,
	store Store,
	root DocumentRoot,
	locale string,
	compiler AIDocumentMutationCompiler,
) (aiDocumentApplied, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || principal.MemberID == "" {
		return aiDocumentApplied{}, releaseNotFound(root.ID)
	}
	memberID, err := uuidFromCanonicalString(principal.MemberID.String())
	if err != nil {
		return aiDocumentApplied{}, releaseNotFound(root.ID).WithCause(err)
	}
	state, err := loadAIDocumentStateAfterAuthorization(ctx, store, root, locale, memberID.String())
	if err != nil {
		return aiDocumentApplied{}, err
	}
	input, err := compiler(state)
	if err != nil {
		return aiDocumentApplied{}, &releaseAIDocumentCompilerError{cause: err}
	}
	applied := aiDocumentApplied{input: input, locale: state.Locale, sourceLocale: state.SourceLocale, memberID: memberID.String()}
	expected, err := validateCompiledReleaseAIDocumentMutation(state, input, memberID)
	if err != nil {
		return applied, err
	}
	if err := requireDocumentContributors(ctx, store, []string{memberID.String()}); err != nil {
		return applied, err
	}
	fence := releaseAuthorizedAIDocumentFence(root, state.SourceLocale)
	output, sourceResult, err := applyAIDocumentMode(ctx, store, input, expected, memberID, *root.DocumentID, fence)
	if err != nil {
		return applied, err
	}
	applied.output = output
	applied.sourceChanged = sourceResult.Changed && sourceResult.TranslationSourceChanged
	return applied, nil
}

// mapReleaseAIDocumentExecutionError answers a compiler failure with its own
// cause and a revision race with the conflict the AI document port reports.
func mapReleaseAIDocumentExecutionError(err error, input AIDocumentMutation) error {
	var compilerErr *releaseAIDocumentCompilerError
	var conflict *AIDocumentRevisionConflictError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return compilerErr.cause
	case errors.As(err, &conflict):
		return conflict
	case errors.As(err, &stale):
		return &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String()}
	case errors.As(err, &targetConflict):
		return &AIDocumentRevisionConflictError{Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: input.ExpectedDocumentRevision, CurrentTargetRevision: releaseTargetConflictRevision(targetConflict)}
	default:
		return mapReleaseAIDocumentMutationError(err)
	}
}

func validateCompiledReleaseAIDocumentMutation(state AIDocumentState, input AIDocumentMutation, memberID uuid.UUID) (uuid.UUID, error) {
	_, expected, err := validateReleaseAIDocumentMutation(input)
	if err != nil {
		return uuid.Nil, err
	}
	if input.ReleaseID != state.ReleaseID || input.Locale != state.Locale || input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists || input.ContributorMemberID != memberID || state.ViewerMemberID != memberID.String() {
		return uuid.Nil, domainerr.InvalidField("mutation", "compiled Release identity, locale, contributor, source observation, and locale presence must match the locked state")
	}
	if input.ExpectedDocumentRevision != state.DocumentRevision {
		return uuid.Nil, &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision}
	}
	if !optional.Equal(input.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, &AIDocumentRevisionConflictError{Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: state.DocumentRevision, CurrentTargetRevision: optional.Clone(state.TargetRevision)}
	}
	if input.Batch != nil && (input.Batch.DocumentID != state.DocumentID || input.Batch.ExpectedRevision != expected || len(input.Batch.ContributorMemberIDs) != 1 || input.Batch.ContributorMemberIDs[0] != memberID) {
		return uuid.Nil, domainerr.InvalidField("mutation", "compiled Release document, revision, and attribution must match the locked state")
	}
	return expected, nil
}

func applyAIDocumentMode(ctx context.Context, store Store, input AIDocumentMutation, expected, memberID, documentID uuid.UUID, fence ContentFence) (AIDocumentMutationResult, contentblock.Result, error) {
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{memberID}}
	if input.Batch != nil {
		batch = contentblock.CloneBatch(*input.Batch)
		batch.DocumentID, batch.ExpectedRevision = documentID, expected
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
	}
	if input.Locale != input.ExpectedSource {
		if input.DeleteTranslation {
			result, err := deleteReleaseTargetLocale(ctx, store, input.ReleaseID, documentID, input.Locale, expected, input.ExpectedTargetRevision, []uuid.UUID{memberID}, fence)
			if err != nil {
				return AIDocumentMutationResult{}, contentblock.Result{}, err
			}
			return AIDocumentMutationResult{DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed}, contentblock.Result{}, nil
		}
		target, err := applyReleaseTargetLocaleMutation(ctx, store, releaseTargetLocaleMutationInput{
			ReleaseID: input.ReleaseID, DocumentID: documentID, Locale: input.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: input.ExpectedTargetRevision,
			// A missing target is an explicit lifecycle boundary.  Editing a
			// sparse target must not silently create its metadata/overlay; callers
			// create it with the dedicated CreateTranslation operation first.
			Policy: targetlocale.Policy{AllowCreate: input.CreateTranslation, SeedSource: true},
			Batch:  batch, SetTitle: input.SetTitle, Title: input.Title,
			CreditNotePatch: input.CreditNotePatch, Now: time.Now().UTC(), Fence: fence,
		})
		if err != nil {
			return AIDocumentMutationResult{}, contentblock.Result{}, err
		}
		targetRevision := target.TargetRevision
		return AIDocumentMutationResult{DocumentRevision: target.Content.DocumentRevision.String(), TargetRevision: &targetRevision, Changed: target.Content.Changed}, contentblock.Result{}, nil
	}
	if input.ExpectedTargetRevision != nil {
		return AIDocumentMutationResult{}, contentblock.Result{}, domainerr.InvalidField("expected_target_revision", "source Release mutation cannot carry a target revision")
	}
	if input.CreateTranslation || input.DeleteTranslation {
		return AIDocumentMutationResult{}, contentblock.Result{}, domainerr.InvalidField("locale", "Release source translation lifecycle cannot be changed")
	}
	result, err := store.ApplyContentBatchWithMetadata(ctx, batch, fence, func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
		changed, err := applyReleaseSourceAIDocumentMetadata(ctx, store, input, time.Now().UTC())
		return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: changed, SourceLocale: input.ExpectedSource, ChangedLocales: []string{input.ExpectedSource}}, err
	})
	if err != nil {
		return AIDocumentMutationResult{}, contentblock.Result{}, err
	}
	return AIDocumentMutationResult{DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed}, result, nil
}

func applyReleaseSourceAIDocumentMetadata(ctx context.Context, store Store, input AIDocumentMutation, now time.Time) (bool, error) {
	row, exists, err := store.LocaleMetadata(ctx, input.ReleaseID, input.ExpectedSource, true)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, domainerr.Precondition("Release source locale metadata is missing")
	}
	currentNotes, err := store.CreditNotes(ctx, input.ReleaseID, input.ExpectedSource)
	if err != nil {
		return false, err
	}
	nextTitle := row.Title
	if input.SetTitle {
		if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
			return false, domainerr.InvalidField("title", "must not be empty")
		}
		normalized := strings.TrimSpace(*input.Title)
		nextTitle = &normalized
	}
	nextNotes := maps.Clone(currentNotes)
	for creditID, note := range input.CreditNotePatch {
		nextNotes[creditID] = note
	}
	// A patch can only remove one note by emptying it, as the target writer
	// treats an empty target note.
	maps.DeleteFunc(nextNotes, func(_ string, note string) bool {
		return strings.TrimSpace(note) == ""
	})
	if err := validateReleaseCreditNoteIdentityMap(ctx, store, input.ReleaseID, nextNotes); err != nil {
		return false, err
	}
	titleChanged := !releaseOptionalStringsEqual(row.Title, nextTitle)
	notesChanged := !maps.Equal(currentNotes, nextNotes)
	if !titleChanged && !notesChanged {
		return false, nil
	}
	if titleChanged {
		if _, err := store.UpdateLocaleTitle(ctx, input.ReleaseID, input.ExpectedSource, nextTitle, now); err != nil {
			return false, err
		}
	}
	if notesChanged {
		if err := store.ReplaceCreditNotes(ctx, input.ReleaseID, input.ExpectedSource, nextNotes, now); err != nil {
			return false, err
		}
	}
	if err := touchReleaseSourceLocaleState(ctx, store, input.ReleaseID, now); err != nil {
		return false, err
	}
	return true, nil
}

func releaseAuthorizedAIDocumentFence(root DocumentRoot, expectedSource string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.DocumentID == nil || documentID != *root.DocumentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Release content document changed; reload before saving")
		}
		domain, err := lockReleaseTranslationSourceContext(ctx, store, root.ID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != expectedSource {
			return contentblock.DomainContext{}, domainerr.Precondition("Release source locale changed; reload before saving")
		}
		return domain, nil
	}
}

func releaseAIDocumentContentUpdatedEvent(input AIDocumentMutation, result AIDocumentMutationResult, contributor string) *ContentUpdate {
	if !result.Changed {
		return nil
	}
	revision := result.DocumentRevision
	fields := make([]ChangedField, 0, 3)
	if input.SetTitle {
		fields = append(fields, ChangedField{Path: "title", Kind: FieldText})
	}
	if input.Batch != nil || input.CreateTranslation || input.DeleteTranslation {
		fields = append(fields, ChangedField{Path: "document.content", Kind: FieldText})
	}
	if len(input.CreditNotePatch) != 0 {
		fields = append(fields, ChangedField{Path: "document.credits", Kind: FieldText})
	}
	if len(fields) == 0 {
		// A translation lifecycle operation can be represented without a
		// content field, but the subscriber still needs a meaningful invalidation
		// signal. Keep the document content path as the stable Release surface.
		fields = append(fields, ChangedField{Path: "document.content", Kind: FieldText})
	}
	sourcePath := input.Locale == input.ExpectedSource
	exists := !input.DeleteTranslation
	if sourcePath && result.TargetRevision != nil {
		return nil
	}
	if !sourcePath && exists && result.TargetRevision == nil {
		return nil
	}
	event := &ContentUpdate{
		ReleaseID: input.ReleaseID, Source: SourceAI,
		Fields: fields, ContributorMemberIDs: []string{contributor},
		DocumentStateChanged: sourcePath, DocumentRevision: &revision,
		Locale: &input.Locale, LocaleExists: &exists,
	}
	if !sourcePath && exists && result.TargetRevision != nil {
		targetRevision := *result.TargetRevision
		event.TargetRevision = &targetRevision
	}
	return event
}

func releaseTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists {
		return nil
	}
	revision := conflict.CurrentRevision
	return &revision
}
