package emailauthoring

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
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

// RecipientContext is the kind of recipient a queued email names.
type RecipientContext string

// The recipient contexts of a queued email.
const (
	RecipientAccountSelectedPrimaryEmail RecipientContext = "account_selected_primary_email"
	RecipientNewsletterSubscription      RecipientContext = "newsletter_subscription"
	RecipientAccountVerification         RecipientContext = "account_verification"
	RecipientAuthLogin                   RecipientContext = "auth_login"
	RecipientAuthRegistration            RecipientContext = "auth_registration"
	RecipientSystemDirect                RecipientContext = "system_direct"
	RecipientTestEmail                   RecipientContext = "test_email"
)

// EmailTemplateContextAllowed reports whether an email of templateType may
// go to a recipient of context; an unknown context is never allowed.
func EmailTemplateContextAllowed(templateType string, context RecipientContext) bool {
	class := ClassifyEmailTemplateType(templateType)
	switch context {
	case RecipientAccountSelectedPrimaryEmail:
		return class == EmailTemplateClassCampaign ||
			class == EmailTemplateClassLegalNotice ||
			class == EmailTemplateClassAccountTransactional
	case RecipientNewsletterSubscription:
		return class == EmailTemplateClassCampaign
	case RecipientAccountVerification:
		return class == EmailTemplateClassAccountVerification
	case RecipientAuthLogin:
		return class == EmailTemplateClassAuthLogin
	case RecipientAuthRegistration:
		return class == EmailTemplateClassAuthRegistration
	case RecipientSystemDirect:
		return class == EmailTemplateClassAccountTransactional
	case RecipientTestEmail:
		return class == EmailTemplateClassCampaign || class == EmailTemplateClassTestDirect
	default:
		return false
	}
}
