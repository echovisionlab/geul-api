package public

import (
	"testing"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
)

func TestPostLocalizationKeepsSourceWholeResultForSourceFallback(t *testing.T) {
	sourceSummary := "Source summary"

	localization := LocalizedContentSelection{
		RequestedLocale: "ko", DisplayedLocale: "en", SourceLocale: "en",
		IsOriginal: true, IsFallback: true,
	}
	protoPost := &openv1.Post{
		Title:   "Source Post",
		Summary: &sourceSummary,
	}

	applyPostLocalization(protoPost, localization)

	assert.Equal(t, "Source Post", protoPost.Title)
	assert.Equal(t, "Source summary", protoPost.GetSummary())
	assert.Equal(t, "en", localization.DisplayedLocale)
	assert.True(t, localization.IsOriginal)
	assert.True(t, localization.IsFallback)
}
