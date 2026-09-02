package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestWorkFilterConfigOnlyAcceptsPublicStatuses(t *testing.T) {
	require.ElementsMatch(t, []string{
		managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
	}, workFilterConfig.Fields["status"].EnumValues)
}
