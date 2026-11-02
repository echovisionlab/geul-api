package contentblock

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
)

func TestLoadRevisionReadsTheDocumentRevision(t *testing.T) {
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE content_document (id TEXT PRIMARY KEY, revision TEXT NOT NULL)`).Error)
	documentID, revision := uuid.New(), uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, revision) VALUES (?, ?)`, documentID.String(), revision.String()).Error)

	loaded, err := LoadRevision(t.Context(), db, documentID)
	require.NoError(t, err)
	require.Equal(t, revision, loaded)

	_, err = LoadRevision(t.Context(), db, uuid.New())
	require.ErrorIs(t, err, ErrDocumentNotFound)
}
