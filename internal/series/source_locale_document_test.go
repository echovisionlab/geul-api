package series

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlaySourceLocaleDocumentProjectsOnlyLocalizedFields(t *testing.T) {
	t.Parallel()

	series := &model.Series{ID: "series-1", Title: "Stored title", Slug: "stored-slug"}
	title := "Localized title"
	summary := "Localized summary"
	ogAssetID := "asset-1"

	OverlaySourceLocaleDocument(series, &SourceLocaleDocument{
		Title:     &title,
		Summary:   &summary,
		OgAssetID: &ogAssetID,
	})

	assert.Equal(t, "Localized title", series.Title)
	require.NotNil(t, series.Description)
	assert.Equal(t, "Localized summary", *series.Description)
	require.NotNil(t, series.OgAssetID)
	assert.Equal(t, "asset-1", *series.OgAssetID)
	assert.Equal(t, "stored-slug", series.Slug)
}

func TestNormalizeSourceLocaleDocumentIDs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"one", "two"}, normalizeSourceLocaleDocumentIDs([]string{" one ", "", "two", "one"}))
}
