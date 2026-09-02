package series

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSeriesTranslationApplyFenceAllowsUnpublishedRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE series (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL
	)`).Error)

	seriesID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO series (id, status) VALUES (?, ?)",
		seriesID, "SERIES_STATUS_DRAFT",
	).Error)

	applyFence := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return lockSeriesRoot(t.Context(), tx, seriesID)
		})
	}

	require.NoError(t, applyFence(), "an already-submitted translation job may apply to a draft Series")
	require.NoError(t, db.Exec("DELETE FROM series WHERE id = ?", seriesID).Error)
	err = applyFence()
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a deleted Series root must reject an existing translation job")
}
