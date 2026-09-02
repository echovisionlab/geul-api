package legal

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLegalContentDocumentRootUsesPersistedSourceLocale(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE terms_history (
			id TEXT PRIMARY KEY, title TEXT, status TEXT NOT NULL,
			version INTEGER, content_document_id TEXT, source_locale TEXT NOT NULL
		)
	`).Error)
	entityID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO terms_history (id, title, status, version, content_document_id, source_locale) VALUES (?, 'Terms', 'draft', 1, ?, 'ko')",
		entityID, documentID,
	).Error)

	root, err := loadLegalContentDocumentRoot(t.Context(), db, "terms", entityID, false)
	require.NoError(t, err)
	require.Equal(t, "ko", root.SourceLocale)
	require.Equal(t, documentID, root.ContentDocumentID.String())
}
