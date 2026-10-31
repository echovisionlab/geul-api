package aidocumentadapter

import (
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	emaildomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
)

func NewEmailTemplateRegistration(internal *emaildomain.InternalService) (DomainRegistration, error) {
	if internal == nil {
		return DomainRegistration{}, errors.New("email template AI document owner is required")
	}
	return newEmailContentRegistration(core.DomainEmailTemplate, "Email Template", internal.TemplateAIDocuments())
}
