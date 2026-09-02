//go:build integration

package public

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var publicIntegrationSpiceDB *auth.SpiceDBClient

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start public Page integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close public Page integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	publicIntegrationSpiceDB = stack.SpiceDBClient
	return stack.DB
}

func seedPublicAdminMemberIdentityLink(
	t *testing.T,
	db *gorm.DB,
	identityID string,
	nickname string,
) string {
	t.Helper()
	memberID := uuid.NewString()
	var primaryEmail string
	require.NoError(t, db.Raw(
		"SELECT traits->>'email' FROM kratos.identities WHERE id = ?::uuid", identityID,
	).Scan(&primaryEmail).Error)
	require.NotEmpty(t, primaryEmail)
	require.NoError(t, db.Exec(
		"INSERT INTO account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING", identityID,
	).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true,
		PrimaryEmail: &primaryEmail, AvailableEmails: pq.StringArray{primaryEmail},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID,
	).Error)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = publicIntegrationSpiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	return memberID
}

func publicPrincipalContext(memberID, identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		MemberID: auth.MemberID(memberID), IdentityID: auth.IdentityID(identityID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func publicLegalAdminCtx(memberID, identityID string) context.Context {
	return publicPrincipalContext(memberID, identityID)
}

func seedCanonicalPublicFileFixture(
	t *testing.T,
	db *gorm.DB,
	fileName string,
	mimeType string,
	assetKind string,
) (string, string) {
	t.Helper()
	fileID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	fileName = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	now := time.Now().UTC()
	fileDigest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: fileDigest[:], CreatedAt: now,
	}).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	fileSize := int64(1024)
	assetDigest := sha256.Sum256([]byte(assetID))
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: assetKind, ObjectKey: objectKey,
		Extension: extension, MimeType: mimeType, FileSize: &fileSize, SHA256: assetDigest[:],
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID, assetID
}

func setPublicDownloadAudience(t *testing.T, db *gorm.DB, blockID, audience string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"UPDATE content_block_attachment SET download_audience = ? WHERE block_id = ? AND reference_path = 'file'", audience, blockID,
	).Error)
}

type publicPageRenderConfig struct{}

func (publicPageRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newPublicPageRuntimeForTest(db *gorm.DB, cdnDomain string) *pageadapter.Runtime {
	planner := og.NewPlanner(db, cdnDomain, publicPageRenderConfig{}, pageadapter.NewProjection())
	return pageadapter.NewRuntime(
		cdnDomain,
		og.NewRefresher(planner, og.NewResolver(pageadapter.NewRequests())),
	)
}

type publicReferenceAsyncPublisher struct{}

func (publicReferenceAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (publicReferenceAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

type publicReferenceIdentityManager struct{ identity *auth.Identity }

func (m *publicReferenceIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.identity, nil
}

func (m *publicReferenceIdentityManager) GetIdentityWithIncludeCredential(
	context.Context,
	string,
	string,
) (*auth.Identity, error) {
	return m.identity, nil
}

type publicPageFileReuseAuthorizer struct{}

func (publicPageFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

type publicPageFileGateway struct {
	db        *gorm.DB
	cdnDomain string
}

func newPublicReferenceManageFileService(db *gorm.DB) *publicPageFileGateway {
	return &publicPageFileGateway{db: db, cdnDomain: "https://cdn.example.com"}
}

func (*publicPageFileGateway) DeleteFileByID(context.Context, string) error { return nil }

func (g *publicPageFileGateway) ResolveAuthorizedPageFeaturedImage(
	_ context.Context,
	_ string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	if expectedFileID == "" {
		return nil, nil
	}
	return &commonv1.MediaDelivery{
		FileId: expectedFileID,
		Inline: &commonv1.ExpiringMediaRef{
			FileId:  expectedFileID,
			Url:     g.cdnDomain + "/media/test/" + expectedFileID,
			Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE,
		},
	}, nil
}

func (g *publicPageFileGateway) ResolvePublicDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	return g.resolvePublicDisplay(ctx, fileIDs)
}

func (g *publicPageFileGateway) resolvePublicDisplay(
	ctx context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	result := make(map[string]*commonv1.MediaDelivery, len(fileIDs))
	for _, fileID := range fileIDs {
		ref, err := mediaasset.ReadyPublicAssetRefForSourceFile(
			ctx, g.db, g.cdnDomain, fileID, "image",
		)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		delivery := &commonv1.MediaDelivery{FileId: fileID}
		if err == nil {
			delivery.Asset = ref
			delivery.Thumbnail = ref
		}
		result[fileID] = delivery
	}
	return result, nil
}

func (g *publicPageFileGateway) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	result := make([]*contentv1.ContentBlockMediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		copy := proto.Clone(item).(*contentv1.ContentBlockMediaItem)
		fileID := copy.GetAttachment().GetActiveFileId()
		deliveries, err := g.resolvePublicDisplay(ctx, []string{fileID})
		if err != nil {
			return nil, err
		}
		copy.Delivery = deliveries[fileID]
		copy.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE
		copy.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD
		result = append(result, copy)
	}
	return result, nil
}

func (g *publicPageFileGateway) HydrateAuthorizedPageBlockMediaWithDB(
	ctx context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return g.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func (g *publicPageFileGateway) ResolveReadyOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	for _, candidate := range []*string{localizedAssetID, sourceAssetID} {
		if candidate == nil || strings.TrimSpace(*candidate) == "" {
			continue
		}
		var asset model.PublicAsset
		if err := g.db.WithContext(ctx).Where("id = ? AND status = ?", *candidate, model.PublicAssetStatusReady).
			Take(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		path, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
		if err != nil {
			return nil, err
		}
		fileSize := int64(0)
		if asset.FileSize != nil {
			fileSize = *asset.FileSize
		}
		return &commonv1.AssetRef{
			AssetId: asset.ID, Url: strings.TrimRight(g.cdnDomain, "/") + "/" + strings.TrimLeft(path, "/"),
			Extension: asset.Extension, MimeType: asset.MimeType, FileSize: fileSize,
			Sha256:      append([]byte(nil), asset.SHA256...),
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		}, nil
	}
	return nil, nil
}

func readyPublicAssetURLForFileFixture(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	fileID string,
) string {
	t.Helper()
	ref, err := mediaasset.ReadyPublicAssetRefForSourceFile(
		t.Context(), db, cdnDomain, fileID, "image",
	)
	require.NoError(t, err)
	return ref.GetUrl()
}
