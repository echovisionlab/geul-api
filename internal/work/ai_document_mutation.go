package work

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/translation"
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

type AIDocumentRevisionConflictKind uint8

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = iota + 1
	AIDocumentTargetRevisionConflict
)

// AIDocumentRevisionConflictError preserves the current shared document token
// and, for target-only conflicts, the exact locale token.
type AIDocumentRevisionConflictError struct {
	Kind                    AIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return "Work AI document revision changed: current revision is " + e.CurrentDocumentRevision
}

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
	var root model.Work
	var documentID uuid.UUID
	var principal *auth.UserInfo
	var state AIDocumentState
	var compiled AIDocumentMutation
	var validated AIDocumentMutationResult
	applied, err := mutation.Run(ctx, s.mutations, mutation.Spec[workAIDocumentApply]{
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			if root, documentID, err = loadWorkAIDocumentRoot(ctx, tx, workID, clause.LockingStrengthUpdate); err != nil {
				return err
			}
			principal, err = authorizeWorkAIDocument(ctx, tx, s.spiceDB, root, workAuthorizationMutation)
			if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
				return domainerr.ResourceNotFound("work", workID)
			}
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (workAIDocumentApply, error) {
			var err error
			if state, err = s.loadAIDocumentStateAfterAuthorization(ctx, tx, root, documentID, locale, principal.MemberID.String()); err != nil {
				return workAIDocumentApply{}, err
			}
			if compiled, err = compiler(state); err != nil {
				return workAIDocumentApply{}, &workAIDocumentCompilerError{cause: err}
			}
			if err := validateCompiledWorkAIDocumentMutation(state, compiled); err != nil {
				return workAIDocumentApply{}, err
			}
			applied, err := s.applyAIDocumentMutationInTransaction(ctx, tx,
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
		Outbox: func(ctx context.Context, tx *gorm.DB, applied workAIDocumentApply) error {
			content := applied.result.Content
			// Like the editor, a new title requests that locale's OG image. Public
			// reads show a translation only once its OG generation exists.
			if content.Changed && workAIDocumentTitleChanged(state, compiled) {
				if _, err := s.requestWorkOG(ctx, tx, compiled.WorkID, compiled.Locale, "work_ai_document_saved"); err != nil {
					return err
				}
			}
			return contentsignal.PublishInteractive(ctx, tx, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
				compiled.WorkID, compiled.Locale, content.DocumentRevision.String(), applied.result.TargetRevision,
				content.Changed, compiled.DeleteTranslation, compiled.ContributorMemberID)
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
	var conflict *AIDocumentRevisionConflictError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return compilerErr.cause
	case errors.As(err, &conflict):
		return conflict
	case errors.As(err, &targetConflict):
		current := targetConflict.CurrentRevision
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: currentDocumentRevision,
			CurrentTargetRevision: &current,
		}
	case errors.As(err, &stale):
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String(),
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
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
		}
	}
	if !optional.Equal(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: optional.Clone(state.TargetRevision),
		}
	}
	return nil
}

func workAuthorizedAIDocumentFence(
	workID string,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
	contributorMemberID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if strings.TrimSpace(workID) == "" || documentID == uuid.Nil || requestedDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Work content document changed; reload before saving")
		}
		if err := requireDocumentContributors(ctx, tx, []string{contributorMemberID}); err != nil {
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
