package page

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPageTranslationApplyFenceAllowsDraftRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE page (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			content_document_id TEXT,
			source_locale TEXT NOT NULL
		)
	`).Error)

	pageID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		`INSERT INTO page (id, status, content_document_id, source_locale) VALUES (?, ?, ?, ?)`,
		pageID, "PAGE_STATUS_DRAFT", documentID.String(), "en",
	).Error)

	fence := pageSystemTranslationDocumentFence(pageID)
	domain, err := fence(t.Context(), db, documentID)
	require.NoError(t, err)
	require.Equal(t, "en", domain.SourceLocale)

	// A deleted Page must reject a late result.
	require.NoError(t, db.Exec(`DELETE FROM page WHERE id = ?`, pageID).Error)
	_, err = fence(t.Context(), db, documentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
