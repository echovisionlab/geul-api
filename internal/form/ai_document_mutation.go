package form

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ExecuteAIDocumentMutationCommand runs Compile against the locked state of
// one Form locale and validates or applies its mutation.
type ExecuteAIDocumentMutationCommand struct {
	FormID  string
	Locale  string
	Mode    AIDocumentExecutionMode
	Compile AIDocumentMutationCompiler
}

// formAIDocumentExecution is what one AI document mutation compiled and
// what its change produced.
type formAIDocumentExecution struct {
	state    AIDocumentState
	mutation AIDocumentMutation
	result   AIDocumentMutationResult
}

// ExecuteAIDocumentMutation is Form's exact DCDP mutation boundary. It locks
// the Form root, performs exactly one Form.Edit decision, and only then
// exposes the current authorized state to the adapter compiler. Validation and
// Apply execute the same compiler, CAS, persistence, Audit, and derived-effect
// path; Validation deliberately rolls the transaction back at the end.
func (s *Service) ExecuteAIDocumentMutation(ctx context.Context, command ExecuteAIDocumentMutationCommand) (AIDocumentMutationResult, error) {
	if err := validateFormAIDocumentCommand(command); err != nil {
		return AIDocumentMutationResult{}, err
	}
	locale := strings.TrimSpace(command.Locale)
	var root formAIDocumentRoot
	var execution formAIDocumentExecution
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[formAIDocumentExecution, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			if root, err = loadFormAIDocumentRoot(ctx, store, command.FormID, UpdateLock); err != nil {
				return err
			}
			if !root.Status.Known() {
				return errFormUnsupportedLifecycle
			}
			return nil
		},
		Access: s.formAIDocumentEdit(command.FormID),
		Change: func(ctx context.Context, store Store) (formAIDocumentExecution, error) {
			if err := executeAIDocumentMutation(ctx, store, command, locale, root, &execution); err != nil {
				return formAIDocumentExecution{}, err
			}
			return execution, nil
		},
		Audit: func(execution formAIDocumentExecution) mutation.Audit {
			if !execution.result.Changed {
				return mutation.Unchanged()
			}
			return formAIDocumentAudit(execution.mutation)
		},
		Outbox: func(ctx context.Context, store Store, execution formAIDocumentExecution) error {
			if !execution.result.Changed {
				return nil
			}
			compiled := execution.mutation
			if _, err := store.RequestOG(
				ctx, compiled.FormID, compiled.Locale, compiled.Locale == execution.state.SourceLocale, "form_ai_document_saved",
			); err != nil {
				return err
			}
			return store.PublishContentUpdate(ctx, formAIDocumentContentUpdate(compiled, execution.result))
		},
	})
	if err == nil || errors.Is(err, errRollbackFormAIDocumentValidation) {
		return execution.result, nil
	}
	var compilerErr *formAIDocumentCompilerError
	if errors.As(err, &compilerErr) {
		err = compilerErr.cause
	}
	return AIDocumentMutationResult{}, failurecause.WithAttrs(err, slog.String("entity_type", "form"), slog.String("entity_id", command.FormID), slog.String("locale", locale))
}

func validateFormAIDocumentCommand(command ExecuteAIDocumentMutationCommand) error {
	if !IsValidUUID(command.FormID) {
		return domainerr.InvalidField("form_id", "must be a canonical UUID")
	}
	if strings.TrimSpace(command.Locale) == "" {
		return domainerr.Required("locale")
	}
	if command.Mode != AIDocumentExecutionValidate && command.Mode != AIDocumentExecutionApply {
		return domainerr.InvalidField("mode", "is not supported")
	}
	if command.Compile == nil {
		return domainerr.DependencyUnavailable("Form AI document compiler")
	}
	return nil
}

// formAIDocumentEdit is the access of an AI document mutation: a caller who
// may not edit the Form, or who is not an onboarded Member, finds none.
func (s *Service) formAIDocumentEdit(formID string) mutation.Access {
	return mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		notFound := domainerr.ResourceNotFound("form", formID)
		if err := s.requireFreshFormAction(ctx, lockActive, formID, formActionEdit); err != nil {
			if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
				return notFound.WithCause(err)
			}
			return err
		}
		principal := auth.GetUser(ctx)
		if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned ||
			principal.MemberID == "" || !IsValidUUID(principal.MemberID.String()) {
			return notFound
		}
		return nil
	})
}

