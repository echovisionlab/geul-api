package form

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// formAIDocumentRoot is the Form root with its Content Document revision.
type formAIDocumentRoot struct {
	DocumentRoot
	DocumentRevision string
}

type formAIDocumentLoadedState struct {
	State         AIDocumentState
	Source        Locale
	Current       Locale
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
	err := s.units.ReadLocked(ctx, func(store Store) error {
		root, err := loadFormAIDocumentRoot(ctx, store, formID, KeyShareLock)
		if err != nil {
			return err
		}
		if !root.Status.Known() {
			return errFormUnsupportedLifecycle
		}
		if err := s.requireVisibleFormAction(ctx, formID, formActionView, domainerr.ResourceNotFound("form", formID)); err != nil {
			return err
		}
		loaded, err := loadAIDocumentStateAfterAuthorization(ctx, store, formID, locale, root, principal.MemberID.String(), false)
		state = loaded.State
		return err
	})
	return state, failurecause.WithAttrs(err, slog.String("entity_type", "form"), slog.String("entity_id", formID), slog.String("locale", locale))
}

// errFormUnsupportedLifecycle is a stored Form status this build does not
// know.
var errFormUnsupportedLifecycle = errors.New("Form has an unsupported lifecycle status")

// loadFormAIDocumentRoot reads the Form root with lock and its Content
// Document revision.
func loadFormAIDocumentRoot(
	ctx context.Context,
	store Store,
	formID string,
	lock RowLock,
) (formAIDocumentRoot, error) {
	root, err := store.DocumentRoot(ctx, formID, lock)
	if err != nil {
		return formAIDocumentRoot{}, formNotFound(err, domainerr.ResourceNotFound("form", formID))
	}
	documentID, err := formContentDocumentID(root.DocumentID)
	if err != nil {
		return formAIDocumentRoot{}, err
	}
	revision, err := store.DocumentRevision(ctx, documentID)
	if err != nil {
		return formAIDocumentRoot{}, err
	}
	return formAIDocumentRoot{DocumentRoot: root, DocumentRevision: revision.String()}, nil
}

func loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	store Store,
	formID string,
	locale string,
	root formAIDocumentRoot,
	viewerMemberID string,
	update bool,
) (formAIDocumentLoadedState, error) {
	sourceLocale := root.SourceLocale
	source, exists, err := store.Locale(ctx, formID, sourceLocale, update)
	if err != nil {
		return formAIDocumentLoadedState{}, err
	}
	if !exists {
		return formAIDocumentLoadedState{}, domainerr.Precondition("Form source locale document is missing")
	}
	requested, requestedExists := source, true
	if locale != sourceLocale {
		requested, requestedExists, err = store.Locale(ctx, formID, locale, update)
		if err != nil {
			return formAIDocumentLoadedState{}, err
		}
	}
	state := AIDocumentState{
		FormID: formID, Status: string(root.Status), DocumentRevision: root.DocumentRevision,
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

func deriveFormTargetRevision(documentRevision string, row Locale) (string, error) {
	updatedAt := row.UpdatedAt
	return translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists:     true,
		DocumentRevision: documentRevision,
		LocaleUpdatedAt:  &updatedAt,
	})
}
