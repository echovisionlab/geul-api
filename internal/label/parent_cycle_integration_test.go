//go:build integration

package label

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestLabelParentTransitionRejectsSelfAndDescendantIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Label hierarchy admin")
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(adminID),
		MemberID:      auth.MemberID(integrationMemberID(adminID)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
		Onboarded:     true,
	})
	stack := testutil.SetupOryStack(t)
	contentBlocks := newCreativeContentIntegrationStore(t, stack.SpiceDBClient)
	syncLabelParentIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())

	rootID := createLabelParentIntegrationFixture(t, db, stack.SpiceDBClient, contentBlocks, ctx, adminID, "Root label", "root-label-"+integrationTestUUID())
	childID := createLabelParentIntegrationFixture(t, db, stack.SpiceDBClient, contentBlocks, ctx, adminID, "Child label", "child-label-"+integrationTestUUID())
	grandchildID := createLabelParentIntegrationFixture(t, db, stack.SpiceDBClient, contentBlocks, ctx, adminID, "Grandchild label", "grandchild-label-"+integrationTestUUID())
	require.NoError(t, db.Table("label").Where("id = ?", childID).Update("parent_label_id", rootID).Error)
	require.NoError(t, db.Table("label").Where("id = ?", grandchildID).Update("parent_label_id", childID).Error)

	for _, testCase := range []struct {
		name     string
		labelID  string
		parentID string
	}{
		{name: "self", labelID: rootID, parentID: rootID},
		{name: "descendant", labelID: rootID, parentID: grandchildID},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				return validateLabelParentTransition(ctx, tx, testCase.labelID, testCase.parentID)
			})
			require.Error(t, err)
		})
	}

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return validateLabelParentTransition(ctx, tx, grandchildID, rootID)
	}))

	service := NewInternalLabelService(
		db, noopArtistAsyncPublisher{}, stack.SpiceDBClient,
		Dependencies{Translation: labeladapter.NewTranslation(), Runtime: newLabelRuntimeForTest(db, "")},
		WithInternalLabelContentBlockStore(contentBlocks),
		WithInternalLabelCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	)
	var revision string
	require.NoError(t, db.Raw(`
		SELECT document.revision::text
		FROM label
		JOIN content_document AS document ON document.id = label.content_document_id
		WHERE label.id = ?::uuid
	`, rootID).Scan(&revision).Error)
	_, err := service.UpdateLabelDocumentMetadata(ctx, connect.NewRequest(&intrav1.UpdateLabelDocumentMetadataRequest{
		LabelId:              rootID,
		ExpectedRevision:     revision,
		ContributorMemberIds: []string{integrationMemberID(adminID)},
		Update: &intrav1.LabelDocumentMetadataUpdate{ParentLabelId: &intrav1.NullableStringMutation{
			Operation: &intrav1.NullableStringMutation_Set{Set: grandchildID},
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	var storedParentID *string
	require.NoError(t, db.Table("label").Select("parent_label_id").Where("id = ?", rootID).Row().Scan(&storedParentID))
	require.Nil(t, storedParentID)
}

func syncLabelParentIntegrationGlobalRole(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	identityID string,
	role policyv1.RoleID,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func createLabelParentIntegrationFixture(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	contentBlocks *contentblock.Store,
	ctx context.Context,
	adminID, name, slug string,
) string {
	t.Helper()
	service := NewLabelService(
		db,
		spiceDB,
		&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")},
		&recordingArtistFileDeleter{},
		noopArtistAsyncPublisher{},
		Dependencies{
			Translation: labeladapter.NewTranslation(),
			Members:     labeladapter.NewMemberProjection(db, ""),
			Runtime:     newLabelRuntimeForTest(db, ""),
		},
		WithLabelContentBlockStore(contentBlocks),
	)
	response, err := service.CreateLabel(ctx, connect.NewRequest(&managev1.CreateLabelRequest{
		Name:     name,
		Slug:     &slug,
		Document: creativeContentIntegrationDocument("en", name+" description"),
	}))
	require.NoError(t, err)
	return response.Msg.Id
}
