package label

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLabelTranslationApplyFenceAllowsUnpublishedRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE label (
		id TEXT PRIMARY KEY,
		content_document_id TEXT NOT NULL,
		source_locale TEXT NOT NULL,
		status TEXT NOT NULL
	)`).Error)

	labelID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		"INSERT INTO label (id, content_document_id, source_locale, status) VALUES (?, ?, ?, ?)",
		labelID, documentID.String(), "en", "LABEL_STATUS_DRAFT",
	).Error)

	applyFence := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			context, fenceErr := internalLabelContentFence(labelID)(t.Context(), tx, documentID)
			if fenceErr == nil {
				require.Equal(t, "en", context.SourceLocale)
			}
			return fenceErr
		})
	}

	require.NoError(t, applyFence(), "an already-submitted translation job may apply to a draft Label")
	require.NoError(t, db.Exec("DELETE FROM label WHERE id = ?", labelID).Error)
	err = applyFence()
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a deleted Label root must reject an existing translation job")
}
