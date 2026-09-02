package campaign

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func campaignTargetModeFromProto(
	mode managev1.CampaignTargetMode,
) (string, error) {
	switch mode {
	case managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL:
		return model.CampaignTargetModeAll, nil
	case managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_SEGMENT:
		return model.CampaignTargetModeSegment, nil
	default:
		return "", fmt.Errorf("campaign target_mode is required")
	}
}

func campaignTargetModeToProto(
	mode string,
) managev1.CampaignTargetMode {
	switch strings.TrimSpace(mode) {
	case model.CampaignTargetModeAll:
		return managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL
	case model.CampaignTargetModeSegment:
		return managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_SEGMENT
	default:
		return managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_UNSPECIFIED
	}
}

func validateCampaignTargetDefinition(campaign model.Campaign) error {
	segmentID := strings.TrimSpace(ptrStringValue(campaign.SegmentID))
	switch strings.TrimSpace(campaign.TargetMode) {
	case model.CampaignTargetModeAll:
		if segmentID != "" {
			return fmt.Errorf(
				"all-recipient campaign must not reference an audience segment",
			)
		}
	case model.CampaignTargetModeSegment:
		if segmentID == "" {
			return fmt.Errorf(
				"segment-targeted campaign requires an audience segment",
			)
		}
	default:
		return fmt.Errorf("campaign target_mode is required")
	}
	return nil
}

func campaignStatusAllowsEdit(status string) bool {
	return status == managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()
}

func campaignStatusAllowsSchedule(status string) bool {
	return status == managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()
}

func campaignStatusAllowsSendNow(status string) bool {
	return status == managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()
}
