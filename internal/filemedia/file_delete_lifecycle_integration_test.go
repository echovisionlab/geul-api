//go:build integration

package filemedia

import (
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestFileServiceDeleteFilePersistsRequestAndPublishesDeleteEventIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	publisher := &hardCutAsyncPublisher{}
	service := &FileService{
		db:             db,
		s3Bucket:       "media-bucket",
		spiceDB:        stack.SpiceDBClient,
		asyncPublisher: publisher,
	}
	fileID := uuid.NewString()
	seedFileDeleteLifecycleFile(t, db, fileID, "poster.jpg", "image/jpeg", "jpg")
	seedFileDeleteLifecyclePolicy(t, stack.SpiceDBClient, fileID)
	asset := seedFileDeleteLifecycleAssetDerivative(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL, "thumbnail", "webp", "image/webp")
	generation := seedFileDeleteLifecycleGenerationDerivative(t, db, fileID)
	retiredGenerationID := uuid.NewString()
	retiredPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, retiredGenerationID)
	require.NoError(t, err)
	now := time.Now().UTC().Add(-time.Minute)
	retiredManifestSHA := sha256.Sum256([]byte(retiredGenerationID))
	retiredObjectCount := int32(2)
	retiredTotalSize := int64(1024)
	readyAt := now.Add(-time.Hour)
	retiredGeneration := model.MediaGeneration{
		ID:             retiredGenerationID,
		FileID:         fileID,
		Kind:           "hls",
		ObjectPrefix:   retiredPrefix,
		ManifestName:   "master.m3u8",
		ManifestSHA256: retiredManifestSHA[:],
		ObjectCount:    &retiredObjectCount,
		TotalSize:      &retiredTotalSize,
		Status:         model.MediaGenerationStatusRetired,
		ReadyAt:        &readyAt,
		RetiredAt:      &now,
		DeleteAfter:    func() *time.Time { value := now.Add(7 * time.Hour); return &value }(),
		CreatedAt:      now.Add(-time.Hour), UpdatedAt: now,
	}
	require.NoError(t, db.Create(&retiredGeneration).Error)

	resp, err := service.DeleteFile(
		auth.WithUser(t.Context(), admin.AuthUserInfo()),
		connect.NewRequest(&managev1.DeleteFileRequest{FileId: fileID}),
	)
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	requireFileDeleteLifecyclePending(t, db, fileID)

	events := decodeHardCutRoutedMessages(t, publisher.messages, "", eventpkg.QueueFileDelete, func() *managev1.FileDeleteEvent {
		return &managev1.FileDeleteEvent{}
	})
	require.Lenf(t, events, 1, "published messages: %+v", publisher.messages)
	require.Equal(t, fileID, events[0].GetFileId())
	expectedOriginalKey, err := mediaauth.MediaObjectKey(fileID, "jpg")
	require.NoError(t, err)
	require.Equal(t, expectedOriginalKey, events[0].GetOriginal().GetObjectKey())
	require.Equal(t, "jpg", events[0].GetOriginal().GetExtension())
	require.Equal(t, "image/jpeg", events[0].GetOriginal().GetMimeType())
	require.Empty(t, events[0].GetAssets(), "tracked assets use the public-asset deletion lifecycle")
	var retiringAsset model.PublicAsset
	require.NoError(t, db.First(&retiringAsset, "id = ?", asset.ID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, retiringAsset.Status)
	require.NotNil(t, retiringAsset.DeleteRequestedAt)
	require.NotNil(t, retiringAsset.SourceFileID)
	require.Equal(t, fileID, *retiringAsset.SourceFileID)
	require.Len(t, events[0].GetGenerations(), 2)
	require.ElementsMatch(t, []string{generation.ID, retiredGeneration.ID}, []string{
		events[0].GetGenerations()[0].GetGenerationId(),
		events[0].GetGenerations()[1].GetGenerationId(),
	})
	require.ElementsMatch(t, []string{generation.ObjectPrefix, retiredGeneration.ObjectPrefix}, []string{
		events[0].GetGenerations()[0].GetObjectPrefix(),
		events[0].GetGenerations()[1].GetObjectPrefix(),
	})
}

