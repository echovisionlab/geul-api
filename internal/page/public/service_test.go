package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/assert"
)

func TestApplyLocalizedPageFieldsDoesNotUseLegacyBodyProjection(t *testing.T) {
	t.Parallel()

	document := &contentv1.LocalizedPageDocument{Locale: "en"}
	page := &Read{Document: document}
	localizedTitle := "Localized title"
	applyLocalizedPageFields(page, publiccontent.Selection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		IsOriginal:      false,
		Title:           &localizedTitle,
	})

	assert.Equal(t, localizedTitle, page.Title)
	assert.Same(t, document, page.Document)
}
