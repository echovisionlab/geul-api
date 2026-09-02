package emailauthoring

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

type frozenTemplateTranslationReferences struct{}

func (frozenTemplateTranslationReferences) TemplateDeliveryRunCounts(context.Context, *gorm.DB, []string) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (frozenTemplateTranslationReferences) LayoutExternalReferenceCounts(context.Context, *gorm.DB, []string) (map[string]LayoutExternalReferenceCounts, error) {
	return map[string]LayoutExternalReferenceCounts{}, nil
}

func (frozenTemplateTranslationReferences) RequireTemplateMutable(context.Context, *gorm.DB, string) error {
	return errs.FailedPrecondition("Email Template is frozen by an active delivery run")
}

func (frozenTemplateTranslationReferences) RequireLayoutMutable(context.Context, *gorm.DB, string) error {
	return errs.FailedPrecondition("Email Layout is frozen by an active delivery run")
}

func (frozenTemplateTranslationReferences) DetachTemplateHistory(context.Context, *gorm.DB, string) error {
	return nil
}

func (frozenTemplateTranslationReferences) DetachLayoutHistory(context.Context, *gorm.DB, string) error {
	return nil
}

func TestEmailTemplateTranslationJobApplyFenceUsesRootExistenceOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE email_template (
			id TEXT PRIMARY KEY,
			content_document_id TEXT,
			source_locale TEXT NOT NULL,
			is_active BOOLEAN NOT NULL
		)
	`).Error)

	templateID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		"INSERT INTO email_template (id, content_document_id, source_locale, is_active) VALUES (?, ?, 'en', FALSE)",
		templateID, documentID,
	).Error)

	domain, err := emailTemplateTranslationJobApplyFence(
		emailTemplateContentEntity,
		templateID,
	)(t.Context(), db, documentID)
	require.NoError(t, err, "an unpublished Template keeps an already-accepted job")
	require.Equal(t, "en", domain.SourceLocale)

	_, editErr := campaignEmailContentFence(
		frozenTemplateTranslationReferences{},
		emailTemplateContentEntity,
		templateID,
	)(t.Context(), db, documentID)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(editErr), "direct edits retain the delivery freeze")

	require.NoError(t, db.Exec("DELETE FROM email_template WHERE id = ?", templateID).Error)
	_, err = emailTemplateTranslationJobApplyFence(
		emailTemplateContentEntity,
		templateID,
	)(t.Context(), db, documentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
