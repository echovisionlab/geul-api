package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
)

func TestApplyLocalizedPageFieldsUsesLocaleMetadataAndPreservesDocument(t *testing.T) {
	t.Parallel()

	title := "Source title"
	summary := "Source summary"
	page := &openv1.Page{
		Title:   title,
		Summary: &summary,
		Document: &contentv1.LocalizedPageDocument{
			Locale: "en",
		},
		DocumentLayout: &commonv1.DocumentLayout{
			ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT,
			PageChrome:    commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
			Footer:        commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		},
	}
	wantLayout := page.DocumentLayout

	localizedSummary := "Localized summary"
	applyLocalizedPageFields(page, publiccontent.Selection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		IsOriginal:      false,
		Title:           nil,
		Summary:         &localizedSummary,
	})

	assert.Equal(t, "", page.Title)
	assert.Equal(t, &localizedSummary, page.Summary)
	assert.Equal(t, "en", page.GetDocument().GetLocale())
	assert.Equal(t, wantLayout, page.DocumentLayout)
}
