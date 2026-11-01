package form

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// TranslationDocumentSaveInput is a source locale document write; a nil
// Title or ContentJSON keeps the stored one.
type TranslationDocumentSaveInput struct {
	Title       *string
	ContentJSON []byte
	Now         time.Time
}

// FormSourceDocument is a Form's source locale document as public reads
// serve it.
type FormSourceDocument struct {
	Title       string
	Schema      []byte
	ContentText *string
}

// DefaultTitle is the title of a Form whose source locale has none.
const DefaultTitle = "Untitled Form"

func defaultFormTitle() string {
	return DefaultTitle
}

func resolveFormTitle(title *string) string {
	if title != nil && strings.TrimSpace(*title) != "" {
		return *title
	}
	return defaultFormTitle()
}

// loadFormLocaleState reads one stored locale document; a missing one is
// not found.
func loadFormLocaleState(ctx context.Context, store SourceReader, formID, locale string) (TranslationDocumentState, error) {
	state, err := store.LocaleState(ctx, formID, locale)
	if errors.Is(err, ErrLocaleNotFound) {
		return TranslationDocumentState{}, domainerr.ResourceNotFound("form translation", formID+":"+locale).WithCause(err)
	}
	return state, err
}

// loadFormResolvedSourceLocale reads the Form's source locale; a missing
// Form or locale is not found.
func loadFormResolvedSourceLocale(ctx context.Context, store SourceReader, formID string) (string, error) {
	locale, err := store.SourceLocale(ctx, formID)
	if errors.Is(err, ErrFormNotFound) {
		return "", domainerr.ResourceNotFound("form source translation", formID).WithCause(err)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(locale) == "" {
		return "", domainerr.ResourceNotFound("form source translation", formID)
	}
	return locale, nil
}

// LoadFormCanonicalSourceDocumentState reads the Form's source locale
// document.
func LoadFormCanonicalSourceDocumentState(
	ctx context.Context,
	store SourceReader,
	formID string,
	sourceLocale string,
) (TranslationDocumentState, error) {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if sourceLocale == "" {
		return TranslationDocumentState{}, domainerr.ResourceNotFound("form source translation", formID)
	}
	return loadFormLocaleState(ctx, store, formID, sourceLocale)
}

// SourceReader reads a Form's source locale document: the management Store
// and the public read Store both are one.
type SourceReader interface {
	SourceLocale(ctx context.Context, formID string) (string, error)
	LocaleState(ctx context.Context, formID, locale string) (TranslationDocumentState, error)
}

// LoadCurrentFormSourceLocaleDocumentState reads the Form's current source
// locale document.
func LoadCurrentFormSourceLocaleDocumentState(ctx context.Context, store SourceReader, formID string) (TranslationDocumentState, error) {
	sourceLocale, err := loadFormResolvedSourceLocale(ctx, store, formID)
	if err != nil {
		return TranslationDocumentState{}, err
	}
	return LoadFormCanonicalSourceDocumentState(ctx, store, formID, sourceLocale)
}

// PrepareSourceLocaleSwitch ensures that a requested Form source locale has a
// canonical row before the shared Content Document pointer is switched. A
// missing locale starts as an empty localized overlay over the current source
// topology; an existing locale is preserved exactly and must already match
// that topology.
func PrepareSourceLocaleSwitch(
	ctx context.Context,
	store Store,
	formID string,
	currentSourceLocale string,
	requestedLocale string,
	now time.Time,
) error {
	if store == nil || !IsValidUUID(formID) {
		return domainerr.InvalidField("form_id", "must be a canonical UUID")
	}
	currentSourceLocale = strings.TrimSpace(currentSourceLocale)
	requestedLocale = strings.TrimSpace(requestedLocale)
	if currentSourceLocale == "" || requestedLocale == "" ||
		currentSourceLocale == requestedLocale {
		return domainerr.InvalidField(
			"source_locale",
			"current and requested Form source locales are required and must differ",
		)
	}
	if now.IsZero() {
		return domainerr.InvalidField("now", "Form source-locale switch time is required")
	}
	root, err := loadFormAIDocumentRoot(ctx, store, formID, Unlocked)
	if err != nil {
		return err
	}
	if root.SourceLocale != currentSourceLocale {
		return domainerr.Precondition("Form source locale changed")
	}
	source, sourceExists, err := store.Locale(ctx, formID, currentSourceLocale, true)
	if err != nil {
		return err
	}
	if !sourceExists {
		return domainerr.Precondition("Form source locale document is missing")
	}
	if err := validateCanonicalFormSchema(source.Schema); err != nil {
		return domainerr.Precondition("Form source locale document is invalid").WithCause(failurecause.ServerFault(err))
	}
	requested, requestedExists, err := store.Locale(ctx, formID, requestedLocale, true)
	if err != nil {
		return err
	}
	if requestedExists {
		if err := validateFormAIDocumentTargetSchema(source.Schema, requested.Schema); err != nil {
			return domainerr.Precondition("Form requested source locale document is invalid").WithCause(failurecause.ServerFault(err))
		}
		return nil
	}
	emptySchema, err := formAIDocumentEmptyTargetSchema(source.Schema)
	if err != nil {
		return domainerr.Precondition("Form source locale document is invalid").WithCause(failurecause.ServerFault(err))
	}
	created, err := store.CreateEmptyLocale(ctx, formID, requestedLocale, emptySchema, now.UTC())
	if err != nil {
		return err
	}
	if !created {
		return domainerr.Precondition("Form requested source locale changed")
	}
	return nil
}

func saveFormSourceLocaleDocumentState(
	ctx context.Context,
	store FormLocales,
	formID string,
	locale string,
	input TranslationDocumentSaveInput,
) error {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if len(input.ContentJSON) == 0 {
		existing, err := store.LocaleState(ctx, formID, locale)
		if err != nil && !errors.Is(err, ErrLocaleNotFound) {
			return err
		}
		if err == nil {
			input.ContentJSON = existing.ContentJSON
		}
	}
	return store.SaveSourceLocale(ctx, formID, locale, input.Title, input.ContentJSON, formCanonicalContentText(input.ContentJSON), now)
}

func loadFormSourceSchema(ctx context.Context, store SourceReader, formID string) ([]byte, *string, error) {
	state, err := LoadCurrentFormSourceLocaleDocumentState(ctx, store, formID)
	if err != nil {
		return nil, nil, err
	}
	return state.ContentJSON, state.Title, nil
}

// LoadCurrentFormSourceDocument reads the Form's current source locale
// document with its title resolved.
func LoadCurrentFormSourceDocument(ctx context.Context, store SourceReader, formID string) (*FormSourceDocument, error) {
	state, err := LoadCurrentFormSourceLocaleDocumentState(ctx, store, formID)
	if err != nil {
		return nil, err
	}

	return &FormSourceDocument{
		Title:       resolveFormTitle(state.Title),
		Schema:      state.ContentJSON,
		ContentText: state.ContentText,
	}, nil
}
