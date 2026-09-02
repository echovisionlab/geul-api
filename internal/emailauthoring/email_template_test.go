//go:build integration

package emailauthoring

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestEmailTemplateServiceCreateEmailTemplateSeedsCanonicalSourceLocaleRowIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	contentBlocks := testutil.NewEmailContentBlockStore(t, spiceDB)
	svc := NewEmailTemplateService(
		db, nil, emailTemplateRuntimeFixture{}, "https://cdn.example.com", "https://example.com", spiceDB,
		WithEmailTemplateContentBlockStore(contentBlocks),
		WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
	)
	subject := "Welcome to {{site_name}}"

	resp, err := svc.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
		Key:          "welcome_template",
		Name:         "Welcome Template",
		Subject:      subject,
		SourceLocale: "ko-KR",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg)
	assert.Equal(t, subject, resp.Msg.Subject)

	var root struct {
		SourceLocale string    `gorm:"column:source_locale"`
		DocumentID   uuid.UUID `gorm:"column:content_document_id"`
		Profile      string    `gorm:"column:profile"`
	}
	require.NoError(t, db.Table("email_template AS root").
		Select("root.source_locale, root.content_document_id, document.profile").
		Joins("JOIN content_document AS document ON document.id = root.content_document_id").
		Where("root.id = ?", resp.Msg.Id).
		Take(&root).Error)
	assert.Equal(t, "ko", root.SourceLocale)
	assert.NotEqual(t, uuid.Nil, root.DocumentID)
	assert.Equal(t, emailContentProfile, root.Profile)

	var sourceRow struct {
		Subject *string `gorm:"column:subject"`
	}
	require.NoError(t, db.Table("email_template_translation").
		Select("subject").
		Where("entity_id = ? AND locale = ?", resp.Msg.Id, "ko").
		Take(&sourceRow).Error)
	require.NotNil(t, sourceRow.Subject)
	assert.Equal(t, subject, *sourceRow.Subject)
}

func TestEmailTemplateServiceCreateEmailTemplateRejectsInvalidSourceLocaleWithoutPersistenceIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	service := NewEmailTemplateService(
		db, nil, emailTemplateRuntimeFixture{}, "", "", spiceDB,
		WithEmailTemplateContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
	)

	var documentsBefore int64
	require.NoError(t, db.Table("content_document").Count(&documentsBefore).Error)
	for _, test := range []struct {
		name         string
		key          string
		sourceLocale string
	}{
		{name: "missing", key: "missing_source_locale", sourceLocale: ""},
		{name: "unsupported", key: "unsupported_source_locale", sourceLocale: "xx-ZZ"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
				Key: test.key, Name: "Invalid source locale", Subject: "Subject", SourceLocale: test.sourceLocale,
			}))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

			var templates int64
			require.NoError(t, db.Table("email_template").Where("key = ?", test.key).Count(&templates).Error)
			require.Zero(t, templates)
			var documentsAfter int64
			require.NoError(t, db.Table("content_document").Count(&documentsAfter).Error)
			require.Equal(t, documentsBefore, documentsAfter)
		})
	}
}

func TestEmailTemplateServiceCreateEmailTemplatePersistsRootDocumentAndSourceSubjectAtomicallyIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	service := NewEmailTemplateService(
		db, nil, emailTemplateRuntimeFixture{}, "", "", spiceDB,
		WithEmailTemplateContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
	)

	var templatesBefore, translationsBefore, documentsBefore int64
	require.NoError(t, db.Table("email_template").Count(&templatesBefore).Error)
	require.NoError(t, db.Table("email_template_translation").Count(&translationsBefore).Error)
	require.NoError(t, db.Table("content_document").Count(&documentsBefore).Error)

	created, err := service.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
		Key: "atomic_source_subject", Name: "Atomic source subject", Subject: "Subject", SourceLocale: "ko-KR",
	}))
	require.NoError(t, err)

	for _, snapshot := range []struct {
		table string
		want  int64
	}{
		{table: "email_template", want: templatesBefore + 1},
		{table: "email_template_translation", want: translationsBefore + 1},
		{table: "content_document", want: documentsBefore + 1},
	} {
		var got int64
		require.NoError(t, db.Table(snapshot.table).Count(&got).Error)
		require.Equal(t, snapshot.want, got, snapshot.table)
	}
	var sourceSubject string
	require.NoError(t, db.Table("email_template_translation").
		Select("subject").
		Where("entity_id = ? AND locale = ?", created.Msg.Id, "ko").
		Scan(&sourceSubject).Error)
	require.Equal(t, "Subject", sourceSubject)
}

func TestEmailTemplateServiceListDoesNotRequireTranslationSourceStateIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	now := time.Now().UTC()
	templateID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'email')`,
		documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO email_template (
			id, key, name, variables, is_system, is_active, content_document_id, created_at, updated_at
		) VALUES (?, ?, ?, '[]'::jsonb, FALSE, TRUE, ?, ?, ?)
	`, templateID, "missing_source_"+uuid.NewString(), "Missing source", documentID, now, now).Error)

	service := NewEmailTemplateService(
		db,
		nil,
		emailTemplateRuntimeFixture{},
		"https://cdn.example.com",
		"https://example.com",
		spiceDB,
		WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
	)
	response, err := service.ListEmailTemplatesAdmin(
		ctx,
		connect.NewRequest(&managev1.ListEmailTemplatesAdminRequest{}),
	)

	require.NoError(t, err)
	require.NotNil(t, response)
	require.Condition(t, func() bool {
		for _, template := range response.Msg.Templates {
			if template.GetId() == templateID {
				return true
			}
		}
		return false
	})

	var stored model.EmailTemplate
	require.NoError(t, db.First(
		&stored,
		"id = ?",
		templateID,
	).Error)
}

func TestCustomEmailTemplateVariablesFollowTypedSourceBlocksIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	templateService := NewEmailTemplateService(
		db, nil, emailTemplateRuntimeFixture{}, "https://cdn.example.com", "https://example.com", spiceDB,
		WithEmailTemplateContentBlockStore(store),
		WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
	)
	created, err := templateService.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
		Key:          "variable_catalog_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Name:         "Variable catalog",
		Subject:      "Variable catalog",
		SourceLocale: "en",
	}))
	require.NoError(t, err)

	publishEmailTemplateSourceBlocksForIntegration(
		t,
		db,
		spiceDB,
		created.Msg.Id,
		"{{ Recipient_Name }} {{custom_code}} {{recipient_name}}",
	)
	require.Equal(t, []string{"custom_code", "recipient_name"}, storedEmailTemplateVariableNames(t, db, created.Msg.Id))
}

func storedEmailTemplateVariableNames(t *testing.T, db *gorm.DB, templateID string) []string {
	t.Helper()
	var template model.EmailTemplate
	require.NoError(t, db.Select("variables").First(&template, "id = ?", templateID).Error)
	names := make([]string, 0, len(template.Variables))
	for _, variable := range template.Variables {
		names = append(names, variable.Name)
	}
	return names
}
