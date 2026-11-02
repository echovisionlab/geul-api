package filemedia

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *Service) meshOptimizationService() *MeshOptimizationService {
	return NewMeshOptimizationService(MeshOptimizationDeps{Units: s.units, FileDeleter: s, Authorizer: s.spiceDB})
}

// ListMeshOptimizationCandidatesQuery lists the optimized candidates of a
// 3D model File attached to an entity.
type ListMeshOptimizationCandidatesQuery struct {
	SourceFileID string
	EntityType   EntityType
	EntityID     string
	Profile      MeshProfile
}

func (q ListMeshOptimizationCandidatesQuery) input() MeshOptimizationListInput {
	return MeshOptimizationListInput{
		SourceFileID: strings.TrimSpace(q.SourceFileID),
		EntityType:   q.EntityType,
		EntityID:     strings.TrimSpace(q.EntityID),
		Profile:      q.Profile,
	}
}

func (s *Service) ListMeshOptimizationCandidates(ctx context.Context, query ListMeshOptimizationCandidatesQuery) ([]*MeshCandidateView, error) {
	candidates, err := s.meshOptimizationService().ListCandidates(ctx, query.input())
	if err != nil {
		return nil, err
	}
	projected := make([]*MeshCandidateView, 0, len(candidates))
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
	EntityType         EntityType
	EntityID           string
	TargetRatioPercent int32
	Method             MeshCompressionMethod
	Profile            MeshProfile
}

func (s *Service) GenerateMeshOptimizationCandidate(ctx context.Context, command GenerateMeshOptimizationCandidateCommand) (*MeshCandidateGeneration, error) {
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
	return &MeshCandidateGeneration{
		Candidate: projected,
		CacheHit:  result.CacheHit,
		Enqueued:  result.Enqueued,
	}, nil
}

// UseMeshOptimizationCandidateCommand selects an optimized candidate.
type UseMeshOptimizationCandidateCommand struct {
	CandidateID string
}

func (s *Service) UseMeshOptimizationCandidate(ctx context.Context, command UseMeshOptimizationCandidateCommand) (*MeshCandidateView, error) {
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

func (s *Service) meshOptimizationCandidateResponse(candidate domain.MeshCandidate) (*MeshCandidateView, error) {
	result := &MeshCandidateView{Record: candidate}
	if meshOptimizationCandidateHasUsableOutput(candidate) {
		outputFileID := strings.TrimSpace(*candidate.OutputFileID)
		fileName := outputFileID + ".glb"
		delivery, err := s.fileURLsResponseFromStoredFile(outputFileID, "glb", "model/gltf-binary", int64Value(candidate.OptimizedFileSize), &fileName)
		if err != nil {
			return nil, err
		}
		delivery.Delivery.FileName = &fileName
		result.Delivery = delivery.Delivery
	}
	return result, nil
}

func meshOptimizationCandidateHasUsableOutput(candidate domain.MeshCandidate) bool {
	return candidate.Status == domain.MeshStatusReady &&
		candidate.OutputFileID != nil &&
		strings.TrimSpace(*candidate.OutputFileID) != ""
}

func meshOptimizationRequestMethod(
	method MeshCompressionMethod,
) string {
	switch method {
	case MeshCompressionUnspecified,
		MeshCompressionDraco:
		return domain.MeshMethodDraco
	default:
		return ""
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
