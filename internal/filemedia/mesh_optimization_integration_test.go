//go:build integration

package filemedia

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestMeshOptimizationServiceRequiresSourceFileLinkedToEntityIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	pageID := seedHardCutPageFixture(t, db)
	attachInternalResourcePolicy(t, spiceDB, pageID)
	linkedSourceID := seedMeshOptimizationSourceFile(t, db)
	unlinkedSourceID := seedMeshOptimizationSourceFile(t, db)
	seedMeshOptimizationPageFileLink(t, db, pageID, linkedSourceID)
	unlinkedCandidate := seedMeshOptimizationCandidate(t, db, meshOptimizationCandidateFixture{
		SourceFileID: unlinkedSourceID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusPending,
	})
	service := NewMeshOptimizationService(db, &hardCutRecordingMeshOptimizationPublisher{}, nil, spiceDB)

	result, err := service.GenerateCandidate(ctx, MeshOptimizationGenerateInput{
		SourceFileID:       linkedSourceID,
		EntityType:         managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:           pageID,
		TargetRatioPercent: 50,
		Method:             model.MeshOptimizationMethodDraco,
	})
	require.NoError(t, err)
	require.True(t, result.Enqueued)
	require.NotEmpty(t, result.Candidate.OutputObjectID)
	require.Nil(t, result.Candidate.OutputFileID)
	requireFileDeleteLifecycleFileAbsent(t, db, result.Candidate.OutputObjectID)

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

func TestMeshOptimizationUseCandidateSelectsDerivativeWithoutChangingDocumentAttachmentIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	pageID := seedHardCutPageFixture(t, db)
	attachInternalResourcePolicy(t, spiceDB, pageID)
	sourceID := seedMeshOptimizationSourceFile(t, db)
	outputID := seedMeshOptimizationSourceFile(t, db)
	seedMeshOptimizationPageFileLink(t, db, pageID, sourceID)
	candidate := seedMeshOptimizationCandidate(t, db, meshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		OutputFileID: &outputID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusReady,
	})
	service := NewMeshOptimizationService(db, nil, nil, spiceDB)

	selected, err := service.UseCandidate(ctx, candidate.ID)
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

func TestMeshOptimizationHandleCompleteRequiresOptimizedSizeIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	pageID := seedHardCutPageFixture(t, db)
	sourceID := seedMeshOptimizationSourceFile(t, db)
	seedMeshOptimizationPageFileLink(t, db, pageID, sourceID)
	outputFileID := uuid.NewString()
	candidate := seedMeshOptimizationCandidate(t, db, meshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusProcessing,
		JobID:        "mesh-job-missing-size",
	})
	requireFileDeleteLifecycleFileAbsent(t, db, outputFileID)
	service := NewMeshOptimizationService(db, nil, nil, durableAudienceSpiceDB(t))

	_, err := service.HandleComplete(context.Background(), &managev1.MeshOptimizationCompleteEvent{
		JobId:         "mesh-job-missing-size",
		CorrelationId: candidate.ID,
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   pageID,
			FileId:     sourceID,
		},
		Output: &managev1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{FileId: outputFileID},
		},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requireFileDeleteLifecycleFileAbsent(t, db, outputFileID)
}

func TestMeshOptimizationHandleCompletePersistsExtensionlessBasenameIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	pageID := seedHardCutPageFixture(t, db)
	sourceID := seedMeshOptimizationSourceFile(t, db)
	seedMeshOptimizationPageFileLink(t, db, pageID, sourceID)
	outputFileID := uuid.NewString()
	jobID := uuid.NewString()
	candidate := seedMeshOptimizationCandidate(t, db, meshOptimizationCandidateFixture{
		SourceFileID: sourceID,
		OutputFileID: &outputFileID,
		EntityType:   managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityID:     pageID,
		Status:       model.MeshOptimizationCandidateStatusProcessing,
		JobID:        jobID,
	})
	optimizedSize := int64(512)
	digest := sha256.Sum256([]byte("optimized mesh"))
	service := NewMeshOptimizationService(db, nil, nil, durableAudienceSpiceDB(t))

	completed, err := service.HandleComplete(context.Background(), &managev1.MeshOptimizationCompleteEvent{
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
	})
	require.NoError(t, err)
	require.Equal(t, model.MeshOptimizationCandidateStatusReady, completed.Status)

	var output model.File
	require.NoError(t, db.First(&output, "id = ?", outputFileID).Error)
	require.Equal(t, outputFileID, output.FileName)
	require.Equal(t, "glb", output.Extension)
}

type meshOptimizationCandidateFixture struct {
	SourceFileID string
	OutputFileID *string
	EntityType   managev1.TranscodeEntityType
	EntityID     string
	Status       string
	JobID        string
}

func seedMeshOptimizationSourceFile(t *testing.T, db *gorm.DB) string {
	t.Helper()

	fileID := uuid.NewString()
	seedFileDeleteLifecycleFile(t, db, fileID, "source", "model/gltf-binary", "glb")
	return fileID
}

func seedMeshOptimizationPageFileLink(t *testing.T, db *gorm.DB, pageID string, fileID string) {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'page', ?)
	`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		UPDATE page SET content_document_id = ? WHERE id = ?
	`, documentID, pageID).Error)
	blockID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) VALUES (?, ?, NULL, 'body', 0, 'file', '{}'::jsonb)
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'file', 'active', ?)
	`, blockID, fileID).Error)
}

func seedMeshOptimizationCandidate(
	t *testing.T,
	db *gorm.DB,
	fixture meshOptimizationCandidateFixture,
) model.MeshOptimizationCandidate {
	t.Helper()

	now := time.Now().UTC()
	candidateID := uuid.NewString()
	targetRatioPercent := int32(50)
	method := model.MeshOptimizationMethodDraco
	pipelineVersion := model.MeshOptimizationPipelineVersionDracoWebpV1
	cacheKey := BuildMeshOptimizationCacheKey(fixture.SourceFileID, targetRatioPercent, method, pipelineVersion)
	status := fixture.Status
	if status == "" {
		status = model.MeshOptimizationCandidateStatusPending
	}
	jobID := fixture.JobID
	if jobID == "" {
		jobID = uuid.NewString()
	}
	outputFileID := fixture.OutputFileID
	outputObjectID := uuid.NewString()
	if outputFileID != nil {
		outputObjectID = *outputFileID
		var count int64
		require.NoError(t, db.Table("file").Where("id = ?", *outputFileID).Count(&count).Error)
		if count == 0 {
			outputFileID = nil
		}
	}
	if outputFileID == nil && fixture.OutputFileID == nil {
		outputObjectID = uuid.NewString()
	}
	entityType := fixture.EntityType.String()
	entityID := fixture.EntityID
	candidate := model.MeshOptimizationCandidate{
		ID:                 candidateID,
		SourceFileID:       fixture.SourceFileID,
		OutputObjectID:     outputObjectID,
		OutputFileID:       outputFileID,
		EntityType:         &entityType,
		EntityID:           &entityID,
		TargetRatioPercent: targetRatioPercent,
		Method:             method,
		PipelineVersion:    pipelineVersion,
		CacheKey:           cacheKey,
		Status:             status,
		JobID:              &jobID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	require.NoError(t, db.Create(&candidate).Error)
	return candidate
}
