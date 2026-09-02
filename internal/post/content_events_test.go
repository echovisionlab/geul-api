package post

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPostContentUpdatedEvent(t *testing.T) {
	event := buildPostBlockContentUpdatedEvent(
		"post-1",
		[]string{"summary", "content"},
		"edit-1",
		[]string{"member-1", "member-2"},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"en",
		true,
		nil,
		true,
	)
	require.NotNil(t, event)
	assert.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST, event.EntityType)
	assert.Equal(t, "post-1", event.EntityId)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, event.Source)
	assert.Equal(t, "edit-1", event.GetDocumentRevision())
	assert.True(t, event.DocumentStateChanged)
	assert.Equal(t, "en", event.GetLocale())
	assert.True(t, event.GetLocaleExists())
	assert.Nil(t, event.TargetRevision)
	assert.Equal(t, []string{"member-1", "member-2"}, event.ContributorMemberIds)
	require.Len(t, event.ChangedFields, 2)
	assert.Equal(t, "summary", event.ChangedFields[0].Path)
	assert.Equal(t, "document.content", event.ChangedFields[1].Path)
}

func TestBuildPostTargetContentUpdatedEventCarriesExactLocaleFence(t *testing.T) {
	targetRevision := "target-revision"
	event := buildPostBlockContentUpdatedEvent(
		"post-1", []string{"summary"}, "document-revision", []string{"member-1"},
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

func TestBuildPostContentUpdatedEventRejectsInconsistentLocaleFence(t *testing.T) {
	targetRevision := "target-revision"
	assert.Nil(t, buildPostBlockContentUpdatedEvent(
		"post-1", []string{"content"}, "document-revision", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"ko", true, &targetRevision, true,
	))
	assert.Nil(t, buildPostBlockContentUpdatedEvent(
		"post-1", []string{"content"}, "document-revision", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"en", true, nil, false,
	))
}

func TestPostContentUpdatedFieldSpecs(t *testing.T) {
	configuration := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION
	media := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA
	relation := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION
	state := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE
	text := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT

	assert.Equal(t, map[string]contentUpdatedFieldSpec{
		"title":               {path: "title", kind: text},
		"summary":             {path: "summary", kind: text},
		"content":             {path: "document.content", kind: text},
		"featuredImageFileId": {path: "media.featured_image", kind: media},
		"slug":                {path: "settings.slug", kind: configuration},
		"status":              {path: "state.status", kind: state},
		"categoryIds":         {path: "relations.categories", kind: relation},
		"tagIds":              {path: "relations.tags", kind: relation},
		"seriesId":            {path: "relations.series", kind: relation},
		"seriesOrder":         {path: "relations.series_order", kind: relation},
		"mapPlaceId":          {path: "relations.map_place", kind: relation},
		"commentsEnabled":     {path: "settings.comments_enabled", kind: configuration},
		"documentLayout":      {path: "settings.document_layout", kind: configuration},
		"sourceLocale":        {path: "settings.source_locale", kind: configuration},
	}, postContentUpdatedFieldSpecs)
}

func TestBuildManagePostContentUpdatedEventSkipsTranslationIrrelevantRelationClears(t *testing.T) {
	event := buildManagePostContentUpdatedEvent(&managev1.UpdatePostRequest{
		Id:         "post-1",
		MapPlaceId: new(""),
	})
	require.Nil(t, event)
}
