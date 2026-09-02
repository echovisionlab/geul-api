package form

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFormTranslationApplyFenceAllowsUnpublishedRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE form (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL
	)`).Error)

	formID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO form (id, status) VALUES (?, ?)",
		formID, "FORM_STATUS_DRAFT",
	).Error)

	applyFence := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return LockTranslationRoot(t.Context(), tx, formID)
		})
	}

	require.NoError(t, applyFence(), "an already-submitted translation job may apply to a draft Form")
	require.NoError(t, db.Exec("DELETE FROM form WHERE id = ?", formID).Error)
	err = applyFence()
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a deleted Form root must reject an existing translation job")
}
