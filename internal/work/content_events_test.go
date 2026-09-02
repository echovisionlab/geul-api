package work

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkSourceContentUpdatedEvent(t *testing.T) {
	event := buildWorkSourceContentUpdatedEvent(
		"work-1", true, false, true, "document-revision",
		[]string{"member-1"}, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"en", true, nil, true,
	)
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

func TestBuildWorkSourceContentUpdatedEventForSummaryOnly(t *testing.T) {
	event := buildWorkSourceContentUpdatedEvent(
		"work-1", false, true, false, "document-revision", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"en", true, nil, true,
	)
	require.NotNil(t, event)
	assert.True(t, event.DocumentStateChanged)
	assert.Equal(t, []string{"summary"}, workContentUpdatedPaths(event.ChangedFields))
}

func TestBuildWorkTargetContentUpdatedEventCarriesExactLocaleFence(t *testing.T) {
	targetRevision := "target-revision"
	event := buildWorkSourceContentUpdatedEvent(
		"work-1", false, true, false, "document-revision", []string{"member-1"},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"ko", true, &targetRevision, false,
	)
	require.NotNil(t, event)
	assert.False(t, event.DocumentStateChanged)
	assert.Equal(t, "document-revision", event.GetDocumentRevision())
	assert.Equal(t, "ko", event.GetLocale())
	assert.True(t, event.GetLocaleExists())
	assert.Equal(t, targetRevision, event.GetTargetRevision())
}

func TestBuildManageWorkContentUpdatedEventWithGlobalMetadataChange(t *testing.T) {
	event := buildManageWorkContentUpdatedEvent(&managev1.UpdateWorkRequest{
		Id:       "work-1",
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
