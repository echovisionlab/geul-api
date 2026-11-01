package filemedia

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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

	generations, err := domain.ClassifyGenerationDeleteRefs(fileID, []domain.FileGenerationDeleteRef{
		{ID: currentID, FileID: fileID, Kind: "hls", ObjectPrefix: currentPrefix},
		{ID: retiredID, FileID: fileID, Kind: "hls", ObjectPrefix: retiredPrefix},
	})
	require.NoError(t, err)
	require.Len(t, generations, 2)
	require.Equal(t, []string{currentID, retiredID}, []string{
		generations[0].GenerationID, generations[1].GenerationID,
	})
	require.Equal(t, []string{currentPrefix, retiredPrefix}, []string{
		generations[0].ObjectPrefix, generations[1].ObjectPrefix,
	})
}

func TestFileDeletionEnqueueFailureRollsBackDomainIntent(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	fileID := seedUnitMeshOptimizationFile(t, db, "media/source.glb", "model/gltf-binary")
	publishErr := errors.New("publisher unavailable")
	publisher := &capturingAsyncPublisher{confirmedErr: publishErr}
	service := withTestStorage(&Service{db: db, asyncPublisher: publisher})

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
	fileService := withTestStorage(&Service{db: db, s3Bucket: "media-bucket", asyncPublisher: publisher})
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
