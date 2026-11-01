package filemedia

import (
	"context"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Service) meshOptimizationService() *MeshOptimizationService {
	var publisher MeshOptimizationJobPublisher
	if meshPublisher, ok := s.publisher.(MeshOptimizationJobPublisher); ok {
		publisher = meshPublisher
	} else if meshPublisher, ok := s.asyncPublisher.(MeshOptimizationJobPublisher); ok {
		publisher = meshPublisher
	}
	return NewMeshOptimizationService(s.db, publisher, s, s.spiceDB)
}

// ListMeshOptimizationCandidatesQuery lists the optimized candidates of a
// 3D model File attached to an entity.
type ListMeshOptimizationCandidatesQuery struct {
	SourceFileID string
	EntityType   managev1.TranscodeEntityType
	EntityID     string
	Profile      managev1.MeshOptimizationProfile
}

func (q ListMeshOptimizationCandidatesQuery) input() MeshOptimizationListInput {
	return MeshOptimizationListInput{
		SourceFileID: strings.TrimSpace(q.SourceFileID),
		EntityType:   q.EntityType,
		EntityID:     strings.TrimSpace(q.EntityID),
		Profile:      q.Profile,
	}
}

func (s *Service) ListMeshOptimizationCandidates(ctx context.Context, query ListMeshOptimizationCandidatesQuery) ([]*managev1.MeshOptimizationCandidate, error) {
	candidates, err := s.meshOptimizationService().ListCandidates(ctx, query.input())
	if err != nil {
		return nil, err
	}
	projected := make([]*managev1.MeshOptimizationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		response, err := s.meshOptimizationCandidateResponse(candidate)
		if err != nil {
			return nil, err
		}
		projected = append(projected, response)
	}
	return projected, nil
}

// GenerateMeshOptimizationCandidateCommand generates, or reuses, an optimized
// candidate of a 3D model File.
type GenerateMeshOptimizationCandidateCommand struct {
	SourceFileID       string
	EntityType         managev1.TranscodeEntityType
	EntityID           string
	TargetRatioPercent int32
	Method             managev1.MeshOptimizationCompressionMethod
	Profile            managev1.MeshOptimizationProfile
}

func (s *Service) GenerateMeshOptimizationCandidate(ctx context.Context, command GenerateMeshOptimizationCandidateCommand) (*managev1.GenerateMeshOptimizationCandidateResponse, error) {
	result, err := s.meshOptimizationService().GenerateCandidate(ctx, MeshOptimizationGenerateInput{
		SourceFileID:       strings.TrimSpace(command.SourceFileID),
		EntityType:         command.EntityType,
		EntityID:           strings.TrimSpace(command.EntityID),
		TargetRatioPercent: command.TargetRatioPercent,
		Method:             meshOptimizationRequestMethod(command.Method),
		Profile:            command.Profile,
	})
	if err != nil {
		return nil, err
	}
	projected, err := s.meshOptimizationCandidateResponse(result.Candidate)
	if err != nil {
		return nil, err
	}
	return &managev1.GenerateMeshOptimizationCandidateResponse{
		Candidate: projected,
		CacheHit:  result.CacheHit,
		Enqueued:  result.Enqueued,
	}, nil
}

// UseMeshOptimizationCandidateCommand selects an optimized candidate.
type UseMeshOptimizationCandidateCommand struct {
	CandidateID string
}

func (s *Service) UseMeshOptimizationCandidate(ctx context.Context, command UseMeshOptimizationCandidateCommand) (*managev1.MeshOptimizationCandidate, error) {
	candidate, err := s.meshOptimizationService().UseCandidate(ctx, strings.TrimSpace(command.CandidateID))
	if err != nil {
		return nil, err
	}
	return s.meshOptimizationCandidateResponse(*candidate)
}

// ClearMeshOptimizationCandidatesCommand discards one candidate when
// CandidateID is set, or every candidate the list query matches.
type ClearMeshOptimizationCandidatesCommand struct {
	CandidateID string
	Candidates  ListMeshOptimizationCandidatesQuery
}

func (s *Service) ClearMeshOptimizationCandidates(ctx context.Context, command ClearMeshOptimizationCandidatesCommand) error {
	meshOptimization := s.meshOptimizationService()
	if candidateID := strings.TrimSpace(command.CandidateID); candidateID != "" {
		return meshOptimization.DiscardCandidate(ctx, candidateID)
	}
	candidates, err := meshOptimization.ListCandidates(ctx, command.Candidates.input())
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := meshOptimization.DiscardCandidate(ctx, candidate.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) meshOptimizationCandidateResponse(
	candidate model.MeshOptimizationCandidate,
) (*managev1.MeshOptimizationCandidate, error) {
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
	if meshOptimizationCandidateHasUsableOutput(candidate) {
		outputFileID := strings.TrimSpace(*candidate.OutputFileID)
		mimeType := "model/gltf-binary"
		fileName := outputFileID + ".glb"
		response.FileId = &outputFileID
		response.FileName = &fileName
		response.FileSize = cloneInt64(candidate.OptimizedFileSize)
		delivery, err := s.fileURLsResponseFromStoredFile(
			outputFileID,
			"glb",
			mimeType,
			int64Value(candidate.OptimizedFileSize),
			&fileName,
		)
		if err != nil {
			return nil, err
		}
		delivery.Delivery.FileName = &fileName
		response.Delivery = delivery.Delivery
	}
	return response, nil
}

func meshOptimizationCandidateHasUsableOutput(candidate model.MeshOptimizationCandidate) bool {
	return candidate.Status == model.MeshOptimizationCandidateStatusReady &&
		candidate.OutputFileID != nil &&
		strings.TrimSpace(*candidate.OutputFileID) != ""
}

func meshOptimizationRequestMethod(
	method managev1.MeshOptimizationCompressionMethod,
) string {
	switch method {
	case managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_UNSPECIFIED,
		managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO:
		return model.MeshOptimizationMethodDraco
	default:
		return ""
	}
}

func meshOptimizationResponseMethod(method string) managev1.MeshOptimizationCompressionMethod {
	if strings.EqualFold(strings.TrimSpace(method), model.MeshOptimizationMethodDraco) {
		return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO
	}
	return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_UNSPECIFIED
}

func meshOptimizationCandidateStatus(status string) managev1.MeshOptimizationCandidateStatus {
	switch status {
	case model.MeshOptimizationCandidateStatusPending:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_PENDING
	case model.MeshOptimizationCandidateStatusProcessing:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_PROCESSING
	case model.MeshOptimizationCandidateStatusReady:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_READY
	case model.MeshOptimizationCandidateStatusFailed:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_FAILED
	case model.MeshOptimizationCandidateStatusCancelled:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_CANCELLED
	default:
		return managev1.MeshOptimizationCandidateStatus_MESH_OPTIMIZATION_CANDIDATE_STATUS_UNSPECIFIED
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	next := *value
	return &next
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
