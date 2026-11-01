package campaign

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
)

type campaignAudienceTargetFunc struct {
	Store
	fn func(context.Context, string) (CampaignAudienceTarget, error)
}

func (s campaignAudienceTargetFunc) LockAudienceTarget(ctx context.Context, id string) (CampaignAudienceTarget, error) {
	return s.fn(ctx, id)
}

func TestCampaignDeliveryRecipientStatusPolicyOnlyPersistsBusinessResults(t *testing.T) {
	require.False(t, campaignDeliveryRecipientStatusFinalizesRun(CampaignDeliveryRecipientStatusPending))
	for _, status := range []string{
		CampaignDeliveryRecipientStatusSent,
		CampaignDeliveryRecipientStatusDelivered,
		CampaignDeliveryRecipientStatusSkipped,
		CampaignDeliveryRecipientStatusPermanentFailed,
		CampaignDeliveryRecipientStatusBlocked,
		CampaignDeliveryRecipientStatusSuppressed,
		CampaignDeliveryRecipientStatusBounced,
		CampaignDeliveryRecipientStatusComplained,
	} {
		require.True(t, campaignDeliveryRecipientStatusFinalizesRun(status), status)
	}
}

func TestCampaignRecipientScopeControlsNewsletterFilter(t *testing.T) {

	_, err := deriveCampaignDeliveryTarget(context.Background(), nil, Campaign{
		TargetMode:     CampaignTargetModeAll,
		RecipientScope: "LEGACY",
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
}

func TestDeriveCampaignDeliveryTargetUsesAudiencePortSnapshot(t *testing.T) {
	segmentID := "segment-a"
	createdAfter := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	port := campaignAudienceTargetFunc{fn: func(
		_ context.Context,
		gotSegmentID string,
	) (CampaignAudienceTarget, error) {
		require.Equal(t, segmentID, gotSegmentID)
		return CampaignAudienceTarget{
			Valid:             true,
			SegmentType:       AudienceSegmentMembersByFilter,
			CreatedAfter:      &createdAfter,
			AccountRoles:      []string{"ADMIN"},
			ExcludedMemberIDs: []string{"member-a"},
		}, nil
	}}

	target, err := deriveCampaignDeliveryTarget(
		context.Background(),
		port,
		Campaign{
			TargetMode:     CampaignTargetModeSegment,
			SegmentID:      &segmentID,
			RecipientScope: campaignRecipientScopeSubscribedUsers,
		},
	)
	require.NoError(t, err)
	require.Equal(t, CampaignDeliveryTargetModeUsersByFilter, target.Mode)
	require.Equal(t, []string{"ADMIN"}, target.AccountRoles)
	require.Equal(t, []string{"member-a"}, target.ExcludedMemberIDs)
	require.Equal(t, createdAfter, *target.CreatedAfter)
}

func TestCampaignDeliveryCompletionWaitsForPendingBusinessResults(t *testing.T) {
	require.False(t, decideEmailDeliveryCompletion(DeliveryCompletionCounts{
		Total:   2,
		Pending: 1,
		Sent:    1,
	}, 2, EmailDeliveryRunKindCampaign).Complete)
	require.True(t, decideEmailDeliveryCompletion(DeliveryCompletionCounts{
		Total:     2,
		Sent:      1,
		Delivered: 1,
	}, 2, EmailDeliveryRunKindCampaign).Complete)
}
