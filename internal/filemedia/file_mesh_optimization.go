package filemedia

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) meshOptimizationService() *MeshOptimizationService {
	var publisher MeshOptimizationJobPublisher
	if meshPublisher, ok := s.publisher.(MeshOptimizationJobPublisher); ok {
		publisher = meshPublisher
	} else if meshPublisher, ok := s.asyncPublisher.(MeshOptimizationJobPublisher); ok {
		publisher = meshPublisher
	}
	return NewMeshOptimizationService(s.db, publisher, s, s.spiceDB)
}

func (s *FileService) ListMeshOptimizationCandidates(
	ctx context.Context,
	req *connect.Request[managev1.ListMeshOptimizationCandidatesRequest],
) (*connect.Response[managev1.ListMeshOptimizationCandidatesResponse], error) {
	candidates, err := s.meshOptimizationService().ListCandidates(ctx, MeshOptimizationListInput{
		SourceFileID: strings.TrimSpace(req.Msg.SourceFileId),
		EntityType:   req.Msg.EntityType,
		EntityID:     strings.TrimSpace(req.Msg.EntityId),
		Profile:      req.Msg.Profile,
	})
	if err != nil {
		return nil, err
	}

	response := &managev1.ListMeshOptimizationCandidatesResponse{
		Candidates: make([]*managev1.MeshOptimizationCandidate, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		projected, err := s.meshOptimizationCandidateResponse(candidate)
		if err != nil {
			return nil, err
		}
		response.Candidates = append(response.Candidates, projected)
	}
	return connect.NewResponse(response), nil
}

func (s *FileService) GenerateMeshOptimizationCandidate(
	ctx context.Context,
	req *connect.Request[managev1.GenerateMeshOptimizationCandidateRequest],
) (*connect.Response[managev1.GenerateMeshOptimizationCandidateResponse], error) {
	result, err := s.meshOptimizationService().GenerateCandidate(ctx, MeshOptimizationGenerateInput{
		SourceFileID:       strings.TrimSpace(req.Msg.SourceFileId),
		EntityType:         req.Msg.EntityType,
		EntityID:           strings.TrimSpace(req.Msg.EntityId),
		TargetRatioPercent: req.Msg.TargetRatioPercent,
		Method:             meshOptimizationRequestMethod(req.Msg.Method),
		Profile:            req.Msg.Profile,
	})
	if err != nil {
		return nil, err
	}

	projected, err := s.meshOptimizationCandidateResponse(result.Candidate)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.GenerateMeshOptimizationCandidateResponse{
		Candidate: projected,
		CacheHit:  result.CacheHit,
		Enqueued:  result.Enqueued,
	}), nil
}

func (s *FileService) UseMeshOptimizationCandidate(
	ctx context.Context,
	req *connect.Request[managev1.UseMeshOptimizationCandidateRequest],
) (*connect.Response[managev1.UseMeshOptimizationCandidateResponse], error) {
	candidate, err := s.meshOptimizationService().UseCandidate(ctx, strings.TrimSpace(req.Msg.CandidateId))
	if err != nil {
		return nil, err
	}

	projected, err := s.meshOptimizationCandidateResponse(*candidate)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.UseMeshOptimizationCandidateResponse{
		Candidate: projected,
	}), nil
}

func (s *FileService) ClearMeshOptimizationCandidates(
	ctx context.Context,
	req *connect.Request[managev1.ClearMeshOptimizationCandidatesRequest],
) (*connect.Response[managev1.ClearMeshOptimizationCandidatesResponse], error) {
	meshOptimization := s.meshOptimizationService()
	if req.Msg.CandidateId != nil && strings.TrimSpace(*req.Msg.CandidateId) != "" {
		if err := meshOptimization.DiscardCandidate(ctx, strings.TrimSpace(*req.Msg.CandidateId)); err != nil {
			return nil, err
		}
		return connect.NewResponse(&managev1.ClearMeshOptimizationCandidatesResponse{Success: true}), nil
	}

	candidates, err := meshOptimization.ListCandidates(ctx, MeshOptimizationListInput{
		SourceFileID: strings.TrimSpace(req.Msg.SourceFileId),
		EntityType:   req.Msg.EntityType,
		EntityID:     strings.TrimSpace(req.Msg.EntityId),
		Profile:      req.Msg.Profile,
	})
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if err := meshOptimization.DiscardCandidate(ctx, candidate.ID); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&managev1.ClearMeshOptimizationCandidatesResponse{Success: true}), nil
}

func (s *FileService) meshOptimizationCandidateResponse(
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
