package work

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
func (s *AIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	workID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.internal == nil || s.internal.db == nil ||
		s.internal.spiceDB == nil || s.internal.contentBlocks == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Work AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Work AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	if err := validateWorkAIDocumentIdentity(workID, locale); err != nil {
		return AIDocumentMutationResult{}, err
	}

	var result AIDocumentMutationResult
	var currentDocumentRevision string
	err := s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadWorkAIDocumentRoot(ctx, tx, workID, "UPDATE")
		if err != nil {
			return err
		}
		principal, err := authorizeWorkAIDocument(
			ctx, tx, s.internal.spiceDB, root, workAuthorizationMutation,
		)
		if err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return errs.NotFound("work", workID)
			default:
				return err
			}
		}
		state, err := s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, documentID, locale, principal.MemberID.String(),
		)
		if err != nil {
			return err
		}
		currentDocumentRevision = state.DocumentRevision
		mutation, err := compiler(state)
		if err != nil {
			return &workAIDocumentCompilerError{cause: err}
		}
		if err := validateCompiledWorkAIDocumentMutation(state, mutation); err != nil {
			return err
		}
		domain := contentblock.DomainContext{
			SourceLocale: state.SourceLocale,
		}
		result, err = s.applyAIDocumentMutationInTransaction(
			ctx,
			tx,
			mutation,
			workAuthorizedAIDocumentFence(root.ID, documentID, domain, mutation.ContributorMemberID),
		)
		if err != nil {
			return err
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackWorkAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackWorkAIDocumentValidation) {
		return result, nil
	}
	if err == nil {
		return result, nil
	}
	var compilerErr *workAIDocumentCompilerError
	var conflict *AIDocumentRevisionConflictError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return AIDocumentMutationResult{}, compilerErr.cause
	case errors.As(err, &conflict):
		return AIDocumentMutationResult{}, conflict
	case errors.As(err, &targetConflict):
		current := targetConflict.CurrentRevision
		return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: currentDocumentRevision,
			CurrentTargetRevision: &current,
		}
	case errors.As(err, &stale):
		return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String(),
		}
	default:
		return AIDocumentMutationResult{}, normalizeWorkContentBlockError(err)
	}
}

func validateCompiledWorkAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
) error {
	contributor, err := uuid.Parse(mutation.ContributorMemberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != mutation.ContributorMemberID {
		return errs.InvalidArgument("contributor", "compiled Work contributor must be a canonical Member UUID")
	}
	if mutation.WorkID != state.Work.ID || mutation.Locale != state.Locale ||
		mutation.ObservedSourceLocale != state.SourceLocale ||
		mutation.ObservedLocaleExists != state.LocaleExists ||
		mutation.ContributorMemberID != state.ViewerMemberID ||
		mutation.Batch.DocumentID != state.Snapshot.Document.ID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Work identity, locale, contributor, source observation, and content document must match the locked state",
		)
	}
	if mutation.ExpectedRevision.String() != state.DocumentRevision {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
		}
	}
	if !equalWorkTargetRevision(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: cloneWorkTargetRevision(state.TargetRevision),
		}
	}
	return nil
}

func equalWorkTargetRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneWorkTargetRevision(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func workAuthorizedAIDocumentFence(
	workID string,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
	contributorMemberID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if strings.TrimSpace(workID) == "" || documentID == uuid.Nil || requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Work content document changed; reload before saving")
		}
		if err := requireDocumentContributors(ctx, tx, []string{contributorMemberID}); err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}
