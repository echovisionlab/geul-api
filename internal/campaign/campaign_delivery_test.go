package campaign

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type campaignAudienceTargetFunc func(
	context.Context,
	*gorm.DB,
	string,
) (CampaignAudienceTarget, error)

func (fn campaignAudienceTargetFunc) LockTarget(
	ctx context.Context,
	db *gorm.DB,
	segmentID string,
) (CampaignAudienceTarget, error) {
	return fn(ctx, db, segmentID)
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
	selection, err := campaignDeliveryTargetRecipientSelection(CampaignDeliveryTarget{
		QueryVersion:   CampaignDeliveryTargetQueryVersion,
		Mode:           CampaignDeliveryTargetModeAllUsers,
		RecipientScope: campaignRecipientScopeSubscribedUsers,
	})
	require.NoError(t, err)
	require.Equal(t, CampaignDeliveryTargetModeAllUsers, selection.Mode)
	require.True(t, selection.RequireNewsletterSubscription)

	selection, err = campaignDeliveryTargetRecipientSelection(CampaignDeliveryTarget{
		QueryVersion:   CampaignDeliveryTargetQueryVersion,
		Mode:           CampaignDeliveryTargetModeAllUsers,
		RecipientScope: campaignRecipientScopeAllMatchingUsers,
	})
	require.NoError(t, err)
	require.False(t, selection.RequireNewsletterSubscription)

	_, err = deriveCampaignDeliveryTarget(context.Background(), nil, model.Campaign{
		TargetMode:     model.CampaignTargetModeAll,
		RecipientScope: "LEGACY",
	}, nil)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestDeriveCampaignDeliveryTargetUsesAudiencePortSnapshot(t *testing.T) {
	segmentID := "segment-a"
	createdAfter := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	port := campaignAudienceTargetFunc(func(
		_ context.Context,
		_ *gorm.DB,
		gotSegmentID string,
	) (CampaignAudienceTarget, error) {
		require.Equal(t, segmentID, gotSegmentID)
		return CampaignAudienceTarget{
			Valid:             true,
			SegmentType:       managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String(),
			CreatedAfter:      &createdAfter,
			AccountRoles:      []string{"ADMIN"},
			ExcludedMemberIDs: []string{"member-a"},
		}, nil
	})

	target, err := deriveCampaignDeliveryTarget(
		context.Background(),
		nil,
		model.Campaign{
			TargetMode:     model.CampaignTargetModeSegment,
			SegmentID:      &segmentID,
			RecipientScope: campaignRecipientScopeSubscribedUsers,
		},
		port,
	)
	require.NoError(t, err)
	require.Equal(t, CampaignDeliveryTargetModeUsersByFilter, target.Mode)
	require.Equal(t, []string{"ADMIN"}, target.AccountRoles)
	require.Equal(t, []string{"member-a"}, target.ExcludedMemberIDs)
	require.Equal(t, createdAfter, *target.CreatedAfter)
}

func TestDeriveCampaignDeliveryTargetFailsClosedWithoutAudiencePort(t *testing.T) {
	segmentID := "segment-a"
	_, err := deriveCampaignDeliveryTarget(
		context.Background(),
		nil,
		model.Campaign{
			TargetMode:     model.CampaignTargetModeSegment,
			SegmentID:      &segmentID,
			RecipientScope: campaignRecipientScopeSubscribedUsers,
		},
		nil,
	)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestCampaignDeliveryCompletionWaitsForPendingBusinessResults(t *testing.T) {
	require.False(t, decideEmailDeliveryCompletion(campaignDeliveryCompletionCounts{
		Total:   2,
		Pending: 1,
		Sent:    1,
	}, 2, EmailDeliveryRunKindCampaign).Complete)
	require.True(t, decideEmailDeliveryCompletion(campaignDeliveryCompletionCounts{
		Total:     2,
		Sent:      1,
		Delivered: 1,
	}, 2, EmailDeliveryRunKindCampaign).Complete)
}
