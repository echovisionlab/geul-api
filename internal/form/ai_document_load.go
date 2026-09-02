package form

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type formAIDocumentLocaleRow struct {
	Revision    string    `gorm:"column:revision"`
	Title       *string   `gorm:"column:title"`
	Schema      []byte    `gorm:"column:content_json"`
	ContentText *string   `gorm:"column:content_text"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

type formAIDocumentRoot struct {
	Status           string `gorm:"column:status"`
	SourceLocale     string `gorm:"column:source_locale"`
	DocumentID       string `gorm:"column:content_document_id"`
	DocumentRevision string `gorm:"column:document_revision"`
}

type formAIDocumentLoadedState struct {
	State         AIDocumentState
	Source        formAIDocumentLocaleRow
	Current       formAIDocumentLocaleRow
	CurrentExists bool
}

func (s *InternalFormService) LoadAIDocumentState(ctx context.Context, formID, locale string) (AIDocumentState, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.translation == nil {
		return AIDocumentState{}, errs.Internal(errors.New("form AI document dependencies are not configured"))
	}
	if !IsValidUUID(formID) {
		return AIDocumentState{}, errs.InvalidArgument("form_id", "must be a canonical UUID")
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return AIDocumentState{}, errs.Required("locale")
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, errs.AuthenticationRequired()
	}
	var state AIDocumentState
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadFormAIDocumentRoot(ctx, tx, formID, "KEY SHARE")
		if err != nil {
			return err
		}
		_, valid := formAIDocumentLifecycle(root.Status)
		if !valid {
			return errs.InternalMsg("Form has an unsupported lifecycle status")
		}
		if err := s.requireFormAction(ctx, formID, formActionView); err != nil {
			if connect.CodeOf(err) == connect.CodePermissionDenied {
				return errs.NotFound("form", formID)
			}
			return err
		}
		loaded, loadErr := s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, formID, locale, root, principal.MemberID.String(), false,
		)
		state = loaded.State
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return state, err
}

func loadFormAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	lock string,
) (formAIDocumentRoot, error) {
	query := tx.WithContext(ctx).Table("form AS root").
		Select("root.status, root.source_locale, root.content_document_id, document.revision::text AS document_revision").
		Joins("JOIN content_document AS document ON document.id = root.content_document_id").
		Where("root.id = ?::uuid", formID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root formAIDocumentRoot
	result := query.Take(&root)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return formAIDocumentRoot{}, errs.NotFound("form", formID)
	}
	if result.Error != nil {
		return formAIDocumentRoot{}, errs.Internal(result.Error)
	}
	return root, nil
}

func (s *InternalFormService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	locale string,
	root formAIDocumentRoot,
	viewerMemberID string,
	update bool,
) (formAIDocumentLoadedState, error) {
	sourceLocale := root.SourceLocale
	source, exists, err := loadFormAIDocumentLocale(ctx, tx, formID, sourceLocale, update)
	if err != nil {
		return formAIDocumentLoadedState{}, err
	}
	if !exists {
		return formAIDocumentLoadedState{}, errs.FailedPrecondition("Form source locale document is missing")
	}
	requested, requestedExists := source, true
	if locale != sourceLocale {
		requested, requestedExists, err = loadFormAIDocumentLocale(ctx, tx, formID, locale, update)
		if err != nil {
			return formAIDocumentLoadedState{}, err
		}
	}
	state := AIDocumentState{
		FormID: formID, Status: root.Status, DocumentRevision: root.DocumentRevision,
		SourceLocale: sourceLocale, Locale: locale, LocaleExists: requestedExists,
		SourceTitle: source.Title, SourceSchema: source.Schema, ViewerMemberID: viewerMemberID,
	}
	if requestedExists {
		state.LocaleTitle, state.LocaleSchema = requested.Title, requested.Schema
		if locale != sourceLocale {
			targetRevision, err := deriveFormTargetRevision(root.DocumentRevision, requested)
			if err != nil {
				return formAIDocumentLoadedState{}, errs.Internal(err)
			}
			state.TargetRevision = &targetRevision
		}
	}
	return formAIDocumentLoadedState{
		State: state, Source: source, Current: requested, CurrentExists: requestedExists,
	}, nil
}

func deriveFormTargetRevision(documentRevision string, row formAIDocumentLocaleRow) (string, error) {
	updatedAt := row.UpdatedAt
	return translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists:     true,
		DocumentRevision: documentRevision,
		LocaleUpdatedAt:  &updatedAt,
	})
}

func loadFormAIDocumentLocale(ctx context.Context, tx *gorm.DB, formID, locale string, update bool) (formAIDocumentLocaleRow, bool, error) {
	query := tx.WithContext(ctx).Table("form_translation").Select("xmin::text AS revision", "title", "content_json", "content_text", "updated_at").Where("entity_id = ?::uuid AND locale = ?", formID, locale)
	if update {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row formAIDocumentLocaleRow
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return formAIDocumentLocaleRow{}, false, nil
	}
	if result.Error != nil {
		return formAIDocumentLocaleRow{}, false, errs.Internal(result.Error)
	}
	return row, true, nil
}

func formAIDocumentLifecycle(status string) (published, valid bool) {
	switch status {
	case managev1.FormStatus_FORM_STATUS_DRAFT.String():
		return false, true
	case managev1.FormStatus_FORM_STATUS_PUBLISHED.String():
		return true, true
	default:
		return false, false
	}
}
