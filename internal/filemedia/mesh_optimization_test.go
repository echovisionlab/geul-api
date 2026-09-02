package filemedia

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestValidateMeshOptimizationRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		targetRatioPercent int32
		method             string
		wantErr            bool
	}{
		{name: "minimum draco", targetRatioPercent: 1, method: "DRACO"},
		{name: "off ten-step draco", targetRatioPercent: 15, method: "DRACO"},
		{name: "maximum lowercase draco", targetRatioPercent: 100, method: "draco"},
		{name: "default method", targetRatioPercent: 50, method: ""},
		{name: "below minimum", targetRatioPercent: 0, method: "DRACO", wantErr: true},
		{name: "above maximum", targetRatioPercent: 110, method: "DRACO", wantErr: true},
		{name: "unsupported method", targetRatioPercent: 50, method: "MESHOPT", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateMeshOptimizationRequest(tt.targetRatioPercent, tt.method)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestMeshOptimizationCacheKey(t *testing.T) {
	t.Parallel()

	legacyKey := BuildMeshOptimizationCacheKey(" source-file-id ", 10, "draco", "")
	require.Equal(
		t,
		"source-file-id:10:DRACO:"+model.MeshOptimizationPipelineVersionDracoWebpV1,
		legacyKey,
	)
	particleKey := BuildMeshOptimizationCacheKey(
		" source-file-id ",
		10,
		"draco",
		model.MeshOptimizationPipelineVersionParticleMeshV1,
	)
	require.NotEqual(t, legacyKey, particleKey)
}

func TestMeshOptimizationPipelineVersionForProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		profile managev1.MeshOptimizationProfile
		want    string
		wantErr bool
	}{
		{
			name:    "unspecified uses legacy",
			profile: managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED,
			want:    model.MeshOptimizationPipelineVersionDracoWebpV1,
		},
		{
			name:    "explicit legacy",
			profile: managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1,
			want:    model.MeshOptimizationPipelineVersionDracoWebpV1,
		},
		{
			name:    "particle mesh",
			profile: managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
			want:    model.MeshOptimizationPipelineVersionParticleMeshV1,
		},
		{name: "unknown", profile: managev1.MeshOptimizationProfile(99), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := meshOptimizationPipelineVersionForProfile(tt.profile)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMeshOptimizationJobFromCandidateUsesGeneratedContract(t *testing.T) {
	t.Parallel()

	jobID := "33333333-3333-4333-8333-333333333333"
	sourceFileID := "11111111-1111-4111-8111-111111111111"
	outputFileID := "22222222-2222-4222-8222-222222222222"
	outputKey := "media/22222222-2222-4222-8222-222222222222.glb"
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK.String()
	entityID := "work-id"
	slotID := "page-block:immersive-scene:mesh"
	attemptID := "attempt-id"

	job, err := meshOptimizationJobFromCandidate(
		model.MeshOptimizationCandidate{
			ID:                 "candidate-id",
			SourceFileID:       sourceFileID,
			OutputObjectID:     outputFileID,
			OutputFileID:       &outputFileID,
			EntityType:         &entityType,
			EntityID:           &entityID,
			TargetRatioPercent: 1,
			Method:             model.MeshOptimizationMethodDraco,
			PipelineVersion:    model.MeshOptimizationPipelineVersionParticleMeshV1,
			JobID:              &jobID,
		},
		model.File{
			ID:              sourceFileID,
			Extension:       "glb",
			MimeType:        "model/gltf-binary",
			IngestSlotID:    &slotID,
			IngestAttemptID: &attemptID,
		},
	)

	require.NoError(t, err)
	require.Equal(t, jobID, job.GetJobId())
	require.Equal(t, "candidate-id", job.GetCorrelationId())
	require.Equal(t, outputKey, job.GetOutput().GetObjectKey())
	require.Greater(t, job.GetTimestampMs(), int64(0))
	require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK, job.GetIdentity().GetEntityType())
	require.Equal(t, entityID, job.GetIdentity().GetEntityId())
	require.Equal(t, sourceFileID, job.GetIdentity().GetFileId())
	require.Equal(t, "media/"+sourceFileID+".glb", job.GetIdentity().GetSource().GetObjectKey())
	require.Equal(t, outputFileID, job.GetOutput().GetFileId())
	require.Equal(t, slotID, job.GetIdentity().GetSlotId())
	require.Equal(t, attemptID, job.GetIdentity().GetAttemptId())
	require.Equal(
		t,
		managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO,
		job.GetOptions().GetCompressionMethod(),
	)
	require.Equal(
		t,
		int32(1),
		job.GetOptions().GetTargetRatioPercent(),
	)
	require.Equal(
		t,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
		job.GetOptions().GetProfile(),
	)
}

func TestMeshOptimizationCacheAndProtectionStatuses(t *testing.T) {
	t.Parallel()

	require.True(t, meshOptimizationCandidateCacheUsable(model.MeshOptimizationCandidateStatusPending))
	require.True(t, meshOptimizationCandidateCacheUsable(model.MeshOptimizationCandidateStatusProcessing))
	require.True(t, meshOptimizationCandidateCacheUsable(model.MeshOptimizationCandidateStatusReady))
	require.False(t, meshOptimizationCandidateCacheUsable(model.MeshOptimizationCandidateStatusFailed))
	require.False(t, meshOptimizationCandidateCacheUsable(model.MeshOptimizationCandidateStatusCancelled))

	require.ElementsMatch(t, []string{
		model.MeshOptimizationCandidateStatusPending,
		model.MeshOptimizationCandidateStatusProcessing,
		model.MeshOptimizationCandidateStatusReady,
	}, MeshOptimizationOutputProtectionStatuses())
}

func TestMeshOptimizationEventsRequireJobID(t *testing.T) {
	t.Parallel()

	service := &MeshOptimizationService{}

	_, err := service.HandleProgress(t.Context(), &managev1.MeshOptimizationProgressEvent{
		CorrelationId: "candidate-id",
	})
	require.Error(t, err)

	_, err = service.HandleComplete(t.Context(), &managev1.MeshOptimizationCompleteEvent{
		CorrelationId: "candidate-id",
		Output: &managev1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{FileId: "output-file"},
		},
	})
	require.Error(t, err)

	_, err = service.HandleFailed(t.Context(), &managev1.MeshOptimizationFailEvent{
		CorrelationId: "candidate-id",
		Error:         "failed",
	})
	require.Error(t, err)
}

func TestFileServiceMeshOptimizationServiceUsesAsyncPublisherFallback(t *testing.T) {
	t.Parallel()

	asyncPublisher := &recordingFileMeshAsyncPublisher{}
	service := &FileService{
		db:             newMeshOptimizationUnitDB(t),
		asyncPublisher: asyncPublisher,
		publisher:      fileMeshTranscoderOnlyPublisher{},
		spiceDB:        nil,
	}

	meshOptimization := service.meshOptimizationService()

	require.Same(t, asyncPublisher, meshOptimization.publisher)
}

func TestFileServiceMeshOptimizationCandidateResponseHidesFailedOutputTarget(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	errorMessage := "mesh optimization publisher is not configured"
	service := &FileService{cdnDomain: "cdn.example.com"}

	response, err := service.meshOptimizationCandidateResponse(model.MeshOptimizationCandidate{
		ID:                 "candidate-id",
		SourceFileID:       "source-file-id",
		TargetRatioPercent: 40,
		Method:             model.MeshOptimizationMethodDraco,
		Status:             model.MeshOptimizationCandidateStatusFailed,
		ErrorMessage:       &errorMessage,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	require.NoError(t, err)

	require.Nil(t, response.FileId)
	require.Nil(t, response.FileName)
	require.Nil(t, response.Delivery)
	require.Nil(t, response.FileSize)
	require.Equal(t, errorMessage, response.GetErrorMessage())
	require.Equal(
		t,
		managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_FAILED,
		response.GetStatus(),
	)
	require.Equal(
		t,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1,
		response.GetProfile(),
	)
}

func TestFileServiceMeshOptimizationCandidateResponseIncludesReadyOutputFile(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	outputFileID := "22222222-2222-4222-8222-222222222222"
	outputFileSize := int64(512)
	service := &FileService{cdnDomain: "cdn.example.com", mediaSecret: "secret"}

	response, err := service.meshOptimizationCandidateResponse(model.MeshOptimizationCandidate{
		ID:                 "candidate-id",
		SourceFileID:       "source-file-id",
		OutputObjectID:     outputFileID,
		OutputFileID:       &outputFileID,
		TargetRatioPercent: 10,
		Method:             model.MeshOptimizationMethodDraco,
		PipelineVersion:    model.MeshOptimizationPipelineVersionParticleMeshV1,
		Status:             model.MeshOptimizationCandidateStatusReady,
		OptimizedFileSize:  &outputFileSize,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	require.NoError(t, err)

	require.Equal(t, outputFileID, response.GetFileId())
	require.Equal(t, outputFileID+".glb", response.GetFileName())
	require.Equal(t, outputFileSize, response.GetFileSize())
	require.Equal(t, outputFileID, response.GetDelivery().GetFileId())
	require.Equal(t, "glb", response.GetDelivery().GetExtension())
	require.Equal(t, "model/gltf-binary", response.GetDelivery().GetMimeType())
	require.NotEmpty(t, response.GetDelivery().GetInline().GetUrl())
	require.NotEmpty(t, response.GetDelivery().GetDownload().GetUrl())
	require.Equal(
		t,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
		response.GetProfile(),
	)
}

type fileMeshTranscoderOnlyPublisher struct{}

func (fileMeshTranscoderOnlyPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}

func (fileMeshTranscoderOnlyPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}

func (fileMeshTranscoderOnlyPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}

type recordingFileMeshAsyncPublisher struct {
	jobs []*managev1.MeshOptimizationJob
}

func (p *recordingFileMeshAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (p *recordingFileMeshAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p *recordingFileMeshAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	_ string,
	_ string,
	_ proto.Message,
) error {
	if executor == nil {
		return errNilTransactionalExecutor
	}
	return nil
}

func (p *recordingFileMeshAsyncPublisher) PublishMeshOptimizationJob(
	_ context.Context,
	job *managev1.MeshOptimizationJob,
) error {
	p.jobs = append(p.jobs, job)
	return nil
}