// executeAIDocumentMutation compiles the mutation against the locked state,
// persists it and reads the revisions it produced into execution.
// Validation rolls the transaction back.
func executeAIDocumentMutation(
	ctx context.Context,
	store Store,
	command ExecuteAIDocumentMutationCommand,
	locale string,
	root formAIDocumentRoot,
	execution *formAIDocumentExecution,
) error {
	viewer := auth.GetUser(ctx).MemberID.String()
	loaded, err := loadAIDocumentStateAfterAuthorization(ctx, store, command.FormID, locale, root, viewer, true)
	if err != nil {
		return err
	}
	execution.state = loaded.State
	if execution.mutation, err = command.Compile(loaded.State); err != nil {
		return &formAIDocumentCompilerError{cause: err}
	}
	if err := validateCompiledFormAIDocumentMutation(loaded.State, execution.mutation); err != nil {
		return err
	}
	if err := validateFormAIDocumentMutation(execution.mutation); err != nil {
		return err
	}
	changed, err := persistAIDocumentMutation(ctx, store, execution.mutation, root, loaded.Source, loaded.Current, loaded.CurrentExists)
	if err != nil {
		return err
	}
	nextRoot, err := loadFormAIDocumentRoot(ctx, store, command.FormID, Unlocked)
	if err != nil {
		return err
	}
	next, err := loadAIDocumentStateAfterAuthorization(ctx, store, command.FormID, locale, nextRoot, viewer, false)
	if err != nil {
		return err
	}
	execution.result = AIDocumentMutationResult{
		DocumentRevision: next.State.DocumentRevision,
		TargetRevision:   optional.Clone(next.State.TargetRevision),
		Changed:          changed,
	}
	if command.Mode == AIDocumentExecutionValidate {
		return errRollbackFormAIDocumentValidation
	}
	return nil
}

func validateCompiledFormAIDocumentMutation(state AIDocumentState, input AIDocumentMutation) error {
	if input.FormID != state.FormID || input.Locale != state.Locale ||
		input.ContributorMemberID != state.ViewerMemberID {
		return domainerr.InvalidField(
			"mutation",
			"compiled Form identity, locale, and contributor must match the locked state",
		)
	}
	if input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: optional.Clone(state.TargetRevision),
		}
	}
	if input.ExpectedDocumentRevision != state.DocumentRevision {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: optional.Clone(state.TargetRevision),
		}
	}
	if !optional.Equal(input.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: optional.Clone(state.TargetRevision),
		}
	}
	return nil
}

// persistAIDocumentMutation writes input over the locked locale rows and
// reports whether a row changed. A source locale write advances the Content
// Document revision.
func persistAIDocumentMutation(ctx context.Context, store Store, input AIDocumentMutation, root formAIDocumentRoot, source, current Locale, exists bool) (bool, error) {
	now := dbtime.Now()
	switch {
	case input.Noop:
		return false, nil
	case input.CreateTranslation:
		return createFormAIDocumentTranslation(ctx, store, input, source, now)
	case input.DeleteTranslation:
		return store.DeleteLocale(ctx, input.FormID, input.Locale)
	}
	if !exists {
		if _, err := createFormAIDocumentTranslation(ctx, store, input, source, now); err != nil {
			return false, err
		}
	}
	isSource := input.Locale == root.SourceLocale
	if err := validateFormAIDocumentSchemaWrite(input, source, isSource); err != nil {
		return false, err
	}
	writeNow := now
	if !isSource && exists {
		writeNow = translation.NextTargetUpdatedAt(writeNow, current.UpdatedAt)
	}
	change, changed := formAIDocumentLocaleChange(input, current, exists, writeNow)
	if !changed {
		return false, nil
	}
	apply := func(ctx context.Context, store Store) (bool, error) {
		return store.UpdateLocale(ctx, input.FormID, input.Locale, change)
	}
	if !isSource {
		return apply(ctx, store)
	}
	expectedRevision, err := uuid.Parse(input.ExpectedDocumentRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return false, domainerr.InvalidField("expected_document_revision", "must be a canonical UUID")
	}
	advance, err := advanceFormSourceLocale(ctx, store, root.DocumentID, expectedRevision, root.SourceLocale, apply)
	if err != nil {
		return false, err
	}
	return advance.Changed, nil
}

