//go:build integration

package emailpostgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/email/emailtest"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	emailtest.RunStoreContract(t, func(t *testing.T) emailtest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return emailtest.Harness{
			Harness:             mutationtest.PostgresHarness(db, mutation.Postgres(db, emailpostgres.Bind)),
			SeedTemplate:        func(t *testing.T, template model.EmailTemplate) string { return seedTemplate(t, db, template) },
			SeedTemplateContent: func(t *testing.T, id string, content email.TemplateContent) { seedTemplateContent(t, db, id, content) },
			SeedLayout:          func(t *testing.T, sourceLocale string) string { return seedLayout(t, db, sourceLocale) },
			SeedSuppression: func(t *testing.T, address string, released bool) {
				suppression := model.EmailSuppression{Email: address, Reason: "bounce", Source: "admin"}
				if released {
					now := time.Now()
					suppression.ReleasedAt = &now
				}
				require.NoError(t, db.Create(&suppression).Error)
			},
			SeedMailAdapter: func(t *testing.T, adapter model.MailAdapter) string {
				adapter.ID = uuid.NewString()
				require.NoError(t, db.Create(&adapter).Error)
				// gorm skips a false IsActive in favor of the column default.
				require.NoError(t, db.Model(&model.MailAdapter{}).Where("id = ?", adapter.ID).Update("is_active", adapter.IsActive).Error)
				return adapter.ID
			},
		}
	})
}

func seedContentDocument(t *testing.T, db *gorm.DB, profile string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, ?, ?)`, id, profile, uuid.NewString(),
	).Error)
	return id
}

func seedTemplate(t *testing.T, db *gorm.DB, template model.EmailTemplate) string {
	t.Helper()
	contentDocumentID := seedContentDocument(t, db, "email")
	template.ID = uuid.NewString()
	template.ContentDocumentID = &contentDocumentID
	template.Key = "template-" + template.ID
	template.Name = "Template " + template.ID
	template.Variables = model.EmailTemplateVariables{}
	require.NoError(t, db.Create(&template).Error)
	// gorm skips a false IsActive in favor of the column default.
	require.NoError(t, db.Model(&model.EmailTemplate{}).Where("id = ?", template.ID).Update("is_active", template.IsActive).Error)
	return template.ID
}

func seedTemplateContent(t *testing.T, db *gorm.DB, templateID string, content email.TemplateContent) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO email_template_translation (entity_id, locale, subject, content_html) VALUES (?, ?, ?, ?)`,
		templateID, content.Locale, content.Subject, content.ContentHTML,
	).Error)
}

func seedLayout(t *testing.T, db *gorm.DB, sourceLocale string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Create(&model.EmailLayout{
		ID: id, ContentDocumentID: seedContentDocument(t, db, "compact"), SourceLocale: sourceLocale,
		Key: "layout-" + id, Name: "Layout " + id,
	}).Error)
	return id
}
