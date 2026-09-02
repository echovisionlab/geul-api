package campaign

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestCampaignTargetModePolicyRequiresExplicitConsistentDefinition(t *testing.T) {
	segmentID := "segment-id"
	tests := []struct {
		name     string
		campaign model.Campaign
		wantErr  bool
	}{
		{
			name: "all",
			campaign: model.Campaign{
				TargetMode: model.CampaignTargetModeAll,
			},
		},
		{
			name: "segment",
			campaign: model.Campaign{
				TargetMode: model.CampaignTargetModeSegment,
				SegmentID:  &segmentID,
			},
		},
		{
			name: "unspecified",
			campaign: model.Campaign{
				SegmentID: &segmentID,
			},
			wantErr: true,
		},
		{
			name: "all with segment",
			campaign: model.Campaign{
				TargetMode: model.CampaignTargetModeAll,
				SegmentID:  &segmentID,
			},
			wantErr: true,
		},
		{
			name: "segment without segment",
			campaign: model.Campaign{
				TargetMode: model.CampaignTargetModeSegment,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCampaignTargetDefinition(tt.campaign)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}

	_, err := campaignTargetModeFromProto(
		managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_UNSPECIFIED,
	)
	require.Error(t, err)
	require.Equal(
		t,
		model.CampaignTargetModeAll,
		mustCampaignTargetModeFromProto(
			t,
			managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL,
		),
	)
	require.Equal(
		t,
		model.CampaignTargetModeSegment,
		mustCampaignTargetModeFromProto(
			t,
			managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_SEGMENT,
		),
	)
}

func mustCampaignTargetModeFromProto(
	t *testing.T,
	mode managev1.CampaignTargetMode,
) string {
	t.Helper()
	result, err := campaignTargetModeFromProto(mode)
	require.NoError(t, err)
	return result
}

func TestCampaignStatusPolicyMatrix(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		wantEdit     bool
		wantSchedule bool
		wantSendNow  bool
	}{
		{
			name:         "draft",
			status:       managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			wantEdit:     true,
			wantSchedule: true,
			wantSendNow:  true,
		},
		{
			name:         "scheduled",
			status:       managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
			wantEdit:     false,
			wantSchedule: false,
			wantSendNow:  false,
		},
		{
			name:         "failed",
			status:       managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String(),
			wantEdit:     false,
			wantSchedule: false,
			wantSendNow:  false,
		},
		{
			name:         "sending",
			status:       managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
			wantEdit:     false,
			wantSchedule: false,
			wantSendNow:  false,
		},
		{
			name:         "sent",
			status:       managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
			wantEdit:     false,
			wantSchedule: false,
			wantSendNow:  false,
		},
		{
			name:         "unknown",
			status:       "CAMPAIGN_STATUS_UNKNOWN",
			wantEdit:     false,
			wantSchedule: false,
			wantSendNow:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantEdit, campaignStatusAllowsEdit(tt.status))
			require.Equal(t, tt.wantSchedule, campaignStatusAllowsSchedule(tt.status))
			require.Equal(t, tt.wantSendNow, campaignStatusAllowsSendNow(tt.status))
		})
	}
}

func TestCampaignDeliveryCompletionDecisionMatrix(t *testing.T) {
	tests := []struct {
		name           string
		counts         campaignDeliveryCompletionCounts
		target         int
		complete       bool
		runStatus      string
		campaignStatus string
	}{
		{
			name: "all sent",
			counts: campaignDeliveryCompletionCounts{
				Total: 2,
				Sent:  2,
			},
			target:         2,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusSent,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		},
		{
			name: "sent plus skipped",
			counts: campaignDeliveryCompletionCounts{
				Total:   2,
				Sent:    1,
				Skipped: 1,
			},
			target:         2,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusSent,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		},
		{
			name: "sent plus blocked",
			counts: campaignDeliveryCompletionCounts{
				Total:   2,
				Sent:    1,
				Blocked: 1,
			},
			target:         2,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusSent,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		},
		{
			name: "sent plus permanent failure",
			counts: campaignDeliveryCompletionCounts{
				Total:         2,
				Sent:          1,
				PermanentFail: 1,
			},
			target:         2,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusSent,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		},
		{
			name: "sent plus suppressed",
			counts: campaignDeliveryCompletionCounts{
				Total:      2,
				Sent:       1,
				Suppressed: 1,
			},
			target:         2,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusSent,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		},
		{
			name: "zero sent blocked",
			counts: campaignDeliveryCompletionCounts{
				Total:   1,
				Blocked: 1,
			},
			target:         1,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusFailed,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String(),
		},
		{
			name: "zero sent permanent failure",
			counts: campaignDeliveryCompletionCounts{
				Total:         1,
				PermanentFail: 1,
			},
			target:         1,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusFailed,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String(),
		},
		{
			name: "zero sent suppressed",
			counts: campaignDeliveryCompletionCounts{
				Total:      1,
				Suppressed: 1,
			},
			target:         1,
			complete:       true,
			runStatus:      CampaignDeliveryRunStatusFailed,
			campaignStatus: managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String(),
		},
		{
			name: "pending recipient",
			counts: campaignDeliveryCompletionCounts{
				Total:   2,
				Sent:    1,
				Pending: 1,
			},
			target:   2,
			complete: false,
		},
		{
			name: "fanout incomplete",
			counts: campaignDeliveryCompletionCounts{
				Total: 1,
				Sent:  1,
			},
			target:   2,
			complete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideEmailDeliveryCompletion(tt.counts, tt.target, EmailDeliveryRunKindCampaign)
			require.Equal(t, tt.complete, got.Complete)
			require.Equal(t, tt.runStatus, got.RunStatus)
			require.Equal(t, tt.campaignStatus, got.CampaignStatus)
		})
	}
}

func TestNewEmailDeliveryBulkJobCarriesOnlyRunAuthorityAndFlowControls(t *testing.T) {
	job := newEmailDeliveryBulkJob(" run-123 ")

	require.Equal(t, "run-123", job.GetDeliveryRunId())
	require.Equal(t, int32(100), job.GetBatchSize())
	require.Equal(t, int32(10), job.GetRatePerSecond())
}
