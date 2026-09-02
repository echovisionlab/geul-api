//go:build integration

package filemedia

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestMeshOptimizationServiceRequiresSourceFileLinkedToEntity(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	linkedSourceID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/linked.glb", "model/gltf-binary")
	unlinkedSourceID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/unlinked.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, linkedSourceID)
	unlinkedCandidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: unlinkedSourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusPending,
	})
	publisher := &recordingMeshOptimizationPublisher{}
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	attachInternalResourcePolicy(t, stack.SpiceDBClient, pageID)
	service := NewMeshOptimizationService(db, publisher, nil, stack.SpiceDBClient)
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())

	result, err := service.GenerateCandidate(ctx, MeshOptimizationGenerateInput{
		SourceFileID:       linkedSourceID,
		EntityType:         managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:           pageID,
		TargetRatioPercent: 50,
		Method:             model.MeshOptimizationMethodDraco,
	})
	require.NoError(t, err)
	require.True(t, result.Enqueued)
	require.Equal(t, linkedSourceID, result.Candidate.SourceFileID)
	require.Equal(t, int32(50), result.Candidate.TargetRatioPercent)
	require.Equal(t, model.MeshOptimizationMethodDraco, result.Candidate.Method)
	require.Equal(t, model.MeshOptimizationPipelineVersionDracoWebpV1, result.Candidate.PipelineVersion)
	require.NotEmpty(t, result.Candidate.OutputObjectID)
	require.Nil(t, result.Candidate.OutputFileID)
	require.NotEqual(t, linkedSourceID, result.Candidate.OutputObjectID)
	require.Len(t, publisher.jobs, 1)
	require.Equal(t, result.Candidate.OutputObjectID, publisher.jobs[0].GetOutput().GetFileId())
	require.Equal(t, "media/"+result.Candidate.OutputObjectID+".glb", publisher.jobs[0].GetOutput().GetObjectKey())

	legacyCandidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID:    linkedSourceID,
		EntityType:      managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:        pageID,
		Status:          model.MeshOptimizationCandidateStatusReady,
		PipelineVersion: model.MeshOptimizationPipelineVersionDracoV1,
	})
	candidates, err := service.ListCandidates(ctx, MeshOptimizationListInput{
		SourceFileID: linkedSourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
	})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, result.Candidate.ID, candidates[0].ID)
	require.NotEqual(t, legacyCandidate.ID, candidates[0].ID)

	_, err = service.GenerateCandidate(ctx, MeshOptimizationGenerateInput{
		SourceFileID:       unlinkedSourceID,
		EntityType:         managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:           pageID,
		TargetRatioPercent: 60,
		Method:             model.MeshOptimizationMethodDraco,
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = service.ListCandidates(ctx, MeshOptimizationListInput{
		SourceFileID: unlinkedSourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = service.UseCandidate(ctx, unlinkedCandidate.ID)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	err = service.DiscardCandidate(ctx, unlinkedCandidate.ID)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestMeshOptimizationProfilesUseIndependentPipelinesAndCaches(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/source.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	publisher := &recordingMeshOptimizationPublisher{}
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	attachInternalResourcePolicy(t, stack.SpiceDBClient, pageID)
	service := NewMeshOptimizationService(db, publisher, nil, stack.SpiceDBClient)
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())

	baseInput := MeshOptimizationGenerateInput{
		SourceFileID:       sourceID,
		EntityType:         managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:           pageID,
		TargetRatioPercent: 50,
		Method:             model.MeshOptimizationMethodDraco,
	}
	legacy, err := service.GenerateCandidate(ctx, baseInput)
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationPipelineVersionDracoWebpV1, legacy.Candidate.PipelineVersion)
	require.Equal(
		t,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1,
		publisher.jobs[0].GetOptions().GetProfile(),
	)

	particleInput := baseInput
	particleInput.Profile = managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1
	particle, err := service.GenerateCandidate(ctx, particleInput)
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationPipelineVersionParticleMeshV1, particle.Candidate.PipelineVersion)
	require.NotEqual(t, legacy.Candidate.ID, particle.Candidate.ID)
	require.NotEqual(t, legacy.Candidate.CacheKey, particle.Candidate.CacheKey)
	require.Len(t, publisher.jobs, 2)
	require.Equal(
		t,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
		publisher.jobs[1].GetOptions().GetProfile(),
	)

	particleCacheHit, err := service.GenerateCandidate(ctx, particleInput)
	require.NoError(t, err)
	require.True(t, particleCacheHit.CacheHit)
	require.Equal(t, particle.Candidate.ID, particleCacheHit.Candidate.ID)
	require.Len(t, publisher.jobs, 2)

	legacyCandidates, err := service.ListCandidates(ctx, MeshOptimizationListInput{
		SourceFileID: sourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
	})
	require.NoError(t, err)
	require.Len(t, legacyCandidates, 1)
	require.Equal(t, legacy.Candidate.ID, legacyCandidates[0].ID)

	particleCandidates, err := service.ListCandidates(ctx, MeshOptimizationListInput{
		SourceFileID: sourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Profile:      managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
	})
	require.NoError(t, err)
	require.Len(t, particleCandidates, 1)
	require.Equal(t, particle.Candidate.ID, particleCandidates[0].ID)
}

func TestFileServiceBatchClearMeshOptimizationCandidatesScopesByProfile(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/source.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	legacy := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID:    sourceID,
		EntityType:      managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:        pageID,
		Status:          model.MeshOptimizationCandidateStatusFailed,
		PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1,
	})
	particle := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID:    sourceID,
		EntityType:      managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:        pageID,
		Status:          model.MeshOptimizationCandidateStatusFailed,
		PipelineVersion: model.MeshOptimizationPipelineVersionParticleMeshV1,
	})
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	attachInternalResourcePolicy(t, stack.SpiceDBClient, pageID)
	service := &FileService{db: db, spiceDB: stack.SpiceDBClient}

	response, err := service.ClearMeshOptimizationCandidates(
		auth.WithUser(context.Background(), admin.AuthUserInfo()),
		connect.NewRequest(&managev1.ClearMeshOptimizationCandidatesRequest{
			SourceFileId: sourceID,
			EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:     pageID,
			Profile:      managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
		}),
	)
	require.NoError(t, err)
	require.True(t, response.Msg.GetSuccess())

	var preserved model.MeshOptimizationCandidate
	require.NoError(t, db.First(&preserved, "id = ?", legacy.ID).Error)
	require.Equal(t, model.MeshOptimizationPipelineVersionDracoWebpV1, preserved.PipelineVersion)

	var discarded model.MeshOptimizationCandidate
	require.ErrorIs(t, db.First(&discarded, "id = ?", particle.ID).Error, gorm.ErrRecordNotFound)
}

