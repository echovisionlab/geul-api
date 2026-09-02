package emailauthoring

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestClassifyEmailTemplateType(t *testing.T) {
	tests := []struct {
		name         string
		templateType string
		want         EmailTemplateClass
	}{
		{name: "campaign", templateType: "campaign:campaign-1", want: EmailTemplateClassCampaign},
		{name: "direct template", templateType: email.DirectTemplateType("template-1"), want: EmailTemplateClassTestDirect},
		{name: "legal update", templateType: email.EventTermsUpdate.String(), want: EmailTemplateClassLegalNotice},
		{name: "legal effective", templateType: email.EventPrivacyEffective.String(), want: EmailTemplateClassLegalNotice},
		{name: "account transactional", templateType: email.EventWelcome.String(), want: EmailTemplateClassAccountTransactional},
		{name: "account verification", templateType: email.EventVerificationCode.String(), want: EmailTemplateClassAccountVerification},
		{name: "auth login", templateType: email.EventLoginCode.String(), want: EmailTemplateClassAuthLogin},
		{name: "auth registration", templateType: email.EventRegistrationCode.String(), want: EmailTemplateClassAuthRegistration},
		{name: "empty campaign id", templateType: "campaign:", want: EmailTemplateClassUnknown},
		{name: "unknown", templateType: "custom", want: EmailTemplateClassUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ClassifyEmailTemplateType(tt.templateType))
		})
	}
}

func TestEmailTemplateContextAllowed(t *testing.T) {
	tests := []struct {
		name string
		job  *managev1.SendEmailEvent
		want bool
	}{
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, EmailTemplateContextAllowed(tt.job))
		})
	}
}
