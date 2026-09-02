package form

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
) (*connect.Response[intrav1.SaveFormDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	r := req.Msg
	locale := strings.TrimSpace(r.Locale)
	if locale == "" {
		return nil, errs.Required("locale")
	}
	expectedDocumentRevision, err := uuid.Parse(strings.TrimSpace(r.ExpectedDocumentRevision))
	if err != nil || expectedDocumentRevision == uuid.Nil || expectedDocumentRevision.String() != r.ExpectedDocumentRevision {
		return nil, errs.InvalidArgument("expected_document_revision", "must be a canonical UUID")
	}
	canonicalTitle, canonicalSchema, err := formCanonicalDocumentFromMeta(r.Meta)
	if err != nil {
		return nil, errs.InvalidArgument("meta", err.Error())
	}
	if err := validateFormCanonicalLocalePresence(
		canonicalTitle, canonicalSchema, r.PresentLocaleValues,
	); err != nil {
		return nil, errs.InvalidArgument("present_locale_values", err.Error())
	}

	now := time.Now().UTC()
	var result formCollaborativeSaveResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.translation.LockRoot(ctx, tx, r.FormId); err != nil {
			return err
		}
		root, err := loadFormAIDocumentRoot(ctx, tx, r.FormId, "")
		if err != nil {
			return err
		}
		if root.DocumentRevision != expectedDocumentRevision.String() {
			return errs.FailedPrecondition("Form Content Document revision changed")
		}
		contributorMemberID, err := canonicalFormCollaborationContributor(r.ContributorMemberIds)
		if err != nil {
			return err
		}
		if err := s.translation.RequireMutationContributor(
			ctx,
			tx,
			s.spiceDB,
			intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_FORM,
			r.FormId,
			contributorMemberID,
		); err != nil {
			return err
		}

		current, exists, err := loadFormAIDocumentLocale(ctx, tx, r.FormId, locale, true)
		if err != nil {
			return err
		}
		if !exists {
			return errs.FailedPrecondition("Form locale document does not exist")
		}
		writeNow := now
		if locale == root.SourceLocale {
			if r.ExpectedTargetRevision != nil {
				return errs.InvalidArgument("expected_target_revision", "source locale has no target revision")
			}
		} else {
			currentTargetRevision, err := deriveFormTargetRevision(root.DocumentRevision, current)
			if err != nil {
				return errs.Internal(err)
			}
			if err := translation.ValidateExpectedTargetRevision(
				r.ExpectedTargetRevision, currentTargetRevision, true,
			); err != nil {
				return err
			}
			source, sourceExists, err := loadFormAIDocumentLocale(
				ctx, tx, r.FormId, root.SourceLocale, false,
			)
			if err != nil {
				return err
			}
			if !sourceExists {
				return errs.FailedPrecondition("Form source locale document is missing")
			}
			if err := validateFormAIDocumentTargetSchema(source.Schema, canonicalSchema); err != nil {
				return errs.InvalidArgument("meta.schema", err.Error())
			}
			writeNow = translation.NextTargetUpdatedAt(writeNow, current.UpdatedAt)
		}

		updates, titleChanged := formCollaborativeLocaleUpdates(
			current, canonicalTitle, canonicalSchema, writeNow,
		)
		apply := func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return applyFormCollaborativeLocaleUpdates(
				ctx, tx, r.FormId, locale, updates, writeNow,
			)
		}
		changed := false
		documentRevision := root.DocumentRevision
		if locale == root.SourceLocale {
			documentID, parseErr := uuid.Parse(root.DocumentID)
			if parseErr != nil || documentID == uuid.Nil {
				return errs.FailedPrecondition("Form Content Document is not initialized")
			}
			advance, advanceErr := s.contentBlocks.AdvanceRevision(
				ctx,
				tx,
				contentblock.AdvanceInput{
					DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
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
						ChangedLocales:           []string{locale},
					}, nil
				},
			)
			if advanceErr != nil {
				return advanceErr
			}
			changed = advance.Changed
			documentRevision = advance.DocumentRevision.String()
		} else {
			changed, err = apply(ctx, tx)
			if err != nil {
				return err
			}
		}
		if changed && titleChanged {
			if _, err := s.og.RequestAfterMutation(
				ctx, tx, r.FormId, locale, locale == root.SourceLocale, "form_document_saved",
			); err != nil {
				return err
			}
		}
		if changed {
			if err := s.appendFormCollaborativeLocaleContentAudit(
				ctx, tx, contributorMemberID, r.FormId, locale,
			); err != nil {
				return err
			}
		}
		result = formCollaborativeSaveResult{
			locale: locale, documentRevision: documentRevision,
		}
		if locale != root.SourceLocale {
			updated, updatedExists, err := loadFormAIDocumentLocale(
				ctx, tx, r.FormId, locale, false,
			)
			if err != nil {
				return err
			}
			if !updatedExists {
				return errs.FailedPrecondition("Form locale document disappeared")
			}
			targetRevision, err := deriveFormTargetRevision(documentRevision, updated)
			if err != nil {
				return errs.Internal(err)
			}
			result.targetRevision = &targetRevision
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		slog.Error("Failed to save collaborative Form document", "formId", r.FormId, "error", err)
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("form", r.FormId)
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

type formCollaborativeLocaleUpdate struct {
	fields structured.Fields
}

func formCollaborativeLocaleUpdates(
	current formAIDocumentLocaleRow,
	title *string,
	schema []byte,
	now time.Time,
) (formCollaborativeLocaleUpdate, bool) {
	fields := structured.Fields{}
	titleChanged := !formAIDocumentEqualString(current.Title, title)
	if titleChanged {
		fields["title"] = title
	}
	if !formAIDocumentJSONEqual(current.Schema, schema) {
		fields["content_json"] = schema
		fields["content_text"] = formCanonicalContentText(schema)
	}
	if len(fields) != 0 {
		fields["updated_at"] = now
	}
	return formCollaborativeLocaleUpdate{fields: fields}, titleChanged
}

func applyFormCollaborativeLocaleUpdates(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	locale string,
	update formCollaborativeLocaleUpdate,
	now time.Time,
) (bool, error) {
	if len(update.fields) == 0 {
		return false, nil
	}
	result := tx.WithContext(ctx).Table("form_translation").
		Where("entity_id = ?::uuid AND locale = ?", formID, locale).
		Updates(update.fields)
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
			return errs.FailedPrecondition("Form source locale document is invalid")
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
				return errs.FailedPrecondition("Form requested locale document is invalid")
			}
			if locale != root.SourceLocale {
				if err := validateFormAIDocumentTargetSchema(source.Schema, requested.Schema); err != nil {
					return errs.FailedPrecondition("Form requested locale document is invalid")
				}
			}
			presentLocaleValues, err = formCanonicalLocaleTargets(
				requested.Title, requested.Schema,
			)
			if err != nil {
				return errs.FailedPrecondition("Form requested locale document is invalid")
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
