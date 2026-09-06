//go:build integration

package artist

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedInternalArtist(t *testing.T, db *gorm.DB, contentBlocks *contentblock.Store) string {
	t.Helper()

	artistID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		document, err := contentBlocks.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: creativeContentProfile, SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO artist (id, status, source_locale, content_document_id, created_at, updated_at)
			VALUES (?, 'ARTIST_STATUS_DRAFT', 'en', ?, ?, ?)
		`, artistID, document.Document.ID, now, now).Error
	}))
	return artistID
}

func TestInternalArtistParentRelationRejectsCyclesAndCanBeClearedIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	requireInternalResourceAdmin(t, stack.SpiceDBClient, identityID)
	service := NewAuditedInternalArtistService(
		db, noopArtistAsyncPublisher{}, stack.SpiceDBClient, apitelemetry.NewDurableWriter(db),
		Dependencies{Translation: artistIntegrationTranslation{}, Runtime: newArtistIntegrationRuntime(db, "")},
		WithInternalArtistContentBlockStore(testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)),
		WithInternalArtistCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	)
	contentBlocks := service.contentBlocks
	rootID := seedInternalArtist(t, db, contentBlocks)
	childID := seedInternalArtist(t, db, contentBlocks)
	grandchildID := seedInternalArtist(t, db, contentBlocks)
	for _, artistID := range []string{rootID, childID, grandchildID} {
		now := time.Now().UTC()
		title := "Internal Artist " + artistID
		require.NoError(t, db.Exec("UPDATE artist SET source_locale = 'en' WHERE id = ?::uuid", artistID).Error)
		require.NoError(t, saveArtistSourceLocaleDocumentState(
			context.Background(), db, artistID, "en",
			translationLocaleDocumentSaveInput{
				SourceLocale: "en", Title: &title, Now: now,
			},
		))
		attachArtistContentDocument(t, db, contentBlocks, artistID, "en", title)
		attachInternalResourcePolicy(t, stack.SpiceDBClient, artistID)
	}
	ctx := auth.WithUser(artistDocumentContributorContext(t), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
	contributorID := uuid.NewString()
	testutil.InsertAuthorizedDocumentContributor(t, db, stack.SpiceDBClient, contributorID)

	setParent := func(artistID, parentID string) error {
		var revision string
		if err := db.Raw(`
			SELECT document.revision::text
			FROM artist
			JOIN content_document AS document ON document.id = artist.content_document_id
			WHERE artist.id = ?::uuid
		`, artistID).Scan(&revision).Error; err != nil {
			return err
		}
		parentMutation := &intrav1.NullableStringMutation{
			Operation: &intrav1.NullableStringMutation_Set{Set: parentID},
		}
		if parentID == "" {
			parentMutation.Operation = &intrav1.NullableStringMutation_Clear{Clear: true}
		}
		_, err := service.UpdateArtistDocumentMetadata(ctx, connect.NewRequest(&intrav1.UpdateArtistDocumentMetadataRequest{
			ArtistId:             artistID,
			Locale:               "en",
			ExpectedRevision:     revision,
			ContributorMemberIds: []string{contributorID},
			Update: &intrav1.ArtistDocumentMetadataUpdate{
				ParentArtistId: parentMutation,
			},
		}))
		return err
	}

	require.NoError(t, setParent(childID, rootID))
	require.NoError(t, setParent(grandchildID, childID))
	require.Error(t, setParent(rootID, grandchildID))

	var rootParentID *string
	require.NoError(t, db.Table("artist").Select("parent_artist_id").Where("id = ?", rootID).Scan(&rootParentID).Error)
	require.Nil(t, rootParentID)

	require.NoError(t, setParent(childID, ""))
	var childParentID *string
	require.NoError(t, db.Table("artist").Select("parent_artist_id").Where("id = ?", childID).Scan(&childParentID).Error)
	require.Nil(t, childParentID)
}

func requireInternalResourceAdmin(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
}

func attachInternalResourcePolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID string) {
	t.Helper()
	mutation, err := policyv1.Artist.TouchPolicy(resourceID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}
