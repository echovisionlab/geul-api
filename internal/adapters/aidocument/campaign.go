package aidocumentadapter

import (
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
)

func NewCampaignRegistration(internal *campaigndomain.InternalService) (DomainRegistration, error) {
	if internal == nil {
		return DomainRegistration{}, errors.New("campaign AI document owner is required")
	}
	return newEmailContentRegistration(core.DomainCampaign, "Campaign", internal.AIDocuments())
}
