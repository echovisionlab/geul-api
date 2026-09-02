package campaign

import (
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// CampaignFilterConfig defines the Campaign-owned management list filters.
var CampaignFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "subject"},
		},
		"name": {
			Column:     "name",
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.TextOps,
		},
		"subject": {
			Column:     "subject",
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.TextOps,
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
				managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
				managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
				managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
				managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String(),
			},
		},
	},
}
