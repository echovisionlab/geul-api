package series

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// loadSourceCopies loads the source copy for each unique, non-empty Series
// ID. Missing IDs are omitted from the result.
func loadSourceCopies(ctx context.Context, store TranslationStore, ids []string) (map[string]SourceCopy, error) {
	ids = sliceutil.UniqueTrimmed(ids)
	if len(ids) == 0 {
		return map[string]SourceCopy{}, nil
	}
	return store.SourceCopies(ctx, ids)
}

// loadRequiredSourceCopy returns the source copy for one Series.
func loadRequiredSourceCopy(ctx context.Context, store TranslationStore, seriesID string) (SourceCopy, error) {
	copies, err := loadSourceCopies(ctx, store, []string{seriesID})
	if err != nil {
		return SourceCopy{}, err
	}
	source, ok := copies[seriesID]
	if !ok {
		return SourceCopy{}, domainerr.ResourceNotFound("series_translation", seriesID)
	}
	return source, nil
}

// overlaySourceCopy projects source-localized fields onto a Series.
func overlaySourceCopy(ctx context.Context, store TranslationStore, series *Series) error {
	source, err := loadRequiredSourceCopy(ctx, store, series.ID)
	if err != nil {
		return err
	}
	series.overlay(source)
	return nil
}

func overlaySourceCopies(ctx context.Context, store TranslationStore, series []Series) error {
	ids := make([]string, 0, len(series))
	for i := range series {
		ids = append(ids, series[i].ID)
	}
	copies, err := loadSourceCopies(ctx, store, ids)
	if err != nil {
		return err
	}
	for i := range series {
		source, ok := copies[series[i].ID]
		if !ok {
			return domainerr.ResourceNotFound("series_translation", series[i].ID)
		}
		series[i].overlay(source)
	}
	return nil
}

// overlay projects the source copy's localized fields onto the Series.
func (s *Series) overlay(source SourceCopy) {
	if source.Title != nil {
		s.Title = *source.Title
	}
	s.Description = source.Summary
	s.OgAssetID = source.OgAssetID
}

// LoadTranslationSourceDocument returns the source-owned Series fields the
// provider may receive.
func LoadTranslationSourceDocument(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
) (*translation.SourceDocument, error) {
	document, err := loadRequiredSourceCopy(ctx, store, seriesID)
	if err != nil {
		return nil, err
	}
	contentDocument, err := loadSeriesContentDocumentState(ctx, store, seriesID, false)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		SourceLocale:            contentDocument.SourceLocale,
		Title:                   derefString(document.Title),
		Summary:                 document.Summary,
		ContentText:             document.ContentText,
		ContentDocumentRevision: contentDocument.Revision.String(),
	}, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
