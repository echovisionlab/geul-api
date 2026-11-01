package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
)

func TestNewServiceNamesEveryMissingCollaborator(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "public legal dependencies are required: Localizer, OGImages, Units")
}

func TestPublicKindsReadTheirOwnHistory(t *testing.T) {
	for kind, want := range map[legaldomain.PolicyKind]publicKind{
		legaldomain.Terms: {
			kind: legaldomain.Terms, entity: "terms", scheduled: "TERMS_STATUS_SCHEDULED",
			active: "TERMS_STATUS_ACTIVE", archived: "TERMS_STATUS_ARCHIVED",
			shareLinkEntityType: "SHARE_LINK_ENTITY_TYPE_TERMS", serviceName: "public.Terms",
		},
		legaldomain.Privacy: {
			kind: legaldomain.Privacy, entity: "privacy", scheduled: "PRIVACY_STATUS_SCHEDULED",
			active: "PRIVACY_STATUS_ACTIVE", archived: "PRIVACY_STATUS_ARCHIVED",
			shareLinkEntityType: "SHARE_LINK_ENTITY_TYPE_PRIVACY", serviceName: "public.Privacy",
		},
	} {
		got, err := publicKindOf(kind)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := publicKindOf("cookies")
	require.Error(t, err)
}
