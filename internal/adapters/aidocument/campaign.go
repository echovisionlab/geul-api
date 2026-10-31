package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
)

func NewCampaignRegistration(
	internal *campaigndomain.InternalCampaignService,
) (DomainRegistration, error) {
	service, err := campaigndomain.NewAIDocumentService(internal)
	if err != nil {
		return DomainRegistration{}, err
	}
	return newEmailContentRegistration(core.DomainCampaign, "Campaign", service)
}
