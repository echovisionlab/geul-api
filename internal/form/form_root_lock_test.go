package form

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
)

func TestFormRootLockAllowsUnpublishedRootAndRejectsDeletedRoot(t *testing.T) {
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
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

	lock := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var form model.Form
			return lockFormRoot(t.Context(), tx, formID, &form, domainerr.ResourceNotFound("form", formID), "id")
		})
	}

	require.NoError(t, lock(), "a draft Form root is lockable")
	require.NoError(t, db.Exec("DELETE FROM form WHERE id = ?", formID).Error)
	err = lock()
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code(), "a deleted Form root answers not found")
}
