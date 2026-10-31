package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkLocaleContentUpdatedEvent(t *testing.T) {
	event := buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "work-1",
		Fields:               []string{"title", "content"},
		DocumentRevision:     "document-revision",
		Contributors:         []string{"member-1"},
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:               "en",
		DocumentStateChanged: true,
	})
	require.NotNil(t, event)
	assert.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, event.EntityType)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, event.Source)
	assert.True(t, event.DocumentStateChanged)
	assert.Equal(t, "document-revision", event.GetDocumentRevision())
	assert.Equal(t, "en", event.GetLocale())
	assert.True(t, event.GetLocaleExists())
	assert.Nil(t, event.TargetRevision)
	assert.Equal(t, []string{"member-1"}, event.ContributorMemberIds)
	assert.Equal(t, []string{"title", "document.content"}, workContentUpdatedPaths(event.ChangedFields))
}

func TestBuildWorkLocaleContentUpdatedEventForSummaryOnly(t *testing.T) {
	event := buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "work-1",
		Fields:               []string{"summary"},
		DocumentRevision:     "document-revision",
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:               "en",
		DocumentStateChanged: true,
	})
	require.NotNil(t, event)
	assert.True(t, event.DocumentStateChanged)
	assert.Equal(t, []string{"summary"}, workContentUpdatedPaths(event.ChangedFields))
}

func TestBuildWorkTargetContentUpdatedEventCarriesExactLocaleFence(t *testing.T) {
	targetRevision := "target-revision"
	event := buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "work-1",
		Fields:           []string{"summary"},
		DocumentRevision: "document-revision",
		Contributors:     []string{"member-1"},
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:           "ko",
		TargetRevision:   &targetRevision,
	})
	require.NotNil(t, event)
	assert.False(t, event.DocumentStateChanged)
	assert.Equal(t, "document-revision", event.GetDocumentRevision())
	assert.Equal(t, "ko", event.GetLocale())
	assert.True(t, event.GetLocaleExists())
	assert.Equal(t, targetRevision, event.GetTargetRevision())
}

func TestBuildManageWorkContentUpdatedEventWithGlobalMetadataChange(t *testing.T) {
	event := buildManageWorkContentUpdatedEvent(UpdateWorkCommand{
		ID:       "work-1",
		Featured: new(true),
	})
	require.NotNil(t, event)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, event.Source)
	assert.False(t, event.DocumentStateChanged)
	assert.Equal(t, []string{"state.featured"}, workContentUpdatedPaths(event.ChangedFields))
}

func workContentUpdatedPaths(fields []*managev1.ContentUpdatedField) []string {
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		paths = append(paths, field.Path)
	}
	return paths
}
