package emailauthoring

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type EmailTemplateClass string

const (
	EmailTemplateClassUnknown              EmailTemplateClass = "unknown"
	EmailTemplateClassCampaign             EmailTemplateClass = "campaign"
	EmailTemplateClassLegalNotice          EmailTemplateClass = "legal_notice"
	EmailTemplateClassAccountTransactional EmailTemplateClass = "account_transactional"
	EmailTemplateClassAccountVerification  EmailTemplateClass = "account_verification"
	EmailTemplateClassAuthLogin            EmailTemplateClass = "auth_login"
	EmailTemplateClassAuthRegistration     EmailTemplateClass = "auth_registration"
	EmailTemplateClassTestDirect           EmailTemplateClass = "test_direct"
)

func ClassifyEmailTemplateType(templateType string) EmailTemplateClass {
	templateType = strings.TrimSpace(templateType)
	if templateType == "" {
		return EmailTemplateClassUnknown
	}
	if email.IsDirectTemplateType(templateType) {
		return EmailTemplateClassTestDirect
	}
	if strings.HasPrefix(templateType, "campaign:") && strings.TrimSpace(strings.TrimPrefix(templateType, "campaign:")) != "" {
		return EmailTemplateClassCampaign
	}

	switch templateType {
	case eventTermsUpdate.String(), eventTermsEffective.String(),
		eventPrivacyUpdate.String(), eventPrivacyEffective.String():
		return EmailTemplateClassLegalNotice
	case eventAccountDeletionConfirm.String(), eventAccountDeletionScheduled.String(),
		eventAccountDeletionCancelled.String(), eventAccountDeletionComplete.String(),
		eventAccountRecoveryConfirm.String(), eventAccountRecoveryComplete.String(),
		eventPrimaryEmailChanged.String(), eventEmailAdded.String(), eventEmailRemoved.String(),
		eventPasskeyAdded.String(), eventPasskeyRemoved.String(), eventSocialLoginAdded.String(),
		eventSocialLoginRemoved.String(), eventWelcome.String():
		return EmailTemplateClassAccountTransactional
	case eventVerificationCode.String():
		return EmailTemplateClassAccountVerification
	case eventLoginCode.String():
		return EmailTemplateClassAuthLogin
	case eventRegistrationCode.String():
		return EmailTemplateClassAuthRegistration
	default:
		return EmailTemplateClassUnknown
	}
}

func EmailTemplateContextAllowed(job *managev1.SendEmailEvent) bool {
	if job == nil {
		return false
	}

	class := ClassifyEmailTemplateType(job.GetTemplateType())
	switch job.GetRecipientContext().(type) {
	case *managev1.SendEmailEvent_AccountSelectedPrimaryEmail:
		return class == EmailTemplateClassCampaign ||
			class == EmailTemplateClassLegalNotice ||
			class == EmailTemplateClassAccountTransactional
	case *managev1.SendEmailEvent_NewsletterSubscription:
		return class == EmailTemplateClassCampaign
	case *managev1.SendEmailEvent_AccountVerification:
		return class == EmailTemplateClassAccountVerification
	case *managev1.SendEmailEvent_AuthLogin:
		return class == EmailTemplateClassAuthLogin
	case *managev1.SendEmailEvent_AuthRegistration:
		return class == EmailTemplateClassAuthRegistration
	case *managev1.SendEmailEvent_SystemDirect:
		return class == EmailTemplateClassAccountTransactional
	case *managev1.SendEmailEvent_TestEmail:
		return class == EmailTemplateClassCampaign || class == EmailTemplateClassTestDirect
	default:
		return false
	}
}
