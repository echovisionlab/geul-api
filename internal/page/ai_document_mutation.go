package page

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/translation"
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

// ExecuteAIDocumentMutationCommand runs Compile against the locked state
// of one Page locale and validates or applies its mutation.
type ExecuteAIDocumentMutationCommand struct {
	PageID  string
	Locale  string
	Mode    AIDocumentExecutionMode
	Compile AIDocumentMutationCompiler
}

var errRollbackPageAIDocumentValidation = errors.New("rollback Page AI document validation")

// pageAIDocumentExecution is what one AI document mutation observed and
// applied.
type pageAIDocumentExecution struct {
	state    AIDocumentState
	mutation AIDocumentMutation
	applied  pageAIDocumentApply
}

// ExecuteAIDocumentMutation is the exact Page-owned Validate/Apply boundary.
// It locks the Page before current-principal and SpiceDB checks, performs one
// Edit decision, and invokes the adapter compiler only after authorization.
// Compilation and persistence share the same transaction and root lock.
func (s *Service) ExecuteAIDocumentMutation(ctx context.Context, command ExecuteAIDocumentMutationCommand) (AIDocumentMutationResult, error) {
	if command.Compile == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Page AI document compiler")
	}
	if command.Mode != AIDocumentExecutionValidate && command.Mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	if err := validatePageAIDocumentIdentity(command.PageID, command.Locale); err != nil {
		return AIDocumentMutationResult{}, err
	}

	var root lockedAIDocumentRoot
	var execution pageAIDocumentExecution
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[pageAIDocumentExecution, Store]{
		Access: lockedAIDocumentEdit(command.PageID, &root),
		Change: func(ctx context.Context, store Store) (pageAIDocumentExecution, error) {
			if err := executeAIDocumentMutation(ctx, store, command, root, &execution); err != nil {
				return pageAIDocumentExecution{}, err
			}
			return execution, nil
		},
		Audit: func(execution pageAIDocumentExecution) mutation.Audit {
			return pageAIDocumentAudit(execution.mutation, execution.applied)
		},
		Outbox: func(ctx context.Context, store Store, execution pageAIDocumentExecution) error {
			if !execution.applied.result.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, pageAIDocumentContentUpdate(execution.mutation, execution.applied))
		},
	})
	result := AIDocumentMutationResult{
		DocumentRevision: execution.applied.result.DocumentRevision.String(),
		TargetRevision:   execution.applied.targetRevision, Changed: execution.applied.result.Changed,
	}
	if err == nil || errors.Is(err, errRollbackPageAIDocumentValidation) {
		return result, nil
	}
	return AIDocumentMutationResult{}, pageAIDocumentExecutionError(err, execution.state.Revision)
}

// lockedAIDocumentEdit is the access of an AI document mutation. A caller
// who may not edit the Page finds none.
func lockedAIDocumentEdit(pageID string, root *lockedAIDocumentRoot) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		err := lockAIDocumentRoot(ctx, store, require, pageID, UpdateLock, policyv1.Page.Edit, root)
		if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
			return domainerr.ResourceNotFound("page", pageID).WithCause(err)
		}
		return err
	})
}

// executeAIDocumentMutation compiles the mutation against the locked state
// and applies it into execution. Validation rolls the transaction back.
func executeAIDocumentMutation(
	ctx context.Context,
	store Store,
	command ExecuteAIDocumentMutationCommand,
	root lockedAIDocumentRoot,
	execution *pageAIDocumentExecution,
) error {
	var err error
	if execution.state, err = loadAIDocumentStateAfterAuthorization(ctx, store, root, command.Locale); err != nil {
		return err
	}
	if execution.mutation, err = command.Compile(execution.state); err != nil {
		return &pageAIDocumentCompilerError{cause: err}
	}
	if err := validateCompiledPageAIDocumentMutation(execution.state, execution.mutation); err != nil {
		return err
	}
	fence := pageAuthorizedAIDocumentFence(root.page.ID, root.documentID,
		contentblock.DomainContext{SourceLocale: execution.state.SourceLocale}, execution.mutation.ContributorMemberID)
	if execution.applied, err = applyAIDocumentMutationInTransaction(ctx, store, execution.mutation, fence); err != nil {
		return err
	}
	if command.Mode == AIDocumentExecutionValidate {
		return errRollbackPageAIDocumentValidation
	}
	// Like the editor, a new title requests that locale's OG image. Public
	// reads show a translation only once its OG generation exists.
	if !execution.applied.result.Changed || !pageAIDocumentTitleChanged(execution.state, execution.mutation) {
		return nil
	}
	_, err = store.RequestOG(ctx, execution.mutation.PageID, execution.mutation.Locale, false, "page_ai_document_saved")
	return err
}

