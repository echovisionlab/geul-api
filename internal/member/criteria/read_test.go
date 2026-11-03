package criteria

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReferenceCountsAreExposedByProto(t *testing.T) {
	criteria := criteriaMessage(CriteriaView{
		Criteria:   Criteria{CreatedAt: time.Now().UTC()},
		References: References{Campaigns: 2, DeliveryRuns: 3, DownloadPolicies: 4},
	})

	require.Equal(t, int32(2), criteria.CampaignCount)
	require.Equal(t, int32(3), criteria.DeliveryRunCount)
	require.Equal(t, int32(4), criteria.DownloadPolicyReferenceCount)
}
