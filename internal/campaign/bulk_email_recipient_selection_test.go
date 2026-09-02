package campaign

import (
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecipientSelectionFromAudienceSegmentMapsConfig(t *testing.T) {
	t.Parallel()

	createdAfter := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	selection, err := recipientSelectionFromAudienceSegment(&model.AudienceSegment{
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String(),
		Config: model.AudienceSegmentConfig{
			AccountRoles:     []string{"admin", "author"},
			CreatedAfter:     &createdAfter,
			ExcludeMemberIDs: []string{"member-1"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, CampaignDeliveryTargetModeUsersByFilter, selection.Mode)
	assert.False(t, selection.RequireNewsletterSubscription)
	assert.Equal(t, []string{"admin", "author"}, selection.Filters.AccountRoles)
	assert.Equal(t, &createdAfter, selection.Filters.CreatedAfter)
	assert.Equal(t, []string{"member-1"}, selection.Filters.ExcludedMemberIDs)
}

func TestBuildBulkEmailRecipientCandidatesValidatesAudiencePlans(t *testing.T) {
	t.Parallel()

	_, err := buildBulkEmailRecipientCandidates(&bulkEmailRecipientSelection{
		Mode: CampaignDeliveryTargetModeUserTags,
	})
	require.ErrorContains(t, err, "member_tag_ids required")

	candidates, err := buildBulkEmailRecipientCandidates(&bulkEmailRecipientSelection{
		Mode:                          CampaignDeliveryTargetModeUsersByFilter,
		RequireNewsletterSubscription: true,
		Filters: &bulkEmailRecipientFilters{
			AccountRoles: []string{"admin"},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, candidates.SQL, "m.primary_email")
	assert.Contains(t, candidates.SQL, "kratos.identities")
	assert.NotContains(t, candidates.SQL, "identity_verifiable_addresses")
	assert.Contains(t, candidates.SQL, "JOIN newsletter_subscription")
	assert.Contains(t, candidates.SQL, "m.onboarded = TRUE")
	assert.True(t, strings.Count(candidates.SQL, "?") <= len(candidates.Args))

}
