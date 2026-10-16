package menu

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSiteVariableMigrationInvalidatesStaleMenuEditor(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	now := time.Now().UTC().Add(-time.Hour)
	items := []byte(`[{"id":"home","url":"https://www.dsub.io/path"}]`)
	documentID, revision := seedMenuAIDocumentStateTestRoot(t, db, "menu-1", "en", items, now)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return MigrateSiteVariableURLs(t.Context(), tx, "menu-1", func(id uuid.UUID) error {
			require.Equal(t, documentID, id.String())
			return tx.Table("menu").Where("id = ?", "menu-1").UpdateColumn("items", []byte(`[{"id":"home","url":"/path"}]`)).Error
		})
	}))
	root, err := lockMenuForUpdate(t.Context(), db, "menu-1")
	require.NoError(t, err)
	current, err := loadMenuContentDocumentStateFromRoot(t.Context(), db, *root, false)
	require.NoError(t, err)
	require.NotEqual(t, revision, current.Revision.String())
	require.True(t, root.UpdatedAt.After(now))
	// Normal saves reject the editor's pre-migration CAS revision.
	_, err = advanceMenuContentDocument(t.Context(), db, "menu-1", current.ID, uuid.MustParse(revision), time.Now())
	require.ErrorContains(t, err, "revision changed")
	require.JSONEq(t, `[{"id":"home","url":"/path"}]`, string(root.Items))
}

func TestSiteVariableMigrationFailurePreservesMenuAndRevision(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	items := []byte(`[{"id":"home","url":"https://www.dsub.io/path"}]`)
	_, revision := seedMenuAIDocumentStateTestRoot(t, db, "menu-1", "en", items, time.Now().UTC())
	wantErr := errors.New("rewrite failed")
	err := db.Transaction(func(tx *gorm.DB) error {
		return MigrateSiteVariableURLs(t.Context(), tx, "menu-1", func(uuid.UUID) error {
			if err := tx.Table("menu").Where("id = ?", "menu-1").UpdateColumn("items", []byte(`[]`)).Error; err != nil {
				return err
			}
			return wantErr
		})
	})
	require.ErrorIs(t, err, wantErr)
	root, err := lockMenuForUpdate(t.Context(), db, "menu-1")
	require.NoError(t, err)
	current, err := loadMenuContentDocumentStateFromRoot(t.Context(), db, *root, false)
	require.NoError(t, err)
	require.Equal(t, revision, current.Revision.String())
	require.JSONEq(t, string(items), string(root.Items))
}
