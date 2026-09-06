package artist

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestArtistTranslationApplyFenceAllowsUnpublishedRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE artist (
		id TEXT PRIMARY KEY,
		content_document_id TEXT NOT NULL,
		status TEXT NOT NULL,
		source_locale TEXT NOT NULL
	)`).Error)

	artistID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		"INSERT INTO artist (id, content_document_id, status, source_locale) VALUES (?, ?, ?, ?)",
		artistID, documentID.String(), "ARTIST_STATUS_DRAFT", "en",
	).Error)

	applyFence := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			context, fenceErr := internalCreativeContentFence(artistContentEntity, artistID)(t.Context(), tx, documentID)
			if fenceErr == nil {
				require.Equal(t, "en", context.SourceLocale)
			}
			return fenceErr
		})
	}

	require.NoError(t, applyFence(), "an already-submitted translation job may apply to a draft Artist")
	require.NoError(t, db.Exec("DELETE FROM artist WHERE id = ?", artistID).Error)
	err = applyFence()
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a deleted Artist root must reject an existing translation job")
}
