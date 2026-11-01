package form

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
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

// LoadAIDocumentQuery reads one Form locale as an AI document.
type LoadAIDocumentQuery struct {
	FormID string
	Locale string
}

// LoadAIDocument returns the AI document state of a Form locale the caller
// may view; another Form is not found.
func (s *Service) LoadAIDocument(ctx context.Context, query LoadAIDocumentQuery) (AIDocumentState, error) {
	formID := query.FormID
	if !IsValidUUID(formID) {
		return AIDocumentState{}, domainerr.InvalidField("form_id", "must be a canonical UUID")
	}
	locale := strings.TrimSpace(query.Locale)
	if locale == "" {
		return AIDocumentState{}, domainerr.Required("locale")
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, domainerr.AuthenticationRequired()
	}
	var state AIDocumentState
	err := mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		root, err := loadFormAIDocumentRoot(ctx, tx, formID, "KEY SHARE")
		if err != nil {
			return err
		}
		if _, valid := formAIDocumentLifecycle(root.Status); !valid {
			return errFormUnsupportedLifecycle
		}
		if err := s.requireVisibleFormAction(ctx, formID, formActionView, domainerr.ResourceNotFound("form", formID)); err != nil {
			return err
		}
		loaded, err := loadAIDocumentStateAfterAuthorization(ctx, tx, formID, locale, root, principal.MemberID.String(), false)
		state = loaded.State
		return err
	})
	return state, failurecause.WithAttrs(err, slog.String("entity_type", "form"), slog.String("entity_id", formID), slog.String("locale", locale))
}

// errFormUnsupportedLifecycle is a stored Form status this build does not
// know.
var errFormUnsupportedLifecycle = errors.New("Form has an unsupported lifecycle status")

// loadFormAIDocumentRoot reads the Form root and its Content Document
// revision, locking the root with lock when set.
func loadFormAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	lock string,
) (formAIDocumentRoot, error) {
	query := tx.WithContext(ctx).Table("form").
		Select("status, source_locale, content_document_id").
		Where("id = ?::uuid", formID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root formAIDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return formAIDocumentRoot{}, domainerr.ResourceNotFound("form", formID)
		}
		return formAIDocumentRoot{}, err
	}
	documentID, err := formContentDocumentID(root.DocumentID)
	if err != nil {
		return formAIDocumentRoot{}, err
	}
	revision, err := contentblock.CurrentRevision(ctx, tx, documentID)
	if err != nil {
		return formAIDocumentRoot{}, err
	}
	root.DocumentRevision = revision.String()
	return root, nil
}

func loadAIDocumentStateAfterAuthorization(
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
		return formAIDocumentLoadedState{}, domainerr.Precondition("Form source locale document is missing")
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
				return formAIDocumentLoadedState{}, err
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row formAIDocumentLocaleRow
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return formAIDocumentLocaleRow{}, false, nil
	}
	if result.Error != nil {
		return formAIDocumentLocaleRow{}, false, result.Error
	}
	return row, true, nil
}

func formAIDocumentLifecycle(status string) (published, valid bool) {
	switch model.FormStatus(status) {
	case model.FormStatusDraft:
		return false, true
	case model.FormStatusPublished:
		return true, true
	default:
		return false, false
	}
}
