package form

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SaveDocument persists only the Form room's canonical locale product state.
// The Y.Doc passed between Collab replicas is transient and is never a storage
// or semantic-change authority.
func (s *InternalFormService) SaveDocument(
	ctx context.Context,
	req *connect.Request[intrav1.SaveFormDocumentRequest],
) (_ *connect.Response[intrav1.SaveFormDocumentResponse], rpcErr error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	defer failurecause.Annotate(&rpcErr, slog.String("form_id", req.Msg.GetFormId()))
	save, err := parseFormDocumentSave(req.Msg)
	if err != nil {
		return nil, err
	}

	now := dbtime.Now()
	var result formCollaborativeSaveResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = s.saveFormDocument(ctx, tx, save, now)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		err = normalizeFormDocumentSaveError(err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("form", save.formID)
		}
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.SaveFormDocumentResponse{
		Success:          true,
		Locale:           result.locale,
		DocumentRevision: result.documentRevision,
		TargetRevision:   result.targetRevision,
	}), nil
}

// formDocumentSave is a validated SaveDocument request.
type formDocumentSave struct {
	formID                   string
	locale                   string
	expectedDocumentRevision uuid.UUID
	expectedTargetRevision   *string
	contributorMemberIDs     []string
	title                    *string
	schema                   []byte
}

func parseFormDocumentSave(r *intrav1.SaveFormDocumentRequest) (formDocumentSave, error) {
	save := formDocumentSave{
		formID:                 r.FormId,
		locale:                 strings.TrimSpace(r.Locale),
		expectedTargetRevision: r.ExpectedTargetRevision,
		contributorMemberIDs:   r.ContributorMemberIds,
	}
	if save.locale == "" {
		return save, errs.Required("locale")
	}
	var err error
	save.expectedDocumentRevision, err = uuid.Parse(strings.TrimSpace(r.ExpectedDocumentRevision))
	if err != nil || save.expectedDocumentRevision == uuid.Nil || save.expectedDocumentRevision.String() != r.ExpectedDocumentRevision {
		return save, errs.InvalidArgument("expected_document_revision", "must be a canonical UUID")
	}
	save.title, save.schema, err = formCanonicalDocumentFromMeta(r.Meta)
	if err != nil {
		return save, errs.InvalidArgument("meta", err.Error())
	}
	if err := validateFormCanonicalLocalePresence(save.title, save.schema, r.PresentLocaleValues); err != nil {
		return save, errs.InvalidArgument("present_locale_values", err.Error())
	}
	return save, nil
}

func (s *InternalFormService) saveFormDocument(
	ctx context.Context,
	tx *gorm.DB,
	save formDocumentSave,
	now time.Time,
) (formCollaborativeSaveResult, error) {
	if err := s.translation.LockRoot(ctx, tx, save.formID); err != nil {
		return formCollaborativeSaveResult{}, err
	}
	root, err := loadFormAIDocumentRoot(ctx, tx, save.formID, "")
	if err != nil {
		return formCollaborativeSaveResult{}, err
	}
	if root.DocumentRevision != save.expectedDocumentRevision.String() {
		return formCollaborativeSaveResult{}, formDocumentRevisionChangedConflict(root.DocumentRevision)
	}
	contributorMemberID, err := canonicalFormCollaborationContributor(save.contributorMemberIDs)
	if err != nil {
		return formCollaborativeSaveResult{}, err
	}
	if err := s.translation.RequireMutationContributor(
		ctx,
		tx,
		s.spiceDB,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_FORM,
		save.formID,
		contributorMemberID,
	); err != nil {
		return formCollaborativeSaveResult{}, err
	}

	current, exists, err := loadFormAIDocumentLocale(ctx, tx, save.formID, save.locale, true)
	if err != nil {
		return formCollaborativeSaveResult{}, err
	}
	if !exists {
		return formCollaborativeSaveResult{}, errs.FailedPrecondition("Form locale document does not exist")
	}
	isSource := save.locale == root.SourceLocale
	writeNow := now
	if isSource {
		if save.expectedTargetRevision != nil {
			return formCollaborativeSaveResult{}, errs.InvalidArgument("expected_target_revision", "source locale has no target revision")
		}
	} else {
		if err := validateFormTargetSave(ctx, tx, root, current, save); err != nil {
			return formCollaborativeSaveResult{}, err
		}
		writeNow = translation.NextTargetUpdatedAt(writeNow, current.UpdatedAt)
	}

	update, titleChanged := formCollaborativeLocaleUpdates(current, save.title, save.schema, writeNow)
	changed, documentRevision, err := s.writeFormLocale(ctx, tx, root, save, update, writeNow)
	if err != nil {
		return formCollaborativeSaveResult{}, err
	}
	if changed && titleChanged {
		if _, err := s.og.RequestAfterMutation(
			ctx, tx, save.formID, save.locale, isSource, "form_document_saved",
		); err != nil {
			return formCollaborativeSaveResult{}, err
		}
	}
	if changed {
		if err := s.appendFormCollaborativeLocaleContentAudit(
			ctx, tx, contributorMemberID, save.formID, save.locale,
		); err != nil {
			return formCollaborativeSaveResult{}, err
		}
	}
	result := formCollaborativeSaveResult{locale: save.locale, documentRevision: documentRevision}
	if !isSource {
		result.targetRevision, err = savedFormTargetRevision(ctx, tx, save, documentRevision)
		if err != nil {
			return formCollaborativeSaveResult{}, err
		}
	}
	return result, nil
}