// createFormAIDocumentTranslation adds a missing target locale as an empty
// overlay of the source schema.
func createFormAIDocumentTranslation(ctx context.Context, store Store, input AIDocumentMutation, source Locale, now time.Time) (bool, error) {
	emptySchema, err := formAIDocumentEmptyTargetSchema(source.Schema)
	if err != nil {
		return false, err
	}
	return store.CreateEmptyLocale(ctx, input.FormID, input.Locale, emptySchema, now)
}

// validateFormAIDocumentSchemaWrite checks a schema write: a source schema
// must be canonical and a target schema may change locale copy only.
func validateFormAIDocumentSchemaWrite(input AIDocumentMutation, source Locale, isSource bool) error {
	if !input.SetSchema {
		return nil
	}
	var err error
	if isSource {
		err = validateCanonicalFormSchema(input.Schema)
	} else {
		err = validateFormAIDocumentTargetSchema(source.Schema, input.Schema)
	}
	if err != nil {
		return domainerr.New(domainerr.Invalid, "", err.Error())
	}
	return nil
}

// formAIDocumentLocaleChange is the locale write of input, and false when
// nothing changes.
func formAIDocumentLocaleChange(input AIDocumentMutation, current Locale, exists bool, now time.Time) (LocaleChange, bool) {
	change := LocaleChange{UpdatedAt: now}
	if input.SetTitle && (!exists || !formAIDocumentEqualString(current.Title, input.Title)) {
		change.SetTitle, change.Title = true, input.Title
	}
	if input.SetSchema && (!exists || !formAIDocumentJSONEqual(current.Schema, input.Schema)) {
		change.SetSchema, change.Schema = true, input.Schema
		if text := strings.TrimSpace(extractFormSchemaTextFromJSON(input.Schema)); text != "" {
			change.ContentText = &text
		}
	}
	return change, change.SetTitle || change.SetSchema
}

func formAIDocumentEqualString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func formAIDocumentJSONEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func extractFormSchemaTextFromJSON(schema []byte) string {
	var value formDocumentObject
	if len(schema) == 0 || json.Unmarshal(schema, &value) != nil {
		return ""
	}
	return extractFormSchemaText(formValueSlice(value["steps"]))
}

// formAIDocumentAudit records the changed locale content for the
// contributor, as a collaboration save does.
func formAIDocumentAudit(input AIDocumentMutation) mutation.Audit {
	return mutation.MemberAudit(input.ContributorMemberID, sharedtelemetry.AuditFormUpdated,
		formLocaleContentRecord(input.FormID, input.Locale, formAIDocumentAuditOperation(input)))
}

func formAIDocumentAuditOperation(input AIDocumentMutation) sharedtelemetry.AuditItemOperation {
	switch {
	case input.Locale == input.ExpectedSource:
		return sharedtelemetry.AuditItemOperationUpdated
	case input.CreateTranslation || !input.ExpectedPresence:
		return sharedtelemetry.AuditItemOperationCreated
	case input.DeleteTranslation:
		return sharedtelemetry.AuditItemOperationDeleted
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
}

// formAIDocumentContentUpdate is the content.updated signal of a changed
// locale.
func formAIDocumentContentUpdate(input AIDocumentMutation, result AIDocumentMutationResult) ContentUpdate {
	fields := make([]string, 0, 2)
	if input.SetTitle {
		fields = append(fields, "title")
	}
	if input.SetSchema || input.CreateTranslation || input.DeleteTranslation {
		fields = append(fields, "document.content")
	}
	update := ContentUpdate{
		FormID: input.FormID, Locale: input.Locale, Fields: fields, DocumentRevision: result.DocumentRevision,
		Contributor: input.ContributorMemberID, SourceLocale: input.Locale == input.ExpectedSource,
		LocaleDeleted: input.DeleteTranslation,
	}
	if input.Locale != input.ExpectedSource && !input.DeleteTranslation && result.TargetRevision != nil {
		targetRevision := *result.TargetRevision
		update.TargetRevision = &targetRevision
	}
	return update
}
