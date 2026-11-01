package emailauthoring

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
)

func TestResolveEmailTemplatePreviewSubject(t *testing.T) {
	savedSubject := "Saved subject"
	overrideSubject := "Draft subject"

	emailTemplate := Template{Subject: savedSubject}

	assert.Equal(t, savedSubject, resolveEmailTemplatePreviewSubject(PreviewEmailTemplateQuery{}, emailTemplate))
	assert.Equal(t, overrideSubject, resolveEmailTemplatePreviewSubject(PreviewEmailTemplateQuery{
		Subject: &overrideSubject,
	}, emailTemplate))
}

func TestResolveEmailTemplatePreviewHTML(t *testing.T) {
	savedHTML := "<p>saved</p>"

	emailTemplate := Template{ContentHTML: &savedHTML}

	html, err := resolveEmailTemplatePreviewHTML(context.Background(), PreviewEmailTemplateQuery{}, emailTemplate)
	require.NoError(t, err)
	assert.Equal(t, savedHTML, html)
}

func TestResolveEmailTemplatePreviewLayoutID(t *testing.T) {
	savedLayoutID := uuid.NewString()
	overrideLayoutID := uuid.NewString()
	clearLayoutID := ""

	emailTemplate := Template{LayoutID: &savedLayoutID}

	assert.Equal(t, &savedLayoutID, resolveEmailTemplatePreviewLayoutID(PreviewEmailTemplateQuery{}, emailTemplate))
	assert.Equal(t, &overrideLayoutID, resolveEmailTemplatePreviewLayoutID(PreviewEmailTemplateQuery{
		LayoutID: &overrideLayoutID,
	}, emailTemplate))
	assert.Nil(t, resolveEmailTemplatePreviewLayoutID(PreviewEmailTemplateQuery{
		LayoutID: &clearLayoutID,
	}, emailTemplate))
}

func TestValidateEmailTemplateEventKeyUsesAutomaticMailCatalog(t *testing.T) {
	t.Parallel()

	for _, eventKey := range automaticEmailEventKeys() {
		require.NoError(t, validateEmailTemplateEventKey(eventKey.String()))
	}
	require.Error(t, validateEmailTemplateEventKey("custom_event"))
}

func TestVerifiedSystemEmailTemplateVariableNamesVerificationCode(t *testing.T) {
	variableNames := verifiedSystemEmailTemplateVariableNames(email.EventVerificationCode)

	assert.Contains(t, variableNames, "site_name")
	assert.Contains(t, variableNames, "site_origin")
	assert.Contains(t, variableNames, "logo_email_url")
	assert.Contains(t, variableNames, "to")
	assert.Contains(t, variableNames, "recipient_email")
	assert.NotContains(t, variableNames, "subscriber_email")
	assert.Contains(t, variableNames, "identity_email")
	assert.Contains(t, variableNames, "verification_code")
	assert.Contains(t, variableNames, "verification_url")
	assert.Contains(t, variableNames, "expires_in_minutes")
	assert.NotContains(t, variableNames, "identity_name")
}

func TestBuildEmailTemplateSendTestJobBuildsDirectTemplatePayload(t *testing.T) {
	templateID := uuid.NewString()
	runtime := emailTemplateRuntimeFixture{}

	job, err := newEmailTemplateTestEmail(runtime, templateID, "  johndoe@example.com  ", nil, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, "johndoe@example.com", job.Recipient)
	assert.Equal(t, templateID, job.TemplateID)
	assert.Equal(t, "johndoe@example.com", job.Data["recipient_email"])
	assert.Contains(t, job.MessageID, "template-test:")
	assert.Equal(t, "admin-1", job.ActorMemberID)
	assert.Nil(t, job.Locale)

	secondJob, err := newEmailTemplateTestEmail(runtime, templateID, "johndoe@example.com", nil, "admin-1")
	require.NoError(t, err)
	require.NotEqual(t, job.MessageID, secondJob.MessageID, "each explicit test send needs a fresh command identity")
}

func TestEmailTemplateTestSendUsesPreviewSamplesForEverySystemEvent(t *testing.T) {
	job, err := newEmailTemplateTestEmail(emailTemplateRuntimeFixture{}, uuid.NewString(), "admin@example.com", nil, "admin-1")
	require.NoError(t, err)
	previewData := emailTemplateSampleData()
	for key, value := range previewData {
		if key == "recipient_email" || key == "identity_email" || key == "to" {
			value = "admin@example.com"
		}
		assert.Equal(t, value, job.Data[key], key)
	}
	for _, event := range automaticEmailEventKeys() {
		t.Run(event.String(), func(t *testing.T) {
			for _, key := range verifiedSystemEmailTemplateVariableNames(event) {
				if slices.Contains(emailTemplateSiteVariables, key) {
					continue // Site branding is resolved by BuildEmailRenderData at delivery.
				}
				_, err := email.RenderSubjectVarsStrict("{{"+key+"}}", job.Data)
				require.NoError(t, err)
			}
		})
	}
	subject, err := email.RenderSubjectVarsStrict("Login code: {{login_code}}", job.Data)
	require.NoError(t, err)
	assert.Equal(t, "Login code: 789012", subject)
	job.Data["login_code"] = "changed"
	assert.Equal(t, "789012", emailTemplateSampleData()["login_code"])
}

func TestResolveEmailTemplateSendTestLocaleNormalizesOverride(t *testing.T) {
	locale := " pt "

	normalized, err := resolveEmailTemplateSendTestLocale(emailTemplateRuntimeFixture{}, &locale)
	require.NoError(t, err)
	require.NotNil(t, normalized)
	assert.Equal(t, "pt-BR", *normalized)
}

func TestResolveEmailTemplateSendTestLocaleRejectsUnsupportedOverride(t *testing.T) {
	locale := "xx"

	normalized, err := resolveEmailTemplateSendTestLocale(emailTemplateRuntimeFixture{}, &locale)
	require.Nil(t, normalized)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
}

type emailTemplateRuntimeFixture struct{}

func (emailTemplateRuntimeFixture) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}