func TestFileServiceDeleteFileCleansUnselectedMeshOptimizationCandidatesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	publisher := &hardCutAsyncPublisher{}
	service := &FileService{
		db:             db,
		s3Bucket:       "media-bucket",
		spiceDB:        stack.SpiceDBClient,
		asyncPublisher: publisher,
	}
	pageID := seedHardCutPageFixture(t, db)
	sourceFileID := seedMeshOptimizationSourceFile(t, db)
	seedFileDeleteLifecyclePolicy(t, stack.SpiceDBClient, sourceFileID)
	outputFileID := uuid.NewString()
	seedFileDeleteLifecycleFile(t, db, outputFileID, "optimized.glb", "model/gltf-binary", "glb")
	seedFileDeleteLifecyclePolicy(t, stack.SpiceDBClient, outputFileID)
	candidate := seedMeshOptimizationCandidate(t, db, meshOptimizationCandidateFixture{
		SourceFileID: sourceFileID,
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusReady,
	})

	resp, err := service.DeleteFile(
		auth.WithUser(t.Context(), admin.AuthUserInfo()),
		connect.NewRequest(&managev1.DeleteFileRequest{FileId: sourceFileID}),
	)
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	requireFileDeleteLifecyclePending(t, db, sourceFileID)
	requireFileDeleteLifecyclePending(t, db, outputFileID)
	requireMeshOptimizationCandidateAbsent(t, db, candidate.ID)

	events := decodeHardCutRoutedMessages(t, publisher.messages, "", eventpkg.QueueFileDelete, func() *managev1.FileDeleteEvent {
		return &managev1.FileDeleteEvent{}
	})
	require.Len(t, events, 2)
	require.ElementsMatch(t, []string{sourceFileID, outputFileID}, []string{
		events[0].GetFileId(),
		events[1].GetFileId(),
	})
}

func seedFileDeleteLifecycleFile(t *testing.T, db *gorm.DB, fileID string, fileName string, mimeType string, extension string) {
	t.Helper()

	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  storedFileBasename(fileName, fileID, extension),
		MimeType:  mimeType,
		FileSize:  1024,
		Extension: extension,
		SHA256:    digest[:],
		CreatedAt: time.Now().UTC(),
	}).Error)
}

func seedFileDeleteLifecyclePolicy(t *testing.T, spiceDB *auth.SpiceDBClient, fileID string) {
	t.Helper()

	mutation, err := policyv1.File.TouchPolicy(fileID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func seedFileDeleteLifecycleAssetDerivative(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	kind string,
	extension string,
	mimeType string,
) model.PublicAsset {
	t.Helper()

	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(512)
	digest := sha256.Sum256([]byte(assetID))
	asset := model.PublicAsset{
		ID:           assetID,
		SourceFileID: &fileID,
		Kind:         kind,
		ObjectKey:    objectKey,
		Extension:    extension,
		MimeType:     mimeType,
		FileSize:     &fileSize,
		SHA256:       digest[:],
		Disposition:  "inline",
		Status:       model.PublicAssetStatusReady,
		ReadyAt:      &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	require.NoError(t, db.Create(&asset).Error)
	require.NoError(t, db.Create(&model.FileDerivative{
		ID:        uuid.NewString(),
		FileID:    fileID,
		Type:      derivativeType,
		AssetID:   &assetID,
		CreatedAt: now,
	}).Error)
	return asset
}

func seedFileDeleteLifecycleGenerationDerivative(t *testing.T, db *gorm.DB, fileID string) model.MediaGeneration {
	t.Helper()

	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	now := time.Now().UTC()
	manifestSHA := sha256.Sum256([]byte(generationID))
	objectCount := int32(2)
	totalSize := int64(1024)
	generation := model.MediaGeneration{
		ID:             generationID,
		FileID:         fileID,
		Kind:           "hls",
		ObjectPrefix:   objectPrefix,
		ManifestName:   "master.m3u8",
		ManifestSHA256: manifestSHA[:],
		ObjectCount:    &objectCount,
		TotalSize:      &totalSize,
		Status:         model.MediaGenerationStatusReady,
		ReadyAt:        &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(t, db.Create(&generation).Error)
	require.NoError(t, db.Create(&model.FileDerivative{
		ID:                uuid.NewString(),
		FileID:            fileID,
		Type:              managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
		MediaGenerationID: &generationID,
		CreatedAt:         now,
	}).Error)
	return generation
}

func requireFileDeleteLifecycleFileAbsent(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()

	var file model.File
	err := db.First(&file, "id = ?", fileID).Error
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func requireFileDeleteLifecyclePending(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var file model.File
	require.NoError(t, db.First(&file, "id = ?", fileID).Error)
	require.NotNil(t, file.DeleteRequestedAt)
}

func requireMeshOptimizationCandidateAbsent(t *testing.T, db *gorm.DB, candidateID string) {
	t.Helper()

	var candidate model.MeshOptimizationCandidate
	err := db.First(&candidate, "id = ?", candidateID).Error
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
