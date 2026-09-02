package page

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
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// AIDocumentExecutionMode selects whether the exact Page mutation transaction
// commits or deliberately rolls back after running the same compiler,
// authorization, CAS, validation and persistence path.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler is owned by the schema adapter. The Page domain
// exposes only the authorized state locked by its current transaction and
// receives its own typed mutation in return.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type pageAIDocumentCompilerError struct{ cause error }

func (e *pageAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *pageAIDocumentCompilerError) Unwrap() error { return e.cause }

type AIDocumentMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target"
)

type AIDocumentRevisionConflictError struct {
	Kind                  AIDocumentRevisionConflictKind
	CurrentRevision       string
	CurrentTargetRevision *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return "Page AI document revision changed: current revision is " + e.CurrentRevision
}

// ExecuteAIDocumentMutation is the exact Page-owned Validate/Apply boundary.
// It locks the Page before current-principal and SpiceDB checks, performs one
// Edit decision, and invokes the adapter compiler only after authorization.
// Compilation and persistence share the same transaction and root lock.
func (s *AIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	pageID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.internal == nil || s.internal.db == nil ||
		s.internal.spiceDB == nil || s.internal.contentBlocks == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Page AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Page AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	if err := validatePageAIDocumentIdentity(pageID, locale); err != nil {
		return AIDocumentMutationResult{}, err
	}

	var result contentblock.Result
	var targetRevision *string
	var currentRevision string
	var acceptedMutation AIDocumentMutation
	err := s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadPageAIDocumentRoot(ctx, tx, pageID, "UPDATE")
		if err != nil {
			return err
		}
		principal, err := authorizePagePermissionAfterRootLock(
			ctx, tx, s.internal.spiceDB, pageID, policyv1.Page.Edit,
		)
		if err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return errs.NotFound("page", pageID)
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
		currentRevision = state.Revision
		mutation, err := compiler(state)
		if err != nil {
			return &pageAIDocumentCompilerError{cause: err}
		}
		if err := validateCompiledPageAIDocumentMutation(state, mutation); err != nil {
			return err
		}
		acceptedMutation = mutation
		domain := contentblock.DomainContext{
			SourceLocale: state.SourceLocale,
		}
		result, targetRevision, err = s.applyAIDocumentMutationInTransaction(
			ctx,
			tx,
			mutation,
			pageAuthorizedAIDocumentFence(root.ID, documentID, domain, mutation.ContributorMemberID),
		)
		if err != nil {
			return err
		}
		if result.Changed && mutation.Locale != mutation.ObservedSourceLocale {
			if err := s.appendPageAIDocumentTargetAudit(ctx, tx, mutation); err != nil {
				return err
			}
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackPageAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackPageAIDocumentValidation) {
		return AIDocumentMutationResult{DocumentRevision: result.DocumentRevision.String(), TargetRevision: targetRevision, Changed: result.Changed}, nil
	}
	if err == nil {
		if mode == AIDocumentExecutionApply && result.Changed {
			publishContentUpdatedEvent(ctx, s.internal.asyncPublisher, buildPageContentUpdatedEvent(
				acceptedMutation.PageID,
				pageAIDocumentContentUpdatedFields(acceptedMutation),
				result.DocumentRevision.String(),
				[]string{acceptedMutation.ContributorMemberID},
				managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
				acceptedMutation.Locale,
				!acceptedMutation.DeleteTranslation,
				targetRevision,
				acceptedMutation.Locale == acceptedMutation.ObservedSourceLocale,
			))
		}
		return AIDocumentMutationResult{DocumentRevision: result.DocumentRevision.String(), TargetRevision: targetRevision, Changed: result.Changed}, nil
	}
	var compilerErr *pageAIDocumentCompilerError
	var conflict *AIDocumentRevisionConflictError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return AIDocumentMutationResult{}, compilerErr.cause
	case errors.As(err, &conflict):
		return AIDocumentMutationResult{}, conflict
	case errors.As(err, &stale):
		return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: stale.CurrentRevision.String(),
		}
	case errors.As(err, &targetConflict):
		return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: currentRevision,
			CurrentTargetRevision: pageTargetConflictRevision(targetConflict),
		}
	default:
		return AIDocumentMutationResult{}, normalizePageContentBlockError(err)
	}
}

func pageAIDocumentContentUpdatedFields(mutation AIDocumentMutation) []string {
	fields := make([]string, 0, 3)
	if mutation.Metadata.SetTitle {
		fields = append(fields, "title")
	}
	if mutation.Metadata.SetSummary {
		fields = append(fields, "summary")
	}
	if hasPageAIDocumentBatchChanges(mutation.Batch) || mutation.Metadata.EnsureLocale || mutation.DeleteTranslation {
		fields = append(fields, "content")
	}
	return fields
}

func (s *AIDocumentService) appendPageAIDocumentTargetAudit(ctx context.Context, tx *gorm.DB, mutation AIDocumentMutation) error {
	if s.internal.auditWriter == nil {
		return nil
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if mutation.Metadata.EnsureLocale && !mutation.ObservedLocaleExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	} else if mutation.DeleteTranslation {
		operation = sharedtelemetry.AuditItemOperationDeleted
	}
	return appendPageMemberLocaleContentAudit(
		ctx, tx, s.internal.auditWriter, mutation.ContributorMemberID,
		mutation.PageID, mutation.Locale, operation,
	)
}

func validateCompiledPageAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
) error {
	contributor, err := uuid.Parse(mutation.ContributorMemberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != mutation.ContributorMemberID {
		return errs.InvalidArgument("contributor", "compiled Page contributor must be a canonical Member UUID")
	}
	if mutation.PageID != state.Page.ID || mutation.Locale != state.Locale ||
		mutation.ObservedSourceLocale != state.SourceLocale ||
		mutation.ObservedLocaleExists != state.LocaleExists ||
		mutation.ContributorMemberID != state.ViewerMemberID ||
		mutation.Batch.DocumentID != state.Snapshot.Document.ID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Page identity, locale, contributor, source observation, and content document must match the locked state",
		)
	}
	if mutation.ExpectedRevision.String() != state.Revision {
		return &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: state.Revision}
	}
	if !samePageTargetRevision(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: state.Revision,
			CurrentTargetRevision: cloneOptionalString(state.TargetRevision),
		}
	}
	return nil
}

func samePageTargetRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func pageTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists || conflict.CurrentRevision == "" {
		return nil
	}
	current := conflict.CurrentRevision
	return &current
}

func pageAuthorizedAIDocumentFence(
	pageID string,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
	contributorMemberID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if strings.TrimSpace(pageID) == "" || documentID == uuid.Nil || requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Page content document changed; reload before saving")
		}
		if err := requireDocumentContributors(ctx, tx, []string{contributorMemberID}); err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}
