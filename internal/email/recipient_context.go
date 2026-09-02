package email

import (
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func AccountSelectedPrimaryEmailContext(identityID string) *managev1.SendEmailEvent_AccountSelectedPrimaryEmail {
	return &managev1.SendEmailEvent_AccountSelectedPrimaryEmail{
		AccountSelectedPrimaryEmail: &managev1.AccountSelectedPrimaryEmailRecipient{
			IdentityId: strings.TrimSpace(identityID),
		},
	}
}

func NewsletterSubscriptionContext(identityID, memberID string) *managev1.SendEmailEvent_NewsletterSubscription {
	return &managev1.SendEmailEvent_NewsletterSubscription{
		NewsletterSubscription: &managev1.NewsletterSubscriptionRecipient{
			IdentityId: strings.TrimSpace(identityID),
			MemberId:   strings.TrimSpace(memberID),
		},
	}
}

func AccountVerificationContext(identityID, targetEmail string) *managev1.SendEmailEvent_AccountVerification {
	return &managev1.SendEmailEvent_AccountVerification{
		AccountVerification: &managev1.AccountVerificationRecipient{
			IdentityId:  strings.TrimSpace(identityID),
			TargetEmail: strings.TrimSpace(targetEmail),
		},
	}
}

func TestEmailContext(actorMemberID string) *managev1.SendEmailEvent_TestEmail {
	return &managev1.SendEmailEvent_TestEmail{
		TestEmail: &managev1.TestEmailRecipient{ActorMemberId: strings.TrimSpace(actorMemberID)},
	}
}

func SystemDirectEmailContext(reason string) *managev1.SendEmailEvent_SystemDirect {
	return &managev1.SendEmailEvent_SystemDirect{
		SystemDirect: &managev1.SystemDirectRecipient{Reason: strings.TrimSpace(reason)},
	}
}
