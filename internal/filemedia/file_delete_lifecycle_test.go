//go:build integration

package filemedia

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

func TestClassifyFileMediaGenerationDeleteRefsIncludesRetiredGenerations(t *testing.T) {
	t.Parallel()
	fileID := uuid.NewString()
	currentID := uuid.NewString()
	retiredID := uuid.NewString()
	currentPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, currentID)
	require.NoError(t, err)
	retiredPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, retiredID)
	require.NoError(t, err)

	generations, err := classifyFileMediaGenerationDeleteRefs(fileID, []fileMediaGenerationDeleteRef{
		{ID: currentID, FileID: fileID, Kind: "hls", ObjectPrefix: currentPrefix},
		{ID: retiredID, FileID: fileID, Kind: "hls", ObjectPrefix: retiredPrefix},
	})
	require.NoError(t, err)
	require.Len(t, generations, 2)
	require.Equal(t, []string{currentID, retiredID}, []string{
		generations[0].GetGenerationId(), generations[1].GetGenerationId(),
	})
	require.Equal(t, []string{currentPrefix, retiredPrefix}, []string{
		generations[0].GetObjectPrefix(), generations[1].GetObjectPrefix(),
	})
}

func TestRequestUnboundFileDerivativeAssetsDeletionPreservesBoundAssets(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE file_derivative (
			id TEXT PRIMARY KEY, file_id TEXT NOT NULL, type TEXT NOT NULL, asset_id TEXT
		)`,
		`CREATE TABLE public_asset (
			id TEXT PRIMARY KEY, source_file_id TEXT, kind TEXT NOT NULL,
			object_key TEXT NOT NULL, extension TEXT NOT NULL, mime_type TEXT NOT NULL,
			file_size INTEGER, sha256 BLOB, disposition TEXT NOT NULL,
			download_filename TEXT, status TEXT NOT NULL, ready_at DATETIME,
			delete_requested_at DATETIME, deleted_at DATETIME, failed_at DATETIME,
			failure_reason TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE public_asset_binding (asset_id TEXT NOT NULL)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	fileID := uuid.NewString()
	now := time.Now().UTC()
	newAsset := func(kind string) model.PublicAsset {
		assetID := uuid.NewString()
		fileSize := int64(32)
		return model.PublicAsset{
			ID: assetID, SourceFileID: &fileID, Kind: kind,
			ObjectKey: "asset/" + assetID + ".webp", Extension: "webp",
			MimeType: "image/webp", FileSize: &fileSize, SHA256: make([]byte, 32),
			Disposition: "inline", Status: model.PublicAssetStatusReady,
			ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
		}
	}
	unbound := newAsset("thumbnail")
	bound := newAsset("thumbnail")
	require.NoError(t, db.Create(&unbound).Error)
	require.NoError(t, db.Create(&bound).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO file_derivative (id, file_id, type, asset_id) VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		uuid.NewString(), fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String(), unbound.ID,
		uuid.NewString(), fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(), bound.ID,
	).Error)
	require.NoError(t, db.Exec(`INSERT INTO public_asset_binding (asset_id) VALUES (?)`, bound.ID).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return requestUnboundFileDerivativeAssetsDeletion(t.Context(), tx, fileID)
	}))

	require.NoError(t, db.First(&unbound, "id = ?", unbound.ID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, unbound.Status)
	require.NotNil(t, unbound.DeleteRequestedAt)
	require.NoError(t, db.First(&bound, "id = ?", bound.ID).Error)
	require.Equal(t, model.PublicAssetStatusReady, bound.Status)
	require.Nil(t, bound.DeleteRequestedAt)
}

func TestRequestUnboundSourceFileAssetsDeletionPreservesBoundProjection(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE public_asset (
			id TEXT PRIMARY KEY, source_file_id TEXT, kind TEXT NOT NULL,
			object_key TEXT NOT NULL, extension TEXT NOT NULL, mime_type TEXT NOT NULL,
			file_size INTEGER, sha256 BLOB, disposition TEXT NOT NULL,
			download_filename TEXT, status TEXT NOT NULL, ready_at DATETIME,
			delete_requested_at DATETIME, deleted_at DATETIME, failed_at DATETIME,
			failure_reason TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE public_asset_binding (asset_id TEXT NOT NULL)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	unboundFileID := uuid.NewString()
	boundFileID := uuid.NewString()
	now := time.Now().UTC()
	newProjection := func(fileID string) model.PublicAsset {
		assetID := uuid.NewString()
		fileSize := int64(32)
		return model.PublicAsset{
			ID: assetID, SourceFileID: &fileID, Kind: "image",
			ObjectKey: "asset/" + assetID + ".webp", Extension: "webp",
			MimeType: "image/webp", FileSize: &fileSize, SHA256: make([]byte, 32),
			Disposition: "inline", Status: model.PublicAssetStatusReady,
			ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
		}
	}
	unbound := newProjection(unboundFileID)
	bound := newProjection(boundFileID)
	require.NoError(t, db.Create(&unbound).Error)
	require.NoError(t, db.Create(&bound).Error)
	require.NoError(t, db.Exec(`INSERT INTO public_asset_binding (asset_id) VALUES (?)`, bound.ID).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return requestUnboundSourceFileAssetsDeletion(t.Context(), tx, unboundFileID)
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return requestUnboundSourceFileAssetsDeletion(t.Context(), tx, boundFileID)
	}))

	require.NoError(t, db.First(&unbound, "id = ?", unbound.ID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, unbound.Status)
	require.NotNil(t, unbound.DeleteRequestedAt)
	require.NoError(t, db.First(&bound, "id = ?", bound.ID).Error)
	require.Equal(t, model.PublicAssetStatusReady, bound.Status)
	require.Nil(t, bound.DeleteRequestedAt)
}

