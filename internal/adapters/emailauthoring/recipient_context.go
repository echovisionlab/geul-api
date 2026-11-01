package emailauthoring

import (
	emailauthoringdomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// TemplateContextAllowed reports whether job's template class may go to the
// recipient its context names; a job without a known context never may.
func TemplateContextAllowed(job *managev1.SendEmailEvent) bool {
	if job == nil {
		return false
	}
	return emailauthoringdomain.EmailTemplateContextAllowed(job.GetTemplateType(), recipientContext(job))
}

func recipientContext(job *managev1.SendEmailEvent) emailauthoringdomain.RecipientContext {
	switch job.GetRecipientContext().(type) {
	case *managev1.SendEmailEvent_AccountSelectedPrimaryEmail:
		return emailauthoringdomain.RecipientAccountSelectedPrimaryEmail
	case *managev1.SendEmailEvent_NewsletterSubscription:
		return emailauthoringdomain.RecipientNewsletterSubscription
	case *managev1.SendEmailEvent_AccountVerification:
		return emailauthoringdomain.RecipientAccountVerification
	case *managev1.SendEmailEvent_AuthLogin:
		return emailauthoringdomain.RecipientAuthLogin
	case *managev1.SendEmailEvent_AuthRegistration:
		return emailauthoringdomain.RecipientAuthRegistration
	case *managev1.SendEmailEvent_SystemDirect:
		return emailauthoringdomain.RecipientSystemDirect
	case *managev1.SendEmailEvent_TestEmail:
		return emailauthoringdomain.RecipientTestEmail
	default:
		return ""
	}
}
