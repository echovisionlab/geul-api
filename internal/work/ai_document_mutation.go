package work

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
)

// AIDocumentExecutionMode selects whether the exact Work mutation transaction
// commits or deliberately rolls back after the same authorization, compiler,
// CAS, validation and persistence path.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler is the schema adapter callback invoked only with
// the authorized Work state loaded under the current root lock.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type workAIDocumentCompilerError struct{ cause error }

func (e *workAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *workAIDocumentCompilerError) Unwrap() error { return e.cause }

// ExecuteAIDocumentMutation locks the Work root before authority evaluation,
// selects Edit or EditArchived from that locked lifecycle, and performs one
// exact SpiceDB check before exposing state to the adapter compiler. The
// compiler and persistence run inside the same transaction and root lock.
func (s *Service) ExecuteAIDocumentMutation(
	ctx context.Context,
	workID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Work AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	if err := validateWorkAIDocumentIdentity(workID, locale); err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root Work
	var documentID uuid.UUID
	var principal *auth.UserInfo
	var state AIDocumentState
	var compiled AIDocumentMutation
	var validated AIDocumentMutationResult
	applied, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[workAIDocumentApply, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			var err error
			if root, documentID, err = loadWorkAIDocumentRoot(ctx, store, workID, UpdateLock); err != nil {
				return err
			}
			principal, err = authorizeWorkAIDocument(ctx, lockActive, s.spiceDB, root, workAuthorizationMutation)
			if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
				return domainerr.ResourceNotFound("work", workID)
			}
			return err
		}),
		Change: func(ctx context.Context, store Store) (workAIDocumentApply, error) {
			var err error
			if state, err = loadAIDocumentStateAfterAuthorization(ctx, store, root, documentID, locale, principal.MemberID.String()); err != nil {
				return workAIDocumentApply{}, err
			}
			if compiled, err = compiler(state); err != nil {
				return workAIDocumentApply{}, &workAIDocumentCompilerError{cause: err}
			}
			if err := validateCompiledWorkAIDocumentMutation(state, compiled); err != nil {
				return workAIDocumentApply{}, err
			}
			applied, err := applyAIDocumentMutationInTransaction(ctx, store,
				compiled, workAuthorizedAIDocumentFence(root.ID, documentID, contentblock.DomainContext{SourceLocale: state.SourceLocale}, compiled.ContributorMemberID),
			)
			if err == nil && mode == AIDocumentExecutionValidate {
				validated = applied.result
				return applied, errRollbackWorkAIDocumentValidation
			}
			return applied, err
		},
		Audit: func(applied workAIDocumentApply) mutation.Audit {
			if !applied.audited {
				return mutation.Unchanged()
			}
			return workLocaleContentAudit(compiled.ContributorMemberID, compiled.WorkID, compiled.Locale, applied.operation)
		},
		Outbox: func(ctx context.Context, store Store, applied workAIDocumentApply) error {
			content := applied.result.Content
			// Like the editor, a new title requests that locale's OG image. Public
			// reads show a translation only once its OG generation exists.
			if content.Changed && workAIDocumentTitleChanged(state, compiled) {
				if _, err := requestWorkOG(ctx, store, compiled.WorkID, compiled.Locale, "work_ai_document_saved"); err != nil {
					return err
				}
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{WorkID: compiled.WorkID, Kind: InteractiveUpdate, Interactive: InteractiveChange{
				Locale: compiled.Locale, DocumentRevision: content.DocumentRevision.String(), TargetRevision: applied.result.TargetRevision,
				Changed: content.Changed, LocaleDeleted: compiled.DeleteTranslation, ContributorID: compiled.ContributorMemberID,
			}})
		},
	})
	if errors.Is(err, errRollbackWorkAIDocumentValidation) {
		return validated, nil
	}
	if err != nil {
		return AIDocumentMutationResult{}, workAIDocumentFailure(err, state.DocumentRevision)
	}
	return applied.result, nil
}

// workAIDocumentFailure answers a failed AI document mutation: a compiler
// failure as itself, a revision conflict with the current revisions.
func workAIDocumentFailure(err error, currentDocumentRevision string) error {
	var compilerErr *workAIDocumentCompilerError
	var conflict *aidocument.RevisionConflict
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return compilerErr.cause
	case errors.As(err, &conflict):
		return conflict
	case errors.As(err, &targetConflict):
		current := targetConflict.CurrentRevision
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictTargetRevision,
			CurrentDocumentRevision: currentDocumentRevision,
			CurrentTargetRevision:   &current,
			Message:                 "Work AI document revision changed: current revision is " + currentDocumentRevision,
		}
	case errors.As(err, &stale):
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictDocumentRevision,
			CurrentDocumentRevision: stale.CurrentRevision.String(),
			Message:                 "Work AI document revision changed: current revision is " + stale.CurrentRevision.String(),
		}
	default:
		return normalizeWorkContentBlockError(err)
	}
}

func validateCompiledWorkAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
) error {
	contributor, err := uuid.Parse(mutation.ContributorMemberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != mutation.ContributorMemberID {
		return domainerr.InvalidField("contributor", "compiled Work contributor must be a canonical Member UUID").WithCause(err)
	}
	if mutation.WorkID != state.Work.ID || mutation.Locale != state.Locale ||
		mutation.ObservedSourceLocale != state.SourceLocale ||
		mutation.ObservedLocaleExists != state.LocaleExists ||
		mutation.ContributorMemberID != state.ViewerMemberID ||
		mutation.Batch.DocumentID != state.Snapshot.Document.ID {
		return domainerr.InvalidField(
			"mutation",
			"compiled Work identity, locale, contributor, source observation, and content document must match the locked state",
		)
	}
	if mutation.ExpectedRevision.String() != state.DocumentRevision {
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictDocumentRevision,
			CurrentDocumentRevision: state.DocumentRevision,
			Message:                 "Work AI document revision changed: current revision is " + state.DocumentRevision,
		}
	}
	if !optional.Equal(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictTargetRevision,
			CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision:   optional.Clone(state.TargetRevision),
			Message:                 "Work AI document revision changed: current revision is " + state.DocumentRevision,
		}
	}
	return nil
}

func workAuthorizedAIDocumentFence(
	workID string,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
	contributorMemberID string,
) ContentFence {
	return func(ctx context.Context, store Store, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if strings.TrimSpace(workID) == "" || documentID == uuid.Nil || requestedDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Work content document changed; reload before saving")
		}
		if err := requireDocumentContributors(ctx, store, []string{contributorMemberID}); err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}

// workAIDocumentTitleChanged reports whether the mutation saved a different
// title for its locale, including the first title of a new translation.
func workAIDocumentTitleChanged(state AIDocumentState, mutation AIDocumentMutation) bool {
	if !mutation.Metadata.SetTitle || mutation.DeleteTranslation {
		return false
	}
	if !state.LocaleExists {
		return true
	}
	return optionalWorkAIDocumentText(state.Title) != optionalWorkAIDocumentText(mutation.Metadata.Title)
}

func optionalWorkAIDocumentText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