// validateFormTargetSave checks a target locale save against the caller's
// target revision and the source schema it translates.
func validateFormTargetSave(
	ctx context.Context,
	tx *gorm.DB,
	root formAIDocumentRoot,
	current formAIDocumentLocaleRow,
	save formDocumentSave,
) error {
	currentTargetRevision, err := deriveFormTargetRevision(root.DocumentRevision, current)
	if err != nil {
		return errs.Internal(err)
	}
	if err := translation.ValidateExpectedTargetRevision(
		save.expectedTargetRevision, currentTargetRevision, true,
	); err != nil {
		return translation.WithDocumentRevision(err, root.DocumentRevision)
	}
	source, sourceExists, err := loadFormAIDocumentLocale(ctx, tx, save.formID, root.SourceLocale, false)
	if err != nil {
		return err
	}
	if !sourceExists {
		return errs.FailedPrecondition("Form source locale document is missing")
	}
	if err := validateFormAIDocumentTargetSchema(source.Schema, save.schema); err != nil {
		return errs.InvalidArgument("meta.schema", err.Error())
	}
	return nil
}

// writeFormLocale applies update to the saved locale. A source locale write
// advances the Content Document revision; a target write leaves it as is.
func (s *InternalFormService) writeFormLocale(
	ctx context.Context,
	tx *gorm.DB,
	root formAIDocumentRoot,
	save formDocumentSave,
	update formCollaborativeLocaleUpdate,
	now time.Time,
) (changed bool, documentRevision string, err error) {
	apply := func(ctx context.Context, tx *gorm.DB) (bool, error) {
		return applyFormCollaborativeLocaleUpdates(ctx, tx, save.formID, save.locale, update, now)
	}
	if save.locale != root.SourceLocale {
		changed, err = apply(ctx, tx)
		return changed, root.DocumentRevision, err
	}
	documentID, parseErr := uuid.Parse(root.DocumentID)
	if parseErr != nil || documentID == uuid.Nil {
		return false, "", errs.WithCause(errs.FailedPrecondition("Form Content Document is not initialized"), failurecause.ServerFault(parseErr))
	}
	advance, err := s.contentBlocks.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{
			DocumentID: documentID, ExpectedRevision: save.expectedDocumentRevision,
		},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: root.SourceLocale}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			applied, applyErr := apply(ctx, tx)
			if applyErr != nil {
				return contentblock.MetadataEffect{}, applyErr
			}
			return contentblock.MetadataEffect{
				Changed:                  applied,
				AffectsTranslationSource: applied,
				ChangedLocales:           []string{save.locale},
			}, nil
		},
	)
	if err != nil {
		return false, "", err
	}
	return advance.Changed, advance.DocumentRevision.String(), nil
}

func savedFormTargetRevision(
	ctx context.Context,
	tx *gorm.DB,
	save formDocumentSave,
	documentRevision string,
) (*string, error) {
	updated, updatedExists, err := loadFormAIDocumentLocale(ctx, tx, save.formID, save.locale, false)
	if err != nil {
		return nil, err
	}
	if !updatedExists {
		return nil, errs.FailedPrecondition("Form locale document disappeared")
	}
	targetRevision, err := deriveFormTargetRevision(documentRevision, updated)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return &targetRevision, nil
}

func formDocumentRevisionChangedConflict(revisions ...string) error {
	current := ""
	if len(revisions) > 0 {
		current = revisions[0]
	}
	return errs.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		"Form Content Document revision changed", errs.CollaborationState{Document: current},
	)
}

func normalizeFormDocumentSaveError(err error) error {
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			targetConflict.Error(), err,
		)
	}
	if dberrors.IsSerializationFailure(err) {
		return connect.NewError(connect.CodeAborted, err)
	}
	return err
}

func canonicalFormCollaborationContributor(values []string) (string, error) {
	if len(values) != 1 {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"Form collaboration mutation requires exactly one origin Member",
		)
	}
	value := strings.TrimSpace(values[0])
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"Form collaboration mutation requires one canonical Member UUID",
		)
	}
	return value, nil
}

type formCollaborativeSaveResult struct {
	locale           string
	documentRevision string
	targetRevision   *string
}

