package emailauthoring

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
)

func TestVerifiedSystemEmailTemplateMetadataRecognizesEverySystemEvent(t *testing.T) {
	t.Parallel()

	for _, eventKey := range automaticEmailEventKeys() {
		t.Run(eventKey.String(), func(t *testing.T) {
			t.Parallel()

			resolvedEventKey, variables, ok := verifiedSystemEmailTemplateMetadata(" " + eventKey.String() + " ")

			require.True(t, ok)
			require.NotNil(t, resolvedEventKey)
			require.Equal(t, eventKey.String(), *resolvedEventKey)
			require.NotEmpty(t, variables)
			require.Contains(t, emailTemplateVariableNames(variables), "site_name")
			require.Contains(t, emailTemplateVariableNames(variables), "site_origin")
			require.Contains(t, emailTemplateVariableNames(variables), "logo_email_url")
		})
	}
}

func TestVerifiedSystemEmailTemplateMetadataRejectsUnknownTemplateKey(t *testing.T) {
	t.Parallel()

	eventKey, variables, ok := verifiedSystemEmailTemplateMetadata("not-a-system-template")

	require.False(t, ok)
	require.Nil(t, eventKey)
	require.Nil(t, variables)
}

func TestVerifiedSystemEmailTemplateVariableMatrix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		eventKey email.EventKey
		want     []string
		notWant  []string
	}{
		{
			name:     "account recovery confirm",
			eventKey: email.EventAccountRecoveryConfirm,
			want:     []string{"site_name", "name", "recipient_name", "confirm_url", "expires_in"},
			notWant:  []string{"terms_url"},
		},
		{
			name:     "account recovery complete",
			eventKey: email.EventAccountRecoveryComplete,
			want:     []string{"site_name", "name", "recipient_name", "login_url"},
			notWant:  []string{"confirm_url", "expires_in"},
		},
		{
			name:     "verification code",
			eventKey: email.EventVerificationCode,
			want:     []string{"site_name", "recipient_email", "verification_code", "verification_url", "expires_in_minutes"},
			notWant:  []string{"name", "request_url"},
		},
		{
			name:     "login code",
			eventKey: email.EventLoginCode,
			want:     []string{"site_name", "recipient_email", "login_code", "expires_in_minutes"},
			notWant:  []string{"registration_code", "request_url"},
		},
		{
			name:     "registration code",
			eventKey: email.EventRegistrationCode,
			want:     []string{"site_name", "recipient_email", "registration_code", "expires_in_minutes"},
			notWant:  []string{"login_code", "request_url"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			variables := emailTemplateVariableNames(verifiedSystemEmailTemplateVariables(tc.eventKey))

			for _, want := range tc.want {
				require.Contains(t, variables, want)
			}
			for _, notWant := range tc.notWant {
				require.NotContains(t, variables, notWant)
			}
		})
	}
}

func TestUniqueEmailTemplateVariableNamesNormalizesAndDeduplicates(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		[]string{"name", "recipient_email", "site_origin"},
		uniqueEmailTemplateVariableNames([]string{
			" Name ",
			"name",
			"",
			"RECIPIENT_EMAIL",
			" recipient_email ",
			"SITE_ORIGIN",
		}),
	)
}

func emailTemplateVariableNames(variables model.EmailTemplateVariables) []string {
	names := make([]string, 0, len(variables))
	for _, variable := range variables {
		names = append(names, variable.Name)
	}
	return names
}
