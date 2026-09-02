package public

import (
	"context"
	"log/slog"
	"strings"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type localizedContentSelection = formdomain.LocalizationSelection

// FormService implements the public FormService
func (s *FormService) buildProtoForm(
	ctx context.Context,
	form *model.Form,
	acceptLanguage string,
) (*openv1.Form, error) {
	sourceDocument, localization, err := s.loadResolvedFormDocumentWithLocalization(ctx, form, acceptLanguage)
	if err != nil {
		return nil, err
	}

	ogAsset, err := s.resolveCoherentFormOgAsset(ctx, form, localization)
	if err != nil {
		return nil, err
	}

	return buildProtoFormFromResolvedDocument(
		form,
		sourceDocument,
		localization,
		s.toProtoStatus(form.Status),
		ogAsset,
		s.getFormFeaturedImageAsset(ctx, form.ID),
	), nil
}

func buildProtoFormFromResolvedDocument(
	form *model.Form,
	sourceDocument *formdomain.FormSourceDocument,
	localization *localizedContentSelection,
	status openv1.FormStatus,
	ogAsset *commonv1.AssetRef,
	featuredImageAsset *commonv1.AssetRef,
) *openv1.Form {
	protoForm := &openv1.Form{
		Id:                       form.ID,
		Title:                    sourceDocument.Title,
		Schema:                   sourceDocument.Schema,
		Status:                   status,
		IsPublic:                 form.IsPublic,
		RequireAuth:              form.RequireAuth != nil && *form.RequireAuth,
		AllowedRoles:             form.AllowedRoles,
		AllowDuplicateSubmission: form.AllowDuplicateSubmission != nil && *form.AllowDuplicateSubmission,
		HasPassword:              form.AccessPassword != nil && *form.AccessPassword != "",
		CreatedAt:                timestamppb.New(form.CreatedAt),
	}

	if form.Slug != nil {
		protoForm.Slug = form.Slug
	}
	if form.MaxSubmissions != nil {
		protoForm.MaxSubmissions = form.MaxSubmissions
	}
	if form.OpensAt != nil {
		protoForm.OpensAt = timestamppb.New(*form.OpensAt)
	}
	if form.ClosesAt != nil {
		protoForm.ClosesAt = timestamppb.New(*form.ClosesAt)
	}
	if form.UpdatedAt != nil {
		protoForm.UpdatedAt = timestamppb.New(*form.UpdatedAt)
	}
	protoForm.FeaturedImageAsset = featuredImageAsset
	protoForm.OgAsset = ogAsset
	if localization != nil {
		protoForm.LocalizationInfo = toProtoLocalizationInfo(*localization)
	}

	return protoForm
}

func (s *FormService) loadResolvedFormDocument(
	ctx context.Context,
	form *model.Form,
	acceptLanguage string,
) (*formdomain.FormSourceDocument, error) {
	sourceDocument, err := formdomain.LoadCurrentFormSourceDocument(
		ctx,
		s.db,
		form.ID,
	)
	if err != nil {
		return nil, err
	}
	localization := s.loadResolvedFormLocalization(ctx, form, acceptLanguage)
	localization = s.resolveFormOgLocalization(ctx, form.ID, localization)
	applyLocalizedFormDocument(sourceDocument, localization)
	return sourceDocument, nil
}

func (s *FormService) resolveFormOgLocalization(
	ctx context.Context,
	formID string,
	selection localizedContentSelection,
) localizedContentSelection {
	if selection.DisplayedLocale == "" || selection.DisplayedLocale == selection.SourceLocale {
		return selection
	}
	disposition, err := s.assets.LocalizedOGDisposition(ctx, s.db, formID, selection.DisplayedLocale)
	if err != nil {
		slog.Warn("failed to resolve form OG generation", "formId", formID, "error", err)
		return sourceFormOgFallback(selection)
	}
	switch disposition {
	case formdomain.LocalizedOGPending:
		selection.OgAssetID = nil
		selection.OmitSourceOgFallback = true
		return selection
	case formdomain.LocalizedOGReady:
		return selection
	default:
		return sourceFormOgFallback(selection)
	}
}

func sourceFormOgFallback(selection localizedContentSelection) localizedContentSelection {
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

func (s *FormService) resolveCoherentFormOgAsset(
	ctx context.Context,
	form *model.Form,
	selection *localizedContentSelection,
) (*commonv1.AssetRef, error) {
	var sourceAssetID *string
	var localizedAssetID *string
	if selection == nil || !selection.OmitSourceOgFallback {
		sourceAssetID = form.OgAssetID
	}
	if selection != nil {
		localizedAssetID = selection.OgAssetID
	}
	asset, err := s.assets.ResolvedOG(ctx, s.db, sourceAssetID, localizedAssetID)
	if err != nil || selection == nil || selection.DisplayedLocale == selection.SourceLocale || selection.OgAssetID == nil {
		return asset, err
	}
	if asset == nil || asset.GetAssetId() != strings.TrimSpace(*selection.OgAssetID) {
		return nil, nil
	}
	return asset, nil
}

func (s *FormService) loadResolvedFormDocumentWithLocalization(
	ctx context.Context,
	form *model.Form,
	acceptLanguage string,
) (*formdomain.FormSourceDocument, *localizedContentSelection, error) {
	sourceDocument, err := formdomain.LoadCurrentFormSourceDocument(
		ctx,
		s.db,
		form.ID,
	)
	if err != nil {
		return nil, nil, err
	}
	localization := s.loadResolvedFormLocalization(ctx, form, acceptLanguage)
	localization = s.resolveFormOgLocalization(ctx, form.ID, localization)
	applyLocalizedFormDocument(sourceDocument, localization)
	return sourceDocument, &localization, nil
}

func (s *FormService) loadResolvedFormLocalization(
	ctx context.Context,
	form *model.Form,
	acceptLanguage string,
) localizedContentSelection {
	if strings.TrimSpace(acceptLanguage) == "" {
		return localizedContentSelection{}
	}

	localization, err := formdomain.ResolvePublicLocalization(ctx, s.db, form.ID, acceptLanguage)
	if err != nil {
		slog.Warn("failed to resolve form localization", "formId", form.ID, "error", err)
		return localizedContentSelection{}
	}
	return localization
}

func applyLocalizedFormDocument(
	sourceDocument *formdomain.FormSourceDocument,
	localization localizedContentSelection,
) {
	if sourceDocument == nil {
		return
	}
	if localization.Title != nil && strings.TrimSpace(*localization.Title) != "" {
		sourceDocument.Title = *localization.Title
	}
	if applyLocalizedFormSchema(sourceDocument, localization) {
		return
	}
	if localization.ContentText != nil {
		sourceDocument.ContentText = localization.ContentText
	}
}

func applyLocalizedFormSchema(
	sourceDocument *formdomain.FormSourceDocument,
	localization localizedContentSelection,
) bool {
	if len(localization.ContentJSON) == 0 {
		return false
	}
	if localization.DisplayedLocale == "" || localization.SourceLocale == "" ||
		localization.DisplayedLocale == localization.SourceLocale {
		sourceDocument.Schema = localization.ContentJSON
		return false
	}
	canonicalSchema, canonicalText, err := formdomain.CanonicalizeLocalizedFormSchema(
		sourceDocument.Schema,
		localization.ContentJSON,
	)
	if err != nil {
		slog.Warn(
			"failed to canonicalize localized form schema against source",
			"displayedLocale", localization.DisplayedLocale,
			"sourceLocale", localization.SourceLocale,
			"error", err,
		)
		sourceDocument.Schema = localization.ContentJSON
		return false
	}
	sourceDocument.Schema = canonicalSchema
	if canonicalText != nil {
		sourceDocument.ContentText = canonicalText
	}
	return true
}

func (s *FormService) buildProtoFormMetadata(
	ctx context.Context,
	form *model.Form,
	acceptLanguage string,
) (*openv1.Form, error) {
	sourceDocument, localization, err := s.loadResolvedFormDocumentWithLocalization(ctx, form, acceptLanguage)
	if err != nil {
		return nil, err
	}

	ogAsset, err := s.resolveCoherentFormOgAsset(ctx, form, localization)
	if err != nil {
		return nil, err
	}

	return buildProtoFormMetadataFromResolvedDocument(
		form,
		sourceDocument,
		localization,
		s.toProtoStatus(form.Status),
		ogAsset,
		s.getFormFeaturedImageAsset(ctx, form.ID),
	), nil
}

func buildProtoFormMetadataFromResolvedDocument(
	form *model.Form,
	sourceDocument *formdomain.FormSourceDocument,
	localization *localizedContentSelection,
	status openv1.FormStatus,
	ogAsset *commonv1.AssetRef,
	featuredImageAsset *commonv1.AssetRef,
) *openv1.Form {
	protoForm := &openv1.Form{
		Id:          form.ID,
		Title:       sourceDocument.Title,
		Status:      status,
		IsPublic:    form.IsPublic,
		HasPassword: form.AccessPassword != nil && *form.AccessPassword != "",
		CreatedAt:   timestamppb.New(form.CreatedAt),
	}

	if form.Slug != nil {
		protoForm.Slug = form.Slug
	}
	if form.UpdatedAt != nil {
		protoForm.UpdatedAt = timestamppb.New(*form.UpdatedAt)
	}
	protoForm.OgAsset = ogAsset
	protoForm.FeaturedImageAsset = featuredImageAsset
	if localization != nil {
		protoForm.LocalizationInfo = toProtoLocalizationInfo(*localization)
	}

	return protoForm
}

// toProtoStatus converts model form status to proto status
func (s *FormService) toProtoStatus(status model.FormStatus) openv1.FormStatus {
	switch status {
	case model.FormStatus(managev1.FormStatus_FORM_STATUS_DRAFT.String()):
		return openv1.FormStatus_FORM_STATUS_DRAFT
	case model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String()):
		return openv1.FormStatus_FORM_STATUS_PUBLISHED
	default:
		return openv1.FormStatus_FORM_STATUS_UNSPECIFIED
	}
}

func (s *FormService) getFormFeaturedImageAsset(ctx context.Context, formID string) *commonv1.AssetRef {
	return s.assets.FeaturedImage(ctx, s.db, formID)
}

func toProtoLocalizationInfo(selection localizedContentSelection) *openv1.LocalizationInfo {
	return &openv1.LocalizationInfo{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, IsFallback: selection.IsFallback, IsOriginal: selection.IsOriginal,
		FallbackReason: selection.FallbackReason, AvailableLocales: selection.AvailableLocales,
	}
}

// GetDashboard retrieves form dashboard stats via dashboard share link.
