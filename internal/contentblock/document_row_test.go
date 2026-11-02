package contentblock

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCurrentRevisionReadsTheDocumentRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE content_document (id TEXT PRIMARY KEY, revision TEXT)`).Error)
	documentID, revision := uuid.New(), uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, revision) VALUES (?, ?)`, documentID, revision).Error)

	current, err := CurrentRevision(context.Background(), db, documentID)
	require.NoError(t, err)
	require.Equal(t, revision, current)
	_, err = CurrentRevision(context.Background(), db, uuid.New())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