// formCollaborativeLocaleUpdate is what a collaborative save changes in one
// form_translation row.
type formCollaborativeLocaleUpdate struct {
	titleChanged  bool
	title         *string
	schemaChanged bool
	schema        []byte
	updatedAt     time.Time
}

func formCollaborativeLocaleUpdates(
	current formAIDocumentLocaleRow,
	title *string,
	schema []byte,
	now time.Time,
) (formCollaborativeLocaleUpdate, bool) {
	update := formCollaborativeLocaleUpdate{
		titleChanged:  !formAIDocumentEqualString(current.Title, title),
		title:         title,
		schemaChanged: !formAIDocumentJSONEqual(current.Schema, schema),
		schema:        schema,
		updatedAt:     now,
	}
	return update, update.titleChanged
}

// columns is the form_translation column update, empty when nothing changed.
func (u formCollaborativeLocaleUpdate) columns() structured.Fields {
	columns := structured.Fields{}
	if u.titleChanged {
		columns["title"] = u.title
	}
	if u.schemaChanged {
		columns["content_json"] = u.schema
		columns["content_text"] = formCanonicalContentText(u.schema)
	}
	if len(columns) != 0 {
		columns["updated_at"] = u.updatedAt
	}
	return columns
}

func applyFormCollaborativeLocaleUpdates(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	locale string,
	update formCollaborativeLocaleUpdate,
	now time.Time,
) (bool, error) {
	columns := update.columns()
	if len(columns) == 0 {
		return false, nil
	}
	result := tx.WithContext(ctx).Table("form_translation").
		Where("entity_id = ?::uuid AND locale = ?", formID, locale).
		Updates(columns)
	if result.Error != nil {
		return false, errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return false, errs.FailedPrecondition("Form locale document disappeared")
	}
	if err := tx.WithContext(ctx).Table("form").Where("id = ?::uuid", formID).
		Update("updated_at", now).Error; err != nil {
		return false, errs.Internal(err)
	}
	return true, nil
}

// LoadDocument returns only canonical persisted Form values. Collab turns this
// response into a transient Y.Doc and cannot revive historical Yjs bytes.
func (s *InternalFormService) LoadDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadFormDocumentRequest],
) (*connect.Response[intrav1.LoadFormDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	locale := strings.TrimSpace(req.Msg.Locale)
	if locale == "" {
		return nil, errs.Required("locale")
	}
	var response *intrav1.LoadFormDocumentResponse
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadFormAIDocumentRoot(ctx, tx, req.Msg.FormId, "")
		if err != nil {
			return err
		}
		source, sourceExists, err := loadFormAIDocumentLocale(
			ctx, tx, req.Msg.FormId, root.SourceLocale, false,
		)
		if err != nil {
			return err
		}
		if !sourceExists {
			return errs.FailedPrecondition("Form source locale document is missing")
		}
		sourceMeta, err := formMetaFromCanonicalRow(source)
		if err != nil {
			return errs.WithCause(errs.FailedPrecondition("Form source locale document is invalid"), failurecause.ServerFault(err))
		}

		requested := source
		requestedExists := true
		if locale != root.SourceLocale {
			requested, requestedExists, err = loadFormAIDocumentLocale(
				ctx, tx, req.Msg.FormId, locale, false,
			)
			if err != nil {
				return err
			}
		}
		var requestedMeta *intrav1.FormMeta
		var presentLocaleValues []*managev1.AIDocumentFieldTarget
		var targetRevision *string
		if requestedExists {
			requestedMeta, err = formMetaFromCanonicalRow(requested)
			if err != nil {
				return errs.WithCause(errs.FailedPrecondition("Form requested locale document is invalid"), failurecause.ServerFault(err))
			}
			if locale != root.SourceLocale {
				if err := validateFormAIDocumentTargetSchema(source.Schema, requested.Schema); err != nil {
					return errs.WithCause(errs.FailedPrecondition("Form requested locale document is invalid"), failurecause.ServerFault(err))
				}
			}
			presentLocaleValues, err = formCanonicalLocaleTargets(
				requested.Title, requested.Schema,
			)
			if err != nil {
				return errs.WithCause(errs.FailedPrecondition("Form requested locale document is invalid"), failurecause.ServerFault(err))
			}
			if locale != root.SourceLocale {
				revision, err := deriveFormTargetRevision(root.DocumentRevision, requested)
				if err != nil {
					return errs.Internal(err)
				}
				targetRevision = &revision
			}
		}
		response = &intrav1.LoadFormDocumentResponse{
			SourceMetadata:      sourceMeta,
			LocaleMetadata:      requestedMeta,
			SourceLocale:        root.SourceLocale,
			Locale:              locale,
			LocaleExists:        requestedExists,
			PresentLocaleValues: presentLocaleValues,
			DocumentRevision:    root.DocumentRevision,
			TargetRevision:      targetRevision,
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}
