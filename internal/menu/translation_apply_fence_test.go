package menu

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMenuTranslationApplyFenceAllowsExistingRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE menu (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		items BLOB NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error)

	menuID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO menu (id, name, items) VALUES (?, ?, ?)",
		menuID, "Main", []byte(`[]`),
	).Error)

	applyFence := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			_, fenceErr := lockMenuForUpdate(t.Context(), tx, menuID)
			return fenceErr
		})
	}

	require.NoError(t, applyFence(), "an already-submitted translation job may apply while the Menu root exists")
	require.NoError(t, db.Exec("DELETE FROM menu WHERE id = ?", menuID).Error)
	err = applyFence()
	require.ErrorIs(t, err, gorm.ErrRecordNotFound,
		"a deleted Menu root must reject an existing translation job")
}
