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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[formAIDocumentExecution]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			if root, err = loadFormAIDocumentRoot(ctx, tx, command.FormID, clause.LockingStrengthUpdate); err != nil {
				return err
			}
			if _, valid := formAIDocumentLifecycle(root.Status); !valid {
				return errFormUnsupportedLifecycle
			}
			return nil
		},
		Access: s.formAIDocumentEdit(command.FormID),
		Change: func(ctx context.Context, tx *gorm.DB) (formAIDocumentExecution, error) {
			if err := s.executeAIDocumentMutation(ctx, tx, command, locale, root, &execution); err != nil {
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
		Outbox: func(ctx context.Context, tx *gorm.DB, execution formAIDocumentExecution) error {
			if !execution.result.Changed {
				return nil
			}
			compiled := execution.mutation
			if _, err := s.og.RequestAfterMutation(
				ctx, tx, compiled.FormID, compiled.Locale, compiled.Locale == execution.state.SourceLocale, "form_ai_document_saved",
			); err != nil {
				return err
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.signals, formAIDocumentContentUpdatedEvent(compiled, execution.result))
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
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		notFound := domainerr.ResourceNotFound("form", formID)
		if err := s.requireFreshFormAction(ctx, tx, formID, formActionEdit); err != nil {
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
func (s *Service) executeAIDocumentMutation(
	ctx context.Context,
	tx *gorm.DB,
	command ExecuteAIDocumentMutationCommand,
	locale string,
	root formAIDocumentRoot,
	execution *formAIDocumentExecution,
) error {
	viewer := auth.GetUser(ctx).MemberID.String()
	loaded, err := loadAIDocumentStateAfterAuthorization(ctx, tx, command.FormID, locale, root, viewer, true)
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
	changed, err := persistAIDocumentMutation(ctx, tx, s.contentBlocks, execution.mutation, root, loaded.Source, loaded.Current, loaded.CurrentExists)
	if err != nil {
		return err
	}
	nextRoot, err := loadFormAIDocumentRoot(ctx, tx, command.FormID, "")
	if err != nil {
		return err
	}
	next, err := loadAIDocumentStateAfterAuthorization(ctx, tx, command.FormID, locale, nextRoot, viewer, false)
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
func persistAIDocumentMutation(ctx context.Context, tx *gorm.DB, store *contentblock.Store, input AIDocumentMutation, root formAIDocumentRoot, source, current formAIDocumentLocaleRow, exists bool) (bool, error) {
	now := dbtime.Now()
	switch {
	case input.Noop:
		return false, nil
	case input.CreateTranslation:
		return createFormAIDocumentTranslation(ctx, tx, input, source, now)
	case input.DeleteTranslation:
		result := tx.WithContext(ctx).Exec("DELETE FROM form_translation WHERE entity_id = ?::uuid AND locale = ?", input.FormID, input.Locale)
		return result.RowsAffected != 0, result.Error
	}
	if !exists {
		if _, err := createFormAIDocumentTranslation(ctx, tx, input, source, now); err != nil {
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
	updates := formAIDocumentLocaleUpdates(input, current, exists, writeNow)
	if len(updates) == 1 {
		return false, nil
	}
	apply := func(ctx context.Context, tx *gorm.DB) (bool, error) {
		result := tx.WithContext(ctx).Table("form_translation").
			Where("entity_id = ?::uuid AND locale = ?", input.FormID, input.Locale).
			Updates(updates)
		return result.RowsAffected != 0, result.Error
	}
	if !isSource {
		return apply(ctx, tx)
	}
	expectedRevision, err := uuid.Parse(input.ExpectedDocumentRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return false, domainerr.InvalidField("expected_document_revision", "must be a canonical UUID")
	}
	advance, err := advanceFormSourceLocale(ctx, tx, store, root.DocumentID, expectedRevision, root.SourceLocale, apply)
	if err != nil {
		return false, err
	}
	return advance.Changed, nil
}

// createFormAIDocumentTranslation adds a missing target locale as an empty
// overlay of the source schema.
func createFormAIDocumentTranslation(ctx context.Context, tx *gorm.DB, input AIDocumentMutation, source formAIDocumentLocaleRow, now time.Time) (bool, error) {
	emptySchema, err := formAIDocumentEmptyTargetSchema(source.Schema)
	if err != nil {
		return false, err
	}
	result := tx.WithContext(ctx).Exec(`INSERT INTO form_translation (entity_id, locale, title, content_json, created_at, updated_at) VALUES (?::uuid, ?, NULL, CAST(? AS jsonb), ?, ?) ON CONFLICT (entity_id, locale) DO NOTHING`, input.FormID, input.Locale, string(emptySchema), now, now)
	return result.RowsAffected != 0, result.Error
}

// validateFormAIDocumentSchemaWrite checks a schema write: a source schema
// must be canonical and a target schema may change locale copy only.
func validateFormAIDocumentSchemaWrite(input AIDocumentMutation, source formAIDocumentLocaleRow, isSource bool) error {
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

// formAIDocumentLocaleUpdates is the form_translation update of input; it
// holds only updated_at when nothing changes.
func formAIDocumentLocaleUpdates(input AIDocumentMutation, current formAIDocumentLocaleRow, exists bool, now time.Time) structured.Fields {
	updates := structured.Fields{"updated_at": now}
	if input.SetTitle && (!exists || !formAIDocumentEqualString(current.Title, input.Title)) {
		updates["title"] = input.Title
	}
	if input.SetSchema && (!exists || !formAIDocumentJSONEqual(current.Schema, input.Schema)) {
		updates["content_json"] = input.Schema
		if text := strings.TrimSpace(extractFormSchemaTextFromJSON(input.Schema)); text == "" {
			updates["content_text"] = nil
		} else {
			updates["content_text"] = text
		}
	}
	return updates
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

func formAIDocumentContentUpdatedEvent(input AIDocumentMutation, result AIDocumentMutationResult) *managev1.ContentUpdatedEvent {
	if !result.Changed {
		return nil
	}
	fields := make([]*managev1.ContentUpdatedField, 0, 2)
	textKind := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT
	if input.SetTitle {
		fields = append(fields, &managev1.ContentUpdatedField{Path: "title", Kind: textKind})
	}
	if input.SetSchema || input.CreateTranslation || input.DeleteTranslation {
		fields = append(fields, &managev1.ContentUpdatedField{Path: "document.content", Kind: textKind})
	}
	if len(fields) == 0 {
		return nil
	}
	revision := result.DocumentRevision
	locale := input.Locale
	localeExists := !input.DeleteTranslation
	event := &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM,
		EntityId:             input.FormID,
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		ChangedFields:        fields,
		ContributorMemberIds: []string{input.ContributorMemberID},
		DocumentRevision:     &revision,
		DocumentStateChanged: input.Locale == input.ExpectedSource,
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TimestampMs:          time.Now().UnixMilli(),
	}
	if input.Locale != input.ExpectedSource && localeExists && result.TargetRevision != nil {
		targetRevision := *result.TargetRevision
		event.TargetRevision = &targetRevision
	}
	return event
}
