package campaign

import (
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

// campaignFilterConfig defines the Campaign-owned management list filters.
var campaignFilterConfig = &queryutil.FilterConfig{
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
				string(model.CampaignStatusDraft),
				string(model.CampaignStatusScheduled),
				string(model.CampaignStatusSending),
				string(model.CampaignStatusSent),
				string(model.CampaignStatusFailed),
			},
		},
	},
}
