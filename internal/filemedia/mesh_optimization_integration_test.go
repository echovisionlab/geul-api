//go:build integration

package filemedia

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

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
