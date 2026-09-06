package public

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestReleasePublicStatusFilterAllowsPublishedOnly(t *testing.T) {
	require.Equal(t, []string{
		managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(),
	}, ReleaseFilterConfig.Fields["status"].EnumValues)
	require.Empty(t, ReleaseFilterConfig.DefaultFilters)
}
