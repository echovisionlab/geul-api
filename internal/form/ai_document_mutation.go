package form

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ExecuteAIDocumentMutation is Form's exact DCDP mutation boundary. It locks
// the Form root, performs exactly one Form.Edit decision, and only then
// exposes the current authorized state to the adapter compiler. Validation and
// Apply execute the same compiler, CAS, persistence, Audit, and derived-effect
// path; Validation deliberately rolls the transaction back at the end.
func (s *InternalFormService) ExecuteAIDocumentMutation(
	ctx context.Context,
	formID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.translation == nil || s.og == nil {
		return AIDocumentMutationResult{}, errs.Internal(errors.New("form AI document dependencies are not configured"))
	}
	if !IsValidUUID(formID) {
		return AIDocumentMutationResult{}, errs.InvalidArgument("form_id", "must be a canonical UUID")
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return AIDocumentMutationResult{}, errs.Required("locale")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.Internal(errors.New("form AI document compiler is not configured"))
	}

	var output AIDocumentMutationResult
	var input AIDocumentMutation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadFormAIDocumentRoot(ctx, tx, formID, "UPDATE")
		if err != nil {
			return err
		}
		if _, valid := formAIDocumentLifecycle(root.Status); !valid {
			return errs.InternalMsg("Form has an unsupported lifecycle status")
		}
		if err := s.requireFreshFormAction(ctx, tx, formID, formActionEdit); err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return errs.NotFound("form", formID)
			default:
				return err
			}
		}
		principal := auth.GetUser(ctx)
		if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned ||
			principal.MemberID == "" || !IsValidUUID(principal.MemberID.String()) {
			return errs.NotFound("form", formID)
		}
		loaded, err := s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, formID, locale, root, principal.MemberID.String(), true,
		)
		if err != nil {
			return err
		}
		input, err = compiler(loaded.State)
		if err != nil {
			return &formAIDocumentCompilerError{cause: err}
		}
		if err := validateCompiledFormAIDocumentMutation(loaded.State, input); err != nil {
			return err
		}
		if err := validateFormAIDocumentMutation(input); err != nil {
			return err
		}
		changed, err := s.persistAIDocumentMutation(
			ctx, tx, input, root, loaded.Source, loaded.Current, loaded.CurrentExists,
		)
		if err != nil {
			return err
		}
		nextRoot, err := loadFormAIDocumentRoot(ctx, tx, formID, "")
		if err != nil {
			return err
		}
		next, err := s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, formID, locale, nextRoot, principal.MemberID.String(), false,
		)
		if err != nil {
			return err
		}
		output = AIDocumentMutationResult{
			DocumentRevision: next.State.DocumentRevision,
			TargetRevision:   cloneFormRevision(next.State.TargetRevision),
			Changed:          changed,
		}
		if changed {
			if err := s.appendAIDocumentAudit(ctx, tx, input); err != nil {
				return err
			}
			if _, err := s.og.RequestAfterMutation(
				ctx, tx, input.FormID, input.Locale,
				input.Locale == loaded.State.SourceLocale,
				"form_ai_document_saved",
			); err != nil {
				return err
			}
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackFormAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackFormAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *formAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return AIDocumentMutationResult{}, compilerErr.cause
		}
		return AIDocumentMutationResult{}, err
	}
	if mode == AIDocumentExecutionApply && output.Changed && s.asyncPublisher != nil {
		if publishErr := s.asyncPublisher.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, formAIDocumentContentUpdatedEvent(input, output)); publishErr != nil {
			slog.Warn("Failed to publish Form AI document content update", "form_id", input.FormID, "error", publishErr)
		}
	}
	return output, nil
}

func validateCompiledFormAIDocumentMutation(state AIDocumentState, input AIDocumentMutation) error {
	if input.FormID != state.FormID || input.Locale != state.Locale ||
		input.ContributorMemberID != state.ViewerMemberID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Form identity, locale, and contributor must match the locked state",
		)
	}
	if input.ExpectedSource != state.SourceLocale || input.ExpectedPresence != state.LocaleExists {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: cloneFormRevision(state.TargetRevision),
		}
	}
	if input.ExpectedDocumentRevision != state.DocumentRevision {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: cloneFormRevision(state.TargetRevision),
		}
	}
	if !equalFormRevision(input.ExpectedTargetRevision, state.TargetRevision) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: state.DocumentRevision,
			CurrentTargetRevision: cloneFormRevision(state.TargetRevision),
		}
	}
	return nil
}

