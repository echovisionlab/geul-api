package emailauthoring

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/stretchr/testify/require"
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
