package audience

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReferenceCountsAreExposedByProto(t *testing.T) {
	segment := segmentMessage(SegmentView{
		Segment:    Segment{CreatedAt: time.Now().UTC()},
		References: References{Campaigns: 2, DeliveryRuns: 3, DownloadPolicies: 4},
	})

	require.Equal(t, int32(2), segment.CampaignCount)
	require.Equal(t, int32(3), segment.DeliveryRunCount)
	require.Equal(t, int32(4), segment.DownloadPolicyReferenceCount)
}
