//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/crypto"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const publicMediaSecret = "media-secret"

func TestWorkServiceOwnsDraftMediaAndSharePasswordAuthorizationIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Work Draft Media Admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Work Draft Media Admin")
	adminCtx := publicLegalAdminCtx(adminMemberID, adminID)
	suffix := uuid.NewString()
	fileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{ID: fileID, FileName: "draft-audio", MimeType: "audio/flac", FileSize: 1234, Extension: "flac", SHA256: make([]byte, 32), CreatedAt: time.Now().UTC()}).Error)
	seedReadyMediaGenerationFixture(t, db, fileID)
	seedReadyPublicDerivativeFixture(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM, "waveform", "application/json")

	isPresent := true
	blockID := uuid.NewString()
	workSvc := newPublicWorkManageService(t, db, adminID)
	work, err := workSvc.CreateWork(adminCtx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Public Draft Work Media " + suffix, Slug: stringPtr("public-draft-work-media-" + suffix),
		Type: managev1.WorkType_WORK_TYPE_PORTFOLIO, Year: 2026, Month: 5,
		IsPresent: &isPresent, Document: publicWorkFileDocument("en", blockID, fileID),
	}))
	require.NoError(t, err)
	workID := work.Msg.Id

	fileSvc := filepublic.NewFileService(db, publicIntegrationSpiceDB, "https://cdn.example.com", "https://media.example.com", publicMediaSecret, 30*time.Minute)
	publicWork := workpublic.NewWorkService(
		db, publicIntegrationSpiceDB, fileSvc,
		newPublicWorkRuntimeForTest(db, "https://cdn.example.com"),
		workadapter.NewMemberSummaries(db, "https://cdn.example.com"),
		referencecatalogadapter.PublicMapPlaces{},
		workpublic.WithWorkContentBlockStore(newPublicWorkContentBlockStore(t)),
	)
	_, err = publicWork.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: workID}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	adminResp, err := publicWork.Get(adminCtx, connect.NewRequest(&openv1.GetWorkRequest{Slug: workID}))
	require.NoError(t, err)
	require.Len(t, adminResp.Msg.BlockMedia, 1)
	require.Equal(t, blockID, adminResp.Msg.BlockMedia[0].GetSelector().GetBlockId())
	require.Equal(t, fileID, adminResp.Msg.BlockMedia[0].GetAttachment().GetActiveFileId())
	require.NotEmpty(t, adminResp.Msg.BlockMedia[0].GetDelivery().GetPlayback().GetUrl())

	password := "work-share-secret"
	passwordHash, err := crypto.NewPasswordHasher(nil).Hash(password)
	require.NoError(t, err)
	token := uuid.NewString()
	expiresAt := time.Now().UTC().Add(time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{
		ID:           uuid.NewString(),
		Token:        token,
		EntityType:   managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK.String(),
		EntityID:     workID,
		PasswordHash: &passwordHash,
		ExpiresAt:    &expiresAt,
		CreatedAt:    time.Now().UTC(),
	}).Error)
	_, err = publicWork.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: workID, ShareToken: &token}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	wrongPassword := "wrong"
	_, err = publicWork.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: workID, ShareToken: &token, SharePassword: &wrongPassword}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	sharedResp, err := publicWork.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: workID, ShareToken: &token, SharePassword: &password}))
	require.NoError(t, err)
	require.Len(t, sharedResp.Msg.BlockMedia, 1)
	require.NotEmpty(t, sharedResp.Msg.BlockMedia[0].GetDelivery().GetPlayback().GetUrl())
}

func publicWorkFileDocument(locale, blockID, fileID string) *contentv1.RichTextDocument {
	name := "draft-audio.flac"
	alt := "Draft Work audio"
	caption := "Draft Work audio caption"
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		SourceLocale:            locale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
				Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}}, Name: &name,
			}}}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value:   &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{Alt: &alt, Caption: &caption}}},
		}}}},
	}
}

func seedReadyPublicDerivativeFixture(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	assetKind string,
	mimeType string,
) string {
	t.Helper()
	assetID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: assetKind, ObjectKey: objectKey,
		Extension: extension, MimeType: mimeType, FileSize: &fileSize,
		SHA256: make([]byte, 32), Disposition: "inline",
		Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO file_derivative (file_id, type, asset_id) VALUES (?, ?, ?)`,
		fileID, derivativeType.String(), assetID,
	).Error)
	return assetID
}

func seedReadyMediaGenerationFixture(t *testing.T, db *gorm.DB, fileID string) string {
	t.Helper()
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	now := time.Now().UTC()
	objectCount := int32(2)
	totalSize := int64(2048)
	require.NoError(t, db.Create(&model.MediaGeneration{
		ID: generationID, FileID: fileID, Kind: "hls", ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8", ManifestSHA256: make([]byte, 32),
		ObjectCount: &objectCount, TotalSize: &totalSize, Status: model.MediaGenerationStatusReady,
		ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO file_derivative (file_id, type, media_generation_id) VALUES (?, ?, ?)`,
		fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), generationID,
	).Error)
	return generationID
}
