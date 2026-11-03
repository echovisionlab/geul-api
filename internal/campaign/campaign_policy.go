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
	case TargetModeMemberCriteria:
		return CampaignTargetModeMemberCriteria, nil
	default:
		return "", fmt.Errorf("campaign target_mode is required")
	}
}

func validateCampaignTargetDefinition(campaign Campaign) error {
	criteriaID := strings.TrimSpace(ptrStringValue(campaign.CriteriaID))
	switch strings.TrimSpace(campaign.TargetMode) {
	case CampaignTargetModeAll:
		if criteriaID != "" {
			return fmt.Errorf(
				"all-recipient campaign must not reference a Member criteria",
			)
		}
	case CampaignTargetModeMemberCriteria:
		if criteriaID == "" {
			return fmt.Errorf(
				"criteria-targeted campaign requires a Member criteria",
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
