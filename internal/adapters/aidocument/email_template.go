package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	emaildomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
)

func NewEmailTemplateRegistration(
	internal *emaildomain.InternalEmailTemplateService,
) (DomainRegistration, error) {
	service, err := emaildomain.NewAIDocumentService(internal)
	if err != nil {
		return DomainRegistration{}, err
	}
	return newEmailContentRegistration(core.DomainEmailTemplate, "Email Template", service)
}
