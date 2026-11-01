package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseContentUpdatedEventsClassifyMediaAndRelations(t *testing.T) {
	media := buildReleaseMediaMutationContentUpdatedEvent("release-1", "media.artwork")
	require.NotNil(t, media)
	require.Len(t, media.Fields, 1)
	assert.Equal(t, FieldMedia, media.Fields[0].Kind)
	assert.Equal(t, "media.artwork", media.Fields[0].Path)

	relations := buildReleaseRelationMutationContentUpdatedEvent(
		"release-1",
		[]string{"relations.labels", "relations.genres"},
	)
	require.NotNil(t, relations)
	require.Len(t, relations.Fields, 2)
	for _, field := range relations.Fields {
		assert.Equal(t, FieldRelation, field.Kind)
	}
}

// A Release date change is a content change subscribers must see, alone or
// with other settings.
func TestReleaseUpdateSignalsReleaseDateChanges(t *testing.T) {
	for _, change := range []ReleaseDateChange{ReleaseDateSet, ReleaseDateCleared} {
		event := buildReleaseContentUpdatedEvent("release-1", releaseUpdatedFields(UpdateReleaseCommand{ID: "release-1", ReleaseDateChange: change}))
		require.NotNil(t, event)
		require.Len(t, event.Fields, 1)
		assert.Equal(t, "state.release_date", event.Fields[0].Path)
		assert.Equal(t, FieldState, event.Fields[0].Kind)
	}
	catalog := "CAT-1"
	event := buildReleaseContentUpdatedEvent("release-1", releaseUpdatedFields(UpdateReleaseCommand{
		ID: "release-1", CatalogNumber: &catalog, ReleaseDateChange: ReleaseDateSet,
	}))
	require.Len(t, event.Fields, 2)
	assert.Nil(t, buildReleaseContentUpdatedEvent("release-1", releaseUpdatedFields(UpdateReleaseCommand{ID: "release-1"})))
}
