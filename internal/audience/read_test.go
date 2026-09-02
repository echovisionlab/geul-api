package audience

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestReferenceCountsAreExposedByProto(t *testing.T) {
	segment := toProtoSegment(&model.AudienceSegment{
		CreatedAt:                    time.Now().UTC(),
		CampaignCount:                2,
		DeliveryRunCount:             3,
		DownloadPolicyReferenceCount: 4,
	})

	require.Equal(t, int32(2), segment.CampaignCount)
	require.Equal(t, int32(3), segment.DeliveryRunCount)
	require.Equal(t, int32(4), segment.DownloadPolicyReferenceCount)
}
