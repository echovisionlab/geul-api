//go:build integration

package email

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRenderTemplateForLocaleIntegration(t *testing.T) {
	db := newEmailIntegrationDB(t)
	layoutID := seedEmailLayout(t, db)
	sourceLayout, err := CanonicalizeLayoutSourceMarkers("<main>{{content}}</main><footer>Layout footer</footer>")
	require.NoError(t, err)
	units, err := ExtractLayoutContentUnits(sourceLayout)
	require.NoError(t, err)
	footerHandle := ""
	for _, unit := range units {
		if unit.SourceValue == "Layout footer" {
			footerHandle = unit.Handle
			break
		}
	}
	require.NotEmpty(t, footerHandle)
	targetLayout, _, err := ApplyLayoutLocaleValues(sourceLayout, map[string]string{footerHandle: "레이아웃"})
	require.NoError(t, err)
	seedEmailLayoutTranslation(t, db, layoutID, "en", sourceLayout)
	seedEmailLayoutTranslation(t, db, layoutID, "ko", *targetLayout)

	templateID := seedEmailTemplate(t, db, layoutID)
	seedEmailTemplateTranslation(t, db, templateID, "en", "Welcome {{recipient_email}}", "<p>Hello {{recipient_email}}</p>")
	seedEmailTemplateTranslation(t, db, templateID, "ko", "환영합니다 {{recipient_email}}", "<p>안녕하세요 {{recipient_email}}</p>")

	rendered, err := RenderTemplateForLocale(
		context.Background(),
		db,
		DirectTemplateType(templateID),
		"ko",
		map[string]string{"recipient_email": "johndoe@example.com"},
	)
	require.NoError(t, err)
	require.NotNil(t, rendered)
	assert.Equal(t, "환영합니다 johndoe@example.com", rendered.Subject)
	assert.Contains(t, rendered.HTML, "레이아웃")
	assert.Contains(t, rendered.HTML, "안녕하세요 johndoe@example.com")
	assert.Equal(t, "ko", rendered.TemplateLocale)
	assert.Equal(t, "ko", rendered.LayoutLocale)
}

func newEmailIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newEmailIntegrationTransaction(t)
}

func seedEmailTemplate(t *testing.T, db *gorm.DB, layoutID string) string {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	id := uuid.NewString()
	contentDocumentID := uuid.NewString()
	revision := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, 'email', ?, ?, ?)`,
		contentDocumentID,
		revision,
		now,
		now,
	).Error)
	template := model.EmailTemplate{
		ID:                id,
		ContentDocumentID: &contentDocumentID,
		Key:               "template-" + id,
		Name:              "Template " + id,
		IsActive:          true,
		Variables:         model.EmailTemplateVariables{},
		CreatedAt:         now,
		UpdatedAt:         &now,
	}
	if layoutID != "" {
		template.LayoutID = &layoutID
	}
	require.NoError(t, db.Create(&template).Error)
	return id
}

func seedEmailLayout(t *testing.T, db *gorm.DB) string {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	id := uuid.NewString()
	contentDocumentID := uuid.NewString()
	revision := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, 'compact', ?, ?, ?)`,
		contentDocumentID,
		revision,
		now,
		now,
	).Error)
	require.NoError(t, db.Create(&model.EmailLayout{
		ID:                id,
		ContentDocumentID: contentDocumentID,
		SourceLocale:      "en",
		Key:               "layout-" + id,
		Name:              "Layout " + id,
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	return id
}

func seedEmailTemplateTranslation(t *testing.T, db *gorm.DB, entityID string, locale string, subject string, html string) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO email_template_translation (
			entity_id, locale, subject, content_html, content_text, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entityID,
		locale,
		subject,
		html,
		StripHTML(html),
		now,
		now,
	).Error)
}

func seedEmailLayoutTranslation(t *testing.T, db *gorm.DB, entityID string, locale string, html string) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO email_layout_translation (
			entity_id, locale, html_content, content_text, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		entityID,
		locale,
		html,
		StripHTML(html),
		now,
		now,
	).Error)
}
