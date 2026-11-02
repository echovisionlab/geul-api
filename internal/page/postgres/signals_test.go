package pagepostgres

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/page"
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
		"accessPolicy":        {path: "settings.access_policy", kind: configuration},
		"sourceLocale":        {path: "settings.source_locale", kind: configuration},
	}, pageContentUpdatedFieldSpecs)
}

func TestBuildPageLocaleContentUpdatedEvent(t *testing.T) {
	event := buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "page-1",
		Fields:               []string{"content"},
		DocumentRevision:     "document-revision",
		Contributors:         []string{"member-1"},
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:               "en",
		DocumentStateChanged: true,
	})
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
	event := buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "page-1",
		Fields:           []string{"content"},
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

func TestBuildPageAIDeleteContentUpdatedEventCarriesDeletedTargetFence(t *testing.T) {
	event := buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "page-1",
		Fields:           []string{"content"},
		DocumentRevision: "document-revision",
		Contributors:     []string{"member-1"},
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:           "ko",
		LocaleDeleted:    true,
	})
	require.NotNil(t, event)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, event.Source)
	assert.False(t, event.DocumentStateChanged)
	assert.Equal(t, "ko", event.GetLocale())
	require.NotNil(t, event.LocaleExists)
	assert.False(t, event.GetLocaleExists())
	assert.Nil(t, event.TargetRevision)
}

func TestBuildPageLocaleContentUpdatedEventRejectsInconsistentLocaleFence(t *testing.T) {
	targetRevision := "target-revision"
	assert.Nil(t, buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "page-1",
		Fields:               []string{"content"},
		DocumentRevision:     "document-revision",
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:               "ko",
		TargetRevision:       &targetRevision,
		DocumentStateChanged: true,
	}))
	assert.Nil(t, buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "page-1",
		Fields:           []string{"content"},
		DocumentRevision: "document-revision",
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:           "en",
	}))
}

func TestBuildManagePageContentUpdatedEvent(t *testing.T) {
	event := contentUpdatedEvent(page.ContentUpdate{PageID: "page-1", Kind: page.SettingsUpdate, Fields: []string{"showTitle"}})
	require.NotNil(t, event)

	assert.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE, event.EntityType)
	assert.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, event.Source)
	assert.False(t, event.DocumentStateChanged)
	require.Len(t, event.ChangedFields, 1)
	assert.Equal(t, "settings.show_title", event.ChangedFields[0].Path)
}