func TestMeshOptimizationUseCandidateSelectsDerivativeWithoutChangingDocumentAttachment(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/source.glb", "model/gltf-binary")
	outputID := seedUnitMeshOptimizationFile(t, db, "media/output.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		OutputFileID: &outputID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusReady,
	})
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	attachInternalResourcePolicy(t, stack.SpiceDBClient, pageID)
	service := NewMeshOptimizationService(db, nil, nil, stack.SpiceDBClient)

	selected, err := service.UseCandidate(auth.WithUser(context.Background(), admin.AuthUserInfo()), candidate.ID)
	require.NoError(t, err)
	require.NotNil(t, selected.SelectedAt)

	var sourceLinks int64
	require.NoError(t, db.Table("content_block_attachment AS cbf").
		Joins("JOIN content_block AS cb ON cb.id = cbf.block_id").
		Joins("JOIN page ON page.content_document_id = cb.document_id").
		Where("page.id = ? AND cbf.file_id = ?", pageID, sourceID).
		Count(&sourceLinks).Error)
	require.Equal(t, int64(1), sourceLinks)

	var outputLinks int64
	require.NoError(t, db.Table("content_block_attachment").
		Where("file_id = ?", outputID).
		Count(&outputLinks).Error)
	require.Zero(t, outputLinks)
}

func TestMeshOptimizationHandleCompleteRequiresOptimizedSize(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "page/"+pageID+"/files/source.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	outputFileID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusProcessing,
		JobID:        "mesh-job-missing-size",
	})
	service := NewMeshOptimizationService(db, nil, nil, nil)

	_, err := service.HandleComplete(context.Background(), &managev1.MeshOptimizationCompleteEvent{
		JobId:         "mesh-job-missing-size",
		CorrelationId: candidate.ID,
		Output: &managev1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{FileId: outputFileID},
		},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requireUnitFileAbsent(t, db, outputFileID)
}

func TestMeshOptimizationUnprotectsCandidateBeforeDeletingOutput(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	outputFileID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: uuid.NewString(),
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     uuid.NewString(),
		Status:       model.MeshOptimizationCandidateStatusReady,
	})
	deleter := &candidateOrderCheckingFileDeleter{db: db, candidateID: candidate.ID}
	service := NewMeshOptimizationService(db, nil, deleter, nil)

	require.NoError(t, service.deleteCandidateAndOutput(t.Context(), candidate))
	require.Equal(t, outputFileID, deleter.deletedFileID)
	requireUnitMeshOptimizationCandidateAbsent(t, db, candidate.ID)
}

func TestMeshOptimizationKeepsCancelledCandidateWhenOutputDeletionFails(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	outputFileID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: uuid.NewString(),
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     uuid.NewString(),
		Status:       model.MeshOptimizationCandidateStatusReady,
	})
	deleteErr := fmt.Errorf("delete failed")
	deleter := &candidateOrderCheckingFileDeleter{db: db, candidateID: candidate.ID, deleteErr: deleteErr}
	service := NewMeshOptimizationService(db, nil, deleter, nil)

	require.ErrorIs(t, service.deleteCandidateAndOutput(t.Context(), candidate), deleteErr)
	var persisted model.MeshOptimizationCandidate
	require.NoError(t, db.First(&persisted, "id = ?", candidate.ID).Error)
	require.Equal(t, model.MeshOptimizationCandidateStatusCancelled, persisted.Status)
	require.Equal(t, outputFileID, deleter.deletedFileID)
}

