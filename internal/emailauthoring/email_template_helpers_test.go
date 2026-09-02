package emailauthoring

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestResolveEmailTemplatePreviewSubject(t *testing.T) {
	savedSubject := "Saved subject"
	overrideSubject := "Draft subject"

	emailTemplate := model.EmailTemplate{Subject: savedSubject}

	assert.Equal(t, savedSubject, resolveEmailTemplatePreviewSubject(&managev1.PreviewEmailTemplateRequest{}, emailTemplate))
	assert.Equal(t, overrideSubject, resolveEmailTemplatePreviewSubject(&managev1.PreviewEmailTemplateRequest{
		Subject: &overrideSubject,
	}, emailTemplate))
}

func TestResolveEmailTemplatePreviewHTML(t *testing.T) {
	savedHTML := "<p>saved</p>"

	emailTemplate := model.EmailTemplate{ContentHTML: &savedHTML}

	html, err := resolveEmailTemplatePreviewHTML(context.Background(), &managev1.PreviewEmailTemplateRequest{}, emailTemplate)
	require.NoError(t, err)
	assert.Equal(t, savedHTML, html)
}

func TestResolveEmailTemplatePreviewLayoutID(t *testing.T) {
	savedLayoutID := uuid.NewString()
	overrideLayoutID := uuid.NewString()
	clearLayoutID := ""

	emailTemplate := model.EmailTemplate{LayoutID: &savedLayoutID}

	assert.Equal(t, &savedLayoutID, resolveEmailTemplatePreviewLayoutID(&managev1.PreviewEmailTemplateRequest{}, emailTemplate))
	assert.Equal(t, &overrideLayoutID, resolveEmailTemplatePreviewLayoutID(&managev1.PreviewEmailTemplateRequest{
		LayoutId: &overrideLayoutID,
	}, emailTemplate))
	assert.Nil(t, resolveEmailTemplatePreviewLayoutID(&managev1.PreviewEmailTemplateRequest{
		LayoutId: &clearLayoutID,
	}, emailTemplate))
}

func TestValidateEmailTemplateEventKeyUsesAutomaticMailCatalog(t *testing.T) {
	t.Parallel()

	for _, eventKey := range automaticEmailEventKeys() {
		require.NoError(t, validateEmailTemplateEventKey(eventKey.String()))
	}
	require.Error(t, validateEmailTemplateEventKey("custom_event"))
}

func TestEmailTemplateBaseRowInsertOmitsRemovedCanonicalColumns(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	now := time.Unix(1_700_000_000, 0).UTC()
	description := "Welcome"
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Omit("ID").
			Clauses(clause.Returning{}).
			Create(&emailTemplateBaseRow{
				Key:         "welcome_template",
				Name:        "Welcome Template",
				Description: &description,
				IsSystem:    true,
				IsActive:    true,
				CreatedAt:   now,
				UpdatedAt:   &now,
			})
	})
	assert.Contains(t, sql, `INSERT INTO "email_template"`)
	assert.NotContains(t, sql, `"subject"`)
	assert.NotContains(t, sql, `"content_html"`)
	assert.NotContains(t, sql, `"yjs_state"`)
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

	job, err := buildEmailTemplateSendTestJob(runtime, templateID, "  johndoe@example.com  ", nil, "admin-1")
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "johndoe@example.com", job.Recipient)
	assert.Equal(t, email.DirectTemplateType(templateID), job.TemplateType)
	assert.Equal(t, "johndoe@example.com", job.TemplateData["recipient_email"])
	require.NotNil(t, job.ReferenceId)
	assert.Equal(t, templateID, *job.ReferenceId)
	require.NotEmpty(t, job.GetMessageId())
	assert.Contains(t, job.GetMessageId(), "template-test:")
	require.NotNil(t, job.GetTestEmail())
	assert.Equal(t, "admin-1", job.GetTestEmail().GetActorMemberId())
	assert.Nil(t, job.Locale)

	secondJob, err := buildEmailTemplateSendTestJob(runtime, templateID, "johndoe@example.com", nil, "admin-1")
	require.NoError(t, err)
	require.NotEqual(t, job.GetMessageId(), secondJob.GetMessageId(), "each explicit test send needs a fresh command identity")
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
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

type emailTemplateRuntimeFixture struct{}

func (emailTemplateRuntimeFixture) ResolveLocalizedTemplate(ctx context.Context, db *gorm.DB, template model.EmailTemplate, locale string) (model.EmailTemplate, error) {
	resolved, _, err := email.ResolveLocalizedEmailTemplate(ctx, db, template, locale)
	return resolved, err
}

func (emailTemplateRuntimeFixture) RenderVariables(value string, data map[string]string) string {
	return email.RenderVars(value, data)
}

func (emailTemplateRuntimeFixture) WrapWithLayout(ctx context.Context, db *gorm.DB, layoutID, locale, content string, data map[string]string) (string, error) {
	rendered, _, err := email.WrapWithLayoutForLocaleStrict(ctx, db, layoutID, locale, content, data)
	return rendered, err
}

func (emailTemplateRuntimeFixture) NormalizeRenderedHTML(value string) string {
	return email.NormalizeRenderedHTML(value)
}

func (emailTemplateRuntimeFixture) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}