// pageAIDocumentAudit records the changed locale content for its
// contributor, as a collaboration save does, and a changed document layout.
func pageAIDocumentAudit(compiled AIDocumentMutation, applied pageAIDocumentApply) mutation.Audit {
	records := []mutation.Audit{}
	source := compiled.Locale == compiled.ObservedSourceLocale
	localeChanged := !source || applied.metadataChanged || hasPageAIDocumentBatchChanges(compiled.Batch)
	if applied.result.Changed && localeChanged {
		operation := sharedtelemetry.AuditItemOperationUpdated
		if compiled.Metadata.EnsureLocale && !compiled.ObservedLocaleExists {
			operation = sharedtelemetry.AuditItemOperationCreated
		} else if compiled.DeleteTranslation {
			operation = sharedtelemetry.AuditItemOperationDeleted
		}
		records = append(records, localeContentAudit(compiled.ContributorMemberID, compiled.PageID, compiled.Locale, operation))
	}
	if applied.layoutChanged {
		records = append(records, pageLayoutAudit(compiled.PageID))
	}
	return mutation.Audits(records...)
}

func pageAIDocumentContentUpdate(compiled AIDocumentMutation, applied pageAIDocumentApply) ContentUpdate {
	return ContentUpdate{PageID: compiled.PageID, Kind: LocaleUpdate, Locale: LocaleUpdateChange{
		Fields:               pageAIDocumentContentUpdatedFields(compiled),
		DocumentRevision:     applied.result.DocumentRevision.String(),
		Contributors:         []string{compiled.ContributorMemberID},
		Source:               SourceAI,
		Locale:               compiled.Locale,
		LocaleDeleted:        compiled.DeleteTranslation,
		TargetRevision:       applied.targetRevision,
		DocumentStateChanged: compiled.Locale == compiled.ObservedSourceLocale,
	}}
}

// pageAIDocumentExecutionError answers a failed AI document mutation: the
// compiler's own error, a revision conflict the adapter reports, or a
// content store failure.
func pageAIDocumentExecutionError(err error, currentRevision string) error {
	var compilerErr *pageAIDocumentCompilerError
	var conflict *AIDocumentRevisionConflictError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return compilerErr.cause
	case errors.As(err, &conflict):
		return conflict
	case errors.As(err, &stale):
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: stale.CurrentRevision.String(),
		}
	case errors.As(err, &targetConflict):
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: currentRevision,
			CurrentTargetRevision: pageTargetConflictRevision(targetConflict),
		}
	default:
		return normalizePageContentBlockError(err)
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
	if mutation.Metadata.SetDocumentLayout {
		fields = append(fields, "documentLayout")
	}
	if hasPageAIDocumentBatchChanges(mutation.Batch) || mutation.Metadata.EnsureLocale || mutation.DeleteTranslation {
		fields = append(fields, "content")
	}
	return fields
}

func validateCompiledPageAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
) error {
	contributor, err := uuid.Parse(mutation.ContributorMemberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != mutation.ContributorMemberID {
		return domainerr.InvalidField("contributor", "compiled Page contributor must be a canonical Member UUID").WithCause(err)
	}
	if mutation.PageID != state.Page.ID || mutation.Locale != state.Locale ||
		mutation.ObservedSourceLocale != state.SourceLocale ||
		mutation.ObservedLocaleExists != state.LocaleExists ||
		mutation.ContributorMemberID != state.ViewerMemberID ||
		mutation.Batch.DocumentID != state.Snapshot.Document.ID {
		return domainerr.InvalidField(
			"mutation",
			"compiled Page identity, locale, contributor, source observation, and content document must match the locked state",
		)
	}
	if mutation.ExpectedRevision.String() != state.Revision {
		return &AIDocumentRevisionConflictError{Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: state.Revision}
	}
	if !optional.Equal(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentRevision: state.Revision,
			CurrentTargetRevision: cloneOptionalString(state.TargetRevision),
		}
	}
	return nil
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
) ContentFence {
	return func(ctx context.Context, store Store, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if strings.TrimSpace(pageID) == "" || documentID == uuid.Nil || requestedDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Page content document changed; reload before saving")
		}
		if err := requireDocumentContributors(ctx, store, []string{contributorMemberID}); err != nil {
			return contentblock.DomainContext{}, err
		}
		return domain, nil
	}
}

// pageAIDocumentTitleChanged reports whether the mutation saved a different
// title for its locale, including the first title of a new translation.
func pageAIDocumentTitleChanged(state AIDocumentState, mutation AIDocumentMutation) bool {
	if !mutation.Metadata.SetTitle || mutation.DeleteTranslation {
		return false
	}
	if !state.LocaleExists {
		return true
	}
	return optionalPageAIDocumentText(state.Title) != optionalPageAIDocumentText(mutation.Metadata.Title)
}

func optionalPageAIDocumentText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
