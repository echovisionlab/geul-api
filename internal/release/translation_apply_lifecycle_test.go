package release

import (
	"testing"

	"connectrpc.com/connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestReleaseTranslationApplyFenceAllowsDraftRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE release (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			content_document_id TEXT,
			source_locale TEXT NOT NULL
		)
	`).Error)

	releaseID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		`INSERT INTO release (id, status, content_document_id, source_locale) VALUES (?, ?, ?, ?)`,
		releaseID, managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(), documentID.String(), "en",
	).Error)

	fence := internalReleaseContentFence(releaseID)
	domain, err := fence(t.Context(), db, documentID)
	require.NoError(t, err)
	require.Equal(t, "en", domain.SourceLocale)

	// The root is the apply authority. Orphaned document/source rows must not
	// make a late result applicable after Release deletion.
	require.NoError(t, db.Exec(`DELETE FROM release WHERE id = ?`, releaseID).Error)
	_, err = fence(t.Context(), db, documentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
