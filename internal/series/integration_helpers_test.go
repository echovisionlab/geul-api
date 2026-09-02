//go:build integration

package series

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/auth"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/stretchr/testify/require"
)

func newServiceIntegrationDB(t *testing.T) *gorm.DB { return testutil.NewIntegrationDB(t) }
func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	return testutil.NewConcurrentPostIntegrationDB(t)
}
func integrationTestUUID() string { return testutil.IntegrationUUID() }
func integrationMemberID(identityID string) string {
	return testutil.PostIntegrationMemberID(identityID)
}

type recordingSeriesFileDeleter struct {
	deletedIDs []string
}

type seriesTestMenuTargets struct{}

func (seriesTestMenuTargets) UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error {
	return nil
}
func (seriesTestMenuTargets) Remove(context.Context, *gorm.DB, string, string, string) error {
	return nil
}

type seriesTestPostAccess struct{}

func (seriesTestPostAccess) PostSourceTitleSQL() string {
	return "COALESCE((SELECT translation.title FROM post_translation AS translation WHERE translation.entity_id = post.id AND translation.locale = post.source_locale LIMIT 1), '')"
}
func (seriesTestPostAccess) RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	return nil
}

type seriesTestMemberSummaries struct{}

func (seriesTestMemberSummaries) LoadSeriesManagers(context.Context, []string) (map[string]*managev1.SeriesManager, error) {
	return map[string]*managev1.SeriesManager{}, nil
}

type seriesTestRenderConfig struct{}

func (seriesTestRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newOGRefresherForTest(db *gorm.DB, cdnDomain string) *og.Refresher {
	planner := og.NewPlanner(db, cdnDomain, seriesTestRenderConfig{}, seriesadapter.NewProjection())
	return og.NewRefresher(planner, og.NewResolver(seriesadapter.NewRequests()))
}

type ogAssetSnapshotForTest struct {
	AssetID   string `json:"asset_id"`
	URL       string `json:"url"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogEntitySnapshotForTest struct {
	Locale        *string                 `json:"locale,omitempty"`
	FeaturedImage *ogAssetSnapshotForTest `json:"featured_image,omitempty"`
}

func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func requireFileRowExists(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func seriesIntegrationMenuTargets() menudomain.TargetLifecycle {
	return menudomain.NewTargetLifecycle(nil)
}

func seedSeriesActor(t *testing.T, db *gorm.DB, identityID, nickname string) {
	t.Helper()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, nickname)
}

func seedSeriesPost(t *testing.T, db *gorm.DB, authorMemberID string) string {
	t.Helper()
	postID := testutil.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String())
	require.NoError(t, db.Exec(`INSERT INTO post_author (post_id, member_id, created_at) VALUES (?::uuid, ?::uuid, NOW())`, postID, authorMemberID).Error)
	return postID
}

func grantSeriesPostAuthorRelation(t *testing.T, service *SeriesService, postID, identityID string) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	policy, err := policyv1.Post.TouchPolicy(postID)
	require.NoError(t, err)
	author, err := policyv1.Post.TouchAuthor(postID, actor)
	require.NoError(t, err)
	_, err = service.spiceDB.ApplyRelationships(t.Context(), policy, author)
	require.NoError(t, err)
}

func seedImageBindingUploadedFileFixture(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	fileID := integrationTestUUID()
	assetID := integrationTestUUID()
	digest := sha256.Sum256([]byte(key))
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "series-image-" + fileID, MimeType: "image/webp",
		FileSize: fileSize, Extension: "webp", SHA256: digest[:],
	}).Error)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "image", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize, SHA256: digest[:],
		Disposition: "inline",
		Status:      model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID
}
