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
