package filemedia

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func meshCandidateMessage(value *MeshCandidateView) *managev1.MeshOptimizationCandidate {
	if value == nil {
		return nil
	}
	candidate := value.Record
	response := &managev1.MeshOptimizationCandidate{
		Id:                 candidate.ID,
		SourceFileId:       candidate.SourceFileID,
		Method:             meshOptimizationResponseMethod(candidate.Method),
		TargetRatioPercent: candidate.TargetRatioPercent,
		Profile:            meshOptimizationProfileForPipelineVersion(candidate.PipelineVersion),
		Status:             meshOptimizationCandidateStatus(candidate.Status),
		CreatedAt:          timestamppb.New(candidate.CreatedAt),
		UpdatedAt:          timestamppb.New(candidate.UpdatedAt),
	}
	if candidate.EntityType != nil {
		if value, ok := managev1.TranscodeEntityType_value[*candidate.EntityType]; ok {
			entityType := managev1.TranscodeEntityType(value)
			response.EntityType = &entityType
		}
	}
	response.EntityId = candidate.EntityID
	response.OriginalFileSize = cloneInt64(candidate.OriginalFileSize)
	response.OptimizedFileSize = cloneInt64(candidate.OptimizedFileSize)
	response.ProcessingTimeMs = cloneInt64(candidate.ProcessingTimeMs)
	response.OriginalVertexCount = cloneInt64(candidate.OriginalVertexes)
	response.OptimizedVertexCount = cloneInt64(candidate.OptimizedVertexes)
	response.OriginalTriangleCount = cloneInt64(candidate.OriginalTriangles)
	response.OptimizedTriangleCount = cloneInt64(candidate.OptimizedTriangles)
	response.ErrorMessage = candidate.ErrorMessage
	response.PublicAssetId = candidate.PublicAssetID
	if candidate.SelectedAt != nil {
		response.SelectedAt = timestamppb.New(*candidate.SelectedAt)
	}
	if value.Delivery != nil {
		response.FileId = &value.Delivery.FileID
		response.FileName = value.Delivery.FileName
		response.FileSize = cloneInt64(candidate.OptimizedFileSize)
		response.Delivery = DeliveryMessage(value.Delivery)
	}
	return response
}

func meshCandidatesMessage(values []*MeshCandidateView) []*managev1.MeshOptimizationCandidate {
	if values == nil {
		return nil
	}
	result := make([]*managev1.MeshOptimizationCandidate, len(values))
	for i, value := range values {
		result[i] = meshCandidateMessage(value)
	}
	return result
}

func meshGenerationMessage(value *MeshCandidateGeneration) *managev1.GenerateMeshOptimizationCandidateResponse {
	if value == nil {
		return nil
	}
	return &managev1.GenerateMeshOptimizationCandidateResponse{Candidate: meshCandidateMessage(value.Candidate), CacheHit: value.CacheHit, Enqueued: value.Enqueued}
}

func meshOptimizationResponseMethod(method string) managev1.MeshOptimizationCompressionMethod {
	if strings.EqualFold(strings.TrimSpace(method), domain.MeshMethodDraco) {
		return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO
	}
	return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_UNSPECIFIED
}

func meshOptimizationCandidateStatus(status string) managev1.MeshOptimizationCandidateStatus {
	switch status {
	case domain.MeshStatusPending:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_PENDING
	case domain.MeshStatusProcessing:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_PROCESSING
	case domain.MeshStatusReady:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_READY
	case domain.MeshStatusFailed:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_FAILED
	case domain.MeshStatusCancelled:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_CANCELLED
	default:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_UNSPECIFIED
	}
}
