//go:build integration

package collaborationadapter

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Collaboration integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Collaboration integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func seedServiceIntegrationContentDocument(t *testing.T, db *gorm.DB, profile string) string {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)",
		documentID,
		profile,
	).Error)
	return documentID
}

func seedInternalPostBaseRow(t *testing.T, db *gorm.DB) string {
	t.Helper()
	postID := uuid.NewString()
	documentID := seedServiceIntegrationContentDocument(t, db, "post")
	require.NoError(t, db.Exec(`
		INSERT INTO post (id, content_document_id, status, comments_enabled, created_at, updated_at)
		VALUES (?, ?, 'POST_STATUS_DRAFT', TRUE, NOW(), NOW())
	`, postID, documentID).Error)
	return postID
}
