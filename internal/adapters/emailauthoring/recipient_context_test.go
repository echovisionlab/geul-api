package emailauthoring

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

type templateContextAllowedCase struct {
	name string
	job  *managev1.SendEmailEvent
	want bool
}

func TestTemplateContextAllowed(t *testing.T) {
	tests := []templateContextAllowedCase{
		{
			name: "campaign allows newsletter subscription",
			job: &managev1.SendEmailEvent{
				TemplateType:     "campaign:campaign-1",
				RecipientContext: email.NewsletterSubscriptionContext("identity-1", "member-1"),
			},
			want: true,
		},
		{
			name: "campaign allows test send",
			job: &managev1.SendEmailEvent{
				TemplateType:     "campaign:campaign-1",
				RecipientContext: email.TestEmailContext("admin-1"),
			},
			want: true,
		},
		{
			name: "campaign allows account current user target",
			job: &managev1.SendEmailEvent{
				TemplateType:     "campaign:campaign-1",
				RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-1"),
			},
			want: true,
		},
		{
			name: "campaign rejects verification bypass",
			job: &managev1.SendEmailEvent{
				TemplateType:     "campaign:campaign-1",
				RecipientContext: email.AccountVerificationContext("identity-1", "user@example.com"),
			},
			want: false,
		},
		{
			name: "legal notice allows account current email",
			job: &managev1.SendEmailEvent{
				TemplateType:     email.EventTermsEffective.String(),
				RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-1"),
			},
			want: true,
		},
		{
			name: "legal notice rejects newsletter subscription context",
			job: &managev1.SendEmailEvent{
				TemplateType:     email.EventTermsEffective.String(),
				RecipientContext: email.NewsletterSubscriptionContext("identity-1", "member-1"),
			},
			want: false,
		},
		{
			name: "account transactional allows system direct",
			job: &managev1.SendEmailEvent{
				TemplateType:     email.EventAccountDeletionComplete.String(),
				RecipientContext: email.SystemDirectEmailContext(email.EventAccountDeletionComplete.String()),
			},
			want: true,
		},
	}
	tests = append(tests, authenticationTemplateContextCases()...)
	tests = append(tests, []templateContextAllowedCase{
		{
			name: "direct template only allows test send",
			job: &managev1.SendEmailEvent{
				TemplateType:     email.DirectTemplateType("template-1"),
				RecipientContext: email.TestEmailContext("admin-1"),
			},
			want: true,
		},
		{
			name: "unknown template rejects account current context",
			job: &managev1.SendEmailEvent{
				TemplateType:     "custom",
				RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-1"),
			},
			want: false,
		},
		{
			name: "missing context rejects",
			job: &managev1.SendEmailEvent{
				TemplateType: email.EventWelcome.String(),
			},
			want: false,
		},
	}...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, TemplateContextAllowed(tt.job))
		})
	}
}

func authenticationTemplateContextCases() []templateContextAllowedCase {
	return []templateContextAllowedCase{
		{
			name: "account verification only allows verification context",
			job: &managev1.SendEmailEvent{
				TemplateType:     email.EventVerificationCode.String(),
				RecipientContext: email.AccountVerificationContext("identity-1", "user@example.com"),
			},
			want: true,
		},
		{
			name: "auth login only allows login context",
			job: &managev1.SendEmailEvent{
				TemplateType: email.EventLoginCode.String(),
				RecipientContext: &managev1.SendEmailEvent_AuthLogin{
					AuthLogin: &managev1.AuthLoginRecipient{
						IdentityId:  "identity-1",
						TargetEmail: "user@example.com",
					},
				},
			},
			want: true,
		},
		{
			name: "auth registration only allows pre-identity registration context",
			job: &managev1.SendEmailEvent{
				TemplateType: email.EventRegistrationCode.String(),
				RecipientContext: &managev1.SendEmailEvent_AuthRegistration{
					AuthRegistration: &managev1.AuthRegistrationRecipient{
						TargetEmail: "new@example.com",
					},
				},
			},
			want: true,
		},
		{
			name: "auth login rejects registration context",
			job: &managev1.SendEmailEvent{
				TemplateType: email.EventLoginCode.String(),
				RecipientContext: &managev1.SendEmailEvent_AuthRegistration{
					AuthRegistration: &managev1.AuthRegistrationRecipient{
						TargetEmail: "user@example.com",
					},
				},
			},
			want: false,
		},
	}
}
