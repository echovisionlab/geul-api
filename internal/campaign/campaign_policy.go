package campaign

import (
	"fmt"
	"strings"
)

func campaignTargetModeValue(
	mode TargetMode,
) (string, error) {
	switch mode {
	case TargetModeAll:
		return CampaignTargetModeAll, nil
	case TargetModeSegment:
		return CampaignTargetModeSegment, nil
	default:
		return "", fmt.Errorf("campaign target_mode is required")
	}
}

func validateCampaignTargetDefinition(campaign Campaign) error {
	segmentID := strings.TrimSpace(ptrStringValue(campaign.SegmentID))
	switch strings.TrimSpace(campaign.TargetMode) {
	case CampaignTargetModeAll:
		if segmentID != "" {
			return fmt.Errorf(
				"all-recipient campaign must not reference an audience segment",
			)
		}
	case CampaignTargetModeSegment:
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

func campaignStatusAllowsEdit(status CampaignStatus) bool {
	return CampaignStatus(status) == CampaignStatusDraft
}

func campaignStatusAllowsSchedule(status CampaignStatus) bool {
	return CampaignStatus(status) == CampaignStatusDraft
}

func campaignStatusAllowsSendNow(status CampaignStatus) bool {
	return CampaignStatus(status) == CampaignStatusDraft
}
