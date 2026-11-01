package public

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/form"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// View is a Form as a visitor sees it in the displayed locale. A Metadata
// view leaves out the schema, the access settings and the submission
// settings.
type View struct {
	Form          form.Form
	Metadata      bool
	Document      form.FormSourceDocument
	Localization  *LocalizationSelection
	OgAsset       *form.Asset
	FeaturedImage *form.Asset
}

func (s *Service) buildFormView(ctx context.Context, found *form.Form, acceptLanguage string, metadata bool) (View, error) {
	sourceDocument, localization, err := s.loadResolvedFormDocument(ctx, found, acceptLanguage)
	if err != nil {
		return View{}, err
	}

	ogAsset, err := s.resolveCoherentFormOgAsset(ctx, found, &localization)
	if err != nil {
		return View{}, err
	}

	return View{
		Form: *found, Metadata: metadata, Document: *sourceDocument, Localization: &localization,
		OgAsset: ogAsset, FeaturedImage: s.getFormFeaturedImageAsset(ctx, found.ID),
	}, nil
}

func (s *Service) resolveFormOgLocalization(
	ctx context.Context,
	formID string,
	selection LocalizationSelection,
) LocalizationSelection {
	if selection.DisplayedLocale == "" || selection.DisplayedLocale == selection.SourceLocale {
		return selection
	}
	var disposition form.LocalizedOGDisposition
	err := s.read(ctx, func(store Store) error {
		var err error
		disposition, err = store.LocalizedOGDisposition(ctx, formID, selection.DisplayedLocale)
		return err
	})
	if err != nil {
		slog.WarnContext(ctx, "Localized form OG lookup failed; serving the source OG image", "domain", "form", "event", "form.operation_failed", "operation", "resolve_localized_og", "outcome", "failed", "form_id", formID, "locale", selection.DisplayedLocale, failurecause.Attr(err))
		return sourceFormOgFallback(selection)
	}
	switch disposition {
	case form.LocalizedOGPending:
		selection.OgAssetID = nil
		selection.OmitSourceOgFallback = true
		return selection
	case form.LocalizedOGReady:
		return selection
	default:
		return sourceFormOgFallback(selection)
	}
}

func sourceFormOgFallback(selection LocalizationSelection) LocalizationSelection {
	selection.DisplayedLocale = selection.SourceLocale
	selection.IsFallback = true
	selection.IsOriginal = true
	selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
	selection.Title = nil
	selection.Summary = nil
	selection.ContentJSON = nil
	selection.ContentHTML = nil
	selection.ContentText = nil
	selection.OgAssetID = nil
	selection.OmitSourceOgFallback = false
	return selection
}

func (s *Service) resolveCoherentFormOgAsset(
	ctx context.Context,
	found *form.Form,
	selection *LocalizationSelection,
) (*form.Asset, error) {
	var sourceAssetID *string
	var localizedAssetID *string
	if selection == nil || !selection.OmitSourceOgFallback {
		sourceAssetID = found.OgAssetID
	}
	if selection != nil {
		localizedAssetID = selection.OgAssetID
	}
	var asset *form.Asset
	err := s.read(ctx, func(store Store) error {
		var err error
		asset, err = store.ReadyOGAsset(ctx, localizedAssetID, sourceAssetID)
		return err
	})
	if err != nil || selection == nil || selection.DisplayedLocale == selection.SourceLocale || selection.OgAssetID == nil {
		return asset, err
	}
	if asset == nil || asset.ID != strings.TrimSpace(*selection.OgAssetID) {
		return nil, nil
	}
	return asset, nil
}

// loadResolvedFormDocument reads the Form's source document and overlays
// the locale acceptLanguage selects.
func (s *Service) loadResolvedFormDocument(
	ctx context.Context,
	found *form.Form,
	acceptLanguage string,
) (*form.FormSourceDocument, LocalizationSelection, error) {
	var sourceDocument *form.FormSourceDocument
	if err := s.read(ctx, func(store Store) error {
		var err error
		sourceDocument, err = form.LoadCurrentFormSourceDocument(ctx, store, found.ID)
		return err
	}); err != nil {
		return nil, LocalizationSelection{}, err
	}
	localization := s.loadResolvedFormLocalization(ctx, found, acceptLanguage)
	localization = s.resolveFormOgLocalization(ctx, found.ID, localization)
	applyLocalizedFormDocument(ctx, sourceDocument, localization)
	return sourceDocument, localization, nil
}

func (s *Service) loadResolvedFormLocalization(
	ctx context.Context,
	found *form.Form,
	acceptLanguage string,
) LocalizationSelection {
	if strings.TrimSpace(acceptLanguage) == "" {
		return LocalizationSelection{}
	}

	localization, err := s.resolvePublicLocalization(ctx, found.ID, acceptLanguage)
	if err != nil {
		slog.WarnContext(ctx, "Form localization lookup failed; serving the source locale", "domain", "form", "event", "form.operation_failed", "operation", "resolve_localization", "outcome", "failed", "form_id", found.ID, failurecause.Attr(err))
		return LocalizationSelection{}
	}
	return localization
}

func applyLocalizedFormDocument(
	ctx context.Context,
	sourceDocument *form.FormSourceDocument,
	localization LocalizationSelection,
) {
	if sourceDocument == nil {
		return
	}
	if localization.Title != nil && strings.TrimSpace(*localization.Title) != "" {
		sourceDocument.Title = *localization.Title
	}
	if applyLocalizedFormSchema(ctx, sourceDocument, localization) {
		return
	}
	if localization.ContentText != nil {
		sourceDocument.ContentText = localization.ContentText
	}
}

func applyLocalizedFormSchema(
	ctx context.Context,
	sourceDocument *form.FormSourceDocument,
	localization LocalizationSelection,
) bool {
	if len(localization.ContentJSON) == 0 {
		return false
	}
	if localization.DisplayedLocale == "" || localization.SourceLocale == "" ||
		localization.DisplayedLocale == localization.SourceLocale {
		sourceDocument.Schema = localization.ContentJSON
		return false
	}
	canonicalSchema, canonicalText, err := form.CanonicalizeLocalizedFormSchema(
		sourceDocument.Schema,
		localization.ContentJSON,
	)
	if err != nil {
		slog.WarnContext(ctx, "Localized form schema does not match the source; serving it uncanonicalized", "domain", "form", "event", "form.operation_failed", "operation", "canonicalize_localized_schema", "outcome", "failed", "locale", localization.DisplayedLocale, "source_locale", localization.SourceLocale, failurecause.Attr(err))
		sourceDocument.Schema = localization.ContentJSON
		return false
	}
	sourceDocument.Schema = canonicalSchema
	if canonicalText != nil {
		sourceDocument.ContentText = canonicalText
	}
	return true
}

// getFormFeaturedImageAsset resolves the Form's featured image in a read of
// its own: a failure is logged and serves the Form without one.
func (s *Service) getFormFeaturedImageAsset(ctx context.Context, formID string) *form.Asset {
	var image *form.Asset
	if err := s.read(ctx, func(store Store) error {
		image = store.FeaturedImage(ctx, formID)
		return nil
	}); err != nil {
		slog.ErrorContext(ctx, "Form featured image lookup failed; serving the Form without a featured image", failurecause.Attr(err), "form_id", formID)
		return nil
	}
	return image
}
