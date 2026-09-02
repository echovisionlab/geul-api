package post

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

// Localization adapts neutral public localization policy to Post's public
// projection, including the Series summary embedded in a Post response.
type Localization struct{}

func NewLocalization() *Localization { return &Localization{} }

func (*Localization) ResolveSelectionWithPolicy(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	acceptLanguage string,
	requireNonBlankTitle bool,
) (postpublic.LocalizedContentSelection, error) {
	spec, err := publicLocalizationSpec(entityType, requireNonBlankTitle)
	if err != nil {
		return postpublic.LocalizedContentSelection{}, err
	}
	selection, err := publiccontent.Resolve(ctx, db, spec, entityID, acceptLanguage)
	return toPostSelection(selection), err
}

func (*Localization) ResolveSelectionsWithPolicy(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityIDs []string,
	acceptLanguage string,
	requireNonBlankTitle bool,
) (map[string]postpublic.LocalizedContentSelection, error) {
	spec, err := publicLocalizationSpec(entityType, requireNonBlankTitle)
	if err != nil {
		return nil, err
	}
	selections, err := publiccontent.ResolveBatch(ctx, db, spec, entityIDs, acceptLanguage)
	if err != nil {
		return nil, err
	}
	result := make(map[string]postpublic.LocalizedContentSelection, len(selections))
	for entityID, selection := range selections {
		result[entityID] = toPostSelection(selection)
	}
	return result, nil
}

func (*Localization) ResolveOgConsistency(
	ctx context.Context,
	db *gorm.DB,
	_ string,
	entityType string,
	entityID string,
	selection postpublic.LocalizedContentSelection,
) (postpublic.LocalizedContentSelection, error) {
	spec, err := publicLocalizationSpec(entityType, true)
	if err != nil {
		return selection, err
	}
	resolved, err := publiccontent.ResolveOGConsistency(
		ctx,
		db,
		spec,
		entityID,
		fromPostSelection(selection),
		func(ctx context.Context, assetID string) (bool, error) {
			var count int64
			err := db.WithContext(ctx).Model(&model.PublicAsset{}).
				Where("id = ? AND status = ? AND file_size IS NOT NULL AND octet_length(sha256) = 32", assetID, model.PublicAssetStatusReady).
				Count(&count).Error
			return count != 0, err
		},
	)
	return toPostSelection(resolved), err
}

func publicLocalizationSpec(entityType string, _ bool) (publiccontent.Spec, error) {
	var spec publiccontent.Spec
	switch entityType {
	case "post":
		spec = publiccontent.Spec{
			EntityType: "post", TableName: "post_translation",
			SelectClause: "locale, title, summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, og_asset_id",
		}
	case "series":
		spec = publiccontent.Spec{
			EntityType: "series", TableName: "series_translation",
			SelectClause: "locale, title, summary, content_json, content_html, content_text, og_asset_id",
		}
	default:
		return publiccontent.Spec{}, fmt.Errorf("unsupported Post public localization entity %q", entityType)
	}
	return spec, nil
}

func toPostSelection(selection publiccontent.Selection) postpublic.LocalizedContentSelection {
	return postpublic.LocalizedContentSelection{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, AvailableLocales: selection.AvailableLocales,
		IsFallback: selection.IsFallback, IsOriginal: selection.IsOriginal,
		FallbackReason: selection.FallbackReason, Title: selection.Title, Summary: selection.Summary,
		ContentJSON: selection.ContentJSON, ContentHTML: selection.ContentHTML,
		ContentText: selection.ContentText, OgAssetID: selection.OgAssetID,
		OmitSourceOgFallback: selection.OmitSourceOgFallback,
	}
}

func fromPostSelection(selection postpublic.LocalizedContentSelection) publiccontent.Selection {
	return publiccontent.Selection{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, AvailableLocales: selection.AvailableLocales,
		IsFallback: selection.IsFallback, IsOriginal: selection.IsOriginal,
		FallbackReason: selection.FallbackReason, Title: selection.Title, Summary: selection.Summary,
		ContentJSON: selection.ContentJSON, ContentHTML: selection.ContentHTML,
		ContentText: selection.ContentText, OgAssetID: selection.OgAssetID,
		OmitSourceOgFallback: selection.OmitSourceOgFallback,
	}
}

var _ postpublic.LocalizationService = (*Localization)(nil)