func cloneFormRevision(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalFormRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *InternalFormService) persistAIDocumentMutation(ctx context.Context, tx *gorm.DB, input AIDocumentMutation, root formAIDocumentRoot, source, current formAIDocumentLocaleRow, exists bool) (bool, error) {
	now := time.Now().UTC()
	sourceLocale := root.SourceLocale
	if input.Noop {
		return false, nil
	}
	if input.CreateTranslation {
		emptySchema, err := formAIDocumentEmptyTargetSchema(source.Schema)
		if err != nil {
			return false, errs.Internal(err)
		}
		result := tx.WithContext(ctx).Exec(`INSERT INTO form_translation (entity_id, locale, title, content_json, created_at, updated_at) VALUES (?::uuid, ?, NULL, CAST(? AS jsonb), ?, ?) ON CONFLICT (entity_id, locale) DO NOTHING`, input.FormID, input.Locale, string(emptySchema), now, now)
		return result.RowsAffected != 0, result.Error
	}
	if input.DeleteTranslation {
		result := tx.WithContext(ctx).Exec("DELETE FROM form_translation WHERE entity_id = ?::uuid AND locale = ?", input.FormID, input.Locale)
		return result.RowsAffected != 0, result.Error
	}
	if !exists {
		if _, err := s.persistAIDocumentMutation(ctx, tx, AIDocumentMutation{FormID: input.FormID, Locale: input.Locale, CreateTranslation: true}, root, source, formAIDocumentLocaleRow{}, false); err != nil {
			return false, err
		}
	}
	if input.Locale == sourceLocale && input.SetSchema {
		if err := validateCanonicalFormSchema(input.Schema); err != nil {
			return false, errs.InvalidArgumentMsg(err.Error())
		}
	}
	schema := input.Schema
	if input.SetSchema && input.Locale != sourceLocale {
		if err := validateFormAIDocumentTargetSchema(source.Schema, input.Schema); err != nil {
			return false, errs.InvalidArgumentMsg(err.Error())
		}
	}
	writeNow := now
	if input.Locale != sourceLocale && exists {
		writeNow = translation.NextTargetUpdatedAt(writeNow, current.UpdatedAt)
	}
	updates := structured.Fields{"updated_at": writeNow}
	if input.SetTitle && (!exists || !formAIDocumentEqualString(current.Title, input.Title)) {
		updates["title"] = input.Title
	}
	if input.SetSchema && (!exists || !formAIDocumentJSONEqual(current.Schema, schema)) {
		updates["content_json"] = schema
		text := strings.TrimSpace(extractFormSchemaTextFromJSON(schema))
		if text == "" {
			updates["content_text"] = nil
		} else {
			updates["content_text"] = text
		}
	}
	if len(updates) == 1 {
		return false, nil
	}
	apply := func(ctx context.Context, tx *gorm.DB) (bool, error) {
		result := tx.WithContext(ctx).Table("form_translation").
			Where("entity_id = ?::uuid AND locale = ?", input.FormID, input.Locale).
			Updates(updates)
		if result.Error != nil {
			return false, errs.Internal(result.Error)
		}
		return result.RowsAffected != 0, nil
	}
	if input.Locale != sourceLocale {
		return apply(ctx, tx)
	}
	documentID, err := uuid.Parse(root.DocumentID)
	if err != nil || documentID == uuid.Nil {
		return false, errs.FailedPrecondition("Form Content Document is not initialized")
	}
	expectedRevision, err := uuid.Parse(input.ExpectedDocumentRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return false, errs.InvalidArgument("expected_document_revision", "must be a canonical UUID")
	}
	advance, err := s.contentBlocks.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			changed, applyErr := apply(ctx, tx)
			if applyErr != nil {
				return contentblock.MetadataEffect{}, applyErr
			}
			return contentblock.MetadataEffect{
				Changed: changed, AffectsTranslationSource: changed,
				ChangedLocales: []string{sourceLocale},
			}, nil
		},
	)
	if err != nil {
		return false, err
	}
	return advance.Changed, nil
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

func (s *InternalFormService) appendAIDocumentAudit(ctx context.Context, tx *gorm.DB, input AIDocumentMutation) error {
	if s.auditWriter == nil {
		return errs.InternalMsg("Form AI document audit writer is not configured")
	}
	if input.Locale != input.ExpectedSource {
		operation := sharedtelemetry.AuditItemOperationUpdated
		if input.CreateTranslation || !input.ExpectedPresence {
			operation = sharedtelemetry.AuditItemOperationCreated
		} else if input.DeleteTranslation {
			operation = sharedtelemetry.AuditItemOperationDeleted
		}
		return domainaudit.AppendMember(
			ctx, tx, s.auditWriter, input.ContributorMemberID,
			sharedtelemetry.AuditFormUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewFormLocaleContentAuditRecord(
					metadata, input.FormID, input.Locale, operation,
				)
			},
		)
	}
	return domainaudit.AppendMember(
		ctx, tx, s.auditWriter, input.ContributorMemberID,
		sharedtelemetry.AuditFormUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormLocaleContentAuditRecord(
				metadata, input.FormID, input.Locale, sharedtelemetry.AuditItemOperationUpdated,
			)
		},
	)
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
