package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/assert"
)

func TestApplyLocalizedPageFieldsUsesLocaleMetadataAndPreservesDocument(t *testing.T) {
	t.Parallel()

	title := "Source title"
	summary := "Source summary"
	layout := page.DocumentLayout{ContentHeight: page.ContentHeightViewport, PageChrome: page.PlacementPinned, Footer: page.PlacementFlow}
	read := &Read{
		Title:   title,
		Summary: &summary,
		Page:    page.Page{DocumentLayout: layout},
		Document: &contentv1.LocalizedPageDocument{
			Locale: "en",
		},
	}

	localizedSummary := "Localized summary"
	applyLocalizedPageFields(read, publiccontent.Selection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		IsOriginal:      false,
		Title:           nil,
		Summary:         &localizedSummary,
	})

	assert.Equal(t, "", read.Title)
	assert.Equal(t, &localizedSummary, read.Summary)
	assert.Equal(t, "en", read.Document.GetLocale())
	assert.Equal(t, layout, read.Page.DocumentLayout)
}
