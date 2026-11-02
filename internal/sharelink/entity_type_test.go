package sharelink

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestEveryAPIEntityTypeMapsToItsStoredValue(t *testing.T) {
	for value, name := range managev1.ShareLinkEntityType_name {
		entityType := managev1.ShareLinkEntityType(value)
		stored, ok := EntityTypeFromProto(entityType)
		if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_UNSPECIFIED {
			require.False(t, ok)
			continue
		}
		require.True(t, ok, name)
		require.Equal(t, EntityType(name), stored)
		require.Equal(t, entityType, EntityTypeToProto(stored))
	}
}

func TestEveryLinkKindHasAnAPIEntityType(t *testing.T) {
	for _, entityType := range EntityTypes() {
		require.True(t, entityType.Known(), entityType)
		require.NotEqual(t, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_UNSPECIFIED, EntityTypeToProto(entityType), entityType)
	}
	require.Len(t, EntityTypes(), len(managev1.ShareLinkEntityType_name)-1)
}
