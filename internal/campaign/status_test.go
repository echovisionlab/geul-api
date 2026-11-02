package campaign

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API status except
// UNSPECIFIED must be the mapping of the stored status of its name; a new
// API status fails here until it is mapped.
func TestCampaignStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.CampaignStatus_name {
		status := managev1.CampaignStatus(value)
		if status == managev1.CampaignStatus_CAMPAIGN_STATUS_UNSPECIFIED {
			continue
		}
		if mapped := campaignStatusToProto(CampaignStatus(name)); mapped != status {
			t.Fatalf("stored %s maps to %s", name, mapped)
		}
	}
	if mapped := campaignStatusToProto(CampaignStatus("CAMPAIGN_STATUS_RETIRED")); mapped != managev1.CampaignStatus_CAMPAIGN_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status maps to %s", mapped)
	}
}
