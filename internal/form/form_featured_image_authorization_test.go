//go:build integration

package form

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

func TestFormFeaturedImageMutationsRequireSiteAdmin(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	formID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO content_document (id, profile) VALUES (?::uuid, 'compact')",
		documentID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO form (id, status, is_public, source_locale, content_document_id) VALUES (?::uuid, 'FORM_STATUS_DRAFT', FALSE, 'en', ?::uuid)",
		formID, documentID,
	).Error)
	policy, err := policyv1.Form.TouchPolicy(formID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePolicy, deleteErr := policyv1.Form.DeletePolicy(formID)
		require.NoError(t, deleteErr)
		_, cleanupErr := spiceDB.ApplyRelationships(context.Background(), deletePolicy)
		require.NoError(t, cleanupErr)
	})
	testutil.GrantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
	service := &FormService{
		db: db, spiceDB: spiceDB,
		authorization: newFormPermissionChecker(spiceDB, db),
	}

	_, err = service.SetFormFeaturedImage(ctx, connect.NewRequest(&managev1.SetFormFeaturedImageRequest{FormId: formID, FileId: uuid.NewString()}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = service.DeleteFormFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteFormFeaturedImageRequest{FormId: formID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