type candidateOrderCheckingFileDeleter struct {
	db            *gorm.DB
	candidateID   string
	deletedFileID string
	deleteErr     error
}

func (d *candidateOrderCheckingFileDeleter) DeleteFileByID(ctx context.Context, fileID string) error {
	var candidate model.MeshOptimizationCandidate
	if err := d.db.WithContext(ctx).Where("id = ?", d.candidateID).Take(&candidate).Error; err != nil {
		return err
	}
	if candidate.Status != model.MeshOptimizationCandidateStatusCancelled {
		return fmt.Errorf("candidate output is still protected by status %q", candidate.Status)
	}
	d.deletedFileID = fileID
	return d.deleteErr
}

func TestMeshOptimizationHandleCompleteValidatesAllocatedResultAndIsIdempotent(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "ignored/source.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	outputFileID := uuid.NewString()
	jobID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusProcessing,
		JobID:        jobID,
	})
	service := NewMeshOptimizationService(db, nil, nil, nil)
	optimizedSize := int64(512)
	digest := sha256.Sum256([]byte("optimized mesh"))
	event := &managev1.MeshOptimizationCompleteEvent{
		JobId:         jobID,
		CorrelationId: candidate.ID,
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   pageID,
			FileId:     sourceID,
		},
		Output: &managev1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{
				FileId:   outputFileID,
				FileSize: optimizedSize,
				Sha256:   digest[:],
			},
			OptimizedSizeBytes: &optimizedSize,
		},
	}

	completed, err := service.HandleComplete(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationCandidateStatusReady, completed.Status)

	var output model.File
	require.NoError(t, db.First(&output, "id = ?", outputFileID).Error)
	require.Equal(t, optimizedSize, output.FileSize)
	require.Equal(t, outputFileID, output.FileName)
	require.Equal(t, "glb", output.Extension)
	require.Equal(t, digest[:], output.SHA256)

	completed, err = service.HandleComplete(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationCandidateStatusReady, completed.Status)

	wrongID := uuid.NewString()
	event.Output.Written.FileId = wrongID
	_, err = service.HandleComplete(context.Background(), event)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	event.Output.Written.FileId = outputFileID

	wrongDigest := sha256.Sum256([]byte("different bytes"))
	event.Output.Written.Sha256 = wrongDigest[:]
	_, err = service.HandleComplete(context.Background(), event)
	require.Error(t, err)
	require.NoError(t, db.First(&output, "id = ?", outputFileID).Error)
	require.Equal(t, digest[:], output.SHA256)
}

func TestMeshOptimizationHandleCompleteRejectsUnspecifiedProfileForParticleMesh(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "ignored/source.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	outputFileID := uuid.NewString()
	jobID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID:    sourceID,
		OutputFileID:    &outputFileID,
		EntityType:      managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:        pageID,
		Status:          model.MeshOptimizationCandidateStatusProcessing,
		JobID:           jobID,
		PipelineVersion: model.MeshOptimizationPipelineVersionParticleMeshV1,
	})
	service := NewMeshOptimizationService(db, nil, nil, nil)
	optimizedSize := int64(512)
	digest := sha256.Sum256([]byte("particle mesh"))
	event := &managev1.MeshOptimizationCompleteEvent{
		JobId:         jobID,
		CorrelationId: candidate.ID,
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   pageID,
			FileId:     sourceID,
		},
		Output: &managev1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{
				FileId:   outputFileID,
				FileSize: optimizedSize,
				Sha256:   digest[:],
			},
			OptimizedSizeBytes: &optimizedSize,
		},
	}

	_, err := service.HandleComplete(context.Background(), event)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "output.profile")
	requireUnitFileAbsent(t, db, outputFileID)

	event.Output.Profile = managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1
	completed, err := service.HandleComplete(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationCandidateStatusReady, completed.Status)
}

func TestMeshOptimizationEventsRequireExactIdentity(t *testing.T) {
	db := newMeshOptimizationUnitDB(t)
	pageID := uuid.NewString()
	sourceID := seedUnitMeshOptimizationFile(t, db, "ignored/source.glb", "model/gltf-binary")
	seedUnitPageFileLink(t, db, pageID, sourceID)
	jobID := uuid.NewString()
	candidate := seedUnitMeshOptimizationCandidate(t, db, unitMeshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusPending,
		JobID:        jobID,
	})
	service := NewMeshOptimizationService(db, nil, nil, nil)

	_, err := service.HandleProgress(context.Background(), &managev1.MeshOptimizationProgressEvent{
		JobId:         jobID,
		CorrelationId: candidate.ID,
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   pageID,
			FileId:     uuid.NewString(),
		},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = service.HandleProgress(context.Background(), &managev1.MeshOptimizationProgressEvent{
		JobId:         jobID,
		CorrelationId: uuid.NewString(),
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   pageID,
			FileId:     sourceID,
		},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
