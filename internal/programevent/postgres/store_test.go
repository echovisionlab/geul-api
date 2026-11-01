package programeventpostgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestProgramEventAdminFilterSupportsExactSlugResolution(t *testing.T) {
	slug, ok := eventFilterConfig.Fields["slug"]
	require.True(t, ok)
	require.Equal(t, "slug", slug.Column)
	require.Contains(t, slug.AllowedOps, commonv1.FilterOp_FILTER_OP_EQ)
}
