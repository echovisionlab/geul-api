package release

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseContentUpdatedEventsClassifyMediaAndRelations(t *testing.T) {
	media := buildReleaseMediaMutationContentUpdatedEvent("release-1", "media.artwork")
	require.NotNil(t, media)
	require.Len(t, media.ChangedFields, 1)
	assert.Equal(t, managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA, media.ChangedFields[0].Kind)
	assert.Equal(t, "media.artwork", media.ChangedFields[0].Path)

	relations := buildReleaseRelationMutationContentUpdatedEvent(
		"release-1",
		[]string{"relations.labels", "relations.genres"},
	)
	require.NotNil(t, relations)
	require.Len(t, relations.ChangedFields, 2)
	for _, field := range relations.ChangedFields {
		assert.Equal(t, managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION, field.Kind)
	}
}

// A Release date change is a content change subscribers must see, alone or
// with other settings.
func TestReleaseUpdateSignalsReleaseDateChanges(t *testing.T) {
	for _, change := range []ReleaseDateChange{ReleaseDateSet, ReleaseDateCleared} {
		event := buildReleaseContentUpdatedEvent("release-1", releaseUpdatedFields(UpdateReleaseCommand{ID: "release-1", ReleaseDateChange: change}))
		require.NotNil(t, event)
		require.Len(t, event.ChangedFields, 1)
		assert.Equal(t, "state.release_date", event.ChangedFields[0].Path)
		assert.Equal(t, managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE, event.ChangedFields[0].Kind)
	}
	catalog := "CAT-1"
	event := buildReleaseContentUpdatedEvent("release-1", releaseUpdatedFields(UpdateReleaseCommand{
		ID: "release-1", CatalogNumber: &catalog, ReleaseDateChange: ReleaseDateSet,
	}))
	require.Len(t, event.ChangedFields, 2)
	assert.Nil(t, buildReleaseContentUpdatedEvent("release-1", releaseUpdatedFields(UpdateReleaseCommand{ID: "release-1"})))
}
