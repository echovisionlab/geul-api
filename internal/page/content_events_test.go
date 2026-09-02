package page

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageContentUpdatedFieldSpecs(t *testing.T) {
	configuration := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION
	media := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA
	state := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE
	text := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT

	assert.Equal(t, map[string]contentUpdatedFieldSpec{
		"title":               {path: "title", kind: text},
		"summary":             {path: "summary", kind: text},
		"content":             {path: "document.content", kind: text},
		"featuredImageFileId": {path: "media.featured_image", kind: media},
		"slug":                {path: "settings.slug", kind: configuration},
		"status":              {path: "state.status", kind: state},
		"showTitle":           {path: "settings.show_title", kind: configuration},
		"documentLayout":      {path: "settings.document_layout", kind: configuration},
		"sourceLocale":        {path: "settings.source_locale", kind: configuration},
	}, pageContentUpdatedFieldSpecs)
}

func TestBuildPageContentUpdatedEvent(t *testing.T) {
	event := buildPageContentUpdatedEvent(
		"page-1",
		[]string{"content"},
		"document-revision",
		[]string{"member-1"},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"en",
		true,
		nil,
		true,
	)
	require.NotNil(t, event)

	assert.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE, event.EntityType)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, event.Source)
	assert.True(t, event.DocumentStateChanged)
	assert.Equal(t, "document-revision", event.GetDocumentRevision())
	assert.Equal(t, "en", event.GetLocale())
	assert.True(t, event.GetLocaleExists())
	assert.Nil(t, event.TargetRevision)
	assert.Equal(t, []string{"member-1"}, event.ContributorMemberIds)
	require.Len(t, event.ChangedFields, 1)
	assert.Equal(t, "document.content", event.ChangedFields[0].Path)
}

func TestBuildPageTargetContentUpdatedEventCarriesExactTargetFence(t *testing.T) {
	targetRevision := "target-revision"
	event := buildPageContentUpdatedEvent(
		"page-1",
		[]string{"content"},
		"document-revision",
		[]string{"member-1"},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		"ko",
		true,
		&targetRevision,
		false,
	)
	require.NotNil(t, event)
	assert.False(t, event.DocumentStateChanged)
	assert.Equal(t, "document-revision", event.GetDocumentRevision())
	assert.Equal(t, "ko", event.GetLocale())
	assert.True(t, event.GetLocaleExists())
	assert.Equal(t, targetRevision, event.GetTargetRevision())
}

func TestBuildPageAIDeleteContentUpdatedEventCarriesDeletedTargetFence(t *testing.T) {
	event := buildPageContentUpdatedEvent(
		"page-1",
		[]string{"content"},
		"document-revision",
		[]string{"member-1"},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		"ko",
		false,
		nil,
		false,
	)
	require.NotNil(t, event)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, event.Source)
	assert.False(t, event.DocumentStateChanged)
	assert.Equal(t, "ko", event.GetLocale())
	require.NotNil(t, event.LocaleExists)
	assert.False(t, event.GetLocaleExists())
	assert.Nil(t, event.TargetRevision)
}

func TestBuildPageContentUpdatedEventRejectsInconsistentLocaleFence(t *testing.T) {
	targetRevision := "target-revision"
	assert.Nil(t, buildPageContentUpdatedEvent(
		"page-1", []string{"content"}, "document-revision", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, "ko", true, &targetRevision, true,
	))
	assert.Nil(t, buildPageContentUpdatedEvent(
		"page-1", []string{"content"}, "document-revision", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, "en", true, nil, false,
	))
}

func TestBuildManagePageContentUpdatedEvent(t *testing.T) {
	showTitle := false
	event := buildManagePageContentUpdatedEvent(&managev1.UpdatePageRequest{
		Id:        "page-1",
		ShowTitle: &showTitle,
	})
	require.NotNil(t, event)

	assert.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE, event.EntityType)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, event.Source)
	assert.False(t, event.DocumentStateChanged)
	require.Len(t, event.ChangedFields, 1)
	assert.Equal(t, "settings.show_title", event.ChangedFields[0].Path)
}
