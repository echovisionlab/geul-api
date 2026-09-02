package publiccontent

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/og"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"gorm.io/gorm"
)

// ReadyAsset reports whether the domain's localized OG asset is ready for
// public delivery. Asset lookup and URL projection remain domain-owned.
type ReadyAsset func(context.Context, string) (bool, error)

// ResolveOGConsistency keeps translated text and its exact OG generation
// identity coherent. Pending exact work keeps target text but omits the image;
// terminal or missing work falls back as one source selection.
func ResolveOGConsistency(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	selection Selection,
	readyAsset ReadyAsset,
) (Selection, error) {
	if err := validateSpec(spec); err != nil {
		return selection, err
	}
	if selection.DisplayedLocale == selection.SourceLocale {
		return selection, nil
	}
	disposition, err := og.ResolveExactLocalizedGeneration(
		ctx, db, spec.EntityType, entityID, selection.DisplayedLocale,
	)
	if err != nil {
		return selection, err
	}
	switch disposition {
	case og.LocalizedGenerationPending:
		selection.OgAssetID = nil
		selection.OmitSourceOgFallback = true
		return selection, nil
	case og.LocalizedGenerationReady:
		if selection.OgAssetID != nil && strings.TrimSpace(*selection.OgAssetID) != "" && readyAsset != nil {
			ready, err := readyAsset(ctx, *selection.OgAssetID)
			if err != nil {
				return selection, err
			}
			if ready {
				return selection, nil
			}
		}
		selection.OgAssetID = nil
		selection.OmitSourceOgFallback = true
		return selection, nil
	default:
		return fallbackOGSelection(ctx, db, spec, entityID, selection)
	}
}

func fallbackOGSelection(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	selection Selection,
) (Selection, error) {
	rows, err := loadRows(ctx, db, spec, entityID, []string{selection.SourceLocale})
	if err != nil {
		return selection, err
	}
	fallback := selection
	fallback.DisplayedLocale = selection.SourceLocale
	fallback.IsFallback = true
	fallback.IsOriginal = true
	fallback.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
	fallback.Title = nil
	fallback.Summary = nil
	fallback.ContentJSON = nil
	fallback.ContentHTML = nil
	fallback.ContentText = nil
	fallback.OgAssetID = nil
	fallback.OmitSourceOgFallback = false
	if source, ok := rows[selection.SourceLocale]; ok {
		applyRow(&fallback, source, selection.SourceLocale)
		fallback.IsOriginal = true
	}
	return fallback, nil
}

// FallbackToSource rebuilds a whole-result source selection from the current
// source-identity row. It is used by domains whose public OG target identity
// differs from the versioned content identity.
func FallbackToSource(
	ctx context.Context,
	db *gorm.DB,
	spec Spec,
	entityID string,
	selection Selection,
) (Selection, error) {
	if err := validateSpec(spec); err != nil {
		return selection, err
	}
	return fallbackOGSelection(ctx, db, spec, entityID, selection)
}