func TestFileDeletionEnqueueFailureRollsBackDomainIntent(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	fileID := seedUnitMeshOptimizationFile(t, db, "media/source.glb", "model/gltf-binary")
	publishErr := errors.New("publisher unavailable")
	publisher := &capturingAsyncPublisher{confirmedErr: publishErr}
	service := &FileService{db: db, asyncPublisher: publisher}

	err := service.deleteFileRecordByID(t.Context(), fileID)
	require.ErrorIs(t, err, publishErr)
	var file model.File
	require.NoError(t, db.First(&file, "id = ?", fileID).Error)
	require.Nil(t, file.DeleteRequestedAt)
	require.Equal(t, 1, publisher.confirmCalls)
	require.Equal(t, 0, publisher.rawPublishCalls)
	require.Len(t, publisher.messages, 1)
	require.True(t, publisher.messages[0].mandatory)
	require.Equal(t, fileID, publisher.messages[0].messageID)
}

func TestFileDeletionRetiresLateMeshCompletionWithoutAttachingOutput(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	publisher := &capturingAsyncPublisher{}
	fileService := &FileService{db: db, s3Bucket: "media-bucket", asyncPublisher: publisher}
	pageID := uuid.NewString()
	sourceFileID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/source.glb", "model/gltf-binary")
	outputFileID := uuid.NewString()
	jobID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: sourceFileID,
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusProcessing,
		JobID:        jobID,
	})

	require.NoError(t, fileService.DeleteFileByID(t.Context(), sourceFileID))
	requireUnitFileDeletionPending(t, db, sourceFileID)
	var cancelled model.MeshOptimizationCandidate
	require.NoError(t, db.First(&cancelled, "id = ?", candidate.ID).Error)
	require.Equal(t, model.MeshOptimizationCandidateStatusCancelled, cancelled.Status)

	initialEvents := decodePublishedRoutedMessages(t, publisher.messages, "", eventpkg.QueueFileDelete, func() *managev1.FileDeleteEvent {
		return &managev1.FileDeleteEvent{}
	})
	require.Len(t, initialEvents, 1)
	require.Equal(t, sourceFileID, initialEvents[0].GetFileId())
	require.Empty(t, initialEvents[0].GetAssets())
	require.Empty(t, initialEvents[0].GetGenerations())

	optimizedSize := int64(512)
	digest := sha256.Sum256([]byte("late optimized mesh"))
	meshService := NewMeshOptimizationService(db, nil, fileService, nil)
	completed, err := meshService.HandleComplete(t.Context(), &managev1.MeshOptimizationCompleteEvent{
		JobId:         jobID,
		CorrelationId: candidate.ID,
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   pageID,
			FileId:     sourceFileID,
		},
		Output: &managev1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{
				FileId:   outputFileID,
				FileSize: optimizedSize,
				Sha256:   digest[:],
			},
			OptimizedSizeBytes: &optimizedSize,
		},
	})
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationCandidateStatusCancelled, completed.Status)
	requireUnitFileDeletionPending(t, db, outputFileID)
	requireUnitMeshOptimizationCandidateAbsent(t, db, candidate.ID)

	allEvents := decodePublishedRoutedMessages(t, publisher.messages, "", eventpkg.QueueFileDelete, func() *managev1.FileDeleteEvent {
		return &managev1.FileDeleteEvent{}
	})
	require.Len(t, allEvents, 2)
	require.Equal(t, outputFileID, allEvents[1].GetFileId())
}

func requireUnitFileDeletionPending(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var file model.File
	require.NoError(t, db.First(&file, "id = ?", fileID).Error)
	require.NotNil(t, file.DeleteRequestedAt)
}

func requireUnitMeshOptimizationCandidateAbsent(t *testing.T, db *gorm.DB, candidateID string) {
	t.Helper()

	var candidate model.MeshOptimizationCandidate
	err := db.First(&candidate, "id = ?", candidateID).Error
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
