package public

import (
	"testing"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestLabelPublicIDFilterAllowsBatchLookupOnly(t *testing.T) {
	definition, ok := LabelFilterConfig.Fields["id"]
	require.True(t, ok)
	require.Equal(t, "id", definition.Column)
	require.Equal(t, queryutil.TypeID, definition.Type)
	require.Equal(t, []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_IN}, definition.AllowedOps)
}

func TestLabelPublicStatusFilterAllowsDraftAndPublishedOnly(t *testing.T) {
	require.ElementsMatch(t, []string{
		managev1.LabelStatus_LABEL_STATUS_DRAFT.String(),
		managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String(),
	}, LabelFilterConfig.Fields["status"].EnumValues)
}
